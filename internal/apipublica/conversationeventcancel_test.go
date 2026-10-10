package apipublica_test

// conversationeventcancel_test.go — cubre el contrato de conversationeventcancel.go
// (ConversationEventCanceller, ConversationEventCancelDeps, MountConversationEventCancel): el
// montaje condicional, la cadena W con su gate, el 404 único de sus cuatro caminos, la
// idempotencia que deja pasar, los 500 y el wire de I19. Los dobles del resolver son los de
// conversationevents_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

const (
	conversationEventCancelPattern  = "POST /api/v1/conversation-events/{id}/cancel"
	conversationEventCancelPerm     = "intakes.write"
	conversationEventCancelResource = "conversation_event"

	// Ids de los eventos sembrados (UUID: lo que no lo es recibe 404 sin llegar al puerto).
	conversationEventCancelCartA   = "aaaa0000-0000-4000-8000-00000000000a"
	conversationEventCancelSurveyA = "aaaa0000-0000-4000-8000-00000000005a"
	conversationEventCancelCartB   = "bbbb0000-0000-4000-8000-00000000000b"
	conversationEventCancelMissing = "dddd0000-0000-4000-8000-00000000000d"

	conversationEventCancelNotFoundBody = `{"error":"evento no encontrado"}`
	conversationEventCancelMsgGet       = "no se pudo leer el evento"
	conversationEventCancelMsgCancel    = "no se pudo cancelar el evento"
	conversationEventCancelMsgKinds     = "no se pudieron resolver los tipos habilitados del plan"

	// El 200 de cancelar conversationEventCancelCartA con el doble recién sembrado.
	conversationEventCancelCartABody = `{"id":"` + conversationEventCancelCartA + `","history_id":"cart-2026-10-01-1200","kind":"cart",` +
		`"status":"cancelled","contact_id":"contacto-opaco","session_id":"ses-1","stale":false,` +
		`"created_at":"2026-10-01T12:00:00Z","last_activity_at":"2026-10-01T12:30:00Z","closed_at":"2026-10-05T10:00:00Z"}`
)

// El puerto de I19 lo cumple el runtime REAL del módulo conversación nuevo.
var _ apipublica.ConversationEventCanceller = (*runtime.Runtime)(nil)

// conversationEventCancelSealedAt es el instante FIJO en que el doble sella closed_at: con él,
// dos respuestas 200 del mismo evento son byte-idénticas.
var conversationEventCancelSealedAt = time.Date(2026, 10, 5, 7, 0, 0, 0, time.FixedZone("-03", -3*3600))

// conversationEventCancelSpy es ConversationEventCanceller con el contrato del runtime en
// miniatura: acotado por tenant (otro tenant ⇒ events.ErrEventNotFound) e idempotente (ya
// terminal ⇒ la fila tal cual). Apunta cada llamada y cuántas transiciones hizo de verdad.
type conversationEventCancelSpy struct {
	rows      map[string]events.Event
	getErr    error
	cancelErr error

	calls       []string
	transitions int
}

var _ apipublica.ConversationEventCanceller = (*conversationEventCancelSpy)(nil)

func (s *conversationEventCancelSpy) GetEventForTenant(_ context.Context, tenantID, eventID string) (events.Event, error) {
	s.calls = append(s.calls, "get "+tenantID+" "+eventID)
	if s.getErr != nil {
		return events.Event{}, s.getErr
	}
	ev, ok := s.rows[tenantID+"|"+eventID]
	if !ok {
		return events.Event{}, fmt.Errorf("doble: %w", events.ErrEventNotFound)
	}
	return ev, nil
}

func (s *conversationEventCancelSpy) CancelEventForTenant(_ context.Context, tenantID, eventID string) (events.Event, error) {
	s.calls = append(s.calls, "cancel "+tenantID+" "+eventID)
	if s.cancelErr != nil {
		return events.Event{}, s.cancelErr
	}
	ev, ok := s.rows[tenantID+"|"+eventID]
	if !ok {
		return events.Event{}, fmt.Errorf("doble: %w", events.ErrEventNotFound)
	}
	if ev.Status == events.StatusOpen {
		ev.Status, ev.ClosedAt = events.StatusCancelled, conversationEventCancelSealedAt
		s.rows[tenantID+"|"+eventID] = ev
		s.transitions++
	}
	return ev, nil
}

