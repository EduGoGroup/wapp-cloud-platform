package cart

// troceo_test.go — LOS DOS FIXTURES DEL CRITERIO, Y LO QUE PROTEGEN
// (Plan 044 · Ola 3.5 · T3.5-3).
//
// Los dos fixtures NO son inventados: son los turnos que se midieron en campo el
// 2026-08-17 perdiendo productos (journal §13.3 y 051/O2-detalle.md), transcritos
// literalmente. El primero pierde las 2 pizzas y la cantidad del superviviente; el
// segundo pierde tres de cuatro pedidos. Aquí se fija que ya no.
//
// 🔴 LO QUE ESTOS TESTS **NO** PRUEBAN: cuántas llamadas al modelo se hacen. En estos
// dos fixtures son CERO —la cascada casa las cuatro etiquetas— y eso es el diseño
// funcionando (I2 del ADR-0046: el LLM es el último peldaño). El contador de llamadas
// vive donde se hacen, que es turnoacotado/troceado_test.go, y aquí lo que se fija es
// su NEGATIVO: que un turno que la cascada resuelve entero no pide consulta ninguna.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// catalogTroceoRaw es un catálogo de cuatro categorías de UN artículo cada una, con
// las etiquetas EN PLURAL que un negocio de comida real usa.
//
// ⚠️ El plural no es un capricho del fixture y conviene que quede escrito: con la
// etiqueta «Pizza» en singular, «pizzas» NO casa —la distancia normalizada da
// 1 − 1/6 = 0,833 y el umbral es 0,85 (D-044.45)— y el trozo subiría al modelo. Es
// exactamente el umbral haciendo lo que se decidió que hiciera, no un defecto: una
// palabra corta no perdona una runa de diferencia.
func catalogTroceoRaw() map[string]any {
	return map[string]any{
		"categories": []any{
			map[string]any{"code": "1", "label": "Pizzas", "items": []any{
				map[string]any{"code": "1", "sku": "PIZZA", "label": "Pizzas", "price": 10.0},
			}},
			map[string]any{"code": "2", "label": "Hamburguesas", "items": []any{
				map[string]any{"code": "1", "sku": "HAMB", "label": "Hamburguesas", "price": 6.0},
			}},
			map[string]any{"code": "3", "label": "Empanadas", "items": []any{
				map[string]any{"code": "1", "sku": "EMPA", "label": "Empanadas", "price": 2.0},
			}},
			map[string]any{"code": "4", "label": "Jugos", "items": []any{
				map[string]any{"code": "1", "sku": "JUGO", "label": "Jugos", "price": 3.0},
			}},
		},
	}
}

func conTroceo(t *testing.T) (Module, *espia, map[string]any) {
	t.Helper()
	e := &espia{}
	return New(WithMatchHook(e.rec)), e, map[string]any{catalogVarKey: catalogTroceoRaw()}
}

// pedido resume las líneas del carrito en pares (sku, qty) para poder afirmar el
// pedido entero de una vez en vez de línea a línea.
func pedido(st cartState) [][2]any {
	out := make([][2]any, 0, len(st.Lines))
	for _, l := range st.Lines {
		out = append(out, [2]any{l.SKU, l.Qty})
	}
	return out
}

