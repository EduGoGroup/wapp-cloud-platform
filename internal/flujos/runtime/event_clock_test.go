package runtime_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ---------------------------------------------------------------------------
// Plan 043 · Ola 3 · T3.1 / T3.2 / T3.7 — EL reloj de conversación (ADR-0029 E-6)
// ---------------------------------------------------------------------------

// moduloEspia envuelve al módulo REAL y anota qué entradas le llegaron a Step. No
// es un módulo de mentira: delega en el carrito de verdad, así que el escenario
// avanza como en producción y lo único añadido es el testigo.
//
// Es lo que permite afirmar «el módulo NO recibió ese input» sin deducirlo de
// efectos indirectos: una conversación que no avanza puede no avanzar por muchas
// razones, y solo una de ellas es la que este plan arregla.
type moduloEspia struct {
	modules.Module
	mu      sync.Mutex
	entrada []string
}

func (m *moduloEspia) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	m.mu.Lock()
	m.entrada = append(m.entrada, input)
	m.mu.Unlock()
	return m.Module.Step(node, conv, input)
}

func (m *moduloEspia) recibido() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.entrada...)
}

// relojEventStore es el doble de eventos con el reloj MOVIBLE y con dos contadores
// que son el objeto de T3.7.2: cuántas veces se ESCRIBIÓ last_activity_at (Touch) y
// cuántas se LEYÓ para decidir un vencimiento (IsSuspended). Sin ellos, «el limbo no
// consume reloj» solo se podría afirmar leyendo el código.
type relojEventStore struct {
	*memEventStore
	toques      int
	evaluadas   int
	instantánea []time.Time // el valor con que se estampó cada Touch, en orden.
}

func nuevoRelojEventStore(inicio time.Time) *relojEventStore {
	return &relojEventStore{memEventStore: newMemEventStore(inicio)}
}

// en mueve el reloj del plano de eventos: es la única forma de fabricar 22 horas de
// silencio sin esperarlas.
func (s *relojEventStore) en(t time.Time) { s.now = t }

// ahora expone el reloj para inyectarlo TAMBIÉN en el runtime (rt.now), de modo que
// los dos relojes del motor no puedan discrepar dentro de un test.
func (s *relojEventStore) ahora() time.Time { return s.now }

func (s *relojEventStore) Touch(ctx context.Context, eventID string) error {
	s.toques++
	s.instantánea = append(s.instantánea, s.now)
	return s.memEventStore.Touch(ctx, eventID)
}

func (s *relojEventStore) IsSuspended(e events.Event, ttl time.Duration) bool {
	s.evaluadas++
	return s.memEventStore.IsSuspended(e, ttl)
}

// unicoEvento devuelve la única fila del almacén y falla si hay más de una: casi
// todos los criterios de esta ola dicen «el MISMO evento», y comprobar eso empieza
// por comprobar que no nació otro.
func unicoEvento(t *testing.T, evs *relojEventStore) events.Event {
	t.Helper()
	if got := evs.total(); got != 1 {
		t.Fatalf("esperaba UNA fila de evento y hay %d: %+v", got, evs.statuses())
	}
	return evs.alive()[0]
}

// assertSolicitudIntacta comprueba que sigue habiendo UNA solicitud open, que es la
// misma de antes (quiero == "" en la primera llamada, que devuelve su id) y que su
// línea sigue en la bitácora. Devuelve el id para encadenar las comprobaciones.
//
// La línea del pedido se comprueba por el flow_event item_added, que es lo que este
// plan promete y lo único que depende de él: la inactividad no vence ni vacía nada, así
// que la solicitud sigue abierta y su bitácora entera. NO se asevera aquí sobre
// intake_items a propósito — cuántas filas tenga una solicitud ABIERTA lo decide el
// carrito (cart/projection.go), no el reloj de conversación, y atar estos tests a eso
// los volvería rojos cada vez que ese módulo cambie de política sin que nada de esta
// ola se haya roto.
func assertSolicitudIntacta(t *testing.T, repo *store.MemoryRepository, quiero, cuando string) string {
	t.Helper()
	pedidos := repo.Intakes()
	if len(pedidos) != 1 || pedidos[0].Status != "open" {
		t.Fatalf("%s: debe haber UNA solicitud open: %+v", cuando, pedidos)
	}
	if quiero != "" && pedidos[0].ID != quiero {
		t.Fatalf("%s: debe ser LA MISMA solicitud (%s), y es %s", cuando, quiero, pedidos[0].ID)
	}
	if !hasFlowEvent(repo, "item_added") {
		t.Fatalf("%s: la línea del pedido sigue en la bitácora: %+v", cuando, repo.FlowEvents())
	}
	return pedidos[0].ID
}

