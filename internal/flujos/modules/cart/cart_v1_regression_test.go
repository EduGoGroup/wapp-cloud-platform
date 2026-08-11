// cart_v1_regression_test.go es la RED DE REGRESIÓN CONVERSACIONAL del carrito v1
// (Plan 041 · T2.4). Conduce el flujo completo del Plan 016 —categorías →
// artículo → cantidad → resumen → cerrar, con "0" (volver), "9" (cancelar), una
// opción inexistente ("8") y la paginación "Más ▾"— contra un catálogo v1 SIN un
// solo campo del contrato v2, y congela en un golden la TRANSCRIPCIÓN literal:
// cada pantalla emitida y el payload exacto de cada efecto cart_*.
//
// El golden se generó con el código ANTERIOR al catálogo v2. Su valor está en que
// no se toca: si un cambio del v2 altera un texto de nivel o un payload, este test
// falla y eso es una REGRESIÓN para los tenants que ya venden, no una mejora.
//
// ⚠️ ÚNICA LÍNEA DEL GOLDEN TOCADA A MANO desde que se generó (2026-08-11, hallazgo
// #29): la pantalla de «Pedido cancelado». No se regeneró el archivo con -update —eso
// habría borrado la red entera de un plumazo—: se editó ESA línea y ninguna más, y el
// resto del golden siguió sujetando todo lo demás sin moverse. El cambio es
// DELIBERADO y está razonado en screenCancelled (screens.go): la frase vieja prometía
// que cualquier texto reabría el catálogo, y esta ola hizo que dejara de ser verdad.
// Un texto de pantalla que MIENTE no es la no-regresión que este golden protege.
package cart

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// transcript acumula la conversación (pantallas + efectos) en texto plano.
type transcript struct {
	b strings.Builder
}

func (tr *transcript) section(title string) {
	tr.b.WriteString("\n=== " + title + " ===\n")
}

func (tr *transcript) render(outs []string) {
	tr.b.WriteString("\n--- RENDER (arranque del nodo) ---\n")
	tr.writeOutputs(outs)
}

func (tr *transcript) step(input string, res modules.Result) {
	tr.b.WriteString("\n--- STEP <" + input + "> ---\n")
	tr.writeOutputs(res.Outputs)
	tr.writeEffects(res.Effects)
	tr.b.WriteString("estado: " + dumpState(loadState(res.Vars)) + "\n")
}

func (tr *transcript) writeOutputs(outs []string) {
	if len(outs) == 0 {
		tr.b.WriteString("(sin salida)\n")
		return
	}
	for _, o := range outs {
		tr.b.WriteString(o + "\n")
	}
}

func (tr *transcript) writeEffects(effs []modules.Effect) {
	if len(effs) == 0 {
		tr.b.WriteString("efectos: (ninguno)\n")
		return
	}
	for _, e := range effs {
		// PublicPayload y no Payload: el golden sujeta el efecto tal como sale del
		// módulo HACIA public.flow_events, que es el contrato que no puede regresar
		// (lo leen la telemetría y el puente del CRM). Las claves declaradas privadas
		// no llegan ahí, así que meterlas en la transcripción sujetaría como
		// "conversación v1" algo que ningún consumidor v1 vio nunca.
		//
		// Lo privado NO queda sin red: la foto de líneas que item_added/note_added
		// llevan desde el Plan 043 · Ola 3 la sujetan los tests de la proyección
		// (projection_lines_test.go), que es donde tiene consecuencias.
		payload, err := json.Marshal(e.PublicPayload())
		if err != nil {
			payload = []byte("<payload no serializable>")
		}
		tr.b.WriteString("efecto: kind=" + e.Kind + " name=" + e.Name + " payload=" + string(payload) + "\n")
	}
}

// dumpState serializa el cartState resultante (json determinista: claves ordenadas)
// para que el golden sujete también el ESTADO, no solo lo que se ve en pantalla.
func dumpState(st cartState) string {
	b, err := json.Marshal(st)
	if err != nil {
		return "<estado no serializable>"
	}
	return string(b)
}

// driveTranscript conduce una secuencia de entradas sobre unas Vars sembradas y
// las va anotando en la transcripción.
func driveTranscript(tr *transcript, m Module, vars map[string]any, inputs []string) map[string]any {
	for _, in := range inputs {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, in)
		tr.step(in, res)
		vars = res.Vars
	}
	return vars
}

// TestCartV1ConversationalRegression conduce los tres escenarios del Plan 016
// sobre el catálogo v1 real y compara la transcripción completa con el golden.
func TestCartV1ConversationalRegression(t *testing.T) {
	raw := rawFromFile(t, "catalog_v1.json")
	tr := &transcript{}

	// --- Escenario 1: recorrido completo con idas y vueltas ------------------
	// Arranque → opción inexistente → Bebidas → Café → ver descripción →
	// agregar → cantidad inválida → volver → agregar → cantidad 2 → subir a
	// categorías → Postres → Flan → cantidad 1 → resumen → seguir agregando →
	// Té → cantidad 1 → resumen → confirmar → entrada tras el cierre.
	m := New()
	tr.section("escenario 1 · recorrido completo (page_size por defecto)")
	tr.render(m.Render(model.Node{}, model.Content{Raw: raw}))
	vars := map[string]any{catalogVarKey: raw}
	driveTranscript(tr, m, vars, []string{
		"8",   // no existe la categoría 8 → reprompt en L1
		"1",   // Bebidas (cart_started + category_selected)
		"1",   // Café
		"1",   // ver descripción (item_viewed)
		"2",   // agregar → cantidad
		"abc", // cantidad no numérica → reprompt
		"0",   // volver al artículo
		"2",   // agregar → cantidad
		"2",   // cantidad 2 (item_added)
		"0",   // volver al artículo
		"0",   // volver a artículos
		"0",   // volver a categorías
		"2",   // Postres (category_selected)
		"1",   // Flan
		"2",   // agregar → cantidad
		"1",   // cantidad 1 (item_added)
		"2",   // finalizar → resumen
		"2",   // seguir agregando → artículos de Postres
		"0",   // volver a categorías
		"1",   // Bebidas
		"2",   // Té
		"2",   // agregar → cantidad
		"1",   // cantidad 1 (item_added)
		"2",   // finalizar → resumen
		"1",   // confirmar (cart_closed)
		"1",   // el nivel terminal ignora la entrada
	})

	// --- Escenario 2: cancelar con 9 ----------------------------------------
	tr.section("escenario 2 · cancelar con 9 desde continuar")
	vars = map[string]any{catalogVarKey: raw}
	driveTranscript(tr, New(), vars, []string{
		"1", // Bebidas
		"1", // Café
		"2", // agregar
		"1", // cantidad 1
		"9", // cancelar (cart_cancelled)
	})

	// --- Escenario 3: paginación "Más ▾" con page_size = 1 -------------------
	tr.section("escenario 3 · paginación Más ▾ (page_size = 1)")
	mp := New(WithPageSize(1))
	tr.render(mp.Render(model.Node{}, model.Content{Raw: raw}))
	vars = map[string]any{catalogVarKey: raw}
	driveTranscript(tr, mp, vars, []string{
		"3", // Más ▾ → página 1 de categorías
		"2", // Postres
		"2", // Más ▾ no aplica (Postres tiene 1 artículo) → reprompt
		"1", // Flan
	})

	assertGolden(t, "cart_v1_transcript.golden.txt", tr.b.String())
}
