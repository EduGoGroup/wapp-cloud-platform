//go:build pendiente

package apipublica_test

// messages_test.go — cubre el contrato de messages.go (MessageSender, SessionLister,
// MessagesDeps, MountMessages): D1, salvo la traducción de los errores de SendText, que va en
// messages_senderror_test.go (E-13). Aquí viven los dobles que comparten los dos ficheros.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

const (
	tenantB = apipublicahelpertest.TenantB

	messagesTarget = "/api/v1/messages"
	sendTo         = "+15551234567"
	sendText       = "hola-mundo-secreto"
	sendBody       = `{"session_id":"sess-a","to":"` + sendTo + `","text":"` + sendText + `"}`

	msgSent           = "mensaje enviado por la API pública"
	msgSentNotWritten = "no se pudo escribir la respuesta del envío"
	msgGuardExpired   = "la verificación de la sesión no respondió a tiempo: el mensaje NO se envió, reintenta"
	msgGuardLog       = "lectura a BD vencida: se responde 504"
)

// Los puertos de D1 los cumplen las piezas REALES del módulo edge nuevo: sin estas líneas, los
// dobles de abajo probarían un contrato que nadie implementa.
var (
	_ apipublica.MessageSender = (*edgegrpc.Server)(nil)
	_ apipublica.SessionLister = fleet.Repository(nil)
	// El duck-typing que consume la traducción de errores lo cumple el error real del gateway.
	_ interface {
		CommandID() string
		StreamCaido() bool
	} = (*edgegrpc.SendError)(nil)
)

// senderFake es MessageSender: devuelve ack o err y apunta lo que recibió, incluido cuánto le
// quedaba al contexto (remaining) y si traía plazo (bounded).
type senderFake struct {
	mu                sync.Mutex
	ack               *cloudlinkv1.Ack
	err               error
	calls             int
	session, to, text string
	bounded           bool
	remaining         time.Duration
}

var _ apipublica.MessageSender = (*senderFake)(nil)

func (f *senderFake) SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.session, f.to, f.text = sessionID, to, text
	if dl, ok := ctx.Deadline(); ok {
		f.bounded, f.remaining = true, time.Until(dl)
	}
	return f.ack, f.err
}

func okSender() *senderFake {
	return &senderFake{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}}
}

// listerFake es SessionLister: devuelve las sesiones del tenant pedido o err; con block espera a
// que el contexto muera y devuelve su error (una base que no contesta, sin time.Sleep).
type listerFake struct {
	mu        sync.Mutex
	byTenant  map[string][]fleet.Session
	err       error
	block     bool
	calls     int
	tenant    string
	bounded   bool
	remaining time.Duration
}

var _ apipublica.SessionLister = (*listerFake)(nil)

func (f *listerFake) List(ctx context.Context, tenantID string) ([]fleet.Session, error) {
	f.mu.Lock()
	f.calls++
	f.tenant = tenantID
	if dl, ok := ctx.Deadline(); ok {
		f.bounded, f.remaining = true, time.Until(dl)
	}
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.byTenant[tenantID], f.err
}

// sessA es la flota de siempre: sess-a es de tenantA y sess-b de tenantB.
func sessA() *listerFake {
	return &listerFake{byTenant: map[string][]fleet.Session{
		tenantA: {{TenantID: tenantA, SessionID: "sess-otra"}, {TenantID: tenantA, SessionID: "sess-a"}},
		tenantB: {{TenantID: tenantB, SessionID: "sess-b"}},
	}}
}

// messagesCara monta D1 con k y d.
func messagesCara(k apipublica.Common, d apipublica.MessagesDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountMessages(c, k, d)
	return c
}

// send pide D1 con el permiso y el cuerpo dado.
func send(h *apipublicahelpertest.Harness, d apipublica.MessagesDeps, body string) *httptest.ResponseRecorder {
	return h.Call(messagesCara(h.Common(), d), h.With(tenantA, "messages.send"), http.MethodPost, messagesTarget, body)
}

// logLines devuelve las líneas del arnés con ese mensaje.
func logLines(h *apipublicahelpertest.Harness, msg string) []apipublicahelpertest.LogEntry {
	var out []apipublicahelpertest.LogEntry
	for _, e := range h.Log().Entries() {
		if e.Msg == msg {
			out = append(out, e)
		}
	}
	return out
}

// wantNoPII exige que ni el destino ni el texto del mensaje hayan llegado a NINGUNA línea.
func wantNoPII(t *testing.T, h *apipublicahelpertest.Harness) {
	t.Helper()
	for _, e := range h.Log().Entries() {
		if line := e.Msg + fmt.Sprint(e.Fields); strings.Contains(line, sendTo) || strings.Contains(line, sendText) {
			t.Errorf("PII (destino o texto) en el log: %s %q %v", e.Level, e.Msg, e.Fields)
		}
	}
}

