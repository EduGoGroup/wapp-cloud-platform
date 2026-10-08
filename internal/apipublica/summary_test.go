package apipublica_test

// summary_test.go — cubre G10, `GET /api/v1/intakes/summary.json`, tal como lo promete
// MountIntakeReports (intakereports.go): la forma exacta del JSON, el CERO PII, el rango
// publicado, que nada es null, y los errores. La cadena y el gate están en
// intakereports_test.go.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	summaryTarget = "/api/v1/intakes/summary.json"

	msgSummaryFailed = "no se pudo resumir las solicitudes"

	summaryEmpty = `{"generated_at":"2026-10-08T09:30:05Z","range":{"from":"","to":""},` +
		`"totals":{"intakes":0,"revenue":0,"by_status":{}},"top_items":[],"intakes":[]}`

	// Lo que NO puede aparecer en el resumen: va a un LLM externo.
	summaryContact = "9f1c0a7e-0000-4000-8000-000000000abc"
	summarySession = "sess-del-telefono-de-la-tienda"
)

// summaryFixture es un resumen con todo lo que el dominio puede traer dentro, incluido lo que
// no debe salir (contacto, sesión, datos del comprador, revisiones).
func summaryFixture() intakes.Summary {
	zone := time.FixedZone("-03", -3*3600)
	return intakes.Summary{
		GeneratedAt: reportNow(),
		From:        time.Date(2026, 8, 1, 0, 0, 0, 0, zone),
		To:          time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		Intakes:     2,
		Revenue:     21001.5,
		ByStatus:    map[string]int{intakes.StatusConfirmed: 1, intakes.StatusOpen: 1},
		TopItems: []intakes.TopItem{
			{SKU: "torta-v1", Label: "Torta 10-12 porciones", QtyTotal: 1, Revenue: 18000},
			{SKU: "_shipping", Label: "Envío — Providencia", QtyTotal: 2, Revenue: 3001},
		},
		Details: []intakes.Detail{
			{
				Intake: intakes.Intake{
					ID: "11111111-1111-4111-8111-111111111111", ContactID: summaryContact, SessionID: summarySession,
					Status: intakes.StatusConfirmed, Total: 21001, CreatedAt: time.Date(2026, 8, 1, 9, 0, 0, 0, zone),
					UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, zone), CustomerNote: "dejar en portería <sin timbre>",
				},
				Items: []intakes.Item{
					{SKU: "torta-v1", Label: "Torta 10-12 porciones", Customization: "sin sal", Qty: 1, UnitPrice: 18000,
						AddedAt: time.Date(2026, 8, 1, 9, 1, 0, 0, zone)},
					{SKU: "_shipping", Label: "Envío — Providencia", Qty: 2, UnitPrice: 1500.5},
				},
				Revisions:        []intakes.Revision{{}},
				BuyerDataPresent: true,
			},
			{Intake: intakes.Intake{
				ID: "22222222-2222-4222-8222-222222222222", ContactID: summaryContact, SessionID: summarySession,
				Status: intakes.StatusOpen, Total: 0.5, CreatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
			}},
		},
	}
}

// TestSummary_Body: el JSON byte a byte: las claves y su orden, los instantes en UTC, el rango
// aplicado, las líneas con su personalización y la nota del pedido — y nada más.
func TestSummary_Body(t *testing.T) {
	svc := &reportServiceSpy{summary: summaryFixture()}
	rec := reportGet(t, reportDeps(svc), summaryTarget)
	wantCode(t, "G10", rec, http.StatusOK)
	want := `{"generated_at":"2026-10-08T09:30:05Z",` +
		`"range":{"from":"2026-08-01T03:00:00Z","to":"2026-08-03T00:00:00Z"},` +
		`"totals":{"intakes":2,"revenue":21001.5,"by_status":{"confirmed":1,"open":1}},` +
		`"top_items":[` +
		`{"sku":"torta-v1","label":"Torta 10-12 porciones","qty_total":1,"revenue":18000},` +
		`{"sku":"_shipping","label":"Envío — Providencia","qty_total":2,"revenue":3001}],` +
		`"intakes":[` +
		`{"id":"11111111-1111-4111-8111-111111111111","status":"confirmed","created_at":"2026-08-01T12:00:00Z",` +
		// El codificador de la casa escapa `<` y `>` (\x5c es la barra invertida).
		`"total":21001,"customer_note":"dejar en portería ` + "\x5cu003csin timbre\x5cu003e" + `","items":[` +
		`{"sku":"torta-v1","label":"Torta 10-12 porciones","customization":"sin sal","qty":1,"unit_price":18000},` +
		`{"sku":"_shipping","label":"Envío — Providencia","customization":"","qty":2,"unit_price":1500.5}]},` +
		`{"id":"22222222-2222-4222-8222-222222222222","status":"open","created_at":"2026-08-02T00:00:00Z",` +
		`"total":0.5,"customer_note":"","items":[]}]}`
	wantExactBody(t, "G10", rec, want)

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, quiero application/json", got)
	}
	if svc.tenant != tenantA {
		t.Errorf("Summary recibió el tenant %q, quiero el del token %q", svc.tenant, tenantA)
	}
	if svc.remaining != -1 {
		t.Errorf("al contexto de Summary le quedaban %s; la cara no le pone plazo a esta lectura", svc.remaining)
	}
}

