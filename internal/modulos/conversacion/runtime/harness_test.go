package runtime_test

// harness_test.go es el ARNÉS de los tests de comportamiento del runtime: monta un *Runtime
// de verdad sobre los gemelos en memoria y los dobles del módulo, con UN reloj movible, y da
// los ayudantes para sembrar, hablar y leer. Lo usan los tests de runtime_engine, start,
// incoming, events, event_lifecycle, resume, exit_menu, welcome, thread y send.
//
// Va en el paquete EXTERNO (runtime_test) y no en runtime: los dobles de runtimehelpertest
// importan runtime, y un test interno que los importara cerraría un ciclo de imports. Es
// además lo que pide el método: estos tests salen del contrato y hablan con el motor por sus
// puertas exportadas (Start, HandleIncoming, OnIncoming, CancelEventForTenant…).
//
// Sin time.Sleep: el tiempo se mueve con harness.clock. Para esperar a la goroutine de
// OnIncoming se usa Sender.OnSend o un doble propio que cierre un canal.

import (
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/media"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// Los valores fijos de un guion: un tenant, una sesión y un cliente. Todos distintos entre
// sí, para que un cruce de claves se vea.
const (
	harnessTenant   = "11111111-1111-4111-8111-111111111111"
	harnessSession  = "session-zzq"
	harnessPhone    = "573001112233"
	harnessMediaURL = "https://objects.example.test/presigned-zzq"
)

// harnessStart es el instante en que arranca el reloj de todo guion.
var harnessStart = time.Date(2026, time.March, 2, 10, 0, 0, 0, time.UTC)

// harness es un guion montado. Los campos son las piezas de verdad: el test las siembra, las
// hace fallar y las lee directamente cuando el ayudante no alcanza.
type harness struct {
	t *testing.T
	// rt es el Runtime bajo prueba.
	rt *runtime.Runtime
	// clock es el reloj compartido (ver movableClock).
	clock *movableClock
	// repo es el gemelo en memoria del almacén de flujos: estado, definiciones, ajustes,
	// flow_events, solicitudes, resultados de encuesta y la marca de la bienvenida.
	repo *store.MemoryRepository
	// events es el doble del almacén de eventos (eventos, hilo, contenido).
	events *eventshelpertest.Store
	// rules es el almacén en memoria de reglas de disparo; el runtime las resuelve con el
	// trigger.ConfigResolver de verdad.
	rules *trigger.MemoryStore
	// contacts es el resolver de contactos en memoria (migra el estado sobre repo).
	contacts *contact.MemoryResolver
	// features es el resolver de features: todo apagado hasta enableFeature.
	features *entitlementshelpertest.Fake
	// registry y engine son los nuevos, con menu, survey y media más lo que pida withModules.
	registry *modules.Registry
	engine   *engine.Engine
	// Los dobles de los puertos del runtime.
	sender      *runtimehelpertest.Sender
	presigner   *runtimehelpertest.Presigner
	tenants     *runtimehelpertest.TenantResolver
	selfNumbers *runtimehelpertest.SelfNumbers
	deduper     *runtimehelpertest.IngestDeduper
	limiter     *runtimehelpertest.ReplyLimiter
	deposits    *runtimehelpertest.DepositReminder
	abandoner   *abandonRecorder
	// log captura todas las líneas que el runtime y sus sinks escriben.
	log *logRecorder
	// hooks apunta los motivos de corte y las rachas cerradas.
	hooks *hookLog

	profile     string
	extraMods   []modules.Module
	engineOpts  []engine.Option
	buildSinks  func(h *harness) []runtime.EventSink
	buildExtras []func(h *harness) []runtime.Option
}

// harnessOption ajusta el guion ANTES de construir el Runtime. Las que reciben una función
// la llaman con el arnés ya poblado de dobles, para que la opción pueda usarlos.
type harnessOption func(*harness)

// withProfile fija el perfil que el doble de TenantResolver contesta para la sesión
// (runtimehelpertest.ProfileActive por defecto).
func withProfile(profile string) harnessOption {
	return func(h *harness) { h.profile = profile }
}

// withModules registra módulos además de menu, survey y media (el carrito, o un doble que
// emita efectos).
func withModules(mods ...modules.Module) harnessOption {
	return func(h *harness) { h.extraMods = append(h.extraMods, mods...) }
}

// withEngineOptions pasa opciones al engine (la fuente de contenido, el resolutor de
// consultas).
func withEngineOptions(opts ...engine.Option) harnessOption {
	return func(h *harness) { h.engineOpts = append(h.engineOpts, opts...) }
}

// withSinks SUSTITUYE el sink por defecto del guion (un PersistSink sobre repo, sin
// proyectores). Si build devuelve una lista vacía no se cablea ninguno y el runtime se queda
// con su LogSink.
func withSinks(build func(h *harness) []runtime.EventSink) harnessOption {
	return func(h *harness) { h.buildSinks = build }
}

// withOptions añade opciones del runtime DESPUÉS de las del guion: sirve para cablear lo que
// el guion no trae (WithAggregator, WithResumePolicy, WithIncomingTimeout…) y para QUITAR una
// pieza pasándola nil (runtime.WithEventStore(nil)).
func withOptions(build func(h *harness) []runtime.Option) harnessOption {
	return func(h *harness) { h.buildExtras = append(h.buildExtras, build) }
}

// newHarness monta el guion completo y construye el Runtime. Cablea: reloj, reglas de
// disparo, plano de eventos, abandonador, despachador (también como OpeningBuilder),
// FlowForKind, fuentes del resumen, features, bienvenida, limitador (sin tope), números
// propios (ninguno), dedupe, recordatorio de seña (callado), prefirma, los dos hooks y un
// PersistSink. NO cablea el agregador ni ninguna política de reanudación.
func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := &harness{
		t:           t,
		clock:       &movableClock{now: harnessStart},
		repo:        store.NewMemoryRepository(),
		events:      eventshelpertest.NewStore(),
		rules:       trigger.NewMemoryStore(),
		features:    entitlementshelpertest.NewFake(),
		registry:    modules.NewRegistry(),
		sender:      runtimehelpertest.NewSender(),
		presigner:   runtimehelpertest.NewPresigner(harnessMediaURL, harnessStart.Add(time.Hour)),
		selfNumbers: runtimehelpertest.NewSelfNumbers(),
		deduper:     runtimehelpertest.NewIngestDeduper(),
		limiter:     runtimehelpertest.NewReplyLimiter(),
		deposits:    runtimehelpertest.NewDepositReminder(),
		abandoner:   &abandonRecorder{},
		log:         newLogRecorder(),
		hooks:       &hookLog{},
		profile:     runtimehelpertest.ProfileActive,
		buildSinks: func(h *harness) []runtime.EventSink {
			return []runtime.EventSink{runtime.NewPersistSink(h.repo)}
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	h.repo.SetClock(h.clock.Now)
	h.events.SetClock(h.clock.Now)
	h.contacts = contact.NewMemoryResolver(h.repo)
	h.tenants = runtimehelpertest.NewTenantResolver(harnessTenant, h.profile)

	h.registry.Register(menu.New())
	h.registry.Register(survey.New())
	h.registry.Register(media.New())
	for _, m := range h.extraMods {
		h.registry.Register(m)
	}
	h.engine = engine.New(h.registry, h.engineOpts...)

	h.rt = runtime.New(h.repo, h.engine, h.sender, h.tenants, h.contacts, h.log, h.runtimeOptions()...)
	return h
}

// runtimeOptions arma las opciones del guion, en orden: las fijas, los sinks y las extra.
func (h *harness) runtimeOptions() []runtime.Option {
	dispatcher := events.NewDispatcher(h.events, events.NewTriggerKindOffer(h.rules), h.features)
	fixed := []runtime.Option{
		runtime.WithClock(h.clock.Now),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(h.rules)),
		runtime.WithEventStore(h.events),
		runtime.WithIntakeAbandoner(h.abandoner),
		runtime.WithDispatcher(dispatcher),
		runtime.WithOpeningBuilder(dispatcher),
		runtime.WithFlowForKind(ruleFlows{rules: h.rules}),
		runtime.WithSummarySources(runtime.NewSummarySources(h.repo)),
		runtime.WithEntitlements(h.features),
		runtime.WithWelcomeStore(h.repo),
		runtime.WithReplyLimiter(h.limiter),
		runtime.WithSelfNumbers(h.selfNumbers),
		runtime.WithIngestDeduper(h.deduper),
		runtime.WithDepositReminder(h.deposits),
		runtime.WithPresignClient(h.presigner),
		runtime.WithReactiveBlockedHook(h.hooks.blocked),
		runtime.WithAutoreplyStreakHook(h.hooks.streakClosed),
	}
	sinks := h.buildSinks(h)
	opts := make([]runtime.Option, 0, len(fixed)+len(sinks))
	opts = append(opts, fixed...)
	for _, sink := range sinks {
		opts = append(opts, runtime.WithEventSink(sink))
	}
	for _, build := range h.buildExtras {
		opts = append(opts, build(h)...)
	}
	return opts
}

// menuFlow es el flujo mínimo de un guion: un menú de dos opciones que llevan a dos mensajes
// finales. No produce contenido durable, así que arranca por cualquier puerta.
func menuFlow(flowID string) model.Flow {
	return model.Flow{
		FlowID:  flowID,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  "Elige una opción-zzq",
				Options: map[string]string{"1": "first", "2": "second"},
			},
			"first":  {Type: model.NodeTypeMessage, Text: "Elegiste la primera-zzq"},
			"second": {Type: model.NodeTypeMessage, Text: "Elegiste la segunda-zzq"},
		},
	}
}

