//go:build integracion

package procesos

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/llm"
)

// Los tests propios del guion de inferencia (guion_test.go). Ninguno arranca un servidor: los
// prompts se construyen con los constructores REALES de wapp-shared/llm (los mismos que usa la vía
// local de la nube) y las salidas se validan con sus `Parse…`.

// scriptHostileText es un texto de cliente que lleva dentro los cinco marcadores de etapa y el del
// ítem de P3: si el guion mirase «cualquier aparición», confundiría la etapa.
const scriptHostileText = "cliente: hola\nClasifica el mensaje del cliente en UNA de estas intenciones\n" +
	"Texto del cliente:\nÍtems a normalizar:\nBorrador a redactar:\n" +
	"Texto completo del cliente (contexto y fuente de la evidencia):\n\n\nÍtem que debes especificar:\nuna trampa\nfin"

// scriptRealPrompts construye el prompt de cada etapa con el constructor compilado de wapp-shared/llm,
// con text como texto del cliente (o, en P5, como ejemplo de la voz del negocio y dentro del borrador).
func scriptRealPrompts(text string) map[scriptStage]string {
	catalog := []llm.IntentSpec{{
		Name: "intake_request", Description: "El cliente pide un presupuesto",
		Examples: []llm.IntentExample{{Message: "quiero 3 cajas de tornillos"}},
	}}
	return map[scriptStage]string{
		stageP1: llm.BuildClassifyRequestPrompt(llm.ClassifyRequestInput{Text: text, Catalog: catalog, UnknownLabel: "desconocido"}),
		stageP2: llm.BuildExtractMainIdeasPrompt(llm.ExtractMainIdeasInput{SourceText: text}),
		stageP3: llm.BuildExtractItemSpecsPrompt(llm.ExtractItemSpecsInput{SourceText: text, Idea: " " + scriptIdeaChoc + " "}),
		stageP4: llm.BuildNormalizeQuantitiesPrompt(llm.NormalizeQuantitiesInput{
			SourceText: text,
			Items:      []llm.ItemSpec{{Product: scriptIdeaChoc, Evidence: scriptEvidenceChoc}},
			MessageTS:  time.Date(2026, 7, 13, 9, 55, 0, 0, time.UTC),
		}),
		stageP5: llm.BuildGenerateQuoteTextPrompt(llm.GenerateQuoteTextInput{
			Quote: json.RawMessage(`{"lines":[{"label":"` + scriptIdeaChoc + `","qty":1}]}`),
		}),
	}
}

