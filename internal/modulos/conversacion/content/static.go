// Porta internal/flujos/content/static.go @ c0c0c03

package content

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Static es el adapter de contenido por DEFECTO y PURO: no toca BD ni red.
// Resuelve el model.Content copiando los campos estáticos del propio nodo
// (Prompt/Options). Es el adapter que aplica cuando el nodo no declara `content`
// o declara source "static"/"inline".
type Static struct{}

// NewStatic construye el adapter estático (sin dependencias).
func NewStatic() Static { return Static{} }

// Resolve produce el Content del propio nodo, sin I/O y sin error posible:
// content.Prompt == node.Prompt y content.Options == node.Options; Items y Raw
// quedan nil. Ignora el contexto y el tenant.
func (Static) Resolve(_ context.Context, _ string, node model.Node) (model.Content, error) {
	return model.Content{Prompt: node.Prompt, Options: node.Options}, nil
}
