// variants_test.go conduce la sub-máquina con el catálogo v2 (Plan 041 · T2.3):
// el nivel de variante que aparece SOLO cuando el artículo tiene variantes, el
// combo que sigue siendo UNA línea, y —la otra mitad del trato— el artículo sin
// variantes que no gana ni un paso.
package cart

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// v2Vars siembra las Vars con el catálogo v2 del contrato (Tortas con variantes y
// combo, Bebidas con un artículo v1 de toda la vida).
func v2Vars(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{catalogVarKey: rawFromFile(t, "catalog_v2.json")}
}

// mustNotContain falla si la salida contiene alguno de los fragmentos.
func mustNotContain(t *testing.T, outs []string, subs ...string) {
	t.Helper()
	s := joined(outs)
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Fatalf("la salida %q NO debía contener %q", s, sub)
		}
	}
}

// --- el recorrido con variante ---------------------------------------------

func TestVariante_PideVarianteAntesDeCantidad(t *testing.T) {
	m := New()
	vars := v2Vars(t)

	st, outs, vars := drive(t, m, vars, "01") // categoría Tortas
	assertLevel(t, st, LevelArticles, "esperaba articles, got %+v")
	// En la lista, un artículo con variantes anuncia "desde" el precio más bajo:
	// enseñar un precio fijo y cobrar otro al elegir el tamaño sería mentir.
	mustContain(t, outs, "1) Torta de chocolate · desde $18000.00", "2) Combo hamburguesa · $9500.00")

	st, outs, vars = drive(t, m, vars, "1") // la torta
	assertLevel(t, st, LevelArticle, "esperaba article, got %+v")
	mustContain(t, outs, "Torta de chocolate · desde $18000.00", "2) Agregar al pedido")

	st, outs, vars = drive(t, m, vars, "2") // agregar → NIVEL DE VARIANTE
	assertLevel(t, st, LevelVariant, "esperaba el nivel de variante, got %+v")
	mustContain(t, outs,
		"Torta de chocolate · elige una opción:",
		"1) 10-12 porciones · $18000.00",
		"2) 25-30 porciones · $32000.00",
		"0) ← Volver")
	mustNotContain(t, outs, "Escribe la cantidad")

	st, outs, vars = drive(t, m, vars, "2") // la de 25-30 porciones
	assertLevel(t, st, LevelQuantity, "esperaba quantity, got %+v")
	if st.VariantCode != "V2" {
		t.Fatalf("la variante elegida debe quedar en el estado, got %+v", st)
	}
	mustContain(t, outs, `¿Cuántos "Torta de chocolate — 25-30 porciones"?`)

	st, outs, _ = drive(t, m, vars, "3") // cantidad 3
	assertLevel(t, st, LevelContinue, "esperaba continue, got %+v")
	if st.VariantCode != "" {
		t.Errorf("VariantCode es transitorio: debe limpiarse al agregar, got %+v", st)
	}
	if len(st.Lines) != 1 {
		t.Fatalf("esperaba una línea, got %+v", st.Lines)
	}
	line := st.Lines[0]
	if line.SKU != "TORTA-CHOC#V2" {
		t.Errorf("el sku de la línea debe llevar el sufijo de la variante, got %q", line.SKU)
	}
	if line.Label != "Torta de chocolate — 25-30 porciones" {
		t.Errorf("la línea debe nombrar la variante, got %q", line.Label)
	}
	if line.UnitPrice != 32000 {
		t.Errorf("la línea debe llevar el precio de la VARIANTE (32000), got %v", line.UnitPrice)
	}
	mustContain(t, outs, "Añadido al pedido ✅")
}

