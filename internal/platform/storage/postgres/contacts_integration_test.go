package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// columnExists reporta si public.<table>.<column> existe (helper de smoke test
// de esquema).
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&ok); err != nil {
		t.Fatalf("columna %s.%s: %v", table, column, err)
	}
	return ok
}

// TestIntegration_ContactsSchema verifica que las migraciones crean la tabla
// contacts CIFRADA (Plan 011 · 0006: value_bidx/value_enc/value_dek, sin value
// plano), RE-CLAVEAN flow_state a contact_id (clean-slate, §10.C), deduplican por
// ref (PK sobre value_bidx) y son idempotentes. DSN-gated: sin WAPP_TEST_DB_DSN
// hace Skip.
func TestIntegration_ContactsSchema(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migración: %v", err)
	}

	// contacts existe tras migrar.
	var contactsExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'contacts'
	)`).Scan(&contactsExists); err != nil {
		t.Fatalf("comprobando contacts: %v", err)
	}
	if !contactsExists {
		t.Fatal("la tabla public.contacts debería existir tras migrar")
	}

	// flow_state re-claveado: tiene contact_id (UUID) y NO conserva contact.
	if !columnExists(t, db, "flow_state", "contact_id") {
		t.Fatal("flow_state debería tener la columna contact_id tras el re-claveado")
	}
	if columnExists(t, db, "flow_state", "contact") {
		t.Fatal("flow_state NO debería conservar la columna contact (clean-slate §10.C)")
	}

	// El tipo de contact_id debe ser UUID.
	var dataType string
	if err := db.QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
		WHERE table_schema='public' AND table_name='flow_state' AND column_name='contact_id'`).Scan(&dataType); err != nil {
		t.Fatalf("tipo de contact_id: %v", err)
	}
	if dataType != "uuid" {
		t.Fatalf("flow_state.contact_id data_type = %q, want uuid", dataType)
	}

	// contacts CIFRADA (Plan 011 · 0006): NO conserva la columna `value` plano.
	if columnExists(t, db, "contacts", "value") {
		t.Fatal("contacts NO debería conservar la columna value en claro (cifrado 0006)")
	}

	// Dedup por ref: la PK (tenant_id, kind, value_bidx) rechaza duplicados.
	var tenantID string
	slug := fmt.Sprintf("plan010-%d", time.Now().UnixNano())
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id`,
		slug, "Plan010").Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	const insertCipher = `INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek)
		VALUES ($1, 'phone_e164', 'bidx-14155552671', '\x00'::bytea, '\x00'::bytea)`
	if _, err := db.ExecContext(ctx, insertCipher, tenantID); err != nil {
		t.Fatalf("primer insert de contact: %v", err)
	}
	if _, err := db.ExecContext(ctx, insertCipher, tenantID); err == nil {
		t.Fatal("insertar la MISMA ref (tenant,kind,value_bidx) debería violar la PK (dedup)")
	}

	// Idempotencia: re-migrar no rompe y reporta Skipped.
	second, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("segunda migración: %v", err)
	}
	if !second.Skipped {
		t.Fatal("la segunda migración debería marcarse como Skipped (idempotencia)")
	}
}
