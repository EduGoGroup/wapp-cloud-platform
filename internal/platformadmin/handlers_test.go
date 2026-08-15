package platformadmin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"
)

const testPlatformTenantID = "55550000-0000-0000-0000-000000000055"

func setupAdminMux(db *sql.DB) *http.ServeMux {
	repo := platformadmin.NewRepository(db)
	codes := enroll.NewPostgresCodeStore(db)
	mux := http.NewServeMux()
	mux.Handle("GET /admin/tenants", platformadmin.ListTenantsHandler(repo, testPlatformTenantID))
	mux.Handle("POST /admin/tenants", platformadmin.CreateTenantHandler(repo, testPlatformTenantID))
	mux.Handle("GET /admin/tenants/{id}", platformadmin.GetTenantHandler(repo, testPlatformTenantID))
	mux.Handle("GET /admin/tenants/{id}/installations", platformadmin.ListInstallationsHandler(repo, testPlatformTenantID))
	mux.Handle("POST /admin/tenants/{id}/enrollment-codes", platformadmin.IssueEnrollmentCodeHandler(repo, codes, testPlatformTenantID))
	return mux
}

func reqWithIdentity(req *http.Request, tenantID string) *http.Request {
	if tenantID == "" {
		return req
	}
	id := httpapi.Identity{
		TenantID: tenantID,
		Subject:  "operator-test",
		Roles:    []string{"platform_admin"},
	}
	return req.WithContext(httpapi.WithIdentity(req.Context(), id))
}

// A-07a: EnforcePlatformCaller (401 sin token / 403 con tenant ajeno) corta
// ANTES de tocar el repositorio -- los handlers de este mux nunca llegan a
// r.db para ninguno de los dos casos que este test comprueba. openTestDB(t)
// hacía t.Skipf sin WAPP_TEST_DB_DSN y dejaba la cerca de plataforma SIN
// cubrir en cualquier corrida local sin Postgres (go test ./... daba verde
// sin haberla mirado). setupAdminMux(nil) ejercita la MISMA cerca sin BD.
func TestHandlers_EnforcePlatformCaller_Gates(t *testing.T) {
	t.Parallel()
	mux := setupAdminMux(nil)

	paths := []string{
		"/admin/tenants",
		"/admin/tenants/" + uuid.NewString(),
		"/admin/tenants/" + uuid.NewString() + "/installations",
	}

	for _, p := range paths {
		// 1) Sin autenticación -> 401
		reqNoAuth := httptest.NewRequest(http.MethodGet, p, nil)
		recNoAuth := httptest.NewRecorder()
		mux.ServeHTTP(recNoAuth, reqNoAuth)
		if recNoAuth.Code != http.StatusUnauthorized {
			t.Errorf("path %s sin auth: status=%d, want 401", p, recNoAuth.Code)
		}

		// 2) Con tenant de cliente (no plataforma) -> 403
		reqClientTenant := reqWithIdentity(httptest.NewRequest(http.MethodGet, p, nil), uuid.NewString())
		recClientTenant := httptest.NewRecorder()
		mux.ServeHTTP(recClientTenant, reqClientTenant)
		if recClientTenant.Code != http.StatusForbidden {
			t.Errorf("path %s con tenant ajeno: status=%d, want 403", p, recClientTenant.Code)
		}
	}
}

const (
	adminChainSecret = "material-de-firma-hs256-para-el-cerrojo-de-plataforma"
	adminChainIssuer = "wapp-iam-test"
)

// grantsPlatformTenantAdmin reproduce el rol canónico tenant_admin tras la
// migración 0059: su '*' de siempre (0015) más el deny '*.any' que ADR-0039
// añadió para el plano de plataforma. Mismos grants que
// internal/platform/httpapi/adminroute_test.go:grantsTenantAdmin -- se
// re-declaran aquí (no se importan, httpapi_test es un paquete de test
// interno) porque este fichero prueba una capa distinta: QUE ESTE HANDLER
// CONCRETO de platformadmin está detrás de RequirePermission, no solo que el
// permiso EN ABSTRACTO deniegue al comodín.
var grantsPlatformTenantAdmin = identityrbac.Grants{
	Allow: []string{"*"},
	Deny:  []string{"*.any"},
}

