package platformadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

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
