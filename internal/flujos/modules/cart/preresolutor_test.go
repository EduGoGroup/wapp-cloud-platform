package cart

import (
	"strings"
	"testing"
)

// catalogCascadaRaw es el catálogo de esta batería: una categoría con un artículo
// de nombre largo (para que la errata quepa bajo el umbral 0,85) y otra con DOS
// artículos que comparten primera palabra (para fijar la ambigüedad).
func catalogCascadaRaw() map[string]any {
	return map[string]any{
		"categories": []any{
			map[string]any{"code": "1", "label": "Hamburguesas", "items": []any{
				map[string]any{"code": "1", "sku": "HAMB", "label": "Hamburguesa", "price": 5.0},
				map[string]any{"code": "2", "sku": "PAPAS", "label": "Papas fritas", "price": 2.0},
			}},
			map[string]any{"code": "2", "label": "Postres", "items": []any{
				map[string]any{"code": "1", "sku": "TCHOC", "label": "Torta de chocolate", "price": 4.0},
				map[string]any{"code": "2", "sku": "TVAIN", "label": "Torta de vainilla", "price": 4.0},
			}},
		},
	}
}

// espia recoge lo que el hook de telemetría publicaría. Existe para poder afirmar
// también el NEGATIVO —que la cascada NO corrió—, que es la mitad de esta tarea.
type espia struct{ visto [][2]string }

func (e *espia) rec(escalon, nivel string) { e.visto = append(e.visto, [2]string{escalon, nivel}) }

// conCascada monta el módulo con el espía y las Vars con el catálogo sembrado.
func conCascada(t *testing.T) (Module, *espia, map[string]any) {
	t.Helper()
	e := &espia{}
	return New(WithMatchHook(e.rec)), e, map[string]any{catalogVarKey: catalogCascadaRaw()}
}

func (e *espia) soloVio(t *testing.T, escalon, nivel string) {
	t.Helper()
	if len(e.visto) != 1 || e.visto[0] != [2]string{escalon, nivel} {
		t.Fatalf("telemetría = %v, quiero exactamente [{%s %s}]", e.visto, escalon, nivel)
	}
}

func (e *espia) noVioNada(t *testing.T) {
	t.Helper()
	if len(e.visto) != 0 {
		t.Fatalf("la cascada NO debía correr y publicó %v", e.visto)
	}
}

// --- Regresión cero: el código exacto sigue por el camino de siempre ---------

// TestCodigoExactoNoPasaPorLaCascada fija la puerta 2 de la regla de oro: quien
// teclea el número navega EXACTAMENTE como antes de esta tarea, y además ni
// siquiera se paga el coste de comparar (la telemetría queda muda porque la
// cascada no corrió).
func TestCodigoExactoNoPasaPorLaCascada(t *testing.T) {
	m, e, vars := conCascada(t)
	st, outs, _ := drive(t, m, vars, "2")
	if st.Level != LevelArticles || st.CatCode != "2" {
		t.Fatalf("esperaba articles/Postres, got %+v", st)
	}
	mustContain(t, outs, "1) Torta de chocolate")
	e.noVioNada(t)
}

// TestNumeroNuncaSeInterpretaComoEtiqueta protege la paginación y el resto de
// códigos: un número que NO corresponde a ninguna opción tampoco se ofrece a la
// cascada, porque un número tecleado es un índice de pantalla, no un nombre.
func TestNumeroNuncaSeInterpretaComoEtiqueta(t *testing.T) {
	m, e, vars := conCascada(t)
	st, outs, _ := drive(t, m, vars, "77")
	if st.Level != LevelCategories {
		t.Fatalf("un número inválido debe repromptar en L1, got %+v", st)
	}
	mustContain(t, outs, "Opción no válida")
	e.noVioNada(t)
}

// --- Los tres escalones -----------------------------------------------------

