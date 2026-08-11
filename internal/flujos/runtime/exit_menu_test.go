package runtime

// exit_menu_test.go prueba exitMenuChoice (Plan 043 · T5.2, D-043.10) como WHITE
// BOX, llamándolo DIRECTAMENTE (no depende del cableado en incoming.go, que es de
// T5.3 — ver el bloque literal en el informe de cierre de la tarea).
//
// ⚠️ DESVIACIÓN DE §4 DEL CONTRATO, DICHA ENTERA: la tabla de T5.2 anota este
// fichero como `package runtime_test`. exitMenuChoice es MINÚSCULA (unexported) a
// propósito (el módulo nunca debe interpretar 1/2/3, solo el runtime) y un paquete
// EXTERNO `runtime_test` no puede llamarlo — ni con el sufijo `_test`, es un
// paquete Go distinto que no ve símbolos no exportados del paquete `runtime`. Este
// fichero usa `package runtime` (interno), el MISMO patrón que ya existe en este
// directorio para probar bordes internos sin montar todo el motor
// (webhook_sink_test.go, tagline_append_test.go). Es la única forma de que
// "llamar a exitMenuChoice directamente" (instrucción explícita de la tarea)
// compile.
//
// Por la misma razón NO puede reusar memEventStore/newEventRuntime de
// event_switch_test.go (package runtime_test): ese paquete IMPORTA `runtime`, así
// que `runtime` importarlo de vuelta sería un ciclo. t52StubEvents de abajo es el
// doble mínimo equivalente, implementando el mismo EventStore (runtime/events.go)
// que memEventStore.
//
// "ATRAVIESA EL PROCESO" (norma de calidad #2): los tres caminos (1/2/3) se
// prueban contra los métodos REALES del runtime (stopEvent, beginEvent,
// disarmExitMenu, resendScreen — todos en events.go/exit_menu.go/send.go sin
// tocar), exactamente como beginEvent, stopEvent, etc. se prueban hoy en
// event_switch_test.go: solo la frontera de persistencia (EventStore, un puerto
// ESTRECHO, D2) y el Sender llevan doble, igual que en TODOS los tests de este
// paquete. Lo que NO se prueba aquí —y solo se cubrirá cuando T5.3 pegue el
// bloque de incoming.go— es que exitMenuChoice se LLAME desde advanceLive; eso
// queda marcado en cada sub-test con «⚠️ SOLO real tras el cableado».

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const (
	t52Tenant  = "tenant-t52"
	t52Session = "sess-t52"
)

// --- dobles MÍNIMOS, propios de este fichero (t52...) ------------------------

// t52StubEvents es el doble MÍNIMO de runtime.EventStore (events.go:28) para
// exitMenuChoice como white box. No impone el índice único parcial con el rigor
// de memEventStore (esa garantía la fija T5.1/T5.4 contra su propio doble); aquí
// basta con que CreateEvent/TransitionEvent/ListAlive se comporten con
// coherencia para las tres ramas de exitMenuChoice.
type t52StubEvents struct {
	mu    sync.Mutex
	rows  map[string]events.Event
	alive map[string]string // kind -> id vivo
	seq   int
}

func newT52StubEvents() *t52StubEvents {
	return &t52StubEvents{rows: map[string]events.Event{}, alive: map[string]string{}}
}

// seed inserta una fila YA VIVA (para partir con un evento activo, como si
// hubiera nacido en un turno anterior).
func (s *t52StubEvents) seed(ev events.Event) events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[ev.ID] = ev
	if ev.Status == events.StatusOpen {
		s.alive[ev.Kind] = ev.ID
	}
	return ev
}

func (s *t52StubEvents) statusOf(id string) events.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id].Status
}

func (s *t52StubEvents) CreateEvent(_ context.Context, in events.NewEvent) (events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alive[in.Kind]; ok {
		return events.Event{}, events.ErrAliveExists
	}
	s.seq++
	ev := events.Event{
		ID: fmt.Sprintf("t52-ev-%d", s.seq), TenantID: in.TenantID, SessionID: in.SessionID,
		ContactID: in.ContactID, Kind: in.Kind, HistoryID: in.Kind + "-t52-history-id",
		Status: events.StatusOpen, FlowID: in.FlowID, FlowVersion: in.FlowVersion,
	}
	s.rows[ev.ID] = ev
	s.alive[in.Kind] = ev.ID
	return ev, nil
}

