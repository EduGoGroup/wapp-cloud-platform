//go:build pendiente

package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
)

// El gateway NUEVO satisface el transporte SIN adaptador: es lo que garantiza que la vía
// local habla con él directamente y que cada inferencia cuenta en Server.InFlight()
// (D-F3-13). Si la firma de Infer cambiara en cualquiera de los dos lados, esto no compila.
var _ local.Frame = (*edgegrpc.Server)(nil)

// El Provider es el puerto compartido entero: las dos vías se usan por la misma interfaz.
var _ llm.LLMProvider = (*local.Provider)(nil)

// Los dobles (fakeFrame, transportError, newProvider, catalogInput) viven en helpers_test.go.

// stageCall es una de las cinco etapas vista desde fuera: cómo se llama, qué prompt
// compartido le corresponde y qué dice de ella la tabla del contrato (R4.6.b).
type stageCall struct {
	name string
	// stage es la etapa ajustable; vacía en P1, que no tiene plantilla.
	stage llm.Etapa
	call  func(ctx context.Context, p *local.Provider, opts llm.Options) (json.RawMessage, error)
	// compiled es el prompt del Build...Prompt compartido.
	compiled string
	// adjusted arma el prompt con una plantilla dada (Build...PromptCon). nil en P1.
	adjusted func(pl llm.Plantilla) string
	// maxTokens y class son la fila de la etapa en la tabla; measured, la salida más
	// grande observada o estimada, que el techo tiene que cubrir.
	maxTokens int32
	measured  int32
	class     string
}

func stageCalls() []stageCall {
	classify := catalogInput()
	ideas := llm.ExtractMainIdeasInput{SourceText: "hola, quiero dos cosas"}
	specs := llm.ExtractItemSpecsInput{SourceText: "hola", Idea: "pizza"}
	quantities := llm.NormalizeQuantitiesInput{SourceText: "hola", MessageTS: time.Unix(1700000000, 0).UTC()}
	quote := llm.GenerateQuoteTextInput{Quote: json.RawMessage(`{"lines":[]}`)}

	return []stageCall{
		{
			name: "P1 ClassifyRequest",
			call: func(ctx context.Context, p *local.Provider, opts llm.Options) (json.RawMessage, error) {
				return p.ClassifyRequest(ctx, classify, opts)
			},
			compiled:  llm.BuildClassifyRequestPrompt(classify),
			maxTokens: 192, measured: 52, class: edgegrpc.ClassInteractive,
		},
		{
			name: "P2 ExtractMainIdeas", stage: llm.EtapaP2,
			call: func(ctx context.Context, p *local.Provider, opts llm.Options) (json.RawMessage, error) {
				return p.ExtractMainIdeas(ctx, ideas, opts)
			},
			compiled:  llm.BuildExtractMainIdeasPrompt(ideas),
			adjusted:  func(pl llm.Plantilla) string { return llm.BuildExtractMainIdeasPromptCon(pl, ideas) },
			maxTokens: 512, measured: 267, class: edgegrpc.ClassBatch,
		},
		{
			name: "P3 ExtractItemSpecs", stage: llm.EtapaP3,
			call: func(ctx context.Context, p *local.Provider, opts llm.Options) (json.RawMessage, error) {
				return p.ExtractItemSpecs(ctx, specs, opts)
			},
			compiled:  llm.BuildExtractItemSpecsPrompt(specs),
			adjusted:  func(pl llm.Plantilla) string { return llm.BuildExtractItemSpecsPromptCon(pl, specs) },
			maxTokens: 512, measured: 293, class: edgegrpc.ClassBatch,
		},
		{
			name: "P4 NormalizeQuantities", stage: llm.EtapaP4,
			call: func(ctx context.Context, p *local.Provider, opts llm.Options) (json.RawMessage, error) {
				return p.NormalizeQuantities(ctx, quantities, opts)
			},
			compiled:  llm.BuildNormalizeQuantitiesPrompt(quantities),
			adjusted:  func(pl llm.Plantilla) string { return llm.BuildNormalizeQuantitiesPromptCon(pl, quantities) },
			maxTokens: 1024, measured: 830, class: edgegrpc.ClassBatch,
		},
		{
			name: "P5 GenerateQuoteText", stage: llm.EtapaP5,
			call: func(ctx context.Context, p *local.Provider, opts llm.Options) (json.RawMessage, error) {
				return p.GenerateQuoteText(ctx, quote, opts)
			},
			compiled:  llm.BuildGenerateQuoteTextPrompt(quote),
			adjusted:  func(pl llm.Plantilla) string { return llm.BuildGenerateQuoteTextPromptCon(pl, quote) },
			maxTokens: 768, measured: 570, class: edgegrpc.ClassBatch,
		},
	}
}