// TestVariante_ResumenMuestraElPrecioDeLaElegida cierra el pedido y comprueba que
// el resumen y el efecto cart_closed hablan de la variante, no del artículo base.
func TestVariante_ResumenMuestraElPrecioDeLaElegida(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	for _, in := range []string{"01", "1", "2", "2", "3"} { // Tortas → torta → agregar → V2 → 3
		_, _, vars = drive(t, m, vars, in)
	}
	_, outs, vars := drive(t, m, vars, "2") // finalizar → resumen
	mustContain(t, outs, "Torta de chocolate — 25-30 porciones x3  $96000.00", "TOTAL  $96000.00")

	_, effs, _ := driveE(t, m, vars, "1") // confirmar
	closed := effByName(t, effs, EffectCartClosed)
	items, ok := closed.Payload["items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("cart_closed items: %+v", closed.Payload["items"])
	}
	if items[0]["sku"] != "TORTA-CHOC#V2" || items[0]["unit_price"] != 32000.0 {
		t.Errorf("la línea cerrada debe ser la de la variante, got %+v", items[0])
	}
	if closed.Payload["total"] != 96000.0 {
		t.Errorf("total = %v, quiero 96000", closed.Payload["total"])
	}
}

// TestVariante_EfectoItemAddedLlevaElSKUCompuesto: el payload conserva las MISMAS
// claves del v1 (sku/label/qty/unit_price); lo único que cambia es su contenido.
func TestVariante_EfectoItemAddedLlevaElSKUCompuesto(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	for _, in := range []string{"01", "1", "2", "1"} { // → variante 10-12 porciones
		_, _, vars = drive(t, m, vars, in)
	}
	_, effs, _ := driveE(t, m, vars, "2") // cantidad 2
	e := effByName(t, effs, EffectItemAdded)
	// Se mira el payload PÚBLICO —lo que acaba en public.flow_events— porque es ahí
	// donde "no ganar ni perder claves" significa algo: es el contrato que leen la
	// telemetría y los goldens. Desde el Plan 043 · Ola 3 el efecto lleva además la
	// foto de las líneas, declarada PRIVADA justo para no entrar aquí; que siga
	// fuera lo comprueba TestItemAdded_LaFotoDeLineasNoEntraEnFlowEvents.
	if len(e.PublicPayload()) != 4 {
		t.Fatalf("item_added no debe ganar ni perder claves, got %+v", e.PublicPayload())
	}
	if e.Payload["sku"] != "TORTA-CHOC#V1" || e.Payload["label"] != "Torta de chocolate — 10-12 porciones" ||
		e.Payload["qty"] != 2 || e.Payload["unit_price"] != 18000.0 {
		t.Errorf("item_added de variante inesperado: %+v", e.Payload)
	}
}

// --- volver en el nivel de variante ----------------------------------------

func TestVariante_VolverDesdeVarianteVaALaFicha(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	for _, in := range []string{"01", "1", "2"} {
		_, _, vars = drive(t, m, vars, in)
	}
	st, outs, _ := drive(t, m, vars, "0")
	assertLevel(t, st, LevelArticle, "volver desde la variante va a la ficha, got %+v")
	if st.VariantCode != "" {
		t.Errorf("volver debe limpiar la variante, got %+v", st)
	}
	mustContain(t, outs, "1) Ver descripción", "2) Agregar al pedido")
}

// TestVariante_VolverDesdeCantidadVuelveALasVariantes: un paso atrás de verdad. Si
// volviera a la ficha, el cliente tendría que re-elegir la variante sin que nadie
// se lo dijera.
func TestVariante_VolverDesdeCantidadVuelveALasVariantes(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	for _, in := range []string{"01", "1", "2", "2"} { // hasta cantidad, con V2 elegida
		_, _, vars = drive(t, m, vars, in)
	}
	st, outs, _ := drive(t, m, vars, "0")
	assertLevel(t, st, LevelVariant, "volver desde cantidad vuelve a las variantes, got %+v")
	if st.VariantCode != "" {
		t.Errorf("al volver, la variante deja de estar elegida, got %+v", st)
	}
	mustContain(t, outs, "elige una opción:", "1) 10-12 porciones")
}

func TestVariante_EntradaInvalidaReprompt(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	for _, in := range []string{"01", "1", "2"} {
		_, _, vars = drive(t, m, vars, in)
	}
	st, outs, _ := drive(t, m, vars, "9") // no hay variante 9
	assertLevel(t, st, LevelVariant, "una opción inexistente no mueve de nivel, got %+v")
	mustContain(t, outs, "Opción no válida", "elige una opción:")
}

