package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// memEventStore es un doble EN MEMORIA del almacén de eventos que imita la única
// regla que el runtime no puede comprobar por su cuenta: el ÍNDICE ÚNICO PARCIAL
// (E-2). Por eso CreateEvent devuelve ErrAliveExists si ya hay uno vivo del tipo —si
// el doble no lo impusiera, los tests pasarían con un runtime que crea duplicados y
// solo se rompería contra Postgres.
type memEventStore struct {
	mu   sync.Mutex
	rows []events.Event
	seq  int
	now  time.Time
	// contactID lo fija el test que siembra filas a mano (seedAlive), que necesita
	// el contact_id opaco YA resuelto para que la fila case con la conversación.
	contactID string
	// descartados son los eventos cuya solicitud el dueño mandó a `abandoned`: siguen
	// `open` y ya no se rescatan (INV-17). Lo puebla descartar().
	descartados map[string]bool
	// resumenes imita conversation_event_messages con entry_kind='summary': por evento
	// y EN ORDEN, que es lo que permite comprobar que un segundo abandono añade una
	// fila en vez de pisar la primera (T3.4).
	resumenes map[string][]json.RawMessage
}

// AppendSummary imita la escritura del resumen y devuelve el `seq` que le tocó, igual
// que el MAX+1 de la tabla real.
func (m *memEventStore) AppendSummary(_ context.Context, eventID string, body json.RawMessage) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumenes == nil {
		m.resumenes = map[string][]json.RawMessage{}
	}
	m.resumenes[eventID] = append(m.resumenes[eventID], body)
	return len(m.resumenes[eventID]), nil
}

// resumenesDe devuelve los resúmenes escritos para un evento, en orden.
func (m *memEventStore) resumenesDe(eventID string) []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]json.RawMessage(nil), m.resumenes[eventID]...)
}

// totalResumenes cuenta TODAS las filas de resumen del almacén. Es lo que permite
// afirmar «cero filas» sin tener que saber a qué evento habrían ido a parar.
func (m *memEventStore) totalResumenes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, rs := range m.resumenes {
		n += len(rs)
	}
	return n
}

func newMemEventStore(now time.Time) *memEventStore {
	return &memEventStore{now: now}
}

func (m *memEventStore) CreateEvent(_ context.Context, in events.NewEvent) (events.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.rows {
		if e.Status == events.StatusOpen && e.TenantID == in.TenantID && e.SessionID == in.SessionID &&
			e.ContactID == in.ContactID && e.Kind == in.Kind {
			return events.Event{}, events.ErrAliveExists
		}
	}
	m.seq++
	ev := events.Event{
		ID: fmt.Sprintf("ev-%d", m.seq), TenantID: in.TenantID, SessionID: in.SessionID,
		ContactID: in.ContactID, Kind: in.Kind, HistoryID: events.HistoryID(in.Kind, m.now),
		Status: events.StatusOpen, FlowID: in.FlowID, IntakeID: in.IntakeID,
		CreatedAt: m.now, LastActivityAt: m.now,
	}
	m.rows = append(m.rows, ev)
	return ev, nil
}

func (m *memEventStore) GetAliveByKind(_ context.Context, tenantID, sessionID, contactID, kind string) (events.Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.rows {
		if e.Status == events.StatusOpen && e.TenantID == tenantID && e.SessionID == sessionID &&
			e.ContactID == contactID && e.Kind == kind {
			return e, true, nil
		}
	}
	return events.Event{}, false, nil
}

func (m *memEventStore) ListAlive(_ context.Context, tenantID, sessionID, contactID string) ([]events.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]events.Event, 0, len(m.rows))
	for _, e := range m.rows {
		if e.Status == events.StatusOpen && e.TenantID == tenantID && e.SessionID == sessionID && e.ContactID == contactID {
			out = append(out, e)
		}
	}
	return out, nil
}

