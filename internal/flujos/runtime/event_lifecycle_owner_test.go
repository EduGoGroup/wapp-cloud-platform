package runtime

// event_lifecycle_owner_test.go — Plan 053 · Ola 1 · T1.6: el cierre natural
// transiciona el evento DUEÑO (flow_state.owner_event_id) y NUNCA el ACTIVO
// (flow_state.event_id). Cierra REQ-053.1 e INV-053.5, fija los huecos C y D del
// paso 0 (la limpieza de los dos punteros) y MATERIALIZA el test de mutación de la
// guarda de posesión de H2 que D-053.2 retira.
//
// ⚠️ `package runtime` (interno) y no `runtime_test`, por la MISMA razón que
// exit_menu_test.go documenta en su cabecera: closeIfFinished y pendingClosure son
// minúsculas a propósito y un paquete EXTERNO no puede llamarlas. Los tres casos de
// REQ-053.1 exigen construir el flow_state A MANO —el caso «dueño vacío con flujo
// terminal» es una fila LEGADA anterior al backfill de T1.3, que ningún camino de
// producción sabe fabricar— así que atacarlos por HandleIncoming sería probar otra
// cosa. La cobertura por el camino de producción la dan los tests externos que ya
// existen (event_lifecycle_test.go, flujo_ajeno_terminal_test.go).
//
// Reusa los dobles t52* de exit_menu_test.go (mismo paquete) en vez de duplicarlos.
// Lo único propio es el ESPÍA de TransitionEvent —ningún doble de este directorio
// registra a QUÉ id se le pidió la transición, que es justo la pregunta de T1.6— y
// el sink que captura los efectos de ciclo de vida.

import (
	"context"
	"errors"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// --- dobles propios de T1.6 ---------------------------------------------------

// t16Transicion es UNA llamada a TransitionEvent tal y como llegó: el id que se pidió
// cerrar y a dónde. El id es el entregable de esta tarea, así que se guarda crudo.
type t16Transicion struct {
	eventID string
	to      events.Status
}

// t16Espia envuelve al doble mínimo del plano de eventos para REGISTRAR a qué id se
// le pidió cada transición (y, opcionalmente, fabricar un fallo REAL de la misma
// forma que fallaTransicion en el paquete externo). Todo lo demás delega.
type t16Espia struct {
	*t52StubEvents
	espiaMu sync.Mutex
	vistas  []t16Transicion
	err     error
}

func (e *t16Espia) TransitionEvent(ctx context.Context, eventID string, to events.Status) error {
	e.espiaMu.Lock()
	e.vistas = append(e.vistas, t16Transicion{eventID: eventID, to: to})
	fallo := e.err
	e.espiaMu.Unlock()
	if fallo != nil {
		return fallo
	}
	return e.t52StubEvents.TransitionEvent(ctx, eventID, to)
}

func (e *t16Espia) transiciones() []t16Transicion {
	e.espiaMu.Lock()
	defer e.espiaMu.Unlock()
	out := make([]t16Transicion, len(e.vistas))
	copy(out, e.vistas)
	return out
}

func (e *t16Espia) falla(err error) {
	e.espiaMu.Lock()
	defer e.espiaMu.Unlock()
	e.err = err
}

// t16Efecto es lo que un efecto de ciclo de vida DICE de sí mismo: su nombre, sobre
// qué evento se emitió (EffectContext.EventID) y de qué tipo era ese evento. Los tres
// juntos son la prueba de que la telemetría habla del evento que MURIÓ y no del que
// seguía delante.
type t16Efecto struct {
	nombre  string
	eventID string
	kind    string
}

type t16Sink struct {
	mu     sync.Mutex
	vistos []t16Efecto
}

func (s *t16Sink) Handle(_ context.Context, ec EffectContext, eff modules.Effect) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// El aserto comprueba `ok` en vez de descartarlo con blank porque este repo activa
	// errcheck.check-type-assertions (.golangci.yml), igual que menuChoice en events.go.
	kind, ok := eff.Payload["kind"].(string)
	if !ok {
		kind = ""
	}
	s.vistos = append(s.vistos, t16Efecto{nombre: eff.Name, eventID: ec.EventID, kind: kind})
	return nil
}

func (s *t16Sink) efectos() []t16Efecto {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]t16Efecto, len(s.vistos))
	copy(out, s.vistos)
	return out
}

// --- montaje ------------------------------------------------------------------

const t16FlujoCart = "t16-flujo-cart"

type t16Env struct {
	rt         *Runtime
	rtSinPlano *Runtime // el MISMO montaje sin WithEventStore (rt.events == nil)
	evs        *t16Espia
	sink       *t16Sink
	contactID  string
	key        store.Key
}

