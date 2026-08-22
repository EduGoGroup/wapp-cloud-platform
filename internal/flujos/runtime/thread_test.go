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
// T4.5.7b + Plan 044 · T1.6 — el runtime persiste el literal del turno con UN
// SOLO gate: la feature `llm_intake` del tenant. El interruptor de despliegue —el
// booleano de config con su variable de entorno— se RETIRÓ el 2026-08-22: era
// andamiaje con fecha de caducidad y el 044 es esa fecha.
// ---------------------------------------------------------------------------

// newThreadRuntime arma el runtime del hilo: plano de eventos + resolver de
// entitlements (el gate de verdad, misma mecánica que buildSignal / ADR-0022).
//
// 🔴 YA NO RECIBE INTERRUPTOR, y esa ausencia es la mitad de T1.6: ninguno de los
// tests de abajo toca una variable de entorno ni una Option de despliegue para que
// el hilo escriba. Si alguien repone un segundo gate, estos tests son los que se
// ponen rojos.
func newThreadRuntime(t *testing.T, feats *entitlements.Fake, rules ...trigger.Rule) (*runtime.Runtime, *memEventStore, *contact.MemoryResolver) {
	t.Helper()
	return newThreadRuntimeCon(t, feats, nil, rules...)
}

// newThreadRuntimeCon es newThreadRuntime con Options extra, para los tests que
// necesitan además el recordatorio de la seña cableado (salientes fuera de turno).
func newThreadRuntimeCon(t *testing.T, feats *entitlements.Fake, extra []runtime.Option, rules ...trigger.Rule) (*runtime.Runtime, *memEventStore, *contact.MemoryResolver) {
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
	opts := append([]runtime.Option{
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEntitlements(feats),
	}, extra...)
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, evs, contacts
}

