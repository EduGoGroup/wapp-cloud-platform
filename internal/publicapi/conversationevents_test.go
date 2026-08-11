package publicapi_test

// Tests de GET /api/v1/conversation-events (Plan 043 · T3.9b, REQ-28): la bandeja
// por la que el dueño limpia los eventos que la de solicitudes no alcanza.
//
// Aquí se prueba el TRANSPORTE —gate de feature, scope, aislamiento por tenant,
// traducción de la query al filtro y el tope de paginación—. Lo que la consulta
// DEVUELVE (que `content=none&stale=true` sea el survey a medias) se prueba contra
// Postgres real en internal/flujos/events/store_list_integration_test.go: un fake
// que decidiera por su cuenta qué es «vencido» estaría probando el fake.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// fakeEventLister es el puerto de listado con memoria de lo que le pidieron. Guarda
// el ÚLTIMO tenant y filtro recibidos: es lo que permite afirmar que el tenant sale
// del token y no de la query (INV-8), que es lo que este endpoint tiene que
// garantizar y no se puede ver desde el cuerpo de la respuesta.
type fakeEventLister struct {
	porTenant map[string][]events.Rescuable
	gotTenant string
	gotFilter events.ListFilter
	llamadas  int
	err       error
}

func (f *fakeEventLister) ListEvents(_ context.Context, tenantID string,
	filtro events.ListFilter) (events.EventPage, error) {
	f.llamadas++
	f.gotTenant, f.gotFilter = tenantID, filtro
	if f.err != nil {
		return events.EventPage{}, f.err
	}
	// Aplica SOLO el filtro por tipos habilitados. No es «probar el fake»: es la
	// única parte del contrato del store que este test necesita ver reflejada en el
	// cuerpo, porque la decisión del 2026-08-09 es sobre lo que el dueño VE. Que el
	// SQL lo filtre de verdad se prueba contra Postgres (store_list_integration_test).
	evs := make([]events.Rescuable, 0, len(f.porTenant[tenantID]))
	for _, ev := range f.porTenant[tenantID] {
		if filtro.Kinds == nil || slices.Contains(filtro.Kinds, ev.Kind) {
			evs = append(evs, ev)
		}
	}
	return events.EventPage{
		Events: evs, Page: filtro.Page, PageSize: filtro.PageSize, Total: len(evs),
	}, nil
}

// resolverQueFallaTrasN responde como el Fake las primeras `n` preguntas y luego
// falla. Modela lo ÚNICO que puede dejar al handler sin poder resolver los tipos:
// el gate ya pasó (con una feature cacheada) y la siguiente consulta se encuentra
// la BD caída. Sin él, el 500 de AllowedKinds sería inalcanzable desde fuera.
type resolverQueFallaTrasN struct {
	*entitlements.Fake
	n         int
	preguntas int
}

func (r *resolverQueFallaTrasN) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	r.preguntas++
	if r.preguntas > r.n {
		return false, errors.New("la BD de entitlements se cayó")
	}
	return r.Fake.Has(ctx, tenantID, feature)
}

// evento arma una fila del listado con lo justo para identificarla. contentState
// y contentRef son los DERIVADOS del join con event_content (D-043.22) tal como
// el store los habría resuelto: vacíos = el evento no produjo contenido.
func evento(id, kind, contentState, contentRef string, stale bool) events.Rescuable {
	return events.Rescuable{
		Event: events.Event{
			ID: id, TenantID: "", SessionID: "sess-a", ContactID: contactoOpaco,
			Kind: kind, HistoryID: kind + "-2026-01-01-1200", Status: events.StatusOpen,
			CreatedAt:      time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			LastActivityAt: time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC),
		},
		Stale:        stale,
		ContentState: contentState,
		ContentRef:   contentRef,
	}
}