func (s *t52StubEvents) GetAliveByKind(_ context.Context, _, _, _, kind string) (events.Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.alive[kind]
	if !ok {
		return events.Event{}, false, nil
	}
	return s.rows[id], true, nil
}

func (s *t52StubEvents) ListAlive(_ context.Context, _, _, _ string) ([]events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]events.Event, 0, len(s.alive))
	for _, id := range s.alive {
		out = append(out, s.rows[id])
	}
	return out, nil
}

func (s *t52StubEvents) ListRescuable(_ context.Context, _, _, _ string, _ int) ([]events.Rescuable, error) {
	return nil, nil
}

func (s *t52StubEvents) GetEventForTenant(_ context.Context, _, eventID string) (events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.rows[eventID]
	if !ok {
		return events.Event{}, events.ErrEventNotFound
	}
	return ev, nil
}

func (s *t52StubEvents) TransitionEvent(_ context.Context, eventID string, to events.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.rows[eventID]
	if !ok {
		return events.ErrEventNotFound
	}
	ev.Status = to
	s.rows[eventID] = ev
	if s.alive[ev.Kind] == eventID {
		delete(s.alive, ev.Kind)
	}
	return nil
}

func (s *t52StubEvents) Touch(_ context.Context, _ string) error { return nil }

func (s *t52StubEvents) AppendSummary(_ context.Context, _ string, _ json.RawMessage) (int, error) {
	return 1, nil
}

func (s *t52StubEvents) AppendDecision(_ context.Context, _ string, _ []byte) error { return nil }

func (s *t52StubEvents) AppendMessage(_ context.Context, _ string, _ events.Role, _ string) (int, error) {
	return 1, nil
}

func (s *t52StubEvents) IsSuspended(_ events.Event, _ time.Duration) bool { return false }

// t52FakeSender captura los textos enviados (equivalente reducido de fakeSender
// en runtime_engine_test.go, que vive en el paquete EXTERNO y no es alcanzable
// desde aquí).
type t52FakeSender struct {
	mu   sync.Mutex
	sent []string
}

func (f *t52FakeSender) SendText(_ context.Context, _, _, text string) (*cloudlinkv1.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return &cloudlinkv1.Ack{Ok: true, AckedCommandId: fmt.Sprintf("t52-%d", len(f.sent))}, nil
}

func (f *t52FakeSender) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	return &cloudlinkv1.Ack{Ok: true}, nil
}

func (f *t52FakeSender) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

type t52FakeResolver struct{}

func (t52FakeResolver) ResolveTenant(_ context.Context, _ string) (string, string, error) {
	return t52Tenant, "", nil
}

func t52DiscardLogger() logger.Logger { return logger.New(logger.WithWriter(io.Discard)) }

// t52Env arma un runtime MÍNIMO con el plano de eventos cableado (sin dispatcher,
// sin fuentes de resumen: el objetivo es exitMenuChoice, no el resto del motor —
// las llamadas que SÍ dependen de esas piezas (presentMenu, summarizeAbandoned)
// tienen guardas nil-safe verificadas por lectura, ver el informe de cierre) y un
// contacto ya resuelto para que destino()/send() funcionen.
type t52Env struct {
	rt        *Runtime
	repo      *store.MemoryRepository
	evs       *t52StubEvents
	sender    *t52FakeSender
	contactID string
	key       store.Key
}

func newT52Env(t *testing.T) *t52Env {
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
	sender := &t52FakeSender{}
	evs := newT52StubEvents()
	eng := engine.New(modules.NewRegistry())
	rt := New(repo, eng, sender, t52FakeResolver{}, contacts, t52DiscardLogger(), WithEventStore(evs))
	return &t52Env{
		rt: rt, repo: repo, evs: evs, sender: sender, contactID: contactID,
		key: store.Key{TenantID: t52Tenant, SessionID: t52Session, ContactID: contactID},
	}
}

