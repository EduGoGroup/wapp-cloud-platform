package cart_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// golden_test.go es la RED DE REGRESIÓN CONVERSACIONAL del carrito: conduce flujos
// completos y compara la TRANSCRIPCIÓN literal —cada pantalla emitida, el payload
// público de cada efecto y el estado serializado— con los goldens de testdata/.
//
// 🔒 Los goldens son DATOS OBSERVABLES copiados byte a byte del carrito viejo
// (internal/flujos/modules/cart/testdata, D-F8-3). NO SE REGENERAN: aquí no hay
// `-update` ni ninguna otra puerta de escritura de testdata/. Si uno falla, la
// respuesta correcta es arreglar el código. La única línea tocada a mano en su
// historia es la pantalla de «Pedido cancelado» del v1 (2026-08-11, hallazgo #29),
// razonada en screens.go.

// assertGolden compara `got` con el contenido del golden. Solo lee.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want := string(readTestdata(t, name))
	if want == got {
		return
	}
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			t.Fatalf("el golden %s NO coincide (los goldens no se regeneran: arregla el código).\n"+
				"primera diferencia en la línea %d:\n  golden:   %q\n  obtenido: %q", name, i+1, w, g)
		}
	}
	t.Fatalf("el golden %s NO coincide y no se localizó la diferencia por líneas", name)
}

// transcript acumula la conversación (pantallas + efectos + estado) en texto plano.
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

func (tr *transcript) step(t *testing.T, input string, res modules.Result) {
	t.Helper()
	tr.b.WriteString("\n--- STEP <" + input + "> ---\n")
	tr.writeOutputs(res.Outputs)
	tr.writeEffects(res.Effects)
	// El estado se vuelca por el espejo del contrato (cartState, helpers_test.go), que
	// fija también el ORDEN de las claves: el golden sujeta la forma del JSONB.
	state, err := json.Marshal(stateOf(t, res.Vars))
	if err != nil {
		t.Fatalf("estado no serializable: %v", err)
	}
	tr.b.WriteString("estado: " + string(state) + "\n")
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
		// módulo HACIA public.flow_events, que es el contrato que no puede regresar (lo
		// leen la telemetría y el puente del CRM). Lo privado —la foto de líneas— lo
		// sujetan effects_payload_test.go y los tests de la proyección.
		payload, err := json.Marshal(e.PublicPayload())
		if err != nil {
			payload = []byte("<payload no serializable>")
		}
		tr.b.WriteString("efecto: kind=" + e.Kind + " name=" + e.Name + " payload=" + string(payload) + "\n")
	}
}

// driveTranscript conduce una secuencia de entradas —turnos COMPLETOS y sin resolutor,
// que es la degradación— y las va anotando.
func driveTranscript(t *testing.T, tr *transcript, m cart.Module, vars map[string]any, inputs []string) map[string]any {
	t.Helper()
	for _, in := range inputs {
		res := turn(m, model.Conversation{Vars: vars}, in, nil)
		tr.step(t, in, res)
		vars = res.Vars
	}
	return vars
}

// TestGolden_CartV1ConversationalRegression conduce los tres escenarios del Plan 016
// sobre el catálogo v1 real —el recorrido completo con idas y vueltas, el cancelar con
// 9 y la paginación «Más ▾»— contra un catálogo SIN un solo campo del contrato v2.
// Que siga coincidiendo es la prueba de que ni el v2, ni las indicaciones, ni la
// consulta degradada cambian la conversación de los tenants que ya venden.
func TestGolden_CartV1ConversationalRegression(t *testing.T) {
	raw := rawFromFile(t, "catalog_v1.json")
	tr := &transcript{}

	m := cart.New()
	tr.section("escenario 1 · recorrido completo (page_size por defecto)")
	tr.render(m.Render(model.Node{}, model.Content{Raw: raw}))
	driveTranscript(t, tr, m, map[string]any{modules.VarContentRaw: raw}, []string{
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

	tr.section("escenario 2 · cancelar con 9 desde continuar")
	driveTranscript(t, tr, cart.New(), map[string]any{modules.VarContentRaw: raw}, []string{
		"1", // Bebidas
		"1", // Café
		"2", // agregar
		"1", // cantidad 1
		"9", // cancelar (cart_cancelled)
	})

	tr.section("escenario 3 · paginación Más ▾ (page_size = 1)")
	paged := cart.New(cart.WithPageSize(1))
	tr.render(paged.Render(model.Node{}, model.Content{Raw: raw}))
	driveTranscript(t, tr, paged, map[string]any{modules.VarContentRaw: raw}, []string{
		"3", // Más ▾ → página 1 de categorías
		"2", // Postres
		"2", // Más ▾ no aplica (Postres tiene 1 artículo) → reprompt
		"1", // Flan
	})

	assertGolden(t, "cart_v1_transcript.golden.txt", tr.b.String())
}

// TestGolden_CartV2ConversationalTranscript es la conversación con variantes y con
// combo sobre el catálogo v2: la evidencia del e2e conversacional del Plan 041 · T2.3
// y la red que sujeta esos textos.
func TestGolden_CartV2ConversationalTranscript(t *testing.T) {
	raw := rawFromFile(t, "catalog_v2.json")
	tr := &transcript{}
	m := cart.New()

	tr.section("variantes · elegir la presentación antes de la cantidad")
	tr.render(m.Render(model.Node{}, model.Content{Raw: raw}))
	vars := driveTranscript(t, tr, m, map[string]any{modules.VarContentRaw: raw}, []string{
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
	driveTranscript(t, tr, m, vars, []string{
		"1", // agregar más de Tortas
		"2", // el combo
		"2", // agregar → NO pide variante
		"1", // cantidad 1
		"2", // finalizar → resumen
		"1", // confirmar
	})

	assertGolden(t, "cart_v2_transcript.golden.txt", tr.b.String())
}
