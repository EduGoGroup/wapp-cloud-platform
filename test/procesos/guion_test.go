//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// El «modelo» de prueba: el guion de inferencia (diseno.md §3.4 · T9.17). Es lo que el Edge de
// prueba consulta para contestar una InferenceRequest: se asigna a edge.Inferir ANTES de conectar
// (`e.Inferir = script.Infer`). El Edge hace el resto como el real: late READY (trampa T-7), aplica
// el gate de lease, sirve de una en una y devuelve la salida SELLADA con la pública de la nube
// (trampa T-9, edge_falso_inference_test.go). Aquí no hay red ni cripto: solo «qué etapa es este
// prompt» y «qué contesta el modelo».
//
// # Cómo reconoce cada etapa: por un MARCADOR DEL PROMPT, no por el techo de salida
//
// `InferenceRequest.class` no dice la etapa (trampa T-8: es telemetría, «interactivo» o «lote»). Las
// dos candidatas eran el techo `max_output_tokens` y un marcador del texto. Leídos
// internal/llmvia/local/local.go y wapp-shared/llm@v0.4.5 (prompt.go), gana el marcador:
//
//   - el techo NO distingue P2 de P3: los dos valen 512 (P1 192, P4 1024, P5 768);
//   - el techo se puede APAGAR en campo (local.WithMaxOutputTokens, el interruptor de
//     configuración del arranque): entonces el campo viaja ausente en las cinco etapas;
//   - el texto de la plantilla (instrucción y esquema) es AJUSTABLE por fichero y sin release
//     (WAPP_LLM_PROMPTS_DIR), así que tampoco sirve de marcador. Lo que no es ajustable es la
//     COMPOSICIÓN, que escribe el constructor (`Build…PromptCon`: «la composición no es negociable
//     y por eso vive aquí»): el rótulo con el que cada etapa introduce sus DATOS.
//
// Esos rótulos son los marcadores (scriptMarkers). La etapa es la del marcador que aparece ANTES en
// el prompt: en las cinco etapas el rótulo va delante del texto del cliente, así que un cliente que
// escriba «Ítems a normalizar:» en su mensaje no cambia la etapa. El techo se registra en cada
// llamada (scriptCall.MaxOutputTokens) para que el proceso lo afirme como contrato, no para decidir.
// TestArnes_GuionRecognizesStages construye los cinco prompts con los constructores REALES de
// wapp-shared/llm: si un release cambia un rótulo, se pone rojo ese test y no un proceso entero.
//
// # De qué test viejo sale cada JSON (E-8)
//
//   - P2 (scriptReplyP2), P3 (scriptReplyP3Choc, …Vanilla, …Tequenos) y P4 (scriptReplyP4): de
//     internal/intake/pipeline/guion_ambar_test.go, el modelo falso `fusProveedor` del caso Ámbar
//     (ExtractMainIdeas, ExtractItemSpecs, NormalizeQuantities), valor por valor. Las `evidence`
//     son subcadenas literales de la ráfaga del caso (draftBurst, en p4_borrador_helpers_test.go):
//     P2 descarta la idea cuya evidencia no está en el literal (internal/intake/stages/p2_test.go).
//   - P1 (scriptClassification): la forma es el esquema del prompt de P1 y el valor el de
//     `fusClasificador` del mismo fichero (`intake_request`, 0.95).
//   - P5 (scriptQuoteText): `artefactoP5` de internal/intakes/quotetext/dobles_test.go.
//   - El caso «un paquete de 30 es qty 1 con package_size 30, nunca qty 30» y el `package_size`
//     que no puede valer 0: internal/intake/stages/p4_test.go.
//
// 🔴 I-CP-1: ningún JSON del guion lleva un valor que su propio validador rechace (P4 fue 0 de 14
// en campo por un `"package_size": 0`). TestArnes_GuionRepliesPassTheirValidator pasa cada uno por
// el `Parse…` de su etapa.

// scriptStage es una etapa del pipeline LLM, con el identificador corto de wapp-shared/llm.
type scriptStage string

// Las cinco etapas. P1 la pide el adelanto de ventana (solo con catálogo de intenciones publicado);
// P2, P3 y P4, el worker del pipeline de captación; P5, la sugerencia de cotización.
const (
	stageP1 scriptStage = "p1"
	stageP2 scriptStage = "p2"
	stageP3 scriptStage = "p3"
	stageP4 scriptStage = "p4"
	stageP5 scriptStage = "p5"
)

