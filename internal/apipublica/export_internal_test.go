package apipublica

// export_internal_test.go — los auxiliares NO exportados de export.go que llevan una regla que
// el contrato (export_test.go) no alcanza con claridad desde fuera (05 E-4, P6): el ancho de las
// filas, la guarda contra la fila corta —inalcanzable por HTTP, porque el generador siempre da
// el ancho correcto— y el escape de fórmulas carácter a carácter.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// TestExportRows_EveryRowIsAsWideAsTheHeader: el generador produce filas del ancho de la
// cabecera con y sin líneas, y el relleno de la solicitud sin líneas son celdas nil (vacías, no
// cero ni cadena).
func TestExportRows_EveryRowIsAsWideAsTheHeader(t *testing.T) {
	if len(exportColumns) != 13 || len(exportHeadColumns) != 7 || len(exportLineColumns) != 6 {
		t.Fatalf("columnas = %d (%d + %d); el contrato son 13 (7 de solicitud + 6 de línea)",
			len(exportColumns), len(exportHeadColumns), len(exportLineColumns))
	}
	rows := exportRows([]intakes.Detail{
		{Intake: intakes.Intake{ID: "sin-lineas", CreatedAt: time.Unix(0, 0)}},
		{Intake: intakes.Intake{ID: "con-lineas"}, Items: []intakes.Item{{SKU: "a", Qty: 2, UnitPrice: 1.5}, {SKU: "b"}}},
	})
	if len(rows) != 3 {
		t.Fatalf("filas = %d, quiero 3 (una de la solicitud sin líneas y dos de la otra)", len(rows))
	}
	for i, row := range rows {
		if len(row) != len(exportColumns) {
			t.Errorf("fila %d: %d celdas, quiero %d", i, len(row), len(exportColumns))
		}
	}
	for i, cell := range rows[0][len(exportHeadColumns):] {
		if cell != nil {
			t.Errorf("solicitud sin líneas: la celda de línea %d es %#v, quiero nil (vacía)", i, cell)
		}
	}
	if got := rows[1][12]; got != 3.0 {
		t.Errorf("line_total = %#v, quiero 3 (qty × unit_price, como número)", got)
	}
	// Las dos filas de una solicitud no comparten memoria: escribir en una no pisa la otra.
	rows[1][0] = "pisada"
	if rows[2][0] != "con-lineas" {
		t.Errorf("las filas de una misma solicitud comparten la cabecera: %v", rows[2][0])
	}
}

// TestExportWriters_RejectAShortRow: una fila con menos celdas que columnas NO se escribe: el
// escritor de CSV reutiliza su registro y la fila corta saldría con celdas HEREDADAS de la
// anterior —un dato de otro pedido en la última columna—. Vale más el error.
func TestExportWriters_RejectAShortRow(t *testing.T) {
	columns := []string{"a", "b", "c"}
	rows := [][]any{{"1", "2", "3"}, {"4", "5"}}
	const want = "export: la fila 2 trae 2 celdas para 3 columnas"

	var csvOut bytes.Buffer
	if err := exportWriteCSV(&csvOut, columns, rows); err == nil || err.Error() != want {
		t.Errorf("CSV: error = %v, quiero %q", err, want)
	}
	if csvOut.Len() != 0 {
		t.Errorf("CSV: se escribieron %d bytes antes de rechazar la fila corta, quiero 0", csvOut.Len())
	}
	var xlsxOut bytes.Buffer
	if err := exportWriteXLSX(&xlsxOut, "hoja", columns, rows); err == nil || err.Error() != want {
		t.Errorf("XLSX: error = %v, quiero %q", err, want)
	}
	if xlsxOut.Len() != 0 {
		t.Errorf("XLSX: se escribieron %d bytes antes de rechazar la fila corta, quiero 0", xlsxOut.Len())
	}
	// Una fila LARGA es el mismo desajuste.
	if err := exportWriteCSV(&csvOut, columns, [][]any{{"1", "2", "3", "4"}}); err == nil {
		t.Error("CSV: una fila con celdas de más se aceptó")
	}
}

