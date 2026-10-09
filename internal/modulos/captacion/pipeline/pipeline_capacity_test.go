package pipeline_test

// pipeline_capacity_test.go — el aforo VISTO DESDE EL WORKER (R-01), tal como lo promete
// RunOnce: cuándo se toma la plaza, cuánto se retiene y qué pasa cuando no hay plaza que
// tomar. El aforo en sí (Capacity) está en slot_test.go.
//
// 🔴 AQUÍ NO SE MIDE NINGÚN TIEMPO. Un test de aforo con `sleep` y cronómetro es flaky por
// construcción. Lo que se registra son CUENTAS: cada cadena anota que ha entrado, se frena
// en un canal, y las afirmaciones se hacen cuando todas las cadenas lanzadas están
// CONTADAS —dentro de la cadena, o bloqueadas pidiendo plaza—, que es una condición
// positiva: llega siempre, con aforo y sin él, así que un defecto da un fallo limpio y no
// un cuelgue.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
)

// questions son las preguntas que se le hicieron al doble de Slots (slot_test.go).
func (f *fakeSlots) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// bench son N cadenas de lote lanzadas a la vez contra un aforo compartido: un worker por
// job —que es lo que modela varias goroutines o réplicas sobre la misma cola— y el aforo
// como lo ÚNICO que comparten.
type bench struct {
	*rig
	capacity *pipeline.Capacity
	slots    *fakeSlots
	gate     chan struct{}
	wg       sync.WaitGroup

	mu       sync.Mutex
	inFlight map[string]int // cadenas entre la entrada de P2 y la salida de draft, por Edge
	overlaps []string
	entered  int // entradas a una etapa frenada (P2 o draft)
}

// newBench siembra un job por sesión y frena cada cadena DOS veces: al entrar en P2 (su
// primera etapa) y al entrar en draft (la última).
func newBench(t *testing.T, edges map[string]string) *bench {
	t.Helper()
	b := &bench{
		rig: newParts(t), capacity: pipeline.NewCapacity(pipeline.KPerSlot),
		slots: &fakeSlots{edges: edges}, gate: make(chan struct{}), inFlight: map[string]int{},
	}
	for session := range edges {
		row := b.healthyRow("job-" + session)
		row.Key.SessionID = session
		b.mem.Seed(row)
	}
	b.p2.around = func(job intake.ClaimedJob) func() {
		edge := edges[job.Key.SessionID]
		b.mu.Lock()
		b.inFlight[edge]++
		if b.inFlight[edge] > 1 {
			b.overlaps = append(b.overlaps, fmt.Sprintf("%s entró con %d cadenas sobre %s", job.ID, b.inFlight[edge], edge))
		}
		b.entered++
		b.mu.Unlock()
		<-b.gate
		return func() {}
	}
	b.draft.around = func(job intake.ClaimedJob) func() {
		b.mu.Lock()
		b.entered++
		b.mu.Unlock()
		<-b.gate
		return func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.inFlight[edges[job.Key.SessionID]]--
		}
	}
	return b
}

// launch arranca `n` workers, cada uno con UNA vuelta.
func (b *bench) launch(ctx context.Context, t *testing.T, n int) {
	t.Helper()
	for range n {
		w := b.build(t, pipeline.Config{}, pipeline.WithClock(b.clock.Now), pipeline.WithCapacity(b.capacity, b.slots))
		b.wg.Go(func() {
			if _, err := w.RunOnce(ctx); err != nil {
				t.Errorf("RunOnce: %v", err)
			}
		})
	}
}

// chains son las cadenas en vuelo ahora, sumados todos los Edges.
func (b *bench) chains() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, v := range b.inFlight {
		n += v
	}
	return n
}

func (b *bench) entries() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entered
}

// counted es la condición de sincronía: las `n` cadenas están o en vuelo o pidiendo plaza.
func (b *bench) counted(n int) func() bool {
	return func() bool { return b.chains()+b.capacity.Waiting() >= n }
}

// release suelta `n` frenos.
func (b *bench) release(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case b.gate <- struct{}{}:
		case <-time.After(waitLimit):
			t.Fatalf("nadie recogió el freno %d de %d: una cadena no llegó a su etapa", i+1, n)
		}
	}
}

// finish suelta los frenos que quedan, espera a todas las cadenas y exige cero solapes.
func (b *bench) finish(t *testing.T, brakes int) {
	t.Helper()
	b.release(t, brakes)
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatalf("las cadenas no terminaron en %v", waitLimit)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.overlaps) != 0 {
		t.Errorf("hubo solapes sobre la misma plaza: %v", b.overlaps)
	}
}

