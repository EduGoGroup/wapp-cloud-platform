package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// testLogger devuelve un logger real que escribe a la nada: ejercita el camino de
// logging de los handlers (que ahora registran cada envío fallido) sin ensuciar la
// salida del test ni depender de la guarda nil.
func testLogger() sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(io.Discard))
}

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

// fakeTenantRevoker registra la última llamada a RevokeTenant y permite forzar
// un error.
type fakeTenantRevoker struct {
	gotTenant string
	calls     int
	err       error
}

func (f *fakeTenantRevoker) RevokeTenant(_ context.Context, tenantID string) error {
	f.calls++
	f.gotTenant = tenantID
	return f.err
}

// platformTenant es el tenant OPERADOR de wApp en los tests de este plano
// (ADR-0039): el mismo id fijo que siembra 0059_platform_admin.sql y que trae
// por defecto la config.
const platformTenant = "55550000-0000-0000-0000-000000000055"

// tenantBody construye el cuerpo {tenant_id} que ahora nombra a la VÍCTIMA.
func tenantBody(target string) *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`{"tenant_id":%q}`, target))
}

func TestRevokeTenantHandler_OK(t *testing.T) {
	rev := &fakeTenantRevoker{}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	// El token identifica al EJECUTOR (plataforma); el objetivo va en el cuerpo.
	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", tenantBody("t-1")), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rev.calls != 1 {
		t.Fatalf("RevokeTenant llamado %d veces, want 1", rev.calls)
	}
	if rev.gotTenant != "t-1" {
		t.Fatalf("tenant: got %q, want t-1", rev.gotTenant)
	}
}

// TestRevokeTenantHandler_PlatformCallerRevokesThirdParty es REQ-055.7 en un
// test: el caso que el handler viejo no sabía hacer. wApp corta a una empresa
// cliente que dejó de pagar, y lo que llega al revoker es el tenant de ESA
// empresa, no el de quien ejecuta. Cuando el objetivo salía del token, este
// test habría afirmado el tenant de plataforma y el moroso habría seguido
// operando.
func TestRevokeTenantHandler_PlatformCallerRevokesThirdParty(t *testing.T) {
	rev := &fakeTenantRevoker{}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	const victima = "99999999-9999-9999-9999-999999999999"
	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", tenantBody(victima)), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rev.gotTenant != victima {
		t.Fatalf("al revoker le llegó %q, want el tenant de la VÍCTIMA (%s): "+
			"si llega el del llamante, wApp se está cortando a sí misma", rev.gotTenant, victima)
	}
}

// TestRevokeTenantHandler_RejectsNonPlatformCaller es el CERROJO del handler,
// independiente del permiso RBAC: un tenant cliente, aunque su token trajera
// 'tenants.revoke.any' por un rol custom o un grant mal sembrado, no puede
// cortar a nadie. Sin este caso, la seguridad de todo el plano colgaría de una
// única fila de iam_role_grants.
func TestRevokeTenantHandler_RejectsNonPlatformCaller(t *testing.T) {
	rev := &fakeTenantRevoker{}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", tenantBody("victima")), "tenant-cliente")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusForbidden)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeTenant NO debe invocarse desde un tenant que no es el de plataforma (calls=%d)", rev.calls)
	}
}

// TestRevokeTenantHandler_MissingTenantID: el objetivo es obligatorio. Sin él no
// hay nada que revocar y NO se cae de vuelta al tenant del token (que es
// justamente lo que hacía el handler viejo).
func TestRevokeTenantHandler_MissingTenantID(t *testing.T) {
	rev := &fakeTenantRevoker{}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", strings.NewReader(`{}`)), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeTenant NO debería invocarse sin tenant_id (calls=%d)", rev.calls)
	}
}

// TestRevokeTenantHandler_NoIdentity: sin Identity en el contexto (request que
// no pasó por Authenticate) el handler responde 401 y NO revoca.
func TestRevokeTenantHandler_NoIdentity(t *testing.T) {
	rev := &fakeTenantRevoker{}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	req := httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", tenantBody("t-1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeTenant NO debería invocarse sin identidad (calls=%d)", rev.calls)
	}
}

