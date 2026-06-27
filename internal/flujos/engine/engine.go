// Package engine es el núcleo PURO de la máquina de estados: dadas una
// definición y un estado, evalúa el nodo actual y produce el estado siguiente
// y las salidas. No conoce transporte ni base de datos (design.md §3).
//
// En T0 solo están las firmas con cuerpos mínimos que compilan; la lógica real
// (render del menú, validación de opción, transición, reprompt acotado) llega
// en T1.
package engine

import "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"

// Input es la entrada normalizada del usuario. En este corte, el texto del
// IncomingMessage.
type Input struct {
	Text string
}

// Output es una orden de respuesta. En este corte, texto a enviar por SendText.
type Output struct {
	Text string
}

// Enter renderiza el nodo inicial del flujo al abrir la conversación
// (design.md §6, Start). Cuerpo mínimo en T0; lógica real en T1.
func Enter(_ model.Flow, st model.Conversation) (model.Conversation, []Output, error) {
	// TODO(T1): render del nodo inicial (el menú).
	return st, nil, nil
}

// Step evalúa el nodo actual con la entrada del usuario y devuelve el estado
// siguiente y las salidas (design.md §3). Cuerpo mínimo en T0; lógica real
// (delegando en el módulo del tipo de nodo) en T1.
func Step(_ model.Flow, st model.Conversation, _ Input) (model.Conversation, []Output, error) {
	// TODO(T1): evaluar nodo, validar opción, transición y reprompt acotado.
	return st, nil, nil
}
