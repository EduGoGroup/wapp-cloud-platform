package evidence_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/evidence"
)

// Los valores esperados de este fichero se fijaron EJECUTANDO el paquete viejo
// (internal/evidence @ 8d875ab) sobre las mismas entradas: es la equivalencia
// viejo ↔ nuevo, escrita a mano porque el paquete nuevo no importa el viejo.

func TestNormalize(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"lowercases and collapses blanks", "  Dos\tTORTAS\n\n y   un   paquete  ", "dos tortas y un paquete"},
		{"empty string", "", ""},
		{"only blanks", "   \n\t ", ""},
		{"carriage return, vertical tab and form feed", "a\r\nb\vc\fd", "a b c d"},
		{"no-break space", "a\u00a0b", "a b"},
		{"repeated em spaces", "a\u2003\u2003b", "a b"},
		{"ideographic space", "a\u3000b", "a b"},
		{"narrow no-break space", "a\u202fb", "a b"},
		{"next line control", "a\u0085b", "a b"},
		{"line and paragraph separators", "a\u2028b\u2029c", "a b c"},
		{"zero width space is not a blank", "a\u200bb", "a\u200bb"},
		{"byte order mark is not a blank", "a\ufeffb", "a\ufeffb"},
		{"non ASCII uppercase", "CAFÉ ÑANDÚ ÜBER", "café ñandú über"},
		{"dotted capital I", "\u0130STANBUL", "istanbul"},
		{"capital sharp s", "STRA\u1e9eE", "stra\u00dfe"},
		{"greek sigma has no final form", "ΟΔΟΣ", "οδοσ"},
		{"titlecase digraph", "\u01c5", "\u01c6"},
		{"accents are kept", "café", "café"},
		{"combining accent is not composed", "cafe\u0301", "cafe\u0301"},
		{"repeated separators are kept", "a@@b", "a@@b"},
		{"non ASCII digits are kept", "١٢٣ Tortas", "١٢٣ tortas"},
		{"emoji with variation selector", "🎙️ AUDIO", "🎙️ audio"},
		{"invalid UTF-8 becomes the replacement rune", "\xff\xfeA", "\ufffd\ufffda"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := evidence.Normalize(c.input)
			if got != c.want {
				t.Fatalf("Normalize(%+q) = %+q, se esperaba %+q", c.input, got, c.want)
			}
			if again := evidence.Normalize(got); again != got {
				t.Fatalf("Normalize no es idempotente: %+q → %+q", got, again)
			}
		})
	}
}

func TestContains(t *testing.T) {
	text := evidence.Normalize(
		"Hola,\u00a0quería UNA\u2003TORTA\n\tde chocolate para el CAFÉ de la tarde a@@b ١٢ ÑANDÚ")

	cases := []struct {
		name   string
		phrase string
		want   bool
	}{
		{"literal phrase is present", "de chocolate para el café", true},
		{"case does not count", "una torta", true},
		{"a line break is a space", "quería una torta de chocolate", true},
		{"unicode blanks in the phrase are spaces", "QUERÍA\u00a0UNA\u3000TORTA", true},
		{"surrounding blanks are trimmed", " tarde ", true},
		{"non ASCII uppercase folds", "ÑANDÚ", true},
		{"punctuation is part of the phrase", "hola, quería", true},
		{"repeated separators match verbatim", "tarde a@@b ١٢", true},
		{"non ASCII digits match themselves", "١٢", true},
		{"accents DO count: rewriting is not copying", "para el cafe de la tarde", false},
		{"a combining accent is not the precomposed letter", "cafe\u0301 de", false},
		{"missing accents on non ASCII word", "nandu", false},
		{"dropped punctuation is not a copy", "hola quería", false},
		{"a single separator is not the repeated one", "a@b", false},
		{"a separator is not a blank", "a b", false},
		{"ASCII digits are not the Arabic-Indic ones", "12", false},
		{"loose words are not a phrase", "torta chocolate", false},
		{"zero width space is not a blank", "una\u200btorta", false},
		{"what is not there is not there", "y dos bandejas", false},
		{"empty phrase backs nothing", "", false},
		{"only blanks", "   \n\t ", false},
		{"only unicode blanks", "\u00a0\u2003\u3000", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := evidence.Contains(text, c.phrase); got != c.want {
				t.Fatalf("Contains(%+q) = %v, se esperaba %v", c.phrase, got, c.want)
			}
		})
	}
}

// El texto NO se normaliza dentro de Contains: es trabajo del llamante.
func TestContains_TextIsNotNormalized(t *testing.T) {
	if evidence.Contains("Una  TORTA", "una torta") {
		t.Fatal("Contains normalizó el texto: debe compararse contra el texto tal como llega")
	}
	if evidence.Contains("", "x") {
		t.Fatal("un texto vacío no contiene ninguna frase")
	}
	if evidence.Contains("", "") {
		t.Fatal("una frase vacía no es evidencia ni contra un texto vacío")
	}
}
