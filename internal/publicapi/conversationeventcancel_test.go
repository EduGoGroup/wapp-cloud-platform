package publicapi_test

// Tests de POST /api/v1/conversation-events/{id}/cancel (Plan 043 · T4.2): la
// acción de limpieza de la bandeja de eventos.
//
// Aquí se prueba el TRANSPORTE con un doble del puerto: gate de feature, scope,
// los TRES caminos que responden el mismo 404 (no existe / otro tenant / tipo sin
// feature), la idempotencia vista desde fuera y que el tenant sale del token
// (INV-8). Lo que la cancelación ESCRIBE de verdad (closed_at, flow_state, intake
// abandonado) se prueba contra Postgres real en
// conversationeventcancel_e2e_integration_test.go: un fake que decidiera por su
// cuenta qué limpia el runtime estaría probando el fake.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// ids de eventos sembrados (UUID: el handler rechaza con 404 lo que no lo sea).
const (
	evCartA = "e4c11111-1111-4111-8111-111111111111" // tenantA, cart, open
	evCartB = "e4c22222-2222-4222-8222-222222222222" // tenantB, cart, open
	evNadie = "e4c99999-9999-4999-8999-999999999999" // no existe en ningún tenant
)

// cancelInstante es el instante FIJO en que el fake sella closed_at: con un reloj
// fijo dos respuestas 200 del mismo evento son byte-idénticas, que es exactamente
// lo que el test de idempotencia necesita afirmar.
var cancelInstante = time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

// fakeEventCanceller es el doble del puerto de cancelación, con memoria de lo que
// le pidieron (INV-8: afirmar que el tenant sale del token no se ve en el cuerpo)
// y con el contrato del runtime reproducido en miniatura: acotado por tenant
// (cross-tenant ⇒ events.ErrEventNotFound, el fake simula el acotado del SQL) e
// idempotente (ya terminal ⇒ la fila tal cual, SIN transición).
type fakeEventCanceller struct {
	porTenant map[string]map[string]events.Event
	gotTenant string
	gotID     string
	getCalls  int
	// cancelCalls cuenta las LLAMADAS a CancelEventForTenant; transiciones cuenta
	// las que de verdad MUTARON la fila. La resta es la idempotencia.
	cancelCalls  int
	transiciones int
	getErr       error
	cancelErr    error
}

func (f *fakeEventCanceller) GetEventForTenant(_ context.Context, tenantID, eventID string) (events.Event, error) {
	f.getCalls++
	f.gotTenant, f.gotID = tenantID, eventID
	if f.getErr != nil {
		return events.Event{}, f.getErr
	}
	ev, ok := f.porTenant[tenantID][eventID]
	if !ok {
		return events.Event{}, events.ErrEventNotFound
	}
	return ev, nil
}

func (f *fakeEventCanceller) CancelEventForTenant(_ context.Context, tenantID, eventID string) (events.Event, error) {
	f.cancelCalls++
	f.gotTenant, f.gotID = tenantID, eventID
	if f.cancelErr != nil {
		return events.Event{}, f.cancelErr
	}
	ev, ok := f.porTenant[tenantID][eventID]
	if !ok {
		return events.Event{}, events.ErrEventNotFound
	}
	if ev.Status == events.StatusOpen {
		ev.Status = events.StatusCancelled
		ev.ClosedAt = cancelInstante
		f.porTenant[tenantID][eventID] = ev
		f.transiciones++
	}
	return ev, nil
}

// cancellerSembrado deja un carrito abierto en cada tenant. Se toma el .Event a
// propósito: el puerto del cancel habla en events.Event, sin los derivados del
// join del listado (content_state/content_ref son de la bandeja, no de aquí).
func cancellerSembrado() *fakeEventCanceller {
	return &fakeEventCanceller{porTenant: map[string]map[string]events.Event{
		tenantA: {evCartA: evento(evCartA, "cart", "", "", false).Event},
		tenantB: {evCartB: evento(evCartB, "cart", "", "", false).Event},
	}}
}

// cancelDeps arma unas Deps con el canceller y las CUATRO features encendidas en
// los dos tenants (mismo criterio que eventosDeps: los tests de aislamiento deben
// fallar por tenant, no por plan).
func cancelDeps(c *fakeEventCanceller) publicapi.Deps {
	return publicapi.Deps{EventCanceller: c, Entitlements: conTodosLosTipos()}
}

func cancelURL(id string) string {
	return "/api/v1/conversation-events/" + id + "/cancel"
}

// cancelledEventWire espeja el contrato del 200 en el cable: la MISMA shape que
// una fila del listado (conversationevents_test.go), porque ese es el contrato.
type cancelledEventWire struct {
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
}