// assertTodoSigueOpen fija por test la ausencia que define esta ola: no hay estado
// «expired» ni ningún otro valor de status fuera de open|closed|cancelled, y el paso
// del tiempo no mueve ninguna fila.
func assertTodoSigueOpen(t *testing.T, evs *relojEventStore) {
	t.Helper()
	for id, st := range evs.statuses() {
		if st != events.StatusOpen {
			t.Fatalf("vencer la inactividad NO toca el status: %s quedó %q (el estado «expired» no existe)", id, st)
		}
	}
}

// sembrarInactividad fija event_inactivity_ttl_seconds PARTIENDO DE LOS DEFAULTS
// (misma razón que seedConversationTTL en intent_signal_test.go: un TenantSettings
// construido a mano describe una configuración que producción no tiene, porque un 0
// en este campo significa «sin vencimiento» y no «sin configurar»).
func sembrarInactividad(t *testing.T, repo *store.MemoryRepository, inactividad, conversacional time.Duration) {
	t.Helper()
	s := store.DefaultTenantSettings(testTenant)
	s.EventInactivityTTL = inactividad
	s.ConversationTTL = conversacional
	repo.SetTenantSettings(s)
	got, err := repo.GetTenantSettings(context.Background(), testTenant)
	if err != nil {
		t.Fatalf("releer la config sembrada: %v", err)
	}
	if got.EventInactivityTTL != inactividad || got.ConversationTTL != conversacional {
		t.Fatalf("config sembrada = (inactividad %v · conversacional %v), quería (%v · %v)",
			got.EventInactivityTTL, got.ConversationTTL, inactividad, conversacional)
	}
}

// newRelojRuntime arma el runtime completo de la Ola 3: carrito REAL espiado, plano
// de eventos con reloj movible y el resolver de disparos con las reglas dadas. El
// reloj del runtime (rt.now) se inyecta aparte porque no siempre es el mismo que el
// del plano de eventos: el TTL conversacional se mide contra flow_state.updated_at,
// que el repositorio en memoria estampa con la hora REAL (igual que el now() de la
// columna en Postgres), así que los tests que lo ejercitan anclan su reloj ahí.
func newRelojRuntime(t *testing.T, inicio time.Time, ahora func() time.Time, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *relojEventStore, *moduloEspia,
) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(ctx, testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar cart flow: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar menu flow: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	espia := &moduloEspia{Module: cart.New()}
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(espia)
	eng := engine.New(reg, engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := nuevoRelojEventStore(inicio)
	if ahora == nil {
		ahora = evs.ahora
	}
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithEventSink(persistSinkWith(repo)),
		cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithClock(ahora))
	return rt, repo, sender, contacts, evs, espia
}

// cartEventRule es la regla event_start que ata la palabra «carrito» al tipo cart y
// al flujo del carrito: la puerta por la que el evento nace y por la que se rescata.
func cartEventRule() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testCartFlow, Enabled: true,
	}
}