// conversationEventCancelRow arma un evento abierto con lo justo para identificarlo. Lleva flujo
// y versión para comprobar que NO viajan.
func conversationEventCancelRow(tenantID, id, kind string) events.Event {
	return events.Event{ID: id, TenantID: tenantID, SessionID: "ses-1", ContactID: "contacto-opaco", Kind: kind,
		HistoryID: kind + "-2026-10-01-1200", Status: events.StatusOpen, FlowID: "flujo-secreto", FlowVersion: 9,
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), LastActivityAt: time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)}
}

// conversationEventCancelSeeded deja un carrito y una encuesta abiertos en A y un carrito en B.
func conversationEventCancelSeeded() *conversationEventCancelSpy {
	other := apipublicahelpertest.TenantB
	return &conversationEventCancelSpy{rows: map[string]events.Event{
		tenantA + "|" + conversationEventCancelCartA:   conversationEventCancelRow(tenantA, conversationEventCancelCartA, "cart"),
		tenantA + "|" + conversationEventCancelSurveyA: conversationEventCancelRow(tenantA, conversationEventCancelSurveyA, "survey"),
		other + "|" + conversationEventCancelCartB:     conversationEventCancelRow(other, conversationEventCancelCartB, "cart"),
	}}
}

// conversationEventCancelTarget es el camino de I19 para ese id.
func conversationEventCancelTarget(id string) string {
	return "/api/v1/conversation-events/" + id + "/cancel"
}

// conversationEventCancelCara monta I19 con k, el puerto y el resolver dados.
func conversationEventCancelCara(k apipublica.Common, canceller apipublica.ConversationEventCanceller, feats entitlements.Resolver) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountConversationEventCancel(c, k, apipublica.ConversationEventCancelDeps{Canceller: canceller, Entitlements: feats})
	return c
}

// conversationEventCancelCaraWithList monta I18 e I19 en la MISMA cara, con el mismo resolver.
func conversationEventCancelCaraWithList(k apipublica.Common, lister apipublica.ConversationEventLister,
	canceller apipublica.ConversationEventCanceller, feats entitlements.Resolver) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountConversationEvents(c, k, apipublica.ConversationEventsDeps{Events: lister, Entitlements: feats})
	apipublica.MountConversationEventCancel(c, k, apipublica.ConversationEventCancelDeps{Canceller: canceller, Entitlements: feats})
	return c
}

// conversationEventCancelDo sirve UN POST a I19 como tenantA con SOLO el permiso de escritura, y
// devuelve también el banco para mirar la auditoría.
func conversationEventCancelDo(t *testing.T, canceller apipublica.ConversationEventCanceller, feats entitlements.Resolver, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	cara := conversationEventCancelCara(h.Common(), canceller, feats)
	return h, h.Call(cara, h.With(tenantA, conversationEventCancelPerm), http.MethodPost, target, body)
}

// conversationEventCancelWantCalls exige las llamadas EXACTAS que recibió el puerto, en orden.
func conversationEventCancelWantCalls(t *testing.T, what string, canceller *conversationEventCancelSpy, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(append([]string{}, canceller.calls...), append([]string{}, want...)) {
		t.Errorf("%s: el puerto recibió %q, quiero %q", what, canceller.calls, want)
	}
}

// conversationEventCancelWantOneAudit exige EXACTAMENTE un registro de I19 con ese resultado y
// código.
func conversationEventCancelWantOneAudit(t *testing.T, what string, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: I19 dejó %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != conversationEventCancelPerm || r.Resource != conversationEventCancelResource ||
		r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, conversationEventCancelPerm, conversationEventCancelResource, result, status)
	}
}

