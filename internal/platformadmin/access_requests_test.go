package platformadmin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
	// vigentes es lo que identity devuelve en GetUserSystems; getErr fuerza el
	// fallo de LECTURA, que es lo que hoy dispara ErrSystemsUnionUnavailable.
	vigentes []string
	getErr   error
	getCalls int
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

// GetUserSystems completa out.IdentityM2MClient (Plan 047 · Ola B). 🔧 Desde el
// 2026-08-28 la vía del OPERADOR SÍ lo llama —dejó de aproximar la unión con su
// tabla local—, así que este doble ya es programable: `vigentes` es lo que
// identity devolvería y `getErr` fuerza el fallo de lectura. El comentario
// anterior avisaba de que ese día llegaría; llegó.
func (f *fakeM2MClient) GetUserSystems(_ context.Context, _ string) ([]string, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.vigentes, nil
}

func (f *fakeM2MClient) Signup(_ context.Context, email, password, firstName, lastName string) (string, error) {
	return uuid.NewString(), nil
}

func TestIntegration_AccessRequests_Lifecycle(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
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

	testCrossTenantConflict(ctx, t, repo, db, userID, email, operatorID, fakeM2M)
}

// testCrossTenantConflict cubre el criterio (3) de T3.4: el 409 de membresía
// cruzada no debe escribir NADA, ni local ni en identity. Antes solo miraba
// err != nil, que un ErrConflict "de mentira" (p. ej. devuelto por un bug ANTES
// de la comprobación real, o incluso una escritura parcial seguida de un error
// distinto) habría dejado pasar igual. Aquí se cuentan filas y llamadas al M2M
// antes/después para que una escritura fantasma en el tenant conflictivo
// rompa el test en vez de quedar en silencio.
func testCrossTenantConflict(ctx context.Context, t *testing.T, repo *platformadmin.Repository, db *sql.DB, userID, email, operatorID string, fakeM2M *fakeM2MClient) {
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

	// Snapshot ANTES del intento conflictivo: fakeM2M ya se usó en la
	// aprobación previa de este mismo test (replacedSystems no arranca vacío),
	// así que lo que importa es que ESTA llamada no lo toque, no que empiece
	// en cero.
	callsBefore := fakeM2M.calls
	membersBefore := countRows(ctx, t, db, `SELECT count(*) FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2`, userID, ten2.ID)
	rolesBefore := countRows(ctx, t, db, `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1 AND tenant_id = $2`, userID, ten2.ID)

	errConflict := repo.ApproveAccessRequest(ctx, found2.ID, ten2.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, fakeM2M)
	if !errors.Is(errConflict, platformadmin.ErrConflict) {
		t.Fatalf("esperado ErrConflict al aprobar usuario que ya es miembro de otro tenant, obtenido: %v", errConflict)
	}

	if fakeM2M.calls != callsBefore {
		t.Fatalf("ReplaceUserSystems NO debía llamarse en un 409 de membresía cruzada: calls antes=%d, después=%d", callsBefore, fakeM2M.calls)
	}
	if got := countRows(ctx, t, db, `SELECT count(*) FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2`, userID, ten2.ID); got != membersBefore {
		t.Fatalf("tenant_members NO debía escribirse en el tenant conflictivo: antes=%d, después=%d", membersBefore, got)
	}
	if got := countRows(ctx, t, db, `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1 AND tenant_id = $2`, userID, ten2.ID); got != rolesBefore {
		t.Fatalf("iam_user_roles NO debía escribirse en el tenant conflictivo: antes=%d, después=%d", rolesBefore, got)
	}

	if err := repo.RejectAccessRequest(ctx, found2.ID, "usuario ya asignado", operatorID); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
}

// countRows ejecuta un SELECT count(*) parametrizado y devuelve el entero.
// Helper compartido por los tests que necesitan un snapshot antes/después de
// una operación (atomicidad, conflictos) sin repetir el boilerplate de
// QueryRowContext + Scan en cada sitio.
func countRows(ctx context.Context, t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows(%q): %v", query, err)
	}
	return n
}