func decodeCancelado(t *testing.T, body []byte) cancelledEventWire {
	t.Helper()
	var out cancelledEventWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal del cancelado: %v; body=%s", err, body)
	}
	return out
}

// TestConversationEventCancel_200_CaminoFeliz: el 200 con la fila cancelada y —lo
// que el cuerpo no enseña— que el tenant que operó salió DEL TOKEN aunque la query
// intente colar otro (INV-8: jamás de query/body/path).
func TestConversationEventCancel_200_CaminoFeliz(t *testing.T) {
	c := cancellerSembrado()
	api := newAPI(cancelDeps(c), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA)+"?tenant_id="+tenantB, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if c.gotTenant != tenantA || c.gotID != evCartA {
		t.Fatalf("el puerto recibió (tenant=%q, id=%q) con el token de A y ?tenant_id=B: "+
			"el tenant SALE DEL TOKEN (INV-8)", c.gotTenant, c.gotID)
	}
	got := decodeCancelado(t, rec.Body.Bytes())
	if got.ID != evCartA || got.Status != "cancelled" || got.Kind != "cart" {
		t.Fatalf("cuerpo=%+v; quiero el carrito cancelado", got)
	}
	if got.ClosedAt != "2026-01-02T10:00:00Z" {
		t.Fatalf("closed_at=%q; quiero el sello RFC3339 UTC de la cancelación", got.ClosedAt)
	}
	if got.ContactID != contactoOpaco {
		t.Fatalf("cuerpo=%+v; el contacto viaja OPACO (INV-01)", got)
	}
	// El cancel ya no habla de intakes (D-043.21: el abandono va por evento) y los
	// derivados del listado no se recalculan aquí: ni intake_id ni content_* en el
	// cuerpo — la clave omitida, no vacía.
	for _, clave := range []string{"intake_id", "content_state", "content_ref"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(`"`+clave+`"`)) {
			t.Fatalf("la respuesta del cancel publica %q; body=%s", clave, rec.Body.String())
		}
	}
	if got.Stale {
		t.Fatalf("stale=true en la respuesta de cancelación: la marca «vencido» es de eventos abiertos")
	}
	if c.transiciones != 1 {
		t.Fatalf("transiciones=%d; el evento abierto debió cancelarse exactamente una vez", c.transiciones)
	}
}

// TestConversationEventCancel_200_Idempotente: la segunda llamada es 200 con la
// fila SIN CAMBIOS —byte a byte, gracias al reloj fijo del fake— y el puerto no
// recibió una segunda transición (la recibió el fake y decidió no mutar: eso es lo
// que el contrato del runtime promete y el transporte debe dejar pasar tal cual).
func TestConversationEventCancel_200_Idempotente(t *testing.T) {
	c := cancellerSembrado()
	api := newAPI(cancelDeps(c), intakesKeys())

	rec1 := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	rec2 := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("codes=(%d,%d), quiero (200,200); body2=%s", rec1.Code, rec2.Code, rec2.Body.String())
	}
	if !bytes.Equal(rec1.Body.Bytes(), rec2.Body.Bytes()) {
		t.Fatalf("la segunda cancelación cambió la fila:\n1ª=%s\n2ª=%s", rec1.Body.String(), rec2.Body.String())
	}
	if c.transiciones != 1 {
		t.Fatalf("transiciones=%d tras dos llamadas; la segunda NO puede volver a transicionar", c.transiciones)
	}
	if c.cancelCalls != 2 {
		t.Fatalf("cancelCalls=%d; el handler no decide la idempotencia, la delega en el puerto", c.cancelCalls)
	}
}

