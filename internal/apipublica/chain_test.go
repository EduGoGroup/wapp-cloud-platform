//go:build pendiente

package apipublica_test

// chain_test.go — cubre el contrato de chain.go: las cadenas W y R que los Mount* arman con
// Common, el access-log y los campos nil de Common. Common no tiene funciones propias; sus
// promesas se cumplen (o no) en las rutas que montan las áreas, así que se prueban a través de
// ellas: una R (C2, MountEntitlements) y una W (B2, MountRolePlane).
//
// Aquí viven también los auxiliares que comparten los tests de las áreas (wantCode, wantJSON,
// wantPatterns, routeCase y checkChain).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

const (
	tenantA = apipublicahelpertest.TenantA
	subject = apipublicahelpertest.Subject

	accessLogMsg     = "petición pública"
	accessLogFailMsg = "petición pública sin entregar: la respuesta no se pudo escribir"
)

// wantCode exige el código EXACTO (un `>= 400` daría verde con el 403 que se quiere prohibir).
func wantCode(t *testing.T, what string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("%s: código %d, quiero %d (cuerpo %q)", what, rec.Code, want, rec.Body.String())
	}
}

// wantJSON vuelca el cuerpo JSON de rec en dst y falla si no casa.
func wantJSON(t *testing.T, what string, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("%s: cuerpo JSON ilegible: %v (%q)", what, err, rec.Body.String())
	}
}

// wantErrorBody exige el cuerpo {"error": msg}.
func wantErrorBody(t *testing.T, what string, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	var body map[string]string
	wantJSON(t, what, rec, &body)
	if body["error"] != msg || len(body) != 1 {
		t.Errorf("%s: cuerpo %v, quiero {\"error\": %q}", what, body, msg)
	}
}

// wantPatterns compara los patrones registrados en c con want, sin mirar el orden.
func wantPatterns(t *testing.T, what string, c *apipublica.Cara, want []string) {
	t.Helper()
	got := slices.Sorted(slices.Values(c.Patrones()))
	exp := slices.Sorted(slices.Values(want))
	if !slices.Equal(got, exp) {
		t.Errorf("%s: patrones registrados\n  %q\nquiero (byte a byte)\n  %q", what, got, exp)
	}
}

// routeCase es una ruta de un área con su cadena: perm es el permiso, resource el recurso de
// auditoría ("" = cadena R, sin auditoría) y want el código del camino feliz.
type routeCase struct {
	id       string
	method   string
	target   string
	body     string
	perm     string
	resource string
	want     int
}

// checkChain afirma las promesas de Common sobre una ruta montada en cara: 401 sin token, 403
// con un token sin el permiso y con uno sin empresa (sin registro en ninguno de los tres), y el
// camino feliz con su código y EXACTAMENTE un registro si es W (cero si es R).
func checkChain(t *testing.T, h *apipublicahelpertest.Harness, cara *apipublica.Cara, rc routeCase) {
	t.Helper()
	before := len(h.Auditor().Records())

	rec := h.Call(cara, "", rc.method, rc.target, rc.body)
	wantCode(t, rc.id+" sin token", rec, http.StatusUnauthorized)
	wantErrorBody(t, rc.id+" sin token", rec, "autenticación requerida")

	rec = h.Call(cara, h.With(tenantA, "otra.cosa"), rc.method, rc.target, rc.body)
	wantCode(t, rc.id+" sin el permiso "+rc.perm, rec, http.StatusForbidden)
	wantErrorBody(t, rc.id+" sin el permiso", rec, "permiso denegado")

	rec = h.Call(cara, h.Tenantless("sin-empresa"), rc.method, rc.target, rc.body)
	wantCode(t, rc.id+" con token sin empresa", rec, http.StatusForbidden)

	if n := len(h.Auditor().Records()) - before; n != 0 {
		t.Errorf("%s: 401/403 dejaron %d registros de auditoría, quiero 0", rc.id, n)
	}

	rec = h.Call(cara, h.With(tenantA, rc.perm), rc.method, rc.target, rc.body)
	wantCode(t, rc.id+" con el permiso "+rc.perm, rec, rc.want)

	records := h.Auditor().Records()[before:]
	if rc.resource == "" {
		if len(records) != 0 {
			t.Errorf("%s es R: dejó %d registros de auditoría, quiero 0", rc.id, len(records))
		}
		return
	}
	if len(records) != 1 {
		t.Fatalf("%s es W: dejó %d registros de auditoría, quiero exactamente 1", rc.id, len(records))
	}
	got := records[0]
	if got.TenantID != tenantA || got.Actor != subject || got.Action != rc.perm || got.Resource != rc.resource || got.Result != "success" {
		t.Errorf("%s: registro %+v, quiero tenant %s, actor %s, action %s, resource %s, result success",
			rc.id, got, tenantA, subject, rc.perm, rc.resource)
	}
	if got.Meta["status"] != rc.want {
		t.Errorf("%s: meta.status = %v, quiero %d", rc.id, got.Meta["status"], rc.want)
	}
}