// TestVariante_NoSobreviveAlCambioDeArticulo: la variante elegida es del artículo
// en foco; al soltar el artículo (subir a la lista o a categorías) tiene que
// morir con él, o un artículo distinto heredaría una elección que nadie hizo.
func TestVariante_NoSobreviveAlCambioDeArticulo(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	for _, in := range []string{"01", "1", "2", "2"} { // hasta cantidad con V2 elegida
		_, _, vars = drive(t, m, vars, in)
	}
	if loadState(vars).VariantCode != "V2" {
		t.Fatalf("el montaje del caso exige V2 elegida, got %+v", loadState(vars))
	}
	_, _, vars = drive(t, m, vars, "0")   // → variantes
	_, _, vars = drive(t, m, vars, "0")   // → ficha
	st, _, vars := drive(t, m, vars, "0") // → lista de artículos
	if st.VariantCode != "" {
		t.Fatalf("al subir a la lista de artículos la variante debe soltarse, got %+v", st)
	}
	st, _, _ = drive(t, m, vars, "0") // → categorías
	if st.VariantCode != "" {
		t.Fatalf("en categorías no puede quedar variante, got %+v", st)
	}
}

// --- combos ------------------------------------------------------------------

// TestCombo_EsUnaSolaLinea: el combo no despliega sus componentes ni cobra por
// ellos; es UNA línea a su propio precio, y no pide variante.
func TestCombo_EsUnaSolaLinea(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	_, _, vars = drive(t, m, vars, "01") // Tortas
	_, _, vars = drive(t, m, vars, "2")  // el combo
	st, outs, vars := drive(t, m, vars, "2")
	assertLevel(t, st, LevelQuantity, "un combo NO pide variante: va directo a cantidad, got %+v")
	mustContain(t, outs, `¿Cuántos "Combo hamburguesa"?`)
	mustNotContain(t, outs, "HAMB", "REFR", "PAPA")

	st, _, vars = drive(t, m, vars, "1")
	if len(st.Lines) != 1 {
		t.Fatalf("el combo debe agregar UNA sola línea, got %+v", st.Lines)
	}
	if st.Lines[0].SKU != "COMBO-1" || st.Lines[0].UnitPrice != 9500 {
		t.Errorf("la línea del combo es el combo a su precio, got %+v", st.Lines[0])
	}
	_, outs, _ = drive(t, m, vars, "2") // resumen
	mustContain(t, outs, "Combo hamburguesa x1  $9500.00", "TOTAL  $9500.00")
	mustNotContain(t, outs, "HAMB")
}

// --- el artículo sin variantes no cambia ------------------------------------

// TestSinVariantes_MismosPasosQueSiempre: el Café del catálogo v2 no tiene ni
// variantes ni componentes; agregar lleva DIRECTO a cantidad, con el texto de
// siempre y el sku sin sufijos.
func TestSinVariantes_MismosPasosQueSiempre(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	_, _, vars = drive(t, m, vars, "02") // Bebidas
	_, outs, vars := drive(t, m, vars, "1")
	mustContain(t, outs, "Café · $2.50") // sin "desde"
	mustNotContain(t, outs, "desde")

	st, outs, vars := drive(t, m, vars, "2")
	assertLevel(t, st, LevelQuantity, "sin variantes, agregar va directo a cantidad, got %+v")
	mustContain(t, outs, `¿Cuántos "Café"?`)

	st, _, vars = drive(t, m, vars, "2")
	if len(st.Lines) != 1 || st.Lines[0].SKU != "CAFE" || st.Lines[0].UnitPrice != 2.5 {
		t.Fatalf("la línea sin variante es la de siempre, got %+v", st.Lines)
	}
	// Y "volver" desde cantidad sigue llevando a la ficha, no a ningún nivel nuevo.
	_, _, vars = drive(t, m, vars, "0") // continue → article
	_, _, vars = drive(t, m, vars, "2") // article → quantity
	st, outs, _ = drive(t, m, vars, "0")
	assertLevel(t, st, LevelArticle, "sin variantes, volver desde cantidad va a la ficha, got %+v")
	mustContain(t, outs, "1) Ver descripción")
}

