package content

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Static es el adapter de contenido por DEFECTO y PURO: no toca BD ni red.
// Resuelve el model.Content copiando los campos estáticos del propio nodo
// (Prompt/Options), retrofit EXACTO del placeholder inline de T0. Es el adapter
// que aplica cuando el nodo no declara `content` o declara source "static"/"inline".
type Static struct{}

// NewStatic construye el adapter estático (sin dependencias).
func NewStatic() Static { return Static{} }

// Resolve produce el Content del propio nodo, sin I/O. El observable es idéntico
// al render previo (content.Prompt == node.Prompt, content.Options == node.Options).
func (Static) Resolve(_ context.Context, _ string, node model.Node) (model.Content, error) {
	return model.Content{Prompt: node.Prompt, Options: node.Options}, nil
}