// scriptStages es el orden canónico de las etapas.
var scriptStages = []scriptStage{stageP1, stageP2, stageP3, stageP4, stageP5}

// scriptMarkers son los rótulos con los que el CONSTRUCTOR de cada prompt introduce sus datos
// (wapp-shared/llm@v0.4.5, prompt.go). Ver la cabecera del fichero.
var scriptMarkers = map[scriptStage]string{
	stageP1: "\nClasifica el mensaje del cliente en UNA de estas intenciones",
	stageP2: "\nTexto del cliente:\n",
	stageP3: "\nTexto completo del cliente (contexto y fuente de la evidencia):\n",
	stageP4: "\nÍtems a normalizar:\n",
	stageP5: "\nBorrador a redactar:\n",
}

// scriptItemMarker es el rótulo que precede a la idea que P3 debe especificar: va AL FINAL del
// prompt (el hilo es el prefijo estable, la idea lo único que cambia entre las N llamadas).
const scriptItemMarker = "\n\nÍtem que debes especificar:\n"

// scriptCeilings son los techos de salida que el Cloud fija por etapa (internal/llmvia/local/local.go,
// «techo por tarea»). No deciden la etapa: los afirma el proceso.
var scriptCeilings = map[scriptStage]int32{stageP1: 192, stageP2: 512, stageP3: 512, stageP4: 1024, stageP5: 768}

// Las ideas del caso Ámbar: lo que P2 saca del hilo y la clave con la que P3 elige qué contestar
// (P3 se llama una vez por idea).
const (
	scriptIdeaChoc     = "torta de chocolate"
	scriptIdeaVanilla  = "torta de vainilla"
	scriptIdeaTequenos = "tequeños congelados"
)

// Las evidencias del caso Ámbar: subcadenas literales de la ráfaga (draftBurst).
const (
	scriptEvidenceDelivery = "para el miércoles de la semana que viene"
	scriptEvidenceChoc     = "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"
	scriptEvidenceVanilla  = "otra de bizcocho de vainilla que tenga lluvia de colores"
	scriptEvidenceTequenos = "un paquete de tequeños congelados de 30"
)

// scriptStaleDate es la fecha que el modelo «propone» en P4: la del caso original (22/07/2026). La
// aritmética que manda es la de Go contra el message_ts (D-044.9), así que el borrador NO la lleva.
const scriptStaleDate = "2026-07-22"

// Las respuestas del modelo bien portado del caso Ámbar, etapa por etapa.
const (
	scriptReplyP2 = `{"version":1,"wants":[` +
		`{"idea":"` + scriptIdeaChoc + `","evidence":"` + scriptEvidenceChoc + `"},` +
		`{"idea":"` + scriptIdeaVanilla + `","evidence":"` + scriptEvidenceVanilla + `"},` +
		`{"idea":"` + scriptIdeaTequenos + `","evidence":"` + scriptEvidenceTequenos + `"}],` +
		`"delivery_hint":{"text":"el miércoles de la semana que viene","evidence":"` + scriptEvidenceDelivery + `"}}`

	scriptReplyP3Choc = `{"version":1,"items":[{"product":"` + scriptIdeaChoc + `","variant":"10 o 12 porciones",` +
		`"addon_candidates":["decoración infantil"],"notes":"bizcocho húmedo de chocolate con crema de chocolate",` +
		`"evidence":"` + scriptEvidenceChoc + `"}]}`
	scriptReplyP3Vanilla = `{"version":1,"items":[{"product":"` + scriptIdeaVanilla + `","variant":"25 o 30 porciones",` +
		`"customizations":["lluvia de colores, dulce de leche y merengue"],"evidence":"` + scriptEvidenceVanilla + `"}]}`
	scriptReplyP3Tequenos = `{"version":1,"items":[{"product":"` + scriptIdeaTequenos + `","evidence":"` + scriptEvidenceTequenos + `"}]}`

	scriptReplyP4 = `{"version":1,"delivery_date":"` + scriptStaleDate + `","items":[` +
		`{"product":"` + scriptIdeaChoc + `","qty":1,"evidence":"` + scriptEvidenceChoc + `","range":{"min":10,"max":12,"unit":"porciones"}},` +
		`{"product":"` + scriptIdeaVanilla + `","qty":1,"evidence":"` + scriptEvidenceVanilla + `","range":{"min":25,"max":30,"unit":"porciones"}},` +
		`{"product":"` + scriptIdeaTequenos + `","qty":1,"evidence":"` + scriptEvidenceTequenos + `","unit_kind":"package","package_size":30}]}`
)

