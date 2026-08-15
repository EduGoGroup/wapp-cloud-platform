package iampostgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

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

func TestIntegration_RolesAndGrants(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	grants := iampostgres.NewGrantRepo(env.db)

	// El sujeto es un UUID de identity a secas: en wApp ya no hay fila que
	// crear. Que este INSERT funcione sin usuario local ES la prueba de que la
	// FK hacia iam_users se soltó (migración 0038).
	userID := uuid.NewString()
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
	if err := roles.AssignToUser(ctx, userID, role.ID, nil); err != nil {
		t.Fatalf("AssignToUser: %v", err)
	}
	if rs, err := roles.RolesOfUser(ctx, userID, env.tenantID); err != nil || len(rs) != 1 {
		t.Fatalf("RolesOfUser: %+v err=%v", rs, err)
	}

	if err := grants.AddUserGrant(ctx, userID, domain.Grant{Pattern: "flows.delete", Effect: domain.EffectDeny}); err != nil {
		t.Fatalf("AddUserGrant: %v", err)
	}
	ug, err := grants.GrantsOfUser(ctx, userID)
	if err != nil || len(ug) != 1 || ug[0].Effect != domain.EffectDeny {
		t.Fatalf("GrantsOfUser: %+v err=%v", ug, err)
	}
}

func TestIntegration_TenantScopedUserRoles(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)

	userID := uuid.NewString()
	roleGlobal, err := roles.Create(ctx, domain.Role{Name: "global-role-it"})
	if err != nil {
		t.Fatalf("crear global role: %v", err)
	}
	if err := roles.AssignToUser(ctx, userID, roleGlobal.ID, nil); err != nil {
		t.Fatalf("AssignToUser: %v", err)
	}

	// Asignación acotada a otro tenant no se mezcla en env.tenantID
	otherSlug := fmt.Sprintf("iam-it-other-%d", time.Now().UnixNano())
	otherTn, err := postgres.NewTenantRepository(env.db).Create(ctx, otherSlug, "Other")
	if err != nil {
		t.Fatalf("sembrar other tenant: %v", err)
	}
	otherTenantID := otherTn.ID
	otherRole, err := roles.Create(ctx, domain.Role{TenantID: &otherTenantID, Name: "other-role"})
	if err != nil {
		t.Fatalf("crear other role: %v", err)
	}
	_, err = env.db.ExecContext(ctx, `INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1, $2, $3)`, userID, otherRole.ID, otherTenantID)
	if err != nil {
		t.Fatalf("asignar other role con tenant_id: %v", err)
	}

	// En env.tenantID solo debe verse el rol global, no otherRole
	rsTenant1, err := roles.RolesOfUser(ctx, userID, env.tenantID)
	if err != nil || len(rsTenant1) != 1 || rsTenant1[0].ID != roleGlobal.ID {
		t.Fatalf("RolesOfUser tenant1 esperada 1 fila: %+v err=%v", rsTenant1, err)
	}
	// En otherTenantID debe verse tanto el rol global como otherRole
	rsOther, err := roles.RolesOfUser(ctx, userID, otherTenantID)
	if err != nil || len(rsOther) != 2 {
		t.Fatalf("RolesOfUser otherTenant esperadas 2 filas: %+v err=%v", rsOther, err)
	}
}

func TestIntegration_Audit(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	audit := iampostgres.NewAuditRepo(env.db)

	if err := audit.Record(ctx, domain.AuditEvent{
		TenantID: &env.tenantID, Actor: "actor-id", Action: "auth.exchange", Resource: "auth", Result: "ok",
		Meta: map[string]any{"endpoint": "/exchange"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events, err := audit.List(ctx, env.tenantID, 10, 0)
	if err != nil || len(events) != 1 || events[0].Action != "auth.exchange" {
		t.Fatalf("List: %+v err=%v", events, err)
	}
}

// TestIntegration_Memberships ejercita el SQL que lee tenant_members (migración
// 0037): la tabla que resuelve el tenant del canje ahora que los usuarios viven
// en identity. La siembra se escribe a mano, porque lo que se prueba aquí es la
// LECTURA.
func TestIntegration_Memberships(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	members := iampostgres.NewMembershipRepo(env.db)

	userID := uuid.NewString()

	// Sin fila en tenant_members la lista viene vacía, y eso NO es un error: es
	// el caso «usuario sin empresa todavía», que desde el Plan 056 (D-056.12) el
	// canje traduce a un Context Token SIN tenant y sin grants, no a un 401.
	tenants, err := members.TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("TenantsOfUser (sin membresía): %v", err)
	}
	if len(tenants) != 0 {
		t.Fatalf("sin membresía debería devolver lista vacía, got %v", tenants)
	}

	if _, err := env.db.ExecContext(ctx,
		`INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2)`,
		userID, env.tenantID); err != nil {
		t.Fatalf("sembrar membresía: %v", err)
	}

	tenants, err = members.TenantsOfUser(ctx, userID)
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
		userID, env.tenantID); err != nil {
		t.Fatalf("re-sembrar membresía: %v", err)
	}
	tenants, err = members.TenantsOfUser(ctx, userID)
	if err != nil || len(tenants) != 1 {
		t.Fatalf("membresías tras el duplicado = %v err=%v", tenants, err)
	}
}

// TestIntegration_ElIAMPropioNoSobrevivioALaMigracion verifica sobre BD REAL el
// desenlace de la migración 0038 (identity Plan 003 · T5.1), que es donde vive
// el riesgo de esta ola: no basta con que el DDL se ejecute sin error, porque el
// modo de fallo temido —un `DROP ... CASCADE` que se lleva el RBAC de negocio—
// también se ejecuta sin error.
//
// Así que se comprueban las DOS mitades por separado:
//  1. las tres tablas del IAM propio NO existen tras migrar, y
//  2. las cuatro del RBAC de negocio SÍ, sin ninguna FK colgando de iam_users.
func TestIntegration_ElIAMPropioNoSobrevivioALaMigracion(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()

	for _, table := range []string{"iam_users", "iam_refresh_tokens", "iam_api_keys"} {
		if tableExists(ctx, t, env.db, table) {
			t.Errorf("la tabla %s sigue existiendo tras migrar: el IAM propio de wApp debía morir en la 0038", table)
		}
	}
	for _, table := range []string{"iam_roles", "iam_role_grants", "iam_user_roles", "iam_user_grants"} {
		if !tableExists(ctx, t, env.db, table) {
			t.Errorf("la tabla %s NO existe: es RBAC de NEGOCIO de wApp y tenía que sobrevivir (design.md Ola 5 §1)", table)
		}
	}

	// Ninguna FK puede apuntar ya a iam_users: si quedara una, el siguiente
	// replay de las migraciones fallaría al recrear y borrar la tabla, y peor,
	// un CASCADE tendría por dónde propagarse.
	var dangling int
	const q = `
		SELECT count(*)
		FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
		  ON tc.constraint_name = ccu.constraint_name
		 AND tc.table_schema = ccu.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'public'
		  AND ccu.table_name = 'iam_users'`
	if err := env.db.QueryRowContext(ctx, q).Scan(&dangling); err != nil {
		t.Fatalf("consultando FKs hacia iam_users: %v", err)
	}
	if dangling != 0 {
		t.Errorf("quedan %d FKs apuntando a iam_users: la 0038 debía soltarlas TODAS antes del DROP", dangling)
	}
}

// tableExists responde si una tabla existe en el schema public.
func tableExists(ctx context.Context, t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
		name).Scan(&exists)
	if err != nil {
		t.Fatalf("consultando la existencia de %s: %v", name, err)
	}
	return exists
}