// armado siembra un flow_state con el menú de salida ARMADO sobre un evento
// `cart` vivo, tal y como lo deja cart.Module/menu.Module al agotar el reprompt
// (modules.ArmExitMenu).
func (e *t52Env) armado(t *testing.T, pantalla string) model.Conversation {
	t.Helper()
	ev := e.evs.seed(events.Event{
		ID: "t52-ev-cart", TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		Kind: "cart", HistoryID: "cart-t52-history-id", Status: events.StatusOpen,
	})
	st := model.Conversation{
		TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		EventID: ev.ID,
		Vars: map[string]any{
			modules.ExitMenuVar:      pantalla,
			modules.ExitMenuEventVar: ev.ID,
			"otro":                   "dato",
		},
	}
	if err := e.rt.store.Save(context.Background(), st); err != nil {
		t.Fatalf("sembrar flow_state armado: %v", err)
	}
	return st
}

func t52Incoming(text string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{From: "573001112233", Text: text}
}

// --- las CUATRO ramas de exitMenuChoice ---------------------------------------

// TestExitMenuChoice_SinArmarNoHaceNada: sin menú armado (Vars vacío o sin
// EventID), exitMenuChoice no consume el turno — es la guarda que hace que
// esto NUNCA se dispare fuera de un evento (⚠️ SOLO real end-to-end tras el
// cableado: aquí se comprueba el propio método).
func TestExitMenuChoice_SinArmarNoHaceNada(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := model.Conversation{TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID}

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming("1"))
	if err != nil || done {
		t.Fatalf("sin EventID ni Vars armados: done=%v err=%v, quiero false/nil", done, err)
	}
	if len(e.sender.texts()) != 0 {
		t.Fatal("no debe enviar nada")
	}
}

// TestExitMenuChoice_Opcion1_ReemiteYReiniciaContador: «1» re-emite la pantalla
// guardada, DESARMA la marca (persistida) y el turno se consume.
func TestExitMenuChoice_Opcion1_ReemiteYReiniciaContador(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := e.armado(t, "PANTALLA-ORIGINAL-DEL-CARRITO")

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming(" 1 "))
	if err != nil {
		t.Fatalf("exitMenuChoice: %v", err)
	}
	if !done {
		t.Fatal("opción 1: el turno debe consumirse (done=true)")
	}
	texts := e.sender.texts()
	if len(texts) != 1 || texts[0] != "PANTALLA-ORIGINAL-DEL-CARRITO" {
		t.Fatalf("debe re-emitir LA MISMA pantalla guardada; enviados=%v", texts)
	}
	persisted, ok, err := e.repo.Load(context.Background(), e.key)
	if err != nil || !ok {
		t.Fatalf("releer estado: ok=%v err=%v", ok, err)
	}
	if _, armado := persisted.Vars[modules.ExitMenuVar].(string); armado {
		t.Fatal("tras la opción 1 el menú debe quedar DESARMADO en lo persistido")
	}
	if persisted.Vars["otro"] != "dato" {
		t.Fatal("desarmar no debe perder otras claves de Vars")
	}
	if persisted.EventID != "t52-ev-cart" {
		t.Fatal("la opción 1 NO abandona el evento: el puntero sigue igual")
	}
}

