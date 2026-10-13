//go:build integracion

package procesos

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// GET /api/v1/intakes/export (CSV y XLSX) y GET /api/v1/intakes/summary.json: sacar los pedidos del
// sistema. Los abre la feature `intakes_export`, aparte de la que abre la bandeja (`cart_basic`).

const (
	// p5CSVBOM es la marca con la que empieza el CSV (para que una hoja de cálculo lo lea como UTF-8).
	p5CSVBOM = "\xef\xbb\xbf"
	// p5CSVHeader es la cabecera del export: siete columnas de la solicitud y seis de la línea.
	p5CSVHeader = "intake_id,created_at,status,session_id,contact_ref,intake_total,customer_note,sku,label,customization,qty,unit_price,line_total"
	// p5NoExport es el cuerpo del 403 de una empresa sin la feature.
	p5NoExport = `{"error":"feature_not_enabled","feature":"intakes_export"}`
)

// p5ExportedLines son las seis celdas de línea que el export saca de cada línea de p5StoredItems: la
// celda que empieza por `=` o por `@` sale con un apóstrofo delante (una hoja de cálculo la tomaría
// por fórmula); el separador repetido, el U+00A0 y los dígitos no ASCII salen tal cual.
func p5ExportedLines() []string {
	return []string{
		"TORTA-CHOC#10|Torta de chocolate 10 porciones|decoración infantil|1|2100|2100",
		"TORTA-VAIN|Torta de vainilla||1|3900|3900",
		"TEQ-30|Tequeños congelados||2|490|980",
		"PAN@@1|Pan de masa madre|'=SUM(A1)|1|120|120",
		"PAN 2|Pan integral con semillas||1|130|130",
		"PAN-١٢٣|Pan ١٢٣||1|140|140",
		"REGALO|'@@cortesía||1|0|0",
	}
}

// p5ParseCSV exige un 200 con la forma del export —BOM, cabecera exacta, finales de línea CRLF— y
// devuelve sus filas agrupadas por solicitud, cada una como sus celdas unidas por «|».
func p5ParseCSV(t *testing.T, r respuesta, what string) map[string][]string {
	t.Helper()
	body := string(r.Cuerpo)
	if r.Codigo != http.StatusOK || !strings.HasPrefix(body, p5CSVBOM+p5CSVHeader+"\r\n") {
		t.Fatalf("%s: HTTP %d; quería 200 con el BOM y la cabecera del export\ncuerpo: %s", what, r.Codigo, recortar(r.Cuerpo))
	}
	if strings.Count(body, "\n") != strings.Count(body, "\r\n") {
		t.Errorf("%s: el CSV trae saltos de línea que no son CRLF", what)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, p5CSVBOM))).ReadAll()
	if err != nil {
		t.Fatalf("%s: el CSV no se puede leer: %v", what, err)
	}
	out := map[string][]string{}
	for _, row := range rows[1:] {
		if len(row) != 13 {
			t.Fatalf("%s: una fila del CSV trae %d celdas, quería 13: %v", what, len(row), row)
		}
		out[row[0]] = append(out[row[0]], strings.Join(row[1:], "|"))
	}
	return out
}

// exportedHead son las seis celdas de cabecera (sin el id) que el export saca de una solicitud,
// leídas de Postgres: fecha de alta en RFC3339 UTC, estado, sesión, el contact_id OPACO tal cual, el
// total y la nota.
func (w *p5World) exportedHead(t *testing.T, id string) string {
	t.Helper()
	const head = `SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') || '|' || status || '|' || session_id
		|| '|' || contact_id || '|' || (total::float8)::text || '|' || customer_note FROM public.intakes WHERE id = $1::uuid`
	return p9Scalar(t, w.sc.DB, head, id)
}

