package enroll_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en T1).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta si no hay BD configurada. CI no
// levanta PostgreSQL: sin WAPP_TEST_DB_DSN (o si no responde) hace t.Skip.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
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

// seedTenant crea un tenant con slug único y devuelve su UUID.
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Test Tenant")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

func TestIntegration_EnrollPersistsAndConsumes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	tenantID := seedTenant(t, db)
	code := fmt.Sprintf("CODE-%d", time.Now().UnixNano())

	codes := enroll.NewPostgresCodeStore(db)
	if err := codes.Create(ctx, code, tenantID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("sembrar código: %v", err)
	}

	ca, err := enroll.NewDevCA("wapp-dev-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: %v", err)
	}
	certs := enroll.NewPostgresEdgeCertRepository(db)
	svc := enroll.NewService(codes, ca, certs)

	edgeCert, caChain, gotTenant, err := svc.Enroll(ctx, code, newTestCSR(t, "edge-001"))
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if gotTenant != tenantID {
		t.Errorf("tenant: got %q, want %q", gotTenant, tenantID)
	}
	if len(edgeCert) == 0 || len(caChain) == 0 {
		t.Fatal("Enroll debería devolver cert y cadena")
	}

	// edge_certs persistido para el tenant.
	var certCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.edge_certs WHERE tenant_id = $1`, tenantID).Scan(&certCount); err != nil {
		t.Fatalf("contar edge_certs: %v", err)
	}
	if certCount != 1 {
		t.Fatalf("edge_certs: got %d, want 1", certCount)
	}

	// El código quedó marcado usado.
	var usedAt sql.NullTime
	if err := db.QueryRowContext(ctx,
		`SELECT used_at FROM public.enrollment_codes WHERE code = $1`, code).Scan(&usedAt); err != nil {
		t.Fatalf("leer used_at: %v", err)
	}
	if !usedAt.Valid {
		t.Fatal("used_at debería estar poblado tras el consumo")
	}

	// Segundo intento del mismo código -> inválido.
	_, _, _, err = svc.Enroll(ctx, code, newTestCSR(t, "edge-002"))
	if !errors.Is(err, enroll.ErrCodeInvalid) {
		t.Fatalf("segundo intento: got %v, want ErrCodeInvalid", err)
	}
}

func TestIntegration_ConsumeIsAtomic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	tenantID := seedTenant(t, db)
	code := fmt.Sprintf("ATOMIC-%d", time.Now().UnixNano())

	codes := enroll.NewPostgresCodeStore(db)
	if err := codes.Create(ctx, code, tenantID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("sembrar código: %v", err)
	}

	// Dos goroutines consumen el mismo código: exactamente una debe ganar.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		invalids int
	)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := codes.Consume(ctx, code)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, enroll.ErrCodeInvalid):
				invalids++
			default:
				t.Errorf("error inesperado: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 || invalids != 1 {
		t.Fatalf("consumo atómico: wins=%d invalids=%d, want 1/1", wins, invalids)
	}
}

func TestIntegration_MigrateWith0002Idempotent(t *testing.T) {
	db := openTestDB(t) // openTestDB ya migró una vez
	ctx := context.Background()

	// Ambas tablas de T3 deben existir.
	for _, table := range []string{"enrollment_codes", "edge_certs"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`, table).Scan(&exists); err != nil {
			t.Fatalf("comprobando %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("la tabla public.%s debería existir tras migrar", table)
		}
	}

	// Re-aplicar debe ser idempotente (Skipped, ya está en la versión esperada).
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("re-migración: %v", err)
	}
	if !res.Skipped {
		t.Fatal("la re-migración debería marcarse Skipped (idempotencia con 0002)")
	}
	if res.Version != migrations.SchemaVersion {
		t.Fatalf("versión: got %q, want %q", res.Version, migrations.SchemaVersion)
	}
}
