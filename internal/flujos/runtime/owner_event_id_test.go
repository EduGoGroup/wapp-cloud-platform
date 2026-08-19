package runtime

// owner_event_id_test.go — Plan 053 · T1.5: el flow_state deja de tener UN puntero y
// pasa a tener DOS. `event_id` contesta A QUIÉN LE HABLA el contacto ahora;
// `owner_event_id` contesta DE QUIÉN ES el flujo guardado en esa misma fila. Este
// fichero fija las CUATRO escenas en las que esa distinción se nota, y solo esas (la
// cuarta —el flujo HEREDADO bajo un evento sin flujo propio— llegó con la revisión
// adversarial de la Ola 1: ver TestPointStateAtEvent_SobreFlujoHeredado_NoEstampaDueno).
//
// ⚠️ `package runtime` (INTERNO), no `runtime_test`: pointStateAtEvent y saveMenuState
// son MINÚSCULAS y un paquete externo no ve símbolos no exportados. Es el mismo motivo
// —y el mismo precedente— que exit_menu_test.go documenta en su encabezado, y por el
// que este fichero NO puede reusar newEventRuntime/memEventStore de event_switch_test.go
// (viven en `runtime_test`, que IMPORTA `runtime`: importarlo de vuelta sería un ciclo).
// Los dobles se reutilizan TAL CUAL de exit_menu_test.go —newT52Env, t52StubEvents.seed,
// t52FakeSender— sin fabricar ninguno nuevo: ese entorno ya trae el plano de eventos
// cableado, un contacto resuelto y el repositorio EN MEMORIA, que es todo lo que estas
// pruebas necesitan (cero Postgres).
//
// Fichero NUEVO y no un bloque dentro de events-algo: lo que se prueba no es «el menú»
// ni «la apertura de evento» por separado, sino la RELACIÓN entre los dos punteros
// cuando esos dos caminos se cruzan. Tenerlo junto es lo que hace legible el contraste
// —y lo que impide que alguien «unifique» los dos campos otra vez sin ver el test que
// lo prohíbe—.

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

const (
	t15CartFlow = "flujo-carrito"
	t15CartNode = "elegir-articulo"
	t15MenuRaw  = `{"options":[{"number":1,"action":"start","kind":"cart"}]}`
)

// t15SembrarCart deja el escenario de partida: un evento `cart` VIVO y el flow_state
// tal y como lo acaba de escribir startLocked — con flujo, sin terminar y con LOS DOS
// punteros en blanco. Que nazcan en blanco no es un detalle del doble: startLocked
// construye un model.Conversation fresco (start.go) y el puntero se estampa DESPUÉS,
// en pointStateAtEvent. Si esta precondición dejara de ser cierta, el test 1 dejaría de
// demostrar nada, así que se comprueba en vez de suponerse.
func t15SembrarCart(t *testing.T, e *t52Env) events.Event {
	t.Helper()
	cart := e.evs.seed(events.Event{
		ID: "t15-ev-cart", TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		Kind: "cart", HistoryID: "cart-t15-history-id", Status: events.StatusOpen,
		FlowID: t15CartFlow, FlowVersion: 3,
	})
	st := model.Conversation{
		TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		FlowID: t15CartFlow, FlowVersion: 3, CurrentNode: t15CartNode,
		Vars: map[string]any{"articulo": "arepas"},
	}
	if err := e.rt.store.Save(context.Background(), st); err != nil {
		t.Fatalf("sembrar el flow_state del cart: %v", err)
	}
	// Se relee: la precondición que importa no es la del struct en mano sino la de lo
	// PERSISTIDO, y de paso fija que el repositorio en memoria no se inventa punteros
	// (el gemelo de la columna owner_event_id que añadió T1.4).
	sembrado := t15Persistido(t, e)
	if sembrado.EventID != "" || sembrado.OwnerEventID != "" {
		t.Fatalf("precondición: el estado recién arrancado no tiene punteros todavía; event_id=%q owner_event_id=%q",
			sembrado.EventID, sembrado.OwnerEventID)
	}
	return cart
}

