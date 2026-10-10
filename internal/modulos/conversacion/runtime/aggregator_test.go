package runtime

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Este fichero trae el montaje común de los tests del agregador. Los tests van partidos por tema
// (E-13): aggregator_observe_test.go (Observe: AG-1, AG-2, AG-5), aggregator_sweep_test.go (el barrido y sus plazos:
// AG-3, AG-6, AG-8), aggregator_hint_test.go (el adelanto por intent: AG-3, AG-4, AG-8) y
// aggregator_run_test.go (Run: el tick, el despertador y la parada, D-F9-10).
//
// AG-7 (los tres llamantes del puente con el motor) solo se ve a través del Runtime: es de la ola
// de `incoming`.

// aggregatorClock es el reloj movible del test: el del agregador y el del almacén (que hace de
// reloj de Postgres) son la MISMA función. Lleva candado porque Run lo lee desde su goroutine.
type aggregatorClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *aggregatorClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *aggregatorClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// aggregatorAsk es una pregunta al resolver de derechos.
type aggregatorAsk struct{ tenantID, feature string }

// aggregatorEntitlements es el Fake de entitlements con las preguntas contadas: el presupuesto de
// Observe dice cuántas puede hacer y cuándo.
type aggregatorEntitlements struct {
	*entitlementshelpertest.Fake
	mu    sync.Mutex
	asked []aggregatorAsk
}

func (e *aggregatorEntitlements) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	e.mu.Lock()
	e.asked = append(e.asked, aggregatorAsk{tenantID, feature})
	e.mu.Unlock()
	return e.Fake.Has(ctx, tenantID, feature)
}

func (e *aggregatorEntitlements) asks() []aggregatorAsk {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.asked)
}

// aggregatorSettings es el gemelo en memoria de conversacion/store con las lecturas de
// tenant_settings contadas por tenant y un fallo inyectable.
type aggregatorSettings struct {
	repo  *store.MemoryRepository
	mu    sync.Mutex
	err   error
	reads map[string]int
}

func (s *aggregatorSettings) GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error) {
	s.mu.Lock()
	s.reads[tenantID]++
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return store.TenantSettings{}, err
	}
	return s.repo.GetTenantSettings(ctx, tenantID)
}

func (s *aggregatorSettings) readsOf(tenantID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[tenantID]
}

func (s *aggregatorSettings) totalReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.reads {
		total += n
	}
	return total
}

func (s *aggregatorSettings) failWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// setDeadlines fija los dos plazos del tenant, partiendo de los defaults.
func (s *aggregatorSettings) setDeadlines(tenantID string, silence, ceiling time.Duration) {
	cfg := store.DefaultTenantSettings(tenantID)
	cfg.AggregationWindow = silence
	cfg.AggregationMax = ceiling
	s.repo.SetTenantSettings(cfg)
}

// aggregatorJobs es el gemelo en memoria de captacion/intake con lo que el gemelo no trae: fallos
// de listar y de cerrar, la carrera perdida (otro cerró antes) y llamadas que se quedan colgadas
// hasta que el contexto se cancela (para la parada de Run).
type aggregatorJobs struct {
	*intake.MemoryStore
	mu         sync.Mutex
	listErr    error
	closeErr   map[intake.WindowKey]error
	lostRace   bool
	blockList  bool
	blockClose bool
	ctxAware   bool
}

func (j *aggregatorJobs) ListAggregating(ctx context.Context, limit int) ([]intake.OpenJob, error) {
	j.mu.Lock()
	block, aware, err := j.blockList, j.ctxAware, j.listErr
	j.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if aware && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return j.MemoryStore.ListAggregating(ctx, limit)
}

func (j *aggregatorJobs) CloseWindow(ctx context.Context, k intake.WindowKey) (bool, error) {
	j.mu.Lock()
	block, lost, err := j.blockClose, j.lostRace, j.closeErr[k]
	j.mu.Unlock()
	if block {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if err != nil {
		return false, err
	}
	if lost {
		return false, nil
	}
	return j.MemoryStore.CloseWindow(ctx, k)
}

func (j *aggregatorJobs) set(change func(j *aggregatorJobs)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	change(j)
}

// aggregatorComposer apunta las ventanas que se le pide componer.
type aggregatorComposer struct {
	mu    sync.Mutex
	err   error
	block bool
	keys  []intake.WindowKey
}

func (c *aggregatorComposer) ComposeAtFlush(ctx context.Context, key intake.WindowKey) error {
	c.mu.Lock()
	c.keys = append(c.keys, key)
	block, err := c.block, c.err
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (c *aggregatorComposer) composed() []intake.WindowKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.keys)
}

