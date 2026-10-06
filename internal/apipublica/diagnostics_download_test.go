//go:build pendiente

package apipublica_test

// diagnostics_download_test.go — la mitad de diagnostics_test.go que cubre la descarga D6
// (GET /api/v1/diagnostics/{command_id}) de MountDiagnostics. Partido por tema (E-13); los dobles
// (storeSpy, requesterSpy) viven en diagnostics_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
)

const (
	msgBundleExpired  = "la lectura del diagnóstico no respondió a tiempo, reintenta la descarga"
	msgBundleNotFound = "diagnóstico no encontrado"
	msgBundleGone     = "diagnóstico expirado"
	msgBundleFailed   = "no se pudo leer el diagnóstico"
	msgDiagDownloaded = "diagnóstico remoto descargado"
)

// downloadDiag pide D6 como tenant, con el permiso.
func downloadDiag(h *apipublicahelpertest.Harness, d apipublica.DiagnosticsDeps, tenant, commandID string) *httptest.ResponseRecorder {
	return h.Call(diagCara(h.Common(), d), h.With(tenant, diagPerm), http.MethodGet, bundleTarget+commandID, "")
}

// TestMountDiagnostics_Download_BodyByteForByte: los ocho campos, con su nombre y en su orden,
// los instantes en RFC 3339 UTC y ninguno omitido aunque venga vacío.
func TestMountDiagnostics_Download_BodyByteForByte(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store := newStore()
	madrid := time.FixedZone("CEST", 2*60*60)
	store.ready = &diagnostics.Record{
		CommandID: "cmd-1", SessionID: "sess-a", RequestedBy: "user-1",
		RequestedAt: time.Date(2026, 10, 6, 14, 0, 0, 0, madrid),
		ReceivedAt:  time.Date(2026, 10, 6, 14, 0, 7, 0, madrid),
		Bundle:      diagnostics.Bundle{LogTail: "l1\nl2", GoroutineDump: "goroutine 1 [running]", SubsystemsJSON: `{"intent":"closed"}`},
	}
	rec := downloadDiag(h, diagDeps(store, &requesterSpy{}), tenantA, "cmd-1")
	wantCode(t, "D6", rec, http.StatusOK)
	want := `{"command_id":"cmd-1","session_id":"sess-a","requested_by":"user-1",` +
		`"requested_at":"2026-10-06T12:00:00Z","received_at":"2026-10-06T12:00:07Z",` +
		`"log_tail":"l1\nl2","goroutine_dump":"goroutine 1 [running]","subsystems_json":"{\"intent\":\"closed\"}"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("D6: cuerpo\n  %s\nquiero\n  %s", got, want)
	}
	if len(store.gets) != 1 || store.gets[0] != tenantA+"/cmd-1" {
		t.Errorf("GetBundle = %v, quiero una lectura del tenant del token", store.gets)
	}
	lines := logLines(h, msgDiagDownloaded)
	if len(lines) != 1 || lines[0].Level != "info" {
		t.Fatalf("línea %q: %+v; quiero una, de nivel info", msgDiagDownloaded, lines)
	}
	if f := lines[0].Fields; f["tenant_id"] != tenantA || f["subject"] != subject || f["session_id"] != "sess-a" || f["command_id"] != "cmd-1" || len(f) != 4 {
		t.Errorf("campos = %v; quiero solo tenant_id, subject, session_id y command_id (nada del bundle)", f)
	}
	wantDiagAudit(t, h, "diagnostics", "success", http.StatusOK)

	h = apipublicahelpertest.New(t)
	store.ready = &diagnostics.Record{}
	rec = downloadDiag(h, diagDeps(store, &requesterSpy{}), tenantA, "cmd-1")
	want = `{"command_id":"","session_id":"","requested_by":"","requested_at":"0001-01-01T00:00:00Z",` +
		`"received_at":"0001-01-01T00:00:00Z","log_tail":"","goroutine_dump":"","subsystems_json":""}`
	if got := rec.Body.String(); got != want {
		t.Errorf("D6 con el registro vacío: cuerpo %s, quiero %s (ningún campo se omite)", got, want)
	}
}

// TestMountDiagnostics_Download_StoreOutcomes: cada centinela del store, con su código y cuerpo.
func TestMountDiagnostics_Download_StoreOutcomes(t *testing.T) {
	pending := `{"command_id":"cmd-1","message":"el Edge aún no respondió; reintentar la descarga","status":"pending"}`
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not_found_is_404", diagnostics.ErrNotFound, http.StatusNotFound, `{"error":"` + msgBundleNotFound + `"}`},
		{"wrapped_not_found_is_404", fmt.Errorf("leyendo: %w", diagnostics.ErrNotFound), http.StatusNotFound, `{"error":"` + msgBundleNotFound + `"}`},
		{"expired_is_410", diagnostics.ErrExpired, http.StatusGone, `{"error":"` + msgBundleGone + `"}`},
		{"pending_is_202", diagnostics.ErrPending, http.StatusAccepted, pending},
		{"anything_else_is_500", errors.New("bd caída"), http.StatusInternalServerError, `{"error":"` + msgBundleFailed + `"}`},
		{"canceled_is_not_a_deadline", context.Canceled, http.StatusInternalServerError, `{"error":"` + msgBundleFailed + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newStore()
			store.getErr = tc.err
			rec := downloadDiag(h, diagDeps(store, &requesterSpy{}), tenantA, "cmd-1")
			wantCode(t, tc.name, rec, tc.code)
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("cuerpo %s, quiero %s", got, tc.body)
			}
			if n := len(logLines(h, msgDiagDownloaded)) + len(logLines(h, msgGuardLog)); n != 0 {
				t.Errorf("%d líneas de descarga o de plazo vencido sin haber entregado ni vencido nada", n)
			}
			result := "failure"
			if tc.code < 400 {
				result = "success"
			}
			wantDiagAudit(t, h, "diagnostics", result, tc.code)
		})
	}
}

