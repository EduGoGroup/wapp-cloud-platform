package publicapi

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Este archivo es de los POCOS tests internos del paquete (el resto vive en
// publicapi_test, contra la API servida). Está aquí porque lo que comprueba —que
// ninguna fila salga con menos celdas que columnas— no se puede provocar desde
// fuera: es una guarda contra el error del PRÓXIMO que añada una columna, y el
// único sitio donde se puede escribir una fila mal formada es dentro del paquete.

// TestExportRows_TodasLasFilasMidenLoMismo: el generador de filas produce
// exactamente una celda por columna, tenga la solicitud líneas o no.
func TestExportRows_TodasLasFilasMidenLoMismo(t *testing.T) {
	conLínea := intakes.Detail{
		Intake: intakes.Intake{ID: "con-líneas", Status: intakes.StatusConfirmed, Total: 100,
			CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC()},
		Items: []intakes.Item{{SKU: "s", Label: "l", Customization: "sin sal", Qty: 1, UnitPrice: 100}},
	}
	sinLíneas := intakes.Detail{
		Intake: intakes.Intake{ID: "sin-líneas", Status: intakes.StatusOpen,
			CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC()},
	}

	rows := exportRows([]intakes.Detail{conLínea, sinLíneas})
	if len(rows) != 2 {
		t.Fatalf("filas=%d, quiero 2", len(rows))
	}
	for i, row := range rows {
		if len(row) != len(exportColumns) {
			t.Fatalf("fila %d: %d celdas para %d columnas (%v)", i, len(row), len(exportColumns), row)
		}
	}
}

// TestWriteCSV_RechazaFilaCorta: una fila con menos celdas que columnas NO se
// escribe a medias — se rechaza. Sin esta guarda, el escritor reutiliza el buffer
// de registro y las celdas que faltan salen HEREDADAS de la fila anterior: el
// archivo se abre perfectamente con el importe de otro pedido en la última
// columna, y nadie lo nota.
//
// La prueba de que la guarda hace falta va incluida: se comprueba que la fila
// anterior tenía en esa posición un valor que se habría colado.
func TestWriteCSV_RechazaFilaCorta(t *testing.T) {
	columnas := []string{"a", "b", "c"}
	rows := [][]any{
		{"1", "2", "delator"},
		{"3", "4"}, // le falta la tercera celda
	}

	var buf bytes.Buffer
	err := writeCSV(&buf, columnas, rows)
	if err == nil {
		t.Fatalf("writeCSV aceptó una fila corta y escribió:\n%q", buf.String())
	}
	if !strings.Contains(err.Error(), "2 celdas para 3 columnas") {
		t.Fatalf("el error no dice qué fila ni cuánto le falta: %v", err)
	}
	if strings.Contains(buf.String(), "delator") {
		t.Fatalf("se escribió contenido antes de detectar el desajuste: %q", buf.String())
	}
}

// TestWriteXLSX_RechazaFilaCorta: el libro de Excel pasa por la misma guarda. Aquí
// una fila corta no hereda celdas —cada una se escribe por coordenada— pero sí
// produce una hoja con huecos que nadie pidió, y el generador que la produjo está
// igual de roto.
func TestWriteXLSX_RechazaFilaCorta(t *testing.T) {
	var buf bytes.Buffer
	if err := writeXLSX(&buf, "hoja", []string{"a", "b", "c"}, [][]any{{"1", "2"}}); err == nil {
		t.Fatal("writeXLSX aceptó una fila corta")
	}
}
