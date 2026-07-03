package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-shared/auth"
)

const (
	testSigningKey  = "hs256-material-de-firma-para-los-tests"
	testIssuer      = "wapp-iam-test"
	testTenant      = "11111111-1111-1111-1111-111111111111"
	testTenantB     = "22222222-2222-2222-2222-222222222222"
	testEmail       = "op@tenant.example"
	testLoginPhrase = "una-frase-de-acceso-larga"
)

// mustUserSvc/mustAuthSvc/mustRoleSvc arman los servicios comprobando el error
// del constructor (el linter exige no descartarlo).
func mustUserSvc(t *testing.T, s *memory.Store) *usecase.UserService {
	t.Helper()
	svc, err := usecase.NewUserService(s.Users, s.Roles, s.Grants)
	if err != nil {
		t.Fatalf("NewUserService: %v", err)
	}
	return svc
}

func mustAuthSvc(t *testing.T, s *memory.Store, jwt *auth.JWTManager) *usecase.AuthService {
	t.Helper()
	svc, err := usecase.NewAuthService(s.Users, s.Roles, s.Grants, s.Refresh, s.Audit, jwt, usecase.Config{})
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	return svc
}

func mustRoleSvc(t *testing.T, s *memory.Store) *usecase.RoleService {
	t.Helper()
	svc, err := usecase.NewRoleService(s.Roles)
	if err != nil {
		t.Fatalf("NewRoleService: %v", err)
	}
	return svc
}

// fixture arma el AuthService+UserService sobre un Store en memoria, con un
// usuario activo (password hasheada) y un rol "operator" asignado con grants
// flows.* / messages.send. Devuelve también el Store para inspección.
func fixture(t *testing.T) (*usecase.AuthService, *usecase.UserService, *memory.Store, domain.User) {
	t.Helper()
	store := memory.NewStore()
	jwt := auth.NewJWTManager(testSigningKey, testIssuer)

	users, err := usecase.NewUserService(store.Users, store.Roles, store.Grants)
	if err != nil {
		t.Fatalf("NewUserService: %v", err)
	}
	authSvc, err := usecase.NewAuthService(store.Users, store.Roles, store.Grants, store.Refresh, store.Audit, jwt, usecase.Config{})
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}

	// Rol operator con dos grants.
	role := store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "operator"}, []domain.Grant{
		{Pattern: "flows.*", Effect: domain.EffectAllow},
		{Pattern: "messages.send", Effect: domain.EffectAllow},
	})

	u, err := users.CreateUser(context.Background(), in.CreateUserInput{
		TenantID: testTenant, Email: testEmail, Password: testLoginPhrase, RoleIDs: []string{role.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return authSvc, users, store, u
}

func ptr(s string) *string { return &s }

func TestLogin_OK_EmitsTokensAndEffectiveGrants(t *testing.T) {
	t.Parallel()
	authSvc, _, store, u := fixture(t)
	ctx := context.Background()

	res, err := authSvc.Login(ctx, in.LoginInput{Email: testEmail, Password: testLoginPhrase})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.AccessToken == "" || res.RefreshToken == "" {
		t.Fatal("Login: tokens vacíos")
	}
	if res.Context.TenantID != testTenant || res.Context.UserID != u.ID {
		t.Fatalf("Login: contexto inesperado: %+v", res.Context)
	}

	// El access token embebe los grants efectivos del rol operator.
	jwt := auth.NewJWTManager(testSigningKey, testIssuer)
	claims, err := jwt.ValidateToken(res.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.TenantID != testTenant {
		t.Fatalf("claim tenant inesperado: %s", claims.TenantID)
	}
	if !auth.EvaluateGrants(claims.Grants, "flows.create") {
		t.Error("se esperaba allow de flows.create (grant flows.*)")
	}
	if !auth.EvaluateGrants(claims.Grants, "messages.send") {
		t.Error("se esperaba allow de messages.send")
	}
	if auth.EvaluateGrants(claims.Grants, "leases.revoke") {
		t.Error("no se esperaba allow de leases.revoke (default DENY)")
	}

	// El refresh quedó persistido por su hash.
	if _, err := store.Refresh.GetByHash(ctx, auth.HashToken(res.RefreshToken)); err != nil {
		t.Fatalf("refresh no persistido: %v", err)
	}
}

func TestLogin_BadPassword(t *testing.T) {
	t.Parallel()
	authSvc, _, _, _ := fixture(t)
	_, err := authSvc.Login(context.Background(), in.LoginInput{Email: testEmail, Password: "wrong"})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("se esperaba ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	t.Parallel()
	authSvc, _, _, _ := fixture(t)
	_, err := authSvc.Login(context.Background(), in.LoginInput{Email: "nope@x.example", Password: testLoginPhrase})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("se esperaba ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_InactiveUser(t *testing.T) {
	t.Parallel()
	authSvc, users, _, u := fixture(t)
	ctx := context.Background()
	if err := users.DeleteUser(ctx, testTenant, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	_, err := authSvc.Login(ctx, in.LoginInput{Email: testEmail, Password: testLoginPhrase})
	// Usuario soft-deleted: FindByEmail lo excluye → credenciales inválidas.
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("se esperaba ErrInvalidCredentials, got %v", err)
	}
}

func TestRefresh_RotatesAndInvalidatesOld(t *testing.T) {
	t.Parallel()
	authSvc, _, _, _ := fixture(t)
	ctx := context.Background()

	first, err := authSvc.Login(ctx, in.LoginInput{Email: testEmail, Password: testLoginPhrase})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	second, err := authSvc.Refresh(ctx, in.RefreshInput{RefreshToken: first.RefreshToken})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("Refresh no rotó el refresh token")
	}
	// El refresh viejo ya no sirve (rotación).
	if _, err := authSvc.Refresh(ctx, in.RefreshInput{RefreshToken: first.RefreshToken}); !errors.Is(err, domain.ErrRefreshInvalid) {
		t.Fatalf("se esperaba ErrRefreshInvalid al reusar el viejo, got %v", err)
	}
}

func TestLogout_RevokesRefresh(t *testing.T) {
	t.Parallel()
	authSvc, _, _, _ := fixture(t)
	ctx := context.Background()

	res, err := authSvc.Login(ctx, in.LoginInput{Email: testEmail, Password: testLoginPhrase})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := authSvc.Logout(ctx, in.LogoutInput{RefreshToken: res.RefreshToken}); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := authSvc.Refresh(ctx, in.RefreshInput{RefreshToken: res.RefreshToken}); !errors.Is(err, domain.ErrRefreshInvalid) {
		t.Fatalf("se esperaba ErrRefreshInvalid tras logout, got %v", err)
	}
}

func TestVerify_ValidAndInvalid(t *testing.T) {
	t.Parallel()
	authSvc, _, _, u := fixture(t)
	ctx := context.Background()

	res, err := authSvc.Login(ctx, in.LoginInput{Email: testEmail, Password: testLoginPhrase})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	v, err := authSvc.Verify(ctx, res.AccessToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !v.Valid || v.TenantID != testTenant || v.Subject != u.ID {
		t.Fatalf("Verify inesperado: %+v", v)
	}
	bad, err := authSvc.Verify(ctx, "not-a-token")
	if err != nil {
		t.Fatalf("Verify(inválido) no debe devolver error: %v", err)
	}
	if bad.Valid {
		t.Fatal("Verify(inválido) debe ser Valid=false")
	}
}
