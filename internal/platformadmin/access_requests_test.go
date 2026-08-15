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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	"github.com/google/uuid"
)

type fakeM2MClient struct {
	replacedSystems []string
	replaceErr      error
	calls           int
}

func (f *fakeM2MClient) EnsureUser(_ context.Context, email, firstName, lastName string) (domain.IdentityUser, error) {
	return domain.IdentityUser{ID: uuid.NewString(), Email: email, Created: true}, nil
}

func (f *fakeM2MClient) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	f.calls++
	f.replacedSystems = systems
	if f.replaceErr != nil {
		return domain.IdentitySystemsDiff{}, f.replaceErr
	}
	return domain.IdentitySystemsDiff{Systems: systems}, nil
}

func (f *fakeM2MClient) Signup(_ context.Context, email, password, firstName, lastName string) (string, error) {
	return uuid.NewString(), nil
}

func TestIntegration_AccessRequests_Lifecycle(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("req-%d@example.com", time.Now().UnixNano())

	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}

	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest (idempotente): %v", err)
	}

	items, err := repo.ListAccessRequests(ctx, "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	var found *platformadmin.AccessRequestItem
	for i := range items {
		if items[i].UserID == userID {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no se encontró solicitud para %s", userID)
	}
	// C-05 (lado servidor): sin lectura de systems en identity, la bandeja
	// siempre declara "no lo sé" -- nunca inventa ni un [] silencioso.
	if found.Systems == nil || len(found.Systems) != 0 {
		t.Fatalf("Systems esperado [], obtenido: %v", found.Systems)
	}
	if found.SystemsKnown {
		t.Fatal("SystemsKnown esperado false: identity no expone lectura puntual de systems")
	}

	slug := fmt.Sprintf("pa-ar-t-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "AR Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	fakeM2M := &fakeM2MClient{}
	operatorID := uuid.NewString()

	err = repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, fakeM2M)
	if err != nil {
		t.Fatalf("ApproveAccessRequest: %v", err)
	}

	// C-04: reaprobar la MISMA solicitud hacia el MISMO tenant converge -- ni
	// siquiera hace falta que identity fallase la primera vez: un doble clic
	// del operador no debe romper nada. El caso "identity falló y luego se
	// reintenta" tiene su propio test dedicado
	// (TestIntegration_ApproveAccessRequest_RetryConvergesAfterIdentityFailure).
	errReApprove := repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, fakeM2M)
	if errReApprove != nil {
		t.Fatalf("el reintento hacia el mismo tenant debía converger, obtenido: %v", errReApprove)
	}

	testCrossTenantConflict(ctx, t, repo, userID, email, operatorID, fakeM2M)
}

func testCrossTenantConflict(ctx context.Context, t *testing.T, repo *platformadmin.Repository, userID, email, operatorID string, fakeM2M *fakeM2MClient) {
	t.Helper()
	slug2 := fmt.Sprintf("pa-ar-t2-%d", time.Now().UnixNano())
	ten2, err := repo.CreateTenant(ctx, slug2, "AR Tenant 2", nil)
	if err != nil {
		t.Fatalf("CreateTenant 2: %v", err)
	}

	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest tras aprobación: %v", err)
	}
	items2, err := repo.ListAccessRequests(ctx, "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests 2: %v", err)
	}
	var found2 *platformadmin.AccessRequestItem
	for i := range items2 {
		if items2[i].UserID == userID {
			found2 = &items2[i]
			break
		}
	}
	if found2 == nil {
		t.Fatal("esperada nueva solicitud en pending")
	}

	errConflict := repo.ApproveAccessRequest(ctx, found2.ID, ten2.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, fakeM2M)
	if errConflict == nil {
		t.Fatal("esperado conflicto al aprobar usuario que ya es miembro de otro tenant")
	}

	if err := repo.RejectAccessRequest(ctx, found2.ID, "usuario ya asignado", operatorID); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
}