// --- estado incoherente ------------------------------------------------------

// TestVariante_EstadoSinVarianteVuelveAPreguntar: si el estado dice "cantidad" de
// un artículo con variantes pero sin variante elegida (el dueño la quitó del
// catálogo con la conversación viva), se vuelve a preguntar en vez de cobrar el
// precio de referencia.
func TestVariante_EstadoSinVarianteVuelveAPreguntar(t *testing.T) {
	m := New()
	vars := v2Vars(t)
	storeState(vars, cartState{Level: LevelQuantity, CatCode: "01", SKU: "TORTA-CHOC"})
	st, outs, _ := drive(t, m, vars, "2")
	assertLevel(t, st, LevelVariant, "sin variante elegida se vuelve a preguntar, got %+v")
	if len(st.Lines) != 0 {
		t.Fatalf("no debe agregarse ninguna línea a ciegas, got %+v", st.Lines)
	}
	mustContain(t, outs, "elige una opción:")
}

// --- el Primer con variantes (Plan 029) -------------------------------------

// TestPrime_VarianteClara_PreAgregaLaVariante: el cliente nombró la presentación,
// así que la línea es la de esa variante.
func TestPrime_VarianteClara_PreAgregaLaVariante(t *testing.T) {
	m := New()
	content := model.Content{Raw: rawFromFile(t, "catalog_v2.json")}
	res, handled := m.Prime(model.Node{}, content, intentVars(map[string]string{
		"producto": "torta de chocolate 25-30 porciones", "cantidad": "2",
	}))
	if !handled {
		t.Fatal("con producto y variante claros debe pre-cargar")
	}
	st := loadState(res.Vars)
	if st.Level != LevelContinue || len(st.Lines) != 1 {
		t.Fatalf("esperaba la confirmación con una línea, got %+v", st)
	}
	line := st.Lines[0]
	if line.SKU != "TORTA-CHOC#V2" || line.UnitPrice != 32000 || line.Qty != 2 {
		t.Fatalf("la línea pre-agregada debe ser la de la variante nombrada, got %+v", line)
	}
	mustContain(t, res.Outputs, "Agregué 2 × Torta de chocolate — 25-30 porciones ($32000.00 c/u)")
	if got := effectNames(res.Effects); len(got) != 2 || got[1] != EffectItemAdded {
		t.Fatalf("mismos efectos que el add manual, got %v", got)
	}
}

// TestPrime_ArticuloClaroVarianteNo_PreguntaSinInventar: "una torta de chocolate"
// identifica el artículo pero no el tamaño, y los tamaños valen distinto. Se
// PREGUNTA: ni se inventa el precio ni se tira lo que el cliente ya dijo.
func TestPrime_ArticuloClaroVarianteNo_PreguntaSinInventar(t *testing.T) {
	m := New()
	content := model.Content{Raw: rawFromFile(t, "catalog_v2.json")}
	res, handled := m.Prime(model.Node{}, content, intentVars(map[string]string{"producto": "torta de chocolate"}))
	if !handled {
		t.Fatal("con intent_params Prime maneja")
	}
	st := loadState(res.Vars)
	if st.Level != LevelVariant || st.SKU != "TORTA-CHOC" || st.CatCode != "01" {
		t.Fatalf("debe quedar en el nivel de variante del artículo que casó, got %+v", st)
	}
	if len(st.Lines) != 0 {
		t.Fatalf("sin variante clara NO se agrega nada, got %+v", st.Lines)
	}
	if len(res.Effects) != 0 {
		t.Fatalf("sin línea no hay efectos (el cart_started lo emite el primer Step), got %v", effectNames(res.Effects))
	}
	if st.Started {
		t.Error("Started debe quedar false: el arranque lo marca el primer Step, como en el flujo normal")
	}
	mustContain(t, res.Outputs, "Torta de chocolate · elige una opción:", "1) 10-12 porciones")
}