// adjustedTemplate devuelve la plantilla compilada de la etapa con la instrucción
// retocada: una plantilla «de disco» que se distingue de la compilada.
func adjustedTemplate(t *testing.T, stage llm.Etapa) llm.Plantilla {
	t.Helper()
	pl, ok := llm.PlantillaPorDefecto(stage)
	if !ok {
		t.Fatalf("la etapa %q no tiene plantilla compilada", stage)
	}
	pl.Instruccion += "\nAJUSTE DE PRUEBA: responde corto."
	return pl
}

// Los textos de los centinelas y las constantes son observables: se copian literales.
func TestConstantsAndSentinels(t *testing.T) {
	t.Parallel()
	if local.DefaultFormat != "json" {
		t.Errorf("DefaultFormat = %q, quiero \"json\"", local.DefaultFormat)
	}
	if local.MargenVeredicto != 7*time.Second {
		t.Errorf("MargenVeredicto = %v, quiero 7s", local.MargenVeredicto)
	}
	if local.DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %v, quiero 30s", local.DefaultTimeout)
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"ErrSinTransporte", local.ErrSinTransporte, "llmvia/local: el adaptador local necesita un transporte (Frame)"},
		{"ErrSinTenant", local.ErrSinTenant, "llmvia/local: el adaptador local necesita un tenant"},
		{"ErrSinPresupuesto", local.ErrSinPresupuesto, "llmvia/local: al llamante no le queda plazo para una inferencia"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%s = %q, quiero %q", tc.name, got, tc.want)
		}
	}
}

// Una configuración imposible se sabe al construir, no a mitad de un pipeline; y construir
// no gasta ninguna inferencia.
func TestNewFailsAtConstruction(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frame  local.Frame
		tenant string
		want   error
	}{
		{"no frame", nil, "tenant-1", local.ErrSinTransporte},
		{"no tenant", &fakeFrame{}, "", local.ErrSinTenant},
		{"neither: the frame is reported first", nil, "", local.ErrSinTransporte},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := local.New(tc.frame, tc.tenant)
			if !errors.Is(err, tc.want) {
				t.Fatalf("New = %v, quiero %v", err, tc.want)
			}
			if p != nil {
				t.Fatal("New devolvió un Provider junto con el error")
			}
		})
	}

	t.Run("building does not touch the frame", func(t *testing.T) {
		t.Parallel()
		f := &fakeFrame{out: validOutput}
		newProvider(t, f, local.WithTargetSession("s-1"))
		if n := f.calls(); n != 0 {
			t.Fatalf("New hizo %d llamadas al transporte, quiero 0", n)
		}
	})
}