// aggregatorRequest es una petición de clasificación tal como llegó.
type aggregatorRequest struct {
	key  intake.WindowKey
	text string
}

// aggregatorAhead apunta lo que se le pide clasificar.
type aggregatorAhead struct {
	mu       sync.Mutex
	requests []aggregatorRequest
}

func (a *aggregatorAhead) Request(key intake.WindowKey, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, aggregatorRequest{key, text})
}

func (a *aggregatorAhead) requested() []aggregatorRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.requests)
}

// Aserciones de compilación: los puertos que el agregador declara los cumplen los gemelos de los
// otros módulos y los dobles de aquí.
var (
	_ AggregationSettings   = (*store.MemoryRepository)(nil)
	_ AggregationSettings   = (*aggregatorSettings)(nil)
	_ SourceComposer        = (*aggregatorComposer)(nil)
	_ AheadRequester        = (*aggregatorAhead)(nil)
	_ intake.JobStore       = (*aggregatorJobs)(nil)
	_ entitlements.Resolver = (*aggregatorEntitlements)(nil)
)

// La forma de las seis opciones y de los métodos que se prueban en los otros ficheros del tema
// (sin llamarlos: una llamada aquí correría al cargar el paquete).
var (
	_ func(func() time.Time) AggregatorOption = WithAggregatorClock
	_ func(time.Duration) AggregatorOption    = WithSweepInterval
	_ func(int) AggregatorOption              = WithSweepBatch
	_ func(float64) AggregatorOption          = WithIntentConfidence
	_ func(SourceComposer) AggregatorOption   = WithSourceComposer
	_ func(AheadRequester) AggregatorOption   = WithAheadRequester

	_ func(*IntakeAggregator, context.Context, IncomingRef)      = (*IntakeAggregator).Observe
	_ func(*IntakeAggregator, intake.WindowKey, string, float64) = (*IntakeAggregator).OnClassified
	_ func(*IntakeAggregator, context.Context) int               = (*IntakeAggregator).RecoverAtBoot
	_ func(*IntakeAggregator, context.Context) int               = (*IntakeAggregator).Sweep
	_ func(*IntakeAggregator, context.Context)                   = (*IntakeAggregator).Run
)

// Los dos tenants del montaje: el primero tiene llm_intake; el segundo, no.
const (
	aggregatorTenant        = "tenant-1"
	aggregatorTenantWithout = "tenant-without"
	aggregatorClientText    = "quiero dos tortas zzq"
)

// aggregatorStart es el instante en que arranca el reloj movible.
var aggregatorStart = time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

// aggregatorRig son las dependencias de un agregador. Varios agregadores pueden compartirlo: es
// como se simula un reinicio (proceso nuevo, misma base).
type aggregatorRig struct {
	now      func() time.Time
	clock    *aggregatorClock
	log      *sinkLogRecorder
	jobs     *aggregatorJobs
	settings *aggregatorSettings
	ents     *aggregatorEntitlements
	composer *aggregatorComposer
	ahead    *aggregatorAhead
}

// newAggregatorRig monta el entorno con el reloj movible.
func newAggregatorRig() *aggregatorRig {
	clock := &aggregatorClock{t: aggregatorStart}
	rig := newAggregatorRigAt(clock.now)
	rig.clock = clock
	return rig
}

// newAggregatorRigAt monta el entorno sobre el reloj dado (en una burbuja de synctest, time.Now).
func newAggregatorRigAt(now func() time.Time) *aggregatorRig {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(aggregatorTenant, entitlements.FeatureLLMIntake)
	fake.Enable("tenant-2", entitlements.FeatureLLMIntake)
	return &aggregatorRig{
		now:      now,
		log:      newSinkLogRecorder(),
		jobs:     &aggregatorJobs{MemoryStore: intake.NewMemoryStore(now), closeErr: map[intake.WindowKey]error{}},
		settings: &aggregatorSettings{repo: store.NewMemoryRepository(), reads: map[string]int{}},
		ents:     &aggregatorEntitlements{Fake: fake},
		composer: &aggregatorComposer{},
		ahead:    &aggregatorAhead{},
	}
}

