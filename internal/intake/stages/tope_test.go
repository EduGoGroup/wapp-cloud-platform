package stages_test

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ---------------------------------------------------------------------------
// EL TOPE DE ÍTEMS POR PEDIDO (Plan 044 · T2.6 · ADR-0046 § Mecanismo 1)
//
// 🔴 LOS NÚMEROS VAN LITERALES Y NO POR LA CONSTANTE, y no es un descuido de estilo:
// es el criterio de T2.6 al pie de la letra. Un test que escribiera
// `len(prov.llamadas) != stages.MaxItemsPorPedido` PASARÍA CON CUALQUIER VALOR de la
// constante —incluido un 40 que devolvería el fan-out a no tener techo—, porque estaría
// comparando la constante consigo misma. Lo que este banco protege es EL NÚMERO 10, que
// es una decisión de producto (D5, D-044.39) con un coste de plaza detrás.
//
// El fixture es sintético (12 ítems numerados) y no el de Ambar, que tiene 3: aquí no se
// mide calidad de extracción —eso es de T2.3— sino CUÁNTAS VECES se llama al modelo.
// ---------------------------------------------------------------------------

// literalNumerado compone un literal con `n` peticiones, con la forma real que sale del
// compositor del flush (rótulos y una línea `cliente:` por mensaje). Cada línea es la
// evidencia de SU idea, así que el anclaje de P3 las acepta todas y ningún ítem se aísla
// por un motivo que no sea el tope.
func literalNumerado(n int) string {
	var b strings.Builder
	b.WriteString("### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n")
	for i := range n {
		fmt.Fprintf(&b, "cliente: %s\n", evidenciaNumerada(i))
	}
	b.WriteString("### FIN DE LOS MENSAJES ###")
	return b.String()
}

// ideaNumerada / evidenciaNumerada son la idea `i` y la frase del cliente que la
// respalda. Se derivan del índice para que el orden de la lista sea el mismo al que
// apunta `ItemAislado.IdeaPos` y se pueda afirmar QUÉ ítems se atendieron, no solo
// cuántos.
func ideaNumerada(i int) string      { return "torta del pedido numero " + strconv.Itoa(i) }
func evidenciaNumerada(i int) string { return "quiero una " + ideaNumerada(i) + " por favor" }

// ideasNumeradas son las `n` ideas tal como P3 las recibe de P2.
func ideasNumeradas(n int) []llm.Want {
	out := make([]llm.Want, 0, n)
	for i := range n {
		out = append(out, llm.Want{Idea: ideaNumerada(i), Evidence: evidenciaNumerada(i)})
	}
	return out
}

// respondeAlItemNumerado es el fake que contesta bien a la primera a CUALQUIER idea
// numerada, anclando la spec a la evidencia de esa idea. Si el tope dejara de aplicarse,
// contestaría a las 12 igual de bien: el fake no es quien limita nada.
func respondeAlItemNumerado(t *testing.T) func(string, int) respuestaP3 {
	t.Helper()
	return func(idea string, _ int) respuestaP3 {
		i := strings.TrimPrefix(idea, "torta del pedido numero ")
		n, err := strconv.Atoi(i)
		if err != nil {
			t.Fatalf("el fake recibió una idea que no es del fixture: %q", idea)
		}
		return respuestaP3{raw: specDe(t, "torta", "", evidenciaNumerada(n), nil, nil)}
	}
}

// assertLlamóALosPrimeros comprueba que las llamadas que se hicieron fueron las de las
// PRIMERAS ideas y en orden. Sin esto, «10 llamadas» lo cumpliría también un código que
// atendiera las ideas 2..11 y marcara las 0 y 1, que sería otra política distinta y
// silenciosamente peor (el cliente pide primero lo que más le importa).
func assertLlamóALosPrimeros(t *testing.T, llamadas []llamadaP3, ideas []llm.Want) {
	t.Helper()
	for i, ll := range llamadas {
		if ll.in.Idea != ideas[i].Idea {
			t.Fatalf("la llamada %d pidió %q; se esperaba la idea %d (%q)", i, ll.in.Idea, i, ideas[i].Idea)
		}
	}
}

