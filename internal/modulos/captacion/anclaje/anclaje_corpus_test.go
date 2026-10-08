package anclaje_test

// Trozo de anclaje_test.go (E-13): el CORPUS ADVERSARIO de texto (hallazgo 40 de F6).
// Los destinos esperados se fijaron ejecutando el paquete viejo
// (internal/intake/anclaje @ 8d875ab) sobre estas mismas entradas.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
)

// toRequest marca, en las tablas de este fichero, «va a la solicitud».
const toRequest = -1

// Líneas pensadas para tirar de cada borde del troceo en tokens:
//
//	0 → «pizza», «ñoquis» y «١٢٣٤» (dígitos no ASCII: son dígitos)
//	1 → solo «frío» (4 runas, 5 bytes); «té» y «x12» son cortos y se caen
//	2 → solo «menú»; «para», «todo» y «este» son palabras vacías
//	3 → nada: «ñúñ» tiene 3 runas aunque ocupe 6 bytes
var corpusLines = []anclaje.Line{
	{Idx: 0, Evidence: "dos pizzas grandes", Label: "Pizza Ñoquis ١٢٣٤"},
	{Idx: 1, Evidence: "un té helado", Label: "Té frío x12"},
	{Idx: 2, Evidence: "", Label: "Para todo este menú"},
	{Idx: 3, Evidence: "", Label: "Ñúñ"},
}

func TestDistribute_AdversarialCaptions(t *testing.T) {
	cases := []struct {
		name    string
		caption string
		want    int
	}{
		// La mención: mayúsculas, acentos y dígitos.
		{"uppercase ASCII", "la PIZZA", 0},
		{"uppercase non ASCII", "los ÑOQUIS", 0},
		{"missing tilde is another word", "los noquis", toRequest},
		{"non ASCII digits are a token", "el ١٢٣٤", 0},
		{"ASCII digits are not the Arabic-Indic ones", "el 1234", toRequest},
		{"accented token", "MENÚ del día", 2},
		{"missing accent", "el menu", toRequest},
		{"missing accent on a four rune token", "bien frio", toRequest},
		{"plural is another token", "las pizzas", toRequest},
		{"two tokens of the same line are one line", "pizza con ñoquis", 0},
		{"the same token repeated", "FRÍO, frío, Frío", 1},
		// Separadores repetidos y raros.
		{"repeated separators split tokens of two lines", "pizza@@frío", toRequest},
		{"trailing repeated separators", "pizza@@@", 0},
		{"separators inside a word break it", "piz@@za", toRequest},
		{"zero width space breaks a word", "piz\u200bza", toRequest},
		{"punctuation glued to the token", "¿¡pizza!?", 0},
		{"hyphenated tokens of two lines", "menú-pizza", toRequest},
		// Blancos Unicode.
		{"no-break space", "el\u00a0frío", 1},
		{"repeated em spaces", "frío\u2003\u2003", 1},
		{"line breaks and tabs", "el\n\tmenú\r\n", 2},
		// Tokens cortos y palabras vacías.
		{"short token", "un té", toRequest},
		{"short alphanumeric token", "x12", toRequest},
		{"three runes in six bytes is still short", "ñúñ", toRequest},
		{"stop words only", "para todo este", toRequest},
		// Cadenas vacías.
		{"empty caption", "", toRequest},
		{"blank caption", "   ", toRequest},
		{"unicode blank caption", "\u00a0\u3000", toRequest},
		// La proximidad sobre el propio mensaje, con la regla de `evidence`.
		{"evidence with other case and blanks", "DOS\u00a0PIZZAS\n\tgrandes", 0},
		{"evidence with non ASCII uppercase", "UN TÉ HELADO", 1},
		{"evidence without its accent", "un te helado", toRequest},
		{"separator inside the evidence is not a blank", "dos@@pizzas grandes", toRequest},
		{"evidence of two lines in one message", "dos pizzas  grandes y un té helado", toRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			turns := []anclaje.Turn{{Seq: 1, At: at(9, 55, 0), Text: c.caption}}
			photo := image("wapp/media/c.jpg", 1, at(9, 55, 0))
			d := anclaje.Distribute(turns, corpusLines, []anclaje.MediaRef{photo}, anclaje.Options{})
			if c.want == toRequest {
				requireRequest(t, d, "wapp/media/c.jpg")
				requireOnlyLines(t, d)
				return
			}
			requireLine(t, d, c.want, "wapp/media/c.jpg")
			requireRequest(t, d)
			requireOnlyLines(t, d, c.want)
		})
	}
}

func TestDistribute_AdversarialTextlessTurns(t *testing.T) {
	// Qué es «un turno sin texto» lo decide la normalización de `evidence`: un turno de
	// solo blancos —también los de Unicode— no gasta presupuesto; uno con un espacio de
	// ancho cero SÍ, porque U+200B no es un blanco. Presupuesto 1, y el turno del
	// adjunto entre la foto y la evidencia.
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ASCII blanks", " \t\n ", 0},
		{"unicode blanks", "\u00a0\u2003\u3000\u2028", 0},
		{"zero width space is text", "\u200b", toRequest},
		{"punctuation is text", "...", toRequest},
		{"emoji is text", "👍", toRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			turns := []anclaje.Turn{
				{Seq: 1, At: at(9, 55, 0), Text: "quiero dos pizzas grandes"},
				{Seq: 2, At: at(9, 55, 10), Text: c.text},
			}
			photo := image("wapp/media/c.jpg", 2, at(9, 55, 10))
			d := anclaje.Distribute(turns, corpusLines, []anclaje.MediaRef{photo}, anclaje.Options{MaxMessagesBack: 1})
			if c.want == toRequest {
				requireRequest(t, d, "wapp/media/c.jpg")
				requireOnlyLines(t, d)
				return
			}
			requireLine(t, d, c.want, "wapp/media/c.jpg")
			requireRequest(t, d)
		})
	}
}
