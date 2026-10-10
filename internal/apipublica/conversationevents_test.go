package apipublica_test

// conversationevents_test.go — cubre el contrato de conversationevents.go
// (ConversationEventLister, ConversationEventsDeps, MountConversationEvents): el montaje
// condicional, la cadena R con su gate de «alguna feature de tipo», la traducción de la query al
// filtro, el filtro por tipos del plan y el wire de I18. Aquí viven además los dobles del
// resolver que comparte conversationeventcancel_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

const (
	conversationEventsPattern = "GET /api/v1/conversation-events"
	conversationEventsTarget  = "/api/v1/conversation-events"
	conversationEventsPerm    = "intakes.read"

	conversationEventsContact = "c0ffee00-0000-4000-8000-000000000001"

	conversationEventsGateBody    = `{"error":"feature_not_enabled","features":["cart_basic","media","menu","survey"]}`
	conversationEventsMsgKinds    = "no se pudieron resolver los tipos habilitados del plan"
	conversationEventsMsgList     = "no se pudieron listar los eventos conversacionales"
	conversationEventsMsgStatus   = "status desconocido: usa open, closed o cancelled"
	conversationEventsMsgContent  = "content desconocido: usa any, none o alive"
	conversationEventsMsgContact  = "contact_id inválido: es el identificador opaco (UUID) del contacto"
	conversationEventsMsgStale    = "stale inválido: usa true o false"
	conversationEventsEmptyAnswer = `{"events":[],"page":1,"page_size":50,"total":0}`
)

// El puerto del listado lo cumple el store REAL del módulo conversación nuevo.
var _ apipublica.ConversationEventLister = (*events.Store)(nil)

// conversationEventsListerSpy es ConversationEventLister: contesta la página que se le siembra
// (o err) y apunta cada llamada con su tenant y su filtro.
type conversationEventsListerSpy struct {
	page events.EventPage
	err  error

	calls  int
	tenant string
	filter events.ListFilter
}

var _ apipublica.ConversationEventLister = (*conversationEventsListerSpy)(nil)

func (s *conversationEventsListerSpy) ListEvents(_ context.Context, tenantID string, f events.ListFilter) (events.EventPage, error) {
	s.calls++
	s.tenant, s.filter = tenantID, f
	return s.page, s.err
}

// conversationEventsNewSpy devuelve un doble que contesta una página vacía bien formada.
func conversationEventsNewSpy() *conversationEventsListerSpy {
	return &conversationEventsListerSpy{page: events.EventPage{Page: 1, PageSize: events.DefaultPageSize}}
}

// conversationEventsFlakyResolver responde como su Fake las primeras okCalls preguntas y después
// falla. Modela lo ÚNICO que deja al handler sin poder resolver los tipos: el gate ya pasó y la
// siguiente consulta encuentra la BD caída.
type conversationEventsFlakyResolver struct {
	*entitlementshelpertest.Fake
	okCalls int
	calls   int
}

var _ entitlements.Resolver = (*conversationEventsFlakyResolver)(nil)

func (r *conversationEventsFlakyResolver) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	r.calls++
	if r.calls > r.okCalls {
		return false, errors.New("resolver: DSN SECRETO rechazado")
	}
	return r.Fake.Has(ctx, tenantID, feature)
}

// conversationEventsFeatures devuelve un resolver con esas features encendidas en los DOS
// tenants de referencia: los tests de aislamiento tienen que fallar por tenant, no por plan.
func conversationEventsFeatures(features ...string) *entitlementshelpertest.Fake {
	f := entitlementshelpertest.NewFake()
	for _, tenantID := range []string{tenantA, apipublicahelpertest.TenantB} {
		for _, feature := range features {
			f.Enable(tenantID, feature)
		}
	}
	return f
}