// eventosDeps arma unas Deps con el listado sembrado y las CUATRO features de tipo
// encendidas en los DOS tenants: los tests de aislamiento tienen que fallar por
// tenant, no por plan, y los del contrato tienen que ver las dos filas del fixture.
func eventosDeps(lister *fakeEventLister) publicapi.Deps {
	return publicapi.Deps{ConversationEvents: lister, Entitlements: conTodosLosTipos()}
}

// conTodosLosTipos enciende las cuatro features de tipo de fábrica en los dos
// tenants. Se apoya en events.KindFeatures() y no en una lista escrita a mano: si
// mañana nace un quinto tipo, el fixture lo incluye solo y el test del gate sigue
// diciendo la verdad.
func conTodosLosTipos() *entitlements.Fake {
	fake := entitlements.NewFake()
	for _, f := range events.KindFeatures() {
		fake.Enable(tenantA, f)
		fake.Enable(tenantB, f)
	}
	return fake
}

func listerSembrado() *fakeEventLister {
	return &fakeEventLister{porTenant: map[string][]events.Rescuable{
		tenantA: {
			evento("ev-a-survey", "survey", "", "", true),
			evento("ev-a-cart", "cart", "alive", "int-1", false),
		},
		tenantB: {evento("ev-b-cart", "cart", "alive", "int-b", false)},
	}}
}