func TestMountConversationEventCancel_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	canceller := conversationEventCancelSeeded()
	cara := conversationEventCancelCara(h.Common(), canceller, conversationEventsAllFeatures())
	wantPatterns(t, "I19", cara, []string{conversationEventCancelPattern})
	checkChain(t, h, cara, routeCase{id: "I19", method: http.MethodPost, target: conversationEventCancelTarget(conversationEventCancelCartA),
		perm: conversationEventCancelPerm, resource: conversationEventCancelResource, want: http.StatusOK})
	// De las cuatro peticiones de checkChain solo la que pasa la cadena toca el puerto.
	conversationEventCancelWantCalls(t, "I19 cadena", canceller,
		"get "+tenantA+" "+conversationEventCancelCartA, "cancel "+tenantA+" "+conversationEventCancelCartA)
}

// TestMountConversationEventCancel_NotMountedWithoutDeps: sin el puerto o sin el resolver la ruta
// no existe (trampa T-11), y el montaje ni mira la cadena: tampoco hace panic con MW nil.
func TestMountConversationEventCancel_NotMountedWithoutDeps(t *testing.T) {
	cases := []struct {
		name string
		deps apipublica.ConversationEventCancelDeps
	}{
		{"no canceller", apipublica.ConversationEventCancelDeps{Entitlements: conversationEventsAllFeatures()}},
		{"no resolver", apipublica.ConversationEventCancelDeps{Canceller: conversationEventCancelSeeded()}},
		{"neither", apipublica.ConversationEventCancelDeps{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := apipublica.Nueva()
			apipublica.MountConversationEventCancel(cara, h.Common(), tc.deps)
			wantPatterns(t, tc.name, cara, nil)
			rec := h.Call(cara, h.With(tenantA, conversationEventCancelPerm), http.MethodPost,
				conversationEventCancelTarget(conversationEventCancelCartA), "")
			wantCode(t, tc.name, rec, http.StatusNotFound)
			if rec.Body.String() == conversationEventCancelNotFoundBody {
				t.Errorf("%s: respondió el 404 del handler; quiero el de ruta inexistente", tc.name)
			}

			if v := recuperar(func() {
				apipublica.MountConversationEventCancel(apipublica.Nueva(), apipublica.Common{}, tc.deps)
			}); v != nil {
				t.Errorf("%s con MW nil: panic = %v; quiero que no monte nada ni mire la cadena", tc.name, v)
			}
		})
	}
}

func TestMountConversationEventCancel_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		conversationEventCancelCara(apipublica.Common{}, conversationEventCancelSeeded(), conversationEventsAllFeatures())
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountConversationEventCancel") {
		t.Errorf("MountConversationEventCancel con MW nil: panic = %v; quiero un panic de cableado que nombre MountConversationEventCancel", v)
	}
}

// TestMountConversationEventCancel_DoesNotMountTheList: la cancelación se monta sin el listado,
// que en esta cara no existe.
func TestMountConversationEventCancel_DoesNotMountTheList(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := conversationEventCancelCara(h.Common(), conversationEventCancelSeeded(), conversationEventsAllFeatures())
	token := h.With(tenantA, conversationEventCancelPerm, "intakes.read")
	wantCode(t, "I19 sola", h.Call(cara, token, http.MethodPost, conversationEventCancelTarget(conversationEventCancelCartA), ""), http.StatusOK)
	wantCode(t, "I18 sin su Mount", h.Call(cara, token, http.MethodGet, "/api/v1/conversation-events", ""), http.StatusNotFound)
}

// TestMountConversationEventCancel_CancelsWithTheTokenTenant: el 200 es el evento que devolvió la
// cancelación, con la forma de una fila del listado; el tenant que operó salió DEL TOKEN aunque
// la query intente colar otro, y el cuerpo de la petición no se lee.
func TestMountConversationEventCancel_CancelsWithTheTokenTenant(t *testing.T) {
	other := apipublicahelpertest.TenantB
	canceller := conversationEventCancelSeeded()
	target := conversationEventCancelTarget(conversationEventCancelCartA) + "?tenant_id=" + other
	h, rec := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), target, `{no es json, "tenant_id":"`+other+`"`)
	wantCode(t, "I19", rec, http.StatusOK)
	wantExactBody(t, "I19", rec, conversationEventCancelCartABody)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("I19: Content-Type %q, quiero application/json", got)
	}
	conversationEventCancelWantCalls(t, "I19", canceller,
		"get "+tenantA+" "+conversationEventCancelCartA, "cancel "+tenantA+" "+conversationEventCancelCartA)
	if got := canceller.rows[other+"|"+conversationEventCancelCartB].Status; got != events.StatusOpen {
		t.Errorf("I19: el evento del otro tenant quedó %q; nadie lo tocó", got)
	}
	conversationEventCancelWantOneAudit(t, "I19", h, "success", http.StatusOK)
}