// conversationEventsAllFeatures enciende las features de TODOS los tipos. Se apoya en
// events.KindFeatures() y no en una lista escrita a mano: si nace un quinto tipo, entra solo.
func conversationEventsAllFeatures() *entitlementshelpertest.Fake {
	return conversationEventsFeatures(events.KindFeatures()...)
}

// conversationEventsCara monta I18 con k, el puerto y el resolver dados.
func conversationEventsCara(k apipublica.Common, lister apipublica.ConversationEventLister, feats entitlements.Resolver) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountConversationEvents(c, k, apipublica.ConversationEventsDeps{Events: lister, Entitlements: feats})
	return c
}

// conversationEventsDo sirve UN GET a I18 como tenantA con SOLO el permiso de lectura y la query
// dada, y devuelve también el banco para mirar la auditoría.
func conversationEventsDo(t *testing.T, lister apipublica.ConversationEventLister, feats entitlements.Resolver, query url.Values) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	cara := conversationEventsCara(h.Common(), lister, feats)
	target := conversationEventsTarget
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return h, h.Call(cara, h.With(tenantA, conversationEventsPerm), http.MethodGet, target, "")
}

// conversationEventsWantCalls exige cuántas veces se consultó el puerto.
func conversationEventsWantCalls(t *testing.T, what string, lister *conversationEventsListerSpy, want int) {
	t.Helper()
	if lister.calls != want {
		t.Errorf("%s: el puerto del listado recibió %d llamadas, quiero %d", what, lister.calls, want)
	}
}

// conversationEventsWantNoAudit exige que la petición no dejó bitácora: es una lectura.
func conversationEventsWantNoAudit(t *testing.T, what string, h *apipublicahelpertest.Harness) {
	t.Helper()
	if n := len(h.Auditor().Records()); n != 0 {
		t.Errorf("%s: dejó %d registros de auditoría; I18 es una lectura", what, n)
	}
}

func TestMountConversationEvents_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	lister := conversationEventsNewSpy()
	cara := conversationEventsCara(h.Common(), lister, conversationEventsAllFeatures())
	wantPatterns(t, "I18", cara, []string{conversationEventsPattern})
	checkChain(t, h, cara, routeCase{id: "I18", method: http.MethodGet, target: conversationEventsTarget,
		perm: conversationEventsPerm, want: http.StatusOK})
	// De las cuatro peticiones de checkChain solo la que pasa la cadena consulta el puerto.
	conversationEventsWantCalls(t, "I18 cadena", lister, 1)
}

// TestMountConversationEvents_NotMountedWithoutDeps: sin el puerto o sin el resolver la ruta no
// existe (trampa T-11), y el montaje ni mira la cadena: tampoco hace panic con MW nil.
func TestMountConversationEvents_NotMountedWithoutDeps(t *testing.T) {
	cases := []struct {
		name string
		deps apipublica.ConversationEventsDeps
	}{
		{"no lister", apipublica.ConversationEventsDeps{Entitlements: conversationEventsAllFeatures()}},
		{"no resolver", apipublica.ConversationEventsDeps{Events: conversationEventsNewSpy()}},
		{"neither", apipublica.ConversationEventsDeps{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := apipublica.Nueva()
			apipublica.MountConversationEvents(cara, h.Common(), tc.deps)
			wantPatterns(t, tc.name, cara, nil)
			rec := h.Call(cara, h.With(tenantA, conversationEventsPerm), http.MethodGet, conversationEventsTarget, "")
			wantCode(t, tc.name, rec, http.StatusNotFound)

			if v := recuperar(func() {
				apipublica.MountConversationEvents(apipublica.Nueva(), apipublica.Common{}, tc.deps)
			}); v != nil {
				t.Errorf("%s con MW nil: panic = %v; quiero que no monte nada ni mire la cadena", tc.name, v)
			}
		})
	}
}