// expectExported afirma las filas del export de una solicitud: una por línea guardada, o UNA con las
// seis celdas de línea vacías si no tiene ninguna.
func (w *p5World) expectExported(t *testing.T, rows map[string][]string, id string, withLines bool, what string) {
	t.Helper()
	head := w.exportedHead(t, id)
	want := []string{head + "||||||"}
	if withLines {
		want = want[:0]
		for _, line := range p5ExportedLines() {
			want = append(want, head+"|"+line)
		}
	}
	if got := rows[id]; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s: las filas de %s son\n%s\nquería\n%s", what, id, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// p5Export es el export y el resumen sobre lo que el proceso dejó —una solicitud aprobada, una en
// needs_info sin líneas y una descartada— y la empresa que no tiene la feature.
func p5Export(t *testing.T, w *p5World) {
	r := w.read(t, http.MethodGet, "/api/v1/intakes/export", nil)
	rows := p5ParseCSV(t, r, "el export sin filtros")
	if len(rows) != 3 {
		t.Errorf("el export sin filtros trae %d solicitudes, quería las 3", len(rows))
	}
	w.expectExported(t, rows, w.first, true, "el export sin filtros")
	w.expectExported(t, rows, w.asked, false, "el export sin filtros")
	w.expectExported(t, rows, w.open, true, "el export sin filtros")
	for _, phone := range []string{p5FirstPn, p5AskedPn, p5OpenPn} {
		if bytes.Contains(r.Cuerpo, []byte(phone)) {
			t.Errorf("el export lleva el teléfono %s: contact_ref es el identificador opaco", phone)
		}
	}

	for query, want := range map[string][]string{
		"status=confirmed":                   {w.first},
		"status=closed":                      {w.first}, // el `closed` legado se lee como confirmed
		"status=confirmed&status=needs_info": {w.first, w.asked},
		"format=csv&orphan=true":             {w.open},
		"from=2020-01-01&to=2020-01-02":      {},
	} {
		rows = p5ParseCSV(t, w.read(t, http.MethodGet, "/api/v1/intakes/export?"+query, nil), "el export con "+query)
		for _, id := range want {
			if len(rows[id]) == 0 {
				t.Errorf("el export con %s no trae la solicitud %s", query, id)
			}
		}
		if len(rows) != len(want) {
			t.Errorf("el export con %s trae %d solicitudes, quería %d", query, len(rows), len(want))
		}
	}
	p5ExportXLSX(t, w)
	p5FilterRejected(t, w)
	p5Summary(t, w)
	p5ExportWithoutFeature(t, w)
}

// p5ExportXLSX afirma el export en XLSX: un libro (zip) con la hoja `solicitudes`.
func p5ExportXLSX(t *testing.T, w *p5World) {
	t.Helper()
	r := w.read(t, http.MethodGet, "/api/v1/intakes/export?format=xlsx", nil)
	book, err := zip.NewReader(bytes.NewReader(r.Cuerpo), int64(len(r.Cuerpo)))
	if r.Codigo != http.StatusOK || err != nil {
		t.Fatalf("el export en XLSX: HTTP %d, y como zip: %v", r.Codigo, err)
	}
	for _, f := range book.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("abrir xl/workbook.xml: %v", err)
		}
		raw, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		if cerr := rc.Close(); err != nil || cerr != nil {
			t.Fatalf("leer xl/workbook.xml: %v / %v", err, cerr)
		}
		if !bytes.Contains(raw, []byte(`name="solicitudes"`)) {
			t.Errorf("el libro del export no tiene la hoja `solicitudes`")
		}
		return
	}
	t.Errorf("el export en XLSX no trae xl/workbook.xml")
}

// p5FilterRejected es la tabla adversaria de los filtros, la misma para la bandeja, el export y el
// resumen (comparten el filtro): un valor que no se entiende es un 400, nunca «sin filtro».
func p5FilterRejected(t *testing.T, w *p5World) {
	t.Helper()
	cases := map[string]string{
		"status=a@@b":                       "status desconocido",
		"status=%D9%A1%D9%A2%D9%A3":         "status desconocido",
		"status=%C2%A0confirmed":            "status desconocido",
		"status=confirmed,needs_info":       "status desconocido",
		"sort=a@@b":                         "sort desconocido: usa newest u oldest",
		"orphan=si":                         "orphan inválido: usa true o false",
		"from=2026-13-40":                   "from inválido: usa YYYY-MM-DD o RFC3339",
		"to=%D9%A2%D9%A0%D9%A2%D9%A6-01-01": "to inválido: usa YYYY-MM-DD o RFC3339",
	}
	for _, route := range []string{"/api/v1/intakes", "/api/v1/intakes/export", "/api/v1/intakes/summary.json"} {
		for query, want := range cases {
			r := w.read(t, http.MethodGet, route+"?"+query, nil)
			p5ExpectError(t, r, http.StatusBadRequest, want, "GET "+route+"?"+query)
		}
	}
	r := w.read(t, http.MethodGet, "/api/v1/intakes/export?format=pdf", nil)
	p5ExpectError(t, r, http.StatusBadRequest, "format inválido: usa csv o xlsx", "el export en un formato desconocido")
}

// p5SummaryBody es el cuerpo de summary.json.
type p5SummaryBody struct {
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
		ID     string   `json:"id"`
		Status string   `json:"status"`
		Total  float64  `json:"total"`
		Items  []p5Item `json:"items"`
	} `json:"intakes"`
}