func TestHandlers_AccessRequests_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
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
	repo := platformadmin.NewRepository(db, nil)
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
	approveWithFailingIdentitySync(ctx, t, repo, found.ID, ten.ID, operatorID)

	// Lo LOCAL debe haber quedado escrito pese al fallo de identity.
	assertAccessRequestApprovedLocally(ctx, t, db, userID, ten.ID, found.ID)

	// 2) El admin pulsa "Aprobar" otra vez (identity ya volvió): debe
	// CONVERGER, sin error, sin duplicar tenant_members ni iam_user_roles.
	assertApproveRetryConverges(ctx, t, db, repo, found.ID, ten.ID, userID, operatorID)

	// 3) Un reintento hacia un tenant DISTINTO no converge: es un conflicto
	// real, no el mismo destino que el 'approved' ya escrito.
	otroSlug := fmt.Sprintf("pa-c04-otro-%d", time.Now().UnixNano())
	otroTen, err := repo.CreateTenant(ctx, otroSlug, "C-04 Otro Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant otro: %v", err)
	}
	err = repo.ApproveAccessRequest(ctx, found.ID, otroTen.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, &fakeM2MClient{})
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("esperado ErrConflict al reintentar hacia otro tenant, obtenido: %v", err)
	}
}

// approveWithFailingIdentitySync ejecuta la primera aprobación con un M2M
// cuyo ReplaceUserSystems falla, y comprueba que el error envuelve tanto la
// causa (domain.ErrIdentityUnavailable) como el sentinel de dominio
// (ErrSystemsSyncFailed), con UNA sola llamada a ReplaceUserSystems.
func approveWithFailingIdentitySync(ctx context.Context, t *testing.T, repo *platformadmin.Repository, requestID, tenantID, operatorID string) {
	t.Helper()
	down := &fakeM2MClient{replaceErr: domain.ErrIdentityUnavailable}
	err := repo.ApproveAccessRequest(ctx, requestID, tenantID, "tenant_admin", operatorID, []string{"wapp.bff"}, down)
	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("esperado error envolviendo domain.ErrIdentityUnavailable, obtenido: %v", err)
	}
	if !errors.Is(err, platformadmin.ErrSystemsSyncFailed) {
		t.Fatalf("esperado error envolviendo ErrSystemsSyncFailed, obtenido: %v", err)
	}
	if down.calls != 1 {
		t.Fatalf("esperada 1 llamada a ReplaceUserSystems, obtenidas: %d", down.calls)
	}
}

// assertAccessRequestApprovedLocally comprueba que la escritura LOCAL
// (tenant_members + status 'approved') quedó hecha pese al fallo de
// identity de approveWithFailingIdentitySync.
func assertAccessRequestApprovedLocally(ctx context.Context, t *testing.T, db *sql.DB, userID, tenantID, requestID string) {
	t.Helper()
	assertTenantMembersCount(ctx, t, db, userID, tenantID, 1)
	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, requestID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "approved" {
		t.Fatalf("status esperado 'approved' tras el fallo de identity, obtenido: %q", status)
	}
}

// assertApproveRetryConverges reintenta la aprobación (identity ya
// recuperado) y comprueba que converge: una sola llamada a
// ReplaceUserSystems con los systems esperados, y sin duplicar filas de
// tenant_members ni de iam_user_roles.
func assertApproveRetryConverges(ctx context.Context, t *testing.T, db *sql.DB, repo *platformadmin.Repository, requestID, tenantID, userID, operatorID string) {
	t.Helper()
	up := &fakeM2MClient{}
	err := repo.ApproveAccessRequest(ctx, requestID, tenantID, "tenant_admin", operatorID, []string{"wapp.bff"}, up)
	if err != nil {
		t.Fatalf("el reintento debía converger, obtenido: %v", err)
	}
	if up.calls != 1 {
		t.Fatalf("esperada 1 llamada a ReplaceUserSystems en el reintento, obtenidas: %d", up.calls)
	}
	if len(up.replacedSystems) != 1 || up.replacedSystems[0] != "wapp.bff" {
		t.Fatalf("systems reenviados inesperados: %v", up.replacedSystems)
	}

	assertTenantMembersCount(ctx, t, db, userID, tenantID, 1) // sigue 1, no 2
	var roleRows int
	if qerr := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID).Scan(&roleRows); qerr != nil {
		t.Fatalf("contar iam_user_roles: %v", qerr)
	}
	if roleRows != 1 {
		t.Fatalf("iam_user_roles esperado 1 fila, obtenidas: %d", roleRows)
	}
}

