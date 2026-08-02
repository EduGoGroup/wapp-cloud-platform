package iampostgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (mismo gate que el resto
// del repo: lease/store/contact).
const dsnEnv = "WAPP_TEST_DB_DSN"

// itEnv agrupa el pool y un tenant recién sembrado para un test de integración.
type itEnv struct {
	db       *sql.DB
	tenantID string
}

// newITEnv abre la BD (o salta), aplica migraciones y siembra un tenant único.
func newITEnv(t *testing.T) itEnv {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7): la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7)", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	slug := fmt.Sprintf("iam-it-%d", time.Now().UnixNano())
	tn, err := postgres.NewTenantRepository(db).Create(ctx, slug, "IAM IT")
	if err != nil {
		t.Fatalf("sembrar tenant: %v", err)
	}
	return itEnv{db: db, tenantID: tn.ID}
}

func TestIntegration_Users(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	users := iampostgres.NewUserRepo(env.db)

	u, err := users.Create(ctx, domain.User{TenantID: env.tenantID, Email: "op@it.example", PasswordHash: "hash", IsActive: true})
	if err != nil {
		t.Fatalf("crear usuario: %v", err)
	}
	if _, err := users.Create(ctx, domain.User{TenantID: env.tenantID, Email: "op@it.example", PasswordHash: "x", IsActive: true}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("se esperaba ErrConflict por email duplicado, got %v", err)
	}
	if got, err := users.GetByID(ctx, u.ID); err != nil || got.Email != "op@it.example" {
		t.Fatalf("GetByID: got=%+v err=%v", got, err)
	}
	if _, err := users.FindByEmail(ctx, "op@it.example"); err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if err := users.SoftDelete(ctx, env.tenantID, u.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := users.GetByID(ctx, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("tras soft-delete GetByID debía ser ErrNotFound, got %v", err)
	}
}

func TestIntegration_RolesAndGrants(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	users := iampostgres.NewUserRepo(env.db)
	roles := iampostgres.NewRoleRepo(env.db)
	grants := iampostgres.NewGrantRepo(env.db)

	u, err := users.Create(ctx, domain.User{TenantID: env.tenantID, Email: "roles@it.example", PasswordHash: "h", IsActive: true})
	if err != nil {
		t.Fatalf("crear usuario: %v", err)
	}
	role, err := roles.Create(ctx, domain.Role{TenantID: &env.tenantID, Name: "operator-it"})
	if err != nil {
		t.Fatalf("crear rol: %v", err)
	}
	// AddGrant idempotente (dos veces, sin error, un solo registro).
	for range 2 {
		if err := roles.AddGrant(ctx, role.ID, domain.Grant{Pattern: "flows.*", Effect: domain.EffectAllow}); err != nil {
			t.Fatalf("AddGrant: %v", err)
		}
	}
	if gs, err := roles.GrantsOf(ctx, role.ID); err != nil || len(gs) != 1 {
		t.Fatalf("GrantsOf: %+v err=%v", gs, err)
	}
	if err := roles.AssignToUser(ctx, u.ID, role.ID); err != nil {
		t.Fatalf("AssignToUser: %v", err)
	}
	if rs, err := roles.RolesOfUser(ctx, u.ID); err != nil || len(rs) != 1 {
		t.Fatalf("RolesOfUser: %+v err=%v", rs, err)
	}
	if err := grants.AddUserGrant(ctx, u.ID, domain.Grant{Pattern: "flows.delete", Effect: domain.EffectDeny}); err != nil {
		t.Fatalf("AddUserGrant: %v", err)
	}
	ug, err := grants.GrantsOfUser(ctx, u.ID)
	if err != nil || len(ug) != 1 || ug[0].Effect != domain.EffectDeny {
		t.Fatalf("GrantsOfUser: %+v err=%v", ug, err)
	}
}

func TestIntegration_RefreshLifecycle(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	users := iampostgres.NewUserRepo(env.db)
	refresh := iampostgres.NewRefreshRepo(env.db)

	u, err := users.Create(ctx, domain.User{TenantID: env.tenantID, Email: "rt@it.example", PasswordHash: "h", IsActive: true})
	if err != nil {
		t.Fatalf("crear usuario: %v", err)
	}
	if err := refresh.Save(ctx, domain.RefreshToken{UserID: u.ID, TokenHash: "hash-abc", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt, err := refresh.GetByHash(ctx, "hash-abc")
	if err != nil || rt.RevokedAt != nil {
		t.Fatalf("GetByHash: %+v err=%v", rt, err)
	}
	if err := refresh.Revoke(ctx, "hash-abc"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	revoked, err := refresh.GetByHash(ctx, "hash-abc")
	if err != nil {
		t.Fatalf("GetByHash tras revoke: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("el refresh debía quedar revocado")
	}
}

func TestIntegration_APIKeyLifecycle(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	apikeys := iampostgres.NewAPIKeyRepo(env.db)

	k, err := apikeys.Create(ctx, domain.APIKey{
		TenantID: env.tenantID, ClientID: "client-it", KeyHash: "keyhash-xyz",
		Scopes: []string{"messages.send", "flows.*"}, IsActive: true,
	})
	if err != nil {
		t.Fatalf("crear api-key: %v", err)
	}
	got, err := apikeys.GetByHash(ctx, "keyhash-xyz")
	if err != nil || len(got.Scopes) != 2 {
		t.Fatalf("GetByHash: scopes=%v err=%v", got.Scopes, err)
	}
	if err := apikeys.Revoke(ctx, env.tenantID, k.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	revoked, err := apikeys.GetByHash(ctx, "keyhash-xyz")
	if err != nil {
		t.Fatalf("GetByHash tras revoke: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("la api-key debía quedar revocada")
	}
}

func TestIntegration_Audit(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	audit := iampostgres.NewAuditRepo(env.db)

	if err := audit.Record(ctx, domain.AuditEvent{
		TenantID: &env.tenantID, Actor: "actor-id", Action: "auth.login", Resource: "auth", Result: "ok",
		Meta: map[string]any{"endpoint": "/login"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events, err := audit.List(ctx, env.tenantID, 10, 0)
	if err != nil || len(events) != 1 || events[0].Action != "auth.login" {
		t.Fatalf("List: %+v err=%v", events, err)
	}
}

// TestIntegration_Memberships ejercita el SQL que lee tenant_members (migración
// 0037): la tabla que resuelve el tenant del canje cuando los usuarios ya viven
// en identity. La siembra imita a la del propio 0037 —la membresía se hereda del
// usuario— pero la escribe a mano, porque lo que se prueba aquí es la LECTURA.
func TestIntegration_Memberships(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	members := iampostgres.NewMembershipRepo(env.db)

	u, err := iampostgres.NewUserRepo(env.db).Create(ctx, domain.User{
		TenantID: env.tenantID, Email: "miembro@it.example", PasswordHash: "hash", IsActive: true,
	})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Sin fila en tenant_members la lista viene vacía, y eso NO es un error: es
	// el caso «usuario sin membresía», que el canje traduce a «no migrado».
	tenants, err := members.TenantsOfUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("TenantsOfUser (sin membresía): %v", err)
	}
	if len(tenants) != 0 {
		t.Fatalf("sin membresía debería devolver lista vacía, got %v", tenants)
	}

	if _, err := env.db.ExecContext(ctx,
		`INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2)`,
		u.ID, env.tenantID); err != nil {
		t.Fatalf("sembrar membresía: %v", err)
	}

	tenants, err = members.TenantsOfUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(tenants) != 1 || tenants[0] != env.tenantID {
		t.Fatalf("membresías = %v, want [%s]", tenants, env.tenantID)
	}

	// Re-insertar la misma pareja no duplica (PK compuesta): el canje seguiría
	// viendo UN tenant, no dos.
	if _, err := env.db.ExecContext(ctx,
		`INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		u.ID, env.tenantID); err != nil {
		t.Fatalf("re-sembrar membresía: %v", err)
	}
	tenants, err = members.TenantsOfUser(ctx, u.ID)
	if err != nil || len(tenants) != 1 {
		t.Fatalf("membresías tras el duplicado = %v err=%v", tenants, err)
	}
}
