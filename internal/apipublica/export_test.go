//go:build pendiente

package apipublica_test

// export_test.go — cubre G9, `GET /api/v1/intakes/export`, tal como lo promete
// MountIntakeReports (intakereports.go): formatos, cabeceras, columnas, filas, escape de
// fórmulas, tope y errores. La cadena y el gate están en intakereports_test.go; los auxiliares
// no exportados de export.go, en export_internal_test.go.

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	exportBOM    = "\xef\xbb\xbf"
	exportHeader = "intake_id,created_at,status,session_id,contact_ref,intake_total,customer_note," +
		"sku,label,customization,qty,unit_price,line_total"
	exportSheetName = "solicitudes"
	exportCSVType   = "text/csv; charset=utf-8"
	exportXLSXType  = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

	exportIntakeA1 = "11111111-1111-4111-8111-111111111111"
	exportIntakeA2 = "22222222-2222-4222-8222-222222222222"
	exportContact  = "9f1c0a7e-0000-4000-8000-000000000abc"

	msgExportFormat = "format inválido: usa csv o xlsx"
	msgExportRead   = "no se pudieron leer las solicitudes"
)

// exportFixture son dos solicitudes: una con dos líneas (creada en una zona que no es UTC) y
// otra sin ninguna.
func exportFixture() []intakes.Detail {
	zone := time.FixedZone("-03", -3*3600)
	return []intakes.Detail{
		{
			Intake: intakes.Intake{
				ID: exportIntakeA1, ContactID: exportContact, SessionID: "sess-a", Status: intakes.StatusConfirmed,
				Total: 21001, CreatedAt: time.Date(2026, 8, 1, 9, 0, 0, 0, zone), CustomerNote: "dejar en portería",
			},
			Items: []intakes.Item{
				{SKU: "torta-v1", Label: "Torta 10-12 porciones", Customization: "sin sal", Qty: 1, UnitPrice: 18000},
				{SKU: "_shipping", Label: "Envío — Providencia", Qty: 2, UnitPrice: 1500.5},
			},
			// Lo que el export NO publica: ni que el comprador dejó datos.
			BuyerDataPresent: true,
		},
		{Intake: intakes.Intake{
			ID: exportIntakeA2, SessionID: "sess-b", Status: intakes.StatusOpen,
			CreatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		}},
	}
}

// exportParseCSV exige el BOM y devuelve las filas del CSV (cabecera incluida).
func exportParseCSV(t *testing.T, body string) [][]string {
	t.Helper()
	rest, ok := strings.CutPrefix(body, exportBOM)
	if !ok {
		t.Fatalf("el CSV no empieza por el BOM UTF-8: %q", body)
	}
	rows, err := csv.NewReader(strings.NewReader(rest)).ReadAll()
	if err != nil {
		t.Fatalf("el CSV no se puede releer: %v (%q)", err, body)
	}
	return rows
}

// exportOpenXLSX reabre el libro que devolvió la cara; el llamante no tiene que cerrarlo.
func exportOpenXLSX(t *testing.T, body []byte) *excelize.File {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("el XLSX no se puede reabrir: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("cerrando el libro: %v", cerr)
		}
	})
	return f
}

// exportCell lee una celda del libro como texto y exige que no sea una fórmula.
func exportCell(t *testing.T, f *excelize.File, cell string) string {
	t.Helper()
	value, err := f.GetCellValue(exportSheetName, cell)
	if err != nil {
		t.Fatalf("leyendo la celda %s: %v", cell, err)
	}
	formula, err := f.GetCellFormula(exportSheetName, cell)
	if err != nil {
		t.Fatalf("leyendo la fórmula de %s: %v", cell, err)
	}
	if formula != "" {
		t.Errorf("la celda %s se guardó como FÓRMULA (%q): Excel la ejecutaría", cell, formula)
	}
	return value
}

