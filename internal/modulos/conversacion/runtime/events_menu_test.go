package runtime_test

// events_menu_test.go prueba el MENÚ del despachador y la OFERTA de entrada (events.go, EV-7)
// y la regla RT-12: WithOpeningBuilder sustituye SOLO al texto del fallback (INV-20).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Las pantallas del despachador de un guion que solo ofrece `cart`, byte a byte.
const (
	eventsMenuStartOnly = "¿Qué quieres hacer? Responde con el número de la opción:\n\n" +
		"1. Hacer un pedido\n" +
		"\nSi prefieres otra cosa, escríbelo y te ayudamos."
	eventsMenuStartAndRescue = "¿Qué quieres hacer? Responde con el número de la opción:\n\n" +
		"1. Hacer un pedido\n" +
		"2. Retomar algo que dejaste a medias (1)\n" +
		"\nSi prefieres otra cosa, escríbelo y te ayudamos."
	eventsRescueList = "Pasó un rato sin novedades, así que ahora mismo no tenemos nada en curso. " +
		"Si quieres, puedes retomar lo que dejaste a medias — responde con el número:\n\n" +
		"1. Retomar el pedido que dejaste a medias\n" +
		"\nSi prefieres otra cosa, escríbelo y te ayudamos."
	eventsFallbackFlow = "fallback-flow-zzq"
	// Las dos claves de Vars del menú pendiente. Son estado PERSISTIDO (flow_state.vars): el
	// binario nuevo tiene que leer el menú que dejó pendiente el viejo.
	eventsVarPendingMenu     = "dispatcher_menu"
	eventsVarPendingMenuSeal = "dispatcher_menu_event_id"
)

// eventsOpening es un OpeningBuilder de guion: contesta la misma oferta a BuildOpening y a
// BuildRescue, o el error que se le ponga a cada puerta, y cuenta las ofertas pedidas.
type eventsOpening struct {
	offer                             events.Offering
	openingErr, rescueErr, taglineErr error
	openings                          int
}

func (o *eventsOpening) BuildOpening(context.Context, events.ConversationRef) (events.Offering, error) {
	o.openings++
	return o.offer, o.openingErr
}

func (o *eventsOpening) BuildRescue(context.Context, events.ConversationRef) (events.Offering, error) {
	return o.offer, o.rescueErr
}

func (o *eventsOpening) BuildTagline(context.Context, events.ConversationRef) (string, error) {
	return "", o.taglineErr
}

// eventsWithOpening sustituye el OpeningBuilder del guion.
func eventsWithOpening(o runtime.OpeningBuilder) harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithOpeningBuilder(o)}
	})
}

// eventsBrokenDispatcher es un Dispatcher cuyo Build falla.
type eventsBrokenDispatcher struct{}

func (eventsBrokenDispatcher) Build(context.Context, events.ConversationRef) (events.Menu, error) {
	return events.Menu{}, errEventsInjected
}

// eventsMenuHarness es el guion del carrito más la palabra que abre el menú del despachador.
func eventsMenuHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := eventsCartHarness(t, opts...)
	h.seedRule(eventsStartRule(eventsMenuWord, trigger.EventKindMenu, ""))
	return h
}

// eventsFallbackHarness es el guion del carrito más un fallback con su flujo.
func eventsFallbackHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	h := eventsCartHarness(t, opts...)
	h.seedFlow(menuFlow(eventsFallbackFlow))
	h.seedRule(trigger.Rule{Kind: trigger.KindFallback, FlowID: eventsFallbackFlow})
	return h
}

// eventsRequireFallbackStarted exige que el entrante arrancara el flujo del fallback de
// siempre: su pantalla, su estado y ningún evento.
func eventsRequireFallbackStarted(t *testing.T, h *harness) {
	t.Helper()
	if texts := h.texts(); len(texts) != 1 || texts[0] != menuFlow(eventsFallbackFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, quería la pantalla del flujo del fallback\nlog:\n%s", texts, h.log.dump())
	}
	st, found := h.state()
	if !found || st.FlowID != eventsFallbackFlow || st.EventID != "" || len(st.Vars) != 0 {
		t.Errorf("estado = (%+v, %v), quería el flujo del fallback arrancado, sin evento ni menú pendiente", st, found)
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, el fallback no pare ninguno", rows)
	}
}