func newT16Env(t *testing.T) *t16Env {
	t.Helper()
	repo := store.NewMemoryRepository()
	contacts := contact.NewMemoryResolver(repo)
	ref, err := contact.NewRef(contact.KindPhoneE164, "573001112233")
	if err != nil {
		t.Fatalf("NewRef: %v", err)
	}
	contactID, err := contacts.Resolve(context.Background(), t52Tenant, []contact.Ref{ref}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	evs := &t16Espia{t52StubEvents: newT52StubEvents()}
	sink := &t16Sink{}
	eng := engine.New(modules.NewRegistry())
	rt := New(repo, eng, &t52FakeSender{}, t52FakeResolver{}, contacts, t52DiscardLogger(),
		WithEventStore(evs), WithEventSink(sink))
	sinPlano := New(repo, eng, &t52FakeSender{}, t52FakeResolver{}, contacts, t52DiscardLogger())
	return &t16Env{
		rt: rt, rtSinPlano: sinPlano, evs: evs, sink: sink, contactID: contactID,
		key: store.Key{TenantID: t52Tenant, SessionID: t52Session, ContactID: contactID},
	}
}

// evento siembra una fila VIVA. flowID es el flujo CONGELADO en el evento (para el
// tipo `menu` vale "" — D-043.3, el menú no es una fila de flow_definitions).
func (e *t16Env) evento(id, kind, flowID string) events.Event {
	return e.evs.seed(events.Event{
		ID: id, TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		Kind: kind, HistoryID: kind + "-" + id, Status: events.StatusOpen, FlowID: flowID,
	})
}

// terminal arma el flow_state que closeIfFinished se encuentra: CurrentNode en el
// centinela (⇒ Finished()) con los dos punteros puestos a lo que pida cada caso.
func (e *t16Env) terminal(flowID, activo, dueno string) model.Conversation {
	return model.Conversation{
		TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		FlowID: flowID, FlowVersion: 1, CurrentNode: model.NodeTerminal,
		LastWaMessageID: "t16-turno-anterior",
		Vars:            map[string]any{},
		EventID:         activo, OwnerEventID: dueno,
	}
}

func t16Incoming(id, text string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{From: "573001112233", Text: text, WaMessageId: id}
}

// --- REQ-053.1: los TRES casos ------------------------------------------------

// TestCloseIfFinished_TransicionaAlDueno_NoAlActivo es REQ-053.1 entero.
//
// Los tres casos NO son variaciones del mismo: el primero fija la NO-REGRESIÓN (con
// un solo evento de por medio, T1.6 no debe notarse), el segundo fija la corrección
// (y de paso el gap que la Ola 6 dejó consciente: el `cart` heredado, que hasta hoy
// se quedaba `open` para siempre, se cierra), y el tercero fija que un dueño VACÍO no
// autoriza a cerrar NADA — la fila legada anterior al backfill de T1.3 y el `menu`
// puro de D-043.3 pasan de largo sin tocar ni una columna.
// Los tres casos van en funciones propias y no inline en los t.Run por gocyclo
// (min-complexity 15, .golangci.yml, con run.tests activado): gocyclo suma los
// literales de función al bloque que los contiene. Mismo motivo que mudoH2Env declara
// en flujo_ajeno_terminal_test.go.
func TestCloseIfFinished_TransicionaAlDueno_NoAlActivo(t *testing.T) {
	t.Run("dueño == activo: idéntico a hoy, los DOS punteros a vacío", t16CasoDuenoIgualQueActivo)
	t.Run("dueño != activo: cierra el DUEÑO y el activo sobrevive intacto", t16CasoDuenoDistintoDelActivo)
	t.Run("dueño vacío: no transiciona ni limpia nada", t16CasoSinDueno)
}

// t16CasoDuenoIgualQueActivo — REQ-053.1, cláusula 2: mientras los dos punteros
// coincidan, T1.6 no debe notarse. Mismo destino, mismo efecto, y los DOS punteros
// apagados como siempre.
func t16CasoDuenoIgualQueActivo(t *testing.T) {
	e := newT16Env(t)
	ev := e.evento("t16-cart", "cart", t16FlujoCart)
	st := e.terminal(t16FlujoCart, ev.ID, ev.ID)

	e.rt.closeIfFinished(context.Background(), &st)

	trs := e.evs.transiciones()
	if len(trs) != 1 || trs[0].eventID != ev.ID || trs[0].to != events.StatusClosed {
		t.Fatalf("debe transicionar EL evento (%q) a %q y nada más; se pidió %+v", ev.ID, events.StatusClosed, trs)
	}
	if got := e.evs.statusOf(ev.ID); got != events.StatusClosed {
		t.Fatalf("la fila debe quedar %q y quedó %q", events.StatusClosed, got)
	}
	// Los DOS punteros: el dueño por el hueco C, el activo porque ERA el mismo (hueco
	// D). Si alguno sobrevive, la conversación queda apuntando a un muerto.
	if st.OwnerEventID != "" || st.EventID != "" {
		t.Fatalf("con dueño == activo los DOS punteros se apagan; owner=%q active=%q",
			st.OwnerEventID, st.EventID)
	}
	efs := e.sink.efectos()
	if len(efs) != 1 || efs[0].nombre != EffectEventClosed || efs[0].eventID != ev.ID {
		t.Fatalf("debe emitirse UN %q sobre el evento que murió (%q); llegó %+v",
			EffectEventClosed, ev.ID, efs)
	}
}

// t16CasoDuenoDistintoDelActivo — REQ-053.1, cláusula 1 (el corazón de T1.6) más los
// huecos C y D: el escenario de #22 / H2, con el `menu` (sin flujo propio, D-043.3)
// montado sobre el flow_state de un `cart` que sigue siendo su DUEÑO.
func t16CasoDuenoDistintoDelActivo(t *testing.T) {
	e := newT16Env(t)
	cart := e.evento("t16-cart", "cart", t16FlujoCart)
	menu := e.evento("t16-menu", "menu", "")
	st := e.terminal(t16FlujoCart, menu.ID, cart.ID)

	e.rt.closeIfFinished(context.Background(), &st)

	trs := e.evs.transiciones()
	if len(trs) != 1 || trs[0].eventID != cart.ID {
		t.Fatalf("debe transicionar EL DUEÑO (%q, el cart) y SOLO él; se pidió %+v (el menú es %q)",
			cart.ID, trs, menu.ID)
	}
	if got := e.evs.statusOf(cart.ID); got != events.StatusClosed {
		t.Fatalf("🎁 el `cart` heredado —que hasta T1.6 se quedaba open para siempre— debe cerrarse; quedó %q", got)
	}
	if got := e.evs.statusOf(menu.ID); got != events.StatusOpen {
		t.Fatalf("el `menu` ACTIVO no ha terminado nada y NO puede morir aquí "+
			"(la propiedad de H2, ahora sostenida por construcción); quedó %q", got)
	}
	if st.OwnerEventID != "" {
		t.Fatalf("hueco C: el dueño debe apagarse tras cerrarlo (si no, pendingClosure dirá "+
			"pendiente=true para siempre y el contacto se queda mudo); vale %q", st.OwnerEventID)
	}
	if st.EventID != menu.ID {
		t.Fatalf("hueco D: el ACTIVO sobrevive intacto (el `menu` sigue siendo con quien habla el "+
			"contacto); quiero %q y vale %q", menu.ID, st.EventID)
	}
	efs := e.sink.efectos()
	if len(efs) != 1 || efs[0].eventID != cart.ID || efs[0].kind != "cart" {
		t.Fatalf("la telemetría es del evento que MURIÓ (el cart %q), no del que seguía delante (%q); llegó %+v",
			cart.ID, menu.ID, efs)
	}
}

// t16CasoSinDueno — REQ-053.1, cláusula 3: una fila LEGADA (escrita entre la migración
// 0062 y el despliegue de T1.5, con flow_id poblado y owner_event_id todavía NULL ⇒ ""
// en el struct) no autoriza a cerrar nada.
func t16CasoSinDueno(t *testing.T) {
	e := newT16Env(t)
	menu := e.evento("t16-menu", "menu", "")
	st := e.terminal(t16FlujoCart, menu.ID, "")

	e.rt.closeIfFinished(context.Background(), &st)

	if trs := e.evs.transiciones(); len(trs) != 0 {
		t.Fatalf("sin dueño NO se transiciona ningún evento (REQ-053.1, 3.ª cláusula); se pidió %+v", trs)
	}
	if got := e.evs.statusOf(menu.ID); got != events.StatusOpen {
		t.Fatalf("y el activo no se toca: quedó %q", got)
	}
	if st.EventID != menu.ID || st.OwnerEventID != "" {
		t.Fatalf("tampoco se limpia nada: owner=%q active=%q (quería vacío y %q)",
			st.OwnerEventID, st.EventID, menu.ID)
	}
	if efs := e.sink.efectos(); len(efs) != 0 {
		t.Fatalf("ni se emite telemetría de una muerte que no ocurrió: %+v", efs)
	}
}

// TestCloseIfFinished_ElDesenlaceDecideElEstado_ElDuenoDecideLaFila es INV-053.5
// ejecutable: `Outcome` contesta CON QUÉ estado muere el evento y `owner_event_id`
// contesta CUÁL evento muere. Se comprueban JUNTOS y en el caso divergente, que es el
// único donde una respuesta podría contaminar a la otra: el desenlace `cancelled` que
// el módulo selló en las Vars del flujo del `cart` tiene que aterrizar en la fila del
// `cart` —no en la del `menu`, y no degradado a `closed`—.
func TestCloseIfFinished_ElDesenlaceDecideElEstado_ElDuenoDecideLaFila(t *testing.T) {
	e := newT16Env(t)
	cart := e.evento("t16-cart", "cart", t16FlujoCart)
	menu := e.evento("t16-menu", "menu", "")
	st := e.terminal(t16FlujoCart, menu.ID, cart.ID)
	st.Vars[model.VarOutcome] = string(model.OutcomeCancelled)

	e.rt.closeIfFinished(context.Background(), &st)

	if got := e.evs.statusOf(cart.ID); got != events.StatusCancelled {
		t.Fatalf("el desenlace del módulo manda sobre el ESTADO: el cart debe quedar %q y quedó %q",
			events.StatusCancelled, got)
	}
	if got := e.evs.statusOf(menu.ID); got != events.StatusOpen {
		t.Fatalf("y el dueño manda sobre la FILA: el menú no se toca; quedó %q", got)
	}
	efs := e.sink.efectos()
	if len(efs) != 1 || efs[0].nombre != EffectEventCancelled || efs[0].eventID != cart.ID {
		t.Fatalf("el efecto SIGUE al estado y se emite sobre el dueño: quiero %q sobre %q, llegó %+v",
			EffectEventCancelled, cart.ID, efs)
	}
}

// TestCloseIfFinished_FalloRealNoLimpiaNingunoDeLosDosPunteros es el complemento
// INTERNO de TestCierreNatural_FalloRealNoLimpiaElPuntero (event_lifecycle_test.go,
// paquete externo), que solo puede mirar st.EventID por el camino de producción.
//
// E-8 §4 dice «un solo hecho»: si la transición falla DE VERDAD, el estado no puede
// quedar diciendo que el evento murió. Con DOS punteros eso son dos afirmaciones, y
// las dos tienen que conservarse — un dueño limpiado sobre una transición fallida
// perdería el reintento para siempre (pendingClosure ya no diría pendiente).
func TestCloseIfFinished_FalloRealNoLimpiaNingunoDeLosDosPunteros(t *testing.T) {
	e := newT16Env(t)
	cart := e.evento("t16-cart", "cart", t16FlujoCart)
	menu := e.evento("t16-menu", "menu", "")
	st := e.terminal(t16FlujoCart, menu.ID, cart.ID)
	e.evs.falla(errors.New("la BD se fue a almorzar"))

	e.rt.closeIfFinished(context.Background(), &st)

	if got := e.evs.statusOf(cart.ID); got != events.StatusOpen {
		t.Fatalf("con la transición rota el evento sigue open, quedó %q", got)
	}
	if st.OwnerEventID != cart.ID || st.EventID != menu.ID {
		t.Fatalf("NINGUNO de los dos punteros se limpia (E-8 §4); owner=%q (quiero %q) active=%q (quiero %q)",
			st.OwnerEventID, cart.ID, st.EventID, menu.ID)
	}
	// Y el reintento sigue vivo: el siguiente entrante volverá a intentarlo.
	if _, _, pendiente := e.rt.pendingClosure(context.Background(), st); !pendiente {
		t.Fatal("tras un fallo real el cierre debe seguir PENDIENTE (si no, el reintento de E-8 §4 se pierde)")
	}
	// La BD vuelve: el reintento cierra y AHORA sí limpia el dueño.
	e.evs.falla(nil)
	e.rt.closeIfFinished(context.Background(), &st)
	if got := e.evs.statusOf(cart.ID); got != events.StatusClosed {
		t.Fatalf("el reintento debe cerrar el evento, quedó %q", got)
	}
	if st.OwnerEventID != "" || st.EventID != menu.ID {
		t.Fatalf("y limpiar SOLO el dueño; owner=%q active=%q", st.OwnerEventID, st.EventID)
	}
}

// --- hueco C: la fila se suelta ------------------------------------------------

// TestCloseIfFinished_TrasCerrar_LaFilaSeSuelta es el hueco C medido donde duele: en
// el TURNO SIGUIENTE.
//
// El modo de fallo que caza es el más caro de esta tarea y no lo enseña ningún assert
// del turno del cierre. Si closeIfFinished no apagara st.OwnerEventID, la fila
// quedaría {terminal, con dueño} y pendingClosure contestaría pendiente=true en TODOS
// los entrantes posteriores: advanceLive no alcanzaría jamás releaseFinishedState,
// caería a advanceLiveStep, y engine.Step sobre un estado ya Finished() no emite nada
// —el contacto MUDO para siempre, exactamente el #28 que la Ola 6 arregló, reabierto
// por la puerta de al lado—. Es el mismo defecto que flujo_ajeno_terminal_test.go
// midió por sonda en 2026-08-11.
//
// Por eso no se comprueba el struct: se ejercita el TURNO SIGUIENTE de verdad
// (advanceLive, el camino de producción) y se mira lo único que el cliente nota — que
// la fila terminal se SUELTA (Delete) y el entrante se enruta como el de un contacto
// sin conversación, en vez de morir en un Step mudo. El resolver de disparos es el
// Noop de fábrica, así que el enrutado acaba en Ignore sin saliente: lo que se afirma
// es que se LLEGÓ ahí, y la señal de haber llegado es que la fila ya no existe.
func TestCloseIfFinished_TrasCerrar_LaFilaSeSuelta(t *testing.T) {
	casos := []struct {
		nombre    string
		divergent bool
	}{
		{nombre: "caso común (dueño == activo)", divergent: false},
		{nombre: "caso divergente (el activo sobrevive al cierre)", divergent: true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ctx := context.Background()
			e := newT16Env(t)
			cart := e.evento("t16-cart", "cart", t16FlujoCart)
			activo := cart.ID
			if c.divergent {
				activo = e.evento("t16-menu", "menu", "").ID
			}
			st := e.terminal(t16FlujoCart, activo, cart.ID)

			// Turno 1: el flujo termina y el dueño se cierra. Se persiste lo que
			// advanceLiveStep persistiría, porque el turno siguiente LEE de la base.
			e.rt.closeIfFinished(ctx, &st)
			if err := e.rt.store.Save(ctx, st); err != nil {
				t.Fatalf("persistir el estado tras el cierre: %v", err)
			}
			releido, ok, err := e.rt.store.Load(ctx, e.key)
			if err != nil || !ok {
				t.Fatalf("precondición: la fila terminal debe seguir existiendo tras el cierre; ok=%v err=%v", ok, err)
			}
			if !releido.Finished() {
				t.Fatalf("precondición: la fila sigue TERMINAL (el cierre no la borra, es cosa del turno siguiente); nodo=%q", releido.CurrentNode)
			}

			// Turno 2, la propiedad: ya no queda cierre pendiente…
			if _, _, pendiente := e.rt.pendingClosure(ctx, releido); pendiente {
				t.Fatal("HUECO C: pendingClosure sigue diciendo pendiente=true tras el cierre; " +
					"advanceLive nunca soltará la fila y el contacto se queda MUDO")
			}
			// …y el turno de verdad la suelta.
			if err := e.rt.advanceLive(ctx, t52Tenant, t52Session, e.key, e.contactID, releido,
				t16Incoming("t16-siguiente", "necesito ayuda")); err != nil {
				t.Fatalf("el turno siguiente no debe fallar: %v", err)
			}
			if _, sigue, err := e.rt.store.Load(ctx, e.key); err != nil || sigue {
				t.Fatalf("HUECO C: la fila terminal debe quedar SUELTA (borrada) para que el entrante se enrute "+
					"como el de un contacto sin conversación; sigue=%v err=%v", sigue, err)
			}
		})
	}
}

