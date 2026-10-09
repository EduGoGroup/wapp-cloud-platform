// Porta internal/flujos/content/content.go @ c0c0c03

// Package content define el puerto Source del Motor de Flujos: la fuente que
// RESUELVE el model.Content de un nodo ANTES de que el módulo lo renderice
// (primera costura del refactor hexagonal, Plan 015).
//
// El engine no conoce de dónde sale el contenido; delega en una Source. El
// adapter por defecto (Static) es PURO (sin I/O), de modo que el engine sigue
// siendo testeable sin BD. El adapter JSON lee un blob por-tenant a través de la
// interface Store. El Router compone los dos y elige por nodo.
//
// Zero-knowledge (ADR-0007): la resolución NUNCA filtra credenciales ni llaves;
// solo trabaja con contenido de negocio.
package content

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Source resuelve el contenido de un nodo a un model.Content ANTES del Render.
//
// static  => PURO (sin I/O): copia los campos estáticos del propio nodo.
// json    => lee un blob por-tenant (Store) y lo deserializa al contrato.
//
// Las tres implementaciones del paquete (Static, *JSON y Router) lo cumplen. Un
// fallo es siempre un error controlado envuelto en model.ErrInvalidFlow, nunca
// un pánico.
//
// NUNCA filtra credenciales ni PII (zero-knowledge): opera solo sobre contenido
// de negocio.
type Source interface {
	Resolve(ctx context.Context, tenantID string, node model.Node) (model.Content, error)
}