// TestHandlers_RealTokenChain_AuthenticateAndRequirePermission monta la
// MISMA cadena que bootstrap.go arma en producción para cada ruta de
// plataforma (Authenticate → RequirePermission(perm) → handler; ver
// adminHandler y registerAdminRoutes en internal/bootstrap/bootstrap.go) con
// tokens FIRMADOS de verdad -- no httpapi.WithIdentity inyectado a mano
// (T1.3, «al estilo de adminroute_test.go»). A-07a: sin este test, ningún
// test de este paquete ejercitaba Authenticate ni RequirePermission sobre
// las rutas nuevas -- TestHandlers_EnforcePlatformCaller_Gates prueba
// EnforcePlatformCaller (identidad == tenant de plataforma), no el RBAC
// (grant == permiso de la ruta), y los tests HTTP de más abajo inyectan la
// Identity directamente en el contexto, saltándose el middleware entero.
//
// El caso 403 con grantsPlatformTenantAdmin es TAMBIÉN el criterio (6) de
// T3.4 para las tres rutas de solicitudes de acceso («tenant_admin ⇒ 403 en
// las tres»): reqWithIdentity (arriba) siembra siempre platform_admin y
// nunca pasa por RequirePermission, así que ningún test HTTP existente podía
// distinguir "el operador es de plataforma" de "el operador tiene el grant".
func TestHandlers_RealTokenChain_AuthenticateAndRequirePermission(t *testing.T) {
	t.Parallel()

	repo := platformadmin.NewRepository(nil)
	codes := enroll.NewPostgresCodeStore(nil)
	jwt := sharedjwt.NewJWTManager(adminChainSecret, adminChainIssuer)
	mw := httpapi.NewMiddleware(jwt, nil)

	// Mismo par patrón→permiso→handler que registerAdminRoutes construye para
	// las rutas de plataforma (Plan 056 · T1.3/T3.4). Ninguno de estos
	// handlers llega al repo en los dos casos de abajo (401/403 cortan
	// antes), así que un *Repository sobre db=nil es un valor válido.
	routes := []struct {
		name    string
		perm    string
		handler http.Handler
	}{
		{"ListTenants", "tenants.read.any", platformadmin.ListTenantsHandler(repo, testPlatformTenantID)},
		{"CreateTenant", "tenants.create.any", platformadmin.CreateTenantHandler(repo, testPlatformTenantID)},
		{"GetTenant", "tenants.read.any", platformadmin.GetTenantHandler(repo, testPlatformTenantID)},
		{"ListInstallations", "fleet.read.any", platformadmin.ListInstallationsHandler(repo, testPlatformTenantID)},
		{"IssueEnrollmentCode", "enrollment.issue.any", platformadmin.IssueEnrollmentCodeHandler(repo, codes, testPlatformTenantID)},
		{"ListAccessRequests", "users.provision.any", platformadmin.ListAccessRequestsHandler(repo, testPlatformTenantID)},
		{"ApproveAccessRequest", "users.provision.any", platformadmin.ApproveAccessRequestHandler(repo, &fakeM2MClient{}, testPlatformTenantID)},
		{"RejectAccessRequest", "users.provision.any", platformadmin.RejectAccessRequestHandler(repo, testPlatformTenantID)},
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			chain := mw.Authenticate(mw.RequirePermission(rt.perm)(rt.handler))

			// 1) Sin token -> 401 (Authenticate corta antes de RequirePermission).
			recNoTok := httptest.NewRecorder()
			chain.ServeHTTP(recNoTok, httptest.NewRequest(http.MethodGet, "/x", nil))
			if recNoTok.Code != http.StatusUnauthorized {
				t.Errorf("sin token: status = %d, want 401", recNoTok.Code)
			}

			// 2) Token FIRMADO de un tenant_admin (comodín '*' + deny '*.any')
			// -> 403 (RequirePermission corta antes de llegar al handler real:
			// no hace falta BD, y es justo el escenario que ADR-0039 existe
			// para impedir -- ver adminroute_test.go). El TenantID del token
			// es EL DE PLATAFORMA a propósito: si fuera un tenant cualquiera,
			// el EnforcePlatformCaller de DENTRO del handler ya daría 403 por
			// su cuenta y el test dejaría de probar RequirePermission (lo
			// confirmó una mutación: con RequirePermission neutralizado y un
			// tenant ajeno, este caso seguía en verde por la razón equivocada).
			tok, _, err := jwt.GenerateToken(uuid.NewString(), testPlatformTenantID, []string{"tenant_admin"}, grantsPlatformTenantAdmin, time.Hour)
			if err != nil {
				t.Fatalf("GenerateToken: %v", err)
			}
			reqDenied := httptest.NewRequest(http.MethodGet, "/x", nil)
			reqDenied.Header.Set("Authorization", "Bearer "+tok)
			recDenied := httptest.NewRecorder()
			chain.ServeHTTP(recDenied, reqDenied)
			if recDenied.Code != http.StatusForbidden {
				t.Errorf("token tenant_admin (comodín '*' + deny '*.any') para %q: status = %d, want 403", rt.perm, recDenied.Code)
			}
		})
	}
}

