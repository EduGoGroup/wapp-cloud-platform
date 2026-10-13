package modules_test

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// La clave de Vars y las etiquetas de clase y de motivo salen del proceso (Vars, la
// telemetría del engine): sus valores no cambian aunque el identificador sea nuevo.
func TestConsulta_Literals(t *testing.T) {
	if modules.VarQueryVerdict != "consulta_veredicto" {
		t.Errorf("VarQueryVerdict = %q", modules.VarQueryVerdict)
	}
	classes := map[modules.QueryClass]string{
		modules.QueryClassOption:   "opcion",
		modules.QueryClassQuantity: "cantidad",
	}
	for got, want := range classes {
		if string(got) != want {
			t.Errorf("clase = %q, quiero %q", got, want)
		}
	}
	reasons := map[modules.QueryReason]string{
		modules.QueryReasonNoResolver:   "sin_resolutor",
		modules.QueryReasonFailure:      "fallo",
		modules.QueryReasonInconclusive: "no_concluyente",
	}
	for got, want := range reasons {
		if string(got) != want {
			t.Errorf("motivo = %q, quiero %q", got, want)
		}
	}
}

// La forma del Verdict ES la garantía de privacidad: un código, un motivo de enum
// cerrado y códigos por posición. Un campo nuevo (una «evidencia», un mapa con el
// texto del cliente de clave) tiene que tropezar aquí.
func TestVerdict_ShapeCarriesNoFreeText(t *testing.T) {
	want := map[string]reflect.Type{
		"Code":   reflect.TypeFor[string](),
		"Reason": reflect.TypeFor[modules.QueryReason](),
		"Codes":  reflect.TypeFor[[]string](),
	}
	verdict := reflect.TypeFor[modules.Verdict]()
	if verdict.NumField() != len(want) {
		t.Errorf("Verdict tiene %d campos, quiero %d", verdict.NumField(), len(want))
	}
	for i := range verdict.NumField() {
		field := verdict.Field(i)
		if want[field.Name] != field.Type {
			t.Errorf("campo %s de tipo %s: no está en la forma acordada", field.Name, field.Type)
		}
	}
}

// La Query viaja en Result (el lado que no se persiste) y es la única que lleva el
// texto del cliente: entero en Text y por trozos en Chunks.
func TestQuery_TravelsInResult(t *testing.T) {
	query := modules.Query{
		Class:   modules.QueryClassOption,
		Level:   "categoria",
		Text:    "dos cafés y una empanada",
		Options: []modules.QueryOption{{Code: "1", Label: "Café"}, {Code: "2", Label: "Empanada"}},
		Chunks:  []string{"dos cafés", "una empanada"},
	}
	res := modules.Result{Query: &query}

	if res.Query.Class != "opcion" || res.Query.Level != "categoria" {
		t.Errorf("Query = %+v", res.Query)
	}
	if res.Query.Options[1].Code != "2" || res.Query.Options[1].Label != "Empanada" {
		t.Errorf("Options = %+v", res.Query.Options)
	}
	if !slices.Equal(res.Query.Chunks, []string{"dos cafés", "una empanada"}) {
		t.Errorf("Chunks = %v", res.Query.Chunks)
	}

	// Sin trozos, la consulta es sobre un solo texto.
	if single := (modules.Query{Class: modules.QueryClassQuantity, Text: "mejor dos"}); len(single.Chunks) != 0 || single.Options != nil {
		t.Errorf("Query de un solo texto = %+v", single)
	}
}

func TestVerdict_Resolved(t *testing.T) {
	cases := map[string]struct {
		verdict     modules.Verdict
		resolved    bool
		resolvedAny bool
	}{
		"zero value is unresolved":             {modules.Verdict{}, false, false},
		"single code":                          {modules.Verdict{Code: "2"}, true, true},
		"reason without code":                  {modules.Verdict{Reason: modules.QueryReasonInconclusive}, false, false},
		"code wins over a reason":              {modules.Verdict{Code: "2", Reason: modules.QueryReasonFailure}, true, true},
		"some chunk resolved":                  {modules.Verdict{Codes: []string{"", "3", ""}}, false, true},
		"every chunk resolved":                 {modules.Verdict{Codes: []string{"1", "3"}}, false, true},
		"no chunk resolved":                    {modules.Verdict{Codes: []string{"", ""}, Reason: modules.QueryReasonFailure}, false, false},
		"empty chunk list":                     {modules.Verdict{Codes: []string{}}, false, false},
		"single code with unresolved chunks":   {modules.Verdict{Code: "2", Codes: []string{""}}, true, true},
		"blank code is still a code (no trim)": {modules.Verdict{Code: " "}, true, true},
	}
	for name, tc := range cases {
		if got := tc.verdict.Resolved(); got != tc.resolved {
			t.Errorf("%s: Resolved = %v, quiero %v", name, got, tc.resolved)
		}
		if got := tc.verdict.ResolvedAny(); got != tc.resolvedAny {
			t.Errorf("%s: ResolvedAny = %v, quiero %v", name, got, tc.resolvedAny)
		}
	}
}

