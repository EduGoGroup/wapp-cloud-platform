package receipts_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/receipts"
)

// dsnEnv habilita los tests de integración con BD real (mismo gate que el resto
// del repo: iam/lease/store/contact).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openIT abre la BD (o salta) y aplica migraciones (incluye 0022_message_receipts).
func openIT(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7): la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD (Plan 027 · Ola 1 · T7)", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migraciones: %v", err)
	}
	return db
}

// TestPostgresStore_SaveIdempotentAndList verifica el dedupe por (session,
// message, status) sobre BD real y el listado por sesión.
func TestPostgresStore_SaveIdempotentAndList(t *testing.T) {
	db := openIT(t)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar BD: %v", err)
		}
	}()
	ctx := context.Background()

	store := receipts.NewPostgresStore(db)
	// session_id único por corrida para no chocar con datos previos.
	sess := "it-sess-" + time.Now().Format("150405.000000")

	r := receipts.Receipt{SessionID: sess, CommandID: "cmd-it", MessageID: "msg-it", Status: receipts.StatusDelivered, ReceiptAt: time.Unix(1_700_000_000, 0)}
	for i := 0; i < 3; i++ {
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("Save #%d: %v", i, err)
		}
	}
	got, err := store.List(ctx, sess, 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("acuse duplicado en BD: got %d filas, want 1 (ON CONFLICT)", len(got))
	}

	// read del mismo mensaje = fila distinta.
	if err := store.Save(ctx, receipts.Receipt{SessionID: sess, MessageID: "msg-it", Status: receipts.StatusRead}); err != nil {
		t.Fatalf("Save read: %v", err)
	}
	got, err = store.List(ctx, sess, 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("delivered+read en BD: got %d filas, want 2", len(got))
	}
}