// t15SembrarMenu crea el evento `menu` (sin flujo propio: D-043.3, el menú no es una
// fila de flow_definitions).
func t15SembrarMenu(t *testing.T, e *t52Env) events.Event {
	t.Helper()
	return e.evs.seed(events.Event{
		ID: "t15-ev-menu", TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		Kind: "menu", HistoryID: "menu-t15-history-id", Status: events.StatusOpen,
	})
}

// t15SembrarSurvey crea el evento `survey` del escenario de la TERCERA SALIDA de
// enterEventFlow: un evento de tipo normal (no `menu`) que NO trae flujo. La fila del
// evento se siembra con FlowID vacío a propósito — es lo que produce una regla
// {kind: event_start, event_kind: "survey", flow_id: ""}, que admin/triggers.go acepta
// con 200 porque KindEventStart no declara needsFlowID, y también lo que devuelve
// flowForKind (sin error) cuando no hay regla para ese tipo.
func t15SembrarSurvey(t *testing.T, e *t52Env) events.Event {
	t.Helper()
	return e.evs.seed(events.Event{
		ID: "t15-ev-survey", TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		Kind: "survey", HistoryID: "survey-t15-history-id", Status: events.StatusOpen,
	})
}

// t15Persistido relee el flow_state POR EL REPOSITORIO (no del struct en mano): lo que
// importa es lo que quedó escrito, que es lo único que verá el turno siguiente.
func t15Persistido(t *testing.T, e *t52Env) model.Conversation {
	t.Helper()
	st, ok, err := e.repo.Load(context.Background(), e.key)
	if err != nil || !ok {
		t.Fatalf("releer el flow_state: ok=%v err=%v", ok, err)
	}
	return st
}

// TestPointStateAtEvent_EstampaDuenoIgualQueActivo (REQ-053.1): abrir un evento con
// flujo deja los DOS punteros apuntando a él.
//
// Aquí coincidir es lo correcto y es lo que hay que fijar: se llega a pointStateAtEvent
// justo después de que enterEventFlow borrara el estado previo y startLocked escribiera
// uno nuevo, así que el flujo que vive en la fila acaba de nacer PARA este evento. El
// caso interesante —cuando dejan de coincidir— es el test de abajo; sin este, aquel no
// tendría contra qué contrastar.
func TestPointStateAtEvent_EstampaDuenoIgualQueActivo(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	cart := t15SembrarCart(t, e)

	// `true` = el camino de enterEventFlow con flowID != "": Delete + startLocked, o
	// sea el flujo de la fila acaba de nacer PARA el cart. Es lo que autoriza a
	// estampar el dueño (ver pointStateAtEvent).
	if err := e.rt.pointStateAtEvent(context.Background(), e.key, cart.ID, true); err != nil {
		t.Fatalf("pointStateAtEvent: %v", err)
	}

	st := t15Persistido(t, e)
	if st.EventID != cart.ID {
		t.Fatalf("event_id = %q, quiero el del cart (%q)", st.EventID, cart.ID)
	}
	if st.OwnerEventID != cart.ID {
		t.Fatalf("owner_event_id = %q, quiero el del cart (%q): el flujo que corre en esta fila es SUYO",
			st.OwnerEventID, cart.ID)
	}
	// Y el flujo sigue donde estaba: estampar punteros no avanza ni reinicia nada.
	if st.FlowID != t15CartFlow || st.CurrentNode != t15CartNode {
		t.Fatalf("el flujo no debe moverse al estampar los punteros; flow_id=%q current_node=%q",
			st.FlowID, st.CurrentNode)
	}
}

