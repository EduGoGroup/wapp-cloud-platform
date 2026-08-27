// notes_test.go prueba las DOS indicaciones del cliente en el cart numérico
// (Plan 041 · T4.1c, D-041.19 + D-041.20): la de línea —que va a
// intake_items.customization— y la de pedido —que va a intakes.customer_note—,
// más el saneo compartido (SanitizeNote) y el split de línea con qty > 1.
//
// El PRIMER test del archivo es la regresión de teclas (INV-15) y está escrito a
// propósito antes que la funcionalidad: la tecla 3 se añade a dos menús que la
// gente ya usa, y el criterio que manda es que quien NO comenta teclee exactamente
// lo mismo que tecleaba ayer. Si esa secuencia cambia, la tarea está mal hecha
// aunque todo lo demás funcione.
package cart

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// comprarSinComentario es la secuencia LITERAL del recorrido de compra sin
// indicación, tal como se teclea desde el arranque del carrito (design.md
// D-041.19: «el recorrido de compra sin comentario es byte por byte el de hoy»):
//
//	1 → Bebidas · 1 → Café · 2 → Agregar · 1 → cantidad 1 · 2 → Finalizar · 1 → Confirmar
//
// SEIS pulsaciones. No es una constante de conveniencia: es el contrato que INV-15
// protege, y por eso vive aquí escrita a mano y no derivada de nada.
var comprarSinComentario = []string{"1", "1", "2", "1", "2", "1"}

// nivelesDelRecorrido es el nivel en el que queda la sub-máquina DESPUÉS de cada
// una de las seis pulsaciones. Sujeta el recorrido paso a paso: si alguien
// intercala un nivel nuevo (una pregunta de alcance, una pantalla de indicación),
// el estado deja de casar en la pulsación exacta donde se coló.
var nivelesDelRecorrido = []string{
	LevelArticles, // 1 · Bebidas
	LevelArticle,  // 1 · Café
	LevelQuantity, // 2 · Agregar al pedido
	LevelContinue, // 1 · cantidad 1 (item_added)
	LevelSummary,  // 2 · Finalizar pedido
	LevelClosed,   // 1 · Confirmar y finalizar
}

// TestINV15CompraSinComentarioMismasPulsaciones es la RED DE REGRESIÓN DE TECLAS
// (INV-15 / REQ-33b). Conduce el recorrido de compra sin indicación y exige tres
// cosas a la vez:
//
//  1. que las SEIS pulsaciones de siempre basten para cerrar el pedido —ni una más—;
//  2. que cada una deje la sub-máquina en el nivel que dejaba antes de T4.1c, de
//     modo que ningún paso nuevo pueda colarse en medio sin que este test lo diga;
//  3. que el pedido cierre con el dinero intacto (cart_closed, total 2.50).
//
// Lo que este test NO mira es el texto de las pantallas: las dos líneas `3)` que
// T4.1c añade a los menús son un cambio DELIBERADO de pantalla, no de recorrido, y
// quien las sujeta es el golden de la transcripción v1.
func TestINV15CompraSinComentarioMismasPulsaciones(t *testing.T) {
	m := New()
	vars := seededVars()

	if len(comprarSinComentario) != len(nivelesDelRecorrido) {
		t.Fatalf("el recorrido y sus niveles esperados deben tener el mismo largo: %d vs %d",
			len(comprarSinComentario), len(nivelesDelRecorrido))
	}

	var cerrado bool
	for i, tecla := range comprarSinComentario {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla)
		vars = res.Vars
		st := loadState(vars)

		if st.Level != nivelesDelRecorrido[i] {
			t.Fatalf("INV-15 ROTA en la pulsación %d (%q): el carrito quedó en %q y antes de T4.1c quedaba en %q.\n"+
				"Pantalla emitida:\n%s",
				i+1, tecla, st.Level, nivelesDelRecorrido[i], joined(res.Outputs))
		}
		for _, eff := range res.Effects {
			if eff.Name == EffectCartClosed {
				cerrado = true
				if got := eff.Payload["total"]; got != 2.5 {
					t.Fatalf("el total del cierre cambió: %v (esperado 2.5)", got)
				}
			}
			if eff.Name == EffectNoteAdded {
				t.Fatalf("INV-15 ROTA: el recorrido SIN comentario emitió %q en la pulsación %d",
					EffectNoteAdded, i+1)
			}
		}
	}

	if !cerrado {
		t.Fatalf("INV-15 ROTA: tras las %d pulsaciones de siempre el pedido no cerró (falta %q)",
			len(comprarSinComentario), EffectCartClosed)
	}
	// La última pantalla es la confirmación, no una pregunta: si el recorrido
	// hubiera ganado un paso, aquí habría un menú esperando entrada.
	st := loadState(vars)
	if st.Note != "" {
		t.Fatalf("INV-15 ROTA: sin comentar, la nota del pedido quedó en %q", st.Note)
	}
	for _, l := range st.Lines {
		if l.Customization != "" {
			t.Fatalf("INV-15 ROTA: sin comentar, la línea %q quedó con customization %q", l.SKU, l.Customization)
		}
	}
}

