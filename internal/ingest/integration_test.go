package ingest_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (mismo gate que el resto del
// repo: iam/lease/store/contact/receipts).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openIT abre la BD (o salta) y aplica migraciones (incluye 0031_ingest_dedupe).
func openIT(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD: la integración DEBE correr", dsnEnv)
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
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migraciones: %v", err)
	}
	return db
}

// TestPostgresDeduper_SeenIdempotente verifica el dedupe persistente sobre BD real:
// el primer avistamiento de (session, wamid) devuelve false (nuevo) y cualquier
// repetición devuelve true (duplicado), incluso INTERCALADA con otras claves.
func TestPostgresDeduper_SeenIdempotente(t *testing.T) {
	db := openIT(t)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar BD: %v", err)
		}
	}()
	ctx := context.Background()

	ded := ingest.NewPostgresDeduper(db)
	// session_id único por corrida para no chocar con datos previos.
	sess := "it-sess-" + time.Now().Format("150405.000000")
	a, b := "wamid.a-"+sess, "wamid.b-"+sess

	if seen, err := ded.Seen(ctx, sess, a); err != nil || seen {
		t.Fatalf("primer a: seen=%v err=%v (quiero false,nil)", seen, err)
	}
	if seen, err := ded.Seen(ctx, sess, b); err != nil || seen {
		t.Fatalf("primer b: seen=%v err=%v (quiero false,nil)", seen, err)
	}
	// Reenvío INTERCALADO de a (tras b): la guarda consecutiva NO lo cortaría; el
	// dedupe persistente sí.
	if seen, err := ded.Seen(ctx, sess, a); err != nil || !seen {
		t.Fatalf("reenvío intercalado de a: seen=%v err=%v (quiero true,nil)", seen, err)
	}
}

// TestPostgresDeduper_PodaPerezosa verifica que la poda borra las filas fuera de la
// ventana de retención. Se usa una retención de 0 (todo lo previo a "ahora" vence) y
// una cadencia de 1 (barre en cada Seen) para forzarla de forma determinista.
func TestPostgresDeduper_PodaPerezosa(t *testing.T) {
	db := openIT(t)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("cerrar BD: %v", err)
		}
	}()
	ctx := context.Background()

	sess := "it-poda-" + time.Now().Format("150405.000000")
	// Siembra una fila vieja con una retención grande (no se poda todavía).
	seeder := ingest.NewPostgresDeduper(db)
	if _, err := seeder.Seen(ctx, sess, "wamid.viejo"); err != nil {
		t.Fatalf("sembrar fila: %v", err)
	}
	// Deja pasar un instante para que first_seen_at quede en el pasado.
	time.Sleep(10 * time.Millisecond)

	var before int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.ingest_dedupe WHERE session_id = $1`, sess).Scan(&before); err != nil {
		t.Fatalf("contar antes: %v", err)
	}
	if before == 0 {
		t.Fatalf("la fila sembrada no está presente antes de la poda")
	}

	// Retención 1ns + barrido en cada Seen: el siguiente Seen dispara la poda que
	// borra la fila vieja (first_seen_at < now-1ns). El propio insert nuevo queda.
	pruner := ingest.NewPostgresDeduper(db, ingest.WithRetention(time.Nanosecond), ingest.WithSweep(1, 1000))
	if _, err := pruner.Seen(ctx, sess, "wamid.nuevo"); err != nil {
		t.Fatalf("Seen que dispara la poda: %v", err)
	}

	var oldRemaining int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = 'wamid.viejo'`,
		sess).Scan(&oldRemaining); err != nil {
		t.Fatalf("contar después: %v", err)
	}
	if oldRemaining != 0 {
		t.Fatalf("la poda perezosa no borró la fila vieja (quedan %d)", oldRemaining)
	}
}
