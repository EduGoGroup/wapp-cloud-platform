package cart_test

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// Aserciones de compilación: el carrito es un modules.Module con sus dos capacidades
// opcionales.
var (
	_ modules.Module        = cart.Module{}
	_ modules.Primer        = cart.Module{}
	_ modules.NodeValidator = cart.Module{}
)

const (
	catalogUnavailable = "El catálogo no está disponible en este momento. Intenta más tarde."
	categoriesScreen   = "🛒 Elige una categoría:\n1) Bebidas\n2) Postres"
	invalidPrefix      = "Opción no válida. Responde con el número de una de las opciones.\n\n"
)

// brokenCatalog tiene un campo v2 ilegible (tags no es una lista): el parseo
// tolerante lo descarta con UN aviso y el catálogo sigue vendiendo.
const brokenCatalog = `{"categories":[{"code":"1","label":"X","items":[
  {"code":"1","sku":"A","label":"Alfajor","price":1,"tags":"no-soy-lista"}]}]}`

// logCapture es un logger.Logger mínimo que retiene los Warn con sus atributos.
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

// Identidad del módulo: tipo de nodo, interactivo y con contenido durable.
func TestModule_Identity(t *testing.T) {
	m := cart.New()
	if got := m.Type(); got != cart.NodeTypeCart {
		t.Errorf("Type() = %q, quiero \"cart\"", got)
	}
	if !m.WaitsForInput() {
		t.Error("WaitsForInput() = false, quiero true: el carrito espera la entrada del cliente")
	}
	if !m.ProducesDurableContent() {
		t.Error("ProducesDurableContent() = false, quiero true: el carrito proyecta a intakes y exige evento padre")
	}
}

// New acepta las tres opciones, también con los valores que se ignoran.
func TestNew_AcceptsEveryOption(t *testing.T) {
	opts := []cart.Option{cart.WithLogger(nil), cart.WithMatchHook(nil), cart.WithPageSize(0)}
	mustScreen(t, cart.New(opts...).Render(model.Node{}, model.Content{Raw: catalogRaw()}), categoriesScreen)
}

// Render: la lista de categorías, página 0, sin «volver» (L1 es la raíz).
func TestRender_ShowsCategoriesPageZero(t *testing.T) {
	outs := cart.New().Render(model.Node{}, model.Content{Raw: catalogRaw()})
	mustScreen(t, outs, categoriesScreen)
}

