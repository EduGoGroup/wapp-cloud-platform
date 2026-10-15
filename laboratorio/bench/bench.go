//go:build laboratorio

// Package bench es el BANCO de la conversación del Bloque B del laboratorio (E05–E08): monta el
// motor de flujos REAL —runtime.New, el engine, los módulos menu/survey/cart/media, el resolver de
// disparos, el plano de eventos, la bienvenida, el PersistSink con sus proyectores— sobre los
// gemelos en memoria y los dobles de los puertos, con UN reloj manual. Es la misma lista de cables
// que internal/arranque/fase7_flujos.go (construirRuntimeDeFlujos), cable a cable, con los
// almacenes cambiados por sus gemelos.
//
// Vive en su propio paquete y no en common/ para no pisar a otros escalones que tocan common en
// paralelo; solo lo importan los escalones de la conversación.
//
// Qué es real y qué es doble, en una tabla (la explica cada ficha):
//
//	real   runtime.Runtime, engine.Engine, menu/survey/cart/media, trigger.ConfigResolver,
//	       events.Dispatcher, PersistSink + cart.Projector + survey.Projector, el agregador de
//	       ventanas (si se pide), turnoacotado.Resolver y el llmvia.Selector (si se pide)
//	gemelo store.MemoryRepository (flow_state, definiciones, flow_events, intakes, survey_results,
//	       tenant_content, bienvenida), trigger.MemoryStore (flow_triggers),
//	       eventshelpertest.Store (conversation_events y su hilo), contact.MemoryResolver
//	       (contacts), intake.MemoryStore (intake_jobs), intakes.MemoryStore (revisiones)
//	doble  runtimehelpertest.Sender (el Edge: guarda lo que se habría mandado), TenantResolver
//	       (sesión → tenant y perfil), IngestDeduper, ReplyLimiter (sin tope), entitlements Fake,
//	       el garante de la línea de envío (ShippingRecorder) y el abandonador (AbandonRecorder)
package bench

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/media"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// Los valores fijos de la conversación del laboratorio: el tenant de common, una sesión y un
// cliente. El teléfono es inventado y nunca sale del proceso.
const (
	Session  = "lab-session-058"
	Phone    = "573001110058"
	PushName = "Clienta del laboratorio"
)

// Options dice qué piezas opcionales se cablean. El cero monta el motor con todo lo que el
// arranque cablea siempre, sin features, sin resolutor de consultas y sin agregador.
type Options struct {
	// Features son las features del plan del tenant (entitlements.Feature…). Sin llm_intake no hay
	// bienvenida, ni hilo del evento, ni ventana de captación.
	Features []string
	// Resolver es el resolutor de consultas del engine (turnoacotado). nil ⇒ el engine contesta
	// «sin_resolutor» y el carrito repregunta (Trampa T-6 del arranque).
	Resolver engine.QueryResolver
	// Aggregator cablea el agregador de ventanas sobre un intake.MemoryStore. Solo ABRE ventanas:
	// el barrido que las cierra (Run) no se lanza aquí, eso es materia de E09.
	Aggregator bool
	// Limiter sustituye al limitador de auto-respuestas (nil ⇒ un doble sin tope).
	Limiter runtime.ReplyLimiter
	// Log recibe además todo lo que el runtime escribe (nil ⇒ solo se apunta en el LogBook).
	Log logger.Logger
}

// Bench es el motor montado. Los campos son las piezas: el escalón las siembra y las lee.
type Bench struct {
	Clock    *common.Clock
	Repo     *store.MemoryRepository
	Events   *eventshelpertest.Store
	Rules    *trigger.MemoryStore
	Contacts *contact.MemoryResolver
	Features *entitlementshelpertest.Fake
	Sender   *runtimehelpertest.Sender
	Tenants  *runtimehelpertest.TenantResolver
	Deduper  *runtimehelpertest.IngestDeduper
	Jobs     *intake.MemoryStore
	Intakes  *intakes.MemoryStore
	Shipping *ShippingRecorder
	Abandons *AbandonRecorder
	Log      *LogBook
	Engine   *engine.Engine
	Runtime  *runtime.Runtime

	queries []Query
	read    int
}

// Query es un desenlace del turno acotado tal como lo publica el engine (QueryObserver).
type Query struct{ Class, Level, Outcome string }

