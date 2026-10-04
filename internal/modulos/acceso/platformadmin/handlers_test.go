package platformadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
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

const (
	listTenantsPattern   = "GET /admin/tenants"
	getTenantPattern     = "GET /admin/tenants/{id}"
	installationsPattern = "GET /admin/tenants/{id}/installations"
	createTenantPattern  = "POST /admin/tenants"
	enrollmentPattern    = "POST /admin/tenants/{id}/enrollment-codes"
)

// fakeIssuer es el doble de CodeIssuer: registra lo que se le pide persistir.
type fakeIssuer struct {
	mu    sync.Mutex
	err   error
	calls []issuedCode
}

type issuedCode struct {
	code, tenantID string
	expiresAt      time.Time
}

var _ platformadmin.CodeIssuer = (*fakeIssuer)(nil)

func (f *fakeIssuer) Create(_ context.Context, code, tenantID string, expiresAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, issuedCode{code, tenantID, expiresAt})
	return f.err
}

// R-A1: los cinco handlers de empresas cortan con EnforcePlatformCaller ANTES de tocar el almacén
// (y el emisor de códigos): 401 sin identidad, 403 con un tenant que no es el de plataforma.
func TestTenantHandlers_PlatformFence_BeforeTheStore(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	issuer := &fakeIssuer{err: errStoreTouched}
	id := uuid.NewString()
	for _, h := range []struct {
		name, pattern, method, path string
		h                           http.Handler
	}{
		{"ListTenants", listTenantsPattern, http.MethodGet, "/admin/tenants", platformadmin.ListTenantsHandler(b.f, platformTenant)},
		{"GetTenant", getTenantPattern, http.MethodGet, "/admin/tenants/" + id, platformadmin.GetTenantHandler(b.f, platformTenant)},
		{"ListInstallations", installationsPattern, http.MethodGet, "/admin/tenants/" + id + "/installations",
			platformadmin.ListInstallationsHandler(b.f, platformTenant)},
		{"CreateTenant", createTenantPattern, http.MethodPost, "/admin/tenants", platformadmin.CreateTenantHandler(b.f, platformTenant)},
		{"IssueEnrollmentCode", enrollmentPattern, http.MethodPost, "/admin/tenants/" + id + "/enrollment-codes",
			platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)},
	} {
		for _, c := range []struct {
			name, tenant string
			code         int
		}{
			{"NoIdentity", "", http.StatusUnauthorized},
			{"OtherTenant", b.tenantA, http.StatusForbidden},
		} {
			t.Run(h.name+"_"+c.name, func(t *testing.T) {
				r := asCaller(httptest.NewRequest(h.method, h.path, strings.NewReader(`{"slug":"s","display_name":"d"}`)), c.tenant)
				rec, _ := serve(t, h.pattern, h.h, r)
				if rec.Code != c.code {
					t.Fatalf("status = %d, quiero %d (y sin tocar el almacén)", rec.Code, c.code)
				}
			})
		}
	}
	if len(issuer.calls) != 0 {
		t.Fatal("la cerca de plataforma dejó emitir un código")
	}
}

// R-A2: en los tres handlers con {id}, uno que no es UUID es 404 «empresa no encontrada» y uno
// vacío es 400 «id de empresa requerido», sin consultar.
func TestTenantHandlers_TenantID_EmptyIs400_NotUUIDIs404(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	issuer := &fakeIssuer{err: errStoreTouched}
	for _, c := range []struct {
		name, pattern, method, path string
		h                           http.Handler
	}{
		{"GetTenant", getTenantPattern, http.MethodGet, "/admin/tenants/no-es-un-uuid", platformadmin.GetTenantHandler(b.f, platformTenant)},
		{"ListInstallations", installationsPattern, http.MethodGet, "/admin/tenants/no-es-un-uuid/installations",
			platformadmin.ListInstallationsHandler(b.f, platformTenant)},
		{"IssueEnrollmentCode", enrollmentPattern, http.MethodPost, "/admin/tenants/no-es-un-uuid/enrollment-codes",
			platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)},
	} {
		t.Run(c.name+"_NotUUID", func(t *testing.T) {
			rec, _ := serve(t, c.pattern, c.h, asCaller(httptest.NewRequest(c.method, c.path, nil), platformTenant))
			wantText(t, rec, http.StatusNotFound, "empresa no encontrada")
		})
		t.Run(c.name+"_Empty", func(t *testing.T) {
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, asCaller(httptest.NewRequest(c.method, "/", nil), platformTenant))
			wantText(t, rec, http.StatusBadRequest, "id de empresa requerido")
		})
	}
}