// TestMountConversationEventCancel_IdempotenceIsThePorts: la segunda llamada es 200 con la fila
// SIN CAMBIOS, byte a byte, y la cara vuelve a llamar al puerto en vez de adelantar el desenlace;
// un evento que ya estaba cerrado sale tal cual, con su estado.
func TestMountConversationEventCancel_IdempotenceIsThePorts(t *testing.T) {
	canceller := conversationEventCancelSeeded()
	target := conversationEventCancelTarget(conversationEventCancelCartA)
	_, first := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), target, "")
	_, second := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), target, "")
	wantCode(t, "primera", first, http.StatusOK)
	wantCode(t, "segunda", second, http.StatusOK)
	wantExactBody(t, "segunda", second, first.Body.String())
	if canceller.transitions != 1 {
		t.Errorf("dos cancelaciones hicieron %d transiciones, quiero 1", canceller.transitions)
	}
	conversationEventCancelWantCalls(t, "dos cancelaciones", canceller,
		"get "+tenantA+" "+conversationEventCancelCartA, "cancel "+tenantA+" "+conversationEventCancelCartA,
		"get "+tenantA+" "+conversationEventCancelCartA, "cancel "+tenantA+" "+conversationEventCancelCartA)

	canceller = conversationEventCancelSeeded()
	closed := conversationEventCancelRow(tenantA, conversationEventCancelSurveyA, "survey")
	closed.Status, closed.ClosedAt = events.StatusClosed, time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	canceller.rows[tenantA+"|"+conversationEventCancelSurveyA] = closed
	_, rec := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), conversationEventCancelTarget(conversationEventCancelSurveyA), "")
	wantCode(t, "ya cerrado", rec, http.StatusOK)
	wantExactBody(t, "ya cerrado", rec, `{"id":"`+conversationEventCancelSurveyA+`","history_id":"survey-2026-10-01-1200","kind":"survey",`+
		`"status":"closed","contact_id":"contacto-opaco","session_id":"ses-1","stale":false,`+
		`"created_at":"2026-10-01T12:00:00Z","last_activity_at":"2026-10-01T12:30:00Z","closed_at":"2026-10-02T08:00:00Z"}`)
	conversationEventCancelWantCalls(t, "ya cerrado", canceller,
		"get "+tenantA+" "+conversationEventCancelSurveyA, "cancel "+tenantA+" "+conversationEventCancelSurveyA)
}

// TestMountConversationEventCancel_OneNotFoundBody: los caminos por los que el dueño no ve el
// evento responden el MISMO 404 con el MISMO cuerpo, y ninguno llegó a cancelar nada.
func TestMountConversationEventCancel_OneNotFoundBody(t *testing.T) {
	onlySurvey := conversationEventsFeatures("survey")
	cases := []struct {
		name  string
		id    string
		feats entitlements.Resolver
		calls []string
	}{
		{"missing id", conversationEventCancelMissing, conversationEventsAllFeatures(),
			[]string{"get " + tenantA + " " + conversationEventCancelMissing}},
		{"id of another tenant", conversationEventCancelCartB, conversationEventsAllFeatures(),
			[]string{"get " + tenantA + " " + conversationEventCancelCartB}},
		{"kind outside the plan", conversationEventCancelCartA, onlySurvey,
			[]string{"get " + tenantA + " " + conversationEventCancelCartA}},
		{"id is a word", "marta", conversationEventsAllFeatures(), nil},
		{"id is a number", "42", conversationEventsAllFeatures(), nil},
		{"id with a trailing character", conversationEventCancelCartA + "0", conversationEventsAllFeatures(), nil},
		{"id with a non hex digit", "zzzz0000-0000-4000-8000-00000000000a", conversationEventsAllFeatures(), nil},
		{"id with repeated separators", "aaaa0000--000-4000-8000-00000000000a", conversationEventsAllFeatures(), nil},
		{"id with non ascii digits", "aaaa0000-0000-4000-8000-0000000000０a", conversationEventsAllFeatures(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canceller := conversationEventCancelSeeded()
			h, rec := conversationEventCancelDo(t, canceller, tc.feats, conversationEventCancelTarget(tc.id), "")
			wantCode(t, tc.name, rec, http.StatusNotFound)
			wantExactBody(t, tc.name, rec, conversationEventCancelNotFoundBody)
			conversationEventCancelWantCalls(t, tc.name, canceller, tc.calls...)
			if canceller.transitions != 0 {
				t.Errorf("%s: se canceló algo (%d transiciones)", tc.name, canceller.transitions)
			}
			conversationEventCancelWantOneAudit(t, tc.name, h, "failure", http.StatusNotFound)
		})
	}

	// Si es la cancelación quien dice que no existe, la respuesta honesta sigue siendo la misma.
	canceller := conversationEventCancelSeeded()
	canceller.cancelErr = fmt.Errorf("carrera: %w", events.ErrEventNotFound)
	_, rec := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), conversationEventCancelTarget(conversationEventCancelCartA), "")
	wantCode(t, "la cancelación no lo encuentra", rec, http.StatusNotFound)
	wantExactBody(t, "la cancelación no lo encuentra", rec, conversationEventCancelNotFoundBody)
}

