//go:build pendiente

package iamhttp

// Se prueba el TRANSPORTE y nada más: los dobles no comprueban membresías porque el transporte
// tampoco. Una aserción por promesa de R-H1 y R-H2 (active_tenant.go).
//
// serve vive aquí: active_tenant es el primero de los handlers en pasar a verde y el auxiliar es
// común al paquete (hallazgo 20 de F2).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// serve ejecuta una petición contra h y devuelve lo grabado. body vacío ⇒ petición sin cuerpo;
// headers va en pares clave, valor.
func serve(t *testing.T, h http.Handler, method, target, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// jsonKeys devuelve las claves de un objeto JSON, ordenadas.
func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("no es un objeto JSON: %v (%s)", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// fakeSelector devuelve siempre el mismo error (o nil) y guarda lo que recibió.
type fakeSelector struct {
	err      error
	received string
	calls    int
}

var _ in.ActiveTenantSelector = (*fakeSelector)(nil)

func (s *fakeSelector) SelectActiveTenant(_ context.Context, tenantID string) error {
	s.calls++
	s.received = tenantID
	return s.err
}

// fakeLister devuelve siempre la misma lista (o el mismo error) y cuenta las llamadas.
type fakeLister struct {
	tenants  []domain.UserTenant
	activeID string
	err      error
	calls    int
}

var _ in.TenantLister = (*fakeLister)(nil)

func (l *fakeLister) TenantsOfCaller(context.Context) ([]domain.UserTenant, string, error) {
	l.calls++
	return l.tenants, l.activeID, l.err
}

const (
	tenantA = "11111111-1111-1111-1111-111111111111"
	tenantB = "22222222-2222-2222-2222-222222222222"
)

func selectTenant(t *testing.T, sel *fakeSelector, body string) *httptest.ResponseRecorder {
	t.Helper()
	var h *ActiveTenantHandler = NewActiveTenantHandler(sel, &fakeLister{})
	return serve(t, h.Select(), http.MethodPost, "/api/v1/auth/active-tenant", body)
}

func listTenants(t *testing.T, l *fakeLister) *httptest.ResponseRecorder {
	t.Helper()
	return serve(t, NewActiveTenantHandler(&fakeSelector{}, l).List(), http.MethodGet, "/api/v1/auth/tenants", "")
}

// R-H1: 204 sin cuerpo Y el dato llega al puerto, juntos (un 204 sin llamar al puerto pasaría
// cualquier test que solo mirara el código).
func TestSelect_204AndTenantReachesPort(t *testing.T) {
	sel := &fakeSelector{}
	rec := selectTenant(t, sel, `{"tenant_id":"`+tenantB+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("código = %d; se esperaba 204 (cuerpo %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 con cuerpo %q: lo que la persona necesita está en su SIGUIENTE token", rec.Body.String())
	}
	if sel.calls != 1 || sel.received != tenantB {
		t.Errorf("puerto llamado %d veces con %q; se esperaba 1 con %q", sel.calls, sel.received, tenantB)
	}
}

// R-H1: los demás desenlaces.
func TestSelect_Outcomes(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		body      string
		status    int
		wantCalls int
	}{
		{"not_member_is_404", domain.ErrNotFound, `{"tenant_id":"` + tenantB + `"}`, http.StatusNotFound, 1},
		{"invalid_input_is_400", domain.ErrInvalidInput, `{"tenant_id":""}`, http.StatusBadRequest, 1},
		{"broken_json_is_400_without_port", nil, `{`, http.StatusBadRequest, 0},
		{"infra_is_500", errors.New("la base se cayó"), `{"tenant_id":"x"}`, http.StatusInternalServerError, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel := &fakeSelector{err: c.err}
			rec := selectTenant(t, sel, c.body)
			if rec.Code != c.status {
				t.Errorf("código = %d; se esperaba %d (cuerpo %s)", rec.Code, c.status, rec.Body.String())
			}
			if sel.calls != c.wantCalls {
				t.Errorf("llamadas al puerto = %d; se esperaban %d", sel.calls, c.wantCalls)
			}
		})
	}
}

