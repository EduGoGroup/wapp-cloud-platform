package integrationshelpertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// Memoria cumple el puerto.
var _ integrations.Store = (*Memoria)(nil)

// testClock es un reloj que solo avanza cuando el test lo mueve.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// memoriaCases numera los Montajes para que cada caso tenga sus dos tenants.
var memoriaCases atomic.Int64

// newClockedMemoria devuelve un doble vacío con un reloj de test inyectado.
func newClockedMemoria() (*Memoria, *testClock) {
	clock := newTestClock()
	store := NewMemoria()
	store.SetClock(clock.Now)
	return store, clock
}

// TestMemoria_Contrato corre la suite del puerto contra el doble, sin BD y sin reloj real: cada
// caso monta un doble nuevo con su reloj, y Advance lo adelanta un segundo. La misma suite corre
// contra integrations.Postgres en F9: lo que pasa aquí es lo que los tests del módulo pueden dar
// por bueno cuando usan Memoria en lugar de Postgres.
func TestMemoria_Contrato(t *testing.T) {
	Contrato(t, func(*testing.T) Montaje {
		store, clock := newClockedMemoria()
		n := memoriaCases.Add(1)
		return Montaje{
			Store:          store,
			TenantA:        fmt.Sprintf("tenant-%02d-a", n),
			TenantB:        fmt.Sprintf("tenant-%02d-b", n),
			Now:            func(*testing.T) time.Time { return clock.Now() },
			Advance:        func(*testing.T) { clock.Advance(time.Second) },
			OutboxRow:      func(_ *testing.T, id int64) (OutboxRow, bool) { return store.OutboxRow(id) },
			IntegrationRow: func(_ *testing.T, tenant string) (IntegrationRow, bool) { return store.IntegrationRow(tenant) },
			SetClaimedAt: func(t *testing.T, id int64, at time.Time) {
				t.Helper()
				if !store.SetClaimedAt(id, at) {
					t.Fatalf("SetClaimedAt: la entrega %d no existe", id)
				}
			},
		}
	})
}

// mustClaimOne encola una entrega y la reclama.
func mustClaimOne(t *testing.T, store *Memoria) integrations.WebhookOutbox {
	t.Helper()
	ctx := context.Background()
	id, err := store.EnqueueWebhook(ctx, "tenant-1", "intake.push", json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: error inesperado %v", err)
	}
	batch, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("ClaimWebhookBatch = (%+v, %v), quería la entrega %d", batch, err, id)
	}
	return batch[0]
}

// TestNewMemoria_IsEmptyAndReady: recién construido no tiene nada, y se puede usar sin inyectarle
// reloj (fecha con el del proceso).
func TestNewMemoria_IsEmptyAndReady(t *testing.T) {
	store := NewMemoria()
	ctx := context.Background()
	if batch, err := store.ClaimWebhookBatch(ctx, 10); err != nil || len(batch) != 0 {
		t.Errorf("un doble nuevo reclama (%d filas, err=%v), quería nada", len(batch), err)
	}
	if _, found := store.OutboxRow(1); found {
		t.Error("un doble nuevo tiene la entrega 1")
	}
	if _, found := store.IntegrationRow("tenant-1"); found {
		t.Error("un doble nuevo tiene una integración")
	}
	started := time.Now()
	c := mustClaimOne(t, store)
	if c.CreatedAt.Before(started) || c.ClaimedAt.Before(started) {
		t.Errorf("sin reloj inyectado fechó con (%v, %v), quería el reloj del proceso (desde %v)", c.CreatedAt, c.ClaimedAt, started)
	}
}

