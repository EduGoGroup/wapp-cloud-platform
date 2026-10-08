package quotetext_test

// quotetext_fewshot_test.go — parte de quotetext_test.go: EL FEW-SHOT de D-044.11,
// historial aprobado + semilla de `tenant_content` ref `quote_style_examples`, con su
// saneo, su reparto del cupo y sus dos cotas.

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// Aserción de compilación del contrato de quotetext_fewshot.go.
var _ func([]byte) ([]string, error) = quotetext.ParseSeed

// textOf fabrica un ejemplo de `runes` runas, distinguible por su marca.
func textOf(mark string, runes int) string {
	return mark + strings.Repeat("x", runes-len(mark))
}

// wantExamples exige que la última llamada al modelo llevara EXACTAMENTE estos
// ejemplos, en este orden. Compara por largo y prefijo al fallar para no volcar miles
// de runas al log del test.
func wantExamples(t *testing.T, s *scene, want ...string) {
	t.Helper()
	got := s.examples(t)
	if reflect.DeepEqual(got, want) {
		return
	}
	t.Fatalf("few-shot = %s; se esperaba %s", brief(got), brief(want))
}

// brief resume una lista de ejemplos: las primeras runas y el largo de cada uno.
func brief(examples []string) string {
	parts := make([]string, 0, len(examples))
	for _, example := range examples {
		runes := []rune(strings.ToValidUTF8(example, "?"))
		head := runes
		if len(head) > 24 {
			head = head[:24]
		}
		parts = append(parts, fmt.Sprintf("%q (%d runas)", string(head), len(runes)))
	}
	return "[" + strings.Join(parts, " | ") + "]"
}

// TestFewShot_SeedAloneFeedsIt: un tenant SIN historial pero CON semilla sí tiene voz
// que imitar, y por tanto sí se llama al modelo. Es el arranque en frío y la única
// razón por la que la ref existe.
func TestFewShot_SeedAloneFeedsIt(t *testing.T) {
	seed := seedOf(t, sampleQuoteOld, sampleQuoteNew)
	scene := newScene(t, p5Artifact(t, modelText), quotetext.WithSeed(seed))

	if out := scene.suggest(t); out.Source != quotetext.SourceLLM {
		t.Fatalf("origen = %q motivo = %q; con semilla hay voz que imitar", out.Source, out.Reason)
	}
	if !reflect.DeepEqual(seed.refs, []string{quotetext.SeedStyleRef}) ||
		!reflect.DeepEqual(seed.tenants, []string{testTenant}) {
		t.Fatalf("se leyó tenant_content con refs %v de los tenants %v; se esperaba solo %q del tenant de la petición",
			seed.refs, seed.tenants, quotetext.SeedStyleRef)
	}
	wantExamples(t, scene, sampleQuoteOld, sampleQuoteNew)
}

// TestWithSeed_NilDoesNotUndoAnEarlierSeed: un lector nil se ignora, no desenchufa el
// que ya estaba.
func TestWithSeed_NilDoesNotUndoAnEarlierSeed(t *testing.T) {
	seed := seedOf(t, sampleQuoteOld)
	scene := newScene(t, p5Artifact(t, modelText), quotetext.WithSeed(seed), quotetext.WithSeed(nil))

	if out := scene.suggest(t); out.Source != quotetext.SourceLLM {
		t.Fatalf("origen = %q motivo = %q; la semilla enchufada primero sigue valiendo", out.Source, out.Reason)
	}
	wantExamples(t, scene, sampleQuoteOld)
}

// TestFewShot_UnusableSeedIsIgnored: hoy NINGÚN tenant tiene esta ref escrita, así que
// su ausencia es el caso normal; y un blob mal formado no puede dejar sin cotización
// a nadie. En los dos casos basta el historial.
func TestFewShot_UnusableSeedIsIgnored(t *testing.T) {
	cases := map[string]*fakeSeed{
		"the ref does not exist":   {err: errors.New("tenant content not found")},
		"the blob is not readable": {blob: []byte(`{"otra_cosa": 3}`)},
		"the blob is empty":        {blob: nil},
		"the seed has no examples": {blob: []byte(`[]`)},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			scene := newScene(t, p5Artifact(t, modelText), quotetext.WithSeed(seed)).withSampleHistory(t)

			if out := scene.suggest(t); out.Source != quotetext.SourceLLM {
				t.Fatalf("origen = %q motivo = %q; el historial solo basta", out.Source, out.Reason)
			}
			wantExamples(t, scene, sampleQuoteNew, sampleQuoteOld)
		})
	}
}

