package publicapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// exportDeps arma unas Deps con la bandeja sembrada y LAS DOS features encendidas
// (cart_basic para ver la bandeja, intakes_export para sacarla). intakesDeps, la
// de los tests de T1.1, enciende solo la primera: esa diferencia es justamente lo
// que prueba TestIntakesExport_403_SinFeatureExport.
func exportDeps(store *intakes.MemoryStore) publicapi.Deps {
	fake := entitlements.NewFake()
	for _, tenant := range []string{tenantA, tenantB} {
		fake.Enable(tenant, entitlements.FeatureCartBasic)
		fake.Enable(tenant, entitlements.FeatureIntakesExport)
	}
	return publicapi.Deps{Intakes: intakes.NewService(store), Entitlements: fake}
}

// csvGolden arma el CSV esperado: BOM + líneas separadas por CRLF (RFC 4180), con
// su CRLF final.
func csvGolden(lines ...string) string {
	return "\xef\xbb\xbf" + strings.Join(lines, "\r\n") + "\r\n"
}

const csvHeader = "intake_id,created_at,status,session_id,contact_ref,intake_total," +
	"customer_note,sku,label,customization,qty,unit_price,line_total"

// ===================== GET /api/v1/intakes/export (CSV) =====================

// TestIntakesExport_CSV_Golden: la solicitud A1 tiene DOS líneas ⇒ el archivo trae
// la cabecera y DOS filas, con los datos de la solicitud REPETIDOS en cada una
// (desnormalizado, D-041.15). Se compara el contenido exacto, byte a byte: contar
// filas dejaría pasar un orden de columnas equivocado, un total mal formateado o un
// BOM ausente, que son precisamente los errores que arruinan el archivo al abrirlo.
func TestIntakesExport_CSV_Golden(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	// El filtro deja pasar SOLO A1 (la única con líneas en el fixture).
	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?status=closed&session=sess-a&from=2026-08-01&to=2026-08-02", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	const cabecera = "11111111-1111-1111-1111-111111111111,2026-08-01T12:00:00Z,confirmed,sess-a," +
		"9f1c0a7e-0000-4000-8000-000000000abc,18000,"
	quiero := csvGolden(
		csvHeader,
		cabecera+",torta-v1,Torta 10-12 porciones,,1,18000,18000",
		cabecera+",_shipping,Envío — Providencia,,1,3000,3000",
	)
	if got := rec.Body.String(); got != quiero {
		t.Fatalf("CSV distinto del golden.\n got=%q\nquiero=%q", got, quiero)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type=%q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="intakes-`) || !strings.HasSuffix(cd, `.csv"`) {
		t.Fatalf("Content-Disposition=%q; quiero una descarga con nombre .csv", cd)
	}
}

// TestIntakesExport_CSV_EstadoNormalizado: el `closed` legado del módulo cart sale
// del export como `confirmed`. El golden de arriba ya lo fija; esto lo afirma por
// separado para que, si alguien rompe la normalización, el fallo diga QUÉ se rompió
// y no solo "el archivo cambió".
func TestIntakesExport_CSV_EstadoNormalizado(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export?status=closed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), ",closed,") {
		t.Fatalf("el export publica la clave legada `closed`; debe salir normalizada:\n%s", rec.Body.String())
	}
	// El filtro `closed` alcanza también las filas escritas ya con la clave nueva
	// (StoredVariants): A1 (dos líneas) + A4 + A5 ⇒ cuatro filas.
	if n := strings.Count(rec.Body.String(), ",confirmed,"); n != 4 {
		t.Fatalf("filas con confirmed=%d, quiero 4 (dos líneas de A1 + A4 + A5)", n)
	}
}

// TestIntakesExport_CSV_SolicitudSinLíneas: una solicitud sin ninguna línea NO
// desaparece del archivo — sale con las columnas de línea vacías. Perder del export
// las solicitudes abiertas sería una pérdida silenciosa de datos.
func TestIntakesExport_CSV_SolicitudSinLíneas(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export?status=open", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	quiero := csvGolden(
		csvHeader,
		"22222222-2222-2222-2222-222222222222,2026-08-02T12:00:00Z,open,sess-a,"+
			"9f1c0a7e-0000-4000-8000-000000000abc,18000,,,,,,,",
	)
	if got := rec.Body.String(); got != quiero {
		t.Fatalf("CSV distinto del golden.\n got=%q\nquiero=%q", got, quiero)
	}
}

// TestIntakesExport_CSV_EscapaFórmulas: una celda de texto que empieza por `=`,
// `+`, `-` o `@` se prefija con `'` (D-041.15). Sin eso, un `label` que venga de un
// catálogo importado se ejecuta al abrir el archivo.
func TestIntakesExport_CSV_EscapaFórmulas(t *testing.T) {
	api := newAPI(exportDeps(seedInyección()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, celda := range []string{",'=1+1,", ",'@sospechoso,"} {
		if !strings.Contains(body, celda) {
			t.Fatalf("no encuentro la celda escapada %q en:\n%s", celda, body)
		}
	}
}

// seedInyección siembra una solicitud cuyo sku y label parecen fórmulas.
func seedInyección() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID: intakeA1, ContactID: contactoOpaco, SessionID: "sess-a",
		Status: intakes.StatusConfirmed, Total: 100, CreatedAt: día(1), UpdatedAt: día(1),
	}, intakes.Item{SKU: "=1+1", Label: "@sospechoso", Qty: 1, UnitPrice: 100})
	return st
}

// ===================== GET /api/v1/intakes/export (XLSX) ====================

// TestIntakesExport_XLSX_SeReabreConTildes: no basta con que el archivo se genere.
// Se REABRE con excelize (lo mismo que hace Excel/LibreOffice al abrirlo) y se
// comprueba que las tildes, la ñ y la raya sobrevivieron al viaje.
func TestIntakesExport_XLSX_SeReabreConTildes(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?format=xlsx&status=closed&session=sess-a&from=2026-08-01&to=2026-08-02", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("Content-Type=%q", ct)
	}

	rows := leerXLSX(t, rec.Body.Bytes())
	if len(rows) != 3 {
		t.Fatalf("filas=%d, quiero 3 (cabecera + dos líneas); rows=%v", len(rows), rows)
	}
	if got := strings.Join(rows[0], ","); got != csvHeader {
		t.Fatalf("cabecera del XLSX=%q", got)
	}
	quiero := [][]string{
		{"11111111-1111-1111-1111-111111111111", "2026-08-01T12:00:00Z", "confirmed", "sess-a",
			"9f1c0a7e-0000-4000-8000-000000000abc", "18000", "",
			"torta-v1", "Torta 10-12 porciones", "", "1", "18000", "18000"},
		{"11111111-1111-1111-1111-111111111111", "2026-08-01T12:00:00Z", "confirmed", "sess-a",
			"9f1c0a7e-0000-4000-8000-000000000abc", "18000", "",
			"_shipping", "Envío — Providencia", "", "1", "3000", "3000"},
	}
	for i, fila := range quiero {
		if !slices.Equal(rows[i+1], fila) {
			t.Fatalf("fila %d del XLSX = %q\nquiero                = %q", i+1, rows[i+1], fila)
		}
	}
	// La comprobación explícita de la que va este test: el texto acentuado llega
	// intacto tras serializar el libro, mandarlo por HTTP y reabrirlo.
	if rows[2][8] != "Envío — Providencia" {
		t.Fatalf("la etiqueta acentuada llegó como %q", rows[2][8])
	}
}

