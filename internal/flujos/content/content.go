// Package content define el puerto ContentSource del Motor de Flujos: la fuente
// que RESUELVE el model.Content de un nodo ANTES de que el módulo lo renderice
// (primera costura del refactor hexagonal, Plan 015).
//
// El engine no conoce de dónde sale el contenido; delega en una Source. El
// adapter por defecto (Static) es PURO (sin I/O): retrofit exacto del placeholder
// inline de T0, de modo que el engine sigue siendo testeable sin BD. El adapter
// JSON lee un blob por-tenant a través de la interface ContentStore (su respaldo
// Postgres real llega en T2).
//
// Zero-knowledge (ADR-0007): la resolución NUNCA filtra credenciales ni llaves;
// solo trabaja con contenido de negocio.
package content

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Source resuelve el contenido de un nodo a un model.Content ANTES del Render.
//
// static  => PURO (sin I/O): copia los campos estáticos del propio nodo.
// json    => lee un blob por-tenant (ContentStore) y lo deserializa al contrato.
//
// NUNCA filtra credenciales ni PII (zero-knowledge): opera solo sobre contenido
// de negocio.
type Source interface {
	Resolve(ctx context.Context, tenantID string, node model.Node) (model.Content, error)
}