// TestCapacity_TwoChainsOfTheSameEdgeNeverOverlap: con K = 1, dos jobs del MISMO Edge
// nunca están en vuelo a la vez, y la plaza se retiene la CADENA ENTERA: mientras la
// primera está en su última etapa, la segunda sigue esperando. El que espera NO falla ni
// paga intento: `attempts` en 0 distingue «esperó» de «se reencoló castigado».
func TestCapacity_TwoChainsOfTheSameEdgeNeverOverlap(t *testing.T) {
	b := newBench(t, map[string]string{"sess-1": "edge-1", "sess-2": "edge-1"})
	b.launch(context.Background(), t, 2)

	eventually(t, "las dos cadenas contadas", b.counted(2))
	if n, waiting := b.chains(), b.capacity.Waiting(); n != 1 || waiting != 1 {
		t.Fatalf("hay %d cadena(s) en vuelo y %d esperando sobre la MISMA plaza; se esperaba 1 y 1", n, waiting)
	}

	// La primera pasa de P2 y llega a draft: la plaza sigue siendo suya.
	b.release(t, 1)
	eventually(t, "la primera cadena en su última etapa", func() bool { return b.entries() == 2 })
	if n, waiting := b.chains(), b.capacity.Waiting(); n != 1 || waiting != 1 {
		t.Fatalf("entre dos etapas de la primera hay %d cadena(s) en vuelo y %d esperando; "+
			"el entero cuenta CADENAS, no llamadas", n, waiting)
	}

	b.finish(t, 3)
	for _, id := range []string{"job-sess-1", "job-sess-2"} {
		row := b.row(t, id)
		if row.Status != intake.StatusDone || row.Attempts != 0 {
			t.Errorf("%s quedó (%q, attempts=%d, error=%q); esperar NO es fallar ni cuesta intentos",
				id, row.Status, row.Attempts, row.Error)
		}
	}
	if got := b.store.closesOf(opRelease); len(got) != 0 {
		t.Errorf("hubo %d devoluciones a la cola; el que espera no suelta su job", len(got))
	}
}

// TestCapacity_DifferentEdgesDoOverlap: el entero es POR EDGE, no por proceso. Este test
// EXIGE el solapamiento: un K = 1 global serializaría los presupuestos de todos los
// clientes detrás del más lento.
func TestCapacity_DifferentEdgesDoOverlap(t *testing.T) {
	b := newBench(t, map[string]string{"sess-1": "edge-1", "sess-2": "edge-2"})
	b.launch(context.Background(), t, 2)

	eventually(t, "las dos cadenas contadas", b.counted(2))
	if n, waiting := b.chains(), b.capacity.Waiting(); n != 2 || waiting != 0 {
		t.Fatalf("con DOS Edges distintos hay %d cadena(s) en vuelo y %d esperando; se esperaba 2 y 0", n, waiting)
	}
	b.finish(t, 4)
}

// TestCapacity_AnInteractiveTurnNeverFindsMoreThanOneBatchChainAhead: con CUATRO cadenas
// sobre la misma plaza, un turno interactivo encuentra UNA por delante y tres en fila; sin
// el entero serían cuatro.
func TestCapacity_AnInteractiveTurnNeverFindsMoreThanOneBatchChainAhead(t *testing.T) {
	b := newBench(t, map[string]string{
		"sess-1": "edge-1", "sess-2": "edge-1", "sess-3": "edge-1", "sess-4": "edge-1",
	})
	b.launch(context.Background(), t, 4)

	eventually(t, "las cuatro cadenas contadas", b.counted(4))
	if n, waiting := b.chains(), b.capacity.Waiting(); n != 1 || waiting != 3 {
		t.Fatalf("hay %d cadena(s) en vuelo y %d esperando; con una sola plaza se esperaba 1 y 3", n, waiting)
	}
	b.finish(t, 8)
}