// Render sin catálogo legible: la pantalla de catálogo no disponible.
func TestRender_CatalogUnavailable(t *testing.T) {
	cases := []struct {
		name    string
		content model.Content
	}{
		{name: "no raw", content: model.Content{}},
		{name: "raw without categories", content: model.Content{Raw: map[string]any{"otra": 1}}},
		{name: "categories of another type", content: model.Content{Raw: map[string]any{"categories": "no"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustScreen(t, cart.New().Render(model.Node{}, tc.content), catalogUnavailable)
		})
	}
}

// WithPageSize gobierna la página de Render; un valor <= 0 se ignora.
func TestWithPageSize_PaginatesRender(t *testing.T) {
	content := model.Content{Raw: catalogRaw()}
	// Una categoría por página: Bebidas y «Más ▾» con el código siguiente al mayor (3).
	const firstPage = "🛒 Elige una categoría:\n1) Bebidas\n3) Más ▾"
	mustScreen(t, cart.New(cart.WithPageSize(1)).Render(model.Node{}, content), firstPage)
	for _, n := range []int{0, -4} {
		mustScreen(t, cart.New(cart.WithPageSize(n)).Render(model.Node{}, content), categoriesScreen)
	}
	// La última válida manda; una inválida detrás no la deshace.
	m := cart.New(cart.WithPageSize(5), cart.WithPageSize(1), cart.WithPageSize(0))
	mustScreen(t, m.Render(model.Node{}, content), firstPage)
}

// El tamaño de página del Module vale en Step mientras el runtime no siembre el del
// tenant; sembrado, manda el del tenant.
func TestWithPageSize_StepUsesItUnlessTenantSeedsOne(t *testing.T) {
	m := cart.New(cart.WithPageSize(1))
	st, outs, _ := drive(t, m, seededVars(), "3") // «Más ▾» ⇒ página 1
	if st.Level != cart.LevelCategories || st.Page != 1 {
		t.Fatalf("estado = %+v, quiero categories página 1", st)
	}
	mustScreen(t, outs, "🛒 Elige una categoría:\n2) Postres")

	vars := seededVars()
	vars[cart.VarPageSize] = 5
	_, outs, _ = drive(t, m, vars, "3") // con 5 por página no hay «Más»: 3 no existe
	mustScreen(t, outs, invalidPrefix+categoriesScreen)
}

// WithLogger: Render avisa una vez por campo descartado, con sus atributos, y el
// logger no cambia lo que se muestra.
func TestWithLogger_RenderWarnsAboutDiscardedFields(t *testing.T) {
	capture := &logCapture{}
	content := model.Content{Raw: rawFromJSON(t, brokenCatalog)}
	outs := cart.New(cart.WithLogger(capture)).Render(model.Node{}, content)
	mustScreen(t, outs, "🛒 Elige una categoría:\n1) X")
	if len(capture.warns) != 1 {
		t.Fatalf("avisos = %q, quiero uno", capture.warns)
	}
	for _, want := range []string{"cart: campo del catálogo v2 descartado", "categoria", "sku", "campo", "tags", "motivo"} {
		if !strings.Contains(capture.warns[0], want) {
			t.Errorf("el aviso %q no lleva %q", capture.warns[0], want)
		}
	}
	others := []cart.Module{
		cart.New(),
		cart.New(cart.WithLogger(nil)),
		cart.New(cart.WithLogger(capture), cart.WithLogger(nil)),
	}
	for _, m := range others {
		if got := m.Render(model.Node{}, content); joined(got) != joined(outs) {
			t.Errorf("el logger no debe cambiar la pantalla: %q frente a %q", got, outs)
		}
	}
	if len(capture.warns) != 2 {
		t.Errorf("avisos = %d, quiero 2: un logger nil detrás no retira al anterior", len(capture.warns))
	}
}

// Step NO avisa: re-parsea el snapshot en cada mensaje.
func TestWithLogger_StepDoesNotRepeatWarnings(t *testing.T) {
	capture := &logCapture{}
	m := cart.New(cart.WithLogger(capture))
	vars := map[string]any{modules.VarContentRaw: rawFromJSON(t, brokenCatalog)}
	for range 3 {
		vars = m.Step(model.Node{}, model.Conversation{Vars: vars}, "1").Vars
	}
	if st := stateOf(t, vars); st.Level != cart.LevelArticle {
		t.Fatalf("estado = %+v, quiero haber navegado hasta la ficha", st)
	}
	if len(capture.warns) != 0 {
		t.Errorf("avisos = %q, quiero ninguno desde Step", capture.warns)
	}
}

// WithMatchHook: la cascada avisa una vez con escalón y nivel acotados; un código
// tecleado no pasa por ella y no deja rastro; un hook nil no rompe nada.
func TestWithMatchHook_ObservesTheCascadeOnly(t *testing.T) {
	type call struct{ step, level string }
	var calls []call
	m := cart.New(cart.WithMatchHook(func(step, level string) { calls = append(calls, call{step, level}) }))

	_, _, vars := drive(t, m, seededVars(), "1") // código exacto
	if len(calls) != 0 {
		t.Fatalf("llamadas = %+v, quiero ninguna: un código no pasa por la cascada", calls)
	}
	st, _, _ := drive(t, m, vars, "café") // etiqueta exacta en el nivel de artículos
	if st.Level != cart.LevelArticle || st.SKU != "CAFE" {
		t.Fatalf("estado = %+v, quiero la ficha del Café", st)
	}
	if len(calls) != 1 || calls[0] != (call{"exact", cart.LevelArticles}) {
		t.Fatalf("llamadas = %+v, quiero una sola {exact articles}", calls)
	}

	st, _, _ = drive(t, cart.New(cart.WithMatchHook(nil)), vars, "café")
	if st.SKU != "CAFE" {
		t.Errorf("estado = %+v: sin observador la cascada resuelve igual", st)
	}
}
