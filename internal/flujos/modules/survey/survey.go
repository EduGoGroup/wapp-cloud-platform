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
	"strings"

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

// Step valida la opción tecleada y decide la transición o el reprompt:
//   - opción válida (coincide, tras TrimSpace, con una clave de Options):
//     registra la respuesta (answer_code = la clave elegida) bajo
//     Vars["answers"][QuestionID], transiciona al nodo destino y reinicia el
//     contador de reprompt.
//   - opción inválida con < MaxReprompts intentos: re-emite el prompt precedido
//     de un aviso, e incrementa el contador (permanece en el nodo, sin registrar
//     respuesta).
//   - al alcanzar MaxReprompts intentos inválidos: emite un mensaje de ayuda,
//     reinicia el contador (para no spamear) y permanece en el nodo.
func (Module) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	vars := cloneVars(conv.Vars)
	trimmed := strings.TrimSpace(input)

	if target, ok := node.Options[trimmed]; ok {
		// Opción válida → registrar respuesta y transicionar.
		answers := asMap(vars["answers"])
		answers[node.QuestionID] = trimmed // answer_code = la clave elegida (§10.D).
		vars["answers"] = answers
		delete(vars, RepromptKey) // reiniciar el contador para la siguiente pregunta.
		dest := target
		res := modules.Result{Next: &dest, Vars: vars}
		// DECLARA el efecto de persistir la respuesta (Plan 015 · T3): el módulo
		// sigue PURO —solo lo anota; el runtime lo despacha al PersistSink, que
		// escribe flow_events y proyecta survey_results (la MISMA fila que producía
		// el flush del Plan 014). answer_code = la clave elegida, idéntico a
		// answers[QuestionID]. question_id/answer_code son códigos de negocio, no PII.
		res.Effects = append(res.Effects, modules.Effect{
			Kind:    "persist",
			Name:    "survey_answer",
			Payload: map[string]any{"question_id": node.QuestionID, "answer_code": trimmed},
		})
		return res
	}

	// Opción inválida.
	attempts := getInt(vars, RepromptKey) + 1
	if attempts >= MaxReprompts {
		// Tercer intento inválido: mensaje de ayuda + permanecer + reinicio.
		delete(vars, RepromptKey)
		return modules.Result{Vars: vars, Outputs: []string{helpText(node)}}
	}
	vars[RepromptKey] = attempts
	return modules.Result{Vars: vars, Outputs: []string{invalidText(node)}}
}

func invalidText(node model.Node) string {
	return "Opción no válida. Responde con el número de una de las opciones.\n\n" + node.Prompt
}

func helpText(node model.Node) string {
	return "No logré entender tu respuesta. Por favor elige una de las opciones escribiendo solo su número.\n\n" + node.Prompt
}

// cloneVars copia el mapa de variables para mantener la pureza (no mutar el
// estado de entrada). nil → mapa nuevo.
func cloneVars(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// getInt lee un entero tolerando el tipo que deja un round-trip por JSON
// (float64) además de int/int64.
func getInt(vars map[string]any, key string) int {
	switch v := vars[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
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
