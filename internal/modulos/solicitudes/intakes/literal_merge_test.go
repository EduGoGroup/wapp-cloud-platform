//go:build pendiente

package intakes

import (
	"encoding/json"
	"testing"
)

// Trozo de literal_test.go (E-13): MergeLiteral y el viaje de ida y vuelta con
// SplitLiteral. Los literales salen del fichero viejo, como allí se explica.

// TestMergeLiteral_EmptyLiteralReturnsThePayloadAsIs: una revisión sin texto y una
// revisión PODADA devuelven su payload con los mismos bytes, sea lo que sea.
func TestMergeLiteral_EmptyLiteralReturnsThePayloadAsIs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		lit  LiteralRevision
	}{
		{"object", `{"a":1}`, LiteralRevision{}},
		{"not an object", `[1]`, LiteralRevision{}},
		{"spaced object with an empty evidence map", ` { "b" : 1 , "a" : 2 } `, LiteralRevision{Evidence: map[string]string{}}},
		{"empty payload", ``, LiteralRevision{}},
	}
	for _, c := range cases {
		out, err := MergeLiteral(json.RawMessage(c.in), c.lit)
		if err != nil {
			t.Fatalf("%s: MergeLiteral: %v", c.name, err)
		}
		if string(out) != c.in {
			t.Errorf("%s: got %q, quería el payload tal cual %q", c.name, out, c.in)
		}
	}
}