// --- la ratificación del event_stop --------------------------------------------

// t16NodoEnCurso es un nodo cualquiera que NO es el centinela: sirve para sembrar un
// flujo EN CURSO (Finished()==false) frente al que arma e.terminal.
const t16NodoEnCurso = "root"

// t16SueltaLaFila corre el TURNO SIGUIENTE de verdad (advanceLive, el camino de
// producción) sobre un flow_state ya terminal y sin cierre pendiente, y comprueba que
// la fila se SUELTA: el entrante se enruta como el de un contacto sin conversación en
// vez de morir en un Step mudo sobre un estado Finished(). Es el mismo gesto que
// TestCloseIfFinished_TrasCerrar_LaFilaSeSuelta hace inline; vive aparte para que el
// test de abajo no se pase de gocyclo (min-complexity 15 con run.tests activado,
// .golangci.yml), el MISMO motivo por el que los tres casos de REQ-053.1 son funciones
// propias.
func t16SueltaLaFila(t *testing.T, e *t16Env, st model.Conversation) {
	t.Helper()
	ctx := context.Background()
	if err := e.rt.store.Save(ctx, st); err != nil {
		t.Fatalf("persistir el estado tras el cierre: %v", err)
	}
	if err := e.rt.advanceLive(ctx, t52Tenant, t52Session, e.key, e.contactID, st,
		t16Incoming("t16-siguiente", "necesito ayuda")); err != nil {
		t.Fatalf("el turno siguiente no debe fallar: %v", err)
	}
	if _, sigue, err := e.rt.store.Load(ctx, e.key); err != nil || sigue {
		t.Fatalf("la fila terminal debe quedar SUELTA (borrada) para que el entrante se enrute "+
			"como el de un contacto sin conversación; sigue=%v err=%v", sigue, err)
	}
}