// TestINV15SegundaVarianteDosUnidades repite la regresión con la secuencia del
// ejemplo de D-041.20 (dos hamburguesas: `1,1,2,2,2,1`). Importa porque el caso
// `qty > 1` es justo el que gana una pantalla NUEVA (el alcance) cuando SÍ se
// comenta: este test afirma que quien no comenta no la ve ni de lejos.
func TestINV15SegundaVarianteDosUnidadesMismasPulsaciones(t *testing.T) {
	m := New()
	vars := seededVars()

	niveles := []string{LevelArticles, LevelArticle, LevelQuantity, LevelContinue, LevelSummary, LevelClosed}
	for i, tecla := range []string{"1", "1", "2", "2", "2", "1"} {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, tecla)
		vars = res.Vars
		if st := loadState(vars); st.Level != niveles[i] {
			t.Fatalf("INV-15 ROTA con qty=2 en la pulsación %d (%q): nivel %q, esperado %q",
				i+1, tecla, st.Level, niveles[i])
		}
	}
	st := loadState(vars)
	if len(st.Lines) != 1 || st.Lines[0].Qty != 2 {
		t.Fatalf("sin comentar, la línea NO se parte: %+v", st.Lines)
	}
	if got := total(st.Lines); got != 5.0 {
		t.Fatalf("total esperado 5.00 (2 × 2.50), got %v", got)
	}
}

// --- SanitizeNote: una regla para las dos ranuras (REQ-33e) ----------------

// Los caracteres que la función existe para quitar se escriben con su ESCAPE, no
// literales: un test que "se ve bien" pero lleva un zero-width pegado no prueba
// nada, porque nadie que lo lea puede saber qué está pasando por la función.
func TestSanitizeNote(t *testing.T) {
	casos := []struct {
		nombre  string
		in      string
		want    string
		tooLong bool
	}{
		{nombre: "texto normal intacto", in: "sin cebolla", want: "sin cebolla"},
		{nombre: "byte nulo fuera (SQLSTATE 22021)", in: "sin\x00cebolla", want: "sincebolla"},
		{nombre: "controles C0 fuera", in: "sin\x01\x02cebolla", want: "sincebolla"},
		{nombre: "control C1 fuera", in: "sin\u0085cebolla", want: "sincebolla"},
		{nombre: "zero-width fuera", in: "sin\u200bce\u200dbolla", want: "sincebolla"},
		{nombre: "bidi fuera", in: "\u202esin cebolla\u202c", want: "sin cebolla"},
		{nombre: "aislamiento bidi fuera", in: "\u2066sin cebolla\u2069", want: "sin cebolla"},
		{nombre: "BOM fuera", in: "\ufeffsin cebolla", want: "sin cebolla"},
		{nombre: "multilínea a una sola línea", in: "sin cebolla\ny sin sal", want: "sin cebolla y sin sal"},
		{nombre: "CRLF cuenta como UN espacio", in: "sin cebolla\r\ny sin sal", want: "sin cebolla y sin sal"},
		{nombre: "tabulador a espacio", in: "sin\tcebolla", want: "sin cebolla"},
		{nombre: "separador de línea Unicode a espacio", in: "sin\u2028cebolla", want: "sin cebolla"},
		{nombre: "separador de párrafo Unicode a espacio", in: "sin\u2029cebolla", want: "sin cebolla"},
		{nombre: "espacios colapsados y recortados", in: "   sin    cebolla   ", want: "sin cebolla"},
		{nombre: "emoji sobrevive", in: "\U0001F382 sin gluten", want: "\U0001F382 sin gluten"},
		{nombre: "solo espacios equivale a vacío", in: "   \n\t  ", want: ""},
		{nombre: "solo invisibles equivale a vacío", in: "\u200b\u200b\ufeff", want: ""},
		{nombre: "cadena vacía", in: "", want: ""},
		{
			nombre: "280 runas justas pasan",
			in:     strings.Repeat("á", MaxNoteRunes),
			want:   strings.Repeat("á", MaxNoteRunes),
		},
		{nombre: "281 runas se rechazan", in: strings.Repeat("á", MaxNoteRunes+1), tooLong: true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := SanitizeNote(c.in)
			if !c.tooLong {
				if err != nil {
					t.Fatalf("SanitizeNote(%q) devolvió error: %v", c.in, err)
				}
				if got != c.want {
					t.Fatalf("SanitizeNote(%q) = %q, quiero %q", c.in, got, c.want)
				}
				return
			}
			var tooLong NoteTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("SanitizeNote(%q) debía devolver NoteTooLongError, got %v", c.in, err)
			}
			// Se RECHAZA, no se trunca: truncando, `got` traería las 280 primeras
			// runas y «…y sin maní» se habría perdido justo donde va el alérgeno.
			if got != "" {
				t.Fatalf("SanitizeNote NO debe truncar: devolvió %d runas", len([]rune(got)))
			}
			if tooLong.Runes != MaxNoteRunes+1 || tooLong.Max != MaxNoteRunes {
				t.Fatalf("el error debe llevar el largo REAL: got %d de %d", tooLong.Runes, tooLong.Max)
			}
		})
	}
}

