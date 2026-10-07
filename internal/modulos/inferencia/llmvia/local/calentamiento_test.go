//go:build pendiente

package local_test

// El calentamiento. Los dobles (fakeFrame, transportError, catalogInput, newProvider) viven
// en local_test.go, y assertInherited en local_budget_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
)

// truncatedOutput es lo que un calentamiento recibe casi siempre: con 16 tokens de techo
// el JSON no cierra.
const truncatedOutput = `{"vers`

// prefixOf devuelve el prompt menos su última línea, que es donde
// BuildClassifyRequestPrompt pone el mensaje del cliente. Es lo que Ollama cachea.
func prefixOf(prompt string) string {
	i := strings.LastIndexByte(prompt, '\n')
	if i < 0 {
		return prompt
	}
	return prompt[:i]
}

// tailOf recorta a los últimos 200 bytes para que un fallo se lea.
func tailOf(s string) string {
	if len(s) <= 200 {
		return s
	}
	return "…" + s[len(s)-200:]
}

func TestWarmupTextIsHola(t *testing.T) {
	t.Parallel()
	if local.TextoDeCalentamiento != "hola" {
		t.Fatalf("TextoDeCalentamiento = %q, quiero \"hola\"", local.TextoDeCalentamiento)
	}
}

// EL test del calentamiento, y el único que puede fallar sin que nada más lo note: la caché
// de Ollama es por prefijo LITERAL, así que un byte de diferencia y el prefill vuelve a ser
// frío — sin error, sin log; la latencia simplemente no mejora. La única defensa es
// comparar los dos prompts.
func TestWarmCachesTheSamePrefixAsTheRealP1(t *testing.T) {
	t.Parallel()
	in := catalogInput()

	realFrame := &fakeFrame{out: validOutput}
	if _, err := newProvider(t, realFrame).ClassifyRequest(context.Background(), in, llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	warmFrame := &fakeFrame{out: truncatedOutput}
	if err := newProvider(t, warmFrame, local.WithTargetSession("s-1")).Warm(context.Background(), in); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	realPrompt, warmPrompt := realFrame.last(t, 1).Prompt, warmFrame.last(t, 1).Prompt
	if a, b := prefixOf(realPrompt), prefixOf(warmPrompt); a != b {
		t.Fatalf("el calentamiento cachearía un prefijo DISTINTO del que pedirá la P1 real.\n"+
			"--- real (%d B) ---\n%s\n--- calentamiento (%d B) ---\n%s", len(a), tailOf(a), len(b), tailOf(b))
	}

	// El prompt entero es el compartido con el texto trivial: ni un prompt propio aquí.
	trivial := in
	trivial.Text = local.TextoDeCalentamiento
	if want := llm.BuildClassifyRequestPrompt(trivial); warmPrompt != want {
		t.Fatalf("el prompt del calentamiento no es el compartido con %q al final.\n--- salió ---\n%s",
			local.TextoDeCalentamiento, tailOf(warmPrompt))
	}
	if !strings.HasSuffix(warmPrompt, local.TextoDeCalentamiento) {
		t.Errorf("el calentamiento no terminó en el mensaje trivial. Prompt acaba en %q", tailOf(warmPrompt))
	}
	if strings.Contains(warmPrompt, in.Text) {
		t.Errorf("el texto del llamante (%q) llegó al modelo: tenía que ignorarse", in.Text)
	}
}

// Los datos que el frame de un calentamiento tiene que llevar, que son los que hacen que
// el Edge lo trate como tal. Sin `warmup`, el breaker contaría como LENTITUD un prefill
// frío que es lento por diseño: abrir el circuito por haber trabajado bien.
func TestWarmTravelsMarkedAndBounded(t *testing.T) {
	t.Parallel()
	f := &fakeFrame{out: truncatedOutput}
	p := newProvider(t, f, local.WithTargetSession("s-target"), local.WithOriginSession("s-origin"),
		local.WithFormat("yaml"))
	if err := p.Warm(context.Background(), catalogInput()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	req := f.last(t, 1)
	if f.tenants[0] != "tenant-1" {
		t.Errorf("el calentamiento preguntó al tenant %q, quiero tenant-1", f.tenants[0])
	}
	if !req.Warmup {
		t.Error("el frame salió con warmup=false: el Edge lo contaría en el breaker (T1.7-4)")
	}
	if req.MaxOutputTokens != 16 {
		t.Errorf("max_output_tokens = %d, quiero 16: del calentamiento solo interesa el prefill", req.MaxOutputTokens)
	}
	if req.Class != edgegrpc.ClassBatch {
		t.Errorf("class = %q, quiero %q: nadie espera un turno detrás de un calentamiento", req.Class, edgegrpc.ClassBatch)
	}
	if req.TargetSessionID != "s-target" {
		t.Errorf("TargetSessionID = %q, quiero s-target: tiene que salir por el Edge cuya caché se quiere llenar",
			req.TargetSessionID)
	}
	if req.OriginSessionID != "" {
		t.Errorf("OriginSessionID = %q: un calentamiento no lo originó ninguna conversación y ese campo "+
			"pondría un dato de trazabilidad FALSO en el cable", req.OriginSessionID)
	}
	if req.Format != "yaml" {
		t.Errorf("format = %q, quiero el del Provider (yaml)", req.Format)
	}
	if req.Temperature != llm.TemperatureGreedy {
		t.Errorf("temperatura = %v, quiero %v, la de una P1 real", req.Temperature, llm.TemperatureGreedy)
	}
}

// Con 16 tokens el JSON viene truncado casi siempre, y eso es CORRECTO. Si Warm pasara la
// salida por ExtractJSON devolvería ErrLLMQuality en cada calentamiento y quien lo llame
// acabaría reintentando —o avisando— por el desenlace previsto.
func TestWarmDoesNotInterpretTheOutput(t *testing.T) {
	t.Parallel()
	for _, out := range []string{truncatedOutput, "no soy JSON ni lo pretendo", "", validOutput} {
		f := &fakeFrame{out: out}
		if err := newProvider(t, f).Warm(context.Background(), catalogInput()); err != nil {
			t.Fatalf("Warm devolvió error con la salida %q (%v): la salida de un calentamiento SE DESCARTA", out, err)
		}
		if n := f.calls(); n != 1 {
			t.Fatalf("peticiones = %d, quiero exactamente 1 calentamiento", n)
		}
	}
}

// El error del cable vuelve tal cual, para que quien llame lo loguee con su motivo.
func TestWarmReturnsTheTransportErrorIntact(t *testing.T) {
	t.Parallel()
	cause := &transportError{reason: edgegrpc.ReasonEdgeOffline}
	f := &fakeFrame{err: cause}
	err := newProvider(t, f).Warm(context.Background(), catalogInput())
	if err != error(cause) {
		t.Fatalf("el error del transporte no volvió tal cual: %v", err)
	}
	var withReason interface{ Motivo() string }
	if !errors.As(err, &withReason) || withReason.Motivo() != edgegrpc.ReasonEdgeOffline {
		t.Fatalf("el motivo se perdió por el camino: %v", err)
	}
	if n := f.calls(); n != 1 {
		t.Fatalf("peticiones = %d: el calentamiento no reintenta", n)
	}
}

// El calentamiento hereda el plazo con las mismas tres reglas que una inferencia real.
func TestWarmInheritsTheDeadline(t *testing.T) {
	t.Parallel()

	t.Run("with a deadline: what is left minus the margin", func(t *testing.T) {
		t.Parallel()
		const budget = 90 * time.Second
		f := &fakeFrame{out: truncatedOutput}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		if err := newProvider(t, f).Warm(ctx, catalogInput()); err != nil {
			t.Fatalf("Warm: %v", err)
		}
		assertInherited(t, f.last(t, 1).Timeout, budget)
	})

	t.Run("without a deadline: the safety net", func(t *testing.T) {
		t.Parallel()
		f := &fakeFrame{out: truncatedOutput}
		if err := newProvider(t, f).Warm(context.Background(), catalogInput()); err != nil {
			t.Fatalf("Warm: %v", err)
		}
		if got := f.last(t, 1).Timeout; got != local.DefaultTimeout {
			t.Fatalf("timeout del frame = %v, quiero DefaultTimeout (%v)", got, local.DefaultTimeout)
		}
	})

	t.Run("without budget: ErrSinPresupuesto and the frame untouched", func(t *testing.T) {
		t.Parallel()
		f := &fakeFrame{out: truncatedOutput}
		ctx, cancel := context.WithTimeout(context.Background(), local.MargenVeredicto/2)
		defer cancel()
		err := newProvider(t, f).Warm(ctx, catalogInput())
		if !errors.Is(err, local.ErrSinPresupuesto) {
			t.Fatalf("quiero ErrSinPresupuesto, llegó: %v", err)
		}
		if n := f.calls(); n != 0 {
			t.Fatalf("se mandaron %d calentamiento(s) sin plazo para esperarlos", n)
		}
	})
}

// El interruptor es sobre «el Cloud fija el presupuesto de salida», y eso incluye el
// calentamiento. Si conservara su 16 con el interruptor apagado, el interruptor
// significaría dos cosas según el camino y su lectura en el A/B dejaría de ser una.
func TestSwitchingTheBudgetOffAlsoUncapsWarm(t *testing.T) {
	t.Parallel()
	f := &fakeFrame{out: truncatedOutput}
	if err := newProvider(t, f, local.WithMaxOutputTokens(false)).Warm(context.Background(), catalogInput()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	req := f.last(t, 1)
	if req.MaxOutputTokens != 0 {
		t.Fatalf("el calentamiento fijó %d tokens con el interruptor APAGADO", req.MaxOutputTokens)
	}
	if !req.Warmup {
		t.Error("apagar el techo no puede apagar la marca de calentamiento")
	}
}