func TestHandlers_ListTenants_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mux := setupAdminMux(db)
	repo := platformadmin.NewRepository(db)

	slug := fmt.Sprintf("pa-h-list-%d", time.Now().UnixNano())
	_, err := repo.CreateTenant(context.Background(), slug, "HTTP List Test", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	req := reqWithIdentity(httptest.NewRequest(http.MethodGet, "/admin/tenants?limit=10&offset=0", nil), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}

	var res platformadmin.ListTenantsResponse
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	if res.Limit != 10 || res.Offset != 0 {
		t.Fatalf("limit/offset inesperados: limit=%d offset=%d", res.Limit, res.Offset)
	}
	if len(res.Items) == 0 {
		t.Fatal("esperados items en ListTenantsResponse")
	}
}

func TestHandlers_GetTenant_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mux := setupAdminMux(db)
	repo := platformadmin.NewRepository(db)

	slug := fmt.Sprintf("pa-h-get-%d", time.Now().UnixNano())
	created, err := repo.CreateTenant(context.Background(), slug, "HTTP Get Test", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// 200 OK para tenant existente
	req := reqWithIdentity(httptest.NewRequest(http.MethodGet, "/admin/tenants/"+created.ID, nil), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}

	var detail platformadmin.TenantDetail
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if detail.ID != created.ID || detail.Slug != slug {
		t.Fatalf("detalle inesperado: %+v", detail)
	}

	// 404 para tenant inexistente
	req404 := reqWithIdentity(httptest.NewRequest(http.MethodGet, "/admin/tenants/"+uuid.NewString(), nil), testPlatformTenantID)
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec404.Code)
	}
}

func TestHandlers_ListInstallations_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mux := setupAdminMux(db)
	repo := platformadmin.NewRepository(db)

	slug := fmt.Sprintf("pa-h-inst-%d", time.Now().UnixNano())
	created, err := repo.CreateTenant(context.Background(), slug, "HTTP Inst Test", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// 200 OK con lista de instalaciones
	req := reqWithIdentity(httptest.NewRequest(http.MethodGet, "/admin/tenants/"+created.ID+"/installations", nil), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}

	var res platformadmin.ListInstallationsResponse
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	// 404 para tenant inexistente
	req404 := reqWithIdentity(httptest.NewRequest(http.MethodGet, "/admin/tenants/"+uuid.NewString()+"/installations", nil), testPlatformTenantID)
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec404.Code)
	}
}

func TestHandlers_CreateTenant_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mux := setupAdminMux(db)

	slug := fmt.Sprintf("pa-h-create-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"slug":"%s","display_name":"Created via HTTP"}`, slug)

	// 201 Created
	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(body)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. Body: %s", rec.Code, rec.Body.String())
	}

	var created platformadmin.CreatedTenant
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if created.ID == "" || created.Slug != slug {
		t.Fatalf("created inesperado: %+v", created)
	}

	// 409 Conflict si se repite el slug
	reqDup := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(body)), testPlatformTenantID)
	recDup := httptest.NewRecorder()
	mux.ServeHTTP(recDup, reqDup)

	if recDup.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recDup.Code)
	}

	// 400 Bad Request si faltan campos obligatorios
	badBody := `{"slug":"","display_name":""}`
	reqBad := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(badBody)), testPlatformTenantID)
	recBad := httptest.NewRecorder()
	mux.ServeHTTP(recBad, reqBad)

	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recBad.Code)
	}
}

func TestHandlers_IssueEnrollmentCode_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mux := setupAdminMux(db)
	repo := platformadmin.NewRepository(db)
	codeStore := enroll.NewPostgresCodeStore(db)

	slug := fmt.Sprintf("pa-h-code-%d", time.Now().UnixNano())
	created, err := repo.CreateTenant(context.Background(), slug, "HTTP Code Test", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// 201 Created con código emitido
	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/tenants/"+created.ID+"/enrollment-codes", strings.NewReader(`{"ttl":3600}`)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. Body: %s", rec.Code, rec.Body.String())
	}

	var res platformadmin.IssueEnrollmentCodeResponse
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !strings.HasPrefix(res.Code, "WAPP-") {
		t.Fatalf("código inválido: %q (esperado prefijo WAPP-)", res.Code)
	}
	if res.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expires_at en el pasado: %v", res.ExpiresAt)
	}

	// Criterio T2.3: el código se canjea con éxito desde el store y solo una vez
	consumedTenant, err := codeStore.Consume(context.Background(), res.Code)
	if err != nil {
		t.Fatalf("codeStore.Consume fallo en primer intento: %v", err)
	}
	if consumedTenant != created.ID {
		t.Fatalf("consumedTenant = %q, want %q", consumedTenant, created.ID)
	}

	// Segundo intento de consumo debe fallar (un solo uso)
	_, errSecond := codeStore.Consume(context.Background(), res.Code)
	if !errors.Is(errSecond, enroll.ErrCodeInvalid) {
		t.Fatalf("segundo consumo err = %v, want ErrCodeInvalid", errSecond)
	}

	// 404 para tenant inexistente
	req404 := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/tenants/"+uuid.NewString()+"/enrollment-codes", nil), testPlatformTenantID)
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec404.Code)
	}
}
