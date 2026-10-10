//go:build pendiente

package runtime_test

// event_lifecycle_test.go prueba la MUERTE EXPLÍCITA del evento (event_lifecycle.go): el
// cierre NATURAL cuando su flujo termina (LC-1…LC-9, por HandleIncoming), GetEventForTenant y
// ErrNoEventPlane. La cancelación por id va en event_lifecycle_cancel_test.go (E-13).
//
// Usa los ayudantes del plano de eventos (events_test.go): el guion del carrito es el menú de
// dos opciones del arnés, que termina al elegir «1».

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// lifecycleVerdictType es el tipo de nodo del módulo de guion que DECLARA su desenlace.
const lifecycleVerdictType = "verdict_zzq"

// lifecycleVerdictModule termina el flujo en su primer paso y declara cómo: «cancelar» →
// cancelado; cualquier otra cosa → completado.
type lifecycleVerdictModule struct{}

func (lifecycleVerdictModule) Type() string { return lifecycleVerdictType }

func (lifecycleVerdictModule) Render(node model.Node, _ model.Content) []string {
	return []string{node.Prompt}
}

func (lifecycleVerdictModule) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	end := model.NodeTerminal
	res := modules.Result{Next: &end, Vars: modules.CloneVars(conv.Vars), Outputs: []string{"Entendido-zzq"}, Outcome: model.OutcomeCompleted}
	if input == "cancelar" {
		res.Outcome = model.OutcomeCancelled
	}
	return res
}

func (lifecycleVerdictModule) WaitsForInput() bool          { return true }
func (lifecycleVerdictModule) ProducesDurableContent() bool { return false }

// lifecycleLiveCart abre el carrito y devuelve su evento, con el reloj movido para que un
// sellado posterior se distinga del nacimiento.
func lifecycleLiveCart(t *testing.T, h *harness) events.Event {
	t.Helper()
	h.say("wa-1", eventsCartWord)
	ev := eventsAliveOfKind(t, h, trigger.EventKindCart)
	h.clock.Advance(3 * time.Minute)
	return ev
}

// lifecycleRequirePointers exige los dos punteros del estado guardado.
func lifecycleRequirePointers(t *testing.T, h *harness, active, owner string) model.Conversation {
	t.Helper()
	st, found := h.state()
	if !found || st.EventID != active || st.OwnerEventID != owner {
		t.Fatalf("estado = (%+v, %v), quería activo %q y dueño %q\nlog:\n%s", st, found, active, owner, h.log.dump())
	}
	return st
}

// TestEventLifecycle_FinishingTheFlowClosesItsEvent (LC-1…LC-4, LC-8): al terminar el flujo
// el evento pasa a closed (event_closed), los dos punteros se apagan en el MISMO guardado del
// turno y la solicitud no se toca. El tipo queda libre: la misma palabra abre otro.
func TestEventLifecycle_FinishingTheFlowClosesItsEvent(t *testing.T) {
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)

	h.say("wa-2", "1")

	row := eventsRow(t, h, ev.ID)
	if row.Status != events.StatusClosed || !row.ClosedAt.Equal(h.clock.Now()) {
		t.Errorf("evento = %+v, quería closed sellado en %v", row, h.clock.Now())
	}
	st := lifecycleRequirePointers(t, h, "", "")
	if !st.Finished() || st.LastWaMessageID != "wa-2" {
		t.Errorf("estado = %+v, quería el flujo terminado y el turno estampado", st)
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 1)[0], ev, 0)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	if calls := h.abandoner.calls(); len(calls) != 0 {
		t.Errorf("abandonos = %v, el cierre natural no toca la solicitud", calls)
	}
	if texts := h.texts(); len(texts) != 2 || texts[1] != "Elegiste la primera-zzq" {
		t.Errorf("textos = %q, quería la respuesta del paso que terminó el flujo", texts)
	}

	h.say("wa-3", eventsCartWord)

	if born := eventsAliveOfKind(t, h, trigger.EventKindCart); born.ID == ev.ID {
		t.Error("tras el cierre no nació un evento nuevo del mismo tipo")
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 2)
}