// TestCapacity_WithoutASlotNothingIsSerialized: por vía API el entero no aplica y un
// tenant sin Edge vivo tampoco ocupa nada. El worker no distingue los casos —ni debe—: con
// `ok = false`, o con una dirección a medias, no serializa.
func TestCapacity_WithoutASlotNothingIsSerialized(t *testing.T) {
	cases := map[string]func(*bench){
		"the selector says there is no slot": func(b *bench) { b.slots.noSlot = true },
		"the edge comes back empty":          func(b *bench) { b.slots.edges = map[string]string{} },
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBench(t, map[string]string{"sess-1": "edge-1", "sess-2": "edge-1"})
			prepare(b)
			b.launch(context.Background(), t, 2)

			eventually(t, "las dos cadenas en vuelo", func() bool { return b.entries() == 2 })
			if waiting := b.capacity.Waiting(); waiting != 0 {
				t.Fatalf("%d cadena(s) esperando plaza; un tenant SIN plaza no se serializa", waiting)
			}
			b.release(t, 4)
			b.wg.Wait()
			if got := len(b.log.find("el job no ocupa plaza")); got != 2 {
				t.Errorf("hay %d líneas de «no ocupa plaza», se esperaba una por job", got)
			}
		})
	}
}

// TestCapacity_ASlotThatCannotBeResolved_DoesNotStopTheJob: si no se pudo preguntar, la
// cadena sigue SIN aforo y con un aviso. No es un tropiezo aquí: si la config del tenant no
// se lee, la primera etapa fallará por lo mismo y se clasificará una sola vez.
func TestCapacity_ASlotThatCannotBeResolved_DoesNotStopTheJob(t *testing.T) {
	r := newParts(t)
	slots := &fakeSlots{err: errors.New("tenant_llm no contesta")}
	r.build(t, pipeline.Config{}, pipeline.WithClock(r.clock.Now),
		pipeline.WithCapacity(pipeline.NewCapacity(pipeline.KPerSlot), slots))
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	line := r.log.one(t, "WARN", "no se pudo resolver la plaza del job; la cadena sigue SIN aforo")
	line.requireKeys(t, "job_id", "tenant_id", "error")
	if got := r.row(t, id).Attempts; got != 0 {
		t.Errorf("attempts = %d; no poder preguntar por la plaza no es un tropiezo", got)
	}
}

// TestCapacity_TheSlotIsAskedForTheJobTenantAndSession_AfterOpeningTheEnvelope: la
// dirección sale del tenant y de la SESIÓN de origen del job; y a un job cuyo sobre no abre
// ni se le pregunta: tomar la plaza antes la retendría para nada.
func TestCapacity_TheSlotIsAskedForTheJobTenantAndSession_AfterOpeningTheEnvelope(t *testing.T) {
	r := newParts(t)
	slots := &fakeSlots{edges: map[string]string{sessionID: "edge-1"}}
	capacity := pipeline.NewCapacity(pipeline.KPerSlot)
	r.build(t, pipeline.Config{}, pipeline.WithClock(r.clock.Now), pipeline.WithCapacity(capacity, slots))

	sealed := r.healthyRow("sealed")
	sealed.SourceText = intake.SourceText{}
	r.mem.Seed(sealed)
	r.run(t, "sealed", intake.StatusFailed)
	if got := slots.questions(); len(got) != 0 {
		t.Fatalf("se preguntó por la plaza (%v) de un job cuyo sobre no abre", got)
	}

	id := r.seed("")
	r.run(t, id, intake.StatusDone)
	if got := slots.questions(); !reflect.DeepEqual(got, []string{tenantID + "/" + sessionID}) {
		t.Fatalf("se preguntó por %v, se esperaba una vez por el tenant y la sesión del job", got)
	}
}

// TestCapacity_TheSlotIsReleasedHoweverTheJobEnds: termine bien o tropiece, el job suelta
// la plaza; si no, el siguiente del mismo Edge esperaría para siempre.
func TestCapacity_TheSlotIsReleasedHoweverTheJobEnds(t *testing.T) {
	r := newParts(t)
	slots := &fakeSlots{edges: map[string]string{sessionID: "edge-1"}}
	capacity := pipeline.NewCapacity(pipeline.KPerSlot)
	r.build(t, pipeline.Config{}, pipeline.WithClock(r.clock.Now), pipeline.WithCapacity(capacity, slots))
	r.p2.script = []step{{err: errors.New("el Edge no contesta")}, {}, {}}
	r.seed("job-a") // tropieza
	r.clock.Advance(time.Second)
	r.seed("job-b") // termina
	r.clock.Advance(time.Second)
	r.seed("job-c") // termina

	done := make(chan int, 1)
	go func() { done <- r.drain(context.Background()) }()
	select {
	case n := <-done:
		if n != 3 {
			t.Fatalf("Drain procesó %d jobs, se esperaban 3", n)
		}
	case <-time.After(waitLimit):
		t.Fatal("el drenaje se quedó colgado: un job no soltó su plaza")
	}
	// La plaza quedó libre: se puede volver a tomar sin esperar.
	free, err := capacity.Acquire(context.Background(), pipeline.Slot{TenantID: tenantID, EdgeID: "edge-1"})
	if err != nil {
		t.Fatalf("Acquire tras el drenaje: %v", err)
	}
	free()
}