// TestSanitizeNoteLargoSeMideDespuesDeSanear: 300 saltos de línea y una palabra
// caben. Si el límite se midiera ANTES del saneo, un muro de maquetación se
// comería la cuota entera sin que el cliente hubiera escrito una sola instrucción.
func TestSanitizeNoteLargoSeMideDespuesDeSanear(t *testing.T) {
	got, err := SanitizeNote(strings.Repeat("\n", 300) + "sin sal" + strings.Repeat("\t", 300))
	if err != nil {
		t.Fatalf("el largo se mide DESPUÉS de sanear: %v", err)
	}
	if got != "sin sal" {
		t.Fatalf("got %q, quiero %q", got, "sin sal")
	}
}

// --- El ejemplo canónico: dos unidades, UNA comentada, y nota de pedido ----

// driveNotes conduce una secuencia devolviendo la ÚLTIMA respuesta completa
// (estado, pantallas y efectos): las pruebas de indicación miran las tres cosas,
// no solo el estado como hace `drive`.
func driveNotes(t *testing.T, m Module, vars map[string]any, inputs ...string) (cartState, []string, []modules.Effect, map[string]any) {
	t.Helper()
	var res modules.Result
	for _, in := range inputs {
		res = m.Step(model.Node{}, model.Conversation{Vars: vars}, in)
		vars = res.Vars
	}
	return loadState(vars), res.Outputs, res.Effects, vars
}

// effectByName busca un efecto por nombre en el resultado de un Step.
func effectByName(effs []modules.Effect, name string) (modules.Effect, bool) {
	for _, e := range effs {
		if e.Name == name {
			return e, true
		}
	}
	return modules.Effect{}, false
}

// assertLineaPartida comprueba el split de D-041.20 sobre las líneas resultantes:
// dos entradas de UNA unidad, la comentada la ÚLTIMA, mismo producto en las dos y
// —lo que de verdad importa— el mismo dinero y la misma cantidad que antes de
// partir (INV-16). Vive aparte porque son seis afirmaciones sobre UN hecho.
//
// `restante` es lo que debe llevar la línea que NO se comenta, y es un parámetro
// desde D-044.18: partir ya no la deja en blanco, le CONSERVA la indicación que la
// línea traía. Con "" se afirma el caso de siempre —no había ninguna—, y con un
// texto, que la vieja sobrevivió al re-partido. Un helper que diera por hecho el
// vacío afirmaría hoy justo lo contrario de la conducta.
func assertLineaPartida(t *testing.T, lines []cartLine, restante, nota string, totalEsperado float64) {
	t.Helper()
	if len(lines) != 2 {
		t.Fatalf("la línea ×2 debe partirse en dos: %+v", lines)
	}
	if lines[0].Qty != 1 || lines[0].Customization != restante {
		t.Fatalf("la primera línea es el resto, con indicación %q: %+v", restante, lines[0])
	}
	if lines[1].Qty != 1 || lines[1].Customization != nota {
		t.Fatalf("la segunda línea es la comentada: %+v", lines[1])
	}
	if lines[0].SKU != lines[1].SKU || lines[0].UnitPrice != lines[1].UnitPrice ||
		lines[0].Label != lines[1].Label {
		t.Fatalf("partir NO inventa ni cambia producto: %+v vs %+v", lines[0], lines[1])
	}
	if got := total(lines); got != totalEsperado {
		t.Fatalf("partir movió el dinero: total %v, esperado %v", got, totalEsperado)
	}
	if lines[0].Qty+lines[1].Qty != 2 {
		t.Fatalf("partir cambió la cantidad total: %+v", lines)
	}
}

