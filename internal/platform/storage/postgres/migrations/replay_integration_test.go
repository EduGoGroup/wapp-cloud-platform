package migrations

// replay_integration_test.go — EL CRITERIO DEL FULL-REPLAY, EJECUTADO
// (Plan 047 · Ola 5 · T5.1, migración 0086).
//
// 🔴 POR QUÉ ESTE TEST ESTÁ DENTRO DEL PAQUETE Y NO EN `migrations_test`. El
// runner NO reaplica cuando la versión y el hash coinciden (isUpToDate), así que
// llamar a Migrate dos veces desde fuera prueba exactamente lo contrario de lo
// que hay que probar: prueba que la SEGUNDA vez no hizo nada. Lo que el criterio
// pide es que la reejecución REAL del directorio sobre una base CON DATOS deje el
// mismo esquema y no pierda filas — y esa reejecución es `applyStructure`, que es
// privada. De ahí el test interno.
//
// Lo que vigila, en concreto, es el fallo YA SUFRIDO en este repo: un
// `COMMENT ON COLUMN` sobre una columna que no existe mata el SEGUNDO ARRANQUE
// del servidor. No una consulta lejana: el arranque.

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	// pgx/stdlib registra el driver "pgx" en database/sql. Se importa aquí (y no
	// vía internal/platform/storage/postgres) porque ese paquete importa este
	// árbol y el atajo cerraría un ciclo.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// abrirBDDeReplay aplica el esquema sobre la BD de integración. Mismo contrato de
