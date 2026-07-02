// Package engine es el núcleo PURO de la máquina de estados: dadas una
// definición y un estado, evalúa el nodo actual y produce el estado siguiente
// y las salidas. No conoce transporte ni base de datos (design.md §3).
//
// El engine delega en el módulo registrado para cada tipo de nodo (los nodos
// "menu" los maneja modules/menu: render, validación de opción, transición y
// reprompt acotado). Los nodos "message" son triviales y los encadena el propio
// engine: emite el Text y sigue por Next hasta llegar a un menú (que espera
// input) o a Next == nil (que termina el flujo, marcando CurrentNode con el
// centinela model.NodeTerminal).
package engine

import (
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// Input es la entrada normalizada del usuario. En este corte, el texto del
// IncomingMessage.
type Input struct {
	Text string
}

// Output es una orden de respuesta. En este corte, texto a enviar por SendText.
type Output struct {
	Text string
}

// Engine es la máquina de estados. Mantiene el registro de módulos para delegar
// el manejo de cada tipo de nodo. Es inmutable tras construirse y seguro para
// uso concurrente (no guarda estado por conversación; el estado lo lleva el
// Conversation que recibe cada llamada).
type Engine struct {
	reg *modules.Registry
}

// New construye el engine con el registro de módulos ya poblado.
func New(reg *modules.Registry) *Engine {
	return &Engine{reg: reg}
}

// Enter posiciona la conversación en el nodo inicial del flujo y produce su
// render, encadenando nodos "message" hasta el primer "menu" o el fin
// (design.md §6, Start). No muta el estado recibido (devuelve uno nuevo).
func (e *Engine) Enter(def model.Flow, st model.Conversation) (model.Conversation, []Output, error) {
	st.FlowID = def.FlowID
	st.FlowVersion = def.Version
	st.CurrentNode = def.Initial
	return e.renderFrom(def, st)
}

// Step evalúa el nodo actual con la entrada del usuario (design.md §3):
//   - nodo "menu": delega en el módulo; si transiciona, renderiza el destino
//     encadenando "message" como en Enter; si no, emite el reprompt/ayuda y
//     permanece.
//   - conversación terminada (centinela): ignora la entrada (salida neutra).
//
// No muta el estado recibido.
func (e *Engine) Step(def model.Flow, st model.Conversation, in Input) (model.Conversation, []Output, error) {
	if st.Finished() {
		// Nodo terminal: la entrada se ignora, sin salida (documentado §6).
		return st, nil, nil
	}

	node, ok := def.Nodes[st.CurrentNode]
	if !ok {
		return st, nil, fmt.Errorf("%w: nodo actual %q no existe en la definición", model.ErrInvalidFlow, st.CurrentNode)
	}

	// Delegación genérica: cualquier módulo interactivo (menu, survey_question,
	// …) recorre la misma ruta. Si el nodo actual no tiene módulo registrado o
	// su módulo no espera input (p. ej. "message"), es un estado inconsistente:
	// tras un Enter/renderFrom el estado siempre queda en un nodo interactivo o
	// en el centinela.
	mod, ok := e.reg.Get(node.Type)
	if !ok || !mod.WaitsForInput() {
		return st, nil, fmt.Errorf("%w: nodo actual %q de tipo %q no espera entrada", model.ErrInvalidFlow, st.CurrentNode, node.Type)
	}
	res := mod.Step(node, st, in.Text)
	st.Vars = res.Vars
	if res.Next != nil {
		// Transición válida: renderiza el destino (encadenando messages).
		st.CurrentNode = *res.Next
		return e.renderFrom(def, st)
	}
	// Permanece: reprompt o ayuda.
	return st, toOutputs(res.Outputs), nil
}

// renderFrom produce la salida desde st.CurrentNode: emite los "message"
// encadenados por Next y se detiene al llegar a un "menu" (cuyo render delega
// en el módulo) o a Next == nil (marca el fin con el centinela NodeTerminal).
func (e *Engine) renderFrom(def model.Flow, st model.Conversation) (model.Conversation, []Output, error) {
	var outs []Output
	// Cota de seguridad ante ciclos message→message no detectables por Validate.
	for guard := 0; guard <= len(def.Nodes); guard++ {
		node, ok := def.Nodes[st.CurrentNode]
		if !ok {
			return st, outs, fmt.Errorf("%w: nodo %q no existe en la definición", model.ErrInvalidFlow, st.CurrentNode)
		}
		// El tipo "message" es trivial y NO es un módulo: su lógica (emitir el
		// texto y encadenar por Next o terminar) vive inline aquí. Cualquier
		// otro tipo se delega al módulo registrado; si es interactivo, se
		// renderiza y el flujo se detiene esperando input.
		if node.Type == model.NodeTypeMessage {
			outs = append(outs, Output{Text: node.Text})
			if node.Next == nil {
				st.CurrentNode = model.NodeTerminal
				return st, outs, nil
			}
			st.CurrentNode = *node.Next
			continue
		}

		mod, ok := e.reg.Get(node.Type)
		if !ok || !mod.WaitsForInput() {
			return st, outs, fmt.Errorf("%w: nodo %q: tipo desconocido %q", model.ErrInvalidFlow, st.CurrentNode, node.Type)
		}
		outs = append(outs, toOutputs(mod.Render(node))...)
		return st, outs, nil
	}
	return st, outs, fmt.Errorf("%w: cadena de mensajes demasiado larga (¿ciclo?) desde %q", model.ErrInvalidFlow, st.CurrentNode)
}

func toOutputs(texts []string) []Output {
	if len(texts) == 0 {
		return nil
	}
	outs := make([]Output, 0, len(texts))
	for _, t := range texts {
		outs = append(outs, Output{Text: t})
	}
	return outs
}
