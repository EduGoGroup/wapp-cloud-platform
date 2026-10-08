//go:build pendiente

package intakes

import (
	"encoding/json"
	"errors"
	"maps"
	"math"
	"reflect"
	"testing"
	"time"
)

// Las salidas esperadas de este fichero y de literal_merge_test.go son LITERALES
// calculados con el fichero viejo (internal/intakes/literal.go @ 64c181a): el
// candado de fronteras impide importarlo desde aquí, así que la equivalencia
// viejo ↔ nuevo se fija con lo que el viejo devolvió para este mismo corpus. Las
// runas invisibles van con su escape numérico, nunca pegadas en el fuente.

// Runas del corpus adversario, como texto ya decodificado.
const (
	literalTestLineSeparator    = "\u2028"
	literalTestNoBreakSpace     = "\u00a0"
	literalTestIdeographicSpace = "\u3000"
	literalTestArabicDigits     = "\u0661\u0662\u0663"
)

// literalTestAmberPayload imita el contrato §7.4 tal como lo escribe la etapa draft:
// literal del cliente arriba, líneas con su evidencia y una personalización que NO
// se cifra.
const literalTestAmberPayload = `{
	"version": 1,
	"source_text": "### MENSAJES ###\ncliente: Hola Herminia, 2 tortas\n### FIN ###",
	"message_ts": "2026-07-13T09:55:00Z",
	"analysis": {"provider": "local", "reanalyzed_from": null},
	"lines": [
		{"kind": "matched", "sku": "torta-choc", "label": "Torta de chocolate", "qty": 1,
		 "unit_price": 25000, "customization": "sin sal",
		 "evidence": "una torta de bizcocho húmedo"},
		{"kind": "unmatched", "label": "tequeños congelados", "qty": 1, "unit_price": null,
		 "evidence": "un paquete de tequeños de 30"},
		{"kind": "shipping", "label": "Envío", "qty": 1, "unit_price": null}
	],
	"suggested_questions": [],
	"warnings": [{"item_pos": 1, "reason": "sin_precio"}]
}`

// literalTestAmberClean es el payload de arriba SIN su nivel 2: lo que se persiste
// en claro. La personalización, el sku, el label de la línea sin sku, el precio y el
// aviso siguen ahí.
const literalTestAmberClean = `{"analysis":{"provider":"local","reanalyzed_from":null},"lines":[{"customization":"sin sal","kind":"matched","label":"Torta de chocolate","qty":1,"sku":"torta-choc","unit_price":25000},{"kind":"unmatched","label":"tequeños congelados","qty":1,"unit_price":null},{"kind":"shipping","label":"Envío","qty":1,"unit_price":null}],"message_ts":"2026-07-13T09:55:00Z","suggested_questions":[],"version":1,"warnings":[{"item_pos":1,"reason":"sin_precio"}]}`

// literalTestAmberSource es el texto del cliente de literalTestAmberPayload.
const literalTestAmberSource = "### MENSAJES ###\ncliente: Hola Herminia, 2 tortas\n### FIN ###"

// literalTestCartPayload es el payload de una revisión del carrito: sin literal.
const literalTestCartPayload = `{"version":1,"total":5000,"items":[{"sku":"emp-pino","label":"Empanada de pino","qty":2,"unit_price":2500}]}`

// sameEvidence compara dos mapas de evidencias; nil y vacío son lo mismo.
func sameEvidence(got, want map[string]string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return maps.Equal(got, want)
}

// TestDefaultLiteralTTL_Is365Days: 12 meses como duración, espejo de la migración.
func TestDefaultLiteralTTL_Is365Days(t *testing.T) {
	t.Parallel()
	if DefaultLiteralTTL != 8760*time.Hour {
		t.Errorf("DefaultLiteralTTL = %v, quería 8760h", DefaultLiteralTTL)
	}
	if secs := int64(DefaultLiteralTTL / time.Second); secs != 31536000 {
		t.Errorf("DefaultLiteralTTL = %d s, quería 31536000", secs)
	}
}

// TestLiteralKeys_AreTheContractKeys: las tres claves del contrato §7.4.
func TestLiteralKeys_AreTheContractKeys(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, got, want string }{
		{"PayloadKeySourceText", PayloadKeySourceText, "source_text"},
		{"PayloadKeyLines", PayloadKeyLines, "lines"},
		{"LineKeyEvidence", LineKeyEvidence, "evidence"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}
}

