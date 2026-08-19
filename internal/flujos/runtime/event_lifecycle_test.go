// event_lifecycle_test.go — Plan 043 · Ola 4: cierre NATURAL (T4.1) y cancelación
// por id (T4.2-core + T4.3) sobre los dobles en memoria. La verdad de BD (guard
// CAS, closed_at sellado por SQL, índice único parcial) se fija aparte contra
// Postgres real en event_lifecycle_integration_test.go.
package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// avisoFlow es un flujo DE UN SOLO PASO: un message sin next, que deja la
// conversación en el centinela en el propio Enter. Es el caso «flujo de un solo
// nodo» del cierre natural (T4.1): el evento nace y su flujo ya terminó.
func avisoFlow() model.Flow {
	return model.Flow{
		FlowID:  "aviso-unico",
		Initial: "solo",
		Nodes: map[string]model.Node{
			"solo": {Type: model.NodeTypeMessage, Text: "Te aviso en cuanto abra."},
		},
	}
}

// newLifecycleRuntime arma el runtime de la Ola 4: plano de eventos + abandono de
// solicitudes + reglas del tenant, sobre los dobles de siempre.
func newLifecycleRuntime(t *testing.T, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *memEventStore, *fakeAbandoner) {
	t.Helper()
	evs := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	return newLifecycleRuntimeConStore(t, evs, evs, rules...)
}

// ---------------------------------------------------------------------------
// T4.1 — cierre natural: el fin del flujo mata el evento (open→closed)
// ---------------------------------------------------------------------------

// TestCierreNatural_CompletarElFlujoCierraElEvento (T4.1): completar el flujo del
// evento deja la fila en closed con closed_at sellado y el puntero apagado; el
// tipo queda LIBRE (decir la palabra otra vez pare OTRA fila) y la fila cerrada
// sigue existiendo (INV-09: nada se borra).
//
// El assert intermedio —tras nacer, el evento sigue open y el puntero puesto— es
// el GUARD del cierre: sin él, un closeIfFinished que cerrara sin mirar
// st.Finished() pasaría el resto del test igual. Verificado por mutación.
func TestCierreNatural_CompletarElFlujoCierraElEvento(t *testing.T) {
	rt, repo, contacts, evs, _ := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-w1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	// GUARD: a medio flujo NADIE cierra nada.
	alive := evs.alive()
	if len(alive) != 1 || alive[0].Status != events.StatusOpen {
		t.Fatalf("a medio flujo el evento debe seguir open, got %+v", alive)
	}
	nacido := alive[0]
	if st := loadState(t, repo, cid); st.EventID != nacido.ID {
		t.Fatalf("a medio flujo el puntero debe seguir puesto (%q), y vale %q", nacido.ID, st.EventID)
	}

	// «1» transiciona el menú a un message sin next ⇒ el flujo TERMINA.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t41-w2")); err != nil {
		t.Fatalf("completar: %v", err)
	}
	if got := evs.statuses()[nacido.ID]; got != events.StatusClosed {
		t.Fatalf("el fin del flujo debe dejar el evento closed, y quedó %q", got)
	}
	if fila := eventByID(t, evs, nacido.ID); fila.ClosedAt.IsZero() {
		t.Fatal("closed_at debe quedar sellado en el cierre natural")
	}
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("el puntero debe quedar apagado tras el cierre, y vale %q", st.EventID)
	}

	// El tipo quedó libre: «carrito» otra vez pare OTRA fila (el único parcial ya
	// no colisiona) y la cerrada sigue existiendo.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-w3")); err != nil {
		t.Fatalf("carrito nuevo: %v", err)
	}
	if got := evs.total(); got != 2 {
		t.Fatalf("debe haber DOS filas (la cerrada sobrevive, INV-09), hay %d", got)
	}
	vivos := evs.alive()
	if len(vivos) != 1 || vivos[0].ID == nacido.ID {
		t.Fatalf("debe haber UN cart vivo y ser el NUEVO, got %+v", vivos)
	}
}

