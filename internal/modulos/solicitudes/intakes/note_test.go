package intakes

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/flujos/modules/cart/note.go @ 3a21138): el candado de fronteras
// impide importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con
// lo que el viejo devolvió para este mismo corpus. Las runas invisibles van con su
// escape numérico, nunca pegadas en el fuente.

// TestMaxNoteRunes_Is280: el límite de arranque de D-041.19.
func TestMaxNoteRunes_Is280(t *testing.T) {
	t.Parallel()
	if MaxNoteRunes != 280 {
		t.Errorf("MaxNoteRunes = %d, quería 280", MaxNoteRunes)
	}
}

// TestNoteTooLongError_TextKeepsTheCartPrefix: el texto es observable y se conserva byte a byte
// con su prefijo "cart:" aunque el tipo viva ahora en intakes (D-F6-4). Es un error por valor.
func TestNoteTooLongError_TextKeepsTheCartPrefix(t *testing.T) {
	t.Parallel()
	var err error = NoteTooLongError{Runes: 312, Max: 280}
	const want = "cart: la indicación mide 312 runas y el máximo es 280"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, quería %q", got, want)
	}
	var tooLong NoteTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("errors.As no recoge un NoteTooLongError devuelto por valor: %v", err)
	}
	if tooLong.Runes != 312 || tooLong.Max != 280 {
		t.Errorf("el error lleva (%d, %d), quería (312, 280)", tooLong.Runes, tooLong.Max)
	}
}