// TestLiteralRevision_JSONIsTheEnvelopePlaintext: la forma del texto claro del sobre.
func TestLiteralRevision_JSONIsTheEnvelopePlaintext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		lit  LiteralRevision
		want string
	}{
		{"full literal", LiteralRevision{SourceText: "x", Evidence: map[string]string{"0": "y", "10": "z"}}, `{"source_text":"x","evidence":{"0":"y","10":"z"}}`},
		{"zero literal", LiteralRevision{}, `{}`},
		{"empty evidence map is omitted", LiteralRevision{Evidence: map[string]string{}}, `{}`},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.lit)
		if err != nil {
			t.Fatalf("%s: serializar: %v", c.name, err)
		}
		if string(raw) != c.want {
			t.Errorf("%s: got %s, quería %s", c.name, raw, c.want)
		}
	}
}

// TestLiteralRevision_Empty: vacío es «sin texto y sin ninguna evidencia»; no se
// recorta nada y una evidencia de valor vacío cuenta como contenido.
func TestLiteralRevision_Empty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		lit  LiteralRevision
		want bool
	}{
		{"zero value", LiteralRevision{}, true},
		{"empty evidence map", LiteralRevision{Evidence: map[string]string{}}, true},
		{"source text", LiteralRevision{SourceText: "x"}, false},
		{"whitespace source text is content", LiteralRevision{SourceText: " "}, false},
		{"evidence entry with empty value is content", LiteralRevision{Evidence: map[string]string{"0": ""}}, false},
		{"evidence only", LiteralRevision{Evidence: map[string]string{"0": "e"}}, false},
		{"both", LiteralRevision{SourceText: "x", Evidence: map[string]string{"0": "e"}}, false},
	}
	for _, c := range cases {
		if got := c.lit.Empty(); got != c.want {
			t.Errorf("%s: Empty() = %v, quería %v", c.name, got, c.want)
		}
	}
}

// TestLiteralEnvelope_CompleteAndEmpty: completo son las TRES piezas; vacío, las
// tres en cero; a medias no es ni lo uno ni lo otro.
func TestLiteralEnvelope_CompleteAndEmpty(t *testing.T) {
	t.Parallel()
	one := []byte{1}
	cases := []struct {
		name     string
		envelope LiteralEnvelope
		complete bool
		empty    bool
	}{
		{"zero value", LiteralEnvelope{}, false, true},
		{"zero length slices", LiteralEnvelope{Enc: []byte{}, DEK: []byte{}}, false, true},
		{"only enc", LiteralEnvelope{Enc: one}, false, false},
		{"only dek", LiteralEnvelope{DEK: one}, false, false},
		{"only kek id", LiteralEnvelope{KEKID: "k"}, false, false},
		{"enc and dek", LiteralEnvelope{Enc: one, DEK: one}, false, false},
		{"enc and kek id", LiteralEnvelope{Enc: one, KEKID: "k"}, false, false},
		{"dek and kek id", LiteralEnvelope{DEK: one, KEKID: "k"}, false, false},
		{"all three", LiteralEnvelope{Enc: one, DEK: one, KEKID: "k"}, true, false},
		{"zero bytes and a blank kek id still count", LiteralEnvelope{Enc: []byte{0}, DEK: []byte{0}, KEKID: " "}, true, false},
	}
	for _, c := range cases {
		if got := c.envelope.Complete(); got != c.complete {
			t.Errorf("%s: Complete() = %v, quería %v", c.name, got, c.complete)
		}
		if got := c.envelope.Empty(); got != c.empty {
			t.Errorf("%s: Empty() = %v, quería %v", c.name, got, c.empty)
		}
	}
}