func TestMountConversationEvents_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		conversationEventsCara(apipublica.Common{}, conversationEventsNewSpy(), conversationEventsAllFeatures())
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountConversationEvents") {
		t.Errorf("MountConversationEvents con MW nil: panic = %v; quiero un panic de cableado que nombre MountConversationEvents", v)
	}
}

// TestMountConversationEvents_DoesNotMountTheCancel: el listado se monta solo; la cancelación es
// de otro Mount y en esta cara no existe.
func TestMountConversationEvents_DoesNotMountTheCancel(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := conversationEventsCara(h.Common(), conversationEventsNewSpy(), conversationEventsAllFeatures())
	token := h.With(tenantA, conversationEventsPerm, "intakes.write")
	wantCode(t, "I18 sola", h.Call(cara, token, http.MethodGet, conversationEventsTarget, ""), http.StatusOK)
	rec := h.Call(cara, token, http.MethodPost, conversationEventsTarget+"/"+conversationEventsContact+"/cancel", "")
	wantCode(t, "I19 sin su Mount", rec, http.StatusNotFound)
}

// TestMountConversationEvents_GateNeedsAnyKindFeature: el permiso no basta. Sin NINGUNA de las
// features de tipo —o con el resolver caído al preguntar— es 403 con la lista de las que habrían
// valido, sin consultar el puerto; con UNA cualquiera se entra.
func TestMountConversationEvents_GateNeedsAnyKindFeature(t *testing.T) {
	denied := map[string]entitlements.Resolver{
		"no kind feature": conversationEventsFeatures(entitlements.FeatureIntakesExport, entitlements.FeatureLLMIntake),
		"resolver down":   &entitlementshelpertest.Fake{Err: errors.New("bd caída")},
		"explicitly disabled": func() *entitlementshelpertest.Fake {
			f := conversationEventsFeatures()
			f.Disable(tenantA, "survey")
			return f
		}(),
		"only the other tenant": func() *entitlementshelpertest.Fake {
			f := entitlementshelpertest.NewFake()
			f.Enable(apipublicahelpertest.TenantB, "cart_basic")
			return f
		}(),
	}
	for name, feats := range denied {
		t.Run(name, func(t *testing.T) {
			lister := conversationEventsNewSpy()
			h, rec := conversationEventsDo(t, lister, feats, nil)
			wantCode(t, name, rec, http.StatusForbidden)
			wantExactBody(t, name, rec, conversationEventsGateBody)
			conversationEventsWantCalls(t, name, lister, 0)
			conversationEventsWantNoAudit(t, name, h)
		})
	}

	kindByFeature := map[string]string{"cart_basic": "cart", "media": "media", "menu": "menu", "survey": "survey"}
	if got := events.KindFeatures(); len(got) != len(kindByFeature) {
		t.Fatalf("events.KindFeatures() = %v; el contrato de I18 habla de cuatro features de tipo", got)
	}
	for _, feature := range events.KindFeatures() {
		t.Run("only "+feature, func(t *testing.T) {
			lister := conversationEventsNewSpy()
			_, rec := conversationEventsDo(t, lister, conversationEventsFeatures(feature), nil)
			wantCode(t, feature, rec, http.StatusOK)
			conversationEventsWantCalls(t, feature, lister, 1)
			if want := []string{kindByFeature[feature]}; !reflect.DeepEqual(lister.filter.Kinds, want) {
				t.Errorf("solo %s: el puerto recibió Kinds=%v, quiero %v (entrar por una feature no enseña los tipos de las otras)",
					feature, lister.filter.Kinds, want)
			}
		})
	}
}