// assertMarcados comprueba que las posiciones `desde..desde+n-1` están en la lista de
// aislados con el motivo del tope, y NINGUNA otra.
//
// 🔴 El motivo va como LITERAL `"over_limit"` y no como `stages.MotivoTope` por la misma
// razón que el 10: ese valor se serializa al artefacto y lo lee la bandeja del dueño
// (Ola 3). Es un valor de contrato, y compararlo contra su propia constante dejaría
// pasar un renombrado que rompería al lector.
func assertMarcados(t *testing.T, isolated []stages.ItemAislado, desde, n int) {
	t.Helper()
	vistos := map[int]bool{}
	for _, it := range isolated {
		if it.Reason != "over_limit" {
			continue
		}
		if it.IdeaPos < desde || it.IdeaPos >= desde+n {
			t.Fatalf("se marcó por tope la idea %d, fuera del rango [%d,%d)", it.IdeaPos, desde, desde+n)
		}
		vistos[it.IdeaPos] = true
	}
	if len(vistos) != n {
		t.Fatalf("ítems marcados por tope = %d, se esperaban %d (lista completa: %+v)", len(vistos), n, isolated)
	}
}

// ---------------------------------------------------------------------------
// CRITERIO DE T2.6 · el pedido que SUPERA el tope
// ---------------------------------------------------------------------------

// TestP3_PedidoDeDoceItems_HaceDiezLlamadasYMarcaLasDosQueSobran es el criterio literal
// de T2.6: un job con N > 10 (aquí 12) hace EXACTAMENTE 10 llamadas de P3, no falla, y
// el borrador resultante lleva los 2 ítems sobrantes PRESENTES Y MARCADOS.
//
// # LAS DOS MITADES SON DOS ASERCIONES DISTINTAS, Y LAS DOS HACEN FALTA
//
//  1. **El contador de llamadas** dice que la plaza única no se gastó de más: son 10 ×
//     22–32 s en vez de 12. Contar solo las líneas del borrador NO lo vería, porque un
//     código que llamara 12 veces y se quedara con 10 daría el mismo borrador.
//  2. **Las líneas del borrador** dicen que el pedido del cliente NO SE PERDIÓ: 10 + 2 =
//     12, las mismas que entraron. Contar solo las llamadas NO lo vería, porque tirar
//     los 2 sobrantes en silencio da exactamente 10 llamadas también. Es la trampa que
//     el propio criterio de T2.6 deja escrita.
//
// ¿Qué tendría que pasar para que este test fallara?
//
// 💥 MUTACIONES EJECUTADAS, las seis rojas (todas COMPILAN):
//   - `MaxItemsPorPedido = 20` (subir el tope) ⇒ 12 llamadas y 0 marcados;
//   - `MaxItemsPorPedido = 9` (bajarlo) ⇒ 9 llamadas y 3 marcados;
//   - en `acotarAlTope`, `return ideas, 0` siempre (quitar el tope) ⇒ 12 llamadas;
//   - en `acotarAlTope`, la cota del slice a `[:MaxItemsPorPedido-1]` (off-by-one real);
//   - en `Run`, pasarle `ideas` enteras a `fanOut` y acotar DESPUÉS (`art.Items =
//     art.Items[:MaxItemsPorPedido]`) ⇒ 12 llamadas: el borrador sale bien y la plaza se
//     gastó igual, que es justo lo que el contador existe para cazar;
//   - en `marcarSobreTope`, no marcar nada (el descarte silencioso) ⇒ 10 líneas de 12.
func TestP3_PedidoDeDoceItems_HaceDiezLlamadasYMarcaLasDosQueSobran(t *testing.T) {
	const ideasDelPedido = 12 // > el tope, a propósito
	const llamadasEsperadas = 10
	const marcadosEsperados = 2

	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, respondeAlItemNumerado(t), &buf)
	ideas := ideasNumeradas(ideasDelPedido)

	art, err := p3.Run(context.Background(), jobDeAmbar(), literalNumerado(ideasDelPedido), ideas)
	if err != nil {
		t.Fatalf("Run: %v — superar el tope NO es un fallo del job", err)
	}

	// (1) La plaza única no se gastó de más.
	if len(prov.llamadas) != llamadasEsperadas {
		t.Fatalf("llamadas al modelo = %d para un pedido de %d ítems; se esperaban exactamente %d "+
			"(cada llamada de más son 22–32 s de la plaza única del Edge)",
			len(prov.llamadas), ideasDelPedido, llamadasEsperadas)
	}
	assertLlamóALosPrimeros(t, prov.llamadas, ideas)

	// (2) El pedido del cliente NO se perdió: las líneas que entraron son las que salen.
	if len(art.Items) != llamadasEsperadas {
		t.Fatalf("items especificados = %d, se esperaban %d", len(art.Items), llamadasEsperadas)
	}
	if lineas := len(art.Items) + len(art.Isolated); lineas != ideasDelPedido {
		t.Fatalf("líneas del borrador = %d (%d items + %d aislados); tienen que ser las %d que pidió el "+
			"cliente: los sobrantes se MARCAN, nunca se descartan",
			lineas, len(art.Items), len(art.Isolated), ideasDelPedido)
	}
	assertMarcados(t, art.Isolated, llamadasEsperadas, marcadosEsperados)

	// (3) Y eso es lo que quedó EN LA BASE, no solo lo que devolvió Run.
	guardado := assertPersistidoUnaVez(t, store, llamadasEsperadas, marcadosEsperados)
	assertNadaDeTextoDelCliente(t, guardado.Payload, buf.String(), evidenciaNumerada(11))
}