// TestLiteralExpired: el 0 (y el negativo) es RETENCIÓN INDEFINIDA; con TTL, vence
// en el límite exacto (>=, no >).
func TestLiteralExpired(t *testing.T) {
	t.Parallel()
	const century = 100 * 365 * 24 * time.Hour
	cases := []struct {
		name string
		age  time.Duration
		ttl  time.Duration
		want bool
	}{
		{"zero ttl never expires", century, 0, false},
		{"zero age and zero ttl", 0, 0, false},
		{"negative ttl never expires", time.Hour, -time.Hour, false},
		{"negative age and negative ttl", -time.Hour, -time.Hour, false},
		{"age exactly the ttl is expired", DefaultLiteralTTL, DefaultLiteralTTL, true},
		{"one nanosecond younger is not", DefaultLiteralTTL - 1, DefaultLiteralTTL, false},
		{"one nanosecond older is", DefaultLiteralTTL + 1, DefaultLiteralTTL, true},
		{"zero age with the smallest ttl", 0, 1, false},
		{"smallest ttl reached", 1, 1, true},
		{"negative age is not expired", -1, 1, false},
		{"fresh literal", 0, time.Hour, false},
		{"maximum duration on both sides", math.MaxInt64, math.MaxInt64, true},
		{"minimum age", math.MinInt64, 1, false},
		{"maximum age", math.MaxInt64, 1, true},
	}
	for _, c := range cases {
		if got := LiteralExpired(c.age, c.ttl); got != c.want {
			t.Errorf("%s: LiteralExpired(%d, %d) = %v, quería %v", c.name, c.age, c.ttl, got, c.want)
		}
	}
}