// TestCloseIfFinished_TrasEventStop_ElDuenoSobreviveYCierraElEvento FIJA el cambio de
// comportamiento que la Ola 1 introduce en el camino de `event_stop`, para que nadie lo
// "arregle" por sorpresa dentro de seis meses.
//
// # EL CAMBIO, DICHO ENTERO
//
// stopEvent (events.go) apaga st.EventID y NO toca st.OwnerEventID, así que la fila
// queda {flow=F, event="", owner=C}: el flujo F sigue CARGADO —stopEvent desactiva el
// evento, no destruye el estado— y el contacto puede seguir avanzándolo. Cuando F llega
// al centinela:
//
//   - ANTES del Plan 053, pendingClosure cortaba con `st.EventID == ""` ⇒ no se cerraba
//     NADA y C se quedaba `open` para siempre, un evento huérfano con su intake
//     colgando de una muerte que ya nadie iba a firmar por esta vía.
//   - CON la Ola 1, la condición de entrada es el DUEÑO ⇒ C SE CIERRA con su
//     event_closed.
//
// 🔴 DECISIÓN RATIFICADA POR JHOAN (2026-08-19): se ACEPTA el cierre. El flujo era de C
// y terminó; cerrarlo es coherente con D-043.5 y elimina un evento huérfano. Este test
// NO comprueba que el comportamiento sea el deseable —eso ya se decidió—, comprueba que
// SIGUE SIENDO EL QUE SE DECIDIÓ. El razonamiento completo, con la costura del copy de
// stopNotice que queda aceptada, está en el bloque de ratificación de stopEvent.
//
// # HASTA DÓNDE LLEGA EL CAMINO REAL
//
// Los pasos 1 y 2 van por el camino de producción: se llama a rt.stopEvent TAL CUAL lo
// llaman liveEventSwitch (events.go, rama trigger.StopEvent) y exitMenuChoice
// (exit_menu.go, opción «2»), con su firma real y su Save real. El paso 3 —que el
// contacto AVANCE F hasta el centinela— se monta a mano poniendo CurrentNode en
// model.NodeTerminal y llamando a closeIfFinished, que es EXACTAMENTE lo que
// advanceLiveStep hace tras engine.Step (incoming.go): el arnés de este fichero no
// carga definiciones de flujo ni módulos (engine.New(modules.NewRegistry()) vacío), así
// que hacerlo por engine.Step exigiría montar un flujo entero para probar una propiedad
// que no depende de él. El turno siguiente —la suelta de la fila— sí vuelve al camino
// real (advanceLive).
func TestCloseIfFinished_TrasEventStop_ElDuenoSobreviveYCierraElEvento(t *testing.T) {
	ctx := context.Background()
	e := newT16Env(t)
	cart := e.evento("t16-cart", "cart", t16FlujoCart)

	// 1 · El punto de partida: el evento C con SU flujo F corriendo, los dos punteros
	//     en C — lo que deja pointStateAtEvent tras el Delete + startLocked de
	//     enterEventFlow (duenoDelFlujo=true).
	st := e.terminal(t16FlujoCart, cart.ID, cart.ID)
	st.CurrentNode = t16NodoEnCurso // el flujo está EN CURSO, todavía no terminó
	if err := e.rt.store.Save(ctx, st); err != nil {
		t.Fatalf("sembrar el flow_state del evento en curso: %v", err)
	}

	// 2 · El cliente dice «déjalo»: stopEvent, por el camino real.
	if err := e.rt.stopEvent(ctx, e.key, t52Session, st); err != nil {
		t.Fatalf("stopEvent: %v", err)
	}
	tras, ok, err := e.rt.store.Load(ctx, e.key)
	if err != nil || !ok {
		t.Fatalf("stopEvent CONSERVA la fila (no la borra, al revés que handleEscape); ok=%v err=%v", ok, err)
	}
	if tras.EventID != "" {
		t.Fatalf("stopEvent apaga el ACTIVO; event_id=%q", tras.EventID)
	}
	// 🔴 EL ASSERT DE LA RATIFICACIÓN: el dueño SOBREVIVE al stop. Este es el que caza a
	// quien "limpie" st.OwnerEventID en stopEvent por simetría con st.EventID. Si lo
	// pones rojo con un cambio de código, no lo arregles aquí: estás cambiando una
	// decisión firmada (ver el bloque de ratificación en stopEvent).
	if tras.OwnerEventID != cart.ID {
		t.Fatalf("el DUEÑO sobrevive a event_stop a propósito (decisión de Jhoan, 2026-08-19): "+
			"quiero owner_event_id=%q y vale %q. stopEvent desactiva el evento, NO destruye el flujo, "+
			"así que ese flujo sigue siendo de C", cart.ID, tras.OwnerEventID)
	}
	if tras.FlowID != t16FlujoCart || tras.CurrentNode != t16NodoEnCurso {
		t.Fatalf("y el flujo sigue CARGADO e intacto (esa es la razón de conservar el dueño); "+
			"flow_id=%q current_node=%q", tras.FlowID, tras.CurrentNode)
	}
	if got := e.evs.statusOf(cart.ID); got != events.StatusOpen {
		t.Fatalf("stopEvent no mata el evento (D-043.2): status=%q, quiero %q", got, events.StatusOpen)
	}

	// 3 · El contacto sigue avanzando F y F llega al centinela. CurrentNode a mano = lo
	//     que engine.Step deja; closeIfFinished = lo que advanceLiveStep llama justo
	//     después. Ver la nota de arriba.
	tras.CurrentNode = model.NodeTerminal
	e.rt.closeIfFinished(ctx, &tras)
	if got := e.evs.statusOf(cart.ID); got != events.StatusClosed {
		t.Fatalf("C SE CIERRA al terminar su flujo, aunque el stop lo hubiera desactivado antes "+
			"(quedó %q). Antes del Plan 053 se quedaba `open` para siempre: si vuelve a quedarse "+
			"open, la condición de entrada de pendingClosure ha vuelto a mirar al ACTIVO", got)
	}
	if tras.OwnerEventID != "" || tras.EventID != "" {
		t.Fatalf("y la fila queda con los DOS punteros limpios (hueco C; el activo ya venía vacío "+
			"del stop); owner=%q active=%q", tras.OwnerEventID, tras.EventID)
	}
	// Y el turno siguiente la suelta: sin esto el contacto se quedaría mudo sobre una
	// fila terminal que nadie borra.
	t16SueltaLaFila(t, e, tras)
}