// R-H1: el 404 no es oráculo — es el cuerpo genérico del módulo y no nombra la causa.
func TestSelect_404IsNotAnOracle(t *testing.T) {
	rec := selectTenant(t, &fakeSelector{err: domain.ErrNotFound}, `{"tenant_id":"`+tenantB+`"}`)
	if got := rec.Body.String(); got != errorBody("recurso no encontrado") {
		t.Fatalf("cuerpo = %s; se esperaba el genérico %s", got, errorBody("recurso no encontrado"))
	}
	lower := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"miembro", "member", "pertenec", "empresa", "tenant", "existe"} {
		if strings.Contains(lower, leak) {
			t.Errorf("el cuerpo dice %q: distingue «no eres de ahí» de «no existe»", leak)
		}
	}
}

// R-H2: forma exacta — una clave arriba, tres por elemento, orden conservado, solo la activa.
func TestList_ExactShapeOrderAndActiveMark(t *testing.T) {
	l := &fakeLister{
		tenants:  []domain.UserTenant{{ID: tenantA, DisplayName: "Panadería Doña Rosa"}, {ID: tenantB, DisplayName: "Catering del Sur"}},
		activeID: tenantB,
	}
	rec := listTenants(t, l)
	if rec.Code != http.StatusOK || l.calls != 1 {
		t.Fatalf("código = %d, llamadas = %d; se esperaba 200 y 1 (cuerpo %s)", rec.Code, l.calls, rec.Body.String())
	}
	if keys := jsonKeys(t, rec.Body.Bytes()); !slices.Equal(keys, []string{"tenants"}) {
		t.Errorf("claves de la raíz = %v; se esperaba solo [tenants]", keys)
	}
	var out struct {
		Tenants []json.RawMessage `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Tenants) != 2 {
		t.Fatalf("respuesta = %s (err %v); se esperaban 2 empresas", rec.Body.String(), err)
	}
	for _, raw := range out.Tenants {
		if keys := jsonKeys(t, raw); !slices.Equal(keys, []string{"active", "display_name", "id"}) {
			t.Errorf("claves del elemento = %v; se esperaba [active display_name id] y ni una más", keys)
		}
	}
	want := `{"tenants":[{"id":"` + tenantA + `","display_name":"Panadería Doña Rosa","active":false},` +
		`{"id":"` + tenantB + `","display_name":"Catering del Sur","active":true}]}`
	if got := rec.Body.String(); got != want {
		t.Errorf("cuerpo = %s; se esperaba %s", got, want)
	}
}

// R-H2: cero empresas (nil del puerto) ⇒ 200 con la cadena literal "tenants":[] (no null).
func TestList_NoTenantsIsLiteralEmptyArray(t *testing.T) {
	rec := listTenants(t, &fakeLister{tenants: nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("código = %d; se esperaba 200: cero empresas es un estado (D-056.12), no un 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"tenants":[]`) {
		t.Errorf("cuerpo = %s; se esperaba la cadena literal \"tenants\":[] (un null no es iterable)", rec.Body.String())
	}
}

// R-H2: sin elegida (activeID vacío) no se marca ninguna.
func TestList_NoActiveMarksNothing(t *testing.T) {
	rec := listTenants(t, &fakeLister{tenants: []domain.UserTenant{{ID: tenantA, DisplayName: "A"}, {ID: tenantB, DisplayName: "B"}}})
	if strings.Contains(rec.Body.String(), `"active":true`) {
		t.Errorf("marcó una activa sin haberla: %s", rec.Body.String())
	}
}

// R-H2: sin totales ni conteos ni nada que insinúe empresas de fuera.
func TestList_LeaksNothingFromOutside(t *testing.T) {
	rec := listTenants(t, &fakeLister{tenants: []domain.UserTenant{{ID: tenantA, DisplayName: "A"}}, activeID: tenantA})
	lower := strings.ToLower(rec.Body.String())
	if !strings.Contains(lower, "tenants") {
		t.Fatalf("el cuerpo no parece el listado: %s", rec.Body.String())
	}
	for _, leak := range []string{"total", "count", "conteo", "otras", "restant", "has_more", "active_tenant_id"} {
		if strings.Contains(lower, leak) {
			t.Errorf("la respuesta lleva %q: %s", leak, rec.Body.String())
		}
	}
}

// R-H2: el listado no tiene 404 — 400 de cableado y 500 de infraestructura.
func TestList_FailureOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"no_caller_is_400", domain.ErrInvalidInput, http.StatusBadRequest},
		{"infra_is_500", errors.New("la base se cayó"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := listTenants(t, &fakeLister{err: c.err}); rec.Code != c.status {
				t.Errorf("código = %d; se esperaba %d (cuerpo %s)", rec.Code, c.status, rec.Body.String())
			}
		})
	}
}
