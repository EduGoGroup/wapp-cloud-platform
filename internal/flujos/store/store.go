// Package store define el contrato de persistencia del motor de flujos.
//
// En T0 solo está la interfaz Repository y la clave conversacional Key; las
// implementaciones PostgresRepository(*sql.DB) y MemoryRepository llegan en T2
// (siguiendo el patrón de internal/gateway/lease).
package store

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Key es la clave lógica de una conversación (Pieza 05 §3, design.md §5).
// ContactID es la identidad OPACA del contacto (contacts.contact_id, UUID como
// texto), NO el JID crudo: el motor se clava por contact_id (Plan 010, design.md
// §1, §3). La resolución JID→contact_id la hace el runtime (T4); esta capa opera
// sobre el contact_id ya resuelto.
type Key struct {
	TenantID  string
	SessionID string
	ContactID string
}

// Repository persiste el estado conversacional y las definiciones de flujo
// versionadas. Implementaciones (T2): MemoryRepository (unit CI-safe) y
// PostgresRepository (integración, JSONB vía json.Marshal/Unmarshal).
type Repository interface {
	// Exists indica si ya hay una conversación viva para la clave.
	Exists(ctx context.Context, key Key) (bool, error)
	// Load carga el estado de la conversación; found=false sin error si no hay.
	Load(ctx context.Context, key Key) (state model.Conversation, found bool, err error)
	// Save inserta o actualiza (upsert) el estado de la conversación.
	Save(ctx context.Context, state model.Conversation) error
	// LatestDefinition devuelve la versión vigente de la definición del flujo.
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	// GetDefinition devuelve la definición de una versión EXACTA. El runtime lo
	// usa para avanzar una conversación con la versión con la que arrancó
	// (Conversation.FlowVersion), de modo que publicar una versión nueva no
	// "salte" una conversación en curso (versionado, design.md §4). Devuelve
	// ErrDefinitionNotFound si no existe esa (tenant_id, flow_id, version).
	GetDefinition(ctx context.Context, tenantID, flowID string, version int) (model.Flow, error)
	// InsertDefinition persiste una definición como versión nueva (no muta la
	// vigente; versionado design.md §4). La versión la asigna el repositorio
	// (version = COALESCE(max(version),0)+1 por (tenant_id, flow_id)); el campo
	// f.Version del argumento se ignora. Devuelve la versión asignada.
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
}

// ErrDefinitionNotFound lo devuelve LatestDefinition cuando no existe ninguna
// versión de la definición para (tenant_id, flow_id). Se inspecciona con
// errors.Is.
var ErrDefinitionNotFound = errors.New("definición de flujo no encontrada")