// p5Summary afirma el resumen: cuántas solicitudes, la suma de sus totales (TODAS, también la
// descartada), el desglose por estado, el ranking de artículos por importe y el detalle; y, con un
// rango sin solicitudes, las listas vacías y nunca nulas.
func p5Summary(t *testing.T, w *p5World) {
	t.Helper()
	r := w.read(t, http.MethodGet, "/api/v1/intakes/summary.json", nil)
	var sum p5SummaryBody
	r.JSON(t, &sum)
	if _, err := time.Parse(time.RFC3339, sum.GeneratedAt); r.Codigo != http.StatusOK || err != nil {
		t.Fatalf("summary.json: HTTP %d con generated_at %q (%v)\ncuerpo: %s", r.Codigo, sum.GeneratedAt, err, recortar(r.Cuerpo))
	}
	if got := sum.Range.From + "|" + sum.Range.To; got != "|" {
		t.Errorf("el rango del resumen sin filtros = %q, quería sin cotas", got)
	}
	if got := fmt.Sprintf("%d|%v|%v", sum.Totals.Intakes, sum.Totals.Revenue, sum.Totals.ByStatus); got != "3|14740|map[abandoned:1 confirmed:1 needs_info:1]" {
		t.Errorf("los totales del resumen = %s; quería 3 solicitudes, 14740 y una por estado", got)
	}
	tops := make([]string, 0, len(sum.TopItems))
	for _, top := range sum.TopItems {
		tops = append(tops, fmt.Sprintf("%s|%s|%d|%v", top.SKU, top.Label, top.QtyTotal, top.Revenue))
	}
	want := []string{
		"TORTA-VAIN|Torta de vainilla|2|7800", "TORTA-CHOC#10|Torta de chocolate 10 porciones|2|4200", "TEQ-30|Tequeños congelados|4|1960",
		"PAN-١٢٣|Pan ١٢٣|2|280", "PAN 2|Pan integral con semillas|2|260", "PAN@@1|Pan de masa madre|2|240", "REGALO|@@cortesía|2|0",
	}
	if strings.Join(tops, "\n") != strings.Join(want, "\n") {
		t.Errorf("el ranking del resumen es\n%s\nquería\n%s", strings.Join(tops, "\n"), strings.Join(want, "\n"))
	}
	lines := map[string]string{}
	for _, in := range sum.Intakes {
		lines[in.ID] = fmt.Sprintf("%s|%v|%d", in.Status, in.Total, len(in.Items))
	}
	got := fmt.Sprintf("%d %s %s %s", len(lines), lines[w.first], lines[w.asked], lines[w.open])
	if got != "3 confirmed|7370|7 needs_info|0|0 abandoned|7370|7" {
		t.Errorf("el detalle del resumen (aprobada, en needs_info, descartada) = %s", got)
	}

	p5EmptySummary(t, w)
}

