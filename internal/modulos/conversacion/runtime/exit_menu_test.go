//go:build pendiente

package runtime_test

// exit_menu_test.go prueba, por HandleIncoming, el contrato de exit_menu.go (XM-1…XM-9): la
// lectura de la respuesta del cliente al menú de salida del reprompt acotado. El menú se deja
// ARMADO sembrando el estado (y una vez, de punta a punta, con tres entradas inválidas).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

const (
	exitMenuFlowID = "loop-zzq"
	exitMenuPrompt = "Menú en bucle-zzq"
	// exitMenuScreen es la pantalla que el módulo guardó al armar. Es DISTINTA del prompt del
	// nodo para distinguir «el runtime re-envió la pantalla» de «el módulo re-pintó su menú».
	exitMenuScreen = "Pantalla guardada al armar-zzq"
)

// exitMenuFlow es un menú cuyas tres opciones vuelven a él: 1, 2 y 3 son opciones LEGÍTIMAS
// del módulo, así que se ve quién se quedó con el número.
func exitMenuFlow() model.Flow {
	return model.Flow{
		FlowID:  exitMenuFlowID,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: exitMenuPrompt, Options: map[string]string{"1": "root", "2": "root", "3": "root"}},
		},
	}
}

// exitMenuDispatcher es un runtime.Dispatcher que ofrece siempre el mismo menú.
type exitMenuDispatcher struct{ menu events.Menu }

func (d exitMenuDispatcher) Build(context.Context, events.ConversationRef) (events.Menu, error) {
	return d.menu, nil
}

// exitMenuOffer es lo que enseña el despachador de estos tests.
func exitMenuOffer() events.Menu {
	return events.Menu{Options: []events.MenuOption{
		{Number: 1, Action: events.ActionStart, Kind: trigger.EventKindCart},
		{Number: 2, Action: events.ActionStart, Kind: trigger.EventKindSurvey},
	}}
}

// exitMenuHarness monta el guion: el flujo en bucle, el hilo abierto y el despachador fijo.
func exitMenuHarness(t *testing.T, extra ...runtime.Option) *harness {
	t.Helper()
	h := newHarness(t, withOptions(func(*harness) []runtime.Option {
		return append([]runtime.Option{runtime.WithDispatcher(exitMenuDispatcher{menu: exitMenuOffer()})}, extra...)
	}))
	h.seedFlow(exitMenuFlow())
	h.enableFeature(entitlements.FeatureLLMIntake)
	return h
}

// exitMenuArm deja una conversación viva en el menú con un evento `cart` activo y el menú de
// salida armado con esas Vars. Devuelve el evento.
func exitMenuArm(h *harness, vars map[string]any) events.Event {
	h.t.Helper()
	ev := h.seedEvent(trigger.EventKindCart, exitMenuFlowID, 1)
	exitMenuSave(h, ev.ID, vars)
	return ev
}

// exitMenuArmedOn son las Vars de un menú de salida armado y sellado con ese evento.
func exitMenuArmedOn(eventID string) map[string]any {
	return map[string]any{modules.ExitMenuVar: exitMenuScreen, modules.ExitMenuEventVar: eventID, "kept-zzq": "yes"}
}

// exitMenuSave guarda la conversación viva con ese evento activo (y dueño) y esas Vars.
func exitMenuSave(h *harness, eventID string, vars map[string]any) {
	h.t.Helper()
	st := model.Conversation{
		TenantID: harnessTenant, SessionID: harnessSession, ContactID: h.contactID(harnessPhone),
		FlowID: exitMenuFlowID, FlowVersion: 1, CurrentNode: "root",
		Vars: vars, EventID: eventID, OwnerEventID: eventID, LastWaMessageID: "wa-previous",
	}
	if err := h.repo.Save(h.t.Context(), st); err != nil {
		h.t.Fatalf("sembrar el menú de salida armado: %v", err)
	}
}

// exitMenuArmed dice si el estado guardado sigue llevando alguna de las dos claves de la marca.
func exitMenuArmed(h *harness) bool {
	st, _ := h.state()
	_, screen := st.Vars[modules.ExitMenuVar]
	_, seal := st.Vars[modules.ExitMenuEventVar]
	return screen || seal
}

// exitMenuStopNotice es la confirmación del event_stop para un `cart` (events.go, EV-5).
func exitMenuStopNotice() string {
	return fmt.Sprintf("Listo, dejamos el %s por ahora. Sigue abierto: puedes retomarlo cuando quieras.", events.KindName(trigger.EventKindCart))
}

