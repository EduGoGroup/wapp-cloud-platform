// Porta internal/intakes/quotetext/precios.go @ 36d5a04

package quotetext

import (
	"fmt"
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
// Del Draft sale una SECUENCIA ESPERADA de importes, en el orden en que un mensaje
// los diría: por cada línea con precio, su `unit_price` y —solo si la cantidad es
// mayor que uno— su `line_total`; y al final, el `total` del pedido. Es EXACTAMENTE lo
// que escribe el render determinista, y esa coincidencia no es casual: la secuencia
// define qué es una cotización bien puesta, y el render es la que siempre lo cumple.
//
// De ahí se derivan `ESPERADOS` (el conjunto de esa secuencia), `PERMITIDOS`
// (ESPERADOS más los números que ya viven en las etiquetas, las personalizaciones y las
// cantidades del borrador) y el `TECHO` = máx(ESPERADOS).
//
// Del texto se extraen todos los números —dígitos ASCII con puntos y comas dentro; el
// guion NO forma parte, así que «10-12 porciones» son dos números— y cada uno se
// clasifica en dos clases:
//
//	MARCADO — lleva marca de dinero pegada: «$» delante, o detrás una palabra de
//	          moneda sin distinguir mayúsculas («peso», «pesos», «clp», «usd», «bs»,
//	          «soles», «dolar», «dólar», «dolares», «dólares»; «sol», en singular,
//	          NO lo es). Entre la marca y el número cabe, como mucho, UN blanco
//	          ASCII (espacio, tabulador, salto): con un espacio de no separación o
//	          con dos espacios la marca no cuenta. La palabra delante («USD 2950»)
//	          y el símbolo detrás («2950 $») tampoco.
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
// decimales antes de compararse, que es la misma precisión con la que `Amount` los
// escribe. Sin eso, un `unit_price` de 2100,005 se imprimiría como `$2100,01` y el
// propio render determinista no pasaría su propio verificador. Dos importes que
// redondeen al mismo céntimo se funden a propósito: si no se distinguen en el texto,
// tampoco se pueden verificar por separado.
//
// # CONSERVADOR ANTE LA DUDA
//
// Un `$2.100` es 2100 en es-CL y 2,10 en en-US. `parseNumber` resuelve la ambigüedad con
// una regla escrita, pero si la resuelve al revés de como lo pensó el modelo, el valor
// que salga no estará en ESPERADOS y el texto se rechaza. Ése es el desenlace correcto:
// no hay ninguna lectura del texto bajo la cual se le mande al cliente un importe que
// no salió de las líneas.
// ════════════════════════════════════════════════════════════════════════════

// MaxTextRunes acota la salida del modelo: 4000 runas. No es una regla de negocio: es
// la cota que impide que una respuesta desbocada —un modelo en bucle repitiendo la
// lista— se convierta en un mensaje de WhatsApp de un megabyte.
//
// Era `MaxRunasTexto` en el paquete viejo.
const MaxTextRunes = 4000

// Motivos por los que un texto NO se acepta. Son un vocabulario CERRADO porque salen
// por el log y por la API (`fallback_reason`): los VALORES no cambian aunque el
// identificador esté en inglés. Eran los `Motivo…` del paquete viejo.
const (
	// ReasonDraftWithoutAmounts — el borrador no tiene ni un importe positivo, así
	// que no hay nada que verificar. Pasa con un pedido cuyas líneas están todas por
	// confirmar. Era `MotivoSinImportes`.
	ReasonDraftWithoutAmounts = "borrador_sin_importes"
	// ReasonUnreadableText — la salida no es texto utilizable: no es UTF-8, trae
	// caracteres de control, viene vacía o se pasa de MaxTextRunes. Era
	// `MotivoTextoIlegible`.
	ReasonUnreadableText = "texto_ilegible"
	// ReasonUnreadableNumber — hay un número en el texto que no se puede leer como
	// número. ES UN ERROR DE DATO y no un pánico. Era `MotivoNumeroIlegible`.
	ReasonUnreadableNumber = "numero_ilegible"
	// ReasonTextWithoutAmounts — el texto no trae NI UN importe marcado. No dice los
	// precios, así que no es una cotización. Era `MotivoSinImportesEnTexto`.
	ReasonTextWithoutAmounts = "texto_sin_importes"
	// ReasonMissingUnitPrice — falta en el texto el precio unitario de alguna línea
	// (C1). Era `MotivoFaltaUnitario`.
	ReasonMissingUnitPrice = "falta_precio_de_linea"
	// ReasonMissingTotal — falta en el texto el total (C2). Era `MotivoFaltaTotal`.
	ReasonMissingTotal = "falta_total"
	// ReasonForeignAmount — el texto trae un importe que no sale de ninguna línea
	// (C3). Era `MotivoImporteAjeno`.
	ReasonForeignAmount = "importe_ajeno"
	// ReasonForeignNumber — el texto trae un número sin marca de moneda, por encima
	// de todo lo que cuesta el pedido, que tampoco sale del borrador (C4). Era
	// `MotivoNumeroAjeno`.
	ReasonForeignNumber = "numero_ajeno"
	// ReasonAmountsOutOfPlace — todos los importes del texto salen del borrador,
	// pero no están donde les toca: sobran, faltan o van en otro orden (C5). Es el
	// motivo del intercambio, del cargo inventado con importe reutilizado y de la
	// repetición. Era `MotivoImportesFueraDeSitio`.
	ReasonAmountsOutOfPlace = "importes_fuera_de_sitio"
)

// Verdict es el resultado de verificar un texto contra su borrador.
//
// Era `Veredicto` en el paquete viejo.
type Verdict struct {
	// OK dice si el texto se puede mandar.
	OK bool
	// Reason es uno de los Reason… de este fichero cuando OK es falso, y vacío
	// cuando es cierto. Era `Motivo`.
	Reason string
	// Detail amplía el motivo PARA EL LOG. Vacío cuando OK es cierto; nunca vacío en
	// un rechazo. Era `Detalle`.
	//
	// 🔴 NUNCA LLEVA UN FRAGMENTO DEL TEXTO. Solo números y nombres de campo. El
	// texto es una cotización redactada, y lo que un modelo escribe no es material
	// que este código deba copiar a un log (misma regla que P2/P3/P4 con la
	// evidencia, ADR-0034 / INV-6).
	Detail string
}

// ValidateOutput comprueba que lo que devolvió el modelo es texto utilizable, ANTES
// de buscarle números o de dárselo a nadie. Devuelve nil si lo es y, si no, un error
// cuyo texto empieza por ReasonUnreadableText, en este orden de comprobación:
//
//   - no es UTF-8 válido ⇒ `texto_ilegible: la salida no es UTF-8`;
//   - está vacío o solo tiene blancos ⇒ `texto_ilegible: la salida está vacía`;
//   - pasa de MaxTextRunes RUNAS (no bytes; justo MaxTextRunes es válido) ⇒
//     `texto_ilegible: la salida tiene <n> runas y el tope es 4000`;
//   - trae un carácter de control (categoría Cc: el nulo, ESC, DEL, U+0085…) ⇒
//     `texto_ilegible: la salida trae un carácter de control (U+XXXX)`, con el
//     PRIMERO que aparezca en hexadecimal de al menos cuatro cifras.
//
// El salto de línea, el retorno y el tabulador SÍ pasan: son el formato del mensaje.
//
// 🔴 VA PRIMERO Y NO ES CEREMONIA. Lo que sale de aquí acaba en un `rendered_text`
// —una columna TEXT de Postgres, que rechaza `0x00` con SQLSTATE 22021— y en un
// mensaje de WhatsApp.
//
// Era `ValidarSalida` en el paquete viejo.
func ValidateOutput(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("%s: la salida no es UTF-8", ReasonUnreadableText)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s: la salida está vacía", ReasonUnreadableText)
	}
	if n := utf8.RuneCountInString(text); n > MaxTextRunes {
		return fmt.Errorf("%s: la salida tiene %d runas y el tope es %d", ReasonUnreadableText, n, MaxTextRunes)
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("%s: la salida trae un carácter de control (U+%04X)", ReasonUnreadableText, r)
		}
	}
	return nil
}