// C2 a nivel de contenido: el prompt que sale por el cable es BYTE A BYTE el de
// wapp-shared/llm. Se compara contra la función y no contra un literal: un literal sería
// una segunda copia del prompt, que se quedaría clavada en el de ayer sin avisar.
func TestFiveMethodsUseTheSharedPrompt(t *testing.T) {
	t.Parallel()
	for _, sc := range stageCalls() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: validOutput}
			if _, err := sc.call(context.Background(), newProvider(t, f), llm.Options{}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			req := f.last(t, 1)
			if req.Prompt != sc.compiled {
				t.Fatalf("%s no usó el prompt compartido.\n--- salió ---\n%s\n--- quiero ---\n%s",
					sc.name, req.Prompt, sc.compiled)
			}
			if req.Warmup {
				t.Errorf("%s salió marcada como calentamiento: el Edge la sacaría del breaker", sc.name)
			}
			if f.tenants[0] != "tenant-1" {
				t.Errorf("%s preguntó al tenant %q, quiero tenant-1", sc.name, f.tenants[0])
			}
		})
	}
}

// Con ConPlantillas, P2–P5 salen del Build...PromptCon de SU plantilla; una etapa que el
// mapa no trae sigue con el compilado; P1 no tiene plantilla y no cambia nunca.
func TestAdjustedTemplatesReplaceOnlyTheirStage(t *testing.T) {
	t.Parallel()
	all := stageCalls()
	templates := map[llm.Etapa]llm.Plantilla{}
	for _, sc := range all {
		if sc.stage != "" {
			templates[sc.stage] = adjustedTemplate(t, sc.stage)
		}
	}

	for _, sc := range all {
		t.Run(sc.name+" with every template", func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: validOutput}
			p := newProvider(t, f, local.ConPlantillas(templates))
			if _, err := sc.call(context.Background(), p, llm.Options{}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			got := f.last(t, 1).Prompt
			if sc.adjusted == nil {
				if got != sc.compiled {
					t.Fatalf("P1 cambió con las plantillas de P2–P5: no tiene plantilla ajustable")
				}
				return
			}
			want := sc.adjusted(templates[sc.stage])
			if want == sc.compiled {
				t.Fatalf("la plantilla de prueba de %s no se distingue de la compilada", sc.name)
			}
			if got != want {
				t.Fatalf("%s no usó su plantilla ajustada.\n--- salió ---\n%s\n--- quiero ---\n%s", sc.name, got, want)
			}
		})

		if sc.stage == "" {
			continue
		}
		t.Run(sc.name+" absent from the map", func(t *testing.T) {
			t.Parallel()
			// El mapa trae OTRA etapa: la de este caso no está y cae al compilado.
			other := llm.EtapaP2
			if sc.stage == llm.EtapaP2 {
				other = llm.EtapaP5
			}
			f := &fakeFrame{out: validOutput}
			p := newProvider(t, f, local.ConPlantillas(map[llm.Etapa]llm.Plantilla{other: templates[other]}))
			if _, err := sc.call(context.Background(), p, llm.Options{}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			if got := f.last(t, 1).Prompt; got != sc.compiled {
				t.Fatalf("%s no estaba en el mapa y aun así no usó el compilado", sc.name)
			}
		})
		t.Run(sc.name+" with a nil map", func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: validOutput}
			if _, err := sc.call(context.Background(), newProvider(t, f, local.ConPlantillas(nil)), llm.Options{}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			if got := f.last(t, 1).Prompt; got != sc.compiled {
				t.Fatalf("%s con un mapa nil no usó el compilado", sc.name)
			}
		})
	}
}

// La salida pasa por el ExtractJSON compartido: las dos vías tratan igual una salida sucia.
func TestOutputGoesThroughSharedExtractJSON(t *testing.T) {
	t.Parallel()
	for _, sc := range stageCalls() {
		t.Run(sc.name+": JSON wrapped in prose and fences is isolated", func(t *testing.T) {
			t.Parallel()
			raw := "Claro, aquí tienes:\n```json\n{\"version\":1,\"intent\":\"x\"}\n```\n"
			want, err := llm.ExtractJSON(raw)
			if err != nil {
				t.Fatalf("ExtractJSON de la salida de prueba: %v", err)
			}
			f := &fakeFrame{out: raw}
			got, err := sc.call(context.Background(), newProvider(t, f), llm.Options{})
			if err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s devolvió %q, quiero el JSON aislado %q", sc.name, got, want)
			}
		})
		t.Run(sc.name+": no JSON is a QUALITY error", func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: "lo siento, no puedo ayudarte con eso"}
			got, err := sc.call(context.Background(), newProvider(t, f), llm.Options{})
			if !errors.Is(err, llm.ErrLLMQuality) {
				t.Fatalf("quiero ErrLLMQuality, llegó: %v", err)
			}
			if got != nil {
				t.Fatalf("con error de calidad la salida debe ser nil, llegó %q", got)
			}
			if n := f.calls(); n != 1 {
				t.Fatalf("peticiones = %d: el adaptador no reintenta, eso es del llamante", n)
			}
		})
	}
}

