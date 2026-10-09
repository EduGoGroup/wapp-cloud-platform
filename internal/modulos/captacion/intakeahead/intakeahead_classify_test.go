//go:build pendiente

package intakeahead_test

// intakeahead_classify_test.go — lo que un worker hace con UNA petición, visto por el
// contrato de Request: el prompt que arma, los fallos que degradan solo el adelanto y el
// reintento por calidad con su presupuesto.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-shared/intents"
	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

const (
	noCatalogReadMsg  = "adelanto: no se pudo leer el catálogo de intenciones del tenant"
	invalidCatalogMsg = "adelanto: el catálogo publicado del tenant no valida; no se pide inferencia"
	noProviderMsg     = "adelanto: sin proveedor LLM para el tenant; la ventana cerrará por su reloj"
	noClassifyMsg     = "adelanto: la clasificación no salió; la ventana cerrará por su reloj"
)

// wantCatalog es el catálogo publicado tal como tiene que llegar al prompt.
func wantCatalog() []llm.IntentSpec {
	return []llm.IntentSpec{
		{
			Name:        "intake_request",
			Description: "El cliente pide un presupuesto o hace un pedido",
			Params:      []string{},
			Examples: []llm.IntentExample{
				{Message: "quiero 200 sillas para el sábado"},
				{Message: "me pasas precio de 3 mesas"},
			},
		},
		{
			Name:        "consulta",
			Description: "El cliente pregunta algo que no es un pedido",
			Params:      []string{"tema"},
			Examples: []llm.IntentExample{
				{Message: "a qué hora abren", Params: map[string]string{"tema": "hora"}},
			},
		},
	}
}

// TestRequest_BuildsThePromptFromThePublishedCatalog: el prompt sale del catálogo del
// tenant TAL CUAL —en su orden, con `params: []` sin rellenar (D-044.20), con sus
// ejemplos y su vocabulario— y la etiqueta de escape es la reservada del contrato, que
// NO viaja además como una intención.
func TestRequest_BuildsThePromptFromThePublishedCatalog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)
		b.start()
		b.pool.Request(windowKey(), clientText)
		settle()

		if asked := b.cfg.seen(); len(asked) != 1 || asked[0] != tenantID {
			t.Fatalf("el catálogo se lee UNA vez y es el del tenant de la ventana: %v", asked)
		}
		calls := b.prov.seen()
		if len(calls) != 1 {
			t.Fatalf("inferencias = %d, quiero 1", len(calls))
		}
		in := calls[0].in
		if in.Text != clientText {
			t.Errorf("Text = %q, quiero el literal del cliente tal cual", in.Text)
		}
		if len(in.Catalog) != 2 || len(in.Catalog[0].Params) != 0 {
			t.Fatalf("catálogo del prompt: %+v", in.Catalog)
		}
		// `params: []` puede llegar como lista vacía o nil: lo que importa es que no se
		// rellene. El resto se compara campo a campo.
		in.Catalog[0].Params = []string{}
		if want := wantCatalog(); !reflect.DeepEqual(in.Catalog, want) {
			t.Errorf("catálogo del prompt =\n%+v\nquiero\n%+v", in.Catalog, want)
		}
		if in.UnknownLabel != intents.ReservedUnknown {
			t.Errorf("UnknownLabel = %q, quiero la reservada del contrato (%q)", in.UnknownLabel, intents.ReservedUnknown)
		}
		for _, spec := range in.Catalog {
			if spec.Name == in.UnknownLabel {
				t.Errorf("la etiqueta reservada NO puede ir además declarada como intención")
			}
		}
		if want := []string{"sillas", "mesas"}; !reflect.DeepEqual(in.Vocabulary, want) {
			t.Errorf("Vocabulary = %v, quiero %v", in.Vocabulary, want)
		}
	})
}

// TestRequest_WithoutACatalogAsksNothing: sin catálogo —el estado NORMAL de un tenant—
// o con uno que no se puede usar, no se pide proveedor ni inferencia. Solo el estado
// normal calla; los otros dos se dicen por Warn.
func TestRequest_WithoutACatalogAsksNothing(t *testing.T) {
	errDB := errors.New("la base no responde")
	cases := []struct {
		name    string
		blobs   map[string]string
		err     error
		wantMsg string // "" = ni una línea de log
		wantIn  []string
	}{
		{"tenant without a catalog", map[string]string{"t-other": publishedCatalog}, nil, "", nil},
		{"wrapped not-found", nil, fmt.Errorf("tenant %s: %w", tenantID, intentcfg.ErrNotFound), "", nil},
		{"read failure", nil, errDB, noCatalogReadMsg,
			[]string{"tenant_id=" + tenantID, "la base no responde"}},
		{"catalog that does not validate", map[string]string{tenantID: `{"version":"","intents":[]}`}, nil,
			invalidCatalogMsg, []string{"tenant_id=" + tenantID, "version=v-campo"}},
		{"catalog that is not JSON", map[string]string{tenantID: `no es json`}, nil,
			invalidCatalogMsg, []string{"tenant_id=" + tenantID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, intakeahead.WithWorkers(1))
				b.cfg.set(tc.blobs, tc.err)
				b.start()

				b.pool.Request(windowKey(), clientText)
				settle()

				if len(b.cfg.seen()) != 1 {
					t.Fatalf("la petición no llegó a leer el catálogo")
				}
				if len(b.sel.seen()) != 0 || len(b.prov.seen()) != 0 || len(b.sink.seen()) != 0 {
					t.Errorf("sin catálogo utilizable no se pide proveedor ni inferencia, ni se entrega nada")
				}
				assertOnlyLog(t, b.log, "WARN", tc.wantMsg, tc.wantIn...)

				// Solo se perdió el adelanto: con el catálogo en su sitio, la misma ventana
				// vuelve a preguntar y el mismo worker la sirve.
				b.cfg.set(map[string]string{tenantID: publishedCatalog}, nil)
				b.pool.Request(windowKey(), clientText)
				settle()
				if len(b.sink.seen()) != 1 {
					t.Errorf("tras el fallo, la ventana debe quedar libre y el worker vivo")
				}
			})
		})
	}
}

