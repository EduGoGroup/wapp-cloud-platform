package runtime_test

// Plan 054 · T3 (D-054.4 + MD-054.1): la excepción ACOTADA al fan-out
// best-effort del ADR-0003 para el sink que MATERIALIZA contenido durable
// (cart/survey, el MISMO predicado de F1/T2.1). Cuatro escenas OPUESTAS —
// (a) fallo persistente que agota el reintento, (b) fallo transitorio que
// CEDE al reintento, (c) error PERMANENTE que NO gasta reintentos, y
// (d) regresión: un sink NO durable (PhaseNotify, como el webhook) sigue
// best-effort aunque el efecto sea de un módulo durable— porque ninguna de
// las cuatro prueba la excepción sin las otras tres.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// despedidaCartText es la salida que durableCartClosedModule declara al
// terminar su flujo (Result.Outputs, junto con Next=&model.NodeTerminal y
// Outcome=model.OutcomeCompleted) — el texto de "despedida normal" que las
// cuatro escenas usan para distinguir un turno que completó de uno cortado.
const despedidaCartText = "¡Gracias por tu pedido! Lo estamos preparando."

// durableCartClosedModule simula un módulo tipo "cart" (un solo nodo, SIEMPRE
// espera input, ProducesDurableContent()==true) que declara su PROPIO fin —
// Result.Next apunta al centinela, con Outcome — EXACTAMENTE como hace el
// módulo cart real (engine.go, hallazgo #24 del Plan 043 · Ola 6): así
// Conversation.Finished()/Outcome() se comportan byte a byte igual que un
// cart real llegando a cart_closed, sin montar su sub-máquina ni el catálogo
// (ese peso ya lo paga T2/T4; T3 ejercita el fan-out, no la navegación).
// Registrado bajo Type()==model.NodeTypeMenu para reusar sampleFlow() (el
// mismo truco que dispatch_test.go/durable_guard_test.go, con
// ProducesDurableContent() invertido a true).
type durableCartClosedModule struct{ effects []modules.Effect }

func (durableCartClosedModule) Type() string                 { return model.NodeTypeMenu }
func (durableCartClosedModule) WaitsForInput() bool          { return true }
func (durableCartClosedModule) ProducesDurableContent() bool { return true }

func (durableCartClosedModule) Render(node model.Node, _ model.Content) []string {
	return []string{node.Prompt}
}

func (m durableCartClosedModule) Step(_ model.Node, conv model.Conversation, _ string) modules.Result {
	next := model.NodeTerminal
	return modules.Result{
		Next:    &next,
		Outcome: model.OutcomeCompleted,
		Outputs: []string{despedidaCartText},
		Vars:    conv.Vars,
		Effects: m.effects,
	}
}

func newDurableCartEngine(effects []modules.Effect) *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(durableCartClosedModule{effects: effects})
	return engine.New(reg)
}

// flakyCloseIntakeStore envuelve *store.MemoryRepository y hace que CloseIntake
// falle las primeras failTimes llamadas con err antes de delegar en la
// implementación real — mismo patrón que intakeChocaConEventoYaDeclarado
// (internal/flujos/modules/cart/projection_test.go:300: un fake que devuelve
// *pgconn.PgError en cada llamada, envuelto con el MISMO %w que
// store.PostgresRepository usa en producción, para que postgres.
// IsPermanentFailure lo reconozca atravesando el mismo envoltorio), aquí
// parametrizado para poder simular tanto un fallo que AGOTA el reintento
// (failTimes grande) como uno que CEDE (failTimes pequeño).
type flakyCloseIntakeStore struct {
	*store.MemoryRepository
	mu        sync.Mutex
	calls     int
	failTimes int
	err       error
}

func (f *flakyCloseIntakeStore) CloseIntake(ctx context.Context, in store.IntakeClose) (string, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n <= f.failTimes {
		return "", f.err
	}
	return f.MemoryRepository.CloseIntake(ctx, in)
}

func (f *flakyCloseIntakeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// alwaysFailNotifySink es un EventSink de runtime.PhaseNotify (best-effort,
// como el WebhookSink real) que falla SIEMPRE: prueba la escena (d) — el
// ADR-0003 sigue intacto para un sink que NO materializa, aunque el efecto sí
// lo declare un módulo durable (cart). La distinción de D-054.4 es por SINK,
// no por efecto.
type alwaysFailNotifySink struct {
	mu    sync.Mutex
	calls int
}

func (s *alwaysFailNotifySink) Handle(context.Context, runtime.EffectContext, modules.Effect) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return errors.New("webhook: boom")
}

func (s *alwaysFailNotifySink) Phase() runtime.SinkPhase { return runtime.PhaseNotify }

