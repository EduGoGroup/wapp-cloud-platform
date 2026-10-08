package intakes_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// La conducta de los tres métodos la afirma la suite de contrato (reminders_contrato.go). Aquí va
// lo que solo se puede afirmar del doble: que el compare-and-swap es atómico de verdad bajo
// concurrencia y que ninguno mira el reloj del store.

// Aserciones de compilación de lo que promete memory_reminders.go.
var (
	_ func(*intakes.MemoryStore, context.Context, string, string, time.Time) (intakes.Intake, bool, error)  = (*intakes.MemoryStore).MarkDepositReminded
	_ func(*intakes.MemoryStore, context.Context, string, string, time.Time) (intakes.Intake, bool, error)  = (*intakes.MemoryStore).MarkExpiryReminded
	_ func(*intakes.MemoryStore, context.Context, string, string, time.Time, int) ([]intakes.Intake, error) = (*intakes.MemoryStore).PendingDepositReminders
)

// reminderStore es un store con una seña vencida ("deposit") y un presupuesto vencido ("quote") a
// fecha `at`, los dos del mismo contacto.
func reminderStore() (store *intakes.MemoryStore, clock *testClock, at time.Time) {
	store, clock = newMemoryStore()
	at = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	deposit := seeded("deposit", intakes.StatusDepositRequested, 1)
	deposit.DepositDueAt = at.Add(-time.Hour)
	store.Add(tenant1, deposit)
	quote := seeded("quote", intakes.StatusPendingApproval, 1)
	quote.UpdatedAt = at.Add(-intakes.QuoteDeadline - time.Hour)
	store.Add(tenant1, quote)
	return store, clock, at
}

// raceForTheMark lanza n toques simultáneos y devuelve cuántos ganaron.
func raceForTheMark(t *testing.T, n int, mark func(at time.Time) (intakes.Intake, bool, error), at time.Time) int {
	t.Helper()
	var (
		wg    sync.WaitGroup
		won   atomic.Int64
		start = make(chan struct{})
	)
	for i := range n {
		wg.Go(func() {
			<-start
			in, ok, err := mark(at.Add(time.Duration(i) * time.Second))
			if err != nil {
				t.Errorf("toque %d: error inesperado %v", i, err)
			}
			if ok {
				won.Add(1)
				if in.ID == "" {
					t.Errorf("toque %d ganó y devolvió una cabecera a cero", i)
				}
			}
		})
	}
	close(start)
	wg.Wait()
	return int(won.Load())
}

// TestMemoryStore_MarkDepositReminded_OneWinnerUnderConcurrency: de N toques simultáneos gana
// EXACTAMENTE uno, y la marca que queda es la del que ganó («un solo recordatorio», D-041.12).
func TestMemoryStore_MarkDepositReminded_OneWinnerUnderConcurrency(t *testing.T) {
	store, _, at := reminderStore()
	won := raceForTheMark(t, 32, func(at time.Time) (intakes.Intake, bool, error) {
		return store.MarkDepositReminded(ctx, tenant1, "deposit", at)
	}, at)
	if won != 1 {
		t.Errorf("ganaron %d toques, quería exactamente 1", won)
	}
	got := mustGet(t, store, tenant1, "deposit")
	if got.DepositRemindedAt.Before(at) || !got.DepositRemindedAt.Before(at.Add(32*time.Second)) {
		t.Errorf("DepositRemindedAt = %v; no es el instante de ninguno de los toques", got.DepositRemindedAt)
	}
	if pending, err := store.PendingDepositReminders(ctx, tenant1, "contact-1", at, 10); err != nil || len(pending) != 0 {
		t.Errorf("tras el recordatorio quedan %d pendientes (err %v), quería 0", len(pending), err)
	}
}

// TestMemoryStore_MarkExpiryReminded_OneWinnerUnderConcurrency: lo mismo para el aviso del plazo,
// y la marca no mueve UpdatedAt ni saca la solicitud de `pending_approval`.
func TestMemoryStore_MarkExpiryReminded_OneWinnerUnderConcurrency(t *testing.T) {
	store, _, at := reminderStore()
	before := mustGet(t, store, tenant1, "quote")
	won := raceForTheMark(t, 32, func(at time.Time) (intakes.Intake, bool, error) {
		return store.MarkExpiryReminded(ctx, tenant1, "quote", at)
	}, at)
	if won != 1 {
		t.Errorf("ganaron %d toques, quería exactamente 1", won)
	}
	got := mustGet(t, store, tenant1, "quote")
	if got.ExpiryRemindedAt.IsZero() {
		t.Error("nadie dejó la marca del aviso")
	}
	if !got.UpdatedAt.Equal(before.UpdatedAt) || got.Status != intakes.StatusPendingApproval {
		t.Errorf("el aviso movió UpdatedAt (%v → %v) o el estado (%q)", before.UpdatedAt, got.UpdatedAt, got.Status)
	}
}

// TestMemoryStore_Reminders_UseTheCallersInstantNotTheClock: lo vencido se decide contra `at`. Con
// el reloj del store años por delante, un `at` anterior al vencimiento sigue sin ganar; y con el
// reloj parado, un `at` posterior gana.
func TestMemoryStore_Reminders_UseTheCallersInstantNotTheClock(t *testing.T) {
	store, clock, at := reminderStore()
	clock.Advance(5 * 365 * 24 * time.Hour)
	requireNothingDueBefore(t, store, at)

	in, won, err := store.MarkDepositReminded(ctx, tenant1, "deposit", at)
	if err != nil || !won || !in.DepositRemindedAt.Equal(at) || in.Status != intakes.StatusDepositRequested {
		t.Errorf("seña con `at` posterior = (%+v, %v, %v), quería ganar con la marca en %v", in, won, err, at)
	}
	in, won, err = store.MarkExpiryReminded(ctx, tenant1, "quote", at)
	if err != nil || !won || !in.ExpiryRemindedAt.Equal(at) {
		t.Errorf("presupuesto con `at` posterior = (%+v, %v, %v), quería ganar con la marca en %v", in, won, err, at)
	}
}

// requireNothingDueBefore exige que con un `at` anterior al vencimiento ni la seña ni el
// presupuesto ganan y no hay pendientes; y que con límite 0 tampoco los hay ni en `at`.
func requireNothingDueBefore(t *testing.T, store *intakes.MemoryStore, at time.Time) {
	t.Helper()
	early := at.Add(-48 * time.Hour)
	if _, won, err := store.MarkDepositReminded(ctx, tenant1, "deposit", early); won || err != nil {
		t.Errorf("seña con `at` anterior al vencimiento: (ganó=%v, err=%v), quería no ganar", won, err)
	}
	if _, won, err := store.MarkExpiryReminded(ctx, tenant1, "quote", early); won || err != nil {
		t.Errorf("presupuesto con `at` anterior al plazo: (ganó=%v, err=%v), quería no ganar", won, err)
	}
	if pending, err := store.PendingDepositReminders(ctx, tenant1, "contact-1", early, 10); err != nil || pending == nil || len(pending) != 0 {
		t.Errorf("pendientes con `at` anterior = (%#v, %v), quería un slice vacío no nil", pending, err)
	}
	if none, err := store.PendingDepositReminders(ctx, tenant1, "contact-1", at, 0); err != nil || none == nil || len(none) != 0 {
		t.Errorf("pendientes con límite 0 = (%#v, %v), quería un slice vacío no nil", none, err)
	}
}