// TestEtiquetaExactaResuelvePorExact: el cliente escribe el nombre tal cual.
// Resuelve por el escalón barato, sin llegar al fuzzy.
func TestEtiquetaExactaResuelvePorExact(t *testing.T) {
	m, e, vars := conCascada(t)
	st, _, _ := drive(t, m, vars, "postres")
	if st.Level != LevelArticles || st.CatCode != "2" {
		t.Fatalf("«postres» debía resolver la categoría 2, got %+v", st)
	}
	e.soloVio(t, "exact", LevelCategories)
}

// TestErrataResuelvePorFuzzy es el caso que da nombre a la tarea: «hamburgesa»
// —una letra de menos sobre 11 runas: 0,909 contra el umbral 0,85— resuelve sin
// preguntarle a nadie.
func TestErrataResuelvePorFuzzy(t *testing.T) {
	m, e, vars := conCascada(t)
	_, _, vars = drive(t, m, vars, "1") // L1 → Hamburguesas
	st, outs, _ := drive(t, m, vars, "hamburgesa")
	if st.Level != LevelArticle || st.SKU != "HAMB" {
		t.Fatalf("«hamburgesa» debía resolver el artículo HAMB, got %+v", st)
	}
	mustContain(t, outs, "Agregar al pedido")
	// UNA sola entrada, no dos: el "1" del turno anterior era un CÓDIGO y la
	// cascada ni corrió. Ese negativo es tan parte del contrato como el positivo.
	e.soloVio(t, "fuzzy", LevelArticles)
}

// TestFraseConCantidadResuelve: la elección viaja DENTRO de una frase. Se casa por
// ventanas de tokens, y el «2» de la cantidad no compite contra las etiquetas.
func TestFraseConCantidadResuelve(t *testing.T) {
	m, e, vars := conCascada(t)
	_, _, vars = drive(t, m, vars, "1") // L1 → Hamburguesas
	st, _, _ := drive(t, m, vars, "agrega 2 hamburguesas")
	if st.Level != LevelArticle || st.SKU != "HAMB" {
		t.Fatalf("«agrega 2 hamburguesas» debía resolver HAMB, got %+v", st)
	}
	// UNA sola entrada, no dos: el "1" del turno anterior era un CÓDIGO y la
	// cascada ni corrió. Ese negativo es tan parte del contrato como el positivo.
	e.soloVio(t, "fuzzy", LevelArticles)
}

// TestPalabraDeMenuResuelveElControl: «cancelar» sobre el menú de continuar. La
// cascada no solo entiende el catálogo: también las opciones fijas de pantalla.
func TestPalabraDeMenuResuelveElControl(t *testing.T) {
	m, e, vars := conCascada(t)
	vars[catalogVarKey] = catalogCascadaRaw()
	storeState(vars, cartState{Level: LevelContinue, CatCode: "1", Started: true,
		Lines: []cartLine{{SKU: "HAMB", Label: "Hamburguesa", Qty: 1, UnitPrice: 5}}})
	st, _, _ := drive(t, m, vars, "quiero cancelar")
	if st.Level != LevelCancelled {
		t.Fatalf("«quiero cancelar» debía cancelar el pedido, got %+v", st)
	}
	e.soloVio(t, "exact", LevelContinue)
}

// --- Lo que NO debe resolver -------------------------------------------------

// TestTextoSinCorrespondenciaNoResuelve: la cascada corre, no encuentra nada y
// devuelve el input intacto ⇒ el carrito repromptea EXACTAMENTE como el día antes
// de esta tarea. El «ninguno» es el que mide cuánto trabajo le queda al turno LLM.
func TestTextoSinCorrespondenciaNoResuelve(t *testing.T) {
	m, e, vars := conCascada(t)
	st, outs, _ := drive(t, m, vars, "quiero algo rico")
	if st.Level != LevelCategories {
		t.Fatalf("no debía navegar a ningún sitio, got %+v", st)
	}
	mustContain(t, outs, "Opción no válida", "Elige una categoría")
	e.soloVio(t, escalonNinguno, LevelCategories)
}

