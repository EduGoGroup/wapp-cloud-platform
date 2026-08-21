package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// TestIntegration_Migracion0063Profile_ReplayNoPisaElPerfil es el criterio (a) y
// (b) de T1.1 (Plan 046 · Ola 1) ejecutado contra Postgres real: la idempotencia de
// una migración no se prueba leyéndola.
//
// Lo que se ejerce, en este orden:
//
//	(b) BACKFILL — sobre filas legadas (solo con `role`), el primer apply de la 0063
//	    deja profile alineado byte a byte: role='bot' ⟺ profile='active'. Se mide con
//	    el count del criterio, acotado al tenant sembrado aquí para que las filas que
//	    otros tests dejan en la tabla no contaminen el veredicto.
//
//	(a) REPLAY — una fila creada ENTRE los dos applies conserva su profile en el
//	    segundo. La fila se siembra con role y profile DELIBERADAMENTE
//	    CONTRADICTORIOS (role='passive', profile='active'): así el guard
//	    `WHERE profile IS NULL` de la 0063 es lo ÚNICO que impide que el UPDATE del
//	    replay recalcule el perfil desde el rol y lo voltee. Quien quiera ver el test
//	    en rojo solo tiene que borrar ese WHERE de la migración.
//
// Y de propina el DEFAULT: tras el replay, una fila insertada sin nombrar la
// columna nace PASIVA (D-07) y la columna sigue NOT NULL.
//
// La BD se lleva a un estado PRE-046 dropeando la columna: es la única forma de
// ejercer el backfill, porque cuando este test corre la migración ya se aplicó al
// abrir la BD. El Cleanup la restaura con la propia migración. Es seguro porque la
// integración corre con `-p 1` (Makefile): ningún otro paquete toca la tabla a la vez.
//
// 🔧 Nota de forma (CLI, 2026-08-20): el cuerpo estaba escrito como una secuencia
// lineal de comprobaciones y `golangci-lint` lo paró por `gocyclo` (23 > 15). Las
// afirmaciones se extrajeron a funciones NOMBRADAS —no a subtests con closures, que
// no bajan la métrica porque gocyclo imputa los FuncLit a la función madre—. El
// orden y las aserciones son los mismos.
func TestIntegration_Migracion0063Profile_ReplayNoPisaElPerfil(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migración inicial: %v", err)
	}

	tenant := sembrarTenantPerfil(t, db)
	sufijo := uuid.NewString()[:8]

	// El Cleanup se registra ANTES de romper nada (LIFO: restaura la columna y solo
	// después se borra el tenant, que arrastra sus filas por CASCADE).
	t.Cleanup(func() { restaurarColumnaProfile(t, db) })
	llevarAEstadoPre046(t, db)

	// Dos filas LEGADAS: una por cada valor del eje viejo.
	sembrarSesionLegada(t, db, tenant, "edge-legado", "sess-legada-bot-"+sufijo, "bot")
	sembrarSesionLegada(t, db, tenant, "edge-legado", "sess-legada-passive-"+sufijo, "passive")

	// ---- PRIMER apply: crea la columna y backfillea ----
	reaplicarMigracion(t, db, "primer apply de la 0063")

	// Criterio (b): el backfill no inventa ni pierde semántica.
	if n := contarDesalineadas(t, db, tenant); n != 0 {
		t.Fatalf("el backfill dejó %d filas donde role y profile no significan lo mismo (want 0)", n)
	}

	// ---- Fila creada ENTRE los dos applies, con los dos ejes en contradicción ----
	sesionNueva := "sess-entre-applies-" + sufijo
	sembrarSesionContradictoria(t, db, tenant, sesionNueva)

	// ---- SEGUNDO apply (el replay del arranque siguiente) ----
	reaplicarMigracion(t, db, "replay de la 0063")

	if perfil := leerPerfil(t, db, tenant, sesionNueva); perfil != "active" {
		t.Fatalf("el replay PISÓ el perfil de una fila creada entre los dos applies: got %q, want active. "+
			"El guard `WHERE profile IS NULL` de la 0063 no está haciendo su trabajo", perfil)
	}

	// El backfill tampoco tocó a las legadas en la segunda pasada.
	if n := contarDesalineadas(t, db, tenant); n != 1 {
		t.Fatalf("tras el replay debería quedar EXACTAMENTE la fila contradictoria sembrada a mano, got %d", n)
	}

	afirmarDefaultYNotNull(t, db, tenant, sufijo)
	afirmarCheckConNombre(t, db, tenant, sufijo)
}

// llevarAEstadoPre046 dropea la columna para poder ejercer el backfill: cuando este
// test corre, la migración ya se aplicó al abrir la BD.
func llevarAEstadoPre046(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE public.fleet_sessions DROP COLUMN IF EXISTS profile`); err != nil {
		t.Fatalf("llevar la tabla a su estado pre-046: %v", err)
	}
}

// restaurarColumnaProfile devuelve la tabla a su estado real con la propia
// migración. Va en un Cleanup, así que informa con Logf y no aborta.
func restaurarColumnaProfile(t *testing.T, db *sql.DB) {
	t.Helper()
	forzarReplay(t, db)
	if _, err := migrations.Migrate(context.Background(), db); err != nil {
		t.Logf("restaurando la columna profile con la migración: %v", err)
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

// contarDesalineadas es el count del criterio (b), acotado al tenant del test.
func contarDesalineadas(t *testing.T, db *sql.DB, tenantID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM public.fleet_sessions
		WHERE tenant_id = $1 AND (role = 'bot') <> (profile = 'active')
	`, tenantID).Scan(&n); err != nil {
		t.Fatalf("contar filas desalineadas: %v", err)
	}
	return n
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

// sembrarSesionContradictoria inserta la fila clave del criterio (a): los dos ejes
// en desacuerdo a propósito, para que solo el guard del backfill la salve.
func sembrarSesionContradictoria(t *testing.T, db *sql.DB, tenantID, sessionID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, role, profile, last_seen_at, updated_at)
		VALUES ($1, 'edge-nuevo', $2, 'online', 'passive', 'active', now(), now())
	`, tenantID, sessionID); err != nil {
		t.Fatalf("sembrar la fila creada entre los dos applies: %v", err)
	}
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
// replay (corrección del code review 2026-08-20). Es justo lo que la versión inline
// del design.md NO garantizaba: el `ADD COLUMN IF NOT EXISTS` se salta en el segundo
// apply y con él se saltaba el CHECK, que no volvía a crearse nunca. Con el patrón
// DROP+ADD de la 0025, cada replay lo RESTAURA. Se afirma por el nombre y por el
// comportamiento.
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

// sembrarSesionLegada inserta una fila como las que existían ANTES de la 0063: con
// `role` y sin `profile` (la columna ni siquiera existe en ese punto del test).
func sembrarSesionLegada(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, role string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, role, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', $4, now(), now())
	`, tenantID, edgeID, sessionID, role); err != nil {
		t.Fatalf("sembrar sesión legada (role=%s): %v", role, err)
	}
}
