//go:build pendiente

package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// lineReader es un IntakeLineReader que devuelve lo que se le dé y apunta lo que le piden.
type lineReader struct {
	lines []SummaryLine
	err   error
	asked []string
}

func (r *lineReader) OpenIntakeLines(_ context.Context, tenantID, sessionID, contactID string) ([]SummaryLine, error) {
	r.asked = append(r.asked, tenantID+"|"+sessionID+"|"+contactID)
	return r.lines, r.err
}

// answerReader es un SurveyAnswerReader que devuelve lo que se le dé y apunta el evento que recibe.
type answerReader struct {
	answers []SummaryAnswer
	err     error
	asked   []Event
}

func (r *answerReader) SurveyAnswers(_ context.Context, ev Event) ([]SummaryAnswer, error) {
	r.asked = append(r.asked, ev)
	return r.answers, r.err
}

// summaryLog es un SummaryAppender que apunta lo que se le escribe.
type summaryLog struct {
	rows []string
	err  error
}

func (l *summaryLog) AppendSummary(_ context.Context, eventID string, body json.RawMessage) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	l.rows = append(l.rows, eventID+"|"+string(body))
	return len(l.rows), nil
}

var (
	_ IntakeLineReader   = (*lineReader)(nil)
	_ SurveyAnswerReader = (*answerReader)(nil)
	_ SummaryAppender    = (*summaryLog)(nil)
)

// orderLines son las líneas del pedido de referencia.
func orderLines() []SummaryLine {
	return []SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
		{SKU: "TE#V2", Label: "Té — grande", Qty: 1, UnitPrice: 2},
	}
}

// eventOf es un evento de ese tipo, con identificadores reconocibles.
func eventOf(kind string) Event {
	return Event{ID: "ev-1", TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: kind,
		HistoryID: kind + "-2026-08-09-1830", Status: StatusOpen, FlowID: "f-1", FlowVersion: 3,
		CreatedAt: time.Date(2026, 8, 9, 18, 30, 0, 0, time.UTC)}
}

// cartVars es flow_state.vars tal como lo deja el módulo cart: su sub-estado bajo la clave "cart",
// con el nivel en "level".
func cartVars(level string) map[string]any {
	return map[string]any{"cart": map[string]any{"level": level, "page": 2, "items": []any{}}}
}

// TestCartLevelFromVars_ReadsOnlyTheLevel: el nivel sale de vars["cart"]["level"], también cuando
// el sub-estado llega como struct o como JSON ya deserializado; cualquier otra cosa es "".
func TestCartLevelFromVars_ReadsOnlyTheLevel(t *testing.T) {
	type typed struct {
		Level string `json:"level"`
		Page  int    `json:"page"`
	}
	cases := []struct {
		name string
		vars map[string]any
		want string
	}{
		{"map", cartVars("quantity"), "quantity"},
		{"typed struct", map[string]any{"cart": typed{Level: "summary", Page: 1}}, "summary"},
		{"nil vars", nil, ""},
		{"no cart key", map[string]any{"answers": map[string]any{"level": "x"}}, ""},
		{"nil cart", map[string]any{"cart": nil}, ""},
		{"cart is a string", map[string]any{"cart": "quantity"}, ""},
		{"level is not a string", map[string]any{"cart": map[string]any{"level": 3}}, ""},
		{"no level", map[string]any{"cart": map[string]any{"page": 2}}, ""},
		{"not serializable", map[string]any{"cart": make(chan int)}, ""},
	}
	for _, c := range cases {
		if got := CartLevelFromVars(c.vars); got != c.want {
			t.Errorf("%s: CartLevelFromVars = %q, quería %q", c.name, got, c.want)
		}
	}
}

// TestLoadSummary_Cart_LinesFromTheDurableSourceLevelFromVars: las líneas salen del lector —pedidas
// con tenant, SESIÓN y contacto del evento— y el nivel de vars.
func TestLoadSummary_Cart_LinesFromTheDurableSourceLevelFromVars(t *testing.T) {
	reader := &lineReader{lines: orderLines()}
	s, err := LoadSummary(t.Context(), SummarySources{Lines: reader}, eventOf("cart"), cartVars("continue"))
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if s.Kind != "cart" || s.Level != "continue" || len(s.Lines) != 2 || s.Lines[0] != orderLines()[0] || s.Lines[1] != orderLines()[1] || len(s.Answers) != 0 {
		t.Errorf("resumen = %+v", s)
	}
	if len(reader.asked) != 1 || reader.asked[0] != "t-1|s-1|c-1" {
		t.Errorf("lecturas = %v, quería una con la clave completa del evento", reader.asked)
	}
}