// TestAmbiguedadNoResuelve: «torta» con «Torta de chocolate» y «Torta de vainilla»
// en pantalla no es una elección, es una pregunta. Adivinar metería el artículo
// equivocado en el pedido de alguien, así que el empate NO resuelve.
func TestAmbiguedadNoResuelve(t *testing.T) {
	m, e, vars := conCascada(t)
	_, _, vars = drive(t, m, vars, "2") // L1 → Postres
	st, outs, _ := drive(t, m, vars, "torta")
	if st.Level != LevelArticles {
		t.Fatalf("un empate no debe navegar, got %+v", st)
	}
	mustContain(t, outs, "Opción no válida")
	e.soloVio(t, escalonNinguno, LevelArticles)
}

// TestDesempatePorTokensConsumidos: «torta de vainilla» sí resuelve, porque la
// coincidencia de 3 tokens le gana a cualquier coincidencia de 1.
func TestDesempatePorTokensConsumidos(t *testing.T) {
	m, _, vars := conCascada(t)
	_, _, vars = drive(t, m, vars, "2")
	st, _, _ := drive(t, m, vars, "torta de vainilla")
	if st.Level != LevelArticle || st.SKU != "TVAIN" {
		t.Fatalf("«torta de vainilla» debía resolver TVAIN, got %+v", st)
	}
}

// TestProsaLargaNoSeCompara: por encima de maxTokensEntrada no es una elección de
// menú, es prosa — y barrerla contra el catálogo cuesta el producto de las dos
// longitudes. Ni se compara, ni se cuenta.
func TestProsaLargaNoSeCompara(t *testing.T) {
	m, e, vars := conCascada(t)
	larga := strings.TrimSpace(strings.Repeat("hola ", maxTokensEntrada+1))
	st, _, _ := drive(t, m, vars, larga)
	if st.Level != LevelCategories {
		t.Fatalf("la prosa no debe navegar, got %+v", st)
	}
	e.noVioNada(t)
}

// --- 🔴 PRIVACIDAD: los niveles de texto libre NO pasan por la cascada --------

// TestNivelesDeTextoLibreNuncaPasanPorLaCascada es un REQUISITO DE PRIVACIDAD, no
// una omisión: item_note, order_note y sobre todo buyer_data recogen texto libre
// del cliente —y buyer_data, datos personales que se escriben CIFRADOS—. Ese texto
// no se compara contra nada, no se traduce a ningún código y no toca la
// telemetría. La entrada sale por donde entró, byte a byte.
//
// El input de la tabla está elegido a mala idea: casaría una opción de OTRO nivel.
func TestNivelesDeTextoLibreNuncaPasanPorLaCascada(t *testing.T) {
	cat, err := loadCatalog(map[string]any{catalogVarKey: catalogCascadaRaw()})
	if err != nil {
		t.Fatalf("catálogo: %v", err)
	}
	linea := []cartLine{{SKU: "HAMB", Label: "Hamburguesa", Qty: 2, UnitPrice: 5}}
	for _, nivel := range []string{LevelItemNote, LevelOrderNote, LevelBuyerData} {
		t.Run(nivel, func(t *testing.T) {
			st := cartState{Level: nivel, CatCode: "1", SKU: "HAMB", Lines: linea}
			if ops := opcionesDelNivel(cat, st); ops != nil {
				t.Fatalf("%s NO puede ofrecer opciones a la cascada, got %v", nivel, ops)
			}
			for _, in := range []string{"cancelar pedido", "hamburgesa", "Torta de chocolate", "1"} {
				salida, escalon := preresolve(cat, st, in)
				if salida != in {
					t.Fatalf("%s: la entrada %q salió como %q", nivel, in, salida)
				}
				if escalon != "" {
					t.Fatalf("%s: la entrada %q dejó telemetría %q (la cascada corrió)", nivel, in, escalon)
				}
			}
		})
	}
}

// TestBuyerDataNoAvisaAlObservador es el mismo invariante visto desde Step, que es
// por donde entra el dato real: un texto que casaría una opción de otro nivel se
// entrega a stepBuyerData intacto y no publica nada.
func TestBuyerDataNoAvisaAlObservador(t *testing.T) {
	m, e, vars := conCascada(t)
	vars[VarBuyerFields] = []any{map[string]any{"key": "nombre", "label": "Nombre", "required": true}}
	storeState(vars, cartState{Level: LevelBuyerData, Started: true,
		Lines: []cartLine{{SKU: "HAMB", Label: "Hamburguesa", Qty: 1, UnitPrice: 5}}})
	drive(t, m, vars, "cancelar pedido")
	e.noVioNada(t)
}