// TestCapacity_ShutdownWhileWaiting_ReturnsTheJobUnpunished: si el ctx muere ESPERANDO
// plaza, el job vuelve a `pending` TAL COMO ESTABA —ni intento cobrado ni marca empujada—
// y no se llama a ninguna etapa. No es culpa del job.
func TestCapacity_ShutdownWhileWaiting_ReturnsTheJobUnpunished(t *testing.T) {
	b := newBench(t, map[string]string{"sess-1": "edge-1"})
	edge := pipeline.Slot{TenantID: tenantID, EdgeID: "edge-1"}
	held, err := b.capacity.Acquire(context.Background(), edge) // otro tiene la plaza
	if err != nil {
		t.Fatalf("ocupar la plaza: %v", err)
	}
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	b.launch(ctx, t, 1)
	eventually(t, "la cadena esperando plaza", func() bool { return b.capacity.Waiting() == 1 })
	cancel()
	b.wg.Wait()

	row := b.row(t, "job-sess-1")
	if row.Status != intake.StatusPending || row.Attempts != 0 || !row.NextAttemptAt.IsZero() {
		t.Fatalf("el job quedó (%q, attempts=%d, next_attempt_at=%s); se esperaba pending, sin intento y sin marca",
			row.Status, row.Attempts, row.NextAttemptAt)
	}
	if calls := b.trace.list(); len(calls) != 0 {
		t.Errorf("se llamó a %v; sin plaza no se llama a ninguna etapa", calls)
	}
	releases := b.store.closesOf(opRelease)
	if len(releases) != 1 || !releases[0].alive {
		t.Fatalf("devoluciones = %+v; se esperaba UNA, escrita con un ctx que sobrevive a la cancelación", releases)
	}
	line := b.log.one(t, "INFO", "job devuelto a la cola SIN castigo")
	if line.fields["motivo"] != "el worker se apagó esperando plaza" {
		t.Errorf("motivo = %v", line.fields["motivo"])
	}
	b.log.requireNoErrors(t)
}

// TestCapacity_TheWaitIsOnlyAnnouncedWhenThereIsAlreadyAQueue: el Info de la espera sale
// SOLO cuando ya hay otras cadenas esperando esa cola; emitirlo siempre lo volvería ruido.
func TestCapacity_TheWaitIsOnlyAnnouncedWhenThereIsAlreadyAQueue(t *testing.T) {
	cases := []struct {
		name      string
		queue     int
		announced bool
	}{
		{"the slot is taken but nobody waits", 0, false},
		{"another chain is already waiting", 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newBench(t, map[string]string{"sess-1": "edge-1"})
			edge := pipeline.Slot{TenantID: tenantID, EdgeID: "edge-1"}
			held, err := b.capacity.Acquire(context.Background(), edge)
			if err != nil {
				t.Fatalf("ocupar la plaza: %v", err)
			}
			defer held()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for range c.queue {
				acquireInBackground(ctx, b.capacity, edge)
			}
			eventually(t, "la cola previa formada", func() bool { return b.capacity.Waiting() == c.queue })

			b.launch(ctx, t, 1)
			eventually(t, "la cadena del worker esperando plaza", func() bool { return b.capacity.Waiting() == c.queue+1 })
			lines := b.log.find("la plaza del Edge está ocupada; este job ESPERA")
			if (len(lines) == 1) != c.announced {
				t.Fatalf("avisos de espera = %d, se esperaba aviso = %v", len(lines), c.announced)
			}
			if c.announced {
				l := lines[0]
				if l.level != "INFO" || l.fields["plaza"] != "tenant-1/edge-1" || l.fields["esperando"] != 1 || l.fields["job_id"] != "job-sess-1" {
					t.Errorf("el aviso salió %+v; se esperaba un INFO con su job, su plaza y la cola que encontró", l)
				}
			}
			cancel()
			b.wg.Wait()
		})
	}
}
