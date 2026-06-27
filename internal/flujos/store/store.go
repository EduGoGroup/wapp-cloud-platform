// Package store define el contrato de persistencia del motor de flujos.
//
// En T0 solo está la interfaz Repository y la clave conversacional Key; las
// implementaciones PostgresRepository(*sql.DB) y MemoryRepository llegan en T2
// (siguiendo el patrón de internal/gateway/lease).
package store

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Key es la clave lógica de una conversación (Pieza 05 §3, design.md §5).
type Key struct {
	TenantID  string
	SessionID string
	Contact   string
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
	// InsertDefinition persiste una definición como versión nueva (no muta la
	// vigente; versionado design.md §4).
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) error
}
