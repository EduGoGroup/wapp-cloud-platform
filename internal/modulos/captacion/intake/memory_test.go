package intake_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

// Este test es EXTERNO (package intake_test): intakehelpertest importa intake, así que un test
// interno que importara la suite daría un ciclo de imports.

// Aserciones de compilación de lo que MemoryStore promete: el puerto y sus ganchos de test.
var (
	_ intake.JobStore                                                 = (*intake.MemoryStore)(nil)
	_ func(func() time.Time) *intake.MemoryStore                      = intake.NewMemoryStore
	_ func(*intake.MemoryStore, error)                                = (*intake.MemoryStore).FailOpenWith
	_ func(*intake.MemoryStore, error)                                = (*intake.MemoryStore).FailPutWith
	_ func(*intake.MemoryStore) intake.Counters                       = (*intake.MemoryStore).Counters
	_ func(*intake.MemoryStore)                                       = (*intake.MemoryStore).ResetCounters
	_ func(*intake.MemoryStore) []intake.Job                          = (*intake.MemoryStore).Jobs
	_ func(*intake.MemoryStore, intake.Job) string                    = (*intake.MemoryStore).Seed
	_ func(*intake.MemoryStore, context.Context, intake.Append) error = (*intake.MemoryStore).OpenOrAppend
)

