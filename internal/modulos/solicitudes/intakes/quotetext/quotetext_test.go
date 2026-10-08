package quotetext_test

// quotetext_test.go — el generador: su vocabulario, su construcción y el ORDEN de
// Suggest. El few-shot vive en quotetext_fewshot_test.go.
//
// Los asserts de salida son por igualdad donde la igualdad ES lo que se afirma: que el
// texto devuelto es EXACTAMENTE el del modelo, o EXACTAMENTE el del render
// determinista. Confundir esos dos casos es como un fallback se cuela disfrazado de
// éxito.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// Aserciones de compilación del contrato de quotetext.go: las firmas, y quién
// satisface cada puerto (todos de LECTURA).
var (
	_ func(logger.Logger, quotetext.IntakeReader, quotetext.HistoryReader,
		quotetext.ProviderSelector, ...quotetext.Option) (*quotetext.Service, error) = quotetext.NewService
	_ func(*quotetext.Service, context.Context, string, string) (quotetext.Suggestion, error) = (*quotetext.Service).Suggest
	_ func(quotetext.SeedReader) quotetext.Option                                             = quotetext.WithSeed
	_ func(int) quotetext.Option                                                              = quotetext.WithExamples
	_ func(time.Duration) quotetext.Option                                                    = quotetext.WithTimeout

	_ quotetext.IntakeReader     = (*intakes.MemoryStore)(nil)
	_ quotetext.IntakeReader     = (*intakes.Service)(nil)
	_ quotetext.HistoryReader    = (*intakes.MemoryStore)(nil)
	_ quotetext.HistoryReader    = (*intakes.Postgres)(nil)
	_ quotetext.SeedReader       = (*fakeSeed)(nil)
	_ quotetext.ProviderSelector = (*fakeSelector)(nil)
)

// TestGeneratorVocabulary fija lo que viaja por la API, por el log y por
// `tenant_content`: el identificador se tradujo, el valor NO.
func TestGeneratorVocabulary(t *testing.T) {
	numbers := map[string][2]int{
		"DefaultExamples": {quotetext.DefaultExamples, 5},
		"MaxExampleRunes": {quotetext.MaxExampleRunes, 1200},
		"MaxFewShotRunes": {quotetext.MaxFewShotRunes, 3000},
	}
	for name, pair := range numbers {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d; se esperaba %d", name, pair[0], pair[1])
		}
	}
	texts := map[string]string{
		quotetext.SeedStyleRef:              "quote_style_examples",
		quotetext.SourceLLM:                 "llm",
		quotetext.SourceDeterministic:       "deterministic",
		quotetext.ReasonNoExamples:          "sin_ejemplos",
		quotetext.ReasonProviderUnavailable: "proveedor_no_disponible",
		quotetext.ReasonLLMFailed:           "llm_fallo",
		quotetext.ReasonUnreadableOutput:    "salida_no_es_artefacto",
		quotetext.ErrNotWired.Error():       "quotetext: faltan piezas obligatorias (log, solicitudes, historial o selector)",
		quotetext.ErrNoLines.Error():        "quotetext: la solicitud no tiene líneas que cotizar",
	}
	if len(texts) != 9 {
		t.Fatalf("hay %d textos distintos; se esperaban 9", len(texts))
	}
	for got, want := range texts {
		if got != want {
			t.Errorf("texto = %q; el valor observable es %q", got, want)
		}
	}
}

func TestNewService_MissingPieceIsNotWired(t *testing.T) {
	store := intakes.NewMemoryStore()
	log := logger.New(logger.WithWriter(&strings.Builder{}))
	selector := &fakeSelector{}
	cases := []struct {
		name     string
		log      logger.Logger
		intakes  quotetext.IntakeReader
		history  quotetext.HistoryReader
		selector quotetext.ProviderSelector
	}{
		{"no logger", nil, store, store, selector},
		{"no intake reader", log, nil, store, selector},
		{"no history reader", log, store, nil, selector},
		{"no provider selector", log, store, store, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := quotetext.NewService(c.log, c.intakes, c.history, c.selector)
			if !errors.Is(err, quotetext.ErrNotWired) {
				t.Fatalf("err = %v; se esperaba ErrNotWired", err)
			}
			if svc != nil {
				t.Error("con error no se devuelve servicio")
			}
		})
	}
}

func TestNewService_IgnoresNilOptionAndNilSeed(t *testing.T) {
	scene := newScene(t, p5Artifact(t, modelText), nil, quotetext.WithSeed(nil)).withSampleHistory(t)

	if out := scene.suggest(t); out.Source != quotetext.SourceLLM {
		t.Fatalf("origen = %q motivo = %q; una opción nil o una semilla nil no cambian nada", out.Source, out.Reason)
	}
}