// Verify aplica C1–C5 (ver la cabecera del fichero) y dice si el texto se puede
// mandar: Verdict{OK: true} sin motivo ni detalle, o un rechazo con su Reason y un
// Detail que nunca cita el texto. Es PURA: sin BD, sin reloj y sin log.
//
// El orden de las comprobaciones es contrato, porque decide qué motivo sale cuando
// fallan varias. Gana la PRIMERA que falle:
//
//  1. ValidateOutput ⇒ ReasonUnreadableText, con el texto de ese error como Detail;
//  2. ExpectedSequence vacía ⇒ ReasonDraftWithoutAmounts, Detail `ninguna línea del
//     borrador tiene importe`;
//  3. un número que no se puede leer ⇒ ReasonUnreadableNumber. Detail
//     `quotetext: número ilegible: <n> caracteres, separadores no interpretables`
//     (n = largo del literal ya limpio; desborda el float64) o `quotetext: número
//     ilegible: el valor no es finito` (deja de ser finito al pasarlo a céntimos).
//     Vale para marcados y desnudos;
//  4. recorriendo los números EN EL ORDEN DEL TEXTO: un marcado fuera de ESPERADOS ⇒
//     ReasonForeignAmount (C3), Detail `el texto trae el importe <Amount> y no sale
//     de ninguna línea`; un desnudo por encima del TECHO y fuera de PERMITIDOS ⇒
//     ReasonForeignNumber (C4), Detail `el texto trae el número <n>, por encima de lo
//     más caro del pedido (<Amount del techo>), y no sale del borrador`;
//  5. ni un importe marcado ⇒ ReasonTextWithoutAmounts, Detail `el texto no dice ni
//     un precio`;
//  6. C1, línea a línea en el orden del borrador (las que no tienen unitario positivo
//     se saltan: no hay precio que exigir, y el que se inventara uno cae por C3) ⇒
//     ReasonMissingUnitPrice, Detail `la línea <i> vale <Amount> y ese importe no
//     está en el texto` (i desde 1);
//  7. C2 ⇒ ReasonMissingTotal, Detail `el total es <Amount> y no está en el texto`;
//  8. C5 ⇒ ReasonAmountsOutOfPlace: si el número de importes marcados no es el de la
//     secuencia, Detail `el texto dice <n> importes y el presupuesto tiene <m>`; si
//     coincide, el primer puesto que difiere, `el importe nº <i> del texto es
//     <Amount> y ahí va <Amount>` (i desde 1).
//
// Consecuencias que son contrato y que los tests viejos fijaron:
//
//   - el texto que produce Render para un borrador con algún importe pasa SIEMPRE
//     (también con decimales de más de dos cifras, cantidades mayores que uno, líneas
//     por confirmar y etiquetas con números mayores que el precio): el respaldo no
//     puede ser peor que lo que respalda;
//   - dos líneas al mismo precio son legítimas si el texto lo dice dos veces; «las
//     dos a $2100» (una sola aparición) se rechaza;
//   - con UNA línea de cantidad uno, su precio y el total son el mismo número y el
//     texto tiene que decirlo DOS veces;
//   - los números que ya estaban en el borrador (etiquetas, personalizaciones,
//     cantidades) se perdonan como desnudos aunque superen el techo; los que no se
//     pueden leer ahí no aportan permiso y no dan error;
//   - una línea barata no convierte fechas, horas ni cantidades en un rechazo.
//
// Era `Verificar` en el paquete viejo.
func Verify(draft Draft, text string) Verdict {
	if err := ValidateOutput(text); err != nil {
		return Verdict{Reason: ReasonUnreadableText, Detail: err.Error()}
	}
	want := ExpectedSequence(draft)
	if len(want) == 0 {
		return Verdict{Reason: ReasonDraftWithoutAmounts, Detail: "ninguna línea del borrador tiene importe"}
	}
	numbers, err := extractNumbers(text)
	if err != nil {
		return Verdict{Reason: ReasonUnreadableNumber, Detail: err.Error()}
	}

	expected := setOf(want)
	ceiling := maxOf(expected)
	allowed := append(append([]float64(nil), expected...), draftNumbers(draft)...)

	marked := make([]float64, 0, len(numbers))
	for _, n := range numbers {
		if n.marked {
			// C3 — el importe no sale de ninguna línea. Se comprueba antes que C5
			// porque el diagnóstico es distinto y mucho más claro: «este número no
			// existe» frente a «existe pero está mal puesto».
			if !containsAmount(expected, n.value) {
				return Verdict{Reason: ReasonForeignAmount,
					Detail: fmt.Sprintf("el texto trae el importe %s y no sale de ninguna línea", Amount(n.value))}
			}
			marked = append(marked, n.value)
			continue
		}
		// C4 — ver «por qué el techo y no el suelo» en la cabecera.
		if n.value > ceiling && !containsAmount(allowed, n.value) {
			return Verdict{Reason: ReasonForeignNumber,
				Detail: fmt.Sprintf("el texto trae el número %s, por encima de lo más caro del pedido (%s), y no sale del borrador",
					strconv.FormatFloat(n.value, 'f', -1, 64), Amount(ceiling))}
		}
	}
	if len(marked) == 0 {
		return Verdict{Reason: ReasonTextWithoutAmounts, Detail: "el texto no dice ni un precio"}
	}
	if verdict := coverage(draft, marked); !verdict.OK {
		return verdict
	}
	return sameSequence(want, marked)
}