// TestCierreNatural_FlujoDeUnSoloNodo (T4.1): un evento cuyo flujo termina en el
// propio Enter (message sin next) nace y se cierra en el mismo turno: fila closed
// con closed_at, puntero jamás colgando.
func TestCierreNatural_FlujoDeUnSoloNodo(t *testing.T) {
	regla := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "aviso",
		MatchType: trigger.MatchExact, EventKind: "media", FlowID: "aviso-unico", Enabled: true,
	}
	rt, repo, contacts, evs, _ := newLifecycleRuntime(t, regla)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "aviso", "t41-s1")); err != nil {
		t.Fatalf("aviso: %v", err)
	}

	if got := evs.total(); got != 1 {
		t.Fatalf("debe existir UNA fila (el evento nació), hay %d", got)
	}
	if got := len(evs.alive()); got != 0 {
		t.Fatalf("y debe haber nacido YA cerrada (su flujo terminó al entrar); vivas=%d", got)
	}
	for id, status := range evs.statuses() {
		if status != events.StatusClosed {
			t.Fatalf("el cierre natural es closed, no %q (id=%s)", status, id)
		}
		if fila := eventByID(t, evs, id); fila.ClosedAt.IsZero() {
			t.Fatal("closed_at debe quedar sellado")
		}
	}
	if st := loadState(t, repo, resolveID(t, contacts, testContact)); st.EventID != "" {
		t.Fatalf("el puntero no debe sobrevivir apuntando a un cerrado: %q", st.EventID)
	}
}

// TestCierreNatural_CarreraBenignaConOtraMuerte (T4.1): si OTRO escritor selló la
// muerte entre el Step y el cierre (una cancelación desde la app), el ErrNotOpen
// es benigno: el puntero se apaga igual y la muerte ajena NO se pisa.
func TestCierreNatural_CarreraBenignaConOtraMuerte(t *testing.T) {
	rt, repo, contacts, evs, _ := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-r1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	// El otro escritor gana: la fila ya está cancelled cuando el flujo termine.
	if err := evs.TransitionEvent(ctx, ev.ID, events.StatusCancelled); err != nil {
		t.Fatalf("sellar la muerte ajena: %v", err)
	}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t41-r2")); err != nil {
		t.Fatalf("completar sobre la carrera debe ser benigno: %v", err)
	}
	if got := evs.statuses()[ev.ID]; got != events.StatusCancelled {
		t.Fatalf("la muerte ajena NO se pisa: debe seguir cancelled, quedó %q", got)
	}
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("el puntero se apaga igual (apunta a un muerto): %q", st.EventID)
	}
}

// fallaTransicion suplanta al almacén para que TransitionEvent falle con un error
// REAL (no ErrNotOpen) mientras err esté puesto. Todo lo demás delega.
type fallaTransicion struct {
	*memEventStore
	err error
}

func (f *fallaTransicion) TransitionEvent(ctx context.Context, eventID string, to events.Status) error {
	if f.err != nil {
		return f.err
	}
	return f.memEventStore.TransitionEvent(ctx, eventID, to)
}

// TestCierreNatural_FalloRealNoLimpiaElPuntero (T4.1, E-8 §4): si la transición
// falla con un error real, el puntero NO se limpia (un solo hecho) y el turno NO
// se aborta. El siguiente entrante REINTENTA el cierre y lo consigue.
func TestCierreNatural_FalloRealNoLimpiaElPuntero(t *testing.T) {
	evsBase := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	rota := &fallaTransicion{memEventStore: evsBase}
	rt, repo, contacts, _, _ := newLifecycleRuntimeConStore(t, rota, evsBase, eventStartRule("carrito", "cart"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-f1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evsBase.alive()[0]

	rota.err = errors.New("la BD se fue a almorzar")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t41-f2")); err != nil {
		t.Fatalf("el fallo del cierre NO aborta el turno (best-effort con reintento): %v", err)
	}
	if got := evsBase.statuses()[ev.ID]; got != events.StatusOpen {
		t.Fatalf("con la transición rota el evento sigue open, quedó %q", got)
	}
	if st := loadState(t, repo, cid); st.EventID != ev.ID {
		t.Fatalf("y el puntero NO se limpia (un solo hecho, E-8 §4): vale %q", st.EventID)
	}

	// La BD vuelve: el siguiente entrante —aunque el flujo ya esté terminado y no
	// conteste nada— reintenta el cierre pendiente y lo sella.
	rota.err = nil
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "t41-f3")); err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if got := evsBase.statuses()[ev.ID]; got != events.StatusClosed {
		t.Fatalf("el reintento debe cerrar el evento, quedó %q", got)
	}
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("y apagar el puntero: %q", st.EventID)
	}
}

