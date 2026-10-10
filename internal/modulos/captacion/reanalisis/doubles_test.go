package reanalisis_test

// doubles_test.go — LOS DOBLES de los tests del re-análisis (sin fichero de producción
// gemelo): los seis puertos, la bitácora del orden y el banco que los junta.
//
// 🔴 LOS DOBLES CUENTAN SUS ESCRITURAS, y no es decoración: la mitad de las promesas de
// Reanalyze son sobre lo que NO se escribe (un rechazo no puede dejar un job huérfano,
// un texto repetido no puede dejar dos filas). Sin contadores, esas promesas se
// «probarían» mirando el error que devuelve la función, que es lo que ya sabíamos.
//
// El puerto Jobs NO tiene doble propio: lo sirve intakehelpertest.MachineMemory, el
// gemelo en memoria de la tabla `intake_jobs` (hallazgo 2 de F7), envuelto en un espía
// que anota el paso y deja inyectar un fallo. Así el job que abre el servicio es una
// fila de verdad —con la validación de intake.ReanalysisRequest— y «hay un job vivo»
// es una fila `aggregating`, no un booleano.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	tenantID  = "t-1"
	intakeID  = "3f2a9c4e-6b1d-4f8a-9c2e-0d5b7a1f3e64"
	eventID   = "9c1f7b3a-2e64-4d85-bf10-7a3c5e9d2b48"
	sessionID = "sess-1"
	contactID = "c-opaco-1"

	// threadLimit es el límite del hilo con el que se construye el servicio. NO es 200
	// (el del compositor de producción) a propósito: un servicio que llevara ese número
	// escrito dentro pasaría un test que usara el mismo.
	threadLimit = 137

	// customerText es la frase del hilo. Es literal porque hay tests que afirman que
	// NO aparece en el log, y para eso hay que poder buscarla.
	customerText = "quiero 1 hamburguesa con queso y cebolla"
)

// errInfra es el fallo de infraestructura que se inyecta en los puertos.
var errInfra = errors.New("la base se cayó")

// fixedNow es el reloj del gemelo de la cola.
var fixedNow = time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

// Los ESCALONES observables del orden del §8.1. Cada uno es un puerto distinto, así
// que la bitácora no los infiere: los ANOTA el doble cuando de verdad le preguntan.
//
// ⚠️ LA FORMA (escalones 1-2) y LA CREDENCIAL (6) NO ESTÁN, y no faltan: no tocan
// ningún puerto, así que no pueden dejar huella aquí. Su posición la fijan tests de
// conducta en reanalisis_checks_test.go.
const (
	stepLevelGate    = "gate:llm_intake"
	stepVia          = "via:tenant_llm"
	stepViaGate      = "gate:api_llm"
	stepIntake       = "intake"
	stepLiveJob      = "live-job"
	stepSource       = "source:thread"
	stepDedupe       = "dedupe:pasted"
	stepWriteThread  = "write:thread"
	stepOpenJob      = "open:job"
	stepComposeEnvel = "compose:envelope"
)

// journal registra EN ORDEN a qué puerto se le preguntó. Es lo que convierte «el orden
// es contrato» de un comentario en una aserción: mover la solicitud y la fuente DELANTE
// de los gates deja intacto el error de casi todos los caminos, y solo la lista entera
// lo ve. El servicio no usa goroutines, así que no lleva cerrojo.
type journal struct{ steps []string }

func (j *journal) note(step string) { j.steps = append(j.steps, step) }

type fakeIntakes struct {
	target intakes.ReanalysisTarget
	err    error
	log    *journal
}

func (f *fakeIntakes) ReanalysisTargetOf(_ context.Context, _, _ string) (intakes.ReanalysisTarget, error) {
	f.log.note(stepIntake)
	if f.err != nil {
		return intakes.ReanalysisTarget{}, f.err
	}
	return f.target, nil
}

// fakeThread imita al almacén del hilo: las entradas del evento, las transcripciones
// ya pegadas y el escritor de una nueva.
type fakeThread struct {
	entries   []events.ThreadEntry
	pasted    []string
	listErr   error
	pastedErr error
	appendErr error

	// written son los cuerpos que llegaron a AppendPastedMessage, EN ORDEN.
	written []string
	// limits y askedEvents son el `limit` y el evento de cada ListThread.
	limits      []int
	askedEvents []string
	log         *journal
}

func (f *fakeThread) ListThread(_ context.Context, event string, limit int) ([]events.ThreadEntry, error) {
	f.log.note(stepSource)
	f.limits = append(f.limits, limit)
	f.askedEvents = append(f.askedEvents, event)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.entries, nil
}

func (f *fakeThread) ListPastedByOwner(_ context.Context, _ string) ([]string, error) {
	f.log.note(stepDedupe)
	if f.pastedErr != nil {
		return nil, f.pastedErr
	}
	return f.pasted, nil
}

