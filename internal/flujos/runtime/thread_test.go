// thread_test.go — Plan 043 · Ola 4.5: los productores del hilo (T4.5.7, D-043.23)
// y la versión real del flujo en el nacimiento (T4.5.6), sobre los dobles en
// memoria. Criterio de la ola: NINGÚN test siembra ligaduras a mano — el evento no
// conoce a su solicitud y el hilo se alimenta por sus puertas.
package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ---------------------------------------------------------------------------
// T4.5.7a — el PersistSink escribe la fila `decision` (whitelist, best-effort)
// ---------------------------------------------------------------------------

// fakeDecisions es el doble del DecisionAppender: guarda cada payload por evento
// y puede fabricar el fallo best-effort.
type fakeDecisions struct {
	mu   sync.Mutex
	rows map[string][]json.RawMessage
	err  error
}

func (f *fakeDecisions) AppendDecision(_ context.Context, eventID string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.rows == nil {
		f.rows = map[string][]json.RawMessage{}
	}
	f.rows[eventID] = append(f.rows[eventID], append(json.RawMessage(nil), payload...))
	return nil
}

func (f *fakeDecisions) de(eventID string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.rows[eventID]...)
}

func (f *fakeDecisions) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, rs := range f.rows {
		n += len(rs)
	}
	return n
}

// ecConEvento es el EffectContext de un turno DENTRO de un evento vivo.
func ecConEvento(eventID string) runtime.EffectContext {
	return runtime.EffectContext{
		TenantID: testTenant, ContactID: "c-opaco", SessionID: testSession,
		FlowID: testFlow, FlowVersion: 1, EventID: eventID,
	}
}