// TestIntegration_ApproveAccessRequest_SegundaAprobacionUNE cubre (D) tras el
// arreglo del 2026-08-28: un usuario con una aprobación previa YA NO se rehúsa.
// Se LEE su conjunto vigente en identity y se declara la UNIÓN, así que la
// segunda aprobación conserva `wapp.edge` (de la primera) y suma `wapp.bff`.
//
// 🔴 Este test afirmaba lo contrario —ErrSystemsUnionUnavailable y CERO llamadas
// al M2M— y era correcto entonces: no había lectura de identity. La Ola B trajo
// GetUserSystems y esa premisa caducó; el proxy local que la sustituía daba
// falso negativo en la primera aprobación y falso positivo permanente desde la
// segunda.
func TestIntegration_ApproveAccessRequest_SegundaAprobacionUNE(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
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

	// identity ya tiene wapp.edge, concedido en la primera aprobación.
	m2m2 := &fakeM2MClient{vigentes: []string{"wapp.edge"}}
	err = repo.ApproveAccessRequest(ctx, second.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, m2m2)
	if err != nil {
		t.Fatalf("la segunda aprobación tiene que unir, no rehusar; obtenido: %v", err)
	}
	if m2m2.getCalls != 1 {
		t.Fatalf("hay que LEER el conjunto vigente antes de declararlo; GetUserSystems se llamó %d veces", m2m2.getCalls)
	}
	if m2m2.calls != 1 {
		t.Fatalf("ReplaceUserSystems debía llamarse una vez, se llamó %d", m2m2.calls)
	}
	// 🔑 El aserto que da nombre al arreglo: el conjunto declarado es la UNIÓN.
	// Con el reemplazo ciego de antes, wapp.edge habría desaparecido.
	esperado := []string{"wapp.edge", "wapp.bff"}
	if !slices.Equal(m2m2.replacedSystems, esperado) {
		t.Fatalf("se tenía que declarar la unión %v y se declaró %v", esperado, m2m2.replacedSystems)
	}

	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, second.ID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "approved" {
		t.Fatalf("lo local debía quedar escrito igual; status obtenido: %q", status)
	}
}

