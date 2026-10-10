//go:build pendiente

package apipublica_test

// catalogtabular_files_test.go — trozo de catalogtabular_test.go (05 E-13): la LECTURA del
// archivo subido a I15 (MountCatalogTabular): el formato reconocido por el contenido, el libro
// de Excel, las tolerancias del CSV, los fallos de lectura y de contenido, y los dos techos de
// bytes. Los auxiliares (catalogTabular…) viven en catalogtabular_test.go.

import (
	"bytes"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

const (
	catalogTabularMsgNotExcel = "no se pudo abrir el archivo de Excel: comprueba que sea el .xlsx que descargaste de la plantilla."
	catalogTabularMsgNoSheet  = "el libro no tiene ninguna hoja llamada «catalogo»: renómbrala así o parte de la plantilla que se descarga desde aquí."
)

// catalogTabularWorkbook arma un libro con las hojas dadas, en ese orden; la hoja data lleva
// rows —el precio (columna F) como NÚMERO con formato de moneda— y las demás, una celda suelta.
func catalogTabularWorkbook(t *testing.T, sheets []string, data string, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("cerrando el libro de prueba: %v", err)
		}
	})
	fail := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("armando el libro de prueba (%s): %v", what, err)
		}
	}
	fail("primera hoja", f.SetSheetName(f.GetSheetName(0), sheets[0]))
	for _, name := range sheets[1:] {
		_, err := f.NewSheet(name)
		fail("hoja "+name, err)
	}
	currencyFormat := `"$"#,##0.00`
	currency, err := f.NewStyle(&excelize.Style{CustomNumFmt: &currencyFormat})
	fail("estilo de moneda", err)

	for _, name := range sheets {
		if name != data {
			fail("celda ajena", f.SetCellValue(name, "A1", "mis cuentas: 1500"))
			continue
		}
		for r, row := range rows {
			for c, cell := range row {
				ref, cerr := excelize.CoordinatesToCellName(c+1, r+1)
				fail("coordenada", cerr)
				price, perr := strconv.ParseFloat(cell, 64)
				if r == 0 || c != 5 || perr != nil {
					fail("celda "+ref, f.SetCellValue(name, ref, cell))
					continue
				}
				fail("precio "+ref, f.SetCellValue(name, ref, price))
				fail("formato "+ref, f.SetCellStyle(name, ref, ref, currency))
			}
		}
	}
	var buf bytes.Buffer
	fail("serializar", f.Write(&buf))
	return buf.Bytes()
}

// catalogTabularWantFixture exige que el archivo se leyó como la planilla de prueba: 200 y el
// mismo documento que leería el contrato tabular de sus filas.
func catalogTabularWantFixture(t *testing.T, what string, file []byte, filename string) {
	t.Helper()
	document, _ := catalogTabularDocument(t, catalogTabularRows())
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	rec := catalogTabularPost(t, rig, "", filename, file)
	wantCode(t, what, rec, http.StatusOK)
	wantExactBody(t, what, rec, `{"mode":"validate","ref":"catalogo","applied":false,"items":3,"diff":`+catalogImportDiffAllNew+`,"document":`+document+`}`)
}

// TestMountCatalogTabular_FormatIsRecognizedByContent: ni la extensión ni el nombre cuentan: un
// libro llamado .csv se lee como libro y un CSV llamado .xlsx como CSV.
func TestMountCatalogTabular_FormatIsRecognizedByContent(t *testing.T) {
	csvFile := catalogTabularCSV(t, ',', catalogTabularRows())
	book := catalogTabularWorkbook(t, []string{"catalogo"}, "catalogo", catalogTabularRows())
	if !bytes.HasPrefix(book, []byte("PK\x03\x04")) {
		t.Fatal("el libro de prueba no empieza por la firma de un ZIP")
	}
	for name, tc := range map[string]struct {
		filename string
		file     []byte
	}{
		"csv_named_csv":       {"catalogo.csv", csvFile},
		"csv_named_xlsx":      {"catalogo.xlsx", csvFile},
		"csv_without_name":    {"x", csvFile},
		"workbook_named_xlsx": {"catalogo.xlsx", book},
		"workbook_named_csv":  {"catalogo.csv", book},
	} {
		t.Run(name, func(t *testing.T) { catalogTabularWantFixture(t, name, tc.file, tc.filename) })
	}
}

// TestMountCatalogTabular_WorkbookSheetIsFoundByName: la hoja se busca por NOMBRE —sin distinguir
// mayúsculas ni los espacios de alrededor—, esté donde esté, y el precio se lee CRUDO aunque la
// celda lleve formato de moneda.
func TestMountCatalogTabular_WorkbookSheetIsFoundByName(t *testing.T) {
	for name, tc := range map[string]struct {
		sheets []string
		data   string
	}{
		"only_sheet":             {[]string{"catalogo"}, "catalogo"},
		"other_sheets_first":     {[]string{"mis cuentas", "notas", "catalogo"}, "catalogo"},
		"capitalized":            {[]string{"mis cuentas", "Catalogo"}, "Catalogo"},
		"upper_case":             {[]string{"CATALOGO", "notas"}, "CATALOGO"},
		"spaces_around_the_name": {[]string{"notas", " catalogo "}, " catalogo "},
	} {
		t.Run(name, func(t *testing.T) {
			catalogTabularWantFixture(t, name, catalogTabularWorkbook(t, tc.sheets, tc.data, catalogTabularRows()), "catalogo.xlsx")
		})
	}
}