// TestExitMenuChoice_Opcion2_DelegaEnStopEvent_StatusSigueOpen: «2» es EXACTAMENTE
// el event_stop (D-043.2, delegado en rt.stopEvent tal cual): el evento queda SIN
// puntero activo pero su STATUS sigue `open` (no se cierra ni se cancela), y la
// confirmación nombra el TIPO sin el identificador (E-3).
func TestExitMenuChoice_Opcion2_DelegaEnStopEvent_StatusSigueOpen(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := e.armado(t, "PANTALLA-ORIGINAL-DEL-CARRITO")

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming("2"))
	if err != nil {
		t.Fatalf("exitMenuChoice: %v", err)
	}
	if !done {
		t.Fatal("opción 2: el turno debe consumirse (done=true)")
	}
	if got := e.evs.statusOf("t52-ev-cart"); got != events.StatusOpen {
		t.Fatalf("status del evento tras la opción 2 = %q, quiero %q (D-043.2: se DEJA, no se cierra)", got, events.StatusOpen)
	}
	persisted, ok, err := e.repo.Load(context.Background(), e.key)
	if err != nil || !ok {
		t.Fatalf("releer estado: ok=%v err=%v", ok, err)
	}
	if persisted.EventID != "" {
		t.Fatalf("EventID persistido = %q, quiero \"\" (el puntero se apaga)", persisted.EventID)
	}
	texts := e.sender.texts()
	if len(texts) != 1 {
		t.Fatalf("debe mandar UNA confirmación; enviados=%v", texts)
	}
	if !strings.Contains(texts[0], "carrito") && !strings.Contains(texts[0], "pedido") {
		t.Fatalf("la confirmación debe nombrar el TIPO del evento; got=%q", texts[0])
	}
	if strings.Contains(texts[0], "t52-ev-cart") || strings.Contains(texts[0], "t52-history-id") {
		t.Fatalf("la confirmación NO debe filtrar ningún identificador (E-3); got=%q", texts[0])
	}
}

// TestExitMenuChoice_Opcion3_DelegaEnBeginEvent_VaAlMenu: «3» es el despachador
// (D-043.3: el menú es un evento más), con gestureGoTo — nace/conmuta el evento
// `menu` sin cerrar el `cart` que se deja atrás.
func TestExitMenuChoice_Opcion3_DelegaEnBeginEvent_VaAlMenu(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := e.armado(t, "PANTALLA-ORIGINAL-DEL-CARRITO")

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming("3"))
	if err != nil {
		t.Fatalf("exitMenuChoice: %v", err)
	}
	if !done {
		t.Fatal("opción 3: el turno debe consumirse (done=true)")
	}
	alive, err := e.evs.ListAlive(context.Background(), t52Tenant, t52Session, e.contactID)
	if err != nil {
		t.Fatalf("ListAlive: %v", err)
	}
	kinds := map[string]bool{}
	for _, ev := range alive {
		kinds[ev.Kind] = true
	}
	if !kinds["menu"] {
		t.Fatalf("opción 3 debe parir/conmutar un evento `menu`; vivos=%v", alive)
	}
	if got := e.evs.statusOf("t52-ev-cart"); got != events.StatusOpen {
		t.Fatalf("el `cart` que se deja atrás NO se cierra (E-11.3): status=%q", got)
	}
}

// TestExitMenuChoice_EntradaNoNumerica_Desarma: cualquier otra cosa (no 1/2/3)
// DESARMA el menú y NO consume el turno: el texto sigue su camino hacia el
// módulo con el contador desde cero (⚠️ SOLO real end-to-end tras el cableado).
func TestExitMenuChoice_EntradaNoNumerica_Desarma(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := e.armado(t, "PANTALLA-ORIGINAL-DEL-CARRITO")

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming("hola"))
	if err != nil {
		t.Fatalf("exitMenuChoice: %v", err)
	}
	if done {
		t.Fatal("una entrada que no es 1/2/3 NO debe consumir el turno")
	}
	if len(e.sender.texts()) != 0 {
		t.Fatal("una entrada que no es 1/2/3 no debe mandar nada por sí misma")
	}
	persisted, ok, err := e.repo.Load(context.Background(), e.key)
	if err != nil || !ok {
		t.Fatalf("releer estado: ok=%v err=%v", ok, err)
	}
	if _, armado := persisted.Vars[modules.ExitMenuVar].(string); armado {
		t.Fatal("la marca debe quedar DESARMADA aunque no se haya elegido nada")
	}
}

