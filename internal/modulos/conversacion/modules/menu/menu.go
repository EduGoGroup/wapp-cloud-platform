// Porta internal/flujos/modules/menu/menu.go @ 42117b5

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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// RepromptKey es la clave bajo la que se guarda el contador de intentos
// inválidos en Conversation.Vars.
const RepromptKey = "menu_reprompt"

// MaxReprompts es el número de intentos inválidos tras el cual, en lugar de
// repetir el prompt, se envía un mensaje de ayuda y se reinicia el contador
// (se permanece en el nodo). Ver design.md §10.E.
const MaxReprompts = 3

// Module implementa modules.Module para el tipo de nodo "menu". No tiene estado:
// su valor cero es utilizable.
type Module struct{}

// New crea el módulo Menú.
func New() Module { return Module{} }

// Type devuelve el identificador del tipo de nodo manejado: model.NodeTypeMenu.
func (Module) Type() string { return model.NodeTypeMenu }

// WaitsForInput indica que el menú es interactivo: se renderiza y detiene el
// flujo esperando la opción del usuario. Siempre true.
func (Module) WaitsForInput() bool { return true }

// ProducesDurableContent es false: el menú no proyecta ningún contenido durable del cliente (design.md D-054.3(a)).
func (Module) ProducesDurableContent() bool { return false }

// Render devuelve el prompt del menú tal cual (las opciones numeradas ya vienen
// embebidas en el texto del Prompt en este corte). Recibe el contenido YA
// RESUELTO por el engine (Plan 015); en T0 es un placeholder inline con
// content.Prompt == node.Prompt, de observable idéntico.
//
// Siempre UN texto, y es el del contenido resuelto (content.Prompt), no el del
// nodo: con un contenido vacío devuelve [""], nunca nil.
func (Module) Render(_ model.Node, content model.Content) []string {
	return []string{content.Prompt}
}

// Step valida la opción tecleada y decide la transición o el reprompt, sobre la
// base común de opción numerada (modules.NumberedStep, Plan 027 · Ola 2 · T9):
//   - opción válida (coincide, tras TrimSpace, con una clave de Options):
//     transiciona al nodo destino y reinicia el contador de reprompt.
//   - opción inválida con < MaxReprompts intentos: re-emite el prompt precedido
//     de un aviso, e incrementa el contador (permanece en el nodo).
//   - al alcanzar MaxReprompts intentos inválidos: reinicia el contador (para no
//     spamear) y permanece en el nodo; FUERA de un evento emite el mensaje de
//     ayuda, DENTRO de uno arma el menú de salida (modules.ArmExitMenu, D-043.10).
//
// El menú no registra nada extra en la opción válida: solo transiciona (a
// diferencia de la encuesta, que anota la respuesta y declara un efecto). Nunca
// declara efectos, desenlace ni consulta, y no muta conv.Vars: el Result lleva
// una copia. En la opción válida Outputs va vacío (el render del destino lo
// produce el engine).
//
// El prompt que el reprompt y la ayuda re-emiten es node.Prompt —Step no recibe
// el contenido resuelto—, no el content.Prompt que usa Render.
func (Module) Step(node model.Node, conv model.Conversation, input string) modules.Result {
	return modules.NumberedStep(node, conv, input, RepromptKey, MaxReprompts,
		func(vars map[string]any, _ /* choice */, target string) modules.Result {
			dest := target
			return modules.Result{Next: &dest, Vars: vars}
		})
}