// TestSanitizeNote_Sanitizes: una fila por promesa del saneo (R-08) y por frontera de cada bloque
// de runas. Ninguna se pasa del límite: todas devuelven el texto saneado y nil.
func TestSanitizeNote_Sanitizes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text is untouched", in: "sin cebolla", want: "sin cebolla"},
		{name: "accents and enye are kept", in: "sin maní, ñoquis", want: "sin maní, ñoquis"},

		// Maquetación → un espacio.
		{name: "newline becomes a space", in: "sin cebolla\ny sin sal", want: "sin cebolla y sin sal"},
		{name: "crlf becomes one space", in: "sin cebolla\r\ny sin sal", want: "sin cebolla y sin sal"},
		{name: "tab becomes a space", in: "sin\tcebolla", want: "sin cebolla"},
		{name: "vertical tab becomes a space", in: "sin\vcebolla", want: "sin cebolla"},
		{name: "form feed becomes a space", in: "sin\fcebolla", want: "sin cebolla"},
		{name: "line separator becomes a space", in: "sin\u2028cebolla", want: "sin cebolla"},
		{name: "paragraph separator becomes a space", in: "sin\u2029cebolla", want: "sin cebolla"},

		// Controles C0/C1 → fuera, sin dejar espacio.
		{name: "nul byte is dropped", in: "sin\x00cebolla", want: "sincebolla"},
		{name: "c0 controls are dropped", in: "sin\x01\x02\x1b\x1fcebolla", want: "sincebolla"},
		{name: "del is dropped", in: "sin\x7fcebolla", want: "sincebolla"},
		{name: "last rune before del is kept", in: "sin~cebolla", want: "sin~cebolla"},
		{name: "c1 controls are dropped", in: "sin\u0080\u0085\u009fcebolla", want: "sincebolla"},

		// Invisibles → fuera; cada frontera de bloque por separado.
		{name: "zero width space is dropped", in: "ce\u200bbolla", want: "cebolla"},
		{name: "zero width non joiner is dropped", in: "ce\u200cbolla", want: "cebolla"},
		{name: "zero width joiner is dropped", in: "ce\u200dbolla", want: "cebolla"},
		{name: "left to right mark is dropped", in: "ce\u200ebolla", want: "cebolla"},
		{name: "right to left mark is dropped", in: "ce\u200fbolla", want: "cebolla"},
		{name: "hyphen after the zero width block is kept", in: "ce\u2010bolla", want: "ce\u2010bolla"},
		{name: "first bidi embedding is dropped", in: "ce\u202abolla", want: "cebolla"},
		{name: "bidi pop is dropped", in: "ce\u202cbolla", want: "cebolla"},
		{name: "last bidi override is dropped", in: "ce\u202ebolla", want: "cebolla"},
		{name: "rune before the bidi isolates is kept", in: "ce\u2065bolla", want: "ce\u2065bolla"},
		{name: "first bidi isolate is dropped", in: "ce\u2066bolla", want: "cebolla"},
		{name: "last bidi isolate is dropped", in: "ce\u2069bolla", want: "cebolla"},
		{name: "rune after the bidi isolates is kept", in: "ce\u206abolla", want: "ce\u206abolla"},
		{name: "leading bom is dropped", in: "\ufeffsin cebolla", want: "sin cebolla"},
		{name: "bom inside a word is dropped", in: "ce\ufeffbolla", want: "cebolla"},
		{name: "rune before the bom is kept", in: "ce\ufefebolla", want: "ce\ufefebolla"},
		{name: "bidi wrapping is dropped", in: "\u202esin cebolla\u202c", want: "sin cebolla"},
		{
			name: "invisibles interleaved inside the words",
			in:   "s\u200bi\u200en\u202e \u2066c\u2069e\ufeffbolla",
			want: "sin cebolla",
		},
		// No están en la lista de invisibles aunque no se vean: el viejo los deja pasar.
		{name: "word joiner is kept", in: "ce\u2060bolla", want: "ce\u2060bolla"},
		{name: "soft hyphen is kept", in: "ce\u00adbolla", want: "ce\u00adbolla"},

		// Colapso y recorte; «espacio» es el espacio en blanco de Unicode.
		{name: "spaces are collapsed and trimmed", in: "   sin    cebolla   ", want: "sin cebolla"},
		{name: "no-break space becomes a plain space", in: "sin\u00a0cebolla", want: "sin cebolla"},
		{name: "no-break spaces are collapsed and trimmed", in: "sin\u00a0\u00a0cebolla\u00a0", want: "sin cebolla"},
		{name: "ideographic space becomes a plain space", in: "\u3000sin\u3000cebolla", want: "sin cebolla"},
		{name: "hair space becomes a plain space", in: "ce\u200abolla", want: "ce bolla"},
		{name: "narrow no-break space becomes a plain space", in: "ce\u202fbolla", want: "ce bolla"},

		// Emojis y demás runas visibles.
		{name: "emoji survives", in: "\U0001F382 sin gluten", want: "\U0001F382 sin gluten"},
		{
			name: "zwj emoji is split into its parts",
			in:   "\U0001F468\u200d\U0001F469\u200d\U0001F467 mesa",
			want: "\U0001F468\U0001F469\U0001F467 mesa",
		},
		{name: "variation selector is kept", in: "\u2764\ufe0f gracias", want: "\u2764\ufe0f gracias"},
		{name: "skin tone modifier is kept", in: "\U0001F44D\U0001F3FD ok", want: "\U0001F44D\U0001F3FD ok"},
		{name: "non ascii digits are kept", in: "\u0663 unidades \uff12", want: "\u0663 unidades \uff12"},

		// UTF-8 malformado → U+FFFD, uno por byte malo.
		{name: "malformed byte becomes the replacement rune", in: "sin\xffcebolla", want: "sin\ufffdcebolla"},
		{name: "truncated sequence gives one replacement per byte", in: "sin\xe2\x82cebolla", want: "sin\ufffd\ufffdcebolla"},
		{name: "lone continuation byte becomes the replacement rune", in: "\x80", want: "\ufffd"},

		// Vacío no es error.
		{name: "empty stays empty", in: "", want: ""},
		{name: "only spaces is empty", in: "   \n\t  ", want: ""},
		{name: "only invisibles is empty", in: "\u200b\u200b\ufeff", want: ""},
		{name: "only controls is empty", in: "\x00\x01\u0085", want: ""},
		{name: "only unicode spaces is empty", in: "\u00a0\u3000\u2028", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := SanitizeNote(c.in)
			if err != nil {
				t.Fatalf("SanitizeNote(%+q) devolvió el error %v, quería nil", c.in, err)
			}
			if got != c.want {
				t.Errorf("SanitizeNote(%+q) = %+q, quería %+q", c.in, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("SanitizeNote(%+q) = %+q, que no es UTF-8 válido", c.in, got)
			}
		})
	}
}