// seedFlow publica una definición para el tenant del guion y devuelve la versión asignada.
func (h *harness) seedFlow(f model.Flow) int {
	h.t.Helper()
	version, err := h.repo.InsertDefinition(h.t.Context(), harnessTenant, f)
	if err != nil {
		h.t.Fatalf("sembrar el flujo %q: %v", f.FlowID, err)
	}
	return version
}

// seedRule inserta una regla de disparo HABILITADA para el tenant del guion (el TenantID y
// Enabled del argumento se pisan) y devuelve la regla guardada.
func (h *harness) seedRule(r trigger.Rule) trigger.Rule {
	h.t.Helper()
	r.TenantID = harnessTenant
	r.Enabled = true
	saved, err := h.rules.Insert(h.t.Context(), r)
	if err != nil {
		h.t.Fatalf("sembrar la regla %s %q: %v", r.Kind, r.Keyword, err)
	}
	return saved
}

// seedSettings parte de los ajustes por defecto del tenant, aplica mutate y los guarda. El
// TTL de inactividad del evento se copia además al doble de eventos, que lo usa al listar.
func (h *harness) seedSettings(mutate func(s *store.TenantSettings)) {
	h.t.Helper()
	settings := store.DefaultTenantSettings(harnessTenant)
	mutate(&settings)
	h.repo.SetTenantSettings(settings)
	h.events.SetInactivityTTL(harnessTenant, int(settings.EventInactivityTTL/time.Second))
}

