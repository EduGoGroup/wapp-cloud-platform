package apipublica

// pathguards_internal_test.go — las defensas de «comodín vacío en la ruta» que los contratos de
// MountDiagnostics y MountTenantContent prometen y que ninguna ruta montada alcanza (el mux no
// casa un comodín con un segmento vacío): D5 y D6 ante un {id} o un {command_id} vacíos, e I6,
// I7 e I8 ante una {ref} vacía. Se prueban llamando a los handlers a pelo, con una identidad con
// empresa y sin poner el valor de ruta (el mismo molde que flows_internal_test.go).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// pathGuardDiagnosticsStore es un DiagnosticsStore que cuenta las llamadas y contesta el cero:
// el consentimiento sale APAGADO, así que una petición que pasa la defensa se queda en el 403.
type pathGuardDiagnosticsStore struct{ calls int }

var _ DiagnosticsStore = (*pathGuardDiagnosticsStore)(nil)

func (s *pathGuardDiagnosticsStore) ConsentEnabled(context.Context, string) (bool, error) {
	s.calls++
	return false, nil
}

func (s *pathGuardDiagnosticsStore) CreateRequest(context.Context, string, string, string, string, time.Time) error {
	s.calls++
	return nil
}

func (s *pathGuardDiagnosticsStore) DeleteRequest(context.Context, string, string) error {
	s.calls++
	return nil
}

func (s *pathGuardDiagnosticsStore) GetBundle(context.Context, string, string) (diagnostics.Record, error) {
	s.calls++
	return diagnostics.Record{}, nil
}

// pathGuardContentStore es un TenantContentStore que cuenta las llamadas y contesta el cero.
type pathGuardContentStore struct{ calls int }

var _ TenantContentStore = (*pathGuardContentStore)(nil)

func (s *pathGuardContentStore) UpsertTenantContent(context.Context, string, string, []byte) error {
	s.calls++
	return nil
}

func (s *pathGuardContentStore) GetTenantContent(context.Context, string, string) ([]byte, error) {
	s.calls++
	return []byte(`{}`), nil
}

func (s *pathGuardContentStore) ListTenantContent(context.Context, string) ([]store.TenantContentSummary, error) {
	s.calls++
	return nil, nil
}

func (s *pathGuardContentStore) DeleteTenantContent(context.Context, string, string) error {
	s.calls++
	return nil
}

// pathGuardServe llama al handler a pelo con una identidad con empresa. Con value vacío no pone
// el comodín, que es como le llegaría una petición que el mux hubiera dejado pasar sin él.
func pathGuardServe(h http.Handler, method, wildcard, value, body string) *httptest.ResponseRecorder {
	ctx := httpapi.WithIdentity(context.Background(), httpapi.Identity{TenantID: "tenant-a", Subject: "alguien"})
	req := httptest.NewRequestWithContext(ctx, method, "/", strings.NewReader(body))
	if value != "" {
		req.SetPathValue(wildcard, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestDiagnosticsHandlers_EmptyPathValue: D5 rechaza un {id} vacío y D6 un {command_id} vacío,
// cada uno con su texto y sin tocar el almacén; con el valor puesto, la misma petición pasa la
// defensa y llega al almacén (en D5, al gate de consentimiento, que aquí contesta 403).
func TestDiagnosticsHandlers_EmptyPathValue(t *testing.T) {
	cases := []struct {
		name, method, wildcard, wantBody string
		handler                          func(DiagnosticsStore) http.Handler
		wantCodeWithValue                int
	}{
		{"D5_request", http.MethodPost, "id", `{"error":"session id requerido en la ruta"}`,
			func(s DiagnosticsStore) http.Handler {
				return requestDiagnosticsHandler(nil, s, nil, time.Minute, time.Second, nil)
			}, http.StatusForbidden},
		{"D6_get", http.MethodGet, "command_id", `{"error":"command_id requerido en la ruta"}`,
			func(s DiagnosticsStore) http.Handler { return getDiagnosticsHandler(s, time.Second, nil) },
			http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &pathGuardDiagnosticsStore{}
			rec := pathGuardServe(tc.handler(st), tc.method, tc.wildcard, "", "")
			if rec.Code != http.StatusBadRequest || rec.Body.String() != tc.wantBody {
				t.Errorf("comodín vacío: código %d y cuerpo %s; quiero 400 %s", rec.Code, rec.Body.String(), tc.wantBody)
			}
			if st.calls != 0 {
				t.Errorf("comodín vacío: el almacén recibió %d llamadas, quiero 0", st.calls)
			}

			rec = pathGuardServe(tc.handler(st), tc.method, tc.wildcard, "x", "")
			if rec.Code != tc.wantCodeWithValue || st.calls != 1 {
				t.Errorf("con el comodín puesto: código %d y %d llamadas al almacén; quiero %d y 1 (%s)",
					rec.Code, st.calls, tc.wantCodeWithValue, rec.Body.String())
			}
		})
	}
}

// TestTenantContentHandlers_EmptyRef: I6 (PUT), I7 (GET) e I8 (DELETE) rechazan una {ref} vacía
// antes de leer el cuerpo y sin tocar el almacén; con la ref puesta, la misma petición pasa.
func TestTenantContentHandlers_EmptyRef(t *testing.T) {
	const wantBody = `{"error":"ref requerida en la ruta"}`
	cases := []struct {
		name, method      string
		handler           func(TenantContentStore) http.Handler
		wantCodeWithValue int
	}{
		{"I6_upsert", http.MethodPut,
			func(s TenantContentStore) http.Handler { return tenantContentUpsertHandler(s, 0) }, http.StatusOK},
		{"I7_get", http.MethodGet, tenantContentGetHandler, http.StatusOK},
		{"I8_delete", http.MethodDelete, tenantContentDeleteHandler, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &pathGuardContentStore{}
			rec := pathGuardServe(tc.handler(st), tc.method, "ref", "", `{"k":1}`)
			if rec.Code != http.StatusBadRequest || rec.Body.String() != wantBody {
				t.Errorf("ref vacía: código %d y cuerpo %s; quiero 400 %s", rec.Code, rec.Body.String(), wantBody)
			}
			if st.calls != 0 {
				t.Errorf("ref vacía: el almacén recibió %d llamadas, quiero 0", st.calls)
			}

			rec = pathGuardServe(tc.handler(st), tc.method, "ref", "catalogo", `{"k":1}`)
			if rec.Code != tc.wantCodeWithValue || st.calls != 1 {
				t.Errorf("con la ref puesta: código %d y %d llamadas al almacén; quiero %d y 1 (%s)",
					rec.Code, st.calls, tc.wantCodeWithValue, rec.Body.String())
			}
		})
	}
}
