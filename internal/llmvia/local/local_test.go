package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia/local"
)

// frameFake registra lo que se le pidió y devuelve lo que se le diga.
type frameFake struct {
	vistos []gatewaygrpc.InferRequest
	out    string
	err    error
}

func (f *frameFake) Infer(_ context.Context, _ string, req gatewaygrpc.InferRequest) (string, error) {
	f.vistos = append(f.vistos, req)
	return f.out, f.err
}

func nuevoProvider(t *testing.T, f *frameFake, opts ...local.Option) llm.LLMProvider {
	t.Helper()
	p, err := local.New(f, "tenant-1", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// catálogo es un catálogo mínimo pero REAL: con ejemplos, porque el few-shot es lo
// que el prompt compartido usa y un catálogo pelado produciría un prompt legal y
// distinto del de producción.
func catálogo() llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text: "quiero 3 pizzas para el viernes",
		Catalog: []llm.IntentSpec{{
			Name:        "intake_request",
			Description: "el cliente pide productos",
			Examples:    []llm.IntentExample{{Message: "me mandas 2 gaseosas"}},
		}},
		UnknownLabel: "desconocido",
	}
}

// TestLosCincoMetodosUsanElPromptCOMPARTIDO es el test de C2 a nivel de contenido: el
// prompt que sale por el cable tiene que ser BYTE A BYTE el que construye
// wapp-shared/llm, no uno parecido.
//
// 🔴 POR QUÉ SE COMPARA CONTRA Build...Prompt Y NO CONTRA UN LITERAL. Un literal
// pegado aquí sería una segunda copia del prompt: el día que shared lo cambiara, este
// test se pondría rojo por decir la verdad y alguien lo «arreglaría» pegando el nuevo
// literal, con lo que la vía local se quedaría clavada en el prompt de ayer sin que
// nada avisara. Comparando contra la función, la ÚNICA forma de pasar es llamarla.
//
// 🔬 MUTACIÓN: escribir en cualquiera de los cinco métodos un prompt propio
// («"Clasifica: " + in.Text») ⇒ rojo, que es exactamente lo que C2 pide que pase.
func TestLosCincoMetodosUsanElPromptCOMPARTIDO(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ideas := llm.ExtractMainIdeasInput{SourceText: "hola, quiero dos cosas"}
	specs := llm.ExtractItemSpecsInput{SourceText: "hola", Idea: "pizza"}
	cant := llm.NormalizeQuantitiesInput{SourceText: "hola", MessageTS: time.Unix(1700000000, 0).UTC()}
	quote := llm.GenerateQuoteTextInput{Quote: json.RawMessage(`{"lines":[]}`)}

	for _, tc := range []struct {
		nombre string
		llamar func(p llm.LLMProvider) error
		quiero string
	}{
		{"ClassifyRequest", func(p llm.LLMProvider) error {
			_, err := p.ClassifyRequest(ctx, catálogo(), llm.Options{})
			return err
		}, llm.BuildClassifyRequestPrompt(catálogo())},
		{"ExtractMainIdeas", func(p llm.LLMProvider) error {
			_, err := p.ExtractMainIdeas(ctx, ideas, llm.Options{})
			return err
		}, llm.BuildExtractMainIdeasPrompt(ideas)},
		{"ExtractItemSpecs", func(p llm.LLMProvider) error {
			_, err := p.ExtractItemSpecs(ctx, specs, llm.Options{})
			return err
		}, llm.BuildExtractItemSpecsPrompt(specs)},
		{"NormalizeQuantities", func(p llm.LLMProvider) error {
			_, err := p.NormalizeQuantities(ctx, cant, llm.Options{})
			return err
		}, llm.BuildNormalizeQuantitiesPrompt(cant)},
		{"GenerateQuoteText", func(p llm.LLMProvider) error {
			_, err := p.GenerateQuoteText(ctx, quote, llm.Options{})
			return err
		}, llm.BuildGenerateQuoteTextPrompt(quote)},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			f := &frameFake{out: `{"version":1}`}
			if err := tc.llamar(nuevoProvider(t, f)); err != nil {
				t.Fatalf("%s: %v", tc.nombre, err)
			}
			if len(f.vistos) != 1 {
				t.Fatalf("inferencias pedidas = %d, quiero 1", len(f.vistos))
			}
			if f.vistos[0].Prompt != tc.quiero {
				t.Fatalf("%s no usó el prompt compartido.\n--- salió ---\n%s\n--- quiero ---\n%s",
					tc.nombre, f.vistos[0].Prompt, tc.quiero)
			}
		})
	}
}

