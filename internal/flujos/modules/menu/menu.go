// Package menu es el módulo del tipo de nodo "menu" (menú numerado).
//
// Encapsula las tres responsabilidades del nodo de menú (design.md §3): render
// del prompt, validación de la opción tecleada y transición, más el reprompt
// acotado (design.md §10.E: máx 3 intentos inválidos → mensaje de ayuda y
// permanecer; el contador vive en Conversation.Vars y se reinicia al transicionar).
//
// En este corte el Prompt del nodo YA trae las opciones numeradas como texto,
// así que el render es el Prompt tal cual.
package menu

import (
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// RepromptKey es la clave bajo la que se guarda el contador de intentos
// inválidos en Conversation.Vars.
const RepromptKey = "menu_reprompt"

// MaxReprompts es el número de intentos inválidos tras el cual, en lugar de
// repetir el prompt, se envía un mensaje de ayuda y se reinicia el contador
// (se permanece en el nodo). Ver design.md §10.E.
const MaxReprompts = 3

// Module implementa modules.Module para el tipo de nodo "menu".
type Module struct{}

// New crea el módulo Menú.
func New() Module { return Module{} }

// Type devuelve el identificador del tipo de nodo manejado.
func (Module) Type() string { return model.NodeTypeMenu }

// Render devuelve el prompt del menú tal cual (las opciones numeradas ya vienen
// embebidas en el texto del Prompt en este corte).
func (Module) Render(node model.Node) []string {
	return []string{node.Prompt}
}

// Step valida la opción tecleada y decide la transición o el reprompt:
//   - opción válida (coincide, tras TrimSpace, con una clave de Options):
//     transiciona al nodo destino y reinicia el contador de reprompt.
//   - opción inválida con < MaxReprompts intentos: re-emite el prompt precedido
//     de un aviso, e incrementa el contador (permanece en el nodo).
//   - al alcanzar MaxReprompts intentos inválidos: emite un mensaje de ayuda,
//     reinicia el contador (para no spamear) y permanece en el nodo.
func (Module) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	vars := cloneVars(conv.Vars)
	trimmed := strings.TrimSpace(input)

	if target, ok := node.Options[trimmed]; ok {
		// Opción válida → transición. El contador se reinicia al transicionar.
		delete(vars, RepromptKey)
		dest := target
		return modules.Result{Next: &dest, Vars: vars}
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
