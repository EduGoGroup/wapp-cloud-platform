//go:build pendiente

package runtime_test

// start_test.go prueba el contrato de start.go: la puerta de la API (Start, con sus dos
// centinelas) y las reglas del embudo de arranque (ST-A…ST-H). Las que solo alcanza un
// arranque REACTIVO —pre-carga, coletilla, hilo de apertura— van en start_funnel_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

const startFlowID = "greeting-zzq"

// startRef es la referencia de contacto con la que la API abre la conversación.
func startRef(t *testing.T, phone string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(contact.KindPhoneE164, phone)
	if err != nil {
		t.Fatalf("referencia de %s: %v", phone, err)
	}
	return ref
}

// startAPI llama a Start para el cliente del guion.
func startAPI(h *harness, flowID string) (*cloudlinkv1.Ack, error) {
	return h.rt.Start(h.t.Context(), harnessTenant, flowID, harnessSession, startRef(h.t, harnessPhone))
}

// startContacts envuelve el resolver de contactos del guion para hacer fallar una de sus dos
// preguntas, o contestar un destino fijo.
type startContacts struct {
	contact.Resolver
	resolveErr error
	destinoErr error
	destinoRef *contact.Ref
}

func (c startContacts) Resolve(ctx context.Context, tenantID string, refs []contact.Ref, pushName string) (string, error) {
	if c.resolveErr != nil {
		return "", c.resolveErr
	}
	return c.Resolver.Resolve(ctx, tenantID, refs, pushName)
}

func (c startContacts) Destino(ctx context.Context, tenantID, contactID string) (contact.Ref, error) {
	if c.destinoErr != nil {
		return contact.Ref{}, c.destinoErr
	}
	if c.destinoRef != nil {
		return *c.destinoRef, nil
	}
	return c.Resolver.Destino(ctx, tenantID, contactID)
}

// startRuntimeWith construye OTRO Runtime sobre las piezas del guion, con ese resolver de
// contactos en vez del suyo.
func startRuntimeWith(h *harness, contacts contact.Resolver) *runtime.Runtime {
	return runtime.New(h.repo, h.engine, h.sender, h.tenants, contacts, h.log, h.runtimeOptions()...)
}

// startSeedLive guarda una conversación del cliente del guion sobre el flujo de menú.
func startSeedLive(h *harness, node string) model.Conversation {
	h.t.Helper()
	st := model.Conversation{
		TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone),
		FlowID: startFlowID, FlowVersion: 1, CurrentNode: node,
		Vars: map[string]any{"kept-zzq": "yes"}, LastWaMessageID: "wa-previous",
	}
	if err := h.repo.Save(h.t.Context(), st); err != nil {
		h.t.Fatalf("sembrar la conversación: %v", err)
	}
	return st
}

// Pasos 3 y 6 · Start entra al nodo inicial con la versión VIGENTE, guarda y después envía;
// devuelve el Ack de la última salida. RT-4: en el instante del envío el estado ya está.
func TestStart_OpensTheConversation(t *testing.T) {
	h := newHarness(t)
	flow := menuFlow(startFlowID)
	h.seedFlow(flow)
	h.seedFlow(flow) // la vigente es la 2
	savedAtSend := false
	h.sender.OnSend(func(runtimehelpertest.Send) {
		_, savedAtSend = h.state()
	})

	ack, err := startAPI(h, startFlowID)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ack.GetAckedCommandId() != "cmd-1" {
		t.Errorf("Ack = %v, quería el de la última salida enviada (cmd-1)", ack)
	}
	if got := h.texts(); !resumeEqual(got, []string{flow.Nodes["root"].Prompt}) {
		t.Errorf("textos = %q, quería el menú del nodo inicial", got)
	}
	st, found := h.state()
	if !found || st.FlowID != startFlowID || st.FlowVersion != 2 || st.CurrentNode != "root" {
		t.Errorf("estado = (%+v, %v), quería el nodo inicial de la versión vigente (2)", st, found)
	}
	if !savedAtSend {
		t.Error("al enviar, el estado todavía no estaba guardado (RT-4: Save antes de Send)")
	}
}

// Start devuelve nil, sin error, si el nodo inicial no produjo ninguna salida; el estado se
// guarda igual y no cuenta en la racha.
func TestStart_ReturnsNilAckWhenTheInitialNodeSaysNothing(t *testing.T) {
	h := newHarness(t, withModules(resumeProbe{}))
	h.seedFlow(resumeProbeFlow(""))

	ack, err := startAPI(h, resumeProbeFlowID)

	if ack != nil || err != nil {
		t.Errorf("Start = (%v, %v), quería (nil, nil)", ack, err)
	}
	if got := h.sender.Attempts(); len(got) != 0 {
		t.Errorf("intentos de envío = %+v, no había nada que enviar", got)
	}
	if _, found := h.state(); !found {
		t.Error("el estado no se guardó")
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("racha = %d, una emisión sin salidas no suma", got)
	}
}