// TestPointStateAtEvent_SobreFlujoHeredado_NoEstampaDueno — el GEMELO NEGATIVO del test
// de arriba, y el que impide que la retirada de la guarda de posesión (T1.6) deje el
// motor peor de como estaba.
//
// Escena, alcanzable con config de PRODUCCIÓN: la clienta está a medio `cart` C (flujo
// F, fila {flow=F, event=C, owner=C}) y llega un `survey` S declarado SIN flujo. Ese
// evento entra por la TERCERA SALIDA de enterEventFlow (events.go:543-567): no es
// `menu`, así que no se va por presentMenu, y su flowID es "", así que NO hay Delete ni
// startLocked. Se cae hasta pointStateAtEvent con el flow_state del CARRITO intacto
// debajo.
//
// Lo que se fija: el ACTIVO pasa al survey (es a quien le habla ahora) y el DUEÑO SE
// QUEDA EN EL CARRITO. Si el dueño se estampara aquí, cuando el flujo F del carrito
// alcanzara su nodo terminal closeIfFinished cerraría el SURVEY —el evento equivocado—
// y el carrito se quedaría `open` para siempre con su intake colgando. Es literalmente
// el hallazgo H2 resucitado por la puerta de atrás, y con la guarda de posesión ya
// retirada nadie más lo pararía.
//
// ✅ Este test SÍ ejercita el camino real: llama a enterEventFlow, no a
// pointStateAtEvent. Es viable con el arnés de newT52Env porque las dos piezas que ese
// camino toca antes de llegar al estampado son nil-safe con este entorno —Load va al
// repositorio en memoria y summarizeAbandoned se corta sola en su guarda
// `rt.sources.Lines == nil && rt.sources.Answers == nil`—, así que no hace falta
// simular el resto del motor.
func TestPointStateAtEvent_SobreFlujoHeredado_NoEstampaDueno(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	ctx := context.Background()
	cart := t15SembrarCart(t, e)

	// (1) El carrito se abre por el camino CON flujo: los dos punteros quedan en él.
	if err := e.rt.pointStateAtEvent(ctx, e.key, cart.ID, true); err != nil {
		t.Fatalf("pointStateAtEvent (cart): %v", err)
	}
	if st := t15Persistido(t, e); st.EventID != cart.ID || st.OwnerEventID != cart.ID {
		t.Fatalf("precondición: los dos punteros deben quedar en el cart; event_id=%q owner_event_id=%q",
			st.EventID, st.OwnerEventID)
	}

	// (2) Entra el `survey` SIN flujo — la tercera salida, sin Delete ni startLocked.
	survey := t15SembrarSurvey(t, e)
	if err := e.rt.enterEventFlow(ctx, e.key, t52Session, survey.ID, survey.Kind, "", nil, "", ""); err != nil {
		t.Fatalf("enterEventFlow (survey sin flujo): %v", err)
	}

	st := t15Persistido(t, e)
	if st.EventID != survey.ID {
		t.Fatalf("event_id = %q, quiero el del SURVEY (%q): es a quien le habla ahora", st.EventID, survey.ID)
	}
	if st.OwnerEventID != cart.ID {
		t.Fatalf("owner_event_id = %q, quiero el del CART (%q) SIN CAMBIAR: el flujo de esta fila lo arrancó el carrito, no el survey — estamparlo aquí cerraría el evento equivocado al terminar F",
			st.OwnerEventID, cart.ID)
	}
	// La herencia es literal: sin Delete ni startLocked, el flujo del carrito sigue
	// entero bajo el survey. Si esto se rompiera, el assert de arriba pasaría por el
	// motivo equivocado (no habría flujo ajeno del que ser dueño).
	if st.FlowID != t15CartFlow || st.CurrentNode != t15CartNode {
		t.Fatalf("el survey sin flujo debe HEREDAR el flujo vivo, no reiniciarlo; flow_id=%q current_node=%q",
			st.FlowID, st.CurrentNode)
	}
	if st.Vars["articulo"] != "arepas" {
		t.Fatalf("las Vars del flujo heredado deben sobrevivir; vars=%v", st.Vars)
	}
	// Y el carrito sigue VIVO: nada de lo anterior lo cerró de tapadillo.
	if got := e.evs.statusOf(cart.ID); got != events.StatusOpen {
		t.Fatalf("el cart debe seguir open; status=%q", got)
	}
}

