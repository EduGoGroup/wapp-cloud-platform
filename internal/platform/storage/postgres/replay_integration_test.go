package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// TestIntegration_FullReplay_ConservaLosDatos es la PRUEBA de la doctrina que
// gobierna cuándo hay que subir SchemaVersion (ver migrations/version.go, el
// CLAUDE.md y el README de este repo).
//
// Lo que se afirma ahí y aquí se ejecuta: el runner decide reaplicar por versión Y
// hash de contenido, así que tocar un structure/*.sql SIN mover SchemaVersion
// dispara igualmente el full-replay — y ese replay, sobre una BD CON DATOS, no
// pierde una sola fila, porque todo el DDL es idempotente. De ahí que una ola
// intermedia de un plan pueda añadir migraciones sin bump.
//
// Se simula el cambio de hash alterando el registrado en public.schema_version, que
// es exactamente el estado en el que queda una BD cuando alguien edita un .sql sin
// tocar la constante.
func TestIntegration_FullReplay_ConservaLosDatos(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migración inicial: %v", err)
	}

	// Dato de negocio real (no una tabla de juguete): una solicitud con su línea y
	// su revisión, que es justo lo que la migración 0045 acaba de tocar.
	tenant := uuid.NewString()
	intakeID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total)
		VALUES ($1, $2, 'contacto-opaco', 'sesion-1', 'closed', 5000)
	`, intakeID, tenant); err != nil {
		t.Fatalf("sembrar solicitud: %v", err)
	}
	t.Cleanup(func() {
		// Las líneas van PRIMERO: su FK (0012) no lleva ON DELETE CASCADE, a
		// diferencia de intake_revisions e intake_buyer_data (0045). Las revisiones
		// sí caen solas con la cabecera.
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intake_items WHERE intake_id = $1`, intakeID); err != nil {
			t.Logf("limpiando líneas: %v", err)
		}
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando solicitud: %v", err)
		}
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
		VALUES ($1, 'emp-pino', 'Empanada de pino', 2, 2500)
	`, intakeID); err != nil {
		t.Fatalf("sembrar línea: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_revisions (intake_id, revision_no, kind, payload, created_by)
		VALUES ($1, 1, 'cart', '{"version":1,"total":5000,"items":[]}'::jsonb, 'system')
	`, intakeID); err != nil {
		t.Fatalf("sembrar revisión: %v", err)
	}

	// Alterar SOLO el hash registrado, dejando la versión intacta: es el estado de
	// una BD cuya migración cambió sin bump.
	if _, err := db.ExecContext(ctx, `
		UPDATE public.schema_version SET content_hash = 'hash-alterado'
		WHERE id = (SELECT id FROM public.schema_version ORDER BY id DESC LIMIT 1)
	`); err != nil {
		t.Fatalf("alterar el hash registrado: %v", err)
	}

	rec, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("replay con hash alterado: %v", err)
	}
	if rec.Skipped {
		t.Fatal("con el hash alterado el runner DEBE reaplicar, no saltarse la migración")
	}
	if rec.Version != migrations.SchemaVersion {
		t.Fatalf("versión registrada tras el replay: got %q, want %q", rec.Version, migrations.SchemaVersion)
	}

	// Y las filas siguen ahí: el full-replay no es destructivo.
	var items, revisiones int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_items WHERE intake_id = $1`, intakeID).Scan(&items); err != nil {
		t.Fatalf("contar líneas tras el replay: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.intake_revisions WHERE intake_id = $1`, intakeID).Scan(&revisiones); err != nil {
		t.Fatalf("contar revisiones tras el replay: %v", err)
	}
	if items != 1 || revisiones != 1 {
		t.Fatalf("el replay perdió datos: líneas=%d (want 1), revisiones=%d (want 1)", items, revisiones)
	}
}
