package quotetext

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL VERIFICADOR DE PRECIOS — INV-2: EL LLM NUNCA CALCULA PRECIOS
// ════════════════════════════════════════════════════════════════════════════
//
// Éste es el corazón de T5.1, y no el prompt. El prompt PIDE que los importes se
// copien del borrador; esto lo GARANTIZA.
//
// # LO QUE HAY QUE GARANTIZAR NO ES «QUE NO INVENTE NÚMEROS»
//
// La primera versión de esta regla comparaba CONJUNTOS: «¿este importe del texto sale
// de alguna línea?». Cerraba el caso obvio —un monto nuevo— y dejaba abierto el que de
// verdad le importa a la dueña, porque estos tres textos no inventan NADA y aun así le
// mandan al cliente un precio que no es el suyo:
//
//	· SWAP           — los precios de dos líneas, intercambiados;
//	· CARGO NUEVO    — «Seña por adelantado: $490», con un 490 que es el envío;
//	· REPETICIÓN     — «y el segundo también a $2100».
//
// Los tres pasaban con OK. Por eso la regla ya no pregunta si el importe EXISTE, sino
// si está DONDE LE TOCA y las veces que le toca.
//
// # LA REGLA, ENTERA
//
// Del Borrador sale una SECUENCIA ESPERADA de importes, en el orden en que un mensaje
// los diría: por cada línea con precio, su `unit_price` y —solo si la cantidad es
// mayor que uno— su `line_total`; y al final, el `total` del pedido. Es EXACTAMENTE lo
// que escribe el render determinista, y esa coincidencia no es casual: la secuencia
// define qué es una cotización bien puesta, y el render es la que siempre lo cumple.
//
// De ahí se derivan `ESPERADOS` (el conjunto de esa secuencia), `PERMITIDOS`
// (ESPERADOS más los números que ya viven en las etiquetas, las personalizaciones y las
// cantidades del borrador) y el `TECHO` = máx(ESPERADOS).
//
// Del texto se extraen todos los números y cada uno se clasifica en dos clases:
//
//	MARCADO — lleva marca de dinero pegada: «$» delante, o una palabra de moneda
//	          detrás («pesos», «clp», «usd»…).
//	DESNUDO — cualquier otro número.
//
// El texto se ACEPTA si y solo si:
//
//	C3  todo importe MARCADO está en ESPERADOS                    (no se inventa nada)
//	C4  ningún número DESNUDO por encima del TECHO es ajeno a PERMITIDOS
//	C1  cada `unit_price` > 0 aparece MARCADO en el texto          (cobertura)
//	C2  el total aparece MARCADO en el texto                       (cobertura)
//	C5  la SECUENCIA de importes marcados del texto es EXACTAMENTE la esperada
//
// C1 y C2 están contenidas en C5 y se comprueban antes igualmente: dan el diagnóstico
// útil («falta el precio de la línea 2») donde C5 solo diría «no cuadran».
//
// # POR QUÉ C5 ES UNA SECUENCIA Y NO UN RECUENTO
//
// Porque contar apariciones —un multiconjunto— NO cierra el SWAP: intercambiar dos
// precios legítimos deja exactamente los mismos importes con las mismas
// multiplicidades. Lo único que cambia es el ORDEN, así que el orden es lo que hay que
// mirar. De paso, la igualdad de secuencias implica la de multiconjuntos, de modo que
// el cargo inventado y la repetición caen también.
//
// EL PRECIO QUE SE PAGA, DICHO CLARO: un texto correcto que enumere las líneas en otro
// orden que el borrador se rechaza y sale el determinista. Es conservador a propósito
// —el prompt le da las líneas en orden y le prohíbe añadir o quitar— y el coste es un
// texto más sobrio, nunca un precio equivocado.
//
// # LOS CASOS LEGÍTIMOS EN QUE UN IMPORTE SE REPITE, Y QUÉ SE DECIDIÓ
//
//   - **Dos líneas al mismo precio**: la secuencia lo espera dos veces y el texto tiene
//     que decirlo dos veces. «Las dos a $2100» (una sola aparición) se rechaza ⇒
//     determinista. Es el lado conservador y está probado.
//   - **Una sola línea con cantidad uno**: su `unit_price` y el `total` son el mismo
//     número, y la secuencia lo espera DOS veces, una como precio y otra como total. Un
//     texto que solo lo diga una vez cae. También es deliberado: el cliente tiene que
//     leer el total, y el render lo escribe siempre.
//   - **`line_total` con cantidad uno**: NO entra en la secuencia, porque es idéntico
//     al unitario y exigirlo obligaría a escribir el mismo número dos veces seguidas.
//
// # POR QUÉ C4 MIRA EL TECHO Y NO EL SUELO
//
// Distinguir un importe de una cantidad en prosa no se puede hacer con certeza: lo
// único seguro es la marca de moneda. La primera versión de C4 rechazaba todo número
// desnudo por encima del importe MÁS BARATO del pedido, y eso tenía una consecuencia
// medida y silenciosa: con una galleta de $2 en el carrito, el listón caía a 2 y «te
// llamo en 3 días», «es para el 30 de agosto» y «12 porciones» se rechazaban los tres.
// El generador dejaba de funcionar de facto en cuanto el pedido llevara algo barato, y
// el único síntoma era un `fallback_reason` en un log que nadie lee. Un fallo mudo.
//
// Ahora el listón es el TECHO —el importe más caro del pedido— y la lectura es otra: un
// número desnudo por encima de todo lo que este pedido cuesta es un número que el
// cliente puede leer como un precio mayor, y ninguna fecha, hora ni cantidad razonable
// llega ahí. Por debajo del techo, un desnudo no se juzga.
//
// 🔴 ESO DEJA UN AGUJERO Y SE DECLARA: «el total sería 3000» —sin `$` y por debajo del
// techo— pasa. La red fuerte contra los precios falsos son C3 y C5, que miran lo que el
// cliente lee COMO PRECIO; C4 es una red estrecha, y estrecha es mejor que apagada.
//
// # LA PRECISIÓN ES LA DEL DINERO: CÉNTIMOS
//
// Todo —los importes del borrador y los números leídos del texto— se redondea a dos
// decimales antes de compararse, que es la misma precisión con la que `Importe` los
// escribe. Sin eso, un `unit_price` de 2100,005 se imprimiría como `$2100,01` y el
// propio render determinista no pasaría su propio verificador. Dos importes que
// redondeen al mismo céntimo se funden a propósito: si no se distinguen en el texto,
// tampoco se pueden verificar por separado.
//
// # CONSERVADOR ANTE LA DUDA
//
// Un `$2.100` es 2100 en es-CL y 2,10 en en-US. `aNumero` resuelve la ambigüedad con
// una regla escrita, pero si la resuelve al revés de como lo pensó el modelo, el valor
// que salga no estará en ESPERADOS y el texto se rechaza. Ése es el desenlace correcto:
// no hay ninguna lectura del texto bajo la cual se le mande al cliente un importe que
// no salió de las líneas.
// ════════════════════════════════════════════════════════════════════════════

