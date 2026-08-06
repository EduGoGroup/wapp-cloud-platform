// note.go es la PUERTA por la que entra el texto libre del cliente al carrito
// (Plan 041 · T4.1c, D-041.19 / REQ-33e): una sola función de saneo y un solo
// límite para las DOS ranuras —la indicación de línea (intake_items.customization)
// y la del pedido (intakes.customer_note)—.
//
// Vive en su propio archivo y NO dentro de screens.go porque no es una pantalla:
// es el contrato de contenido de dos columnas, y el pipeline LLM del Plan 044
// escribe esas MISMAS columnas por otra puerta. Ese pipeline debe llamar a ESTA
// función —está exportada por eso—, no copiar la regla: dos entradas, una regla.
// Si el numero o el saneo divergieran entre las dos puertas, la columna tendría
// dos contratos y ninguno sería verdad.
package cart

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxNoteRunes es el largo máximo de una indicación, en RUNAS y no en bytes
// (D-041.19): 280. Es una instrucción de producción, no un relato, y un campo
// corto es menos superficie de PII. Se mide DESPUÉS de sanear — si se midiera
// antes, un muro de saltos de línea se comería la cuota sin aportar una palabra.
//
// Dónde se corta el texto PARA TODAS LAS PUERTAS sigue siendo MD-041.3: 280 es el
// valor de arranque de la puerta del cart, no un número cerrado.
const MaxNoteRunes = 280

// NoteTooLongError lo devuelve SanitizeNote cuando el texto SANEADO pasa del
// límite. Lleva el largo real porque quien repregunta tiene que decirle al cliente
// cuánto sobra ("312 de 280"), no un "es muy largo" que no ayuda a arreglarlo.
//
// Pasarse NO trunca (REQ-33e): recortar «…y sin maní» pierde el final, y el final
// es justo donde va el alérgeno. Rechazar y repreguntar es lo único seguro.
type NoteTooLongError struct {
	Runes int // largo del texto ya saneado
	Max   int // límite aplicado (MaxNoteRunes)
}

func (e NoteTooLongError) Error() string {
	return fmt.Sprintf("cart: la indicación mide %d runas y el máximo es %d", e.Runes, e.Max)
}

// Runas de MAQUETACIÓN y runas INVISIBLES, con su escape numérico y no con el
// carácter literal: un U+2028 pegado en el fuente sería invisible para quien
// revise este archivo, que es exactamente el problema que estas tablas existen
// para resolver.
const (
	runeLineSeparator      = '\u2028' // separador de LÍNEA de Unicode
	runeParagraphSeparator = '\u2029' // separador de PÁRRAFO de Unicode
	runeZeroWidthSpace     = '\u200b' // primero del bloque zero-width / marcas de dirección
	runeRightToLeftMark    = '\u200f' // último de ese bloque
	runeLRE                = '\u202a' // primero de los controles bidi de incrustación/anulación
	runePDF                = '\u202e' // último de ese bloque
	runeLRI                = '\u2066' // primero de los controles bidi de aislamiento
	runePDI                = '\u2069' // último de ese bloque
	runeBOM                = '\ufeff' // BOM / zero-width no-break space
)

// SanitizeNote limpia y valida una indicación del cliente. Devuelve el texto
// saneado, o NoteTooLongError si pasa de MaxNoteRunes DESPUÉS de sanear.
//
// Un resultado VACÍO no es un error: equivale a "sin indicación" (REQ-33e), y
// quien llama lo trata igual que si el cliente hubiera pulsado 0.
//
// El saneo, y por qué cada paso (D-041.19):
//
//   - Saltos de línea y tabuladores (\n, \r, \t, \v, \f, U+2028, U+2029) → UN
//     espacio. No se pierde contenido, solo maquetación: el valor viaja en una
//     CELDA del CSV, en una LÍNEA del resumen al cliente y en una LÍNEA de la
//     comanda. Un valor multilínea rompe la lectura del CSV en Excel y parte la
//     comanda en dos.
//   - El resto de controles C0/C1 se ELIMINA, empezando por 0x00: Postgres lo
//     rechaza en TEXT (SQLSTATE 22021) y reventaría el INSERT con un error que
//     nadie sabría leer.
//   - Los invisibles (zero-width y bidi) se ELIMINAN: quien prepara no puede
//     leerlos y en el render de la bandeja son un vector de suplantación de texto.
//   - Espacios repetidos colapsados y recorte de los extremos.
//
// Los emojis SOBREVIVEN tal cual: son UTF-8 válido, Postgres los guarda y
// «sin gluten» con un emoji delante es la voz del cliente — wApp no transcribe lo
// que el cliente escribió. Lo que no sobrevive es lo invisible, que no es un
// emoji: es un carácter que nadie puede leer.
//
// El orden importa y NO es el literal de los cinco pasos del design: allí se
// listan "fuera los controles" ANTES de "los saltos a espacio", y aplicados así,
// en dos pasadas ciegas, «hola\nmundo» saldría «holamundo» —los saltos SON
// controles C0—. Se clasifica cada runa UNA vez, que es lo que el design quiere
// decir: los saltos se convierten, el resto de controles se cae.
//
// Efecto lateral buscado: recorrer por runas y reescribir convierte cualquier byte
// UTF-8 malformado en U+FFFD, así que de aquí nunca sale una cadena que Postgres
// vaya a rechazar por codificacion.
func SanitizeNote(in string) (string, error) {
	var b strings.Builder
	b.Grow(len(in))
	for _, r := range in {
		switch {
		case isLayoutRune(r):
			b.WriteRune(' ')
		case isControlRune(r), isInvisibleRune(r):
			// Se cae: ilegible para quien prepara, o veneno para el INSERT.
		default:
			b.WriteRune(r)
		}
	}
	// strings.Fields + Join hace en un paso lo que el design pide en dos: colapsar
	// los espacios repetidos y recortar los extremos.
	out := strings.Join(strings.Fields(b.String()), " ")
	if n := utf8.RuneCountInString(out); n > MaxNoteRunes {
		return "", NoteTooLongError{Runes: n, Max: MaxNoteRunes}
	}
	return out, nil
}

// isLayoutRune son las runas de MAQUETACIÓN: separan contenido, así que se
// convierten en un espacio en vez de desaparecer.
func isLayoutRune(r rune) bool {
	switch r {
	case '\n', '\r', '\t', '\v', '\f', runeLineSeparator, runeParagraphSeparator:
		return true
	}
	return false
}

// isControlRune son los controles C0 (0x00-0x1F) y C1 (0x7F-0x9F). Los de
// maquetación ya se filtraron antes: aquí solo quedan los que no significan nada
// en una instrucción de cocina.
func isControlRune(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

// isInvisibleRune son los caracteres que ocupan sitio en la cadena pero no en la
// pantalla: zero-width y marcas de dirección (U+200B-U+200F), controles bidi de
// incrustación/anulación (U+202A-U+202E) y de aislamiento (U+2066-U+2069), más el
// BOM (U+FEFF), que es el que más veces llega pegado al principio de un texto
// copiado de otra aplicación.
func isInvisibleRune(r rune) bool {
	switch {
	case r >= runeZeroWidthSpace && r <= runeRightToLeftMark:
		return true
	case r >= runeLRE && r <= runePDF:
		return true
	case r >= runeLRI && r <= runePDI:
		return true
	case r == runeBOM:
		return true
	}
	return false
}