// TestSplitLiteral_ReturnsThePayloadUntouched: lo que no es un objeto, o no trae
// ninguna de las dos claves donde tocan, vuelve con los MISMOS bytes y sin literal.
// Es el caso mayoritario de la tabla (las revisiones del carrito).
func TestSplitLiteral_ReturnsThePayloadUntouched(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in string }{
		{"cart payload", literalTestCartPayload},
		{"empty payload", ``},
		{"json null", `null`},
		{"array even with a source_text inside", `[{"source_text":"x"}]`},
		{"string scalar", `"source_text"`},
		{"number scalar", `123`},
		{"broken json", `{"source_text":"x"`},
		{"empty object", `{}`},
		{"spaced object keeps its spaces and number bytes", ` { "version" : 1 , "total" : 2500.00 } `},
		{"uppercase keys are not the literal", `{"Source_Text":"hola","SOURCE_TEXT":"x","lines":[{"Evidence":"e","EVIDENCE":"f"}]}`},
		{"lines is an object", `{"lines":{"evidence":"e"}}`},
		{"lines is an empty list", `{"lines":[]}`},
		{"lines without evidence", `{"lines":[{"sku":"a"},{"sku":"b"}]}`},
		{"keys nested deeper are not the literal", `{"analysis":{"source_text":"dentro"},"lines":[{"sub":{"evidence":"dentro"}}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clean, lit, err := SplitLiteral(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("SplitLiteral: %v", err)
			}
			if string(clean) != c.in {
				t.Errorf("el payload sin literal se reescribió:\n got %q\nquería %q", clean, c.in)
			}
			if !lit.Empty() || lit.SourceText != "" || len(lit.Evidence) != 0 {
				t.Errorf("se extrajo un literal que no existe: %+v", lit)
			}
		})
	}

	clean, lit, err := SplitLiteral(nil)
	if err != nil || clean != nil || !lit.Empty() {
		t.Errorf("payload nil: got (%q, %+v, %v), quería (nil, vacío, nil)", clean, lit, err)
	}
}

// TestSplitLiteral_ExtractsLevelTwo: qué sale y qué se queda, byte a byte. El
// payload limpio sale reserializado (claves en orden alfabético, sin espacios) y
// los números conservan sus bytes.
func TestSplitLiteral_ExtractsLevelTwo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		clean    string
		source   string
		evidence map[string]string
	}{
		{
			name: "amber payload", in: literalTestAmberPayload, clean: literalTestAmberClean,
			source:   literalTestAmberSource,
			evidence: map[string]string{"0": "una torta de bizcocho húmedo", "1": "un paquete de tequeños de 30"},
		},
		{
			name:   "source only, big integer survives",
			in:     `{"version":1,"source_text":"hola","total":9007199254740993,"lines":[]}`,
			clean:  `{"lines":[],"total":9007199254740993,"version":1}`,
			source: "hola",
		},
		{name: "empty source_text is removed and not kept", in: `{"b":1,"source_text":"","a":2}`, clean: `{"a":2,"b":1}`},
		{name: "null source_text is removed and not kept", in: `{"source_text":null,"a":1}`, clean: `{"a":1}`},
		{name: "duplicated source_text: last wins", in: `{"source_text":"uno","source_text":"dos"}`, clean: `{}`, source: "dos"},
		{name: "null lines are left alone", in: `{"source_text":"s","lines":null}`, clean: `{"lines":null}`, source: "s"},
		{name: "object lines are left alone", in: `{"source_text":"s","lines":{"evidence":"e"}}`, clean: `{"lines":{"evidence":"e"}}`, source: "s"},
		{name: "string lines are left alone", in: `{"source_text":"s","lines":"evidence"}`, clean: `{"lines":"evidence"}`, source: "s"},
		{
			name:     "non object elements keep their position in the index",
			in:       `{"lines":[1,null,"evidence",[{"evidence":"nested"}],{"sku":"a","evidence":"real"}]}`,
			clean:    `{"lines":[1,null,"evidence",[{"evidence":"nested"}],{"sku":"a"}]}`,
			evidence: map[string]string{"4": "real"},
		},
		{
			name:     "empty evidence is removed and not kept",
			in:       `{"lines":[{"sku":"a","evidence":""},{"sku":"b","evidence":"b-ev"}]}`,
			clean:    `{"lines":[{"sku":"a"},{"sku":"b"}]}`,
			evidence: map[string]string{"1": "b-ev"},
		},
		{name: "null evidence is removed and not kept", in: `{"lines":[{"sku":"a","evidence":null}]}`, clean: `{"lines":[{"sku":"a"}]}`},
		{name: "duplicated evidence: last wins", in: `{"lines":[{"evidence":"uno","evidence":"dos"}]}`, clean: `{"lines":[{}]}`, evidence: map[string]string{"0": "dos"}},
		{name: "whitespace evidence is kept as is", in: `{"lines":[{"evidence":"   "}]}`, clean: `{"lines":[{}]}`, evidence: map[string]string{"0": "   "}},
		{
			name:     "position ten is the decimal text 10",
			in:       `{"lines":[{"n":0},{"n":1},{"n":2},{"n":3},{"n":4},{"n":5},{"n":6},{"n":7},{"n":8},{"n":9},{"n":10,"evidence":"diez"}]}`,
			clean:    `{"lines":[{"n":0},{"n":1},{"n":2},{"n":3},{"n":4},{"n":5},{"n":6},{"n":7},{"n":8},{"n":9},{"n":10}]}`,
			evidence: map[string]string{"10": "diez"},
		},
		{
			name:     "number bytes are not reinterpreted",
			in:       `{"source_text":"s","a":1e2,"b":2500.00,"c":-0,"d":12345678901234567890,"lines":[{"qty":1.0,"unit_price":2.50,"evidence":"e"}]}`,
			clean:    `{"a":1e2,"b":2500.00,"c":-0,"d":12345678901234567890,"lines":[{"qty":1.0,"unit_price":2.50}]}`,
			source:   "s",
			evidence: map[string]string{"0": "e"},
		},
		{
			name:   "inner whitespace is compacted, untouched lines keep their key order",
			in:     `{"source_text":"s","nested":{ "k" : [ 1 , 2 ] },"lines":[ { "sku" : "a" , "qty" : 1 } ]}`,
			clean:  `{"lines":[{"sku":"a","qty":1}],"nested":{"k":[1,2]}}`,
			source: "s",
		},
		{
			name: "raw html runes and line separator left behind are escaped",
			in: `{"source_text":"a<b>&c` + literalTestLineSeparator + ` ` + literalTestArabicDigits + literalTestNoBreakSpace +
				`","label":"x<y>&z` + literalTestLineSeparator + `","lines":[{"label":"<b>` + literalTestArabicDigits +
				literalTestNoBreakSpace + ` ","evidence":" ` + literalTestIdeographicSpace + `esp "}]}`,
			clean: `{"label":"x\u003cy\u003e\u0026z\u2028","lines":[{"label":"\u003cb\u003e` +
				literalTestArabicDigits + literalTestNoBreakSpace + ` "}]}`,
			source:   "a<b>&c" + literalTestLineSeparator + " " + literalTestArabicDigits + literalTestNoBreakSpace,
			evidence: map[string]string{"0": " " + literalTestIdeographicSpace + "esp "},
		},
		{
			name:     "escaped keys are the literal once decoded",
			in:       `{"source\u005ftext":"escapada","lines":[{"\u0065vidence":"esc","sku":"a"}]}`,
			clean:    `{"lines":[{"sku":"a"}]}`,
			source:   "escapada",
			evidence: map[string]string{"0": "esc"},
		},
		{
			name:     "json escapes: the literal comes out decoded, what stays keeps its bytes",
			in:       `{"source_text":"\u0048ola \u00e9","label":"caf\u00e9 \u003cb\u003e \u2028","lines":[{"label":"t\u00e9","evidence":"fr\u00e1se"},{"label":"n\u00e9"}]}`,
			clean:    `{"label":"caf\u00e9 \u003cb\u003e \u2028","lines":[{"label":"t\u00e9"},{"label":"n\u00e9"}]}`,
			source:   "Hola é",
			evidence: map[string]string{"0": "fráse"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clean, lit, err := SplitLiteral(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("SplitLiteral: %v", err)
			}
			if string(clean) != c.clean {
				t.Errorf("payload limpio:\n got %s\nquería %s", clean, c.clean)
			}
			if lit.SourceText != c.source {
				t.Errorf("source_text extraído = %q, quería %q", lit.SourceText, c.source)
			}
			if !sameEvidence(lit.Evidence, c.evidence) {
				t.Errorf("evidencias extraídas = %q, quería %q", lit.Evidence, c.evidence)
			}
		})
	}
}