// --- EL TEST DE MUTACIÓN -------------------------------------------------------

// t16PendingClosureConGuarda es pendingClosure con la guarda de posesión de H2
// REINTRODUCIDA a mano: mismo predicado (`conocido && ev.FlowID != st.FlowID`), mismo
// sitio (después de la relectura, antes del return) y mismo retorno que tenía antes de
// T1.6. Solo existe para el test de mutación de abajo.
//
// Por qué envolver en vez de duplicar el cuerpo: la guarda retirada NO tocaba `ev` ni
// `conocido` —solo podía convertir un pendiente=true en pendiente=false—, así que
// componerla por encima es EXACTAMENTE la mutación, sin copiar código que pueda
// derivar del original.
func (rt *Runtime) t16PendingClosureConGuarda(ctx context.Context, st model.Conversation) (events.Event, bool, bool) {
	ev, conocido, pendiente := rt.pendingClosure(ctx, st)
	if !pendiente {
		return ev, conocido, pendiente
	}
	if conocido && ev.FlowID != st.FlowID {
		return ev, true, false
	}
	return ev, conocido, true
}

// t16Escenario es un estado del flow_state alcanzable en producción, descrito por lo
// único que la guarda podía mirar.
type t16Escenario struct {
	nombre   string
	sinPlano bool // rt.events == nil
	// eventos a sembrar: (id, kind, flowID congelado en la fila del evento)
	activoKind, activoFlow string // "" en kind ⇒ no se siembra activo distinto del dueño
	duenoKind, duenoFlow   string // "" en kind ⇒ no hay dueño (owner_event_id vacío)
	duenoMuerto            bool   // el dueño existe pero ya no está `open` ⇒ conocido=false
	stFlow                 string
	noTerminal             bool
}

