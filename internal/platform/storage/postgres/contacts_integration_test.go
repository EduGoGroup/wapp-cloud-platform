package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// TestIntegration_ContactsSchema verifica que la migración 0005 crea la tabla
// contacts, RE-CLAVEA flow_state a contact_id (clean-slate, §10.C), deduplica por
// ref (PK) y es idempotente. DSN-gated: sin WAPP_TEST_DB_DSN hace Skip.
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
	var hasContactID, hasContact bool
	if err := db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='flow_state' AND column_name='contact_id'),
		EXISTS(SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='flow_state' AND column_name='contact')
	`).Scan(&hasContactID, &hasContact); err != nil {
		t.Fatalf("columnas de flow_state: %v", err)
	}
	if !hasContactID {
		t.Fatal("flow_state debería tener la columna contact_id tras el re-claveado")
	}
	if hasContact {
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

	// Dedup por ref: la PK (tenant_id, kind, value) rechaza duplicados.
	var tenantID string
	slug := fmt.Sprintf("plan010-%d", time.Now().UnixNano())
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id`,
		slug, "Plan010").Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO public.contacts (tenant_id, kind, value) VALUES ($1, 'phone_e164', '14155552671')`,
		tenantID); err != nil {
		t.Fatalf("primer insert de contact: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO public.contacts (tenant_id, kind, value) VALUES ($1, 'phone_e164', '14155552671')`,
		tenantID); err == nil {
		t.Fatal("insertar la MISMA ref (tenant,kind,value) debería violar la PK (dedup)")
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
