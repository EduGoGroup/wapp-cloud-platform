package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// TestIntegration_Migracion0070_LasDosColumnasEnClaroNoVuelven cubre los criterios
// (a) y (b) de T5.4 (Plan 046 · Ola 5, D-046.17) contra Postgres real: tras aplicar
// el esquema no queda ni `fleet_sessions.self_pn` ni `contacts.push_name`, y un
// SEGUNDO replay completo tampoco las devuelve.
//
// 🔴 POR QUÉ EL SEGUNDO REPLAY ES EL TEST, Y NO UN EXTRA. El runner es FULL-REPLAY:
// cuando cambia el hash del directorio reaplica TODOS los `structure/*.sql` en orden.
// Eso significa que en cada arranque la 0005/0006 vuelven a declarar `push_name` en
// su CREATE TABLE y la 0028 vuelve a hacer `ADD COLUMN IF NOT EXISTS self_pn` — y
// solo el hecho de que la 0070 vaya DESPUÉS hace que el ciclo converja a «no están».
// Si alguien renumerara esta migración por encima de cualquiera de esas cuatro, el
// estado final del arranque sería con las columnas VIVAS otra vez y el saneo del plan
// se desharía solo, en silencio, sin un error en ningún log. Este test es lo único
// que ata ese orden: no comprueba una migración, comprueba una INVARIANTE DE ORDEN.
//
// 💥 MUTACIÓN EJECUTADA EL 2026-08-21, y el resultado no fue el que este comentario
// predecía: renombrada a 0027_… el test sale ROJO, pero NO por las columnas de vuelta
// — falla ANTES, en el propio Migrate, con «column "self_pn_enc" of relation
// "public.fleet_sessions" does not exist». Es aún más contundente: por encima de la
// 0068, esta migración ni siquiera puede aplicarse, porque comenta columnas del sobre
// que todavía no existen. El orden mal puesto no degrada el saneo: TUMBA EL ARRANQUE.
// Se deja el dato medido y no la predicción, porque una viñeta que promete un rojo
// distinto del que ocurre enseña a buscar el fallo donde no está.
//
// Hermano de TestIntegration_Migracion0063Profile_ReplayNoPisaElPerfil, con el que
// comparte helpers (reaplicarMigracion, forzarReplay): la 0064 hace este mismo ciclo
// con `role` desde la Ola 1.
func TestIntegration_Migracion0070_LasDosColumnasEnClaroNoVuelven(t *testing.T) {
	db := openTestDB(t)

	// 🔴 EL Migrate EXPLÍCITO NO SOBRA. openTestDB solo abre el pool: NO aplica el
	// esquema. Sin esta línea el test pasaba en `make test-integration` —porque otro
	// test del paquete había migrado antes, con -p 1 y en el mismo proceso— y
	// reventaba al correrlo solo. Un test que depende del orden de sus vecinos es un
	// test que un día cambia de veredicto sin que cambie el código.
	if _, err := migrations.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migración inicial: %v", err)
	}

	// Criterio (a) sobre el esquema recién aplicado.
	afirmarColumnaRetirada(t, db, "fleet_sessions", "self_pn")
	afirmarColumnaRetirada(t, db, "contacts", "push_name")

	// Criterio (b): un replay completo del directorio —el que ocurre en producción
	// en cuanto alguien toca un structure/*.sql— no las devuelve.
	reaplicarMigracion(t, db, "primer replay tras la 0070")
	afirmarColumnaRetirada(t, db, "fleet_sessions", "self_pn")
	afirmarColumnaRetirada(t, db, "contacts", "push_name")

	// Y el segundo: «dos arranques seguidos dejan el esquema igual» es literal, y es
	// donde se vería una migración que alternara (crear en uno, borrar en el otro).
	reaplicarMigracion(t, db, "segundo replay tras la 0070")
	afirmarColumnaRetirada(t, db, "fleet_sessions", "self_pn")
	afirmarColumnaRetirada(t, db, "contacts", "push_name")

	// Las columnas del SOBRE siguen ahí: el DROP se llevó el claro y NADA MÁS. Sin
	// esta mitad, un `DROP COLUMN self_pn_enc` por error pasaría este test entero.
	for _, col := range []string{"self_pn_enc", "self_pn_dek", "self_pn_kek_id", "self_pn_bidx"} {
		afirmarColumnaViva(t, db, "fleet_sessions", col)
	}
	for _, col := range []string{"push_name_enc", "push_name_dek", "push_name_kek_id"} {
		afirmarColumnaViva(t, db, "contacts", col)
	}
}

// contarColumna devuelve cuántas filas tiene information_schema para esa columna: 1
// si existe, 0 si no. Es la única fuente que responde por el ESTADO DEL CATÁLOGO —un
// SELECT de la columna diría lo mismo por la vía de fallar, pero mezclaría «no
// existe» con «no tengo permiso» y con «la tabla no está».
func contarColumna(t *testing.T, db *sql.DB, tabla, columna string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
	`, tabla, columna).Scan(&n); err != nil {
		t.Fatalf("consultar information_schema por %s.%s: %v", tabla, columna, err)
	}
	return n
}

// afirmarColumnaRetirada es el criterio (a) sobre UNA columna.
func afirmarColumnaRetirada(t *testing.T, db *sql.DB, tabla, columna string) {
	t.Helper()
	if n := contarColumna(t, db, tabla, columna); n != 0 {
		t.Fatalf("public.%s.%s sigue existiendo: la 0070 no se aplicó, o el replay la recreó DESPUÉS "+
			"de ella (mira el número de la migración: tiene que ir por debajo de la 0005, la 0006, "+
			"la 0028, la 0068 y la 0069)", tabla, columna)
	}
}

// afirmarColumnaViva es la mitad que impide que este test pase por exceso de celo.
func afirmarColumnaViva(t *testing.T, db *sql.DB, tabla, columna string) {
	t.Helper()
	if n := contarColumna(t, db, tabla, columna); n != 1 {
		t.Fatalf("public.%s.%s NO existe, y tenía que sobrevivir: el DROP de la 0070 se llevó por "+
			"delante una columna del sobre cifrado, que es donde vive el dato de verdad", tabla, columna)
	}
}
