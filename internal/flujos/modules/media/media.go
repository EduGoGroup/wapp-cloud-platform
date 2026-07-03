// Package media es el módulo de SALIDA que envía un ARCHIVO (PDF, imagen) por
// WhatsApp desde un nodo del flujo (Plan 017, design.md §4.1). Es el cuarto módulo
// del Motor tras Menú (Plan 006), Encuesta (Plan 014) y Carrito (Plan 016), y el
// primero NO interactivo: no espera respuesta del usuario (WaitsForInput()==false);
// emite el adjunto y el engine AVANZA por Next (como un "message").
//
// PUREZA (invariante, design.md §4.1/§9.C): el módulo NO presigna, NO hace red, NO
// persiste, NO conoce R2 ni la URL. Solo PARSEA y DECLARA un model.MediaRef
// (Key/Filename/Mime/Kind/Caption) desde el descriptor INLINE del nodo. El runtime
// (T4) presigna la Key y despacha por Sender.SendMedia; el Edge descarga y sube el
// binario. El módulo solo describe QUÉ archivo mandar.
//
// DESCRIPTOR INLINE (§9.B): el descriptor viaja en el objeto `content` del nodo
//
//	{"type":"media","content":{"source":"static","key":…,"filename":…,"mime":…,
//	 "kind":…,"caption":…}}
//
// es decir en model.ContentRef (campos aditivos). El adapter static NO transporta
// esos campos a model.Content (solo Prompt/Options), así que el módulo los lee del
// `node` que recibe. Sin migración ni tenant_content: es "static inline" puro.
package media

import (
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// NodeTypeMedia es el tipo de nodo que maneja este módulo.
const NodeTypeMedia = "media"

// Kinds soportados. Mapean a MediaKind del proto en el gateway (T4): "document" →
// DocumentMessage, "image" → ImageMessage. El Mime por sí solo no basta para elegir
// la rama (design.md §6), por eso el descriptor lleva Kind explícito.
const (
	KindDocument = "document"
	KindImage    = "image"
)

// Module implementa modules.Module y modules.MediaEmitter para el tipo "media".
type Module struct{}

// New crea el módulo Media (sin configuración).
func New() Module { return Module{} }

// Type devuelve el identificador del tipo de nodo manejado.
func (Module) Type() string { return NodeTypeMedia }

// WaitsForInput indica que el nodo media NO es interactivo: emite el archivo y el
// engine avanza por Next sin detenerse a esperar entrada (design.md §9.A).
func (Module) WaitsForInput() bool { return false }

// Render no produce texto propio: el texto descriptivo viaja EMBEBIDO en el
// MediaRef.Caption (UN solo mensaje, design.md §9.I), no como un mensaje de texto
// suelto. El engine lo llama por contrato (modules.Module); devuelve nil.
func (Module) Render(_ model.Node, _ model.Content) []string { return nil }

// Step no se usa en el camino real: el nodo media no espera input
// (WaitsForInput()==false), así que el engine nunca deja la conversación posada en
// él ni lo invoca. Se implementa por contrato (modules.Module) con una permanencia
// neutra (sin transición ni efectos).
func (Module) Step(_ model.Node, conv model.Conversation, _ string) modules.Result {
	return modules.Result{Vars: conv.Vars}
}

// EmitMedia PARSEA el descriptor inline del nodo (node.Content) a un model.MediaRef
// (capacidad modules.MediaEmitter, design.md §9.C). Un descriptor ausente o
// incompleto produce un ERROR CONTROLADO (envuelto en model.ErrInvalidFlow), nunca
// un pánico.
func (Module) EmitMedia(node model.Node, _ model.Content) (*model.MediaRef, error) {
	return parseMediaRef(node)
}

// parseMediaRef valida y construye el MediaRef desde el descriptor inline del nodo.
// Exige node.Content presente con Key, Filename y Mime no vacíos y un Kind
// reconocido (document|image); Caption es OPCIONAL (puede llevar espacios/emoji a
// propósito, no se recorta). Devuelve error controlado (model.ErrInvalidFlow) ante
// cualquier ausencia: el módulo NO adivina.
func parseMediaRef(node model.Node) (*model.MediaRef, error) {
	if node.Content == nil {
		return nil, fmt.Errorf("%w: nodo media sin content (descriptor inline requerido)", model.ErrInvalidFlow)
	}
	ref := model.MediaRef{
		Key:      strings.TrimSpace(node.Content.Key),
		Filename: strings.TrimSpace(node.Content.Filename),
		Mime:     strings.TrimSpace(node.Content.Mime),
		Kind:     strings.TrimSpace(node.Content.Kind),
		Caption:  node.Content.Caption,
	}
	switch {
	case ref.Key == "":
		return nil, fmt.Errorf("%w: nodo media sin key", model.ErrInvalidFlow)
	case ref.Filename == "":
		return nil, fmt.Errorf("%w: nodo media sin filename", model.ErrInvalidFlow)
	case ref.Mime == "":
		return nil, fmt.Errorf("%w: nodo media sin mime", model.ErrInvalidFlow)
	case ref.Kind != KindDocument && ref.Kind != KindImage:
		return nil, fmt.Errorf("%w: nodo media con kind %q inválido (document|image)", model.ErrInvalidFlow, ref.Kind)
	}
	return &ref, nil
}