// TestExport_CSVGolden: el fichero byte a byte —BOM, CRLF, las 13 columnas en su orden, la
// cabecera de la solicitud repetida por línea, la solicitud sin líneas con sus 6 huecos, UTC y
// números sin adornos— y sus tres cabeceras HTTP, con el nombre fechado por el reloj inyectado.
func TestExport_CSVGolden(t *testing.T) {
	svc := &reportServiceSpy{details: exportFixture()}
	rec := reportGet(t, reportDeps(svc), exportTarget)
	wantCode(t, "G9", rec, http.StatusOK)

	head := exportIntakeA1 + ",2026-08-01T12:00:00Z,confirmed,sess-a," + exportContact + ",21001,dejar en portería"
	want := exportBOM + exportHeader + "\r\n" +
		head + ",torta-v1,Torta 10-12 porciones,sin sal,1,18000,18000\r\n" +
		head + ",_shipping,Envío — Providencia,,2,1500.5,3001\r\n" +
		exportIntakeA2 + ",2026-08-02T00:00:00Z,open,sess-b,,0,,,,,,,\r\n"
	wantExactBody(t, "G9", rec, want)

	if got := rec.Header().Get("Content-Type"); got != exportCSVType {
		t.Errorf("Content-Type = %q, quiero %q", got, exportCSVType)
	}
	// reportNow son las 06:30:05 en -03: el nombre va en UTC.
	if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename="intakes-20261008-093005.csv"`; got != want {
		t.Errorf("Content-Disposition = %q, quiero %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Errorf("Content-Length = %q, quiero %q (los bytes del cuerpo)", got, want)
	}
	if svc.tenant != tenantA {
		t.Errorf("ListDetails recibió el tenant %q, quiero el del token %q", svc.tenant, tenantA)
	}
	if svc.remaining != -1 {
		t.Errorf("al contexto de ListDetails le quedaban %s; la cara no le pone plazo a esta lectura", svc.remaining)
	}
}

// TestExport_EmptyIsAHeaderOnlyFile: sin solicitudes el fichero es la cabecera sola, también si
// el puerto devuelve nil.
func TestExport_EmptyIsAHeaderOnlyFile(t *testing.T) {
	for name, details := range map[string][]intakes.Detail{"nil": nil, "empty": {}} {
		rec := reportGet(t, reportDeps(&reportServiceSpy{details: details}), exportTarget)
		wantCode(t, name, rec, http.StatusOK)
		wantExactBody(t, name, rec, exportBOM+exportHeader+"\r\n")
	}
}

// TestExport_CSVEscapesFormulas: una celda de TEXTO cuyo primer carácter es `=`, `+`, `-`, `@`,
// tabulador o retorno de carro sale con `'` delante; solo cuenta el primer carácter. El texto
// va en las dos celdas que teclea el cliente final y en una del catálogo.
func TestExport_CSVEscapesFormulas(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"equals", "=1+1", "'=1+1"},
		{"plus", "+56 9 1234 5678", "'+56 9 1234 5678"},
		{"minus", "-sin cebolla", "'-sin cebolla"},
		{"at", "@sospechoso", "'@sospechoso"},
		{"tab", "\t=cmd|' /C calc'!A0", "'\t=cmd|' /C calc'!A0"},
		{"carriage_return", "\r=1+1", "'\r=1+1"},
		{"lone_equals", "=", "'="},
		{"lone_minus", "-", "'-"},
		{"dde_payload", `=HYPERLINK("http://x","y")`, `'=HYPERLINK("http://x","y")`},
		{"leading_space_is_not_first_char", " =1+1", " =1+1"},
		{"formula_char_in_the_middle", "a=b+c-d@e", "a=b+c-d@e"},
		{"already_quoted", "'=1+1", "'=1+1"},
		{"plain_text", "sin sal", "sin sal"},
		{"unicode", "ñandú 🎂 — «sin azúcar»", "ñandú 🎂 — «sin azúcar»"},
		{"fullwidth_equals_is_not_ascii", "＝1+1", "＝1+1"},
		{"digits_as_text", "123", "123"},
		{"comma_quote_and_newline", "dijo \"hola\", y\nse fue", "dijo \"hola\", y\nse fue"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := []intakes.Detail{{
				Intake: intakes.Intake{ID: exportIntakeA1, Status: intakes.StatusOpen, CustomerNote: tc.text},
				Items:  []intakes.Item{{SKU: "sku", Label: tc.text, Customization: tc.text, Qty: 1, UnitPrice: 1}},
			}}
			rec := reportGet(t, reportDeps(&reportServiceSpy{details: details}), exportTarget)
			wantCode(t, tc.name, rec, http.StatusOK)
			rows := exportParseCSV(t, rec.Body.String())
			if len(rows) != 2 || len(rows[1]) != 13 {
				t.Fatalf("filas = %q, quiero la cabecera y UNA fila de 13 celdas", rows)
			}
			for col, name := range map[int]string{6: "customer_note", 8: "label", 9: "customization"} {
				if got := rows[1][col]; got != tc.want {
					t.Errorf("%s = %q, quiero %q", name, got, tc.want)
				}
			}
		})
	}
}