// toleranciaImporte es cuánto pueden diferir dos importes para considerarse el mismo:
// medio céntimo.
//
// Existe porque `UnitPrice` es float64 y no centavos (así está en `intakes.Item` y en
// la columna), y `qty × unit_price` en binario no da exactamente lo que da en decimal:
// 3 × 0,1 es 0,30000000000000004. Comparar con `==` haría que un texto correcto se
// rechazara por el último bit de la mantisa.
const toleranciaImporte = 0.005

// MaxRunasTexto acota la salida del modelo. No es una regla de negocio: es la cota que
// impide que una respuesta desbocada —un modelo en bucle repitiendo la lista— se
// convierta en un mensaje de WhatsApp de un megabyte y en un recorrido de regex sobre
// él. Una cotización real del caso base cabe en unas 400 runas.
const MaxRunasTexto = 4000

// Motivos por los que un texto NO se acepta. Son un vocabulario CERRADO porque salen
// por el log y por la API (`fallback_reason`), y una cadena libre ahí sería un campo
// que nadie puede agregar.
const (
	// MotivoSinImportes — el borrador no tiene ni un importe positivo, así que no hay
	// nada que verificar. Pasa con un pedido cuyas líneas están todas por confirmar.
	MotivoSinImportes = "borrador_sin_importes"
	// MotivoTextoIlegible — la salida no es texto utilizable: no es UTF-8, trae
	// caracteres de control, viene vacía o se pasa de MaxRunasTexto.
	MotivoTextoIlegible = "texto_ilegible"
	// MotivoNumeroIlegible — hay un número en el texto que no se puede leer como
	// número (desbordado, o con separadores imposibles). ES UN ERROR DE DATO y no un
	// pánico: ver aNumero.
	MotivoNumeroIlegible = "numero_ilegible"
	// MotivoSinImportesEnTexto — el texto no trae NI UN importe marcado. No dice los
	// precios, así que no es una cotización.
	MotivoSinImportesEnTexto = "texto_sin_importes"
	// MotivoFaltaUnitario — falta en el texto el precio unitario de alguna línea (C1).
	MotivoFaltaUnitario = "falta_precio_de_linea"
	// MotivoFaltaTotal — falta en el texto el total (C2).
	MotivoFaltaTotal = "falta_total"
	// MotivoImporteAjeno — el texto trae un importe que no sale de ninguna línea (C3).
	MotivoImporteAjeno = "importe_ajeno"
	// MotivoNumeroAjeno — el texto trae un número sin marca de moneda, por encima de
	// todo lo que cuesta el pedido, que tampoco sale del borrador (C4).
	MotivoNumeroAjeno = "numero_ajeno"
	// MotivoImportesFueraDeSitio — todos los importes del texto salen del borrador,
	// pero no están donde les toca: sobran, faltan o van en otro orden (C5). Es el
	// motivo del SWAP, del cargo inventado con importe reutilizado y de la repetición.
	MotivoImportesFueraDeSitio = "importes_fuera_de_sitio"
)