func (s *alwaysFailNotifySink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newDurableCartRuntime arma un runtime con sampleFlow() (el nodo "root",
// menú en T2, aquí registrado como durable) y durableCartClosedModule, con el
// PersistSink REAL y el cart.Projector REAL apuntando a projStore —para que
// la materialización en intakes sea la de producción, no una simulación—.
// Siembra el estado YA ABIERTO en "root" (bypass de Start(): un flujo durable
// NUNCA arranca por Start(), D-054.5 lo rechaza siempre con eventID=="" — el
// mismo motivo por el que T2 escribió seedSurveyOpen). extraSinks se añaden
// DESPUÉS del PersistSink (orden de wiring; el orden REAL en el fan-out lo
// decide sortSinksByPhase, no este orden de registro).
func newDurableCartRuntime(t *testing.T, projStore cart.ProjectionStore, extraSinks ...runtime.EventSink) (
	rt *runtime.Runtime, repo *store.MemoryRepository, sender *fakeSender, contactID string,
) {
	t.Helper()
	ctx := context.Background()
	repo = store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender = &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	sink := runtime.NewPersistSink(repo, cart.NewProjector(projStore, intakes.NewMemoryStore(), sinEnvío{}, intakes.NewMemoryStore()))
	opts := make([]runtime.Option, 0, 1+len(extraSinks))
	opts = append(opts, runtime.WithEventSink(sink))
	for _, es := range extraSinks {
		opts = append(opts, runtime.WithEventSink(es))
	}
	rt = runtime.New(repo, newDurableCartEngine([]modules.Effect{cartClosedEmit()}), sender,
		fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	contactID = resolveID(t, contacts, testContact)
	if err := repo.Save(ctx, model.Conversation{
		TenantID: testTenant, SessionID: testSession, ContactID: contactID,
		FlowID: testFlow, FlowVersion: 1, CurrentNode: "root",
	}); err != nil {
		t.Fatalf("sembrar flow_state en root: %v", err)
	}
	return rt, repo, sender, contactID
}

// TestDurableSink_FalloPersistente_CortaElTurno es la escena (a): el sink que
// materializa (CloseIntake, vía cart.Projector) falla en TODOS los intentos
// con un código TRANSITORIO por clasificación (08006, connection_failure) —
// agota el cupo de 2 reintentos (3 intentos en total) y el turno se corta.
//
// PRUEBA POR MUTACIÓN: si retryDurableSink dejara de reintentar (p. ej.
// alguien "simplifica" dispatch() a un solo intento), flaky.callCount() bajaría
// de 3 a 1 y este test lo detecta; si el corte dejara de anteponerse al Save
// (MD-054.1 opción (a) revertida sin querer), st.CurrentNode ya no sería
// "root" y flaky.Intakes() ya no estaría vacío.
func TestDurableSink_FalloPersistente_CortaElTurno(t *testing.T) {
	flaky := &flakyCloseIntakeStore{
		MemoryRepository: store.NewMemoryRepository(),
		failTimes:        1000,
		err:              &pgconn.PgError{Code: "08006"},
	}
	rt, repo, sender, cid := newDurableCartRuntime(t, flaky)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "confirmar", "wamid.a")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := flaky.callCount(); got != 3 {
		t.Fatalf("CloseIntake debería intentarse 3 veces (1 inicial + 2 reintentos), se intentó %d", got)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("el cliente debe recibir EXACTAMENTE un saliente (el aviso), recibió %d: %v", got, sender.texts())
	}
	got := sender.texts()[0]
	if strings.Contains(got, despedidaCartText) {
		t.Fatalf("el saliente NO debe contener la despedida normal, la contiene: %q", got)
	}
	if !strings.Contains(got, "No pudimos registrar tu pedido") {
		t.Fatalf("el saliente debe llevar el aviso explícito de D-054.4, es: %q", got)
	}
	st := loadState(t, repo, cid)
	if st.CurrentNode != "root" {
		t.Fatalf("el turno NO debe avanzar (nada que revertir, MD-054.1 opción (a)): CurrentNode = %q, quiero %q", st.CurrentNode, "root")
	}
	if st.Outcome() == model.OutcomeCompleted {
		t.Fatal("flow_outcome no debe quedar completed")
	}
	if got := len(flaky.Intakes()); got != 0 {
		t.Fatalf("no debe haber quedado ninguna solicitud creada, hay %d", got)
	}
}

// TestDurableSink_FalloTransitorioQueCede_CompletaConNormalidad es la escena
// (b), la que le da sentido a (a): el mismo fake falla UNA vez (código
// transitorio) y CEDE en el reintento (dentro del cupo de 2) — el turno debe
// completar exactamente como si nunca hubiera fallado, SIN ningún aviso.
// Sin esta escena en verde, la excepción de D-054.4 se convierte en turnos
// cortados a clientes sanos por un hipo de red — justo lo que la decisión de
// Jhoan (R3) quería evitar.
func TestDurableSink_FalloTransitorioQueCede_CompletaConNormalidad(t *testing.T) {
	flaky := &flakyCloseIntakeStore{
		MemoryRepository: store.NewMemoryRepository(),
		failTimes:        1,
		err:              &pgconn.PgError{Code: "08006"},
	}
	rt, repo, sender, cid := newDurableCartRuntime(t, flaky)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "confirmar", "wamid.b")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := flaky.callCount(); got != 2 {
		t.Fatalf("CloseIntake debería intentarse 2 veces (1 fallo + 1 reintento que cede), se intentó %d", got)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("se esperaba 1 saliente (la despedida), hubo %d: %v", got, sender.texts())
	}
	got := sender.texts()[0]
	if !strings.Contains(got, despedidaCartText) {
		t.Fatalf("el turno debe completar con la despedida normal, salió: %q", got)
	}
	if strings.Contains(got, "No pudimos registrar tu pedido") {
		t.Fatalf("NO debe llevar ningún aviso de fallo: %q", got)
	}
	st := loadState(t, repo, cid)
	if st.CurrentNode != model.NodeTerminal {
		t.Fatalf("el flujo debe llegar al centinela, quedó en %q", st.CurrentNode)
	}
	if st.Outcome() != model.OutcomeCompleted {
		t.Fatalf("flow_outcome debe quedar completed, es %q", st.Outcome())
	}
	items := flaky.Intakes()
	if len(items) != 1 || items[0].Status != "closed" {
		t.Fatalf("la solicitud debe quedar cerrada: %+v", items)
	}
}

// TestDurableSink_ErrorPermanente_CortaSinGastarReintentos es la escena (c):
// 23502 (not_null_violation) es el SQLSTATE literal de los hallazgos #001/#003
// (migraciones 0054/0055). postgres.IsPermanentFailure debe reconocerlo y
// dispatch() NO debe gastar ni un reintento en él.
//
// PRUEBA POR MUTACIÓN: si postgres.IsPermanentFailure dejara de clasificar la
// clase 23 como permanente (p. ej. devolviera false siempre), este error
// pasaría a tratarse como transitorio y flaky.callCount() subiría de 1 a 3 —
// exactamente la misma cifra que la escena (a) — así que el test lo detecta.
func TestDurableSink_ErrorPermanente_CortaSinGastarReintentos(t *testing.T) {
	flaky := &flakyCloseIntakeStore{
		MemoryRepository: store.NewMemoryRepository(),
		failTimes:        1000,
		err:              &pgconn.PgError{Code: "23502"},
	}
	rt, repo, sender, cid := newDurableCartRuntime(t, flaky)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "confirmar", "wamid.c")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := flaky.callCount(); got != 1 {
		t.Fatalf("un error PERMANENTE (23502) no debe gastar reintentos: CloseIntake se intentó %d veces, quiero 1", got)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("se esperaba 1 saliente (el aviso), hubo %d: %v", got, sender.texts())
	}
	if got := sender.texts()[0]; strings.Contains(got, despedidaCartText) {
		t.Fatalf("el saliente NO debe contener la despedida normal, la contiene: %q", got)
	}
	st := loadState(t, repo, cid)
	if st.CurrentNode != "root" {
		t.Fatalf("el turno no debe avanzar: CurrentNode = %q, quiero %q", st.CurrentNode, "root")
	}
	if got := len(flaky.Intakes()); got != 0 {
		t.Fatalf("no debe quedar ninguna solicitud creada, hay %d", got)
	}
}