func TestHandlers_AccessRequests_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	fakeM2M := &fakeM2MClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /admin/access-requests", platformadmin.ListAccessRequestsHandler(repo, testPlatformTenantID))
	mux.Handle("POST /admin/access-requests/{id}/approve", platformadmin.ApproveAccessRequestHandler(repo, fakeM2M, testPlatformTenantID))
	mux.Handle("POST /admin/access-requests/{id}/reject", platformadmin.RejectAccessRequestHandler(repo, testPlatformTenantID))

	userID := uuid.NewString()
	email := fmt.Sprintf("http-ar-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(context.Background(), userID, email, "edge"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}

	reqList := reqWithIdentity(httptest.NewRequest(http.MethodGet, "/admin/access-requests?status=pending", nil), testPlatformTenantID)
	recList := httptest.NewRecorder()
	mux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", recList.Code)
	}

	var listRes platformadmin.ListAccessRequestsResponse
	if err := json.NewDecoder(recList.Body).Decode(&listRes); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var reqItem *platformadmin.AccessRequestItem
	for i := range listRes.Items {
		if listRes.Items[i].UserID == userID {
			reqItem = &listRes.Items[i]
			break
		}
	}
	if reqItem == nil {
		t.Fatal("solicitud no listada en HTTP")
	}

	slug := fmt.Sprintf("pa-har-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(context.Background(), slug, "HTTP AR", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	approveBody := fmt.Sprintf(`{"tenant_id":"%s","role":"operator","systems":["wapp.edge"]}`, ten.ID)
	reqApprove := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+reqItem.ID+"/approve", strings.NewReader(approveBody)), testPlatformTenantID)
	recApprove := httptest.NewRecorder()
	mux.ServeHTTP(recApprove, reqApprove)

	if recApprove.Code != http.StatusNoContent {
		t.Fatalf("approve status = %d, want 204. Body: %s", recApprove.Code, recApprove.Body.String())
	}

	// M-02: el motivo es obligatorio, así que el 404 hay que comprobarlo con
	// un motivo VÁLIDO -- de lo contrario lo que se vería es el 400 de
	// "entrada inválida", no el 404 que este caso quiere ejercitar.
	req404 := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+uuid.NewString()+"/reject", strings.NewReader(`{"reason":"motivo de prueba"}`)), testPlatformTenantID)
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("reject 404 status = %d, want 404. Body: %s", rec404.Code, rec404.Body.String())
	}
}