// ListRescuable imita el filtro que el runtime NO puede comprobar por su cuenta
// (REQ-26c): un evento sigue `open` pero deja de ser rescatable en cuanto su solicitud
// se descarta. Aquí eso se representa con el conjunto `descartados`, que el test puebla
// con descartar() — el doble no tiene tabla de intakes, y lo que hay que poder fabricar
// es justo la diferencia entre «vivo» y «rescatable».
func (m *memEventStore) ListRescuable(_ context.Context, tenantID, sessionID, contactID string, limit int) ([]events.Rescuable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]events.Rescuable, 0, len(m.rows))
	for _, e := range m.rows {
		if e.Status != events.StatusOpen || e.TenantID != tenantID || e.SessionID != sessionID || e.ContactID != contactID {
			continue
		}
		if m.descartados[e.ID] {
			continue
		}
		out = append(out, events.Rescuable{Event: e})
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// descartar simula lo que hace el dueño desde su bandeja: manda la solicitud a
// `abandoned` SIN tocar el evento, que se queda `open`. Es el caso hostil de INV-17 —
// el que el predicado tiene que tapar por su cuenta.
func (m *memEventStore) descartar(eventID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.descartados == nil {
		m.descartados = map[string]bool{}
	}
	m.descartados[eventID] = true
}

// GetEventForTenant imita el SELECT acotado al tenant: cualquier ausencia —id
// inexistente o de otro tenant— es el MISMO ErrEventNotFound, igual que en el
// store real (la indistinción es parte del contrato de T4.2).
func (m *memEventStore) GetEventForTenant(_ context.Context, tenantID, eventID string) (events.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.rows {
		if e.ID == eventID && e.TenantID == tenantID {
			return e, nil
		}
	}
	return events.Event{}, fmt.Errorf("%w (id=%s)", events.ErrEventNotFound, eventID)
}

// setIntake liga a mano una solicitud al evento, como haría la proyección del
// carrito al parirla: es lo que permite fabricar un evento CON intake sin montar
// el módulo cart entero.
func (m *memEventStore) setIntake(eventID, intakeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].ID == eventID {
			m.rows[i].IntakeID = intakeID
			return
		}
	}
}

func (m *memEventStore) TransitionEvent(_ context.Context, eventID string, to events.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].ID == eventID {
			if m.rows[i].Status != events.StatusOpen {
				return events.ErrNotOpen
			}
			m.rows[i].Status = to
			m.rows[i].ClosedAt = m.now
			return nil
		}
	}
	return events.ErrNotOpen
}

func (m *memEventStore) Touch(_ context.Context, eventID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].ID == eventID {
			m.rows[i].LastActivityAt = m.now
			return nil
		}
	}
	return events.ErrEventMissing
}

func (m *memEventStore) IsSuspended(e events.Event, ttl time.Duration) bool {
	return events.IsSuspended(e, ttl, m.now)
}

// alive devuelve los eventos VIVOS y el total de filas: los tests que exigen «sigue
// habiendo una sola fila» necesitan distinguir «no se creó otra» de «se creó y se
// cerró», que es exactamente el error que el criterio persigue.
func (m *memEventStore) alive() []events.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]events.Event, 0, len(m.rows))
	for _, e := range m.rows {
		if e.Status == events.StatusOpen {
			out = append(out, e)
		}
	}
	return out
}

func (m *memEventStore) total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *memEventStore) statuses() map[string]events.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]events.Status{}
	for _, e := range m.rows {
		out[e.ID] = e.Status
	}
	return out
}

// newEventRuntime arma el runtime del Plan 043: el de siempre MÁS el plano de
// eventos. Devuelve también el doble del almacén para los asserts sobre filas.
func newEventRuntime(t *testing.T, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore) {
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
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 9, 13, 45, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs))
	return rt, repo, sender, contacts, evs
}

// eventStartRule arma una regla kind='event_start' de keyword exacta para el tipo dado.
func eventStartRule(keyword, kind string) trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: keyword,
		MatchType: trigger.MatchExact, EventKind: kind, FlowID: testFlow, Enabled: true,
	}
}

// ---------------------------------------------------------------------------
// T2.2 — inicio y salto por tipo
// ---------------------------------------------------------------------------

