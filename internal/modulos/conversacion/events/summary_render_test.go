package events

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSummary_Encode_StructureInClearWithoutDerivedTotal: la forma literal del payload; el TOTAL
// no se serializa (INV-13) y lo vacío se omite.
func TestSummary_Encode_StructureInClearWithoutDerivedTotal(t *testing.T) {
	cases := []struct {
		name string
		s    Summary
		want string
	}{
		{"cart", BuildCartSummary(CartState{Level: "summary", Lines: []SummaryLine{
			{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
			{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2},
		}}), `{"kind":"cart","level":"summary","lines":[` +
			`{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5,"customization":"sin azúcar"},` +
			`{"sku":"TE","label":"Té","qty":1,"unit_price":2}]}`},
		{"survey", BuildSurveySummary([]SummaryAnswer{{"p1", "a"}}), `{"kind":"survey","answers":[{"question_id":"p1","answer_code":"a"}]}`},
		{"menu", BuildMenuSummary(), `{"kind":"menu"}`},
	}
	for _, c := range cases {
		raw, err := c.s.Encode()
		if err != nil || string(raw) != c.want || !json.Valid(raw) {
			t.Errorf("%s: Encode = (%s, %v)\nquería %s", c.name, raw, err, c.want)
		}
		if strings.Contains(strings.ToLower(string(raw)), "total") {
			t.Errorf("%s: el payload serializa un total derivado: %s", c.name, raw)
		}
	}
}

// TestSummary_Render_Lines_TheTextTheClientReads: el texto literal, con el precio copiado, la
// indicación bajo SU línea, el TOTAL al final y la frase del nivel.
func TestSummary_Render_Lines_TheTextTheClientReads(t *testing.T) {
	s := BuildCartSummary(CartState{Level: "continue", Lines: []SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
		{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2},
	}})
	want := "Esto es lo que ya habías decidido en tu pedido:\n" +
		"Café x2  $5.00\n" +
		"   ✏️ sin azúcar\n" +
		"Té x1  $2.00\n" +
		"TOTAL  $7.00\n" +
		"Te quedaste decidiendo si agregar algo más."
	if got := s.Render(); got != want {
		t.Errorf("Render:\n%s\n--- quería ---\n%s", got, want)
	}
}

// TestSummary_Render_INV13_TheNoteNeverTouchesTheTotal: INV-13. La indicación va bajo su línea y el
// TOTAL sale solo de qty × unit_price: con y sin indicación es el mismo.
func TestSummary_Render_INV13_TheNoteNeverTouchesTheTotal(t *testing.T) {
	with := BuildCartSummary(CartState{Level: "continue", Lines: []SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
	}}).Render()
	without := BuildCartSummary(CartState{Level: "continue", Lines: []SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
	}}).Render()

	if !strings.Contains(with, "\n   ✏️ sin azúcar\n") {
		t.Errorf("la indicación no aparece bajo su línea:\n%s", with)
	}
	if strings.Contains(without, "✏️") {
		t.Errorf("una línea sin indicación pinta la sub-línea:\n%s", without)
	}
	if !strings.Contains(with, "TOTAL  $5.00") || !strings.Contains(without, "TOTAL  $5.00") {
		t.Errorf("el total cambió con la indicación:\ncon:\n%s\nsin:\n%s", with, without)
	}
	odd := BuildCartSummary(CartState{Lines: []SummaryLine{
		{SKU: "A", Label: "A", Qty: 3, UnitPrice: 1.005, Customization: "100 más"},
		{SKU: "B", Label: "B", Qty: 0, UnitPrice: 50},
	}}).Render()
	if !strings.Contains(odd, "A x3  $3.02\n") && !strings.Contains(odd, "A x3  $3.01\n") {
		t.Errorf("el importe de la línea no es qty × unit_price con dos decimales:\n%s", odd)
	}
	if !strings.Contains(odd, "B x0  $0.00\nTOTAL  $3.0") {
		t.Errorf("el total no es la suma de qty × unit_price:\n%s", odd)
	}
}

