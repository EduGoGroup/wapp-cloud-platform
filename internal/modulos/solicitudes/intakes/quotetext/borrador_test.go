package quotetext_test

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// Aserciones de compilación del contrato de borrador.go.
var (
	_ func([]intakes.Item) quotetext.Draft           = quotetext.DraftOf
	_ func([]intakes.Item) bool                      = quotetext.HasCustomerLines
	_ func(quotetext.Draft) (json.RawMessage, error) = quotetext.Draft.JSON
	_                                                = quotetext.Line{}
	_ int                                            = quotetext.DraftVersion
)

func TestDraftVersion(t *testing.T) {
	if quotetext.DraftVersion != 1 {
		t.Fatalf("DraftVersion = %d; el contrato del prompt es la versión 1", quotetext.DraftVersion)
	}
}

func TestDraftOf_ProjectsItemsInOrder(t *testing.T) {
	draft := fusionDraft()

	if draft.Version != quotetext.DraftVersion {
		t.Errorf("Version = %d; se esperaba DraftVersion", draft.Version)
	}
	if draft.Total != fusionTotal {
		t.Errorf("Total = %v; se esperaba la suma de las líneas, %v", draft.Total, fusionTotal)
	}
	if len(draft.Lines) != len(fusionItems) {
		t.Fatalf("el borrador tiene %d líneas; se esperaban %d", len(draft.Lines), len(fusionItems))
	}
	for i, item := range fusionItems {
		want := quotetext.Line{Label: item.Label, Qty: 1, UnitPrice: item.UnitPrice, LineTotal: item.UnitPrice}
		if draft.Lines[i] != want {
			t.Errorf("línea %d = %+v; se esperaba %+v", i, draft.Lines[i], want)
		}
	}
}

func TestDraftOf_LineRules(t *testing.T) {
	cases := []struct {
		name string
		item intakes.Item
		want quotetext.Line
	}{
		{"quantity multiplies the unit price",
			intakes.Item{SKU: "A", Label: "Torta", Qty: 3, UnitPrice: 100},
			quotetext.Line{Label: "Torta", Qty: 3, UnitPrice: 100, LineTotal: 300}},
		{"zero quantity counts as one",
			intakes.Item{SKU: "A", Label: "Torta", UnitPrice: 100},
			quotetext.Line{Label: "Torta", Qty: 1, UnitPrice: 100, LineTotal: 100}},
		{"negative quantity counts as one",
			intakes.Item{SKU: "A", Label: "Torta", Qty: -4, UnitPrice: 100},
			quotetext.Line{Label: "Torta", Qty: 1, UnitPrice: 100, LineTotal: 100}},
		{"zero unit price is a pending price, not a free line",
			intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 2},
			quotetext.Line{Label: "Envío", Qty: 2, PendingPrice: true}},
		{"customization is copied and never billed",
			intakes.Item{SKU: "A", Label: "Torta", Customization: "sin lactosa", Qty: 1, UnitPrice: 2100},
			quotetext.Line{Label: "Torta", Customization: "sin lactosa", Qty: 1, UnitPrice: 2100, LineTotal: 2100}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			draft := quotetext.DraftOf([]intakes.Item{c.item})
			if len(draft.Lines) != 1 || draft.Lines[0] != c.want {
				t.Fatalf("líneas = %+v; se esperaba una sola: %+v", draft.Lines, c.want)
			}
			if draft.Total != c.want.LineTotal {
				t.Errorf("Total = %v; se esperaba el total de la línea, %v", draft.Total, c.want.LineTotal)
			}
		})
	}
}

func TestDraftOf_PendingLineAddsNothingToTheTotal(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "A", Label: "Torta", Qty: 3, UnitPrice: 100},
		{SKU: "B", Label: "Café", Qty: 1, UnitPrice: 50},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	})
	if draft.Total != 350 {
		t.Errorf("Total = %v; se esperaba 350 (la línea sin precio no suma)", draft.Total)
	}
	if !draft.Lines[2].PendingPrice {
		t.Error("la línea sin precio tiene que salir marcada PendingPrice")
	}
	if draft.Lines[0].PendingPrice || draft.Lines[1].PendingPrice {
		t.Error("una línea con precio no puede salir marcada PendingPrice")
	}
}