// wantOneAudit exige EXACTAMENTE un registro de auditoría de D1 con ese resultado y código.
func wantOneAudit(t *testing.T, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("D1 dejó %d registros de auditoría, quiero exactamente 1", len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != "messages.send" || r.Resource != "message" || r.Result != result || r.Meta["status"] != status {
		t.Errorf("registro %+v, quiero tenant %s, action messages.send, resource message, result %s, status %d",
			r, tenantA, result, status)
	}
}

func TestMountMessages_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	sender := okSender()
	cara := messagesCara(h.Common(), apipublica.MessagesDeps{Sender: sender, Sessions: sessA()})
	wantPatterns(t, "D1", cara, []string{"POST /api/v1/messages"})
	checkChain(t, h, cara, routeCase{
		id: "D1", method: http.MethodPost, target: messagesTarget, body: sendBody,
		perm: "messages.send", resource: "message", want: http.StatusOK,
	})
	if sender.calls != 1 {
		t.Errorf("SendText se llamó %d veces; quiero 1: ni el 401 ni los 403 envían", sender.calls)
	}
}

// TestMountMessages_AlwaysMounts: D1 no tiene condición de montaje.
func TestMountMessages_AlwaysMounts(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := messagesCara(h.Common(), apipublica.MessagesDeps{})
	wantPatterns(t, "D1 sin dependencias", cara, []string{"POST /api/v1/messages"})
	wantCode(t, "D1 sin dependencias y sin token", h.Call(cara, "", http.MethodPost, messagesTarget, sendBody), http.StatusUnauthorized)
}

func TestMountMessages_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountMessages(apipublica.Nueva(), apipublica.Common{}, apipublica.MessagesDeps{Sender: okSender(), Sessions: sessA()})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountMessages") {
		t.Errorf("MountMessages con MW nil: panic = %v; quiero un panic de cableado que nombre MountMessages", v)
	}
}

func TestMountMessages_OK(t *testing.T) {
	h := apipublicahelpertest.New(t)
	sender, lister := okSender(), sessA()
	rec := send(h, apipublica.MessagesDeps{Sender: sender, Sessions: lister}, sendBody)

	wantCode(t, "D1", rec, http.StatusOK)
	if got, want := rec.Body.String(), `{"acked_command_id":"cmd-1","ok":true}`; got != want {
		t.Errorf("D1: cuerpo %s, quiero %s", got, want)
	}
	if sender.session != "sess-a" || sender.to != sendTo || sender.text != sendText {
		t.Errorf("SendText(%q, %q, %q); quiero (sess-a, %q, %q)", sender.session, sender.to, sender.text, sendTo, sendText)
	}
	if lister.calls != 1 || lister.tenant != tenantA {
		t.Errorf("List(%q) en %d llamadas; quiero el tenant del token %q en 1", lister.tenant, lister.calls, tenantA)
	}
	lines := logLines(h, msgSent)
	if len(lines) != 1 || lines[0].Level != "info" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel info", msgSent, lines)
	}
	if f := lines[0].Fields; f["command_id"] != "cmd-1" || f["session_id"] != "sess-a" || f["ok"] != true {
		t.Errorf("campos = %v; quiero command_id cmd-1, session_id sess-a, ok true", f)
	}
	wantNoPII(t, h)
	wantOneAudit(t, h, "success", http.StatusOK)
}

