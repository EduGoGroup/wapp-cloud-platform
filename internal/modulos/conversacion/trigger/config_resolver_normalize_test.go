package trigger_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// El CORPUS DE NORMALIZACIÓN de ConfigResolver: qué texto casa qué keyword. Es la tabla de
// equivalencia viejo ↔ nuevo: la columna want NO se dedujo del contrato, es lo que contesta el
// ConfigResolver de internal/flujos/trigger @ c0c0c03 a cada fila (se pasó el corpus entero por
// él, con un programa suelto que no vive en el repo). Si una fila se pone en rojo, el resolver
// nuevo dejó de normalizar como el que corre en UAT.
//
// Lleva casos ADVERSARIOS a propósito (hallazgo 40 de F1): separadores repetidos, dígitos no
// ASCII, espacios Unicode, la misma letra compuesta y descompuesta (NFC/NFD), mayúsculas turcas
// y bytes que no son UTF-8. Varias filas fijan conductas que nadie pidió y que NO se arreglan
// aquí: «año» casa «ano», dos bytes inválidos distintos casan entre sí, un emoji casa con y sin
// su selector de variación.

type normalizationCase struct {
	name    string
	keyword string
	match   trigger.MatchType
	text    string
	want    bool
}

const (
	exact    = trigger.MatchExact
	contains = trigger.MatchContains
)

var normalizationCorpus = []normalizationCase{
	// Mayúsculas, acentos y forma de composición.
	{"upper case and accent", "men\u00fa", exact, "MENU", true},
	{"composed (NFC) keyword, decomposed (NFD) text", "men\u00fa", exact, "menu\u0301", true},
	{"decomposed (NFD) keyword, composed (NFC) text", "MENU\u0301", exact, "men\u00fa", true},
	{"two stacked combining marks", "e", exact, "e\u0301\u0302", true},
	{"n with tilde loses the tilde", "a\u00f1o", exact, "ano", true},
	{"cedilla is a mark", "fa\u00e7ade", exact, "FACADE", true},
	{"umlaut is a mark, not a digraph", "\u00fcber", exact, "uber", true},
	{"umlaut is not expanded to ue", "\u00fcber", exact, "ueber", false},
	{"stroke letters do not decompose", "sm\u00f8rrebr\u00f8d", exact, "smorrebrod", false},
	{"arabic harakat are marks", "\u0645\u064e\u0631\u0652\u062d\u064e\u0628\u064b\u0627", exact, "\u0645\u0631\u062d\u0628\u0627", true},
	{"hangul syllable and its jamo", "\ud55c", exact, "\u1112\u1161\u11ab", true},
	{"greek question mark is a canonical semicolon", ";", exact, "\u037e", true},
	{"accented keyword inside plain text", "\u00e9", contains, "un cafe", true},

	// Mayúsculas turcas y otros plegados de caja.
	{"turkish dotted capital I lowers to i", "istanbul", exact, "\u0130STANBUL", true},
	{"capital I with combining dot", "i", exact, "I\u0307", true},
	{"turkish dotless i is not i", "\u0131sparta", exact, "ISPARTA", false},
	{"dotless i keyword against dotless i text", "D\u0131\u015f", exact, "d\u0131s", true},
	{"kelvin sign lowers to k", "k", exact, "\u212a", true},
	{"angstrom sign lowers to a", "a", exact, "\u212b", true},
	{"ohm sign lowers to omega", "\u03c9", exact, "\u2126", true},
	{"capital sharp s lowers to sharp s", "stra\u00dfe", exact, "STRA\u1e9eE", true},
	{"sharp s is not ss", "stra\u00dfe", exact, "STRASSE", false},
	{"greek final sigma is not folded", "\u03bf\u03b4\u03cc\u03c2", exact, "\u039f\u0394\u038c\u03a3", false},
	{"titlecase digraph lowers", "\u01c6", exact, "\u01c5", true},
	{"cyrillic homoglyph is another letter", "a", exact, "\u0430", false},

	// Compatibilidad: no hay NFKD.
	{"ligature is not split", "fin", exact, "\ufb01n", false},
	{"fullwidth letters are not narrowed", "menu", exact, "\uff4d\uff45\uff4e\uff55", false},
	{"fullwidth letters still fold case", "\uff4d\uff45\uff4e\uff55", exact, "\uff2d\uff25\uff2e\uff35", true},

	// Dígitos no ASCII.
	{"arabic-indic digits", "123", exact, "\u0661\u0662\u0663", false},
	{"fullwidth digits", "123", exact, "\uff11\uff12\uff13", false},
	{"devanagari digits", "123", exact, "\u0967\u0968\u0969", false},
	{"superscript digit", "m2", exact, "m\u00b2", false},
	{"arabic-indic digits against themselves", "\u0661\u0662\u0663", contains, "pedido \u0661\u0662\u0663", true},
	{"thousands separator is kept", "1000", exact, "1,000", false},

	// Espacios.
	{"spaces, tabs and newlines collapse", "ver menu", exact, "  ver \t\r\n menu  ", true},
	{"double space inside the keyword", "ver  menu", contains, "quiero ver\tmenu ya", true},
	{"no-break space", "ver menu", exact, "ver\u00a0menu", true},
	{"thin space", "ver menu", exact, "ver\u2009menu", true},
	{"narrow no-break space", "1 000", exact, "1\u202f000", true},
	{"ideographic space", "ver menu", exact, "ver\u3000menu", true},
	{"em spaces around", "pedido", exact, "\u2003pedido\u2003", true},
	{"next line control", "ver menu", exact, "ver\u0085menu", true},
	{"zero width space is not a space", "ver menu", exact, "ver\u200bmenu", false},
	{"zero width space is not removed either", "vermenu", exact, "ver\u200bmenu", false},
	{"word joiner is not a space", "ver menu", exact, "ver\u2060menu", false},
	{"byte order mark is kept", "pedido", exact, "\ufeffpedido", false},
	{"soft hyphen is kept", "pedido", exact, "pe\u00addido", false},
	{"a space is not optional", "vermenu", exact, "ver menu", false},

	// Separadores y signos: no se tocan.
	{"repeated separator is not collapsed", "a@b", exact, "a@@b", false},
	{"repeated separator, contains", "a@b", contains, "x a@@b y", false},
	{"repeated separator as the keyword", "@@", contains, "a@@b", true},
	{"repeated hyphen", "a-b", exact, "a--b", false},
	{"trailing punctuation, exact", "hola", exact, "hola!", false},
	{"surrounding punctuation, contains", "hola", contains, "\u00a1hola!!", true},
	{"nul byte is kept", "ab", exact, "a\x00b", false},

	// Emoji.
	{"variation selector is a mark", "\u2764", exact, "\u2764\ufe0f", true},
	{"skin tone modifier is not a mark", "\U0001f44d", exact, "\U0001f44d\U0001f3fd", false},
	{"skin tone modifier, contains", "\U0001f44d", contains, "ok \U0001f44d\U0001f3fd", true},

	// Vacíos: una keyword que normaliza a nada no casa nunca.
	{"empty keyword, empty text", "", exact, "", false},
	{"empty keyword, contains", "", contains, "hola", false},
	{"keyword of spaces", " \t\u00a0", contains, " ", false},
	{"keyword of a lone combining mark", "\u0301", contains, "hola", false},
	{"empty text", "x", contains, "", false},

	// Bytes que no son UTF-8.
	{"same invalid byte on both sides", "caf\xff", exact, "CAF\xff", true},
	{"two different invalid bytes match each other", "caf\xff", exact, "caf\xfe", true},
	{"invalid byte matches the replacement character", "caf\xff", exact, "caf\ufffd", true},
	{"invalid byte is not dropped", "caf", exact, "caf\xff", false},
}