// TestSummary_Render_NeverShowsIdentifiersOrJargon: habla de «pedido» y nunca de «carrito», del
// tipo técnico, del SKU, del nivel interno ni de los identificadores del evento.
func TestSummary_Render_NeverShowsIdentifiersOrJargon(t *testing.T) {
	ev := eventOf("cart")
	s, err := LoadSummary(t.Context(), SummarySources{Lines: &lineReader{lines: orderLines()}}, ev, cartVars("continue"))
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	text := s.Render()
	if !strings.Contains(text, "pedido") {
		t.Errorf("el resumen no nombra el pedido:\n%s", text)
	}
	for _, banned := range []string{"carrito", "cart", ev.ID, ev.ContactID, ev.HistoryID, "level", "continue", "CAFE", "TE#V2"} {
		if strings.Contains(text, banned) {
			t.Errorf("el resumen le enseña %q al cliente:\n%s", banned, text)
		}
	}
}

// TestSummary_Render_LevelPhrases: la frase literal de cada nivel; uno vacío, terminal o
// desconocido no imprime la línea.
func TestSummary_Render_LevelPhrases(t *testing.T) {
	phrases := map[string]string{
		"categories":      "eligiendo una categoría",
		"articles":        "eligiendo un artículo",
		"article":         "mirando un artículo",
		"variant":         "eligiendo una presentación",
		"quantity":        "eligiendo la cantidad",
		"continue":        "decidiendo si agregar algo más",
		"summary":         "revisando el resumen del pedido",
		"item_note_scope": "escribiendo una indicación",
		"item_note":       "escribiendo una indicación",
		"order_note":      "escribiendo una indicación para todo el pedido",
		"buyer_data":      "completando tus datos",
	}
	base := "Esto es lo que ya habías decidido en tu pedido:\nCafé x2  $5.00\nTOTAL  $5.00"
	line := []SummaryLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}
	for level, phrase := range phrases {
		want := base + "\nTe quedaste " + phrase + "."
		if got := BuildCartSummary(CartState{Level: level, Lines: line}).Render(); got != want {
			t.Errorf("nivel %q:\n%s\n--- quería ---\n%s", level, got, want)
		}
	}
	for _, level := range []string{"", "closed", "cancelled", "nivel_nuevo"} {
		if got := BuildCartSummary(CartState{Level: level, Lines: line}).Render(); got != base {
			t.Errorf("nivel %q:\n%s\n--- quería ---\n%s", level, got, base)
		}
	}
}

// TestSummary_Render_Answers_HowManyNotWhich: cuántas preguntas, en singular y en plural, con el
// nombre del tipo; nunca los ids ni los códigos.
func TestSummary_Render_Answers_HowManyNotWhich(t *testing.T) {
	one := BuildSurveySummary([]SummaryAnswer{{"pregunta-1", "codigo-a"}}).Render()
	if want := "Ya habías respondido 1 pregunta de tu encuesta."; one != want {
		t.Errorf("una respuesta: %q, quería %q", one, want)
	}
	three := BuildSurveySummary([]SummaryAnswer{{"pregunta-1", "codigo-a"}, {"pregunta-2", "codigo-b"}, {"pregunta-3", "codigo-c"}}).Render()
	if want := "Ya habías respondido 3 preguntas de tu encuesta."; three != want {
		t.Errorf("tres respuestas: %q, quería %q", three, want)
	}
	if strings.Contains(three, "pregunta-") || strings.Contains(three, "codigo-") {
		t.Errorf("el render enseña ids o códigos: %q", three)
	}
	// Despacha por CONTENIDO: con líneas, las líneas mandan aunque haya respuestas.
	mixed := Summary{Kind: "cart", Lines: orderLines(), Answers: []SummaryAnswer{{"p1", "a"}}}.Render()
	if !strings.HasPrefix(mixed, "Esto es lo que ya habías decidido en tu pedido:") {
		t.Errorf("con líneas y respuestas el render no pinta las líneas:\n%s", mixed)
	}
}