// TestHilo_SinLaFeatureNoSeEscribeNiUnMensaje: el gate es UNO y sigue siendo
// FAIL-CLOSED (D-043.23, Plan 044 · T1.6 criterio (b)). Un tenant sin `llm_intake`
// deja CERO filas con conversación y evento vivos. Lo que T1.6 retiró es el
// interruptor de despliegue; el gate por tenant no se movió.
//
// MUTACIÓN: en thread.go, en threadAllowed, sustituir el `return has` final por
// ESTAS DOS líneas:
//
//	_ = has
//	return true
//
// Este test se pone rojo (aparecen filas de un tenant sin la feature) y
// TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden sigue verde — que es justo lo
// que hace a esta pareja una pareja.
//
// 🔴 EL `_ = has` NO ES ADORNO. La mutación decía antes «cambiar el `return has` por
// `return true`» a secas, y así NO COMPILA: `has` queda declarada por el
// `has, err := rt.entitlements.Has(...)` y sin usar, que en Go es un error de
// compilación. Una mutación que no compila no dice nada de este test.
func TestHilo_SinLaFeatureNoSeEscribeNiUnMensaje(t *testing.T) {
	rt, evs, _ := newThreadRuntime(t, entitlements.NewFake(), eventStartRule("carrito", "cart"))
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

// TestHilo_LaFeatureBASTA_YElProductorDecisionSigueIgual: las DOS mitades de T1.6
// en un solo montaje, porque son la misma afirmación vista por sus dos caras.
//
//  1. LA FEATURE BASTA. Este runtime NO recibe ninguna Option de despliegue y NO
//     se lee ninguna variable de entorno: con `llm_intake` encendida y nada más, el
//     turno deja sus filas. Hasta el 2026-08-22 este mismo montaje daba CERO —hacía
//     falta además encender a mano el interruptor de despliegue— y ése era el bug
//     que dejaba al 044 sin materia prima. (Criterio (b) de T1.6.)
//  2. `decision` NO SE TOCÓ. Es OTRA puerta del mismo EventStore
//     (PersistSink.WithDecisionThread, cableada aparte en el bootstrap) y nunca pasó
//     por el interruptor retirado. Se demuestra escribiendo una decisión de verdad
//     sobre el MISMO evento y viendo que llega. Es la no-regresión que el criterio
//     (c) exige y la que el test viejo fijaba desde su línea del `decision`.
//
// MUTACIÓN: en thread.go, en persistTurnMessages, poner `return` como primera
// sentencia del cuerpo (antes del `if !rt.threadAllowed(...)`). La mitad (1) se
// pone roja —cero filas `message`— y la mitad (2) sigue VERDE, que es exactamente
// lo que prueba que las dos puertas son independientes.
func TestHilo_LaFeatureBASTA_YElProductorDecisionSigueIgual(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t457-f1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t457-f2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	// (1) La feature, sola, ENCIENDE el productor.
	if got := evs.mensajesDe(ev.ID); len(got) == 0 {
		t.Fatal("con llm_intake y SIN ninguna variable de entorno el turno debe dejar filas `message`; hay 0")
	}

	// (2) `decision` sigue llegando al mismo evento por su propia puerta.
	sink := persistSinkWith(store.NewMemoryRepository()).WithDecisionThread(evs)
	eff := modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{"sku": "CAFE"}}
	if err := sink.Handle(ctx, ecConEvento(ev.ID), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := len(evs.decisiones[ev.ID]); got != 1 {
		t.Fatalf("decision escribe por su propia puerta y T1.6 no la tocó; hay %d filas", got)
	}
}

// TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden: con la feature encendida —y
// nada más— el hilo del evento lleva el literal del cliente (RoleClient) y las
// respuestas del negocio (RoleBusiness), en el orden de la conversación, para LOS
// DOS turnos: el que ABRE el evento y el que avanza dentro de él.
//
// 🔧 REESCRITO EL 2026-08-22, Y NO PORQUE FALLARA. Este docstring decía que el turno
// de ARRANQUE «no pasa por advanceLive y —a propósito, mínimo de la ola— no se
// persiste», y esa frase describía un DEFECTO, no un mínimo: la palabra que pare el
// evento SÍ entra en `intake_jobs.source_refs` (observeForAggregation en
// startFromDecision), así que tener su referencia sin su literal componía un
// `source_text` que empezaba por el SEGUNDO mensaje, sin error. Plan 044 · T1.4 lo
// cerró con persistOpeningTurn, así que el arranque deja ahora sus DOS filas y las
// del turno de dentro se desplazan detrás. Lo que este test AFIRMA no cambió —cada
// voz con su literal y en el orden de la conversación—: cambiaron los índices.
//
// Y por eso se afirma también, explícitamente, que las filas del arranque ESTÁN: sin
// ese aserto, el desplazamiento se «arregla» borrando el turno de apertura y el test
// vuelve a verde sobre el defecto que acaba de cerrarse.
//
// MUTACIÓN: en thread.go, en persistTurnMessages, intercambiar events.RoleClient
// por events.RoleBusiness en la llamada del `clientText`. Se pone rojo en la primera
// aserción (la voz de la primera fila, que hoy es la del arranque).
//
// MUTACIÓN 2 (compila, y es la que vigila el desplazamiento): en thread.go, en
// persistOpeningTurn, cambiar la guarda `if !opening.FromClient` por
//
//	if opening.FromClient || true {
//
// El turno de apertura deja de escribirse (`opening` sigue usada, así que compila) y
// este test se pone rojo en el primer aserto: el hilo vuelve a empezar por «ni idea».
func TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t457-b1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t457-b2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	// DOS turnos × (una fila de cliente + una del negocio) = CUATRO filas. El arranque
	// renderiza el menú inicial (una salida) y «ni idea» provoca el reprompt (otra).
	rows := evs.mensajesDe(ev.ID)
	if len(rows) != 4 {
		t.Fatalf("esperaba las CUATRO filas —el turno que abre el evento y el que avanza dentro—; hay %d: %+v",
			len(rows), rows)
	}
	// (1) EL TURNO DE ARRANQUE ESTÁ EN EL HILO, y va primero. Es la mitad que faltaba:
	// su referencia ya estaba en la ventana de captación.
	if rows[0].role != events.RoleClient || rows[0].body != "carrito" {
		t.Fatalf("la primera fila es el literal con el que el CLIENTE abrió el evento, got %+v", rows[0])
	}
	if rows[1].role != events.RoleBusiness || rows[1].body == "" {
		t.Fatalf("y detrás, la pantalla inicial que contestó el NEGOCIO, got %+v", rows[1])
	}
	// (2) Y EL TURNO DE DENTRO va después, con la misma regla de voces y orden.
	if rows[2].role != events.RoleClient || rows[2].body != "ni idea" {
		t.Fatalf("el turno dentro del evento empieza por la voz del CLIENTE con su literal, got %+v", rows[2])
	}
	for _, r := range rows[3:] {
		if r.role != events.RoleBusiness || r.body == "" {
			t.Fatalf("las siguientes son la voz del NEGOCIO con texto, got %+v", r)
		}
	}
	// Y nada fue a parar a otro evento: los dos turnos cuelgan del MISMO hilo.
	if evs.totalMensajes() != len(rows) {
		t.Fatalf("hay filas de hilo fuera del evento del turno: total=%d, del evento=%d",
			evs.totalMensajes(), len(rows))
	}
}