// El error del cable vuelve INTACTO: ni envuelto ni traducido. Es lo que deja que el
// escritor de avisos encuentre el motivo por duck-typing más arriba; un %v en vez de %w
// rompería errors.As y el aviso se perdería en silencio.
func TestTransportErrorComesBackIntact(t *testing.T) {
	t.Parallel()
	for _, sc := range stageCalls() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			cause := &transportError{reason: edgegrpc.ReasonOllamaDown}
			f := &fakeFrame{out: validOutput, err: cause}
			got, err := sc.call(context.Background(), newProvider(t, f), llm.Options{})
			if err != error(cause) {
				t.Fatalf("el error del transporte no volvió tal cual: %v", err)
			}
			var withReason interface{ Motivo() string }
			if !errors.As(err, &withReason) {
				t.Fatalf("el motivo se perdió por el camino: %v", err)
			}
			if withReason.Motivo() != edgegrpc.ReasonOllamaDown {
				t.Fatalf("motivo = %q, quiero %q", withReason.Motivo(), edgegrpc.ReasonOllamaDown)
			}
			if got != nil {
				t.Fatalf("con error del transporte la salida debe ser nil, llegó %q", got)
			}
			if n := f.calls(); n != 1 {
				t.Fatalf("peticiones = %d: el adaptador no reintenta", n)
			}
		})
	}
}

// La temperatura es la del llamante: su reintento por calidad sube a 0,3 y, si el
// adaptador la ignorara, pediría exactamente lo mismo que la llamada que ya falló.
func TestCallerTemperatureReachesTheFrame(t *testing.T) {
	t.Parallel()
	for _, sc := range stageCalls() {
		for _, temp := range []float64{llm.TemperatureGreedy, llm.TemperatureRetry} {
			f := &fakeFrame{out: validOutput}
			if _, err := sc.call(context.Background(), newProvider(t, f), llm.Options{Temperature: temp}); err != nil {
				t.Fatalf("%s: %v", sc.name, err)
			}
			if got := f.last(t, 1).Temperature; got != temp {
				t.Errorf("%s: temperatura = %v, quiero %v", sc.name, got, temp)
			}
		}
	}
}

// El formato: json por defecto, el de WithFormat si se fija, y el vacío se ignora.
func TestFormat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []local.Option
		want string
	}{
		{"default", nil, local.DefaultFormat},
		{"explicit", []local.Option{local.WithFormat("yaml")}, "yaml"},
		{"empty is ignored", []local.Option{local.WithFormat("")}, local.DefaultFormat},
		{"empty does not erase a previous one", []local.Option{local.WithFormat("yaml"), local.WithFormat("")}, "yaml"},
		{"the last one wins", []local.Option{local.WithFormat("yaml"), local.WithFormat("xml")}, "xml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeFrame{out: validOutput}
			p := newProvider(t, f, tc.opts...)
			if _, err := p.ClassifyRequest(context.Background(), catalogInput(), llm.Options{}); err != nil {
				t.Fatalf("ClassifyRequest: %v", err)
			}
			if got := f.last(t, 1).Format; got != tc.want {
				t.Fatalf("format = %q, quiero %q", got, tc.want)
			}
		})
	}
}