// newLifecycleRuntimeConStore es newLifecycleRuntime con un EventStore suplantado
// que ENVUELVE al memEventStore dado (para fabricar fallos sin perder los asserts).
func newLifecycleRuntimeConStore(t *testing.T, wired runtime.EventStore, evs *memEventStore, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *memEventStore, *fakeAbandoner) {
	t.Helper()
	repo := store.NewMemoryRepository()
	for _, f := range []model.Flow{sampleFlow(), avisoFlow()} {
		if _, err := repo.InsertDefinition(context.Background(), testTenant, f); err != nil {
			t.Fatalf("sembrar definición %s: %v", f.FlowID, err)
		}
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	ab := &fakeAbandoner{}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(wired),
		runtime.WithIntakeAbandoner(ab))
	return rt, repo, contacts, evs, ab
}

// TestCierreNatural_NoTocaElIntake (T4.1, nota de la ola): el cierre natural JAMÁS
// abandona la solicitud — eso ya lo hace la proyección de cart_closed por su
// camino, y abandonar es SOLO de la cancelación (T4.3). Con AbandonByEvent
// (T4.5.5a) la garantía se afirma igual: el abandonador no recibe NI UNA llamada.
func TestCierreNatural_NoTocaElIntake(t *testing.T) {
	rt, _, _, evs, ab := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-i1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t41-i2")); err != nil {
		t.Fatalf("completar: %v", err)
	}
	if got := evs.statuses()[ev.ID]; got != events.StatusClosed {
		t.Fatalf("el evento debe quedar closed, quedó %q", got)
	}
	if got := ab.seen(); len(got) != 0 {
		t.Fatalf("el cierre natural NO abandona la solicitud, y se pidió %v", got)
	}
}

// eventByID localiza una fila por id en el doble, viva o muerta.
func eventByID(t *testing.T, evs *memEventStore, id string) events.Event {
	t.Helper()
	evs.mu.Lock()
	defer evs.mu.Unlock()
	for _, e := range evs.rows {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no existe la fila %q", id)
	return events.Event{}
}

// ---------------------------------------------------------------------------
// T4.2-core + T4.3 — cancelación por id acotada al tenant
// ---------------------------------------------------------------------------

// TestCancel_CaminoFeliz (T4.2/T4.3): cancelar un evento open lo deja cancelled
// con closed_at, abandona su solicitud y apaga el puntero de SU conversación.
func TestCancel_CaminoFeliz(t *testing.T) {
	rt, repo, contacts, evs, ab := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-c1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	got, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("CancelEventForTenant: %v", err)
	}
	if got.Status != events.StatusCancelled || got.ClosedAt.IsZero() {
		t.Fatalf("la fila devuelta debe venir cancelled y con closed_at, got %+v", got)
	}
	if s := evs.statuses()[ev.ID]; s != events.StatusCancelled {
		t.Fatalf("la fila del almacén debe quedar cancelled, quedó %q", s)
	}
	if vistos := ab.seen(); len(vistos) != 1 || vistos[0] != ev.ID {
		t.Fatalf("el abandono debe pedirse POR EVENTO (%q, T4.5.5a; jamás borrar, INV-09): %v", ev.ID, vistos)
	}
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("el puntero de su conversación debe quedar apagado: %q", st.EventID)
	}
}