// --- Cobertura de los 13 niveles: la exclusión es por CONSTRUCCIÓN ------------

// TestOpcionesDelNivelCubreLosTreceNiveles enumera TODOS los niveles de state.go y
// fija cuáles admiten cascada y cuáles no. Es el test que impide que un nivel
// nuevo entre en la cascada por descuido: opcionesDelNivel es fail-closed (su
// `default` devuelve nil), así que un nivel que nadie declare aquí queda fuera —y
// si alguien lo declara sin pensarlo, esta tabla se pone roja.
func TestOpcionesDelNivelCubreLosTreceNiveles(t *testing.T) {
	cat, err := loadCatalog(map[string]any{catalogVarKey: catalogCascadaRaw()})
	if err != nil {
		t.Fatalf("catálogo: %v", err)
	}
	// Estado con TODO lo que cada nivel pueda necesitar para poder ofrecer sus
	// opciones: categoría y artículo en foco, y una línea con qty > 1.
	base := cartState{CatCode: "1", SKU: "HAMB",
		Lines: []cartLine{{SKU: "HAMB", Label: "Hamburguesa", Qty: 2, UnitPrice: 5}}}
	admite := map[string]bool{
		LevelCategories:    true,
		LevelArticles:      true,
		LevelArticle:       true,
		LevelVariant:       false, // El artículo del fixture no tiene variantes.
		LevelContinue:      true,
		LevelSummary:       true,
		LevelItemNoteScope: true,
		LevelQuantity:      false, // Aritmética del lenguaje, no similitud: es del LLM.
		LevelItemNote:      false, // Texto libre.
		LevelOrderNote:     false, // Texto libre.
		LevelBuyerData:     false, // 🔴 Datos personales cifrados.
		LevelClosed:        false, // Terminal.
		LevelCancelled:     false, // Terminal.
	}
	if len(admite) != 13 {
		t.Fatalf("la tabla cubre %d niveles y state.go declara 13", len(admite))
	}
	for nivel, quiero := range admite {
		st := base
		st.Level = nivel
		if got := len(opcionesDelNivel(cat, st)) > 0; got != quiero {
			t.Fatalf("nivel %s: ofrece opciones = %v, quiero %v", nivel, got, quiero)
		}
	}
	// Y un nivel INVENTADO cae por el default: fail-closed.
	st := base
	st.Level = "nivel_que_no_existe_todavia"
	if ops := opcionesDelNivel(cat, st); ops != nil {
		t.Fatalf("un nivel desconocido debe quedar FUERA de la cascada, got %v", ops)
	}
}

// TestVarianteResuelvePorEtiqueta comprueba el nivel que numera POR POSICIÓN: lo
// que la cascada devuelve es el ordinal de pantalla, no Variant.Code (que es un
// identificador de negocio que nadie teclea).
func TestVarianteResuelvePorEtiqueta(t *testing.T) {
	raw := map[string]any{"categories": []any{
		map[string]any{"code": "1", "label": "Postres", "items": []any{
			map[string]any{"code": "1", "sku": "TORTA", "label": "Torta", "price": 10.0,
				"variants": []any{
					map[string]any{"code": "V1", "label": "Pequeña", "price": 10.0},
					map[string]any{"code": "V2", "label": "Familiar", "price": 20.0},
				}},
		}},
	}}
	cat, err := loadCatalog(map[string]any{catalogVarKey: raw})
	if err != nil {
		t.Fatalf("catálogo: %v", err)
	}
	st := cartState{Level: LevelVariant, CatCode: "1", SKU: "TORTA"}
	salida, escalon := preresolve(cat, st, "la familiar")
	if salida != "2" || escalon != "exact" {
		t.Fatalf("«la familiar» = (%q,%q), quiero (\"2\",\"exact\")", salida, escalon)
	}
}