// TestIntakesExport_XLSX_NoPrefijaFórmulas: en XLSX la defensa NO es el apóstrofo
// —que quedaría como un carácter más del dato, a la vista del usuario— sino el TIPO
// de la celda: se escribe como cadena, no como fórmula, y Excel no la evalúa.
func TestIntakesExport_XLSX_NoPrefijaFórmulas(t *testing.T) {
	api := newAPI(exportDeps(seedInyección()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export?format=xlsx", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	rows := leerXLSX(t, rec.Body.Bytes())
	if len(rows) != 2 {
		t.Fatalf("filas=%d, quiero 2; rows=%v", len(rows), rows)
	}
	if rows[1][7] != "=1+1" || rows[1][8] != "@sospechoso" {
		t.Fatalf("celdas=%q/%q; el dato debe llegar íntegro (sin apóstrofo) y como texto",
			rows[1][7], rows[1][8])
	}
	// Y sigue sin ser una fórmula: la celda no tiene <f>, así que GetCellFormula
	// devuelve la cadena vacía.
	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("reabrir el libro: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Logf("cerrando el libro: %v", cerr)
		}
	}()
	formula, err := f.GetCellFormula("solicitudes", "H2")
	if err != nil {
		t.Fatalf("GetCellFormula: %v", err)
	}
	if formula != "" {
		t.Fatalf("la celda se guardó como FÓRMULA (%q): Excel la ejecutaría", formula)
	}
}

// leerXLSX reabre el libro que devolvió la API y entrega sus filas.
func leerXLSX(t *testing.T, body []byte) [][]string {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("el XLSX no se puede reabrir: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Logf("cerrando el libro: %v", cerr)
		}
	}()

	if hojas := f.GetSheetList(); !slices.Equal(hojas, []string{"solicitudes"}) {
		t.Fatalf("hojas=%v, quiero exactamente [solicitudes]", hojas)
	}
	rows, err := f.GetRows("solicitudes")
	if err != nil {
		t.Fatalf("leer filas: %v", err)
	}
	return rows
}