// TestMemoria_SetClock_StampsEverything: del reloj inyectado salen created_at y next_attempt_at
// del encolado, el sello del claim y las dos fechas de la integración; con nil vuelve al reloj del
// sistema.
func TestMemoria_SetClock_StampsEverything(t *testing.T) {
	store, clock := newClockedMemoria()
	ctx := context.Background()
	enqueuedAt := clock.Now()
	id, err := store.EnqueueWebhook(ctx, "tenant-1", "intake.push", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: error inesperado %v", err)
	}
	clock.Advance(time.Minute)
	claimedAt := clock.Now()
	if _, err := store.ClaimWebhookBatch(ctx, 1); err != nil {
		t.Fatalf("ClaimWebhookBatch: error inesperado %v", err)
	}
	row, _ := store.OutboxRow(id)
	if !row.CreatedAt.Equal(enqueuedAt) || !row.NextAttemptAt.Equal(enqueuedAt) || !row.ClaimedAt.Equal(claimedAt) {
		t.Errorf("(created_at, next_attempt_at, claimed_at) = (%v, %v, %v), quería (%v, %v, %v)",
			row.CreatedAt, row.NextAttemptAt, row.ClaimedAt, enqueuedAt, enqueuedAt, claimedAt)
	}

	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{TenantID: "tenant-1"}, ""); err != nil {
		t.Fatalf("UpsertTenantIntegration: error inesperado %v", err)
	}
	clock.Advance(time.Minute)
	if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{TenantID: "tenant-1"}, ""); err != nil {
		t.Fatalf("UpsertTenantIntegration: error inesperado %v", err)
	}
	cfg, _ := store.IntegrationRow("tenant-1")
	if !cfg.CreatedAt.Equal(claimedAt) || !cfg.UpdatedAt.Equal(clock.Now()) {
		t.Errorf("(CreatedAt, UpdatedAt) = (%v, %v), quería (%v, %v)", cfg.CreatedAt, cfg.UpdatedAt, claimedAt, clock.Now())
	}

	store.SetClock(nil)
	started := time.Now()
	if c := mustClaimOne(t, store); c.ClaimedAt.Before(started) {
		t.Errorf("tras SetClock(nil) selló con %v, quería el reloj del proceso (desde %v)", c.ClaimedAt, started)
	}
}

// TestMemoria_LeaseBoundary_IsStrict: un claim sellado JUSTO en (reloj − lease) sigue vigente; un
// nanosegundo más viejo, ya no. Es la frontera que la suite no puede fabricar contra Postgres
// (`claimed_at < now() - lease`).
func TestMemoria_LeaseBoundary_IsStrict(t *testing.T) {
	store, clock := newClockedMemoria()
	ctx := context.Background()
	c := mustClaimOne(t, store)
	const lease = 10 * time.Minute

	clock.Advance(lease)
	if n, err := store.RecoverOrphanDeliveries(ctx, lease); err != nil || n != 0 {
		t.Fatalf("en la frontera exacta se rescataron %d entregas (err=%v), quería 0: el lease vence en estricto", n, err)
	}
	clock.Advance(time.Nanosecond)
	if n, err := store.RecoverOrphanDeliveries(ctx, lease); err != nil || n != 1 {
		t.Fatalf("pasada la frontera se rescataron %d entregas (err=%v), quería 1", n, err)
	}
	if err := store.MarkWebhookDelivered(ctx, c); !errors.Is(err, integrations.ErrClaimLost) {
		t.Errorf("el claim rescatado sigue valiendo: err = %v, quería ErrClaimLost", err)
	}
}

// TestMemoria_Claim_NegativeLimit_ClaimsNothing: el doble trata un límite negativo como cero
// (Postgres lo rechaza con un error; el worker nunca lo manda).
func TestMemoria_Claim_NegativeLimit_ClaimsNothing(t *testing.T) {
	store, _ := newClockedMemoria()
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, "tenant-1", "intake.push", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("EnqueueWebhook: error inesperado %v", err)
	}
	if batch, err := store.ClaimWebhookBatch(ctx, -1); err != nil || len(batch) != 0 {
		t.Errorf("con límite -1 reclamó (%d filas, err=%v), quería nada", len(batch), err)
	}
}

// TestMemoria_Claim_ReturnsTheBatchInDueOrder: el lote sale ordenado por next_attempt_at y, a
// igualdad, por id. Es el orden en que el worker lo entrega; la suite compartida no lo afirma
// porque Postgres no lo garantiza.
func TestMemoria_Claim_ReturnsTheBatchInDueOrder(t *testing.T) {
	store, clock := newClockedMemoria()
	ctx := context.Background()
	ids := make([]int64, 0, 6)
	for i := range 6 {
		id, err := store.EnqueueWebhook(ctx, "tenant-1", "intake.push", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("EnqueueWebhook: error inesperado %v", err)
		}
		ids = append(ids, id)
		if i%2 == 1 {
			clock.Advance(time.Second) // de dos en dos comparten instante: desempata el id
		}
	}
	batch, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil || len(batch) != len(ids) {
		t.Fatalf("ClaimWebhookBatch = (%d filas, %v), quería %d", len(batch), err, len(ids))
	}
	for i, c := range batch {
		if c.ID != ids[i] {
			t.Fatalf("posición %d del lote = entrega %d, quería %d (orden de vencimiento)", i, c.ID, ids[i])
		}
	}
}