// TestMountConversationEvents_KindsFollowThePlan: Kinds es lo que el tenant PUEDE ver, en orden
// alfabético, y se aplica a la vez que el `kind` que pidió: pedir un tipo sin su feature es 200.
func TestMountConversationEvents_KindsFollowThePlan(t *testing.T) {
	lister := conversationEventsNewSpy()
	_, rec := conversationEventsDo(t, lister, conversationEventsAllFeatures(), nil)
	wantCode(t, "todas", rec, http.StatusOK)
	if want := []string{"cart", "media", "menu", "survey"}; !reflect.DeepEqual(lister.filter.Kinds, want) {
		t.Errorf("todas las features: Kinds=%v, quiero %v", lister.filter.Kinds, want)
	}

	lister = conversationEventsNewSpy()
	_, rec = conversationEventsDo(t, lister, conversationEventsFeatures("survey", "menu"), url.Values{"kind": {"cart"}})
	wantCode(t, "kind=cart sin su feature", rec, http.StatusOK)
	wantExactBody(t, "kind=cart sin su feature", rec, conversationEventsEmptyAnswer)
	if want := []string{"menu", "survey"}; lister.filter.Kind != "cart" || !reflect.DeepEqual(lister.filter.Kinds, want) {
		t.Errorf("kind=cart sin su feature: Kind=%q Kinds=%v, quiero cart y %v", lister.filter.Kind, lister.filter.Kinds, want)
	}
}

// TestMountConversationEvents_UnresolvedKindsIs500: si el resolver se cae DESPUÉS del gate la
// respuesta es 500 y no una lista recortada, y el puerto no se consulta.
func TestMountConversationEvents_UnresolvedKindsIs500(t *testing.T) {
	// El gate pasa con la primera pregunta (cart_basic); la siguiente, ya en el handler, falla.
	feats := &conversationEventsFlakyResolver{Fake: conversationEventsAllFeatures(), okCalls: 1}
	lister := conversationEventsNewSpy()
	h, rec := conversationEventsDo(t, lister, feats, nil)
	wantCode(t, "resolver caído tras el gate", rec, http.StatusInternalServerError)
	wantErrorBody(t, "resolver caído tras el gate", rec, conversationEventsMsgKinds)
	conversationEventsWantCalls(t, "resolver caído tras el gate", lister, 0)
	conversationEventsWantNoAudit(t, "resolver caído tras el gate", h)
}

