package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const keyADiag = "key-a-diag" // tenantA, scope diagnostics.request

// diagKeys extiende apiKeys() con la credencial de diagnóstico remoto de tenantA.
func diagKeys() map[string]in.ServiceIdentity {
	keys := apiKeys()
	keys[keyADiag] = in.ServiceIdentity{TenantID: tenantA, ClientID: "guardian-a", Scopes: []string{"diagnostics.request"}}
	return keys
}

// fakeDiagRequester captura el DiagnosticsRequest emitido (y puede simular offline).
type fakeDiagRequester struct {
	called    bool
	gotSess   string
	gotCmd    string
	gotScope  string
	returnErr error
}

func (f *fakeDiagRequester) RequestDiagnostics(_ context.Context, sessionID, commandID, scope string) error {
	f.called = true
	f.gotSess, f.gotCmd, f.gotScope = sessionID, commandID, scope
	return f.returnErr
}

// diagDeps arma las Deps del diagnóstico remoto: store en memoria + emisor fake +
// la sesión sess-a bajo tenantA (para el aislamiento).
func diagDeps(store publicapi.DiagnosticsStore, gw publicapi.DiagnosticsRequester) publicapi.Deps {
	return publicapi.Deps{
		Sessions:             fakeSessions{byTenant: map[string][]fleet.Session{tenantA: {{TenantID: tenantA, SessionID: "sess-a"}}}},
		Diagnostics:          store,
		DiagnosticsRequester: gw,
	}
}

