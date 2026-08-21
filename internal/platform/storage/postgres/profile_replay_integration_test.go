package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// TestIntegration_Migracion0063Profile_ReplayNoPisaElPerfil es el criterio (a) de
// T1.1 (Plan 046 · Ola 1) ejecutado contra Postgres real: la idempotencia de una
// migración no se prueba leyéndola.
//
// Lo que ejerce: una fila con su perfil ya fijado sobrevive INTACTA a un replay
// completo del directorio. El guard `WHERE profile IS NULL` del backfill de la 0063
// es lo ÚNICO que lo impide; quien quiera ver este test en rojo solo tiene que
// borrarlo de la migración. De propina, tras el replay: el DEFAULT sigue siendo
// pasivo (D-07), la columna sigue NOT NULL y el CHECK con NOMBRE sigue puesto — eso
// último es lo que la versión inline del design.md NO garantizaba.
//
// 🔧 QUÉ CAMBIÓ AL RETIRARSE `role` (0064), y qué se perdió con ello
// ------------------------------------------------------------------
// Este test comprobaba también el criterio (b): que el backfill tradujera
// `role='bot'` ⇒ `profile='active'` sobre filas legadas. Eso ya NO es ejercitable a
// través del runner: bajo FULL-REPLAY la 0025 recrea `role`, la 0063 lo lee y la
// 0064 lo borra, todo dentro de la MISMA llamada a Migrate — no hay ningún instante
// observable desde fuera en el que la columna exista. Y sembrar una «fila legada»
// tampoco es posible: la columna no está en el estado final.
//
// 🔴 No se sustituye por un test que finja probarlo. La evidencia del backfill es de
// CAMPO y está registrada: el 2026-08-21 se aplicó a las dos bases Neon con la
// predicción escrita ANTES —UAT 2 filas `bot` ⇒ 2 `active`; dev 3 `bot` + 1 `passive`
// ⇒ 3 `active` + 1 `passive`— y se cumplió en las dos (journal 2026-08-20 §22.2).
// Ese camino ya no puede volver a ejecutarse sobre datos legados: no quedan.
//
// La BD se lleva a un estado conocido sembrando perfiles explícitos. Es seguro
// porque la integración corre con `-p 1` (Makefile): ningún otro paquete toca la
// tabla a la vez.
func TestIntegration_Migracion0063Profile_ReplayNoPisaElPerfil(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migración inicial: %v", err)
	}

	tenant := sembrarTenantPerfil(t, db)
	sufijo := uuid.NewString()[:8]

	// Dos filas con perfiles OPUESTOS y explícitos: si el replay recalculara el
	// perfil desde cualquier otra cosa, una de las dos cambiaría.
	sesionActiva := "sess-activa-" + sufijo
	sesionPasiva := "sess-pasiva-" + sufijo
	sembrarSesionConPerfil(t, db, tenant, "edge-1", sesionActiva, "active")
	sembrarSesionConPerfil(t, db, tenant, "edge-1", sesionPasiva, "passive")

	// ---- REPLAY del directorio entero (hash alterado ⇒ el runner reaplica) ----
	reaplicarMigracion(t, db, "replay del directorio")

	if got := leerPerfil(t, db, tenant, sesionActiva); got != "active" {
		t.Fatalf("el replay PISÓ el perfil de una sesión activa: got %q, want active. "+
			"El guard `WHERE profile IS NULL` de la 0063 no está haciendo su trabajo", got)
	}
	if got := leerPerfil(t, db, tenant, sesionPasiva); got != "passive" {
		t.Fatalf("el replay PISÓ el perfil de una sesión pasiva: got %q, want passive", got)
	}

	afirmarDefaultYNotNull(t, db, tenant, sufijo)
	afirmarCheckConNombre(t, db, tenant, sufijo)
	afirmarRoleRetirada(t, db)
}

// afirmarRoleRetirada comprueba que la 0064 dejó la tabla SIN la columna legada.
//
// 🔴 Va aquí, después de un replay COMPLETO, y no en un test suelto: lo que se
// afirma no es «la 0064 borra la columna» —eso lo haría un DROP cualquiera— sino que
// el estado final del replay NO la tiene. Bajo FULL-REPLAY la 0025 la recrea en cada
// arranque; si algún día la 0064 dejara de aplicarse, o alguien la renumerara por
// debajo de la 0063, la columna reaparecería viva y este test es lo único que lo
// nota.
func afirmarRoleRetirada(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'fleet_sessions' AND column_name = 'role'
	`).Scan(&n); err != nil {
		t.Fatalf("consultar information_schema por la columna role: %v", err)
	}
	if n != 0 {
		t.Fatal("fleet_sessions.role sigue existiendo tras el replay: la 0064 no se aplicó")
	}
}

// reaplicarMigracion fuerza el replay y exige que el runner REAPLIQUE de verdad: si
// se saltara la migración, todo lo que este test afirma después sería vacuo.
func reaplicarMigracion(t *testing.T, db *sql.DB, etapa string) {
	t.Helper()
	forzarReplay(t, db)
	rec, err := migrations.Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("%s: %v", etapa, err)
	}
	if rec.Skipped {
		t.Fatalf("%s: con el hash alterado el runner DEBE reaplicar, no saltarse la migración", etapa)
	}
}

// leerPerfil devuelve el profile de una sesión del tenant del test.
func leerPerfil(t *testing.T, db *sql.DB, tenantID, sessionID string) string {
	t.Helper()
	var perfil string
	if err := db.QueryRowContext(context.Background(), `
		SELECT profile FROM public.fleet_sessions
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID).Scan(&perfil); err != nil {
		t.Fatalf("releer el perfil de %s: %v", sessionID, err)
	}
	return perfil
}