// TestP3_ElTopeDejaSuLineaConLasCUENTAS_SEPARADAS mira el aviso, que es la mitad
// operativa: sin él, un pedido truncado no deja rastro en el log y el dueño solo se
// entera si abre la bandeja.
//
// 🔴 `items_sobre_tope` VA APARTE de `items_aislados` y ésa es la aserción que importa.
// Los dos estados que caben en «aislado» piden cosas OPUESTAS al dueño: `quality` y
// `evidence` le dicen «mira tu Ollama»; `over_limit`, «habla con el cliente». Un solo
// número los funde y miente en la mitad de los casos — la misma lección que el aviso de
// «sesión pasiva» que decía lo que no era.
//
// 💥 MUTACIÓN EJECUTADA: en `Run`, quitar el campo `items_sobre_tope` de la línea Info
// ⇒ rojo. Y en `marcarSobreTope`, subir el `if n <= 0` a `if n < 0` NO cambia nada aquí
// (con 12 ideas n vale 2): esa guarda la cubre el test del pedido que cabe.
func TestP3_ElTopeDejaSuLineaConLasCUENTAS_SEPARADAS(t *testing.T) {
	var buf bytes.Buffer
	p3, _, _, _ := etapaP3(t, respondeAlItemNumerado(t), &buf)

	if _, err := p3.Run(context.Background(), jobDeAmbar(), literalNumerado(12), ideasNumeradas(12)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := buf.String()
	for _, campo := range []string{"tope=10", "atendidas=10", "sobre_tope=2", "items_sobre_tope=2"} {
		if !strings.Contains(log, campo) {
			t.Fatalf("el log no lleva %q, así que un pedido truncado no deja rastro accionable: %q", campo, log)
		}
	}
}

// ---------------------------------------------------------------------------
// NO-REGRESIÓN · el pedido que CABE
// ---------------------------------------------------------------------------

// TestP3_PedidoDeExactamenteDiezItems_NiRecortaNiMarca es el lado de ACÁ del corte: con
// exactamente 10 ítems no se recorta nada, no se marca nada y no hay aviso. Es la
// no-regresión que pide el criterio («con N ≤ 10, el comportamiento de hoy»); el caso de
// 3 ítems lo cubre TestP3_TresItems_TresLlamadas, que no se ha tocado.
//
// 🔴 LO QUE ESTE TEST **NO** PROTEGE, Y COSTÓ DESCUBRIRLO UNA MUTACIÓN VERDE. El
// docstring decía aquí que fijaba el off-by-one, y era **FALSO**: con 10 ideas, el
// atajo `len(ideas) <= MaxItemsPorPedido` de `acotarAlTope` devuelve la lista entera
// ANTES de llegar a la cota del slice, así que un fallo en esa cota es INVISIBLE desde
// aquí. Medido:
//
//   - 💥 `acotarAlTope`, `<=` → `<`: **VERDE, y con razón** — es un NO-OP. Con
//     `len == Max`, `ideas[:Max]` es la lista entera y la cuenta de sobrantes es 0: el
//     atajo y el camino largo dicen exactamente lo mismo. No era una mutación.
//   - 💥 `ideas[:MaxItemsPorPedido-1]` (off-by-one REAL en la cota): rojo, pero en el
//     test de los 12 ítems, **no en éste**.
//
// El off-by-one del atajo lo cierra TestP3_OnceItems_ElPRIMERO_QueSOBRA_SeMarca.
//
// 💥 MUTACIÓN EJECUTADA, roja: `MaxItemsPorPedido = 9` ⇒ 9 llamadas y 1 marcado. Es lo
// que este test sí protege: que el número no baje por debajo de lo prometido.
func TestP3_PedidoDeExactamenteDiezItems_NiRecortaNiMarca(t *testing.T) {
	const ideasDelPedido = 10 // JUSTO el tope

	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, respondeAlItemNumerado(t), &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(),
		literalNumerado(ideasDelPedido), ideasNumeradas(ideasDelPedido))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(prov.llamadas) != ideasDelPedido {
		t.Fatalf("llamadas = %d para un pedido de %d ítems que CABE; se esperaban %d",
			len(prov.llamadas), ideasDelPedido, ideasDelPedido)
	}
	if len(art.Items) != ideasDelPedido || len(art.Isolated) != 0 {
		t.Fatalf("artefacto = %d items y %d aislados; un pedido que cabe no marca nada",
			len(art.Items), len(art.Isolated))
	}
	assertPersistidoUnaVez(t, store, ideasDelPedido, 0)

	// Ni el aviso, ni una cuenta distinta de cero. Se comprueban los DOS: el aviso es lo
	// que ve el operador y la cuenta es lo que ve quien agrega el log.
	log := buf.String()
	if strings.Contains(log, "supera el tope") {
		t.Fatalf("un pedido que CABE dejó el aviso del tope: %q", log)
	}
	if !strings.Contains(log, "items_sobre_tope=0") {
		t.Fatalf("la línea de cierre no dice que no se topó nada: %q", log)
	}
}

