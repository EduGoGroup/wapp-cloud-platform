package platformadmin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

type mockSignupM2M struct {
	signupCalled  bool
	ensureCalled  bool
	signupUserID  string
	systemsSet    []string
	signupErr     error
	ensureUserErr error
}

func (m *mockSignupM2M) EnsureUser(_ context.Context, email, firstName, lastName string) (domain.IdentityUser, error) {
	m.ensureCalled = true
	if m.ensureUserErr != nil {
		return domain.IdentityUser{}, m.ensureUserErr
	}
	return domain.IdentityUser{ID: m.signupUserID, Email: email, Created: false}, nil
}

func (m *mockSignupM2M) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	m.systemsSet = systems
	return domain.IdentitySystemsDiff{Systems: systems}, nil
}

func (m *mockSignupM2M) Signup(_ context.Context, email, password, firstName, lastName string) (string, error) {
	m.signupCalled = true
	if m.signupErr != nil {
		return "", m.signupErr
	}
	return m.signupUserID, nil
}

func TestSignupHandler_Success_NewUser(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	uid := uuid.NewString()
	m2m := &mockSignupM2M{signupUserID: uid}

	handler := platformadmin.SignupHandler(repo, m2m, nil, nil)
	email := fmt.Sprintf("new-user-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":"%s","password":"Password123456!","first_name":"Juan","last_name":"Perez","origin":"bff"}`, email)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202. Body: %s", rec.Code, rec.Body.String())
	}

	var res platformadmin.SignupResponse
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Message != "Listo. Entra con tu correo y tu clave." {
		t.Fatalf("mensaje inesperado: %q", res.Message)
	}

	// Verificar llamada a identity
	if !m2m.signupCalled {
		t.Fatal("Signup no fue llamado")
	}
	if len(m2m.systemsSet) != 1 || m2m.systemsSet[0] != "wapp.bff" {
		t.Fatalf("sistemas otorgados inesperados: %v", m2m.systemsSet)
	}

	// Verificar fila en access_requests
	requests, err := repo.ListAccessRequests(context.Background(), "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	var found bool
	for _, it := range requests {
		if it.UserID == uid && it.Origin == "bff" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("solicitud no encontrada en access_requests")
	}

	// Verificar que NO se crearon filas en tenant_members ni iam_user_roles (REQ-056.8)
	verifyNoPrematureMembership(t, db, uid)
}

func verifyNoPrematureMembership(t *testing.T, db *sql.DB, uid string) {
	t.Helper()
	var memberCount, roleCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM public.tenant_members WHERE user_id = $1`, uid).Scan(&memberCount); err != nil {
		t.Fatalf("scan memberCount: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1`, uid).Scan(&roleCount); err != nil {
		t.Fatalf("scan roleCount: %v", err)
	}
	if memberCount != 0 || roleCount != 0 {
		t.Fatalf("el signup prematuramente asignó membresías o roles: members=%d, roles=%d", memberCount, roleCount)
	}
}

func TestSignupHandler_Success_ExistingUser_SameResponse(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	uid := uuid.NewString()
	m2m := &mockSignupM2M{
		signupUserID: uid,
		signupErr:    domain.ErrEmailTaken, // el correo ya existe en identity
	}

	handler := platformadmin.SignupHandler(repo, m2m, nil, nil)
	email := fmt.Sprintf("exist-user-%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":"%s","password":"Password123456!","first_name":"Ana","last_name":"Gomez","origin":"edge"}`, email)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.2:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202. Body: %s", rec.Code, rec.Body.String())
	}

	// Debe haber intentado Signup y luego recurrido a EnsureUser
	if !m2m.signupCalled || !m2m.ensureCalled {
		t.Fatalf("esperadas llamadas a Signup y EnsureUser: signup=%v ensure=%v", m2m.signupCalled, m2m.ensureCalled)
	}
	if len(m2m.systemsSet) != 1 || m2m.systemsSet[0] != "wapp.edge" {
		t.Fatalf("sistemas otorgados inesperados: %v", m2m.systemsSet)
	}
}

func TestSignupHandler_RateLimiter(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	repo := platformadmin.NewRepository(db)
	m2m := &mockSignupM2M{signupUserID: uuid.NewString()}

	limiter := ratelimit.NewLimiter(rate.Limit(1), 1) // 1 rps, burst 1
	handler := platformadmin.SignupHandler(repo, m2m, limiter, nil)

	body := `{"email":"test@example.com","password":"Password123456!","first_name":"T","last_name":"U","origin":"bff"}`

	// Primera request -> 202
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req1.RemoteAddr = "198.51.100.1:4567"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("primera request status = %d, want 202", rec1.Code)
	}

	// Segunda request inmediata desde la misma IP -> 429
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/signup", strings.NewReader(body))
	req2.RemoteAddr = "198.51.100.1:4567"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("segunda request status = %d, want 429", rec2.Code)
	}
}