// TestEventLifecycle_WithoutOwnerNoEventIsTransitioned (LC-1): con el dueño vacío —una fila
// anterior a que existiera— no se transiciona NINGÚN evento, haya o no activo.
func TestEventLifecycle_WithoutOwnerNoEventIsTransitioned(t *testing.T) {
	h := eventsCartHarness(t)
	ev := lifecycleLiveCart(t, h)
	st, _ := h.state()
	st.OwnerEventID = ""
	if err := h.repo.Save(t.Context(), st); err != nil {
		t.Fatalf("vaciar el dueño del estado: %v", err)
	}

	h.say("wa-2", "1")

	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen {
		t.Errorf("el evento activo quedó %q: sin dueño no se cierra nada", row.Status)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 0)
	if got := lifecycleRequirePointers(t, h, ev.ID, ""); !got.Finished() {
		t.Errorf("estado = %+v, quería el flujo terminado con el activo intacto", got)
	}
}

// lifecycleLetterFlow es un flujo cuya única opción es una PALABRA: su respuesta no se
// confunde con un número del menú del despachador.
func lifecycleLetterFlow() model.Flow {
	return model.Flow{
		FlowID:  eventsCartFlow,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: "¿Confirmas?-zzq", Options: map[string]string{"si": "done"}},
			"done": {Type: model.NodeTypeMessage, Text: "Hecho-zzq"},
		},
	}
}

// TestEventLifecycle_AnInheritedCartUnderAnActiveMenuClosesItsOwnEvent (LC-2, LC-4, RT-16,
// O5, INV-053.2): el dueño del estado y el evento al que habla el contacto son dos preguntas.
// Con un `menu` activo montado sobre el flujo de un `cart`, terminar ese flujo cierra AL
// CARRITO —su dueño— y deja el menú open y activo.
func TestEventLifecycle_AnInheritedCartUnderAnActiveMenuClosesItsOwnEvent(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(lifecycleLetterFlow())
	h.seedRule(eventsStartRule(eventsCartWord, trigger.EventKindCart, eventsCartFlow))
	h.seedRule(eventsStartRule(eventsMenuWord, trigger.EventKindMenu, ""))
	h.say("wa-1", eventsCartWord)
	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	h.say("wa-2", eventsMenuWord)
	menuEv := eventsAliveOfKind(t, h, trigger.EventKindMenu)
	lifecycleRequirePointers(t, h, menuEv.ID, cart.ID)

	h.say("wa-3", "si")

	if row := eventsRow(t, h, cart.ID); row.Status != events.StatusClosed {
		t.Errorf("el carrito quedó %q, quería closed: terminó SU flujo", row.Status)
	}
	if row := eventsRow(t, h, menuEv.ID); row.Status != events.StatusOpen || !row.ClosedAt.IsZero() {
		t.Errorf("el menú quedó %q: el fin de un flujo ajeno no lo cierra", row.Status)
	}
	// El dueño se apaga; el activo NO, porque no era el mismo evento.
	if st := lifecycleRequirePointers(t, h, menuEv.ID, ""); !st.Finished() {
		t.Errorf("estado = %+v, quería el flujo del carrito terminado", st)
	}
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 1)[0], cart, 0)
	texts := h.texts()
	if got := texts[len(texts)-1]; got != "Hecho-zzq" {
		t.Errorf("último texto = %q, quería la respuesta del flujo del carrito", got)
	}
}