// TestConfigResolver_NormalizationCorpus pasa cada fila por los tres caminos que comparan texto
// con keyword —Resolve (keyword), IsEscape (escape) y ResolveLive (event_start)—: la
// normalización es UNA y los tres tienen que contestar lo mismo.
func TestConfigResolver_NormalizationCorpus(t *testing.T) {
	ctx := context.Background()
	for _, c := range normalizationCorpus {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(
				rule("1", trigger.KindKeyword, c.keyword, c.match, "flow"),
				rule("2", trigger.KindEscape, c.keyword, c.match, ""),
			)
			dec, err := r.Resolve(ctx, tenant, "", trigger.Signal{Text: c.text})
			if err != nil {
				t.Fatalf("Resolve: error inesperado %v", err)
			}
			if got := dec.Action == trigger.Start; got != c.want {
				t.Errorf("Resolve: keyword %q (%s) contra %q: casa = %v, quería %v", c.keyword, c.match, c.text, got, c.want)
			}
			escaped, _, err := r.IsEscape(ctx, tenant, "", c.text)
			if err != nil {
				t.Fatalf("IsEscape: error inesperado %v", err)
			}
			if escaped != c.want {
				t.Errorf("IsEscape: keyword %q (%s) contra %q: casa = %v, quería %v", c.keyword, c.match, c.text, escaped, c.want)
			}

			live, _ := newResolver(with(rule("1", trigger.KindEventStart, c.keyword, c.match, ""), eventKind(trigger.EventKindCart)))
			dec, err = live.ResolveLive(ctx, tenant, "", c.text)
			if err != nil {
				t.Fatalf("ResolveLive: error inesperado %v", err)
			}
			if got := dec.Action == trigger.StartEvent; got != c.want {
				t.Errorf("ResolveLive: keyword %q (%s) contra %q: casa = %v, quería %v", c.keyword, c.match, c.text, got, c.want)
			}
		})
	}
}

// TestConfigResolver_IntentName_SameNormalization: el nombre de una intención se compara con la
// misma normalización que el texto. Van las filas de coincidencia exacta del corpus (el nombre
// nunca se compara por contains).
func TestConfigResolver_IntentName_SameNormalization(t *testing.T) {
	for _, c := range normalizationCorpus {
		if c.match != exact {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			r, _ := newResolver(rule("1", trigger.KindLLM, c.keyword, "", "flow"))
			dec := resolveSignal(t, r, "", trigger.Signal{Intent: &trigger.IntentSignal{Name: c.text}})
			if got := dec.Action == trigger.Start; got != c.want {
				t.Errorf("regla llm %q contra la intención %q: casa = %v, quería %v", c.keyword, c.text, got, c.want)
			}
		})
	}
}