// conversationEventListDTO espeja el contrato en el wire.
type conversationEventListDTO struct {
	Events []struct {
		ID             string `json:"id"`
		HistoryID      string `json:"history_id"`
		Kind           string `json:"kind"`
		Status         string `json:"status"`
		ContactID      string `json:"contact_id"`
		SessionID      string `json:"session_id"`
		ContentState   string `json:"content_state"`
		ContentRef     string `json:"content_ref"`
		Stale          bool   `json:"stale"`
		CreatedAt      string `json:"created_at"`
		LastActivityAt string `json:"last_activity_at"`
		ClosedAt       string `json:"closed_at"`
	} `json:"events"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

func decodeEventos(t *testing.T, body []byte) conversationEventListDTO {
	t.Helper()
	var out conversationEventListDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal del listado: %v; body=%s", err, body)
	}
	return out
}

// filasCrudas decodifica el listado como mapas crudos, para poder afirmar sobre
// PRESENCIA de claves — el struct del decode no distingue «clave ausente» de
// «cadena vacía».
func filasCrudas(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var crudo struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &crudo); err != nil {
		t.Fatalf("unmarshal crudo del listado: %v; body=%s", err, body)
	}
	return crudo.Events
}

// sinClavesDeContenido afirma que la fila cruda NO publica ninguna clave derivada
// del contenido (omitempty): la ausencia es el dato — «sin fila en la vista»
// (content=none) — y publicar "" mentiría.
func sinClavesDeContenido(t *testing.T, fila map[string]json.RawMessage) {
	t.Helper()
	for _, clave := range []string{"content_state", "content_ref", "intake_id"} {
		if _, está := fila[clave]; está {
			t.Fatalf("la fila sin contenido publica la clave %q; debe OMITIRSE", clave)
		}
	}
}

// TestConversationEvents_200_ContratoDelWire: el camino feliz y lo que publica cada
// fila. La marca «vencido» viaja TAL CUAL la resolvió la consulta —el transporte no
// la recalcula ni tiene con qué—, y el contacto viaja OPACO (INV-01).
func TestConversationEvents_200_ContratoDelWire(t *testing.T) {
	api := newAPI(eventosDeps(listerSembrado()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	list := decodeEventos(t, rec.Body.Bytes())
	if len(list.Events) != 2 || list.Total != 2 {
		t.Fatalf("listado=%d filas / total=%d; quiero 2 y 2", len(list.Events), list.Total)
	}
	primero := list.Events[0]
	if primero.ID != "ev-a-survey" || !primero.Stale || primero.Kind != "survey" {
		t.Fatalf("primera fila=%+v; quiero el survey vencido", primero)
	}
	if primero.ContentState != "" || primero.ContentRef != "" {
		t.Fatalf("content_state=%q content_ref=%q; el survey no produjo contenido", primero.ContentState, primero.ContentRef)
	}
	// Y no es que viajen vacías: las claves NO ESTÁN (omitempty).
	sinClavesDeContenido(t, filasCrudas(t, rec.Body.Bytes())[0])
	if primero.ContactID != contactoOpaco {
		t.Fatalf("contact_id=%q; viaja opaco tal cual (INV-01)", primero.ContactID)
	}
	if primero.ClosedAt != "" {
		t.Fatalf("closed_at=%q en un evento abierto; el cero de time.Time no puede salir como fecha",
			primero.ClosedAt)
	}
	if primero.LastActivityAt != "2026-01-01T12:30:00Z" {
		t.Fatalf("last_activity_at=%q; quiero RFC3339 en UTC", primero.LastActivityAt)
	}
	// El segundo SÍ tiene contenido: los derivados del join viajan tal cual los
	// resolvió la consulta (D-043.22), y content_ref es el hilo evento→solicitud.
	if list.Events[1].ContentState != "alive" || list.Events[1].ContentRef != "int-1" || list.Events[1].Stale {
		t.Fatalf("segunda fila=%+v; quiero el carrito con su contenido vivo y sin marca", list.Events[1])
	}
}

// TestConversationEvents_200_TraduceLaQueryAlFiltro: los cinco filtros de REQ-28
// llegan al store, y el tri-estado de `stale` se respeta.
//
// Se afirma sobre el filtro RECIBIDO y no sobre las filas devueltas a propósito: un
// test que mirara el resultado pasaría igual con un filtro que se pierde por el
// camino, porque el fake devuelve lo mismo pida lo que pida.
func TestConversationEvents_200_TraduceLaQueryAlFiltro(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(eventosDeps(lister), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/conversation-events?status=cancelled&kind=survey&content=none&stale=true"+
			"&contact_id="+contactoOpaco+"&page=2&page_size=25", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	f := lister.gotFilter
	if f.Status != events.StatusCancelled || f.Kind != "survey" || f.Content != events.ContentNone {
		t.Fatalf("filtro=%+v; quiero status=cancelled kind=survey content=none", f)
	}
	if f.Stale == nil || !*f.Stale {
		t.Fatalf("stale=%v; quiero el puntero a true", f.Stale)
	}
	if f.ContactID != contactoOpaco || f.Page != 2 || f.PageSize != 25 {
		t.Fatalf("filtro=%+v; quiero contacto=%s page=2 page_size=25", f, contactoOpaco)
	}
	// Y los tipos del plan, que NO vienen de la query sino del resolver: con las
	// cuatro encendidas llegan las cuatro.
	if !slices.Equal(f.Kinds, []string{"cart", "media", "menu", "survey"}) {
		t.Fatalf("Kinds=%v; quiero los cuatro tipos del plan en orden alfabético", f.Kinds)
	}

}

// TestConversationEvents_200_StaleEsTriEstado: `stale` tiene TRES respuestas y no
// dos. Ausente ⇒ la marca no filtra (INV-19: informa); `true` ⇒ solo los vencidos;
// `false` ⇒ solo los que no lo están.
//
// Confundir «ausente» con «false» escondería justo los vencidos que esta bandeja
// existe para enseñar, y es un fallo que ninguna respuesta 200 delata.
func TestConversationEvents_200_StaleEsTriEstado(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(eventosDeps(lister), intakesKeys())

	call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if lister.gotFilter.Stale != nil {
		t.Fatalf("stale=%v sin pedirlo; ausente NO puede significar false", *lister.gotFilter.Stale)
	}
	// Y los defaults de REQ-28, que se leen en la misma petición: `open` y `any`.
	if lister.gotFilter.Status != events.StatusOpen || lister.gotFilter.Content != events.ContentAny {
		t.Fatalf("defaults=%+v; quiero status=open content=any", lister.gotFilter)
	}

	call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events?stale=false", "")
	if lister.gotFilter.Stale == nil || *lister.gotFilter.Stale {
		t.Fatalf("stale=%v con stale=false; quiero el puntero a false", lister.gotFilter.Stale)
	}
}

// TestConversationEvents_200_TopeDePaginación: pedir 100000 devuelve 200 acotado, no
// un error. La cota es del CONTRATO de la ruta, así que se aplica en el transporte
// y no solo en el store: sin ella un GET sin filtros materializa todos los eventos
// vivos del tenant.
func TestConversationEvents_200_TopeDePaginación(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(eventosDeps(lister), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/conversation-events?page_size=100000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if lister.gotFilter.PageSize != 200 {
		t.Fatalf("el store recibió page_size=%d; el tope del contrato es 200 y se aplica ANTES de consultar",
			lister.gotFilter.PageSize)
	}
	if list := decodeEventos(t, rec.Body.Bytes()); list.PageSize != 200 {
		t.Fatalf("page_size respondido=%d, quiero 200", list.PageSize)
	}
	// Y una página 0 o negativa es la primera, no un offset negativo.
	call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events?page=0", "")
	if lister.gotFilter.Page != 1 || lister.gotFilter.Offset() != 0 {
		t.Fatalf("page=%d offset=%d con ?page=0; quiero la primera página",
			lister.gotFilter.Page, lister.gotFilter.Offset())
	}
}

// TestConversationEvents_AisladoPorTenant es la prueba de INV-8, y está escrita para
// que NO pueda pasar por accidente: la petición lleva `?tenant_id=<A>` en la query
// —el intento de leer la bandeja ajena— con el token de B.
//
// El fake registra qué tenant le pidieron: si el handler hiciera caso a la query, el
// registro diría A. Diga lo que diga el cuerpo, ESA es la afirmación que importa.
//
// ⚠️ Un listado NO puede responder 404 «cross-tenant»: no hay recurso que no
// encontrar. El 404 de REQ-28 es del endpoint por id (…/cancel, T4.2 de la Ola 4).
// Aquí la única respuesta honesta es 200 con lo del tenant del token — y la
// comprobación de que la lista de B no está vacía «porque sí» la da la segunda
// mitad del test: la MISMA petición con el token de A devuelve otras filas.
func TestConversationEvents_AisladoPorTenant(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(eventosDeps(lister), intakesKeys())
	const intento = "/api/v1/conversation-events?tenant_id=" + tenantA

	rec := call(api, keyBIntakes, http.MethodGet, intento, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if lister.gotTenant != tenantB {
		t.Fatalf("el store recibió tenant=%q con el token de B y ?tenant_id=A: "+
			"el tenant SALE DEL TOKEN (INV-8)", lister.gotTenant)
	}
	list := decodeEventos(t, rec.Body.Bytes())
	if len(list.Events) != 1 || list.Events[0].ID != "ev-b-cart" {
		t.Fatalf("B ve %+v; solo puede ver lo suyo", list.Events)
	}

	// La otra mitad: con el token de A, la MISMA URL devuelve lo de A. Sin esto, un
	// handler que devolviera siempre la lista vacía pasaría la primera mitad.
	recA := call(api, keyAIntakes, http.MethodGet, intento, "")
	listA := decodeEventos(t, recA.Body.Bytes())
	if lister.gotTenant != tenantA || len(listA.Events) != 2 {
		t.Fatalf("con el token de A: tenant=%q filas=%d; quiero %s y 2",
			lister.gotTenant, len(listA.Events), tenantA)
	}
}

// TestConversationEvents_403_SinNingunoDeLosCuatroTipos: el scope no basta. Un
// tenant sin NINGUNA de las cuatro features de tipo no abre la bandeja — y el
// cuerpo enumera las que habrían valido, para que la UI ofrezca el upgrade sin
// adivinar cuál de ellas pedir.
//
// Se comprueba además que el store NO se llegó a consultar: un 403 que primero lee
// la BD ya hizo el trabajo del tenant que no tiene el plan.
func TestConversationEvents_403_SinNingunoDeLosCuatroTipos(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(publicapi.Deps{
		ConversationEvents: lister,
		Entitlements:       entitlements.NewFake(), // ninguna feature encendida
	}, intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin ninguna de las cuatro; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error    string   `json:"error"`
		Feature  string   `json:"feature"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 403: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "feature_not_enabled" {
		t.Fatalf("error=%q; el código del gate no cambia por ser plural", body.Error)
	}
	if !slices.Equal(body.Features, events.KindFeatures()) {
		t.Fatalf("features=%v; quiero las cuatro de los tipos (%v)", body.Features, events.KindFeatures())
	}
	if body.Feature != "" {
		t.Fatalf("feature=%q en singular: diría «te falta esta» cuando bastaba cualquiera", body.Feature)
	}
	if lister.llamadas != 0 {
		t.Fatalf("el store se consultó %d veces sin ninguna feature; el gate corta ANTES",
			lister.llamadas)
	}
}

