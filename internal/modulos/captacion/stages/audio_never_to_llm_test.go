package stages_test

// audio_never_to_llm_test.go — EL CANDADO: el audio del cliente JAMÁS entra en un prompt
// (Plan 044 · T5.3, D-044.52). No tiene fichero de producción gemelo: cruza P2, P3 y P4.
//
// La separación es ESTRUCTURAL —el literal es lo ÚNICO del job que viaja a las etapas, y
// `SourceRefs` es un campo aparte que ninguna concatena—, pero «estructural» no es
// «imposible»: bastaría añadir un campo de refs a una entrada del puerto LLM y
// rellenarlo «para dar más contexto al modelo». Por eso hay DOS tests:
//
//   - el de CONDUCTA corre las tres etapas contra un provider espía y mira el PROMPT QUE
//     SE CONSTRUIRÍA —la forma que viaja por el cable, no la cómoda del struct—;
//   - el ESTRUCTURAL congela los campos de los tres tipos de entrada: un campo nuevo lo
//     pone rojo aunque nadie lo haya rellenado todavía.

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// ambarAudioRef es la referencia del audio que Ambar mandó en el hilo. Tiene la forma de
// una key de media y NO es una URL: la referencia es un identificador opaco.
const ambarAudioRef = "wapp/media/t-p2/2026/07/13/audio-0af3c9d1.ogg"

// ambarTextRef es la referencia de un mensaje de texto de la misma ventana.
const ambarTextRef = "wa-msg-9955-01"

// spiedCall es UNA petición al modelo, con las DOS formas: la entrada serializada y el
// PROMPT que esa entrada produce.
type spiedCall struct {
	stage  string
	fields string
	prompt string
}

// spy recuerda todo lo que se le pidió al modelo y construye el prompt REAL con los
// `Build…Prompt` del paquete compartido, los mismos que usa cualquier adaptador.
type spy struct {
	t     *testing.T
	calls []spiedCall
}

func (s *spy) note(stage string, in any, prompt string) {
	s.t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		s.t.Fatalf("el espía no pudo serializar la entrada de %s: %v", stage, err)
	}
	s.calls = append(s.calls, spiedCall{stage: stage, fields: string(raw), prompt: prompt})
}

// provider devuelve el llm.LLMProvider espiado, que contesta bien al caso Ambar.
func (s *spy) provider() *fakeProvider {
	return &fakeProvider{
		onMainIdeas: func(_ context.Context, in llm.ExtractMainIdeasInput, _ llm.Options) (json.RawMessage, error) {
			s.note(intake.StageP2, in, llm.BuildExtractMainIdeasPrompt(in))
			return ambarP2Output(), nil
		},
		onItemSpecs: func(_ context.Context, in llm.ExtractItemSpecsInput, _ llm.Options) (json.RawMessage, error) {
			s.note(intake.StageP3, in, llm.BuildExtractItemSpecsPrompt(in))
			return ambarSpecOutput(s.t, in.Idea), nil
		},
		onQuantities: func(_ context.Context, in llm.NormalizeQuantitiesInput, _ llm.Options) (json.RawMessage, error) {
			s.note(intake.StageP4, in, llm.BuildNormalizeQuantitiesPrompt(in))
			return json.RawMessage(ambarP4Output), nil
		},
	}
}

