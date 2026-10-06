package apipublica_test

// messages_senderror_test.go — la mitad de messages_test.go que cubre la traducción de un error
// de SendText a HTTP (el writeSendError de messages.go), a través de D1. Partido por tema (E-13).
//
// Los textos van aquí ESCRITOS OTRA VEZ, byte a byte: son observables (los lee un operador) y
// el orden de los casos decide si se le dice «reintenta» a quien puede duplicar un WhatsApp.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

const (
	msgOffline      = "sesión offline: no hay stream vivo para el Edge"
	msgAckTimeout   = "timeout esperando el ack del Edge"
	msgSendFailed   = "no se pudo enviar el texto"
	msgStreamClosed = "el stream del Edge se cerró antes del ack: el comando YA viajó, " +
		"así que no se sabe si el mensaje salió. Verifica el envío (por el command_id, contra el " +
		"outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas puede duplicárselo al cliente"
	msgEdgeNotReading = "el Edge dejó de leer su stream y no aceptó el comando dentro del plazo: " +
		"puede salir aún, así que no se sabe si el mensaje llegará. Verifica el envío (por el " +
		"command_id, contra el outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas " +
		"puede duplicárselo al cliente"
	msgBudgetExhausted = "se agotó el plazo de la petición mientras se empujaba el comando " +
		"al Edge: puede salir aún, así que no se sabe si el mensaje llegará. Verifica el envío (por " +
		"el command_id, contra el outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas " +
		"puede duplicárselo al cliente"

	msgSendErrorLog        = "envío por la API pública fallido"
	msgSendErrorNotWritten = "no se pudo escribir la respuesta de error del envío"
)

// fakeSendError es el doble de grpc.SendError: cumple los DOS contratos por duck-typing que D1
// consume. closed es configurable a propósito: un doble que solo supiera decir «sí» no probaría
// que se mira el BOOL y no la mera presencia del método.
type fakeSendError struct {
	cmdID  string
	cause  error
	closed bool
}

func (e *fakeSendError) Error() string     { return fmt.Sprintf("comando %s: %v", e.cmdID, e.cause) }
func (e *fakeSendError) Unwrap() error     { return e.cause }
func (e *fakeSendError) CommandID() string { return e.cmdID }
func (e *fakeSendError) StreamCaido() bool { return e.closed }

// withID envuelve cause como lo hace el gateway: con su command_id y sin stream caído.
func withID(cause error) error { return &fakeSendError{cmdID: "cmd-42", cause: cause} }

// sendFailing pide D1 con un Sender que falla con err.
func sendFailing(h *apipublicahelpertest.Harness, err error) *httptest.ResponseRecorder {
	return send(h, apipublica.MessagesDeps{Sender: &senderFake{err: err}, Sessions: sessA()}, sendBody)
}

func TestMountMessages_SendError(t *testing.T) {
	abandoned := fmt.Errorf("%w: %q: %w", session.ErrPushAbandoned, "sess-a", context.DeadlineExceeded)
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"offline_is_502", withID(fmt.Errorf("%w: %q", session.ErrSessionOffline, "sess-a")), http.StatusBadGateway, msgOffline},
		{"stream_closed_is_504", &fakeSendError{cmdID: "cmd-42", cause: errors.New("stream cerrado"), closed: true},
			http.StatusGatewayTimeout, msgStreamClosed},
		{"push_timeout_is_504", withID(fmt.Errorf("%w: %q", session.ErrPushTimeout, "sess-a")), http.StatusGatewayTimeout, msgEdgeNotReading},
		{"push_abandoned_is_504", withID(abandoned), http.StatusGatewayTimeout, msgBudgetExhausted},
		{"deadline_is_504", withID(context.DeadlineExceeded), http.StatusGatewayTimeout, msgAckTimeout},
		{"canceled_is_504", withID(context.Canceled), http.StatusGatewayTimeout, msgAckTimeout},
		{"anything_else_is_500", withID(errors.New("otra cosa")), http.StatusInternalServerError, msgSendFailed},

		// El ORDEN: cada uno de estos casaría también con un caso posterior.
		{"offline_wins_over_stream_closed", &fakeSendError{cmdID: "cmd-42", cause: session.ErrSessionOffline, closed: true},
			http.StatusBadGateway, msgOffline},
		{"stream_closed_wins_over_deadline", &fakeSendError{cmdID: "cmd-42", cause: context.DeadlineExceeded, closed: true},
			http.StatusGatewayTimeout, msgStreamClosed},
		{"stream_not_closed_falls_to_deadline", &fakeSendError{cmdID: "cmd-42", cause: context.DeadlineExceeded, closed: false},
			http.StatusGatewayTimeout, msgAckTimeout},
		{"push_abandoned_wins_over_its_context_error",
			withID(fmt.Errorf("%w: %w", context.Canceled, session.ErrPushAbandoned)), http.StatusGatewayTimeout, msgBudgetExhausted},
		{"push_timeout_wins_over_deadline",
			withID(fmt.Errorf("%w: %w", context.DeadlineExceeded, session.ErrPushTimeout)), http.StatusGatewayTimeout, msgEdgeNotReading},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			rec := sendFailing(h, tc.err)
			wantCode(t, "D1", rec, tc.code)
			var body map[string]string
			wantJSON(t, "D1", rec, &body)
			if body["error"] != tc.msg || body["command_id"] != "cmd-42" || len(body) != 2 {
				t.Errorf("cuerpo %v;\nquiero {error: %q, command_id: cmd-42}", body, tc.msg)
			}

			lines := logLines(h, msgSendErrorLog)
			if len(lines) != 1 || lines[0].Level != "error" {
				t.Fatalf("línea %q: %+v; quiero una, de nivel error", msgSendErrorLog, lines)
			}
			f := lines[0].Fields
			logged, ok := f["error"].(error)
			if !ok || !errors.Is(logged, tc.err) || f["status"] != tc.code || f["command_id"] != "cmd-42" || f["session_id"] != "sess-a" {
				t.Errorf("campos = %v; quiero status %d, command_id cmd-42, session_id sess-a y el error", f, tc.code)
			}
			wantNoPII(t, h)
			wantOneAudit(t, h, "failure", tc.code)
		})
	}
}