// aggregator construye un agregador sobre el entorno, SIEMPRE con el reloj inyectado. El compositor
// y el AheadRequester no se cablean aquí: los tests que los quieren los pasan como opción.
func (r *aggregatorRig) aggregator(opts ...AggregatorOption) *IntakeAggregator {
	all := append([]AggregatorOption{WithAggregatorClock(r.now)}, opts...)
	return NewIntakeAggregator(r.log, r.jobs, r.settings, r.ents, all...)
}

// status devuelve el estado de cada fila, en orden de creación.
func (r *aggregatorRig) status() []string {
	jobs := r.jobs.Jobs()
	out := make([]string, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, job.Status)
	}
	return out
}

// requireStatus falla si las filas de intake_jobs no tienen exactamente esos estados.
func (r *aggregatorRig) requireStatus(t *testing.T, when string, want ...string) {
	t.Helper()
	if got := r.status(); !slices.Equal(got, want) {
		t.Fatalf("%s: estados de intake_jobs = %v, quería %v", when, got, want)
	}
}

// requireNoSecret falla si el texto del cliente aparece en el log.
func (r *aggregatorRig) requireNoSecret(t *testing.T) {
	t.Helper()
	if dump := r.log.dump(); strings.Contains(dump, "zzq") {
		t.Errorf("el log lleva el texto del cliente:\n%s", dump)
	}
}

// requireLine devuelve la única línea de ese nivel con ese mensaje y comprueba sus claves.
func (r *aggregatorRig) requireLine(t *testing.T, level, msg string, want map[string]any) {
	t.Helper()
	var found []sinkLogLine
	for _, line := range r.log.at(level) {
		if line.msg == msg {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("hay %d líneas %s %q, quería 1; log:\n%s", len(found), level, msg, r.log.dump())
	}
	if len(found[0].fields) != len(want) {
		t.Errorf("%q: claves = %v, quería exactamente %v", msg, found[0].fields, want)
	}
	for k, v := range want {
		if !reflect.DeepEqual(found[0].fields[k], v) {
			t.Errorf("%q: clave %q = %v, quería %v", msg, k, found[0].fields[k], v)
		}
	}
}

// aggregatorKey es la tupla de una ventana del tenant con la feature.
func aggregatorKey(eventID string) intake.WindowKey {
	return intake.WindowKey{TenantID: aggregatorTenant, SessionID: "session-9", ContactID: "contact-opaque", EventID: eventID}
}

// aggregatorRef es un entrante de texto de esa ventana.
func aggregatorRef(key intake.WindowKey, waMessageID string, ts time.Time) IncomingRef {
	return IncomingRef{Key: key, WaMessageID: waMessageID, MessageTS: ts, Text: aggregatorClientText}
}

// TestNewIntakeAggregator_NeverNilAndTouchesNothing: construir no lee, no escribe y no pregunta.
func TestNewIntakeAggregator_NeverNilAndTouchesNothing(t *testing.T) {
	rig := newAggregatorRig()
	if rig.aggregator(WithSourceComposer(rig.composer), WithAheadRequester(rig.ahead)) == nil {
		t.Fatal("NewIntakeAggregator devolvió nil")
	}
	if NewIntakeAggregator(nil, nil, nil, nil) == nil {
		t.Fatal("NewIntakeAggregator sin dependencias devolvió nil")
	}
	if got := rig.jobs.Counters(); got != (intake.Counters{}) {
		t.Errorf("construir tocó intake_jobs: %+v", got)
	}
	if rig.settings.totalReads() != 0 || len(rig.ents.asks()) != 0 || len(rig.ahead.requested()) != 0 || len(rig.log.all()) != 0 {
		t.Error("construir leyó la config, preguntó por derechos, pidió una clasificación o logueó")
	}
}

// TestIntentHint_HasOnlyNameAndConfidence es D-044.20 hecho tipo: la política no puede leer los
// params del intent porque IntentHint no tiene dónde llevarlos. Y el nombre que dispara es literal.
func TestIntentHint_HasOnlyNameAndConfidence(t *testing.T) {
	hint := IntentHint{Name: IntentIntakeRequest, Confidence: 0.9}
	typ := reflect.TypeOf(hint)
	fields := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		fields = append(fields, typ.Field(i).Name+" "+typ.Field(i).Type.String())
	}
	if want := []string{"Name string", "Confidence float64"}; !slices.Equal(fields, want) {
		t.Errorf("campos de IntentHint = %v, quería exactamente %v (ni params ni nada más)", fields, want)
	}
	if IntentIntakeRequest != "intake_request" {
		t.Errorf("IntentIntakeRequest = %q, quería %q", IntentIntakeRequest, "intake_request")
	}
}
