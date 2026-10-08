// Porta internal/intakes/quotetext/render.go @ 36d5a04

package quotetext

import (
	"strconv"
	"strings"
)

// ════════════════════════════════════════════════════════════════════════════
// EL RENDER DETERMINISTA — SU FORMATO ES UNA DECISIÓN DE T5.1
// ════════════════════════════════════════════════════════════════════════════
//
// 🔴 NINGÚN DOCUMENTO DEL PLAN 044 ESPECIFICA ESTE FORMATO. Lo único que hay es el
// objetivo: «sus cotizaciones ya son *producto + tamaño + specs + precio + qué
// incluye*». El render de la bandeja NO sirve aquí: aquél es lo que ve el DUEÑO y
// esto es lo que lee el CLIENTE.
//
// LAS CUATRO DECISIONES, con su porqué:
//
//  1. **Producto, tamaño y specs van EN LA ETIQUETA.** No se parten en campos porque
//     en la línea persistida no están partidos. Inventar aquí una gramática para
//     trocearla sería adivinar.
//  2. **«Qué incluye» = `Customization`**, en su propia línea y con la palabra
//     «Incluye». Es la personalización NO FACTURABLE (INV-13): aparece en el texto
//     porque el cliente tiene que leerla, y NO aparece en ningún importe.
//  3. **Los importes se escriben SIN separador de miles** (`$2100`, no `$2.100`) y con
//     coma decimal solo cuando hay decimales. Es lo que escribió la dueña en el caso
//     real, y de paso elimina la ambigüedad que un `$2.100` tiene entre es-CL y en-US.
//  4. **No hay saludo con nombre y no hay firma.** El nombre del cliente sería PII que
//     este paquete no tiene ni pide (el `contact_id` es OPACO, ADR-0010), y la firma
//     corporativa es justo lo que el prompt de P5 prohíbe.
// ════════════════════════════════════════════════════════════════════════════

// Piezas fijas del render. Son texto observable: lo lee el cliente por WhatsApp.
const (
	renderGreeting = "Hola! Te paso el presupuesto:"
	renderClosing  = "Cualquier duda me dices y lo ajustamos."
	// renderBullet abre cada línea del detalle.
	renderBullet = "• "
	// includesPrefix abre la línea del «qué incluye».
	includesPrefix = "Incluye: "
	// totalLabel abre la línea del total.
	totalLabel = "Total: "
	// unitSuffix acompaña al precio unitario cuando la cantidad es mayor que uno,
	// para que el importe grande de al lado no se lea como el precio de una unidad.
	unitSuffix = " c/u"
	// pendingPriceText es lo que se escribe donde iría un importe que todavía no
	// existe. NO es «$0»: ver Line.PendingPrice.
	pendingPriceText = "precio por confirmar"
	// fieldSeparator une los campos de una línea.
	fieldSeparator = " — "
	// currencySymbol es la marca que hace que un número del texto sea un IMPORTE. El
	// verificador la busca literalmente, así que cambiarla aquí y no allí rompería la
	// cobertura: por eso es UNA constante y no dos literales.
	currencySymbol = "$"
)

// Render redacta la cotización SIN LLM. Es el respaldo, y es también lo que se
// devuelve cuando el tenant no tiene historial del que imitar una voz. Es una función
// PURA y determinista en el sentido fuerte: la misma entrada da byte a byte la misma
// salida, siempre.
//
// El texto, en este orden:
//
//   - el saludo `Hola! Te paso el presupuesto:` y una línea en blanco;
//   - por cada línea del borrador, en su orden, `• <etiqueta> — <precio>`:
//     con cantidad 1, el precio es Amount(UnitPrice); con cantidad mayor que 1 la
//     línea es `• <qty> × <etiqueta> — <unitario> c/u — <total de línea>`; y una línea
//     PendingPrice escribe `precio por confirmar` en vez de un importe (nunca `$0`),
//     tenga la cantidad que tenga. La cantidad 1 NO se escribe: «1 ×» es ruido;
//   - debajo de la línea que trae Customization, `  Incluye: <personalización>`;
//   - una línea en blanco, `Total: <Amount(Total)>`, otra en blanco y el cierre
//     `Cualquier duda me dices y lo ajustamos.`, sin salto de línea final.
//
// No hay saludo con nombre ni firma. Un borrador sin líneas da el saludo, el total
// (`$0`) y el cierre.
func Render(draft Draft) string {
	var sb strings.Builder
	sb.WriteString(renderGreeting)
	sb.WriteString("\n\n")
	for _, line := range draft.Lines {
		writeLine(&sb, line)
	}
	sb.WriteString("\n")
	sb.WriteString(totalLabel)
	sb.WriteString(Amount(draft.Total))
	sb.WriteString("\n\n")
	sb.WriteString(renderClosing)
	return sb.String()
}

// writeLine pinta UNA línea del detalle con su «qué incluye» si lo tiene.
func writeLine(sb *strings.Builder, line Line) {
	sb.WriteString(renderBullet)
	if line.Qty > 1 {
		sb.WriteString(strconv.Itoa(line.Qty))
		sb.WriteString(" × ")
	}
	sb.WriteString(line.Label)
	sb.WriteString(fieldSeparator)
	switch {
	case line.PendingPrice:
		sb.WriteString(pendingPriceText)
	case line.Qty > 1:
		// Los DOS importes: el unitario (que es lo que el verificador exige ver) y el
		// de la línea (que es lo que el cliente suma). Escribir solo uno obligaría a
		// quien lee a multiplicar o a dividir.
		sb.WriteString(Amount(line.UnitPrice))
		sb.WriteString(unitSuffix)
		sb.WriteString(fieldSeparator)
		sb.WriteString(Amount(line.LineTotal))
	default:
		sb.WriteString(Amount(line.UnitPrice))
	}
	sb.WriteString("\n")
	if line.Customization != "" {
		sb.WriteString("  ")
		sb.WriteString(includesPrefix)
		sb.WriteString(line.Customization)
		sb.WriteString("\n")
	}
}

// Amount formatea un monto como lo escribe este render: el símbolo `$` pegado, sin
// separador de miles, redondeado a dos decimales y con coma decimal SOLO si quedan
// decimales (`$2100`, `$1234,50`, `$0,99`). Un valor que redondea a un entero se
// escribe como el entero (`2100,004` ⇒ `$2100`).
//
// Se exporta porque es la contraparte del lector de números del verificador
// (precios.go): uno escribe y el otro lee, y tenerlos separados sin poder cruzarlos
// en un test haría que la pareja se desincronizara en silencio.
//
// Era `Importe` en el paquete viejo.
func Amount(value float64) string {
	s := strconv.FormatFloat(value, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	return currencySymbol + strings.Replace(s, ".", ",", 1)
}