// exitMenuStatus devuelve el estado del evento con ese id.
func exitMenuStatus(h *harness, eventID string) events.Status {
	for _, ev := range h.events.Events(harnessTenant) {
		if ev.ID == eventID {
			return ev.Status
		}
	}
	return ""
}

// assertExitMenuWentToTheModule: el número lo trató el MÓDULO (re-pintó su menú) y la marca
// ya no está.
func assertExitMenuWentToTheModule(t *testing.T, h *harness) {
	t.Helper()
	if got := h.texts(); !resumeEqual(got, []string{exitMenuPrompt}) {
		t.Errorf("textos = %q, quería el menú del módulo: el número era suyo", got)
	}
	if exitMenuArmed(h) {
		t.Error("el menú de salida sigue armado; tenía que desarmarse")
	}
	if got := h.flowEvents(runtime.EffectEventDeactivated); len(got) != 0 {
		t.Errorf("event_deactivated = %+v, el número no era del menú de salida", got)
	}
}

// XM-1 · Sin la marca no hace nada: el número es del módulo.
func TestExitMenu_NotArmedDoesNothing(t *testing.T) {
	h := exitMenuHarness(t)
	ev := exitMenuArm(h, map[string]any{"kept-zzq": "yes"})

	h.say("wa-1", "2")

	assertExitMenuWentToTheModule(t, h)
	if st, _ := h.state(); st.EventID != ev.ID {
		t.Errorf("event_id = %q, el evento activo no debía tocarse", st.EventID)
	}
}

// XM-1 · Sin plano de eventos la marca no se interpreta: el número es del módulo.
func TestExitMenu_WithoutEventPlaneDoesNothing(t *testing.T) {
	h := exitMenuHarness(t, runtime.WithEventStore(nil))
	exitMenuSave(h, "", exitMenuArmedOn(""))

	h.say("wa-1", "2")

	if got := h.texts(); !resumeEqual(got, []string{exitMenuPrompt}) {
		t.Errorf("textos = %q, quería el menú del módulo", got)
	}
}

// XM-2 · Con un evento activo DISTINTO del que armó el menú se desarma y el turno sigue hacia
// el módulo: el número no desactiva al evento que ahora manda.
func TestExitMenu_ArmedOnAnotherEventIsDisarmed(t *testing.T) {
	for _, reply := range []string{modules.ExitMenuKeepTrying, modules.ExitMenuStop, modules.ExitMenuDispatcher} {
		t.Run(reply, func(t *testing.T) {
			h := exitMenuHarness(t)
			ev := exitMenuArm(h, exitMenuArmedOn("another-event-zzq"))

			h.say("wa-1", reply)

			assertExitMenuWentToTheModule(t, h)
			if st, _ := h.state(); st.EventID != ev.ID {
				t.Errorf("event_id = %q, el evento activo %s no debía apagarse", st.EventID, ev.ID)
			}
		})
	}
}

// XM-2 / XM-3 · Sin evento activo NUNCA se interpreta 1/2/3, ni con un marcador sin sello
// (que da "" y casaría con «ningún evento»): solo se desarma.
func TestExitMenu_WithoutActiveEventIsOnlyDisarmed(t *testing.T) {
	marks := map[string]map[string]any{
		"legacy mark without seal": {modules.ExitMenuVar: exitMenuScreen},
		"mark sealed with empty":   exitMenuArmedOn(""),
		"mark sealed with a gone":  exitMenuArmedOn("gone-event-zzq"),
	}
	for name, vars := range marks {
		for _, reply := range []string{modules.ExitMenuKeepTrying, modules.ExitMenuStop, modules.ExitMenuDispatcher} {
			t.Run(name+"/"+reply, func(t *testing.T) {
				h := exitMenuHarness(t)
				exitMenuSave(h, "", modules.CloneVars(vars))

				h.say("wa-1", reply)

				assertExitMenuWentToTheModule(t, h)
				if got := h.events.Events(harnessTenant); len(got) != 0 {
					t.Errorf("eventos = %+v, sin evento activo la opción no abre el despachador", got)
				}
			})
		}
	}
}