// enableFeature le da al tenant del guion una feature (entitlements.FeatureLLMIntake abre el
// hilo y la bienvenida).
func (h *harness) enableFeature(feature string) {
	h.features.Enable(harnessTenant, feature)
}

// contactID resuelve el contact_id opaco del teléfono, con las MISMAS referencias que lleva
// un entrante de incomingFrom: es el contacto con el que el runtime clava la conversación.
func (h *harness) contactID(phone string) string {
	h.t.Helper()
	id, err := h.contacts.Resolve(h.t.Context(), harnessTenant, contact.RefsFrom(phone, "", jid(phone)), "")
	if err != nil {
		h.t.Fatalf("resolver el contacto de %s: %v", phone, err)
	}
	return id
}

// key es la clave de la conversación del cliente del guion.
func (h *harness) key() store.Key {
	h.t.Helper()
	return store.Key{TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone)}
}

// seedEvent crea un evento VIVO de ese tipo para el cliente del guion, sin tocar el estado
// de la conversación (no lo deja activo ni dueño).
func (h *harness) seedEvent(kind, flowID string, flowVersion int) events.Event {
	h.t.Helper()
	ev, err := h.events.CreateEvent(h.t.Context(), events.NewEvent{
		TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone),
		Kind: kind, FlowID: flowID, FlowVersion: flowVersion,
	})
	if err != nil {
		h.t.Fatalf("sembrar el evento %q: %v", kind, err)
	}
	return ev
}