// TestEventLifecycle_AfterEventStopTheOwnerStillClosesItsEvent (LC-2, EV-5): event_stop apaga
// el activo y conserva el dueño; si el contacto termina el flujo que quedó cargado, se
// cierra el evento DUEÑO.
func TestEventLifecycle_AfterEventStopTheOwnerStillClosesItsEvent(t *testing.T) {
	h := eventsSwitchHarness(t)
	ev := lifecycleLiveCart(t, h)
	h.say("wa-2", eventsStopWord)
	lifecycleRequirePointers(t, h, "", ev.ID)

	h.say("wa-3", "1")

	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusClosed {
		t.Errorf("el evento quedó %q, quería closed: su flujo terminó", row.Status)
	}
	lifecycleRequirePointers(t, h, "", "")
	eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 1)
}

// TestEventLifecycle_TheDeclaredOutcomeDecidesTheTerminalStatus (LC-3, LC-8): el desenlace
// que el módulo declaró decide el estado terminal, y el efecto sigue al ESTADO: cancelado →
// cancelled y event_cancelled; cualquier otro → closed y event_closed. La solicitud no se
// abandona tampoco con desenlace cancelado.
func TestEventLifecycle_TheDeclaredOutcomeDecidesTheTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		name, input, effect, silent string
		status                      events.Status
	}{
		{name: "cancelled", input: "cancelar", status: events.StatusCancelled, effect: runtime.EffectEventCancelled, silent: runtime.EffectEventClosed},
		{name: "completed", input: "confirmar", status: events.StatusClosed, effect: runtime.EffectEventClosed, silent: runtime.EffectEventCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withModules(lifecycleVerdictModule{}))
			h.seedFlow(model.Flow{FlowID: eventsCartFlow, Initial: "ask", Nodes: map[string]model.Node{
				"ask": {Type: lifecycleVerdictType, Prompt: "¿Confirmas o cancelas?-zzq"},
			}})
			h.seedRule(eventsStartRule(eventsCartWord, trigger.EventKindCart, eventsCartFlow))
			ev := lifecycleLiveCart(t, h)

			h.say("wa-2", tc.input)

			if row := eventsRow(t, h, ev.ID); row.Status != tc.status || !row.ClosedAt.Equal(h.clock.Now()) {
				t.Errorf("evento = %+v, quería %q sellado en %v", row, tc.status, h.clock.Now())
			}
			eventsRequireRowOf(t, eventsRequireLifecycle(t, h, tc.effect, 1)[0], ev, 0)
			eventsRequireLifecycle(t, h, tc.silent, 0)
			lifecycleRequirePointers(t, h, "", "")
			if calls := h.abandoner.calls(); len(calls) != 0 {
				t.Errorf("abandonos = %v, el cierre natural no abandona la solicitud", calls)
			}
		})
	}
}

// TestEventLifecycle_LosingTheCloseRaceTurnsThePointersOffWithoutEffect (LC-5): si otro
// escritor selló la muerte primero (events.ErrNotOpen), los punteros se apagan igual, no se
// emite efecto y se anuncia a Info.
func TestEventLifecycle_LosingTheCloseRaceTurnsThePointersOffWithoutEffect(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsCartHarness(t, eventsWithFaults(faults))
	ev := lifecycleLiveCart(t, h)
	faults.loseTransitionRace(events.StatusCancelled)

	h.say("wa-2", "1")

	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusCancelled {
		t.Errorf("el evento quedó %q, quería el cancelled del escritor que ganó", row.Status)
	}
	lifecycleRequirePointers(t, h, "", "")
	eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 0)
	eventsRequireLifecycle(t, h, runtime.EffectEventCancelled, 0)
	const benign = "runtime: el evento ya no estaba open al terminar su flujo (carrera benigna)"
	if !eventsLogged(h, "info", benign) {
		t.Errorf("no se anunció la carrera benigna a Info\nlog:\n%s", h.log.dump())
	}
}