// TestEventStart_PareElEventoYNoDiceSuID (T2.2, E-3): «Carrito» con mayúsculas pare
// UNA fila open de kind cart con history_id bien formado, deja flow_state.event_id
// apuntándola, y la respuesta NO menciona el identificador.
func TestEventStart_PareElEventoYNoDiceSuID(t *testing.T) {
	rt, repo, sender, contacts, evs := newEventRuntime(t, eventStartRule("carrito", "cart"))

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "  CARRITO ", "wamid.e1")); err != nil {
		t.Fatalf("HandleIncoming event_start: %v", err)
	}

	alive := evs.alive()
	if len(alive) != 1 || alive[0].Kind != "cart" {
		t.Fatalf("event_start debe parir UNA fila open de tipo cart, got %+v", alive)
	}
	if want := "cart-2026-08-09-1345"; alive[0].HistoryID != want {
		t.Fatalf("history_id = %q, quiero %q", alive[0].HistoryID, want)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != alive[0].ID {
		t.Fatalf("flow_state.event_id = %q, quiero %q", st.EventID, alive[0].ID)
	}
	if sender.count() == 0 {
		t.Fatal("el nacimiento del evento debe responder algo al cliente")
	}
	// E-3: el history_id es para la bandeja del negocio, NUNCA para la conversación.
	for _, txt := range sender.texts() {
		if strings.Contains(txt, alive[0].HistoryID) || strings.Contains(txt, "cart-2026") {
			t.Fatalf("la respuesta NO debe llevar el history_id, y lleva: %q", txt)
		}
	}
}

// TestEventStart_EsIdempotentePorTipo (T2.2): decir «carrito» otra vez NO crea un
// segundo carrito. Es la garantía de que nadie acaba con carrito_001 y carrito_002.
func TestEventStart_EsIdempotentePorTipo(t *testing.T) {
	rt, _, _, _, evs := newEventRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.i1")); err != nil {
		t.Fatalf("primer carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.i2")); err != nil {
		t.Fatalf("segundo carrito: %v", err)
	}

	if got := evs.total(); got != 1 {
		t.Fatalf("dos veces «carrito» deben dejar UNA sola fila, hay %d", got)
	}
	if got := len(evs.alive()); got != 1 {
		t.Fatalf("y esa fila debe seguir viva, vivas=%d", got)
	}
}

// TestEventStart_SaltoPorTipoNoTocaNingunStatus (T2.2, D-043.4): «carrito» →
// «encuesta» → «carrito» deja DOS filas vivas, el puntero en la última pedida y
// NINGÚN status cambiado. Que el status no se toque es la mitad del criterio: el
// evento que deja de ser activo sigue open, no se pausa ni se cierra.
func TestEventStart_SaltoPorTipoNoTocaNingunStatus(t *testing.T) {
	rt, repo, _, contacts, evs := newEventRuntime(t,
		eventStartRule("carrito", "cart"),
		eventStartRule("encuesta", "survey"),
	)
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.s1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.s2")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}

	alive := evs.alive()
	if len(alive) != 2 {
		t.Fatalf("tras el salto debe haber DOS eventos vivos, hay %d (%+v)", len(alive), alive)
	}
	survey := aliveOfKind(t, evs, "survey")
	if st := loadState(t, repo, cid); st.EventID != survey.ID {
		t.Fatalf("el puntero debe apuntar a la encuesta (%q), y apunta a %q", survey.ID, st.EventID)
	}

	// Y de vuelta: sigue habiendo dos filas vivas y ningún status se movió.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.s3")); err != nil {
		t.Fatalf("vuelta al carrito: %v", err)
	}
	if got := len(evs.alive()); got != 2 {
		t.Fatalf("volver al carrito no debe cerrar la encuesta; vivas=%d", got)
	}
	cart := aliveOfKind(t, evs, "cart")
	if st := loadState(t, repo, cid); st.EventID != cart.ID {
		t.Fatalf("el puntero debe haber vuelto al carrito (%q), y apunta a %q", cart.ID, st.EventID)
	}
	for id, status := range evs.statuses() {
		if status != events.StatusOpen {
			t.Fatalf("ningún status debe cambiar en un salto por tipo; %s quedó en %q", id, status)
		}
	}
}