// TestMountConversationEvents_StoreFailureIs500: un fallo del puerto es 500 y NO una lista vacía,
// sin repetir el error.
func TestMountConversationEvents_StoreFailureIs500(t *testing.T) {
	lister := conversationEventsNewSpy()
	lister.err = errors.New("pq: DSN SECRETO rechazado")
	_, rec := conversationEventsDo(t, lister, conversationEventsAllFeatures(), nil)
	wantCode(t, "puerto caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "puerto caído", rec, conversationEventsMsgList)
	conversationEventsWantCalls(t, "puerto caído", lister, 1)
}

// TestMountConversationEvents_TenantFromToken es INV-8: la petición lleva `?tenant_id=<B>` con el
// token de A, y el puerto recibe A; la respuesta no lleva ningún tenant.
func TestMountConversationEvents_TenantFromToken(t *testing.T) {
	other := apipublicahelpertest.TenantB
	lister := conversationEventsNewSpy()
	lister.page.Events = []events.Rescuable{{Event: events.Event{ID: "e1", TenantID: other, Kind: "cart", Status: events.StatusOpen}}}
	_, rec := conversationEventsDo(t, lister, conversationEventsAllFeatures(), url.Values{"tenant_id": {other}})
	wantCode(t, "I18", rec, http.StatusOK)
	if lister.tenant != tenantA {
		t.Errorf("I18: el puerto recibió el tenant %q, quiero el del token (%s)", lister.tenant, tenantA)
	}
	if body := rec.Body.String(); strings.Contains(body, tenantA) || strings.Contains(body, other) {
		t.Errorf("I18: la respuesta lleva un tenant (%s); es el del token y no viaja", body)
	}
}

// TestMountConversationEvents_Wire: la página, el total y cada fila salen tal cual los dio el
// puerto, con las claves en su orden; el contenido se omite si no lo hay y un instante cero es "".
func TestMountConversationEvents_Wire(t *testing.T) {
	zone := time.FixedZone("-03", -3*3600)
	lister := conversationEventsNewSpy()
	lister.page = events.EventPage{Page: 7, PageSize: 3, Total: 41, Events: []events.Rescuable{
		{
			Event: events.Event{ID: "ev-1", TenantID: tenantA, SessionID: "ses-1", ContactID: conversationEventsContact,
				Kind: "cart", HistoryID: "cart-2026-10-01-1200", Status: events.StatusCancelled, FlowID: "flujo-secreto", FlowVersion: 9,
				CreatedAt:      time.Date(2026, 10, 1, 9, 0, 0, 999, zone),
				LastActivityAt: time.Date(2026, 10, 1, 9, 30, 0, 0, zone),
				ClosedAt:       time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)},
			Stale: true, ContentState: "alive", ContentRef: "intake-77",
		},
		{
			Event: events.Event{ID: "ev-2", SessionID: "ses-2", ContactID: "opaco", Kind: "survey",
				HistoryID: "survey-2026-10-03-0800", Status: events.StatusOpen,
				CreatedAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC), LastActivityAt: time.Date(2026, 10, 3, 8, 5, 0, 0, time.UTC)},
		},
	}}
	_, rec := conversationEventsDo(t, lister, conversationEventsAllFeatures(), nil)
	wantCode(t, "I18", rec, http.StatusOK)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("I18: Content-Type %q, quiero application/json", got)
	}
	wantExactBody(t, "I18", rec, `{"events":[`+
		`{"id":"ev-1","history_id":"cart-2026-10-01-1200","kind":"cart","status":"cancelled",`+
		`"contact_id":"`+conversationEventsContact+`","session_id":"ses-1","content_state":"alive","content_ref":"intake-77",`+
		`"stale":true,"created_at":"2026-10-01T12:00:00Z","last_activity_at":"2026-10-01T12:30:00Z","closed_at":"2026-10-02T00:00:00Z"},`+
		`{"id":"ev-2","history_id":"survey-2026-10-03-0800","kind":"survey","status":"open",`+
		`"contact_id":"opaco","session_id":"ses-2",`+
		`"stale":false,"created_at":"2026-10-03T08:00:00Z","last_activity_at":"2026-10-03T08:05:00Z","closed_at":""}`+
		`],"page":7,"page_size":3,"total":41}`)

	// Sin eventos la lista es `[]`, nunca `null`, también si el puerto devuelve nil.
	lister = conversationEventsNewSpy()
	_, rec = conversationEventsDo(t, lister, conversationEventsAllFeatures(), nil)
	wantCode(t, "vacío", rec, http.StatusOK)
	wantExactBody(t, "vacío", rec, conversationEventsEmptyAnswer)
}

// conversationEventsBool devuelve un puntero a v, para el tri-estado de Stale.
func conversationEventsBool(v bool) *bool { return &v }