// TestMountDiagnostics_Download_DeadlineIs504: el plazo vencido se mira ANTES que los centinelas
// del store. Un deadline que además casara con uno de ellos sigue siendo un 504: no se leyó nada.
func TestMountDiagnostics_Download_DeadlineIs504(t *testing.T) {
	for name, arrange := range map[string]func(*storeSpy){
		"store_that_does_not_answer":    func(s *storeSpy) { s.blockGet = true },
		"deadline_wins_over_not_found":  func(s *storeSpy) { s.getErr = fmt.Errorf("%w: %w", diagnostics.ErrNotFound, context.DeadlineExceeded) },
		"deadline_wins_over_pending":    func(s *storeSpy) { s.getErr = fmt.Errorf("%w: %w", diagnostics.ErrPending, context.DeadlineExceeded) },
		"deadline_wins_over_expiration": func(s *storeSpy) { s.getErr = fmt.Errorf("%w: %w", diagnostics.ErrExpired, context.DeadlineExceeded) },
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newStore()
			arrange(store)
			d := diagDeps(store, &requesterSpy{})
			d.DBTimeout = 20 * time.Millisecond
			rec := downloadDiag(h, d, tenantA, "cmd-1")
			wantCode(t, name, rec, http.StatusGatewayTimeout)
			wantErrorBody(t, name, rec, msgBundleExpired)
			wantDeadlineLine(t, h, "diagnostics.bundle", "command_id", "cmd-1")
			wantDiagAudit(t, h, "diagnostics", "failure", http.StatusGatewayTimeout)
		})
	}
}

// TestMountDiagnostics_Download_Clock: DBTimeout acota la lectura del bundle; <= 0 ⇒ 1,5 s.
func TestMountDiagnostics_Download_Clock(t *testing.T) {
	for name, tc := range map[string]struct{ wired, floor, ceil time.Duration }{
		"zero_falls_to_1500ms": {0, time.Second, 1500 * time.Millisecond},
		"wired_timeout":        {5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newStore()
			d := diagDeps(store, &requesterSpy{})
			d.DBTimeout = tc.wired
			downloadDiag(h, d, tenantA, "cmd-1")
			if got := store.remaining["get"]; got <= tc.floor || got > tc.ceil {
				t.Errorf("al contexto de GetBundle le quedaban %s, quiero entre %s y %s", got, tc.floor, tc.ceil)
			}
		})
	}
}