// TestEventLifecycle_ARealFailureKeepsThePointersAndTheNextIncomingRetries (LC-6): un fallo
// REAL de la transición no aborta el turno y no limpia ningún puntero; el estado se guarda
// terminal y el SIGUIENTE entrante reintenta el cierre sobre esa misma fila.
func TestEventLifecycle_ARealFailureKeepsThePointersAndTheNextIncomingRetries(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsCartHarness(t, eventsWithFaults(faults))
	ev := lifecycleLiveCart(t, h)
	faults.onTransition(func(context.Context, string, events.Status) error { return errEventsInjected })

	h.say("wa-2", "1")

	if st := lifecycleRequirePointers(t, h, ev.ID, ev.ID); !st.Finished() {
		t.Errorf("estado = %+v, quería el estado guardado terminal con sus dos punteros", st)
	}
	if texts := h.texts(); len(texts) != 2 || texts[1] != "Elegiste la primera-zzq" {
		t.Errorf("textos = %q, el fallo del cierre no debía tumbar la respuesta del turno", texts)
	}
	const keeps = "runtime: no se pudo cerrar el evento al terminar su flujo; el puntero se conserva para reintentar"
	if !eventsLogged(h, "warn", keeps) {
		t.Errorf("no se avisó a WARN del cierre que no se pudo sellar\nlog:\n%s", h.log.dump())
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 0)

	faults.onTransition(nil)
	h.say("wa-3", "cualquier cosa-zzq")

	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusClosed {
		t.Errorf("el evento quedó %q, quería closed: el siguiente entrante reintenta el cierre", row.Status)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 1)
	lifecycleRequirePointers(t, h, "", "")
}

// TestEventLifecycle_AnEventThatDiedUnreadEmitsNoEffect (LC-7): si el evento murió pero no se
// pudo releer antes para su telemetría, no hay efecto y se avisa a WARN.
func TestEventLifecycle_AnEventThatDiedUnreadEmitsNoEffect(t *testing.T) {
	faults := &eventsFaultyStore{}
	h := eventsSwitchHarness(t, eventsWithFaults(faults))
	ev := lifecycleLiveCart(t, h)
	h.say("wa-2", eventsStopWord) // sin activo, el turno siguiente solo relee al DUEÑO
	faults.failListAlive(errEventsInjected)

	h.say("wa-3", "1")

	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusClosed {
		t.Errorf("el evento quedó %q, quería closed aunque no se pudiera releer", row.Status)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 0)
	const unread = "runtime: el evento murió al terminar su flujo pero no se pudo releer antes para emitir su efecto de ciclo de vida"
	if !eventsLogged(h, "warn", unread) {
		t.Errorf("no se avisó a WARN de la telemetría perdida\nlog:\n%s", h.log.dump())
	}
	lifecycleRequirePointers(t, h, "", "")
}

// TestEventLifecycle_AOneStepFlowClosesInTheSameStart (LC-9): un flujo de UN solo paso (un
// `message` sin `next`) cierra su evento en el mismo arranque: la fila queda closed y el
// puntero no sobrevive ni un turno.
func TestEventLifecycle_AOneStepFlowClosesInTheSameStart(t *testing.T) {
	h := newHarness(t)
	h.seedFlow(model.Flow{FlowID: "notice-flow-zzq", Initial: "only", Nodes: map[string]model.Node{
		"only": {Type: model.NodeTypeMessage, Text: "Abrimos de 9 a 18-zzq"},
	}})
	h.seedRule(eventsStartRule("horario", trigger.EventKindMedia, "notice-flow-zzq"))

	h.say("wa-1", "horario")

	rows := h.events.Events(harnessTenant)
	if len(rows) != 1 || rows[0].Status != events.StatusClosed {
		t.Fatalf("eventos = %+v, quería uno, ya closed", rows)
	}
	if st := lifecycleRequirePointers(t, h, "", ""); !st.Finished() {
		t.Errorf("estado = %+v, quería el flujo de un paso terminado", st)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventClosed, 1)[0], rows[0], 0)
	if texts := h.texts(); len(texts) != 1 || texts[0] != "Abrimos de 9 a 18-zzq" {
		t.Errorf("textos = %q, quería el único mensaje del flujo", texts)
	}
}

