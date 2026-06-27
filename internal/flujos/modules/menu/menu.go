// Package menu es el módulo del tipo de nodo "menu" (menú numerado).
//
// En T0 es un stub que solo declara su tipo para poder registrarse. El render
// del menú, la validación de la opción, la transición y el reprompt acotado
// (design.md §10.E: máx 3 intentos → mensaje de ayuda y permanecer) llegan
// en T1.
package menu

import "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"

// Module implementa modules.Module para el tipo de nodo "menu".
type Module struct{}

// New crea el módulo Menú.
func New() Module { return Module{} }

// Type devuelve el identificador del tipo de nodo manejado.
func (Module) Type() string { return model.NodeTypeMenu }
