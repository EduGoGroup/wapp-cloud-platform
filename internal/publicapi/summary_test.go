package publicapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// summaryDTO espeja el contrato de GET /api/v1/intakes/summary.json (D-041.15).
// Se declara aquí, aparte de la implementación, para que un cambio de claves en el
// servidor rompa el test en vez de arrastrarlo.
type summaryDTO struct {
	GeneratedAt string `json:"generated_at"`
	Range       struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"range"`
	Totals struct {
		Intakes  int            `json:"intakes"`
		Revenue  float64        `json:"revenue"`
		ByStatus map[string]int `json:"by_status"`
	} `json:"totals"`
	TopItems []struct {
		SKU      string  `json:"sku"`
		Label    string  `json:"label"`
		QtyTotal int     `json:"qty_total"`
		Revenue  float64 `json:"revenue"`
	} `json:"top_items"`
	Intakes []struct {
		ID           string  `json:"id"`
		Status       string  `json:"status"`
		CreatedAt    string  `json:"created_at"`
		Total        float64 `json:"total"`
		CustomerNote string  `json:"customer_note"`
		Items        []struct {
			SKU           string  `json:"sku"`
			Label         string  `json:"label"`
			Customization string  `json:"customization"`
			Qty           int     `json:"qty"`
			UnitPrice     float64 `json:"unit_price"`
		} `json:"items"`
	} `json:"intakes"`
}

func decodeSummary(t *testing.T, body []byte) summaryDTO {
	t.Helper()
	var out summaryDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal del summary: %v; body=%s", err, body)
	}
	return out
}

// summaryDeTenantA pide el summary completo del tenant A sobre el fixture de
// cinco solicitudes.
func summaryDeTenantA(t *testing.T) summaryDTO {
	t.Helper()
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/summary.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	return decodeSummary(t, rec.Body.Bytes())
}

// TestIntakeSummary_200_Totales: los totales y el desglose por estado, contados a
// mano sobre el fixture (5 solicitudes de 18000).
func TestIntakeSummary_200_Totales(t *testing.T) {
	sum := summaryDeTenantA(t)

	if sum.Totals.Intakes != 5 {
		t.Fatalf("totals.intakes=%d, quiero 5", sum.Totals.Intakes)
	}
	if sum.Totals.Revenue != 90000 {
		t.Fatalf("totals.revenue=%v, quiero 90000 (5 × 18000)", sum.Totals.Revenue)
	}
	// A1 y A4 están en BD con el `closed` legado y A5 con la clave nueva: los tres
	// cuentan en el MISMO cubo. Si el summary contara `closed` aparte, el dueño
	// vería su negocio partido en dos por un detalle de almacenamiento.
	quiero := map[string]int{"confirmed": 3, "open": 1, "cancelled": 1}
	if len(sum.Totals.ByStatus) != len(quiero) {
		t.Fatalf("by_status=%v, quiero %v", sum.Totals.ByStatus, quiero)
	}
	for estado, n := range quiero {
		if sum.Totals.ByStatus[estado] != n {
			t.Fatalf("by_status[%s]=%d, quiero %d (by_status=%v)",
				estado, sum.Totals.ByStatus[estado], n, sum.Totals.ByStatus)
		}
	}
	if _, err := time.Parse(time.RFC3339, sum.GeneratedAt); err != nil {
		t.Fatalf("generated_at=%q no es RFC3339: %v", sum.GeneratedAt, err)
	}
}

// TestIntakeSummary_200_TopItems: solo A1 tiene líneas; el ranking va por
// facturación descendente.
func TestIntakeSummary_200_TopItems(t *testing.T) {
	sum := summaryDeTenantA(t)

	if len(sum.TopItems) != 2 {
		t.Fatalf("top_items=%+v, quiero 2 artículos", sum.TopItems)
	}
	if sum.TopItems[0].SKU != "torta-v1" || sum.TopItems[0].QtyTotal != 1 || sum.TopItems[0].Revenue != 18000 {
		t.Fatalf("top_items[0]=%+v, quiero torta-v1 qty=1 revenue=18000", sum.TopItems[0])
	}
	if sum.TopItems[0].Label != "Torta 10-12 porciones" {
		t.Fatalf("label del ranking=%q", sum.TopItems[0].Label)
	}
	if sum.TopItems[1].SKU != "_shipping" || sum.TopItems[1].Revenue != 3000 {
		t.Fatalf("top_items[1]=%+v, quiero _shipping revenue=3000", sum.TopItems[1])
	}
}

// TestIntakeSummary_200_DetalleCrudo: las cinco solicitudes con sus líneas, más
// recientes primero y con el estado normalizado.
func TestIntakeSummary_200_DetalleCrudo(t *testing.T) {
	sum := summaryDeTenantA(t)

	if len(sum.Intakes) != 5 {
		t.Fatalf("intakes=%d, quiero 5", len(sum.Intakes))
	}
	if sum.Intakes[0].ID != intakeA5 || sum.Intakes[4].ID != intakeA1 {
		t.Fatalf("orden=%s…%s, quiero de la más reciente a la más antigua",
			sum.Intakes[0].ID, sum.Intakes[4].ID)
	}
	a1 := sum.Intakes[4]
	if len(a1.Items) != 2 {
		t.Fatalf("líneas de A1=%d, quiero 2", len(a1.Items))
	}
	if a1.Items[1].Label != "Envío — Providencia" {
		t.Fatalf("etiqueta acentuada en el summary=%q", a1.Items[1].Label)
	}
	if a1.Status != "confirmed" {
		t.Fatalf("status=%q; el `closed` legado sale normalizado", a1.Status)
	}
}

// TestIntakeSummary_PublicaLaPersonalización es el CUARTO de los cinco caminos de
// T4.1b (D-041.17): el `summary.json` publica la personalización por línea.
//
// No contradice el CERO PII de este endpoint, y merece decirse: `customization` es
// dato de PRODUCTO —«sin sal»—, no de persona. Es justamente lo que le da valor al
// resumen que el dueño le pega a un LLM: «cuántos me piden sin sal» no se puede
// contestar si el campo se queda fuera.
//
// Y comprueba el reverso: el dinero no se mueve. Con una línea personalizada, el
// revenue total y el del ranking por SKU son los mismos (INV-13).
func TestIntakeSummary_PublicaLaPersonalización(t *testing.T) {
	sum := summaryDeTenantA(t)

	a1 := sum.Intakes[4] // la más antigua: la única con líneas en el fixture
	if len(a1.Items) != 2 {
		t.Fatalf("líneas de A1=%d, quiero 2", len(a1.Items))
	}
	if a1.Items[0].Customization != "sin sal" {
		t.Fatalf("items[0].customization=%q, quiero %q", a1.Items[0].Customization, "sin sal")
	}
	if a1.Items[1].Customization != "" {
		t.Fatalf("items[1].customization=%q; esa línea no tenía personalización", a1.Items[1].Customization)
	}
	// INV-13: ni el total de la solicitud, ni el revenue agregado, ni el del
	// ranking de ese SKU se enteran de la personalización.
	if a1.Total != 18000 || sum.Totals.Revenue != 90000 {
		t.Fatalf("total=%v revenue=%v; personalizar movió el dinero", a1.Total, sum.Totals.Revenue)
	}
	if sum.TopItems[0].SKU != "torta-v1" || sum.TopItems[0].Revenue != 18000 {
		t.Fatalf("top_items[0]=%+v; el ranking cambió por una personalización", sum.TopItems[0])
	}
}

// TestIntakeSummary_SinPII es el invariante del endpoint: el summary se genera para
// pegárselo a un LLM EXTERNO, así que no puede llevar NADA que identifique a nadie
// — ni el contact_id opaco. Se comprueba sobre el JSON SERIALIZADO, no sobre el
// struct: es lo que sale por el cable, y una clave que el DTO del test no declara
// pasaría desapercibida al deserializar.
func TestIntakeSummary_SinPII(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/summary.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// El fixture PONE el contacto opaco en las cinco solicitudes: si apareciera,
	// aparecería aquí.
	if strings.Contains(body, contactoOpaco) {
		t.Fatalf("el summary filtra el contact_id (%s):\n%s", contactoOpaco, body)
	}
	for _, prohibido := range []string{"contact_id", "contact_ref", "buyer_data", "session"} {
		if strings.Contains(body, prohibido) {
			t.Fatalf("el summary trae la clave %q:\n%s", prohibido, body)
		}
	}
	// Y la prueba de que el fixture era capaz de delatarlo: el mismo contacto SÍ
	// sale por la bandeja, que es de la consola del dueño.
	lista := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", "")
	if !strings.Contains(lista.Body.String(), contactoOpaco) {
		t.Fatalf("la lista no trae el contacto opaco: el test de ausencia no probaría nada")
	}
}

// TestIntakeSummary_MismosFiltrosQueLaLista: el summary responde a los mismos
// filtros que la bandeja y cuenta exactamente lo mismo.
func TestIntakeSummary_MismosFiltrosQueLaLista(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	for _, q := range []string{
		"", "?status=confirmed", "?status=closed", "?session=sess-b",
		"?from=2026-08-03", "?from=2026-08-01&to=2026-08-02",
	} {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes"+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("lista %q: code=%d", q, rec.Code)
		}
		quiero := idsDe(decodeList(t, rec.Body.Bytes()))

		rec = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/summary.json"+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("summary %q: code=%d; body=%s", q, rec.Code, rec.Body.String())
		}
		sum := decodeSummary(t, rec.Body.Bytes())
		if sum.Totals.Intakes != len(quiero) {
			t.Fatalf("filtro %q: totals.intakes=%d, la lista devuelve %d",
				q, sum.Totals.Intakes, len(quiero))
		}
		got := make([]string, 0, len(sum.Intakes))
		for _, in := range sum.Intakes {
			got = append(got, in.ID)
		}
		if strings.Join(got, ",") != strings.Join(quiero, ",") {
			t.Fatalf("filtro %q: summary=%v, lista=%v", q, got, quiero)
		}
	}
}

// TestIntakeSummary_RangoPublicado: el rango que se devuelve es el REALMENTE
// aplicado — con `to` ya convertido a su cota exclusiva (una fecha suelta significa
// "hasta el final de ese día"). Una cota que no se pidió sale vacía.
func TestIntakeSummary_RangoPublicado(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/summary.json?from=2026-08-01&to=2026-08-02", "")
	sum := decodeSummary(t, rec.Body.Bytes())
	if sum.Range.From != "2026-08-01T00:00:00Z" || sum.Range.To != "2026-08-03T00:00:00Z" {
		t.Fatalf("range=%+v; quiero [2026-08-01T00:00:00Z, 2026-08-03T00:00:00Z)", sum.Range)
	}

	rec = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/summary.json", "")
	sum = decodeSummary(t, rec.Body.Bytes())
	if sum.Range.From != "" || sum.Range.To != "" {
		t.Fatalf("range=%+v; sin filtro las dos cotas van vacías", sum.Range)
	}
}

// TestIntakeSummary_Aislamiento: el tenant B resume LO SUYO (INV-8).
func TestIntakeSummary_Aislamiento(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyBIntakes, http.MethodGet, "/api/v1/intakes/summary.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	sum := decodeSummary(t, rec.Body.Bytes())
	if sum.Totals.Intakes != 1 || len(sum.Intakes) != 1 || sum.Intakes[0].ID != intakeB1 {
		t.Fatalf("el summary del tenant B = %+v; quiero solo %s", sum, intakeB1)
	}
}

// TestIntakeSummary_VacíoNoEsNulo: un tenant sin coincidencias devuelve listas
// vacías y mapas vacíos, no `null`. Quien lo consuma itera sin ramificar.
func TestIntakeSummary_VacíoNoEsNulo(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/summary.json?status=settled", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "null") {
		t.Fatalf("el summary vacío trae null:\n%s", body)
	}
	sum := decodeSummary(t, rec.Body.Bytes())
	if sum.Totals.Intakes != 0 || sum.Totals.Revenue != 0 {
		t.Fatalf("totals=%+v, quiero ceros", sum.Totals)
	}
}

// TestIntakeSummary_PublicaLaNotaDelPedido es el CUARTO de los cinco caminos de
// T4.1c (D-041.19, REQ-33f): el `summary.json` publica la indicación del pedido a
// nivel de solicitud.
//
// Merece decirse por qué no contradice el CERO PII de este endpoint —que se genera
// para pegárselo a un LLM externo—: es una instrucción de producción y entrega, la
// misma clase de dato que `customization`. Lo que la contiene no es el cifrado sino
// el diseño de la ranura: 280 runas, propósito declarado en pantalla y advertencia
// explícita de no escribir ahí datos personales (D-041.23).
func TestIntakeSummary_PublicaLaNotaDelPedido(t *testing.T) {
	sum := summaryDeTenantA(t)

	a1 := sum.Intakes[4] // la más antigua: la única con líneas y con indicación
	if a1.CustomerNote != "dejar en portería" {
		t.Fatalf("customer_note=%q, quiero %q", a1.CustomerNote, "dejar en portería")
	}
	// Las demás solicitudes no la heredan: el campo viaja con SU solicitud.
	for i, in := range sum.Intakes[:4] {
		if in.CustomerNote != "" {
			t.Fatalf("intakes[%d].customer_note=%q; esa solicitud no indicó nada", i, in.CustomerNote)
		}
	}
	// INV-13: el revenue agregado es el mismo con indicación o sin ella.
	if sum.Totals.Revenue != 90000 {
		t.Fatalf("revenue=%v; la indicación del pedido movió el dinero", sum.Totals.Revenue)
	}
}
