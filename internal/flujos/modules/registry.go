// Package modules define el registro de módulos enchufables por tipo de nodo.
//
// Reusa la IDEA del ProcessorRegistry de edugo-worker (ADR-0004,
// copia-adaptación) SIN importar edugo-worker ni arrastrar RabbitMQ: el
// despacho usa concurrencia Go. El engine delega en el módulo registrado para
// el tipo de nodo (design.md §3): el módulo decide validación de la entrada,
// transición y render; el engine no conoce los detalles del menú.
package modules

import (
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Result es el veredicto puro de un módulo sobre la entrada del usuario en un
// nodo. Usa tipos primitivos (string) en lugar de engine.Output para evitar un
// ciclo de importación (engine → modules); el engine envuelve Outputs en
// []engine.Output.
type Result struct {
	// Next es el id del nodo destino al que transicionar. nil = permanecer en
	// el nodo actual (caso reprompt / ayuda).
	Next *string
	// Outputs son los textos a emitir cuando se permanece en el nodo (reprompt
	// o mensaje de ayuda). En una transición válida va vacío: el render del
	// destino lo produce el engine.
	Outputs []string
	// Vars es el mapa de variables actualizado (incluye el contador de reprompt).
	Vars map[string]any
}

// Module es un módulo enchufable que maneja un tipo de nodo (p. ej. "menu").
type Module interface {
	// Type devuelve el identificador del tipo de nodo que maneja.
	Type() string
	// Render produce los textos a emitir al mostrar el nodo (p. ej. el prompt
	// del menú). Es puro: no depende de transporte ni BD.
	Render(node model.Node) []string
	// Step procesa la entrada del usuario sobre el nodo y devuelve el veredicto
	// (transición o permanencia con reprompt/ayuda). Puro.
	Step(node model.Node, conv model.Conversation, input string) Result
}

// Registry asocia tipos de nodo con su Module. Seguro para uso concurrente.
type Registry struct {
	mu      sync.RWMutex
	modules map[string]Module
}

// NewRegistry crea un registro vacío.
func NewRegistry() *Registry {
	return &Registry{modules: make(map[string]Module)}
}

// Register registra un módulo bajo su Type(). Un Type repetido sobrescribe al
// anterior.
func (r *Registry) Register(m Module) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules[m.Type()] = m
}

// Get devuelve el módulo registrado para el tipo dado y si existe.
func (r *Registry) Get(nodeType string) (Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[nodeType]
	return m, ok
}