// TestExitMenuChoice_MarcadorDeOtroEvento_NoSecuestraElTurno: el menú de salida
// PERTENECE al evento sobre el que se armó. Si entre el menú y la respuesta la
// conversación cambió de evento por un camino que CONSERVA las Vars
// (saveMenuState, el menú del despachador), el marcador es basura: se desarma y el
// texto sigue su camino. Sin esta guarda, un «2» del despachador desactivaba el
// evento `menu` recién abierto («Listo, dejamos el menú por ahora») — medido.
func TestExitMenuChoice_MarcadorDeOtroEvento_NoSecuestraElTurno(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := e.armado(t, "PANTALLA-ORIGINAL-DEL-CARRITO")
	// El evento ACTIVO pasa a ser otro (el del menú), pero las Vars se conservan.
	otro := e.evs.seed(events.Event{
		ID: "t52-ev-menu", TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
		Kind: "menu", HistoryID: "menu-t52-history-id", Status: events.StatusOpen,
	})
	st.EventID = otro.ID
	if err := e.rt.store.Save(context.Background(), st); err != nil {
		t.Fatalf("guardar el cambio de contexto: %v", err)
	}

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming("2"))
	if err != nil {
		t.Fatalf("exitMenuChoice: %v", err)
	}
	if done {
		t.Fatal("un marcador de OTRO evento no debe consumir el turno")
	}
	if len(e.sender.texts()) != 0 {
		t.Fatalf("no debe confirmar nada; enviados=%v", e.sender.texts())
	}
	persisted, ok, err := e.repo.Load(context.Background(), e.key)
	if err != nil || !ok {
		t.Fatalf("releer estado: ok=%v err=%v", ok, err)
	}
	if persisted.EventID != otro.ID {
		t.Fatalf("el evento activo NO debe desactivarse; event_id=%q", persisted.EventID)
	}
	if _, armado := persisted.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("el marcador rancio debe quedar DESARMADO")
	}
	if got := e.evs.statusOf(otro.ID); got != events.StatusOpen {
		t.Fatalf("status del evento activo = %q, quiero open", got)
	}
}

// TestExitMenuChoice_EventoDesactivado_SeDesarma (E-3): la MISMA propiedad que
// TestExitMenuChoice_MarcadorDeOtroEvento_NoSecuestraElTurno, con la variante que esa
// no cubre — el evento se desactiva del todo (EventID="") en vez de conmutar a OTRO.
// Antes de esta corrección la guarda de arriba cortaba con st.EventID=="" ANTES de
// llegar al desarme, así que el marcador sobrevivía más de un turno, incumpliendo la
// promesa literal de ExitMenuVar («vive exactamente un turno»). Fuga sin daño
// demostrable HOY (volver al MISMO evento pasa por enterEventFlow, que borra el
// flow_state entero antes de que este código lo vea) pero real: cualquier camino
// futuro que reactive un evento CONSERVANDO Vars (el mismo patrón que ya existe para
// saveMenuState/stopEvent) heredaría un menú de salida ARMADO que nadie pidió.
func TestExitMenuChoice_EventoDesactivado_SeDesarma(t *testing.T) {
	t.Parallel()
	e := newT52Env(t)
	st := e.armado(t, "PANTALLA-ORIGINAL-DEL-CARRITO")
	// El evento se DESACTIVA del todo (event_stop/cancelación), pero las Vars se
	// conservan — el mismo hueco que saveMenuState/releaseStateFrom dejan abierto.
	st.EventID = ""
	if err := e.rt.store.Save(context.Background(), st); err != nil {
		t.Fatalf("guardar la desactivación: %v", err)
	}

	done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming("2"))
	if err != nil {
		t.Fatalf("exitMenuChoice: %v", err)
	}
	if done {
		t.Fatal("sin evento activo, el marcador rancio no debe consumir el turno")
	}
	if len(e.sender.texts()) != 0 {
		t.Fatalf("no debe confirmar nada; enviados=%v", e.sender.texts())
	}
	persisted, ok, err := e.repo.Load(context.Background(), e.key)
	if err != nil || !ok {
		t.Fatalf("releer estado: ok=%v err=%v", ok, err)
	}
	if persisted.EventID != "" {
		t.Fatalf("no se reactiva ningún evento; event_id=%q", persisted.EventID)
	}
	if _, armado := persisted.Vars[modules.ExitMenuVar]; armado {
		t.Fatal("E-3: el marcador debe quedar DESARMADO aunque la conversación ya no tenga evento activo")
	}
	if _, armado := persisted.Vars[modules.ExitMenuEventVar]; armado {
		t.Fatal("E-3: el sello del marcador también debe borrarse (DisarmExitMenu borra las DOS claves)")
	}
	if got := e.evs.statusOf("t52-ev-cart"); got != events.StatusOpen {
		t.Fatalf("status del evento cuyo menú quedó armado = %q, quiero open (nadie lo tocó)", got)
	}
}