// jid es el JID de WhatsApp de un teléfono, como llega en IncomingMessage.From.
func jid(phone string) string { return phone + "@s.whatsapp.net" }

// incomingFrom fabrica el entrante de un teléfono: número y JID poblados, sin LID, con el
// instante del reloj del guion.
func (h *harness) incomingFrom(phone, waMessageID, text string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{
		From: jid(phone), FromPn: phone, Text: text, WaMessageId: waMessageID,
		PushName: "Cliente-zzq", TsUnix: h.clock.Now().Unix(),
	}
}

// incoming fabrica un entrante del cliente del guion.
func (h *harness) incoming(waMessageID, text string) *cloudlinkv1.IncomingMessage {
	return h.incomingFrom(harnessPhone, waMessageID, text)
}

// handle procesa el entrante en línea (HandleIncoming) por la sesión del guion.
func (h *harness) handle(m *cloudlinkv1.IncomingMessage) error {
	return h.rt.HandleIncoming(h.t.Context(), harnessSession, m)
}

// say es handle con un entrante del cliente del guion; falla el test si devuelve error.
func (h *harness) say(waMessageID, text string) {
	h.t.Helper()
	if err := h.handle(h.incoming(waMessageID, text)); err != nil {
		h.t.Fatalf("HandleIncoming(%s, %q): %v\nlog:\n%s", waMessageID, text, err, h.log.dump())
	}
}

// texts devuelve los textos despachados por SendText, en orden.
func (h *harness) texts() []string { return h.sender.Texts() }

// state devuelve el estado guardado de la conversación del cliente del guion.
func (h *harness) state() (model.Conversation, bool) {
	h.t.Helper()
	st, found, err := h.repo.Load(h.t.Context(), h.key())
	if err != nil {
		h.t.Fatalf("leer el estado: %v", err)
	}
	return st, found
}

// flowEvents devuelve las filas de flow_events con ese nombre, en orden de escritura.
func (h *harness) flowEvents(name string) []store.FlowEvent {
	var out []store.FlowEvent
	for _, fe := range h.repo.FlowEvents() {
		if fe.Name == name {
			out = append(out, fe)
		}
	}
	return out
}

// thread devuelve las entradas del hilo de un evento, sin el cuerpo (rol, clase y origen).
func (h *harness) thread(eventID string) []eventshelpertest.Entry { return h.events.Entries(eventID) }

// blockedReasons devuelve los motivos contados por WithReactiveBlockedHook, en orden.
func (h *harness) blockedReasons() []string {
	h.hooks.mu.Lock()
	defer h.hooks.mu.Unlock()
	return append([]string(nil), h.hooks.reasons...)
}

// closedStreaks devuelve las longitudes reportadas por WithAutoreplyStreakHook, en orden.
func (h *harness) closedStreaks() []int {
	h.hooks.mu.Lock()
	defer h.hooks.mu.Unlock()
	return append([]int(nil), h.hooks.streaks...)
}

// TestHarness_BuildsARuntime: el arnés monta un Runtime utilizable y sus ayudantes hablan con
// las piezas que dicen. Recorre un guion mínimo —una palabra clave que arranca un menú, con la
// bienvenida encendida— y comprueba que cada lectura del arnés ve lo que el runtime escribió.
func TestHarness_BuildsARuntime(t *testing.T) {
	h := newHarness(t,
		withProfile(runtimehelpertest.ProfileActive),
		withModules(),
		withEngineOptions(),
		withSinks(func(h *harness) []runtime.EventSink {
			return []runtime.EventSink{runtime.NewPersistSink(h.repo)}
		}),
		withOptions(func(*harness) []runtime.Option {
			return []runtime.Option{runtime.WithMaxConcurrentIncoming(4)}
		}),
	)
	if h.rt == nil {
		t.Fatal("newHarness no construyó el Runtime")
	}

	flow := menuFlow("greeting-zzq")
	if version := h.seedFlow(flow); version != 1 {
		t.Fatalf("seedFlow = versión %d, quería 1", version)
	}
	rule := h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: flow.FlowID})
	if rule.TriggerID == "" || !rule.Enabled || rule.TenantID != harnessTenant {
		t.Fatalf("seedRule = %+v, quería una regla habilitada del tenant del guion con id", rule)
	}
	h.seedSettings(func(s *store.TenantSettings) { s.ConversationTTL = time.Hour })
	h.enableFeature(entitlements.FeatureLLMIntake)

	h.say("wa-1", "hola")

	assertHarnessSeesTheStart(t, h, flow)
	assertHarnessIsQuiet(t, h)
	assertHarnessSharesOneClock(t, h)

	// Un segundo teléfono es otra conversación: no mueve el estado del primero.
	if err := h.handle(h.incomingFrom("573009998877", "wa-2", "1")); err != nil {
		t.Fatalf("HandleIncoming del segundo teléfono: %v", err)
	}
	if st, _ := h.state(); st.CurrentNode != flow.Initial {
		t.Errorf("el entrante de otro teléfono movió la conversación del primero a %q", st.CurrentNode)
	}
}