// ListTenantsHandler pasa limit/offset al almacén y responde los efectivos, acotados.
func TestListTenantsHandler_Paging(t *testing.T) {
	b := newInbox(t) // dos empresas: A (más vieja) y B
	h := platformadmin.ListTenantsHandler(b.f, platformTenant)
	get := func(t *testing.T, query string) platformadmin.ListTenantsResponse {
		t.Helper()
		rec, _ := serve(t, listTenantsPattern, h, asCaller(httptest.NewRequest(http.MethodGet, "/admin/tenants"+query, nil), platformTenant))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("respuesta = %d %s", rec.Code, rec.Body.String())
		}
		var resp platformadmin.ListTenantsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("json: %v", err)
		}
		return resp
	}
	for _, c := range []struct {
		query                string
		items, limit, offset int
	}{
		{"", 2, 50, 0},
		{"?limit=1&offset=1", 1, 1, 1},
		{"?limit=abc&offset=xyz", 2, 50, 0},
		{"?limit=0&offset=-5", 2, 50, 0},
		{"?limit=9999", 2, 500, 0},
		{"?offset=7", 0, 50, 7},
	} {
		t.Run(c.query, func(t *testing.T) {
			resp := get(t, c.query)
			if len(resp.Items) != c.items || resp.Limit != c.limit || resp.Offset != c.offset {
				t.Fatalf("items %d limit %d offset %d; quiero %d, %d, %d", len(resp.Items), resp.Limit, resp.Offset, c.items, c.limit, c.offset)
			}
		})
	}
	if resp := get(t, "?limit=1&offset=1"); resp.Items[0].ID != b.tenantA {
		t.Fatalf("la segunda página de una es %s, quiero la empresa más vieja %s", resp.Items[0].ID, b.tenantA)
	}
	if rec, _ := serve(t, listTenantsPattern, h, asCaller(httptest.NewRequest(http.MethodGet, "/admin/tenants?offset=7", nil), platformTenant)); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("una página vacía es [] y no null: %s", rec.Body.String())
	}
	b.f.Fail("ListTenants", errors.New("bd caída"))
	rec, _ := serve(t, listTenantsPattern, h, asCaller(httptest.NewRequest(http.MethodGet, "/admin/tenants", nil), platformTenant))
	wantText(t, rec, http.StatusInternalServerError, "error al listar empresas")
}

// GetTenantHandler: 200 con el detalle, 404 si no existe, 500 si falla; el {id} es el tenant
// objetivo auditado.
func TestGetTenantHandler(t *testing.T) {
	b := newInbox(t)
	b.f.SetFeatures(b.tenantA, "menu", "cart_basic")
	h := platformadmin.GetTenantHandler(b.f, platformTenant)
	get := func(id string) (*httptest.ResponseRecorder, *auditCapture) {
		return serve(t, getTenantPattern, h, asCaller(httptest.NewRequest(http.MethodGet, "/admin/tenants/"+id, nil), platformTenant))
	}
	rec, audit := get(b.tenantA)
	var d platformadmin.TenantDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("respuesta = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	if d.ID != b.tenantA || len(d.Features) != 2 || d.Features[0] != "cart_basic" {
		t.Fatalf("detalle = %+v", d)
	}
	if got := audit.target(t); got != b.tenantA {
		t.Fatalf("tenant objetivo = %q, quiero %q", got, b.tenantA)
	}
	missing := uuid.NewString()
	rec, audit = get(missing)
	wantText(t, rec, http.StatusNotFound, "empresa no encontrada")
	if got := audit.target(t); got != missing {
		t.Fatalf("tenant objetivo = %q, quiero %q (se publica antes de consultar)", got, missing)
	}
	b.f.Fail("GetTenant", errors.New("bd caída"))
	rec, _ = get(b.tenantA)
	wantText(t, rec, http.StatusInternalServerError, "error al leer empresa")
}