// afirmarDefaultYNotNull comprueba que el DEFAULT (D-07) y el NOT NULL siguen
// puestos TRAS el replay: una sesión nueva nace pasiva.
func afirmarDefaultYNotNull(t *testing.T, db *sql.DB, tenantID, sufijo string) {
	t.Helper()
	ctx := context.Background()
	sesionPorDefecto := "sess-default-" + sufijo
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state)
		VALUES ($1, 'edge-nuevo', $2, 'online')
	`, tenantID, sesionPorDefecto); err != nil {
		t.Fatalf("sembrar una fila sin nombrar profile: %v", err)
	}
	if perfil := leerPerfil(t, db, tenantID, sesionPorDefecto); perfil != "passive" {
		t.Fatalf("perfil por defecto de una sesión nueva (D-07): got %q, want passive", perfil)
	}

	var nullable string
	if err := db.QueryRowContext(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'fleet_sessions' AND column_name = 'profile'
	`).Scan(&nullable); err != nil {
		t.Fatalf("leer la nulabilidad de profile: %v", err)
	}
	if nullable != "NO" {
		t.Fatalf("profile debería quedar NOT NULL tras el replay: is_nullable=%q", nullable)
	}
}

// afirmarCheckConNombre comprueba que el CHECK con NOMBRE sigue puesto TRAS el
// replay. Es justo lo que la versión inline del design.md NO garantizaba: el
// `ADD COLUMN IF NOT EXISTS` se salta en el segundo apply y con él se saltaba el
// CHECK, que no volvía a crearse nunca. Con el patrón DROP+ADD de la 0025, cada
// replay lo RESTAURA. Se afirma por el nombre y por el comportamiento.
func afirmarCheckConNombre(t *testing.T, db *sql.DB, tenantID, sufijo string) {
	t.Helper()
	ctx := context.Background()
	var chks int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.fleet_sessions'::regclass
		  AND contype = 'c' AND conname = 'fleet_sessions_profile_chk'
	`).Scan(&chks); err != nil {
		t.Fatalf("buscar el CHECK con nombre: %v", err)
	}
	if chks != 1 {
		t.Fatalf("fleet_sessions_profile_chk tras el replay: %d constraints, want 1. "+
			"El replay dejó de restaurar el CHECK (¿volvió a ser inline y sin nombre?)", chks)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile)
		VALUES ($1, 'edge-nuevo', $2, 'online', 'supervisor')
	`, tenantID, "sess-chk-"+sufijo); err == nil {
		t.Fatal("el CHECK aceptó un perfil fuera del dominio ('supervisor')")
	}
}

// forzarReplay altera SOLO el hash registrado, dejando la versión intacta: es el
// estado en el que queda una BD cuando alguien edita un structure/*.sql sin tocar
// SchemaVersion, y es lo que obliga al runner a reaplicar (mismo truco que
// TestIntegration_FullReplay_ConservaLosDatos).
func forzarReplay(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		UPDATE public.schema_version SET content_hash = 'hash-alterado-046'
		WHERE id = (SELECT id FROM public.schema_version ORDER BY id DESC LIMIT 1)
	`); err != nil {
		t.Fatalf("alterar el hash registrado: %v", err)
	}
}

// sembrarTenantPerfil crea el tenant dueño de las sesiones del test y registra su
// borrado (que arrastra las fleet_sessions por ON DELETE CASCADE, 0003).
func sembrarTenantPerfil(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	var tenant string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.tenants (slug, display_name)
		VALUES ($1, 'Perfil de sesion 046') RETURNING id::text
	`, "perfil-046-"+uuid.NewString()[:8]).Scan(&tenant); err != nil {
		t.Fatalf("sembrar tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})
	return tenant
}

// sembrarSesionConPerfil inserta una fila con su perfil EXPLÍCITO. No se deja al
// DEFAULT a propósito: el default es pasivo, y una fila sembrada «a pelo» sería un
// caso de prueba distinto del que el test dice estar probando.
func sembrarSesionConPerfil(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, perfil string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, profile, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', $4, now(), now())
	`, tenantID, edgeID, sessionID, perfil); err != nil {
		t.Fatalf("sembrar sesión (profile=%s): %v", perfil, err)
	}
}