// TestSanitizeNote_LengthLimit: el largo se mide en RUNAS y DESPUÉS de sanear; 280 pasan, 281 no,
// y al pasarse no se trunca: cadena vacía y un NoteTooLongError con el largo saneado.
func TestSanitizeNote_LengthLimit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		// want es la salida cuando cabe; con wantRunes > 0 se espera el rechazo y ese largo.
		want      string
		wantRunes int
	}{
		{name: "exactly 280 runes pass", in: strings.Repeat("a", 280), want: strings.Repeat("a", 280)},
		{name: "281 runes are rejected", in: strings.Repeat("a", 281), wantRunes: 281},
		// 560 bytes: si se midiera en bytes, no cabría.
		{name: "280 two-byte runes pass", in: strings.Repeat("á", 280), want: strings.Repeat("á", 280)},
		{name: "281 two-byte runes are rejected", in: strings.Repeat("á", 281), wantRunes: 281},
		{name: "280 emojis pass", in: strings.Repeat("\U0001F382", 280), want: strings.Repeat("\U0001F382", 280)},
		{name: "281 emojis are rejected", in: strings.Repeat("\U0001F382", 281), wantRunes: 281},
		{
			name: "281 runes that sanitize down to 280 pass",
			in:   strings.Repeat("a", 140) + "\u200b" + strings.Repeat("a", 140),
			want: strings.Repeat("a", 280),
		},
		{
			name: "collapsed spaces do not count",
			in:   strings.Repeat("a", 140) + "   " + strings.Repeat("a", 139),
			want: strings.Repeat("a", 140) + " " + strings.Repeat("a", 139),
		},
		{
			name: "a wall of layout runes does not eat the quota",
			in:   strings.Repeat("\n", 300) + "sin sal" + strings.Repeat("\t", 300),
			want: "sin sal",
		},
		{
			name:      "the error carries the sanitized length, not the raw one",
			in:        strings.Repeat("a", 285) + strings.Repeat("\u200b", 10),
			wantRunes: 285,
		},
		{
			name:      "single spaces between words do count",
			in:        strings.Repeat("a   ", 140) + "a",
			wantRunes: 281,
		},
		// Cada byte malo es una runa U+FFFD: 280 caben (840 bytes de salida), 281 no.
		{name: "280 malformed bytes pass as replacement runes", in: strings.Repeat("\xff", 280), want: strings.Repeat("\ufffd", 280)},
		{name: "281 malformed bytes are rejected", in: strings.Repeat("\xff", 281), wantRunes: 281},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := SanitizeNote(c.in)
			if c.wantRunes == 0 {
				if err != nil {
					t.Fatalf("SanitizeNote devolvió el error %v, quería nil: cabe en el límite", err)
				}
				if got != c.want {
					t.Errorf("SanitizeNote = %+q (%d runas), quería %+q", got, utf8.RuneCountInString(got), c.want)
				}
				return
			}
			var tooLong NoteTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("SanitizeNote devolvió (%+q, %v), quería un NoteTooLongError", got, err)
			}
			// Se RECHAZA, no se trunca: truncando se perdería el final, que es donde va el alérgeno.
			if got != "" {
				t.Errorf("SanitizeNote no debe truncar: devolvió %d runas junto al error", utf8.RuneCountInString(got))
			}
			if tooLong.Runes != c.wantRunes || tooLong.Max != MaxNoteRunes {
				t.Errorf("el error lleva (%d, %d), quería (%d, %d)", tooLong.Runes, tooLong.Max, c.wantRunes, MaxNoteRunes)
			}
		})
	}
}