// TestMountMessages_SendErrorTextsDoNotLie: lo que los 504 del envío pueden y no pueden decir
// (MD-054.5). El comando pudo salir: ni «no se pudo enviar» ni una invitación a reintentar; sí
// «ANTES de reenviar». Y los tres se distinguen entre sí y del timeout del ack.
func TestMountMessages_SendErrorTextsDoNotLie(t *testing.T) {
	texts := []string{msgStreamClosed, msgEdgeNotReading, msgBudgetExhausted}
	for _, text := range texts {
		if strings.Contains(text, "no se pudo enviar") || strings.Contains(text, "reintenta") {
			t.Errorf("el texto afirma que no salió o invita a reintentar: %q", text)
		}
		if !strings.Contains(text, "ANTES de reenviar") {
			t.Errorf("el texto no dice qué hacer (verificar antes de reenviar): %q", text)
		}
	}
	seen := map[string]bool{msgAckTimeout: true}
	for _, text := range texts {
		if seen[text] {
			t.Errorf("dos 504 comparten cuerpo: %q", text)
		}
		seen[text] = true
	}
}

// TestMountMessages_SendErrorWithoutCommandID: un fallo anterior a la asignación del command_id
// no se inventa uno vacío: el campo se omite.
func TestMountMessages_SendErrorWithoutCommandID(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := sendFailing(h, context.DeadlineExceeded)
	wantCode(t, "D1 sin command_id", rec, http.StatusGatewayTimeout)
	if got, want := rec.Body.String(), `{"error":"`+msgAckTimeout+`"}`; got != want {
		t.Errorf("cuerpo %s, quiero %s (sin command_id)", got, want)
	}
	if lines := logLines(h, msgSendErrorLog); len(lines) != 1 || lines[0].Fields["command_id"] != "" {
		t.Errorf("línea de fallo = %+v; quiero una con command_id vacío", lines)
	}
}

// TestMountMessages_UndeliveredSendErrorIsLogged: con el Write roto, el log no puede afirmar un
// código que el cliente nunca recibió sin decirlo.
func TestMountMessages_UndeliveredSendErrorIsLogged(t *testing.T) {
	h := apipublicahelpertest.New(t)
	d := apipublica.MessagesDeps{Sender: &senderFake{err: withID(session.ErrSessionOffline)}, Sessions: sessA()}
	req := httptest.NewRequest(http.MethodPost, messagesTarget, strings.NewReader(sendBody))
	req.Header.Set("Authorization", "Bearer "+h.With(tenantA, "messages.send"))
	messagesCara(h.Common(), d).ServeHTTP(&failingWriter{header: http.Header{}}, req)

	lines := logLines(h, msgSendErrorNotWritten)
	if len(lines) != 1 || lines[0].Level != "error" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel error", msgSendErrorNotWritten, lines)
	}
	f := lines[0].Fields
	if err, ok := f["error"].(error); !ok || !errors.Is(err, errWriteFailed) ||
		f["status"] != http.StatusBadGateway || f["command_id"] != "cmd-42" || f["session_id"] != "sess-a" {
		t.Errorf("campos = %v; quiero status 502, command_id cmd-42, session_id sess-a y el error del Write", f)
	}
	if n := len(logLines(h, msgSendErrorLog)); n != 1 {
		t.Errorf("la línea del fallo de envío salió %d veces, quiero 1 además de la de escritura", n)
	}
}
