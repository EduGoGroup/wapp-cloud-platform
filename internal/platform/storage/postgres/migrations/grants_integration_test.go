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