// TestExportWriteCSV_ReceivesItsColumns: el escritor no lee ninguna global: escribe las columnas
// y filas que le dan, con BOM y CRLF.
func TestExportWriteCSV_ReceivesItsColumns(t *testing.T) {
	var out bytes.Buffer
	if err := exportWriteCSV(&out, []string{"x", "y"}, [][]any{{"=1", 2}, {nil, -0.5}}); err != nil {
		t.Fatalf("exportWriteCSV: %v", err)
	}
	if got, want := out.String(), "\xef\xbb\xbfx,y\r\n'=1,2\r\n,-0.5\r\n"; got != want {
		t.Errorf("CSV = %q, quiero %q", got, want)
	}
}

// TestExportEscapeFormula: los seis caracteres que disparan una fórmula, y solo en la primera
// posición.
func TestExportEscapeFormula(t *testing.T) {
	for _, first := range []string{"=", "+", "-", "@", "\t", "\r"} {
		for _, rest := range []string{"", "1+1", "ñ"} {
			if got, want := exportEscapeFormula(first+rest), "'"+first+rest; got != want {
				t.Errorf("exportEscapeFormula(%q) = %q, quiero %q", first+rest, got, want)
			}
		}
	}
	for _, safe := range []string{"", " ", " =1", "a=1", "'=1", "\n=1", "0", "ñ=", "＝1", "\x00=1", strings.Repeat("a", 4096) + "="} {
		if got := exportEscapeFormula(safe); got != safe {
			t.Errorf("exportEscapeFormula(%q) = %q; no empieza por un disparador, quiero intacto", safe, got)
		}
	}
}

// TestExportCSVCell: cada tipo de celda. El texto pasa por el escape; los números, nunca.
func TestExportCSVCell(t *testing.T) {
	type textual string
	cases := []struct {
		name string
		cell any
		want string
	}{
		{"nil_is_empty", nil, ""},
		{"text", "hola", "hola"},
		{"text_formula", "=1", "'=1"},
		{"int", 42, "42"},
		{"negative_int_is_not_escaped", -42, "-42"},
		{"float_shortest", 0.1, "0.1"},
		{"float_integer_has_no_decimals", 18000.0, "18000"},
		{"negative_float_is_not_escaped", -0.5, "-0.5"},
		{"float_without_exponent", 1e-7, "0.0000001"},
		// Cualquier otro tipo se trata como TEXTO: formateado y escapado.
		{"other_type_is_text", textual("@x"), "'@x"},
		{"other_number_type_is_text", int64(-7), "'-7"},
	}
	for _, tc := range cases {
		if got := exportCSVCell(tc.cell); got != tc.want {
			t.Errorf("%s: exportCSVCell(%#v) = %q, quiero %q", tc.name, tc.cell, got, tc.want)
		}
	}
}

// TestExportFilenameAndContentType: el nombre lleva el instante en UTC y la extensión del
// formato; el MIME es el de cada formato.
func TestExportFilenameAndContentType(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.FixedZone("+05", 5*3600))
	if got, want := exportFilename(exportFormatXLSX, at), "intakes-20260228-190000.xlsx"; got != want {
		t.Errorf("exportFilename = %q, quiero %q", got, want)
	}
	if got, want := exportFilename(exportFormatCSV, at), "intakes-20260228-190000.csv"; got != want {
		t.Errorf("exportFilename = %q, quiero %q", got, want)
	}
	if got := exportContentType(exportFormatCSV); got != "text/csv; charset=utf-8" {
		t.Errorf("exportContentType(csv) = %q", got)
	}
	if got := exportContentType(exportFormatXLSX); got != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("exportContentType(xlsx) = %q", got)
	}
}