// TestLoadSummary_Cart_NilVarsIsTheRescue: con vars nil (el rescate) el resumen conserva sus
// líneas y solo pierde el nivel.
func TestLoadSummary_Cart_NilVarsIsTheRescue(t *testing.T) {
	s, err := LoadSummary(t.Context(), SummarySources{Lines: &lineReader{lines: orderLines()}}, eventOf("cart"), nil)
	if err != nil || s.Level != "" || len(s.Lines) != 2 || s.Empty() {
		t.Errorf("LoadSummary con vars nil = (%+v, %v); quería las dos líneas sin nivel", s, err)
	}
}

// TestLoadSummary_Survey_LastAnswerPerQuestionSortedByQuestion: el lector recibe el Event ENTERO;
// de las respuestas queda UNA por pregunta —la última— y salen ordenadas por QuestionID, sin vars.
func TestLoadSummary_Survey_LastAnswerPerQuestionSortedByQuestion(t *testing.T) {
	reader := &answerReader{answers: []SummaryAnswer{
		{QuestionID: "p3", AnswerCode: "a"},
		{QuestionID: "p1", AnswerCode: "b"},
		{QuestionID: "p3", AnswerCode: "c"},
		{QuestionID: "p2", AnswerCode: "a"},
		{QuestionID: "p1", AnswerCode: "d"},
	}}
	ev := eventOf("survey")
	s, err := LoadSummary(t.Context(), SummarySources{Answers: reader}, ev, nil)
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	want := []SummaryAnswer{{"p1", "d"}, {"p2", "a"}, {"p3", "c"}}
	if s.Kind != "survey" || len(s.Answers) != len(want) || len(s.Lines) != 0 || s.Level != "" {
		t.Fatalf("resumen = %+v, quería las tres respuestas", s)
	}
	for i := range want {
		if s.Answers[i] != want[i] {
			t.Errorf("respuesta %d = %+v, quería %+v", i, s.Answers[i], want[i])
		}
	}
	if len(reader.asked) != 1 || reader.asked[0] != ev {
		t.Errorf("el lector recibió %+v, quería el evento entero", reader.asked)
	}
}

// TestLoadSummary_SameStateSameSummary: es determinista: el mismo estado da el mismo byte, sea
// cual sea el orden en que el lector entregue las respuestas de preguntas distintas.
func TestLoadSummary_SameStateSameSummary(t *testing.T) {
	encode := func(answers []SummaryAnswer) string {
		t.Helper()
		s, err := LoadSummary(t.Context(), SummarySources{Answers: &answerReader{answers: answers}}, eventOf("survey"), nil)
		if err != nil {
			t.Fatalf("LoadSummary: %v", err)
		}
		raw, err := s.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		return string(raw)
	}
	one := encode([]SummaryAnswer{{"p1", "a"}, {"p2", "b"}, {"p3", "c"}})
	other := encode([]SummaryAnswer{{"p3", "c"}, {"p1", "a"}, {"p2", "b"}})
	if one != other {
		t.Errorf("dos lecturas del mismo estado serializan distinto:\n%s\n%s", one, other)
	}
	for range 20 {
		if again := encode([]SummaryAnswer{{"p1", "a"}, {"p2", "b"}, {"p3", "c"}}); again != one {
			t.Fatalf("la serialización no es estable:\n%s\n%s", again, one)
		}
	}
}

// TestLoadSummary_MissingReader_IsAnErrorNotAnEmptySummary: no poder leer no es leer y no
// encontrar: sin el lector de su tipo, el centinela; el del OTRO tipo no hace falta.
func TestLoadSummary_MissingReader_IsAnErrorNotAnEmptySummary(t *testing.T) {
	texts := map[error]string{
		ErrNoIntakeLineReader:   "events: sin IntakeLineReader no se puede resumir un pedido (leer y no encontrar no es lo mismo que no poder leer)",
		ErrNoSurveyAnswerReader: "events: sin SurveyAnswerReader no se puede resumir una encuesta (leer y no encontrar no es lo mismo que no poder leer)",
	}
	for sentinel, want := range texts {
		if sentinel.Error() != want {
			t.Errorf("centinela = %q, quería %q", sentinel.Error(), want)
		}
	}
	s, err := LoadSummary(t.Context(), SummarySources{Answers: &answerReader{}}, eventOf("cart"), cartVars("continue"))
	if !errors.Is(err, ErrNoIntakeLineReader) || err.Error() != ErrNoIntakeLineReader.Error() || s.Kind != "" || !s.Empty() {
		t.Errorf("cart sin lector de líneas = (%+v, %v), quería (cero, ErrNoIntakeLineReader)", s, err)
	}
	s, err = LoadSummary(t.Context(), SummarySources{Lines: &lineReader{}}, eventOf("survey"), nil)
	if !errors.Is(err, ErrNoSurveyAnswerReader) || err.Error() != ErrNoSurveyAnswerReader.Error() || s.Kind != "" || !s.Empty() {
		t.Errorf("survey sin lector de respuestas = (%+v, %v), quería (cero, ErrNoSurveyAnswerReader)", s, err)
	}
}