// TestDecision_ItemAddedEscribeElPayloadPublico: la decisión arquetípica. Un
// item_added dentro de un evento vivo deja UNA fila `decision` con el payload
// PÚBLICO — la foto privada del carrito (PrivateKeys) NO entra en el hilo en
// claro, la misma poda que ya protege a flow_events (defecto A2 del Plan 041).
func TestDecision_ItemAddedEscribeElPayloadPublico(t *testing.T) {
	repo := store.NewMemoryRepository()
	fd := &fakeDecisions{}
	sink := persistSinkWith(repo).WithDecisionThread(fd)
	eff := modules.Effect{
		Kind: "event", Name: "item_added",
		Payload: map[string]any{
			"sku": "CAFE", "qty": 2,
			"items": []map[string]any{{"sku": "CAFE", "qty": 2}},
		},
		PrivateKeys: []string{"items"},
	}

	if err := sink.Handle(context.Background(), ecConEvento("ev-hilo-1"), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	rows := fd.de("ev-hilo-1")
	if len(rows) != 1 {
		t.Fatalf("esperaba UNA fila decision, hay %d", len(rows))
	}
	var got map[string]any
	if err := json.Unmarshal(rows[0], &got); err != nil {
		t.Fatalf("la decisión debe ser JSON estructurado: %v", err)
	}
	if got["sku"] != "CAFE" {
		t.Fatalf("el payload estructurado debe viajar entero: %v", got)
	}
	if _, filtrada := got["items"]; filtrada {
		t.Fatalf("una clave PRIVADA jamás entra en el hilo en claro: %v", got)
	}
}

// TestDecision_SurveyAnswerEscribeYAdemasProyecta: la respuesta de encuesta es
// decisión Y proyección — las dos filas, cada una por su puerta.
func TestDecision_SurveyAnswerEscribeYAdemasProyecta(t *testing.T) {
	repo := store.NewMemoryRepository()
	fd := &fakeDecisions{}
	sink := persistSinkWith(repo).WithDecisionThread(fd)
	eff := modules.Effect{Kind: "persist", Name: "survey_answer",
		Payload: map[string]any{"question_id": "q1", "answer_code": "si"}}

	if err := sink.Handle(context.Background(), ecConEvento("ev-hilo-2"), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fd.de("ev-hilo-2"); len(got) != 1 {
		t.Fatalf("la respuesta de encuesta es una decisión (D-043.23); filas=%d", len(got))
	}
	if res := repo.SurveyResults(); len(res) != 1 {
		t.Fatalf("y su proyección tipada sigue intacta; filas=%d", len(res))
	}
}

// TestDecision_FueraDeLaWhitelistNoEscribe: navegación y ciclo de vida NO son
// decisiones. cart_closed y cart_cancelled son la muerte del contenedor;
// cart_started y item_viewed, mirar sin decidir.
func TestDecision_FueraDeLaWhitelistNoEscribe(t *testing.T) {
	repo := store.NewMemoryRepository()
	fd := &fakeDecisions{}
	sink := persistSinkWith(repo).WithDecisionThread(fd)

	for _, name := range []string{"cart_started", "category_selected", "item_viewed", "cart_cancelled"} {
		eff := modules.Effect{Kind: "event", Name: name, Payload: map[string]any{}}
		if err := sink.Handle(context.Background(), ecConEvento("ev-hilo-3"), eff); err != nil {
			t.Fatalf("Handle(%s): %v", name, err)
		}
	}

	if got := fd.total(); got != 0 {
		t.Fatalf("ciclo de vida/navegación no producen decision; hay %d filas", got)
	}
}

// TestDecision_SinEventoVivoNoEscribe (D-043.23, «siempre que haya evento vivo»):
// un turno sin EventID no tiene hilo en el que vivir y no se inventa uno.
func TestDecision_SinEventoVivoNoEscribe(t *testing.T) {
	repo := store.NewMemoryRepository()
	fd := &fakeDecisions{}
	sink := persistSinkWith(repo).WithDecisionThread(fd)
	eff := modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "CAFE"}}

	if err := sink.Handle(context.Background(), ecConEvento(""), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := fd.total(); got != 0 {
		t.Fatalf("sin evento vivo no hay decision; hay %d filas", got)
	}
}

// TestDecision_KindPrivateJamasEntraEnClaro: un efecto de datos personales
// (buyer_data_captured) no produce fila `decision` aunque alguien lo añadiera a la
// whitelist por error — la guarda de KindPrivate es de plataforma, no de lista.
func TestDecision_KindPrivateJamasEntraEnClaro(t *testing.T) {
	repo := store.NewMemoryRepository()
	fd := &fakeDecisions{}
	sink := persistSinkWith(repo).WithDecisionThread(fd)
	eff := modules.Effect{Kind: modules.KindPrivate, Name: "buyer_data_captured",
		Payload: map[string]any{"key": "rut", "value": "11.111.111-1"}}

	// El proyector del carrito intentará cifrarlo; su resultado no es lo que se
	// afirma aquí — lo que se afirma es que el HILO en claro no vio nada.
	if err := sink.Handle(context.Background(), ecConEvento("ev-hilo-4"), eff); err != nil {
		t.Logf("proyección del dato privado (fuera de lo afirmado aquí): %v", err)
	}

	if got := fd.total(); got != 0 {
		t.Fatalf("un efecto KindPrivate jamás deja decision en claro; hay %d filas", got)
	}
}

// TestDecision_ElFalloEsBestEffort: si el hilo falla, el error SUBE (el dispatcher
// lo loguea y sigue — jamás tumba el turno) y la PROYECCIÓN corre igual: perder
// una fila de historial no puede costar una fila de negocio.
func TestDecision_ElFalloEsBestEffort(t *testing.T) {
	repo := store.NewMemoryRepository()
	fd := &fakeDecisions{err: errors.New("el hilo se cayó")}
	sink := persistSinkWith(repo).WithDecisionThread(fd)
	eff := modules.Effect{Kind: "persist", Name: "survey_answer",
		Payload: map[string]any{"question_id": "q1", "answer_code": "no"}}

	err := sink.Handle(context.Background(), ecConEvento("ev-hilo-5"), eff)
	if err == nil {
		t.Fatal("el fallo del hilo debe SUBIR para que el dispatcher lo loguee")
	}
	if res := repo.SurveyResults(); len(res) != 1 {
		t.Fatalf("y la proyección debe haber corrido igual; filas=%d", len(res))
	}
	if evs := repo.FlowEvents(); len(evs) != 1 {
		t.Fatalf("y el outbox también; filas=%d", len(evs))
	}
}

// TestDecision_SinCablearNoEscribeNada: la entrega de esta ola va SIN cablear
// (bootstrap prohibido) — un PersistSink sin WithDecisionThread se comporta byte a
// byte como antes.
func TestDecision_SinCablearNoEscribeNada(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo) // sin hilo
	eff := modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "CAFE"}}

	if err := sink.Handle(context.Background(), ecConEvento("ev-hilo-6"), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if evs := repo.FlowEvents(); len(evs) != 1 {
		t.Fatalf("el outbox de siempre sigue igual; filas=%d", len(evs))
	}
}

