// Package model define los tipos de la definición de flujo (Flow/Node) y del
// estado conversacional (Conversation), junto con su (de)serialización JSON.
//
// Esquema B (resuelto 2026-06-26, design.md §10.B): `Nodes` es un mapa
// id→nodo; los tipos de nodo son "menu" (Prompt + Options) y "message"
// (Text + Next). `Next == nil` termina el flujo.
//
// La VALIDACIÓN de esquema y la lógica de la máquina viven en T1; aquí solo
// están los tipos, sus tags JSON y una firma `Validate` sin lógica real.
package model

import "encoding/json"

// Tipos de nodo soportados en este corte (Menú). Ver design.md §4.
const (
	NodeTypeMenu    = "menu"
	NodeTypeMessage = "message"
)

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
// (TenantID, SessionID, Contact) (Pieza 05 §3). `Vars` guardará en T1 el
// contador de reprompt (design.md §10.E) y variables recolectadas.
type Conversation struct {
	TenantID        string         `json:"tenant_id"`
	SessionID       string         `json:"session_id"`
	Contact         string         `json:"contact"`
	FlowID          string         `json:"flow_id"`
	FlowVersion     int            `json:"flow_version"`
	CurrentNode     string         `json:"current_node"`
	Vars            map[string]any `json:"vars"`
	LastWaMessageID string         `json:"last_wa_message_id,omitempty"`
}

// MarshalDefinition serializa una definición de flujo a JSON (cuerpo JSONB).
func MarshalDefinition(f Flow) ([]byte, error) { return json.Marshal(f) }

// UnmarshalDefinition deserializa una definición de flujo desde JSON.
func UnmarshalDefinition(data []byte) (Flow, error) {
	var f Flow
	err := json.Unmarshal(data, &f)
	return f, err
}

// Validate comprobará el esquema de la definición (initial existe, opciones y
// `next` apuntan a nodos existentes, tipos conocidos por un módulo registrado).
//
// TODO(T1): implementar la validación real. En T0 no tiene lógica.
func Validate(_ Flow) error {
	return nil
}