// entitlementsCara monta C2 (cadena R) con k.
func entitlementsCara(k apipublica.Common) *apipublica.Cara {
	f := entitlementshelpertest.NewFake()
	f.SetPlan(tenantA, "basic")
	f.Enable(tenantA, "cart_basic")
	c := apipublica.Nueva()
	apipublica.MountEntitlements(c, k, apipublica.EntitlementsDeps{Entitlements: f})
	return c
}

// rolesCara monta el grupo de roles (B2 es W, recurso role) con k y el doble dado.
func rolesCara(k apipublica.Common, roles *roleAdminFake) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountRolePlane(c, k, apipublica.RolePlaneDeps{Roles: roles})
	return c
}

func TestCommon_ReadChain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	checkChain(t, h, entitlementsCara(h.Common()), routeCase{
		id: "C2", method: http.MethodGet, target: "/api/v1/entitlements", perm: "entitlements.read", want: http.StatusOK,
	})
}

func TestCommon_WriteChain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	checkChain(t, h, rolesCara(h.Common(), &roleAdminFake{}), routeCase{
		id: "B2", method: http.MethodPost, target: "/api/v1/roles", body: `{"name":"ventas"}`,
		perm: "roles.write", resource: "role", want: http.StatusCreated,
	})
}

func TestCommon_WriteChainAuditsFailure(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := rolesCara(h.Common(), &roleAdminFake{err: domain.ErrConflict})
	rec := h.Call(cara, h.With(tenantA, "roles.write"), http.MethodPost, "/api/v1/roles", `{"name":"ventas"}`)
	wantCode(t, "B2 con conflicto", rec, http.StatusConflict)
	records := h.Auditor().Records()
	if len(records) != 1 || records[0].Result != "failure" || records[0].Meta["status"] != http.StatusConflict {
		t.Errorf("registros = %+v, quiero uno con result failure y meta.status 409", records)
	}
}

// accessLines devuelve las líneas de access-log (de éxito o de fallo de entrega).
func accessLines(h *apipublicahelpertest.Harness) []apipublicahelpertest.LogEntry {
	var out []apipublicahelpertest.LogEntry
	for _, e := range h.Log().Entries() {
		if e.Msg == accessLogMsg || e.Msg == accessLogFailMsg {
			out = append(out, e)
		}
	}
	return out
}

func TestCommon_AccessLogLine(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := entitlementsCara(h.Common())
	h.Call(cara, h.With(tenantA, "entitlements.read"), http.MethodGet, "/api/v1/entitlements?correo=ana@x.com", "")

	lines := accessLines(h)
	if len(lines) != 1 {
		t.Fatalf("access-log: %d líneas, quiero 1: %+v", len(lines), lines)
	}
	l := lines[0]
	if l.Level != "info" || l.Msg != accessLogMsg {
		t.Errorf("línea = %s %q, quiero info %q", l.Level, l.Msg, accessLogMsg)
	}
	want := map[string]any{"method": http.MethodGet, "path": "/api/v1/entitlements", "status": http.StatusOK, "tenant_id": tenantA}
	for k, v := range want {
		if l.Fields[k] != v {
			t.Errorf("campo %s = %v, quiero %v", k, l.Fields[k], v)
		}
	}
	if _, ok := l.Fields["duration_ms"]; !ok {
		t.Error("falta el campo duration_ms")
	}
	if strings.Contains(fmt.Sprint(l.Fields), "ana@x.com") {
		t.Errorf("la query llegó al log (PII): %v", l.Fields)
	}
}