// assertHarnessSeesTheStart: tras la palabra clave, el arnés lee la bienvenida y el menú en
// el Sender y el estado recién guardado en el almacén, fechado con el reloj del guion.
func assertHarnessSeesTheStart(t *testing.T, h *harness, flow model.Flow) {
	t.Helper()
	texts := h.texts()
	if len(texts) != 2 || texts[0] != store.DefaultWelcomeText {
		t.Fatalf("textos enviados = %q, quería la bienvenida y después el menú\nlog:\n%s", texts, h.log.dump())
	}
	st, found := h.state()
	if !found || st.FlowID != flow.FlowID || st.CurrentNode != flow.Initial || st.FlowVersion != 1 {
		t.Fatalf("estado = (%+v, %v), quería la conversación en el nodo inicial de %s v1", st, found, flow.FlowID)
	}
	if !st.UpdatedAt.Equal(harnessStart) {
		t.Errorf("UpdatedAt = %v, quería el instante del reloj del guion %v", st.UpdatedAt, harnessStart)
	}
	if got := h.key(); got.ContactID != st.ContactID {
		t.Errorf("key().ContactID = %q, el estado dice %q", got.ContactID, st.ContactID)
	}
}

// assertHarnessIsQuiet: un arranque por keyword sin incidencias no deja errores en el log,
// ni cortes, ni rachas cerradas, ni evento, ni abandonos.
func assertHarnessIsQuiet(t *testing.T, h *harness) {
	t.Helper()
	if lines := h.log.at("error"); len(lines) != 0 {
		t.Errorf("líneas a error = %+v, no quería ninguna", lines)
	}
	if reasons := h.blockedReasons(); len(reasons) != 0 {
		t.Errorf("motivos de corte = %v, no quería ninguno", reasons)
	}
	if streaks := h.closedStreaks(); len(streaks) != 0 {
		t.Errorf("rachas cerradas = %v, no quería ninguna con la conversación viva", streaks)
	}
	if got := h.flowEvents(runtime.EffectEventStarted); len(got) != 0 {
		t.Errorf("event_started = %+v, una keyword sin event_kind no pare evento", got)
	}
	h.abandoner.fail(nil)
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, no quería ninguno", calls)
	}
}

// assertHarnessSharesOneClock: el reloj es UNO. Lo que el doble de eventos fecha es lo que el
// guion marca, y Set lo devuelve a su sitio.
func assertHarnessSharesOneClock(t *testing.T, h *harness) {
	t.Helper()
	h.clock.Advance(90 * time.Second)
	ev := h.seedEvent(trigger.EventKindCart, "", 0)
	if want := harnessStart.Add(90 * time.Second); !ev.CreatedAt.Equal(want) || !h.clock.Now().Equal(want) {
		t.Errorf("CreatedAt del evento = %v y reloj = %v, quería los dos en %v", ev.CreatedAt, h.clock.Now(), want)
	}
	if entries := h.thread(ev.ID); len(entries) != 0 {
		t.Errorf("hilo del evento recién sembrado = %+v, quería vacío", entries)
	}
	h.clock.Set(harnessStart)
	if !h.clock.Now().Equal(harnessStart) {
		t.Errorf("tras Set, el reloj marca %v, quería %v", h.clock.Now(), harnessStart)
	}
}