// coverage aplica C1 y C2: que estén TODOS los unitarios y el total.
//
// Está contenida en C5 —una secuencia igual los contiene por definición— y se conserva
// porque el mensaje que produce es el que sirve para arreglar el prompt: «la línea 2
// vale $2950 y ese importe no está en el texto» dice dónde mirar; «los importes no
// cuadran» no.
//
// Era `cobertura` en el paquete viejo.
func coverage(draft Draft, marked []float64) Verdict {
	for i := range draft.Lines {
		price := toCents(draft.Lines[i].UnitPrice)
		if price <= 0 {
			// Línea por confirmar: no tiene precio que exigir. El texto que la
			// menciona sin importe es correcto —es lo que hace el render— y el que
			// se inventara uno caería por C3, que sí la mira.
			continue
		}
		if !containsAmount(marked, price) {
			return Verdict{Reason: ReasonMissingUnitPrice,
				Detail: fmt.Sprintf("la línea %d vale %s y ese importe no está en el texto", i+1, Amount(price))}
		}
	}
	if !containsAmount(marked, toCents(draft.Total)) {
		return Verdict{Reason: ReasonMissingTotal,
			Detail: fmt.Sprintf("el total es %s y no está en el texto", Amount(draft.Total))}
	}
	return Verdict{OK: true}
}

