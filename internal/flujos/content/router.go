package content

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Router es el adapter de composición que selecciona el Source concreto según
// la fuente declarada por el nodo (node.Content.Source). El engine ve un único
// puerto content.Source; la selección de adapter es responsabilidad de esta
// pieza de composición (hexagonal: el dominio —módulos/engine— no conoce fuentes).
//
// Punto de extensión (Plan 016): http/webhook/caché entrarían como nuevas ramas
// del switch de Resolve componiendo sus propios adapters, sin tocar el engine ni
// los módulos. Aquí NO se implementan (source no soportado ⇒ error controlado).
type Router struct {
	static Static
	json   *JSON
}

// NewRouter compone el Router sobre los adapters concretos (Static PURO + JSON).
func NewRouter(static Static, json *JSON) Router { return Router{static: static, json: json} }

// Resolve elige el adapter por node.Content.Source:
//
//	nil | "" | "static" | "inline"  => Static (PURO, del propio nodo)
//	"json"                          => JSON (lee tenant_content)
//	cualquier otro                  => error controlado (source no soportado)
//
// El switch por fuente vive SOLO aquí: el engine y los módulos siguen sin
// conocer el origen del contenido.
func (r Router) Resolve(ctx context.Context, tenantID string, node model.Node) (model.Content, error) {
	if node.Content == nil {
		return r.static.Resolve(ctx, tenantID, node)
	}
	switch node.Content.Source {
	case "", "static", "inline":
		return r.static.Resolve(ctx, tenantID, node)
	case "json":
		return r.json.Resolve(ctx, tenantID, node)
	default:
		return model.Content{}, fmt.Errorf("%w: content source %q no soportado", model.ErrInvalidFlow, node.Content.Source)
	}
}