// ===================== Gates, formatos y filtros del export =================

// TestIntakesExport_403_SinFeatureExport es la prueba de que las dos features están
// separadas DE VERDAD: el mismo tenant, con cart_basic encendida (su bandeja
// responde 200), recibe 403 en el export porque le falta intakes_export.
func TestIntakesExport_403_SinFeatureExport(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys()) // solo cart_basic

	if rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", ""); rec.Code != http.StatusOK {
		t.Fatalf("la bandeja da %d; el tenant SÍ tiene cart_basic y el test perdería sentido", rec.Code)
	}

	for _, ruta := range []string{"/api/v1/intakes/export", "/api/v1/intakes/summary.json"} {
		rec := call(api, keyAIntakes, http.MethodGet, ruta, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: code=%d, quiero 403; body=%s", ruta, rec.Code, rec.Body.String())
		}
		var body struct {
			Error   string `json:"error"`
			Feature string `json:"feature"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: unmarshal del 403: %v; body=%s", ruta, err, rec.Body.String())
		}
		if body.Error != "feature_not_enabled" || body.Feature != "intakes_export" {
			t.Fatalf("%s: cuerpo del 403 = %+v; quiero la feature intakes_export", ruta, body)
		}
	}
}

// TestIntakesExport_403_SinScope: la feature no sustituye al grant. Con las dos
// features encendidas pero sin intakes.read, la cadena corta antes.
func TestIntakesExport_403_SinScope(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyARead, http.MethodGet, "/api/v1/intakes/export", "") // grant flows.read
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakesExport_401_SinToken: sin identidad no hay tenant del que sacar nada.
func TestIntakesExport_401_SinToken(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, "", http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakesExport_400_FormatoDesconocido: un formato que no existe se rechaza en
// vez de servir un CSV que nadie pidió.
func TestIntakesExport_400_FormatoDesconocido(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export?format=pdf", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakesExport_400_FiltroInválido: el export valida los filtros con el MISMO
// parser que la lista, así que una fecha mal escrita se rechaza igual.
func TestIntakesExport_400_FiltroInválido(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	for _, q := range []string{"?from=ayer", "?status=inventado"} {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}
}

// TestIntakesExport_Aislamiento: el tenant B exporta LO SUYO. El export toma el
// tenant del token (INV-8), nunca de la query.
func TestIntakesExport_Aislamiento(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyBIntakes, http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, intakeB1) {
		t.Fatalf("el export del tenant B no trae su propia solicitud:\n%s", body)
	}
	for _, id := range []string{intakeA1, intakeA2, intakeA3, intakeA4, intakeA5} {
		if strings.Contains(body, id) {
			t.Fatalf("el export del tenant B trae la solicitud %s del tenant A:\n%s", id, body)
		}
	}
}

// TestIntakesExport_MismosFiltrosQueLaLista recorre varios filtros y exige que el
// export contenga EXACTAMENTE las solicitudes que la lista devuelve para el mismo
// filtro. Es la garantía de que el archivo no miente respecto de la pantalla.
func TestIntakesExport_MismosFiltrosQueLaLista(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	for _, q := range []string{
		"", "?status=confirmed", "?session=sess-b", "?from=2026-08-03",
		"?from=2026-08-01&to=2026-08-02", "?status=confirmed&session=sess-a",
	} {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes"+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("lista %q: code=%d", q, rec.Code)
		}
		quiero := idsDe(decodeList(t, rec.Body.Bytes()))

		rec = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export"+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("export %q: code=%d; body=%s", q, rec.Code, rec.Body.String())
		}
		if got := idsDelCSV(rec.Body.String()); !slices.Equal(got, quiero) {
			t.Fatalf("filtro %q: export=%v, lista=%v", q, got, quiero)
		}
	}
}

// idsDelCSV extrae los intake_id del archivo en orden, sin repetir: una solicitud
// con dos líneas ocupa dos filas y aquí cuenta una vez.
func idsDelCSV(body string) []string {
	var out []string
	for i, line := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		if i == 0 {
			continue // cabecera
		}
		id, _, _ := strings.Cut(strings.TrimPrefix(line, "\xef\xbb\xbf"), ",")
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}
