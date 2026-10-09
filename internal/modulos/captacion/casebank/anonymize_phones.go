// Porta internal/casebank/anonimizar.go @ 8d875ab

package casebank

import (
	"regexp"
	"unicode/utf8"
)

// anonymize_phones.go — la mitad de TELÉFONOS de anonymize.go, partida por tamaño
// (05 E-13): los dos umbrales, el patrón del candidato, el conteo y el reparto de
// una racha de varios números. El alcance entero, con lo que NO se cubre, está en
// la cabecera de anonymize.go.

// Los dos umbrales del teléfono, en DÍGITOS (no en longitud de la cadena: los
// separadores no cuentan).
//
// 🔴 EL SUELO ES EL PARÁMETRO QUE IMPORTA. Con 8, «10 o 12 porciones», «paquete
// de 30» y «22/07» sobreviven; con 6 se empezarían a comer cantidades del pedido
// y el banco de casos dejaría de poder evaluar a P4, que es justo la etapa que
// vive de esos números. El techo de 15 es el máximo de E.164: por encima ya no es
// un teléfono, es un identificador de otra cosa, y este fichero no sabe de cuál.
const (
	minPhoneDigits = 8
	maxPhoneDigits = 15
)

// rePhoneCandidate (antes `reCandidatoTelefono`) es solo el CANDIDATO: empieza
// y acaba en dígito y admite separadores por medio. Quién es teléfono de
// verdad lo decide el conteo de dígitos, no este patrón — meterlo en la
// expresión regular obligaría a enumerar formatos y se escaparía el primero
// que no estuviera en la lista.
//
// 🔴 LA CLASE DE SEPARADORES ES EL PUNTO FRÁGIL DE TODO ESTE FICHERO, y hay
// que decirlo aquí porque no se ve: un separador que NO esté en la clase no
// produce un recorte parcial, produce un PASE ENTERO. `0412/1234567` con la
// barra fuera de la clase no casa por ningún lado —ni «0412» ni «1234567»
// llegan a 8 dígitos por separado— así que el número sale intacto Y `Remains`
// dice «cero hallazgos» sobre un teléfono completo. Ese fallo se midió el
// 2026-08-27 con `/`, `_` y el salto de línea, los tres a la vez.
//
// Por eso la clase es DELIBERADAMENTE ANCHA: barra, guion bajo, guion, punto,
// paréntesis, espacio, tabulador y los dos caracteres de fin de línea. El
// coste de meter uno de más es tapar alguna fecha (ver el falso positivo
// declarado en anonymize.go); el de dejar uno fuera es publicar un teléfono. No son
// errores del mismo tamaño y la clase se elige por el segundo.
//
// Los DÍGITOS del patrón son los de `phoneDigitClass`, no solo los ASCII.
var rePhoneCandidate = regexp.MustCompile(
	`\+?[` + phoneDigitClass + `][` + phoneDigitClass + ` \t\r\n_/().\-]*[` + phoneDigitClass + `]`)

// phoneDigitClass son los CUATRO sistemas de dígitos que cuentan para un
// teléfono, como rangos de una clase de expresión regular: ASCII, árabes-índicos
// (U+0660–0669), árabes-índicos extendidos (U+06F0–06F9) y de ancho completo
// (U+FF10–FF19). Los tres últimos no estaban en el viejo, que dejaba pasar ENTERO
// un teléfono escrito con ellos (hallazgo 1 de F7; divergencia deliberada,
// decisión de Jhoan del 2026-10-08). Se redactan con la misma marca, cuentan
// igual para los umbrales y pueden ir mezclados dentro de un mismo número.
//
// 🔴 LA LISTA ES CERRADA: el devanagari, el bengalí y los demás dígitos de
// Unicode siguen sin verse. Quien añada un rango lo añade en los DOS sitios —aquí
// y en `isPhoneDigit`— y pone su caso en anonymize_phones_test.go.
const phoneDigitClass = `0-9\x{0660}-\x{0669}\x{06F0}-\x{06F9}\x{FF10}-\x{FF19}`

// phonesIn (antes `telefonos`) decide qué candidatos son teléfonos, por CONTEO de
// dígitos. Es donde vive la decisión que separa un teléfono de una cantidad del
// pedido:
//
//   - menos de 8 dígitos: no es un teléfono;
//   - de 8 a 15: es UN teléfono, tenga los separadores que tenga por dentro
//     («0412 123 45 67», «+58 412-123.45.67»);
//   - más de 15: no cabe en un teléfono, y lo reparte `splitPhoneRun`.
func (a Anonymizer) phonesIn(text string) [][]int {
	out := make([][]int, 0, 2)
	for _, loc := range findWithBoundaries(text, rePhoneCandidate) {
		switch n := countDigits(text[loc[0]:loc[1]]); {
		case n < minPhoneDigits:
		case n <= maxPhoneDigits:
			out = append(out, loc)
		default:
			out = append(out, splitPhoneRun(text, loc)...)
		}
	}
	return out
}

