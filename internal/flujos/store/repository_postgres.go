package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// PostgresRepository implementa Repository con SQL raw sobre public.flow_state y
// public.flow_definitions. Los cuerpos flexibles (vars del estado, definition
// del flujo) viajan como JSONB y se (de)serializan con json.Marshal/Unmarshal
// ↔ []byte.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository construye el repositorio sobre el pool dado.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// Exists indica si ya hay una conversación viva para la clave.
func (r *PostgresRepository) Exists(ctx context.Context, key Key) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM public.flow_state
			WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
		)
	`, key.TenantID, key.SessionID, key.ContactID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: exists estado: %w", err)
	}
	return exists, nil
}

// Load carga el estado de la conversación; found=false sin error si no hay.
func (r *PostgresRepository) Load(ctx context.Context, key Key) (model.Conversation, bool, error) {
	var (
		c       model.Conversation
		varsRaw []byte
		lastWa  sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, session_id, contact_id::text, flow_id, flow_version,
		       current_node, vars, last_wa_message_id
		FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID).Scan(
		&c.TenantID, &c.SessionID, &c.ContactID, &c.FlowID, &c.FlowVersion,
		&c.CurrentNode, &varsRaw, &lastWa,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model.Conversation{}, false, nil
	case err != nil:
		return model.Conversation{}, false, fmt.Errorf("store: leer estado: %w", err)
	}
	if lastWa.Valid {
		c.LastWaMessageID = lastWa.String
	}
	if len(varsRaw) > 0 {
		if err := json.Unmarshal(varsRaw, &c.Vars); err != nil {
			return model.Conversation{}, false, fmt.Errorf("store: deserializar vars: %w", err)
		}
	}
	return c, true, nil
}

// Save inserta o actualiza (upsert) el estado de la conversación. updated_at se
// fija a now() en cada escritura.
func (r *PostgresRepository) Save(ctx context.Context, state model.Conversation) error {
	vars := state.Vars
	if vars == nil {
		vars = map[string]any{}
	}
	varsRaw, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("store: serializar vars: %w", err)
	}
	var lastWa sql.NullString
	if state.LastWaMessageID != "" {
		lastWa = sql.NullString{String: state.LastWaMessageID, Valid: true}
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars, last_wa_message_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (tenant_id, session_id, contact_id) DO UPDATE
		SET flow_id = EXCLUDED.flow_id,
		    flow_version = EXCLUDED.flow_version,
		    current_node = EXCLUDED.current_node,
		    vars = EXCLUDED.vars,
		    last_wa_message_id = EXCLUDED.last_wa_message_id,
		    updated_at = now()
	`, state.TenantID, state.SessionID, state.ContactID, state.FlowID, state.FlowVersion,
		state.CurrentNode, varsRaw, lastWa)
	if err != nil {
		return fmt.Errorf("store: upsert estado: %w", err)
	}
	return nil
}

// LatestDefinition devuelve la definición de la mayor version para (tenant, flow).
// Devuelve ErrDefinitionNotFound si no existe ninguna versión.
func (r *PostgresRepository) LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error) {
	var (
		defRaw  []byte
		version int
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT version, definition
		FROM public.flow_definitions
		WHERE tenant_id = $1 AND flow_id = $2
		ORDER BY version DESC
		LIMIT 1
	`, tenantID, flowID).Scan(&version, &defRaw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model.Flow{}, fmt.Errorf("%w: tenant=%s flow=%s", ErrDefinitionNotFound, tenantID, flowID)
	case err != nil:
		return model.Flow{}, fmt.Errorf("store: leer definición: %w", err)
	}
	f, err := model.UnmarshalDefinition(defRaw)
	if err != nil {
		return model.Flow{}, fmt.Errorf("store: deserializar definición: %w", err)
	}
	// La columna version es la autoritativa (la asigna InsertDefinition); el
	// version embebido en el JSONB puede ser obsoleto.
	f.Version = version
	return f, nil
}

// GetDefinition devuelve la definición de la versión EXACTA indicada para
// (tenant, flow). ErrDefinitionNotFound si no existe esa versión.
func (r *PostgresRepository) GetDefinition(ctx context.Context, tenantID, flowID string, version int) (model.Flow, error) {
	var defRaw []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT definition
		FROM public.flow_definitions
		WHERE tenant_id = $1 AND flow_id = $2 AND version = $3
	`, tenantID, flowID, version).Scan(&defRaw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model.Flow{}, fmt.Errorf("%w: tenant=%s flow=%s version=%d", ErrDefinitionNotFound, tenantID, flowID, version)
	case err != nil:
		return model.Flow{}, fmt.Errorf("store: leer definición por versión: %w", err)
	}
	f, err := model.UnmarshalDefinition(defRaw)
	if err != nil {
		return model.Flow{}, fmt.Errorf("store: deserializar definición: %w", err)
	}
	// La columna version es la autoritativa (la asigna InsertDefinition).
	f.Version = version
	return f, nil
}

// InsertDefinition persiste la definición como versión nueva: asigna
// version = COALESCE(max(version),0)+1 por (tenant_id, flow_id) de forma atómica
// y devuelve la versión asignada. El campo f.Version del argumento se ignora.
func (r *PostgresRepository) InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (int, error) {
	defRaw, err := model.MarshalDefinition(f)
	if err != nil {
		return 0, fmt.Errorf("store: serializar definición: %w", err)
	}
	var version int
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO public.flow_definitions (tenant_id, flow_id, version, definition)
		SELECT $1, $2, COALESCE(MAX(version), 0) + 1, $3::jsonb
		FROM public.flow_definitions
		WHERE tenant_id = $1 AND flow_id = $2
		RETURNING version
	`, tenantID, f.FlowID, defRaw).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: insertar definición: %w", err)
	}
	return version, nil
}