// XM-4 · Una entrada que no es una de las tres opciones desarma y sigue hacia el módulo, que
// la trata como una entrada más.
func TestExitMenu_NonOptionDisarmsAndGoesToTheModule(t *testing.T) {
	h := exitMenuHarness(t)
	ev := exitMenuArm(h, nil)
	exitMenuSave(h, ev.ID, exitMenuArmedOn(ev.ID))

	h.say("wa-1", "zzz")

	if exitMenuArmed(h) {
		t.Error("el menú de salida sigue armado tras una entrada que no era opción")
	}
	texts := h.texts()
	if len(texts) != 1 || texts[0] == exitMenuScreen || texts[0] == exitMenuStopNotice() || !strings.Contains(texts[0], exitMenuPrompt) {
		t.Errorf("textos = %q, quería el reprompt del módulo sobre su menú", texts)
	}
	if st, _ := h.state(); st.EventID != ev.ID || st.LastWaMessageID != "wa-1" {
		t.Errorf("estado = %+v, quería el turno procesado por el módulo con el evento intacto", st)
	}
}

// XM-4 / XM-6 · La entrada se compara sin los espacios de los lados. «1» re-envía la pantalla
// guardada TAL CUAL, cobra un token y consume el turno; XM-5: al enviar, el estado ya está
// guardado sin la marca. Corre ANTES de la idempotencia consecutiva.
func TestExitMenu_KeepTryingResendsTheSavedScreen(t *testing.T) {
	h := exitMenuHarness(t)
	ev := exitMenuArm(h, nil)
	exitMenuSave(h, ev.ID, exitMenuArmedOn(ev.ID))
	armedAtSend := true
	h.sender.OnSend(func(runtimehelpertest.Send) { armedAtSend = exitMenuArmed(h) })

	h.say("wa-previous", "  1 ") // mismo wa_message_id que el último procesado

	if got := h.texts(); !resumeEqual(got, []string{exitMenuScreen}) {
		t.Errorf("textos = %q, quería la pantalla guardada al armar", got)
	}
	if armedAtSend {
		t.Error("al re-enviar la pantalla el estado guardado aún llevaba la marca (XM-5)")
	}
	if got := len(h.limiter.Calls()); got != 1 {
		t.Errorf("tokens cobrados = %d, quería 1", got)
	}
	st, _ := h.state()
	if st.EventID != ev.ID || st.CurrentNode != "root" || st.Vars["kept-zzq"] != "yes" {
		t.Errorf("estado = %+v, «seguir intentando» no abandona nada ni toca las demás Vars", st)
	}
	if got := resumeThread(h, ev.ID); len(got) != 0 {
		t.Errorf("hilo = %v, el número del menú de salida no entra al hilo (XM-9)", got)
	}
}

// XM-6 · Con el cupo agotado, o con la pantalla vacía, el turno se consume sin enviar.
func TestExitMenu_KeepTryingWithoutTokenOrScreenSendsNothing(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		h := exitMenuHarness(t)
		ev := exitMenuArm(h, nil)
		exitMenuSave(h, ev.ID, exitMenuArmedOn(ev.ID))
		h.limiter.Limit(0)

		h.say("wa-1", modules.ExitMenuKeepTrying)

		if len(h.texts()) != 0 || exitMenuArmed(h) {
			t.Errorf("textos = %q, armado = %v; quería el turno consumido, sin envío y desarmado", h.texts(), exitMenuArmed(h))
		}
		if got := h.blockedReasons(); !resumeEqual(got, []string{"rate_limit"}) {
			t.Errorf("motivos de corte = %v, quería [rate_limit]", got)
		}
	})
	t.Run("empty screen", func(t *testing.T) {
		h := exitMenuHarness(t)
		ev := exitMenuArm(h, nil)
		exitMenuSave(h, ev.ID, map[string]any{modules.ExitMenuVar: "", modules.ExitMenuEventVar: ev.ID})

		h.say("wa-1", modules.ExitMenuKeepTrying)

		if len(h.texts()) != 0 || len(h.limiter.Calls()) != 0 {
			t.Errorf("textos = %q, tokens = %v; sin pantalla no se envía ni se cobra", h.texts(), h.limiter.Calls())
		}
		if st, _ := h.state(); st.LastWaMessageID != "wa-previous" {
			t.Errorf("last_wa_message_id = %q, el turno se consume sin llegar al módulo", st.LastWaMessageID)
		}
	})
}