// TestRelojInactividad_ChatInfinito (T3.1, REQ-08): el caso que decide la forma del
// reloj. El cliente abre su pedido a la HORA 1, vuelve a la HORA 23 y vuelve otra vez
// a la HORA 30, con un silencio tolerado de 24 h. El evento sigue vivo las tres veces
// porque lo que se mide es la INACTIVIDAD (22 h y 7 h), no la edad.
//
// La tercera visita no sobra, es la que hace que el test MIRE: con la regla que este
// plan rechaza —un plazo absoluto de 24 h desde created_at— las dos primeras visitas
// pasarían igual (a la hora 23 el evento solo tiene 22 h de vida) y solo la de la hora
// 30 lo delataría. Un test de dos visitas estaría en verde con las dos reglas.
func TestRelojInactividad_ChatInfinito(t *testing.T) {
	hora := func(h int) time.Time { return time.Date(2026, 3, 1, h, 0, 0, 0, time.UTC) }
	rt, repo, _, _, evs, espia := newRelojRuntime(t, hora(1), nil, cartEventRule())
	sembrarInactividad(t, repo, 24*time.Hour, 0)
	ctx := context.Background()

	// Hora 1: nace el evento y el carrito pinta las categorías.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.h1")); err != nil {
		t.Fatalf("hora 1: %v", err)
	}
	nacido := unicoEvento(t, evs)
	if !nacido.LastActivityAt.Equal(hora(1)) {
		t.Fatalf("el evento nace con last_activity_at = %v, quería %v", nacido.LastActivityAt, hora(1))
	}

	// Hora 23: 22 h de silencio contra 24 h de tolerancia ⇒ el evento sigue vivo, el
	// input llega al módulo y el reloj se adelanta.
	evs.en(hora(23))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.h23")); err != nil {
		t.Fatalf("hora 23: %v", err)
	}
	ev := unicoEvento(t, evs)
	if ev.Status != events.StatusOpen {
		t.Fatalf("a la hora 23 el evento debe seguir open, y está %q", ev.Status)
	}
	if !ev.LastActivityAt.Equal(hora(23)) {
		t.Fatalf("last_activity_at a la hora 23 = %v, quería %v (la interacción no refrescó el reloj)",
			ev.LastActivityAt, hora(23))
	}
	if got := espia.recibido(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("a la hora 23 el módulo debe recibir el input: %v", got)
	}

	// Hora 30: 7 h de silencio ⇒ sigue vivo, aunque el evento ya tenga 29 h de EDAD.
	evs.en(hora(30))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.h30")); err != nil {
		t.Fatalf("hora 30: %v", err)
	}
	ev = unicoEvento(t, evs)
	if ev.Status != events.StatusOpen {
		t.Fatalf("con 29 h de edad y 7 h de silencio el evento sigue vivo; está %q (¿plazo absoluto desde created_at?)", ev.Status)
	}
	if !ev.LastActivityAt.Equal(hora(30)) {
		t.Fatalf("last_activity_at a la hora 30 = %v, quería %v", ev.LastActivityAt, hora(30))
	}
	if got := espia.recibido(); len(got) != 2 {
		t.Fatalf("a la hora 30 el módulo debe recibir el segundo input: %v", got)
	}
	if !ev.CreatedAt.Equal(hora(1)) {
		t.Fatalf("created_at no se toca nunca: %v", ev.CreatedAt)
	}
}

// TestRelojInactividad_NoventaMinutos (T3.2, REQ-09 + T3.7.d): el caso de los 90
// minutos, con el silencio tolerado en 1 h. El cliente pide un café, se va, y a los
// 90 minutos escribe «2» creyendo que sigue eligiendo. Ese «2» NO es del carrito.
//
// Lo que este test exige, en el orden en que importa: el módulo no recibe el input;
// la fila del evento sigue `open` —no hay estado `expired`, ni lo habrá— y su
// solicitud conserva sus líneas; y decir «carrito» después rescata EL MISMO evento y
// le estampa el reloj en ese instante, igual que al nacer.
func TestRelojInactividad_NoventaMinutos(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	rt, repo, sender, contacts, evs, espia := newRelojRuntime(t, t0, nil, cartEventRule())
	sembrarInactividad(t, repo, time.Hour, 0)
	ctx := context.Background()

	// El pedido de verdad: «carrito» pare el evento y la navegación deja UNA línea en
	// la solicitud (Bebidas → Café → agregar → 2 unidades).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.n0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cartAddCafe(t, rt, "wamid.add")
	nacido := unicoEvento(t, evs)
	intakeID := assertSolicitudIntacta(t, repo, "", "tras armar el pedido")
	recibidoAntes := len(espia.recibido())
	enviadoAntes := sender.count()

	// 90 minutos de silencio contra 1 h de tolerancia: el «2» ya no es del carrito.
	evs.en(t0.Add(90 * time.Minute))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "wamid.n90")); err != nil {
		t.Fatalf("a los 90 minutos: %v", err)
	}
	if got := espia.recibido(); len(got) != recibidoAntes {
		t.Fatalf("el módulo NO debe recibir el input tras el vencimiento; recibió %v", got[recibidoAntes:])
	}
	if got := sender.count(); got != enviadoAntes {
		t.Fatalf("sin regla que case «2», el vencimiento no responde nada; envió %d mensajes", got-enviadoAntes)
	}
	vencido := unicoEvento(t, evs)
	if !vencido.LastActivityAt.Equal(t0) {
		t.Fatalf("un entrante que NO entra al evento no refresca su reloj: %v", vencido.LastActivityAt)
	}
	assertTodoSigueOpen(t, evs)
	assertSolicitudIntacta(t, repo, intakeID, "tras vencer la inactividad")

	// Y decir «carrito» rescata EL MISMO evento, con su solicitud, refrescando el reloj.
	evs.en(t0.Add(2 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.n120")); err != nil {
		t.Fatalf("rescate: %v", err)
	}
	rescatado := unicoEvento(t, evs)
	if rescatado.ID != nacido.ID {
		t.Fatalf("el rescate debe devolver EL MISMO evento (%s), y devolvió %s", nacido.ID, rescatado.ID)
	}
	if !rescatado.LastActivityAt.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("rescatar estampa last_activity_at = now() igual que nacer (T3.7.d): %v", rescatado.LastActivityAt)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != nacido.ID {
		t.Fatalf("tras el rescate el puntero debe volver al evento: %q", st.EventID)
	}
	// Rescatar devuelve evento Y pedido (REQ-26b): la MISMA solicitud, todavía open, sin
	// que el rescate abra una segunda ni cierre la primera.
	assertSolicitudIntacta(t, repo, intakeID, "tras el rescate")
	if got := sender.count(); got == enviadoAntes {
		t.Fatalf("el rescate debe volver a hablarle al cliente y no envió nada")
	}
}

