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
	NodeTypeMenu           = "menu"
	NodeTypeMessage        = "message"
	NodeTypeSurveyQuestion = "survey_question"
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
//
// Content es OPCIONAL (puntero): describe DE DÓNDE sale el contenido a renderizar
// (fuente + referencia). Ausente/nil ⇒ contenido estático inline (Prompt/Options),
// retro-compatible con las definiciones existentes. Es la primera costura del
// refactor hexagonal del Motor de Flujos (Plan 015): en T0 solo se abre la firma;
// la resolución real por fuente llega en T1.
type Node struct {
	Type       string            `json:"type"`
	Prompt     string            `json:"prompt,omitempty"`
	Text       string            `json:"text,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
	Next       *string           `json:"next,omitempty"`
	QuestionID string            `json:"question_id,omitempty"`
	Content    *ContentRef       `json:"content,omitempty"`
}

// Content es el contenido RESUELTO que un módulo renderiza en un nodo (tipos
// PUROS, sin dependencias externas). Es la vista que el engine entrega a
// Module.Render tras resolver la fuente (Plan 015): Prompt/Options para nodos
// interactivos, Items para catálogos (pedido) y Raw para el resto de la carga
// específica de la fuente. En T0 el engine lo construye como un placeholder
// inline (copia de Prompt/Options del nodo); la resolución real llega en T1.
type Content struct {
	Prompt  string
	Options map[string]string
	Items   []ContentItem
	Raw     map[string]any `json:"-"`
}

// ContentItem es un ítem de catálogo (p. ej. una línea del menú de un pedido):
// código de selección, SKU, etiqueta y precio. Tipo PURO de dominio (Plan 015).
type ContentItem struct {
	Code  string  `json:"code"`
	SKU   string  `json:"sku"`
	Label string  `json:"label"`
	Price float64 `json:"price"`
}

// ContentRef es la referencia declarativa a la fuente del contenido de un nodo
// (Plan 015): Source indica el origen ("static" | "inline" | "json") y Ref la
// clave/identificador dentro de esa fuente. Vive en la definición del flujo
// (Node.Content) y la resuelve el engine a un Content antes de renderizar.
type ContentRef struct {
	Source string `json:"source"` // "static" | "inline" | "json"
	Ref    string `json:"ref"`

	// Descriptor INLINE del nodo "media" (Plan 017 §4.1/§9.B): cuando el nodo es
	// {"type":"media","content":{"source":"static", …}}, estos campos viajan en el
	// MISMO objeto `content` y el módulo media los lee para construir un MediaRef.
	// Son ADITIVOS y OPCIONALES (omitempty): los nodos menu/message/survey/cart no
	// los declaran ⇒ retro-compatibles, sin tocar el adapter static ni el Router
	// (el descriptor no pasa por model.Content; el módulo lo lee del `node`).
	Key      string `json:"key,omitempty"`
	Filename string `json:"filename,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Caption  string `json:"caption,omitempty"`
}

