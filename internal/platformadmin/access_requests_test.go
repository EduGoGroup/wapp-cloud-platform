package platformadmin_test

import (
	"context"
	"encoding/json"
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
}

func (f *fakeM2MClient) EnsureUser(_ context.Context, email, firstName, lastName string) (domain.IdentityUser, error) {
	return domain.IdentityUser{ID: uuid.NewString(), Email: email, Created: true}, nil
}

func (f *fakeM2MClient) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	f.replacedSystems = systems
	return domain.IdentitySystemsDiff{Systems: systems}, f.replaceErr
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

	errReApprove := repo.ApproveAccessRequest(ctx, found.ID, ten.ID, "tenant_admin", operatorID, []string{"wapp.bff"}, fakeM2M)
	if errReApprove == nil {
		t.Fatal("esperado error al re-aprobar solicitud ya resuelta")
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

	req404 := reqWithIdentity(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+uuid.NewString()+"/reject", nil), testPlatformTenantID)
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("reject 404 status = %d, want 404", rec404.Code)
	}
}
