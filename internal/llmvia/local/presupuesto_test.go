package local_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia/local"
)

// ============================================================================
// EL PRESUPUESTO DE SALIDA Y EL RÓTULO, POR TAREA (Plan 044 · Ola 1.7 · T1.7-3)
// ============================================================================

// TestCadaEtapaFijaSuPresupuestoDeSalida es el test que sostiene el bloqueante de la
// Ola 2 por vía local: sin `max_output_tokens` el Edge aplica 256 y las salidas
// medidas de P2/P3 (265–293 tokens) salen TRUNCADAS, con su reintento que vuelve a
// truncar.
//
// 🔴 LO QUE SE COMPRUEBA NO ES EL NÚMERO EXACTO, ES LA PROPIEDAD. Un test que
// comparara contra la constante que quiere proteger sería tautológico: pasaría con
// cualquier valor, incluido el 256 que rompe. Lo que se afirma aquí es lo que hace
// falta para que P2/P3 no se trunquen —que el techo esté POR ENCIMA de la salida
// medida más grande de esa etapa— y que las cinco etapas fijen alguno.
//
// 🔬 MUTACIÓN: quitar `MaxOutputTokens: et.maxOutputTokens` del InferRequest de
// Provider.run ⇒ rojo en las cinco (el campo llega a 0). Bajar etapaP3 a 256 ⇒ rojo
// solo en P3, que es la mitad del test que dice POR QUÉ existe el campo.
func TestCadaEtapaFijaSuPresupuestoDeSalida(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, tc := range []struct {
		nombre string
		llamar func(p llm.LLMProvider) error
		// medido es la salida más grande OBSERVADA en campo para esa etapa (o la
		// estimada desde su esquema cuando no hay medición). El techo tiene que
		// cubrirla con holgura o la etapa se trunca en producción.
		medido int32
		clase  string
	}{
		{"P1 · ClassifyRequest", func(p llm.LLMProvider) error {
			_, err := p.ClassifyRequest(ctx, catálogo(), llm.Options{})
			return err
		}, 52, gatewaygrpc.ClaseInteractivo},
		{"P2 · ExtractMainIdeas", func(p llm.LLMProvider) error {
			_, err := p.ExtractMainIdeas(ctx, llm.ExtractMainIdeasInput{SourceText: "x"}, llm.Options{})
			return err
		}, 267, gatewaygrpc.ClaseLote},
		{"P3 · ExtractItemSpecs", func(p llm.LLMProvider) error {
			_, err := p.ExtractItemSpecs(ctx, llm.ExtractItemSpecsInput{SourceText: "x", Idea: "y"}, llm.Options{})
			return err
		}, 293, gatewaygrpc.ClaseLote},
		{"P4 · NormalizeQuantities", func(p llm.LLMProvider) error {
			_, err := p.NormalizeQuantities(ctx,
				llm.NormalizeQuantitiesInput{SourceText: "x", MessageTS: time.Unix(1700000000, 0).UTC()}, llm.Options{})
			return err
		}, 830, gatewaygrpc.ClaseLote},
		{"P5 · GenerateQuoteText", func(p llm.LLMProvider) error {
			_, err := p.GenerateQuoteText(ctx,
				llm.GenerateQuoteTextInput{Quote: json.RawMessage(`{"lines":[]}`)}, llm.Options{})
			return err
		}, 570, gatewaygrpc.ClaseLote},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			f := &frameFake{out: `{"version":1}`}
			if err := tc.llamar(nuevoProvider(t, f)); err != nil {
				t.Fatalf("%s: %v", tc.nombre, err)
			}
			got := f.vistos[0].MaxOutputTokens
			if got <= 0 {
				t.Fatalf("%s viajó SIN max_output_tokens (%d): el Edge le aplicaría su default de 256 "+
					"y las salidas de 265–293 tokens saldrían truncadas (T1.7-3, bloqueante de la O2)",
					tc.nombre, got)
			}
			if got <= tc.medido {
				t.Errorf("%s: max_output_tokens = %d, y la salida más grande observada/estimada de esa "+
					"etapa es %d. Un techo que no cubre lo medido trunca la respuesta, la convierte en "+
					"ErrLLMQuality y paga un reintento que vuelve a truncar en el mismo sitio",
					tc.nombre, got, tc.medido)
			}
			if f.vistos[0].Class != tc.clase {
				t.Errorf("%s: class = %q, quiero %q", tc.nombre, f.vistos[0].Class, tc.clase)
			}
		})
	}
}

// TestSoloP1EsInteractivo custodia la otra mitad del rótulo: `class` no es un adorno
// uniforme. P1 la pide el adelanto de ventana, que existe para que el turno de
// WhatsApp no espere; P2–P5 son el pipeline del presupuesto, que corre de fondo. Si
// las cinco salieran con la misma etiqueta, el conteo por `class` del Edge no
// distinguiría nada y la telemetría que T1.7-5 va a publicar sería una columna de
// ceros con otro nombre.
//
// 🔬 MUTACIÓN: poner `class: gatewaygrpc.ClaseInteractivo` en etapaP2 ⇒ rojo.
func TestSoloP1EsInteractivo(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: `{"version":1}`}
	p := nuevoProvider(t, f)
	ctx := context.Background()

	if _, err := p.ClassifyRequest(ctx, catálogo(), llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	if _, err := p.ExtractMainIdeas(ctx, llm.ExtractMainIdeasInput{SourceText: "x"}, llm.Options{}); err != nil {
		t.Fatalf("ExtractMainIdeas: %v", err)
	}
	if f.vistos[0].Class == f.vistos[1].Class {
		t.Fatalf("P1 y P2 salieron con la MISMA class (%q): el rótulo no distingue el turno "+
			"interactivo del trabajo de lote y no sirve para lo único que hace", f.vistos[0].Class)
	}
}