// memoryClock es un reloj que solo avanza cuando el test lo mueve.
type memoryClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMemoryClock() *memoryClock {
	return &memoryClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *memoryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *memoryClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// memoryCases numera los Montajes para que cada caso tenga sus dos tenants.
var memoryCases atomic.Int64

// rowOf traduce una fila del gemelo a la fila entera de la suite. Las columnas de la máquina,
// que el gemelo de la cola no guarda, salen a cero.
func rowOf(j intake.Job) intakehelpertest.Row {
	return intakehelpertest.Row{
		ID: j.ID, Key: j.Key, Status: j.Status, MessageTS: j.MessageTS,
		SourceRefs: j.SourceRefs, SourceText: j.SourceText,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

// TestMemoryStore_ContratoQueue corre la suite de intake.JobStore contra el gemelo, sin BD y sin
// reloj real: cada caso monta un store nuevo con su reloj, y Advance lo adelanta un segundo.
func TestMemoryStore_ContratoQueue(t *testing.T) {
	intakehelpertest.ContratoQueue(t, func(*testing.T) intakehelpertest.QueueMontaje {
		clock := newMemoryClock()
		store := intake.NewMemoryStore(clock.Now)
		n := memoryCases.Add(1)
		return intakehelpertest.QueueMontaje{
			Store:   store,
			TenantA: fmt.Sprintf("tenant-%02d-a", n),
			TenantB: fmt.Sprintf("tenant-%02d-b", n),
			Rows: func(_ *testing.T, tenantID string) []intakehelpertest.Row {
				var out []intakehelpertest.Row
				for _, j := range store.Jobs() {
					if j.Key.TenantID == tenantID {
						out = append(out, rowOf(j))
					}
				}
				return out
			},
			// La siembra traduce al revés que rowOf: las columnas de la máquina se pierden,
			// porque el gemelo de la cola no las guarda.
			Seed: func(_ *testing.T, r intakehelpertest.Row) string {
				return store.Seed(intake.Job{
					ID: r.ID, Key: r.Key, Status: r.Status, MessageTS: r.MessageTS,
					SourceRefs: r.SourceRefs, SourceText: r.SourceText,
					CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
				})
			},
			Advance: func(*testing.T) { clock.Advance(time.Second) },
		}
	})
}

// TestMemoryStore_Seed_InsertsTheRowAsGivenAndFillsTheZeros: la siembra mete la fila tal cual
// —sin guarda ninguna— y rellena lo que viene a cero como lo haría la tabla; la fila se copia.
func TestMemoryStore_Seed_InsertsTheRowAsGivenAndFillsTheZeros(t *testing.T) {
	clock := newMemoryClock()
	store := intake.NewMemoryStore(clock.Now)
	created := clock.Now().Add(-time.Hour)
	refs := []string{"r1"}
	given := intake.Job{
		ID: "mine", Key: key("e1"), Status: intake.StatusAggregating, SourceRefs: refs,
		SourceText: fullEnvelope(), CreatedAt: created, UpdatedAt: created.Add(time.Minute),
	}
	if id := store.Seed(given); id != "mine" {
		t.Errorf("Seed con id = %q, quería %q", id, "mine")
	}
	refs[0] = "mutated"
	id := store.Seed(intake.Job{Key: key("e2")})
	jobs := store.Jobs()
	if len(jobs) != 2 {
		t.Fatalf("Jobs = %d filas, quería 2", len(jobs))
	}
	if got := jobs[0]; got.Status != intake.StatusAggregating || !got.CreatedAt.Equal(created) ||
		!got.UpdatedAt.Equal(created.Add(time.Minute)) || got.SourceRefs[0] != "r1" || !got.SourceText.Complete() {
		t.Errorf("fila sembrada = %+v, quería la dada tal cual", got)
	}
	if got := jobs[1]; id == "" || got.ID != id || got.Status != intake.StatusPending ||
		!got.CreatedAt.Equal(clock.Now()) || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("fila sembrada a cero = %+v (id %q), quería id propio, pending y las dos marcas del reloj", got, id)
	}
}

// key devuelve una clave de ventana completa para el evento dado.
func key(event string) intake.WindowKey {
	return intake.WindowKey{TenantID: "tenant-a", SessionID: "session-a", ContactID: "contact-a", EventID: event}
}

// fullEnvelope es un sobre completo.
func fullEnvelope() intake.SourceText {
	return intake.SourceText{Enc: []byte("enc"), DEK: []byte("dek"), KEKID: "k1"}
}

// TestNewMemoryStore_NilClock_UsesTheProcessClock: sin reloj inyectado fecha con el del proceso.
func TestNewMemoryStore_NilClock_UsesTheProcessClock(t *testing.T) {
	store := intake.NewMemoryStore(nil)
	earliest := time.Now()
	if err := store.OpenOrAppend(context.Background(), intake.Append{Key: key("e1")}); err != nil {
		t.Fatalf("OpenOrAppend: error inesperado %v", err)
	}
	jobs := store.Jobs()
	if len(jobs) != 1 || jobs[0].CreatedAt.Before(earliest) || jobs[0].CreatedAt.After(time.Now()) {
		t.Errorf("Jobs = %+v, quería una fila fechada con el reloj del proceso", jobs)
	}
}

// TestMemoryStore_Counters_CountCallsNotEffects: el presupuesto de I/O cuenta LLAMADAS —las que
// escriben, las que no encuentran nada y las que fallan—, cada operación en su contador; es lo
// que deja afirmar que dos cierres LLEGARON al guard. Counters devuelve una copia y
// ResetCounters lo pone a cero sin tocar las filas.
func TestMemoryStore_Counters_CountCallsNotEffects(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	ctx := context.Background()
	if got := store.Counters(); got != (intake.Counters{}) {
		t.Fatalf("Counters de un store nuevo = %+v, quería todo a cero", got)
	}
	k := key("e1")
	for range 3 {
		if err := store.OpenOrAppend(ctx, intake.Append{Key: k, Refs: []string{"r"}}); err != nil {
			t.Fatalf("OpenOrAppend: error inesperado %v", err)
		}
	}
	for range 2 { // el segundo no encuentra ventana viva, y cuenta igual
		if _, err := store.CloseWindow(ctx, k); err != nil {
			t.Fatalf("CloseWindow: error inesperado %v", err)
		}
	}
	for _, limit := range []int{5, 0, 5, -1} { // un límite no positivo también es una lectura
		if _, err := store.ListAggregating(ctx, limit); err != nil {
			t.Fatalf("ListAggregating: error inesperado %v", err)
		}
	}
	if _, err := store.PutSourceText(ctx, k, fullEnvelope()); err != nil {
		t.Fatalf("PutSourceText: error inesperado %v", err)
	}
	if _, err := store.PutSourceText(ctx, k, intake.SourceText{}); err == nil { // rechazado, y cuenta
		t.Fatal("PutSourceText con un sobre vacío: quería error")
	}
	want := intake.Counters{OpenOrAppend: 3, Close: 2, Reads: 4, PutSourceText: 2}
	got := store.Counters()
	if got != want {
		t.Errorf("Counters = %+v, quería %+v", got, want)
	}
	got.OpenOrAppend = 99
	if again := store.Counters(); again != want {
		t.Errorf("mutar la copia cambió el presupuesto del store: %+v", again)
	}

	store.ResetCounters()
	if got := store.Counters(); got != (intake.Counters{}) {
		t.Errorf("Counters tras ResetCounters = %+v, quería todo a cero", got)
	}
	if jobs := store.Jobs(); len(jobs) != 1 {
		t.Errorf("ResetCounters dejó %d filas, quería la que había", len(jobs))
	}
}

// TestMemoryStore_FailOpenWith_FailsWithoutWriting: con el fallo puesto, OpenOrAppend devuelve
// ESE error, no abre ni amplía nada, y la llamada cuenta; nil vuelve a la normalidad.
func TestMemoryStore_FailOpenWith_FailsWithoutWriting(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	ctx := context.Background()
	k := key("e1")
	if err := store.OpenOrAppend(ctx, intake.Append{Key: k, Refs: []string{"one"}}); err != nil {
		t.Fatalf("OpenOrAppend: error inesperado %v", err)
	}
	boom := errors.New("la base no contesta")
	store.FailOpenWith(boom)
	for _, k := range []intake.WindowKey{k, key("e2")} {
		if err := store.OpenOrAppend(ctx, intake.Append{Key: k, Refs: []string{"two"}}); !errors.Is(err, boom) {
			t.Errorf("OpenOrAppend con el fallo puesto = %v, quería %v", err, boom)
		}
	}
	jobs := store.Jobs()
	if len(jobs) != 1 || len(jobs[0].SourceRefs) != 1 {
		t.Errorf("con el fallo puesto las filas son %+v, quería la ventana inicial con su única referencia", jobs)
	}
	if got := store.Counters().OpenOrAppend; got != 3 {
		t.Errorf("Counters.OpenOrAppend = %d, quería 3 (las fallidas cuentan)", got)
	}
	store.FailOpenWith(nil)
	if err := store.OpenOrAppend(ctx, intake.Append{Key: k, Refs: []string{"three"}}); err != nil {
		t.Errorf("OpenOrAppend tras quitar el fallo: error inesperado %v", err)
	}
}

// TestMemoryStore_FailPutWith_LeavesTheClosedWindowWithoutEnvelope: con el fallo puesto,
// PutSourceText devuelve (false, ESE error) —antes incluso de mirar el sobre— y la ventana se
// queda `pending` con el sobre vacío; nil vuelve a la normalidad.
func TestMemoryStore_FailPutWith_LeavesTheClosedWindowWithoutEnvelope(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	ctx := context.Background()
	k := key("e1")
	if err := store.OpenOrAppend(ctx, intake.Append{Key: k}); err != nil {
		t.Fatalf("OpenOrAppend: error inesperado %v", err)
	}
	if ok, err := store.CloseWindow(ctx, k); err != nil || !ok {
		t.Fatalf("CloseWindow = (%v, %v), quería (true, nil)", ok, err)
	}
	boom := errors.New("el compositor no pudo")
	store.FailPutWith(boom)
	for name, env := range map[string]intake.SourceText{"completo": fullEnvelope(), "incompleto": {}} {
		if ok, err := store.PutSourceText(ctx, k, env); !errors.Is(err, boom) || ok {
			t.Errorf("PutSourceText (sobre %s) con el fallo puesto = (%v, %v), quería (false, %v)", name, ok, err, boom)
		}
	}
	if job := store.Jobs()[0]; job.Status != intake.StatusPending || job.SourceText.Complete() || len(job.SourceText.Enc) != 0 {
		t.Errorf("tras el fallo la fila es (status %q, sobre %+v), quería pending y sin sobre", job.Status, job.SourceText)
	}
	store.FailPutWith(nil)
	if ok, err := store.PutSourceText(ctx, k, fullEnvelope()); err != nil || !ok {
		t.Errorf("PutSourceText tras quitar el fallo = (%v, %v), quería (true, nil)", ok, err)
	}
}

// TestMemoryStore_PutSourceText_IncompleteEnvelopeMessage: el rechazo del sobre a medias lleva
// el texto del gemelo viejo, byte a byte.
func TestMemoryStore_PutSourceText_IncompleteEnvelopeMessage(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	_, err := store.PutSourceText(context.Background(), key("e1"), intake.SourceText{Enc: []byte("enc")})
	const want = "intake: sobre del literal incompleto (son las tres o ninguna)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, quería %q", err, want)
	}
}