// splitPhoneRun reparte en teléfonos un candidato de MÁS DE 15 DÍGITOS. No
// existía en el viejo, que dejaba pasar la racha ENTERA con el barrido diciendo
// «limpio»: dos teléfonos separados por un espacio o un salto de línea eran dos
// números en claro (hallazgo 1 de F7; divergencia deliberada, decisión de Jhoan
// del 2026-10-08).
//
// EL CRITERIO. La racha se corta por sus separadores en TROZOS —cada trozo, una
// tira de dígitos seguidos— y un teléfono es uno o varios trozos CONSECUTIVOS que
// suman de 8 a 15 dígitos. De todos los repartos posibles se elige:
//
//  1. el que deja MENOS DÍGITOS EN CLARO. Por eso no es voraz: en «0412 123 4567
//     0414 987 6543» el corte voraz se llevaría 15 dígitos y dejaría «987 6543»
//     fuera; el elegido es 11 + 11;
//  2. a igualdad, el de MENOS teléfonos;
//  3. a igualdad, el que hace el PRIMERO lo más largo posible.
//
// Un trozo que no entra en ningún teléfono SE QUEDA COMO ESTÁ. Es el caso de un
// trozo de más de 15 dígitos —un número de pedido o de cuenta escrito de
// corrido—, que además no se junta con sus vecinos: «12345678901234567890
// 04121234567» pierde el teléfono y conserva el identificador. Una racha de más
// de 15 dígitos SIN separadores es un solo trozo y pasa entera, como en el viejo.
//
// Cada tramo devuelto va del primer dígito de su primer trozo al último del
// último; los separadores entre teléfonos no se tocan. El `+` de delante solo
// entra con el primer trozo.
func splitPhoneRun(text string, loc []int) [][]int {
	pieces := digitPieces(text, loc)
	// best[i] es el mejor reparto de pieces[i:], y next[i] dónde acaba (sin
	// incluirlo) el teléfono que empieza en i; next[i] == i es «el trozo i se
	// queda fuera».
	type score struct{ digits, phones int }
	best := make([]score, len(pieces)+1)
	next := make([]int, len(pieces))
	for i := len(pieces) - 1; i >= 0; i-- {
		best[i], next[i] = best[i+1], i
		sum := 0
		for j := i; j < len(pieces); j++ {
			if sum += pieces[j].digits; sum > maxPhoneDigits {
				break
			}
			if sum < minPhoneDigits {
				continue
			}
			c := score{sum + best[j+1].digits, 1 + best[j+1].phones}
			// Con `<=` en los dos desempates gana, entre iguales, el teléfono más
			// largo, que es el último que se prueba.
			if c.digits > best[i].digits || (c.digits == best[i].digits && c.phones <= best[i].phones) {
				best[i], next[i] = c, j+1
			}
		}
	}
	out := make([][]int, 0, best[0].phones)
	for i := 0; i < len(pieces); {
		if next[i] == i {
			i++
			continue
		}
		out = append(out, []int{pieces[i].start, pieces[next[i]-1].end})
		i = next[i]
	}
	return out
}

// digitPiece es una tira de dígitos seguidos dentro de un candidato: sus índices
// de byte en el texto y cuántos dígitos tiene.
type digitPiece struct{ start, end, digits int }

// digitPieces corta el candidato por sus separadores. El primer trozo empieza
// donde el candidato, para llevarse el `+` si lo hay.
func digitPieces(text string, loc []int) []digitPiece {
	var out []digitPiece
	open := false
	for i, r := range text[loc[0]:loc[1]] {
		// Un dígito no ASCII ocupa más de un byte: el trozo acaba donde acaba la runa.
		switch start, end := loc[0]+i, loc[0]+i+utf8.RuneLen(r); {
		case !isPhoneDigit(r):
			open = false
		case open:
			out[len(out)-1].end = end
			out[len(out)-1].digits++
		default:
			open = true
			out = append(out, digitPiece{start: start, end: end, digits: 1})
		}
	}
	if len(out) > 0 {
		out[0].start = loc[0]
	}
	return out
}

// isPhoneDigit dice si la runa cuenta como dígito de un teléfono: los cuatro
// sistemas de `phoneDigitClass`, y ninguno más.
func isPhoneDigit(r rune) bool {
	return (r >= '0' && r <= '9') ||
		(r >= '\u0660' && r <= '\u0669') ||
		(r >= '\u06f0' && r <= '\u06f9') ||
		(r >= '\uff10' && r <= '\uff19')
}

// countDigits (antes `digitos`) cuenta los dígitos de teléfono de `s`.
func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if isPhoneDigit(r) {
			n++
		}
	}
	return n
}