// TestDurableSink_SinkNoDurableSigueBestEffort_NoCorta es la escena (d), la
// regresión del ADR-0003: el sink que MATERIALIZA (PersistSink + cart real)
// funciona bien; el que falla SIEMPRE es un sink de PhaseNotify (como el
// WebhookSink real) — D-054.4 no lo toca. Distinción por SINK, no por efecto
// (el efecto SÍ es de un módulo durable, cart): sin esta escena, nada impide
// que una futura "simplificación" gatee el reintento solo por ec.Durable y
// empiece a cortar turnos por un webhook caído.
func TestDurableSink_SinkNoDurableSigueBestEffort_NoCorta(t *testing.T) {
	healthyStore := store.NewMemoryRepository()
	notify := &alwaysFailNotifySink{}
	rt, repo, sender, cid := newDurableCartRuntime(t, healthyStore, notify)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "confirmar", "wamid.d")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := notify.count(); got != 1 {
		t.Fatalf("un sink NO durable no debe reintentarse: llamadas=%d, quiero 1", got)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("se esperaba 1 saliente (la despedida), hubo %d: %v", got, sender.texts())
	}
	if got := sender.texts()[0]; !strings.Contains(got, despedidaCartText) {
		t.Fatalf("el turno debe completar con normalidad pese al fallo del sink de notificación: %q", got)
	}
	st := loadState(t, repo, cid)
	if st.CurrentNode != model.NodeTerminal || st.Outcome() != model.OutcomeCompleted {
		t.Fatalf("el flujo debe terminar completed pese al webhook caído: node=%q outcome=%q", st.CurrentNode, st.Outcome())
	}
	if got := len(healthyStore.Intakes()); got != 1 {
		t.Fatalf("el sink que SÍ materializa debe haber cerrado la solicitud igualmente, hay %d", got)
	}
}