// assertCierreConLasDosIndicaciones comprueba que el efecto de cierre lleva las
// DOS indicaciones por separado —la de receta en SU línea, la de entrega en la
// cabecera— y que el total no se enteró de ninguna.
func assertCierreConLasDosIndicaciones(t *testing.T, effs []modules.Effect, nota string, tot float64) {
	t.Helper()
	closed, ok := effectByName(effs, EffectCartClosed)
	if !ok {
		t.Fatalf("falta %q: %+v", EffectCartClosed, effs)
	}
	if closed.Payload["customer_note"] != nota {
		t.Fatalf("customer_note no viaja en el cierre: %v", closed.Payload)
	}
	if closed.Payload["total"] != tot {
		t.Fatalf("el total del cierre cambió: %v", closed.Payload["total"])
	}
	items, ok := closed.Payload["items"].([]map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("el cierre debe llevar DOS líneas: %v", closed.Payload["items"])
	}
	if _, hay := items[0]["customization"]; hay {
		t.Fatalf("la línea sin indicación no publica la clave: %v", items[0])
	}
	if items[1]["customization"] != "sin azúcar" {
		t.Fatalf("la personalización no viaja en el cierre: %v", items[1])
	}
}

// TestEjemploCanonicoDosUnidadesUnaComentadaMasNotaDePedido es el escenario
// literal de D-041.20 (allí, dos hamburguesas y una sin cebolla; aquí, dos cafés y
// uno sin azúcar, que es el mismo caso con el catálogo de estas pruebas).
//
// Demuestra las cuatro cosas del ejemplo: que la línea se PARTE en dos, que la
// comentada queda la última, que el dinero NO se mueve (INV-13/INV-16) y que las
// dos indicaciones llegan al cierre sin mezclarse.
func TestEjemploCanonicoDosUnidadesUnaComentadaMasNotaDePedido(t *testing.T) {
	m := New()

	// 1 Bebidas · 1 Café · 2 Agregar · 2 unidades · 3 indicación · 2 solo para 1 · texto
	st, outs, effs, vars := driveNotes(t, m, seededVars(),
		"1", "1", "2", "2", "3", "2", "sin azúcar")

	if st.Level != LevelContinue {
		t.Fatalf("tras anotar se vuelve al nivel de origen (L5), no a %q", st.Level)
	}
	// El resto va SIN indicación porque la línea no traía ninguna: aquí el "" no es
	// un borrado, es que no había nada que conservar.
	assertLineaPartida(t, st.Lines, "", "sin azúcar", 5.0)
	mustContain(t, outs, `Anotado para 1 de las 2: "sin azúcar" ✅`, "Añadido al pedido ✅")

	eff, ok := effectByName(effs, EffectNoteAdded)
	if !ok {
		t.Fatalf("anotar debe emitir %q: %+v", EffectNoteAdded, effs)
	}
	if eff.Payload["scope"] != "item" || eff.Payload["sku"] != "CAFE" ||
		eff.Payload["text"] != "sin azúcar" || eff.Payload["split_from_qty"] != 2 {
		t.Fatalf("payload de %s inesperado: %v", EffectNoteAdded, eff.Payload)
	}

	// 2 Finalizar → el resumen enseña lo anotado ANTES de confirmar (REQ-33c).
	st, outs, _, vars = driveNotes(t, m, vars, "2")
	if st.Level != LevelSummary {
		t.Fatalf("nivel %q, esperado summary", st.Level)
	}
	mustContain(t, outs, "   ✏️ sin azúcar", "TOTAL  $5.00", "3) ✏️ Indicación para todo el pedido")

	// 3 → indicación del PEDIDO.
	st, outs, effs, vars = driveNotes(t, m, vars, "3", "dejarlo en portería")
	if st.Note != "dejarlo en portería" || st.Level != LevelSummary {
		t.Fatalf("tras anotar el pedido: nota=%q nivel=%q", st.Note, st.Level)
	}
	mustContain(t, outs,
		`Anotado para todo el pedido: "dejarlo en portería" ✅`,
		"✏️ Para todo el pedido: dejarlo en portería")
	assertEfectoNotaDePedido(t, effs, "dejarlo en portería")

	// 1 Confirmar → el cierre lleva las DOS indicaciones y el mismo dinero.
	st, _, effs, _ = driveNotes(t, m, vars, "1")
	if st.Level != LevelClosed {
		t.Fatalf("nivel %q, esperado closed", st.Level)
	}
	assertCierreConLasDosIndicaciones(t, effs, "dejarlo en portería", 5.0)
}