// TestCancel_EsIdempotenteSobreTerminales (T4.2): la segunda llamada devuelve la
// fila TAL CUAL —mismo closed_at— y sin error. Lo que NO cambia es la FILA DEL
// EVENTO; los efectos colaterales SÍ se vuelven a pedir, y eso es la reparación de
// la costura de fallo parcial (repairCancelled): pedirlos otra vez es lo único que
// arregla una cancelación que se quedó a medias, y repetirlos sobre una que salió
// bien no escribe nada —AbandonByEvent es idempotente por contrato (cero filas =
// éxito) y releaseStateFrom no toca un puntero ya apagado—.
// Verificado por mutación (tratar ErrNotOpen como error real pone esto en rojo).
func TestCancel_EsIdempotenteSobreTerminales(t *testing.T) {
	rt, _, _, evs, ab := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-d1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	primera, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("primera cancelación: %v", err)
	}
	segunda, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("la segunda cancelación debe ser idempotente, no un error: %v", err)
	}
	if segunda.Status != events.StatusCancelled || !segunda.ClosedAt.Equal(primera.ClosedAt) {
		t.Fatalf("la fila NO cambia: primera %+v vs segunda %+v", primera, segunda)
	}
	if vistos := ab.seen(); len(vistos) != 2 || vistos[1] != ev.ID {
		t.Fatalf("el reintento debe RE-pedir el abandono por evento (reparación de la costura): %v", vistos)
	}
}

// TestCancel_ElReintentoReparaElAbandonoQueFalló (T4.3, hallazgo del barrido CLI del
// 2026-08-10): la costura «transición-ok / abandono-falla» ya no deja una solicitud
// huérfana sin salida.
//
// Por qué importaba: el fallo deja el evento `cancelled` y la solicitud `open`, y esa
// solicitud TAMPOCO se puede descartar a mano —POST /api/v1/intakes/discard la salta
// con `live_event`, porque su guarda mira el `cart` del flow_state y no el estado del
// evento; comprobado contra Postgres real—. Con la rama idempotente antigua, que no
// tocaba nada, las dos puertas quedaban cerradas y hacía falta un reconciliador. Con
// esta, el reintento del dueño ES la reparación.
func TestCancel_ElReintentoReparaElAbandonoQueFalló(t *testing.T) {
	rt, repo, contacts, evs, ab := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t43-r1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	// (1) El abandono FALLA: el evento ya quedó cancelled y el error se propaga.
	ab.failWith(errors.New("la base se cayó justo aquí"))
	if _, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID); err == nil {
		t.Fatal("un abandono que falla debe PROPAGAR el error")
	}
	if s := evs.statuses()[ev.ID]; s != events.StatusCancelled {
		t.Fatalf("la transición ya estaba sellada y no se deshace: %q", s)
	}
	if st := loadState(t, repo, cid); st.EventID != ev.ID {
		t.Fatalf("el puntero NO llegó a apagarse (el orden es transición→abandono→puntero): %q", st.EventID)
	}

	// (2) El dueño reintenta y la base ya responde: la solicitud queda abandonada y el
	// puntero apagado, sin que la fila del evento se toque otra vez.
	ab.failWith(nil)
	got, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("el reintento debe reparar, no fallar: %v", err)
	}
	if got.Status != events.StatusCancelled {
		t.Fatalf("la fila sigue cancelled, got %q", got.Status)
	}
	if vistos := ab.seen(); len(vistos) == 0 || vistos[len(vistos)-1] != ev.ID {
		t.Fatalf("el reintento debe volver a pedir el abandono por el evento (%q): %v", ev.ID, vistos)
	}
	if st := loadState(t, repo, cid); st.EventID != "" {
		t.Fatalf("y debe apagar el puntero que se quedó puesto: %q", st.EventID)
	}
}

