package pipeline_test

// doubles_stages_test.go — LAS ETAPAS FALSAS Y EL BANCO (sin fichero de producción gemelo).
//
// Las etapas falsas PERSISTEN de verdad su artefacto en la máquina en memoria cuando salen
// bien: así la reanudación que se prueba es la real (artefactos que dejó una corrida
// anterior) y no una escenografía escrita a mano. Lo que hace cada etapa de verdad lo
// prueban los tests de `stages`; aquí solo importa qué les pasa el worker y qué hace con
// lo que devuelven.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline/pipelinehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// step es lo que una etapa falsa hace en su i-ésima llamada. Un guion más corto que el
// número de llamadas repite su última entrada: «falla siempre» es una entrada, no diez.
type step struct {
	err error
	// takes es lo que el reloj falso avanza durante la llamada: lo que hace que
	// `elapsed_ms` sea distinto de cero sin dormir.
	takes time.Duration
}

// callTrace apunta el orden en que el worker llamó a las etapas.
type callTrace struct {
	mu    sync.Mutex
	names []string
}

func (c *callTrace) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = append(c.names, name)
}

func (c *callTrace) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.names...)
}

// stageBase es lo común a las cinco falsas: el guion, el contador, el reloj y los ganchos.
type stageBase struct {
	name  string
	store stages.StageStore
	clock *fakeClock
	trace *callTrace

	mu     sync.Mutex
	script []step
	calls  int
	// bounded apunta, por llamada, si el ctx que llegó traía plazo.
	bounded []bool
	// around corre al entrar y devuelve lo que corre al salir. Es el hueco por el que un
	// test mete «otro worker terminó el job mientras esta etapa corría», o frena la etapa.
	around func(job intake.ClaimedJob) func()
}

// begin cuenta la llamada, corre el gancho y avanza el reloj; devuelve la salida del
// gancho y el error del guion.
func (b *stageBase) begin(ctx context.Context, job intake.ClaimedJob) (func(), error) {
	b.mu.Lock()
	st := step{}
	if len(b.script) > 0 {
		st = b.script[min(b.calls, len(b.script)-1)]
	}
	b.calls++
	_, has := ctx.Deadline()
	b.bounded = append(b.bounded, has)
	around := b.around
	b.mu.Unlock()

	b.trace.add(b.name)
	leave := func() {}
	if around != nil {
		leave = around(job)
	}
	if st.takes > 0 {
		b.clock.Advance(st.takes)
	}
	return leave, st.err
}

func (b *stageBase) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// anyBounded dice si alguna llamada recibió un ctx con plazo.
func (b *stageBase) anyBounded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, has := range b.bounded {
		if has {
			return true
		}
	}
	return false
}

// persist deja el artefacto en la máquina, como hace una etapa real.
func (b *stageBase) persist(ctx context.Context, jobID string, art any) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return err
	}
	ok, err := b.store.SaveStage(ctx, jobID, intake.Artifact{Stage: b.name, Payload: payload})
	if err != nil {
		return err
	}
	if !ok {
		return stages.ErrJobNotProcessing
	}
	return nil
}

// fakeP2 implementa pipeline.IdeasStage.
type fakeP2 struct {
	stageBase
	wants []llm.Want
	hint  *llm.Hint
	// literal es el que recibió en su última llamada.
	literal string
}

func (e *fakeP2) Run(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error) {
	leave, err := e.begin(ctx, job)
	defer leave()
	e.mu.Lock()
	e.literal = literal
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	art := &llm.MainIdeas{Version: llm.ArtifactVersion, Wants: e.wants, DeliveryHint: e.hint}
	return art, e.persist(ctx, job.ID, art)
}

// fakeP3 implementa pipeline.SpecsStage.
type fakeP3 struct {
	stageBase
	items []llm.ItemSpec
	// literal y wants son lo que recibió en su última llamada.
	literal string
	wants   []llm.Want
}

func (e *fakeP3) Run(ctx context.Context, job intake.ClaimedJob, literal string, wants []llm.Want) (*stages.P3Artifact, error) {
	leave, err := e.begin(ctx, job)
	defer leave()
	e.mu.Lock()
	e.literal, e.wants = literal, wants
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	art := &stages.P3Artifact{Version: llm.ArtifactVersion, Items: e.items}
	return art, e.persist(ctx, job.ID, art)
}