// TestLoadSummary_ReaderFailure_Wrapped: el fallo del lector llega envuelto con su texto.
func TestLoadSummary_ReaderFailure_Wrapped(t *testing.T) {
	boom := errors.New("boom")
	_, err := LoadSummary(t.Context(), SummarySources{Lines: &lineReader{err: boom}}, eventOf("cart"), nil)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "events: leer las líneas del pedido para el resumen: ") {
		t.Errorf("cart: err = %v", err)
	}
	_, err = LoadSummary(t.Context(), SummarySources{Answers: &answerReader{err: boom}}, eventOf("survey"), nil)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "events: leer las respuestas de la encuesta para el resumen: ") {
		t.Errorf("survey: err = %v", err)
	}
}

// TestLoadSummary_KindsWithoutDecisions_EmptyAndWithoutReaders: el menú, media y cualquier tipo
// nuevo dan un resumen VACÍO con su Kind, sin necesitar ningún lector ni tocar los que haya.
func TestLoadSummary_KindsWithoutDecisions_EmptyAndWithoutReaders(t *testing.T) {
	lines, answers := &lineReader{lines: orderLines()}, &answerReader{answers: []SummaryAnswer{{"p1", "a"}}}
	for _, kind := range []string{"menu", "media", "reserva", ""} {
		for _, src := range []SummarySources{{}, {Lines: lines, Answers: answers}} {
			s, err := LoadSummary(t.Context(), src, eventOf(kind), cartVars("continue"))
			if err != nil || s.Kind != kind || !s.Empty() || s.Level != "" {
				t.Errorf("tipo %q: (%+v, %v), quería un resumen vacío de ese tipo", kind, s, err)
			}
		}
	}
	if len(lines.asked) != 0 || len(answers.asked) != 0 {
		t.Error("un tipo sin decisiones consultó un lector")
	}
	if s := BuildMenuSummary(); s.Kind != "menu" || !s.Empty() || s.Render() != "" {
		t.Errorf("BuildMenuSummary = %+v, quería el resumen vacío del menú", s)
	}
}

// TestBuildCartSummary_NoLinesIsEmptyEvenWithALevel: estar mirando categorías no es una decisión.
func TestBuildCartSummary_NoLinesIsEmptyEvenWithALevel(t *testing.T) {
	s := BuildCartSummary(CartState{Level: "categories"})
	if s.Kind != "cart" || s.Level != "" || !s.Empty() || s.Render() != "" {
		t.Errorf("BuildCartSummary sin líneas = %+v, quería el resumen vacío del pedido", s)
	}
	if s := BuildSurveySummary(nil); s.Kind != "survey" || !s.Empty() || s.Render() != "" {
		t.Errorf("BuildSurveySummary sin respuestas = %+v, quería el resumen vacío de la encuesta", s)
	}
}

// TestBuildSummaries_AreASnapshot: el resumen no comparte memoria con el estado del que salió.
func TestBuildSummaries_AreASnapshot(t *testing.T) {
	lines := orderLines()
	cart := BuildCartSummary(CartState{Level: "continue", Lines: lines})
	lines[0].Qty, lines[0].Label = 99, "manoseada"
	if cart.Lines[0] != orderLines()[0] {
		t.Errorf("el resumen del pedido cambió por detrás: %+v", cart.Lines[0])
	}
	answers := []SummaryAnswer{{"p1", "a"}}
	survey := BuildSurveySummary(answers)
	answers[0].AnswerCode = "z"
	if survey.Answers[0] != (SummaryAnswer{"p1", "a"}) {
		t.Errorf("el resumen de la encuesta cambió por detrás: %+v", survey.Answers[0])
	}
}

// TestSummary_Empty: vacío es «sin líneas y sin respuestas», tenga o no tipo y nivel.
func TestSummary_Empty(t *testing.T) {
	cases := []struct {
		s    Summary
		want bool
	}{
		{Summary{}, true},
		{Summary{Kind: "cart", Level: "continue"}, true},
		{Summary{Kind: "cart", Lines: orderLines()}, false},
		{Summary{Kind: "survey", Answers: []SummaryAnswer{{"p1", "a"}}}, false},
	}
	for _, c := range cases {
		if got := c.s.Empty(); got != c.want {
			t.Errorf("%+v.Empty() = %v, quería %v", c.s, got, c.want)
		}
	}
}

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