// Paso 1 · Si el contacto no se resuelve: error envuelto y nada más se toca.
func TestStart_ContactResolutionFailure(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(menuFlow(startFlowID))
	rt := startRuntimeWith(h, startContacts{Resolver: h.contacts, resolveErr: errResumeCause})

	ack, err := rt.Start(t.Context(), harnessTenant, startFlowID, harnessSession, startRef(t, harnessPhone))

	if ack != nil || err == nil || !strings.Contains(err.Error(), "runtime: resolver contacto: ") || !errors.Is(err, errResumeCause) {
		t.Errorf("Start = (%v, %v), quería «runtime: resolver contacto: …» envolviendo la causa", ack, err)
	}
	if got := h.sender.Attempts(); len(got) != 0 {
		t.Errorf("intentos de envío = %+v, no debía tocarse nada", got)
	}
	if _, found := h.state(); found {
		t.Error("quedó estado guardado tras fallar la resolución del contacto")
	}
}

// Paso 3 · Sin definición vigente: error envuelto, sin estado y sin envío.
func TestStart_UnknownFlow(t *testing.T) {
	h := newHarness(t)

	ack, err := startAPI(h, "missing-zzq")

	if ack != nil || err == nil || !strings.Contains(err.Error(), "runtime: definición vigente: ") {
		t.Errorf("Start = (%v, %v), quería «runtime: definición vigente: …»", ack, err)
	}
	if _, found := h.state(); found || len(h.sender.Attempts()) != 0 {
		t.Error("un flujo inexistente dejó estado o intentó enviar")
	}
}

// startDurableFlow es un menú con UN nodo de un módulo durable que no es el inicial.
func startDurableFlow() model.Flow {
	f := menuFlow("durable-zzq")
	f.Nodes["order"] = model.Node{Type: resumeProbeType, Prompt: resumeProbeScreen}
	return f
}

// Paso 4 / ST-B · Un flujo con contenido durable no arranca por la API y el rechazo no deja
// rastro: ni estado, ni efectos, ni envío, ni token, ni racha.
func TestStart_DurableFlowIsRejectedWithoutATrace(t *testing.T) {
	sinks := &resumeSinkLog{}
	h := resumeHarness(t, resumeProbe{durable: true}, []runtime.EventSink{&resumeSink{name: "a", log: sinks}})
	h.seedFlow(startDurableFlow())

	ack, err := startAPI(h, "durable-zzq")

	if ack != nil || !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Errorf("Start = (%v, %v), quería ErrDurableFlowNeedsEvent", ack, err)
	}
	if errors.Is(err, runtime.ErrConversationExists) {
		t.Error("el rechazo por flujo durable no puede confundirse con ErrConversationExists")
	}
	if _, found := h.state(); found {
		t.Error("el rechazo dejó estado guardado")
	}
	if len(h.sender.Attempts()) != 0 || len(sinks.order()) != 0 || len(h.repo.FlowEvents()) != 0 {
		t.Errorf("el rechazo dejó rastro: envíos=%v sinks=%v flow_events=%v", h.sender.Attempts(), sinks.order(), h.repo.FlowEvents())
	}
	if len(h.limiter.Calls()) != 0 || h.rt.MaxAutoreplyStreak() != 0 {
		t.Error("el rechazo cobró un token o sumó a la racha")
	}
}

// ST-A · La guarda de contenido durable va ANTES que la de existencia.
func TestStart_DurableGuardRunsBeforeTheExistenceGuard(t *testing.T) {
	h := resumeHarness(t, resumeProbe{durable: true}, nil)
	h.seedFlow(startDurableFlow())
	h.seedFlow(menuFlow(startFlowID))
	startSeedLive(h, "root")

	_, err := startAPI(h, "durable-zzq")

	if !errors.Is(err, runtime.ErrDurableFlowNeedsEvent) {
		t.Errorf("error = %v, quería ErrDurableFlowNeedsEvent aunque ya hubiera conversación", err)
	}
}

