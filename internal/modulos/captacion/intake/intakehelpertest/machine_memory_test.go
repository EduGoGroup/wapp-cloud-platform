package intakehelpertest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

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

// memoryCases numera los Montajes para que cada caso tenga sus dos tenants.
var memoryCases atomic.Int64

// newTable monta sobre un MachineMemory nuevo, con su reloj de test, lo que las suites piden de
// la tabla.
func newTable() (*intakehelpertest.MachineMemory, intakehelpertest.Table) {
	clock := newTestClock()
	store := intakehelpertest.NewMachineMemory(clock.Now)
	n := memoryCases.Add(1)
	return store, intakehelpertest.Table{
		TenantA: fmt.Sprintf("tenant-%02d-a", n),
		TenantB: fmt.Sprintf("tenant-%02d-b", n),
		Seed:    func(_ *testing.T, r intakehelpertest.Row) string { return store.Seed(r) },
		Row: func(t *testing.T, id string) intakehelpertest.Row {
			t.Helper()
			r, ok := store.View(id)
			if !ok {
				t.Fatalf("no existe el job %q", id)
			}
			return r
		},
		Now:     func(*testing.T) time.Time { return clock.Now() },
		Advance: func(*testing.T) { clock.Advance(time.Second) },
	}
}

// TestMachineMemory_ContratoMachine corre la suite de intake.PipelineStore contra el doble, sin
// BD y sin reloj real: cada caso monta un doble nuevo con su reloj.
func TestMachineMemory_ContratoMachine(t *testing.T) {
	intakehelpertest.ContratoMachine(t, func(*testing.T) intakehelpertest.MachineMontaje {
		store, table := newTable()
		return intakehelpertest.MachineMontaje{Store: store, Table: table}
	})
}

// pendingRow es una fila `pending` del tenant, con la marca vencida respecto al reloj de test.
func pendingRow(tenant, event string) intakehelpertest.Row {
	return intakehelpertest.Row{
		Key:    intake.WindowKey{TenantID: tenant, SessionID: "s", ContactID: "c", EventID: event},
		Status: intake.StatusPending,
	}
}

// TestMachineMemory_Seed_FillsWhatComesEmpty: lo que se siembra a cero se rellena como lo haría
// la tabla, con ids propios correlativos; lo que viene puesto se respeta.
func TestMachineMemory_Seed_FillsWhatComesEmpty(t *testing.T) {
	clock := newTestClock()
	store := intakehelpertest.NewMachineMemory(clock.Now)
	first := store.Seed(intakehelpertest.Row{})
	second := store.Seed(intakehelpertest.Row{})
	if first != "job-001" || second != "job-002" {
		t.Errorf("ids sembrados = (%q, %q), quería (job-001, job-002)", first, second)
	}
	got, ok := store.View(first)
	if !ok {
		t.Fatal("View no encuentra la fila recién sembrada")
	}
	if got.ID != first || got.Status != intake.StatusPending {
		t.Errorf("(ID, Status) = (%q, %q), quería (%q, pending)", got.ID, got.Status, first)
	}
	if !got.CreatedAt.Equal(clock.Now()) || !got.UpdatedAt.Equal(clock.Now()) {
		t.Errorf("(CreatedAt, UpdatedAt) = (%v, %v), quería el reloj (%v) en las dos", got.CreatedAt, got.UpdatedAt, clock.Now())
	}
	if got.Artifacts == nil || len(got.Artifacts) != 0 {
		t.Errorf("Artifacts = %v, quería un mapa vacío no nil", got.Artifacts)
	}
	if !got.NextAttemptAt.IsZero() {
		t.Errorf("NextAttemptAt = %v, quería cero (reclamable ya)", got.NextAttemptAt)
	}

	created := clock.Now().Add(-time.Hour)
	own := store.Seed(intakehelpertest.Row{ID: "mine", Status: intake.StatusDone, CreatedAt: created})
	got, _ = store.View(own)
	if own != "mine" || got.Status != intake.StatusDone || !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(created) {
		t.Errorf("fila con valores propios = (%q, %q, %v, %v), quería (mine, done, %v, %v)",
			own, got.Status, got.CreatedAt, got.UpdatedAt, created, created)
	}
}