// TestMemoria_ReturnedRowsDoNotAliasTheStore: lo que el doble devuelve (por el reclamo y por el
// observador) es una copia; cambiarlo no cambia lo guardado.
func TestMemoria_ReturnedRowsDoNotAliasTheStore(t *testing.T) {
	store, _ := newClockedMemoria()
	c := mustClaimOne(t, store)
	copy(c.Payload, `{"n":9}`)
	seen, _ := store.OutboxRow(c.ID)
	copy(seen.Payload, `{"n":8}`)
	if again, _ := store.OutboxRow(c.ID); string(again.Payload) != `{"n":1}` {
		t.Errorf("payload guardado = %s tras mutar las copias devueltas, quería {\"n\":1}", again.Payload)
	}
}

// TestMemoria_Observers: OutboxRow e IntegrationRow no encuentran lo que no existe, SetClaimedAt
// dice false para una fila que no existe y, para una que sí, solo cambia el sello.
func TestMemoria_Observers(t *testing.T) {
	store, clock := newClockedMemoria()
	c := mustClaimOne(t, store)
	if _, found := store.OutboxRow(c.ID + 1); found {
		t.Error("OutboxRow encontró una entrega que no existe")
	}
	if _, found := store.IntegrationRow("tenant-sin-fila"); found {
		t.Error("IntegrationRow encontró una integración que no existe")
	}
	if store.SetClaimedAt(c.ID+1, clock.Now()) {
		t.Error("SetClaimedAt dijo true para una entrega que no existe")
	}
	seal := clock.Now().Add(-time.Hour)
	if !store.SetClaimedAt(c.ID, seal) {
		t.Fatal("SetClaimedAt dijo false para una entrega que existe")
	}
	want := c
	want.ClaimedAt = seal
	got, _ := store.OutboxRow(c.ID)
	requireSameOutbox(t, "la fila tras SetClaimedAt", got, want)
}

// TestMemoria_SecretSeal_ChangesOnEveryWrite: la marca del sobre cambia cada vez que se escribe un
// secreto —aunque sea el mismo—, no cambia mientras no se escribe y jamás lo contiene.
func TestMemoria_SecretSeal_ChangesOnEveryWrite(t *testing.T) {
	store, _ := newClockedMemoria()
	ctx := context.Background()
	cfg := integrations.TenantIntegration{TenantID: "tenant-1", EventsAdapter: "webhook"}
	seals := make([]string, 0, 3)
	for _, secret := range []string{firstSecret, "", firstSecret} {
		if err := store.UpsertTenantIntegration(ctx, cfg, secret); err != nil {
			t.Fatalf("UpsertTenantIntegration: error inesperado %v", err)
		}
		row, _ := store.IntegrationRow("tenant-1")
		if row.SecretSeal == "" || row.SecretSeal == firstSecret {
			t.Fatalf("marca del sobre = %q: tiene que existir y no ser el secreto", row.SecretSeal)
		}
		seals = append(seals, row.SecretSeal)
	}
	if seals[0] != seals[1] {
		t.Errorf("un upsert sin secreto cambió la marca: %q → %q", seals[0], seals[1])
	}
	if seals[1] == seals[2] {
		t.Errorf("reescribir el secreto no cambió la marca (%q)", seals[2])
	}
}

// TestMemoria_ConcurrentUse: se puede usar desde varias goroutines a la vez (lo vigila -race).
func TestMemoria_ConcurrentUse(t *testing.T) {
	store := NewMemoria()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			tenant := fmt.Sprintf("tenant-%d", i)
			if _, err := store.EnqueueWebhook(ctx, tenant, "intake.push", json.RawMessage(`{}`)); err != nil {
				t.Errorf("EnqueueWebhook: %v", err)
			}
			if err := store.UpsertTenantIntegration(ctx, integrations.TenantIntegration{TenantID: tenant}, "s"); err != nil {
				t.Errorf("UpsertTenantIntegration: %v", err)
			}
			batch, err := store.ClaimWebhookBatch(ctx, 2)
			if err != nil {
				t.Errorf("ClaimWebhookBatch: %v", err)
			}
			for _, c := range batch {
				if err := store.MarkWebhookDelivered(ctx, c); err != nil {
					t.Errorf("MarkWebhookDelivered: %v", err)
				}
			}
			if _, err := store.RecoverOrphanDeliveries(ctx, time.Hour); err != nil {
				t.Errorf("RecoverOrphanDeliveries: %v", err)
			}
			store.IntegrationRow(tenant)
			store.SetClock(time.Now)
		})
	}
	wg.Wait()
}