// TestSummary_ZeroPII: el resumen sale del perímetro. Ni el contacto —aunque sea opaco—, ni la
// sesión, ni rastro del comprador, por valor y por clave.
func TestSummary_ZeroPII(t *testing.T) {
	rec := reportGet(t, reportDeps(&reportServiceSpy{summary: summaryFixture()}), summaryTarget)
	wantCode(t, "G10", rec, http.StatusOK)
	body := rec.Body.String()
	for _, banned := range []string{
		summaryContact, summarySession, "contact", "session", "buyer", "revision", "updated_at", "added_at", tenantA,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("el resumen contiene %q; no puede llevar nada que identifique a nadie:\n%s", banned, body)
		}
	}
}

// TestSummary_NothingIsNull: sin coincidencias —y aunque el puerto dé nil— las listas son [] y
// el desglose {}; una cota sin pedir viaja como cadena vacía.
func TestSummary_NothingIsNull(t *testing.T) {
	cases := map[string]intakes.Summary{
		"nil_collections":   {GeneratedAt: reportNow()},
		"empty_collections": {GeneratedAt: reportNow(), ByStatus: map[string]int{}, TopItems: []intakes.TopItem{}, Details: []intakes.Detail{}},
	}
	for name, summary := range cases {
		rec := reportGet(t, reportDeps(&reportServiceSpy{summary: summary}), summaryTarget)
		wantCode(t, name, rec, http.StatusOK)
		wantExactBody(t, name, rec, summaryEmpty)
	}

	withNilItems := intakes.Summary{GeneratedAt: reportNow(), Details: []intakes.Detail{{Intake: intakes.Intake{ID: "x", Status: intakes.StatusOpen}}}}
	rec := reportGet(t, reportDeps(&reportServiceSpy{summary: withNilItems}), summaryTarget)
	if !strings.Contains(rec.Body.String(), `"items":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Errorf("una solicitud sin líneas debe llevar \"items\":[] y nada null: %s", rec.Body.String())
	}
}

// TestSummary_RangeIsTheAppliedOne: cada cota se publica por separado; la que no se pidió, vacía.
func TestSummary_RangeIsTheAppliedOne(t *testing.T) {
	bound := time.Date(2026, 8, 1, 21, 0, 0, 0, time.FixedZone("-03", -3*3600))
	cases := []struct {
		name     string
		from, to time.Time
		want     string
	}{
		{"only_from", bound, time.Time{}, `"range":{"from":"2026-08-02T00:00:00Z","to":""}`},
		{"only_to", time.Time{}, bound, `"range":{"from":"","to":"2026-08-02T00:00:00Z"}`},
		{"none", time.Time{}, time.Time{}, `"range":{"from":"","to":""}`},
	}
	for _, tc := range cases {
		summary := intakes.Summary{GeneratedAt: reportNow(), From: tc.from, To: tc.to}
		rec := reportGet(t, reportDeps(&reportServiceSpy{summary: summary}), summaryTarget)
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: cuerpo %s; quiero %s", tc.name, rec.Body.String(), tc.want)
		}
	}
}

// TestSummary_Filter: el filtro de la query llega al puerto; uno mal escrito es 400 con el
// mensaje del filtro y el puerto no se consulta. (El filtro a fondo lo prueba su dueño.)
func TestSummary_Filter(t *testing.T) {
	svc := &reportServiceSpy{summary: intakes.Summary{GeneratedAt: reportNow()}}
	rec := reportGet(t, reportDeps(svc), summaryTarget+"?session=sess-a&tenant_id="+tenantB)
	wantCode(t, "filtro", rec, http.StatusOK)
	if svc.filter.SessionID != "sess-a" || svc.tenant != tenantA {
		t.Errorf("el puerto recibió tenant %q y filtro %+v; quiero el tenant del token y session sess-a", svc.tenant, svc.filter)
	}

	svc = &reportServiceSpy{}
	rec = reportGet(t, reportDeps(svc), summaryTarget+"?from=ayer")
	wantCode(t, "filtro inválido", rec, http.StatusBadRequest)
	wantErrorBody(t, "filtro inválido", rec, "from inválido: usa YYYY-MM-DD o RFC3339")
	if svc.calls != 0 {
		t.Errorf("filtro inválido: el puerto recibió %d llamadas, quiero 0", svc.calls)
	}
}

// TestSummary_Errors: la misma cota que el export (422) y un 500 que no repite el error.
func TestSummary_Errors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"too_large", intakes.ErrTooLarge, http.StatusUnprocessableEntity, reportTooLarge},
		{"too_large_wrapped", fmt.Errorf("resumiendo: %w", intakes.ErrTooLarge), http.StatusUnprocessableEntity, reportTooLarge},
		{"store_down", errors.New("postgres://usuario:secreto@host/bd: conexión rechazada"), http.StatusInternalServerError, msgSummaryFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &reportServiceSpy{err: tc.err, summary: summaryFixture()}
			rec := reportGet(t, reportDeps(svc), summaryTarget)
			wantCode(t, tc.name, rec, tc.code)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if svc.calls != 1 {
				t.Errorf("%s: el puerto recibió %d llamadas, quiero 1", tc.name, svc.calls)
			}
		})
	}
}

// TestSummary_WithTheModuleService: con el servicio REAL del módulo, generated_at es el instante
// del reloj inyectado EN EL SERVICIO (D-F6-5) —no el de la cara—, cada tenant resume lo suyo y
// el rango publicado es el del filtro.
func TestSummary_WithTheModuleService(t *testing.T) {
	store := intakes.NewMemoryStore()
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store.Add(tenantA, intakes.Intake{ID: "a-1", ContactID: summaryContact, SessionID: summarySession,
		Status: intakes.StatusClosedLegacy, Total: 6, CreatedAt: created},
		intakes.Item{SKU: "pan", Label: "Pan", Customization: "sin sal", Qty: 3, UnitPrice: 2})
	store.Add(tenantB, intakes.Intake{ID: "b-1", Status: intakes.StatusOpen, Total: 99, CreatedAt: created})

	serviceClock := time.Date(2026, 9, 9, 9, 9, 9, 0, time.FixedZone("+02", 2*3600))
	h := apipublicahelpertest.New(t)
	d := reportDeps(intakes.NewService(store, intakes.WithClock(func() time.Time { return serviceClock })))
	cara := reportCara(h.Common(), d)

	rec := h.Call(cara, h.With(tenantA, reportPerm), http.MethodGet, summaryTarget+"?from=2026-08-01T00:00:00Z", "")
	wantCode(t, "tenantA", rec, http.StatusOK)
	wantExactBody(t, "tenantA", rec, `{"generated_at":"2026-09-09T07:09:09Z",`+
		`"range":{"from":"2026-08-01T00:00:00Z","to":""},`+
		`"totals":{"intakes":1,"revenue":6,"by_status":{"confirmed":1}},`+
		`"top_items":[{"sku":"pan","label":"Pan","qty_total":3,"revenue":6}],`+
		`"intakes":[{"id":"a-1","status":"confirmed","created_at":"2026-08-01T12:00:00Z","total":6,"customer_note":"",`+
		`"items":[{"sku":"pan","label":"Pan","customization":"sin sal","qty":3,"unit_price":2}]}]}`)

	rec = h.Call(cara, h.With(tenantB, reportPerm), http.MethodGet, summaryTarget, "")
	wantCode(t, "tenantB", rec, http.StatusOK)
	if body := rec.Body.String(); !strings.Contains(body, `"id":"b-1"`) || strings.Contains(body, "a-1") {
		t.Errorf("tenantB resume %s; quiero lo suyo (b-1) y nada de tenantA", body)
	}
}