// TestMountCatalogTabular_CSVTransportTolerances: el BOM, el separador de Excel en español, las
// filas cortas y una comilla suelta son del transporte y no rechazan una planilla que está bien.
func TestMountCatalogTabular_CSVTransportTolerances(t *testing.T) {
	rows := catalogTabularRows()
	comma := string(catalogTabularCSV(t, ',', rows))
	header := strings.Join(catalogimport.TabularColumns(), ",")
	short := header + "\n1|Bebidas,,1,CAFE,Café,2.9\n1|Bebidas,,2,TE,Té,2\n1|Bebidas,,3,AGUA,Agua,1.5\n"

	for name, file := range map[string]string{
		"comma":                    comma,
		"utf8_bom":                 "\xef\xbb\xbf" + comma,
		"semicolon":                string(catalogTabularCSV(t, ';', rows)),
		"tab":                      string(catalogTabularCSV(t, '\t', rows)),
		"bom_and_semicolon":        "\xef\xbb\xbf" + string(catalogTabularCSV(t, ';', rows)),
		"crlf_line_endings":        strings.ReplaceAll(comma, "\n", "\r\n"),
		"rows_shorter_than_header": short,
	} {
		t.Run(name, func(t *testing.T) { catalogTabularWantFixture(t, name, []byte(file), "catalogo.csv") })
	}

	// Una comilla suelta en un campo sin comillar no aborta la lectura del archivo: se lee tal
	// cual y el artículo llega con ella.
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	rec := catalogTabularPost(t, rig, "", "catalogo.csv", []byte(strings.Replace(short, "Agua", `Agua 5" fría`, 1)))
	wantCode(t, "comilla suelta", rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `Agua 5\" fría`) {
		t.Errorf("comilla suelta: el artículo no llegó con su comilla: %s", rec.Body.String())
	}
}

// TestMountCatalogTabular_ReadFailures: lo que no deja leer el archivo sale con la forma del
// import JSON y UNA entrada sin fila, con su motivo literal; no toca el almacén.
func TestMountCatalogTabular_ReadFailures(t *testing.T) {
	csvFile := catalogTabularCSV(t, ',', catalogTabularRows())
	upload := func(field string, file []byte) func(*testing.T, catalogImportRig) (int, string) {
		return func(t *testing.T, rig catalogImportRig) (int, string) {
			rec := catalogTabularUpload(t, rig, rig.h.With(tenantA, catalogImportPermWrite), "?mode=apply", field, "catalogo.xlsx", file)
			return rec.Code, rec.Body.String()
		}
	}
	cases := map[string]struct {
		send   func(*testing.T, catalogImportRig) (int, string)
		reason string
	}{
		"json_body_instead_of_a_form": {func(_ *testing.T, rig catalogImportRig) (int, string) {
			rec := rig.h.Call(rig.cara, rig.h.With(tenantA, catalogImportPermWrite), http.MethodPost, catalogTabularTarget+"?mode=apply", catalogImportDoc)
			return rec.Code, rec.Body.String()
		}, catalogTabularMsgNoFile},
		"empty_body": {func(_ *testing.T, rig catalogImportRig) (int, string) {
			rec := rig.h.Call(rig.cara, rig.h.With(tenantA, catalogImportPermWrite), http.MethodPost, catalogTabularTarget, "")
			return rec.Code, rec.Body.String()
		}, catalogTabularMsgNoFile},
		"field_with_another_name":   {upload("archivo", csvFile), catalogTabularMsgNoFile},
		"field_name_in_upper_case":  {upload("File", csvFile), catalogTabularMsgNoFile},
		"zip_signature_but_no_book": {upload(catalogTabularField, []byte("PK\x03\x04esto no es un libro")), catalogTabularMsgNotExcel},
		"workbook_without_the_sheet": {upload(catalogTabularField,
			catalogTabularWorkbook(t, []string{"mis cuentas", "catalogo 2"}, "catalogo 2", catalogTabularRows())), catalogTabularMsgNoSheet},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
			code, body := tc.send(t, rig)
			if code != http.StatusBadRequest || body != catalogTabularReadFailure(tc.reason) {
				t.Errorf("%s: %d %s\nquiero 400 %s", name, code, body, catalogTabularReadFailure(tc.reason))
			}
			catalogImportWantStoreCalls(t, name, rig.store, 0, 0)
			catalogImportWantOneAudit(t, name, rig.h, "failure", http.StatusBadRequest)
		})
	}
}