// MediaRef es el descriptor PURO de un archivo a enviar por un nodo "media"
// (Plan 017 §4.1): identifica el objeto en el almacén (Key) y los metadatos que
// el Edge fija en WhatsApp (Filename/Mime/Kind/Caption). Es un tipo de DOMINIO
// neutral (vive en model, hoja del grafo de imports del Motor) para que el módulo
// media lo DECLARE y el engine lo transporte OPACO en Output.Media SIN que el
// engine importe el paquete del módulo (dirección hexagonal, §9.C).
//
// PURO: no lleva URL ni credenciales. El runtime presigna la Key (T4) y el Edge
// descarga y sube el binario; el módulo solo describe QUÉ archivo mandar.
type MediaRef struct {
	Key      string // key del objeto en el almacén (p. ej. "wapp/media/lista-precios.pdf")
	Filename string // nombre que verá el usuario en WhatsApp
	Mime     string // "application/pdf", "image/png", …
	Kind     string // "document" | "image" (mapea a MediaKind del proto, T4)
	Caption  string // texto que ACOMPAÑA al archivo en el MISMO mensaje (§9.I)
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
//
// moduleTypes son los tipos de nodo que aportan los MÓDULOS enchufables (Registry):
// nodos de esos tipos se aceptan de forma LAXA (la validación profunda del contenido
// la hace el módulo en runtime). Así el modelo NO se acopla a los módulos concretos
// (los tipos se inyectan como strings, evitando el ciclo model→modules).
func ParseAndValidate(data []byte, moduleTypes ...string) (Flow, error) {
	f, err := UnmarshalDefinition(data)
	if err != nil {
		return Flow{}, fmt.Errorf("%w: JSON mal formado: %w", ErrInvalidFlow, err)
	}
	if err := Validate(f, moduleTypes...); err != nil {
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
//   - el Type de cada nodo es un tipo CORE (menu, message, survey_question) o un
//     tipo de MÓDULO declarado en moduleTypes (Registry), que se acepta laxo.
//
// moduleTypes son los tipos de nodo registrados por los módulos enchufables
// (p. ej. "cart"): un nodo de ese tipo pasa la validación sin exigir el esquema
// de los tipos core (options/question_id); su contenido lo valida el módulo en
// runtime. El rechazo de "tipo desconocido" se conserva para todo lo que no sea
// ni core ni de módulo (protege contra typos).
//
// Devuelve errores envueltos sobre ErrInvalidFlow (inspeccionables con errors.Is).
func Validate(f Flow, moduleTypes ...string) error {
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
	mods := make(map[string]struct{}, len(moduleTypes))
	for _, t := range moduleTypes {
		mods[t] = struct{}{}
	}
	for id, n := range f.Nodes {
		if err := validateNode(f, id, n, mods); err != nil {
			return err
		}
	}
	return nil
}

// validateNode valida un nodo individual según su Type (extraído de Validate
// para mantener acotada la complejidad ciclomática). Los tipos interactivos
// (menu, survey_question) comparten la validación de options→destino existente;
// survey_question exige además question_id. Un tipo que no es core pero sí está
// en moduleTypes (Registry) se acepta LAXO (lo valida el módulo en runtime); solo
// se rechaza como "tipo desconocido" lo que no es ni core ni de módulo.
func validateNode(f Flow, id string, n Node, moduleTypes map[string]struct{}) error {
	switch n.Type {
	case NodeTypeMenu:
		return validateOptions(f, id, "menu", n.Options)
	case NodeTypeSurveyQuestion:
		if n.QuestionID == "" {
			return fmt.Errorf("%w: nodo %q survey sin question_id", ErrInvalidFlow, id)
		}
		return validateOptions(f, id, "survey", n.Options)
	case NodeTypeMessage:
		if n.Next != nil {
			if _, ok := f.Nodes[*n.Next]; !ok {
				return fmt.Errorf("%w: nodo message %q: next apunta a nodo inexistente %q",
					ErrInvalidFlow, id, *n.Next)
			}
		}
		return nil
	default:
		if _, ok := moduleTypes[n.Type]; ok {
			// Tipo manejado por un módulo enchufable (p. ej. "cart"): validación
			// laxa; el módulo valida su contenido en runtime.
			return nil
		}
		return fmt.Errorf("%w: nodo %q: tipo desconocido %q", ErrInvalidFlow, id, n.Type)
	}
}

// validateOptions comprueba que un nodo interactivo tenga options no vacío y que
// cada destino exista en la definición. kind es la etiqueta del tipo para el
// mensaje de error (p. ej. "menu", "survey").
func validateOptions(f Flow, id, kind string, options map[string]string) error {
	if len(options) == 0 {
		return fmt.Errorf("%w: nodo %s %q sin options", ErrInvalidFlow, kind, id)
	}
	for opt, target := range options {
		if _, ok := f.Nodes[target]; !ok {
			return fmt.Errorf("%w: nodo %s %q: opción %q apunta a nodo inexistente %q",
				ErrInvalidFlow, kind, id, opt, target)
		}
	}
	return nil
}