// TestIntegration_ApproveAccessRequest_RetryConvergesAfterIdentityFailure es
// el test que el criterio (2) de la Tanda 2 exige: provocar A PROPÓSITO un
// fallo del PUT systems tras el commit local y comprobar que el reintento
// CONVERGE, sin duplicar filas de tenant_members ni de iam_user_roles (C-04).
func TestIntegration_ApproveAccessRequest_RetryConvergesAfterIdentityFailure(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("c04-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	found := findPendingByUser(ctx, t, repo, userID)

	slug := fmt.Sprintf("pa-c04-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "C-04 Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	operatorID := uuid.NewString()

	// 1) identity está caído: el PUT systems falla con 502. Antes de este
	// arreglo, la solicitud quedaba "zombi" -- approved localmente, sin
	// systems en identity, y fuera de la bandeja pending -- y CUALQUIER
	// reintento moría con 409 porque status ya no era 'pending'.
	down := &fakeM2MClient{replaceErr: domain.ErrIdentityUnavailable}
	err = repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, down)
	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("esperado error envolviendo domain.ErrIdentityUnavailable, obtenido: %v", err)
	}
	if !errors.Is(err, platformadmin.ErrSystemsSyncFailed) {
		t.Fatalf("esperado error envolviendo ErrSystemsSyncFailed, obtenido: %v", err)
	}
	if down.calls != 1 {
		t.Fatalf("esperada 1 llamada a ReplaceUserSystems, obtenidas: %d", down.calls)
	}

	// Lo LOCAL debe haber quedado escrito pese al fallo de identity.
	assertTenantMembersCount(ctx, t, db, userID, ten.ID, 1)
	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, found.ID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "approved" {
		t.Fatalf("status esperado 'approved' tras el fallo de identity, obtenido: %q", status)
	}

	// 2) El admin pulsa "Aprobar" otra vez (identity ya volvió): debe
	// CONVERGER, sin error, sin duplicar tenant_members ni iam_user_roles.
	up := &fakeM2MClient{}
	err = repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, up)
	if err != nil {
		t.Fatalf("el reintento debía converger, obtenido: %v", err)
	}
	if up.calls != 1 {
		t.Fatalf("esperada 1 llamada a ReplaceUserSystems en el reintento, obtenidas: %d", up.calls)
	}
	if len(up.replacedSystems) != 1 || up.replacedSystems[0] != "wapp.bff" {
		t.Fatalf("systems reenviados inesperados: %v", up.replacedSystems)
	}

	assertTenantMembersCount(ctx, t, db, userID, ten.ID, 1) // sigue 1, no 2
	var roleRows int
	if qerr := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1 AND tenant_id = $2
	`, userID, ten.ID).Scan(&roleRows); qerr != nil {
		t.Fatalf("contar iam_user_roles: %v", qerr)
	}
	if roleRows != 1 {
		t.Fatalf("iam_user_roles esperado 1 fila, obtenidas: %d", roleRows)
	}

	// 3) Un reintento hacia un tenant DISTINTO no converge: es un conflicto
	// real, no el mismo destino que el 'approved' ya escrito.
	otroSlug := fmt.Sprintf("pa-c04-otro-%d", time.Now().UnixNano())
	otroTen, err := repo.CreateTenant(ctx, otroSlug, "C-04 Otro Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant otro: %v", err)
	}
	err = repo.ApproveAccessRequest(ctx, found.ID, otroTen.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, up)
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("esperado ErrConflict al reintentar hacia otro tenant, obtenido: %v", err)
	}
}

// TestIntegration_ApproveAccessRequest_PreexistingAccountSkipsSystemsSync
// cubre (D): un usuario con una aprobación previa (y por tanto, posiblemente,
// systems reales en identity que wApp no puede leer) NO debe recibir un
// ReplaceUserSystems ciego en una aprobación posterior -- eso reemplazaría en
// vez de sumar. Debe fallar con ErrSystemsUnionUnavailable y NUNCA llamar al
// M2M, aunque lo local (tenant + rol) sí se escriba.
func TestIntegration_ApproveAccessRequest_PreexistingAccountSkipsSystemsSync(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("union-%d@example.com", time.Now().UnixNano())

	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest 1: %v", err)
	}
	first := findPendingByUser(ctx, t, repo, userID)

	slug := fmt.Sprintf("pa-union-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "Union Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	operatorID := uuid.NewString()

	if err := repo.ApproveAccessRequest(ctx, first.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.edge"}, &fakeM2MClient{}); err != nil {
		t.Fatalf("ApproveAccessRequest 1: %v", err)
	}

	// Segunda solicitud del MISMO usuario (p.ej. ahora pide desde el BFF).
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest 2: %v", err)
	}
	second := findPendingByUser(ctx, t, repo, userID)

	m2m2 := &fakeM2MClient{}
	err = repo.ApproveAccessRequest(ctx, second.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, m2m2)
	if !errors.Is(err, platformadmin.ErrSystemsUnionUnavailable) {
		t.Fatalf("esperado ErrSystemsUnionUnavailable, obtenido: %v", err)
	}
	if m2m2.calls != 0 {
		t.Fatalf("ReplaceUserSystems NO debía llamarse, se llamó %d veces", m2m2.calls)
	}

	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, second.ID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "approved" {
		t.Fatalf("lo local debía quedar escrito igual; status obtenido: %q", status)
	}
}

func findPendingByUser(ctx context.Context, t *testing.T, repo *platformadmin.Repository, userID string) *platformadmin.AccessRequestItem {
	t.Helper()
	items, err := repo.ListAccessRequests(ctx, "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	for i := range items {
		if items[i].UserID == userID {
			return &items[i]
		}
	}
	t.Fatalf("no se encontró solicitud pending para %s", userID)
	return nil
}

func assertTenantMembersCount(ctx context.Context, t *testing.T, db *sql.DB, userID, tenantID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&got); err != nil {
		t.Fatalf("contar tenant_members: %v", err)
	}
	if got != want {
		t.Fatalf("tenant_members esperado %d fila(s), obtenidas: %d", want, got)
	}
}

// ── Tests sin BD: las cuatro comprobaciones siguientes son el PRIMER paso de
// su función, antes de tocar r.db, así que corren con Repository{db: nil} y
// no dependen de WAPP_TEST_DB_DSN. ──────────────────────────────────────────

func TestApproveAccessRequest_RejectsPlatformSystem(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	err := repo.ApproveAccessRequest(context.Background(), uuid.NewString(), uuid.NewString(), "tenant_admin", uuid.NewString(), []string{"wapp.bff", "wapp.platform"}, nil)
	if !errors.Is(err, platformadmin.ErrPlatformSystemForbidden) {
		t.Fatalf("esperado ErrPlatformSystemForbidden, obtenido: %v", err)
	}
}

func TestRejectAccessRequest_RequiresReason(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	err := repo.RejectAccessRequest(context.Background(), uuid.NewString(), "   ", uuid.NewString())
	if !errors.Is(err, platformadmin.ErrInvalidInput) {
		t.Fatalf("esperado ErrInvalidInput con motivo en blanco, obtenido: %v", err)
	}
}

func TestHandlers_ApproveAccessRequest_RejectsPlatformSystemBody(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	mux := http.NewServeMux()
	mux.Handle("POST /admin/access-requests/{id}/approve", platformadmin.ApproveAccessRequestHandler(repo, &fakeM2MClient{}, testPlatformTenantID))

	body := fmt.Sprintf(`{"tenant_id":"%s","role":"tenant_admin","systems":["wapp.bff","wapp.platform"]}`, uuid.NewString())
	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+uuid.NewString()+"/approve", strings.NewReader(body)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "wapp.platform") {
		t.Fatalf("cuerpo esperado mencionando wapp.platform, obtenido: %s", rec.Body.String())
	}
}

func TestHandlers_RejectAccessRequest_RequiresReasonBody(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil)
	mux := http.NewServeMux()
	mux.Handle("POST /admin/access-requests/{id}/reject", platformadmin.RejectAccessRequestHandler(repo, testPlatformTenantID))

	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+uuid.NewString()+"/reject", strings.NewReader(`{"reason":""}`)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
}