// aliveOfKind localiza el evento vivo del tipo dado o falla.
func aliveOfKind(t *testing.T, evs *memEventStore, kind string) events.Event {
	t.Helper()
	for _, e := range evs.alive() {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no hay evento vivo de tipo %q", kind)
	return events.Event{}
}

// ---------------------------------------------------------------------------
// T2.4 — event_stop y escape global: desactivan, no matan
// ---------------------------------------------------------------------------

// TestEventStop_DesactivaSinMatar (T2.4): event_stop apaga el puntero, la fila SIGUE
// open y la confirmación nombra el TIPO, no el identificador.
func TestEventStop_DesactivaSinMatar(t *testing.T) {
	stop := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "parar",
		MatchType: trigger.MatchExact, Enabled: true,
	}
	rt, repo, sender, contacts, evs := newEventRuntime(t, eventStartRule("carrito", "cart"), stop)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.p1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	before := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "parar", "wamid.p2")); err != nil {
		t.Fatalf("event_stop: %v", err)
	}

	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != "" {
		t.Fatalf("event_stop debe APAGAR el puntero, y vale %q", st.EventID)
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].Status != events.StatusOpen {
		t.Fatalf("event_stop NO mata: la fila debe seguir open, got %+v", alive)
	}
	if sender.count() != before+1 {
		t.Fatalf("event_stop debe confirmar con UN mensaje, envió %d", sender.count()-before)
	}
	last := sender.texts()[sender.count()-1]
	// El nombre es el MISMO que usa el despachador para rotular sus opciones. Se
	// afirma contra events.KindName y no contra la palabra copiada a mano: si el
	// despachador cambia su vocabulario, la confirmación debe cambiar con él, y un
	// literal aquí solo conseguiría que las dos voces se separaran sin que nadie lo
	// notara.
	tipo := events.KindName("cart")
	if tipo == "" || tipo == "cart" {
		t.Fatalf("KindName debe dar un nombre de cara al cliente, y dio %q", tipo)
	}
	if !strings.Contains(last, tipo) {
		t.Fatalf("la confirmación debe nombrar el tipo como %q, y dice %q", tipo, last)
	}
	if strings.Contains(last, alive[0].HistoryID) {
		t.Fatalf("la confirmación NO debe llevar el history_id (E-3): %q", last)
	}
}

// TestEscapeGlobal_ConservaSuSemantica (T2.4, INV-06): la palabra de escape sigue
// borrando el flow_state —su único cambio observable es que el evento deja de estar
// activo porque el puntero se va con la fila— y el evento sigue OPEN y recuperable.
func TestEscapeGlobal_ConservaSuSemantica(t *testing.T) {
	escape := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEscape, Keyword: "salir",
		MatchType: trigger.MatchExact, Enabled: true,
	}
	rt, repo, _, contacts, evs := newEventRuntime(t, eventStartRule("carrito", "cart"), escape)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.x1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "wamid.x2")); err != nil {
		t.Fatalf("escape: %v", err)
	}

	if _, ok, err := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact)}); err != nil {
		t.Fatalf("load: %v", err)
	} else if ok {
		t.Fatal("el escape global debe seguir BORRANDO el flow_state")
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].Status != events.StatusOpen {
		t.Fatalf("el escape NO mata el evento: debe seguir open, got %+v", alive)
	}
}

// ---------------------------------------------------------------------------
// T2.5 — nacimiento TARDÍO: el saludo no pare nada
// ---------------------------------------------------------------------------

// TestFallback_NoPareEvento (T2.5, E-6): un saludo que cae al fallback arranca el
// flujo del tenant y NO crea ninguna fila. Es el TIEMPO MUERTO — sin fila y sin reloj.
func TestFallback_NoPareEvento(t *testing.T) {
	fb := trigger.Rule{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true}
	rt, _, sender, _, evs := newEventRuntime(t, eventStartRule("carrito", "cart"), fb)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "hola Herminia cómo estás", "wamid.f1")); err != nil {
		t.Fatalf("saludo: %v", err)
	}

	if got := evs.total(); got != 0 {
		t.Fatalf("un saludo NO debe parir evento; hay %d filas", got)
	}
	if sender.count() != 1 {
		t.Fatalf("el fallback debe seguir arrancando su flujo (1 envío), envió %d", sender.count())
	}
}

// fallbackWithEventKind es un resolver que devuelve un Fallback CON event_kind
// poblado: la regla mal configurada contra la que el guard de startFromDecision
// existe. El ConfigResolver de hoy no puede producirla —su rama de fallback nunca
// mira event_kind—, así que sin este doble el guard sería inalcanzable y su test,
// decorativo: se verificó rompiéndolo a propósito y el test seguía en verde.
type fallbackWithEventKind struct{}

func (fallbackWithEventKind) Resolve(context.Context, string, string, trigger.Signal) (trigger.Decision, error) {
	return trigger.Decision{Action: trigger.Fallback, FlowID: testFlow, EventKind: "cart"}, nil
}

