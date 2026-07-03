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
	// Effects son los efectos de lado que el módulo DECLARA (puro: no los
	// ejecuta) para que el runtime los despache (persistir una respuesta, emitir
	// un evento, …). Es la segunda costura del refactor hexagonal (Plan 015). En
	// T0 nadie los emite (siempre vacío); el dispatch llega en T2.
	Effects []Effect
}

// VarContentRaw es la clave de Conversation.Vars bajo la que el engine EXPONE el
// blob crudo (model.Content.Raw) del contenido resuelto de un nodo ANTES de
// llamar a Step, para que un módulo cuya sub-máquina navega en Step —que NO
// recibe el content resuelto, a diferencia de Render— pueda leer datos de dominio
// sin hacer I/O (Plan 016 · T2, design.md §4.1). Solo se siembra si
// Content.Raw != nil: menú/encuesta usan contenido static (Raw nil) y NO la ven
// (sin regresión). Es una clave del contrato engine↔módulos, no del dominio cart.
const VarContentRaw = "cart_catalog"

// Effect es un efecto de lado DECLARADO por un módulo para que lo despache el
// runtime (Plan 015). Kind clasifica el efecto (p. ej. "persist", "emit"), Name
// lo nombra dentro de su clase (p. ej. "survey_answer") y Payload lleva los
// datos. Tipo PURO: los módulos lo producen, nunca lo ejecutan.
type Effect struct {
	Kind    string
	Name    string
	Payload map[string]any
}

// Module es un módulo enchufable que maneja un tipo de nodo (p. ej. "menu").
type Module interface {
	// Type devuelve el identificador del tipo de nodo que maneja.
	Type() string
	// Render produce los textos a emitir al mostrar el nodo (p. ej. el prompt
	// del menú), a partir del contenido YA RESUELTO que le entrega el engine. Es
	// puro: no depende de transporte ni BD ni conoce la fuente del contenido
	// (Plan 015: el engine resuelve Node.Content a model.Content antes de llamar).
	Render(node model.Node, content model.Content) []string
	// Step procesa la entrada del usuario sobre el nodo y devuelve el veredicto
	// (transición o permanencia con reprompt/ayuda). Puro.
	Step(node model.Node, conv model.Conversation, input string) Result
	// WaitsForInput indica si el nodo manejado por el módulo es interactivo: se
	// renderiza y detiene el flujo a la espera de la entrada del usuario (menu,
	// survey_question). El engine lo consulta para delegar Render/Step sin
	// cablear tipos concretos.
	WaitsForInput() bool
}

// MediaEmitter es la capacidad OPCIONAL de un módulo de SALIDA (no interactivo,
// WaitsForInput()==false) para DECLARAR un adjunto (model.MediaRef) además del
// texto de Render, de modo que el runtime lo presigne y despache por
// Sender.SendMedia en vez de SendText (Plan 017 §9.C).
//
// El engine la consulta por ASERCIÓN DE CAPACIDAD (mod.(MediaEmitter)), NO por
// node.Type: cualquier módulo que la implemente participa del canal de archivos y
// el engine sigue GENÉRICO (sin switch por tipo). PURO: el módulo PARSEA y DECLARA
// el descriptor (Key/Filename/Mime/Kind/Caption); no presigna, no hace red, no
// conoce el almacén ni la URL. Un descriptor inválido produce un error controlado.
type MediaEmitter interface {
	EmitMedia(node model.Node, content model.Content) (*model.MediaRef, error)
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

// Types devuelve los tipos de nodo actualmente registrados (orden no garantizado).
// Lo consume la validación de esquema del alta admin (model.Validate) para aceptar
// de forma LAXA los nodos manejados por un módulo enchufable (p. ej. "cart"): el
// modelo NO conoce los módulos concretos (evita el ciclo model→modules), los tipos
// se le INYECTAN como strings.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.modules))
	for t := range r.modules {
		types = append(types, t)
	}
	return types
}

// WaitsForInput indica si el tipo dado está registrado y su módulo es
// interactivo (espera entrada del usuario). Un tipo no registrado devuelve
// false. Lo usa el engine para decidir si un nodo detiene el flujo esperando
// input, sin cablear tipos concretos.
func (r *Registry) WaitsForInput(typ string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[typ]
	return ok && m.WaitsForInput()
}
