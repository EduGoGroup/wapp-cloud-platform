//go:build pendiente

package stages_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// ---------------------------------------------------------------------------
// EL TOPE DE ÍTEMS POR PEDIDO
//
// 🔴 LOS NÚMEROS VAN LITERALES Y NO POR LA CONSTANTE. Un test que comparase las llamadas
// con `stages.MaxItemsPerOrder` PASARÍA CON CUALQUIER VALOR de la constante —incluido
// un 40 que devolvería el fan-out a no tener techo—. Lo que este banco protege es EL
// NÚMERO 10, que es una decisión de producto con un coste de plaza detrás. Y el motivo
// va como literal `"over_limit"` por la misma razón: es un valor de contrato.
//
// El fixture es sintético (ítems numerados) y no el de Ambar, que tiene 3: aquí no se
// mide calidad de extracción sino CUÁNTAS VECES se llama al modelo.
// ---------------------------------------------------------------------------

const numberedIdeaPrefix = "torta del pedido numero "

// numberedIdea / numberedEvidence son la idea `i` y la frase del cliente que la
// respalda. Se derivan del índice para poder afirmar QUÉ ítems se atendieron.
func numberedIdea(i int) string     { return numberedIdeaPrefix + strconv.Itoa(i) }
func numberedEvidence(i int) string { return "quiero una " + numberedIdea(i) + " por favor" }

// numberedLiteral compone un literal con `n` peticiones, con la forma real que sale del
// compositor. Cada línea es la evidencia de SU idea, así que ningún ítem se aísla por un
// motivo que no sea el tope.
func numberedLiteral(n int) string {
	var b strings.Builder
	b.WriteString("### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n")
	for i := range n {
		fmt.Fprintf(&b, "cliente: %s\n", numberedEvidence(i))
	}
	b.WriteString("### FIN DE LOS MENSAJES ###")
	return b.String()
}

// numberedIdeas son las `n` ideas tal como P3 las recibe de P2.
func numberedIdeas(n int) []llm.Want {
	out := make([]llm.Want, 0, n)
	for i := range n {
		out = append(out, llm.Want{Idea: numberedIdea(i), Evidence: numberedEvidence(i)})
	}
	return out
}

// answersAnyNumberedItem contesta bien a la primera a CUALQUIER idea numerada. Si el
// tope dejara de aplicarse contestaría a todas igual de bien: el doble no limita nada.
func answersAnyNumberedItem(t *testing.T) func(string, int) p3Reply {
	t.Helper()
	return func(idea string, _ int) p3Reply {
		n, err := strconv.Atoi(strings.TrimPrefix(idea, numberedIdeaPrefix))
		if err != nil {
			t.Fatalf("el provider recibió una idea que no es del fixture: %q", idea)
		}
		return p3Reply{raw: specOutput(t, "torta", "", numberedEvidence(n), nil, nil)}
	}
}

// runNumberedOrder corre P3 sobre un pedido de `n` ítems numerados.
func runNumberedOrder(t *testing.T, n int) (*p3Bench, *stages.P3Artifact) {
	t.Helper()
	b := newP3Bench(t, answersAnyNumberedItem(t))
	art, err := b.stage.Run(context.Background(), ambarJob(), numberedLiteral(n), numberedIdeas(n))
	if err != nil {
		t.Fatalf("Run con %d ítems: %v — superar el tope NO es un fallo del job", n, err)
	}
	return b, art
}

// assertFirstIdeasWereAsked comprueba que las llamadas fueron las de las PRIMERAS ideas
// y en orden. Sin esto, «10 llamadas» lo cumpliría también un código que atendiera las
// ideas 2..11 y marcara la 0 y la 1.
func assertFirstIdeasWereAsked(t *testing.T, calls []p3Call) {
	t.Helper()
	for i, call := range calls {
		if call.in.Idea != numberedIdea(i) {
			t.Fatalf("la llamada %d pidió %q; se esperaba la idea %d", i, call.in.Idea, i)
		}
	}
}

// assertMarkedOverLimit comprueba que `Isolated` son EXACTAMENTE las posiciones
// `from..from+n-1`, en orden ascendente y con el motivo del tope.
func assertMarkedOverLimit(t *testing.T, isolated []stages.IsolatedItem, from, n int) {
	t.Helper()
	if len(isolated) != n {
		t.Fatalf("ítems marcados = %d, se esperaban %d (lista completa: %+v)", len(isolated), n, isolated)
	}
	for i, mark := range isolated {
		if mark.IdeaPos != from+i || mark.Reason != "over_limit" {
			t.Fatalf("marca %d = %+v; se esperaba {IdeaPos:%d Reason:over_limit}", i, mark, from+i)
		}
	}
}

// TestMaxItemsPerOrder_IsTen: el número es contrato (D5, D-044.39, ADR-0046).
func TestMaxItemsPerOrder_IsTen(t *testing.T) {
	if stages.MaxItemsPerOrder != 10 {
		t.Fatalf("MaxItemsPerOrder = %d; el tope decidido es 10 y se cambia con una medición delante", stages.MaxItemsPerOrder)
	}
}