// TestSaveMenuState_HeredaEstadoVivo_ConservaDueno (REQ-053.2, INV-053.2) — EL CORAZÓN
// DEL PLAN.
//
// Escena: la clienta está a medio carrito y pide el menú. saveMenuState NO borra el
// flow_state (es el único camino que estampa el puntero sin pasar por pointStateAtEvent,
// y lo hace a propósito para no tirar un carrito a medias), así que la fila queda con el
// flujo DEL CARRITO y el evento activo DEL MENÚ. Con un solo puntero esa situación era
// INDESCRIPTIBLE: `event_id` tenía que elegir entre las dos respuestas, elegía «menú», y
// el hallazgo H2 solo podía taparse con una heurística (comparar ev.FlowID con st.FlowID
// en pendingClosure). Con dos punteros se puede AFIRMAR, que es lo que hace este test:
// activo = menú, dueño = carrito, a la vez y sin ambigüedad.
//
// El dueño lo estampa el CÓDIGO DE PRODUCCIÓN (pointStateAtEvent, el paso 1), no la
// mano del test: lo que se mide es que saveMenuState lo CONSERVE, y sembrarlo a mano
// dejaría fuera la mitad interesante.
func TestSaveMenuState_HeredaEstadoVivo_ConservaDueno(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	ctx := context.Background()
	cart := t15SembrarCart(t, e)

	// (1) Se abre el carrito: los dos punteros quedan en él. El `true` es el camino
	// con flujo propio (Delete + startLocked), el único que autoriza el dueño.
	if err := e.rt.pointStateAtEvent(ctx, e.key, cart.ID, true); err != nil {
		t.Fatalf("pointStateAtEvent (cart): %v", err)
	}
	if st := t15Persistido(t, e); st.Finished() {
		t.Fatalf("precondición: el flujo del cart debe seguir EN CURSO; current_node=%q", st.CurrentNode)
	}

	// (2) El `menu` se monta ENCIMA, sobre un flow_state vivo que NO se resetea.
	men := t15SembrarMenu(t, e)
	if err := e.rt.saveMenuState(ctx, e.key, men.ID, t15MenuRaw); err != nil {
		t.Fatalf("saveMenuState: %v", err)
	}

	st := t15Persistido(t, e)
	if st.EventID != men.ID {
		t.Fatalf("event_id = %q, quiero el del MENÚ (%q): es a quien le habla ahora", st.EventID, men.ID)
	}
	if st.OwnerEventID != cart.ID {
		t.Fatalf("owner_event_id = %q, quiero el del CART (%q) SIN CAMBIAR: el flujo de esta fila sigue siendo del carrito, no del menú",
			st.OwnerEventID, cart.ID)
	}
	// La herencia es literal: el flujo del carrito sigue entero bajo el menú. Si esto
	// se rompiera, el assert de arriba pasaría por el motivo equivocado (no habría
	// flujo ajeno del que ser dueño).
	if st.FlowID != t15CartFlow || st.CurrentNode != t15CartNode {
		t.Fatalf("el menú debe HEREDAR el flujo vivo, no reiniciarlo; flow_id=%q current_node=%q",
			st.FlowID, st.CurrentNode)
	}
	if st.Vars["articulo"] != "arepas" {
		t.Fatalf("las Vars del flujo heredado deben sobrevivir; vars=%v", st.Vars)
	}
	// Y el menú deja sus dos claves, sellado con SU evento (E-2, Ola 6). El `ok` del
	// aserto se comprueba y no se descarta con blank porque este repo activa
	// errcheck.check-type-assertions (.golangci.yml).
	raw, esTexto := st.Vars[varPendingMenu].(string)
	if !esTexto || raw != t15MenuRaw {
		t.Fatalf("%s = %#v, quiero el menú renderizado", varPendingMenu, st.Vars[varPendingMenu])
	}
	sello, sellado := st.Vars[varPendingMenuEventID].(string)
	if !sellado || sello != men.ID {
		t.Fatalf("%s = %#v, quiero el evento del menú (%q)", varPendingMenuEventID, st.Vars[varPendingMenuEventID], men.ID)
	}
}

