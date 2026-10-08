package apipublica

// intakes_items_internal_test.go — lo que de intakes_items.go no se ve bien desde una ruta
// (05 E-4, P6: nace con el verde): la defensa del handler sin identidad y las tres reglas de
// sus auxiliares que desde fuera se diagnostican mal. El doble y el auxiliar de la defensa están
// en intakes_status_internal_test.go.

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

func TestIntakePutItemsHandler_IdentityDefense(t *testing.T) {
	svc := &intakeCountingService{}
	h := intakePutItemsHandler(svc, &intakeGateResolver{}, intakeFixedClock)
	intakeWantIdentityDefense(t, "G4", svc, h, http.MethodPut, `{"items":[]}`)
}

// TestIntakeEditModeOf: el único sitio donde se decide «esto es una corrección».
func TestIntakeEditModeOf(t *testing.T) {
	if intakeEditModeOf(false) != intakes.EditPlain || intakeEditModeOf(true) != intakes.EditAsCorrection {
		t.Errorf("modos = %d / %d; quiero EditPlain sin el campo y EditAsCorrection con él",
			intakeEditModeOf(false), intakeEditModeOf(true))
	}
}

// TestIntakeDecodeEditItems: el saneo es el del carrito —se LLAMA, no se copia— y el único
// defecto que produce es el largo, medido en runas y DESPUÉS de sanear. Una línea por cada una
// que llegó, en su orden, también las defectuosas (con el texto que no pasó, vacío).
func TestIntakeDecodeEditItems(t *testing.T) {
	limit := strings.Repeat("é", intakes.MaxNoteRunes)
	items, defects := intakeDecodeEditItems([]intakeEditItemDTO{
		{SKU: "\t A \n", Label: " uno\r\ndos ", Customization: "\u202esin\u00a0sal\u2028", Qty: -3, UnitPrice: -1},
		// 280 runas justas pasan, también si el relleno de alrededor las hacía más largas.
		{SKU: "B", Label: "   " + limit + "\n\n", Customization: limit, Qty: 1},
		{SKU: "C", Label: limit + "x", Customization: limit + "x", Qty: 1},
		{SKU: "", Label: "", Customization: "", Qty: 0},
	})
	want := []intakes.Item{
		{SKU: "A", Label: "uno dos", Customization: "sin sal", Qty: -3, UnitPrice: -1},
		{SKU: "B", Label: limit, Customization: limit, Qty: 1},
		{SKU: "C", Qty: 1},
		{},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("líneas\n  %+v\nquiero\n  %+v", items, want)
	}
	wantDefects := []intakes.LineDefect{
		{Index: 2, Field: "label", Message: "la etiqueta pasa del máximo de 280 caracteres"},
		{Index: 2, Field: "customization", Message: "la personalización pasa del máximo de 280 caracteres"},
	}
	if !reflect.DeepEqual(defects, wantDefects) {
		t.Errorf("defectos\n  %+v\nquiero\n  %+v", defects, wantDefects)
	}

	if items, defects := intakeDecodeEditItems(nil); items == nil || len(items) != 0 || defects != nil {
		t.Errorf("sin líneas: %v y %v; quiero una lista vacía NO nil y ningún defecto", items, defects)
	}
}

// TestIntakeMergeItemDefects: los defectos del dominio se suman a los del saneo y el conjunto se
// ordena por línea SIN barajar los de una misma línea. Si lo que el dominio rechaza es el NÚMERO
// de líneas, no hay defectos por línea que sumar: salen solo los del saneo.
func TestIntakeMergeItemDefects(t *testing.T) {
	long := intakes.LineDefect{Index: 1, Field: "customization", Message: "larga"}
	items := []intakes.Item{
		{SKU: "A", Label: "A", Qty: 0},
		{SKU: "B", Label: "B", Qty: 1},
		{SKU: "_envio", Label: "", Qty: 1, UnitPrice: -1},
	}
	got := intakeMergeItemDefects([]intakes.LineDefect{long}, items)
	fields := make([]string, 0, len(got))
	for _, d := range got {
		fields = append(fields, strconv.Itoa(d.Index)+":"+d.Field)
	}
	if want := "0:qty 1:customization 2:sku 2:label 2:unit_price"; strings.Join(fields, " ") != want {
		t.Errorf("defectos %q, quiero %q", strings.Join(fields, " "), want)
	}

	tooMany := make([]intakes.Item, intakes.MaxEditableItems+1)
	if got := intakeMergeItemDefects([]intakes.LineDefect{long}, tooMany); !reflect.DeepEqual(got, []intakes.LineDefect{long}) {
		t.Errorf("con más líneas de la cuenta: %+v; quiero solo el defecto del saneo", got)
	}
	if got := intakeMergeItemDefects(nil, []intakes.Item{{SKU: "A", Label: "A", Qty: 1}}); len(got) != 0 {
		t.Errorf("líneas buenas dieron defectos: %+v", got)
	}
}