// TestConversationEvents_200_SoloLosTiposDelPlan es EL test de la decisión de Jhoan
// del 2026-08-09, y el caso que descartó `cart_basic` como gate: un tenant con
// `survey` y SIN `cart_basic` entra (200) y ve sus encuestas — pero ni un carrito.
//
// Las dos mitades importan y son distintas: la primera es el gate (no se le cierra
// la puerta por no tener la del carrito), la segunda es el contenido (entrar por
// una feature no da derecho a ver los tipos de las otras).
func TestConversationEvents_200_SoloLosTiposDelPlan(t *testing.T) {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureSurvey) // y NADA más: sin cart_basic
	lister := listerSembrado()
	api := newAPI(publicapi.Deps{ConversationEvents: lister, Entitlements: fake}, intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200: `survey` basta para entrar; body=%s", rec.Code, rec.Body.String())
	}
	// Primero lo que VE el dueño, que es la mitad que de verdad decidió el gate: si
	// alguien quita el filtro por features, el que canta es este `cart` de más.
	list := decodeEventos(t, rec.Body.Bytes())
	for _, ev := range list.Events {
		if ev.Kind != "survey" {
			t.Fatalf("la lista trae un %q y el tenant NO tiene esa feature: %+v", ev.Kind, ev)
		}
	}
	if len(list.Events) != 1 || list.Events[0].ID != "ev-a-survey" {
		t.Fatalf("lista=%+v; quiero exactamente la encuesta del tenant", list.Events)
	}
	// Y después, que el recorte lo hizo el STORE y no esta capa tirando filas: el
	// filtro tiene que haber llegado, o la paginación contaría lo que no se enseña.
	if !slices.Equal(lister.gotFilter.Kinds, []string{"survey"}) {
		t.Fatalf("el store recibió Kinds=%v; quiero solo [survey]", lister.gotFilter.Kinds)
	}
}