// TestSuggest_WithHistoryReturnsTheModelText es la primera conducta del criterio.
// Comprueba (a) que se llamó al modelo UNA vez, por el selector, con el tenant y la
// SESIÓN DE ORIGEN de la solicitud —es lo que enruta la inferencia al Edge correcto—;
// (b) que las dos cotizaciones viajaron como few-shot, la MÁS RECIENTE primero; y (c)
// que viajó el JSON del borrador, greedy, con los importes y SIN los SKU.
func TestSuggest_WithHistoryReturnsTheModelText(t *testing.T) {
	scene := newScene(t, p5Artifact(t, modelText)).withSampleHistory(t)

	out := scene.suggest(t)

	if want := (quotetext.Suggestion{Text: modelText, Source: quotetext.SourceLLM}); out != want {
		t.Fatalf("sugerencia = %+v; se esperaba el texto EXACTO del modelo, sin motivo", out)
	}
	if scene.selector.count() != 1 || scene.provider.count() != 1 {
		t.Fatalf("selector llamado %d veces y modelo %d; se esperaba 1 y 1",
			scene.selector.count(), scene.provider.count())
	}
	if scene.selector.tenants[0] != testTenant || scene.selector.sessions[0] != testSession {
		t.Errorf("el selector recibió (tenant=%q, sesión=%q); se esperaba (%q, %q)",
			scene.selector.tenants[0], scene.selector.sessions[0], testTenant, testSession)
	}
	call := scene.provider.last(t)
	if want := []string{sampleQuoteNew, sampleQuoteOld}; !reflect.DeepEqual(call.input.Examples, want) {
		t.Errorf("few-shot = %q; se esperaban las dos cotizaciones, la más reciente primero", call.input.Examples)
	}
	if call.options.Temperature != llm.TemperatureGreedy {
		t.Errorf("temperatura = %v; P5 se pide greedy", call.options.Temperature)
	}
	wantQuote, err := fusionDraft().JSON()
	if err != nil {
		t.Fatalf("JSON del borrador: %v", err)
	}
	if string(call.input.Quote) != string(wantQuote) {
		t.Errorf("al prompt viajó\n%s\ny se esperaba el JSON del borrador\n%s", call.input.Quote, wantQuote)
	}
	if strings.Contains(string(call.input.Quote), "TORTA-CHOC") {
		t.Errorf("el borrador NO debe llevar los SKU internos al prompt:\n%s", call.input.Quote)
	}
}

// TestSuggest_WithoutExamplesNeverTouchesTheLLM es la tercera conducta: «tenant sin
// historial ⇒ render determinista directo (sin llamada)». Se comprueba con DOS
// contadores en cero —el del modelo y el del SELECTOR—, porque pedirle el provider al
// selector ya toca `tenant_llm`. El sub-test de control recorre la rama gemela: sin
// él, el assert negativo sería decorado.
func TestSuggest_WithoutExamplesNeverTouchesTheLLM(t *testing.T) {
	t.Run("no history: zero calls", func(t *testing.T) {
		scene := newScene(t, nil)
		scene.provider.err = errDown

		wantDeterministic(t, scene.suggest(t), quotetext.ReasonNoExamples)
		if scene.selector.count() != 0 || scene.provider.count() != 0 {
			t.Errorf("selector llamado %d veces y modelo %d; sin ejemplos no se llama a ninguno",
				scene.selector.count(), scene.provider.count())
		}
	})
	t.Run("control: with history the same scene does call", func(t *testing.T) {
		scene := newScene(t, p5Artifact(t, modelText)).withSampleHistory(t)
		scene.suggest(t)
		if scene.selector.count() != 1 {
			t.Fatalf("con historial el selector tiene que llamarse 1 vez y se llamó %d", scene.selector.count())
		}
	})
}