// TestSaveMenuState_SobreEstadoTerminal_ReseteaYDuenoQuedaVacio: la OTRA rama de
// saveMenuState (`!ok || st.Finished()`), la que reconstruye la Conversation desde cero
// conservando solo LastWaMessageID.
//
// Aquí el dueño queda "" y es CORRECTO, no un olvido: el flujo que había terminó, la
// fila nueva no tiene ninguno, y un `menu` no tiene flujo propio del que ser dueño
// (D-043.3). Se fija con un assert en vez de dejarlo implícito porque "" es justamente
// el valor que un backfill descuidado o un ON CONFLICT mal escrito producirían por
// accidente en la rama de ARRIBA: si algún día este test es el único que pasa, el que
// falla dice exactamente qué se rompió.
func TestSaveMenuState_SobreEstadoTerminal_ReseteaYDuenoQuedaVacio(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	ctx := context.Background()
	cart := t15SembrarCart(t, e)

	// El flujo del carrito TERMINÓ, con los dos punteros puestos en él y la dedupe del
	// entrante escrita.
	st := model.Conversation{
		TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		FlowID: t15CartFlow, FlowVersion: 3, CurrentNode: model.NodeTerminal,
		Vars: map[string]any{"articulo": "arepas"}, LastWaMessageID: "wamid-t15-dedupe",
		EventID: cart.ID, OwnerEventID: cart.ID,
	}
	if !st.Finished() {
		t.Fatal("precondición: el estado sembrado debe ser TERMINAL")
	}
	if err := e.rt.store.Save(ctx, st); err != nil {
		t.Fatalf("sembrar el flow_state terminal: %v", err)
	}

	men := t15SembrarMenu(t, e)
	if err := e.rt.saveMenuState(ctx, e.key, men.ID, t15MenuRaw); err != nil {
		t.Fatalf("saveMenuState: %v", err)
	}

	got := t15Persistido(t, e)
	if got.EventID != men.ID {
		t.Fatalf("event_id = %q, quiero el del menú (%q)", got.EventID, men.ID)
	}
	if got.OwnerEventID != "" {
		t.Fatalf("owner_event_id = %q, quiero \"\": la Conversation se reconstruyó desde cero y un menú puro no tiene flujo del que ser dueño (D-043.3)",
			got.OwnerEventID)
	}
	// El reset es de VERDAD: el flujo acabado no se hereda (si se heredara, el cierre
	// natural mataría el evento del MENÚ creyendo que ese flujo terminado era suyo —
	// el `event_closed` falso que documenta saveMenuState).
	if got.FlowID != "" || got.CurrentNode != "" {
		t.Fatalf("el estado terminal NO debe heredarse; flow_id=%q current_node=%q", got.FlowID, got.CurrentNode)
	}
	if _, quedó := got.Vars["articulo"]; quedó {
		t.Fatalf("las Vars del flujo acabado no deben sobrevivir al reset; vars=%v", got.Vars)
	}
	// Lo ÚNICO que cruza el reset (y debe cruzarlo): la dedupe del entrante.
	if got.LastWaMessageID != "wamid-t15-dedupe" {
		t.Fatalf("LastWaMessageID = %q: la dedupe del entrante no pertenece al flujo que acabó y debe conservarse",
			got.LastWaMessageID)
	}
}
