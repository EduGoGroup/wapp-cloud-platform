package publicapi_test

import (
	"bytes"
	"encoding/csv"
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

	// La cabecera de la solicitud se repite en cada fila, `customer_note` incluida
	// (D-041.15): es desnormalizada como `intake_total`, y quien filtre por líneas
	// en la hoja sigue viendo a qué pedido pertenece cada una y qué pidió el cliente.
	const cabecera = "11111111-1111-1111-1111-111111111111,2026-08-01T12:00:00Z,confirmed,sess-a," +
		"9f1c0a7e-0000-4000-8000-000000000abc,18000,dejar en portería"
	quiero := csvGolden(
		csvHeader,
		cabecera+",torta-v1,Torta 10-12 porciones,sin sal,1,18000,18000",
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

// TestIntakesExport_CSV_Personalización es el SEGUNDO de los cinco caminos de
// T4.1b (D-041.17): el «sin sal» viaja en el CSV como columna propia, entre `label`
// y `qty`. El export es el camino por el que una cocina que no usa la consola
// recibe la comanda: si la personalización no está aquí, no llega (INV-12).
//
// La posición se afirma por ÍNDICE de la cabecera, no por "aparece en el texto":
// una cadena suelta también estaría si la columna se hubiera colado en otro sitio,
// y en un CSV el sitio es el significado.
func TestIntakesExport_CSV_Personalización(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?status=closed&session=sess-a&from=2026-08-01&to=2026-08-02", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// El BOM sigue delante (Excel en español lo necesita para las tildes) y no se
	// coló ni un byte antes de él.
	if !strings.HasPrefix(body, "\xef\xbb\xbf") {
		t.Fatalf("el CSV perdió el BOM: %q", body[:min(8, len(body))])
	}

	filas := camposCSV(t, body)
	iCustom := slices.Index(filas[0], "customization")
	switch {
	case iCustom < 0:
		t.Fatalf("la cabecera no trae `customization`: %v", filas[0])
	case filas[0][iCustom-1] != "label" || filas[0][iCustom+1] != "qty":
		t.Fatalf("`customization` está entre %q y %q; la quiero entre `label` y `qty`",
			filas[0][iCustom-1], filas[0][iCustom+1])
	}

	if got := filas[1][iCustom]; got != "sin sal" {
		t.Fatalf("la línea personalizada exporta customization=%q, quiero %q", got, "sin sal")
	}
	if got := filas[2][iCustom]; got != "" {
		t.Fatalf("la línea SIN personalización exporta customization=%q, quiero vacío", got)
	}
	// INV-13: el dinero de esa línea es el mismo que sin personalizar.
	iTotal := slices.Index(filas[0], "line_total")
	if filas[1][iTotal] != "18000" {
		t.Fatalf("line_total=%q; personalizar no puede mover el dinero", filas[1][iTotal])
	}
}

// TestIntakesExport_CSV_EscapaLaPersonalización: la personalización la escribe el
// CLIENTE FINAL por WhatsApp, así que es —con `customer_note`— la celda más
// expuesta del archivo. Una que empiece por `-` la ejecutaría Excel al abrirlo, y
// por eso sale prefijada con `'` como cualquier otro texto (D-041.15).
func TestIntakesExport_CSV_EscapaLaPersonalización(t *testing.T) {
	api := newAPI(exportDeps(seedInyección()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ",'-sin cebolla,") {
		t.Fatalf("la personalización no salió escapada:\n%s", rec.Body.String())
	}
}

// TestIntakesExport_CSV_CabeceraCanónica fija el CONTRATO completo de columnas en
// un solo sitio y en su orden exacto. Quien añada una columna (T4.1c pone
// `customer_note` tras `intake_total`; T4.6 toca estados) rompe ESTE test, que es
// barato de leer y de actualizar, en vez de romper en silencio el archivo que el
// operador ya abre con sus filtros y sus macros montados encima.
//
// Y afirma lo que ninguna cabecera enseña: que TODAS las filas miden lo mismo que
// ella —incluida la de una solicitud sin líneas—. Una fila corta no sale corta del
// escritor de CSV: sale con celdas heredadas de la fila anterior, y el archivo se
// abre tan campante con el importe de otro pedido en la última columna.
func TestIntakesExport_CSV_CabeceraCanónica(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	// Sin filtro: entran las solicitudes CON líneas y las que no tienen ninguna,
	// que es la combinación que destapa el desajuste de anchura.
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	filas := camposCSV(t, rec.Body.String())
	if got := strings.Join(filas[0], ","); got != csvHeader {
		t.Fatalf("cabecera del export:\n got=%q\nquiero=%q", got, csvHeader)
	}
	for i, fila := range filas {
		if len(fila) != len(filas[0]) {
			t.Fatalf("la fila %d trae %d celdas y la cabecera %d: %q",
				i, len(fila), len(filas[0]), fila)
		}
	}

	// La solicitud A2 no tiene líneas: sus seis celdas de línea salen VACÍAS, no
	// con lo que hubiera en la fila anterior.
	for _, fila := range filas[1:] {
		if fila[0] != intakeA2 {
			continue
		}
		if vacías := fila[len(fila)-6:]; slices.ContainsFunc(vacías, func(c string) bool { return c != "" }) {
			t.Fatalf("la solicitud sin líneas arrastró celdas de la fila anterior: %q", fila)
		}
		return
	}
	t.Fatalf("el export no trae la solicitud sin líneas %s; el test perdería sentido", intakeA2)
}

// camposCSV parsea el archivo servido como lo haría una hoja de cálculo (sin el
// BOM) y devuelve sus filas. Parsear en vez de partir por comas no es lujo: una
// celda con coma o comillas viaja entrecomillada y un `strings.Split` la partiría
// en dos, dando por buena una columna corrida.
func camposCSV(t *testing.T, body string) [][]string {
	t.Helper()
	filas, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatalf("el CSV no se puede parsear: %v", err)
	}
	if len(filas) < 2 {
		t.Fatalf("el CSV solo trae %d filas; el test necesita cabecera y datos", len(filas))
	}
	return filas
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

// seedInyección siembra una solicitud cuyo sku, label y personalización parecen
// fórmulas. La personalización es la que de verdad importa aquí: sku y label salen
// del catálogo del tenant, pero `customization` la escribe el CLIENTE FINAL desde
// WhatsApp, así que es la celda que un desconocido controla.
func seedInyección() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID: intakeA1, ContactID: contactoOpaco, SessionID: "sess-a",
		Status: intakes.StatusConfirmed, Total: 100, CreatedAt: día(1), UpdatedAt: día(1),
		// La nota del PEDIDO la escribe el mismo desconocido que la personalización,
		// y por eso va aquí con pinta de fórmula: son las DOS únicas celdas del
		// archivo que teclea el cliente final.
		CustomerNote: "=SUM(A1:A9)",
	}, intakes.Item{SKU: "=1+1", Label: "@sospechoso", Customization: "-sin cebolla",
		Qty: 1, UnitPrice: 100})
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
			"9f1c0a7e-0000-4000-8000-000000000abc", "18000", "dejar en portería",
			"torta-v1", "Torta 10-12 porciones", "sin sal", "1", "18000", "18000"},
		{"11111111-1111-1111-1111-111111111111", "2026-08-01T12:00:00Z", "confirmed", "sess-a",
			"9f1c0a7e-0000-4000-8000-000000000abc", "18000", "dejar en portería",
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

// TestIntakesExport_XLSX_Personalización es el TERCERO de los cinco caminos de
// T4.1b (D-041.17): la misma columna, en el mismo sitio, en el libro de Excel. El
// XLSX es su propio camino y no un derivado del CSV —lo escribe otro serializador,
// celda a celda—, así que llevarla en uno no prueba nada del otro.
//
// Se REABRE el libro (lo que hace Excel) en vez de mirar los bytes: es la única
// forma de saber que la celda quedó donde se cree.
func TestIntakesExport_XLSX_Personalización(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?format=xlsx&status=closed&session=sess-a&from=2026-08-01&to=2026-08-02", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	rows := leerXLSX(t, rec.Body.Bytes())
	iCustom := slices.Index(rows[0], "customization")
	if iCustom < 0 || rows[0][iCustom-1] != "label" || rows[0][iCustom+1] != "qty" {
		t.Fatalf("`customization` no está entre `label` y `qty` en la cabecera: %v", rows[0])
	}
	if got := rows[1][iCustom]; got != "sin sal" {
		t.Fatalf("la línea personalizada del XLSX trae %q, quiero %q", got, "sin sal")
	}
	if got := rows[2][iCustom]; got != "" {
		t.Fatalf("la línea sin personalización del XLSX trae %q, quiero vacío", got)
	}
	// INV-13: la hoja sigue sumando lo mismo.
	iTotal := slices.Index(rows[0], "line_total")
	if rows[1][iTotal] != "18000" {
		t.Fatalf("line_total=%q; personalizar no puede mover el dinero", rows[1][iTotal])
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

// TestIntakesExport_CSV_NotaDelPedido es el SEGUNDO de los cinco caminos de T4.1c
// (D-041.19, REQ-33f): la indicación del PEDIDO viaja en el CSV como columna
// propia tras `intake_total`, y DESNORMALIZADA —repetida en cada línea de la
// solicitud, como el total—, porque una hoja de cálculo no tiene cabeceras: tiene
// filas, y quien filtre las líneas de un día tiene que seguir viendo la indicación
// que acompaña a cada una.
//
// La posición se afirma por ÍNDICE de la cabecera y no por "aparece en el texto":
// en un CSV el sitio ES el significado, y una columna corrida rompe cualquier
// plantilla montada encima.
func TestIntakesExport_CSV_NotaDelPedido(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?status=closed&session=sess-a&from=2026-08-01&to=2026-08-02", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	filas := camposCSV(t, rec.Body.String())
	iNota := slices.Index(filas[0], "customer_note")
	switch {
	case iNota < 0:
		t.Fatalf("la cabecera no trae `customer_note`: %v", filas[0])
	case filas[0][iNota-1] != "intake_total":
		t.Fatalf("`customer_note` va TRAS `intake_total`, no tras %q", filas[0][iNota-1])
	}
	for i, fila := range filas[1:] {
		if fila[iNota] != "dejar en portería" {
			t.Fatalf("fila %d: customer_note=%q; se repite en TODAS las líneas de la solicitud",
				i+1, fila[iNota])
		}
	}
	// INV-13: el dinero de la línea es el mismo con indicación o sin ella.
	iTotal := slices.Index(filas[0], "line_total")
	if filas[1][iTotal] != "18000" {
		t.Fatalf("line_total=%q; la indicación del pedido movió el dinero", filas[1][iTotal])
	}
}

// TestIntakesExport_CSV_EscapaLaNotaDelPedido: `customer_note` y `customization`
// son las DOS únicas celdas del archivo que teclea el cliente final, y Excel
// ejecuta lo que parece fórmula. Las dos salen prefijadas con apóstrofo (D-041.15).
func TestIntakesExport_CSV_EscapaLaNotaDelPedido(t *testing.T) {
	api := newAPI(exportDeps(seedInyección()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	filas := camposCSV(t, rec.Body.String())
	iNota := slices.Index(filas[0], "customer_note")
	if got := filas[1][iNota]; got != "\u0027=SUM(A1:A9)" {
		t.Fatalf("la nota del pedido no salió escapada: %q", got)
	}
}

// TestIntakesExport_XLSX_NotaDelPedido es el TERCERO de los cinco caminos de
// T4.1c: la misma columna, en el mismo sitio, en el libro de Excel. El XLSX lo
// escribe otro serializador celda a celda, así que llevarla en el CSV no prueba
// nada de él.
func TestIntakesExport_XLSX_NotaDelPedido(t *testing.T) {
	api := newAPI(exportDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?format=xlsx&status=closed&session=sess-a&from=2026-08-01&to=2026-08-02", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	rows := leerXLSX(t, rec.Body.Bytes())
	iNota := slices.Index(rows[0], "customer_note")
	if iNota < 0 || rows[0][iNota-1] != "intake_total" {
		t.Fatalf("`customer_note` no está tras `intake_total` en la cabecera: %v", rows[0])
	}
	for i, fila := range rows[1:] {
		if fila[iNota] != "dejar en portería" {
			t.Fatalf("fila %d del XLSX: customer_note=%q", i+1, fila[iNota])
		}
	}
}