// TestMountMessages_AckNotOKIs200: el Edge recibió el comando y su ejecución falló.
func TestMountMessages_AckNotOKIs200(t *testing.T) {
	h := apipublicahelpertest.New(t)
	sender := &senderFake{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-2", Ok: false, Error: "destino inválido"}}
	rec := send(h, apipublica.MessagesDeps{Sender: sender, Sessions: sessA()}, sendBody)
	wantCode(t, "D1 con ack ok=false", rec, http.StatusOK)
	if got, want := rec.Body.String(), `{"acked_command_id":"cmd-2","ok":false,"error":"destino inválido"}`; got != want {
		t.Errorf("D1 con ack ok=false: cuerpo %s, quiero %s", got, want)
	}
}

// TestMountMessages_WithTheModuleFake: con el doble del módulo edge (el que pasa la suite del
// puerto fleet), una sesión que se conectó alguna vez es del tenant y la de otro no.
func TestMountMessages_WithTheModuleFake(t *testing.T) {
	repo := fleethelpertest.NewMemoria()
	if err := repo.MarkOnline(t.Context(), tenantA, "edge-1", "sess-a"); err != nil {
		t.Fatalf("sembrando sess-a: %v", err)
	}
	if err := repo.MarkOffline(t.Context(), tenantB, "edge-2", "sess-b"); err != nil {
		t.Fatalf("sembrando sess-b: %v", err)
	}
	h := apipublicahelpertest.New(t)
	d := apipublica.MessagesDeps{Sender: okSender(), Sessions: repo}
	wantCode(t, "D1 por la sesión propia", send(h, d, sendBody), http.StatusOK)
	wantCode(t, "D1 por la sesión de otro tenant", send(h, d, strings.Replace(sendBody, "sess-a", "sess-b", 1)), http.StatusNotFound)
}

func TestMountMessages_BadRequest(t *testing.T) {
	const required = "session_id, to y text son requeridos"
	cases := []struct {
		name, body, want string
	}{
		{"not_json", `{"session_id":`, "cuerpo JSON inválido"},
		{"empty_body", ``, "cuerpo JSON inválido"},
		{"wrong_type", `{"session_id":7,"to":"x","text":"y"}`, "cuerpo JSON inválido"},
		{"missing_session_id", `{"to":"x","text":"y"}`, required},
		{"missing_to", `{"session_id":"sess-a","text":"y"}`, required},
		{"missing_text", `{"session_id":"sess-a","to":"x"}`, required},
		{"empty_text", `{"session_id":"sess-a","to":"x","text":""}`, required},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			sender, lister := okSender(), sessA()
			rec := send(h, apipublica.MessagesDeps{Sender: sender, Sessions: lister}, tc.body)
			wantCode(t, "D1", rec, http.StatusBadRequest)
			wantErrorBody(t, "D1", rec, tc.want)
			if sender.calls != 0 || lister.calls != 0 {
				t.Errorf("un 400 consultó la flota (%d) o envió (%d); quiero 0 y 0", lister.calls, sender.calls)
			}
			wantOneAudit(t, h, "failure", http.StatusBadRequest)
		})
	}
}

