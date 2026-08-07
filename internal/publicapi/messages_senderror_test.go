package publicapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