// TestPrime_ArticuloSinVariantes_PreAgregaComoSiempre: no-regresión del Plan 029
// contra un catálogo v2 (el Café de este catálogo no tiene variantes).
func TestPrime_ArticuloSinVariantes_PreAgregaComoSiempre(t *testing.T) {
	m := New()
	content := model.Content{Raw: rawFromFile(t, "catalog_v2.json")}
	res, handled := m.Prime(model.Node{}, content, intentVars(map[string]string{"producto": "cafe"}))
	if !handled {
		t.Fatal("un artículo sin variantes se pre-agrega como siempre")
	}
	st := loadState(res.Vars)
	if st.Level != LevelContinue || len(st.Lines) != 1 || st.Lines[0].SKU != "CAFE" {
		t.Fatalf("esperaba la línea CAFE pre-agregada, got %+v", st)
	}
}

// TestPrime_ComboPorNombre_UnaLinea: pedir el combo por su nombre agrega UNA línea
// a su precio, sin desplegar componentes.
func TestPrime_ComboPorNombre_UnaLinea(t *testing.T) {
	m := New()
	content := model.Content{Raw: rawFromFile(t, "catalog_v2.json")}
	res, handled := m.Prime(model.Node{}, content, intentVars(map[string]string{"producto": "combo hamburguesa"}))
	if !handled {
		t.Fatal("el combo casa por su label")
	}
	st := loadState(res.Vars)
	if len(st.Lines) != 1 || st.Lines[0].SKU != "COMBO-1" || st.Lines[0].UnitPrice != 9500 {
		t.Fatalf("el combo es UNA línea a su precio, got %+v", st.Lines)
	}
}

// --- matching de variantes: qué es "match claro" ----------------------------

func TestMatchVariant(t *testing.T) {
	cat, err := ParseCatalog(model.Content{Raw: rawFromFile(t, "catalog_v2.json")})
	if err != nil {
		t.Fatalf("catálogo v2: %v", err)
	}
	torta := cat.Categories[0].Items[0]
	cafe := cat.Categories[1].Items[0]

	cases := []struct {
		name     string
		article  Article
		query    string
		wantCode string // "" ⇒ sin match claro
	}{
		{"la variante nombrada gana", torta, "torta de chocolate 25-30 porciones", "V2"},
		{"la otra variante", torta, "torta 10-12 porciones", "V1"},
		{"solo el artículo: ninguna variante casa", torta, "torta de chocolate", ""},
		{"palabra común a las dos: empate ⇒ sin match", torta, "torta porciones", ""},
		{"artículo sin variantes", cafe, "cafe", ""},
		{"consulta vacía", torta, "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := matchVariant(tc.article, tc.query)
			if tc.wantCode == "" {
				if ok {
					t.Fatalf("no debía haber match claro, got %+v", v)
				}
				return
			}
			if !ok || v.Code != tc.wantCode {
				t.Fatalf("quiero %q, got %+v (ok=%v)", tc.wantCode, v, ok)
			}
		})
	}
}

// --- transcripción del flujo con variantes ---------------------------------

// TestCartV2ConversationalTranscript deja escrita, literal, la conversación con
// variantes y con combo: es la evidencia del e2e conversacional de T2.3 y, a la
// vez, la red que sujeta estos textos de aquí en adelante.
func TestCartV2ConversationalTranscript(t *testing.T) {
	raw := rawFromFile(t, "catalog_v2.json")
	tr := &transcript{}
	m := New()

	tr.section("variantes · elegir la presentación antes de la cantidad")
	tr.render(m.Render(model.Node{}, model.Content{Raw: raw}))
	vars := map[string]any{catalogVarKey: raw}
	vars = driveTranscript(tr, m, vars, []string{
		"01", // Tortas
		"1",  // Torta de chocolate (con variantes)
		"2",  // agregar → pide la variante
		"0",  // volver a la ficha
		"2",  // agregar → pide la variante otra vez
		"9",  // no existe esa opción → reprompt
		"2",  // 25-30 porciones
		"0",  // volver: a las VARIANTES, no a la ficha
		"2",  // 25-30 porciones
		"3",  // cantidad 3
	})

	tr.section("combo · una sola línea al precio del combo")
	driveTranscript(tr, m, vars, []string{
		"1", // agregar más de Tortas
		"2", // el combo
		"2", // agregar → NO pide variante
		"1", // cantidad 1
		"2", // finalizar → resumen
		"1", // confirmar
	})

	assertGolden(t, "cart_v2_transcript.golden.txt", tr.b.String())
}

