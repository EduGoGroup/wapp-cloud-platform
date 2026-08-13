package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	// pgx/stdlib registra el driver "pgx" en database/sql. Se importa aquí
	// (y no vía internal/platform/storage/postgres) porque ese paquete importa
	// este árbol y el atajo cerraría un ciclo.
	_ "github.com/jackc/pgx/v5/stdlib"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// openGrantsTestDB aplica el esquema sobre la BD de integración. Mismo contrato
// de skip que el resto de *_integration_test.go del repo.
func openGrantsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WAPP_TEST_DB_DSN")
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatal("WAPP_TEST_DB_DSN no definido pero WAPP_TEST_REQUIRE_DB exige BD")
		}
		t.Skip("WAPP_TEST_DB_DSN no definido: se omiten los tests de integración con BD")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("abrir BD: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible (%v) pero WAPP_TEST_REQUIRE_DB exige BD", err)
		}
		t.Skipf("BD no disponible (%v): se omiten", err)
	}
	if _, merr := migrations.Migrate(ctx, db); merr != nil {
		t.Fatalf("aplicar migraciones: %v", merr)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	return db
}

// TestSeed_OperatorTieneLosScopesDeLectura afirma la regla que este repo tiene
// escrita en internal/publicapi/publicapi.go (cabecera de
// registerConversationEvents): «un scope nuevo no lo tiene nadie hasta que una
// migración se lo conceda al rol operator».
//
// Sin ella, estrenar un scope deja la ruta montada y devolviendo 403 a la única
// persona que la necesita, y NINGÚN test lo nota: los tests de handler inyectan
// la identidad con el grant ya puesto a mano, así que pasan igual con el seed
// vacío. Esta es la única prueba del árbol que mira el SEED real.
//
// Se verifica contra los IDs de rol canónicos de 0015_iam_roles.sql (roles =
// PLANTILLAS globales, tenant_id NULL); tenant_admin ('*') y viewer ('*.read')
// no aparecen porque los cubre el glob — lo que hay que defender es
// exactamente el rol SIN glob amplio.
func TestSeed_OperatorTieneLosScopesDeLectura(t *testing.T) {
	db := openGrantsTestDB(t)
	const operatorRoleID = "10000000-0000-0000-0000-000000000002"

	// Cada scope de lectura estrenado por un plan y su migración de grant.
	scopes := map[string]string{
		"sessions.read":         "0030",
		"entitlements.read":     "0040",
		"intakes.read":          "0042",
		"events_telemetry.read": "0057 (Plan 043 · T6.5)",
	}
	for scope, origen := range scopes {
		var n int
		err := db.QueryRow(`
			SELECT count(*) FROM public.iam_role_grants
			 WHERE role_id = $1 AND pattern = $2 AND effect = 'allow'
		`, operatorRoleID, scope).Scan(&n)
		if err != nil {
			t.Fatalf("consultar grant %q: %v", scope, err)
		}
		if n != 1 {
			t.Fatalf("el rol canónico `operator` NO tiene el grant %q (migración %s): count=%d.\n"+
				"Estrenar un scope sin su migración de grant deja la ruta montada devolviendo 403 "+
				"al rol operativo del tenant, en silencio.", scope, origen, n)
		}
	}
}