// TestConversationEvents_500_SiNoSePuedenResolverLosTipos: si el resolver se cae
// DESPUÉS del gate, la respuesta es 500 y no una lista recortada.
//
// Es la única asimetría deliberada con el gate, que ante lo mismo responde 403: aquel
// decide el ACCESO y negar es lo prudente; este decide el CONTENIDO, y una bandeja que
// dice «no queda nada» cuando la verdad es «no pude mirar» manda a Herminia a casa
// con pedidos abiertos.
func TestConversationEvents_500_SiNoSePuedenResolverLosTipos(t *testing.T) {
	lister := listerSembrado()
	// 1 pregunta buena: la del gate (que corta en cuanto encuentra una encendida).
	feats := &resolverQueFallaTrasN{Fake: conTodosLosTipos(), n: 1}
	api := newAPI(publicapi.Deps{ConversationEvents: lister, Entitlements: feats}, intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500 con el resolver caído tras el gate; body=%s",
			rec.Code, rec.Body.String())
	}
	if lister.llamadas != 0 {
		t.Fatalf("se consultó el store %d veces sin saber qué tipos ve el tenant", lister.llamadas)
	}
}

// TestConversationEvents_403_SinScope: la feature tampoco basta. El grant dice
// «puedes operar esto» y la feature «tu plan lo incluye»; ninguno sustituye al otro.
func TestConversationEvents_403_SinScope(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(eventosDeps(lister), intakesKeys())

	// keyARead porta flows.read: autenticada, del tenant A, pero sin intakes.read.
	rec := call(api, keyARead, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin el scope; body=%s", rec.Code, rec.Body.String())
	}
	if lister.llamadas != 0 {
		t.Fatalf("el store se consultó %d veces sin el scope", lister.llamadas)
	}
}