// TestConversationEventCancel_404_TresCaminosUnCuerpo: los tres motivos por los que
// el dueño no ve el evento responden el MISMO 404 con el MISMO cuerpo —id
// inexistente, id de otro tenant (NUNCA 403: un 403 confirmaría que el id existe;
// aquí el {id} es explícito, así que a diferencia del listado el cross-tenant SÍ es
// alcanzable y el 404 obligatorio), y tipo cuya feature el tenant no tiene (un tipo
// que no ves en el listado no existe para ti)—. Ninguno de los tres llegó a
// cancelar nada: «cerrar los que no ve» está prohibido por la nota del gate.
func TestConversationEventCancel_404_TresCaminosUnCuerpo(t *testing.T) {
	// Camino 1: no existe en ningún tenant.
	c := cancellerSembrado()
	api := newAPI(cancelDeps(c), intakesKeys())
	recNoExiste := call(api, keyAIntakes, http.MethodPost, cancelURL(evNadie), "")
	if recNoExiste.Code != http.StatusNotFound {
		t.Fatalf("inexistente: code=%d, quiero 404; body=%s", recNoExiste.Code, recNoExiste.Body.String())
	}

	// Camino 2: existe, pero para tenantB, y el token es de A. El fake simula el
	// acotado del runtime: para (tenantA, evCartB) no hay fila ⇒ ErrEventNotFound.
	recCross := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartB), "")
	if recCross.Code == http.StatusForbidden {
		t.Fatalf("cross-tenant respondió 403: eso le CONFIRMA al tenant A que el id de B existe")
	}
	if recCross.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant: code=%d, quiero 404; body=%s", recCross.Code, recCross.Body.String())
	}
	if c.cancelCalls != 0 {
		t.Fatalf("cancelCalls=%d; nada de esto debió intentar cancelar", c.cancelCalls)
	}
	if ev := c.porTenant[tenantB][evCartB]; ev.Status != events.StatusOpen {
		t.Fatalf("el evento de B quedó %q: el token de A canceló una fila ajena", ev.Status)
	}

	// Camino 3: existe para A, pero su tipo (cart) no está en el plan del tenant
	// —solo survey encendida; el gate pasa porque survey basta para entrar—.
	soloSurvey := entitlements.NewFake()
	soloSurvey.Enable(tenantA, entitlements.FeatureSurvey)
	cKind := cancellerSembrado()
	apiKind := newAPI(publicapi.Deps{EventCanceller: cKind, Entitlements: soloSurvey}, intakesKeys())
	recKind := call(apiKind, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	if recKind.Code != http.StatusNotFound {
		t.Fatalf("tipo sin feature: code=%d, quiero 404; body=%s", recKind.Code, recKind.Body.String())
	}
	if cKind.cancelCalls != 0 {
		t.Fatalf("cancelCalls=%d con el tipo vetado: cancelar lo que no se ve está prohibido", cKind.cancelCalls)
	}

	// Y el cuerpo es EL MISMO en los tres: desde fuera no se distingue cuál fue.
	if !bytes.Equal(recNoExiste.Body.Bytes(), recCross.Body.Bytes()) ||
		!bytes.Equal(recNoExiste.Body.Bytes(), recKind.Body.Bytes()) {
		t.Fatalf("los 404 difieren y filtran el motivo:\nno-existe=%s\ncross=%s\nkind=%s",
			recNoExiste.Body.String(), recCross.Body.String(), recKind.Body.String())
	}
}

