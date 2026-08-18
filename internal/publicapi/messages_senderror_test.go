package publicapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// sendWithIDError es el doble del *gatewaygrpc.SendError: un error de envío que
// lleva su command_id. Lo que se prueba aquí es el CONTRATO por duck-typing
// (`interface{ CommandID() string }`) que el handler consume; que el Gateway real
// lo cumple se prueba en internal/gateway/grpc/send_error_test.go. Los dos tests
// se sostienen mutuamente: sin el de allá, este pasaría contra un contrato que
// nadie implementa.
type sendWithIDError struct {
	cmdID string
	causa error
}

func (e *sendWithIDError) Error() string     { return fmt.Sprintf("comando %s: %v", e.cmdID, e.causa) }
func (e *sendWithIDError) Unwrap() error     { return e.causa }
func (e *sendWithIDError) CommandID() string { return e.cmdID }

// depsConSender arma unas Deps con la sesión sess-a viva en tenantA.
func depsConSender(s *fakeSender) publicapi.Deps {
	return publicapi.Deps{
		Sender: s,
		SessionDeps: publicapi.SessionDeps{
			Sessions: fakeSessions{byTenant: map[string][]fleet.Session{
				tenantA: {{TenantID: tenantA, SessionID: "sess-a"}},
			}},
		},
	}
}

// leerSendError decodifica el cuerpo del error de envío.
func leerSendError(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var resp struct {
		Error     string `json:"error"`
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decodificando el error: %v; body=%s", err, body)
	}
	return resp.Error, resp.CommandID
}

// TestMessages_504_TimeoutDelAck_LlevaCommandID es la contrapartida HTTP del
// cuelgue del 2026-08-06: entonces el endpoint no devolvía 504 —se quedaba
// esperando y el servidor cerraba la conexión sin cuerpo—, así que este caso no
// tenía cobertura ninguna.
//
// El command_id importa especialmente en el 504 y no es un adorno: a diferencia
// del 502, aquí el comando YA viajó al Edge, de modo que el mensaje pudo haberse
// enviado. El 504 dice «no sé si llegó», y el command_id es el único hilo para
// averiguarlo después contra el outbox del Edge o los acuses del Plan 013.
func TestMessages_504_TimeoutDelAck_LlevaCommandID(t *testing.T) {
	sender := &fakeSender{err: &sendWithIDError{cmdID: "cmd-42", causa: context.DeadlineExceeded}}
	mux := newAPI(depsConSender(sender), apiKeys())

	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	msg, cmdID := leerSendError(t, rec.Body.Bytes())
	if cmdID != "cmd-42" {
		t.Fatalf("command_id=%q, quiero cmd-42: un 504 sin él no es diagnosticable", cmdID)
	}
	if msg == "" {
		t.Fatal("el 504 llegó sin mensaje de error")
	}
}

// TestMessages_502_Offline_LlevaCommandID: el 502 también lo lleva. Aquí el
// comando NO salió (no hay stream vivo), y poder distinguir ese caso del 504 es
// justo lo que se busca cuando alguien pregunta «¿le llegó?».
func TestMessages_502_Offline_LlevaCommandID(t *testing.T) {
	sender := &fakeSender{err: &sendWithIDError{cmdID: "cmd-7", causa: session.ErrSessionOffline}}
	mux := newAPI(depsConSender(sender), apiKeys())

	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d, quiero 502; body=%s", rec.Code, rec.Body.String())
	}
	if _, cmdID := leerSendError(t, rec.Body.Bytes()); cmdID != "cmd-7" {
		t.Fatalf("command_id=%q, quiero cmd-7", cmdID)
	}
}

// TestMessages_ErrorSinCommandID_OmiteElCampo: un fallo anterior a la asignación
// del command_id no debe inventarse uno vacío en el cuerpo (`omitempty`), para que
// "no hay command_id" y "el command_id es la cadena vacía" no se confundan.
func TestMessages_ErrorSinCommandID_OmiteElCampo(t *testing.T) {
	sender := &fakeSender{err: context.DeadlineExceeded}
	mux := newAPI(depsConSender(sender), apiKeys())

	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504; body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	if _, presente := raw["command_id"]; presente {
		t.Fatalf("command_id no debería aparecer cuando no lo hay; body=%s", rec.Body.String())
	}
}

// sendStreamCaidoError es el doble del *gatewaygrpc.SendError cuando el stream de
// la sesión murió esperando el ack: cumple los DOS contratos por duck-typing que el
// handler consume, `interface{ CommandID() string }` e `interface{ StreamCaido() bool }`.
// El campo caido es configurable a propósito: un doble que solo supiera decir "sí"
// no probaría que el handler mira el BOOL y no la mera presencia del método.
type sendStreamCaidoError struct {
	cmdID string
	causa error
	caido bool
}

func (e *sendStreamCaidoError) Error() string     { return fmt.Sprintf("comando %s: %v", e.cmdID, e.causa) }
func (e *sendStreamCaidoError) Unwrap() error     { return e.causa }
func (e *sendStreamCaidoError) CommandID() string { return e.cmdID }
func (e *sendStreamCaidoError) StreamCaido() bool { return e.caido }