// assertEfectoNotaDePedido comprueba REQ-33g: con scope "order" el efecto lleva el
// ámbito y el LARGO, nunca el texto. `flow_events` no lo poda nadie, y el literal
// —que es donde de verdad se cuela la PII— sobreviviría a la retención de la propia
// solicitud.
func assertEfectoNotaDePedido(t *testing.T, effs []modules.Effect, nota string) {
	t.Helper()
	eff, ok := effectByName(effs, EffectNoteAdded)
	if !ok {
		t.Fatalf("anotar el pedido debe emitir %q", EffectNoteAdded)
	}
	if eff.Payload["scope"] != "order" || eff.Payload["len"] != len([]rune(nota)) {
		t.Fatalf("payload de la nota de pedido: %v", eff.Payload)
	}
	if _, hay := eff.Payload["text"]; hay {
		t.Fatalf("el efecto de la nota de PEDIDO no puede llevar el texto (REQ-33g): %v", eff.Payload)
	}
}

// --- D-044.18: la indicación previa SOBREVIVE al re-partido ---------------

// TestIndicacionPreviaSobreviveAlRePartido es el caso real que cierra A4/MD-041.12
// del Plan 041 (decisión de producto de Jhoan del 2026-08-09, D-044.18): una línea
// ×2 que YA lleva «con cebolla» —anotada con alcance «para las 2»— y sobre la que
// después se anota otra cosa SOLO para una.
//
// Hasta este arreglo, partir vaciaba la indicación del resto: el cliente pedía
// cebolla para los dos, precisaba algo sobre UNO, y el otro salía de cocina sin
// cebolla. Partir es una RE-AGRUPACIÓN de unidades, no una edición del pedido, así
// que lo ya pedido tiene que sobrevivir entero: mismo sku, mismo label, mismo
// unit_price, misma suma de qty, mismo total (INV-16) y la MISMA indicación.
//
// Los dos textos se escriben a mano («con cebolla», «sin sal») en cada afirmación y
// no se comparten con lo que se teclea: un test que se compara contra la variable
// que alimenta al sistema pasa con cualquier conducta.
func TestIndicacionPreviaSobreviveAlRePartido(t *testing.T) {
	m := New()

	// 1 Bebidas · 1 Café · 2 Agregar · 2 unidades · 3 indicación · 1 «Para las 2» · texto.
	st, _, _, vars := driveNotes(t, m, seededVars(), "1", "1", "2", "2", "3", "1", "con cebolla")
	if len(st.Lines) != 1 || st.Lines[0].Qty != 2 || st.Lines[0].Customization != "con cebolla" {
		t.Fatalf("punto de partida: UNA línea ×2 con la indicación puesta, got %+v", st.Lines)
	}
	antes := st.Lines[0]

	// 3 indicación · 2 «Solo para 1» · texto NUEVO ⇒ es aquí donde se parte.
	st, outs, effs, vars := driveNotes(t, m, vars, "3", "2", "sin sal")

	// La restante conserva «con cebolla»; la separada lleva la nueva.
	assertLineaPartida(t, st.Lines, "con cebolla", "sin sal", 5.0)

	// Y el pedido es el MISMO pedido: solo cambió cómo están agrupadas las unidades.
	assertRepartidoConservaProductoYDinero(t, st.Lines, antes, 5.0)
	assertNotaAnotadaTrasRepartir(t, st, outs, effs)

	// El resumen enseña las DOS indicaciones antes de confirmar (REQ-33c): si el
	// resto hubiera perdido la suya, aquí faltaría un renglón.
	st, outs, _, vars = driveNotes(t, m, vars, "2")
	assertResumenConDosIndicacionesTrasRepartir(t, st, outs)

	// 1 Confirmar → el cierre es lo ÚNICO que cruza al mundo (INV-12): las dos
	// indicaciones tienen que ir ahí, cada una en SU línea.
	st, _, effs, _ = driveNotes(t, m, vars, "1")
	assertCierreDelRePartido(t, st, effs)
}

// assertRepartidoConservaProductoYDinero comprueba que partir una línea NO toca
// el producto ni el dinero (INV-16): mismo sku, mismo label y mismo unit_price en
// las dos líneas resultantes frente a la línea original (`antes`), la misma suma
// de unidades y el mismo total que había antes de partir.
func assertRepartidoConservaProductoYDinero(t *testing.T, lines []cartLine, antes cartLine, totalEsperado float64) {
	t.Helper()
	for i, l := range lines {
		if l.SKU != antes.SKU || l.Label != antes.Label || l.UnitPrice != antes.UnitPrice {
			t.Fatalf("partir no toca el producto: línea %d %+v vs la original %+v", i, l, antes)
		}
	}
	if suma := lines[0].Qty + lines[1].Qty; suma != antes.Qty {
		t.Fatalf("partir cambió la cantidad: %d unidades tras partir, %d antes", suma, antes.Qty)
	}
	if got := total(lines); got != totalEsperado {
		t.Fatalf("partir movió el dinero: total %v, esperado 5.00 (INV-16)", got)
	}
}