// TestConversationEventCancel_404_IdQueNoEsUUID: un id que no puede existir recibe
// el mismo 404 que uno que no existe, sin llegar al puerto — que en producción es
// una columna UUID y convertiría el typo en un 500 de Postgres.
func TestConversationEventCancel_404_IdQueNoEsUUID(t *testing.T) {
	c := cancellerSembrado()
	api := newAPI(cancelDeps(c), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, cancelURL("pedido-de-marta"), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	if c.getCalls != 0 {
		t.Fatalf("el puerto se consultó %d veces con un id imposible", c.getCalls)
	}
	recReal := call(api, keyAIntakes, http.MethodPost, cancelURL(evNadie), "")
	if !bytes.Equal(rec.Body.Bytes(), recReal.Body.Bytes()) {
		t.Fatalf("el 404 del id imposible difiere del real:\nimposible=%s\nreal=%s",
			rec.Body.String(), recReal.Body.String())
	}
}

// TestConversationEventCancel_403_SinFeature_CuerposIdenticos es el criterio
// literal de T4.2: sin NINGUNA feature de tipo, 403 «sin revelar si el evento
// existe» — el gate corta ANTES de mirar el id, así que el cuerpo es BYTE-IDÉNTICO
// para un id que existe y para uno inventado. No se da por hecho del middleware:
// se compara.
func TestConversationEventCancel_403_SinFeature_CuerposIdenticos(t *testing.T) {
	c := cancellerSembrado()
	api := newAPI(publicapi.Deps{
		EventCanceller: c,
		Entitlements:   entitlements.NewFake(), // ninguna feature encendida
	}, intakesKeys())

	recExiste := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	recInventado := call(api, keyAIntakes, http.MethodPost, cancelURL(evNadie), "")
	if recExiste.Code != http.StatusForbidden || recInventado.Code != http.StatusForbidden {
		t.Fatalf("codes=(%d,%d), quiero (403,403); body=%s",
			recExiste.Code, recInventado.Code, recExiste.Body.String())
	}
	if !bytes.Equal(recExiste.Body.Bytes(), recInventado.Body.Bytes()) {
		t.Fatalf("el 403 revela si el id existe:\nexistente=%s\ninventado=%s",
			recExiste.Body.String(), recInventado.Body.String())
	}
	if c.getCalls != 0 || c.cancelCalls != 0 {
		t.Fatalf("el puerto se tocó (%d get, %d cancel) sin la feature: el gate corta ANTES del id",
			c.getCalls, c.cancelCalls)
	}
	// Y el evento sigue abierto: el 403 no dejó efectos.
	if ev := c.porTenant[tenantA][evCartA]; ev.Status != events.StatusOpen {
		t.Fatalf("el evento quedó %q tras un 403", ev.Status)
	}
}

// TestConversationEventCancel_403_SinScope: la feature tampoco basta — sin
// intakes.write no se cancela (mismo reparto que el resto de la bandeja: el grant
// dice «puedes operar esto», la feature «tu plan lo incluye»).
func TestConversationEventCancel_403_SinScope(t *testing.T) {
	c := cancellerSembrado()
	api := newAPI(cancelDeps(c), intakesKeys())

	// keyARead porta flows.read: autenticada, del tenant A, sin intakes.write.
	rec := call(api, keyARead, http.MethodPost, cancelURL(evCartA), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin el scope; body=%s", rec.Code, rec.Body.String())
	}
	if c.getCalls != 0 || c.cancelCalls != 0 {
		t.Fatalf("el puerto se tocó (%d get, %d cancel) sin el scope", c.getCalls, c.cancelCalls)
	}
}

// TestConversationEventCancel_401_SinCredencial: sin token no hay tenant del que
// acotar, y sin tenant este endpoint no tiene pregunta que hacer.
func TestConversationEventCancel_401_SinCredencial(t *testing.T) {
	api := newAPI(cancelDeps(cancellerSembrado()), intakesKeys())

	rec := call(api, "", http.MethodPost, cancelURL(evCartA), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestConversationEventCancel_500_SiFallaAllowedKinds: el resolver caído DESPUÉS
// del gate es 5xx, no un 404 — igual que en el listado: AllowedKinds decide
// CONTENIDO y su error se PROPAGA; un 404 diría «ese evento no existe» cuando la
// verdad es «no pude mirar qué tipos ves». Y por supuesto no se canceló nada.
func TestConversationEventCancel_500_SiFallaAllowedKinds(t *testing.T) {
	c := cancellerSembrado()
	// 1 pregunta buena: la del gate (corta en cuanto encuentra una encendida).
	feats := &resolverQueFallaTrasN{Fake: conTodosLosTipos(), n: 1}
	api := newAPI(publicapi.Deps{EventCanceller: c, Entitlements: feats}, intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500 con el resolver caído tras el gate; body=%s",
			rec.Code, rec.Body.String())
	}
	if c.cancelCalls != 0 {
		t.Fatalf("se canceló sin saber qué tipos ve el tenant (cancelCalls=%d)", c.cancelCalls)
	}
	if ev := c.porTenant[tenantA][evCartA]; ev.Status != events.StatusOpen {
		t.Fatalf("el evento quedó %q tras el 500", ev.Status)
	}
}

// TestConversationEventCancel_500_ErroresDelPuerto: un fallo de infraestructura en
// cualquiera de las dos operaciones es 500 con el shape de error estándar, nunca
// un 404 que diría «no existe» de algo que no se pudo mirar.
func TestConversationEventCancel_500_ErroresDelPuerto(t *testing.T) {
	cGet := cancellerSembrado()
	cGet.getErr = context.DeadlineExceeded
	api := newAPI(cancelDeps(cGet), intakesKeys())
	if rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("get caído: code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}

	cCancel := cancellerSembrado()
	cCancel.cancelErr = context.DeadlineExceeded
	api = newAPI(cancelDeps(cCancel), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("cancel caído: code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("el 500 no lleva el shape {error}: %s", rec.Body.String())
	}
}

// TestConversationEventCancel_Auditada: la cancelación es una ESCRITURA
// irreversible y deja bitácora — action intakes.write (el scope) y el recurso
// NUEVO conversation_event, que es lo que distingue en la auditoría cancelar un
// evento de descartar una solicitud.
func TestConversationEventCancel_Auditada(t *testing.T) {
	auditor := &recordingAuditor{}
	api := apiConLog(cancelDeps(cancellerSembrado()), intakesKeys(), &bytes.Buffer{}, auditor)

	if rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), ""); rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(auditor.eventos) != 1 {
		t.Fatalf("se auditaron %d eventos, quiero exactamente 1: %+v", len(auditor.eventos), auditor.eventos)
	}
	got := auditor.eventos[0]
	if got.Action != "intakes.write" || got.Resource != "conversation_event" {
		t.Fatalf("auditado (action=%q, resource=%q); quiero (intakes.write, conversation_event)",
			got.Action, got.Resource)
	}
	if got.TenantID != tenantA || got.Result != "success" {
		t.Fatalf("auditado %+v; quiero tenant=%s result=success", got, tenantA)
	}
}

// TestConversationEventCancel_NoSeMontaSinDependencias: sin el canceller o sin el
// resolver la ruta no existe (404 de mux). Y el montaje es INDEPENDIENTE del
// listado: el canceller sin lister monta el cancel (y no el GET) — así vivió la
// bandeja entre la Ola 3 y la 4, pero al revés.
func TestConversationEventCancel_NoSeMontaSinDependencias(t *testing.T) {
	for nombre, d := range map[string]publicapi.Deps{
		"sin canceller": {Entitlements: conTodosLosTipos()},
		"sin features":  {EventCanceller: cancellerSembrado()},
	} {
		api := newAPI(d, intakesKeys())
		rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: code=%d, quiero 404 de ruta inexistente; body=%s",
				nombre, rec.Code, rec.Body.String())
		}
	}

	// Canceller sin lister: el POST funciona, el GET del listado no está.
	api := newAPI(cancelDeps(cancellerSembrado()), intakesKeys())
	if rec := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel sin lister: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET sin lister: code=%d, quiero 404 de ruta inexistente", rec.Code)
	}
}

