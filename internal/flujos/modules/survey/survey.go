// Package survey es el módulo del tipo de nodo "survey_question" (pregunta de
// encuesta de opción cerrada).
//
// Encapsula las tres responsabilidades del nodo de pregunta (design.md §3):
// render del prompt, validación de la opción tecleada y transición, más el
// reprompt acotado (design.md §10.E: máx 3 intentos inválidos → mensaje de
// ayuda y permanecer; el contador vive en Conversation.Vars y se reinicia al
// transicionar). A diferencia del menú, al validar una opción registra la
// respuesta elegida bajo `Vars["answers"][QuestionID] = answer_code`
// (design.md §10.D: el answer_code es la CLAVE de Options elegida), sin pisar
// respuestas de preguntas previas.
//
// En este corte el Prompt del nodo YA trae las opciones numeradas como texto,
// así que el render es el Prompt tal cual; `Options` es código→id-nodo-destino
// (sin etiquetas).
package survey

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// RepromptKey es la clave bajo la que se guarda el contador de intentos
// inválidos en Conversation.Vars. Es propia del módulo de encuesta y no
// colisiona con la del menú.
const RepromptKey = "survey_reprompt"

// MaxReprompts es el número de intentos inválidos tras el cual, en lugar de
// repetir el prompt, se envía un mensaje de ayuda y se reinicia el contador
// (se permanece en el nodo). Ver design.md §10.E.
const MaxReprompts = 3

// Module implementa modules.Module para el tipo de nodo "survey_question".
type Module struct{}

// New crea el módulo Encuesta.
func New() Module { return Module{} }

// Type devuelve el identificador del tipo de nodo manejado.
func (Module) Type() string { return model.NodeTypeSurveyQuestion }

// WaitsForInput indica que la pregunta es interactiva: se renderiza y detiene
// el flujo esperando la opción del usuario.
func (Module) WaitsForInput() bool { return true }

// Render devuelve el prompt de la pregunta tal cual (las opciones numeradas ya
// vienen embebidas en el texto del Prompt en este corte). Recibe el contenido YA
// RESUELTO por el engine (Plan 015); en T0 es un placeholder inline con
// content.Prompt == node.Prompt, de observable idéntico.
func (Module) Render(_ model.Node, content model.Content) []string {
	return []string{content.Prompt}
}

// Step valida la opción tecleada y decide la transición o el reprompt, sobre la
// base común de opción numerada (modules.NumberedStep, Plan 027 · Ola 2 · T9):
//   - opción válida (coincide, tras TrimSpace, con una clave de Options):
//     registra la respuesta (answer_code = la clave elegida) bajo
//     Vars["answers"][QuestionID], transiciona al nodo destino y reinicia el
//     contador de reprompt.
//   - opción inválida con < MaxReprompts intentos: re-emite el prompt precedido
//     de un aviso, e incrementa el contador (permanece en el nodo, sin registrar
//     respuesta).
//   - al alcanzar MaxReprompts intentos inválidos: emite un mensaje de ayuda,
//     reinicia el contador (para no spamear) y permanece en el nodo.
//
// La ÚNICA diferencia con el menú vive en el callback de opción válida: la encuesta
// anota la respuesta en Vars["answers"] y DECLARA el efecto survey_answer (el módulo
// sigue PURO: solo lo anota; el runtime lo despacha al PersistSink, misma fila
// survey_results del Plan 014). El reprompt y la tolerancia de entrada son comunes.
func (Module) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	return modules.NumberedStep(node, conv, input, RepromptKey, MaxReprompts,
		func(vars map[string]any, choice, target string) modules.Result {
			answers := asMap(vars["answers"])
			answers[node.QuestionID] = choice // answer_code = la clave elegida (§10.D).
			vars["answers"] = answers
			dest := target
			res := modules.Result{Next: &dest, Vars: vars}
			res.Effects = append(res.Effects, modules.Effect{
				Kind:    "persist",
				Name:    EffectSurveyAnswer,
				Payload: map[string]any{"question_id": node.QuestionID, "answer_code": choice},
			})
			return res
		})
}

// asMap devuelve el mapa de respuestas mutable, tolerando el round-trip JSON
// (map[string]any) o el tipo nativo (map[string]string) o nil. Parte del mapa
// existente para no pisar respuestas de preguntas previas. Guarda answer_code
// como string.
func asMap(v any) map[string]string {
	out := map[string]string{}
	switch m := v.(type) {
	case map[string]string:
		for k, val := range m {
			out[k] = val
		}
	case map[string]any:
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}