// assertNotaAnotadaTrasRepartir comprueba que, tras el re-partido, la sub-máquina
// vuelve a L5 (LevelContinue) y que la indicación NUEVA —la de la línea que se
// acaba de separar— se anota con el scope, el sku y el split_from_qty correctos.
func assertNotaAnotadaTrasRepartir(t *testing.T, st cartState, outs []string, effs []modules.Effect) {
	t.Helper()
	if st.Level != LevelContinue {
		t.Fatalf("tras anotar se vuelve a L5, no a %q", st.Level)
	}
	mustContain(t, outs, `Anotado para 1 de las 2: "sin sal" ✅`)

	eff, ok := effectByName(effs, EffectNoteAdded)
	if !ok {
		t.Fatalf("anotar debe emitir %q: %+v", EffectNoteAdded, effs)
	}
	if eff.Payload["scope"] != "item" || eff.Payload["sku"] != "CAFE" ||
		eff.Payload["text"] != "sin sal" || eff.Payload["split_from_qty"] != 2 {
		t.Fatalf("payload de %s inesperado: %v", EffectNoteAdded, eff.Payload)
	}
}

// assertResumenConDosIndicacionesTrasRepartir comprueba que el resumen previo a
// confirmar (REQ-33c) enseña las DOS indicaciones tras el re-partido: si el resto
// hubiera perdido la suya, aquí faltaría un renglón.
func assertResumenConDosIndicacionesTrasRepartir(t *testing.T, st cartState, outs []string) {
	t.Helper()
	if st.Level != LevelSummary {
		t.Fatalf("nivel %q, esperado summary", st.Level)
	}
	mustContain(t, outs, "   ✏️ con cebolla", "   ✏️ sin sal", "TOTAL  $5.00")
}

// assertCierreDelRePartido comprueba que el cierre —lo ÚNICO que cruza al mundo,
// INV-12— lleva las DOS líneas del re-partido con su indicación cada una en su
// sitio, y que el total no se enteró de la separación.
func assertCierreDelRePartido(t *testing.T, st cartState, effs []modules.Effect) {
	t.Helper()
	if st.Level != LevelClosed {
		t.Fatalf("nivel %q, esperado closed", st.Level)
	}
	closed, ok := effectByName(effs, EffectCartClosed)
	if !ok {
		t.Fatalf("falta %q: %+v", EffectCartClosed, effs)
	}
	items, ok := closed.Payload["items"].([]map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("el cierre debe llevar DOS líneas: %v", closed.Payload["items"])
	}
	if items[0]["customization"] != "con cebolla" {
		t.Fatalf("la indicación conservada NO llega al cierre: %v", items[0])
	}
	if items[1]["customization"] != "sin sal" {
		t.Fatalf("la indicación nueva no viaja en el cierre: %v", items[1])
	}
	if closed.Payload["total"] != 5.0 {
		t.Fatalf("el total del cierre cambió: %v", closed.Payload["total"])
	}
}

// TestIndicacionConservadaLlegaAIntakeItems recorre el camino de REQ-17d hasta el
// final: no basta con que la indicación sobreviva EN EL ESTADO del carrito, tiene
// que acabar en `intake_items.customization`, que es lo que lee quien prepara el
// pedido. Se conduce por el módulo real y se despachan sus efectos por el proyector
// —igual que hace el PersistSink—, así que lo que se afirma es la cadena entera:
// split → foto de líneas del note_added → filas de la solicitud.
func TestIndicacionConservadaLlegaAIntakeItems(t *testing.T) {
	p, repo := lineProjector()
	m := New()
	vars := seededVars()

	for _, in := range []string{
		"1", "1", "2", "2", // 2 × Café
		"3", "1", "con cebolla", // indicación para las 2
		"3", "2", "sin sal", // y otra solo para 1 ⇒ parte
		"2", "1", // finalizar y confirmar
	} {
		_, effs, next := driveE(t, m, vars, in)
		project(t, p, effs)
		vars = next
	}

	solicitud := soloSolicitud(t, repo)
	espejo(t, repo, solicitud.ID, []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5, Customization: "con cebolla"},
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5, Customization: "sin sal"},
	})
}