// TestCancel_SinIntakeNoFalla (T4.3 + T4.5.5a): un evento sin solicitud se cancela
// sin error. Con AbandonByEvent el runtime YA NO pregunta si había solicitud: pide
// el abandono por evento y «cero filas» ES el éxito idempotente — así que la
// llamada SÍ ocurre (una), y no falla. La condición «¿hay algo que abandonar?» se
// delegó entera en la implementación del puerto.
func TestCancel_SinIntakeNoFalla(t *testing.T) {
	rt, _, _, evs, ab := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-e1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	got, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("cancelar sin intake: %v", err)
	}
	if got.Status != events.StatusCancelled {
		t.Fatalf("debe quedar cancelled, quedó %q", got.Status)
	}
	if vistos := ab.seen(); len(vistos) != 1 || vistos[0] != ev.ID {
		t.Fatalf("la llamada idempotente por evento debe ocurrir UNA vez (%q): %v", ev.ID, vistos)
	}
}

// TestCancel_OtroTenantEsNotFound (T4.2, INV-8): el id de otro tenant contesta
// ErrEventNotFound —el mismo que un id inexistente— y NO toca la fila ajena.
func TestCancel_OtroTenantEsNotFound(t *testing.T) {
	rt, repo, contacts, evs, ab := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-x1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	if _, err := rt.CancelEventForTenant(ctx, "t41-otro-tenant", ev.ID); !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("otro tenant debe recibir ErrEventNotFound, y recibió %v", err)
	}
	if _, err := rt.CancelEventForTenant(ctx, testTenant, "t41-no-existe"); !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("un id inexistente debe recibir ErrEventNotFound, y recibió %v", err)
	}
	if s := evs.statuses()[ev.ID]; s != events.StatusOpen {
		t.Fatalf("la fila NO se toca: debe seguir open, quedó %q", s)
	}
	if st := loadState(t, repo, cid); st.EventID != ev.ID {
		t.Fatalf("ni su puntero: %q", st.EventID)
	}
	if vistos := ab.seen(); len(vistos) != 0 {
		t.Fatalf("ni su solicitud: %v", vistos)
	}
}

// TestCancel_NoTocaElPunteroDeOtroEvento: si la conversación ya está en OTRO
// evento, cancelar el primero no le apaga el puntero al segundo.
func TestCancel_NoTocaElPunteroDeOtroEvento(t *testing.T) {
	rt, repo, contacts, evs, _ := newLifecycleRuntime(t,
		eventStartRule("carrito", "cart"), eventStartRule("encuesta", "survey"))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-p1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "t41-p2")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}
	survey := aliveOfKind(t, evs, "survey")

	if _, err := rt.CancelEventForTenant(ctx, testTenant, cart.ID); err != nil {
		t.Fatalf("cancelar el carrito: %v", err)
	}
	if st := loadState(t, repo, cid); st.EventID != survey.ID {
		t.Fatalf("el puntero de la encuesta activa NO se toca: %q (quería %q)", st.EventID, survey.ID)
	}
}

// carreraCancel simula al OTRO escritor ganando entre el fetch y el UPDATE: la
// transición encuentra la fila ya sellada (aquí: closed) y devuelve ErrNotOpen.
type carreraCancel struct {
	*memEventStore
}

func (c *carreraCancel) TransitionEvent(ctx context.Context, eventID string, to events.Status) error {
	// El otro escritor sella closed justo antes de nuestro CAS…
	if err := c.memEventStore.TransitionEvent(ctx, eventID, events.StatusClosed); err != nil {
		return err
	}
	// …y nuestro UPDATE con guard no toca ninguna fila.
	return events.ErrNotOpen
}