// TestFewShot_BrokenHistory: si la BD del historial falla, el generador no puede
// tumbar la petición del dueño. Sin semilla no quedan ejemplos (y no se pide
// provider); con semilla, el few-shot va sin el historial.
func TestFewShot_BrokenHistory(t *testing.T) {
	t.Run("without a seed there are no examples", func(t *testing.T) {
		scene := newSceneOn(t, fusionStore(), &fakeHistory{err: errDown}, p5Artifact(t, modelText))

		wantDeterministic(t, scene.suggest(t), quotetext.ReasonNoExamples)
		if scene.selector.count() != 0 {
			t.Errorf("con el historial caído y sin semilla no hay ejemplos, así que no se pide provider")
		}
		if !strings.Contains(scene.logs.String(), "quotetext: no se pudo leer el historial aprobado del tenant; el few-shot va sin él") {
			t.Errorf("el historial caído no se avisó:\n%s", scene.logs)
		}
	})
	t.Run("with a seed the few-shot goes without the history", func(t *testing.T) {
		scene := newSceneOn(t, fusionStore(), &fakeHistory{err: errDown}, p5Artifact(t, modelText),
			quotetext.WithSeed(seedOf(t, "semilla A", "semilla B", "semilla C")))

		if out := scene.suggest(t); out.Source != quotetext.SourceLLM {
			t.Fatalf("origen = %q motivo = %q; la semilla sola basta", out.Source, out.Reason)
		}
		wantExamples(t, scene, "semilla A", "semilla B", "semilla C")
	})
}

// TestFewShot_QuotaSplit es el reparto del cupo, que es una decisión de T5.1 (D-044.11
// no la fija): la semilla tiene reservada la mitad y el historial se queda con el
// resto; historial primero, de más reciente a más antiguo.
func TestFewShot_QuotaSplit(t *testing.T) {
	sixHistory := []string{"h1", "h2", "h3", "h4", "h5", "h6"}
	threeSeeds := []string{"semilla A", "semilla B", "semilla C"}
	cases := []struct {
		name    string
		history []string
		seed    []string
		opts    []quotetext.Option
		want    []string
	}{
		{"full history and seed: three and two", sixHistory, threeSeeds, nil,
			[]string{"h1", "h2", "h3", "semilla A", "semilla B"}},
		{"no seed: the history takes the whole quota", sixHistory, nil, nil,
			[]string{"h1", "h2", "h3", "h4", "h5"}},
		{"short history: the seed fills up to the quota", []string{"h1"}, threeSeeds, nil,
			[]string{"h1", "semilla A", "semilla B", "semilla C"}},
		{"history exactly at its share is not trimmed", []string{"h1", "h2", "h3"}, threeSeeds, nil,
			[]string{"h1", "h2", "h3", "semilla A", "semilla B"}},
		{"a seed text already in the history is not repeated", []string{"h1"}, []string{"h1", "semilla A"}, nil,
			[]string{"h1", "semilla A"}},
		{"a smaller quota", sixHistory, nil, []quotetext.Option{quotetext.WithExamples(2)},
			[]string{"h1", "h2"}},
		{"a smaller quota with seed: one and one", sixHistory, threeSeeds,
			[]quotetext.Option{quotetext.WithExamples(2)}, []string{"h1", "semilla A"}},
		{"a quota of one with seed keeps the history", sixHistory, threeSeeds,
			[]quotetext.Option{quotetext.WithExamples(1)}, []string{"h1"}},
		{"a bigger quota", sixHistory, threeSeeds, []quotetext.Option{quotetext.WithExamples(8)},
			[]string{"h1", "h2", "h3", "h4", "semilla A", "semilla B", "semilla C"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := c.opts
			if c.seed != nil {
				opts = append(opts, quotetext.WithSeed(seedOf(t, c.seed...)))
			}
			scene := newScene(t, p5Artifact(t, modelText), opts...).withHistory(t, c.history...)

			scene.suggest(t)

			wantExamples(t, scene, c.want...)
		})
	}
}