// TestMountConversationEvents_QueryBecomesANormalizedFilter: se afirma sobre el filtro RECIBIDO y
// no sobre las filas devueltas: el doble devuelve lo mismo pida lo que pida.
func TestMountConversationEvents_QueryBecomesANormalizedFilter(t *testing.T) {
	all := []string{"cart", "media", "menu", "survey"}
	base := events.ListFilter{Status: events.StatusOpen, Content: events.ContentAny, Kinds: all, Page: 1, PageSize: 50}
	with := func(edit func(*events.ListFilter)) events.ListFilter {
		f := base
		edit(&f)
		return f
	}
	cases := []struct {
		name  string
		query url.Values
		want  events.ListFilter
	}{
		{"no query gives the defaults", nil, base},
		{"empty values count as absent", url.Values{"status": {""}, "content": {""}, "contact_id": {""}, "stale": {""}, "kind": {""}}, base},
		{"all filters", url.Values{"status": {"closed"}, "kind": {"survey"}, "content": {"none"}, "stale": {"true"},
			"contact_id": {conversationEventsContact}, "page": {"3"}, "page_size": {"25"}},
			events.ListFilter{Status: events.StatusClosed, Kind: "survey", Content: events.ContentNone, Stale: conversationEventsBool(true),
				ContactID: conversationEventsContact, Kinds: all, Page: 3, PageSize: 25}},
		{"status cancelled", url.Values{"status": {"cancelled"}}, with(func(f *events.ListFilter) { f.Status = events.StatusCancelled })},
		{"content alive", url.Values{"content": {"alive"}}, with(func(f *events.ListFilter) { f.Content = events.ContentAlive })},
		{"unknown kind is not validated", url.Values{"kind": {"No Existe"}}, with(func(f *events.ListFilter) { f.Kind = "No Existe" })},
		{"stale false is not absent", url.Values{"stale": {"false"}}, with(func(f *events.ListFilter) { f.Stale = conversationEventsBool(false) })},
		{"stale 1", url.Values{"stale": {"1"}}, with(func(f *events.ListFilter) { f.Stale = conversationEventsBool(true) })},
		{"stale T", url.Values{"stale": {"T"}}, with(func(f *events.ListFilter) { f.Stale = conversationEventsBool(true) })},
		{"stale 0", url.Values{"stale": {"0"}}, with(func(f *events.ListFilter) { f.Stale = conversationEventsBool(false) })},
		{"stale FALSE", url.Values{"stale": {"FALSE"}}, with(func(f *events.ListFilter) { f.Stale = conversationEventsBool(false) })},
		{"contact without hyphens arrives as written", url.Values{"contact_id": {"c0ffee0000004000800000000000000a"}},
			with(func(f *events.ListFilter) { f.ContactID = "c0ffee0000004000800000000000000a" })},
		{"contact in braces arrives as written", url.Values{"contact_id": {"{" + conversationEventsContact + "}"}},
			with(func(f *events.ListFilter) { f.ContactID = "{" + conversationEventsContact + "}" })},
		{"contact as urn arrives as written", url.Values{"contact_id": {"urn:uuid:" + conversationEventsContact}},
			with(func(f *events.ListFilter) { f.ContactID = "urn:uuid:" + conversationEventsContact })},
		{"page size is capped at 200", url.Values{"page_size": {"100000"}}, with(func(f *events.ListFilter) { f.PageSize = 200 })},
		{"page size 201 is capped", url.Values{"page_size": {"201"}}, with(func(f *events.ListFilter) { f.PageSize = 200 })},
		{"page size 200 passes", url.Values{"page_size": {"200"}}, with(func(f *events.ListFilter) { f.PageSize = 200 })},
		{"page size 1 passes", url.Values{"page_size": {"1"}}, with(func(f *events.ListFilter) { f.PageSize = 1 })},
		{"page size 0 is the default", url.Values{"page_size": {"0"}}, base},
		{"page 0 is page 1", url.Values{"page": {"0"}}, base},
		{"negative paging falls to the defaults", url.Values{"page": {"-3"}, "page_size": {"-1"}}, base},
		{"non numeric paging falls to the defaults", url.Values{"page": {"dos"}, "page_size": {"2.5"}}, base},
		{"non ascii digits fall to the defaults", url.Values{"page": {"２"}, "page_size": {"１２"}}, base},
		{"overflow falls to the defaults", url.Values{"page": {"99999999999999999999"}}, base},
		{"large page passes", url.Values{"page": {"4000"}}, with(func(f *events.ListFilter) { f.Page = 4000 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := conversationEventsNewSpy()
			_, rec := conversationEventsDo(t, lister, conversationEventsAllFeatures(), tc.query)
			wantCode(t, tc.name, rec, http.StatusOK)
			conversationEventsWantCalls(t, tc.name, lister, 1)
			if !reflect.DeepEqual(lister.filter, tc.want) {
				t.Errorf("%s: el puerto recibió el filtro\n  %+v (stale=%s)\nquiero\n  %+v (stale=%s)", tc.name,
					lister.filter, conversationEventsStaleText(lister.filter.Stale), tc.want, conversationEventsStaleText(tc.want.Stale))
			}
		})
	}
}

// conversationEventsStaleText pinta el tri-estado para los mensajes de fallo.
func conversationEventsStaleText(v *bool) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprint(*v)
}