// eventsStringVar lee una Var de texto del estado.
func eventsStringVar(vars map[string]any, key string) (string, bool) {
	value, ok := vars[key].(string)
	return value, ok
}

// eventsRequirePendingMenu exige el estado que deja el menú recién enviado: el evento del
// menú como activo, sin flujo ni dueño, y en Vars el menú ENTERO sellado con ese evento.
func eventsRequirePendingMenu(t *testing.T, h *harness, menuEv events.Event) {
	t.Helper()
	st, found := h.state()
	if !found || st.EventID != menuEv.ID || st.OwnerEventID != "" || st.FlowID != "" {
		t.Fatalf("estado = (%+v, %v), quería el menú como activo, sin flujo y sin dueño", st, found)
	}
	raw, _ := eventsStringVar(st.Vars, eventsVarPendingMenu)
	pending, err := events.DecodeMenu([]byte(raw))
	if err != nil || len(pending.Options) != 1 || pending.Options[0].Action != events.ActionStart || pending.Options[0].Kind != trigger.EventKindCart {
		t.Errorf("menú pendiente = (%+v, %v), quería el menú ENTERO que se envió", pending, err)
	}
	if seal, _ := eventsStringVar(st.Vars, eventsVarPendingMenuSeal); seal != menuEv.ID {
		t.Errorf("sello del menú = %q, quería el evento sobre el que se armó: %s", seal, menuEv.ID)
	}
}

// TestEvents_MenuIsSentAndLeftPendingSealed (EV-7, EV-2): el menú se envía y queda PENDIENTE
// en Vars, entero y SELLADO con su evento; el número del mensaje siguiente lo interpreta el
// runtime, y «empezar uno nuevo» es la tercera puerta del nacimiento.
func TestEvents_MenuIsSentAndLeftPendingSealed(t *testing.T) {
	h := eventsMenuHarness(t)

	h.say("wa-1", eventsMenuWord)

	menuEv := eventsAliveOfKind(t, h, trigger.EventKindMenu)
	if texts := h.texts(); len(texts) != 1 || texts[0] != eventsMenuStartOnly {
		t.Fatalf("textos = %q, quería el menú del despachador:\n%q", texts, eventsMenuStartOnly)
	}
	eventsRequirePendingMenu(t, h, menuEv)

	h.say("wa-2", "1")

	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if cart.FlowID != eventsCartFlow || cart.FlowVersion != 1 {
		t.Errorf("el carrito nació con %q v%d, quería el flujo de su regla event_start", cart.FlowID, cart.FlowVersion)
	}
	if st, _ := h.state(); st.EventID != cart.ID || st.OwnerEventID != cart.ID || st.FlowID != eventsCartFlow {
		t.Errorf("estado = %+v, quería la conversación dentro del carrito recién nacido", st)
	}
	if row := eventsRow(t, h, menuEv.ID); row.Status != events.StatusOpen {
		t.Errorf("el menú quedó %q, elegir una opción no lo cierra", row.Status)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 2)
	if texts := h.texts(); len(texts) != 2 || texts[1] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, quería la pantalla inicial del carrito tras elegirlo", texts)
	}
}

// TestEvents_EmptyMenuIsNotSent (EV-7): si el tenant no ofrece nada y el contacto no tiene
// nada que retomar, mandar una lista sin opciones sería peor que callar.
func TestEvents_EmptyMenuIsNotSent(t *testing.T) {
	h := newHarness(t)
	h.seedRule(eventsStartRule(eventsMenuWord, trigger.EventKindMenu, ""))

	h.say("wa-1", eventsMenuWord)

	eventsAliveOfKind(t, h, trigger.EventKindMenu)
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, un menú vacío no se envía", texts)
	}
	if st, found := h.state(); found {
		t.Errorf("estado = %+v, un menú vacío no deja nada pendiente", st)
	}
}

