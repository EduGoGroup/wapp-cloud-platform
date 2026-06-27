// Package modules define el registro de módulos enchufables por tipo de nodo.
//
// Reusa la IDEA del ProcessorRegistry de edugo-worker (ADR-0004,
// copia-adaptación) SIN importar edugo-worker ni arrastrar RabbitMQ: el
// despacho usa concurrencia Go. En T1 la interfaz Module se amplía con la
// validación y el render propios de cada tipo de nodo.
package modules

import "sync"

// Module es un módulo enchufable que maneja un tipo de nodo (p. ej. "menu").
// En T0 solo expone su tipo; T1 añadirá validación/render.
type Module interface {
	// Type devuelve el identificador del tipo de nodo que maneja.
	Type() string
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
// anterior (la validación de duplicados, si se requiere, llega en T1).
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
