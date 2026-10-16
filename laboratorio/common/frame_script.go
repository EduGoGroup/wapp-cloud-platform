//go:build laboratorio

package common

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// ScriptFrame es el «modelo» determinista del laboratorio: contesta un JSON fijo según la etapa que
// reconoce en el prompt. Es una ADAPTACIÓN de inferenceScript (test/procesos/guion_test.go), que no se
// puede importar porque es un _test.go: se copia la regla de reconocimiento y los JSON del caso Ámbar.
//
// # Cómo reconoce la etapa
//
// Por el RÓTULO con el que el constructor de cada prompt (wapp-shared/llm, Build…Prompt) introduce
// sus datos: es lo único que no se puede ajustar por fichero (WAPP_LLM_PROMPTS_DIR). Gana el rótulo
// que aparece ANTES en el prompt, así que un cliente que escriba «Ítems a normalizar:» no cambia la
// etapa. El techo de salida NO sirve: P2 y P3 valen los dos 512.
//
// # Qué contesta
//
//   - P2, P3 (por ítem) y P4: las del caso Ámbar (NewAmberScriptFrame).
//   - P1, si nadie fijó otra: `intake_request` con confianza 0,95 y, como evidencia, el mensaje entero
//     del cliente (es subcadena de sí mismo por construcción).
//   - P5, si nadie fijó otra: un texto de cotización fijo.
//   - El turno acotado (StageTurn), si nadie fijó otra: «no_entendido» (TurnReplyNotUnderstood).
//   - Una etapa sin respuesta, o un prompt sin rótulo conocido: error, y queda anotado en Problems.
//
// Es seguro entre goroutines.
type ScriptFrame struct {
	mu        sync.Mutex
	replies   map[Stage]string
	byItem    map[string]string
	failures  map[Stage]error
	delays    map[Stage]time.Duration
	autoP1    bool
	calls     []ScriptCall
	problems  []string
	lastStage Stage
}

var _ local.Frame = (*ScriptFrame)(nil)

// Stage es una etapa del pipeline LLM, con el identificador corto de wapp-shared/llm.
type Stage string

// Las cinco etapas. P1 la pide el adelanto de ventana; P2, P3 y P4, el worker de captación; P5, la
// sugerencia de cotización.
const (
	StageP1 Stage = "p1"
	StageP2 Stage = "p2"
	StageP3 Stage = "p3"
	StageP4 Stage = "p4"
	StageP5 Stage = "p5"
)

// Stages es el orden canónico.
var Stages = []Stage{StageP1, StageP2, StageP3, StageP4, StageP5}

// StageTurn es el TURNO ACOTADO (internal/modulos/conversacion/turnoacotado, que pregunta por
// llmvia.Selector.Turno): la pregunta suelta que el carrito eleva cuando no entiende una cantidad o
// una opción. No es una etapa del pipeline y por eso NO está en Stages; StageOf la reconoce aparte
// (Plan 058 · Ola 3, E08).
const StageTurn Stage = "turno"

// recognizedStages son las etapas que StageOf sabe reconocer: las del pipeline y el turno.
var recognizedStages = append(append([]Stage(nil), Stages...), StageTurn)

// Las salidas del turno acotado: el JSON que fuerza su esquema (turnoacotado/prompt.go).
const (
	// TurnReplyNotUnderstood es lo que contesta un modelo bien portado ante «abc».
	TurnReplyNotUnderstood = `{"usable": false, "value": null, "reason": "no_entendido"}`
)

// TurnReplyValue arma un turno resuelto: usable, con ese valor (una cantidad, o la POSICIÓN de la
// opción elegida).
func TurnReplyValue(v int) string {
	return fmt.Sprintf(`{"usable": true, "value": %d, "reason": "ok"}`, v)
}

// marcadores son los rótulos de cada constructor (wapp-shared/llm@v0.4.5, prompt.go).
var markers = map[Stage]string{
	StageP1: "\nClasifica el mensaje del cliente en UNA de estas intenciones",
	StageP2: "\nTexto del cliente:\n",
	StageP3: "\nTexto completo del cliente (contexto y fuente de la evidencia):\n",
	StageP4: "\nÍtems a normalizar:\n",
	StageP5: "\nBorrador a redactar:\n",
	// El turno acotado (turnoacotado/prompt.go): el rótulo que precede al caso a resolver.
	StageTurn: "\nAhora resolvé este caso:\n",
}

// marcadorItem precede, AL FINAL del prompt de P3, a la idea que hay que especificar.
const itemMarker = "\n\nÍtem que debes especificar:\n"

// marcadorMensajeP1 precede, al final del prompt de P1, al mensaje del cliente.
const p1MessageMarker = "\nMensaje del cliente:\n"

// ScriptCall es una inferencia que el guion atendió.
type ScriptCall struct {
	Stage           Stage
	Prompt          string
	Item            string
	MaxOutputTokens int32
	Class           string
	Format          string
	Temperature     float64
	Timeout         time.Duration
}

// NewScriptFrame devuelve un guion VACÍO, sin P1 automática: toda etapa sin respuesta falla.
func NewScriptFrame() *ScriptFrame {
	return &ScriptFrame{
		replies:  map[Stage]string{},
		byItem:   map[string]string{},
		failures: map[Stage]error{},
		delays:   map[Stage]time.Duration{},
	}
}