// TestExport_CSVEscapesEveryTextColumnAndNoNumber: el escape es de TODA celda de texto, no de
// tres columnas elegidas, y NUNCA de un número: un negativo prefijado dejaría de sumarse.
func TestExport_CSVEscapesEveryTextColumnAndNoNumber(t *testing.T) {
	details := []intakes.Detail{{
		Intake: intakes.Intake{
			ID: "=id", ContactID: "@contact", SessionID: "+session", Status: "-status", Total: -5,
			CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), CustomerNote: "=note",
		},
		Items: []intakes.Item{{SKU: "=sku", Label: "@label", Customization: "\tcustom", Qty: -1, UnitPrice: 2.5}},
	}}
	rec := reportGet(t, reportDeps(&reportServiceSpy{details: details}), exportTarget)
	wantCode(t, "G9", rec, http.StatusOK)
	want := []string{
		"'=id", "2026-08-01T00:00:00Z", "'-status", "'+session", "'@contact", "-5", "'=note",
		"'=sku", "'@label", "'\tcustom", "-1", "2.5", "-2.5",
	}
	if rows := exportParseCSV(t, rec.Body.String()); len(rows) != 2 || !slices.Equal(rows[1], want) {
		t.Errorf("filas = %q\nquiero la cabecera y %q", rows, want)
	}
}

// TestExport_CSVNumbers: los números son datos, no presentación: punto decimal, sin separador de
// miles, sin ceros de relleno y sin notación científica.
func TestExport_CSVNumbers(t *testing.T) {
	cases := []struct {
		name               string
		total, price       float64
		qty                int
		wantTotal, wantRow string
	}{
		{"integers", 18000, 18000, 1, "18000", "1,18000,18000"},
		{"decimals", 0.5, 0.25, 3, "0.5", "3,0.25,0.75"},
		{"millions_without_thousands_separator", 1234567.89, 1234567.89, 1, "1234567.89", "1,1234567.89,1234567.89"},
		{"large_without_exponent", 1e21, 1e21, 1, "1000000000000000000000", "1,1000000000000000000000,1000000000000000000000"},
		{"zero_quantity", 0, 990, 0, "0", "0,990,0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := []intakes.Detail{{
				Intake: intakes.Intake{ID: "id", Status: intakes.StatusOpen, Total: tc.total, CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
				Items:  []intakes.Item{{SKU: "s", Label: "l", Qty: tc.qty, UnitPrice: tc.price}},
			}}
			rec := reportGet(t, reportDeps(&reportServiceSpy{details: details}), exportTarget)
			want := exportBOM + exportHeader + "\r\n" + "id,2026-08-01T00:00:00Z,open,,," + tc.wantTotal + ",,s,l,," + tc.wantRow + "\r\n"
			wantExactBody(t, tc.name, rec, want)
		})
	}
}