// enviarYLeerError hace el POST de envío y devuelve (código, error, command_id).
func enviarYLeerError(t *testing.T, sender *fakeSender) (int, string, string) {
	t.Helper()
	mux := newAPI(depsConSender(sender), apiKeys())
	rec := call(mux, keyAFull, http.MethodPost, "/api/v1/messages",
		`{"session_id":"sess-a","to":"+15551234567","text":"hola"}`)
	msg, cmdID := leerSendError(t, rec.Body.Bytes())
	return rec.Code, msg, cmdID
}

// TestMessages_504_StreamCaido_TextoDistintoDelTimeout es el test EN PAREJA del
// mapeo nuevo (Plan 050 · Ola 2 · T2.4): ejercita los dos 504 en la misma prueba
// porque cualquiera de los dos por separado pasaría contra un handler que devolviera
// SIEMPRE el mismo cuerpo. Lo que se afirma no es "hay un texto", es que los dos
// sucesos —«se cayó» y «no contestó a tiempo»— se distinguen mirando la respuesta.
//
// Ambos son 504 y eso NO es un descuido: en los dos el comando ya viajó al Edge (si
// el Push hubiera fallado, el error sería ErrSessionOffline y el código un 502), así
// que en los dos la respuesta honesta es «no sé si llegó». El 502 diría «no llegó».
func TestMessages_504_StreamCaido_TextoDistintoDelTimeout(t *testing.T) {
	codigoCaido, msgCaido, cmdCaido := enviarYLeerError(t,
		&fakeSender{err: &sendStreamCaidoError{cmdID: "cmd-caido", caido: true}})
	if codigoCaido != http.StatusGatewayTimeout {
		t.Fatalf("stream caído: code=%d, quiero 504 (el comando YA viajó; un 502 diría «no salió»)", codigoCaido)
	}
	if cmdCaido != "cmd-caido" {
		t.Fatalf("stream caído: command_id=%q, quiero cmd-caido", cmdCaido)
	}

	codigoTimeout, msgTimeout, _ := enviarYLeerError(t,
		&fakeSender{err: &sendWithIDError{cmdID: "cmd-lento", causa: context.DeadlineExceeded}})
	if codigoTimeout != http.StatusGatewayTimeout {
		t.Fatalf("timeout: code=%d, quiero 504", codigoTimeout)
	}
	if msgTimeout != "timeout esperando el ack del Edge" {
		t.Fatalf("timeout: el cuerpo de siempre cambió: %q", msgTimeout)
	}

	if msgCaido == msgTimeout {
		t.Fatalf("los dos 504 devuelven el MISMO cuerpo (%q): el mapeo nuevo no distingue nada", msgCaido)
	}
	if !strings.Contains(msgCaido, "el stream del Edge se cerró") {
		t.Fatalf("el 504 del stream caído no se reconoce a simple vista: %q", msgCaido)
	}
	// MD-054.5: accionable de verdad. El texto no puede afirmar que el mensaje no
	// salió (sería falso) ni invitar a reintentar a ciegas (duplicaría el WhatsApp).
	if !strings.Contains(msgCaido, "ANTES de reenviar") {
		t.Fatalf("el texto no dice qué hacer (verificar antes de reenviar): %q", msgCaido)
	}
	if strings.Contains(msgCaido, "no se pudo enviar") {
		t.Fatalf("el texto afirma algo falso: el comando viajó y el mensaje pudo salir: %q", msgCaido)
	}
}

// TestMessages_504_StreamNoCaido_UsaElTextoViejo cierra el hueco que deja el test de
// arriba: el handler mira el VALOR de StreamCaido(), no si el error implementa el
// método. Sin este caso, un `errors.As(err, &caido)` sin comprobar el bool pasaría.
func TestMessages_504_StreamNoCaido_UsaElTextoViejo(t *testing.T) {
	code, msg, cmdID := enviarYLeerError(t,
		&fakeSender{err: &sendStreamCaidoError{cmdID: "cmd-9", causa: context.DeadlineExceeded, caido: false}})

	if code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504", code)
	}
	if msg != "timeout esperando el ack del Edge" {
		t.Fatalf("StreamCaido()=false debe dar el cuerpo de siempre, no el nuevo: %q", msg)
	}
	if cmdID != "cmd-9" {
		t.Fatalf("command_id=%q, quiero cmd-9", cmdID)
	}
}

// TestMessages_StreamCaido_NoPisaEl502DeOffline: el caso nuevo comparte código con el
// timeout, pero NO debe tragarse el 502. Un error offline que además dijera que el
// stream cayó sigue siendo un 502 —ahí el comando no llegó a salir—, y esa es la
// distinción que el resto del contrato de envío se apoya en no perder.
func TestMessages_StreamCaido_NoPisaEl502DeOffline(t *testing.T) {
	code, msg, _ := enviarYLeerError(t,
		&fakeSender{err: &sendStreamCaidoError{cmdID: "cmd-off", causa: session.ErrSessionOffline, caido: true}})

	if code != http.StatusBadGateway {
		t.Fatalf("code=%d, quiero 502: con la sesión offline el comando NO salió", code)
	}
	if msg != "sesión offline: no hay stream vivo para el Edge" {
		t.Fatalf("cuerpo del 502 cambiado: %q", msg)
	}
}