// TestEventLifecycle_WithoutEventPlaneBothDoorsSayErrNoEventPlane: sin WithEventStore,
// GetEventForTenant y CancelEventForTenant devuelven ErrNoEventPlane y el evento cero. No se
// disfraza de «no encontrado».
func TestEventLifecycle_WithoutEventPlaneBothDoorsSayErrNoEventPlane(t *testing.T) {
	h := eventsCartHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithEventStore(nil)}
	}))
	seeded := h.seedEvent(trigger.EventKindCart, eventsCartFlow, 1)

	got, err := h.rt.GetEventForTenant(t.Context(), harnessTenant, seeded.ID)
	if !errors.Is(err, runtime.ErrNoEventPlane) || errors.Is(err, events.ErrEventNotFound) || got != (events.Event{}) {
		t.Errorf("GetEventForTenant sin plano = (%+v, %v), quería el evento cero y ErrNoEventPlane", got, err)
	}
	got, err = h.rt.CancelEventForTenant(t.Context(), harnessTenant, seeded.ID)
	if !errors.Is(err, runtime.ErrNoEventPlane) || errors.Is(err, events.ErrEventNotFound) || got != (events.Event{}) {
		t.Errorf("CancelEventForTenant sin plano = (%+v, %v), quería el evento cero y ErrNoEventPlane", got, err)
	}
	if row := eventsRow(t, h, seeded.ID); row.Status != events.StatusOpen {
		t.Errorf("el evento quedó %q: sin plano no se toca nada", row.Status)
	}
	if want := "runtime: sin plano de eventos cableado (WithEventStore)"; runtime.ErrNoEventPlane.Error() != want {
		t.Errorf("ErrNoEventPlane = %q, quería %q", runtime.ErrNoEventPlane.Error(), want)
	}
}

// TestGetEventForTenant_DelegatesScopedToTheTenant: lee un evento por id ACOTADO al tenant
// delegando en el almacén. Un id inexistente y uno de OTRO tenant dan la MISMA respuesta
// (events.ErrEventNotFound); cualquier otro error sube sin envolver; no escribe nada.
func TestGetEventForTenant_DelegatesScopedToTheTenant(t *testing.T) {
	const otherTenant = "22222222-2222-4222-8222-222222222222"
	faults := &eventsFaultyStore{}
	h := eventsCartHarness(t, eventsWithFaults(faults))
	ev := lifecycleLiveCart(t, h)
	before, _ := h.state()

	got, err := h.rt.GetEventForTenant(t.Context(), harnessTenant, ev.ID)
	if err != nil || got != eventsRow(t, h, ev.ID) {
		t.Errorf("GetEventForTenant = (%+v, %v), quería la fila del almacén", got, err)
	}
	for name, call := range map[string][2]string{
		"another tenant": {otherTenant, ev.ID},
		"unknown id":     {harnessTenant, uuid.NewString()},
	} {
		got, err := h.rt.GetEventForTenant(t.Context(), call[0], call[1])
		if !errors.Is(err, events.ErrEventNotFound) || got != (events.Event{}) {
			t.Errorf("%s: GetEventForTenant = (%+v, %v), quería events.ErrEventNotFound", name, got, err)
		}
	}
	faults.failGet(errEventsInjected)
	_, err = h.rt.GetEventForTenant(t.Context(), harnessTenant, ev.ID)
	if !errors.Is(err, errEventsInjected) || err.Error() != errEventsInjected.Error() {
		t.Errorf("GetEventForTenant = %v, quería el error del almacén sin envolver", err)
	}

	after, _ := h.state()
	if !after.UpdatedAt.Equal(before.UpdatedAt) || after.EventID != before.EventID {
		t.Errorf("estado = %+v, leer un evento no escribe nada (antes: %+v)", after, before)
	}
	if rows := h.flowEvents(runtime.EffectEventCancelled); len(rows) != 0 {
		t.Errorf("event_cancelled = %+v, leer no emite efectos", rows)
	}
}