// TestLaSalidaPasaPorExtractJSONCompartido: el contrato del puerto dice que quien
// llama recibe un objeto JSON o un error. La vía local no puede devolver el texto
// crudo del modelo tal cual, ni «arreglarlo» por su cuenta.
//
// Los dos casos son los mismos que ejercita la vía API, y a propósito: si las dos
// vías no tratan igual una salida sucia, el pipeline se comporta distinto según la
// vía y C2 deja de ser cierto.
func TestLaSalidaPasaPorExtractJSONCompartido(t *testing.T) {
	t.Parallel()
	t.Run("el modelo envuelve el JSON en texto y en fences", func(t *testing.T) {
		t.Parallel()
		f := &frameFake{out: "Claro, aquí tienes:\n```json\n{\"version\":1,\"intent\":\"x\"}\n```\n"}
		out, err := nuevoProvider(t, f).ClassifyRequest(context.Background(), catálogo(), llm.Options{})
		if err != nil {
			t.Fatalf("ClassifyRequest: %v", err)
		}
		var probe map[string]any
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("la salida no es JSON aislado: %q", out)
		}
	})
	t.Run("el modelo no devuelve JSON: es error de CALIDAD, no de infraestructura", func(t *testing.T) {
		t.Parallel()
		f := &frameFake{out: "lo siento, no puedo ayudarte con eso"}
		_, err := nuevoProvider(t, f).ClassifyRequest(context.Background(), catálogo(), llm.Options{})
		if !errors.Is(err, llm.ErrLLMQuality) {
			t.Fatalf("quiero ErrLLMQuality, llegó: %v", err)
		}
	})
}

// TestElErrorDelTransporteSePropagaINTACTO: el adaptador NO envuelve el error del
// cable. Es lo que permite que el escritor de avisos siga encontrando el motivo por
// duck-typing más arriba; un `fmt.Errorf("inferencia local: %v", err)` —con %v en vez
// de %w— rompería errors.As y el aviso se perdería en silencio.
//
// 🔬 MUTACIÓN: envolver el error de p.frame.Infer con %v ⇒ rojo aquí.
func TestElErrorDelTransporteSePropagaINTACTO(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("el cable falló")
	f := &frameFake{err: sentinel}
	_, err := nuevoProvider(t, f).ClassifyRequest(context.Background(), catálogo(), llm.Options{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("el error del transporte no llegó intacto: %v", err)
	}
}

// TestLaTemperaturaDelCallerLlegaAlFrame: el reintento por calidad del caller sube la
// temperatura a 0.3, y si el adaptador la ignorara ese reintento pediría exactamente
// lo mismo que la llamada que ya falló.
func TestLaTemperaturaDelCallerLlegaAlFrame(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: `{"version":1}`}
	p := nuevoProvider(t, f)
	if _, err := p.ClassifyRequest(context.Background(), catálogo(), llm.Options{Temperature: llm.TemperatureRetry}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	if f.vistos[0].Temperature != llm.TemperatureRetry {
		t.Fatalf("temperatura = %v, quiero %v", f.vistos[0].Temperature, llm.TemperatureRetry)
	}
}

// TestNewFallaAlCONSTRUIRSiLeFaltaLoImprescindible: mismo criterio que llm/api.New.
// Una configuración imposible se sabe al armarla, no a mitad de un pipeline.
func TestNewFallaAlCONSTRUIRSiLeFaltaLoImprescindible(t *testing.T) {
	t.Parallel()
	if _, err := local.New(nil, "t"); !errors.Is(err, local.ErrSinTransporte) {
		t.Fatalf("sin transporte quiero ErrSinTransporte, llegó: %v", err)
	}
	if _, err := local.New(&frameFake{}, ""); !errors.Is(err, local.ErrSinTenant) {
		t.Fatalf("sin tenant quiero ErrSinTenant, llegó: %v", err)
	}
}

// TestLosDefaultsSeMaterializan: ninguna inferencia sale sin formato ni sin reloj.
// Un timeout cero llegaría al frame como «usa tu default», que es un default en OTRA
// máquina y con otro criterio; el reloj de esta vía lo fija quien conoce su ventana.
func TestLosDefaultsSeMaterializan(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: `{"version":1}`}
	p := nuevoProvider(t, f, local.WithTimeout(0), local.WithFormat(""))
	if _, err := p.ClassifyRequest(context.Background(), catálogo(), llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	if f.vistos[0].Format != local.DefaultFormat {
		t.Fatalf("format = %q, quiero %q", f.vistos[0].Format, local.DefaultFormat)
	}
	if f.vistos[0].Timeout != local.DefaultTimeout {
		t.Fatalf("timeout = %v, quiero %v", f.vistos[0].Timeout, local.DefaultTimeout)
	}
}

// TestElAdaptadorNoNombraLaVia es C2 leído sobre este paquete: el adaptador local NO
// sabe que existe una vía llamada «local» ni una llamada «api». Quien lo sabe es
// llmvia, que es el único sitio del repo donde se pregunta.
//
// Se comprueba sobre el PROMPT y sobre la petición porque son lo único que sale de
// aquí: si algún día alguien metiera la vía en el frame «para trazabilidad», sería la
// primera piedra del `if via` que REQ-37 prohíbe.
func TestElAdaptadorNoNombraLaVia(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: `{"version":1}`}
	if _, err := nuevoProvider(t, f).ClassifyRequest(context.Background(), catálogo(), llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	req := f.vistos[0]
	for _, campo := range []string{req.Format, req.OriginSessionID} {
		if strings.Contains(campo, "via") {
			t.Fatalf("la petición del adaptador menciona la vía: %q", campo)
		}
	}
}