// TestEvents_WithoutDispatcherTheMenuPresentsNothing (Dispatcher nil): el evento `menu` nace
// pero no presenta nada, y se dice a Debug.
func TestEvents_WithoutDispatcherTheMenuPresentsNothing(t *testing.T) {
	h := eventsMenuHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithDispatcher(nil)}
	}))

	h.say("wa-1", eventsMenuWord)

	eventsAliveOfKind(t, h, trigger.EventKindMenu)
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, sin despachador no hay lista que presentar", texts)
	}
	const silent = "runtime: sin despachador cableado; el menú no presenta nada"
	if !eventsLogged(h, "debug", silent) {
		t.Errorf("no se anunció a Debug que no hay despachador\nlog:\n%s", h.log.dump())
	}
}

// TestEvents_DispatcherErrorRisesAndCutsTheTurn (Dispatcher): un error de Build sube.
func TestEvents_DispatcherErrorRisesAndCutsTheTurn(t *testing.T) {
	h := eventsMenuHarness(t, withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithDispatcher(eventsBrokenDispatcher{})}
	}))

	err := h.handle(h.incoming("wa-1", eventsMenuWord))

	if !errors.Is(err, errEventsInjected) {
		t.Fatalf("HandleIncoming = %v, quería el error del despachador", err)
	}
	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, con el turno cortado no se envía nada", texts)
	}
}

// TestEvents_PendingMenuExpiresWhenItsSealNoLongerMatches (EV-7, E-2): el número solo se
// interpreta si el sello coincide con el activo de ESE turno. Cancelado el evento del menú
// desde la app (el puntero se apaga y las Vars se conservan), el «1» ya no despacha nada.
func TestEvents_PendingMenuExpiresWhenItsSealNoLongerMatches(t *testing.T) {
	h := eventsMenuHarness(t)
	h.say("wa-1", eventsMenuWord)
	menuEv := eventsAliveOfKind(t, h, trigger.EventKindMenu)
	if _, err := h.rt.CancelEventForTenant(t.Context(), harnessTenant, menuEv.ID); err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}
	if st, _ := h.state(); st.EventID != "" || st.Vars[eventsVarPendingMenu] == nil {
		t.Fatalf("estado = %+v, quería el puntero apagado y el menú todavía en Vars", st)
	}

	h.say("wa-2", "1")

	if rows := h.events.Events(harnessTenant); len(rows) != 1 {
		t.Errorf("eventos = %+v, el «1» de un menú caducado no debía parir un carrito", rows)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	if texts := h.texts(); len(texts) != 1 {
		t.Errorf("textos = %q, el menú caduca en silencio", texts)
	}
}