// TestMountCatalogTabular_ContentDefectsAreLocatedByRow: los defectos de la planilla son los del
// contrato tabular tal cual, ubicados por fila; no hay documento y un apply no escribe.
func TestMountCatalogTabular_ContentDefectsAreLocatedByRow(t *testing.T) {
	badPrice := catalogTabularRows()
	badPrice[2] = catalogTabularRow("1|Bebidas", "", "2", "TE", "Té", "$18.000")
	cases := map[string][][]string{
		"price_with_currency_symbol": badPrice,
		"no_rows_at_all":             nil,
		"header_only":                {catalogimport.TabularColumns()},
		"unknown_header":             {{"producto", "valor"}, {"Café", "2.9"}},
		"repeated_sku":               append(catalogTabularRows(), catalogTabularRow("1|Bebidas", "", "4", "CAFE", "Otro café", "3")),
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := catalogimport.ParseTabular(rows, catalogimport.DefaultLimits())
			if verr == nil {
				t.Fatalf("el caso %s valida; no sirve como planilla con defectos", name)
			}
			rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
			rig.store.repo.SetTenantContent(tenantA, catalogImportRef, []byte(catalogImportCurrent))
			rec := catalogTabularPost(t, rig, "?mode=apply", "catalogo.csv", catalogTabularCSV(t, ',', rows))
			wantCode(t, name, rec, http.StatusBadRequest)
			if got := catalogImportDefects(t, name, rec); !reflect.DeepEqual(got, verr.Errors) {
				t.Errorf("%s: defectos\n  %+v\nquiero los del contrato tabular, tal cual\n  %+v", name, got, verr.Errors)
			}
			catalogImportWantStoreCalls(t, name, rig.store, 0, 0)
			if got := rig.blob(t, tenantA, catalogImportRef); got != catalogImportCurrent {
				t.Errorf("%s: un apply con defectos escribió igualmente: %q", name, got)
			}
		})
	}

	// La fila es la que la hoja enseña en su margen: la cabecera es la 1.
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular)
	rec := catalogTabularPost(t, rig, "", "catalogo.csv", catalogTabularCSV(t, ',', badPrice))
	defects := catalogImportDefects(t, "fila", rec)
	if len(defects) != 1 || defects[0].Row != 3 || defects[0].Field != "precio" || defects[0].CategoryIndex != nil {
		t.Errorf("fila: defectos %+v; quiero uno, en la fila 3 y el campo precio, sin índices", defects)
	}
	if !strings.Contains(rec.Body.String(), `"row":3`) {
		t.Errorf("fila: el cuerpo no ubica el defecto con \"row\": %s", rec.Body.String())
	}
}

// TestMountCatalogTabular_TwoByteCeilings: el techo del archivo es el de tenant_content y el del
// cuerpo le suma 8 KiB de sobre; pasarse de cualquiera es 413 con la forma de validation_failed
// y la cifra del ARCHIVO.
func TestMountCatalogTabular_TwoByteCeilings(t *testing.T) {
	const limit = 200
	capped := func(d *apipublica.CatalogImportDeps) { d.ContentMaxBytes = limit }
	tooLarge := catalogTabularReadFailure("el archivo excede el tamaño máximo de 200 bytes")

	// Justo en el techo el archivo PASA los dos techos (y luego no valida, que es otra cosa).
	rig := catalogImportSetup(t, apipublica.MountCatalogTabular, capped)
	rec := catalogTabularPost(t, rig, "", "catalogo.csv", bytes.Repeat([]byte("a"), limit))
	wantCode(t, "justo en el techo", rec, http.StatusBadRequest)
	if strings.Contains(rec.Body.String(), "excede") {
		t.Errorf("justo en el techo: se rechazó por tamaño: %s", rec.Body.String())
	}

	for name, size := range map[string]int{
		"one_byte_over_the_file_ceiling": limit + 1,
		"over_the_file_under_the_body":   limit + 4<<10,
		"over_the_body_ceiling":          64 << 10,
	} {
		t.Run(name, func(t *testing.T) {
			rig := catalogImportSetup(t, apipublica.MountCatalogTabular, capped)
			rec := catalogTabularPost(t, rig, "?mode=apply", "catalogo.csv", bytes.Repeat([]byte("a"), size))
			wantCode(t, name, rec, http.StatusRequestEntityTooLarge)
			wantExactBody(t, name, rec, tooLarge)
			catalogImportWantStoreCalls(t, name, rig.store, 0, 0)
			catalogImportWantOneAudit(t, name, rig.h, "failure", http.StatusRequestEntityTooLarge)
		})
	}

	// Sin configurar, el techo es 1 MiB.
	rig = catalogImportSetup(t, apipublica.MountCatalogTabular)
	rec = catalogTabularPost(t, rig, "", "catalogo.csv", bytes.Repeat([]byte("a"), 1<<20+1))
	wantCode(t, "techo por defecto", rec, http.StatusRequestEntityTooLarge)
	wantExactBody(t, "techo por defecto", rec, catalogTabularReadFailure("el archivo excede el tamaño máximo de 1048576 bytes"))
}