// TestMountConversationEventCancel_IDArrivesAsWritten: las otras formas de UUID que la cara deja
// pasar llegan al puerto sin normalizar.
func TestMountConversationEventCancel_IDArrivesAsWritten(t *testing.T) {
	for _, id := range []string{
		strings.ReplaceAll(conversationEventCancelMissing, "-", ""),
		"urn:uuid:" + conversationEventCancelMissing,
		strings.ToUpper(conversationEventCancelMissing),
	} {
		canceller := conversationEventCancelSeeded()
		_, rec := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), conversationEventCancelTarget(id), "")
		wantCode(t, id, rec, http.StatusNotFound)
		conversationEventCancelWantCalls(t, id, canceller, "get "+tenantA+" "+id)
	}
}

// TestMountConversationEventCancel_GateDoesNotRevealTheEvent: sin NINGUNA feature de tipo es 403
// con el cuerpo byte-idéntico para un id que existe y para uno inventado; el puerto no se toca, y
// el corte —que va por dentro de la auditoría— queda registrado como fallo.
func TestMountConversationEventCancel_GateDoesNotRevealTheEvent(t *testing.T) {
	const gateBody = `{"error":"feature_not_enabled","features":["cart_basic","media","menu","survey"]}`
	for name, feats := range map[string]entitlements.Resolver{
		"no kind feature": conversationEventsFeatures(entitlements.FeatureIntakesExport),
		"resolver down":   &entitlementshelpertest.Fake{Err: errors.New("bd caída")},
	} {
		t.Run(name, func(t *testing.T) {
			for _, id := range []string{conversationEventCancelCartA, conversationEventCancelMissing, "marta"} {
				canceller := conversationEventCancelSeeded()
				h, rec := conversationEventCancelDo(t, canceller, feats, conversationEventCancelTarget(id), "")
				wantCode(t, name+" "+id, rec, http.StatusForbidden)
				wantExactBody(t, name+" "+id, rec, gateBody)
				conversationEventCancelWantCalls(t, name+" "+id, canceller)
				conversationEventCancelWantOneAudit(t, name+" "+id, h, "failure", http.StatusForbidden)
			}
		})
	}
}

// TestMountConversationEventCancel_UnresolvedKindsIs500: el resolver caído DESPUÉS del gate es
// 500, no un 404 que diría «ese evento no existe»; y no se canceló nada.
func TestMountConversationEventCancel_UnresolvedKindsIs500(t *testing.T) {
	// El gate pasa con la primera pregunta (cart_basic); la siguiente, ya en el handler, falla.
	feats := &conversationEventsFlakyResolver{Fake: conversationEventsAllFeatures(), okCalls: 1}
	canceller := conversationEventCancelSeeded()
	h, rec := conversationEventCancelDo(t, canceller, feats, conversationEventCancelTarget(conversationEventCancelCartA), "")
	wantCode(t, "resolver caído tras el gate", rec, http.StatusInternalServerError)
	wantErrorBody(t, "resolver caído tras el gate", rec, conversationEventCancelMsgKinds)
	conversationEventCancelWantCalls(t, "resolver caído tras el gate", canceller, "get "+tenantA+" "+conversationEventCancelCartA)
	conversationEventCancelWantOneAudit(t, "resolver caído tras el gate", h, "failure", http.StatusInternalServerError)
}