// scriptQuoteText arma la salida de P5 con el texto dado: {"version":1,"text":"…"}. Falla el test si
// el texto no serializa.
func scriptQuoteText(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 1, "text": text})
	if err != nil {
		t.Fatalf("scriptQuoteText: %v", err)
	}
	return string(raw)
}

// scriptClassification arma la salida de P1: la intención (copiada literal del catálogo publicado),
// la confianza (0 a 1) y la evidencia. Falla el test si no serializa.
func scriptClassification(t *testing.T, intent string, confidence float64, evidence string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 1, "intent": intent, "confidence": confidence, "evidence": evidence})
	if err != nil {
		t.Fatalf("scriptClassification: %v", err)
	}
	return string(raw)
}

// scriptCall es una inferencia que el guion atendió: la etapa que reconoció («» si ninguna), el
// prompt entero, la idea si es P3, el techo de salida (0 = el campo viajó ausente) y el rótulo de
// telemetría.
type scriptCall struct {
	Stage           scriptStage
	Prompt          string
	Item            string
	MaxOutputTokens int32
	Class           string
}

// inferenceScript es el guion. Por etapa lleva una respuesta, un fallo y un retardo; P3 admite además
// una respuesta por ítem. Es seguro entre goroutines: lo consulta el bucle del Edge y lo cambia el test.
type inferenceScript struct {
	mu       sync.Mutex
	replies  map[scriptStage]string
	byItem   map[string]string
	failures map[scriptStage]cloudlinkv1.InferenceError
	delays   map[scriptStage]time.Duration
	calls    []scriptCall
	problems []string

	releaseOnce sync.Once
	released    chan struct{}
}

// newInferenceScript devuelve un guion VACÍO: toda etapa sin respuesta se contesta con
// INFERENCE_ERROR_OLLAMA_DOWN y queda anotada en Problems. Release se registra en el Cleanup del test.
func newInferenceScript(t *testing.T) *inferenceScript {
	t.Helper()
	g := &inferenceScript{
		replies:  map[scriptStage]string{},
		byItem:   map[string]string{},
		failures: map[scriptStage]cloudlinkv1.InferenceError{},
		delays:   map[scriptStage]time.Duration{},
		released: make(chan struct{}),
	}
	t.Cleanup(g.Release)
	return g
}

// newAmbarScript devuelve el guion del caso Ámbar: P2, las tres P3 y P4 contestan lo que contestaba
// el modelo falso de guion_ambar_test.go. P1 y P5 quedan sin respuesta: las pone quien las use.
func newAmbarScript(t *testing.T) *inferenceScript {
	t.Helper()
	g := newInferenceScript(t)
	g.UseAmbar()
	return g
}

// UseAmbar pone (o repone) las respuestas del caso Ámbar en P2, P3 y P4. No toca P1 ni P5, ni los
// fallos ni los retardos.
func (g *inferenceScript) UseAmbar() {
	g.Respond(stageP2, scriptReplyP2)
	g.RespondToItem(scriptIdeaChoc, scriptReplyP3Choc)
	g.RespondToItem(scriptIdeaVanilla, scriptReplyP3Vanilla)
	g.RespondToItem(scriptIdeaTequenos, scriptReplyP3Tequenos)
	g.Respond(stageP4, scriptReplyP4)
}

// Respond fija lo que el modelo contesta en una etapa: el JSON crudo, tal cual saldría de Ollama.
// Sustituye a la respuesta anterior de esa etapa. No valida: un JSON que el validador rechaza es un
// caso legítimo (calidad), y lo decide el test.
func (g *inferenceScript) Respond(stage scriptStage, rawJSON string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.replies[stage] = rawJSON
}

// RespondToItem fija lo que P3 contesta cuando el ítem que se le pide especificar es idea (se compara
// sin espacios en los extremos). Gana a Respond(stageP3, …).
func (g *inferenceScript) RespondToItem(idea, rawJSON string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byItem[strings.TrimSpace(idea)] = rawJSON
}