// TestUnaUnidadVaDirectoAlTexto: con qty == 1 no hay pregunta de alcance (D-041.20).
// Es el caso corriente y no puede ganar una pantalla.
func TestUnaUnidadVaDirectoAlTexto(t *testing.T) {
	m := New()
	st, outs, _, vars := driveNotes(t, m, seededVars(), "1", "1", "2", "1", "3")
	if st.Level != LevelItemNote {
		t.Fatalf("con una unidad, 3 lleva DIRECTO al texto; nivel %q", st.Level)
	}
	mustContain(t, outs, `✏️ Escribe la indicación para "Café".`, "Máx. 280 caracteres.",
		"0) ← Volver sin indicación")
	if strings.Contains(joined(outs), "Solo para 1") {
		t.Fatalf("con una unidad NO se pregunta el alcance:\n%s", joined(outs))
	}

	st, outs, effs, _ := driveNotes(t, m, vars, "sin azúcar")
	if len(st.Lines) != 1 || st.Lines[0].Customization != "sin azúcar" || st.Lines[0].Qty != 1 {
		t.Fatalf("la indicación va a la línea sin partir nada: %+v", st.Lines)
	}
	mustContain(t, outs, `Anotado: "sin azúcar" ✅`)
	eff, _ := effectByName(effs, EffectNoteAdded)
	if _, hay := eff.Payload["split_from_qty"]; hay {
		t.Fatalf("sin split, la clave no aparece: %v", eff.Payload)
	}
}

// TestVolverSinIndicacionDejaElCarritoIntacto recorre las TRES salidas por `0`:
// desde la pantalla de alcance, desde el texto de línea y desde el texto de
// pedido. En las tres, el carrito queda exactamente como estaba: sin indicación,
// sin partir y sin nota.
func TestVolverSinIndicacionDejaElCarritoIntacto(t *testing.T) {
	m := New()

	t.Run("desde el alcance", func(t *testing.T) {
		st, _, effs, _ := driveNotes(t, m, seededVars(), "1", "1", "2", "2", "3", "0")
		if st.Level != LevelContinue {
			t.Fatalf("0 vuelve a L5; nivel %q", st.Level)
		}
		if len(st.Lines) != 1 || st.Lines[0].Qty != 2 {
			t.Fatalf("desistir NO parte la línea: %+v", st.Lines)
		}
		if _, hay := effectByName(effs, EffectNoteAdded); hay {
			t.Fatalf("desistir no emite %q", EffectNoteAdded)
		}
	})

	t.Run("desde el texto de línea", func(t *testing.T) {
		st, _, _, _ := driveNotes(t, m, seededVars(), "1", "1", "2", "2", "3", "2", "0")
		if st.Level != LevelContinue {
			t.Fatalf("0 vuelve a L5; nivel %q", st.Level)
		}
		// El split ocurre AL GUARDAR el texto, no al elegir el alcance: quien
		// desiste después de decir "solo para 1" no se encuentra el pedido partido.
		if len(st.Lines) != 1 || st.Lines[0].Qty != 2 || st.Lines[0].Customization != "" {
			t.Fatalf("desistir tras elegir el alcance NO parte la línea: %+v", st.Lines)
		}
		if st.NoteSplit {
			t.Fatalf("el alcance en curso muere al salir del nivel de nota")
		}
	})

	t.Run("desde el texto de pedido", func(t *testing.T) {
		st, _, effs, _ := driveNotes(t, m, seededVars(), "1", "1", "2", "1", "2", "3", "0")
		if st.Level != LevelSummary {
			t.Fatalf("0 vuelve a L6; nivel %q", st.Level)
		}
		if st.Note != "" {
			t.Fatalf("desistir no escribe nota: %q", st.Note)
		}
		if _, hay := effectByName(effs, EffectNoteAdded); hay {
			t.Fatalf("desistir no emite %q", EffectNoteAdded)
		}
	})
}

// TestTextoVacioTrasSanearEquivaleACero: escribir solo espacios o solo invisibles
// no es un error ni una indicación (REQ-33e). Se sale como con `0`.
func TestTextoVacioTrasSanearEquivaleACero(t *testing.T) {
	m := New()
	st, outs, effs, _ := driveNotes(t, m, seededVars(), "1", "1", "2", "1", "3", "\u200b\u200b")
	if st.Level != LevelContinue {
		t.Fatalf("vacío tras sanear ≡ 0: nivel %q", st.Level)
	}
	if st.Lines[0].Customization != "" {
		t.Fatalf("no se escribe indicación vacía: %+v", st.Lines[0])
	}
	if _, hay := effectByName(effs, EffectNoteAdded); hay {
		t.Fatalf("una indicación vacía no es una indicación: %+v", effs)
	}
	if strings.Contains(joined(outs), "Anotado") {
		t.Fatalf("no se acusa lo que no se anotó:\n%s", joined(outs))
	}
}

