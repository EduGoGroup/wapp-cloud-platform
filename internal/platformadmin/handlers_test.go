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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
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

func TestHandlers_EnforcePlatformCaller_Gates(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	mux := setupAdminMux(db)

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