// TestMountMessages_CrossTenantIs404: el tenant es el del token; el del cuerpo no cuenta (INV-8).
func TestMountMessages_CrossTenantIs404(t *testing.T) {
	cases := []struct{ name, body string }{
		{"session_of_another_tenant", `{"session_id":"sess-b","to":"x","text":"y"}`},
		{"tenant_in_body_is_ignored", `{"tenant_id":"` + tenantB + `","session_id":"sess-b","to":"x","text":"y"}`},
		{"unknown_session", `{"session_id":"sess-nadie","to":"x","text":"y"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			sender, lister := okSender(), sessA()
			rec := send(h, apipublica.MessagesDeps{Sender: sender, Sessions: lister}, tc.body)
			wantCode(t, "D1", rec, http.StatusNotFound)
			wantErrorBody(t, "D1", rec, "sesión no encontrada para el tenant")
			if lister.tenant != tenantA {
				t.Errorf("List recibió el tenant %q, quiero el del token %q", lister.tenant, tenantA)
			}
			if sender.calls != 0 {
				t.Error("se envió por una sesión que no es del tenant del token")
			}
			wantOneAudit(t, h, "failure", http.StatusNotFound)
		})
	}
}

func TestMountMessages_GuardErrorIs500(t *testing.T) {
	h := apipublicahelpertest.New(t)
	sender := okSender()
	rec := send(h, apipublica.MessagesDeps{Sender: sender, Sessions: &listerFake{err: errors.New("bd caída")}}, sendBody)
	wantCode(t, "D1 con la flota caída", rec, http.StatusInternalServerError)
	wantErrorBody(t, "D1 con la flota caída", rec, "no se pudo verificar la sesión")
	if sender.calls != 0 {
		t.Error("se envió sin haber verificado la sesión")
	}
	if n := len(logLines(h, msgGuardLog)); n != 0 {
		t.Errorf("un fallo que no es el plazo dejó %d líneas de plazo vencido", n)
	}
}

// TestMountMessages_GuardDeadlineIs504: una flota que no contesta se corta con DBTimeout, y eso
// es un 504 que dice que el mensaje NO salió, no un 500.
func TestMountMessages_GuardDeadlineIs504(t *testing.T) {
	h := apipublicahelpertest.New(t)
	sender := okSender()
	d := apipublica.MessagesDeps{Sender: sender, Sessions: &listerFake{block: true}, DBTimeout: 20 * time.Millisecond}
	rec := send(h, d, sendBody)
	wantCode(t, "D1 con la guarda vencida", rec, http.StatusGatewayTimeout)
	wantErrorBody(t, "D1 con la guarda vencida", rec, msgGuardExpired)
	if sender.calls != 0 {
		t.Error("se empujó al Edge pese a que la guarda de tenant nunca resolvió")
	}
	lines := logLines(h, msgGuardLog)
	if len(lines) != 1 || lines[0].Level != "warn" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel warn", msgGuardLog, lines)
	}
	if f := lines[0].Fields; f["op"] != "messages.guarda_tenant" || f["tenant_id"] != tenantA || f["session_id"] != "sess-a" {
		t.Errorf("campos = %v; quiero op messages.guarda_tenant, tenant_id y session_id", f)
	}
	wantNoPII(t, h)
	wantOneAudit(t, h, "failure", http.StatusGatewayTimeout)
}

// TestMountMessages_Clocks: qué plazo recibe cada tramo. Se mira el Deadline del contexto que
// llega a los puertos, sin esperar a que venza nada.
func TestMountMessages_Clocks(t *testing.T) {
	const slack = 500 * time.Millisecond
	cases := []struct {
		name              string
		dbTimeout, budget time.Duration
		guard             time.Duration // techo del plazo que ve List
		send              time.Duration // techo del plazo que ve SendText; 0 = sin plazo
	}{
		{"db_timeout_zero_falls_to_default", 0, 0, 1500 * time.Millisecond, 0},
		{"db_timeout_negative_falls_to_default", -time.Second, 0, 1500 * time.Millisecond, 0},
		{"db_timeout_given", 5 * time.Second, 0, 5 * time.Second, 0},
		{"budget_bounds_the_send", 5 * time.Second, time.Minute, 5 * time.Second, time.Minute},
		{"budget_shorter_than_guard_bounds_the_guard_too", 30 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second},
		{"negative_budget_is_no_budget", 5 * time.Second, -time.Second, 5 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			sender, lister := okSender(), sessA()
			rec := send(h, apipublica.MessagesDeps{Sender: sender, Sessions: lister, DBTimeout: tc.dbTimeout, SendBudget: tc.budget}, sendBody)
			wantCode(t, "D1", rec, http.StatusOK)
			if !lister.bounded || lister.remaining > tc.guard || lister.remaining < tc.guard-slack {
				t.Errorf("la guarda vio plazo=%v restante=%v; quiero un plazo de ~%v", lister.bounded, lister.remaining, tc.guard)
			}
			if tc.send == 0 {
				if sender.bounded {
					t.Errorf("sin presupuesto, SendText recibió un plazo (restante %v); quiero ninguno", sender.remaining)
				}
				return
			}
			if !sender.bounded || sender.remaining > tc.send || sender.remaining < tc.send-slack {
				t.Errorf("SendText vio plazo=%v restante=%v; quiero el presupuesto de ~%v", sender.bounded, sender.remaining, tc.send)
			}
		})
	}
}

// TestMountMessages_UndeliveredOKIsLogged: el envío ocurrió y el cliente no llegó a leerlo (el
// incidente del 2026-08-06): tiene que quedar dicho en el log.
func TestMountMessages_UndeliveredOKIsLogged(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := messagesCara(h.Common(), apipublica.MessagesDeps{Sender: okSender(), Sessions: sessA()})
	req := httptest.NewRequest(http.MethodPost, messagesTarget, strings.NewReader(sendBody))
	req.Header.Set("Authorization", "Bearer "+h.With(tenantA, "messages.send"))
	cara.ServeHTTP(&failingWriter{header: http.Header{}}, req)

	lines := logLines(h, msgSentNotWritten)
	if len(lines) != 1 || lines[0].Level != "error" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel error", msgSentNotWritten, lines)
	}
	f := lines[0].Fields
	if err, ok := f["error"].(error); !ok || !errors.Is(err, errWriteFailed) || f["command_id"] != "cmd-1" || f["session_id"] != "sess-a" {
		t.Errorf("campos = %v; quiero command_id cmd-1, session_id sess-a y el error del Write", f)
	}
}

// TestMountMessages_NilLogServesTheSame: sin logger, las mismas respuestas y ningún panic.
func TestMountMessages_NilLogServesTheSame(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	call := func(d apipublica.MessagesDeps) *httptest.ResponseRecorder {
		return h.Call(messagesCara(k, d), h.With(tenantA, "messages.send"), http.MethodPost, messagesTarget, sendBody)
	}
	wantCode(t, "D1 sin logger", call(apipublica.MessagesDeps{Sender: okSender(), Sessions: sessA()}), http.StatusOK)
	wantCode(t, "D1 sin logger, guarda vencida",
		call(apipublica.MessagesDeps{Sender: okSender(), Sessions: &listerFake{block: true}, DBTimeout: 20 * time.Millisecond}),
		http.StatusGatewayTimeout)
	wantCode(t, "D1 sin logger, envío fallido",
		call(apipublica.MessagesDeps{Sender: &senderFake{err: errors.New("x")}, Sessions: sessA()}),
		http.StatusInternalServerError)
}
