package tenantvars_test

import (
	"context"
	"database/sql"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la BD de integración y aplica el esquema. Sin DSN se salta (el
// mismo contrato que el resto de los *_integration_test.go del repo).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// tenantAislado devuelve un tenant_id único del test y limpia sus filas al
// terminar. tenant_variables.tenant_id es TEXT sin FK (como tenant_content): no
// hace falta sembrar la fila de tenants.
func tenantAislado(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := "vars-" + uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_variables WHERE tenant_id = $1`, id); err != nil {
			t.Logf("limpiando variables de %s: %v", id, err)
		}
	})
	return id
}

// listaComoMapa aplana el resultado del store para compararlo de un vistazo.
func listaComoMapa(t *testing.T, st *tenantvars.Postgres, tenantID string) map[string]string {
	t.Helper()
	rows, err := st.List(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("listando variables: %v", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out
}

// TestMigracion_DobleAplicacion_NoRompeNiBorra prueba de VERDAD el criterio de
// T2.1 contra Postgres, no por lectura del SQL.
//
// Detalle que importa: llamar a Migrate dos veces seguidas NO probaría nada — el
// runner ve el mismo hash y responde Skipped sin tocar la BD. Lo que hay que
// reproducir es el FULL-REPLAY real: alguien cambia CUALQUIER structure/*.sql, el
// hash deja de coincidir y se reejecutan TODOS los archivos sobre una BD que ya
// tiene el esquema y DATOS. Aquí se fuerza ese camino ensuciando el hash
// registrado, y se comprueba lo que de verdad duele: que la segunda pasada no
// falla y que las variables ya guardadas SIGUEN AHÍ (un CREATE TABLE sin IF NOT
// EXISTS reventaría; un DROP/CREATE se llevaría las filas por delante).
func TestMigracion_DobleAplicacion_NoRompeNiBorra(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := tenantvars.NewPostgres(db)
	tenant := tenantAislado(t, db)

	quiero := map[string]string{"moneda": "Bs", "saludo": "¡Hola, Ñandú!"}
	if err := st.Replace(ctx, tenant, quiero); err != nil {
		t.Fatalf("sembrando variables: %v", err)
	}

	// Se ensucia el hash registrado ⇒ la próxima Migrate reejecuta TODO.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.schema_version (version, content_hash, description)
		VALUES ('0.0.0-replay', 'hash-forzado-por-test', 'T2.1: fuerza el FULL-REPLAY')
	`); err != nil {
		t.Fatalf("forzando el replay: %v", err)
	}

	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("segunda aplicación del esquema: %v", err)
	}
	if res.Skipped {
		t.Fatalf("el replay no llegó a ejecutarse (Skipped=true): el test no probaría nada")
	}

	if got := listaComoMapa(t, st, tenant); !maps.Equal(got, quiero) {
		t.Fatalf("el replay se llevó datos por delante: got=%#v, quiero=%#v", got, quiero)
	}
	// Y la tabla sigue usable tras el replay.
	if err := st.Replace(ctx, tenant, map[string]string{"moneda": "USD"}); err != nil {
		t.Fatalf("escribiendo tras el replay: %v", err)
	}
}

// TestPostgres_Roundtrip_Verbatim: acentos, espacios en los bordes y un JSON
// dentro de una cadena sobreviven al viaje sin que nadie los interprete (D-041.1).
func TestPostgres_Roundtrip_Verbatim(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := tenantvars.NewPostgres(db)
	tenant := tenantAislado(t, db)

	quiero := map[string]string{
		"moneda":         "Bs",
		"saludo":         "¡Hola! ¿Qué tal? — Panadería Ñandú",
		"aviso":          "   dos espacios a cada lado   ",
		"config_externa": `{"a":1,"b":["x","y"],"c":null}`,
		"vacio":          "",
	}
	if err := st.Replace(ctx, tenant, quiero); err != nil {
		t.Fatalf("guardando: %v", err)
	}
	if got := listaComoMapa(t, st, tenant); !maps.Equal(got, quiero) {
		t.Fatalf("roundtrip alteró el contenido: got=%#v, quiero=%#v", got, quiero)
	}

	// List devuelve ordenado por clave (contrato del store).
	rows, err := st.List(ctx, tenant)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Key > rows[i].Key {
			t.Fatalf("List no vino ordenada por clave: %q antes que %q", rows[i-1].Key, rows[i].Key)
		}
	}
}