// ListInstallationsHandler: comprueba la empresa con ExistsTenant (no con GetTenant) y lista.
func TestListInstallationsHandler(t *testing.T) {
	b := newInbox(t)
	seen := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	b.f.SeedFleetSession(t, b.tenantA, "edge-1", "s1", &seen)
	h := platformadmin.ListInstallationsHandler(b.f, platformTenant)
	get := func(id string) (*httptest.ResponseRecorder, *auditCapture) {
		path := "/admin/tenants/" + id + "/installations"
		return serve(t, installationsPattern, h, asCaller(httptest.NewRequest(http.MethodGet, path, nil), platformTenant))
	}
	b.f.Fail("GetTenant", errStoreTouched) // la consulta ligera, no el detalle
	rec, audit := get(b.tenantA)
	wantJSON(t, rec, http.StatusOK, `{"items":[{"edge_id":"edge-1","sessions":1,"last_seen_at":"2026-10-04T12:00:00Z","lease_revoked":false}]}`)
	if got := audit.target(t); got != b.tenantA {
		t.Fatalf("tenant objetivo = %q, quiero %q", got, b.tenantA)
	}
	rec, _ = get(b.tenantB)
	wantJSON(t, rec, http.StatusOK, `{"items":[]}`)
	rec, _ = get(uuid.NewString())
	wantText(t, rec, http.StatusNotFound, "empresa no encontrada")
	b.f.Fail("ListInstallations", errors.New("bd caída"))
	rec, _ = get(b.tenantA)
	wantText(t, rec, http.StatusInternalServerError, "error al listar instalaciones")
	b.f.Fail("ExistsTenant", errors.New("bd caída"))
	rec, _ = get(b.tenantA)
	wantText(t, rec, http.StatusInternalServerError, "error al verificar empresa")
}

// CreateTenantHandler: validación del cuerpo antes del almacén y cada desenlace con su texto.
func TestCreateTenantHandler(t *testing.T) {
	post := func(t *testing.T, b inbox, body string) (*httptest.ResponseRecorder, *auditCapture) {
		t.Helper()
		r := asCaller(httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(body)), platformTenant)
		return serve(t, createTenantPattern, platformadmin.CreateTenantHandler(b.f, platformTenant), r)
	}
	t.Run("Created_201", func(t *testing.T) {
		b := newInbox(t)
		rec, audit := post(t, b, `{"slug":"nueva","display_name":"Nueva","plan_id":"pro"}`)
		var created platformadmin.CreatedTenant
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated || created.Slug != "nueva" {
			t.Fatalf("respuesta = %d %s (%v)", rec.Code, rec.Body.String(), err)
		}
		d, err := b.f.GetTenant(context.Background(), created.ID)
		if err != nil || d.DisplayName != "Nueva" || d.PlanID == nil || *d.PlanID != "pro" {
			t.Fatalf("empresa creada = %+v, %v", d, err)
		}
		if got := audit.target(t); got != created.ID {
			t.Fatalf("tenant objetivo = %q, quiero el id nuevo %q", got, created.ID)
		}
	})
	t.Run("InvalidBody_400_BeforeTheStore", func(t *testing.T) {
		b := newInbox(t)
		failAll(b.f)
		for _, c := range []struct{ body, text string }{
			{`{`, "cuerpo JSON inválido"},
			{`{"display_name":"X"}`, "slug y display_name son requeridos"},
			{`{"slug":"x"}`, "slug y display_name son requeridos"},
		} {
			rec, audit := post(t, b, c.body)
			wantText(t, rec, http.StatusBadRequest, c.text)
			if got := audit.target(t); got != "" {
				t.Fatalf("un alta rechazada no publica tenant objetivo: %q", got)
			}
		}
	})
	t.Run("DuplicateSlug_409", func(t *testing.T) {
		b := newInbox(t)
		rec, _ := post(t, b, `{"slug":"empresa-a","display_name":"Otra"}`)
		wantText(t, rec, http.StatusConflict, "el slug ya existe")
	})
	t.Run("StoreInvalidInput_400", func(t *testing.T) {
		b := newInbox(t)
		b.f.Fail("CreateTenant", platformadmin.ErrInvalidInput)
		rec, _ := post(t, b, `{"slug":"x","display_name":"X"}`)
		wantText(t, rec, http.StatusBadRequest, "entrada inválida")
	})
	t.Run("StoreFails_500", func(t *testing.T) {
		b := newInbox(t)
		b.f.Fail("CreateTenant", errors.New("bd caída"))
		rec, _ := post(t, b, `{"slug":"x","display_name":"X"}`)
		wantText(t, rec, http.StatusInternalServerError, "error al crear empresa")
	})
}