// TestAudio_NeverEntersAPrompt corre las TRES etapas que hablan con el modelo —P2, P3
// (una llamada por idea) y P4— con el MISMO espía, sobre un job cuyas `source_refs`
// llevan de verdad la referencia del audio.
func TestAudio_NeverEntersAPrompt(t *testing.T) {
	job := ambarP4Job()
	job.SourceRefs = []string{ambarTextRef, ambarAudioRef}

	// (0) EL FIXTURE LLEVA EL AUDIO. Sin esto el resto pasaría también con un job sin
	// adjuntos y no probaría nada.
	if !slices.Contains(job.SourceRefs, ambarAudioRef) {
		t.Fatalf("el fixture no lleva la ref del audio en source_refs (%v): el test sería VACUO", job.SourceRefs)
	}

	watcher := &spy{t: t}
	store := &fakeStore{}
	runTheThreeLLMStages(t, job, &fakeSelector{provider: watcher.provider()}, store)

	// (1) HUBO LLAMADAS DE VERDAD: 1 de P2 + 3 de P3 (una por idea) + 1 de P4. Un espía
	// con cero llamadas pasaría cualquier aserción de ausencia.
	if len(watcher.calls) != 5 {
		t.Fatalf("el espía recogió %d llamadas; se esperaban 5 (P2 + una P3 por idea + P4)", len(watcher.calls))
	}

	// (2) EL ESPÍA SÍ VE EL CONTENIDO: el literal del cliente llega en TODAS, en las dos
	// formas.
	for _, call := range watcher.calls {
		if !strings.Contains(call.fields, "tequeños congelados") || !strings.Contains(call.prompt, "tequeños congelados") {
			t.Fatalf("la llamada de %s no contiene el literal del cliente: el espía no ve lo que viaja", call.stage)
		}
	}

	// (3) LA AUSENCIA, sobre las dos formas: la entrada y el prompt.
	forbidden := []string{
		ambarAudioRef,      // la ref completa
		"audio-0af3c9d1",   // el nombre del objeto suelto
		"wapp/media",       // el prefijo de la key de media
		".ogg",             // la extensión
		anclaje.AudioLabel, // el rótulo que el dueño ve en la bandeja
		anclaje.KindAudio,  // "audio"
		anclaje.KindPTT,    // "ptt", la nota de voz de WhatsApp
		ambarTextRef,       // y de paso: NINGUNA source_ref viaja al modelo
	}
	for _, call := range watcher.calls {
		for _, bad := range forbidden {
			if strings.Contains(call.fields, bad) {
				t.Errorf("la llamada de %s lleva %q en su ENTRADA: %s", call.stage, bad, call.fields)
			}
			if strings.Contains(call.prompt, bad) {
				t.Errorf("el PROMPT de %s contiene %q", call.stage, bad)
			}
		}
	}

	// (4) Y tampoco acaba en lo que las etapas persisten.
	for _, artifact := range store.saved {
		if strings.Contains(string(artifact.Payload), ambarAudioRef) {
			t.Errorf("el artefacto de %s lleva la ref del audio: %s", artifact.Stage, artifact.Payload)
		}
	}
}

// runTheThreeLLMStages encadena P2 → P3 → P4 sobre el job, como lo hace el worker: cada
// etapa recibe lo que dejó la anterior.
func runTheThreeLLMStages(t *testing.T, job intake.ClaimedJob, selector stages.ProviderSelector, store stages.StageStore) {
	t.Helper()
	log := captureLog(&bytes.Buffer{})
	ctx := context.Background()

	p2, err := stages.NewP2(log, selector, store)
	if err != nil {
		t.Fatalf("NewP2: %v", err)
	}
	ideas, err := p2.Run(ctx, job, ambarText)
	if err != nil {
		t.Fatalf("P2.Run: %v", err)
	}

	p3, err := stages.NewP3(log, selector, store)
	if err != nil {
		t.Fatalf("NewP3: %v", err)
	}
	specs, err := p3.Run(ctx, job, ambarText, ideas.Wants)
	if err != nil {
		t.Fatalf("P3.Run: %v", err)
	}

	p4, err := stages.NewP4(log, selector, store, stages.DefaultZone)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}
	if _, err := p4.Run(ctx, job, ambarText, specs.Items, ideas.DeliveryHint); err != nil {
		t.Fatalf("P4.Run: %v", err)
	}
}

// TestLLMInputs_HaveNowhereToPutAnAttachment congela los campos de los tres tipos de
// entrada del puerto LLM. El test de conducta solo puede afirmar sobre lo que HOY se
// rellena; éste se pone rojo EN EL COMMIT QUE ABRE EL HUECO, que es cuando la decisión
// todavía se puede tomar. No es una prohibición eterna: es una PUERTA CON TIMBRE.
func TestLLMInputs_HaveNowhereToPutAnAttachment(t *testing.T) {
	cases := []struct {
		input any
		want  []string
	}{
		{llm.ExtractMainIdeasInput{}, []string{"SourceText"}},
		{llm.ExtractItemSpecsInput{}, []string{"SourceText", "Idea"}},
		{llm.NormalizeQuantitiesInput{}, []string{"SourceText", "Items", "MessageTS"}},
	}
	for _, c := range cases {
		typ := reflect.TypeOf(c.input)
		t.Run(typ.Name(), func(t *testing.T) {
			got := make([]string, 0, typ.NumField())
			for i := range typ.NumField() {
				got = append(got, typ.Field(i).Name)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s tiene los campos %v; se esperaban %v.\n"+
					"Si el campo nuevo transporta un adjunto —o cualquier cosa derivada de "+
					"`source_refs`—, el audio acaba de dejar de estar fuera del prompt.",
					typ.Name(), got, c.want)
			}
		})
	}
}