// TestEvents_UnreadableMenuIsDiscardedWithAWarning (EV-7): un menú pendiente ilegible no
// bloquea la conversación ni despacha nada.
func TestEvents_UnreadableMenuIsDiscardedWithAWarning(t *testing.T) {
	h := eventsMenuHarness(t)
	h.say("wa-1", eventsMenuWord)
	st, _ := h.state()
	st.Vars[eventsVarPendingMenu] = "{esto no es un menú"
	if err := h.repo.Save(t.Context(), st); err != nil {
		t.Fatalf("estropear el menú pendiente: %v", err)
	}

	h.say("wa-2", "1")

	const unreadable = "runtime: menú pendiente ilegible; se ignora"
	if !eventsLogged(h, "warn", unreadable) {
		t.Errorf("no se avisó a WARN del menú ilegible\nlog:\n%s", h.log.dump())
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
}

// TestEvents_MenuOverATerminalStateStartsClean (EV-7): un estado TERMINAL cuenta como
// «ninguna conversación» al montar el menú; del flujo que acabó solo se conserva
// last_wa_message_id, que es la dedupe del entrante.
func TestEvents_MenuOverATerminalStateStartsClean(t *testing.T) {
	h := eventsMenuHarness(t)
	h.seedFlow(menuFlow("plain-flow-zzq"))
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "plain-flow-zzq"})
	h.say("wa-1", "hola")
	h.say("wa-2", "1")
	if st, found := h.state(); !found || !st.Finished() || st.LastWaMessageID != "wa-2" {
		t.Fatalf("estado = (%+v, %v), quería el flujo plano terminado con su último wa_message_id", st, found)
	}

	h.say("wa-3", eventsMenuWord)

	menuEv := eventsAliveOfKind(t, h, trigger.EventKindMenu)
	st, found := h.state()
	if !found || st.Finished() || st.FlowID != "" || st.CurrentNode != "" || st.EventID != menuEv.ID || st.OwnerEventID != "" {
		t.Errorf("estado = (%+v, %v), quería un estado limpio, sin el flujo acabado, apuntando al menú", st, found)
	}
	if st.LastWaMessageID != "wa-2" {
		t.Errorf("last_wa_message_id = %q, quería conservar %q", st.LastWaMessageID, "wa-2")
	}
	if row := eventsRow(t, h, menuEv.ID); row.Status != events.StatusOpen {
		t.Errorf("el menú quedó %q montado sobre un flujo ajeno ya acabado, quería open", row.Status)
	}
}

// TestEvents_OpeningReplacesOnlyTheFallbackText (RT-12, INV-20): con una oferta NO vacía, el
// fallback envía la oferta —sin arrancar su flujo ni parir evento— y deja su menú pendiente
// SIN evento. Elegir «1» es la tercera puerta del nacimiento.
func TestEvents_OpeningReplacesOnlyTheFallbackText(t *testing.T) {
	h := eventsFallbackHarness(t)

	h.say("wa-1", "hola-zzq")

	if texts := h.texts(); len(texts) != 1 || texts[0] != eventsMenuStartOnly {
		t.Fatalf("textos = %q, quería la oferta de entrada:\n%q", texts, eventsMenuStartOnly)
	}
	st, found := h.state()
	if !found || st.FlowID != "" || st.EventID != "" || st.OwnerEventID != "" {
		t.Fatalf("estado = (%+v, %v), quería el menú de la oferta pendiente, sin flujo y sin evento", st, found)
	}
	if seal, sealed := eventsStringVar(st.Vars, eventsVarPendingMenuSeal); !sealed || seal != "" {
		t.Errorf("sello de la oferta = (%q, %v), quería el sello vacío: no hay evento", seal, sealed)
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 0 {
		t.Errorf("eventos = %+v, ofrecer no pare ninguno", rows)
	}
	if calls := h.limiter.Calls(); len(calls) != 1 {
		t.Errorf("tokens pedidos = %d, la oferta cobra UNO", len(calls))
	}

	h.say("wa-2", "1")

	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if st, _ := h.state(); st.EventID != cart.ID || st.OwnerEventID != cart.ID || st.FlowID != eventsCartFlow {
		t.Errorf("estado = %+v, quería la conversación dentro del carrito elegido en la oferta", st)
	}
}

// TestEvents_OpeningFallsBackToTheFallbackOfAlways (RT-12): una oferta vacía, un fallo al
// construirla o la opción sin cablear dejan el fallback como siempre. Un tenant recién
// creado no se queda mudo.
func TestEvents_OpeningFallsBackToTheFallbackOfAlways(t *testing.T) {
	const warning = "runtime: no se pudo construir la entrada que ofrece"
	for _, tc := range []struct {
		name     string
		opening  runtime.OpeningBuilder
		wantWarn bool
	}{
		{name: "empty offering", opening: &eventsOpening{}},
		{name: "builder fails", opening: &eventsOpening{openingErr: errEventsInjected}, wantWarn: true},
		{name: "option not wired", opening: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := eventsFallbackHarness(t, eventsWithOpening(tc.opening))

			h.say("wa-1", "hola-zzq")

			eventsRequireFallbackStarted(t, h)
			if got := eventsLogged(h, "warn", warning); got != tc.wantWarn {
				t.Errorf("WARN de la oferta = %v, quería %v\nlog:\n%s", got, tc.wantWarn, h.log.dump())
			}
		})
	}
}

