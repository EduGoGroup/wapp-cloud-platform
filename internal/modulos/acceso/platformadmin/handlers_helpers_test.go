package platformadmin_test

// Parte de handlers_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): los helpers compartidos de los tests de handlers (bandeja, llamante, auditoría, respuestas).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin/platformadminhelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Este test es EXTERNO (package platformadmin_test): usa platformadminhelpertest.Fake, que importa
// platformadmin. Aquí viven también las ayudas comunes a los tests del paquete (hallazgo 20: el
// primer test que pasó a verde): la bandeja de pruebas, la identidad de plataforma, el captador de
// auditoría y las aserciones de respuesta.

// ── ayudas comunes ───────────────────────────────────────────────────────────────────────────

// platformTenant es el tenant del plano de plataforma con el que se construyen los handlers.
const platformTenant = "55550000-0000-0000-0000-000000000055"

// operatorSubject es el sujeto (UUID) del operador de plataforma de los tests: se guarda como
// decided_by.
const operatorSubject = "0b5c6f40-7d1e-4a3b-9c2d-1e2f3a4b5c6d"

// errStoreTouched es el error con el que failAll hace fallar todo el almacén: un handler o una
// regla que debía cortar ANTES de tocarlo y no lo hace acaba con este error, no con el esperado.
var errStoreTouched = errors.New("el almacén no debía tocarse")

// storeMethods son los doce métodos de los dos puertos (los nombres que entiende Fake.Fail).
var storeMethods = []string{
	"ListTenants", "GetTenant", "ExistsTenant", "CreateTenant", "ListInstallations",
	"ListAccessRequests", "CreateAccessRequest", "RejectAccessRequest", "LookupAccessRequestStatus",
	"ResolveRoleID", "CheckRetryApproved", "ExecuteApprovalTx",
}

// failAll hace fallar con errStoreTouched todos los métodos de los dos puertos del Fake.
func failAll(f *platformadminhelpertest.Fake) {
	for _, m := range storeMethods {
		f.Fail(m, errStoreTouched)
	}
}

// Roles del Fake: los ids y nombres de las plantillas globales de la migración 0015.
var (
	roleOperator = platformadminhelpertest.Role{ID: "10000000-0000-0000-0000-000000000002", Name: "operator"}
	roleViewer   = platformadminhelpertest.Role{ID: "10000000-0000-0000-0000-000000000003", Name: "viewer"}
)

// inbox es un Fake con los dos roles y dos empresas.
type inbox struct {
	f                *platformadminhelpertest.Fake
	tenantA, tenantB string
}

func newInbox(t *testing.T) inbox {
	t.Helper()
	f := platformadminhelpertest.NewFake()
	f.AddRole(roleOperator.ID, roleOperator.Name)
	f.AddRole(roleViewer.ID, roleViewer.Name)
	return inbox{f: f, tenantA: mustTenant(t, f, "empresa-a"), tenantB: mustTenant(t, f, "empresa-b")}
}

func mustTenant(t *testing.T, f *platformadminhelpertest.Fake, slug string) string {
	t.Helper()
	created, err := f.CreateTenant(context.Background(), slug, "Empresa "+slug, nil)
	if err != nil {
		t.Fatalf("CreateTenant(%s): %v", slug, err)
	}
	return created.ID
}

// asCaller pone en la petición la identidad de un llamante con ese tenant (sin identidad si
// tenantID es "") y el sujeto operatorSubject.
func asCaller(r *http.Request, tenantID string) *http.Request {
	if tenantID == "" {
		return r
	}
	id := httpapi.Identity{TenantID: tenantID, Subject: operatorSubject, Roles: []string{"platform_admin"}}
	return r.WithContext(httpapi.WithIdentity(r.Context(), id))
}

// auditCapture registra lo que AuditMiddleware audita: es como se observa el tenant objetivo que
// un handler publica con httpapi.SetAuditTargetTenant.
type auditCapture struct {
	mu     sync.Mutex
	inputs []httpapi.AuditInput
}

func (a *auditCapture) Record(_ context.Context, in httpapi.AuditInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inputs = append(a.inputs, in)
	return nil
}

// target devuelve el tenant objetivo del único evento auditado ("" si no se publicó).
func (a *auditCapture) target(t *testing.T) string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.inputs) != 1 {
		t.Fatalf("eventos auditados = %d, quiero 1", len(a.inputs))
	}
	raw, ok := a.inputs[0].Meta["target_tenant_id"]
	if !ok {
		return ""
	}
	target, ok := raw.(string)
	if !ok {
		t.Fatalf("target_tenant_id auditado no es un string: %#v", raw)
	}
	return target
}

// serve monta h en un ServeMux con el patrón dado (para que {id} llegue por PathValue), detrás de
// AuditMiddleware, y le pasa la petición. Devuelve la respuesta y lo auditado.
func serve(t *testing.T, pattern string, h http.Handler, r *http.Request) (*httptest.ResponseRecorder, *auditCapture) {
	t.Helper()
	audit := &auditCapture{}
	mux := http.NewServeMux()
	mux.Handle(pattern, httpapi.AuditMiddleware(audit, "accion", "recurso", nil)(h))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec, audit
}

// wantText exige el código y el cuerpo de un http.Error: el texto literal más el salto de línea.
func wantText(t *testing.T, rec *httptest.ResponseRecorder, code int, text string) {
	t.Helper()
	if rec.Code != code || rec.Body.String() != text+"\n" {
		t.Fatalf("respuesta = %d %q, quiero %d %q", rec.Code, rec.Body.String(), code, text+"\n")
	}
}

// wantJSON exige el código, el Content-Type JSON y el cuerpo exacto (sin el salto final).
func wantJSON(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("respuesta = %d %s, quiero %d %s", rec.Code, rec.Body.String(), code, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, quiero application/json", ct)
	}
}