// TestSplitLiteral_RejectsALiteralThatIsNotAString: dejarlo pasar persistiría en
// claro algo que se llama `source_text` o `evidence`.
func TestSplitLiteral_RejectsALiteralThatIsNotAString(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"source_text is an object", `{"version":1,"source_text":{"texto":"hola Herminia"}}`, "intakes: source_text del payload no es una cadena: json: cannot unmarshal object into Go value of type string"},
		{"source_text is a number", `{"source_text":7}`, "intakes: source_text del payload no es una cadena: json: cannot unmarshal number into Go value of type string"},
		{"source_text is an array", `{"source_text":["a"]}`, "intakes: source_text del payload no es una cadena: json: cannot unmarshal array into Go value of type string"},
		{"source_text is a bool", `{"source_text":true}`, "intakes: source_text del payload no es una cadena: json: cannot unmarshal bool into Go value of type string"},
		{"evidence of the second line is a number", `{"lines":[{"sku":"a","evidence":"ok"},{"sku":"b","evidence":5}]}`, "intakes: evidence de la línea 1 no es una cadena: json: cannot unmarshal number into Go value of type string"},
		{"evidence of the first line is an object", `{"lines":[{"evidence":{"t":"x"}}]}`, "intakes: evidence de la línea 0 no es una cadena: json: cannot unmarshal object into Go value of type string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clean, lit, err := SplitLiteral(json.RawMessage(c.in))
			if err == nil {
				t.Fatalf("un literal que no es cadena se aceptó en silencio: %s", clean)
			}
			if got := err.Error(); got != c.want {
				t.Errorf("error = %q, quería %q", got, c.want)
			}
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Errorf("el error de encoding/json no va envuelto con %%w: %v", err)
			}
			if clean != nil || !lit.Empty() {
				t.Errorf("con error debe devolver (nil, vacío): got (%s, %+v)", clean, lit)
			}
		})
	}
}

// TestSplitThenMergeLiteral_IsTheSameJSON: el payload reconstruido equivale al
// original como JSON, aunque no byte a byte.
func TestSplitThenMergeLiteral_IsTheSameJSON(t *testing.T) {
	t.Parallel()
	clean, lit, err := SplitLiteral(json.RawMessage(literalTestAmberPayload))
	if err != nil {
		t.Fatalf("SplitLiteral: %v", err)
	}
	out, err := MergeLiteral(clean, lit)
	if err != nil {
		t.Fatalf("MergeLiteral: %v", err)
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(literalTestAmberPayload), &before); err != nil {
		t.Fatalf("payload original ilegible: %v", err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatalf("payload reconstruido ilegible: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("la ida y vuelta no devolvió el mismo payload:\n antes: %v\ndespués: %v", before, after)
	}
}