// TestMountConversationEventCancel_PortFailureIs500: un fallo de infraestructura en cualquiera de
// las dos operaciones es 500 con su texto, nunca un 404, y sin repetir el error.
func TestMountConversationEventCancel_PortFailureIs500(t *testing.T) {
	boom := errors.New("pq: DSN SECRETO rechazado")
	get, cancel := "get "+tenantA+" "+conversationEventCancelCartA, "cancel "+tenantA+" "+conversationEventCancelCartA
	cases := []struct {
		name     string
		sabotage func(*conversationEventCancelSpy)
		msg      string
		calls    []string
	}{
		{"get fails", func(s *conversationEventCancelSpy) { s.getErr = boom }, conversationEventCancelMsgGet, []string{get}},
		{"cancel fails", func(s *conversationEventCancelSpy) { s.cancelErr = boom }, conversationEventCancelMsgCancel, []string{get, cancel}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canceller := conversationEventCancelSeeded()
			tc.sabotage(canceller)
			h, rec := conversationEventCancelDo(t, canceller, conversationEventsAllFeatures(), conversationEventCancelTarget(conversationEventCancelCartA), "")
			wantCode(t, tc.name, rec, http.StatusInternalServerError)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if strings.Contains(rec.Body.String(), "SECRETO") {
				t.Errorf("%s: el cuerpo repite el error del puerto (%s)", tc.name, rec.Body.String())
			}
			conversationEventCancelWantCalls(t, tc.name, canceller, tc.calls...)
			conversationEventCancelWantOneAudit(t, tc.name, h, "failure", http.StatusInternalServerError)
		})
	}
}

// TestMountConversationEventCancel_SameCriterionAsTheList pone a prueba EN PAREJA la regla
// «visibilidad y cancelabilidad son el mismo criterio»: un tenant con SOLO `survey` y un carrito
// vivo suyo. Lo que el listado no le enseña no se cancela (404, no 403), y lo que sí le enseña se
// cancela.
func TestMountConversationEventCancel_SameCriterionAsTheList(t *testing.T) {
	feats := conversationEventsFeatures("survey")
	lister := conversationEventsNewSpy()
	canceller := conversationEventCancelSeeded()
	h := apipublicahelpertest.New(t)
	cara := conversationEventCancelCaraWithList(h.Common(), lister, canceller, feats)
	token := h.With(tenantA, "intakes.read", conversationEventCancelPerm)

	wantCode(t, "listado", h.Call(cara, token, http.MethodGet, "/api/v1/conversation-events", ""), http.StatusOK)
	if want := []string{"survey"}; !reflect.DeepEqual(lister.filter.Kinds, want) {
		t.Fatalf("listado: Kinds=%v, quiero %v", lister.filter.Kinds, want)
	}

	rec := h.Call(cara, token, http.MethodPost, conversationEventCancelTarget(conversationEventCancelCartA), "")
	wantCode(t, "cancelar lo que no se ve", rec, http.StatusNotFound)
	wantExactBody(t, "cancelar lo que no se ve", rec, conversationEventCancelNotFoundBody)
	if got := canceller.rows[tenantA+"|"+conversationEventCancelCartA].Status; got != events.StatusOpen {
		t.Errorf("cancelar lo que no se ve: el carrito quedó %q; tenía que seguir abierto", got)
	}

	rec = h.Call(cara, token, http.MethodPost, conversationEventCancelTarget(conversationEventCancelSurveyA), "")
	wantCode(t, "cancelar lo que se ve", rec, http.StatusOK)
	if got := canceller.rows[tenantA+"|"+conversationEventCancelSurveyA].Status; got != events.StatusCancelled {
		t.Errorf("cancelar lo que se ve: la encuesta quedó %q; tenía que cancelarse", got)
	}
}