// TestSuggest_EveryStumbleFallsBackToTheDeterministicText recorre los tropiezos del
// camino LLM y exige el mismo desenlace: error nil, texto determinista y motivo
// nombrado. `calls` es cuántas veces se llamó al modelo: sin ese número, un caso
// pasaría igual si el generador nunca hubiera llegado a la rama que dice probar.
func TestSuggest_EveryStumbleFallsBackToTheDeterministicText(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*scene)
		reason  string
		calls   int
	}{
		{"the selector gives no provider", func(s *scene) { s.selector.err = errDown },
			quotetext.ReasonProviderUnavailable, 0},
		{"the provider returns an error", func(s *scene) { s.provider.err = errDown },
			quotetext.ReasonLLMFailed, 1},
		{"the provider fails on quality: no retry here", func(s *scene) {
			s.provider.err = fmt.Errorf("%w: salida truncada", llm.ErrLLMQuality)
		}, quotetext.ReasonLLMFailed, 1},
		{"the output is not the P5 artifact", func(s *scene) { s.provider.response = json.RawMessage(`{"nope":1}`) },
			quotetext.ReasonUnreadableOutput, 1},
		{"the output is not even JSON", func(s *scene) { s.provider.response = json.RawMessage(`Hola! $2100`) },
			quotetext.ReasonUnreadableOutput, 1},
		{"the artifact has an empty text", func(s *scene) { s.provider.response = p5Artifact(t, "   ") },
			quotetext.ReasonUnreadableOutput, 1},
		{"the text carries a control character", func(s *scene) {
			s.provider.response = p5Artifact(t, modelText+"\x00")
		}, quotetext.ReasonUnreadableText, 1},
		{"the text states no price", func(s *scene) {
			s.provider.response = p5Artifact(t, "Hola! ya te paso el presupuesto, dame un rato")
		}, quotetext.ReasonTextWithoutAmounts, 1},
		{"the text lacks the total", func(s *scene) {
			s.provider.response = p5Artifact(t, "Torta $2100, la otra $2950 y el envío $490")
		}, quotetext.ReasonMissingTotal, 1},
		{"the model altered a price", func(s *scene) {
			s.provider.response = p5Artifact(t, strings.Replace(modelText, "$2950", "$3000", 1))
		}, quotetext.ReasonForeignAmount, 1},
		{"the model swapped two prices", func(s *scene) { s.provider.response = p5Artifact(t, swappedText) },
			quotetext.ReasonAmountsOutOfPlace, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scene := newScene(t, p5Artifact(t, modelText)).withSampleHistory(t)
			c.prepare(scene)

			out := scene.suggest(t)

			wantDeterministic(t, out, c.reason)
			if got := scene.provider.count(); got != c.calls {
				t.Errorf("se llamó al modelo %d veces; se esperaban %d", got, c.calls)
			}
			if strings.Contains(out.Text, "3000") {
				t.Errorf("el importe inventado se coló en el texto devuelto:\n%q", out.Text)
			}
		})
	}
}

// TestSuggest_Preconditions son las puertas que se reusan de `Approve`. Las tres
// devuelven error, una sugerencia vacía, y no llegan a tocar el LLM.
func TestSuggest_Preconditions(t *testing.T) {
	pendingRevision := intakes.Revision{
		IntakeID: testIntake, Kind: intakes.RevisionKindInterpreted,
		Payload: json.RawMessage(`{"version":1,"lines":[{"label":"Torta vainilla","unit_price":null}]}`),
	}
	cases := []struct {
		name  string
		seed  func(t *testing.T, store *intakes.MemoryStore)
		check func(t *testing.T, err error)
	}{
		{"no customer lines", func(_ *testing.T, store *intakes.MemoryStore) {
			store.Add(testTenant, intakes.Intake{ID: testIntake, Status: intakes.StatusPendingApproval},
				intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 490})
		}, func(t *testing.T, err error) {
			if !errors.Is(err, quotetext.ErrNoLines) {
				t.Fatalf("err = %v; se esperaba ErrNoLines", err)
			}
		}},
		{"the intake belongs to another tenant", func(_ *testing.T, store *intakes.MemoryStore) {
			store.Add("otro-tenant", intakes.Intake{ID: testIntake, Status: intakes.StatusPendingApproval}, fusionItems...)
		}, func(t *testing.T, err error) {
			if !errors.Is(err, intakes.ErrNotFound) {
				t.Fatalf("err = %v; se esperaba el ErrNotFound del lector, tal cual", err)
			}
		}},
		{"a line without price in the current draft", func(t *testing.T, store *intakes.MemoryStore) {
			store.Add(testTenant, intakes.Intake{ID: testIntake, Status: intakes.StatusPendingApproval}, fusionItems...)
			if _, err := store.InsertRevision(context.Background(), pendingRevision); err != nil {
				t.Fatalf("sembrando la revisión interpretada: %v", err)
			}
		}, func(t *testing.T, err error) {
			var pending *intakes.PendingPriceError
			if !errors.As(err, &pending) {
				t.Fatalf("err = %v; se esperaba *intakes.PendingPriceError", err)
			}
			if got := len(pending.Lines); got != 1 {
				t.Errorf("el error trae %d líneas sin precio; se esperaba 1", got)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := intakes.NewMemoryStore()
			c.seed(t, store)
			approveElsewhere(t, store, testTenant, "intake-viejo-1", sampleQuoteOld, today)
			scene := newSceneOn(t, store, store, p5Artifact(t, modelText))

			out, err := scene.svc.Suggest(context.Background(), testTenant, testIntake)

			c.check(t, err)
			if out != (quotetext.Suggestion{}) {
				t.Errorf("con error la sugerencia va vacía; vino %+v", out)
			}
			if scene.selector.count() != 0 {
				t.Errorf("una precondición fallida no llega al LLM (selector llamado %d veces)", scene.selector.count())
			}
		})
	}
}