// skip que el resto de *_integration_test.go del repo.
func abrirBDDeReplay(t *testing.T) *sql.DB {
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
	if _, merr := Migrate(ctx, db); merr != nil {
		t.Fatalf("aplicar migraciones: %v", merr)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	return db
}

// TestReplay_UserActiveTenantSobreviveASegundaAplicacion es el criterio literal
// de T5.1: aplicar la migración DOS VECES SEGUIDAS sobre una base CON DATOS deja
// el mismo esquema y las mismas filas.
//
// Las tres mitades importan y ninguna sobra:
//
//	(a) la reejecución NO REVIENTA — es donde moriría un `COMMENT ON COLUMN`
//	    sobre una columna ausente, y donde muere el arranque del servidor;
//	(b) la FILA SIGUE AHÍ y con el mismo valor — un `DROP TABLE` o un `TRUNCATE`
//	    descuidado dentro de un fichero de estructura pasaría (a) y perdería los
//	    datos de todo el mundo en el siguiente reinicio;
//	(c) el ESQUEMA no se movió — columnas, nulabilidad y la PK sobre `user_id` A
//	    SECAS. Si la PK saliera compuesta, la preferencia se habría convertido en
//	    una lista y el canje volvería a tener que elegir entre dos filas.
func TestReplay_UserActiveTenantSobreviveASegundaAplicacion(t *testing.T) {
	db := abrirBDDeReplay(t)
	ctx := context.Background()

	// Un tenant real (la FK lo exige) y un usuario cualquiera: el user_id no
	// tiene FK, la persona vive en identity.
	var tenantID string
	slug := "replay-0086-" + time.Now().Format("20060102150405.000000000")
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.tenants (slug, display_name) VALUES ($1, 'Replay 0086')
		RETURNING id::text`, slug).Scan(&tenantID); err != nil {
		t.Fatalf("sembrar tenant: %v", err)
	}
	t.Cleanup(func() {
		// El ON DELETE CASCADE se lleva la fila de user_active_tenant con él.
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant de prueba: %v", err)
		}
	})

	const userID = "0a0a0a0a-0000-4000-8000-00000000a086"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.user_active_tenant (user_id, tenant_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id`, userID, tenantID); err != nil {
		t.Fatalf("sembrar empresa activa: %v", err)
	}

	esquemaAntes := esquemaDe(t, db, "user_active_tenant")
	pkAntes := columnasDeLaPK(t, db, "public.user_active_tenant")

	// (a) LA SEGUNDA APLICACIÓN, completa y de verdad. Es lo que el runner hace
	// al arrancar el servidor cuando el hash del conjunto cambia.
	if err := applyStructure(ctx, db); err != nil {
		t.Fatalf("la SEGUNDA aplicación del directorio falló: %v\n"+
			"Es el fallo que mata el arranque del servidor, no una consulta lejana.", err)
	}

	// (b) la fila sigue, y con su valor.
	var tenantDespues string
	if err := db.QueryRowContext(ctx,
		`SELECT tenant_id::text FROM public.user_active_tenant WHERE user_id = $1`, userID).Scan(&tenantDespues); err != nil {
		t.Fatalf("la fila NO sobrevivió al replay: %v", err)
	}
	if tenantDespues != tenantID {
		t.Fatalf("la empresa activa cambió con el replay: %q → %q", tenantID, tenantDespues)
	}

	// (c) el esquema es el mismo.
	esquemaDespues := esquemaDe(t, db, "user_active_tenant")
	if esquemaAntes != esquemaDespues {
		t.Fatalf("el esquema de user_active_tenant CAMBIÓ con el replay.\nantes:   %s\ndespués: %s",
			esquemaAntes, esquemaDespues)
	}
	pkDespues := columnasDeLaPK(t, db, "public.user_active_tenant")
	if pkAntes != pkDespues {
		t.Fatalf("la PK cambió con el replay: %q → %q", pkAntes, pkDespues)
	}
	if pkAntes != "user_id" {
		t.Fatalf("la PK de user_active_tenant es %q y tiene que ser user_id A SECAS: "+
			"con una PK compuesta, una persona podría tener DOS empresas activas y el canje "+
			"volvería a tener que elegir entre ellas", pkAntes)
	}

	// Y el barrido de PII (V4 de la migración): ni una columna de texto donde
	// quepa un correo. Es una consulta sobre el ESQUEMA, así que no caduca.
	var texto int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = 'user_active_tenant'
		   AND data_type IN ('text','character varying','character')`).Scan(&texto); err != nil {
		t.Fatalf("barrido de PII: %v", err)
	}
	if texto != 0 {
		t.Errorf("user_active_tenant tiene %d columna(s) de texto: se diseñó para no tener NINGUNA (ADR-0034)", texto)
	}
}

// esquemaDe devuelve una huella textual de las columnas de la tabla (nombre,
// tipo, nulabilidad y default), en orden. Comparar la huella entera y no columna
// a columna es lo que hace que este test note un cambio que nadie previó.
func esquemaDe(t *testing.T, db *sql.DB, tabla string) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT column_name || ' ' || data_type || ' null=' || is_nullable ||
		       ' def=' || COALESCE(column_default, '-')
		  FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1
		 ORDER BY ordinal_position`, tabla)
	if err != nil {
		t.Fatalf("leyendo el esquema de %s: %v", tabla, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas: %v", cerr)
		}
	}()
	huella := ""
	for rows.Next() {
		var linea string
		if err := rows.Scan(&linea); err != nil {
			t.Fatalf("escaneando el esquema: %v", err)
		}
		huella += linea + " | "
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorriendo el esquema: %v", err)
	}
	// GUARDA ANTI-HUECO: una huella vacía sería "igual" a otra huella vacía y
	// este test pasaría comparando dos nadas.
	if huella == "" {
		t.Fatalf("la tabla %s no tiene columnas (¿existe?): la comparación no probaría nada", tabla)
	}
	return huella
}

// columnasDeLaPK devuelve las columnas de la PRIMARY KEY, separadas por coma.
func columnasDeLaPK(t *testing.T, db *sql.DB, tabla string) string {
	t.Helper()
	var cols string
	err := db.QueryRowContext(context.Background(), `
		SELECT string_agg(a.attname, ',' ORDER BY a.attnum)
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		 WHERE c.conrelid = $1::regclass AND c.contype = 'p'`, tabla).Scan(&cols)
	if err != nil {
		t.Fatalf("leyendo la PK de %s: %v", tabla, err)
	}
	return cols
}