// TestWithExamples_SetsTheHistoryLimit: el N es también el `limit` con el que se pide
// el historial, y un valor que no es positivo deja DefaultExamples.
func TestWithExamples_SetsTheHistoryLimit(t *testing.T) {
	cases := []struct {
		name string
		opts []quotetext.Option
		want int
	}{
		{"without the option", nil, quotetext.DefaultExamples},
		{"a positive value", []quotetext.Option{quotetext.WithExamples(2)}, 2},
		{"zero is ignored", []quotetext.Option{quotetext.WithExamples(0)}, quotetext.DefaultExamples},
		{"negative is ignored", []quotetext.Option{quotetext.WithExamples(-3)}, quotetext.DefaultExamples},
		{"a non-positive value does not undo an earlier one",
			[]quotetext.Option{quotetext.WithExamples(2), quotetext.WithExamples(0)}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			history := &fakeHistory{texts: []string{"h1", "h2", "h3", "h4", "h5", "h6", "h7"}}
			scene := newSceneOn(t, fusionStore(), history, p5Artifact(t, modelText), c.opts...)

			scene.suggest(t)

			if !reflect.DeepEqual(history.limits, []int{c.want}) {
				t.Fatalf("el historial se pidió con los límites %v; se esperaba una lectura con %d", history.limits, c.want)
			}
			if got := len(scene.examples(t)); got != c.want {
				t.Errorf("el few-shot llevó %d ejemplos; el cupo es %d", got, c.want)
			}
		})
	}
}

// TestFewShot_Sanitizing es la PRIMERA cota y la limpieza, por fuente: fuera los
// vacíos, los repetidos, los que no son UTF-8 y los que pasan de MaxExampleRunes
// —ENTEROS: un ejemplo cortado a media frase le enseña al modelo a cortar frases—.
func TestFewShot_Sanitizing(t *testing.T) {
	atLimit := textOf("justo-", quotetext.MaxExampleRunes)
	tooLong := textOf("desbocado-", quotetext.MaxExampleRunes+1)
	accented := strings.Repeat("ñ", quotetext.MaxExampleRunes)
	dirty := []string{tooLong, "  ", "", sampleQuoteOld, "\n " + sampleQuoteOld + " \t", "roto \xff\xfe", atLimit, accented}
	want := []string{sampleQuoteOld, atLimit, accented}

	t.Run("history", func(t *testing.T) {
		scene := newSceneOn(t, fusionStore(), &fakeHistory{texts: dirty}, p5Artifact(t, modelText),
			quotetext.WithExamples(len(dirty)))
		scene.suggest(t)
		wantExamples(t, scene, want...)
	})
	t.Run("seed", func(t *testing.T) {
		// El JSON no puede transportar bytes que no sean UTF-8: el caso «roto» solo
		// existe por el historial.
		seedTexts := []string{tooLong, "  ", "", sampleQuoteOld, "\n " + sampleQuoteOld + " \t", atLimit, accented}
		scene := newScene(t, p5Artifact(t, modelText),
			quotetext.WithSeed(seedOf(t, seedTexts...)), quotetext.WithExamples(len(dirty)))
		scene.suggest(t)
		wantExamples(t, scene, want...)
	})
	t.Run("history kept by the real store", func(t *testing.T) {
		scene := newScene(t, p5Artifact(t, modelText)).withHistory(t, tooLong, "  "+sampleQuoteOld+"  ")
		scene.suggest(t)
		wantExamples(t, scene, sampleQuoteOld)
	})
	t.Run("nothing usable left means no examples", func(t *testing.T) {
		scene := newSceneOn(t, fusionStore(), &fakeHistory{texts: []string{tooLong, " "}}, p5Artifact(t, modelText))
		wantDeterministic(t, scene.suggest(t), quotetext.ReasonNoExamples)
		if scene.selector.count() != 0 {
			t.Errorf("sin ejemplos utilizables no se pide provider")
		}
	})
}

// runesOf suma las runas de todos los ejemplos.
func runesOf(examples []string) int {
	total := 0
	for _, example := range examples {
		total += utf8.RuneCountInString(example)
	}
	return total
}