func TestVerdictFrom(t *testing.T) {
	t.Run("no verdict on the first pass", func(t *testing.T) {
		for _, vars := range []map[string]any{nil, {}, {"cart": "estado"}} {
			if got, ok := modules.VerdictFrom(vars); ok || got.ResolvedAny() {
				t.Errorf("VerdictFrom(%v) = %+v, %v; quiero el cero y false", vars, got, ok)
			}
		}
	})

	t.Run("resolved verdict", func(t *testing.T) {
		vars := map[string]any{modules.VarQueryVerdict: modules.Verdict{Code: "2"}}
		if got, ok := modules.VerdictFrom(vars); !ok || got.Code != "2" {
			t.Errorf("VerdictFrom = %+v, %v; quiero el veredicto sembrado", got, ok)
		}
	})

	// «Hay veredicto y no resolvió» no es «no hay veredicto»: en el primero el módulo
	// ya no puede volver a preguntar.
	t.Run("unresolved verdict is still a verdict", func(t *testing.T) {
		vars := map[string]any{modules.VarQueryVerdict: modules.Verdict{Reason: modules.QueryReasonNoResolver}}
		got, ok := modules.VerdictFrom(vars)
		if !ok {
			t.Fatal("un veredicto sin resolver se leyó como ausente: el módulo volvería a preguntar")
		}
		if got.Resolved() || got.Reason != "sin_resolutor" {
			t.Errorf("veredicto = %+v, quiero sin código y con su motivo", got)
		}
	})

	t.Run("anything else under the key is not a verdict", func(t *testing.T) {
		others := []any{
			nil,
			"2",
			&modules.Verdict{Code: "2"},
			map[string]any{"Code": "2"}, // lo que dejaría un round-trip por JSON
		}
		for _, other := range others {
			if got, ok := modules.VerdictFrom(map[string]any{modules.VarQueryVerdict: other}); ok || got.Resolved() {
				t.Errorf("VerdictFrom con %#v = %+v, %v; quiero el cero y false", other, got, ok)
			}
		}
	})
}

func TestWithVerdict(t *testing.T) {
	vars := map[string]any{"cart": "estado"}
	verdict := modules.Verdict{Codes: []string{"1", ""}}

	out := modules.WithVerdict(vars, verdict)
	if got, ok := modules.VerdictFrom(out); !ok || !slices.Equal(got.Codes, verdict.Codes) {
		t.Errorf("veredicto leído = %+v, %v; quiero el sembrado", got, ok)
	}
	if len(out) != 2 || out["cart"] != "estado" {
		t.Errorf("out = %v, quiero las claves de entrada más el veredicto", out)
	}
	if !maps.Equal(vars, map[string]any{"cart": "estado"}) {
		t.Errorf("el mapa recibido = %v, quiero intacto (la primera pasada se descarta sin rastro)", vars)
	}

	// Un veredicto nuevo sustituye al que hubiera.
	again := modules.WithVerdict(out, modules.Verdict{Code: "9"})
	if got, _ := modules.VerdictFrom(again); got.Code != "9" || len(got.Codes) != 0 {
		t.Errorf("veredicto = %+v, quiero el último sembrado", got)
	}

	if fromNil := modules.WithVerdict(nil, modules.Verdict{Code: "1"}); len(fromNil) != 1 {
		t.Errorf("WithVerdict(nil) = %v, quiero un mapa con solo el veredicto", fromNil)
	}
}

func TestStripQueryVerdict(t *testing.T) {
	t.Run("nothing to strip returns the same map", func(t *testing.T) {
		vars := map[string]any{"cart": "estado"}
		out := modules.StripQueryVerdict(vars)
		out["probe"] = true
		if _, same := vars["probe"]; !same {
			t.Error("sin veredicto que barrer devolvió una copia, quiero el mismo mapa")
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if out := modules.StripQueryVerdict(nil); out != nil {
			t.Errorf("StripQueryVerdict(nil) = %v, quiero nil", out)
		}
	})

	t.Run("verdict is removed from a copy", func(t *testing.T) {
		vars := modules.WithVerdict(map[string]any{"cart": "estado"}, modules.Verdict{Code: "2"})
		out := modules.StripQueryVerdict(vars)

		if !maps.Equal(out, map[string]any{"cart": "estado"}) {
			t.Errorf("out = %v, quiero sin el veredicto y con lo demás", out)
		}
		if _, ok := modules.VerdictFrom(vars); !ok {
			t.Error("el mapa recibido perdió el veredicto, quiero que no se mute")
		}
	})

	// Se barre la CLAVE, valga lo que valga.
	t.Run("unreadable value under the key is removed too", func(t *testing.T) {
		out := modules.StripQueryVerdict(map[string]any{modules.VarQueryVerdict: "basura"})
		if len(out) != 0 {
			t.Errorf("out = %v, quiero vacío", out)
		}
	})
}