func (f *fakeThread) AppendPastedMessage(_ context.Context, _, body string) (int, error) {
	f.log.note(stepWriteThread)
	if f.appendErr != nil {
		return 0, f.appendErr
	}
	f.written = append(f.written, body)
	f.pasted = append(f.pasted, body)
	return len(f.pasted), nil
}

// jobsSpy es el puerto Jobs sobre el gemelo en memoria de la cola.
type jobsSpy struct {
	*intakehelpertest.MachineMemory
	liveErr error
	openErr error
	// opened son los ids de los jobs que se abrieron de verdad, EN ORDEN.
	opened []string
	log    *journal
}

func (j *jobsSpy) LiveJobOfEvent(ctx context.Context, tenant, event string) (string, bool, error) {
	j.log.note(stepLiveJob)
	if j.liveErr != nil {
		return "", false, j.liveErr
	}
	return j.MachineMemory.LiveJobOfEvent(ctx, tenant, event)
}

func (j *jobsSpy) OpenReanalysis(ctx context.Context, req intake.ReanalysisRequest) (string, error) {
	j.log.note(stepOpenJob)
	if j.openErr != nil {
		return "", j.openErr
	}
	id, err := j.MachineMemory.OpenReanalysis(ctx, req)
	if err == nil {
		j.opened = append(j.opened, id)
	}
	return id, err
}

type fakeComposer struct {
	err  error
	keys []intake.WindowKey
	log  *journal
}

func (f *fakeComposer) ComposeAtFlush(_ context.Context, k intake.WindowKey) error {
	f.log.note(stepComposeEnvel)
	f.keys = append(f.keys, k)
	return f.err
}

// fakeFeatures responde por clave y registra TODAS las preguntas.
type fakeFeatures struct {
	has map[string]bool
	// errFor hace fallar al resolver solo para esa clave.
	errFor map[string]error
	asked  []string
	log    *journal
}

func (f *fakeFeatures) Has(_ context.Context, _, feature string) (bool, error) {
	// El paso se nombra por la CLAVE y no por el orden en que llegó: así la bitácora
	// distingue el gate del nivel del gate de la vía.
	if feature == entitlements.FeatureAPILLM {
		f.log.note(stepViaGate)
	} else {
		f.log.note(stepLevelGate)
	}
	f.asked = append(f.asked, feature)
	// Con fallo se devuelve IGUAL lo que el tenant tiene: fail-closed es negar por el
	// error, no porque el booleano venga a false.
	return f.has[feature], f.errFor[feature]
}

func (f *fakeFeatures) askedFor(key string) bool {
	for _, a := range f.asked {
		if a == key {
			return true
		}
	}
	return false
}

type fakeConfig struct {
	cfg   tenantllm.Config
	found bool
	err   error
	log   *journal
}

func (f *fakeConfig) Get(_ context.Context, _ string) (tenantllm.Config, bool, error) {
	f.log.note(stepVia)
	if f.err != nil {
		return tenantllm.Config{}, false, f.err
	}
	return f.cfg, f.found, nil
}

// bench junta los seis dobles, el servicio ya construido, el log capturado (a nivel
// Debug) y la bitácora del orden.
type bench struct {
	svc      *reanalisis.Service
	intakes  *fakeIntakes
	thread   *fakeThread
	jobs     *jobsSpy
	composer *fakeComposer
	features *fakeFeatures
	config   *fakeConfig
	out      *bytes.Buffer
	log      *journal
}

// newBench monta el escenario FELIZ por defecto: tenant con `llm_intake`, sin fila en
// `tenant_llm` (vía efectiva `local`, D-044.48 §4), una solicitud con su evento y su
// revisión 1, y un hilo con una frase del cliente. Cada test tuerce lo que necesita.
func newBench(t *testing.T, tweaks ...func(*bench)) *bench {
	t.Helper()
	log := &journal{}
	b := &bench{
		intakes: &fakeIntakes{log: log, target: intakes.ReanalysisTarget{
			SessionID: sessionID, ContactID: contactID, EventID: eventID,
			Status: "pending_approval", LastRevisionNo: 1,
		}},
		thread: &fakeThread{log: log, entries: []events.ThreadEntry{
			{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: customerText},
		}},
		jobs: &jobsSpy{
			log:           log,
			MachineMemory: intakehelpertest.NewMachineMemory(func() time.Time { return fixedNow }),
		},
		composer: &fakeComposer{log: log},
		features: &fakeFeatures{log: log, has: map[string]bool{entitlements.FeatureLLMIntake: true}},
		config:   &fakeConfig{log: log},
		out:      &bytes.Buffer{},
		log:      log,
	}
	for _, tweak := range tweaks {
		tweak(b)
	}
	svc, err := reanalisis.NewService(captureLog(b.out),
		b.intakes, b.thread, b.jobs, b.composer, b.features, b.config, threadLimit)
	if err != nil {
		t.Fatalf("NewService con todas las piezas falló: %v", err)
	}
	b.svc = svc
	return b
}

