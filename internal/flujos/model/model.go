// Package model define los tipos de la definición de flujo (Flow/Node) y del
// estado conversacional (Conversation), junto con su (de)serialización JSON y
// la validación de esquema.
//
// Esquema B (resuelto 2026-06-26, design.md §10.B): `Nodes` es un mapa
// id→nodo; los tipos de nodo son "menu" (Prompt + Options) y "message"
// (Text + Next). `Next == nil` termina el flujo.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Tipos de nodo soportados en este corte (Menú). Ver design.md §4.
const (
	NodeTypeMenu    = "menu"
	NodeTypeMessage = "message"
)

// NodeTerminal es el valor centinela de Conversation.CurrentNode que marca el
// fin de la conversación (un nodo "message" con Next == nil). No puede colisionar
// con un id real de nodo: la validación rechaza cualquier flujo cuyo mapa de
// nodos contenga esta clave. Ver design.md §6 y el método Finished.
//
// IMPORTANTE: NO usar bytes nulos (0x00) ni de control aquí. Este valor se
// persiste en la columna TEXT flow_state.current_node y PostgreSQL rechaza el
// 0x00 ("invalid byte sequence for encoding UTF8"). El MemoryRepository (mapas Go)
// lo toleraba y enmascaraba el fallo; solo el e2e real contra PostgreSQL lo
// destapó. Mantener un sentinel imprimible.
const NodeTerminal = "__wapp_flow_end__"

// ErrInvalidFlow es el error base (envoltura) de toda definición de flujo que
// no cumple el esquema. Se inspecciona con errors.Is.
var ErrInvalidFlow = errors.New("definición de flujo inválida")

// Flow es la definición declarativa y versionada de un flujo (datos, no
// código; Pieza 05 §3). La unidad persistida es (tenant_id, flow_id, version).
type Flow struct {
	FlowID  string          `json:"flow_id"`
	Version int             `json:"version"`
	Initial string          `json:"initial"`
	Nodes   map[string]Node `json:"nodes"`
}

// Node es un nodo del flujo. Según Type usa unos u otros campos:
//   - "menu":    Prompt + Options (opción→id de nodo destino).
//   - "message": Text + Next (id de nodo siguiente; nil termina el flujo).
type Node struct {
	Type    string            `json:"type"`
	Prompt  string            `json:"prompt,omitempty"`
	Text    string            `json:"text,omitempty"`
	Options map[string]string `json:"options,omitempty"`
	Next    *string           `json:"next,omitempty"`
}

// Conversation es el estado vivo de una conversación ligada a la clave lógica
// (TenantID, SessionID, ContactID) (Pieza 05 §3). ContactID es la identidad
// OPACA del contacto (contacts.contact_id, UUID como texto), NO el JID crudo
// (Plan 010, design.md §1, §3). `Vars` guarda el contador de reprompt
// (design.md §10.E) y variables recolectadas.
type Conversation struct {
	TenantID        string         `json:"tenant_id"`
	SessionID       string         `json:"session_id"`
	ContactID       string         `json:"contact_id"`
	FlowID          string         `json:"flow_id"`
	FlowVersion     int            `json:"flow_version"`
	CurrentNode     string         `json:"current_node"`
	Vars            map[string]any `json:"vars"`
	LastWaMessageID string         `json:"last_wa_message_id,omitempty"`
}

// Finished indica si la conversación llegó al fin del flujo (CurrentNode quedó
// en el centinela NodeTerminal). Un Step sobre una conversación terminada no
// avanza (ver engine.Step).
func (c Conversation) Finished() bool { return c.CurrentNode == NodeTerminal }

// MarshalDefinition serializa una definición de flujo a JSON (cuerpo JSONB).
func MarshalDefinition(f Flow) ([]byte, error) { return json.Marshal(f) }

// UnmarshalDefinition deserializa una definición de flujo desde JSON.
func UnmarshalDefinition(data []byte) (Flow, error) {
	var f Flow
	err := json.Unmarshal(data, &f)
	return f, err
}

// ParseAndValidate deserializa y valida en un paso: rechaza JSON mal formado y
// definiciones que no cumplen el esquema. Es el punto de entrada del handler
// admin (T3) para publicar una definición.
func ParseAndValidate(data []byte) (Flow, error) {
	f, err := UnmarshalDefinition(data)
	if err != nil {
		return Flow{}, fmt.Errorf("%w: JSON mal formado: %w", ErrInvalidFlow, err)
	}
	if err := Validate(f); err != nil {
		return Flow{}, err
	}
	return f, nil
}

// Validate comprueba el esquema de la definición (design.md §4):
//   - flow_id no vacío y version >= 1 (unidad persistida (tenant,flow_id,version));
//   - nodes no vacío y sin la clave centinela NodeTerminal;
//   - initial no vacío y presente en nodes;
//   - cada nodo "menu" tiene Options no vacío y cada destino existe en nodes;
//   - cada nodo "message" con Next != nil apunta a un nodo existente;
//   - el Type de cada nodo está en {menu, message}.
//
// Devuelve errores envueltos sobre ErrInvalidFlow (inspeccionables con errors.Is).
func Validate(f Flow) error {
	if f.FlowID == "" {
		return fmt.Errorf("%w: flow_id vacío", ErrInvalidFlow)
	}
	if f.Version < 1 {
		return fmt.Errorf("%w: version %d inválida (debe ser >= 1)", ErrInvalidFlow, f.Version)
	}
	if len(f.Nodes) == 0 {
		return fmt.Errorf("%w: nodes vacío", ErrInvalidFlow)
	}
	if _, reserved := f.Nodes[NodeTerminal]; reserved {
		return fmt.Errorf("%w: un id de nodo usa la clave reservada de fin de flujo", ErrInvalidFlow)
	}
	if f.Initial == "" {
		return fmt.Errorf("%w: initial vacío", ErrInvalidFlow)
	}
	if _, ok := f.Nodes[f.Initial]; !ok {
		return fmt.Errorf("%w: initial %q no existe en nodes", ErrInvalidFlow, f.Initial)
	}
	for id, n := range f.Nodes {
		switch n.Type {
		case NodeTypeMenu:
			if len(n.Options) == 0 {
				return fmt.Errorf("%w: nodo menu %q sin options", ErrInvalidFlow, id)
			}
			for opt, target := range n.Options {
				if _, ok := f.Nodes[target]; !ok {
					return fmt.Errorf("%w: nodo menu %q: opción %q apunta a nodo inexistente %q",
						ErrInvalidFlow, id, opt, target)
				}
			}
		case NodeTypeMessage:
			if n.Next != nil {
				if _, ok := f.Nodes[*n.Next]; !ok {
					return fmt.Errorf("%w: nodo message %q: next apunta a nodo inexistente %q",
						ErrInvalidFlow, id, *n.Next)
				}
			}
		default:
			return fmt.Errorf("%w: nodo %q: tipo desconocido %q", ErrInvalidFlow, id, n.Type)
		}
	}
	return nil
}