// TestExport_XLSX: el libro se reabre, tiene UNA hoja llamada «solicitudes», las mismas filas
// que el CSV, los números como números y las celdas de línea de una solicitud sin líneas sin
// escribir.
func TestExport_XLSX(t *testing.T) {
	rec := reportGet(t, reportDeps(&reportServiceSpy{details: exportFixture()}), exportTarget+"?format=xlsx")
	wantCode(t, "G9 xlsx", rec, http.StatusOK)
	if got := rec.Header().Get("Content-Type"); got != exportXLSXType {
		t.Errorf("Content-Type = %q, quiero %q", got, exportXLSXType)
	}
	if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename="intakes-20261008-093005.xlsx"`; got != want {
		t.Errorf("Content-Disposition = %q, quiero %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Errorf("Content-Length = %q, quiero %q", got, want)
	}

	f := exportOpenXLSX(t, rec.Body.Bytes())
	if sheets := f.GetSheetList(); !slices.Equal(sheets, []string{exportSheetName}) {
		t.Fatalf("hojas = %v, quiero exactamente [%s]", sheets, exportSheetName)
	}
	want := map[string]string{
		"A1": "intake_id", "G1": "customer_note", "M1": "line_total",
		"A2": exportIntakeA1, "B2": "2026-08-01T12:00:00Z", "C2": "confirmed", "D2": "sess-a", "E2": exportContact,
		"F2": "21001", "G2": "dejar en portería", "H2": "torta-v1", "I2": "Torta 10-12 porciones", "J2": "sin sal",
		"K2": "1", "L2": "18000", "M2": "18000",
		"A3": exportIntakeA1, "G3": "dejar en portería", "H3": "_shipping", "I3": "Envío — Providencia", "J3": "",
		"K3": "2", "L3": "1500.5", "M3": "3001",
		"A4": exportIntakeA2, "C4": "open", "F4": "0", "H4": "", "I4": "", "J4": "", "K4": "", "L4": "", "M4": "",
		"A5": "",
	}
	for cell, value := range want {
		if got := exportCell(t, f, cell); got != value {
			t.Errorf("celda %s = %q, quiero %q", cell, got, value)
		}
	}
	header, err := f.GetRows(exportSheetName)
	if err != nil || len(header) != 4 || strings.Join(header[0], ",") != exportHeader {
		t.Errorf("filas = %q (err %v); quiero 4 y la cabecera %q", header, err, exportHeader)
	}
	// Un número es un número (la hoja lo suma) y un texto es una cadena.
	for cell, wantText := range map[string]bool{"A2": true, "G2": true, "H2": true, "F2": false, "K2": false, "L3": false, "M3": false} {
		kind, kerr := f.GetCellType(exportSheetName, cell)
		if kerr != nil {
			t.Fatalf("tipo de %s: %v", cell, kerr)
		}
		isText := kind == excelize.CellTypeSharedString || kind == excelize.CellTypeInlineString
		if isText != wantText {
			t.Errorf("celda %s: tipo %v; quiero texto=%v", cell, kind, wantText)
		}
	}
}

// TestExport_XLSXKeepsTextIntactAndNeverAsFormula: en XLSX la defensa NO es el apóstrofo —sería
// un carácter más del dato, a la vista— sino el TIPO de la celda: cadena, nunca fórmula.
func TestExport_XLSXKeepsTextIntactAndNeverAsFormula(t *testing.T) {
	texts := []string{"=1+1", "+56 9", "-sin cebolla", "@sospechoso", "=SUM(A1:A9)", `=HYPERLINK("http://x","y")`, "ñandú 🎂", "123"}
	details := make([]intakes.Detail, 0, len(texts))
	for i, text := range texts {
		details = append(details, intakes.Detail{
			Intake: intakes.Intake{ID: fmt.Sprintf("id-%d", i), Status: intakes.StatusOpen, CustomerNote: text},
			Items:  []intakes.Item{{SKU: text, Label: text, Customization: text, Qty: 1, UnitPrice: 1}},
		})
	}
	rec := reportGet(t, reportDeps(&reportServiceSpy{details: details}), exportTarget+"?format=xlsx")
	wantCode(t, "G9 xlsx", rec, http.StatusOK)
	f := exportOpenXLSX(t, rec.Body.Bytes())
	for i, text := range texts {
		for _, col := range []string{"G", "H", "I", "J"} {
			cell := col + strconv.Itoa(i+2)
			if got := exportCell(t, f, cell); got != text {
				t.Errorf("celda %s = %q, quiero %q íntegro (sin apóstrofo)", cell, got, text)
			}
		}
	}
}

// TestExport_Format: sin `format` o vacío es CSV; solo valen los dos literales, y lo demás es un
// 400 que se decide antes de mirar el filtro y sin consultar el puerto.
func TestExport_Format(t *testing.T) {
	cases := []struct {
		query       string
		code        int
		contentType string
	}{
		{"", http.StatusOK, exportCSVType},
		{"?format=", http.StatusOK, exportCSVType},
		{"?format=csv", http.StatusOK, exportCSVType},
		{"?format=xlsx", http.StatusOK, exportXLSXType},
		{"?format=CSV", http.StatusBadRequest, ""},
		{"?format=XLSX", http.StatusBadRequest, ""},
		{"?format=xls", http.StatusBadRequest, ""},
		{"?format=pdf", http.StatusBadRequest, ""},
		{"?format=csv%20", http.StatusBadRequest, ""},
		{"?format=..%2Fetc%2Fpasswd", http.StatusBadRequest, ""},
		// El formato se mira ANTES que el filtro: gana su mensaje.
		{"?format=json&status=nope", http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			svc := &reportServiceSpy{}
			rec := reportGet(t, reportDeps(svc), exportTarget+tc.query)
			wantCode(t, tc.query, rec, tc.code)
			if tc.code == http.StatusOK {
				if got := rec.Header().Get("Content-Type"); got != tc.contentType {
					t.Errorf("Content-Type = %q, quiero %q", got, tc.contentType)
				}
				return
			}
			wantErrorBody(t, tc.query, rec, msgExportFormat)
			exportWantNoFile(t, tc.query, rec, svc, 0)
		})
	}
}

// exportWantNoFile: un error no es una descarga, y el puerto recibió las llamadas que se dicen.
func exportWantNoFile(t *testing.T, what string, rec *httptest.ResponseRecorder, svc *reportServiceSpy, calls int) {
	t.Helper()
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("%s: Content-Disposition = %q en un error; no es una descarga", what, got)
	}
	if svc.calls != calls {
		t.Errorf("%s: el puerto recibió %d llamadas, quiero %d", what, svc.calls, calls)
	}
}

// TestExport_Filter: el filtro de la query llega al puerto; uno mal escrito es 400 con el
// mensaje del filtro y el puerto no se consulta. (El filtro a fondo lo prueba su dueño.)
func TestExport_Filter(t *testing.T) {
	svc := &reportServiceSpy{}
	rec := reportGet(t, reportDeps(svc), exportTarget+"?format=xlsx&session=sess-a&from=2026-08-01T00:00:00Z&tenant_id="+tenantB)
	wantCode(t, "filtro", rec, http.StatusOK)
	if svc.filter.SessionID != "sess-a" || !svc.filter.From.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("el puerto recibió el filtro %+v; quiero session sess-a y from 2026-08-01", svc.filter)
	}
	if svc.tenant != tenantA {
		t.Errorf("el puerto recibió el tenant %q; un tenant en la query no cuenta, quiero %q", svc.tenant, tenantA)
	}

	svc = &reportServiceSpy{}
	rec = reportGet(t, reportDeps(svc), exportTarget+"?status=nope")
	wantCode(t, "filtro inválido", rec, http.StatusBadRequest)
	wantErrorBody(t, "filtro inválido", rec, "status desconocido")
	exportWantNoFile(t, "filtro inválido", rec, svc, 0)
}

// TestExport_Errors: pasarse del tope es 422 (nunca un recorte silencioso) y cualquier otro
// fallo un 500 que no repite el error del puerto.
func TestExport_Errors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"too_large", intakes.ErrTooLarge, http.StatusUnprocessableEntity, reportTooLarge},
		{"too_large_wrapped", fmt.Errorf("leyendo: %w", intakes.ErrTooLarge), http.StatusUnprocessableEntity, reportTooLarge},
		{"store_down", errors.New("postgres://usuario:secreto@host/bd: conexión rechazada"), http.StatusInternalServerError, msgExportRead},
		{"not_found_is_not_special_here", intakes.ErrNotFound, http.StatusInternalServerError, msgExportRead},
	}
	for _, tc := range cases {
		for _, format := range []string{"csv", "xlsx"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				svc := &reportServiceSpy{err: tc.err, details: exportFixture()}
				rec := reportGet(t, reportDeps(svc), exportTarget+"?format="+format)
				wantCode(t, tc.name, rec, tc.code)
				wantErrorBody(t, tc.name, rec, tc.msg)
				exportWantNoFile(t, tc.name, rec, svc, 1)
			})
		}
	}
}

// TestExport_WithTheModuleService: con el servicio REAL del módulo sobre su almacén en memoria,
// cada tenant exporta SOLO lo suyo y el tope es intakes.MaxExportIntakes: con exactamente esa
// cantidad sale todo; con una más, 422 y ningún fichero.
func TestExport_WithTheModuleService(t *testing.T) {
	store := intakes.NewMemoryStore()
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for i := range intakes.MaxExportIntakes {
		store.Add(tenantA, intakes.Intake{ID: fmt.Sprintf("a-%04d", i), Status: intakes.StatusOpen, CreatedAt: created})
	}
	store.Add(tenantB, intakes.Intake{ID: "b-0001", Status: intakes.StatusOpen, Total: 7, CreatedAt: created},
		intakes.Item{SKU: "pan", Label: "Pan", Qty: 7, UnitPrice: 1})

	h := apipublicahelpertest.New(t)
	d := reportDeps(intakes.NewService(store))
	d.Entitlements = withFeatures(entitlements.FeatureIntakesExport)
	cara := reportCara(h.Common(), d)

	rec := h.Call(cara, h.With(tenantB, reportPerm), http.MethodGet, exportTarget, "")
	wantCode(t, "tenantB", rec, http.StatusOK)
	wantExactBody(t, "tenantB", rec, exportBOM+exportHeader+"\r\nb-0001,2026-08-01T12:00:00Z,open,,,7,,pan,Pan,,7,1,7\r\n")

	rec = h.Call(cara, h.With(tenantA, reportPerm), http.MethodGet, exportTarget, "")
	wantCode(t, "en el tope", rec, http.StatusOK)
	if rows := exportParseCSV(t, rec.Body.String()); len(rows) != intakes.MaxExportIntakes+1 {
		t.Errorf("en el tope: %d filas, quiero la cabecera y %d", len(rows), intakes.MaxExportIntakes)
	}

	store.Add(tenantA, intakes.Intake{ID: "a-de-mas", Status: intakes.StatusOpen, CreatedAt: created})
	rec = h.Call(cara, h.With(tenantA, reportPerm), http.MethodGet, exportTarget+"?format=xlsx", "")
	wantCode(t, "una más que el tope", rec, http.StatusUnprocessableEntity)
	wantErrorBody(t, "una más que el tope", rec, reportTooLarge)
}

// TestExport_NilNowUsesTheRealClock: sin reloj cableado el export funciona igual y fecha el
// nombre con el del sistema (aquí solo se afirma la forma).
func TestExport_NilNowUsesTheRealClock(t *testing.T) {
	d := reportDeps(&reportServiceSpy{})
	d.Now = nil
	rec := reportGet(t, d, exportTarget)
	wantCode(t, "sin reloj", rec, http.StatusOK)
	pattern := regexp.MustCompile(`^attachment; filename="intakes-\d{8}-\d{6}\.csv"$`)
	if got := rec.Header().Get("Content-Disposition"); !pattern.MatchString(got) {
		t.Errorf("Content-Disposition = %q, quiero la forma %s", got, pattern)
	}
}

// TestExport_FilenameFollowsTheInjectedClock: el nombre sale del reloj de la cara, llamada a
// llamada, y siempre en UTC.
func TestExport_FilenameFollowsTheInjectedClock(t *testing.T) {
	now := time.Date(2026, 12, 31, 23, 59, 59, 999, time.FixedZone("+02", 2*3600))
	d := reportDeps(&reportServiceSpy{})
	d.Now = func() time.Time { return now }
	h := apipublicahelpertest.New(t)
	cara := reportCara(h.Common(), d)
	for _, want := range []string{"intakes-20261231-215959.csv", "intakes-20270101-000000.csv"} {
		rec := h.Call(cara, h.With(tenantA, reportPerm), http.MethodGet, exportTarget, "")
		if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="`+want+`"` {
			t.Errorf("Content-Disposition = %q, quiero el fichero %s", got, want)
		}
		now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	}
}