func TestRevokeTenantHandler_WrongMethod(t *testing.T) {
	rev := &fakeTenantRevoker{}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	req := httptest.NewRequest(http.MethodGet, "/admin/tenants/revoke", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rev.calls != 0 {
		t.Fatalf("RevokeTenant NO debería invocarse con método incorrecto (calls=%d)", rev.calls)
	}
}

func TestRevokeTenantHandler_RevokerError(t *testing.T) {
	rev := &fakeTenantRevoker{err: errors.New("boom")}
	h := httpapi.RevokeTenantHandler(rev, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/revoke", tenantBody("t-1")), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// fakeTenantRestorer registra la última llamada a RestoreTenant y permite
// forzar un error.
type fakeTenantRestorer struct {
	gotTenant string
	calls     int
	err       error
}

func (f *fakeTenantRestorer) RestoreTenant(_ context.Context, tenantID string) error {
	f.calls++
	f.gotTenant = tenantID
	return f.err
}

func TestRestoreTenantHandler_OK(t *testing.T) {
	res := &fakeTenantRestorer{}
	h := httpapi.RestoreTenantHandler(res, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/restore", tenantBody("t-1")), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if res.calls != 1 {
		t.Fatalf("RestoreTenant llamado %d veces, want 1", res.calls)
	}
	if res.gotTenant != "t-1" {
		t.Fatalf("tenant: got %q, want t-1", res.gotTenant)
	}
}

// TestRestoreTenantHandler_PlatformCallerRestoresThirdParty: el reverso, mismo
// criterio. La plataforma reactiva a la empresa que volvió a pagar.
func TestRestoreTenantHandler_PlatformCallerRestoresThirdParty(t *testing.T) {
	res := &fakeTenantRestorer{}
	h := httpapi.RestoreTenantHandler(res, platformTenant)

	const victima = "99999999-9999-9999-9999-999999999999"
	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/restore", tenantBody(victima)), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if res.gotTenant != victima {
		t.Fatalf("al restorer le llegó %q, want %q", res.gotTenant, victima)
	}
}

// TestRestoreTenantHandler_RejectsNonPlatformCaller es la MITAD QUE FALTABA del
// kill-switch: si el tenant cortado pudiera llamar a /restore con su propio
// token, el corte duraría lo que tardase en pulsar el botón. Este 403 es lo que
// hace que revocar signifique algo.
func TestRestoreTenantHandler_RejectsNonPlatformCaller(t *testing.T) {
	res := &fakeTenantRestorer{}
	h := httpapi.RestoreTenantHandler(res, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/restore", tenantBody("tenant-cliente")), "tenant-cliente")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusForbidden)
	}
	if res.calls != 0 {
		t.Fatalf("un tenant revocado NO puede desrevocarse a sí mismo (calls=%d)", res.calls)
	}
}

func TestRestoreTenantHandler_MissingTenantID(t *testing.T) {
	res := &fakeTenantRestorer{}
	h := httpapi.RestoreTenantHandler(res, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/restore", strings.NewReader(`{}`)), platformTenant)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if res.calls != 0 {
		t.Fatalf("RestoreTenant NO debería invocarse sin tenant_id (calls=%d)", res.calls)
	}
}

func TestRestoreTenantHandler_NoIdentity(t *testing.T) {
	res := &fakeTenantRestorer{}
	h := httpapi.RestoreTenantHandler(res, platformTenant)

	req := httptest.NewRequest(http.MethodPost, "/admin/tenants/restore", tenantBody("t-1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if res.calls != 0 {
		t.Fatalf("RestoreTenant NO debería invocarse sin identidad (calls=%d)", res.calls)
	}
}

func TestRestoreTenantHandler_RestorerError(t *testing.T) {
	res := &fakeTenantRestorer{err: errors.New("boom")}
	h := httpapi.RestoreTenantHandler(res, platformTenant)

	req := asOperator(httptest.NewRequest(http.MethodPost, "/admin/tenants/restore", tenantBody("t-1")), platformTenant)
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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
	h := httpapi.SendMessageHandler(snd, testLogger())

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