// fakeP4 implementa pipeline.NormalizationStage.
type fakeP4 struct {
	stageBase
	// date es la fecha de entrega ABSOLUTA que «calcula»: el único dato que viaja de P4 al
	// borrador saltándose el match.
	date string
	// literal, items y hint son lo que recibió en su última llamada.
	literal string
	items   []llm.ItemSpec
	hint    *llm.Hint
}

func (e *fakeP4) Run(ctx context.Context, job intake.ClaimedJob, literal string,
	items []llm.ItemSpec, hint *llm.Hint) (*llm.Quantities, error) {
	leave, err := e.begin(ctx, job)
	defer leave()
	e.mu.Lock()
	e.literal, e.items, e.hint = literal, items, hint
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	norm := make([]llm.NormalizedItem, 0, len(items))
	for range items {
		norm = append(norm, llm.NormalizedItem{Qty: 1})
	}
	art := &llm.Quantities{Version: llm.ArtifactVersion, Items: norm, DeliveryDate: e.date}
	return art, e.persist(ctx, job.ID, art)
}

// fakeMatch implementa pipeline.MatchStage y GUARDA la entrada que recibió: es la única
// forma de afirmar que el worker le pasó el índice y las zonas en vez de llamarla con la
// mano vacía, que compilaría igual.
type fakeMatch struct {
	stageBase
	input stages.MatchInput
	got   bool
}

func (e *fakeMatch) Run(ctx context.Context, job intake.ClaimedJob, in stages.MatchInput) (*stages.MatchArtifact, error) {
	leave, err := e.begin(ctx, job)
	defer leave()
	e.mu.Lock()
	e.input, e.got = in, true
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	n := 0
	if in.Quantities != nil {
		n = len(in.Quantities.Items)
	}
	// Una línea por ítem más la de envío: que el número dependa de lo que P4 dejó.
	lines := make([]stages.Line, 0, n+1)
	for i := range n {
		lines = append(lines, stages.Line{Kind: stages.KindUnmatched, Label: fmt.Sprintf("item-%d", i), Qty: 1})
	}
	lines = append(lines, stages.Line{Kind: stages.KindShipping, Label: "Envío por confirmar", Qty: 1})
	art := &stages.MatchArtifact{Version: llm.ArtifactVersion, Lines: lines}
	return art, e.persist(ctx, job.ID, art)
}

func (e *fakeMatch) seen() (stages.MatchInput, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.input, e.got
}

// fakeDraft implementa pipeline.DraftStage. Devuelve SIEMPRE el mismo `intake_id`.
type fakeDraft struct {
	stageBase
	intakeID string
	input    stages.DraftInput
	job      intake.ClaimedJob
	got      bool
}

func (e *fakeDraft) Run(ctx context.Context, job intake.ClaimedJob, in stages.DraftInput) (*stages.DraftArtifact, error) {
	leave, err := e.begin(ctx, job)
	defer leave()
	e.mu.Lock()
	e.input, e.job, e.got = in, job, true
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	art := &stages.DraftArtifact{Version: intakes.RevisionPayloadVersion, IntakeID: e.intakeID, RevisionNo: 1}
	if in.Match != nil {
		art.Lines = len(in.Match.Lines)
	}
	return art, e.persist(ctx, job.ID, art)
}

func (e *fakeDraft) seen() (stages.DraftInput, intake.ClaimedJob, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.input, e.job, e.got
}

// envelope es un sobre tal como le llegó al descifrador.
type envelope struct {
	enc, dek []byte
	kek      string
}

// fakeDecrypter implementa pipeline.Decrypter: devuelve `text` o `err`, y apunta qué sobre
// le pidieron abrir.
type fakeDecrypter struct {
	text string
	err  error

	mu     sync.Mutex
	opened []envelope
}

func (d *fakeDecrypter) Decrypt(enc, dek []byte, keyID string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened = append(d.opened, envelope{enc: enc, dek: dek, kek: keyID})
	return d.text, d.err
}

func (d *fakeDecrypter) envelopes() []envelope {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]envelope(nil), d.opened...)
}

// rig es el worker cableado contra los dobles, con todo lo que un test interroga después.
type rig struct {
	mem   *intakehelpertest.MachineMemory
	store *faultyStore
	log   *recordingLog
	clock *fakeClock
	trace *callTrace
	p2    *fakeP2
	p3    *fakeP3
	p4    *fakeP4
	match *fakeMatch
	draft *fakeDraft
	// catalogs es el doble exportado del puerto; zones, el MemoryStore REAL del dominio de
	// solicitudes (el gemelo declarado de su Postgres), no un tercer doble.
	catalogs  *pipelinehelpertest.CatalogMemory
	zones     *intakes.MemoryStore
	decrypter *fakeDecrypter
	ticker    *fakeTicker
	w         *pipeline.Worker
}