// Veredicto es el resultado de verificar un texto contra su borrador.
type Veredicto struct {
	// OK dice si el texto se puede mandar.
	OK bool
	// Motivo es uno de los Motivo* cuando OK es falso, y vacío cuando es cierto.
	Motivo string
	// Detalle amplía el motivo PARA EL LOG.
	//
	// 🔴 NUNCA LLEVA UN FRAGMENTO DEL TEXTO. Solo números y nombres de campo. El
	// texto es una cotización redactada, y aunque el prompt no le da al modelo nada
	// del cliente, lo que un modelo escribe no es material que este código deba
	// copiar a un log (misma regla que P2/P3/P4 con la evidencia, ADR-0034 / INV-6).
	Detalle string
}

// errNumeroIlegible es la familia de «este número no se puede leer».
var errNumeroIlegible = errors.New("quotetext: número ilegible")

// reNumero encuentra un número del texto junto con su marca de dinero, si la lleva.
//
// Los tres grupos son, en orden: el símbolo de moneda pegado por delante, el número
// —dígitos con puntos y comas dentro—, y la palabra de moneda pegada por detrás.
//
// La clase del número NO incluye el guion, y eso es lo que hace que «10-12 porciones»
// dé dos números y no uno raro.
var reNumero = regexp.MustCompile(`(?i)(\$)?\s?(\d[\d.,]*)\s?(pesos?|clp|usd|bs|soles?|d[oó]lar(?:es)?)?`)

// numeroDelTexto es UN número encontrado, ya leído y clasificado.
type numeroDelTexto struct {
	valor float64
	// marcado dice si venía con marca de dinero. Es la clase de C3 frente a C4.
	marcado bool
}

// ValidarSalida comprueba que lo que devolvió el modelo es texto utilizable, ANTES de
// buscarle números o de dárselo a nadie.
//
// 🔴 VA PRIMERO Y NO ES CEREMONIA. Lo que sale de aquí acaba en un `rendered_text`
// —una columna TEXT de Postgres, que rechaza `0x00` con SQLSTATE 22021— y en un
// mensaje de WhatsApp. Un carácter de control colado convertiría un fallo de calidad
// del modelo en un 500 de la base, tres capas más abajo y sin relación aparente.
//
// El salto de línea, el retorno y el tabulador SÍ pasan: son el formato del mensaje.
func ValidarSalida(texto string) error {
	if !utf8.ValidString(texto) {
		return fmt.Errorf("%s: la salida no es UTF-8", MotivoTextoIlegible)
	}
	if strings.TrimSpace(texto) == "" {
		return fmt.Errorf("%s: la salida está vacía", MotivoTextoIlegible)
	}
	if n := utf8.RuneCountInString(texto); n > MaxRunasTexto {
		return fmt.Errorf("%s: la salida tiene %d runas y el tope es %d", MotivoTextoIlegible, n, MaxRunasTexto)
	}
	for _, r := range texto {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("%s: la salida trae un carácter de control (U+%04X)", MotivoTextoIlegible, r)
		}
	}
	return nil
}