// TestIntegration_ApproveAccessRequest_SinPoderLEER_NoDeclaraNada — el hermano
// negativo, y es el que conserva viva la razón de ser de
// ErrSystemsUnionUnavailable: si la LECTURA del conjunto vigente falla, un PUT
// declarativo borraría lo que otra vía concedió. Se rehúsa, y sobre todo NO se
// llama a ReplaceUserSystems. Lo local queda escrito igual.
//
// Sin este test, el arreglo de la unión podría degenerar en «si no puedo leer,
// mando lo que tengo», que es exactamente el borrado que (D) existe para evitar.
func TestIntegration_ApproveAccessRequest_SinPoderLEER_NoDeclaraNada(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("noleer-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	req := findPendingByUser(ctx, t, repo, userID)

	slug := fmt.Sprintf("pa-noleer-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "NoLeer Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	m2m := &fakeM2MClient{getErr: errors.New("identity no contesta")}
	err = repo.ApproveAccessRequest(ctx, req.ID, ten.ID, "tenant_admin", uuid.NewString(), []string{"wapp.bff"}, m2m)
	if !errors.Is(err, platformadmin.ErrSystemsUnionUnavailable) {
		t.Fatalf("sin lectura hay que rehusar con ErrSystemsUnionUnavailable; obtenido: %v", err)
	}
	if m2m.calls != 0 {
		t.Fatalf("sin haber leído NO se puede declarar nada; ReplaceUserSystems se llamó %d veces", m2m.calls)
	}

	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, req.ID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "approved" {
		t.Fatalf("lo local debía quedar escrito igual; status obtenido: %q", status)
	}
}

// TestIntegration_ApproveAccessRequest_NoM2MClient_KeepsLocalSkipsSystems
// fija la Tanda 6 · 1.1: antes, `m2m == nil` compartía la misma salida
// silenciosa (return nil, 204) que `len(systems) == 0` -- un caso legítimo
// (nada que conceder) tapaba uno que no lo es (algo que conceder y sin
// cliente M2M para hacerlo, hoy la realidad de CUALQUIER despliegue: T0.2
// sigue sin WAPP_IDENTITY_API_KEY). Debe devolver ErrIdentityM2MUnavailable,
// no nil, y lo LOCAL (tenant + rol) debe quedar escrito igual.
func TestIntegration_ApproveAccessRequest_NoM2MClient_KeepsLocalSkipsSystems(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("nom2m-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	found := findPendingByUser(ctx, t, repo, userID)

	slug := fmt.Sprintf("pa-nom2m-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "NoM2M Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	operatorID := uuid.NewString()

	err = repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, nil)
	if !errors.Is(err, platformadmin.ErrIdentityM2MUnavailable) {
		t.Fatalf("esperado ErrIdentityM2MUnavailable, obtenido: %v", err)
	}

	assertTenantMembersCount(ctx, t, db, userID, ten.ID, 1)
	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, found.ID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "approved" {
		t.Fatalf("lo local debía quedar escrito igual; status obtenido: %q", status)
	}
}

// TestIntegration_ApproveAccessRequest_NoM2MClient_NoSystemsStillConverges
// distingue el caso LEGÍTIMO (1.1): sin systems que conceder, m2m nil sigue
// devolviendo nil (204) -- no hay nada que la falta de cliente M2M impida.
func TestIntegration_ApproveAccessRequest_NoM2MClient_NoSystemsStillConverges(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("nom2m-nosys-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	found := findPendingByUser(ctx, t, repo, userID)

	slug := fmt.Sprintf("pa-nom2m-nosys-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "NoM2M NoSys Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	operatorID := uuid.NewString()

	err = repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, nil, nil)
	if err != nil {
		t.Fatalf("sin systems que conceder, m2m nil no debía impedir la aprobación: %v", err)
	}
	assertTenantMembersCount(ctx, t, db, userID, ten.ID, 1)
}

// TestHandlers_ApproveAccessRequest_NoM2MClient_HTTP fija el status y cuerpo
// EXACTOS que ve la consola: 503 con ApprovePartialResult{local:"ok",
// identity:"skipped"} -- distinguible tanto del 204 silencioso de antes como
// del 409 de ErrSystemsUnionUnavailable y el 502 de ErrSystemsSyncFailed.
func TestHandlers_ApproveAccessRequest_NoM2MClient_HTTP(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)

	mux := http.NewServeMux()
	// nil de verdad: NINGÚN cliente M2M configurado en este mux, igual que un
	// despliegue real sin WAPP_IDENTITY_API_KEY (T0.2 pendiente).
	mux.Handle("POST /admin/access-requests/{id}/approve", platformadmin.ApproveAccessRequestHandler(repo, nil, testPlatformTenantID))

	userID := uuid.NewString()
	email := fmt.Sprintf("http-nom2m-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(context.Background(), userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	found := findPendingByUser(context.Background(), t, repo, userID)

	slug := fmt.Sprintf("pa-h-nom2m-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(context.Background(), slug, "HTTP NoM2M", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	body := fmt.Sprintf(`{"tenant_id":"%s","role":"tenant_admin","systems":["wapp.bff"]}`, ten.ID)
	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+found.ID+"/approve", strings.NewReader(body)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503. Body: %s", rec.Code, rec.Body.String())
	}

	var result platformadmin.ApprovePartialResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if result.Local != "ok" || result.Identity != "skipped" {
		t.Fatalf("cuerpo inesperado: %+v", result)
	}
}

// TestIntegration_ApproveAccessRequest_RetryDifferentRole_Rejected fija la
// Tanda 6 · 1.2: reaprobar una solicitud YA 'approved' con un ROL distinto
// del que quedó escrito la primera vez NO converge -- antes, saltar
// executeApprovalTx en el reintento también saltaba resolveRoleID, así que
// esto devolvía 204 sin cambiar el rol Y disparaba ReplaceUserSystems con
// los systems de la segunda llamada (el peligro que C-05 cierra, entrando
// por la puerta que C-04 abrió). Cubre los DOS casos medidos: un rol que NO
// EXISTE (ErrInvalidInput, igual que en la aprobación normal) y un rol
// VÁLIDO pero distinto del ya aprobado (ErrRetryRoleMismatch). En ninguno de
// los dos casos debe llamarse a ReplaceUserSystems ni cambiar el rol local.
func TestIntegration_ApproveAccessRequest_RetryDifferentRole_Rejected(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("retry-role-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	found := findPendingByUser(ctx, t, repo, userID)

	slug := fmt.Sprintf("pa-retry-role-%d", time.Now().UnixNano())
	ten, err := repo.CreateTenant(ctx, slug, "Retry Role Tenant", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	operatorID := uuid.NewString()

	first := &fakeM2MClient{}
	if err := repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "operator", operatorID, []string{"wapp.bff"}, first); err != nil {
		t.Fatalf("primera aprobación (operator): %v", err)
	}
	assertUserRoleName(ctx, t, db, userID, ten.ID, "operator")

	// Caso A: rol que NO EXISTE -- resolveRoleID lo rechaza igual que en una
	// aprobación normal, ANTES de tocar nada.
	callsBeforeA := first.calls
	errA := repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "rol-que-no-existe", operatorID, []string{"wapp.edge"}, first)
	if !errors.Is(errA, platformadmin.ErrInvalidInput) {
		t.Fatalf("caso A (rol inexistente): esperado ErrInvalidInput, obtenido: %v", errA)
	}
	if first.calls != callsBeforeA {
		t.Fatalf("caso A: ReplaceUserSystems NO debía llamarse, calls antes=%d después=%d", callsBeforeA, first.calls)
	}
	assertUserRoleName(ctx, t, db, userID, ten.ID, "operator") // sigue operator

	// Caso B: rol VÁLIDO pero DISTINTO del ya aprobado -- no converge.
	callsBeforeB := first.calls
	errB := repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.edge"}, first)
	if !errors.Is(errB, platformadmin.ErrRetryRoleMismatch) {
		t.Fatalf("caso B (rol distinto): esperado ErrRetryRoleMismatch, obtenido: %v", errB)
	}
	if first.calls != callsBeforeB {
		t.Fatalf("caso B: ReplaceUserSystems NO debía llamarse, calls antes=%d después=%d", callsBeforeB, first.calls)
	}
	if len(first.replacedSystems) != 1 || first.replacedSystems[0] != "wapp.bff" {
		t.Fatalf("los systems de la PRIMERA aprobación no debían tocarse: %v", first.replacedSystems)
	}
	assertUserRoleName(ctx, t, db, userID, ten.ID, "operator") // sigue operator, NO tenant_admin

	// Caso C, control: el MISMO rol SÍ converge (comportamiento preexistente
	// de C-04, no debe haberse roto).
	up := &fakeM2MClient{}
	if err := repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "operator", operatorID, []string{"wapp.bff"}, up); err != nil {
		t.Fatalf("caso C (mismo rol, debía converger): %v", err)
	}
	if up.calls != 1 {
		t.Fatalf("caso C: esperada 1 llamada a ReplaceUserSystems, obtenidas %d", up.calls)
	}
}

// assertUserRoleName comprueba que el ÚNICO rol de userID en tenantID es
// exactamente wantName -- lee iam_user_roles JOIN iam_roles, la misma fuente
// de verdad que checkRetryApproved usa para decidir la convergencia.
func assertUserRoleName(ctx context.Context, t *testing.T, db *sql.DB, userID, tenantID, wantName string) {
	t.Helper()
	var gotName string
	err := db.QueryRowContext(ctx, `
		SELECT r.name
		FROM public.iam_user_roles ur
		JOIN public.iam_roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1 AND ur.tenant_id = $2
	`, userID, tenantID).Scan(&gotName)
	if err != nil {
		t.Fatalf("leer rol de %s en %s: %v", userID, tenantID, err)
	}
	if gotName != wantName {
		t.Fatalf("rol de %s en %s = %q, want %q", userID, tenantID, gotName, wantName)
	}
}

// TestHandlers_AccessRequests_InvalidID_Returns404 fija P3: un {id} que no es
// UUID en las rutas de la bandeja (approve/reject) da 404, no 500 -- mismo
// criterio que M-03 ya aplicaba a /admin/tenants (tenantIDFromPath). Antes,
// `WHERE id = $1` sobre una columna UUID con un valor que no codifica
// reventaba con un error de pgx que no es sql.ErrNoRows, mapeado al 500
// genérico del switch. Sin BD: la validación vive en el borde
// (accessRequestIDFromPath), así que corre con setupAdminMux(nil).
func TestHandlers_AccessRequests_InvalidID_Returns404(t *testing.T) {
	t.Parallel()
	mux := setupAdminMux(nil)

	badIDs := []string{"no-es-un-uuid", "12345", "55550000-0000-0000-0000"}
	for _, id := range badIDs {
		reqApprove := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/approve", strings.NewReader(`{"tenant_id":"`+uuid.NewString()+`","role":"operator"}`)), testPlatformTenantID)
		recApprove := httptest.NewRecorder()
		mux.ServeHTTP(recApprove, reqApprove)
		if recApprove.Code != http.StatusNotFound {
			t.Errorf("POST approve id=%q: status = %d, want 404. Body: %s", id, recApprove.Code, recApprove.Body.String())
		}

		reqReject := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/reject", strings.NewReader(`{"reason":"motivo"}`)), testPlatformTenantID)
		recReject := httptest.NewRecorder()
		mux.ServeHTTP(recReject, reqReject)
		if recReject.Code != http.StatusNotFound {
			t.Errorf("POST reject id=%q: status = %d, want 404. Body: %s", id, recReject.Code, recReject.Body.String())
		}
	}
}

// TestIntegration_ApproveAccessRequest_TenantNotFound_Returns404 fija P3: un
// tenant_id sintácticamente válido pero inexistente da 404 (empresa no
// encontrada), no 500 por violación de FK. La tx revierte bien de por sí (ya
// verificado antes de este fix) -- lo que cambia es el status y que el
// chequeo ocurre ANTES de abrir la tx.
func TestIntegration_ApproveAccessRequest_TenantNotFound_Returns404(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db, nil)
	ctx := context.Background()

	userID := uuid.NewString()
	email := fmt.Sprintf("notenant-%d@example.com", time.Now().UnixNano())
	if err := repo.CreateAccessRequest(ctx, userID, email, "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	found := findPendingByUser(ctx, t, repo, userID)

	nonexistentTenantID := uuid.NewString()
	err := repo.ApproveAccessRequest(ctx, found.ID, nonexistentTenantID, "operator", uuid.NewString(), nil, nil)
	if !errors.Is(err, platformadmin.ErrTenantNotFound) {
		t.Fatalf("esperado ErrTenantNotFound, obtenido: %v", err)
	}

	// La solicitud sigue pending: nada se escribió.
	var status string
	if qerr := db.QueryRowContext(ctx, `SELECT status FROM public.access_requests WHERE id = $1`, found.ID).Scan(&status); qerr != nil {
		t.Fatalf("leer status: %v", qerr)
	}
	if status != "pending" {
		t.Fatalf("status esperado 'pending', obtenido: %q", status)
	}

	// Y a nivel HTTP, 404.
	mux := http.NewServeMux()
	mux.Handle("POST /admin/access-requests/{id}/approve", platformadmin.ApproveAccessRequestHandler(repo, &fakeM2MClient{}, testPlatformTenantID))
	body := fmt.Sprintf(`{"tenant_id":"%s","role":"operator"}`, nonexistentTenantID)
	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+found.ID+"/approve", strings.NewReader(body)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404. Body: %s", rec.Code, rec.Body.String())
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
	repo := platformadmin.NewRepository(nil, nil)
	err := repo.ApproveAccessRequest(context.Background(), uuid.NewString(), uuid.NewString(), "tenant_admin", uuid.NewString(), []string{"wapp.bff", "wapp.platform"}, nil)
	if !errors.Is(err, platformadmin.ErrPlatformSystemForbidden) {
		t.Fatalf("esperado ErrPlatformSystemForbidden, obtenido: %v", err)
	}
}

func TestRejectAccessRequest_RequiresReason(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil, nil)
	err := repo.RejectAccessRequest(context.Background(), uuid.NewString(), "   ", uuid.NewString())
	if !errors.Is(err, platformadmin.ErrInvalidInput) {
		t.Fatalf("esperado ErrInvalidInput con motivo en blanco, obtenido: %v", err)
	}
}

func TestHandlers_ApproveAccessRequest_RejectsPlatformSystemBody(t *testing.T) {
	t.Parallel()
	repo := platformadmin.NewRepository(nil, nil)
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
	repo := platformadmin.NewRepository(nil, nil)
	mux := http.NewServeMux()
	mux.Handle("POST /admin/access-requests/{id}/reject", platformadmin.RejectAccessRequestHandler(repo, testPlatformTenantID))

	req := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+uuid.NewString()+"/reject", strings.NewReader(`{"reason":""}`)), testPlatformTenantID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
}