// TestEvents_OnlyTheFallbackAsksForTheOpening (RT-12): Start, StartEvent e Ignore no se
// enteran de la oferta; el fallback la pide UNA vez por entrante.
func TestEvents_OnlyTheFallbackAsksForTheOpening(t *testing.T) {
	opening := &eventsOpening{}
	h := eventsCartHarness(t, eventsWithOpening(opening))
	h.seedFlow(menuFlow("plain-flow-zzq"))
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "plain-flow-zzq"})

	h.say("wa-1", "nada casa-zzq") // Ignore: no hay fallback
	if st, found := h.state(); found || len(h.texts()) != 0 {
		t.Fatalf("Ignore dejó estado (%+v) o envió algo (%q)", st, h.texts())
	}
	h.say("wa-2", "hola")                                                                    // Start
	if err := h.handle(h.incomingFrom("573009998877", "wa-3", eventsCartWord)); err != nil { // StartEvent
		t.Fatalf("HandleIncoming del event_start: %v", err)
	}
	if opening.openings != 0 {
		t.Errorf("BuildOpening se llamó %d veces sin ningún fallback de por medio", opening.openings)
	}

	h.seedFlow(menuFlow(eventsFallbackFlow))
	h.seedRule(trigger.Rule{Kind: trigger.KindFallback, FlowID: eventsFallbackFlow})
	if err := h.handle(h.incomingFrom("573004445566", "wa-4", "nada casa-zzq")); err != nil {
		t.Fatalf("HandleIncoming del fallback: %v", err)
	}
	if opening.openings != 1 {
		t.Errorf("BuildOpening se llamó %d veces por un entrante de fallback, quería 1", opening.openings)
	}
}

// eventsRescueHarness deja al cliente con un `cart` vivo y SIN conversación (lo que queda
// tras vencer), y con la oferta de entrada ya enviada: «1. Hacer un pedido / 2. Retomar…».
func eventsRescueHarness(t *testing.T, opts ...harnessOption) (*harness, events.Event) {
	t.Helper()
	h := eventsFallbackHarness(t, opts...)
	ev := h.seedEvent(trigger.EventKindCart, eventsCartFlow, 1)
	h.say("wa-1", "hola-zzq")
	if texts := h.texts(); len(texts) != 1 || texts[0] != eventsMenuStartAndRescue {
		t.Fatalf("textos = %q, quería la oferta con la entrada de retomar:\n%q", texts, eventsMenuStartAndRescue)
	}
	return h, ev
}

// TestEvents_RescueIsTwoStepsAndOneToken (EV-7, OpeningBuilder.BuildRescue): elegir «retomar»
// en la entrada NO rescata: abre la lista de qué retomar. El número siguiente retoma, con UN
// token para sus dos salientes (el resumen de lo que llevaba y la pantalla del flujo).
func TestEvents_RescueIsTwoStepsAndOneToken(t *testing.T) {
	h, ev := eventsRescueHarness(t, eventsWithCartLines())

	h.say("wa-2", "2")

	if texts := h.texts(); len(texts) != 2 || texts[1] != eventsRescueList {
		t.Fatalf("textos = %q, quería la lista de lo que se puede retomar:\n%q", texts, eventsRescueList)
	}
	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	if st, _ := h.state(); st.EventID != "" || st.FlowID != "" {
		t.Fatalf("estado = %+v, abrir la lista todavía no rescata nada", st)
	}

	h.say("wa-3", "1")

	eventsRequireRowOf(t, eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 1)[0], ev, 0)
	if st, _ := h.state(); st.EventID != ev.ID || st.OwnerEventID != ev.ID || st.FlowID != eventsCartFlow {
		t.Errorf("estado = %+v, quería la conversación de vuelta en el evento rescatado", st)
	}
	texts := h.texts()
	if len(texts) != 4 || !strings.Contains(texts[2], eventsLineLabel) || texts[3] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, quería el resumen de lo decidido y después la pantalla del flujo", texts)
	}
	if calls := h.limiter.Calls(); len(calls) != 3 {
		t.Errorf("tokens pedidos = %d, quería 3: uno por turno, y UNO solo para los dos salientes del rescate", len(calls))
	}
	if rows := h.events.Events(harnessTenant); len(rows) != 1 {
		t.Errorf("eventos = %+v, retomar no pare ninguno", rows)
	}
}