// Verificar aplica C1–C5 (ver la cabecera del fichero). Es PURA: sin BD, sin reloj y
// sin log.
func Verificar(b Borrador, texto string) Veredicto {
	if err := ValidarSalida(texto); err != nil {
		return Veredicto{Motivo: MotivoTextoIlegible, Detalle: err.Error()}
	}
	quiero := SecuenciaEsperada(b)
	if len(quiero) == 0 {
		return Veredicto{Motivo: MotivoSinImportes, Detalle: "ninguna línea del borrador tiene importe"}
	}
	nums, err := extraerNumeros(texto)
	if err != nil {
		return Veredicto{Motivo: MotivoNumeroIlegible, Detalle: err.Error()}
	}

	esperados := conjuntoDe(quiero)
	techo := maximo(esperados)
	permitidos := append(append([]float64(nil), esperados...), numerosDelBorrador(b)...)

	marcados := make([]float64, 0, len(nums))
	for _, n := range nums {
		if n.marcado {
			// C3 — el importe no sale de ninguna línea. Se comprueba antes que C5
			// porque el diagnóstico es distinto y mucho más claro: «este número no
			// existe» frente a «existe pero está mal puesto».
			if !contiene(esperados, n.valor) {
				return Veredicto{Motivo: MotivoImporteAjeno,
					Detalle: fmt.Sprintf("el texto trae el importe %s y no sale de ninguna línea", Importe(n.valor))}
			}
			marcados = append(marcados, n.valor)
			continue
		}
		// C4 — ver «por qué el techo y no el suelo» en la cabecera.
		if n.valor > techo && !contiene(permitidos, n.valor) {
			return Veredicto{Motivo: MotivoNumeroAjeno,
				Detalle: fmt.Sprintf("el texto trae el número %s, por encima de lo más caro del pedido (%s), y no sale del borrador",
					strconv.FormatFloat(n.valor, 'f', -1, 64), Importe(techo))}
		}
	}
	if len(marcados) == 0 {
		return Veredicto{Motivo: MotivoSinImportesEnTexto, Detalle: "el texto no dice ni un precio"}
	}
	if v := cobertura(b, marcados); !v.OK {
		return v
	}
	return mismaSecuencia(quiero, marcados)
}

// cobertura aplica C1 y C2: que estén TODOS los unitarios y el total.
//
// Está contenida en C5 —una secuencia igual los contiene por definición— y se conserva
// porque el mensaje que produce es el que sirve para arreglar el prompt: «la línea 2
// vale $2950 y ese importe no está en el texto» dice dónde mirar; «los importes no
// cuadran» no.
func cobertura(b Borrador, marcados []float64) Veredicto {
	for i := range b.Lineas {
		p := aCentimos(b.Lineas[i].UnitPrice)
		if p <= 0 {
			// Línea por confirmar: no tiene precio que exigir. El texto que la
			// menciona sin importe es correcto —es lo que hace el render— y el que
			// se inventara uno caería por C3, que sí la mira.
			continue
		}
		if !contiene(marcados, p) {
			return Veredicto{Motivo: MotivoFaltaUnitario,
				Detalle: fmt.Sprintf("la línea %d vale %s y ese importe no está en el texto", i+1, Importe(p))}
		}
	}
	if !contiene(marcados, aCentimos(b.Total)) {
		return Veredicto{Motivo: MotivoFaltaTotal,
			Detalle: fmt.Sprintf("el total es %s y no está en el texto", Importe(b.Total))}
	}
	return Veredicto{OK: true}
}

