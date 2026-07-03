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
	// InsertResults persiste (en lote) las respuestas de una encuesta como datos
	// de negocio EN CLARO en survey_results (Plan 014 §10.D, ADR-0009). El
	// runtime (T3) lo llama al terminar la conversación (flush). len(rows)==0 es
	// un no-op. answer_code NO se cifra: es un código de opción agregable, no PII
	// (la identidad la protege el contact_id opaco, ADR-0010).
	InsertResults(ctx context.Context, rows []SurveyResult) error
	// InsertFlowEvent persiste UN efecto del motor de flujos en el outbox
	// append-only flow_events (Plan 015 · T2, ADR-0009). CERO PII: ContactID es la
	// identidad OPACA (ADR-0010). El Payload (map) se serializa a JSONB; nil → {}.
	InsertFlowEvent(ctx context.Context, ev FlowEvent) error
	// GetTenantContent devuelve el blob JSON crudo de contenido de negocio para
	// (tenantID, ref) de public.tenant_content (Plan 015 · T2). Su firma coincide
	// EXACTAMENTE con content.Store (structural typing): el PostgresRepository lo
	// satisface. Devuelve ErrTenantContentNotFound si la ref no existe.
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// SurveyResult es una respuesta de encuesta lista para persistir EN CLARO en
// survey_results (Plan 014 §10.D). ContactID es la identidad OPACA del contacto
// (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID crudo.
// AnswerCode es el código de la opción elegida (dato de negocio agregable, no
// PII). created_at lo pone el DEFAULT de la tabla.
type SurveyResult struct {
	TenantID    string
	ContactID   string
	FlowID      string
	FlowVersion int
	QuestionID  string
	AnswerCode  string
}

// FlowEvent es un efecto del motor de flujos listo para persistir en el outbox
// append-only flow_events (Plan 015 · T2, ADR-0009). ContactID es la identidad
// OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el
// número/JID crudo. Kind es "persist" | "event"; Name es el nombre lógico del
// efecto (p.ej. "survey_answer"). Payload es el cuerpo de negocio del efecto y se
// serializa a JSONB en el repositorio Postgres (nil → {}). created_at lo pone el
// DEFAULT de la tabla.
type FlowEvent struct {
	TenantID    string
	ContactID   string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	FlowID      string
	FlowVersion int
	Kind        string         // "persist" | "event"
	Name        string         // "survey_answer" | ...
	Payload     map[string]any // se serializa a JSONB en el repo postgres
}

// ErrDefinitionNotFound lo devuelve LatestDefinition cuando no existe ninguna
// versión de la definición para (tenant_id, flow_id). Se inspecciona con
// errors.Is.
var ErrDefinitionNotFound = errors.New("definición de flujo no encontrada")

// ErrTenantContentNotFound lo devuelve GetTenantContent cuando no existe blob de
// contenido para (tenant_id, ref) en tenant_content. Se inspecciona con errors.Is.
var ErrTenantContentNotFound = errors.New("contenido de tenant no encontrado")