// p5EmptySummary afirma el resumen de un rango sin solicitudes: el rango tal como se entendió (una
// fecha suelta en `to` llega hasta el final de ese día), los totales a cero y las listas VACÍAS, nunca
// nulas.
func p5EmptySummary(t *testing.T, w *p5World) {
	t.Helper()
	r := w.read(t, http.MethodGet, "/api/v1/intakes/summary.json?from=2020-01-01&to=2020-01-02", nil)
	if r.Codigo != http.StatusOK {
		t.Fatalf("el resumen de un rango vacío: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	for _, piece := range []string{
		`"range":{"from":"2020-01-01T00:00:00Z","to":"2020-01-03T00:00:00Z"}`,
		`"totals":{"intakes":0,"revenue":0,"by_status":{}}`, `"top_items":[]`, `"intakes":[]`,
	} {
		if !bytes.Contains(r.Cuerpo, []byte(piece)) {
			t.Errorf("el resumen de un rango vacío no trae %s\ncuerpo: %s", piece, recortar(r.Cuerpo))
		}
	}
}

// p5DisableFeature apaga una feature para una empresa con un override (tenant_features), que gana
// al plan. Hay que llamarlo ANTES de la primera petición de esa empresa: el servidor cachea cada
// (empresa, feature) 60 s. Falla (t.Fatalf) si la empresa no es un UUID o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: los SEIS planes sembrados traen `intakes_export`, ninguna ruta
// cambia el plan de una empresa ya creada y ninguna escribe tenant_features: una empresa sin la
// feature solo se consigue hoy con el override por SQL, que es como lo hace el operador.
func p5DisableFeature(t *testing.T, db *sql.DB, tenant, feature string) {
	t.Helper()
	if err := exigirUUID("la empresa", tenant); err != nil {
		t.Fatalf("p5DisableFeature: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	if _, err := db.ExecContext(ctx, `INSERT INTO public.tenant_features (tenant_id, feature, enabled)
		VALUES ($1::uuid, $2, false)`, tenant, feature); err != nil {
		t.Fatalf("p5DisableFeature(%s, %s): %v", tenant, feature, err)
	}
}

// p5ExportWithoutFeature da de alta OTRA empresa (plan `basic`, con `intakes_export` apagada por
// override) y afirma: el export y el resumen le contestan 403 con la clave que falta —antes de mirar
// la consulta—; su bandeja sí abre, vacía; y las solicitudes de la empresa del proceso no existen
// para ella por ninguna puerta (INV-8), ni cambian; y la sugerencia, que su plan no trae, le contesta 403.
func p5ExportWithoutFeature(t *testing.T, w *p5World) {
	t.Helper()
	sc := w.sc
	other := crearTenantConPlan(t, sc.S, sc.TokenStaff, "p5-sin-export", "basic")
	p5DisableFeature(t, sc.DB, other, "intakes_export")
	person := p10NewMember(t, sc.edgeEscenario, other, edgeRolTenantAdmin)
	caller := p9Caller{client: sc.S.Publica(person.Token), tenant: other}
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.first)

	for _, path := range []string{"/api/v1/intakes/export", "/api/v1/intakes/export?format=pdf", "/api/v1/intakes/summary.json"} {
		r := w.calls.call(t, caller, "", http.MethodGet, path, nil)
		if r.Codigo != http.StatusForbidden || strings.TrimSpace(string(r.Cuerpo)) != p5NoExport {
			t.Errorf("GET %s sin la feature: HTTP %d %s; quería 403 %s", path, r.Codigo, recortar(r.Cuerpo), p5NoExport)
		}
	}
	r := w.calls.call(t, caller, "", http.MethodGet, "/api/v1/intakes", nil)
	if !p9ErrorIs(r, http.StatusOK, `"intakes":[]`) || !p9ErrorIs(r, http.StatusOK, `"total":0`) {
		t.Errorf("la bandeja de la empresa sin export: HTTP %d %s; quería 200 y vacía", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.calls.call(t, caller, "", http.MethodGet, p5Path(w.first, ""), nil)
	p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "leer una solicitud de otra empresa")
	r = w.calls.call(t, caller, "", http.MethodPost, p5Path(w.first, "approve"), map[string]string{"rendered_text": p5OwnerQuote})
	p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "aprobar una solicitud de otra empresa")
	r = w.calls.call(t, caller, "", http.MethodPut, p5Path(w.asked, "items"), map[string]any{"items": p5Items()})
	p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "editar una solicitud de otra empresa")
	r = w.calls.call(t, caller, "", http.MethodPost, p5Path(w.first, "request-info"), map[string]string{"question": "¿?"})
	p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "pedir información de una solicitud de otra empresa")
	r = w.calls.call(t, caller, "", http.MethodPost, p5Path(w.first, "quote-suggestion"), nil)
	if r.Codigo != http.StatusForbidden || strings.TrimSpace(string(r.Cuerpo)) != `{"error":"feature_not_enabled","feature":"llm_intake"}` {
		t.Errorf("quote-suggestion desde un plan sin llm_intake: HTTP %d %s; quería 403 con la feature que falta", r.Codigo, recortar(r.Cuerpo))
	}
	r = w.calls.call(t, caller, "", http.MethodPost, "/api/v1/intakes/discard", map[string]any{"intake_ids": []string{w.first}})
	if !p9ErrorIs(r, http.StatusOK, `{"discarded":[],"skipped":[{"intake_id":"`+w.first+`","reason":"not_found"}]}`) {
		t.Errorf("descartar una solicitud de otra empresa: HTTP %d %s; quería 200 con not_found", r.Codigo, recortar(r.Cuerpo))
	}
	w.expectUntouched(t, before, w.first, "tras los intentos de otra empresa")

	// La empresa del proceso sigue exportando: el gate es por empresa.
	if r = w.read(t, http.MethodGet, "/api/v1/intakes/export?status=confirmed", nil); r.Codigo != http.StatusOK {
		t.Errorf("el export de la empresa del proceso tras el 403 de la otra: HTTP %d, quería 200", r.Codigo)
	}
}
