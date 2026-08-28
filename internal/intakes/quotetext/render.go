package quotetext

import (
	"strconv"
	"strings"
)

// ════════════════════════════════════════════════════════════════════════════
// EL RENDER DETERMINISTA — SU FORMATO ES UNA DECISIÓN DE ESTA TAREA
// ════════════════════════════════════════════════════════════════════════════
//
// 🔴 QUE QUEDE ESCRITO: NINGÚN DOCUMENTO DEL PLAN ESPECIFICA ESTE FORMATO. Lo único
// que hay es el objetivo, y es una frase: «sus cotizaciones ya son *producto + tamaño
// + specs + precio + qué incluye*» (design.md §1 punto 8, doc 08 §2h). El render de
// design §7.5 —el de la bandeja, con puntos suspensivos y «SIN MATCH»— NO sirve aquí:
// aquél es lo que ve el DUEÑO y esto es lo que lee el CLIENTE.
//
// Así que el formato de abajo lo elegí yo en T5.1, contra ese objetivo y contra el
// único material real que existe (design.md §1: «Pastel para 15 personas, chocolate
// húmedo, relleno chocolate y oreo… 2100. Incluye impresiones no comestibles»).
// No es un contrato heredado: quien quiera cambiarlo no rompe nada de aguas arriba.
//
// LAS CUATRO DECISIONES, con su porqué:
//
//  1. **Producto, tamaño y specs van EN LA ETIQUETA.** No se parten en campos porque
//     en la línea persistida no están partidos: `Item.Label` ya trae «Torta chocolate
//     húmedo + crema choc. — 10-12 porciones». Inventar aquí una gramática para
//     trocearla sería adivinar.
//  2. **«Qué incluye» = `Customization`**, en su propia línea y con la palabra
//     «Incluye». Es la personalización NO FACTURABLE (INV-13): aparece en el texto
//     porque el cliente tiene que leerla, y NO aparece en ningún importe.
//  3. **Los importes se escriben SIN separador de miles** (`$2100`, no `$2.100`) y con
//     coma decimal solo cuando hay decimales. Es lo que escribió Herminia en el caso
//     real («2100», «2950 pesos»), y de paso elimina la ambigüedad que un `$2.100`
//     tiene entre es-CL y en-US — ambigüedad que el verificador SÍ tiene que resolver
//     cuando la comete el modelo, pero que este render no necesita crear.
//  4. **No hay saludo con nombre y no hay firma.** El nombre del cliente sería PII que
//     este paquete no tiene ni pide (el `contact_id` es OPACO, ADR-0010), y la firma
//     corporativa es justo lo que el prompt de P5 prohíbe.
//
// El render es una FUNCIÓN PURA sobre el Borrador: sin BD, sin reloj, sin log. Eso es
// lo que permite el test que de verdad importa — que su propia salida PASA el
// verificador de precios— y por tanto que el respaldo no pueda ser peor que lo que
// respalda.
// ════════════════════════════════════════════════════════════════════════════

// Piezas fijas del render. Se declaran como constantes y no inline porque los tests
// asertan POR ESTRUCTURA, y comparar contra estas constantes es lo que evita que el
// test sea una copia literal del texto que pretende comprobar.
const (
	saludoRender = "Hola! Te paso el presupuesto:"
	cierreRender = "Cualquier duda me dices y lo ajustamos."
	// vinetaRender abre cada línea del detalle.
	vinetaRender = "• "
	// prefijoIncluye abre la línea del «qué incluye».
	prefijoIncluye = "Incluye: "
	// rotuloTotal abre la línea del total.
	rotuloTotal = "Total: "
	// sufijoUnitario acompaña al precio unitario cuando la cantidad es mayor que uno,
	// para que el importe grande de al lado no se lea como el precio de una unidad.
	sufijoUnitario = " c/u"
	// textoPorConfirmar es lo que se escribe donde iría un importe que todavía no
	// existe. NO es «$0»: ver Linea.PorConfirmar.
	textoPorConfirmar = "precio por confirmar"
	// separadorCampos une los campos de una línea.
	separadorCampos = " — "
	// simboloMoneda es la marca que hace que un número del texto sea un IMPORTE. El
	// verificador la busca literalmente, así que cambiarla aquí y no allí rompería la
	// cobertura sin romper ningún test de este fichero: por eso es UNA constante y no
	// dos literales.
	simboloMoneda = "$"
)

// Render redacta la cotización SIN LLM: producto, tamaño y specs de la etiqueta, el
// precio, y lo que incluye. Es el respaldo, y es también lo que se devuelve cuando el
// tenant no tiene historial del que imitar una voz.
//
// Es determinista en el sentido fuerte: la misma entrada da byte a byte la misma
// salida, siempre.
func Render(b Borrador) string {
	var sb strings.Builder
	sb.WriteString(saludoRender)
	sb.WriteString("\n\n")
	for _, l := range b.Lineas {
		escribirLinea(&sb, l)
	}
	sb.WriteString("\n")
	sb.WriteString(rotuloTotal)
	sb.WriteString(Importe(b.Total))
	sb.WriteString("\n\n")
	sb.WriteString(cierreRender)
	return sb.String()
}

// escribirLinea pinta UNA línea del detalle con su «qué incluye» si lo tiene.
//
// La cantidad solo se escribe cuando es mayor que uno: «1 × Torta» es ruido, y además
// mete en el texto un número desnudo que el verificador tendría que perdonar sin
// necesidad.
func escribirLinea(sb *strings.Builder, l Linea) {
	sb.WriteString(vinetaRender)
	if l.Qty > 1 {
		sb.WriteString(strconv.Itoa(l.Qty))
		sb.WriteString(" × ")
	}
	sb.WriteString(l.Label)
	sb.WriteString(separadorCampos)
	switch {
	case l.PorConfirmar:
		sb.WriteString(textoPorConfirmar)
	case l.Qty > 1:
		// Los DOS importes: el unitario (que es lo que el verificador exige ver) y el
		// de la línea (que es lo que el cliente suma). Escribir solo uno obligaría a
		// quien lee a multiplicar o a dividir.
		sb.WriteString(Importe(l.UnitPrice))
		sb.WriteString(sufijoUnitario)
		sb.WriteString(separadorCampos)
		sb.WriteString(Importe(l.LineTotal))
	default:
		sb.WriteString(Importe(l.UnitPrice))
	}
	sb.WriteString("\n")
	if l.Customization != "" {
		sb.WriteString("  ")
		sb.WriteString(prefijoIncluye)
		sb.WriteString(l.Customization)
		sb.WriteString("\n")
	}
}

// Importe formatea un monto como lo escribe este render: símbolo pegado, sin
// separador de miles, y coma decimal SOLO si hay decimales.
//
// Se exporta porque es la contraparte de `aNumero` (precios.go): uno escribe y el otro
// lee, y tenerlos separados sin poder cruzarlos en un test haría que la pareja se
// desincronizara en silencio. Lo custodia TestImporte_YaNumero_SonInversas.
//
// Los decimales se redondean a dos con `strconv.FormatFloat`, que es media vuelta de
// tuerca sobre `toleranciaImporte`: un valor a menos de medio centavo de un entero se
// escribe como el entero.
func Importe(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	return simboloMoneda + strings.Replace(s, ".", ",", 1)
}