// TestIndicacionMuyLargaRepreguntaYNoTrunca: el texto de más se RECHAZA en el
// mismo paso, con el número real, y el carrito no cambia — ni la línea se parte.
func TestIndicacionMuyLargaRepreguntaYNoTrunca(t *testing.T) {
	m := New()
	largo := strings.Repeat("a", MaxNoteRunes+32)
	st, outs, effs, vars := driveNotes(t, m, seededVars(), "1", "1", "2", "2", "3", "2", largo)

	if st.Level != LevelItemNote {
		t.Fatalf("pasarse repregunta el MISMO paso; nivel %q", st.Level)
	}
	mustContain(t, outs, "Esa indicación es muy larga (312 de 280 caracteres). Escríbela más corta.",
		`✏️ Escribe la indicación para 1 de las 2 "Café".`)
	if len(st.Lines) != 1 || st.Lines[0].Qty != 2 || st.Lines[0].Customization != "" {
		t.Fatalf("un texto rechazado no parte la línea ni escribe nada: %+v", st.Lines)
	}
	if _, hay := effectByName(effs, EffectNoteAdded); hay {
		t.Fatalf("un texto rechazado no emite efecto")
	}
	// El alcance elegido SIGUE vigente: el cliente reescribe y se parte entonces.
	st, _, _, _ = driveNotes(t, m, vars, "sin azúcar")
	if len(st.Lines) != 2 || st.Lines[1].Customization != "sin azúcar" {
		t.Fatalf("tras corregir el texto, el split se aplica: %+v", st.Lines)
	}
}

// TestReemplazarUnaIndicacionYaEscrita: la pantalla enseña la actual y `0` la deja
// como estaba (D-041.19); escribir otra la reemplaza.
func TestReemplazarUnaIndicacionYaEscrita(t *testing.T) {
	m := New()
	_, _, _, vars := driveNotes(t, m, seededVars(), "1", "1", "2", "1", "3", "sin azúcar")

	_, outs, _, vars2 := driveNotes(t, m, vars, "3")
	mustContain(t, outs, `Indicación actual: "sin azúcar" — escribe otra para reemplazarla.`)

	st, _, _, _ := driveNotes(t, m, vars2, "0")
	if st.Lines[0].Customization != "sin azúcar" {
		t.Fatalf("0 con indicación previa la DEJA como estaba: %+v", st.Lines[0])
	}
	st, _, _, _ = driveNotes(t, m, vars2, "sin leche")
	if st.Lines[0].Customization != "sin leche" {
		t.Fatalf("escribir otra la reemplaza: %+v", st.Lines[0])
	}
}

// TestEstadoViejoDelCarritoCargaSinIndicaciones es la garantía de round-trip
// JSONB (criterio (f)): un cartState serializado ANTES de T4.1c —sin `note`, sin
// `customization` en las líneas— carga con los campos vacíos y sigue cerrando.
func TestEstadoViejoDelCarritoCargaSinIndicaciones(t *testing.T) {
	viejo := map[string]any{
		"level":   LevelSummary,
		"started": true,
		"lines": []any{
			map[string]any{"sku": "CAFE", "label": "Café", "qty": 2.0, "unit_price": 2.5},
		},
	}
	vars := seededVars()
	vars[stateVarKey] = viejo

	st := loadState(vars)
	if len(st.Lines) != 1 || st.Lines[0].Customization != "" || st.Note != "" {
		t.Fatalf("un estado viejo carga con las indicaciones vacías: %+v", st)
	}

	_, _, effs, _ := driveNotes(t, New(), vars, "1")
	closed, ok := effectByName(effs, EffectCartClosed)
	if !ok {
		t.Fatalf("un carrito viejo sigue cerrando: %+v", effs)
	}
	if _, hay := closed.Payload["customer_note"]; hay {
		t.Fatalf("sin nota, la clave no aparece en el cierre: %v", closed.Payload)
	}
	if closed.Payload["total"] != 5.0 {
		t.Fatalf("total %v, esperado 5.00", closed.Payload["total"])
	}
}

// TestLaIndicacionNoSeCuelaEnElNivelDeCantidad: la tecla 3 SOLO existe en L5 y L6.
// En un nivel de lista, "3" sigue siendo lo que era (un código de catálogo o un
// reprompt), y en el de cantidad, la cantidad 3.
func TestLaIndicacionNoSeCuelaEnOtrosNiveles(t *testing.T) {
	m := New()
	st, _, _, _ := driveNotes(t, m, seededVars(), "1", "1", "2", "3")
	if st.Level != LevelContinue || len(st.Lines) != 1 || st.Lines[0].Qty != 3 {
		t.Fatalf("en el nivel de cantidad, 3 son TRES unidades: %+v", st)
	}
	st, _, _, _ = driveNotes(t, m, seededVars(), "3")
	if st.Level != LevelCategories {
		t.Fatalf("en L1 no hay categoría 3: reprompt en el mismo nivel, got %q", st.Level)
	}
}