// TestPlatformAdminGrants_Integration cierra el circuito del plano de
// plataforma (ADR-0039) contra el SEED REAL: lee de Postgres los grants que
// 0059_platform_admin.sql siembra para tenant_admin y para platform_admin, y
// los pasa por el MISMO evaluador que usa el middleware en producción.
//
// Es el complemento de internal/platform/httpapi/adminroute_test.go, que
// ejercita el middleware con grants escritos a mano en el test. Aquí se
// verifica la otra mitad — que la BD dice de verdad lo que ese test supone —,
// que es exactamente la costura donde se abrió el agujero: la creencia de que
// "el '*' de tenant_admin ya cubre tenants.revoke" era CIERTA, y solo mirando
// el seed real se ve que sigue siéndolo hasta que el deny existe.
func TestPlatformAdminGrants_Integration(t *testing.T) {
	db := openGrantsTestDB(t)
	const (
		tenantAdminRoleID   = "10000000-0000-0000-0000-000000000001"
		platformAdminRoleID = "10000000-0000-0000-0000-000000000004"
	)

	// loadGrants arma un rbac.Grants con los patrones sembrados para un rol.
	loadGrants := func(roleID string) identityrbac.Grants {
		t.Helper()
		rows, err := db.Query(`
			SELECT pattern, effect FROM public.iam_role_grants WHERE role_id = $1
		`, roleID)
		if err != nil {
			t.Fatalf("consultar grants del rol %s: %v", roleID, err)
		}
		defer func() {
			if cerr := rows.Close(); cerr != nil {
				t.Logf("cerrando filas de grants del rol %s: %v", roleID, cerr)
			}
		}()

		var g identityrbac.Grants
		for rows.Next() {
			var pattern, effect string
			if serr := rows.Scan(&pattern, &effect); serr != nil {
				t.Fatalf("escanear grant: %v", serr)
			}
			if effect == "deny" {
				g.Deny = append(g.Deny, pattern)
			} else {
				g.Allow = append(g.Allow, pattern)
			}
		}
		if rerr := rows.Err(); rerr != nil {
			t.Fatalf("recorrer grants: %v", rerr)
		}
		return g
	}

	// El rol platform_admin tiene que EXISTIR: sin él no hay quien revoque.
	platformAdmin := loadGrants(platformAdminRoleID)
	if len(platformAdmin.Allow) == 0 {
		t.Fatal("el rol `platform_admin` (0059_platform_admin.sql) no tiene grants sembrados: " +
			"nadie podría cortar a un tenant moroso (REQ-055.7)")
	}

	tenantAdmin := loadGrants(tenantAdminRoleID)

	for _, perm := range []string{"tenants.revoke.any", "tenants.restore.any"} {
		if identityrbac.EvaluateGrants(tenantAdmin, perm) {
			t.Errorf("los grants sembrados de `tenant_admin` PERMITEN %q (allow=%v deny=%v).\n"+
				"El administrador de una empresa cliente podría cortar a otra: falta el deny "+
				"'*.any' de 0059_platform_admin.sql o alguien lo retiró.", perm, tenantAdmin.Allow, tenantAdmin.Deny)
		}
		if !identityrbac.EvaluateGrants(platformAdmin, perm) {
			t.Errorf("los grants sembrados de `platform_admin` NIEGAN %q (allow=%v deny=%v)",
				perm, platformAdmin.Allow, platformAdmin.Deny)
		}
	}

	// El deny es por FORMA ('*.any'), así que hay que probar que no se llevó por
	// delante lo que tenant_admin ya podía hacer — en particular el OTRO
	// kill-switch, el anti-clon por instalación (ADR-0007).
	for _, perm := range []string{"leases.revoke", "flows.create", "messages.send"} {
		if !identityrbac.EvaluateGrants(tenantAdmin, perm) {
			t.Errorf("los grants sembrados de `tenant_admin` ya NO permiten %q: "+
				"el deny del plano de plataforma se llevó por delante un permiso legítimo del tenant", perm)
		}
	}
}

// TestPlatformTenant_Integration afirma que el tenant OPERADOR existe con el id
// FIJO que la plataforma trae por defecto en config (PlatformTenantID). Los dos
// tienen que hablar del mismo tenant: si el seed cambiara de id y la config no,
// el handler devolvería 403 a la propia wApp y el kill-switch comercial no
// podría dispararse desde ningún token.
func TestPlatformTenant_Integration(t *testing.T) {
	db := openGrantsTestDB(t)
	const platformTenantID = "55550000-0000-0000-0000-000000000055"

	var slug string
	if err := db.QueryRow(`SELECT slug FROM public.tenants WHERE id = $1`, platformTenantID).Scan(&slug); err != nil {
		t.Fatalf("el tenant de plataforma %s no está sembrado (0059_platform_admin.sql): %v.\n"+
			"Sin él nadie puede emitir un token que pase el cerrojo de RevokeTenantHandler.", platformTenantID, err)
	}
	if slug != "wapp-platform" {
		t.Fatalf("slug del tenant de plataforma = %q, want wapp-platform", slug)
	}
}