func (fallbackWithEventKind) IsEscape(context.Context, string, string, string) (bool, string, error) {
	return false, "", nil
}

func (fallbackWithEventKind) ResolveLive(context.Context, string, string, string) (trigger.Decision, error) {
	return trigger.Decision{Action: trigger.Ignore}, nil
}

// TestFallbackConEventKind_SigueSinParirEvento (T2.5, E-6): aunque la decisión
// llegue con un event_kind poblado, un Fallback NO pare evento. Quién puede parir lo
// decide el SITIO, no el dato: si esto cediera, cualquier saludo que cayera al
// fallback arrancaría el reloj de un evento que nadie pidió.
func TestFallbackConEventKind_SigueSinParirEvento(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	evs := newMemEventStore(time.Date(2026, 8, 9, 13, 45, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithTriggerResolver(fallbackWithEventKind{}),
		runtime.WithEventStore(evs))

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "hola", "wamid.g1")); err != nil {
		t.Fatalf("saludo: %v", err)
	}

	if got := evs.total(); got != 0 {
		t.Fatalf("un Fallback NO debe parir evento ni con event_kind poblado; hay %d filas", got)
	}
	if sender.count() != 1 {
		t.Fatalf("pero sí debe arrancar el flujo del fallback, y envió %d", sender.count())
	}
}

// TestKeywordNormal_NoPareEvento (T2.5): una keyword de siempre arranca su flujo y
// tampoco pare evento. Es la no-regresión del Plan 019 dentro de la Ola 2.
func TestKeywordNormal_NoPareEvento(t *testing.T) {
	kw := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword, Keyword: "pedido",
		MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
	rt, _, sender, _, evs := newEventRuntime(t, kw)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "pedido", "wamid.k1")); err != nil {
		t.Fatalf("keyword: %v", err)
	}
	if got := evs.total(); got != 0 {
		t.Fatalf("una keyword normal NO pare evento; hay %d filas", got)
	}
	if sender.count() != 1 {
		t.Fatalf("pero sí arranca su flujo, y envió %d", sender.count())
	}
}

// TestEventStartSobreElActivo_NoConsumeElTurno (T2.2, caso 1): decir «carrito»
// estando YA en el carrito no crea fila, no mueve el puntero y deja que el texto
// siga su curso hacia el módulo — quien dice «carrito» dentro del carrito está
// hablando con el carrito.
func TestEventStartSobreElActivo_NoConsumeElTurno(t *testing.T) {
	rt, repo, _, contacts, evs := newEventRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.n1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	before := loadState(t, repo, resolveID(t, contacts, testContact))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.n2")); err != nil {
		t.Fatalf("carrito otra vez: %v", err)
	}

	if got := evs.total(); got != 1 {
		t.Fatalf("no debe crear otra fila; hay %d", got)
	}
	after := loadState(t, repo, resolveID(t, contacts, testContact))
	if after.EventID != before.EventID {
		t.Fatalf("el puntero no debe moverse: %q → %q", before.EventID, after.EventID)
	}
}

// ---------------------------------------------------------------------------
// E-11 (ADR-0029, Enmienda 6) — empezar de nuevo cierra el VENCIDO, y solo el vencido
// ---------------------------------------------------------------------------

// seedAlive inyecta un evento vivo con la última actividad dada: es lo que permite
// fabricar un evento VENCIDO sin esperar dos horas.
func (m *memEventStore) seedAlive(kind, intakeID string, last time.Time) events.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	ev := events.Event{
		ID: fmt.Sprintf("ev-%d", m.seq), TenantID: testTenant, SessionID: testSession,
		ContactID: m.contactID, Kind: kind, HistoryID: events.HistoryID(kind, last),
		Status: events.StatusOpen, FlowID: testFlow, IntakeID: intakeID,
		CreatedAt: last, LastActivityAt: last,
	}
	m.rows = append(m.rows, ev)
	return ev
}

// fakeAbandoner registra a qué solicitudes se les pidió el abandono.
type fakeAbandoner struct {
	mu  sync.Mutex
	ids []string
	// err, si no es nil, es lo que devuelve AbandonIntake. Sirve para encender y
	// APAGAR el fallo dentro de un mismo test (la costura de fallo parcial y su
	// reparación al reintentar); el cero es «no falla», así que nadie más lo nota.
	err error
}