// newParts arma las piezas SIN construir el worker.
func newParts(t *testing.T) *rig {
	t.Helper()
	clock := newFakeClock()
	mem := intakehelpertest.NewMachineMemory(clock.Now)
	trace := &callTrace{}
	base := func(name string) stageBase {
		return stageBase{name: name, store: mem, clock: clock, trace: trace}
	}
	catalogs, err := pipelinehelpertest.NewCatalogMemory("Torta de chocolate")
	if err != nil {
		t.Fatalf("construir el catálogo de prueba: %v", err)
	}
	return &rig{
		mem: mem, store: newFaultyStore(mem), log: &recordingLog{}, clock: clock, trace: trace,
		p2:        &fakeP2{stageBase: base(intake.StageP2), wants: []llm.Want{{Idea: "torta", Evidence: "torta"}}},
		p3:        &fakeP3{stageBase: base(intake.StageP3), items: []llm.ItemSpec{{Product: "torta", Evidence: "torta"}}},
		p4:        &fakeP4{stageBase: base(intake.StageP4)},
		match:     &fakeMatch{stageBase: base(intake.StageMatch)},
		draft:     &fakeDraft{stageBase: base(intake.StageDraft), intakeID: draftIntakeID},
		catalogs:  catalogs,
		zones:     intakes.NewMemoryStore(),
		decrypter: &fakeDecrypter{text: clearLiteral},
		ticker:    newFakeTicker(),
	}
}

// build construye el worker con EXACTAMENTE esas opciones.
func (r *rig) build(t *testing.T, cfg pipeline.Config, opts ...pipeline.Option) *pipeline.Worker {
	t.Helper()
	w, err := pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, r.draft,
		r.catalogs, r.decrypter, cfg, opts...)
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	r.w = w
	return w
}

// newRig arma el worker con lo que casi todos los tests quieren: lector de zonas, reloj
// falso y ticker falso; `opts` van detrás. `cfg` se completa con los valores de
// producción: un test que no diga nada prueba la política REAL.
func newRig(t *testing.T, cfg pipeline.Config, opts ...pipeline.Option) *rig {
	t.Helper()
	r := newParts(t)
	all := make([]pipeline.Option, 0, 3+len(opts))
	all = append(all,
		pipeline.WithShippingZones(r.zones),
		pipeline.WithClock(r.clock.Now),
		pipeline.WithTicker(r.ticker.start),
	)
	r.build(t, cfg, append(all, opts...)...)
	return r
}

// drain es Worker.Drain. Los tests drenan por aquí y no llamando al worker en línea.
func (r *rig) drain(ctx context.Context) int { return r.w.Drain(ctx) }

// drainAwake es Worker.DrainAwake con el flanco de siempre.
func (r *rig) drainAwake(ctx context.Context) int { return r.w.DrainAwake(ctx, edgeOne) }

// healthyRow es un job `pending` con sobre completo, listo para correr.
func (r *rig) healthyRow(id string) intakehelpertest.Row {
	return intakehelpertest.Row{
		ID:         id,
		Key:        intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: eventID},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:  r.clock.Now(),
		CreatedAt:  r.clock.Now(),
	}
}

// seed siembra un job sano y devuelve su id.
func (r *rig) seed(id string) string { return r.mem.Seed(r.healthyRow(id)) }

// row devuelve la fila o falla.
func (r *rig) row(t *testing.T, id string) intakehelpertest.Row {
	t.Helper()
	row, ok := r.mem.View(id)
	if !ok {
		t.Fatalf("la fila %s no existe", id)
	}
	return row
}

// start lanza Run y devuelve la función que lo cancela y ESPERA a que vuelva.
func (r *rig) start(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.w.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(waitLimit):
				t.Errorf("Run no volvió en %v tras cancelar el contexto", waitLimit)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// drainUntilTerminal drena hasta que el job sea terminal o se agoten las vueltas,
// avanzando el reloj entre una y otra para que el backoff venza. Devuelve la fila final.
func (r *rig) drainUntilTerminal(t *testing.T, id string, rounds int) intakehelpertest.Row {
	t.Helper()
	for range rounds {
		r.drain(context.Background())
		if row := r.row(t, id); intake.IsTerminal(row.Status) {
			return row
		}
		r.clock.Advance(2 * pipeline.DefaultBackoffCap)
	}
	return r.row(t, id)
}