// TestMutacion_ReintroducirLaGuardaDeH2_NoCambiaNingunResultado es el CRITERIO DURO de
// T1.6 (D-053.2): si volver a poner la guarda cambiara algo, es que seguía haciendo
// trabajo que el plan no entendió, y la ola se para.
//
// # POR QUÉ COMPARAR pendingClosure BASTA
//
// La guarda vivía dentro de pendingClosure y su único efecto posible era bajar
// `pendiente` de true a false; `ev` y `conocido` salían intactos. Y closeIfFinished lee
// la decisión de la guarda EXCLUSIVAMENTE a través de `pendiente` (si es false retorna
// sin tocar nada). Luego «los tres valores de retorno coinciden para todo escenario» es
// una prueba COMPLETA de la mutación, no una aproximación — y además inmune a que el
// cuerpo de closeIfFinished cambie mañana.
//
// # LOS ESCENARIOS, Y POR QUÉ ESTOS
//
// Se enumeran los estados en los que la guarda VIEJA valía true (los que blindaba) más
// los vecinos que la rodean, y se comprueba uno por uno:
//
//  1. común — dueño == activo == `cart`, y el flujo de la fila es el del `cart`. La
//     guarda no disparaba y sigue sin disparar: FlowID coincide.
//  2. divergente (#22 / H2) — activo `menu` (FlowID=""), dueño `cart`. Es EL caso que
//     la guarda existía para blindar: disparaba porque comparaba contra el ACTIVO. Con
//     la relectura hecha sobre el DUEÑO, `ev.FlowID` ES el flujo de la fila y la guarda
//     no tiene nada que objetar. Mismo resultado por una razón distinta, que es
//     justamente la tesis del plan.
//  3. dueño vacío — la guarda ni se evalúa (la condición de entrada corta antes).
//  4. dueño ya muerto — `conocido=false`, y la guarda exigía `conocido`.
//  5. sin plano de eventos — corta antes, igual que hoy.
//  6. flujo no terminal — corta antes, igual que hoy.
//  7. divergente CON el dueño ya muerto — la combinación de (2) y (4), que es el
//     reintento de E-8 §4 cayendo justo sobre el caso del `menu`: el sitio donde la
//     guarda vieja y la relectura nueva miran filas distintas Y una de ellas no se
//     puede leer. Se incluye porque es el único cruce que ninguno de los otros seis
//     ejercita.
//
// El único estado en el que la mutación SÍ cambia el resultado está aparte, en
// TestMutacion_ElUnicoEstadoQueLaGuardaAunDistingue: un dueño MAL ESTAMPADO, que no es
// un estado de producción sino la violación del contrato de pointStateAtEvent (T1.5).
func TestMutacion_ReintroducirLaGuardaDeH2_NoCambiaNingunResultado(t *testing.T) {
	escenarios := []t16Escenario{
		{nombre: "1 · común: dueño == activo", duenoKind: "cart", duenoFlow: t16FlujoCart, stFlow: t16FlujoCart},
		{nombre: "2 · divergente #22/H2: menu activo sobre cart dueño",
			activoKind: "menu", activoFlow: "", duenoKind: "cart", duenoFlow: t16FlujoCart, stFlow: t16FlujoCart},
		{nombre: "3 · dueño vacío (fila legada / menú puro)",
			activoKind: "menu", activoFlow: "", stFlow: t16FlujoCart},
		{nombre: "4 · dueño ya no vivo (lo mató otro escritor)",
			duenoKind: "cart", duenoFlow: t16FlujoCart, duenoMuerto: true, stFlow: t16FlujoCart},
		{nombre: "5 · sin plano de eventos", sinPlano: true,
			duenoKind: "cart", duenoFlow: t16FlujoCart, stFlow: t16FlujoCart},
		{nombre: "6 · el flujo no ha terminado", noTerminal: true,
			duenoKind: "cart", duenoFlow: t16FlujoCart, stFlow: t16FlujoCart},
		{nombre: "7 · divergente con el dueño ya muerto (reintento de E-8 §4 sobre el caso del menu)",
			activoKind: "menu", activoFlow: "", duenoKind: "cart", duenoFlow: t16FlujoCart,
			duenoMuerto: true, stFlow: t16FlujoCart},
	}
	for _, esc := range escenarios {
		t.Run(esc.nombre, func(t *testing.T) {
			ctx := context.Background()
			e := newT16Env(t)
			rt := e.rt
			if esc.sinPlano {
				rt = e.rtSinPlano
			}
			var dueno, activo string
			if esc.duenoKind != "" {
				ev := e.evento("t16-dueno", esc.duenoKind, esc.duenoFlow)
				dueno, activo = ev.ID, ev.ID
				if esc.duenoMuerto {
					if err := e.evs.t52StubEvents.TransitionEvent(ctx, ev.ID, events.StatusClosed); err != nil {
						t.Fatalf("precondición: matar al dueño: %v", err)
					}
				}
			}
			if esc.activoKind != "" {
				activo = e.evento("t16-activo", esc.activoKind, esc.activoFlow).ID
			}
			st := e.terminal(esc.stFlow, activo, dueno)
			if esc.noTerminal {
				st.CurrentNode = "root"
			}

			evReal, conocidoReal, pendienteReal := rt.pendingClosure(ctx, st)
			evMut, conocidoMut, pendienteMut := rt.t16PendingClosureConGuarda(ctx, st)

			if pendienteReal != pendienteMut || conocidoReal != conocidoMut || evReal.ID != evMut.ID {
				t.Fatalf("LA MUTACIÓN CAMBIA EL RESULTADO ⇒ la guarda de posesión seguía haciendo trabajo real "+
					"y el plan no lo había entendido: SE PARA LA OLA y se vuelve al design (D-053.2).\n"+
					"  sin guarda: ev=%q conocido=%v pendiente=%v\n"+
					"  con guarda: ev=%q conocido=%v pendiente=%v\n"+
					"  estado: flow=%q owner=%q active=%q finished=%v",
					evReal.ID, conocidoReal, pendienteReal, evMut.ID, conocidoMut, pendienteMut,
					st.FlowID, st.OwnerEventID, st.EventID, st.Finished())
			}
		})
	}
}

