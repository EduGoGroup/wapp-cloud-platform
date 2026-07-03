package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// surveyResultCols es el número de columnas por fila que escribe InsertResults
// (orden de survey_results salvo id y created_at, que usan sus DEFAULT).
const surveyResultCols = 6

// orderItemCols es el número de columnas por fila que escribe InsertOrderItems
// (orden de order_items salvo id y added_at, que usan sus DEFAULT).
const orderItemCols = 5

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

// ListDefinitions devuelve el resumen (flow_id, última versión, alta) de cada
// flujo publicado por el tenant, ordenado por flow_id (Plan 018 · T5). Acota SIEMPRE
// por tenant_id (INV-8): un tenant NUNCA ve los flujos de otro. DISTINCT ON toma la
// fila de mayor versión por flow_id (la vigente). Lista vacía sin error si el tenant
// no tiene flujos.
func (r *PostgresRepository) ListDefinitions(ctx context.Context, tenantID string) (out []FlowSummary, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT ON (flow_id) flow_id, version, created_at
		FROM public.flow_definitions
		WHERE tenant_id = $1
		ORDER BY flow_id, version DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: listar definiciones: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("store: cerrar filas: %w", cerr)
		}
	}()

	out = make([]FlowSummary, 0)
	for rows.Next() {
		var fs FlowSummary
		if scanErr := rows.Scan(&fs.FlowID, &fs.Version, &fs.CreatedAt); scanErr != nil {
			return nil, fmt.Errorf("store: escanear resumen de definición: %w", scanErr)
		}
		out = append(out, fs)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("store: iterar definiciones: %w", rowsErr)
	}
	return out, nil
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

// InsertResults persiste en lote las respuestas de encuesta EN CLARO en
// survey_results (Plan 014 §10.D, ADR-0009). Un solo INSERT multi-fila con
// placeholders; created_at usa el DEFAULT now() de la tabla. len(rows)==0 es un
// no-op.
func (r *PostgresRepository) InsertResults(ctx context.Context, rows []SurveyResult) error {
	if len(rows) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*surveyResultCols)
	for i, row := range rows {
		base := i * surveyResultCols
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6,
		))
		args = append(args,
			row.TenantID, row.ContactID, row.FlowID, row.FlowVersion, row.QuestionID, row.AnswerCode,
		)
	}
	// #nosec G202 -- solo se concatenan placeholders generados ($1, $2, ...); los
	// valores viajan siempre parametrizados en args, nunca interpolados en el SQL.
	query := `
		INSERT INTO survey_results
			(tenant_id, contact_id, flow_id, flow_version, question_id, answer_code)
		VALUES ` + strings.Join(placeholders, ", ")
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: insertar resultados de encuesta: %w", err)
	}
	return nil
}

// InsertFlowEvent persiste UN efecto del motor en el outbox append-only
// flow_events (Plan 015 · T2, ADR-0009). El Payload viaja como JSONB serializado
// con json.Marshal ↔ []byte (mismo patrón que vars/definition); Payload nil se
// materializa como '{}'. created_at usa el DEFAULT now() de la tabla.
func (r *PostgresRepository) InsertFlowEvent(ctx context.Context, ev FlowEvent) error {
	payload := ev.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("store: serializar payload de efecto: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO public.flow_events
			(tenant_id, contact_id, flow_id, flow_version, kind, name, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, ev.TenantID, ev.ContactID, ev.FlowID, ev.FlowVersion, ev.Kind, ev.Name, payloadRaw)
	if err != nil {
		return fmt.Errorf("store: insertar efecto de flujo: %w", err)
	}
	return nil
}

// GetTenantContent devuelve el blob JSON crudo de public.tenant_content para
// (tenantID, ref) (Plan 015 · T2). Firma EXACTA de content.Store (structural
// typing). Devuelve ErrTenantContentNotFound si la ref no existe. Cero pánico.
func (r *PostgresRepository) GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error) {
	var content []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT content
		FROM public.tenant_content
		WHERE tenant_id = $1 AND ref = $2
	`, tenantID, ref).Scan(&content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w: tenant=%s ref=%s", ErrTenantContentNotFound, tenantID, ref)
	case err != nil:
		return nil, fmt.Errorf("store: leer contenido de tenant: %w", err)
	}
	return content, nil
}

// UpsertOrder inserta o actualiza (upsert por id) la orden en public.orders
// (Plan 016 · T0/T2). Idempotente por o.ID. ExpiresAt zero se materializa como
// NULL. created_at/updated_at usan now() (updated_at se refresca en el UPDATE).
func (r *PostgresRepository) UpsertOrder(ctx context.Context, o Order) error {
	var expires sql.NullTime
	if !o.ExpiresAt.IsZero() {
		expires = sql.NullTime{Time: o.ExpiresAt, Valid: true}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.orders
			(id, tenant_id, contact_id, session_id, status, total, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), now())
		ON CONFLICT (id) DO UPDATE
		SET tenant_id  = EXCLUDED.tenant_id,
		    contact_id = EXCLUDED.contact_id,
		    session_id = EXCLUDED.session_id,
		    status     = EXCLUDED.status,
		    total      = EXCLUDED.total,
		    expires_at = EXCLUDED.expires_at,
		    updated_at = now()
	`, o.ID, o.TenantID, o.ContactID, o.SessionID, o.Status, o.Total, expires)
	if err != nil {
		return fmt.Errorf("store: upsert orden: %w", err)
	}
	return nil
}

