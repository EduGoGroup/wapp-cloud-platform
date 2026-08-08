// postgres_integration_test.go verifica el store real contra Postgres: que dos
// claims concurrentes con FOR UPDATE SKIP LOCKED nunca se pisan (T3.1), y que
// el secreto de tenant_integrations queda CIFRADO EN REPOSO — un SELECT directo
// no lo muestra en claro (mismo criterio que buyerdata_integration_test.go,
// T4.5 del Plan 041: un doble en memoria no puede demostrar esto).
package integrations_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la BD de integración y aplica el esquema — mismo contrato que
// el resto de los *_integration_test.go del repo: sin DSN se salta.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
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

// Material de clave de prueba: mismo patrón que buyerdata_integration_test.go.
const (
	kekDePruebaB64   = "ERERERERERERERERERERERERERERERERERERERERERE="
	indexDePruebaB64 = "RERERERERERERERERERERERERERERERERERERERERES="
	kekIDDePrueba    = "test-kek-1"
)

func cipherDePrueba(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: kekIDDePrueba + ":" + kekDePruebaB64,
		CurrentID:  kekIDDePrueba,
		IndexB64:   indexDePruebaB64,
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

func wipeWebhookTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM public.webhook_outbox`); err != nil {
		t.Fatalf("limpiar webhook_outbox: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM public.tenant_integrations`); err != nil {
		t.Fatalf("limpiar tenant_integrations: %v", err)
	}
}

// TestClaimWebhookBatch_ConcurrenciaSinDuplicados es el criterio literal de
// T3.1: dos claims concurrentes NO toman el mismo registro (FOR UPDATE SKIP
// LOCKED probado con goroutines reales contra Postgres, no un mock).
func TestClaimWebhookBatch_ConcurrenciaSinDuplicados(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	const total = 40
	for i := range total {
		if _, err := store.EnqueueWebhook(ctx, "t-concurrencia", "intake.push", []byte(`{}`)); err != nil {
			t.Fatalf("encolar #%d: %v", i, err)
		}
	}

	const workers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		seen     = map[int64]int{}
		errCount int64
	)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				batch, err := store.ClaimWebhookBatch(ctx, 5)
				if err != nil {
					atomic.AddInt64(&errCount, 1)
					return
				}
				if len(batch) == 0 {
					return
				}
				mu.Lock()
				for _, item := range batch {
					seen[item.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if errCount > 0 {
		t.Fatalf("%d goroutines fallaron reclamando (SKIP LOCKED no debería producir errores)", errCount)
	}
	if len(seen) != total {
		t.Fatalf("se reclamaron %d filas distintas, esperaba %d — SKIP LOCKED perdió filas", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("la fila %d se reclamó %d veces — SKIP LOCKED debía impedir el duplicado", id, count)
		}
	}
}

// TestTenantSecret_CifradoEnReposo: un SELECT directo sobre secret_enc/secret_dek
// no muestra el secreto en claro (mismo criterio que T4.5 del Plan 041), y el
// store real lo recupera con la llave.
func TestTenantSecret_CifradoEnReposo(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	const secret = "shhh-secreto-hmac-del-tenant-9f8e7d" // #nosec G101 -- secreto de prueba, no una credencial real
	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{
		TenantID: "t-secreto", CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://bridge.example/callback", Enabled: true,
	}, secret); err != nil {
		t.Fatalf("UpsertTenantIntegration: %v", err)
	}

	var enc, dek []byte
	var kekID string
	if err := db.QueryRowContext(ctx, `
		SELECT secret_enc, secret_dek, secret_kek_id FROM public.tenant_integrations WHERE tenant_id = $1
	`, "t-secreto").Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leer por SQL directo: %v", err)
	}
	if strings.Contains(string(enc), secret) {
		t.Fatalf("FUGA: secret_enc contiene el secreto en claro (%d bytes)", len(enc))
	}
	if len(enc) == 0 || len(dek) == 0 || kekID == "" {
		t.Fatalf("la fila quedó sin material cifrado: enc=%dB dek=%dB kek_id=%q", len(enc), len(dek), kekID)
	}

	got, found, err := store.GetTenantSecret(ctx, "t-secreto")
	if err != nil {
		t.Fatalf("GetTenantSecret: %v", err)
	}
	if !found || got != secret {
		t.Fatalf("GetTenantSecret devolvió (%q, %v), quiero (%q, true)", got, found, secret)
	}
}

// TestUpsertTenantIntegration_SecretoVacioPreservaElExistente: reconfigurar
// endpoint/adapters sin reenviar el secreto no lo borra (T3.1, contrato de
// UpsertTenantIntegration).
func TestUpsertTenantIntegration_SecretoVacioPreservaElExistente(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{
		TenantID: "t-preserva", CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://a.example", Enabled: true,
	}, "secreto-original"); err != nil {
		t.Fatalf("primer upsert: %v", err)
	}
	// Reconfigura solo el endpoint, secret="" — no debe tocar el secreto.
	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{
		TenantID: "t-preserva", CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://b.example", Enabled: true,
	}, ""); err != nil {
		t.Fatalf("segundo upsert: %v", err)
	}

	ti, found, err := store.GetTenantIntegration(ctx, "t-preserva")
	if err != nil || !found {
		t.Fatalf("GetTenantIntegration: found=%v err=%v", found, err)
	}
	if ti.EndpointURL != "https://b.example" {
		t.Fatalf("endpoint_url=%q, quiero el nuevo https://b.example", ti.EndpointURL)
	}
	got, sfound, err := store.GetTenantSecret(ctx, "t-preserva")
	if err != nil || !sfound || got != "secreto-original" {
		t.Fatalf("el secreto original no sobrevivió: found=%v got=%q err=%v", sfound, got, err)
	}
}

// TestRecoverOrphanDeliveries_VuelveAPending confirma D-042.4 contra Postgres
// real: una fila 'delivering' con next_attempt_at vencido vuelve a pending.
func TestRecoverOrphanDeliveries_VuelveAPending(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	id, err := store.EnqueueWebhook(ctx, "t-huerfano", "intake.push", []byte(`{}`))
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE public.webhook_outbox SET status = 'delivering' WHERE id = $1
	`, id); err != nil {
		t.Fatalf("simular delivering: %v", err)
	}

	n, err := store.RecoverOrphanDeliveries(ctx)
	if err != nil {
		t.Fatalf("RecoverOrphanDeliveries: %v", err)
	}
	if n != 1 {
		t.Fatalf("recuperadas=%d, quiero 1", n)
	}

	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM public.webhook_outbox WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("leer status: %v", err)
	}
	if status != integrations.StatusPending {
		t.Fatalf("status=%q, quiero pending", status)
	}
}