// --- el aviso del parseo tolerante llega al log -----------------------------

// logCapture es un logger.Logger mínimo que retiene los mensajes Warn.
type logCapture struct{ warns []string }

func (l *logCapture) Debug(string, ...any) {}
func (l *logCapture) Info(string, ...any)  {}
func (l *logCapture) Warn(msg string, args ...any) {
	parts := []string{msg}
	for _, a := range args {
		if s, ok := a.(string); ok {
			parts = append(parts, s)
		}
	}
	l.warns = append(l.warns, strings.Join(parts, " "))
}
func (l *logCapture) Error(string, ...any)      {}
func (l *logCapture) With(...any) logger.Logger { return l }

var _ logger.Logger = (*logCapture)(nil)

// TestRenderAvisaDeLosCamposDescartados: el catálogo a medias sigue vendiendo (se
// renderiza la lista de categorías) y el defecto NO se traga en silencio.
func TestRenderAvisaDeLosCamposDescartados(t *testing.T) {
	capture := &logCapture{}
	m := New(WithLogger(capture))
	content := model.Content{Raw: rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[
	  {"code":"1","sku":"A","label":"A","price":1,"tags":"no-soy-lista"}]}]}`)}

	outs := m.Render(model.Node{}, content)
	mustContain(t, outs, "Elige una categoría", "1) X")
	if len(capture.warns) != 1 || !strings.Contains(capture.warns[0], "tags") {
		t.Fatalf("esperaba un aviso sobre tags, got %v", capture.warns)
	}

	// Sin logger, el mismo catálogo se comporta EXACTAMENTE igual (el logger es
	// observabilidad, no lógica).
	if got := New().Render(model.Node{}, content); joined(got) != joined(outs) {
		t.Fatalf("el logger no debe cambiar lo que se muestra:\ncon: %q\nsin: %q", joined(outs), joined(got))
	}
}

// TestStepNoRepiteLosAvisos: Step re-parsea el snapshot en CADA mensaje; si
// avisara ahí, un catálogo roto llenaría el log una vez por tecleo.
func TestStepNoRepiteLosAvisos(t *testing.T) {
	capture := &logCapture{}
	m := New(WithLogger(capture))
	vars := map[string]any{catalogVarKey: rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[
	  {"code":"1","sku":"A","label":"A","price":1,"tags":"no-soy-lista"}]}]}`)}
	for i := 0; i < 3; i++ {
		_, _, vars = drive(t, m, vars, "1")
	}
	if len(capture.warns) != 0 {
		t.Fatalf("Step no debe avisar (lo hace Render/Prime al entrar), got %v", capture.warns)
	}
}

// TestPrimeAvisaDeLosCamposDescartados: el otro camino de entrada al nodo.
func TestPrimeAvisaDeLosCamposDescartados(t *testing.T) {
	capture := &logCapture{}
	m := New(WithLogger(capture))
	content := model.Content{Raw: rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[
	  {"code":"1","sku":"A","label":"Alfajor","price":1,"attributes":["no-soy-objeto"]}]}]}`)}
	if _, handled := m.Prime(model.Node{}, content, intentVars(map[string]string{"producto": "alfajor"})); !handled {
		t.Fatal("el alfajor casa: Prime maneja")
	}
	if len(capture.warns) != 1 || !strings.Contains(capture.warns[0], "attributes") {
		t.Fatalf("esperaba un aviso sobre attributes, got %v", capture.warns)
	}
}