func TestCommon_AccessLogSees401(t *testing.T) {
	h := apipublicahelpertest.New(t)
	h.Call(entitlementsCara(h.Common()), "", http.MethodGet, "/api/v1/entitlements", "")
	lines := accessLines(h)
	if len(lines) != 1 || lines[0].Fields["status"] != http.StatusUnauthorized {
		t.Fatalf("access-log del 401 = %+v, quiero una línea con status 401", lines)
	}
	if _, ok := lines[0].Fields["tenant_id"]; ok {
		t.Errorf("el 401 lleva tenant_id: %v", lines[0].Fields)
	}
}

// failingWriter es un ResponseWriter cuyo Write falla, como con el deadline de escritura vencido.
type failingWriter struct{ header http.Header }

var errWriteFailed = errors.New("write: deadline vencido")

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(int)           {}
func (w *failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

func TestCommon_AccessLogWriteError(t *testing.T) {
	h := apipublicahelpertest.New(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/entitlements", nil)
	req.Header.Set("Authorization", "Bearer "+h.With(tenantA, "entitlements.read"))
	entitlementsCara(h.Common()).ServeHTTP(&failingWriter{header: http.Header{}}, req)

	lines := accessLines(h)
	if len(lines) != 1 {
		t.Fatalf("access-log con Write fallido: %d líneas, quiero 1: %+v", len(lines), lines)
	}
	if lines[0].Level != "error" || lines[0].Msg != accessLogFailMsg {
		t.Errorf("línea = %s %q, quiero error %q", lines[0].Level, lines[0].Msg, accessLogFailMsg)
	}
	if err, ok := lines[0].Fields["write_error"].(error); !ok || !errors.Is(err, errWriteFailed) {
		t.Errorf("write_error = %v, quiero el error del Write", lines[0].Fields["write_error"])
	}
}

func TestCommon_NilLogServesTheSame(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	checkChain(t, h, entitlementsCara(k), routeCase{
		id: "C2 sin logger", method: http.MethodGet, target: "/api/v1/entitlements", perm: "entitlements.read", want: http.StatusOK,
	})
	checkChain(t, h, rolesCara(k, &roleAdminFake{}), routeCase{
		id: "B2 sin logger", method: http.MethodPost, target: "/api/v1/roles", body: `{"name":"ventas"}`,
		perm: "roles.write", resource: "role", want: http.StatusCreated,
	})
	if n := len(h.Log().Entries()); n != 0 {
		t.Errorf("sin logger en Common, el logger del arnés recibió %d líneas", n)
	}
}

func TestCommon_NilAuditorServesWrites(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := rolesCara(apipublica.Common{MW: h.MW(), Log: h.Log()}, &roleAdminFake{})
	rec := h.Call(cara, h.With(tenantA, "roles.write"), http.MethodPost, "/api/v1/roles", `{"name":"ventas"}`)
	wantCode(t, "B2 sin auditor", rec, http.StatusCreated)
}

func TestCommon_NilMWPanicsAtMount(t *testing.T) {
	cases := []struct {
		name  string
		mount func(c *apipublica.Cara, k apipublica.Common)
	}{
		{"MountAuth", func(c *apipublica.Cara, k apipublica.Common) {
			apipublica.MountAuth(c, k, apipublica.AuthDeps{Verifier: &verifierFake{}})
		}},
		{"MountRolePlane", func(c *apipublica.Cara, k apipublica.Common) {
			apipublica.MountRolePlane(c, k, apipublica.RolePlaneDeps{Roles: &roleAdminFake{}})
		}},
		{"MountAudit", func(c *apipublica.Cara, k apipublica.Common) {
			apipublica.MountAudit(c, k, apipublica.AuditDeps{Audit: &auditReaderFake{}})
		}},
		{"MountEntitlements", func(c *apipublica.Cara, k apipublica.Common) {
			apipublica.MountEntitlements(c, k, apipublica.EntitlementsDeps{Entitlements: entitlementshelpertest.NewFake()})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := recuperar(func() { tc.mount(apipublica.Nueva(), apipublica.Common{}) })
			if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), tc.name) {
				t.Errorf("%s con MW nil: panic = %v; quiero un panic de cableado al montar que nombre %s", tc.name, v, tc.name)
			}
		})
	}
}