// XM-7 · «2» es exactamente el event_stop: el evento sigue open, se apaga el puntero activo,
// se emite event_deactivated y se confirma por nombre de tipo. XM-9: el número no va al hilo.
func TestExitMenu_StopDeactivatesTheEvent(t *testing.T) {
	h := exitMenuHarness(t)
	ev := exitMenuArm(h, nil)
	exitMenuSave(h, ev.ID, exitMenuArmedOn(ev.ID))

	h.say("wa-1", modules.ExitMenuStop)

	if got := h.texts(); !resumeEqual(got, []string{exitMenuStopNotice()}) {
		t.Errorf("textos = %q, quería la confirmación por nombre de tipo", got)
	}
	if text := strings.Join(h.texts(), " "); strings.Contains(text, ev.ID) || strings.Contains(text, ev.HistoryID) {
		t.Errorf("la confirmación lleva un identificador del evento: %q", text)
	}
	if got := exitMenuStatus(h, ev.ID); got != events.StatusOpen {
		t.Errorf("status = %q, «dejarlo» desactiva sin matar: quería open", got)
	}
	st, _ := h.state()
	if st.EventID != "" || st.OwnerEventID != ev.ID || st.FlowID != exitMenuFlowID || st.Vars["kept-zzq"] != "yes" {
		t.Errorf("estado = %+v, quería el activo apagado conservando dueño, flujo y Vars", st)
	}
	if exitMenuArmed(h) {
		t.Error("la marca sobrevivió al event_stop (XM-5)")
	}
	if got := h.flowEvents(runtime.EffectEventDeactivated); len(got) != 1 {
		t.Errorf("event_deactivated = %d filas, quería 1", len(got))
	}
	for _, entry := range h.thread(ev.ID) {
		if entry.Role == events.RoleClient {
			t.Errorf("hilo = %+v, el número del menú de salida no entra al hilo (XM-9)", h.thread(ev.ID))
		}
	}
}

// XM-8 · «3» salta al evento `menu` con el gesto «ve»: un `menu` vivo y VENCIDO no se cancela
// (solo «nuevo» puede), se conmuta hacia él y no nace ninguna fila.
func TestExitMenu_DispatcherGoesToTheMenuEvent(t *testing.T) {
	h := exitMenuHarness(t)
	h.seedSettings(func(s *store.TenantSettings) { s.EventInactivityTTL = time.Hour })
	stale := h.seedEvent(trigger.EventKindMenu, "", 0)
	h.clock.Advance(2 * time.Hour)
	cart := exitMenuArm(h, nil)
	exitMenuSave(h, cart.ID, exitMenuArmedOn(cart.ID))

	h.say("wa-1", modules.ExitMenuDispatcher)

	if got := exitMenuStatus(h, stale.ID); got != events.StatusOpen {
		t.Errorf("status del `menu` vencido = %q, «ver el menú» es IR, no empezar uno: quería open", got)
	}
	if got := h.events.Events(harnessTenant); len(got) != 2 {
		t.Errorf("eventos = %d, no debía nacer ninguna fila (quería el carrito y el menú)", len(got))
	}
	if st, _ := h.state(); st.EventID != stale.ID {
		t.Errorf("event_id = %q, quería el `menu` de siempre %q", st.EventID, stale.ID)
	}
	if got := exitMenuStatus(h, cart.ID); got != events.StatusOpen {
		t.Errorf("status del carrito = %q, quería open", got)
	}
	texts := h.texts()
	if len(texts) == 0 || texts[len(texts)-1] != exitMenuOffer().Render() {
		t.Errorf("textos = %q, quería terminar con la lista del despachador", texts)
	}
	for _, id := range []string{cart.ID, stale.ID} {
		for _, entry := range h.thread(id) {
			if entry.Role == events.RoleClient {
				t.Errorf("hilo de %s = %+v, el número del menú de salida no entra al hilo (XM-9)", id, h.thread(id))
			}
		}
	}
}

// XM-8 · Sin ningún `menu` vivo, «3» lo hace nacer y lo deja activo.
func TestExitMenu_DispatcherBirthsTheMenuEvent(t *testing.T) {
	h := exitMenuHarness(t)
	cart := exitMenuArm(h, nil)
	exitMenuSave(h, cart.ID, exitMenuArmedOn(cart.ID))

	h.say("wa-1", modules.ExitMenuDispatcher)

	var menuID string
	for _, ev := range h.events.Events(harnessTenant) {
		if ev.Kind == trigger.EventKindMenu && ev.Alive() {
			menuID = ev.ID
		}
	}
	if st, _ := h.state(); menuID == "" || st.EventID != menuID {
		t.Errorf("event_id = %q, quería el `menu` recién nacido %q", st.EventID, menuID)
	}
	if exitMenuArmed(h) {
		t.Error("la marca sobrevivió al salto al despachador (XM-5)")
	}
}