// sameSequence aplica C5: los importes marcados del texto, en su orden de aparición,
// tienen que ser EXACTAMENTE los esperados.
//
// El detalle nombra el PUESTO y los dos importes, que es lo que permite ver de un
// vistazo si lo que pasó fue un swap (dos puestos cruzados) o un cargo de más (las
// longitudes difieren). Nunca cita el texto.
//
// Era `mismaSecuencia` en el paquete viejo.
func sameSequence(want, got []float64) Verdict {
	if len(got) != len(want) {
		return Verdict{Reason: ReasonAmountsOutOfPlace,
			Detail: fmt.Sprintf("el texto dice %d importes y el presupuesto tiene %d", len(got), len(want))}
	}
	for i := range want {
		if !sameAmount(got[i], want[i]) {
			return Verdict{Reason: ReasonAmountsOutOfPlace,
				Detail: fmt.Sprintf("el importe nº %d del texto es %s y ahí va %s", i+1, Amount(got[i]), Amount(want[i]))}
		}
	}
	return Verdict{OK: true}
}

// ExpectedSequence es el orden en que los importes del borrador tienen que aparecer
// en el texto, ya redondeados a céntimos: por cada línea con unitario positivo su
// `unit_price` y, solo si la cantidad es mayor que uno, su `line_total`; y al final,
// el `total` del pedido si es positivo. El `line_total` con cantidad uno NO entra: es
// idéntico al unitario y exigirlo obligaría a escribir el mismo número dos veces
// seguidas. Una línea sin unitario positivo (por confirmar) no aporta nada.
//
// Se exporta porque es la definición de «cotización bien puesta» que comparten el
// verificador y el render: los importes que escribe Render son EXACTAMENTE esta
// secuencia.
//
// Devuelve una lista vacía (largo 0) cuando no hay ni un importe —todas las líneas
// por confirmar, o ninguna línea—, y ese vacío es lo que el llamante traduce a
// ReasonDraftWithoutAmounts. No muta el borrador.
//
// Era `SecuenciaEsperada` en el paquete viejo.
func ExpectedSequence(draft Draft) []float64 {
	out := make([]float64, 0, 2*len(draft.Lines)+1)
	for _, line := range draft.Lines {
		price := toCents(line.UnitPrice)
		if price <= 0 {
			continue
		}
		out = append(out, price)
		if line.Qty > 1 {
			out = append(out, toCents(line.LineTotal))
		}
	}
	if total := toCents(draft.Total); total > 0 {
		out = append(out, total)
	}
	return out
}

// setOf deduplica una secuencia conservando el orden. Es la fuente ÚNICA de
// ESPERADOS: derivarlo de la secuencia en vez de calcularlo aparte es lo que impide
// que C3 y C5 acaben opinando cosas distintas sobre qué importes existen.
//
// Era `conjuntoDe` en el paquete viejo.
func setOf(sequence []float64) []float64 {
	out := make([]float64, 0, len(sequence))
	for _, value := range sequence {
		if !containsAmount(out, value) {
			out = append(out, value)
		}
	}
	return out
}