func (f *fakeAbandoner) AbandonIntake(_ context.Context, _, intakeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, intakeID)
	return f.err
}

// failWith enciende (o apaga, con nil) el fallo del abandono.
func (f *fakeAbandoner) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeAbandoner) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

// newE11Runtime arma el runtime con el abandono de solicitudes cableado y el reloj
// del store de eventos fijado en now.
func newE11Runtime(t *testing.T, now time.Time) (*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *memEventStore, *fakeAbandoner) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(now)
	ab := &fakeAbandoner{}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithEventStore(evs), runtime.WithIntakeAbandoner(ab))
	return rt, repo, contacts, evs, ab
}

// TestE11_EmpezarDeNuevoCierraElVencido: el caso de Marta. Su carrito del 1 de enero
// está vivo y VENCIDO; el 15 pulsa «empezar un pedido» y ese acto suyo lo cierra
// (cancelled) y abandona su solicitud, dejando sitio al nuevo. El pedido NO se borra.
func TestE11_EmpezarDeNuevoCierraElVencido(t *testing.T) {
	quince := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	rt, _, contacts, evs, ab := newE11Runtime(t, quince)
	cid := resolveID(t, contacts, testContact)
	evs.contactID = cid
	viejo := evs.seedAlive("cart", "intake-viejo", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))

	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}
	if _, err := rt.StartNewOfKind(context.Background(), key, testSession, "cart", testFlow, ""); err != nil {
		t.Fatalf("StartNewOfKind: %v", err)
	}

	if got := evs.statuses()[viejo.ID]; got != events.StatusCancelled {
		t.Fatalf("el vencido debe quedar cancelled, y quedó %q", got)
	}
	if got := ab.seen(); len(got) != 1 || got[0] != "intake-viejo" {
		t.Fatalf("su solicitud debe quedar abandonada, y se pidió %v", got)
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].ID == viejo.ID {
		t.Fatalf("debe quedar UN carrito vivo y ser el NUEVO, got %+v", alive)
	}
	if evs.total() != 2 {
		t.Fatalf("nada se borra (INV-09): deben quedar las DOS filas, hay %d", evs.total())
	}
}

// TestE11_NoCierraElQueSigueEnSuVentana: el límite, que es la mitad de la enmienda
// (E-11.3). Un carrito vivo y DENTRO de su ventana se conmuta, no se cierra: nadie
// pierde un pedido en curso por elegir mal una opción del menú.
func TestE11_NoCierraElQueSigueEnSuVentana(t *testing.T) {
	ahora := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	rt, _, contacts, evs, ab := newE11Runtime(t, ahora)
	cid := resolveID(t, contacts, testContact)
	evs.contactID = cid
	// Última actividad hace 10 minutos: muy dentro del TTL por defecto (2 h).
	vivo := evs.seedAlive("cart", "intake-vivo", ahora.Add(-10*time.Minute))

	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}
	if _, err := rt.StartNewOfKind(context.Background(), key, testSession, "cart", testFlow, ""); err != nil {
		t.Fatalf("StartNewOfKind: %v", err)
	}

	if got := evs.statuses()[vivo.ID]; got != events.StatusOpen {
		t.Fatalf("un evento dentro de su ventana NO se cierra; quedó %q", got)
	}
	if got := ab.seen(); len(got) != 0 {
		t.Fatalf("y su solicitud NO se abandona; se pidió %v", got)
	}
	if evs.total() != 1 {
		t.Fatalf("tampoco nace otro: se conmuta hacia el que había; hay %d filas", evs.total())
	}
}

// ---------------------------------------------------------------------------
// T2.3 — el cable del despachador: presentar el menú y despachar la elección
// ---------------------------------------------------------------------------

type fakeDispatcher struct{ menu events.Menu }

func (f fakeDispatcher) Build(context.Context, events.ConversationRef) (events.Menu, error) {
	return f.menu, nil
}

type fakeFlowForKind struct{ flow string }

func (f fakeFlowForKind) FlowForKind(context.Context, string, string, string) (string, error) {
	return f.flow, nil
}