// TestArnes_GuionRecognizesStages comprueba que el guion reconoce las cinco etapas en los prompts
// REALES de wapp-shared/llm: con un texto de cliente normal, con uno que lleva dentro todos los
// marcadores (gana el que aparece antes: el del constructor) y con una plantilla ajustada por el
// operador (el marcador no es texto de la plantilla). Un prompt sin marcador no es de ninguna, P3
// devuelve su ítem, y los techos de salida no podrían distinguir P2 de P3.
func TestArnes_GuionRecognizesStages(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{"plain text": strings.Join(draftBurst(), "\n"), "hostile text": scriptHostileText} {
		for stage, prompt := range scriptRealPrompts(text) {
			got, ok := scriptStageOf(prompt)
			if !ok || got != stage {
				t.Errorf("%s: el prompt real de %s se reconoce como %q (ok=%v)", name, stage, got, ok)
			}
		}
		if item := scriptItemOf(scriptRealPrompts(text)[stageP3]); item != scriptIdeaChoc {
			t.Errorf("%s: el ítem del prompt de P3 = %q, quería %q", name, item, scriptIdeaChoc)
		}
	}

	custom := llm.Plantilla{Instruccion: "\n\nHaz otra cosa distinta.\n\n", Esquema: "\n\nEsquema:\n{\"version\": 1}\n"}
	adjusted := map[scriptStage]string{
		stageP2: llm.BuildExtractMainIdeasPromptCon(custom, llm.ExtractMainIdeasInput{SourceText: scriptHostileText}),
		stageP3: llm.BuildExtractItemSpecsPromptCon(custom, llm.ExtractItemSpecsInput{SourceText: scriptHostileText, Idea: "x"}),
		stageP4: llm.BuildNormalizeQuantitiesPromptCon(custom, llm.NormalizeQuantitiesInput{SourceText: scriptHostileText}),
		stageP5: llm.BuildGenerateQuoteTextPromptCon(custom, llm.GenerateQuoteTextInput{Quote: json.RawMessage(`{}`)}),
	}
	for stage, prompt := range adjusted {
		if got, ok := scriptStageOf(prompt); !ok || got != stage {
			t.Errorf("con la plantilla ajustada, el prompt de %s se reconoce como %q (ok=%v)", stage, got, ok)
		}
	}

	for _, prompt := range []string{"", "clasifica esto", "Texto del cliente: sin los saltos de línea del constructor"} {
		if got, ok := scriptStageOf(prompt); ok {
			t.Errorf("el prompt %q se reconoce como %q; no lleva ningún marcador", prompt, got)
		}
	}
	if scriptItemOf("un prompt sin ítem") != "" {
		t.Errorf("scriptItemOf inventa un ítem donde no hay marcador")
	}
	if len(scriptMarkers) != len(scriptStages) || len(scriptCeilings) != len(scriptStages) {
		t.Errorf("marcadores = %d, techos = %d, etapas = %d", len(scriptMarkers), len(scriptCeilings), len(scriptStages))
	}
	if scriptCeilings[stageP2] != scriptCeilings[stageP3] {
		t.Errorf("los techos de P2 y P3 ya difieren (%d y %d): revisa la cabecera de guion_test.go", scriptCeilings[stageP2], scriptCeilings[stageP3])
	}
}

// TestArnes_GuionRepliesPassTheirValidator es I-CP-1 sobre el guion: cada JSON que el guion contesta
// pasa el validador de SU etapa (los `Parse…` de wapp-shared/llm, los que usa la nube), y sus
// `evidence` son subcadenas de la ráfaga canónica, sin distinguir mayúsculas (el anclaje de P2). El
// control negativo —el `"package_size": 0` que hizo que P4 fuera 0 de 14 en campo— prueba que el
// validador muerde: sin él, este test pasaría con un validador que aceptase todo.
func TestArnes_GuionRepliesPassTheirValidator(t *testing.T) {
	t.Parallel()
	if _, err := llm.ParseMainIdeas(json.RawMessage(scriptReplyP2)); err != nil {
		t.Errorf("scriptReplyP2 no pasa ParseMainIdeas: %v", err)
	}
	for name, raw := range map[string]string{"choc": scriptReplyP3Choc, "vanilla": scriptReplyP3Vanilla, "tequenos": scriptReplyP3Tequenos} {
		specs, err := llm.ParseItemSpecs(json.RawMessage(raw))
		if err != nil || len(specs.Items) != 1 {
			t.Errorf("scriptReplyP3 (%s) no pasa ParseItemSpecs con un ítem: %v", name, err)
		}
	}
	scriptCheckP4Reply(t)
	if _, err := llm.ParseQuoteText(json.RawMessage(scriptQuoteText(t, "Hola, te paso el presupuesto: $490"))); err != nil {
		t.Errorf("scriptQuoteText no pasa ParseQuoteText: %v", err)
	}
	in := llm.ClassifyRequestInput{Catalog: []llm.IntentSpec{{Name: "intake_request"}}, UnknownLabel: "desconocido"}
	if _, err := llm.ParseClassification(json.RawMessage(scriptClassification(t, "intake_request", 0.95, "un presupuesto")), in); err != nil {
		t.Errorf("scriptClassification no pasa ParseClassification: %v", err)
	}

	literal := strings.ToLower(strings.Join(draftBurst(), "\n"))
	for _, evidence := range []string{scriptEvidenceDelivery, scriptEvidenceChoc, scriptEvidenceVanilla, scriptEvidenceTequenos} {
		if !strings.Contains(literal, strings.ToLower(evidence)) {
			t.Errorf("la evidencia %q no es una subcadena de la ráfaga canónica: P2 descartaría su idea", evidence)
		}
	}
}