// TestMemoryStore_IncompleteKey_CountsAndComesBeforeTheInjectedFailure: la llamada rechazada por
// su clave CUENTA en el presupuesto (cuenta llamadas, como las fallidas), y la clave se mira antes
// que el fallo inyectado, que simula la base: a Postgres esa llamada ni le llega.
func TestMemoryStore_IncompleteKey_CountsAndComesBeforeTheInjectedFailure(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	ctx := context.Background()
	boom := errors.New("la base no contesta")
	store.FailOpenWith(boom)
	store.FailPutWith(boom)
	noEvent := key("")
	if err := store.OpenOrAppend(ctx, intake.Append{Key: noEvent}); err == nil || errors.Is(err, boom) {
		t.Errorf("OpenOrAppend = %v, quería el rechazo de la clave y no el fallo inyectado", err)
	}
	if ok, err := store.CloseWindow(ctx, noEvent); err == nil || ok {
		t.Errorf("CloseWindow = (%v, %v), quería (false, el rechazo de la clave)", ok, err)
	}
	if ok, err := store.PutSourceText(ctx, noEvent, fullEnvelope()); err == nil || errors.Is(err, boom) || ok {
		t.Errorf("PutSourceText = (%v, %v), quería (false, el rechazo de la clave) y no el fallo inyectado", ok, err)
	}
	if got, want := store.Counters(), (intake.Counters{OpenOrAppend: 1, Close: 1, PutSourceText: 1}); got != want {
		t.Errorf("Counters = %+v, quería %+v (las rechazadas cuentan)", got, want)
	}
	if jobs := store.Jobs(); len(jobs) != 0 {
		t.Errorf("las llamadas rechazadas dejaron %d filas: %+v", len(jobs), jobs)
	}
}