func mismoPedido(t *testing.T, st cartState, quiero [][2]any) {
	t.Helper()
	got := pedido(st)
	if len(got) != len(quiero) {
		t.Fatalf("pedido = %v, quiero %v", got, quiero)
	}
	for i := range quiero {
		if got[i] != quiero[i] {
			t.Fatalf("pedido = %v, quiero %v", got, quiero)
		}
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 1 · el fixture que PERDÍA las 2 pizzas
// ---------------------------------------------------------------------------

// TestFixture2026_08_17_DosProductosConSusCantidades es el criterio literal de
// T3.5-3 sobre el turno agrupado que se midió perdiendo información.
//
// Lo que el campo devolvía: `{"producto":"hamburguesa"}` — UN producto, SIN cantidad.
// Lo que se exige aquí: los DOS productos, cada uno con SU cantidad, y el ruido
// («hola», «para llevar») descartado sin convertirse en nada.
//
// 💥 MUTACIONES EJECUTADAS (las cuatro rojas, ver el informe de la tarea):
//   - `minTrozosParaTrocear = 99` ⇒ el troceado no se activa y vuelve la pérdida.
//   - en `cantidadDe`, devolver siempre `qty=1` ⇒ cae la aserción de las 2 pizzas.
//   - en `interpretar`, quitar el filtro `idx < 0 && !explicit` ⇒ «hola» y «para
//     llevar» entran como peticiones y el pedido sale con líneas de más.
//   - en `recomponer`, `break` tras la primera línea ⇒ un solo producto, que es
//     exactamente la avería que esta tarea arregla.
func TestFixture2026_08_17_DosProductosConSusCantidades(t *testing.T) {
	m, e, vars := conTroceo(t)
	// El lote agrupado tal como el Edge lo concatena: los cuatro mensajes sueltos que
	// la clienta mandó, en su orden.
	st, outs, _ := drive(t, m, vars, "hola\nquiero 2 pizzas\ny una hamburguesa\npara llevar")

	mismoPedido(t, st, [][2]any{{"PIZZA", 2}, {"HAMB", 1}})
	if st.Level != LevelContinue {
		t.Fatalf("nivel = %q, quiero %q (la confirmación de ítem)", st.Level, LevelContinue)
	}
	mustContain(t, outs, "2 × Pizzas", "1 × Hamburguesas")
	if strings.Contains(joined(outs), "No pude identificar") {
		t.Fatalf("nada se quedó fuera y la pantalla dice que sí: %q", joined(outs))
	}
	// El NEGATIVO del contador de llamadas: la cascada resolvió los dos trozos, así
	// que este turno no pidió ninguna consulta. Cero inferencias.
	for _, v := range e.visto {
		if v[0] != escalonTroceo {
			t.Fatalf("telemetría = %v, quiero solo %q (ningún trozo debía escalar)", e.visto, escalonTroceo)
		}
	}
	if len(e.visto) != 2 {
		t.Fatalf("telemetría = %v, quiero DOS trozos contados", e.visto)
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 2 · cero pérdida con CUATRO pedidos distintos
// ---------------------------------------------------------------------------

// TestFixture2026_08_17_CuatroPedidosCeroPerdida es el segundo fixture medido: en
// campo devolvía `{cantidad:7, producto:pizzas}` y perdía los otros TRES.
//
// 🔴 Y ES EL TEST QUE JUSTIFICA QUE EL TOPE SEA DE LLAMADAS Y NO DE TROZOS. Con un
// tope de 3 TROZOS este caso perdería el cuarto producto para proteger un recurso
// —la plaza única del Edge— que este turno no gasta: la cascada casa las cuatro
// etiquetas y no hay una sola inferencia que acotar. Ver turnoacotado/troceado.go.
func TestFixture2026_08_17_CuatroPedidosCeroPerdida(t *testing.T) {
	m, _, vars := conTroceo(t)
	st, outs, _ := drive(t, m, vars, "7 pizzas, 2 hamburguesas, 9 empanadas y 4 jugos")

	mismoPedido(t, st, [][2]any{{"PIZZA", 7}, {"HAMB", 2}, {"EMPA", 9}, {"JUGO", 4}})
	mustContain(t, outs, "7 × Pizzas", "2 × Hamburguesas", "9 × Empanadas", "4 × Jugos")
}

// ---------------------------------------------------------------------------
// El trozo que SÍ escala, y la aduana de lo que vuelve
// ---------------------------------------------------------------------------

// TestTrozoSinCasarSubeComoConsultaConTrozos fija la mitad que cuesta dinero: un
// trozo con cantidad explícita cuyo producto la cascada NO sabe casar sube al
// resolutor —y sube SOLO ÉL, dentro de una consulta con Trozos—, mientras el que sí
// casó se queda resuelto en Go y no gasta nada.
func TestTrozoSinCasarSubeComoConsultaConTrozos(t *testing.T) {
	m, _, vars := conTroceo(t)
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "2 pizzas y 3 napolitanas")
	if res.Consulta == nil {
		t.Fatal("no se elevó consulta: el trozo sin casar tiene que subir")
	}
	if got := res.Consulta.Trozos; len(got) != 1 || got[0] != "napolitanas" {
		t.Fatalf("Trozos = %v, quiero solo el que la cascada no casó", got)
	}
	if len(res.Consulta.Opciones) != 4 {
		t.Fatalf("Opciones = %d, quiero los 4 artículos del catálogo entero", len(res.Consulta.Opciones))
	}
	// El estado NO se tocó: la petición viaja en un Result que el engine descarta.
	if st := loadState(res.Vars); len(st.Lines) != 0 || st.Started {
		t.Fatalf("la primera pasada mutó el estado: %+v", st)
	}
	if len(res.Effects) != 0 {
		t.Fatalf("la primera pasada declaró efectos: %+v", res.Effects)
	}
}

// TestVeredictoPorTrozoSeAplicaYSeValida fija la segunda pasada: el código que vuelve
// se aplica, y solo si es uno de los que el propio módulo ofreció. Es la misma aduana
// que codigoAdmisible aplica al veredicto de un solo texto.
func TestVeredictoPorTrozoSeAplicaYSeValida(t *testing.T) {
	m, _, vars := conTroceo(t)
	// La opción 0 del catálogo aplanado es «Pizzas»; la 2, «Empanadas».
	res := turno(m, model.Conversation{Vars: vars}, "2 pizzas y 3 napolitanas", func(modules.Consulta) modules.Veredicto {
		return modules.Veredicto{Codigos: []string{"2"}}
	})
	mismoPedido(t, loadState(res.Vars), [][2]any{{"PIZZA", 2}, {"EMPA", 3}})
}

// TestCodigoInventadoNoEntraEnElPedido es la aduana en negativo: un resolutor que
// devuelve una posición que no existe no mete nada en el pedido de nadie, y el trozo
// se cuenta como no identificado.
func TestCodigoInventadoNoEntraEnElPedido(t *testing.T) {
	m, e, vars := conTroceo(t)
	res := turno(m, model.Conversation{Vars: vars}, "2 pizzas y 3 napolitanas", func(modules.Consulta) modules.Veredicto {
		return modules.Veredicto{Codigos: []string{"99"}}
	})
	mismoPedido(t, loadState(res.Vars), [][2]any{{"PIZZA", 2}})
	mustContain(t, res.Outputs, "No pude identificar 1 producto más")
	var perdidos int
	for _, v := range e.visto {
		if v[0] == escalonTroceoPerdido {
			perdidos++
		}
	}
	if perdidos != 1 {
		t.Fatalf("telemetría = %v, quiero UN trozo contado como perdido", e.visto)
	}
}

// ---------------------------------------------------------------------------
// Regresión cero: el troceado se aparta
// ---------------------------------------------------------------------------

// TestUnSoloProductoNoActivaElTroceado fija que el camino de siempre sigue siendo el
// camino de siempre. Con una sola petición el troceado devuelve ok=false y quien
// decide es el pre-resolutor de T3.5-1: aquí «pizzas» navega a su categoría, que es
// lo que hacía ayer.
func TestUnSoloProductoNoActivaElTroceado(t *testing.T) {
	m, _, vars := conTroceo(t)
	st, _, _ := drive(t, m, vars, "quiero pizzas")
	if st.Level != LevelArticles || st.CatCode != "1" {
		t.Fatalf("esperaba navegar a la categoría Pizzas, got %+v", st)
	}
	if len(st.Lines) != 0 {
		t.Fatalf("el troceado agregó líneas con UNA sola petición: %v", pedido(st))
	}
}

// TestFueraDeCategoriasNoSeTrocea fija el alcance: el troceado aterriza en el arranque
// del carrito y en ningún otro nivel. Dentro de una categoría el cliente está
// eligiendo, no pidiendo, y el número que teclea tiene que seguir siendo un índice.
func TestFueraDeCategoriasNoSeTrocea(t *testing.T) {
	m, _, vars := conTroceo(t)
	storeState(vars, cartState{Level: LevelArticles, CatCode: "1", Started: true})
	st, _, _ := drive(t, m, vars, "7 pizzas, 2 hamburguesas y 9 empanadas")
	if len(st.Lines) != 0 {
		t.Fatalf("se troceó fuera del nivel de categorías: %v", pedido(st))
	}
}

// TestElRuidoSoloNoTrocea comprueba el filtro de la cabecera en su caso puro: un
// turno que es TODO ruido no produce peticiones, no eleva consulta y por tanto no
// gasta ni una llamada del presupuesto.
func TestElRuidoSoloNoTrocea(t *testing.T) {
	m, _, vars := conTroceo(t)
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "hola, buenas tardes y gracias")
	if res.Consulta != nil && len(res.Consulta.Trozos) > 0 {
		t.Fatalf("el ruido subió al modelo como trozos: %v", res.Consulta.Trozos)
	}
	if len(loadState(res.Vars).Lines) != 0 {
		t.Fatal("el ruido acabó en el pedido")
	}
}

// ---------------------------------------------------------------------------
// Las piezas puras, por separado
// ---------------------------------------------------------------------------

// TestTrocear fija la mitad «Go descompone»: qué corta y —tan importante— qué NO
// corta. La «y» dentro de una palabra no es un separador.
func TestTrocear(t *testing.T) {
	casos := []struct {
		in     string
		quiero []string
	}{
		{"hola\nquiero 2 pizzas\ny una hamburguesa\npara llevar",
			[]string{"hola", "quiero 2 pizzas", "y una hamburguesa", "para llevar"}},
		{"7 pizzas, 2 hamburguesas, 9 empanadas y 4 jugos",
			[]string{"7 pizzas", "2 hamburguesas", "9 empanadas", "4 jugos"}},
		// La palabra «yogur» empieza por y y «empanadas» por e: si los separadores no
		// llevaran espacios a los dos lados, estas dos se partirían por la mitad.
		{"un yogur y unas empanadas", []string{"un yogur", "unas empanadas"}},
		{"solo una cosa", []string{"solo una cosa"}},
	}
	for _, c := range casos {
		got := trocear(c.in)
		if strings.Join(got, "|") != strings.Join(c.quiero, "|") {
			t.Errorf("trocear(%q) = %v, quiero %v", c.in, got, c.quiero)
		}
	}
}

// TestCantidadDe fija la aritmética del lenguaje que Go SÍ hace (y que la cascada
// determinista se negaba a hacer, con razón, en su sitio).
func TestCantidadDe(t *testing.T) {
	casos := []struct {
		in       string
		qty      int
		resto    string
		explicit bool
	}{
		{"quiero 2 pizzas", 2, "quiero pizzas", true},
		{"una hamburguesa", 1, "hamburguesa", true},
		{"un par de empanadas", 2, "de empanadas", true}, // «un» gana por ser el primero
		{"pizzas", 1, "pizzas", false},
		{"2 empanadas de 3 quesos", 2, "empanadas de 3 quesos", true},
	}
	for _, c := range casos {
		qty, resto, explicit := cantidadDe(strings.Fields(c.in))
		if qty != c.qty || strings.Join(resto, " ") != c.resto || explicit != c.explicit {
			t.Errorf("cantidadDe(%q) = (%d, %q, %v), quiero (%d, %q, %v)",
				c.in, qty, strings.Join(resto, " "), explicit, c.qty, c.resto, c.explicit)
		}
	}
}