// TestMountDiagnostics_Lifecycle: el ciclo entero sobre el doble en memoria del módulo, sin
// overrides: pedir ⇒ pendiente ⇒ llega el bundle ⇒ listo; y expirado ⇒ 410.
func TestMountDiagnostics_Lifecycle(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store := newStore()
	d := diagDeps(store.Memoria, &requesterSpy{})

	var asked struct {
		CommandID string `json:"command_id"`
	}
	wantJSON(t, "D5", requestDiag(h, d, requestTarget, ""), &asked)
	if asked.CommandID == "" {
		t.Fatal("D5 no devolvió command_id")
	}
	rec := downloadDiag(h, d, tenantA, asked.CommandID)
	wantCode(t, "D6 antes del bundle", rec, http.StatusAccepted)
	if want := `{"command_id":"` + asked.CommandID + `","message":"el Edge aún no respondió; reintentar la descarga","status":"pending"}`; rec.Body.String() != want {
		t.Errorf("D6 pendiente: cuerpo %s, quiero %s", rec.Body.String(), want)
	}

	// Llega el bundle (lo que hace el demux del gateway).
	found, err := store.SaveBundle(context.Background(), tenantA, "sess-a", asked.CommandID, diagnostics.Bundle{LogTail: "linea-1"})
	if err != nil || !found {
		t.Fatalf("SaveBundle found=%v err=%v", found, err)
	}
	var bundle struct {
		CommandID   string `json:"command_id"`
		SessionID   string `json:"session_id"`
		RequestedBy string `json:"requested_by"`
		LogTail     string `json:"log_tail"`
	}
	rec = downloadDiag(h, d, tenantA, asked.CommandID)
	wantCode(t, "D6 con el bundle", rec, http.StatusOK)
	wantJSON(t, "D6 con el bundle", rec, &bundle)
	if bundle.CommandID != asked.CommandID || bundle.SessionID != "sess-a" || bundle.RequestedBy != subject || bundle.LogTail != "linea-1" {
		t.Errorf("bundle %+v; quiero el de la solicitud, pedido por el subject del token", bundle)
	}

	if !store.Expire(tenantA, asked.CommandID) {
		t.Fatal("no se pudo vencer la solicitud")
	}
	rec = downloadDiag(h, d, tenantA, asked.CommandID)
	wantCode(t, "D6 vencido", rec, http.StatusGone)
	wantErrorBody(t, "D6 vencido", rec, msgBundleGone)
	wantCode(t, "D6 tras el borrado perezoso", downloadDiag(h, d, tenantA, asked.CommandID), http.StatusNotFound)
}

// TestMountDiagnostics_Download_TenantIsolation: el command_id de OTRO tenant es un 404 opaco
// (INV-8), igual que uno que no existe, aunque el bundle ya esté listo.
func TestMountDiagnostics_Download_TenantIsolation(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store := newStore()
	d := diagDeps(store, &requesterSpy{})
	var asked struct {
		CommandID string `json:"command_id"`
	}
	wantJSON(t, "D5 de tenantA", requestDiag(h, d, requestTarget, ""), &asked)
	if found, err := store.SaveBundle(context.Background(), tenantA, "sess-a", asked.CommandID, diagnostics.Bundle{LogTail: "secreto-de-a"}); err != nil || !found {
		t.Fatalf("SaveBundle found=%v err=%v", found, err)
	}

	rec := downloadDiag(h, d, tenantB, asked.CommandID)
	wantCode(t, "D6 de tenantB sobre el command_id de tenantA", rec, http.StatusNotFound)
	wantErrorBody(t, "D6 de tenantB", rec, msgBundleNotFound)
	if last := store.gets[len(store.gets)-1]; last != tenantB+"/"+asked.CommandID {
		t.Errorf("GetBundle(%s); quiero que se lea con el tenant DEL TOKEN (%s)", last, tenantB)
	}
	unknown := downloadDiag(h, d, tenantB, "no-existe")
	if unknown.Code != rec.Code || unknown.Body.String() != rec.Body.String() {
		t.Errorf("el ajeno (%d %s) se distingue del inexistente (%d %s)", rec.Code, rec.Body.String(), unknown.Code, unknown.Body.String())
	}
	wantCode(t, "D6 de su dueño", downloadDiag(h, d, tenantA, asked.CommandID), http.StatusOK)
}

func TestMountDiagnostics_Download_NilLogServesTheSame(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	token := h.With(tenantA, diagPerm)
	store := newStore()
	store.ready = &diagnostics.Record{CommandID: "cmd-1"}
	cara := diagCara(k, diagDeps(store, &requesterSpy{}))
	wantCode(t, "D6 sin logger", h.Call(cara, token, http.MethodGet, bundleTarget+"cmd-1", ""), http.StatusOK)
	store.blockGet = true
	slow := diagCara(k, apipublica.DiagnosticsDeps{
		Diagnostics: store, DiagnosticsRequester: &requesterSpy{}, Sessions: sessA(), DBTimeout: 20 * time.Millisecond,
	})
	wantCode(t, "D6 vencido sin logger", h.Call(slow, token, http.MethodGet, bundleTarget+"cmd-1", ""), http.StatusGatewayTimeout)
}
