package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// fakeRevoker registra la última llamada a RevokeLease y permite forzar un error.
type fakeRevoker struct {
	gotTenant string
	gotEdge   string
	calls     int
	err       error
}

func (f *fakeRevoker) RevokeLease(_ context.Context, tenantID, edgeID string) error {
	f.calls++
	f.gotTenant = tenantID
	f.gotEdge = edgeID
	return f.err
}

// asOperator devuelve una copia del request con una Identity de operador (tenant
// del token) inyectada, como haría Authenticate en producción (Plan 018 · T4).
func asOperator(req *http.Request, tenant string) *http.Request {
	return req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
}

func TestRevokeLeaseHandler_OK(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	// El tenant sale del TOKEN (INV-8); el cuerpo solo lleva edge_id.
	body := `{"edge_id":"edge-1"}`
	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(body)), "t-1")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rev.calls != 1 {
		t.Fatalf("RevokeLease llamado %d veces, want 1", rev.calls)
	}
	if rev.gotTenant != "t-1" || rev.gotEdge != "edge-1" {
		t.Fatalf("argumentos: got (%q,%q), want (t-1,edge-1)", rev.gotTenant, rev.gotEdge)
	}
}

func TestRevokeLeaseHandler_MissingFields(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	// Identidad presente pero sin edge_id en el cuerpo → 400.
	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(`{}`)), "t-1")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeLease NO debería invocarse con campos faltantes (calls=%d)", rev.calls)
	}
}

// TestRevokeLeaseHandler_NoIdentity: sin Identity en el contexto (request que no
// pasó por Authenticate) el handler responde 401 y NO revoca (INV-8).
func TestRevokeLeaseHandler_NoIdentity(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	req := httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(`{"edge_id":"edge-1"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeLease NO debería invocarse sin identidad (calls=%d)", rev.calls)
	}
}

func TestRevokeLeaseHandler_WrongMethod(t *testing.T) {
	rev := &fakeRevoker{}
	h := httpapi.RevokeLeaseHandler(rev)

	req := httptest.NewRequest(http.MethodGet, "/admin/leases/revoke", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeLease NO debería invocarse con método incorrecto (calls=%d)", rev.calls)
	}
}

func TestRevokeLeaseHandler_RevokerError(t *testing.T) {
	rev := &fakeRevoker{err: errors.New("boom")}
	h := httpapi.RevokeLeaseHandler(rev)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/leases/revoke", strings.NewReader(`{"edge_id":"edge-1"}`)), "t-1")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// fakeSender registra la última llamada a SendText y permite devolver un Ack o
// forzar un error.
type fakeSender struct {
	gotSession string
	gotTo      string
	gotText    string
	calls      int
	ack        *cloudlinkv1.Ack
	err        error
}

func (f *fakeSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	f.calls++
	f.gotSession = sessionID
	f.gotTo = to
	f.gotText = text
	return f.ack, f.err
}

func TestSendMessageHandler_OK(t *testing.T) {
	snd := &fakeSender{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-1", Ok: true}}
	h := httpapi.SendMessageHandler(snd)

	body := `{"session_id":"s-1","to":"5491100000000","text":"hola"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if snd.calls != 1 || snd.gotSession != "s-1" || snd.gotTo != "5491100000000" || snd.gotText != "hola" {
		t.Fatalf("argumentos: got (%q,%q,%q) calls=%d", snd.gotSession, snd.gotTo, snd.gotText, snd.calls)
	}

	var resp struct {
		AckedCommandID string `json:"acked_command_id"`
		OK             bool   `json:"ok"`
		Error          string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decodificando respuesta: %v", err)
	}
	if resp.AckedCommandID != "cmd-1" || !resp.OK {
		t.Fatalf("respuesta: got %+v, want acked_command_id=cmd-1 ok=true", resp)
	}
}

// TestSendMessageHandler_AckNotOK documenta la decisión: un Ack con ok=false (el
// Edge recibió el comando pero su ejecución falló) sigue siendo 200; el fallo se
// refleja en el cuerpo JSON (ok=false + error), no en el código HTTP.
func TestSendMessageHandler_AckNotOK(t *testing.T) {
	snd := &fakeSender{ack: &cloudlinkv1.Ack{AckedCommandId: "cmd-2", Ok: false, Error: "envío rechazado"}}
	h := httpapi.SendMessageHandler(snd)

	body := `{"session_id":"s-1","to":"549110","text":"hola"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decodificando respuesta: %v", err)
	}
	if resp.OK || resp.Error != "envío rechazado" {
		t.Fatalf("respuesta: got %+v, want ok=false error=envío rechazado", resp)
	}
}

func TestSendMessageHandler_Offline(t *testing.T) {
	snd := &fakeSender{err: fmt.Errorf("push: %w", session.ErrSessionOffline)}
	h := httpapi.SendMessageHandler(snd)

	body := `{"session_id":"s-x","to":"549110","text":"hola"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestSendMessageHandler_Timeout(t *testing.T) {
	snd := &fakeSender{err: fmt.Errorf("esperando ack: %w", context.DeadlineExceeded)}
	h := httpapi.SendMessageHandler(snd)

	body := `{"session_id":"s-1","to":"549110","text":"hola"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
}

func TestSendMessageHandler_SenderError(t *testing.T) {
	snd := &fakeSender{err: errors.New("boom")}
	h := httpapi.SendMessageHandler(snd)

	body := `{"session_id":"s-1","to":"549110","text":"hola"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSendMessageHandler_MissingFields(t *testing.T) {
	snd := &fakeSender{}
	h := httpapi.SendMessageHandler(snd)

	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(`{"session_id":"s-1","to":"549110"}`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if snd.calls != 0 {
		t.Fatalf("SendText NO debería invocarse con campos faltantes (calls=%d)", snd.calls)
	}
}

func TestSendMessageHandler_InvalidBody(t *testing.T) {
	snd := &fakeSender{}
	h := httpapi.SendMessageHandler(snd)

	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send", strings.NewReader(`{not-json`))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if snd.calls != 0 {
		t.Fatalf("SendText NO debería invocarse con body inválido (calls=%d)", snd.calls)
	}
}

func TestSendMessageHandler_WrongMethod(t *testing.T) {
	snd := &fakeSender{}
	h := httpapi.SendMessageHandler(snd)

	req := httptest.NewRequest(http.MethodGet, "/admin/messages/send", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if snd.calls != 0 {
		t.Fatalf("SendText NO debería invocarse con método incorrecto (calls=%d)", snd.calls)
	}
}