// codeFormat es el formato de un código de enrolamiento: "WAPP-" + 10 bytes en hex (R-A4).
var codeFormat = regexp.MustCompile(`^WAPP-[0-9a-f]{20}$`)

// R-A4: el TTL (por defecto, del cuerpo, acotado) llega a expires_at, y el código que se persiste
// es el que se responde.
func TestIssueEnrollmentCodeHandler_TTL(t *testing.T) {
	const day = 86400
	for _, c := range []struct {
		name string
		body string
		ttl  int
	}{
		{"NoBody_DefaultDay", "", day},
		{"BodyTTL", `{"ttl":3600}`, 3600},
		{"BelowMinimum_60", `{"ttl":30}`, 60},
		{"AboveMaximum_30Days", `{"ttl":99999999}`, 30 * day},
		{"ZeroTTL_Default", `{"ttl":0}`, day},
		{"NegativeTTL_Default", `{"ttl":-5}`, day},
		{"NotJSON_Default", `{`, day},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newInbox(t)
			issuer := &fakeIssuer{}
			h := platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)
			path := "/admin/tenants/" + b.tenantA + "/enrollment-codes"
			r := httptest.NewRequest(http.MethodPost, path, nil)
			if c.body != "" {
				r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body))
			}
			before := time.Now().UTC()
			rec, audit := serve(t, enrollmentPattern, h, asCaller(r, platformTenant))
			after := time.Now().UTC()
			var resp platformadmin.IssueEnrollmentCodeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusCreated {
				t.Fatalf("respuesta = %d %s (%v)", rec.Code, rec.Body.String(), err)
			}
			ttl := time.Duration(c.ttl) * time.Second
			if resp.ExpiresAt.Before(before.Add(ttl)) || resp.ExpiresAt.After(after.Add(ttl)) {
				t.Fatalf("expires_at = %v, quiero entre %v y %v (ahora + %v)", resp.ExpiresAt, before.Add(ttl), after.Add(ttl), ttl)
			}
			if len(issuer.calls) == 1 && issuer.calls[0].expiresAt.Location() != time.UTC {
				t.Fatalf("expires_at persistido = %v (%v), quiero UTC", issuer.calls[0].expiresAt, issuer.calls[0].expiresAt.Location())
			}
			if !codeFormat.MatchString(resp.Code) {
				t.Fatalf("código = %q, quiero WAPP- y 20 hex en minúsculas", resp.Code)
			}
			if len(issuer.calls) != 1 || issuer.calls[0].code != resp.Code || issuer.calls[0].tenantID != b.tenantA ||
				!issuer.calls[0].expiresAt.Equal(resp.ExpiresAt) {
				t.Fatalf("persistido %+v, quiero UNA vez el código y el vencimiento respondidos para %s", issuer.calls, b.tenantA)
			}
			if got := audit.target(t); got != b.tenantA {
				t.Fatalf("tenant objetivo = %q, quiero %q", got, b.tenantA)
			}
		})
	}
}