func TestDiagnostics_Request_202_EmiteYPersiste(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	gw := &fakeDiagRequester{}
	mux := newAPI(diagDeps(store, gw), diagKeys())

	rec := call(mux, keyADiag, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", `{"scope":"logs"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code=%d, quiero 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CommandID == "" || resp.Status != "pending" {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
	// El request se emitió con el mismo command_id y scope, a la sesión indicada.
	if !gw.called || gw.gotSess != "sess-a" || gw.gotCmd != resp.CommandID || gw.gotScope != "logs" {
		t.Fatalf("RequestDiagnostics mal invocado: %+v", gw)
	}
	// La solicitud quedó pendiente (descargarla ⇒ 202, aún sin bundle).
	if _, err := store.GetBundle(context.Background(), tenantA, resp.CommandID); !errors.Is(err, diagnostics.ErrPending) {
		t.Fatalf("esperaba ErrPending tras el request, got %v", err)
	}
}

func TestDiagnostics_Request_403_OptOut(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	store.SetConsent(tenantA, false) // el tenant se excluyó (opt-out)
	gw := &fakeDiagRequester{}
	mux := newAPI(diagDeps(store, gw), diagKeys())

	rec := call(mux, keyADiag, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 (opt-out); body=%s", rec.Code, rec.Body.String())
	}
	if gw.called {
		t.Fatal("no debió emitir el request con el consentimiento apagado")
	}
}

func TestDiagnostics_Request_403_SinScope(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	gw := &fakeDiagRequester{}
	mux := newAPI(diagDeps(store, gw), diagKeys())

	// keyARead (flows.read) no cubre diagnostics.request ⇒ 403 en el middleware.
	rec := call(mux, keyARead, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 (sin scope); body=%s", rec.Code, rec.Body.String())
	}
	if gw.called {
		t.Fatal("no debió emitir el request sin el grant")
	}
}

func TestDiagnostics_Request_404_CrossTenant(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	gw := &fakeDiagRequester{}
	mux := newAPI(diagDeps(store, gw), diagKeys())

	// sess-b no es de tenantA ⇒ 404 (aislamiento INV-8), sin emitir nada.
	rec := call(mux, keyADiag, http.MethodPost, "/api/v1/sessions/sess-b/diagnostics", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (aislamiento); body=%s", rec.Code, rec.Body.String())
	}
	if gw.called {
		t.Fatal("no debió emitir el request por una sesión ajena")
	}
}

func TestDiagnostics_Request_502_Offline_HaceRollback(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	gw := &fakeDiagRequester{returnErr: session.ErrSessionOffline}
	mux := newAPI(diagDeps(store, gw), diagKeys())

	rec := call(mux, keyADiag, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d, quiero 502 (offline); body=%s", rec.Code, rec.Body.String())
	}
	if gw.gotCmd == "" {
		t.Fatal("el request debió intentarse (con un command_id)")
	}
	// Rollback: la solicitud NO quedó pendiente (se borró tras el push fallido).
	if _, err := store.GetBundle(context.Background(), tenantA, gw.gotCmd); !errors.Is(err, diagnostics.ErrNotFound) {
		t.Fatalf("esperaba ErrNotFound (rollback), got %v", err)
	}
}

func TestDiagnostics_Download_Pending_Then_Ready(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	gw := &fakeDiagRequester{}
	mux := newAPI(diagDeps(store, gw), diagKeys())

	// 1) Solicitar ⇒ pendiente.
	rec := call(mux, keyADiag, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", "")
	var reqResp struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reqResp); err != nil {
		t.Fatalf("unmarshal command_id: %v", err)
	}

	// 2) Descargar mientras sigue pendiente ⇒ 202.
	if got := call(mux, keyADiag, http.MethodGet, "/api/v1/diagnostics/"+reqResp.CommandID, ""); got.Code != http.StatusAccepted {
		t.Fatalf("descarga pendiente code=%d, quiero 202; body=%s", got.Code, got.Body.String())
	}

	// 3) Llega el bundle (simula el demux del Gateway) ⇒ descarga 200 con el contenido.
	found, err := store.SaveBundle(context.Background(), tenantA, "sess-a", reqResp.CommandID, diagnostics.Bundle{
		LogTail: "linea-1\nlinea-2", GoroutineDump: "goroutine 1 [running]", SubsystemsJSON: `{"intent":"closed"}`,
	})
	if err != nil || !found {
		t.Fatalf("SaveBundle found=%v err=%v", found, err)
	}
	got := call(mux, keyADiag, http.MethodGet, "/api/v1/diagnostics/"+reqResp.CommandID, "")
	if got.Code != http.StatusOK {
		t.Fatalf("descarga lista code=%d, quiero 200; body=%s", got.Code, got.Body.String())
	}
	var bundle struct {
		LogTail       string `json:"log_tail"`
		GoroutineDump string `json:"goroutine_dump"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	if bundle.LogTail == "" || bundle.GoroutineDump == "" {
		t.Fatalf("bundle incompleto: %+v", bundle)
	}
}

func TestDiagnostics_Download_404_Desconocido(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	mux := newAPI(diagDeps(store, &fakeDiagRequester{}), diagKeys())

	if rec := call(mux, keyADiag, http.MethodGet, "/api/v1/diagnostics/no-existe", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (desconocido); body=%s", rec.Code, rec.Body.String())
	}
}

func TestDiagnostics_Download_CrossTenant_404(t *testing.T) {
	store := diagnostics.NewMemoryStore()
	gw := &fakeDiagRequester{}
	// tenantB también tiene su credencial y su sesión, para intentar leer el ajeno.
	keys := diagKeys()
	keys["key-b-diag"] = in.ServiceIdentity{TenantID: tenantB, ClientID: "guardian-b", Scopes: []string{"diagnostics.request"}}
	d := diagDeps(store, gw)
	mux := newAPI(d, keys)

	// tenantA solicita un diagnóstico.
	rec := call(mux, keyADiag, http.MethodPost, "/api/v1/sessions/sess-a/diagnostics", "")
	var reqResp struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reqResp); err != nil {
		t.Fatalf("unmarshal command_id: %v", err)
	}

	// tenantB intenta descargarlo por su command_id ⇒ 404 (aislamiento INV-8).
	if got := call(mux, "key-b-diag", http.MethodGet, "/api/v1/diagnostics/"+reqResp.CommandID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant download code=%d, quiero 404; body=%s", got.Code, got.Body.String())
	}
}