// TestPersistSummary_WritesOneRowWithTheSummary: escribe UNA fila con el payload del resumen en el
// evento que se abandona y devuelve su seq.
func TestPersistSummary_WritesOneRowWithTheSummary(t *testing.T) {
	log := &summaryLog{}
	src := SummarySources{Lines: &lineReader{lines: orderLines()}}
	seq, written, err := PersistSummary(t.Context(), log, src, eventOf("cart"), cartVars("continue"))
	if err != nil || !written || seq != 1 {
		t.Fatalf("PersistSummary = (%d, %v, %v), quería (1, true, nil)", seq, written, err)
	}
	want := `ev-1|{"kind":"cart","level":"continue","lines":[` +
		`{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5},` +
		`{"sku":"TE#V2","label":"Té — grande","qty":1,"unit_price":2}]}`
	if len(log.rows) != 1 || log.rows[0] != want {
		t.Errorf("filas = %v\nquería %s", log.rows, want)
	}
}

// TestPersistSummary_SecondAbandonAppendsAnotherRow: un segundo abandono escribe SU fila y no pisa
// la del primero.
func TestPersistSummary_SecondAbandonAppendsAnotherRow(t *testing.T) {
	log := &summaryLog{}
	reader := &lineReader{lines: orderLines()[:1]}
	src := SummarySources{Lines: reader}
	if _, _, err := PersistSummary(t.Context(), log, src, eventOf("cart"), cartVars("quantity")); err != nil {
		t.Fatalf("primer abandono: %v", err)
	}
	first := log.rows[0]
	reader.lines = orderLines()
	seq, written, err := PersistSummary(t.Context(), log, src, eventOf("cart"), cartVars("summary"))
	if err != nil || !written || seq != 2 {
		t.Fatalf("segundo abandono = (%d, %v, %v), quería (2, true, nil)", seq, written, err)
	}
	if len(log.rows) != 2 || log.rows[0] != first || log.rows[1] == first {
		t.Errorf("filas = %v; quería la del primero intacta y otra distinta", log.rows)
	}
}

// TestPersistSummary_NothingToSummarize_WritesNoRow: un pedido sin líneas, una encuesta sin
// respuestas, el menú y un tipo sin decisiones no escriben fila: (0, false, nil).
func TestPersistSummary_NothingToSummarize_WritesNoRow(t *testing.T) {
	log := &summaryLog{}
	src := SummarySources{Lines: &lineReader{}, Answers: &answerReader{}}
	for _, kind := range []string{"cart", "survey", "menu", "media"} {
		seq, written, err := PersistSummary(t.Context(), log, src, eventOf(kind), cartVars("categories"))
		if err != nil || written || seq != 0 {
			t.Errorf("tipo %s: (%d, %v, %v), quería (0, false, nil)", kind, seq, written, err)
		}
	}
	if len(log.rows) != 0 {
		t.Errorf("se escribieron %d filas vacías: %v", len(log.rows), log.rows)
	}
}

// TestPersistSummary_Survey_WritesItsRow: la encuesta abandonada con respuestas escribe su fila,
// con vars nil.
func TestPersistSummary_Survey_WritesItsRow(t *testing.T) {
	log := &summaryLog{}
	src := SummarySources{Answers: &answerReader{answers: []SummaryAnswer{{"p2", "b"}, {"p1", "a"}}}}
	seq, written, err := PersistSummary(t.Context(), log, src, eventOf("survey"), nil)
	if err != nil || !written || seq != 1 {
		t.Fatalf("PersistSummary = (%d, %v, %v), quería (1, true, nil)", seq, written, err)
	}
	want := `ev-1|{"kind":"survey","answers":[{"question_id":"p1","answer_code":"a"},{"question_id":"p2","answer_code":"b"}]}`
	if log.rows[0] != want {
		t.Errorf("fila = %s\nquería %s", log.rows[0], want)
	}
}

// TestPersistSummary_Failures_ReachTheCaller: el fallo del historial llega envuelto con el evento
// y el tipo; el de la fuente, tal cual; y en los dos casos no se da nada por escrito.
func TestPersistSummary_Failures_ReachTheCaller(t *testing.T) {
	boom := errors.New("boom")
	src := SummarySources{Lines: &lineReader{lines: orderLines()}}
	seq, written, err := PersistSummary(t.Context(), &summaryLog{err: boom}, src, eventOf("cart"), nil)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "events: persistir el resumen del evento ev-1 (tipo cart): ") || written || seq != 0 {
		t.Errorf("con el historial caído: (%d, %v, %v)", seq, written, err)
	}
	log := &summaryLog{}
	seq, written, err = PersistSummary(t.Context(), log, SummarySources{}, eventOf("cart"), nil)
	if !errors.Is(err, ErrNoIntakeLineReader) || err.Error() != ErrNoIntakeLineReader.Error() || written || seq != 0 || len(log.rows) != 0 {
		t.Errorf("sin lector: (%d, %v, %v) y %d filas; quería (0, false, ErrNoIntakeLineReader) y ninguna", seq, written, err, len(log.rows))
	}
}