// mismaSecuencia aplica C5: los importes marcados del texto, en su orden de aparición,
// tienen que ser EXACTAMENTE los esperados.
//
// El detalle nombra el PUESTO y los dos importes, que es lo que permite ver de un
// vistazo si lo que pasó fue un swap (dos puestos cruzados) o un cargo de más (las
// longitudes difieren). Nunca cita el texto.
func mismaSecuencia(quiero, tengo []float64) Veredicto {
	if len(tengo) != len(quiero) {
		return Veredicto{Motivo: MotivoImportesFueraDeSitio,
			Detalle: fmt.Sprintf("el texto dice %d importes y el presupuesto tiene %d", len(tengo), len(quiero))}
	}
	for i := range quiero {
		if !mismoImporte(tengo[i], quiero[i]) {
			return Veredicto{Motivo: MotivoImportesFueraDeSitio,
				Detalle: fmt.Sprintf("el importe nº %d del texto es %s y ahí va %s", i+1, Importe(tengo[i]), Importe(quiero[i]))}
		}
	}
	return Veredicto{OK: true}
}

// SecuenciaEsperada es el orden en que los importes del borrador tienen que aparecer en
// el texto: por cada línea con precio su unitario, y su total de línea solo si la
// cantidad es mayor que uno; y al final, el total del pedido.
//
// Se exporta porque es la definición de «cotización bien puesta» que comparten el
// verificador y el render: `Render` produce EXACTAMENTE esta secuencia, y un test lo
// exige. Tenerlas en dos sitios es como se desincronizan.
//
// Devuelve vacío cuando no hay ni un importe (todas las líneas por confirmar), y ese
// vacío es lo que el llamante traduce a MotivoSinImportes.
func SecuenciaEsperada(b Borrador) []float64 {
	out := make([]float64, 0, 2*len(b.Lineas)+1)
	for _, l := range b.Lineas {
		p := aCentimos(l.UnitPrice)
		if p <= 0 {
			continue
		}
		out = append(out, p)
		if l.Qty > 1 {
			out = append(out, aCentimos(l.LineTotal))
		}
	}
	if t := aCentimos(b.Total); t > 0 {
		out = append(out, t)
	}
	return out
}