// Paso 5 / ST-B · Con estado para la clave: ErrConversationExists INCONDICIONAL. No se
// consulta ninguna política de reanudación ni se reinicia una conversación terminal, y el
// estado queda intacto.
func TestStart_ExistingConversationIsRejectedUnconditionally(t *testing.T) {
	for _, node := range []string{"root", model.NodeTerminal} {
		t.Run(node, func(t *testing.T) {
			policy := &resumePolicy{restart: true, notice: resumeNotice}
			h := newHarness(t, withOptions(func(*harness) []runtime.Option {
				return []runtime.Option{runtime.WithResumePolicy(model.NodeTypeMenu, policy)}
			}))
			h.seedFlow(menuFlow(startFlowID))
			before := startSeedLive(h, node)

			ack, err := startAPI(h, startFlowID)

			if ack != nil || !errors.Is(err, runtime.ErrConversationExists) {
				t.Errorf("Start = (%v, %v), quería ErrConversationExists", ack, err)
			}
			if restarts, seeds := policy.calls(); restarts != 0 || seeds != 0 {
				t.Errorf("se consultó la política de reanudación (Restart=%d, Seed=%d)", restarts, seeds)
			}
			st, _ := h.state()
			if st.CurrentNode != before.CurrentNode || st.Vars["kept-zzq"] != "yes" || st.LastWaMessageID != before.LastWaMessageID {
				t.Errorf("estado = %+v, quería el de antes intacto", st)
			}
			if got := h.sender.Attempts(); len(got) != 0 {
				t.Errorf("intentos de envío = %+v, un rechazo no envía", got)
			}
		})
	}
}

// RT-4 · Si el envío falla el error sube con el estado YA guardado: un segundo Start da
// ErrConversationExists.
func TestStart_SendFailureLeavesTheStateSaved(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(menuFlow(startFlowID))
	h.sender.FailText(errResumeCause)

	_, err := startAPI(h, startFlowID)

	if err == nil || !strings.Contains(err.Error(), "runtime: enviar texto: ") || !errors.Is(err, errResumeCause) {
		t.Errorf("error = %v, quería «runtime: enviar texto: …» envolviendo la causa", err)
	}
	if _, found := h.state(); !found {
		t.Fatal("el estado no quedó guardado antes del envío fallido")
	}
	h.sender.FailText(nil)
	if _, err := startAPI(h, startFlowID); !errors.Is(err, runtime.ErrConversationExists) {
		t.Errorf("segundo Start = %v, quería ErrConversationExists", err)
	}
}

// Lo que Start NO hace: ni evento, ni token, ni coletilla, ni hilo, ni las guardas del
// entrante (perfil pasivo, anti-self-loop, dedupe). ST-H: sí cuenta en la racha.
func TestStart_SkipsTheIncomingMachinery(t *testing.T) {
	h := newHarness(t, withProfile(runtimehelpertest.ProfilePassive))
	flow := menuFlow(startFlowID)
	h.seedFlow(flow)
	h.enableFeature(entitlements.FeatureLLMIntake)
	h.selfNumbers.Add(harnessTenant, harnessPhone)
	h.limiter.Limit(0)
	ev := h.seedEvent(trigger.EventKindCart, "", 0) // algo «a medias» que una coletilla anunciaría

	if _, err := startAPI(h, startFlowID); err != nil {
		t.Fatalf("Start: %v\nlog:\n%s", err, h.log.dump())
	}

	if got := h.texts(); !resumeEqual(got, []string{flow.Nodes["root"].Prompt}) {
		t.Errorf("textos = %q, quería SOLO el menú, sin coletilla ni bienvenida", got)
	}
	if len(h.limiter.Calls()) != 0 || len(h.deduper.Calls()) != 0 || len(h.selfNumbers.Queries()) != 0 {
		t.Errorf("Start pasó por las guardas del entrante: limitador=%v dedupe=%v propios=%v",
			h.limiter.Calls(), h.deduper.Calls(), h.selfNumbers.Queries())
	}
	if got := h.blockedReasons(); len(got) != 0 {
		t.Errorf("motivos de corte = %v, Start no se corta por perfil ni por número propio", got)
	}
	if got := h.events.Events(harnessTenant); len(got) != 1 {
		t.Errorf("eventos = %d, Start no crea evento (solo estaba el sembrado)", len(got))
	}
	if st, _ := h.state(); st.EventID != "" || st.OwnerEventID != "" {
		t.Errorf("estado = %+v, Start no estampa evento", st)
	}
	if got := resumeThread(h, ev.ID); len(got) != 0 {
		t.Errorf("hilo = %v, Start no escribe hilo", got)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 1 {
		t.Errorf("racha = %d, quería 1: el arranque es la primera auto-respuesta del episodio (ST-H)", got)
	}
}