// TestP3_OnceItems_ElPRIMERO_QueSOBRA_SeMarca es el ítem número 11: el PRIMERO que no
// cabe. Existe porque una mutación salió VERDE y enseñó el agujero — ensanchar el atajo
// de `acotarAlTope` a `len(ideas) <= MaxItemsPorPedido+1` dejaba el tope INERTE justo
// para este caso, y ni el test de 10 ni el de 12 lo veían: el de 10 se va por el atajo
// (correctamente) y el de 12 tiene margen de sobra para que un tope de 11 siga cortando.
//
// La lección, escrita donde muerde: **un corte se prueba en sus DOS lados y en el
// primero del otro lado.** 10 dice «esto entra», 11 dice «esto ya no» y 12 dice «y el
// resto tampoco». Sin el 11, la frontera está probada solo por un lado.
//
// 💥 MUTACIONES EJECUTADAS, las dos rojas:
//   - en `acotarAlTope`, `<= MaxItemsPorPedido` → `<= MaxItemsPorPedido+1` ⇒ 11 llamadas
//     y 0 marcados (era VERDE antes de que existiera este test);
//   - `MaxItemsPorPedido = 20` ⇒ 11 llamadas y 0 marcados.
func TestP3_OnceItems_ElPRIMERO_QueSOBRA_SeMarca(t *testing.T) {
	const ideasDelPedido = 11 // el tope + 1: el primero que NO cabe
	const llamadasEsperadas = 10

	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, respondeAlItemNumerado(t), &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(),
		literalNumerado(ideasDelPedido), ideasNumeradas(ideasDelPedido))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(prov.llamadas) != llamadasEsperadas {
		t.Fatalf("llamadas = %d para %d ítems; el ítem que sobra NO se pregunta al modelo (se esperaban %d)",
			len(prov.llamadas), ideasDelPedido, llamadasEsperadas)
	}
	if lineas := len(art.Items) + len(art.Isolated); lineas != ideasDelPedido {
		t.Fatalf("líneas del borrador = %d; el cliente pidió %d y ninguna se descarta", lineas, ideasDelPedido)
	}
	assertMarcados(t, art.Isolated, llamadasEsperadas, 1)
	assertPersistidoUnaVez(t, store, llamadasEsperadas, 1)
}