// TestPostgres_Replace_ReemplazaElConjunto: altas, cambios y BORRADO de lo que no
// viene, incluido el conjunto vacío (que es donde muerde el detalle del array
// NO-NIL: con un nil, `key <> ALL(NULL)` no borraría nada).
func TestPostgres_Replace_ReemplazaElConjunto(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := tenantvars.NewPostgres(db)
	tenant := tenantAislado(t, db)

	if err := st.Replace(ctx, tenant, map[string]string{"moneda": "Bs", "envio": "gratis", "tel": "soporte"}); err != nil {
		t.Fatalf("primer guardado: %v", err)
	}
	if err := st.Replace(ctx, tenant, map[string]string{"moneda": "USD", "nueva": "sí"}); err != nil {
		t.Fatalf("reemplazo: %v", err)
	}
	quiero := map[string]string{"moneda": "USD", "nueva": "sí"}
	if got := listaComoMapa(t, st, tenant); !maps.Equal(got, quiero) {
		t.Fatalf("tras el reemplazo: got=%#v, quiero=%#v", got, quiero)
	}

	if err := st.Replace(ctx, tenant, map[string]string{}); err != nil {
		t.Fatalf("vaciado: %v", err)
	}
	if got := listaComoMapa(t, st, tenant); len(got) != 0 {
		t.Fatalf("el vaciado dejó filas: %#v", got)
	}
}

// TestPostgres_Aislamiento_PorTenant (INV-8): el conjunto de un tenant es suyo y
// el reemplazo TOTAL de otro no lo roza.
func TestPostgres_Aislamiento_PorTenant(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := tenantvars.NewPostgres(db)
	tenantA := tenantAislado(t, db)
	tenantB := tenantAislado(t, db)

	varsA := map[string]string{"moneda": "Bs", "secreto_de_a": "solo-a"}
	if err := st.Replace(ctx, tenantA, varsA); err != nil {
		t.Fatalf("guardando A: %v", err)
	}
	if got := listaComoMapa(t, st, tenantB); len(got) != 0 {
		t.Fatalf("el tenant B ve variables ajenas: %#v", got)
	}

	// El PUT de B reemplaza el conjunto ENTERO de B: el DELETE va acotado a B.
	if err := st.Replace(ctx, tenantB, map[string]string{"moneda": "USD"}); err != nil {
		t.Fatalf("guardando B: %v", err)
	}
	if got := listaComoMapa(t, st, tenantA); !maps.Equal(got, varsA) {
		t.Fatalf("el reemplazo de B pisó a A: got=%#v, quiero=%#v", got, varsA)
	}
	// Y vaciar B tampoco toca a A.
	if err := st.Replace(ctx, tenantB, map[string]string{}); err != nil {
		t.Fatalf("vaciando B: %v", err)
	}
	if got := listaComoMapa(t, st, tenantA); !maps.Equal(got, varsA) {
		t.Fatalf("el vaciado de B se llevó las de A: %#v", got)
	}
}

// TestPostgres_UpdatedAt_SoloSeMueveAlCambiar: la marca dice cuándo cambió el
// VALOR, no cuándo se pulsó guardar. El Plan 042 se apoya en eso para decidir si
// refresca; un updated_at que se mueve solo obligaría a refrescar de más.
func TestPostgres_UpdatedAt_SoloSeMueveAlCambiar(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	st := tenantvars.NewPostgres(db)
	tenant := tenantAislado(t, db)

	if err := st.Replace(ctx, tenant, map[string]string{"moneda": "Bs", "envio": "gratis"}); err != nil {
		t.Fatalf("primer guardado: %v", err)
	}
	antes, err := st.List(ctx, tenant)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}

	// Mismo valor para "envio", valor nuevo para "moneda".
	if err := st.Replace(ctx, tenant, map[string]string{"moneda": "USD", "envio": "gratis"}); err != nil {
		t.Fatalf("segundo guardado: %v", err)
	}
	despues, err := st.List(ctx, tenant)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}

	marca := func(rows []tenantvars.Variable, key string) time.Time {
		t.Helper()
		for _, r := range rows {
			if r.Key == key {
				return r.UpdatedAt
			}
		}
		t.Fatalf("no está la variable %q", key)
		return time.Time{}
	}
	if !marca(despues, "envio").Equal(marca(antes, "envio")) {
		t.Fatalf("el valor de envio no cambió: su updated_at no debía moverse (%v → %v)",
			marca(antes, "envio"), marca(despues, "envio"))
	}
	if !marca(despues, "moneda").After(marca(antes, "moneda")) {
		t.Fatalf("moneda cambió de valor: su updated_at debía avanzar (%v → %v)",
			marca(antes, "moneda"), marca(despues, "moneda"))
	}
}