// TestGuarda_ConEventoActivoElTTLConversacionalNoDescarta (T3.7.a, REQ-01c/INV-18):
// la regresión que la guarda arregla. Con un pedido en curso y un
// conversation_ttl_seconds ridículamente corto, el estado NO se descarta: con evento
// vivo manda event_inactivity_ttl_seconds y nadie más.
//
// El reloj del runtime se ancla a la hora REAL porque el TTL conversacional se mide
// contra flow_state.updated_at, que el repositorio estampa con now() como la columna
// de Postgres (mismo anclaje que TestConversationTTL_* en intent_signal_test.go).
func TestGuarda_ConEventoActivoElTTLConversacionalNoDescarta(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	unaHoraDespués := func() time.Time { return time.Now().Add(time.Hour) }
	rt, repo, _, contacts, evs, espia := newRelojRuntime(t, t0, unaHoraDespués, cartEventRule())
	// Inactividad holgada (24 h) y TTL conversacional de UN SEGUNDO: si el limbo
	// gobernara aquí, el estado moriría en el segundo entrante.
	sembrarInactividad(t, repo, 24*time.Hour, time.Second)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.g0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := unicoEvento(t, evs)
	antes := loadState(t, repo, resolveID(t, contacts, testContact))
	if antes.EventID != ev.ID {
		t.Fatalf("el estado debe apuntar al evento: %q", antes.EventID)
	}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.g1")); err != nil {
		t.Fatalf("segundo entrante: %v", err)
	}

	if got := espia.recibido(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("con evento activo el entrante avanza la conversación; el módulo recibió %v", got)
	}
	después := loadState(t, repo, resolveID(t, contacts, testContact))
	if después.EventID != ev.ID {
		t.Fatalf("el estado no se descarta ni pierde su evento: %q", después.EventID)
	}
	if después.FlowID != testCartFlow {
		t.Fatalf("sigue siendo la MISMA conversación del carrito: %q", después.FlowID)
	}
	if evs.toques != 1 {
		t.Fatalf("la interacción dentro del evento estampa su reloj UNA vez; toques=%d", evs.toques)
	}
}