// TestMenu_PresentaYDespachaLaEleccion: la palabra del menú presenta la lista y el
// número siguiente la despacha. Dos garantías que el criterio pide explícitamente: en
// el texto del menú NO aparece ningún identificador (E-3) y elegir «1) hacer un
// pedido» pare el evento de ese tipo, no otro.
func TestMenu_PresentaYDespachaLaEleccion(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("menu", "menu")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 9, 13, 45, 0, 0, time.UTC))
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()

	// (1) La palabra del menú: se pare el evento `menu` y se envía la lista.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "wamid.m1")); err != nil {
		t.Fatalf("palabra del menú: %v", err)
	}
	alive := evs.alive()
	if len(alive) != 1 || alive[0].Kind != "menu" {
		t.Fatalf("la palabra del menú debe parir el evento `menu`, got %+v", alive)
	}
	if sender.count() != 1 {
		t.Fatalf("debe enviarse la lista (1 mensaje), envió %d", sender.count())
	}
	rendered := sender.texts()[0]
	for _, ev := range alive {
		if strings.Contains(rendered, ev.HistoryID) || strings.Contains(rendered, ev.ID) {
			t.Fatalf("el menú NO debe llevar identificadores (E-3): %q", rendered)
		}
	}

	// (2) El número: despacha a un evento de tipo cart.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.m2")); err != nil {
		t.Fatalf("elección del menú: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != cart.ID {
		t.Fatalf("elegir el carrito debe dejarlo ACTIVO; event_id=%q, carrito=%q", st.EventID, cart.ID)
	}
	if sender.count() < 2 {
		t.Fatalf("elegir debe responder algo, envió %d en total", sender.count())
	}
}

// TestMenu_LoQueNoEsUnNumeroSigueSuCamino: el menú es un atajo, no una cárcel. Un
// texto que no casa ninguna opción no se traga el turno: cae al camino de siempre
// (aquí, el fallback del tenant) en vez de dejar al cliente en silencio.
func TestMenu_LoQueNoEsUnNumeroSigueSuCamino(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("menu", "menu"),
		{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 9, 13, 45, 0, 0, time.UTC))
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "wamid.o1")); err != nil {
		t.Fatalf("palabra del menú: %v", err)
	}
	before := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pues no sé", "wamid.o2")); err != nil {
		t.Fatalf("texto libre: %v", err)
	}

	if sender.count() <= before {
		t.Fatal("un texto que no es del menú no puede dejar al cliente en silencio")
	}
	// Y NO pare ningún evento: sigue habiendo solo el del menú.
	if got := evs.total(); got != 1 {
		t.Fatalf("un texto libre no debe parir evento; hay %d filas", got)
	}
}

// TestMenu_EmpezarUnoNuevoAplicaE11: el hueco que destapó la verificación al revés.
// Elegir «empezar uno nuevo» en el menú es el gesto NUEVO, así que sobre un evento
// vivo y VENCIDO aplica E-11 (lo cierra y abandona su solicitud). Sin este test se
// podía cambiar el gesto a «ve» y E-11 quedaba inalcanzable desde el único camino que
// puede dispararlo, con todo el paquete en verde.
func TestMenu_EmpezarUnoNuevoAplicaE11(t *testing.T) {
	quince := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("menu", "menu")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(quince)
	ab := &fakeAbandoner{}
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs), runtime.WithIntakeAbandoner(ab),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()

	// Un carrito del 1 de enero: vivo y muy vencido.
	evs.contactID = resolveID(t, contacts, testContact)
	viejo := evs.seedAlive("cart", "intake-viejo", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "wamid.e11a")); err != nil {
		t.Fatalf("palabra del menú: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.e11b")); err != nil {
		t.Fatalf("elegir empezar uno nuevo: %v", err)
	}

	if got := evs.statuses()[viejo.ID]; got != events.StatusCancelled {
		t.Fatalf("elegir «empezar uno nuevo» sobre un vencido debe cerrarlo (E-11); quedó %q", got)
	}
	if got := ab.seen(); len(got) != 1 || got[0] != "intake-viejo" {
		t.Fatalf("y su solicitud debe quedar abandonada; se pidió %v", got)
	}
	nuevo := aliveOfKind(t, evs, "cart")
	if nuevo.ID == viejo.ID {
		t.Fatal("debe nacer un carrito NUEVO, no reutilizarse el cerrado")
	}
}