// TestCancel_CarreraBenignaDevuelveLoQueQuedo (T4.2): perder la carrera contra
// otra muerte NO es un error: se re-lee y se devuelve la fila como quedó, sin
// abandonar nada (eso es del escritor que ganó).
func TestCancel_CarreraBenignaDevuelveLoQueQuedo(t *testing.T) {
	evsBase := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	rt, _, _, _, ab := newLifecycleRuntimeConStore(t, &carreraCancel{memEventStore: evsBase}, evsBase,
		eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-g1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evsBase.alive()[0]

	got, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID)
	if err != nil {
		t.Fatalf("la carrera perdida es benigna: %v", err)
	}
	if got.Status != events.StatusClosed {
		t.Fatalf("debe devolver lo que QUEDÓ (la muerte del otro), y devolvió %q", got.Status)
	}
	if vistos := ab.seen(); len(vistos) != 0 {
		t.Fatalf("quien pierde la carrera no abandona nada: %v", vistos)
	}
}

// abandonadorRoto falla siempre: fabrica la costura transición-ok/abandono-falla.
type abandonadorRoto struct{}

func (abandonadorRoto) AbandonByEvent(context.Context, string, string) error {
	return errors.New("intakes fuera de servicio")
}

// TestCancel_AbandonoFallaPropagaConEventoCancelado: la costura CONOCIDA de la
// ola, fijada para que nadie la "arregle" en silencio: si el abandono falla, el
// error se propaga Y el evento queda cancelado (la transición no se deshace).
func TestCancel_AbandonoFallaPropagaConEventoCancelado(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), eventStartRule("carrito", "cart")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithIntakeAbandoner(abandonadorRoto{}))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-b1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	if _, err := rt.CancelEventForTenant(ctx, testTenant, ev.ID); err == nil {
		t.Fatal("el fallo del abandono debe PROPAGARSE, no taparse")
	}
	if s := evs.statuses()[ev.ID]; s != events.StatusCancelled {
		t.Fatalf("y el evento queda cancelado igual (costura conocida): %q", s)
	}
}

// TestCancel_SinPlanoDeEventos: sin WithEventStore, Get y Cancel contestan
// ErrNoEventPlane — un error de cableado, no un 404 que lo esconda.
func TestCancel_SinPlanoDeEventos(t *testing.T) {
	rt, _, _, _ := newRuntime(t)
	ctx := context.Background()

	if _, err := rt.GetEventForTenant(ctx, testTenant, "ev-1"); !errors.Is(err, runtime.ErrNoEventPlane) {
		t.Fatalf("GetEventForTenant sin plano: %v", err)
	}
	if _, err := rt.CancelEventForTenant(ctx, testTenant, "ev-1"); !errors.Is(err, runtime.ErrNoEventPlane) {
		t.Fatalf("CancelEventForTenant sin plano: %v", err)
	}
}

// TestGetEventForTenant_DelegaAcotadoAlTenant: la lectura por id devuelve la fila
// del tenant y ErrEventNotFound para el ajeno — la misma indistinción del store.
func TestGetEventForTenant_DelegaAcotadoAlTenant(t *testing.T) {
	rt, _, _, evs, _ := newLifecycleRuntime(t, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-l1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]

	got, err := rt.GetEventForTenant(ctx, testTenant, ev.ID)
	if err != nil || got.ID != ev.ID {
		t.Fatalf("GetEventForTenant propio: got=%+v err=%v", got, err)
	}
	if _, err := rt.GetEventForTenant(ctx, "t41-otro", ev.ID); !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("el ajeno debe ser ErrEventNotFound: %v", err)
	}
}