// TestMemoryStore_Jobs_CopiesInCreationOrder: Jobs devuelve las filas en orden de creación —con
// más de nueve también: "job-10" va después de "job-9"—, con ids "job-N" correlativos, y como
// copias: mutar lo devuelto no toca el store.
func TestMemoryStore_Jobs_CopiesInCreationOrder(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	ctx := context.Background()
	const windows = 12
	for i := 1; i <= windows; i++ {
		a := intake.Append{Key: key(fmt.Sprintf("event-%d", i)), Refs: []string{fmt.Sprintf("ref-%d", i)}}
		if err := store.OpenOrAppend(ctx, a); err != nil {
			t.Fatalf("OpenOrAppend: error inesperado %v", err)
		}
	}
	jobs := store.Jobs()
	if len(jobs) != windows {
		t.Fatalf("Jobs devolvió %d filas, quería %d", len(jobs), windows)
	}
	for i, j := range jobs {
		if want := fmt.Sprintf("job-%d", i+1); j.ID != want || j.Key.EventID != fmt.Sprintf("event-%d", i+1) {
			t.Errorf("fila %d = (%q, %q), quería (%q, event-%d): orden de creación", i, j.ID, j.Key.EventID, want, i+1)
		}
	}

	k := key("event-1")
	if _, err := store.CloseWindow(ctx, k); err != nil {
		t.Fatalf("CloseWindow: error inesperado %v", err)
	}
	if _, err := store.PutSourceText(ctx, k, fullEnvelope()); err != nil {
		t.Fatalf("PutSourceText: error inesperado %v", err)
	}
	first := store.Jobs()[0]
	first.SourceRefs[0] = "mutated"
	first.SourceText.Enc[0] = 'X'
	first.SourceText.DEK[0] = 'X'
	first.Status = intake.StatusDone
	again := store.Jobs()[0]
	if again.SourceRefs[0] != "ref-1" || string(again.SourceText.Enc) != "enc" || string(again.SourceText.DEK) != "dek" ||
		again.Status != intake.StatusPending {
		t.Errorf("mutar lo que devuelve Jobs cambió el store: %+v", again)
	}
}

// TestMemoryStore_OpenOrAppend_DoesNotKeepTheCallersSlice: las referencias se copian al abrir;
// que el llamante reutilice su slice no cambia la fila.
func TestMemoryStore_OpenOrAppend_DoesNotKeepTheCallersSlice(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	refs := []string{"one", "two"}
	if err := store.OpenOrAppend(context.Background(), intake.Append{Key: key("e1"), Refs: refs}); err != nil {
		t.Fatalf("OpenOrAppend: error inesperado %v", err)
	}
	refs[0] = "mutated"
	if got := store.Jobs()[0].SourceRefs; got[0] != "one" {
		t.Errorf("source_refs = %v: la fila comparte el slice del llamante", got)
	}
}

// TestMemoryStore_ConcurrentAppends_OneLiveWindowPerKey: con -race. Muchos entrantes a la vez
// sobre la misma tupla dejan UNA ventana viva con todas sus referencias.
func TestMemoryStore_ConcurrentAppends_OneLiveWindowPerKey(t *testing.T) {
	store := intake.NewMemoryStore(newMemoryClock().Now)
	const senders = 32
	var wg sync.WaitGroup
	for i := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := intake.Append{Key: key("e1"), Refs: []string{fmt.Sprintf("ref-%d", i)}}
			if err := store.OpenOrAppend(context.Background(), a); err != nil {
				t.Errorf("OpenOrAppend: error inesperado %v", err)
			}
			if _, err := store.ListAggregating(context.Background(), 10); err != nil {
				t.Errorf("ListAggregating: error inesperado %v", err)
			}
			_ = store.Counters()
		}()
	}
	wg.Wait()
	jobs := store.Jobs()
	if len(jobs) != 1 || len(jobs[0].SourceRefs) != senders || jobs[0].Status != intake.StatusAggregating {
		t.Fatalf("Jobs = %d filas (refs de la primera: %d), quería 1 ventana viva con %d referencias", len(jobs), len(jobs[0].SourceRefs), senders)
	}
}