// TestSuggest_EveryLinePendingSkipsExamplesAndLLM: sin ni un importe, no hay nada que
// el modelo pueda copiar ni que se le pueda verificar. Ni siquiera se leen ejemplos.
func TestSuggest_EveryLinePendingSkipsExamplesAndLLM(t *testing.T) {
	store := intakes.NewMemoryStore()
	items := []intakes.Item{{SKU: "TORTA", Label: "Torta por presupuestar", Qty: 1}}
	store.Add(testTenant, intakes.Intake{ID: testIntake, Status: intakes.StatusPendingApproval}, items...)
	history := &fakeHistory{texts: []string{sampleQuoteOld}}
	seed := seedOf(t, sampleQuoteNew)
	scene := newSceneOn(t, store, history, p5Artifact(t, modelText), quotetext.WithSeed(seed))

	out := scene.suggest(t)

	want := quotetext.Suggestion{
		Text:   quotetext.Render(quotetext.DraftOf(items)),
		Source: quotetext.SourceDeterministic,
		Reason: quotetext.ReasonDraftWithoutAmounts,
	}
	if out != want {
		t.Fatalf("sugerencia = %+v; se esperaba %+v", out, want)
	}
	if scene.selector.count() != 0 {
		t.Errorf("no hay importes: no se pide provider (se pidió %d veces)", scene.selector.count())
	}
	if len(history.limits) != 0 || len(seed.refs) != 0 {
		t.Errorf("no hay importes: no se leen ejemplos (historial %d lecturas, semilla %d)",
			len(history.limits), len(seed.refs))
	}
}

// TestSuggest_DoesNotCheckTheStatus: `Approve` exige `pending_approval` porque
// transiciona; esto no transiciona nada, y el dueño puede mirar cómo habría quedado
// el texto de un pedido que ya cerró.
func TestSuggest_DoesNotCheckTheStatus(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.Add(testTenant, intakes.Intake{
		ID: testIntake, Status: intakes.StatusConfirmed, SessionID: testSession, Total: fusionTotal,
	}, fusionItems...)
	scene := newSceneOn(t, store, store, p5Artifact(t, modelText)).withSampleHistory(t)

	if out := scene.suggest(t); out.Source != quotetext.SourceLLM || out.Text != modelText {
		t.Fatalf("sugerencia = %+v; el estado de la solicitud no cierra esta puerta", out)
	}
}

// TestSuggest_WritesNothing: el generador solo lee. La solicitud queda como estaba.
func TestSuggest_WritesNothing(t *testing.T) {
	scene := newScene(t, p5Artifact(t, modelText)).withSampleHistory(t)
	before, err := scene.store.Get(context.Background(), testTenant, testIntake)
	if err != nil {
		t.Fatalf("leyendo la solicitud: %v", err)
	}

	scene.suggest(t)

	after, err := scene.store.Get(context.Background(), testTenant, testIntake)
	if err != nil {
		t.Fatalf("releyendo la solicitud: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("la solicitud cambió tras pedir la sugerencia:\nantes   %+v\ndespués %+v", before, after)
	}
}

// TestWithTimeout: el plazo acota LA llamada al modelo, y un valor que no es positivo
// deja el contexto del llamante tal cual.
func TestWithTimeout(t *testing.T) {
	cases := []struct {
		name    string
		opts    []quotetext.Option
		bounded bool
	}{
		{"without the option", nil, false},
		{"a positive timeout", []quotetext.Option{quotetext.WithTimeout(time.Minute)}, true},
		{"zero is ignored", []quotetext.Option{quotetext.WithTimeout(0)}, false},
		{"negative is ignored", []quotetext.Option{quotetext.WithTimeout(-time.Second)}, false},
		{"a non-positive value does not undo an earlier one",
			[]quotetext.Option{quotetext.WithTimeout(time.Minute), quotetext.WithTimeout(0)}, true},
		{"a negative value does not undo an earlier one",
			[]quotetext.Option{quotetext.WithTimeout(time.Minute), quotetext.WithTimeout(-time.Second)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scene := newScene(t, p5Artifact(t, modelText), c.opts...).withSampleHistory(t)
			scene.suggest(t)

			call := scene.provider.last(t)
			if call.hasDeadline != c.bounded {
				t.Fatalf("la llamada llevaba plazo = %v; se esperaba %v", call.hasDeadline, c.bounded)
			}
			if c.bounded && (call.timeLeft <= 0 || call.timeLeft > time.Minute) {
				t.Errorf("al plazo le quedaban %v; se esperaba como mucho un minuto", call.timeLeft)
			}
		})
	}
}
