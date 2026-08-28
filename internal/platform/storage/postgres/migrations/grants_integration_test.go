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
	// Delega en el helper de paquete (el mismo que usan los tests de INV-10):
	// un solo lector del seed, un solo formato.
	loadGrants := func(roleID string) identityrbac.Grants {
		return loadRoleGrants(t, db, roleID)
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

// ---------------------------------------------------------------------------
// Plan 047 · Ola 1.0 · T1.0-3 — el plano de roles del tenant y la cerca INV-10.
// ---------------------------------------------------------------------------

// IDs de los roles canónicos (0015_iam_roles.sql) y del rol de plataforma
// (0059_platform_admin.sql).
const (
	roleTenantAdmin   = "10000000-0000-0000-0000-000000000001"
	roleOperator      = "10000000-0000-0000-0000-000000000002"
	roleViewer        = "10000000-0000-0000-0000-000000000003"
	rolePlatformAdmin = "10000000-0000-0000-0000-000000000004"
)

// rolePlaneScopes son los cuatro scopes que 0084 define para el plano 2 del
// ADR-0033 (las tablas iam_* y tenant_members del PROPIO tenant).
var rolePlaneScopes = []string{"roles.read", "roles.write", "members.read", "members.write"}

// platformPlaneScopes son los del plano de plataforma: los dos del kill-switch
// comercial (0059) y los cinco de la consola (0060). Todos '.any'.
var platformPlaneScopes = []string{
	"tenants.revoke.any", "tenants.restore.any",
	"tenants.read.any", "tenants.create.any", "fleet.read.any",
	"users.provision.any", "enrollment.issue.any",
}

// loadRoleGrants arma un rbac.Grants con los patrones SEMBRADOS para un rol,
// leídos de la BD real. Es el mismo formato que el emisor de tokens mete en el
// claim (usecase.grantsToAuth) y que evalúa el middleware en producción.
func loadRoleGrants(t *testing.T, db *sql.DB, roleID string) identityrbac.Grants {
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

// TestRolePlaneScopes_Integration verifica la mitad del criterio de T1.0-3 que
// habla de los roles del TENANT, contra el seed real y con el evaluador de
// producción: tenant_admin alcanza los cuatro scopes por su glob, viewer lee y
// NO escribe, y operator no alcanza ninguno.
//
// Lo del operator no es un descuido convertido en test: 0084 lo excluye a
// propósito (roles.write es escalada directa — quien asigna roles se asigna
// tenant_admin) y este assert es lo que hace ruido si alguien se lo concede sin
// leer ese porqué. No es vacuo: si mañana el seed le añadiera 'roles.write'
// allow al operator, esta comprobación se pone roja.
func TestRolePlaneScopes_Integration(t *testing.T) {
	db := openGrantsTestDB(t)

	tenantAdmin := loadRoleGrants(t, db, roleTenantAdmin)
	viewer := loadRoleGrants(t, db, roleViewer)
	operator := loadRoleGrants(t, db, roleOperator)

	// tenant_admin: los CUATRO, por su '*' (0015). Es el destinatario de las
	// pantallas T1.2/T1.3/T1.4.
	for _, scope := range rolePlaneScopes {
		if !identityrbac.EvaluateGrants(tenantAdmin, scope) {
			t.Errorf("`tenant_admin` NO alcanza %q (allow=%v deny=%v): la dueña de la empresa "+
				"no podría administrar su propio equipo", scope, tenantAdmin.Allow, tenantAdmin.Deny)
		}
	}

	// viewer: lee por '*.read' (0015) y NO escribe.
	for _, scope := range []string{"roles.read", "members.read"} {
		if !identityrbac.EvaluateGrants(viewer, scope) {
			t.Errorf("`viewer` NO alcanza %q (allow=%v deny=%v): '*.read' debería cubrirlo",
				scope, viewer.Allow, viewer.Deny)
		}
	}
	for _, scope := range []string{"roles.write", "members.write"} {
		if identityrbac.EvaluateGrants(viewer, scope) {
			t.Errorf("`viewer` PUEDE %q (allow=%v deny=%v): un rol de solo lectura estaría "+
				"cambiando quién puede qué en la empresa", scope, viewer.Allow, viewer.Deny)
		}
	}

	// operator: ninguno de los cuatro (decisión escrita en 0084).
	for _, scope := range rolePlaneScopes {
		if identityrbac.EvaluateGrants(operator, scope) {
			t.Errorf("`operator` alcanza %q (allow=%v deny=%v). 0084 lo excluye a propósito: "+
				"quien asigna roles puede asignarse tenant_admin. Si la concesión es deliberada, "+
				"cámbiala en la migración Y aquí, con el porqué escrito.",
				scope, operator.Allow, operator.Deny)
		}
	}
}

// TestINV10_LosDosPerimetrosSiguenAislados_Integration es el corazón del criterio
// de T1.0-3: la cerca del Plan 056 en los CUATRO cruces, contra el seed real.
//
//	(1) tenant_admin  -> plano de PLATAFORMA  = NIEGA   (cruce ilegítimo)
//	(2) platform_admin -> plano de TENANT     = NIEGA   (cruce ilegítimo)
//	(3) tenant_admin  -> plano de TENANT      = PERMITE (legítimo)
//	(4) platform_admin -> plano de PLATAFORMA = PERMITE (legítimo)
//
// Los dos legítimos no son decoración: son lo que impide "arreglar" un cruce
// ilegítimo amputando el plano entero. Un deny demasiado ancho pondría (3) o (4)
// en rojo, que es justo lo que 0084 evita al denegar POR NOMBRE y no por forma.
func TestINV10_LosDosPerimetrosSiguenAislados_Integration(t *testing.T) {
	db := openGrantsTestDB(t)

	tenantAdmin := loadRoleGrants(t, db, roleTenantAdmin)
	platformAdmin := loadRoleGrants(t, db, rolePlatformAdmin)

	if len(platformAdmin.Allow) == 0 {
		t.Fatal("`platform_admin` no tiene grants sembrados: la cerca no se puede medir")
	}

	// (1) El administrador de una empresa NO entra en el plano de plataforma.
	// Aquí el deny '*.any' de 0059 es imprescindible y se ve: tenant_admin tiene
	// '*', que sin ese deny cubriría los siete.
	for _, scope := range platformPlaneScopes {
		if identityrbac.EvaluateGrants(tenantAdmin, scope) {
			t.Errorf("CRUCE 1 ABIERTO: `tenant_admin` alcanza %q (allow=%v deny=%v). "+
				"Falta el deny '*.any' de 0059_platform_admin.sql o alguien lo retiró.",
				scope, tenantAdmin.Allow, tenantAdmin.Deny)
		}
	}

	// (2) El operador de plataforma NO entra en la administración de una empresa.
	for _, scope := range rolePlaneScopes {
		if identityrbac.EvaluateGrants(platformAdmin, scope) {
			t.Errorf("CRUCE 2 ABIERTO: `platform_admin` alcanza %q (allow=%v deny=%v)",
				scope, platformAdmin.Allow, platformAdmin.Deny)
		}
	}

	// (2b) …y lo niega por el DENY de 0084, no por casualidad. Sin este bloque el
	// assert de arriba sería DECORADO: hoy platform_admin tampoco tiene ningún
	// allow que case, así que saldría verde con las cuatro filas de 0084
	// borradas. Se le añade a mano el '*' que un día podría ganar la consola
	// (exactamente el agujero que 0059 documenta para el otro lado) y se vuelve a
	// preguntar: si las filas de deny existen, la respuesta sigue siendo no.
	conGlob := identityrbac.Grants{
		Allow: append(append([]string{}, platformAdmin.Allow...), "*"),
		Deny:  append([]string{}, platformAdmin.Deny...),
	}
	for _, scope := range rolePlaneScopes {
		if identityrbac.EvaluateGrants(conGlob, scope) {
			t.Errorf("CRUCE 2 sostenido solo por el default-DENY: con un '*' añadido a "+
				"`platform_admin`, %q queda PERMITIDO. Faltan las filas de deny de "+
				"0084_iam_role_plane_grants.sql.", scope)
		}
	}

	// (3) El cruce legítimo del tenant: sigue abierto en su propio plano, y no
	// solo en los cuatro scopes nuevos — el deny de 0059 es por forma y hay que
	// probar que no se llevó por delante lo de siempre (leases.revoke es el OTRO
	// kill-switch, el anti-clon por instalación del ADR-0007).
	legitimosDelTenant := append(append([]string{}, rolePlaneScopes...),
		"leases.revoke", "flows.create", "messages.send", "intakes.write")
	for _, scope := range legitimosDelTenant {
		if !identityrbac.EvaluateGrants(tenantAdmin, scope) {
			t.Errorf("CRUCE 3 ROTO: `tenant_admin` ya NO alcanza %q (allow=%v deny=%v): "+
				"una cerca se llevó por delante un permiso legítimo del tenant",
				scope, tenantAdmin.Allow, tenantAdmin.Deny)
		}
	}

	// (4) El cruce legítimo de la plataforma: los deny de 0084 no pueden tocar
	// los siete '.any' de la consola (0059/0060). Es lo que se rompería si
	// alguien "endureciera" 0084 cambiando los nombres exactos por 'roles.*' /
	// 'members.*' el día que exista un 'members.read.any' de soporte.
	for _, scope := range platformPlaneScopes {
		if !identityrbac.EvaluateGrants(platformAdmin, scope) {
			t.Errorf("CRUCE 4 ROTO: `platform_admin` ya NO alcanza %q (allow=%v deny=%v): "+
				"un deny del plano de tenant amputó la consola de plataforma",
				scope, platformAdmin.Allow, platformAdmin.Deny)
		}
	}
}

// TestSeed_RolePlaneDenyRows_Integration mira la FILA, no solo la conducta. La
// conducta de (2) ya la cubre el bloque (2b) de arriba, pero el fallo que se ve
// aquí dice qué falta y dónde, en vez de dejar deducirlo de un evaluador.
func TestSeed_RolePlaneDenyRows_Integration(t *testing.T) {
	db := openGrantsTestDB(t)

	for _, scope := range rolePlaneScopes {
		var n int
		if err := db.QueryRow(`
			SELECT count(*) FROM public.iam_role_grants
			 WHERE role_id = $1 AND pattern = $2 AND effect = 'deny'
		`, rolePlatformAdmin, scope).Scan(&n); err != nil {
			t.Fatalf("consultar deny %q: %v", scope, err)
		}
		if n != 1 {
			t.Errorf("falta el deny de `platform_admin` sobre %q "+
				"(0084_iam_role_plane_grants.sql): count=%d. La cerca INV-10 en la dirección "+
				"plataforma→tenant quedaría sostenida solo por el default-DENY.", scope, n)
		}
	}
}