// Fail hace que la etapa conteste con el error nombrado (INFERENCE_ERROR_…) en vez de con su salida,
// hasta que Clear la devuelva a su sitio. Gana a la respuesta; el retardo, si lo hay, se cumple antes.
func (g *inferenceScript) Fail(stage scriptStage, code cloudlinkv1.InferenceError) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[stage] = code
}

// Delay hace que la etapa tarde d en contestar (el modelo lento), hasta Clear. Mientras espera, el
// Edge sigue atendiendo el resto de comandos, pero no otra inferencia (una sola plaza).
func (g *inferenceScript) Delay(stage scriptStage, d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.delays[stage] = d
}

// Clear quita el fallo y el retardo de la etapa. Las respuestas no se tocan.
func (g *inferenceScript) Clear(stage scriptStage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, stage)
	delete(g.delays, stage)
}

// Release corta los retardos en curso y los futuros: lo llama el Cleanup del test para que un guion
// lento no retenga el cierre del Edge. Es idempotente.
func (g *inferenceScript) Release() {
	g.releaseOnce.Do(func() { close(g.released) })
}

// Calls devuelve, en orden, las inferencias atendidas de una etapa; con «» devuelve todas.
func (g *inferenceScript) Calls(stage scriptStage) []scriptCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []scriptCall
	for _, c := range g.calls {
		if stage == "" || c.Stage == stage {
			out = append(out, c)
		}
	}
	return out
}

// Problems devuelve lo que el guion no supo atender: un prompt sin marcador conocido o una etapa
// sin respuesta. Un proceso termina comprobando que está vacío.
func (g *inferenceScript) Problems() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.problems...)
}

// Infer es lo que se asigna a edge.Inferir. Reconoce la etapa del prompt, registra la llamada,
// cumple el retardo y contesta: el error nombrado si la etapa está en fallo; si no, la respuesta por
// ítem (P3) o la de la etapa. Sin etapa reconocida o sin respuesta contesta OLLAMA_DOWN y lo anota.
func (g *inferenceScript) Infer(req *cloudlinkv1.InferenceRequest) (string, *cloudlinkv1.InferenceError) {
	prompt := req.GetPrompt()
	stage, _ := scriptStageOf(prompt)
	call := scriptCall{Stage: stage, Prompt: prompt, MaxOutputTokens: req.GetMaxOutputTokens(), Class: req.GetClass()}
	if stage == stageP3 {
		call.Item = scriptItemOf(prompt)
	}

	g.mu.Lock()
	g.calls = append(g.calls, call)
	delay := g.delays[stage]
	failure, failing := g.failures[stage]
	reply, scripted := g.byItem[call.Item]
	if stage != stageP3 || !scripted {
		reply, scripted = g.replies[stage]
	}
	switch {
	case stage == "":
		g.problems = append(g.problems, fmt.Sprintf("prompt sin marcador de etapa (%d bytes, class %q)", len(prompt), call.Class))
	case !failing && !scripted:
		g.problems = append(g.problems, fmt.Sprintf("la etapa %s no tiene respuesta en el guion (ítem %q)", stage, call.Item))
	}
	g.mu.Unlock()

	if delay > 0 {
		g.wait(delay)
	}
	if failing {
		return "", &failure
	}
	if stage == "" || !scripted {
		down := cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN
		return "", &down
	}
	return reply, nil
}

// wait espera d o hasta Release, lo que ocurra antes.
func (g *inferenceScript) wait(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-g.released:
	}
}

// scriptStageOf reconoce la etapa de un prompt: la del marcador que aparece ANTES. Devuelve false si
// no contiene ninguno.
func scriptStageOf(prompt string) (scriptStage, bool) {
	best, at := scriptStage(""), -1
	for _, stage := range scriptStages {
		if i := strings.Index(prompt, scriptMarkers[stage]); i >= 0 && (at < 0 || i < at) {
			best, at = stage, i
		}
	}
	return best, at >= 0
}

// scriptItemOf devuelve la idea que un prompt de P3 pide especificar: lo que sigue al ÚLTIMO
// scriptItemMarker, sin espacios en los extremos; «» si el prompt no lo trae.
func scriptItemOf(prompt string) string {
	i := strings.LastIndex(prompt, scriptItemMarker)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(prompt[i+len(scriptItemMarker):])
}