// Las opciones se aplican en orden y la última gana, también en las dos que gobiernan el
// reloj y el techo (cuyo detalle está en local_budget_test.go).
func TestLastOptionWins(t *testing.T) {
	t.Parallel()
	f := &fakeFrame{out: validOutput}
	p := newProvider(t, f,
		local.WithTimeout(40*time.Second), local.WithTimeout(50*time.Second),
		local.WithMaxOutputTokens(false), local.WithMaxOutputTokens(true))
	if _, err := p.ClassifyRequest(context.Background(), catalogInput(), llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	req := f.last(t, 1)
	if req.Timeout != 50*time.Second {
		t.Errorf("timeout = %v, quiero 50s (el último WithTimeout)", req.Timeout)
	}
	if req.MaxOutputTokens != 192 {
		t.Errorf("max_output_tokens = %d, quiero 192 (el último WithMaxOutputTokens lo encendió)", req.MaxOutputTokens)
	}
}

// Origen y destino son campos distintos del frame y cada opción llena SOLO el suyo. Sin
// opciones viajan vacíos: una inferencia de alcance Edge es un estado legítimo.
func TestOriginAndTargetSessions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		opts               []local.Option
		wantOrig, wantDest string
	}{
		{"none", nil, "", ""},
		{"origin only", []local.Option{local.WithOriginSession("s-origin")}, "s-origin", ""},
		{"target only", []local.Option{local.WithTargetSession("s-target")}, "", "s-target"},
		{"both", []local.Option{local.WithOriginSession("s-origin"), local.WithTargetSession("s-target")}, "s-origin", "s-target"},
		{"an empty origin travels empty", []local.Option{local.WithOriginSession("")}, "", ""},
	} {
		for _, sc := range stageCalls() {
			t.Run(tc.name+"/"+sc.name, func(t *testing.T) {
				t.Parallel()
				f := &fakeFrame{out: validOutput}
				if _, err := sc.call(context.Background(), newProvider(t, f, tc.opts...), llm.Options{}); err != nil {
					t.Fatalf("%s: %v", sc.name, err)
				}
				req := f.last(t, 1)
				if req.OriginSessionID != tc.wantOrig {
					t.Errorf("OriginSessionID = %q, quiero %q", req.OriginSessionID, tc.wantOrig)
				}
				if req.TargetSessionID != tc.wantDest {
					t.Errorf("TargetSessionID = %q, quiero %q", req.TargetSessionID, tc.wantDest)
				}
			})
		}
	}
}

// C2 leído sobre este paquete: el adaptador no sabe que existe una vía «local» ni una
// «api». Si alguien metiera la vía en el frame «para trazabilidad», sería la primera
// piedra del `if via` que REQ-37 prohíbe.
func TestTheAdapterDoesNotNameTheVia(t *testing.T) {
	t.Parallel()
	f := &fakeFrame{out: validOutput}
	if _, err := newProvider(t, f).ClassifyRequest(context.Background(), catalogInput(), llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	req := f.last(t, 1)
	for _, field := range []string{req.Format, req.OriginSessionID, req.TargetSessionID, req.Class} {
		if strings.Contains(field, "via") || field == "local" || field == "api" {
			t.Fatalf("la petición del adaptador menciona la vía: %q", field)
		}
	}
}

// Un mismo Provider se comparte entre goroutines: no guarda estado de llamada. Con -race,
// un campo escrito durante una inferencia pondría este test en rojo.
func TestProviderIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	f := &fakeFrame{out: validOutput}
	p := newProvider(t, f, local.WithOriginSession("s-origin"))
	all := stageCalls()

	const rounds = 8
	var wg sync.WaitGroup
	errs := make(chan error, rounds*len(all))
	for range rounds {
		for _, sc := range all {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := sc.call(context.Background(), p, llm.Options{})
				errs <- err
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("una llamada concurrente falló: %v", err)
		}
	}
	if n := f.calls(); n != rounds*len(all) {
		t.Fatalf("peticiones = %d, quiero %d", n, rounds*len(all))
	}
}