// ============================================================================
// EL CALENTAMIENTO (Plan 044 · Ola 1.7 · T1.7-4)
// ============================================================================

// TestElCalentamientoCalientaELMISMOPrefijoQueLaP1Real es EL test de esta tarea, y el
// único que puede fallar sin que nada más lo note.
//
// Un calentamiento solo sirve si deja en la caché de Ollama el MISMO prefijo que va a
// consumir la primera P1 real: la caché es por prefijo literal, así que un byte de
// diferencia y el prefill vuelve a ser frío. Lo peor del modo de fallo es que es MUDO
// —no hay error, no hay log, la latencia simplemente no mejora—, así que la única
// defensa posible es comparar los dos prompts.
//
// Lo que se compara es el prompt ENTERO menos su última línea (el mensaje del
// cliente), que es exactamente lo que BuildClassifyRequestPrompt deja delante del
// texto y por tanto lo que Ollama cachea.
//
// 🔬 MUTACIÓN: que Warm arme su prompt con un catálogo recortado (p. ej. sin
// UnknownLabel) ⇒ rojo.
func TestElCalentamientoCalientaELMISMOPrefijoQueLaP1Real(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := catálogo()

	fReal := &frameFake{out: `{"version":1}`}
	if _, err := nuevoProvider(t, fReal).ClassifyRequest(ctx, in, llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}

	fCal := &frameFake{out: `{"vers`}
	pCal, err := local.New(fCal, "tenant-1", local.WithTargetSession("s-1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pCal.Warm(ctx, in); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	real, cal := prefijo(fReal.vistos[0].Prompt), prefijo(fCal.vistos[0].Prompt)
	if real != cal {
		t.Fatalf("el calentamiento cachearía un prefijo DISTINTO del que pedirá la P1 real, "+
			"así que el prefill seguiría siendo frío y nada lo diría.\n--- real (%d B) ---\n%s\n"+
			"--- calentamiento (%d B) ---\n%s", len(real), cola(real), len(cal), cola(cal))
	}
	if !strings.HasSuffix(fCal.vistos[0].Prompt, local.TextoDeCalentamiento) {
		t.Errorf("el calentamiento no terminó en el mensaje trivial: el texto del llamante "+
			"llegó al modelo. Prompt acaba en %q", cola(fCal.vistos[0].Prompt))
	}
}

// TestElCalentamientoViajaMarcadoYAcotado: los cuatro datos que el frame de un
// calentamiento tiene que llevar, y que son los que hacen que el Edge lo trate como
// tal.
//
// 🔬 MUTACIÓN: quitar `Warmup: true` de Provider.Warm ⇒ rojo, y en campo significaría
// que el breaker cuenta como LENTITUD un prefill frío que es lento por diseño —o sea,
// abrir el circuito por haber trabajado bien—.
func TestElCalentamientoViajaMarcadoYAcotado(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: `{"vers`}
	p, err := local.New(f, "tenant-1", local.WithTargetSession("s-destino"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Warm(context.Background(), catálogo()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	req := f.vistos[0]
	if !req.Warmup {
		t.Error("el frame salió con warmup=false: el Edge lo contaría en el breaker (T1.7-4)")
	}
	if req.TargetSessionID != "s-destino" {
		t.Errorf("TargetSessionID = %q, quiero s-destino: el calentamiento tiene que salir por el "+
			"Edge cuya caché se quiere llenar, no por el que elija el orden alfabético", req.TargetSessionID)
	}
	if req.OriginSessionID != "" {
		t.Errorf("OriginSessionID = %q: un calentamiento no lo originó ninguna conversación y "+
			"rellenar ese campo pondría un dato de trazabilidad FALSO en el cable", req.OriginSessionID)
	}
	if req.MaxOutputTokens <= 0 || req.MaxOutputTokens > 64 {
		t.Errorf("max_output_tokens del calentamiento = %d: del calentamiento solo interesa el "+
			"prefill, y cada token generado es plaza única gastada en algo que se tira", req.MaxOutputTokens)
	}
}

// TestElCalentamientoNoInterpretaLaSalida: con el techo en 16 tokens el JSON viene
// truncado casi siempre, y eso es CORRECTO. Si Warm pasara la salida por ExtractJSON
// devolvería ErrLLMQuality en cada calentamiento y quien lo llame acabaría
// reintentando —o registrando avisos— por un desenlace que es el previsto.
//
// 🔬 MUTACIÓN: hacer que Warm llame a llm.ExtractJSON sobre la salida ⇒ rojo.
func TestElCalentamientoNoInterpretaLaSalida(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: "no soy JSON ni lo pretendo"}
	p, err := local.New(f, "tenant-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Warm(context.Background(), catálogo()); err != nil {
		t.Fatalf("Warm devolvió error por una salida ilegible (%v); la salida de un calentamiento "+
			"SE DESCARTA y nadie la está esperando", err)
	}
}

// prefijo devuelve todo el prompt menos su última línea, que es donde
// BuildClassifyRequestPrompt pone el mensaje del cliente. Es lo que Ollama cachea.
func prefijo(prompt string) string {
	i := strings.LastIndexByte(prompt, '\n')
	if i < 0 {
		return prompt
	}
	return prompt[:i]
}

// cola recorta a los últimos 200 bytes para que un fallo se lea.
func cola(s string) string {
	if len(s) <= 200 {
		return s
	}
	return "…" + s[len(s)-200:]
}