// TestGuarda_SinEventoElTTLConversacionalSigueDescartando (T3.7.b): la otra mitad,
// que es cero regresión. Sin evento activo el TTL conversacional hace exactamente lo
// de siempre: descarta el flow_state y el entrante entra por el resolver de disparos.
//
// El discriminador es la palabra clave: con el estado VIVO, «ayuda» sería una entrada
// para el módulo (ResolveLive no evalúa keywords, INV-02); con el estado descartado,
// arranca el flujo del menú. Que el módulo no la vea es lo que prueba el descarte.
func TestGuarda_SinEventoElTTLConversacionalSigueDescartando(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	dosHorasDespués := func() time.Time { return time.Now().Add(2 * time.Hour) }
	arranque := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "hola",
		MatchType: trigger.MatchExact, FlowID: testCartFlow, Enabled: true,
	}
	ayuda := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "ayuda",
		MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
	rt, repo, sender, contacts, evs, espia := newRelojRuntime(t, t0, dosHorasDespués, arranque, ayuda)
	sembrarInactividad(t, repo, 24*time.Hour, time.Hour)
	ctx := context.Background()

	// Conversación SIN evento: la palabra clave arranca el flujo y no pare nada (E-6).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.b0")); err != nil {
		t.Fatalf("hola: %v", err)
	}
	if evs.total() != 0 {
		t.Fatalf("una keyword no pare eventos (E-6); hay %d filas", evs.total())
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != "" {
		t.Fatalf("la conversación está en el limbo y no debe tener evento: %q", st.EventID)
	}

	// Dos horas después (TTL conversacional de 1 h) llega «ayuda».
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "ayuda", "wamid.b1")); err != nil {
		t.Fatalf("ayuda: %v", err)
	}
	if got := espia.recibido(); len(got) != 0 {
		t.Fatalf("el estado vencido se descarta ANTES de avanzar; el módulo recibió %v", got)
	}
	final := loadState(t, repo, resolveID(t, contacts, testContact))
	if final.FlowID != testFlow {
		t.Fatalf("el entrante debe ir a handleTrigger y arrancar el flujo de la keyword; quedó en %q", final.FlowID)
	}
	if got := strings.Join(sender.texts(), "\n"); !strings.Contains(got, "1) Ventas") {
		t.Fatalf("debe verse el arranque del flujo nuevo: %q", got)
	}
}

// TestLimbo_NoConsumeElRelojDelEvento (T3.7.c y T3.7.2): el contacto saluda, tarda
// TRES HORAS en decidirse y el silencio tolerado son DOS. No vence nada, porque
// durante esas tres horas no había evento: el reloj de conversación empieza a contar
// cuando el evento NACE, no cuando alguien saludó.
//
// Los dos contadores son la prueba de que el limbo no toca conversation_events: cero
// escrituras de last_activity_at (Touch) y cero lecturas para decidir vencimiento
// (IsSuspended) hasta que el evento existe. Y al nacer, el evento se estampa con la
// hora de ESE instante, no con la del saludo.
func TestLimbo_NoConsumeElRelojDelEvento(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	saludo := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true,
	}
	rt, repo, _, contacts, evs, _ := newRelojRuntime(t, t0, nil, saludo, cartEventRule())
	// El reloj de evento en 2 h es el DEFAULT de plataforma (migración 0052); se siembra
	// explícito para que el test diga lo que mide.
	sembrarInactividad(t, repo, 2*time.Hour, 0)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola, buenas", "wamid.l0")); err != nil {
		t.Fatalf("saludo: %v", err)
	}
	if evs.total() != 0 {
		t.Fatalf("el saludo NO pare evento (E-6); hay %d filas", evs.total())
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != "" {
		t.Fatalf("el saludo deja la conversación en el limbo, sin evento: %q", st.EventID)
	}
	if evs.toques != 0 || evs.evaluadas != 0 {
		t.Fatalf("el limbo no toca conversation_events.last_activity_at: toques=%d evaluaciones=%d", evs.toques, evs.evaluadas)
	}

	// Tres horas de cháchara después —una hora MÁS que el silencio tolerado— el
	// contacto se decide.
	evs.en(t0.Add(3 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.l1")); err != nil {
		t.Fatalf("elección tardía: %v", err)
	}

	ev := unicoEvento(t, evs)
	if !ev.LastActivityAt.Equal(t0.Add(3*time.Hour)) || !ev.CreatedAt.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("el evento nace con el reloj de AHORA (created=%v last=%v), quería %v",
			ev.CreatedAt, ev.LastActivityAt, t0.Add(3*time.Hour))
	}
	if evs.toques != 0 {
		t.Fatalf("nacer no es refrescar: el INSERT ya estampa el reloj y no hace falta Touch; toques=%d (estampados en %v)",
			evs.toques, evs.instantánea)
	}
	if evs.evaluadas != 0 {
		t.Fatalf("no había evento anterior, así que no hay ningún vencimiento que evaluar; evaluaciones=%d", evs.evaluadas)
	}
	if events.IsSuspended(ev, 2*time.Hour, evs.ahora()) {
		t.Fatalf("un evento recién nacido no puede estar suspendido: last=%v ahora=%v", ev.LastActivityAt, evs.ahora())
	}
}