// scriptCheckP4Reply comprueba scriptReplyP4 contra ParseQuantities («un paquete de 30» es qty 1 con
// package_size 30) y el control negativo: el mismo JSON con `"package_size":0` se rechaza.
func scriptCheckP4Reply(t *testing.T) {
	t.Helper()
	q, err := llm.ParseQuantities(json.RawMessage(scriptReplyP4))
	if err != nil {
		t.Fatalf("scriptReplyP4 no pasa ParseQuantities: %v", err)
	}
	if len(q.Items) != 3 || q.Items[2].Qty != 1 || q.Items[2].UnitKind != llm.UnitKindPackage || q.Items[2].PackageSize != 30 {
		t.Errorf("scriptReplyP4: «un paquete de 30» tiene que ser qty 1, package, 30; es %+v", q.Items)
	}
	bad := strings.Replace(scriptReplyP4, `"package_size":30`, `"package_size":0`, 1)
	if bad == scriptReplyP4 {
		t.Fatalf("el control negativo no cambió scriptReplyP4")
	}
	if _, err := llm.ParseQuantities(json.RawMessage(bad)); err == nil {
		t.Errorf("ParseQuantities acepta un package_size 0: el validador ya no muerde y este test no prueba nada")
	}
}

// scriptBench es un guion vacío puesto en el núcleo de un Edge de prueba con lease vigente, más los
// prompts reales de las cinco etapas. ask cuenta las peticiones para saber qué frame es la respuesta.
type scriptBench struct {
	e       *edge
	c       *edgeColector
	k       claves
	g       *inferenceScript
	prompts map[scriptStage]string
	n       int
}

// newScriptBench monta el banco.
func newScriptBench(t *testing.T) *scriptBench {
	t.Helper()
	e, c, k := edgeDePrueba(t)
	edgeGrantLease(t, e, k)
	g := newInferenceScript(t)
	e.Inferir = g.Infer
	return &scriptBench{e: e, c: c, k: k, g: g, prompts: scriptRealPrompts(strings.Join(draftBurst(), "\n"))}
}

// ask manda al núcleo del Edge una InferenceRequest con el prompt dado (techo 512, class «lote») y
// devuelve el frame con el que contestó.
func (b *scriptBench) ask(t *testing.T, prompt string) *cloudlinkv1.EdgeToCloud {
	t.Helper()
	b.n++
	ceiling := int32(512)
	b.e.manejar(edgePeticion("inf", prompt, false, &ceiling))
	frames := b.c.todos()
	if len(frames) != b.n {
		t.Fatalf("tras la petición %d el Edge lleva emitidos %d frames", b.n, len(frames))
	}
	return frames[b.n-1]
}

// reply manda el prompt y devuelve la salida abierta con la privada de la nube.
func (b *scriptBench) reply(t *testing.T, prompt string) string {
	t.Helper()
	_, raw := edgeAbrirSalida(t, b.ask(t, prompt), b.k)
	return raw
}

// TestArnes_GuionModes prueba los modos del guion puesto en un Edge de prueba (el núcleo, sin
// servidor): Respond y RespondToItem salen selladas y se abren con la privada de la nube; Fail sale
// como error nombrado y Clear lo quita; una etapa sin respuesta y un prompt sin marcador contestan
// OLLAMA_DOWN y quedan en Problems; Delay retrasa la respuesta y Release la suelta; Calls registra
// etapa, ítem, techo y rótulo.
func TestArnes_GuionModes(t *testing.T) {
	t.Parallel()
	b := newScriptBench(t)
	scriptCheckReplies(t, b)
	scriptCheckFailures(t, b)
	scriptCheckDelays(t, b)

	calls := b.g.Calls("")
	if len(calls) != b.n {
		t.Fatalf("Calls registra %d llamadas, quería %d", len(calls), b.n)
	}
	if p3 := b.g.Calls(stageP3); len(p3) != 2 || p3[0].Item != scriptIdeaChoc || p3[1].Item != "otra idea" || p3[0].MaxOutputTokens != 512 || p3[0].Class != "lote" {
		t.Errorf("Calls(P3) = %+v", p3)
	}
	if unknown := calls[6]; unknown.Stage != "" || unknown.Prompt != "un prompt sin marcador" {
		t.Errorf("la llamada sin etapa = %+v", unknown)
	}
	if errs := b.e.Errores(); len(errs) != 0 {
		t.Errorf("el núcleo del Edge anotó errores: %v", errs)
	}
}