// assertOnlyLog exige que el log tenga UNA sola línea, con ese nivel, ese mensaje y esos
// trozos; o que esté vacío si msg es "".
func assertOnlyLog(t *testing.T, log *syncBuffer, level, msg string, fragments ...string) {
	t.Helper()
	all := strings.TrimSpace(log.String())
	if msg == "" {
		if all != "" {
			t.Errorf("no se esperaba ni una línea de log:\n%s", all)
		}
		return
	}
	lines := log.lines(level, msg)
	if len(lines) != 1 || strings.Count(all, "\n") != 0 {
		t.Fatalf("quiero UNA línea %s %q y nada más; log:\n%s", level, msg, all)
	}
	for _, f := range fragments {
		if !strings.Contains(lines[0], f) {
			t.Errorf("a la línea le falta %q: %s", f, lines[0])
		}
	}
}

// TestRequest_SelectorFailureLosesOnlyTheAdvance es REQ-35: con la vía caída no hay
// inferencia, no hay pista y no hay pánico; el pool sigue vivo. El aviso al dueño NO se
// escribe aquí: es del selector.
func TestRequest_SelectorFailureLosesOnlyTheAdvance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t, intakeahead.WithWorkers(1))
		b.sel.setErr(errors.New("la vía del tenant no está disponible"))
		b.start()

		b.pool.Request(windowKey(), clientText)
		settle()

		if len(b.sel.seen()) != 1 {
			t.Fatalf("el proveedor se pide una vez: %d", len(b.sel.seen()))
		}
		if len(b.prov.seen()) != 0 || len(b.sink.seen()) != 0 {
			t.Errorf("sin proveedor no hay inferencia ni entrega")
		}
		assertOnlyLog(t, b.log, "DEBUG", noProviderMsg,
			"tenant_id="+tenantID, "la vía del tenant no está disponible")

		b.sel.setErr(nil)
		b.pool.Request(windowKey(), clientText)
		settle()
		if len(b.sink.seen()) != 1 {
			t.Errorf("tras el fallo, la ventana debe quedar libre y el worker vivo")
		}
	})
}

// TestRequest_ProviderFailureIsNotRetried: el reintento es por CALIDAD, no por vía.
// Reintentar un Edge caído sería pagar dos veces el mismo timeout dentro de la ventana.
func TestRequest_ProviderFailureIsNotRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t, intakeahead.WithWorkers(1))
		b.prov.setReplies(reply{err: errors.New("el Edge no responde")})
		b.start()

		b.pool.Request(windowKey(), clientText)
		settle()

		if got := len(b.prov.seen()); got != 1 {
			t.Fatalf("un fallo de VÍA no se reintenta: %d llamadas", got)
		}
		if len(b.sink.seen()) != 0 {
			t.Errorf("con la vía caída no llega ninguna pista")
		}
		assertOnlyLog(t, b.log, "DEBUG", noClassifyMsg,
			"tenant_id="+tenantID, "session_id="+sessionID, "el Edge no responde")

		b.prov.setReplies(reply{raw: goodArtifact()})
		b.pool.Request(windowKey(), clientText)
		settle()
		if len(b.sink.seen()) != 1 {
			t.Errorf("tras el fallo, la ventana debe quedar libre y el worker vivo")
		}
	})
}