// TestConversationEvents_401_SinCredencial: sin token no hay tenant del que acotar,
// y sin tenant esta ruta no tiene pregunta que hacer.
func TestConversationEvents_401_SinCredencial(t *testing.T) {
	api := newAPI(eventosDeps(listerSembrado()), intakesKeys())

	rec := call(api, "", http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestConversationEvents_400_FiltrosInválidos: un typo se dice, no se ignora.
// Devolver la bandeja entera ante `status=abiertos` sería peor que un error, porque
// quien limpia creería estar viendo lo que pidió.
func TestConversationEvents_400_FiltrosInválidos(t *testing.T) {
	lister := listerSembrado()
	api := newAPI(eventosDeps(lister), intakesKeys())

	// `contact_id=marta` es 400 y no 500: la columna es UUID (ADR-0017) y sin la
	// comprobación el error saldría de Postgres al castear, con un 500 que le echa
	// la culpa al servidor de un id mal escrito.
	for _, q := range []string{"?status=abiertos", "?status=expired", "?content=todos",
		"?stale=quizá", "?contact_id=marta", "?contact_id=123"} {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s → code=%d, quiero 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}
	if lister.llamadas != 0 {
		t.Fatalf("el store se consultó %d veces con filtros inválidos", lister.llamadas)
	}

	// Un parámetro VACÍO no es un typo: es no haberlo puesto. `?status=` vale lo
	// mismo que no escribirlo y cae en los defaults — rechazarlo con un 400 rompería
	// a cualquier formulario que envíe sus campos vacíos.
	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/conversation-events?status=&content=&stale=&kind=&contact_id=", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("con los filtros vacíos code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if f := lister.gotFilter; f.Status != events.StatusOpen || f.Content != events.ContentAny ||
		f.Stale != nil || f.Kind != "" || f.ContactID != "" {
		t.Fatalf("filtro con los parámetros vacíos=%+v; quiero los defaults", f)
	}
}

// TestConversationEvents_500_ErrorDelStore: un fallo de BD es 500 y NO una lista
// vacía. Una bandeja que responde «no queda nada» cuando en realidad no pudo mirar
// es exactamente el fallo que deja a Herminia creyendo que ya limpió.
func TestConversationEvents_500_ErrorDelStore(t *testing.T) {
	lister := listerSembrado()
	lister.err = context.DeadlineExceeded
	api := newAPI(eventosDeps(lister), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestConversationEvents_NoSeMontaSinDependencias: sin el lister o sin el resolver
// de features la ruta no existe (404 de mux), que es preferible a una bandeja que se
// abre sin poder comprobar el plan.
func TestConversationEvents_NoSeMontaSinDependencias(t *testing.T) {
	for nombre, d := range map[string]publicapi.Deps{
		"sin lister":   {Entitlements: entitlements.NewFake()},
		"sin features": {ConversationEvents: listerSembrado()},
	} {
		api := newAPI(d, intakesKeys())
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: code=%d, quiero 404 de ruta inexistente; body=%s",
				nombre, rec.Code, rec.Body.String())
		}
	}
}