// Los códigos son aleatorios: dos emisiones no repiten.
func TestIssueEnrollmentCodeHandler_CodesDiffer(t *testing.T) {
	b := newInbox(t)
	issuer := &fakeIssuer{}
	h := platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/admin/tenants/"+b.tenantA+"/enrollment-codes", nil)
		if rec, _ := serve(t, enrollmentPattern, h, asCaller(r, platformTenant)); rec.Code != http.StatusCreated {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if len(issuer.calls) != 2 || issuer.calls[0].code == issuer.calls[1].code {
		t.Fatalf("códigos = %+v, quiero dos distintos", issuer.calls)
	}
}

// La empresa se comprueba antes de emitir, y un fallo del emisor es un 500 con su texto.
func TestIssueEnrollmentCodeHandler_Errors(t *testing.T) {
	post := func(t *testing.T, b inbox, issuer *fakeIssuer, id string) *httptest.ResponseRecorder {
		t.Helper()
		h := platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)
		r := httptest.NewRequest(http.MethodPost, "/admin/tenants/"+id+"/enrollment-codes", nil)
		rec, _ := serve(t, enrollmentPattern, h, asCaller(r, platformTenant))
		return rec
	}
	t.Run("MissingTenant_404_NothingIssued", func(t *testing.T) {
		b := newInbox(t)
		issuer := &fakeIssuer{}
		wantText(t, post(t, b, issuer, uuid.NewString()), http.StatusNotFound, "empresa no encontrada")
		if len(issuer.calls) != 0 {
			t.Fatal("se emitió un código para una empresa que no existe")
		}
	})
	t.Run("ExistsFails_500", func(t *testing.T) {
		b := newInbox(t)
		b.f.Fail("ExistsTenant", errors.New("bd caída"))
		wantText(t, post(t, b, &fakeIssuer{}, b.tenantA), http.StatusInternalServerError, "error al verificar empresa")
	})
	t.Run("IssuerFails_500", func(t *testing.T) {
		b := newInbox(t)
		wantText(t, post(t, b, &fakeIssuer{err: errors.New("bd caída")}, b.tenantA), http.StatusInternalServerError,
			"error al persistir código de enrolamiento")
	})
}

// Los cuerpos de la consola se leen y se escriben con sus nombres de campo.
func TestTenantHandlerDTOs_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		v    any
		want string
	}{
		{"ListTenantsResponse", platformadmin.ListTenantsResponse{Items: []platformadmin.TenantListItem{}, Limit: 50, Offset: 0},
			`{"items":[],"limit":50,"offset":0}`},
		{"ListInstallationsResponse", platformadmin.ListInstallationsResponse{Items: []platformadmin.InstallationItem{}}, `{"items":[]}`},
		{"IssueEnrollmentCodeResponse", platformadmin.IssueEnrollmentCodeResponse{Code: "WAPP-x", ExpiresAt: at},
			`{"code":"WAPP-x","expires_at":"2026-10-04T12:00:00Z"}`},
		{"IssueEnrollmentCodeRequest_OmitsZero", platformadmin.IssueEnrollmentCodeRequest{}, `{}`},
		{"CreateTenantRequest_OmitsNilPlan", platformadmin.CreateTenantRequest{Slug: "s", DisplayName: "D"}, `{"slug":"s","display_name":"D"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.v)
			if err != nil || string(got) != c.want {
				t.Fatalf("JSON = %s (%v), quiero %s", got, err, c.want)
			}
		})
	}
}
