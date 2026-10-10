// Porta internal/flujos/events/summary.go @ 9d5a4b6 (trozo: las dos SALIDAS del resumen).
//
// summary.go pasaba de 600 líneas con su lógica y se parte por tema (05 E-13), solo moviendo
// declaraciones: allí quedan los tipos, las fuentes durables y cómo se ARMA y se persiste un
// resumen; aquí, cómo SALE: Encode (la fila que se guarda) y Render (lo que el cliente lee), con
// el vocabulario de niveles y el formato de importes que solo usa el render.

package events

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// total es el importe de la línea. Las indicaciones NO lo tocan (INV-13): sale de
// qty × unit_price y de nada más.
func (l SummaryLine) total() float64 { return float64(l.Qty) * l.UnitPrice }

// Niveles de la sub-máquina del carrito, traducidos a lo que el cliente lee.
//
// Faltan a propósito los dos terminales (closed, cancelled): en un pedido ya
// confirmado o ya cancelado no hay un «te quedaste» que sea cierto, y decirlo
// sería contarle al cliente que dejó a medias algo que terminó. Un nivel que no
// esté en el mapa —uno nuevo, o el vacío— simplemente no imprime esa línea: el
// resumen pierde un matiz, que es mejor que enseñar jerga interna.
var summaryCartLevelWords = map[string]string{
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

// Encode serializa el resumen para Store.AppendSummary, que exige
// json.RawMessage justamente para que por esa puerta no entre prosa (ver el doc
// del paquete): lo que sale de aquí es estructura, y por eso puede vivir EN CLARO
// en payload (nivel 1, ADR-0034).
//
// La serialización es estable —campos en orden fijo, sin mapas— así que dos
// resúmenes del mismo estado son el mismo byte.
//
// La forma, literal: {"kind":"cart","level":"summary","lines":[{"sku":"CAFE","label":"Café","qty":2,
// "unit_price":2.5,"customization":"sin azúcar"}]} — `level`, `lines`, `answers` y `customization`
// se omiten vacíos, y las respuestas van como [{"question_id":"p1","answer_code":"a"}]. El TOTAL NO
// se serializa: es derivado (INV-13).
//
// Texto de error (literal): "events: serializar el resumen: %w".
func (s Summary) Encode() (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("events: serializar el resumen: %w", err)
	}
	return b, nil
}

// Render arma el texto que LEE EL CLIENTE al reanudar («esto es lo que ya habías
// decidido»). Un resumen vacío devuelve la cadena vacía, no un encabezado sin
// nada debajo: así, quien no comprobó Empty tampoco puede mandar un mensaje hueco.
//
// Despacha por CONTENIDO y no por Kind a propósito: lo que hay que enseñar son
// líneas o respuestas, y un tipo nuevo que acumule líneas se renderiza bien sin
// tocar esta función.
//
// Con líneas, literal:
//
//	Esto es lo que ya habías decidido en tu pedido:
//	Café x2  $5.00
//	   ✏️ sin azúcar
//	Té x1  $2.00
//	TOTAL  $7.00
//	Te quedaste decidiendo si agregar algo más.
//
// La primera línea nombra el tipo con KindName(Kind) («pedido», nunca «carrito» ni «cart»). Cada
// línea es "<Label> x<Qty>  $<Qty × UnitPrice con dos decimales>"; la indicación, si la hay, va en
// SU sub-línea, "\n   ✏️ <indicación>". INV-13: el TOTAL sale solo de Σ qty × unit_price —la
// indicación no lo toca— como "TOTAL  $5.00". La última línea, «Te quedaste <frase>.», solo si el
// nivel tiene traducción: categories → "eligiendo una categoría", articles → "eligiendo un
// artículo", article → "mirando un artículo", variant → "eligiendo una presentación", quantity →
// "eligiendo la cantidad", continue → "decidiendo si agregar algo más", summary → "revisando el
// resumen del pedido", item_note_scope e item_note → "escribiendo una indicación", order_note →
// "escribiendo una indicación para todo el pedido", buyer_data → "completando tus datos"; un nivel
// vacío, terminal (closed, cancelled) o desconocido no imprime esa línea. Nunca aparece un
// identificador (ni el SKU, ni el nivel interno, ni el id del evento).
//
// Con respuestas, literal: "Ya habías respondido 1 pregunta de tu encuesta." o "Ya habías
// respondido 3 preguntas de tu encuesta." (cuántas, no cuáles).
func (s Summary) Render() string {
	switch {
	case len(s.Lines) > 0:
		return s.renderLines()
	case len(s.Answers) > 0:
		return s.renderAnswers()
	default:
		return ""
	}
}

// renderLines pinta las líneas decididas con la MISMA forma que la pantalla de
// resumen del propio carrito («Café x2  $5.00», la indicación como sub-línea
// debajo de lo que describe, el TOTAL al final). Que el cliente vea el mismo
// renglón al confirmar y al retomar es lo que le permite reconocer su pedido; dos
// formatos distintos para lo mismo le harían dudar de si es lo mismo.
func (s Summary) renderLines() string {
	var b strings.Builder
	b.WriteString("Esto es lo que ya habías decidido en tu ")
	b.WriteString(KindName(s.Kind))
	b.WriteString(":")

	var total float64
	for _, l := range s.Lines {
		b.WriteString("\n")
		b.WriteString(l.Label)
		b.WriteString(" x")
		b.WriteString(strconv.Itoa(l.Qty))
		b.WriteString("  ")
		b.WriteString(summaryMoney(l.total()))
		if l.Customization != "" {
			b.WriteString("\n   ✏️ ")
			b.WriteString(l.Customization)
		}
		total += l.total()
	}
	b.WriteString("\nTOTAL  ")
	b.WriteString(summaryMoney(total))

	if phrase, ok := summaryCartLevelWords[s.Level]; ok {
		b.WriteString("\nTe quedaste ")
		b.WriteString(phrase)
		b.WriteString(".")
	}
	return b.String()
}

// renderAnswers dice CUÁNTAS preguntas llevaba respondidas y no cuáles, porque
// cuáles no se puede decir sin mentir: el estado durable guarda ids y códigos
// (question_id → answer_code), y el texto de la pregunta vive en la definición
// del flujo, que pudo editarse desde entonces. Enseñar «p3: b» sería jerga; ir a
// buscar el texto de hoy sería atribuirle al cliente una respuesta a una pregunta
// que quizá no es la que le hicimos. El detalle queda en el payload, que es donde
// sirve (para analizar, no para leer en WhatsApp).
func (s Summary) renderAnswers() string {
	n := len(s.Answers)
	noun := " preguntas de tu "
	if n == 1 {
		noun = " pregunta de tu "
	}
	return "Ya habías respondido " + strconv.Itoa(n) + noun + KindName(s.Kind) + "."
}

// summaryMoney formatea un importe con el MISMO formato que las pantallas del
// carrito (cart/screens.go: money). Se redeclara por la misma razón que los
// niveles —este paquete no importa el módulo— y el test lo fija contra los
// precios reales del catálogo.
func summaryMoney(v float64) string { return fmt.Sprintf("$%.2f", v) }