// conjuntoDe deduplica una secuencia conservando el orden. Es la fuente ÚNICA de
// ESPERADOS: derivarlo de la secuencia en vez de calcularlo aparte es lo que impide
// que C3 y C5 acaben opinando cosas distintas sobre qué importes existen.
func conjuntoDe(seq []float64) []float64 {
	out := make([]float64, 0, len(seq))
	for _, v := range seq {
		if !contiene(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// numerosDelBorrador saca los números que ya viven en el borrador FUERA de los
// importes: las cantidades y los que van dentro de las etiquetas y las
// personalizaciones («10-12 porciones», «paquete x30»).
//
// Son los que C4 perdona por encima del techo, y el motivo es directo: el prompt le
// pide al modelo que copie el borrador, así que un número que ya estaba ahí no lo
// inventó él. Los que no se pueden leer se descartan en silencio —no aportan permiso—,
// que es el lado conservador.
func numerosDelBorrador(b Borrador) []float64 {
	out := make([]float64, 0, 2*len(b.Lineas))
	for _, l := range b.Lineas {
		if !contiene(out, float64(l.Qty)) {
			out = append(out, float64(l.Qty))
		}
		for _, m := range reNumero.FindAllStringSubmatch(l.Label+" "+l.Customization, -1) {
			v, err := aNumero(m[2])
			if err != nil || contiene(out, v) {
				continue
			}
			out = append(out, v)
		}
	}
	return out
}

// extraerNumeros recorre el texto y devuelve cada número con su clase. Un número que
// no se puede leer NO se salta: aborta la verificación entera con error de dato.
//
// 🔴 ESO ES A PROPÓSITO Y ES LA LECCIÓN CARA DE ESTE PLAN: lo que viene del modelo se
// valida en Go ANTES de convertirlo o de indexar con ello. Saltarse el número raro
// dejaría pasar un texto del que no se puede afirmar nada.
func extraerNumeros(texto string) ([]numeroDelTexto, error) {
	ms := reNumero.FindAllStringSubmatch(texto, -1)
	out := make([]numeroDelTexto, 0, len(ms))
	for _, m := range ms {
		v, err := aNumero(m[2])
		if err != nil {
			return nil, err
		}
		out = append(out, numeroDelTexto{valor: v, marcado: m[1] != "" || m[3] != ""})
	}
	return out, nil
}

// aNumero lee un número del texto resolviendo los separadores, y lo devuelve YA
// REDONDEADO A CÉNTIMOS. Es la contraparte de Importe.
//
// # LA REGLA DE LOS SEPARADORES, QUE ES LO ÚNICO INTERESANTE DE ESTA FUNCIÓN
//
//   - Con punto Y coma, el ÚLTIMO de los dos manda como decimal y el otro es de miles
//     («2.950,00» ⇒ 2950; «2,950.00» ⇒ 2950).
//   - Con uno solo: es DECIMAL si aparece una vez y le siguen exactamente uno o dos
//     dígitos («2100,50» ⇒ 2100,5); en cualquier otro caso es de MILES y se borra
//     («2.100» ⇒ 2100, «1.234.567» ⇒ 1234567, «2100.» ⇒ 2100).
//
// La regla se equivoca con «$2.10» escrito por un modelo que quería decir 2100. NO SE
// ARREGLA AQUÍ y no hace falta: el valor que salga (2,10) no estará en ESPERADOS y el
// texto se rechazará. Ver «conservador ante la duda» en la cabecera.
//
// Devuelve error —nunca un cero silencioso y nunca un pánico— cuando el literal
// desborda el float64 o no es un número.
func aNumero(s string) (float64, error) {
	// 🔴 LA PUNTUACIÓN DE LA FRASE SE RECORTA PRIMERO, y no es cosmética: un «$1234,50.»
	// al final de una oración deja el punto DENTRO del literal, y entonces la regla de
	// abajo ve punto Y coma, elige el punto como decimal y devuelve 123450 — cien veces
	// el importe real, y con pinta de importe inventado. Un número no acaba nunca en
	// separador, así que recortarlos por la derecha no puede perder información.
	s = strings.TrimRight(strings.TrimSpace(s), ".,")
	ultPunto, ultComa := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case ultPunto >= 0 && ultComa >= 0:
		dec := "."
		if ultComa > ultPunto {
			dec = ","
		}
		s = strings.ReplaceAll(s, mil(dec), "")
		s = strings.Replace(s, dec, ".", 1)
	case ultPunto >= 0 || ultComa >= 0:
		sep := "."
		if ultComa >= 0 {
			sep = ","
		}
		if esDecimal(s, sep) {
			s = strings.Replace(s, sep, ".", 1)
		} else {
			s = strings.ReplaceAll(s, sep, "")
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %d caracteres, separadores no interpretables", errNumeroIlegible, len(s))
	}
	// El redondeo va ANTES de la comprobación de finitud a propósito: multiplicar por
	// 100 un float que ya rozaba el máximo lo desborda, y ese desbordamiento tiene que
	// salir como error de dato igual que el del parseo.
	v = aCentimos(v)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: el valor no es finito", errNumeroIlegible)
	}
	return v, nil
}

// aCentimos redondea a la precisión con la que se escribe el dinero, que es la misma
// que usa Importe. Ver «la precisión es la del dinero» en la cabecera.
func aCentimos(v float64) float64 { return math.Round(v*100) / 100 }

// mil devuelve el separador de miles dado el decimal.
func mil(dec string) string {
	if dec == "." {
		return ","
	}
	return "."
}

// esDecimal dice si `sep` aparece UNA vez en `s` y le siguen uno o dos dígitos: la
// forma de un decimal y no la de un separador de miles.
func esDecimal(s, sep string) bool {
	if strings.Count(s, sep) != 1 {
		return false
	}
	cola := s[strings.Index(s, sep)+1:]
	if len(cola) == 0 || len(cola) > 2 {
		return false
	}
	for _, r := range cola {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// contiene dice si `v` está en `vals` con la tolerancia de importe.
func contiene(vals []float64, v float64) bool {
	for _, x := range vals {
		if mismoImporte(x, v) {
			return true
		}
	}
	return false
}

// mismoImporte compara dos montos con toleranciaImporte. Ver allí por qué no es `==`.
func mismoImporte(a, b float64) bool { return math.Abs(a-b) < toleranciaImporte }

// maximo es el mayor de un conjunto NO VACÍO. El llamante ya cortó con el vacío
// (MotivoSinImportes), y por eso aquí no hay un cero de cortesía que después alguien
// interpretaría como un techo de verdad.
func maximo(vals []float64) float64 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