// New monta el banco con el reloj parado en common.AmberStart.
func New(opts Options) *Bench {
	b := &Bench{
		Clock:    common.NewClock(common.AmberStart),
		Repo:     store.NewMemoryRepository(),
		Events:   eventshelpertest.NewStore(),
		Rules:    trigger.NewMemoryStore(),
		Features: entitlementshelpertest.NewFake(),
		Sender:   runtimehelpertest.NewSender(),
		Tenants:  runtimehelpertest.NewTenantResolver(common.LabTenant, runtimehelpertest.ProfileActive),
		Deduper:  runtimehelpertest.NewIngestDeduper(),
		Intakes:  intakes.NewMemoryStore(),
		Shipping: &ShippingRecorder{},
		Abandons: &AbandonRecorder{},
		Log:      NewLogBook(opts.Log),
	}
	b.Repo.SetClock(b.Clock.Now)
	b.Events.SetClock(b.Clock.Now)
	b.Contacts = contact.NewMemoryResolver(b.Repo)
	for _, f := range opts.Features {
		b.Features.Enable(common.LabTenant, f)
	}

	// El registro y el engine, como faseFlujos: los cuatro módulos y la fuente de contenido
	// enrutada (static para menú/encuesta, JSON de tenant_content para el carrito).
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New(cart.WithLogger(b.Log)))
	reg.Register(media.New())
	engineOpts := []engine.Option{
		engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(b.Repo))),
		engine.WithQueryObserver(func(class, level, outcome string) {
			b.queries = append(b.queries, Query{Class: class, Level: level, Outcome: outcome})
		}),
	}
	if opts.Resolver != nil {
		engineOpts = append(engineOpts, engine.WithQueryResolver(opts.Resolver))
	}
	b.Engine = engine.New(reg, engineOpts...)

	limiter := opts.Limiter
	if limiter == nil {
		limiter = runtimehelpertest.NewReplyLimiter()
	}
	dispatcher := events.NewDispatcher(b.Events, events.NewTriggerKindOffer(b.Rules), b.Features)
	rtOpts := []runtime.Option{
		runtime.WithClock(b.Clock.Now),
		// El PersistSink con los dos proyectores del arranque. El del carrito recibe el gemelo de
		// flujos (sus intakes) y, para revisiones, el de solicitudes; el garante del envío es un
		// doble: ver ShippingRecorder.
		runtime.WithEventSink(runtime.NewPersistSink(b.Repo,
			cart.NewProjector(b.Repo, b.Intakes, b.Shipping, b.Intakes),
			survey.NewProjector(b.Repo)).WithDecisionThread(b.Events)),
		runtime.WithWelcomeStore(b.Repo),
		runtime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(b.Repo)),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(b.Rules)),
		runtime.WithEventStore(b.Events),
		runtime.WithIntakeAbandoner(b.Abandons),
		runtime.WithSummarySources(runtime.NewSummarySources(b.Repo)),
		runtime.WithDispatcher(dispatcher),
		runtime.WithOpeningBuilder(dispatcher),
		runtime.WithFlowForKind(ruleFlows{rules: b.Rules}),
		runtime.WithEntitlements(b.Features),
		runtime.WithReplyLimiter(limiter),
		runtime.WithIngestDeduper(b.Deduper),
	}
	if opts.Aggregator {
		b.Jobs = intake.NewMemoryStore(b.Clock.Now)
		agg := runtime.NewIntakeAggregator(b.Log, b.Jobs, b.Repo, b.Features,
			runtime.WithAggregatorClock(b.Clock.Now))
		rtOpts = append(rtOpts, runtime.WithAggregator(agg))
	}
	b.Runtime = runtime.New(b.Repo, b.Engine, b.Sender, b.Tenants, b.Contacts, b.Log, rtOpts...)
	return b
}

// PublishFlow publica una definición para el tenant y devuelve su versión.
func (b *Bench) PublishFlow(f model.Flow) (int, error) {
	return b.Repo.InsertDefinition(context.Background(), common.LabTenant, f)
}

// AddRule inserta una regla de disparo HABILITADA del tenant.
func (b *Bench) AddRule(r trigger.Rule) (trigger.Rule, error) {
	r.TenantID = common.LabTenant
	r.Enabled = true
	return b.Rules.Insert(context.Background(), r)
}

// SetContent guarda un blob en tenant_content (el catálogo del carrito).
func (b *Bench) SetContent(ref string, blob []byte) {
	b.Repo.SetTenantContent(common.LabTenant, ref, blob)
}

// Incoming fabrica el entrante de un teléfono como lo sube el Edge: número y JID, sin LID, con la
// hora del reloj del banco.
func (b *Bench) Incoming(phone, waID, text string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{
		From: phone + "@s.whatsapp.net", FromPn: phone, Text: text, WaMessageId: waID,
		PushName: PushName, TsUnix: b.Clock.Now().Unix(),
	}
}

