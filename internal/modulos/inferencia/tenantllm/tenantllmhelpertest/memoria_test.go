package tenantllmhelpertest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// Memoria cumple el puerto.
var _ tenantllm.Store = (*Memoria)(nil)

// newTenantID devuelve un UUID v4 nuevo, hecho con la biblioteca estándar.
func newTenantID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generando un UUID de prueba: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// TestMemoria_Contrato corre la suite del puerto contra el doble. La misma suite corre contra
// tenantllm.Postgres en F9: lo que pasa aquí es lo que los tests del módulo pueden dar por bueno
// cuando usan Memoria en lugar de Postgres.
func TestMemoria_Contrato(t *testing.T) {
	Contrato(t, func(t *testing.T) Montaje {
		t.Helper()
		store := NewMemoria()
		return Montaje{
			Store: store,
			// La tabla no referencia a public.tenants: un tenant «sembrado» es un UUID nuevo.
			SeedTenant: newTenantID,
			Row:        func(_ *testing.T, tenantID string) (Row, bool) { return store.Row(tenantID) },
		}
	})
}

// TestMemoria_RejectionTexts: el doble rechaza con los MISMOS tres textos que el adaptador
// (postgres_test.go los fija byte a byte), y en el mismo orden: vía, clave, consentimiento.
func TestMemoria_RejectionTexts(t *testing.T) {
	const tenant = "5e0b3a52-6a52-4a44-8b1c-2f0d6a3f9c11"
	cases := []struct {
		name    string
		via     string
		apiKey  string
		consent time.Time
		want    string
	}{
		{"unknown via wins over everything", "remota", "", time.Time{}, "tenantllm: upsert de " + tenant + ` con vía "remota": fuera del vocabulario (local|api)`},
		{"api without key wins over the missing consent", tenantllm.ViaAPI, "", time.Time{}, "tenantllm: upsert de " + tenant + " en vía api sin API key: esa vía no existe sin credencial"},
		{"api without consent", tenantllm.ViaAPI, keyFirst, time.Time{}, "tenantllm: upsert de " + tenant + " en vía api sin consentimiento: la fila no puede existir sin él"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := NewMemoria()
			cfg := apiConfig(tenant)
			cfg.Via = c.via
			err := store.Upsert(context.Background(), cfg, c.apiKey, c.consent)
			if err == nil || err.Error() != c.want {
				t.Errorf("error =\n%v\nquería, byte a byte:\n%s", err, c.want)
			}
			if _, found := store.Row(tenant); found {
				t.Error("el Upsert rechazado dejó fila")
			}
		})
	}
}

// TestMemoria_StampsWithItsClock: created_at es el reloj de la primera escritura y updated_at el
// de la última, en UTC; el consentimiento se guarda en UTC.
func TestMemoria_StampsWithItsClock(t *testing.T) {
	const tenant = "tenant-del-reloj"
	store := NewMemoria()
	first := time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("x", 3600))
	current := first
	store.now = func() time.Time { return current }
	ctx := context.Background()

	consent := consentFirst.In(time.FixedZone("y", -5*3600))
	if err := store.Upsert(ctx, apiConfig(tenant), keyFirst, consent); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	current = first.Add(time.Minute)
	if err := store.Upsert(ctx, localConfig(tenant), "", time.Time{}); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	current = first.Add(2 * time.Minute)
	if err := store.Upsert(ctx, apiConfig(tenant), keyRotated, consent); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}

	cfg, found, err := store.Get(ctx, tenant)
	if err != nil || !found {
		t.Fatalf("Get = (found=%v, err=%v), quería la fila", found, err)
	}
	if !cfg.CreatedAt.Equal(first) || cfg.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt = %v, quería %v en UTC (el del alta)", cfg.CreatedAt, first.UTC())
	}
	if !cfg.UpdatedAt.Equal(current) || cfg.UpdatedAt.Location() != time.UTC {
		t.Errorf("UpdatedAt = %v, quería %v en UTC (el de la última escritura)", cfg.UpdatedAt, current.UTC())
	}
	if !cfg.ConsentedAt.Equal(consentFirst) || cfg.ConsentedAt.Location() != time.UTC {
		t.Errorf("ConsentedAt = %v, quería %v en UTC", cfg.ConsentedAt, consentFirst)
	}
}

// TestMemoria_APIKeyCalls: cuenta cada petición de la credencial, con éxito o sin él, y ninguna
// otra operación la cuenta. Es lo que deja afirmar «la clave no se pidió» (R4.4.b).
func TestMemoria_APIKeyCalls(t *testing.T) {
	const tenant = "tenant-contado"
	store := NewMemoria()
	ctx := context.Background()

	if err := store.Upsert(ctx, apiConfig(tenant), keyFirst, consentFirst); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	if _, _, err := store.Get(ctx, tenant); err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if _, found := store.Row(tenant); !found {
		t.Fatal("Row no encontró la fila recién escrita")
	}
	if n := store.APIKeyCalls(); n != 0 {
		t.Fatalf("APIKeyCalls = %d sin haber pedido la clave, quería 0", n)
	}

	if key, err := store.APIKey(ctx, tenant); err != nil || key != keyFirst {
		t.Errorf("APIKey de la fila api: err = %v, o una clave distinta de la guardada", err)
	}
	if _, err := store.APIKey(ctx, "tenant-sin-fila"); !errors.Is(err, tenantllm.ErrNotConfigured) {
		t.Errorf("APIKey de un tenant sin fila: error = %v, quería ErrNotConfigured", err)
	}
	if n := store.APIKeyCalls(); n != 2 {
		t.Errorf("APIKeyCalls = %d tras dos peticiones, quería 2", n)
	}
	if err := store.Delete(ctx, tenant); err != nil {
		t.Fatalf("Delete: error inesperado %v", err)
	}
	if n := store.APIKeyCalls(); n != 2 {
		t.Errorf("APIKeyCalls = %d tras un Delete, quería 2", n)
	}
}