func TestDraftOf_NoItemsGivesAnEmptyNonNilList(t *testing.T) {
	for name, items := range map[string][]intakes.Item{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			draft := quotetext.DraftOf(items)
			if draft.Lines == nil || len(draft.Lines) != 0 {
				t.Fatalf("Lines = %#v; se esperaba una lista vacía, no nil", draft.Lines)
			}
			if draft.Total != 0 || draft.Version != quotetext.DraftVersion {
				t.Errorf("Total = %v, Version = %d; se esperaba 0 y DraftVersion", draft.Total, draft.Version)
			}
		})
	}
}

func TestDraftOf_DoesNotMutateTheInput(t *testing.T) {
	items := []intakes.Item{{SKU: "A", Label: "Torta", Qty: 0, UnitPrice: 100}}
	before := append([]intakes.Item(nil), items...)

	if draft := quotetext.DraftOf(items); draft.Lines[0].Qty != 1 {
		t.Fatalf("Qty = %d; el caso tiene que pasar por la corrección de la cantidad", draft.Lines[0].Qty)
	}

	if !reflect.DeepEqual(items, before) {
		t.Fatalf("DraftOf mutó la entrada: %+v; era %+v", items, before)
	}
}

func TestHasCustomerLines(t *testing.T) {
	cases := []struct {
		name  string
		items []intakes.Item
		want  bool
	}{
		{"no items", nil, false},
		{"only the shipping line", []intakes.Item{{SKU: intakes.ShippingSKU}}, false},
		{"only reserved-prefix lines", []intakes.Item{{SKU: "_a"}, {SKU: intakes.ReservedSKUPrefix}}, false},
		{"a customer line after a system line", []intakes.Item{{SKU: intakes.ShippingSKU}, {SKU: "TORTA"}}, true},
		{"an empty sku is not a system line", []intakes.Item{{SKU: ""}}, true},
		{"the prefix in the middle does not reserve the sku", []intakes.Item{{SKU: "A_B"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := quotetext.HasCustomerLines(c.items); got != c.want {
				t.Fatalf("HasCustomerLines = %v; se esperaba %v", got, c.want)
			}
		})
	}
}

// TestDraft_JSON_IsThePromptContract fija el blob BYTE A BYTE: es lo que lee el modelo
// y el prefijo que el proveedor cachea.
func TestDraft_JSON_IsThePromptContract(t *testing.T) {
	draft := quotetext.DraftOf([]intakes.Item{
		{SKU: "TORTA-CHOC", Label: "Torta", Customization: "sin lactosa", Qty: 2, UnitPrice: 2100},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	})
	const want = `{"version":1,"lines":[` +
		`{"label":"Torta","customization":"sin lactosa","qty":2,"unit_price":2100,"line_total":4200},` +
		`{"label":"Envío","qty":1,"unit_price":0,"line_total":0,"pending_price":true}` +
		`],"total":4200}`

	raw, err := draft.JSON()
	if err != nil {
		t.Fatalf("JSON devolvió error: %v", err)
	}
	if string(raw) != want {
		t.Errorf("JSON =\n%s\nse esperaba\n%s", raw, want)
	}
	if strings.Contains(string(raw), "TORTA-CHOC") || strings.Contains(string(raw), intakes.ShippingSKU) {
		t.Errorf("el borrador NO debe llevar los SKU internos al prompt:\n%s", raw)
	}
}

func TestDraft_JSON_EmptyDraftKeepsTheLinesKeyAsAnArray(t *testing.T) {
	raw, err := quotetext.DraftOf(nil).JSON()
	if err != nil {
		t.Fatalf("JSON devolvió error: %v", err)
	}
	if string(raw) != `{"version":1,"lines":[],"total":0}` {
		t.Errorf("JSON = %s; un borrador sin líneas serializa una lista vacía, no null", raw)
	}
}

func TestDraft_JSON_NonFiniteAmountIsANamedError(t *testing.T) {
	raw, err := quotetext.Draft{Version: quotetext.DraftVersion, Total: math.NaN()}.JSON()
	if err == nil {
		t.Fatalf("JSON aceptó un total no finito: %s", raw)
	}
	if raw != nil {
		t.Errorf("con error no se devuelve blob; se devolvió %s", raw)
	}
	if !strings.HasPrefix(err.Error(), "quotetext: serializar el borrador: ") {
		t.Errorf("error = %q; se esperaba el prefijo del paquete", err)
	}
	var cause *json.UnsupportedValueError
	if !errors.As(err, &cause) {
		t.Errorf("el error no envuelve la causa de encoding/json: %v", err)
	}
}