// TestFewShot_AggregateBudget es la SEGUNDA cota, MaxFewShotRunes: se recorre la
// lista en orden de prioridad y, en cuanto uno NO CABE, se descartan él y TODOS LOS
// SIGUIENTES. El resultado es siempre «los K primeros», y el recorte se avisa.
func TestFewShot_AggregateBudget(t *testing.T) {
	const warning = "quotetext: el few-shot no cabe en su presupuesto; se recorta por la cola"
	thousand := []string{textOf("uno-", 1000), textOf("dos-", 1000), textOf("tres-", 1000), textOf("cuatro-", 1000)}
	big := []string{textOf("uno-", 1200), textOf("dos-", 1200), textOf("tres-", 1200), textOf("corto-", 50)}
	normal := []string{textOf("a-", 150), textOf("b-", 150), textOf("c-", 150), textOf("d-", 150), textOf("e-", 150)}
	cases := []struct {
		name    string
		history []string
		seed    []string
		kept    int
		logged  string
	}{
		{"exactly the budget fits; the next one is cut", thousand, nil, 3,
			"ejemplos_pedidos=4 ejemplos_usados=3 runas_usadas=3000 presupuesto_runas=3000"},
		{"it stops at the first that does not fit instead of skipping it", big, nil, 2,
			"ejemplos_pedidos=4 ejemplos_usados=2 runas_usadas=2400 presupuesto_runas=3000"},
		{"the seed, last in priority, is the first to go", thousand[:3], []string{"semilla A"}, 3,
			"ejemplos_pedidos=4 ejemplos_usados=3 runas_usadas=3000 presupuesto_runas=3000"},
		{"control: examples of normal size all fit", normal, nil, 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := []quotetext.Option{}
			if c.seed != nil {
				opts = append(opts, quotetext.WithSeed(seedOf(t, c.seed...)))
			}
			scene := newScene(t, p5Artifact(t, modelText), opts...).withHistory(t, c.history...)

			scene.suggest(t)

			wantExamples(t, scene, c.history[:c.kept]...)
			if n := runesOf(scene.examples(t)); n > quotetext.MaxFewShotRunes {
				t.Errorf("el few-shot usó %d runas; el presupuesto es %d", n, quotetext.MaxFewShotRunes)
			}
			logs := scene.logs.String()
			if got := strings.Contains(logs, warning); got != (c.logged != "") {
				t.Fatalf("aviso de recorte presente = %v; se esperaba %v. Log:\n%s", got, c.logged != "", logs)
			}
			if c.logged != "" && !strings.Contains(logs, "tenant_id="+testTenant+" "+c.logged) {
				t.Errorf("al aviso le faltan sus cifras (%s):\n%s", c.logged, logs)
			}
		})
	}
}

func TestParseSeed(t *testing.T) {
	const notAList = "quotetext: la semilla no es un array de textos ni un objeto con `examples`"
	const noKey = "quotetext: el objeto de la semilla no trae la clave `examples`"
	cases := []struct {
		name    string
		blob    string
		want    []string
		wantErr string
	}{
		{"bare array", `["uno","dos"]`, []string{"uno", "dos"}, ""},
		{"wrapped", `{"examples":["uno","dos"]}`, []string{"uno", "dos"}, ""},
		{"wrapped with more keys", `{"v":2,"examples":["uno"]}`, []string{"uno"}, ""},
		{"texts are returned as they come", `["  uno  ","","uno"]`, []string{"  uno  ", "", "uno"}, ""},
		{"empty bare array", `[]`, []string{}, ""},
		{"empty wrapped array", `{"examples":[]}`, []string{}, ""},
		{"json null reads as no examples", `null`, nil, ""},

		{"object without the key", `{"nope":1}`, nil, noKey},
		{"empty object", `{}`, nil, noKey},
		{"object with the key set to null", `{"examples":null}`, nil, noKey},
		{"json matches the key without minding the case", `{"EXAMPLES":["uno"]}`, []string{"uno"}, ""},
		{"a number", `42`, nil, notAList},
		{"a bare string", `"texto suelto"`, nil, notAList},
		{"not json", `no es json`, nil, notAList},
		{"empty blob", ``, nil, notAList},
		{"array of numbers", `[1,2]`, nil, notAList},
		{"array with a non-text element", `["uno",2]`, nil, notAList},
		{"wrapped array of numbers", `{"examples":[1]}`, nil, notAList},
		{"wrapped value that is not an array", `{"examples":"uno"}`, nil, notAList},
		{"trailing garbage", `["uno"] x`, nil, notAList},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := quotetext.ParseSeed([]byte(c.blob))
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("err = %v; se esperaba %q", err, c.wantErr)
				}
				if got != nil {
					t.Errorf("con error no se devuelven ejemplos; vino %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSeed devolvió error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ejemplos = %#v; se esperaba %#v", got, c.want)
			}
		})
	}
}