// TestP3Run_TwelveItems_MakesTenCallsAndMarksTheTwoLeftOver lleva dos mitades que son
// aserciones distintas: el CONTADOR DE LLAMADAS dice que la plaza única no se gastó de
// más (un código que llamara 12 veces y se quedara con 10 daría el mismo borrador), y
// LAS LÍNEAS dicen que el pedido no se perdió (tirar los 2 sobrantes en silencio da
// exactamente 10 llamadas también).
func TestP3Run_TwelveItems_MakesTenCallsAndMarksTheTwoLeftOver(t *testing.T) {
	b, art := runNumberedOrder(t, 12)

	if len(b.calls) != 10 {
		t.Fatalf("llamadas al modelo = %d para un pedido de 12 ítems; se esperaban exactamente 10 "+
			"(cada llamada de más son 22–32 s de la plaza única del Edge)", len(b.calls))
	}
	assertFirstIdeasWereAsked(t, b.calls)

	if len(art.Items) != 10 {
		t.Fatalf("items especificados = %d, se esperaban 10", len(art.Items))
	}
	if lines := len(art.Items) + len(art.Isolated); lines != 12 {
		t.Fatalf("líneas del borrador = %d; tienen que ser las 12 que pidió el cliente: los sobrantes se MARCAN, nunca se descartan", lines)
	}
	assertMarkedOverLimit(t, art.Isolated, 10, 2)

	// Y eso es lo que quedó EN LA BASE, sin una palabra del cliente en las marcas.
	saved := assertSavedOnce(t, b.store, 10, 2)
	assertNoCustomerText(t, saved.Payload, b.log.String(), numberedEvidence(11))
}

// TestP3Run_OverTheCap_LogsTheCountsSeparately: sin el aviso, un pedido truncado no deja
// rastro. 🔴 `items_sobre_tope` VA APARTE de `items_aislados`: los dos estados que caben
// en «aislado» piden cosas OPUESTAS al dueño —mirar su Ollama, o hablar con el cliente—.
func TestP3Run_OverTheCap_LogsTheCountsSeparately(t *testing.T) {
	b, _ := runNumberedOrder(t, 12)

	log := b.log.String()
	for _, want := range []string{
		"p3: el pedido supera el tope de ítems",
		"tope=10", "ideas=12", "atendidas=10", "sobre_tope=2", "reason=over_limit",
		"items_aislados=2", "items_sobre_tope=2",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("el log no lleva %q, así que un pedido truncado no deja rastro accionable: %q", want, log)
		}
	}
}

// TestP3Run_ExactlyTenItems_NeitherCutsNorMarks es el lado de ACÁ del corte: con 10
// ítems no se recorta nada, no se marca nada y no hay aviso.
func TestP3Run_ExactlyTenItems_NeitherCutsNorMarks(t *testing.T) {
	b, art := runNumberedOrder(t, 10)

	if len(b.calls) != 10 {
		t.Fatalf("llamadas = %d para un pedido de 10 ítems que CABE; se esperaban 10", len(b.calls))
	}
	if len(art.Items) != 10 || len(art.Isolated) != 0 {
		t.Fatalf("artefacto = %d items y %d aislados; un pedido que cabe no marca nada", len(art.Items), len(art.Isolated))
	}
	assertSavedOnce(t, b.store, 10, 0)

	// Ni el aviso, ni una cuenta distinta de cero.
	log := b.log.String()
	if strings.Contains(log, "supera el tope") {
		t.Fatalf("un pedido que CABE dejó el aviso del tope: %q", log)
	}
	if !strings.Contains(log, "items_sobre_tope=0") {
		t.Fatalf("la línea de cierre no dice que no se topó nada: %q", log)
	}
}

// TestP3Run_ElevenItems_TheFirstOneOverIsMarked es el ítem número 11: el PRIMERO que no
// cabe. Un corte se prueba en sus dos lados Y EN EL PRIMERO DEL OTRO LADO: con 10 no se
// llega a la frontera y con 12 sobra margen para que un tope de 11 siga cortando.
func TestP3Run_ElevenItems_TheFirstOneOverIsMarked(t *testing.T) {
	b, art := runNumberedOrder(t, 11)

	if len(b.calls) != 10 {
		t.Fatalf("llamadas = %d para 11 ítems; el ítem que sobra NO se pregunta al modelo (se esperaban 10)", len(b.calls))
	}
	assertFirstIdeasWereAsked(t, b.calls)
	if lines := len(art.Items) + len(art.Isolated); lines != 11 {
		t.Fatalf("líneas del borrador = %d; el cliente pidió 11 y ninguna se descarta", lines)
	}
	assertMarkedOverLimit(t, art.Isolated, 10, 1)
	assertSavedOnce(t, b.store, 10, 1)
}

// TestP3Run_OverTheCap_MarksGoAfterTheFanOutIsolations: las marcas del tope se añaden
// DETRÁS de las del fan-out, así que `Isolated` queda en orden ascendente de posición.
func TestP3Run_OverTheCap_MarksGoAfterTheFanOutIsolations(t *testing.T) {
	answer := answersAnyNumberedItem(t)
	b := newP3Bench(t, func(idea string, attempt int) p3Reply {
		if idea == numberedIdea(3) {
			return p3Reply{raw: specOutput(t, "torta", "", inventedEvidence, nil, nil)}
		}
		return answer(idea, attempt)
	})

	art, err := b.stage.Run(context.Background(), ambarJob(), numberedLiteral(11), numberedIdeas(11))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []stages.IsolatedItem{{IdeaPos: 3, Reason: "evidence"}, {IdeaPos: 10, Reason: "over_limit"}}
	if len(art.Isolated) != 2 || art.Isolated[0] != want[0] || art.Isolated[1] != want[1] {
		t.Fatalf("aislados = %+v; se esperaba %+v", art.Isolated, want)
	}
	if len(art.Items) != 9 {
		t.Fatalf("items = %d; se esperaban 9 (10 atendidos menos el aislado)", len(art.Items))
	}
	if !strings.Contains(b.log.String(), "items_aislados=2") || !strings.Contains(b.log.String(), "items_sobre_tope=1") {
		t.Fatalf("las cuentas del cierre no separan el tope del resto: %q", b.log.String())
	}
}