// captureLog devuelve un logger que escribe en el búfer TODOS los niveles.
func captureLog(buf *bytes.Buffer) logger.Logger {
	return logger.New(logger.WithWriter(buf), logger.WithLevel(slog.LevelDebug))
}

// ask ejecuta el caso de uso con el cuerpo dado, sobre el tenant y la solicitud de
// prueba si el cuerpo no trae otros.
func (b *bench) ask(req reanalisis.Request) (reanalisis.Result, error) {
	if req.TenantID == "" {
		req.TenantID = tenantID
	}
	if req.IntakeID == "" {
		req.IntakeID = intakeID
	}
	return b.svc.Reanalyze(context.Background(), req)
}

// mustAsk es ask para los caminos que tienen que salir bien.
func (b *bench) mustAsk(t *testing.T, req reanalisis.Request) reanalisis.Result {
	t.Helper()
	out, err := b.ask(req)
	if err != nil {
		t.Fatalf("Reanalyze(%+v) falló: %v", req, err)
	}
	return out
}

// requireNoWrites es la afirmación que comparten TODOS los rechazos: una petición
// rechazada no deja fila en ninguna de las tres tablas que esta puerta toca.
func (b *bench) requireNoWrites(t *testing.T) {
	t.Helper()
	if len(b.jobs.opened) != 0 {
		t.Errorf("un rechazo abrió un job huérfano: %v", b.jobs.opened)
	}
	if len(b.thread.written) != 0 {
		t.Errorf("un rechazo escribió en el hilo del evento: %d filas", len(b.thread.written))
	}
	if len(b.composer.keys) != 0 {
		t.Errorf("un rechazo compuso un sobre: %v", b.composer.keys)
	}
	for _, step := range b.log.steps {
		if step == stepWriteThread || step == stepOpenJob || step == stepComposeEnvel {
			t.Errorf("un rechazo llegó a un puerto de escritura (%s); pasos: %v", step, b.log.steps)
		}
	}
}

// requireSteps compara la bitácora ENTERA y en orden.
func (b *bench) requireSteps(t *testing.T, want ...string) {
	t.Helper()
	got := b.log.steps
	if len(got) != len(want) {
		t.Fatalf("pasos = %v; se esperaba %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pasos = %v; se esperaba %v", got, want)
		}
	}
}

// isUnwrapped dice si err es want «tal cual»: el mismo error y sin un prefijo delante.
func isUnwrapped(err, want error) bool {
	return errors.Is(err, want) && err.Error() == want.Error()
}

// finishJob termina el job abierto, como haría el worker: lo reclama y lo cierra. Sin
// esto, el segundo re-análisis del mismo evento encontraría vivo al primero.
func (b *bench) finishJob(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	job, ok, err := b.jobs.ClaimNext(ctx)
	if err != nil || !ok || job.ID != id {
		t.Fatalf("no se pudo reclamar el job %s: job=%q ok=%t err=%v", id, job.ID, ok, err)
	}
	if done, err := b.jobs.Finish(ctx, id, intakeID); err != nil || !done {
		t.Fatalf("no se pudo terminar el job %s: ok=%t err=%v", id, done, err)
	}
}

// seedLiveJob deja un job NO terminal sobre el evento de prueba y devuelve su id.
func (b *bench) seedLiveJob(status string) string {
	return b.jobs.Seed(intakehelpertest.Row{
		Key:    intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: eventID},
		Status: status,
	})
}

// withAPIConfig deja al tenant con fila `via=api` COMPLETA: credencial y
// consentimiento, que es lo único que la 0073 permite guardar con esa vía.
func withAPIConfig(b *bench) {
	b.config.found = true
	b.config.cfg = tenantllm.Config{
		TenantID: tenantID, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic,
		Model: "claude-x", HasAPIKey: true,
		ConsentedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
}

// withLocalConfig deja al tenant con fila `via=local`: sin credencial ni consentimiento.
func withLocalConfig(b *bench) {
	b.config.found = true
	b.config.cfg = tenantllm.Config{TenantID: tenantID, Via: tenantllm.ViaLocal}
}

// withAPILLM le da al tenant el add-on de la vía API.
func withAPILLM(b *bench) { b.features.has[entitlements.FeatureAPILLM] = true }

// withoutFeatures le quita al tenant todas las capacidades.
func withoutFeatures(b *bench) { b.features.has = map[string]bool{} }