// ---------------------------------------------------------------------------
// T4.5.7b / T4.6 — el runtime persiste el literal del turno SOLO con llm_intake
// Y con el interruptor de despliegue messageThreadEnabled (D-043.23 + decisión
// de Jhoan del 2026-08-10) encendidos a la vez.
// ---------------------------------------------------------------------------

// newThreadRuntime arma el runtime del hilo: plano de eventos + resolver de
// entitlements (el gate de verdad, misma mecánica que buildSignal / ADR-0022) +
// el interruptor de despliegue msgEnabled (WithMessageThreadEnabled).
func newThreadRuntime(t *testing.T, feats *entitlements.Fake, msgEnabled bool, rules ...trigger.Rule) (*runtime.Runtime, *memEventStore, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEntitlements(feats),
		runtime.WithMessageThreadEnabled(msgEnabled))
	return rt, evs, contacts
}

// TestHilo_SinLaFeatureNoSeEscribeNiUnMensaje: el gate APAGADO es el estado por
// defecto de la entrega (D-043.23: lo enciende el plan que trae `llm_intake`; lo
// explota el Plan 044). Cero filas `message`, con conversación y evento vivos —
// AUNQUE el interruptor de despliegue esté encendido (msgEnabled=true): la
// feature sigue siendo, por sí sola, insuficiente y necesaria.
func TestHilo_SinLaFeatureNoSeEscribeNiUnMensaje(t *testing.T) {
	rt, evs, _ := newThreadRuntime(t, entitlements.NewFake(), true, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t457-a1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t457-a2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	if got := evs.totalMensajes(); got != 0 {
		t.Fatalf("sin llm_intake el hilo literal queda VACÍO; hay %d filas", got)
	}
}

// TestHilo_ConFeatureYFlagApagadoNoEscribeMensajePeroDecisionSigue: la SEGUNDA
// condición (decisión de Jhoan del 2026-08-10, WAPP_CONVERSATION_THREAD_MESSAGES)
// es igual de necesaria que la feature: con `llm_intake` ENCENDIDA pero el
// interruptor de despliegue en su default (false, sin WithMessageThreadEnabled),
// el turno dentro del evento no deja ni una fila `message`. El productor
// `decision` es OTRA puerta del mismo EventStore (PersistSink.WithDecisionThread,
// cableado aparte en el bootstrap) y el gate del hilo de mensajes no la toca —se
// demuestra escribiendo una decisión de verdad sobre el MISMO evento y viendo que
// llega, mientras `message` sigue en cero.
func TestHilo_ConFeatureYFlagApagadoNoEscribeMensajePeroDecisionSigue(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, false, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t457-f1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t457-f2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	if got := evs.mensajesDe(ev.ID); len(got) != 0 {
		t.Fatalf("con el flag apagado (default) el literal NO se escribe aunque la feature esté encendida; hay %d filas", len(got))
	}

	// `decision` no pasa por messageThreadEnabled: sigue llegando al mismo evento.
	sink := persistSinkWith(store.NewMemoryRepository()).WithDecisionThread(evs)
	eff := modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "CAFE"}}
	if err := sink.Handle(ctx, ecConEvento(ev.ID), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := len(evs.decisiones[ev.ID]); got != 1 {
		t.Fatalf("decision debe seguir escribiendo con el flag de message apagado; hay %d filas", got)
	}
}

// TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden: con la feature Y el
// interruptor de despliegue encendidos, el turno dentro del evento deja el
// literal del cliente (RoleClient) y las respuestas del negocio (RoleBusiness),
// en el orden de la conversación. El turno de ARRANQUE (la palabra que pare el
// evento) no pasa por advanceLive y —a propósito, mínimo de la ola— no se
// persiste.
func TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, true, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t457-b1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t457-b2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	rows := evs.mensajesDe(ev.ID)
	if len(rows) < 2 {
		t.Fatalf("esperaba el literal del cliente Y la respuesta del negocio; hay %d filas", len(rows))
	}
	if rows[0].role != events.RoleClient || rows[0].body != "ni idea" {
		t.Fatalf("la primera fila es la voz del CLIENTE con su literal, got %+v", rows[0])
	}
	for _, r := range rows[1:] {
		if r.role != events.RoleBusiness || r.body == "" {
			t.Fatalf("las siguientes son la voz del NEGOCIO con texto, got %+v", r)
		}
	}
	// Y nada fue a parar a otro evento (el turno de arranque no persistió).
	if evs.totalMensajes() != len(rows) {
		t.Fatalf("hay filas de hilo fuera del evento del turno: total=%d, del evento=%d",
			evs.totalMensajes(), len(rows))
	}
}

// TestHilo_ElTurnoQueCierraElFlujoPerteneceAlEvento: el turno que TERMINA el flujo
// apaga st.EventID en closeIfFinished, pero sus textos pertenecen al evento que
// estaba vivo mientras se hablaba — la misma regla del turnEventID que ya rige
// para los efectos (T4.5.1).
func TestHilo_ElTurnoQueCierraElFlujoPerteneceAlEvento(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, true, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t457-c1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	// «1» transiciona el menú a un message sin next: el flujo TERMINA y el cierre
	// natural sella el evento en este mismo turno.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t457-c2")); err != nil {
		t.Fatalf("turno de cierre: %v", err)
	}

	if got := evs.statuses()[ev.ID]; got != events.StatusClosed {
		t.Fatalf("montaje: el evento debía cerrar por fin natural, quedó %q", got)
	}
	rows := evs.mensajesDe(ev.ID)
	if len(rows) < 2 || rows[0].role != events.RoleClient || rows[0].body != "1" {
		t.Fatalf("el literal del turno del cierre debe colgar del evento que moría: %+v", rows)
	}
}

// TestHilo_SinEventoNoSeEscribeAunConFeature: la feature no basta — el literal
// solo vive DENTRO de un evento (D-043.23). Una conversación de keyword plana
// avanza sin dejar ni una fila.
func TestHilo_SinEventoNoSeEscribeAunConFeature(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	kw := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "pedido",
		MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
	rt, evs, _ := newThreadRuntime(t, feats, true, kw)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pedido", "t457-d1")); err != nil {
		t.Fatalf("keyword: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t457-d2")); err != nil {
		t.Fatalf("avance sin evento: %v", err)
	}

	if got := evs.totalMensajes(); got != 0 {
		t.Fatalf("sin evento no hay hilo que alimentar; hay %d filas", got)
	}
}

// ---------------------------------------------------------------------------
// T4.5.6 — birthEvent congela la VERSIÓN real del flujo
// ---------------------------------------------------------------------------

// TestBirthEvent_CongelaLaVersionRealDelFlujo (T4.5.6): el COMMENT de la columna
// promete «el flujo Y SU VERSIÓN con los que nació» y hasta esta ola toda fila
// nacía con 0. Se publica la definición DOS veces para que la vigente sea la 2:
// un birthEvent que pasara 0 (la mentira vieja) o un 1 cableado a mano quedan
// los dos en rojo.
func TestBirthEvent_CongelaLaVersionRealDelFlujo(t *testing.T) {
	repo := store.NewMemoryRepository()
	for i := 0; i < 2; i++ {
		if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
			t.Fatalf("publicar definición (%d): %v", i+1, err)
		}
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("carrito", "cart")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	evs := newMemEventStore(time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs))

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "carrito", "t456-v1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}

	alive := evs.alive()
	if len(alive) != 1 {
		t.Fatalf("debe nacer UN evento, hay %d", len(alive))
	}
	if alive[0].FlowVersion != 2 {
		t.Fatalf("el evento debe congelar la versión VIGENTE del flujo (2), y nació con %d", alive[0].FlowVersion)
	}
	if alive[0].FlowID != testFlow {
		t.Fatalf("y el flujo de siempre: %q", alive[0].FlowID)
	}
}