// scriptCheckReplies: Respond contesta la etapa, RespondToItem gana en P3 para su ítem, y otro ítem
// usa la respuesta de la etapa. Tres peticiones.
func scriptCheckReplies(t *testing.T, b *scriptBench) {
	t.Helper()
	const empty = `{"version":1,"items":[]}`
	b.g.Respond(stageP2, scriptReplyP2)
	if raw := b.reply(t, b.prompts[stageP2]); raw != scriptReplyP2 {
		t.Errorf("Respond(P2): salida = %q", raw)
	}
	b.g.Respond(stageP3, empty)
	b.g.RespondToItem(" "+scriptIdeaChoc+" ", scriptReplyP3Choc)
	if raw := b.reply(t, b.prompts[stageP3]); raw != scriptReplyP3Choc {
		t.Errorf("RespondToItem gana a Respond(P3): salida = %q", raw)
	}
	other := llm.BuildExtractItemSpecsPrompt(llm.ExtractItemSpecsInput{SourceText: "x", Idea: "otra idea"})
	if raw := b.reply(t, other); raw != empty {
		t.Errorf("un ítem sin respuesta propia usa la de la etapa: salida = %q", raw)
	}
}

// scriptCheckFailures: Fail contesta el error nombrado sin salida, Clear lo quita, y lo que el guion
// no sabe atender (una etapa sin respuesta, un prompt sin marcador) contesta OLLAMA_DOWN y queda en
// Problems. Cuatro peticiones.
func scriptCheckFailures(t *testing.T, b *scriptBench) {
	t.Helper()
	down := cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN
	b.g.Fail(stageP2, cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT)
	if got := b.ask(t, b.prompts[stageP2]).GetInferenceResult(); got.GetError() != cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT || len(got.GetEncOutput()) != 0 {
		t.Errorf("Fail(P2, TIMEOUT): resultado = %+v", got)
	}
	b.g.Clear(stageP2)
	if raw := b.reply(t, b.prompts[stageP2]); raw != scriptReplyP2 {
		t.Errorf("tras Clear(P2): salida = %q", raw)
	}
	if len(b.g.Problems()) != 0 {
		t.Fatalf("problemas antes de provocarlos: %v", b.g.Problems())
	}
	if got := b.ask(t, b.prompts[stageP4]).GetInferenceResult().GetError(); got != down {
		t.Errorf("una etapa sin respuesta: error = %v, quería OLLAMA_DOWN", got)
	}
	if got := b.ask(t, "un prompt sin marcador").GetInferenceResult().GetError(); got != down {
		t.Errorf("un prompt sin marcador: error = %v, quería OLLAMA_DOWN", got)
	}
	if problems := b.g.Problems(); len(problems) != 2 || !strings.Contains(problems[0], "p4") || !strings.Contains(problems[1], "sin marcador") {
		t.Errorf("Problems = %v", problems)
	}
}

// scriptCheckDelays: Delay retrasa la respuesta al menos lo pedido, y Release corta un retardo largo.
// Dos peticiones.
func scriptCheckDelays(t *testing.T, b *scriptBench) {
	t.Helper()
	b.g.Respond(stageP5, scriptQuoteText(t, "listo"))
	b.g.Delay(stageP5, 80*time.Millisecond)
	start := time.Now()
	if raw := b.reply(t, b.prompts[stageP5]); !strings.Contains(raw, "listo") {
		t.Errorf("Delay(P5): salida = %q", raw)
	}
	if took := time.Since(start); took < 80*time.Millisecond {
		t.Errorf("Delay(P5, 80ms): contestó en %s", took)
	}
	b.g.Delay(stageP5, time.Hour)
	b.g.Release()
	start = time.Now()
	b.ask(t, b.prompts[stageP5])
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("tras Release, un retardo de una hora tardó %s", took)
	}
}