// TestMergeLiteral_PutsTheLiteralBack: el texto vuelve a la raíz y cada evidencia a
// la línea de su posición; el resto conserva sus bytes y sale con las claves en
// orden alfabético.
func TestMergeLiteral_PutsTheLiteralBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		lit  LiteralRevision
		want string
	}{
		{
			name: "source only, html runes and line separator are escaped",
			in:   `{"version":1,"lines":[{"sku":"a"}],"a":9007199254740993}`,
			lit:  LiteralRevision{SourceText: "hola <Herminia> & co" + literalTestLineSeparator},
			want: `{"a":9007199254740993,"lines":[{"sku":"a"}],"source_text":"hola \u003cHerminia\u003e \u0026 co\u2028","version":1}`,
		},
		{
			name: "source overwrites the one already there",
			in:   `{"source_text":"viejo","z":1}`,
			lit:  LiteralRevision{SourceText: "nuevo"},
			want: `{"source_text":"nuevo","z":1}`,
		},
		{
			name: "evidence only, number bytes survive",
			in:   `{"version":1,"lines":[{"sku":"a"},{"sku":"b","qty":2.50}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"1": "frase b"}},
			want: `{"lines":[{"sku":"a"},{"evidence":"frase b","qty":2.50,"sku":"b"}],"version":1}`,
		},
		{
			name: "source and every evidence",
			in:   `{"version":1,"lines":[{"sku":"a"},{"sku":"b"}],"total":1e2}`,
			lit:  LiteralRevision{SourceText: "s", Evidence: map[string]string{"0": "ea", "1": "eb"}},
			want: `{"lines":[{"evidence":"ea","sku":"a"},{"evidence":"eb","sku":"b"}],"source_text":"s","total":1e2,"version":1}`,
		},
		{
			name: "evidence overwrites the one already there",
			in:   `{"lines":[{"evidence":"vieja","sku":"a"}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"0": "nueva"}},
			want: `{"lines":[{"evidence":"nueva","sku":"a"}]}`,
		},
		{
			name: "last valid position",
			in:   `{"lines":[{"sku":"a"},{"sku":"b"}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"1": "x"}},
			want: `{"lines":[{"sku":"a"},{"evidence":"x","sku":"b"}]}`,
		},
		{
			name: "empty evidence value is written as an empty string",
			in:   `{"lines":[{"sku":"a"}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"0": ""}},
			want: `{"lines":[{"evidence":"","sku":"a"}]}`,
		},
		{
			name: "plus sign is read as a decimal position",
			in:   `{"lines":[{"sku":"a"}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"+0": "x"}},
			want: `{"lines":[{"evidence":"x","sku":"a"}]}`,
		},
		{
			name: "minus zero is position zero",
			in:   `{"lines":[{"sku":"a"}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"-0": "x"}},
			want: `{"lines":[{"evidence":"x","sku":"a"}]}`,
		},
		{
			name: "leading zeros are read as a decimal position",
			in:   `{"lines":[{"sku":"a"},{"sku":"b"}]}`,
			lit:  LiteralRevision{Evidence: map[string]string{"01": "x"}},
			want: `{"lines":[{"sku":"a"},{"evidence":"x","sku":"b"}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := MergeLiteral(json.RawMessage(c.in), c.lit)
			if err != nil {
				t.Fatalf("MergeLiteral: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("payload fundido:\n got %s\nquería %s", out, c.want)
			}
		})
	}
}

// TestMergeLiteral_RejectsWhatItCannotPlace: perder el literal en silencio es perder
// la defensa del dueño, y devolver una evidencia a la línea EQUIVOCADA es peor que
// no devolverla. Cada caso lleva UNA sola evidencia: el orden de un mapa no es
// contrato.
func TestMergeLiteral_RejectsWhatItCannotPlace(t *testing.T) {
	t.Parallel()
	const notAnObject = "intakes: el payload de la revisión no es un objeto JSON: no hay dónde devolver el literal"
	oneLine := `{"lines":[{"sku":"a"}]}`
	at := func(pos string) LiteralRevision {
		return LiteralRevision{Evidence: map[string]string{pos: "x"}}
	}
	cases := []struct {
		name string
		in   string
		lit  LiteralRevision
		want string
	}{
		{"payload is an array", `[1]`, LiteralRevision{SourceText: "s"}, notAnObject},
		{"payload is null", `null`, LiteralRevision{SourceText: "s"}, notAnObject},
		{"payload is empty", ``, LiteralRevision{SourceText: "s"}, notAnObject},
		{"payload is broken json", `{"a":`, at("0"), notAnObject},

		{"position beyond the lines", `{"version":1,"lines":[{"sku":"a"},{"sku":"b"}]}`, at("7"), `intakes: la evidencia "7" no corresponde a ninguna de las 2 líneas de la revisión`},
		{"position exactly the number of lines", `{"lines":[{"sku":"a"},{"sku":"b"}]}`, at("2"), `intakes: la evidencia "2" no corresponde a ninguna de las 2 líneas de la revisión`},
		{"negative position", oneLine, at("-1"), `intakes: la evidencia "-1" no corresponde a ninguna de las 1 líneas de la revisión`},
		{"position is not a number", oneLine, at("x"), `intakes: la evidencia "x" no corresponde a ninguna de las 1 líneas de la revisión`},
		{"empty position", oneLine, at(""), `intakes: la evidencia "" no corresponde a ninguna de las 1 líneas de la revisión`},
		{"arabic-indic digit is not a position", oneLine, at("\u0660"), "intakes: la evidencia \"\u0660\" no corresponde a ninguna de las 1 líneas de la revisión"},
		{"fullwidth digit is not a position", oneLine, at("\uff10"), "intakes: la evidencia \"\uff10\" no corresponde a ninguna de las 1 líneas de la revisión"},
		{"space padded position", oneLine, at(" 0"), `intakes: la evidencia " 0" no corresponde a ninguna de las 1 líneas de la revisión`},
		{"decimal position", oneLine, at("0.0"), `intakes: la evidencia "0.0" no corresponde a ninguna de las 1 líneas de la revisión`},
		{"no lines key counts as zero lines", `{"version":1}`, at("0"), `intakes: la evidencia "0" no corresponde a ninguna de las 0 líneas de la revisión`},
		{"null lines count as zero lines", `{"lines":null}`, at("0"), `intakes: la evidencia "0" no corresponde a ninguna de las 0 líneas de la revisión`},
		{"empty lines", `{"lines":[]}`, at("0"), `intakes: la evidencia "0" no corresponde a ninguna de las 0 líneas de la revisión`},
		{"object lines count as zero lines", `{"lines":{"0":{}}}`, at("0"), `intakes: la evidencia "0" no corresponde a ninguna de las 0 líneas de la revisión`},
		{"valid source does not excuse an orphan evidence", oneLine, LiteralRevision{SourceText: "s", Evidence: map[string]string{"3": "x"}}, `intakes: la evidencia "3" no corresponde a ninguna de las 1 líneas de la revisión`},

		{"line is a number", `{"lines":[7,{"sku":"b"}]}`, at("0"), "intakes: la línea 0 de la revisión no es un objeto JSON"},
		{"line is null", `{"lines":[{"sku":"a"},null]}`, at("1"), "intakes: la línea 1 de la revisión no es un objeto JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := MergeLiteral(json.RawMessage(c.in), c.lit)
			if err == nil {
				t.Fatalf("se fundió un literal que no tiene sitio: %s", out)
			}
			if got := err.Error(); got != c.want {
				t.Errorf("error = %q, quería %q", got, c.want)
			}
			if out != nil {
				t.Errorf("con error el payload debe ser nil, got %s", out)
			}
		})
	}
}

// TestSplitThenMergeLiteral_RoundTrip: lo que se guarda vuelve. Sin esto el cifrado
// podría estar impecable y la bandeja seguir sin poder enseñarle al dueño el
// original al lado de la interpretación.
func TestSplitThenMergeLiteral_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{
			name: "amber payload",
			in:   literalTestAmberPayload,
			want: `{"analysis":{"provider":"local","reanalyzed_from":null},"lines":[{"customization":"sin sal","evidence":"una torta de bizcocho húmedo","kind":"matched","label":"Torta de chocolate","qty":1,"sku":"torta-choc","unit_price":25000},{"evidence":"un paquete de tequeños de 30","kind":"unmatched","label":"tequeños congelados","qty":1,"unit_price":null},{"kind":"shipping","label":"Envío","qty":1,"unit_price":null}],"message_ts":"2026-07-13T09:55:00Z","source_text":"### MENSAJES ###\ncliente: Hola Herminia, 2 tortas\n### FIN ###","suggested_questions":[],"version":1,"warnings":[{"item_pos":1,"reason":"sin_precio"}]}`,
		},
		{name: "cart payload comes back byte for byte", in: literalTestCartPayload, want: literalTestCartPayload},
		{name: "broken json comes back byte for byte", in: `{"source_text":"x"`, want: `{"source_text":"x"`},
		{
			name: "source only",
			in:   `{"version":1,"source_text":"hola","total":9007199254740993,"lines":[]}`,
			want: `{"lines":[],"source_text":"hola","total":9007199254740993,"version":1}`,
		},
		{name: "empty source_text does not come back", in: `{"b":1,"source_text":"","a":2}`, want: `{"a":2,"b":1}`},
		{name: "null evidence does not come back", in: `{"lines":[{"sku":"a","evidence":null}]}`, want: `{"lines":[{"sku":"a"}]}`},
		{
			name: "non object elements",
			in:   `{"lines":[1,null,"evidence",[{"evidence":"nested"}],{"sku":"a","evidence":"real"}]}`,
			want: `{"lines":[1,null,"evidence",[{"evidence":"nested"}],{"evidence":"real","sku":"a"}]}`,
		},
		{
			name: "position ten",
			in:   `{"lines":[{"n":0},{"n":1},{"n":2},{"n":3},{"n":4},{"n":5},{"n":6},{"n":7},{"n":8},{"n":9},{"n":10,"evidence":"diez"}]}`,
			want: `{"lines":[{"n":0},{"n":1},{"n":2},{"n":3},{"n":4},{"n":5},{"n":6},{"n":7},{"n":8},{"n":9},{"evidence":"diez","n":10}]}`,
		},
		{
			name: "number bytes",
			in:   `{"source_text":"s","a":1e2,"b":2500.00,"c":-0,"d":12345678901234567890,"lines":[{"qty":1.0,"unit_price":2.50,"evidence":"e"}]}`,
			want: `{"a":1e2,"b":2500.00,"c":-0,"d":12345678901234567890,"lines":[{"evidence":"e","qty":1.0,"unit_price":2.50}],"source_text":"s"}`,
		},
		{
			name: "json escapes: the literal comes back as utf-8, what stayed keeps its bytes",
			in:   `{"source_text":"\u0048ola \u00e9","label":"caf\u00e9 \u003cb\u003e \u2028","lines":[{"label":"t\u00e9","evidence":"fr\u00e1se"},{"label":"n\u00e9"}]}`,
			want: `{"label":"caf\u00e9 \u003cb\u003e \u2028","lines":[{"evidence":"fráse","label":"t\u00e9"},{"label":"n\u00e9"}],"source_text":"Hola é"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clean, lit, err := SplitLiteral(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("SplitLiteral: %v", err)
			}
			out, err := MergeLiteral(clean, lit)
			if err != nil {
				t.Fatalf("MergeLiteral: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("ida y vuelta:\n got %s\nquería %s", out, c.want)
			}
		})
	}
}