// TestMutacion_ElUnicoEstadoQueLaGuardaAunDistingue_EsUnDuenoMalEstampado aísla, y
// deja EJECUTABLE, el único estado en el que reintroducir la guarda cambiaría algo —
// para que nadie lo descubra en producción y para que la reverificación de la Ola 2
// tenga dónde mirar.
//
// El estado: `owner_event_id` apuntando a un evento SIN flujo (FlowID="") sobre un
// flow_state cuyo FlowID NO está vacío. La guarda dispararía; sin ella, closeIfFinished
// cierra ese evento por el fin de un flujo que no es suyo — el falso `event_closed` de
// #22 / H2, resucitado.
//
// # POR QUÉ NO PARA LA OLA (y qué hay que vigilar para que siga siendo verdad)
//
// Porque no es un estado que este fichero pueda alcanzar: es la VIOLACIÓN del contrato
// de pointStateAtEvent (T1.5), que estampa el dueño «donde el flujo se acaba de
// arrancar PARA este evento» y explícitamente NO en saveMenuState. Bajo ese contrato,
// `ev.FlowID == st.FlowID` para el dueño SIEMPRE (los dos salen de la misma decisión:
// enterEventFlow arranca `ev.FlowID` y startLocked lo escribe en la fila), luego la
// guarda no puede sino confirmar lo que el campo ya dice.
//
// ✅ HUBO UN camino de producción que lo fabricaba, Y YA ESTÁ CERRADO (no lo busques
// abierto: este párrafo lo describía como vivo hasta la revisión de la Ola 1). Era una
// regla `event_start` con `event_kind` distinto de `menu` y `flow_id` VACÍO: la TERCERA
// salida de enterEventFlow (events.go), que no borra el flow_state previo —solo lo borra
// la rama que arranca un flujo— y aun así llegaba a pointStateAtEvent, estampando el
// dueño sobre un estado HEREDADO. No era la config canónica (admin/triggers.go da
// flow_id a cart y survey, y el `menu` sale por presentMenu sin pasar por ahí), pero era
// alcanzable configurando.
//
// Se cerró POR EL ESTAMPADO, que es donde tocaba —no reponiendo esta guarda, porque una
// fila con ese dueño ya miente antes de que nadie la lea—: pointStateAtEvent recibe un
// cuarto parámetro `duenoDelFlujo bool` (events.go) que solo vale true cuando el camino
// ACABA de hacer Delete + startLocked, y el ACTIVO se sigue estampando siempre. Sobre un
// flujo heredado el dueño ya no se escribe. Lo fija
// TestPointStateAtEvent_SobreFlujoHeredado_NoEstampaDueno (owner_event_id_test.go).
//
// ⚠️ Y ESTE TEST SIGUE VALIENDO, por eso no se borra: siembra el estado A MANO, sin
// pasar por pointStateAtEvent, así que mide la propiedad del LECTOR —qué hace
// closeIfFinished si alguna vez encuentra un dueño mal estampado— y no la del escritor.
// Es la red de seguridad del día en que aparezca un SEGUNDO camino de estampado que se
// salte el contrato de T1.5.
func TestMutacion_ElUnicoEstadoQueLaGuardaAunDistingue_EsUnDuenoMalEstampado(t *testing.T) {
	ctx := context.Background()
	e := newT16Env(t)
	// Un evento SIN flujo estampado como dueño de una fila que SÍ tiene flujo: la
	// combinación que el contrato de T1.5 prohíbe.
	malDueno := e.evento("t16-sin-flujo", "media", "")
	st := e.terminal(t16FlujoCart, malDueno.ID, malDueno.ID)

	_, _, pendienteReal := e.rt.pendingClosure(ctx, st)
	_, _, pendienteMut := e.rt.t16PendingClosureConGuarda(ctx, st)

	if !pendienteReal {
		t.Fatal("sin la guarda, un dueño (aunque esté mal estampado) SÍ produce cierre pendiente: " +
			"si esto deja de ser cierto, la condición de entrada de pendingClosure cambió y este test ya no mide nada")
	}
	if pendienteMut {
		t.Fatal("con la guarda puesta este estado NO produciría cierre pendiente: si esto deja de ser cierto, " +
			"el predicado de la guarda ya no es `conocido && ev.FlowID != st.FlowID` y hay que reescribir este test")
	}
	// La consecuencia, dicha entera: sin guarda, ese evento SE CIERRA.
	e.rt.closeIfFinished(ctx, &st)
	if got := e.evs.statusOf(malDueno.ID); got != events.StatusClosed {
		t.Fatalf("y la consecuencia de retirar la guarda sobre un dueño mal estampado es cerrarlo; quedó %q", got)
	}
}