// InsertOrderItems persiste en lote las líneas de una orden en public.order_items
// (Plan 016 · T0/T2). Un solo INSERT multi-fila con placeholders; added_at usa el
// DEFAULT now() de la tabla. len(items)==0 es un no-op.
func (r *PostgresRepository) InsertOrderItems(ctx context.Context, orderID string, items []OrderItem) error {
	if len(items) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(items))
	args := make([]any, 0, len(items)*orderItemCols)
	for i, it := range items {
		base := i * orderItemCols
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5,
		))
		args = append(args, orderID, it.SKU, it.Label, it.Qty, it.UnitPrice)
	}
	// #nosec G202 -- solo se concatenan placeholders generados ($1, $2, ...); los
	// valores viajan siempre parametrizados en args, nunca interpolados en el SQL.
	query := `
		INSERT INTO public.order_items
			(order_id, sku, label, qty, unit_price)
		VALUES ` + strings.Join(placeholders, ", ")
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: insertar líneas de orden: %w", err)
	}
	return nil
}

// GetOpenOrder devuelve la orden "open" del contacto para (tenantID, contactID);
// found=false sin error si no hay (Plan 016 · T2/T3). Usa el índice orders_open_idx.
func (r *PostgresRepository) GetOpenOrder(ctx context.Context, tenantID, contactID string) (Order, bool, error) {
	var (
		o       Order
		expires sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, tenant_id, contact_id, session_id, status, total,
		       created_at, updated_at, expires_at
		FROM public.orders
		WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID, contactID).Scan(
		&o.ID, &o.TenantID, &o.ContactID, &o.SessionID, &o.Status, &o.Total,
		&o.CreatedAt, &o.UpdatedAt, &expires,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Order{}, false, nil
	case err != nil:
		return Order{}, false, fmt.Errorf("store: leer orden abierta: %w", err)
	}
	if expires.Valid {
		o.ExpiresAt = expires.Time
	}
	return o, true, nil
}

// MarkOrderStatus transiciona el estado de una orden (por id) y fija su total,
// refrescando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled" |
// "expired".
func (r *PostgresRepository) MarkOrderStatus(ctx context.Context, orderID, status string, total float64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.orders
		SET status = $2, total = $3, updated_at = now()
		WHERE id = $1
	`, orderID, status, total)
	if err != nil {
		return fmt.Errorf("store: marcar estado de orden: %w", err)
	}
	return nil
}

// GetTenantSettings devuelve la config del carrito para tenantID desde
// public.tenant_settings (Plan 016 · T0). Si el tenant no tiene fila, devuelve los
// DEFAULTS (DefaultPageSize, DefaultOrderTTL) SIN error (design.md §9.E/§9.G).
func (r *PostgresRepository) GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error) {
	var (
		pageSize int
		ttlSecs  int
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT page_size, order_ttl_seconds
		FROM public.tenant_settings
		WHERE tenant_id = $1
	`, tenantID).Scan(&pageSize, &ttlSecs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TenantSettings{
			TenantID: tenantID,
			PageSize: DefaultPageSize,
			OrderTTL: DefaultOrderTTL,
		}, nil
	case err != nil:
		return TenantSettings{}, fmt.Errorf("store: leer config de tenant: %w", err)
	}
	return TenantSettings{
		TenantID: tenantID,
		PageSize: pageSize,
		OrderTTL: time.Duration(ttlSecs) * time.Second,
	}, nil
}