// TestRequest_RetriesOnceOnQuality: la salida no interpretable se reintenta UNA vez a
// TemperatureRetry, con la MISMA entrada. Vale igual si la calidad la rechaza el parser
// (la salida no es un artefacto) o el propio proveedor (devuelve el centinela envuelto).
func TestRequest_RetriesOnceOnQuality(t *testing.T) {
	cases := []struct {
		name  string
		first reply
	}{
		{"output that is not JSON", reply{raw: "esto no es JSON ni de lejos"}},
		{"intent outside the catalog", reply{raw: artifact("otra_cosa", 0.9, goodEvidence, nil)}},
		{"provider reports quality", reply{err: fmt.Errorf("salida truncada: %w", llm.ErrLLMQuality)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t)
				b.prov.setReplies(tc.first, reply{raw: artifact("intake_request", 0.88, goodEvidence, nil)})
				b.start()

				b.pool.Request(windowKey(), clientText)
				settle()

				calls := b.prov.seen()
				if len(calls) != 2 {
					t.Fatalf("un fallo de calidad se reintenta UNA vez: %d llamadas", len(calls))
				}
				if calls[0].temperature != llm.TemperatureGreedy || calls[1].temperature != llm.TemperatureRetry {
					t.Errorf("temperaturas = %v y %v, quiero greedy y luego la de reintento",
						calls[0].temperature, calls[1].temperature)
				}
				if !reflect.DeepEqual(calls[0].in, calls[1].in) {
					t.Errorf("el reintento lleva la misma entrada que la primera pasada")
				}
				if len(b.sel.seen()) != 1 {
					t.Errorf("el proveedor se elige una vez, no una por pasada")
				}
				got := b.sink.seen()
				if len(got) != 1 || got[0].confidence != 0.88 {
					t.Errorf("el reintento debe poder salvar la clasificación: %+v", got)
				}
				if b.log.String() != "" {
					t.Errorf("un reintento que sale bien no loguea:\n%s", b.log.String())
				}
			})
		})
	}
}

// TestRequest_GivesUpAfterTwoQualityFailures: el segundo fallo de calidad seguido no es
// mala suerte. No hay tercera pasada.
func TestRequest_GivesUpAfterTwoQualityFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t, intakeahead.WithWorkers(1))
		b.prov.setReplies(reply{raw: "basura"})
		b.start()

		b.pool.Request(windowKey(), clientText)
		settle()

		if got := len(b.prov.seen()); got != 2 {
			t.Fatalf("como mucho DOS pasadas (una y su reintento): %d", got)
		}
		if len(b.sink.seen()) != 0 {
			t.Errorf("sin clasificación interpretable no llega ninguna pista")
		}
		assertOnlyLog(t, b.log, "DEBUG", noClassifyMsg, llm.ErrLLMQuality.Error())

		b.prov.setReplies(reply{raw: goodArtifact()})
		b.pool.Request(windowKey(), clientText)
		settle()
		if len(b.sink.seen()) != 1 {
			t.Errorf("tras rendirse, la ventana debe quedar libre y el worker vivo")
		}
	})
}

// TestRequest_RetryNeedsHalfTheBudget: las dos pasadas comparten UN presupuesto, y el
// reintento solo arranca si le queda al menos la MITAD. El umbral se escala con
// WithTimeout. La primera pasada «tarda» sobre el reloj de la burbuja.
func TestRequest_RetryNeedsHalfTheBudget(t *testing.T) {
	const custom = 10 * time.Second
	cases := []struct {
		name      string
		opts      []intakeahead.Option
		firstPass time.Duration
		wantRetry bool
	}{
		{"instant first pass", nil, 0, true},
		{"exactly half left", nil, intakeahead.DefaultTimeout / 2, true},
		{"a hair under half left", nil, intakeahead.DefaultTimeout/2 + time.Nanosecond, false},
		{"almost nothing left", nil, intakeahead.DefaultTimeout - time.Second, false},
		{"custom budget, exactly half left",
			[]intakeahead.Option{intakeahead.WithTimeout(custom)}, custom / 2, true},
		{"custom budget, a hair under half left",
			[]intakeahead.Option{intakeahead.WithTimeout(custom)}, custom/2 + time.Nanosecond, false},
		// 6 s serían «de sobra» con el presupuesto por defecto: el umbral es relativo.
		{"custom budget, six seconds gone",
			[]intakeahead.Option{intakeahead.WithTimeout(custom)}, 6 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, tc.opts...)
				b.prov.setReplies(reply{raw: "esto no es JSON"}, reply{raw: goodArtifact()})
				b.prov.setHook(func(_ context.Context, n int) error {
					if n == 0 {
						time.Sleep(tc.firstPass) // reloj de la burbuja
					}
					return nil
				})
				b.start()

				b.pool.Request(windowKey(), clientText)
				// El test duerme un pelo MÁS que la primera pasada: así el reloj de la burbuja
				// solo llega ahí cuando el worker ya decidió y no queda nada por pasar.
				time.Sleep(tc.firstPass + time.Nanosecond)
				settle()

				calls, delivered := len(b.prov.seen()), len(b.sink.seen())
				if tc.wantRetry {
					if calls != 2 || delivered != 1 {
						t.Fatalf("con media vuelta de presupuesto el reintento arranca y salva la clasificación: "+
							"%d llamadas, %d entregas", calls, delivered)
					}
					return
				}
				if calls != 1 || delivered != 0 {
					t.Fatalf("con menos de media vuelta de presupuesto NO se reintenta: %d llamadas, %d entregas",
						calls, delivered)
				}
				// Se registra la causa de verdad —calidad—, no una de plazo inventada.
				assertOnlyLog(t, b.log, "DEBUG", noClassifyMsg, llm.ErrLLMQuality.Error())
			})
		})
	}
}
