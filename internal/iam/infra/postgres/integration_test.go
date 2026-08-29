package iampostgres_test

import (
	"context"
	"database/sql"
	"errors"
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
	// ACOTADA AL TENANT, y no global. Hasta el Plan 047 · Ola 5 · T5.6 aquí iba un
	// `nil` que hacía la asignación global; era gratis porque nada lo impedía, y
	// era justo el defecto que T5.6 cierra — un rol de empresa asignado con
	// tenant_id NULL vale en TODAS las empresas del titular. Lo que el test mide
	// (que RolesOfUser devuelve ese rol para ese tenant) no cambia.
	if err := roles.AssignToUser(ctx, userID, role.ID, &env.tenantID); err != nil {
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
	// El nombre lleva sufijo único (mismo patrón que otherSlug, abajo) porque un
	// rol GLOBAL vive en `tenant_id IS NULL`, donde no hay tenant recién sembrado
	// que lo aísle: el índice parcial iam_roles_global_name_uidx (0015) lo hace
	// único en TODA la base, y un nombre fijo hacía que el test pasara contra una
	// BD virgen y reventara contra la misma BD por segunda vez.
	globalName := fmt.Sprintf("global-role-it-%d", time.Now().UnixNano())
	roleGlobal, err := roles.Create(ctx, domain.Role{Name: globalName})
	if err != nil {
		t.Fatalf("crear global role: %v", err)
	}
	// La fila GLOBAL se escribe por SQL directo, igual que la acotada de más
	// abajo. Desde el Plan 047 · Ola 5 · T5.6 la vía de producto reserva el
	// tenant_id NULL al rol transversal (domain.RolTransversalID), y lo que este
	// test mide es la RESOLUCIÓN de una fila global —que sigue siendo la conducta
	// correcta de RolesOfUser—, no el permiso para crearla.
	if _, err := env.db.ExecContext(ctx,
		`INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1, $2, NULL)`,
		userID, roleGlobal.ID); err != nil {
		t.Fatalf("sembrar la asignación global: %v", err)
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
	members := iampostgres.NewMembershipRepo(env.db, nil)

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

// TestIntegration_GrantTenantAccess_EsElAltaCompleta ejercita contra Postgres el
// caso de uso compartido por las dos vías de alta (Plan 047 · Ola 1.0 · T1.0-2):
// membresía Y rol en una sola llamada, que es lo que la bandeja del operador
// hacía a mano y ahora delega.
//
// Se llama con el *sql.DB directamente —sin transacción— a propósito: lo que se
// prueba aquí es el SQL, no la atomicidad. La atomicidad de la vía del operador
// ya tiene su propio test y este no la duplica
// (platformadmin/executeapprovaltx_internal_test.go).
func TestIntegration_GrantTenantAccess_EsElAltaCompleta(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	members := iampostgres.NewMembershipRepo(env.db, nil)

	userID := uuid.NewString()
	role, err := roles.Create(ctx, domain.Role{TenantID: &env.tenantID, Name: "acceso-it"})
	if err != nil {
		t.Fatalf("crear rol: %v", err)
	}

	// Dos veces seguidas: el alta es idempotente entera, ni duplica membresía ni
	// duplica rol (los dos INSERT llevan ON CONFLICT DO NOTHING).
	for vuelta := range 2 {
		if err := iampostgres.GrantTenantAccess(ctx, env.db, nil, userID, env.tenantID, &role.ID); err != nil {
			t.Fatalf("GrantTenantAccess (vuelta %d): %v", vuelta, err)
		}
		tenants, terr := members.TenantsOfUser(ctx, userID)
		if terr != nil {
			t.Fatalf("TenantsOfUser: %v", terr)
		}
		if len(tenants) != 1 || tenants[0] != env.tenantID {
			t.Fatalf("membresías = %v, quiero [%s]", tenants, env.tenantID)
		}
		asignados, rerr := roles.RolesOfUser(ctx, userID, env.tenantID)
		if rerr != nil {
			t.Fatalf("RolesOfUser: %v", rerr)
		}
		if len(asignados) != 1 || asignados[0].ID != role.ID {
			t.Fatalf("roles = %+v, quiero solo %s", asignados, role.ID)
		}
	}
}

// TestIntegration_GrantTenantAccess_LaGuardaCortaAntesDelRol: la segunda empresa
// se rechaza, y el rechazo ocurre ANTES de escribir nada — rol incluido.
func TestIntegration_GrantTenantAccess_LaGuardaCortaAntesDelRol(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	userID := uuid.NewString()

	if err := iampostgres.GrantTenantAccess(ctx, env.db, nil, userID, env.tenantID, nil); err != nil {
		t.Fatalf("GrantTenantAccess (primera empresa): %v", err)
	}

	otherSlug := fmt.Sprintf("iam-it-acceso-otra-%d", time.Now().UnixNano())
	otherTn, err := postgres.NewTenantRepository(env.db).Create(ctx, otherSlug, "Otra")
	if err != nil {
		t.Fatalf("sembrar other tenant: %v", err)
	}
	otherRole, err := roles.Create(ctx, domain.Role{TenantID: &otherTn.ID, Name: "acceso-it-otra"})
	if err != nil {
		t.Fatalf("crear other role: %v", err)
	}

	err = iampostgres.GrantTenantAccess(ctx, env.db, nil, userID, otherTn.ID, &otherRole.ID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("segunda empresa: err = %v, quiero domain.ErrConflict", err)
	}
	asignados, err := roles.RolesOfUser(ctx, userID, otherTn.ID)
	if err != nil {
		t.Fatalf("RolesOfUser: %v", err)
	}
	if len(asignados) != 0 {
		t.Fatalf("el rechazo debía cortar ANTES del rol: %+v", asignados)
	}
}

// TestIntegration_GrantTenantAccess_SinRol: roleID nil es la vía del plano de
// administración del tenant — da la membresía y NO toca iam_user_roles.
func TestIntegration_GrantTenantAccess_SinRol(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	userID := uuid.NewString()

	if err := iampostgres.GrantTenantAccess(ctx, env.db, nil, userID, env.tenantID, nil); err != nil {
		t.Fatalf("GrantTenantAccess (sin rol): %v", err)
	}
	tenants, err := iampostgres.NewMembershipRepo(env.db, nil).TenantsOfUser(ctx, userID)
	if err != nil || len(tenants) != 1 {
		t.Fatalf("membresías = %v err=%v", tenants, err)
	}
	asignados, err := roles.RolesOfUser(ctx, userID, env.tenantID)
	if err != nil {
		t.Fatalf("RolesOfUser: %v", err)
	}
	if len(asignados) != 0 {
		t.Fatalf("sin roleID no debía asignarse ningún rol: %+v", asignados)
	}
}

// TestIntegration_MembersOf ejercita contra Postgres REAL la lectura inversa que
// estrena el Plan 047 · Ola 1.0 · T1.0-4: los miembros de UN tenant.
//
// 🔴 Este test no es opcional aunque el listado ya tenga unitarios: los
// unitarios corren contra el doble en memoria y NO ejecutan una sola línea de
// este SQL. Un `WHERE` que se olvida, un `::text` que falta o un ORDER BY sobre
// una columna que no existe compilan igual de bien y darían verde en todo lo
// demás — el SQL solo se prueba ejecutándolo.
//
// Se siembran DOS tenants a propósito: con uno solo, una consulta sin WHERE
// devolvería exactamente lo mismo que la correcta.
func TestIntegration_MembersOf(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	members := iampostgres.NewMembershipRepo(env.db, nil)

	// Una empresa sin nadie devuelve lista vacía, no error: los tenants nacen
	// antes que su gente.
	iniciales, err := members.MembersOf(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("MembersOf (empresa vacía): %v", err)
	}
	if len(iniciales) != 0 {
		t.Fatalf("una empresa recién creada debería venir sin miembros, got %v", iniciales)
	}

	vecina := sembrarTenantVecino(t, env)
	primero, segundo, ajeno := uuid.NewString(), uuid.NewString(), uuid.NewString()
	sembrarMiembro(t, env, primero, env.tenantID)
	sembrarMiembro(t, env, segundo, env.tenantID)
	sembrarMiembro(t, env, ajeno, vecina)

	propios, err := members.MembersOf(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("MembersOf: %v", err)
	}
	if len(propios) != 2 {
		t.Fatalf("MembersOf devolvió %d filas, want 2: %+v", len(propios), propios)
	}
	exigirFilasPropias(t, propios, env.tenantID)
	vistos := idsDeMembresias(propios)
	if !vistos[primero] || !vistos[segundo] {
		t.Errorf("faltan miembros propios en %+v", propios)
	}
	if vistos[ajeno] {
		t.Error("MembersOf devolvió el miembro de la OTRA empresa: fuga de aislamiento")
	}

	// La baja se refleja en la lectura: es la comprobación de que el listado lee
	// de la misma tabla en la que escribe Remove, y no de otro sitio.
	if err := members.Remove(ctx, primero, env.tenantID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tras, err := members.MembersOf(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("MembersOf tras la baja: %v", err)
	}
	if len(tras) != 1 || tras[0].UserID != segundo {
		t.Errorf("tras la baja de %s el listado es %+v, want solo %s", primero, tras, segundo)
	}
}

// sembrarTenantVecino crea una SEGUNDA empresa en la misma base. Sin ella, una
// consulta a la que se le olvidara el WHERE devolvería lo mismo que la correcta.
func sembrarTenantVecino(t *testing.T, env itEnv) string {
	t.Helper()
	tn, err := postgres.NewTenantRepository(env.db).Create(context.Background(),
		fmt.Sprintf("iam-it-vecina-%d", time.Now().UnixNano()), "IAM IT vecina")
	if err != nil {
		t.Fatalf("sembrar el segundo tenant: %v", err)
	}
	return tn.ID
}

// sembrarMiembro escribe una fila de tenant_members a mano: lo que se prueba es
// la LECTURA, así que la siembra no pasa por el repositorio.
func sembrarMiembro(t *testing.T, env itEnv, userID, tenantID string) {
	t.Helper()
	if _, err := env.db.ExecContext(context.Background(),
		`INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2)`,
		userID, tenantID); err != nil {
		t.Fatalf("sembrar membresía (%s): %v", userID, err)
	}
}

// exigirFilasPropias comprueba lo que TODA fila devuelta tiene que cumplir: ser
// del tenant pedido y traer la fecha de alta, que es la columna sobre la que se
// apoya el ORDER BY.
func exigirFilasPropias(t *testing.T, filas []domain.Membership, tenantID string) {
	t.Helper()
	for _, m := range filas {
		if m.TenantID != tenantID {
			t.Errorf("una fila trae tenant_id=%q, want %q: el WHERE no acota", m.TenantID, tenantID)
		}
		if m.CreatedAt.IsZero() {
			t.Errorf("la fila %s viene sin created_at: el ORDER BY se apoya en esa columna", m.UserID)
		}
	}
}

// idsDeMembresias indexa por user_id para preguntar por presencia sin recorrer.
func idsDeMembresias(filas []domain.Membership) map[string]bool {
	vistos := make(map[string]bool, len(filas))
	for _, m := range filas {
		vistos[m.UserID] = true
	}
	return vistos
}