// TestHilo_ElTurnoQueCierraElFlujoPerteneceAlEvento: el turno que TERMINA el flujo
// apaga st.EventID en closeIfFinished, pero sus textos pertenecen al evento que
// estaba vivo mientras se hablaba — la misma regla del turnEventID que ya rige
// para los efectos (T4.5.1).
//
// 🔧 ÍNDICES CORREGIDOS EL 2026-08-22 (Plan 044 · T1.4). Lo que este test afirma no
// cambió; lo que cambió es que el turno de ARRANQUE ya deja sus dos filas delante
// (persistOpeningTurn), así que el «1» del cierre pasó de rows[0] a rows[2]. Se
// afirma además que las filas del arranque siguen ahí, para que el desplazamiento no
// se pueda «arreglar» borrándolas.
//
// MUTACIÓN (compila): en incoming.go, en advanceLiveStep, cambiar el `turnEventID`
// de la llamada a persistTurnMessages por `st.EventID`. En este escenario
// closeIfFinished ya lo apagó, así que el literal del cierre se iría a un hilo de
// event_id vacío y el aserto de rows[2] queda rojo — mientras las dos filas del
// arranque siguen en su sitio, que es lo que distingue esta mutación de la de arriba.
func TestHilo_ElTurnoQueCierraElFlujoPerteneceAlEvento(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, eventStartRule("carrito", "cart"))
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
	// CUATRO filas: el turno que abrió el evento («carrito» + la pantalla inicial) y el
	// que lo cerró («1» + la despedida de Ventas).
	if len(rows) != 4 {
		t.Fatalf("esperaba el turno de apertura Y el del cierre colgando del mismo evento; hay %d: %+v",
			len(rows), rows)
	}
	// El turno de ARRANQUE sigue en su sitio: sin este par de asertos, borrarlo dejaría
	// el test verde y reabriría la referencia sin literal que T1.4 cerró.
	if rows[0].role != events.RoleClient || rows[0].body != "carrito" {
		t.Fatalf("el hilo empieza por el literal con el que se abrió el evento: %+v", rows[0])
	}
	if rows[1].role != events.RoleBusiness || rows[1].body == "" {
		t.Fatalf("y por lo que el negocio contestó al arrancar: %+v", rows[1])
	}
	// Y el turno del CIERRE cuelga del evento que moría, no de un hilo vacío.
	if rows[2].role != events.RoleClient || rows[2].body != "1" {
		t.Fatalf("el literal del turno del cierre debe colgar del evento que moría: %+v", rows)
	}
	if rows[3].role != events.RoleBusiness || rows[3].body == "" {
		t.Fatalf("junto con la respuesta que lo despidió: %+v", rows[3])
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
	rt, evs, _ := newThreadRuntime(t, feats, kw)
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
// Plan 044 · T1.6 (D-044.24) — los salientes FUERA DE TURNO entran al hilo,
// ROTULADOS. El emisor que se prueba aquí es el RECORDATORIO DE LA SEÑA: la
// coletilla arquetípica, la que sale por el Notifier de `intakes` sin que nazca
// de nada que el cliente pidiera.
// ---------------------------------------------------------------------------

// recordatorioQueManda es el recordatorio de la seña que SÍ manda algo. El spy de
// deposit_touch_test.go devuelve nil por defecto (el caso normal, «no procedía»);
// aquí se le carga un texto para poder observar la fila que deja en el hilo.
func recordatorioQueManda(texto string) *recordatorioSpy {
	return &recordatorioSpy{manda: []string{texto}}
}

// TestFueraDeTurno_ElRecordatorioDeSenaDejaSuFilaMARCADA: el criterio ejecutable de
// T1.6. El cliente escribe dentro de su evento, se le contesta, y DESPUÉS —en el
// defer, con el candado ya libre— sale el recordatorio de la seña. Ese texto tiene
// que quedar en el hilo del MISMO evento y en el cajón de los MARCADOS, no en el de
// las filas del turno.
//
// Las dos aserciones son inseparables y por eso están juntas: que la fila EXISTA
// (sin ella el «sí, esa» posterior no tendría antecedente) y que esté SEPARADA de
// las del turno (sin la marca, el LLM leería el recordatorio como si el cliente
// hubiera pedido algo).
//
// MUTACIÓN: en incoming.go, en touchDeposit, borrar la línea
// `rt.persistOutOfTurnMessage(ctx, tenantID, sessionID, eventID, enviados...)`.
// Compila —`enviados` se sigue usando en el `len(enviados) == 0` de arriba— y este
// test se pone rojo en la primera aserción.
//
// MUTACIÓN ALTERNATIVA, la que prueba la MARCA y no el cableado: en thread.go, en
// persistOutOfTurnMessage, cambiar `rt.events.AppendOutOfTurnMessage(ctx, eventID,
// text)` por `rt.events.AppendMessage(ctx, eventID, events.RoleBusiness, text)`.
// También compila (las dos devuelven `(int, error)`).
//
// 🔧 DÓNDE MUERE, corregido el 2026-08-22: el comentario decía «pone en rojo la
// segunda aserción sin tocar la primera», y es al revés — muere en la PRIMERA,
// porque con la fila desviada al cajón de `message` el cajón de los marcados queda
// vacío, `len(marcadas) != 1` y ese aserto es un `t.Fatalf`, que corta el test ahí
// mismo. El aserto que de verdad distingue esta mutación de la de arriba es el
// ÚLTIMO —el bucle sobre `mensajesDe`, que encuentra el recordatorio colado entre
// las filas del turno— y a ése no se llega. Se deja anotado en vez de reordenar los
// asertos: el orden actual (existe → tiene el literal → es del negocio → no está en
// el turno) es el orden en que se lee el criterio, y cambiarlo para acomodar una
// mutación sería escribir el test para la mutación y no para el criterio.
func TestFueraDeTurno_ElRecordatorioDeSenaDejaSuFilaMARCADA(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	const recordatorio = "Te recordamos la seña de tu pedido."
	rt, evs, _ := newThreadRuntimeCon(t, feats,
		[]runtime.Option{runtime.WithDepositReminder(recordatorioQueManda(recordatorio))},
		eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t16-a1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t16-a2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	marcadas := evs.fueraDeTurnoDe(ev.ID)
	if len(marcadas) != 1 {
		t.Fatalf("el recordatorio de la seña debe dejar UNA fila fuera de turno en el hilo del evento; hay %d", len(marcadas))
	}
	if marcadas[0].body != recordatorio {
		t.Fatalf("y con su literal: got %q", marcadas[0].body)
	}
	// La voz es la del NEGOCIO y la clava el store, no el llamante.
	if marcadas[0].role != events.RoleBusiness {
		t.Fatalf("un saliente es la voz del negocio; got %q", marcadas[0].role)
	}
	// Y NO se coló entre las filas del turno: si el recordatorio hubiera entrado
	// como `message`, aquí habría una fila de más y T1.4 no podría distinguirlas.
	for _, fila := range evs.mensajesDe(ev.ID) {
		if fila.body == recordatorio {
			t.Fatal("el recordatorio NO puede entrar como fila del turno: sin la marca, el LLM lo lee como pedido del cliente")
		}
	}
}

// TestFueraDeTurno_SinLaFeatureCeroFilasMarcadas: el gate `llm_intake` se aplica
// IGUAL a los salientes fuera de turno (INV-11 / ADR-0034). El montaje es idéntico
// al de arriba salvo por la feature, y el recordatorio se manda igual —al cliente
// le llega, eso no lo gobierna el hilo—: lo que no ocurre es la FILA.
//
// MUTACIÓN: en thread.go, en persistOutOfTurnMessage, borrar el
// `if !rt.threadAllowed(...) { return }` entero. Se pone rojo aquí y deja verde el
// test de arriba, que es lo que distingue «el gate falta» de «el productor falta».
func TestFueraDeTurno_SinLaFeatureCeroFilasMarcadas(t *testing.T) {
	rt, evs, _ := newThreadRuntimeCon(t, entitlements.NewFake(),
		[]runtime.Option{runtime.WithDepositReminder(recordatorioQueManda("Te recordamos la seña."))},
		eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t16-b1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ni idea", "t16-b2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	if got := evs.totalFueraDeTurno(); got != 0 {
		t.Fatalf("sin llm_intake un saliente fuera de turno tampoco deja fila; hay %d", got)
	}
}

// TestFueraDeTurno_SinEventoNoHayHiloQueMarcar: el primer entrante de una
// conversación no tiene estado vivo, así que no hay evento del que colgar el
// recordatorio — y no se inventa uno. Es el mismo criterio que ya rige para el
// turno (D-043.23) y el que deja fuera al saludo de sesión (E-6: el saludo no crea
// evento).
//
// # 🔧 REESCRITO EL 2026-08-22. LO QUE HABÍA NO PROBABA NADA
//
// Tenía dos defectos y los dos eran de fondo:
//
//  1. SU MUTACIÓN ERA INERTE. Decía «mover `depositEventID = st.EventID` a justo
//     después del `rt.store.Load(...)`, antes del `if !ok`». Compila, sí, pero en
//     ESTE escenario no cambia absolutamente nada: con un solo entrante no hay
//     estado que cargar, `Load` devuelve `ok=false` y el `st` de la rama es el valor
//     cero, así que `st.EventID` es "" tanto antes como después del `if`. La
//     asignación movida asigna vacío sobre vacío.
//  2. ERA UN NEGATIVO PURO. «Cero filas marcadas» pasaba también con el productor
//     ENTERO borrado, con el recordatorio sin cablear, o con el `DepositReminder`
//     devolviendo nil — que es lo que devuelve el 99,9 % de las veces. Un test que
//     no puede distinguir «la guarda funcionó» de «no pasó nada» no vigila la
//     guarda.
//
// Ahora afirma las TRES piezas del escenario, y el orden importa: el recordatorio
// SE EVALUÓ (el spy recibió su toque), TENÍA algo que mandar (por eso se monta con
// `recordatorioQueManda`), y aun así NO dejó fila — porque no había evento del que
// colgarla. Sin las dos primeras, la tercera no significa nada.
//
// MUTACIÓN (compila, y es la de verdad): en thread.go, en threadAllowed, quitar
// `eventID == ""` de la guarda, dejando
//
//	if rt.events == nil || rt.entitlements == nil {
//
// (`eventID` se queda como parámetro sin usar, que en Go compila). Sin esa
// condición, `persistOutOfTurnMessage` llama a `AppendOutOfTurnMessage` con el
// event_id VACÍO: en el doble aparece una fila bajo la clave "" y este test se pone
// rojo. En Postgres sería un `event_id` NOT NULL reventando en cada recordatorio
// disparado sin conversación viva — o, peor, si la columna lo admitiera, una fila
// de hilo huérfana que ningún `ListThread` volvería a encontrar.
//
// MUTACIÓN 2 (compila, y pone rojo el aserto del toque): en incoming.go, en
// touchDeposit, mover el `if rt.deposits == nil { return }` a envolver también la
// llamada a `RemindContact`… o más simple, cambiar la primera línea por
//
//	if rt.deposits == nil || eventID == "" {
//
// El recordatorio deja de EVALUARSE cuando no hay evento. Parece inocente y no lo
// es: rompe el criterio de Plan 041 · T4.4 —«pregunta por el CONTACTO, no por la
// conversación»—, que existe porque el cliente puede deber la seña de un pedido de
// la semana pasada y escribir hoy un «hola» sin carrito abierto. Ese caso dejaría de
// recordarse, y sin el aserto del toque nadie lo vería.
func TestFueraDeTurno_SinEventoNoHayHiloQueMarcar(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	// El spy se guarda: sin él no se puede distinguir «la guarda paró la fila» de
	// «el recordatorio ni siquiera corrió».
	spy := recordatorioQueManda("Te recordamos la seña.")
	rt, evs, _ := newThreadRuntimeCon(t, feats,
		[]runtime.Option{runtime.WithDepositReminder(spy)},
		eventStartRule("carrito", "cart"))

	// UN SOLO entrante: el que PARE el evento. No hay estado previo que cargar, así
	// que el turno pasa por handleTrigger y el recordatorio corre sin evento.
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "carrito", "t16-c1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}

	// (1) EL RECORDATORIO SE EVALUÓ. Es el control positivo: el defer de
	// HandleIncoming corre igual sin conversación viva, porque la seña se debe por
	// contacto y no por conversación (Plan 041 · T4.4).
	if got := spy.count(); got != 1 {
		t.Fatalf("el recordatorio tenía que evaluarse UNA vez aunque no hubiera evento; toques=%d "+
			"(con 0, el «cero filas» de abajo no prueba la guarda: prueba que no pasó nada)", got)
	}

	// (2) Y NO SE COLÓ POR LA OTRA PUERTA. El doble está cargado con un texto a
	// propósito —con el `nil` del caso por defecto no habría nada que impedir—, así
	// que hay un saliente REAL buscando dónde escribirse; lo que se comprueba aquí
	// es que tampoco acabó en el cajón de las filas del turno bajo un event_id
	// vacío, que es la otra forma de que apareciera un huérfano.
	if len(evs.mensajesDe("")) != 0 {
		t.Fatalf("tampoco puede colarse como fila del turno bajo un event_id vacío: %+v", evs.mensajesDe(""))
	}

	// (3) Y AUN ASÍ NO HAY FILA MARCADA, en ningún evento.
	if got := evs.totalFueraDeTurno(); got != 0 {
		t.Fatalf("sin evento no hay hilo que marcar; hay %d filas", got)
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