// TestExitMenuChoice_MarcadorLegacySinSelloNoSecuestraElTurno (revisión de la Ola 6,
// defecto de E-3): un marcador armado ANTES de que la Ola 5 añadiera
// modules.ExitMenuEventVar —es decir, el estado de CUALQUIER flow_state que hoy corra
// en producción con el menú de salida armado— hace que ExitMenuArmedOn devuelva "".
// Al quitar E-3 la guarda `st.EventID == ""` del principio, ese "" casaba con una
// conversación SIN evento activo (event_stop y el vencimiento del reloj apagan el
// puntero CONSERVANDO las Vars) y el runtime interpretaba 1/2/3 sobre un marcador que
// ya no manda: «1» re-emitía una pantalla rancia, «2» consumía el turno en silencio
// absoluto (stopEvent con EventID=="" devuelve nil sin escribir ni decir nada) y «3»
// abría el despachador. Es literalmente el lado que el contrato de ExitMenuArmedOn
// declara inseguro («secuestrar un turno ajeno»).
//
// Las TRES entradas, no una: cada rama de exitMenuChoice hace un daño distinto, y
// elegir una sola dejaría las otras dos sin red.
func TestExitMenuChoice_MarcadorLegacySinSelloNoSecuestraElTurno(t *testing.T) {
	t.Parallel()
	for _, entrada := range []string{modules.ExitMenuKeepTrying, modules.ExitMenuStop, modules.ExitMenuDispatcher} {
		t.Run("entrada="+entrada, func(t *testing.T) {
			e := newT52Env(t)
			// Marcador LEGACY: ExitMenuVar SIN ExitMenuEventVar, sobre una conversación
			// cuyo evento ya se apagó (los dos caminos que conservan Vars).
			st := model.Conversation{
				TenantID: t52Tenant, SessionID: t52Session, ContactID: e.contactID,
				EventID: "",
				Vars:    map[string]any{modules.ExitMenuVar: "PANTALLA-RANCIA", "otro": "dato"},
			}
			if err := e.rt.store.Save(context.Background(), st); err != nil {
				t.Fatalf("sembrar flow_state legacy: %v", err)
			}

			done, err := e.rt.exitMenuChoice(context.Background(), e.key, t52Session, st, t52Incoming(entrada))
			if err != nil {
				t.Fatalf("exitMenuChoice: %v", err)
			}
			if done {
				t.Fatalf("un marcador legacy SIN sello, sobre una conversación sin evento, NO puede consumir el turno (entrada=%q): el texto es del módulo", entrada)
			}
			if len(e.sender.texts()) != 0 {
				t.Fatalf("tampoco puede hablar; enviados=%v", e.sender.texts())
			}
			persisted, ok, lerr := e.repo.Load(context.Background(), e.key)
			if lerr != nil || !ok {
				t.Fatalf("releer estado: ok=%v err=%v", ok, lerr)
			}
			if persisted.EventID != "" {
				t.Fatalf("no puede activar ningún evento; event_id=%q", persisted.EventID)
			}
			// Y la mitad que E-3 sí venía a arreglar: el marcador NO sobrevive al turno.
			if _, armado := persisted.Vars[modules.ExitMenuVar]; armado {
				t.Fatal("el marcador legacy debe quedar DESARMADO (E-3: vive exactamente un turno)")
			}
		})
	}
}