// TestCierreNatural_NoMataElEventoDelMenuHeredandoUnFlujoAcabado (T4.1, hallazgo del
// barrido CLI del 2026-08-10): el CUARTO camino que el cierre natural tenía abierto,
// y que no era saltárselo sino dispararlo sobre el evento EQUIVOCADO.
//
// enterEventFlow borra el flow_state previo cuando arranca un flujo, pero el camino
// del `menu` no arranca ninguno (D-043.3) y no lo borraba: saveMenuState heredaba el
// FlowID de un flujo YA TERMINADO y estampaba encima el puntero al evento del menú.
// El siguiente entrante que no fuera una opción llegaba al Step —no-op sobre el
// centinela— y closeIfFinished veía «estado terminal + evento activo» y cerraba el
// evento del MENÚ, que ni siquiera tiene flujo que terminar. `closed` mentiroso en la
// fila y un `event_closed` falso en la telemetría.
//
// El guard es la secuencia entera: sin ella el test pasa igual con el bug puesto.
func TestCierreNatural_NoMataElEventoDelMenuHeredandoUnFlujoAcabado(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("carrito", "cart"),
		eventStartRule("menu", "menu"),
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC))
	menu := events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	// (1) Un cart nace y su flujo TERMINA: el flow_state queda en el centinela.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-m1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t41-m2")); err != nil {
		t.Fatalf("completar: %v", err)
	}
	if st := loadState(t, repo, cid); !st.Finished() {
		t.Fatalf("el montaje exige un estado TERMINAL persistido; nodo=%q", st.CurrentNode)
	}

	// (2) El contacto pide el menú SOBRE ese estado terminal: el flujo acabado NO se
	// hereda —el menú no cuelga de ningún flujo— y el puntero es el del menú.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "t41-m3")); err != nil {
		t.Fatalf("menu: %v", err)
	}
	men := aliveOfKind(t, evs, "menu")
	st := loadState(t, repo, cid)
	if st.Finished() || st.FlowID != "" {
		t.Fatalf("el estado del menú no puede heredar el flujo acabado; finished=%v flow=%q", st.Finished(), st.FlowID)
	}
	if st.EventID != men.ID {
		t.Fatalf("el puntero debe ser el del menú (%q), y vale %q", men.ID, st.EventID)
	}

	// (3) Un texto que NO es opción del menú: el evento del menú sigue VIVO. Su muerte
	// es de su reloj de inactividad o de una cancelación, nunca del cierre natural.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pues no sé", "t41-m4")); err != nil {
		t.Fatalf("texto libre: %v", err)
	}
	if got := evs.statuses()[men.ID]; got != events.StatusOpen {
		t.Fatalf("el evento `menu` debe seguir open y quedó %q: ningún flujo SUYO terminó", got)
	}
}