// TestEvents_ADiscardedOrderIsNotResurrectedByAStaleList (EV-7, INV-17): retomar relee los
// RESCATABLES, no los vivos. Un evento cuyo pedido se descartó entre que se pintó la lista y
// que el cliente contestó sigue open y aun así no se resucita.
func TestEvents_ADiscardedOrderIsNotResurrectedByAStaleList(t *testing.T) {
	h, ev := eventsRescueHarness(t)
	h.say("wa-2", "2")
	h.events.SetContent(ev.ID, eventshelpertest.ContentDiscarded)

	h.say("wa-3", "1")

	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	if st, _ := h.state(); st.EventID != "" || st.FlowID != "" {
		t.Errorf("estado = %+v, el evento descartado no debía retomarse", st)
	}
	if texts := h.texts(); len(texts) != 2 {
		t.Errorf("textos = %q, no debía enviarse nada al no poder retomar", texts)
	}
	const gone = "runtime: el evento elegido ya no se puede retomar; no se rescata"
	if !eventsLogged(h, "info", gone) {
		t.Errorf("no se anunció a Info que el evento ya no se puede retomar\nlog:\n%s", h.log.dump())
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen {
		t.Errorf("el evento quedó %q, nadie lo cierra por esto", row.Status)
	}
}

// TestEvents_EmptyRescueDoesNotConsumeTheTurn (EV-7, OpeningBuilder.BuildRescue): si al pedir
// la lista ya no queda nada que retomar, el turno NO se consume y el texto sigue su camino:
// acaba en el fallback, que vuelve a ofrecer lo que sí se puede hacer.
func TestEvents_EmptyRescueDoesNotConsumeTheTurn(t *testing.T) {
	h, ev := eventsRescueHarness(t)
	h.events.SetContent(ev.ID, eventshelpertest.ContentDiscarded)

	h.say("wa-2", "2")

	const nothingLeft = "runtime: no queda nada que retomar cuando el cliente lo pidió; el texto sigue su camino"
	if !eventsLogged(h, "info", nothingLeft) {
		t.Errorf("no se anunció a Info que no queda nada que retomar\nlog:\n%s", h.log.dump())
	}
	texts := h.texts()
	if len(texts) != 2 || texts[1] != eventsMenuStartOnly {
		t.Errorf("textos = %q, quería de nuevo la oferta, ya sin la entrada de retomar", texts)
	}
}

// TestEvents_RescueErrorRises (OpeningBuilder.BuildRescue): un error al armar la lista sube.
func TestEvents_RescueErrorRises(t *testing.T) {
	opening := &eventsOpening{
		offer: events.Offering{Text: "oferta-zzq", Menu: events.Menu{Options: []events.MenuOption{
			{Number: 1, Action: events.ActionRescue, Count: 1},
		}}},
		rescueErr: errEventsInjected,
	}
	h := eventsFallbackHarness(t, eventsWithOpening(opening))
	h.say("wa-1", "hola-zzq")
	if texts := h.texts(); len(texts) != 1 || texts[0] != "oferta-zzq" {
		t.Fatalf("textos = %q, quería el Text de la oferta tal cual", texts)
	}

	err := h.handle(h.incoming("wa-2", "1"))

	if !errors.Is(err, errEventsInjected) {
		t.Fatalf("HandleIncoming = %v, quería el error de BuildRescue", err)
	}
}
