//go:build laboratorio

package common

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// HybridFrame enruta cada inferencia POR ETAPA: las etapas elegidas (-script-stages) las contesta
// el guion (ScriptFrame) y el resto el modelo de verdad (OllamaFrame).
//
// # Para qué existe
//
// Con Gemma, el P2 real tumba 12 de 14 frases del banco (E10), y con él muere todo lo que viene
// detrás. Jhoan decidió NO arreglarlo sin entenderlo (queda como deuda y expediente aparte), pero
// quiere seguir estudiando el flujo con el modelo donde el modelo sí funciona. Este frame es eso:
// un andamio para trabajar, NO un arreglo.
//
// # El fallo sigue a la vista
//
// Cada llamada se narra con quién contestó y, si fue el guion, POR QUÉ: «⚠️ P2 por GUION: Gemma
// falla aquí, ver deuda». Al final, Tally resume cuántas contestó cada uno. Un escalón que corre
// con -frame hybrid no puede presumir de que «Gemma lo hace bien»: lo dice la pantalla.
//
// Es seguro entre goroutines.
type HybridFrame struct {
	script    *ScriptFrame
	model     *OllamaFrame
	scripted  map[Stage]bool
	n         *Narrator
	mu        sync.Mutex
	byScript  map[Stage]int
	byModel   map[Stage]int
	modelErrs map[Stage]int
}

var _ local.Frame = (*HybridFrame)(nil)

// DefaultScriptStages son las etapas que van por el guion si nadie dice otra cosa: las tres del
// pipeline, para que el flujo llegue SIEMPRE al borrador. ⚠️ Medido en E11 el 2026-10-14: con P2
// por el guion, Gemma hace P3 y P4 de la ráfaga Ámbar y el borrador sale con las 3 líneas casadas;
// el «torta» de E11 con -frame ollama nacía en las ideas de SU P2. Para usar el modelo todo lo
// posible: -script-stages p2.
const DefaultScriptStages = "p2,p3,p4"

// whyScripted es el motivo que se imprime cuando una etapa va por el guion. Lo que dice sale de
// lo medido en E10 y E11 (README del Plan 058, §4): no es una opinión del frame.
var whyScripted = map[Stage]string{
	StageP1: "elegida con -script-stages (con Gemma, P1 acierta 13 de 14 en E10)",
	StageP2: "Gemma falla aquí, ver deuda (E10: 12 de 14 frases mueren en P2 por evidence vacía o delivery_hint null)",
	StageP3: "elegida con -script-stages (con SUS ideas de P2, Gemma deja «torta» y el match no casa: E10, E11; con las del guion, sí casa)",
	StageP4: "elegida con -script-stages (con SUS ideas de P2, Gemma baraja atributos entre ítems: E10)",
	StageP5: "elegida con -script-stages",
}

// NewHybridFrame construye el frame. scripted son las etapas del guion; las demás van a model. n
// es por donde se narra cada llamada (nil ⇒ stdout).
func NewHybridFrame(script *ScriptFrame, model *OllamaFrame, scripted []Stage, n *Narrator) *HybridFrame {
	if n == nil {
		n = NewNarrator(nil)
	}
	set := map[Stage]bool{}
	for _, s := range scripted {
		set[s] = true
	}
	return &HybridFrame{
		script: script, model: model, scripted: set, n: n,
		byScript: map[Stage]int{}, byModel: map[Stage]int{}, modelErrs: map[Stage]int{},
	}
}

// ParseStages lee una lista «p2,p3,p4» (sin distinguir mayúsculas ni espacios). Una etapa
// desconocida es un error: un -script-stages con una errata enviaría al modelo lo que se quería
// guionizar, y el alumno creería que Gemma lo hizo bien.
func ParseStages(csv string) ([]Stage, error) {
	var out []Stage
	for _, raw := range strings.Split(csv, ",") {
		s := Stage(strings.ToLower(strings.TrimSpace(raw)))
		if s == "" {
			continue
		}
		if !slices.Contains(Stages, s) {
			return nil, fmt.Errorf("-script-stages: etapa %q desconocida (valen p1, p2, p3, p4 y p5)", s)
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Script es el guion de dentro: para fijar respuestas o romper una etapa en un ejercicio.
func (h *HybridFrame) Script() *ScriptFrame { return h.script }

// Model es el frame de Ollama de dentro.
func (h *HybridFrame) Model() *OllamaFrame { return h.model }

// Scripted dice, en orden canónico, qué etapas van por el guion.
func (h *HybridFrame) Scripted() []Stage {
	var out []Stage
	for _, s := range Stages {
		if h.scripted[s] {
			out = append(out, s)
		}
	}
	return out
}

// Infer implementa local.Frame: reconoce la etapa, elige quién contesta y lo narra.
func (h *HybridFrame) Infer(ctx context.Context, tenantID string, req edgegrpc.InferRequest) (string, error) {
	stage, _ := StageOf(req.Prompt)
	label := strings.ToUpper(string(stage))
	if stage == "" {
		label = "¿etapa?"
	}
	item := ""
	if stage == StageP3 {
		item = fmt.Sprintf(" («%s»)", ItemOf(req.Prompt))
	}

	if h.scripted[stage] {
		h.count(h.byScript, stage)
		h.n.Note("⚠️ %s%s por GUION: %s", label, item, whyScripted[stage])
		return h.script.Infer(ctx, tenantID, req)
	}

	start := time.Now()
	out, err := h.model.Infer(ctx, tenantID, req)
	h.count(h.byModel, stage)
	if err != nil {
		h.count(h.modelErrs, stage)
		h.n.Fail(err, "%s%s por el MODELO (%s) tras %s", label, item, h.model.Model(), roundMs(time.Since(start)))
		return out, err
	}
	h.n.Note("🧠 %s%s por el MODELO (%s) en %s", label, item, h.model.Model(), roundMs(time.Since(start)))
	return out, nil
}

func (h *HybridFrame) count(m map[Stage]int, s Stage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m[s]++
}

// Tally resume quién contestó qué: «P1 modelo 1 · P2 guion 1 · P3 guion 3 …». Las etapas sin
// ninguna llamada no salen. Los fallos del modelo (transporte o plazo; la CALIDAD la juzga después
// el validador, no el frame) van entre paréntesis.
func (h *HybridFrame) Tally() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var parts []string
	for _, s := range Stages {
		label := strings.ToUpper(string(s))
		if n := h.byScript[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s guion %d", label, n))
		}
		if n := h.byModel[s]; n > 0 {
			p := fmt.Sprintf("%s modelo %d", label, n)
			if e := h.modelErrs[s]; e > 0 {
				p += fmt.Sprintf(" (%d con error)", e)
			}
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "ninguna inferencia"
	}
	return strings.Join(parts, " · ")
}