// --- Plan 053 · Ola 2 · T2.3: la rama retirada y el cierre del ciclo -------------

// TestReleaseFinishedState_EventoAjenoVivo_YaNoEsConstruible (T2.3, criterio 1): el
// escenario que disparaba la rama `conocido=true, pendiente=false` —la guarda de
// POSESIÓN de H2— ya NO se puede montar. Se deja como test de IMPOSIBILIDAD en vez de
// borrar la cobertura en silencio, que es lo que el criterio de la tarea pide.
//
// El estado es el que deja el turno terminal en el caso divergente: dueño APAGADO
// (hueco C, ya se cerró) y activo VIVO (hueco D, el `menu` no ha terminado nada). Con
// el dueño vacío pendingClosure corta en su condición de entrada, así que `conocido`
// sale false: la combinación que alimentaba la rama retirada no tiene forma de darse.
func TestReleaseFinishedState_EventoAjenoVivo_YaNoEsConstruible(t *testing.T) {
	e := newT16Env(t)
	menu := e.evento("t23-ev-menu", "menu", "")
	st := e.terminal(t16FlujoCart, menu.ID, "") // activo vivo, dueño ya apagado

	ev, conocido, pendiente := e.rt.pendingClosure(context.Background(), st)
	if pendiente || conocido {
		t.Fatalf("la rama `conocido=true, pendiente=false` de la guarda de posesión debe ser "+
			"INCONSTRUIBLE tras T1.6; conocido=%v pendiente=%v ev=%q", conocido, pendiente, ev.ID)
	}
}

// TestPendingClosure_TrasCerrar_NoQuedaPendiente (T2.3, criterio 3 — el que ata esta
// tarea con el hueco C de T1.6): una fila cuyo cierre YA se consumó contesta
// pendiente=false, aunque el puntero ACTIVO siga puesto.
//
// 🔴 Es el ÚNICO sitio del plan donde un fallo del hueco C se nota ANTES de producción:
// si T1.6 no limpiara st.OwnerEventID, esto diría pendiente=true en todos los turnos
// siguientes, advanceLive no alcanzaría jamás releaseFinishedState y el contacto se
// quedaría MUDO sobre una fila terminal que nadie suelta.
func TestPendingClosure_TrasCerrar_NoQuedaPendiente(t *testing.T) {
	ctx := context.Background()
	e := newT16Env(t)
	cart := e.evento("t23-ev-cart", "cart", t16FlujoCart)
	menu := e.evento("t23-ev-menu2", "menu", "")
	st := e.terminal(t16FlujoCart, menu.ID, cart.ID)

	// El turno que consuma el cierre: cierra al DUEÑO y apaga su puntero.
	e.rt.closeIfFinished(ctx, &st)
	if st.OwnerEventID != "" {
		t.Fatalf("precondición (hueco C): el dueño debe quedar apagado tras cerrarse; owner=%q", st.OwnerEventID)
	}
	if st.EventID != menu.ID {
		t.Fatalf("precondición (hueco D): el activo NO debe apagarse por el cierre de otro; event=%q", st.EventID)
	}

	if _, _, pendiente := e.rt.pendingClosure(ctx, st); pendiente {
		t.Fatal("tras consumarse el cierre no puede quedar NINGÚN cierre pendiente: si queda, la fila " +
			"terminal no se suelta nunca y el contacto se queda mudo (hueco C de T1.6)")
	}
}