// «Dónde corre» · El salto por tipo va ANTES: una palabra de navegación del tenant dicha ante
// el menú de salida sigue siendo navegación.
func TestExitMenu_NavigationWordWinsOverTheExitMenu(t *testing.T) {
	h := exitMenuHarness(t)
	h.seedRule(trigger.Rule{
		Kind: trigger.KindEventStart, Keyword: "3", MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindSurvey, FlowID: exitMenuFlowID,
	})
	cart := exitMenuArm(h, nil)
	exitMenuSave(h, cart.ID, exitMenuArmedOn(cart.ID))

	h.say("wa-1", "3")

	for _, ev := range h.events.Events(harnessTenant) {
		if ev.Kind == trigger.EventKindMenu {
			t.Errorf("nació un evento `menu`: el menú de salida se adelantó al salto por tipo")
		}
	}
	var survey events.Event
	for _, ev := range h.events.Events(harnessTenant) {
		if ev.Kind == trigger.EventKindSurvey {
			survey = ev
		}
	}
	if st, _ := h.state(); survey.ID == "" || st.EventID != survey.ID {
		t.Errorf("event_id = %q, quería la encuesta nacida por la palabra del tenant (%q)", st.EventID, survey.ID)
	}
}

// exitMenuStore envuelve el almacén del guion para hacer fallar Save a voluntad.
type exitMenuStore struct {
	*store.MemoryRepository
	failSave *atomic.Bool
}

func (s exitMenuStore) Save(ctx context.Context, st model.Conversation) error {
	if s.failSave.Load() {
		return errResumeCause
	}
	return s.MemoryRepository.Save(ctx, st)
}

// XM-5 · Si el Save que borra la marca falla: error envuelto y el turno se corta sin actuar.
func TestExitMenu_DisarmSaveFailureCutsTheTurn(t *testing.T) {
	h := exitMenuHarness(t)
	failSave := &atomic.Bool{}
	rt := runtime.New(exitMenuStore{MemoryRepository: h.repo, failSave: failSave}, h.engine, h.sender, h.tenants, h.contacts, h.log, h.runtimeOptions()...)
	ev := exitMenuArm(h, nil)
	exitMenuSave(h, ev.ID, exitMenuArmedOn(ev.ID))
	failSave.Store(true)

	err := rt.HandleIncoming(t.Context(), harnessSession, h.incoming("wa-1", modules.ExitMenuStop))

	if err == nil || !strings.Contains(err.Error(), "runtime: desarmar el menú de salida: ") || !errors.Is(err, errResumeCause) {
		t.Errorf("error = %v, quería «runtime: desarmar el menú de salida: …» envolviendo la causa", err)
	}
	if got := h.sender.Attempts(); len(got) != 0 {
		t.Errorf("intentos de envío = %+v, el turno se corta antes de actuar", got)
	}
	if st, _ := h.state(); st.EventID != ev.ID {
		t.Errorf("event_id = %q, el evento no debía desactivarse", st.EventID)
	}
}

// De punta a punta: dentro de un evento, tres entradas inválidas hacen que el MÓDULO arme el
// menú de salida, y el «2» siguiente lo lee el runtime (no el módulo) y desactiva el evento.
func TestExitMenu_ThreeInvalidInputsArmItAndTwoDeactivates(t *testing.T) {
	h := exitMenuHarness(t)
	h.seedRule(trigger.Rule{
		Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindCart, FlowID: exitMenuFlowID,
	})
	h.say("wa-0", "carrito")
	for i := range modules.MaxReprompts {
		h.say(fmt.Sprintf("wa-invalid-%d", i), "zzz")
	}
	texts := h.texts()
	if texts[len(texts)-1] != modules.ExitMenuText(exitMenuPrompt) || !exitMenuArmed(h) {
		t.Fatalf("tras %d inválidos el cliente leyó %q y armado = %v; quería el menú de salida armado", modules.MaxReprompts, texts[len(texts)-1], exitMenuArmed(h))
	}
	before := len(texts)

	h.say("wa-2", modules.ExitMenuStop)

	if got := h.texts()[before:]; !resumeEqual(got, []string{exitMenuStopNotice()}) {
		t.Errorf("textos = %q, quería solo la confirmación del event_stop", got)
	}
	if st, _ := h.state(); st.EventID != "" {
		t.Errorf("event_id = %q, el «2» tenía que apagar el evento activo", st.EventID)
	}
}