// Say procesa EN LÍNEA (HandleIncoming, síncrono) un entrante del cliente del banco y devuelve los
// textos que el Edge habría mandado por él.
func (b *Bench) Say(waID, text string) ([]string, error) {
	return b.SayFrom(Phone, waID, text)
}

// SayFrom es Say desde otro teléfono: otra conversación.
func (b *Bench) SayFrom(phone, waID, text string) ([]string, error) {
	err := b.Runtime.HandleIncoming(context.Background(), Session, b.Incoming(phone, waID, text))
	return b.NewTexts(), err
}

// NewTexts devuelve los textos despachados desde la última llamada.
func (b *Bench) NewTexts() []string {
	all := b.Sender.Texts()
	out := append([]string(nil), all[b.read:]...)
	b.read = len(all)
	return out
}

// ContactID es el contact_id OPACO de un teléfono: el que el runtime clava en la conversación.
func (b *Bench) ContactID(phone string) (string, error) {
	return b.Contacts.Resolve(context.Background(), common.LabTenant,
		contact.RefsFrom(phone, "", phone+"@s.whatsapp.net"), "")
}

// Key es la clave de la conversación de un teléfono.
func (b *Bench) Key(phone string) (store.Key, error) {
	id, err := b.ContactID(phone)
	return store.Key{TenantID: common.LabTenant, SessionID: Session, ContactID: id}, err
}

// State lee el estado guardado (flow_state) de la conversación de un teléfono.
func (b *Bench) State(phone string) (model.Conversation, bool, error) {
	k, err := b.Key(phone)
	if err != nil {
		return model.Conversation{}, false, err
	}
	return b.Repo.Load(context.Background(), k)
}

// Queries devuelve los desenlaces del turno acotado publicados desde la última llamada.
func (b *Bench) Queries() []Query {
	out := b.queries
	b.queries = nil
	return out
}

// FlowEventNames devuelve «kind:name» de cada fila de flow_events, en orden de escritura.
func (b *Bench) FlowEventNames() []string {
	var out []string
	for _, fe := range b.Repo.FlowEvents() {
		out = append(out, fe.Kind+":"+fe.Name)
	}
	return out
}

// ruleFlows es el runtime.FlowForKind del banco: el flujo de la primera regla event_start
// habilitada de ese tipo, como el adaptador del arranque (internal/arranque/flowforkind.go).
type ruleFlows struct{ rules *trigger.MemoryStore }

// FlowForKind implementa runtime.FlowForKind.
func (f ruleFlows) FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	rules, err := f.rules.ListByKind(ctx, tenantID, sessionID, trigger.KindEventStart)
	if err != nil {
		return "", err
	}
	for _, r := range rules {
		if r.Enabled && r.EventKind == kind && r.FlowID != "" {
			return r.FlowID, nil
		}
	}
	return "", nil
}

// ShippingRecorder es el DOBLE del garante de la línea de envío del proyector del carrito.
//
// Por qué es doble y no el intakes.MemoryStore real: en producción la solicitud que el carrito
// cierra (store.CloseIntake) y la que recibe la línea de envío (EnsureShippingLine) son la MISMA
// fila de public.intakes. En memoria son DOS gemelos que no se ven: el cierre escribe en
// store.MemoryRepository y el garante busca en intakes.MemoryStore, que no la tiene y contesta
// ErrNotFound. Con un tenant sin zonas de envío (el del laboratorio) la política
// ShippingOnlyIfZones no tocaría nada, así que este doble apunta la llamada y contesta nil.
type ShippingRecorder struct{ Calls []string }

// EnsureShippingLine implementa cart.ShippingEnsurer.
func (s *ShippingRecorder) EnsureShippingLine(_ context.Context, _, intakeID string, policy intakes.ShippingPolicy) error {
	name := fmt.Sprintf("política %v", policy)
	if policy == intakes.ShippingOnlyIfZones {
		name = "ShippingOnlyIfZones"
	}
	s.Calls = append(s.Calls, intakeID+" ("+name+")")
	return nil
}

// AbandonRecorder es el DOBLE del abandonador de solicitudes (en producción, intakes.Service):
// apunta qué eventos abandonó el cliente.
type AbandonRecorder struct{ Events []string }

// AbandonByEvent implementa runtime.IntakeAbandoner.
func (a *AbandonRecorder) AbandonByEvent(_ context.Context, _, eventID string) error {
	a.Events = append(a.Events, eventID)
	return nil
}