// TestMachineMemory_SeedAndView_CopyTheRow: ni la fila que se sembró ni la que View devuelve
// comparten memoria con la del doble: mutarlas desde fuera no fabrica un estado.
func TestMachineMemory_SeedAndView_CopyTheRow(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	seeded := intakehelpertest.Row{
		SourceRefs: []string{"wamid.one"},
		SourceText: intake.SourceText{Enc: []byte("enc"), DEK: []byte("dek"), KEKID: "k1"},
		Artifacts:  map[string]json.RawMessage{intake.StageP2: json.RawMessage(`{"version":1}`)},
	}
	id := store.Seed(seeded)
	seeded.SourceRefs[0] = "mutated"
	seeded.SourceText.Enc[0] = 'X'
	seeded.Artifacts[intake.StageP3] = json.RawMessage(`{"version":1}`)

	view, _ := store.View(id)
	view.SourceRefs[0] = "mutated"
	view.SourceText.DEK[0] = 'X'
	delete(view.Artifacts, intake.StageP2)

	got, _ := store.View(id)
	if got.SourceRefs[0] != "wamid.one" || string(got.SourceText.Enc) != "enc" || string(got.SourceText.DEK) != "dek" {
		t.Errorf("la fila del doble cambió desde fuera: refs %v, sobre %q/%q", got.SourceRefs, got.SourceText.Enc, got.SourceText.DEK)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[intake.StageP2] == nil {
		t.Errorf("los artefactos del doble cambiaron desde fuera: %v", got.Artifacts)
	}
	if _, ok := store.View("no-existe"); ok {
		t.Error("View de un id que no existe devolvió ok=true")
	}
}

// TestMachineMemory_Claims_CountsEveryClaimThatReachesTheQueue: cuenta los dos reclamos, con o
// sin job que llevarse; el reclamo por evento con el tenant vacío no llega a la cola.
func TestMachineMemory_Claims_CountsEveryClaimThatReachesTheQueue(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	ctx := context.Background()
	if got := store.Claims(); got != 0 {
		t.Fatalf("Claims de un doble nuevo = %d, quería 0", got)
	}
	store.Seed(pendingRow("tenant-a", "event-1"))
	for range 3 {
		if _, _, err := store.ClaimNext(ctx); err != nil {
			t.Fatalf("ClaimNext: error inesperado %v", err)
		}
	}
	if _, _, err := store.ClaimNextIgnoringBackoff(ctx, "tenant-a"); err != nil {
		t.Fatalf("ClaimNextIgnoringBackoff: error inesperado %v", err)
	}
	if _, _, err := store.ClaimNextIgnoringBackoff(ctx, ""); err != nil {
		t.Fatalf("ClaimNextIgnoringBackoff sin tenant: error inesperado %v", err)
	}
	if got := store.Claims(); got != 4 {
		t.Errorf("Claims = %d, quería 4 (tres ClaimNext y un reclamo por evento con tenant)", got)
	}
}

// TestMachineMemory_BreakClaim_FailsBothClaimsWithoutTouchingRows: con el reclamo roto los dos
// devuelven ese error, la llamada cuenta y el job sigue `pending`; nil lo repara.
func TestMachineMemory_BreakClaim_FailsBothClaimsWithoutTouchingRows(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	ctx := context.Background()
	id := store.Seed(pendingRow("tenant-a", "event-1"))
	before, _ := store.View(id)
	boom := errors.New("la base no contesta")
	store.BreakClaim(boom)

	if job, ok, err := store.ClaimNext(ctx); !errors.Is(err, boom) || ok || job.ID != "" {
		t.Errorf("ClaimNext roto = (%q, %v, %v), quería (\"\", false, %v)", job.ID, ok, err, boom)
	}
	if job, ok, err := store.ClaimNextIgnoringBackoff(ctx, "tenant-a"); !errors.Is(err, boom) || ok || job.ID != "" {
		t.Errorf("ClaimNextIgnoringBackoff roto = (%q, %v, %v), quería (\"\", false, %v)", job.ID, ok, err, boom)
	}
	if got := store.Claims(); got != 2 {
		t.Errorf("Claims con el reclamo roto = %d, quería 2", got)
	}
	if after, _ := store.View(id); after.Status != intake.StatusPending || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("el reclamo roto tocó la fila: status %q, updated_at %v", after.Status, after.UpdatedAt)
	}

	store.BreakClaim(nil)
	if job, ok, err := store.ClaimNext(ctx); err != nil || !ok || job.ID != id {
		t.Errorf("ClaimNext reparado = (%q, %v, %v), quería (%q, true, nil)", job.ID, ok, err, id)
	}
}

// TestNewMachineMemory_NilClock_UsesTheProcessClock: sin reloj inyectado fecha con el del
// proceso, y un job sembrado sin marca es reclamable ya.
func TestNewMachineMemory_NilClock_UsesTheProcessClock(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(nil)
	earliest := time.Now()
	id := store.Seed(pendingRow("tenant-a", "event-1"))
	got, _ := store.View(id)
	if got.CreatedAt.Before(earliest) || got.CreatedAt.After(time.Now()) {
		t.Errorf("CreatedAt = %v, quería el reloj del proceso", got.CreatedAt)
	}
	if job, ok, err := store.ClaimNext(context.Background()); err != nil || !ok || job.ID != id {
		t.Errorf("ClaimNext = (%q, %v, %v), quería (%q, true, nil)", job.ID, ok, err, id)
	}
}

// TestMachineMemory_ConcurrentClaims_EachJobIsTakenOnce: con -race. Varias goroutines reclaman
// a la vez y cada job sale exactamente una vez. No afirma nada de SKIP LOCKED: solo que el
// doble no se corrompe.
func TestMachineMemory_ConcurrentClaims_EachJobIsTakenOnce(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	const jobs, workers = 40, 8
	for i := range jobs {
		store.Seed(pendingRow("tenant-a", fmt.Sprintf("event-%d", i)))
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		taken = map[string]int{}
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, ok, err := store.ClaimNext(context.Background())
				if err != nil || !ok {
					return
				}
				if done, err := store.Finish(context.Background(), job.ID, ""); err != nil || !done {
					t.Errorf("Finish(%s) = (%v, %v), quería (true, nil)", job.ID, done, err)
				}
				mu.Lock()
				taken[job.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(taken) != jobs {
		t.Errorf("se reclamaron %d jobs distintos, quería %d", len(taken), jobs)
	}
	for id, n := range taken {
		if n != 1 {
			t.Errorf("el job %s se reclamó %d veces, quería 1", id, n)
		}
	}
}