// TestCierreNatural_NoMataElEventoActivoConFlujoAjenoAunEnCurso (#22 / H2, hallazgo
// heredado a la Ola 6): la variante que TestCierreNatural_NoMataElEventoDelMenuHeredandoUnFlujoAcabado
// NO cubre, porque ahí el flujo heredado ya estaba TERMINADO cuando se pidió el menú
// (saveMenuState lo detecta con st.Finished() y resetea el estado, D-043.3). Aquí el
// flujo heredado sigue EN CURSO (esperando input) en el momento de heredar: saveMenuState
// no resetea nada y el evento `menu` se queda apuntando a un flow_state cuyo FlowID es
// el del `cart`. Si ese flujo ajeno alcanza su nodo TERMINAL más tarde, closeIfFinished
// sin la guarda de ev.FlowID cerraba el `menu` — que ni siquiera tiene flujo propio
// (D-043.3, ev.FlowID="") — con un event_closed FALSO.
//
// La regla `menu` FUERZA FlowID="" (a mano, y NO con eventStartRule, que hardcodea
// testFlow para todos los tipos): es la config REAL de un tenant (D-043.3) y es lo
// que hace que ev.FlowID ("") difiera de st.FlowID (testFlow, heredado) — sin esa
// diferencia el guard de H2 no se ejerce y este test no prueba nada.
//
// 🔁 EXPECTATIVA REESCRITA EN EL PLAN 053 · T1.6 (D-053.2), y se dice en vez de
// hacerse en silencio: la PROPIEDAD de este test —el `menu` no muere por el fin de un
// flujo ajeno— sigue intacta, pero pasa a sostenerse por CONSTRUCCIÓN (closeIfFinished
// transiciona st.OwnerEventID, que en este montaje ES el `cart`) en vez de por la
// guarda de posesión, que se retiró. Lo que SÍ cambia es el aserto del `cart`: hasta
// hoy este test exigía que quedara `open`, que era el gap que la Ola 6 dejó CONSCIENTE
// —mientras la guarda disparaba, closeIfFinished no cerraba nada, ni el `menu`
// (correcto) ni el `cart` (incorrecto: se quedaba `open` para siempre con su intake
// colgando)—. Con el dueño explícito el `cart` se cierra, que es la reparación que
// D-053.2 anuncia como «de propina». El aserto se invierte por eso, no porque el test
// estorbara.
func TestCierreNatural_NoMataElEventoActivoConFlujoAjenoAunEnCurso(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu",
			MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))
	// La lista del despachador NO ofrece "1" ni "2": así los dos números que el
	// flujo heredado del carrito SÍ entiende (sampleFlow: root→1/2) no los
	// intercepta menuChoice y llegan al Step del flujo ajeno.
	menu := events.Menu{Options: []events.MenuOption{{Number: 9, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)

	// (1) «carrito»: nace y se queda ESPERANDO en el nodo raíz (menú, no terminal).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t22-h2-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if st := loadState(t, repo, cid); st.Finished() {
		t.Fatalf("precondición: el montaje exige un estado EN CURSO (no terminal); nodo=%q", st.CurrentNode)
	}

	// (2) «menu»: nace el evento `menu` (FlowID=""), y como el cart AÚN NO terminó,
	// saveMenuState NO resetea nada: hereda FlowID/CurrentNode del cart tal cual.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "t22-h2-2")); err != nil {
		t.Fatalf("menu: %v", err)
	}
	men := aliveOfKind(t, evs, "menu")
	st := loadState(t, repo, cid)
	if st.EventID != men.ID {
		t.Fatalf("precondición: el puntero debe ser el del menú (%q), y vale %q", men.ID, st.EventID)
	}
	if st.FlowID != testFlow || st.Finished() {
		t.Fatalf("precondición: el estado del menú debe HEREDAR el flujo ajeno en curso; flow=%q finished=%v",
			st.FlowID, st.Finished())
	}

	// (3) «1»: no lo ofrece la lista del despachador (solo tiene el 9) así que cae
	// al Step del flujo heredado (sampleFlow: root, opción "1" → "ventas", TERMINAL).
	// El flujo que termina es del `cart`, pero el evento ACTIVO es el `menu`.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t22-h2-3")); err != nil {
		t.Fatalf("completar el flujo ajeno: %v", err)
	}
	if st := loadState(t, repo, cid); !st.Finished() {
		t.Fatalf("precondición: el Step del flujo ajeno debe dejar el estado TERMINAL; nodo=%q", st.CurrentNode)
	}

	// GUARD: el evento `menu` NO puede haber muerto — el flujo que acaba de terminar
	// no es suyo (D-043.3: el menú no tiene flujo propio), y desde el Plan 053 eso lo
	// dice flow_state.owner_event_id, que apunta al `cart`.
	if got := evs.statuses()[men.ID]; got != events.StatusOpen {
		t.Fatalf("H2: el evento `menu` NO debe cerrarse por el fin de un flujo AJENO (el del cart); quedó %q", got)
	}
	// Y el `cart` —DUEÑO real del flujo que acaba de terminar— SÍ se cierra (Plan 053 ·
	// T1.6, D-053.2). Ver la nota «EXPECTATIVA REESCRITA» del docstring: hasta el 053
	// aquí se exigía `open`, y ese `open` no era una garantía sino el gap consciente que
	// la guarda de posesión dejaba abierto.
	if got := evs.statuses()[cart.ID]; got != events.StatusClosed {
		t.Fatalf("el `cart` —dueño del flujo que terminó— debe cerrarse por el cierre natural; quedó %q "+
			"(si dice %q, closeIfFinished volvió a transicionar el evento ACTIVO en vez del DUEÑO)",
			got, events.StatusOpen)
	}
}