// NewAmberScriptFrame devuelve el guion del caso Ámbar: P2, las tres P3 y P4 como en el arnés, P1
// automática (intake_request, 0,95), P5 con AmberQuoteText y el turno acotado con
// TurnReplyNotUnderstood.
func NewAmberScriptFrame() *ScriptFrame {
	g := NewScriptFrame()
	g.autoP1 = true
	g.Respond(StageP2, AmberReplyP2)
	g.RespondToItem(AmberIdeaChocolate, AmberReplyP3Chocolate)
	g.RespondToItem(AmberIdeaVanilla, AmberReplyP3Vanilla)
	g.RespondToItem(AmberIdeaTequenos, AmberReplyP3Tequenos)
	g.Respond(StageP4, AmberReplyP4)
	g.Respond(StageP5, P5Reply(AmberQuoteText))
	g.Respond(StageTurn, TurnReplyNotUnderstood)
	return g
}

// Respond fija lo que el modelo contesta en una etapa: el JSON crudo, tal cual saldría de Ollama. No
// valida: un JSON que el validador rechaza es justo lo que un escalón «Rómpelo» quiere ver. En P1 apaga
// la respuesta automática.
func (g *ScriptFrame) Respond(etapa Stage, rawJSON string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.replies[etapa] = rawJSON
	if etapa == StageP1 {
		g.autoP1 = false
	}
}

// RespondToItem fija lo que P3 contesta cuando la idea pedida es idea (sin espacios en los extremos).
// Gana a Respond(StageP3, …).
func (g *ScriptFrame) RespondToItem(idea, rawJSON string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byItem[strings.TrimSpace(idea)] = rawJSON
}

// Fail hace que la etapa conteste con err en vez de con su salida. nil la devuelve a su sitio.
func (g *ScriptFrame) Fail(etapa Stage, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err == nil {
		delete(g.failures, etapa)
		return
	}
	g.failures[etapa] = err
}

// Delay hace que la etapa tarde d en contestar (el modelo lento), respetando el plazo del ctx. 0 lo
// quita.
func (g *ScriptFrame) Delay(etapa Stage, d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.delays[etapa] = d
}

// Calls devuelve, en orden, las inferencias atendidas de una etapa; con «» devuelve todas.
func (g *ScriptFrame) Calls(etapa Stage) []ScriptCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []ScriptCall
	for _, c := range g.calls {
		if etapa == "" || c.Stage == etapa {
			out = append(out, c)
		}
	}
	return out
}

// Problems devuelve lo que el guion no supo atender.
func (g *ScriptFrame) Problems() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.problems...)
}

// LastStage es la etapa que el guion reconoció en la última llamada («» si ninguna).
func (g *ScriptFrame) LastStage() Stage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastStage
}

// Infer implementa local.Frame.
func (g *ScriptFrame) Infer(ctx context.Context, _ string, req edgegrpc.InferRequest) (string, error) {
	stage, _ := StageOf(req.Prompt)
	call := ScriptCall{
		Stage: stage, Prompt: req.Prompt, MaxOutputTokens: req.MaxOutputTokens, Class: req.Class,
		Format: req.Format, Temperature: req.Temperature, Timeout: req.Timeout,
	}
	if stage == StageP3 {
		call.Item = ItemOf(req.Prompt)
	}

	g.mu.Lock()
	g.calls = append(g.calls, call)
	g.lastStage = stage
	delay := g.delays[stage]
	failure := g.failures[stage]
	reply, scripted := g.byItem[call.Item]
	if stage != StageP3 || !scripted {
		reply, scripted = g.replies[stage]
	}
	if stage == StageP1 && g.autoP1 {
		reply, scripted = autoClassification(req.Prompt), true
	}
	var problem string
	switch {
	case stage == "":
		problem = fmt.Sprintf("prompt sin rótulo de etapa (%d bytes, class %q)", len(req.Prompt), req.Class)
	case failure == nil && !scripted:
		problem = fmt.Sprintf("la etapa %s no tiene respuesta en el guion (ítem %q)", stage, call.Item)
	}
	if problem != "" {
		g.problems = append(g.problems, problem)
	}
	g.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if failure != nil {
		return "", failure
	}
	if problem != "" {
		return "", fmt.Errorf("guion: %s", problem)
	}
	return reply, nil
}

// StageOf reconoce la etapa de un prompt: la del rótulo que aparece ANTES. false si no trae ninguno.
func StageOf(prompt string) (Stage, bool) {
	best, at := Stage(""), -1
	for _, stage := range recognizedStages {
		if i := strings.Index(prompt, markers[stage]); i >= 0 && (at < 0 || i < at) {
			best, at = stage, i
		}
	}
	return best, at >= 0
}

// ItemOf devuelve la idea que un prompt de P3 pide especificar; «» si no la trae.
func ItemOf(prompt string) string {
	i := strings.LastIndex(prompt, itemMarker)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(prompt[i+len(itemMarker):])
}

// autoClassification es la P1 automática: intake_request, 0,95 y el mensaje entero como evidencia.
func autoClassification(prompt string) string {
	text := ""
	if i := strings.LastIndex(prompt, p1MessageMarker); i >= 0 {
		text = strings.TrimSpace(prompt[i+len(p1MessageMarker):])
	}
	return P1Reply(IntentIntakeRequest, 0.95, text)
}

// P1Reply arma la salida de P1: {"version":1,"intent":…,"confidence":…,"evidence":…}.
func P1Reply(intent string, confidence float64, evidence string) string {
	return mustJSON(map[string]any{"version": 1, "intent": intent, "confidence": confidence, "evidence": evidence})
}

// P5Reply arma la salida de P5: {"version":1,"text":…}.
func P5Reply(text string) string {
	return mustJSON(map[string]any{"version": 1, "text": text})
}

// mustJSON serializa v; un map de cadenas y números siempre serializa, así que un fallo es de
// programación.
func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("common: serializando %T: %v", v, err))
	}
	return string(raw)
}