// TestConversationEvents_VisibilidadYCancelabilidadSonElMISMOCriterio es la nota del
// gate (`registerConversationEvents`) puesta a prueba EN PAREJA, que es la única
// forma de probarla: hasta aquí el listado fijaba su filtro por tipos en un test y
// el cancel el suyo en otro, y dos criterios que coinciden por separado pueden
// separarse sin que ningún test se ponga rojo.
//
// El montaje es el que se pidió refutar: un tenant con SOLO `survey` encendida y un
// evento `cart` VIVO suyo. La pareja tiene que quedar simétrica en las dos
// direcciones — lo que no se ve no se cancela, y lo que se ve sí se cancela — porque
// el mismo criterio que oculta es el que rechaza. Un 403 aquí, en vez del 404, le
// confirmaría al dueño que ese id existe justo cuando el listado le dice que no.
func TestConversationEvents_VisibilidadYCancelabilidadSonElMISMOCriterio(t *testing.T) {
	soloSurvey := entitlements.NewFake()
	soloSurvey.Enable(tenantA, entitlements.FeatureSurvey)
	lister, canceller := listerSembrado(), cancellerSembrado()
	api := newAPI(publicapi.Deps{
		ConversationEvents: lister, EventCanceller: canceller, Entitlements: soloSurvey,
	}, intakesKeys())

	// (1) El listado: entra (survey basta para el gate) y NO enseña el cart.
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/conversation-events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listado: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var listado conversationEventListDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &listado); err != nil {
		t.Fatalf("listado ilegible (%v): %s", err, rec.Body.String())
	}
	vistos := map[string]bool{}
	for _, ev := range listado.Events {
		vistos[ev.Kind] = true
	}
	if !vistos["survey"] || vistos["cart"] {
		t.Fatalf("el listado debe enseñar survey y ocultar cart; vio %v", vistos)
	}

	// (2) El cancel del cart INVISIBLE: 404, y sin llegar a cancelar nada.
	recCart := call(api, keyAIntakes, http.MethodPost, cancelURL(evCartA), "")
	if recCart.Code != http.StatusNotFound {
		t.Fatalf("cancelar un tipo que el listado oculta: code=%d, quiero 404; body=%s",
			recCart.Code, recCart.Body.String())
	}
	if canceller.cancelCalls != 0 {
		t.Fatalf("cancelCalls=%d: se intentó cancelar lo que no se ve", canceller.cancelCalls)
	}

	// (3) La otra mitad de la simetría, que es la que impide «arreglar» esto
	// devolviendo 404 a todo: un evento de un tipo que SÍ se ve sí se cancela.
	evSurvey := "e4c55555-5555-4555-8555-555555555555"
	canceller.porTenant[tenantA][evSurvey] = evento(evSurvey, "survey", "", "", false).Event
	recSurvey := call(api, keyAIntakes, http.MethodPost, cancelURL(evSurvey), "")
	if recSurvey.Code != http.StatusOK {
		t.Fatalf("cancelar un tipo VISIBLE: code=%d, quiero 200; body=%s",
			recSurvey.Code, recSurvey.Body.String())
	}
}