// TestMountConversationEvents_InvalidFiltersAre400: un typo se dice, no se ignora; el puerto no
// se consulta, y con varios inválidos gana el primero del orden status, content, contact_id,
// stale.
func TestMountConversationEvents_InvalidFiltersAre400(t *testing.T) {
	cases := []struct {
		name  string
		query url.Values
		msg   string
	}{
		{"unknown status", url.Values{"status": {"abiertos"}}, conversationEventsMsgStatus},
		{"status is case sensitive", url.Values{"status": {"Open"}}, conversationEventsMsgStatus},
		{"status with spaces", url.Values{"status": {" open"}}, conversationEventsMsgStatus},
		{"unknown content", url.Values{"content": {"todo"}}, conversationEventsMsgContent},
		{"content settled is not a filter", url.Values{"content": {"settled"}}, conversationEventsMsgContent},
		{"contact is not a uuid", url.Values{"contact_id": {"marta"}}, conversationEventsMsgContact},
		{"contact with a trailing character", url.Values{"contact_id": {conversationEventsContact + "0"}}, conversationEventsMsgContact},
		{"contact with a non hex digit", url.Values{"contact_id": {"g0ffee00-0000-4000-8000-000000000001"}}, conversationEventsMsgContact},
		{"contact with repeated separators", url.Values{"contact_id": {"c0ffee00--0000-4000-8000-00000000001"}}, conversationEventsMsgContact},
		{"stale yes", url.Values{"stale": {"yes"}}, conversationEventsMsgStale},
		{"stale in mixed case", url.Values{"stale": {"tRUE"}}, conversationEventsMsgStale},
		{"stale with spaces", url.Values{"stale": {" true"}}, conversationEventsMsgStale},
		{"status wins over the rest", url.Values{"status": {"x"}, "content": {"x"}, "contact_id": {"x"}, "stale": {"x"}}, conversationEventsMsgStatus},
		{"content wins over contact and stale", url.Values{"content": {"x"}, "contact_id": {"x"}, "stale": {"x"}}, conversationEventsMsgContent},
		{"contact wins over stale", url.Values{"contact_id": {"x"}, "stale": {"x"}}, conversationEventsMsgContact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := conversationEventsNewSpy()
			h, rec := conversationEventsDo(t, lister, conversationEventsAllFeatures(), tc.query)
			wantCode(t, tc.name, rec, http.StatusBadRequest)
			wantErrorBody(t, tc.name, rec, tc.msg)
			conversationEventsWantCalls(t, tc.name, lister, 0)
			conversationEventsWantNoAudit(t, tc.name, h)
		})
	}
}

// TestMountConversationEvents_InvalidFilterDoesNotAskThePlan: el 400 sale antes de resolver los
// tipos: con el resolver caído tras el gate, un filtro inválido sigue siendo 400 y no 500.
func TestMountConversationEvents_InvalidFilterDoesNotAskThePlan(t *testing.T) {
	feats := &conversationEventsFlakyResolver{Fake: conversationEventsAllFeatures(), okCalls: 1}
	_, rec := conversationEventsDo(t, conversationEventsNewSpy(), feats, url.Values{"status": {"abiertos"}})
	wantCode(t, "filtro inválido con el resolver caído", rec, http.StatusBadRequest)
	if feats.calls != 1 {
		t.Errorf("filtro inválido: el resolver recibió %d preguntas, quiero 1 (solo la del gate)", feats.calls)
	}
}
