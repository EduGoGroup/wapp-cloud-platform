package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// execer es la cara de escritura común de *sql.DB y *sql.Tx (ExecContext), para
// que los INSERT en lote se reusen tanto en el camino autocommit como dentro de
// una transacción (CloseIntake).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// surveyResultCols es el número de columnas por fila que escribe InsertResults
// (orden de survey_results salvo id y created_at, que usan sus DEFAULT).
const surveyResultCols = 6

// intakeItemCols es el número de columnas por fila que escribe insertIntakeItems
// (orden de intake_items salvo id y added_at, que usan sus DEFAULT).
const intakeItemCols = 6

// reservedSKUPrefix es el prefijo de los skus que pone LA PLATAFORMA (hoy solo la
// línea de envío, D-041.11) y que ninguna escritura del carrito puede tirar.
//
// Es el MISMO literal que intakes.ReservedSKUPrefix, de quien es la regla, y se
// repite aquí en vez de importarlo a propósito: este paquete es el almacén del motor
// de flujos y no debe depender del dominio de solicitudes para escribir una tabla.
// Lo que impide que los dos literales diverjan no es la disciplina sino un test
// (reserved_prefix_test.go), que los compara.
const reservedSKUPrefix = "_"

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
		eventID sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, session_id, contact_id::text, flow_id, flow_version,
		       current_node, vars, last_wa_message_id, updated_at, event_id::text
		FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID).Scan(
		&c.TenantID, &c.SessionID, &c.ContactID, &c.FlowID, &c.FlowVersion,
		&c.CurrentNode, &varsRaw, &lastWa, &c.UpdatedAt, &eventID,
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
	// event_id NULL ⇒ EventID "" (la conversación no tiene evento activo, E-6):
	// es un estado legítimo y frecuente, no una lectura fallida. Se lee con
	// ::text para que el UUID llegue como cadena, igual que tenant_id/contact_id.
	if eventID.Valid {
		c.EventID = eventID.String
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
	// EventID "" ⇒ NULL, y el UPDATE lo escribe igual que cualquier otro valor: apagar
	// el puntero del evento activo (cierre/cancelación, D-043.4) es guardar un estado
	// con EventID vacío. Si esta columna se excluyera del DO UPDATE, un evento cerrado
	// dejaría el puntero pegado para siempre y la conversación seguiría "dentro" de él.
	var eventID sql.NullString
	if state.EventID != "" {
		eventID = sql.NullString{String: state.EventID, Valid: true}
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars, last_wa_message_id, event_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (tenant_id, session_id, contact_id) DO UPDATE
		SET flow_id = EXCLUDED.flow_id,
		    flow_version = EXCLUDED.flow_version,
		    current_node = EXCLUDED.current_node,
		    vars = EXCLUDED.vars,
		    last_wa_message_id = EXCLUDED.last_wa_message_id,
		    event_id = EXCLUDED.event_id,
		    updated_at = now()
	`, state.TenantID, state.SessionID, state.ContactID, state.FlowID, state.FlowVersion,
		state.CurrentNode, varsRaw, lastWa, eventID)
	if err != nil {
		return fmt.Errorf("store: upsert estado: %w", err)
	}
	return nil
}

// Delete elimina la conversación viva de la clave (Plan 019 · T4, escape global).
// Idempotente: un DELETE sin filas NO es error (la clave ya estaba libre).
func (r *PostgresRepository) Delete(ctx context.Context, key Key) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID)
	if err != nil {
		return fmt.Errorf("store: borrar estado: %w", err)
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

// ListResults devuelve las respuestas de este contacto en este flujo, en orden
// CRONOLÓGICO y acotadas al tenant (INV-8). Ver SurveyResultStore.ListResults para
// las dos cosas que esta tabla NO puede decir (ni sesión ni pasada).
//
// El orden es (created_at, id) y no solo created_at: el DEFAULT now() es el reloj de
// la TRANSACCIÓN, así que dos respuestas escritas en la misma tanda comparten
// created_at al milisegundo y sin el id de desempate saldrían en orden arbitrario —
// justo en el caso en que quien resume necesita saber cuál fue la última.
func (r *PostgresRepository) ListResults(ctx context.Context, tenantID, contactID, flowID string) (out []SurveyResult, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tenant_id, contact_id, flow_id, flow_version, question_id, answer_code, created_at
		FROM survey_results
		WHERE tenant_id = $1 AND contact_id = $2 AND flow_id = $3
		ORDER BY created_at, id
	`, tenantID, contactID, flowID)
	if err != nil {
		return nil, fmt.Errorf("store: listar resultados de encuesta: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("store: cerrar filas de resultados: %w", cerr)
		}
	}()

	out = make([]SurveyResult, 0)
	for rows.Next() {
		var s SurveyResult
		if serr := rows.Scan(&s.TenantID, &s.ContactID, &s.FlowID, &s.FlowVersion,
			&s.QuestionID, &s.AnswerCode, &s.CreatedAt); serr != nil {
			return nil, fmt.Errorf("store: escanear resultado de encuesta: %w", serr)
		}
		out = append(out, s)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("store: iterar resultados de encuesta: %w", rerr)
	}
	return out, nil
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

// UpsertTenantContent inserta o actualiza (upsert por PK (tenant_id, ref)) el blob
// de contenido de negocio en public.tenant_content (Plan 018 · T6, ADR-0009). El
// blob se persiste como JSONB (debe ser JSON válido; lo valida el transporte).
// created_at usa el DEFAULT now() en el alta; updated_at se refresca en cada
// escritura. Acotado al tenant (INV-8).
func (r *PostgresRepository) UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.tenant_content (tenant_id, ref, content, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (tenant_id, ref) DO UPDATE
		SET content = EXCLUDED.content, updated_at = now()
	`, tenantID, ref, blob)
	if err != nil {
		return fmt.Errorf("store: upsert contenido de tenant: %w", err)
	}
	return nil
}

// ReplaceTenantContentVersioned implementa TenantContentVersioner sobre Postgres:
// archiva el blob vigente en public.tenant_content_versions y escribe el nuevo en
// public.tenant_content DENTRO DE UNA SOLA TRANSACCIÓN (Plan 041 · T3.3,
// D-041.8), vía el helper postgres.WithTx (rollback inmune a panic + retry
// 40P01/40001).
//
// EL ORDEN NO ES LO QUE DA LA ATOMICIDAD, LA TRANSACCIÓN SÍ. Fuera de ella no hay
// secuencia buena: archivar-y-caer deja una versión de un catálogo que sigue
// vigente; escribir-y-caer pierde para siempre el que se sustituyó.
//
// Bloquea la fila vigente con FOR UPDATE antes de calcular MAX(version)+1: dos
// imports simultáneos sobre la MISMA (tenant, ref) se serializan y numeran 1 y 2,
// en vez de pelearse por el mismo número y violar la PK. Si no hay fila vigente no
// hay nada que bloquear ni que archivar —el upsert final resuelve la carrera— y se
// devuelve archived=0.
func (r *PostgresRepository) ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (int, error) {
	if !validVersionSource(source) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidVersionSource, source)
	}
	var archived int
	err := postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		archived = 0 // WithTx reintenta ante deadlock: el acumulador se recalcula entero.
		var current []byte
		err := tx.QueryRowContext(ctx, `
			SELECT content
			FROM public.tenant_content
			WHERE tenant_id = $1 AND ref = $2
			FOR UPDATE
		`, tenantID, ref).Scan(&current)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Sin contenido vigente no se versiona nada (D-041.8): la versión 1
			// nacerá del PRÓXIMO import, con lo que este escriba.
		case err != nil:
			return fmt.Errorf("store: leer contenido vigente para versionar: %w", err)
		default:
			var next int
			if verr := tx.QueryRowContext(ctx, `
				SELECT COALESCE(MAX(version), 0) + 1
				FROM public.tenant_content_versions
				WHERE tenant_id = $1 AND ref = $2
			`, tenantID, ref).Scan(&next); verr != nil {
				return fmt.Errorf("store: calcular siguiente versión de contenido: %w", verr)
			}
			if _, ierr := tx.ExecContext(ctx, `
				INSERT INTO public.tenant_content_versions
					(tenant_id, ref, version, content, source)
				VALUES ($1, $2, $3, $4, $5)
			`, tenantID, ref, next, current, source); ierr != nil {
				return fmt.Errorf("store: archivar versión de contenido: %w", ierr)
			}
			archived = next
		}

		if _, uerr := tx.ExecContext(ctx, `
			INSERT INTO public.tenant_content (tenant_id, ref, content, created_at, updated_at)
			VALUES ($1, $2, $3, now(), now())
			ON CONFLICT (tenant_id, ref) DO UPDATE
			SET content = EXCLUDED.content, updated_at = now()
		`, tenantID, ref, blob); uerr != nil {
			return fmt.Errorf("store: escribir contenido versionado: %w", uerr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return archived, nil
}

// ListTenantContent devuelve las cabeceras (ref + timestamps) de los blobs de
// public.tenant_content del tenant, ordenadas por ref (Plan 018 · T6). NO trae el
// blob (se obtiene con GetTenantContent). Acotado al tenant (INV-8): el WHERE
// tenant_id garantiza que un blob ajeno nunca aparece.
func (r *PostgresRepository) ListTenantContent(ctx context.Context, tenantID string) (out []TenantContentSummary, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ref, created_at, updated_at
		FROM public.tenant_content
		WHERE tenant_id = $1
		ORDER BY ref
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: listar contenido de tenant: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("store: cerrar filas: %w", cerr)
		}
	}()

	out = make([]TenantContentSummary, 0)
	for rows.Next() {
		var s TenantContentSummary
		if scanErr := rows.Scan(&s.Ref, &s.CreatedAt, &s.UpdatedAt); scanErr != nil {
			return nil, fmt.Errorf("store: escanear contenido de tenant: %w", scanErr)
		}
		out = append(out, s)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("store: iterar contenido de tenant: %w", rowsErr)
	}
	return out, nil
}

// DeleteTenantContent borra el blob (tenant_id, ref) de public.tenant_content
// (Plan 018 · T6). Devuelve ErrTenantContentNotFound si no existía (simetría con
// GetTenantContent → 404 en el transporte). Acotado al tenant (INV-8).
func (r *PostgresRepository) DeleteTenantContent(ctx context.Context, tenantID, ref string) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM public.tenant_content
		WHERE tenant_id = $1 AND ref = $2
	`, tenantID, ref)
	if err != nil {
		return fmt.Errorf("store: borrar contenido de tenant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: filas afectadas al borrar contenido: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: tenant=%s ref=%s", ErrTenantContentNotFound, tenantID, ref)
	}
	return nil
}

// UpsertIntake inserta o actualiza (upsert por id) la solicitud en public.intakes
// (Plan 016 · T0/T2). Idempotente por o.ID. ExpiresAt zero se materializa como
// NULL. created_at/updated_at usan now() (updated_at se refresca en el UPDATE).
func (r *PostgresRepository) UpsertIntake(ctx context.Context, o Intake) error {
	var expires sql.NullTime
	if !o.ExpiresAt.IsZero() {
		expires = sql.NullTime{Time: o.ExpiresAt, Valid: true}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.intakes
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
		return fmt.Errorf("store: upsert solicitud: %w", err)
	}
	return nil
}

// ReplaceIntakeItems deja las líneas de cliente de la solicitud EXACTAMENTE en
// `items`, en UNA transacción (Plan 043 · Ola 3): borrar y volver a escribir tienen
// que ser un solo acto o existiría un instante en el que el pedido no tiene líneas,
// y ese instante lo puede leer el CRM.
func (r *PostgresRepository) ReplaceIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error {
	return postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		return replaceIntakeItemsTx(ctx, tx, intakeID, items)
	})
}

// replaceIntakeItemsTx retira las líneas de CLIENTE de la solicitud y escribe las
// nuevas, sobre una transacción ya abierta. Es el ÚNICO camino por el que el motor
// de flujos escribe intake_items —lo usan la proyección de item_added y el cierre—,
// y por eso escribir dos veces el mismo conjunto deja el mismo conjunto.
//
// El DELETE excluye el prefijo reservado (copiado de intakes.replaceClientItemsTx,
// que es como el CRM rehace las líneas en una revisión): las líneas de wApp —hoy la
// de envío, D-041.11— llevan su precio puesto a mano y no son del carrito. Hoy no
// pueden coexistir con una escritura del carrito, porque la de envío se cuelga
// DESPUÉS del cierre y a una solicitud cerrada ya no le entran item_added; la
// exclusión está para que ese orden pueda cambiar sin que nadie pierda una línea.
//
// El orden del pedido se conserva aunque se reescriba entero: la lectura ordena por
// (added_at, id) y las filas de un INSERT multi-fila reciben el BIGSERIAL en el orden
// de los VALUES, que es el del carrito. La advertencia de applyRevalidationItemsTx
// —«reescribirlas todas le reordenaría el pedido»— aplica a un DELETE+INSERT PARCIAL,
// no a uno que reescribe el conjunto completo en su orden.
func replaceIntakeItemsTx(ctx context.Context, ex execer, intakeID string, items []IntakeItem) error {
	if _, err := ex.ExecContext(ctx, `
		DELETE FROM public.intake_items
		WHERE intake_id = $1 AND left(sku, 1) <> $2
	`, intakeID, reservedSKUPrefix); err != nil {
		return fmt.Errorf("store: retirar líneas de solicitud: %w", err)
	}
	return insertIntakeItems(ctx, ex, intakeID, items)
}

// insertIntakeItems ejecuta el INSERT multi-fila de líneas sobre cualquier execer.
// len(items)==0 es un no-op. NO es un punto de entrada: se llama SIEMPRE detrás del
// DELETE de replaceIntakeItemsTx, porque una solicitud recibe hoy varias escrituras
// de su conjunto de líneas y añadirlas sin retirar las anteriores las duplicaría.
func insertIntakeItems(ctx context.Context, ex execer, intakeID string, items []IntakeItem) error {
	if len(items) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(items))
	args := make([]any, 0, len(items)*intakeItemCols)
	for i, it := range items {
		base := i * intakeItemCols
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6,
		))
		// Customization viaja SIEMPRE, aunque esté vacía: la columna es NOT NULL y
		// su vacío significa "sin personalización" (D-041.17), no "no sé".
		args = append(args, intakeID, it.SKU, it.Label, it.Customization, it.Qty, it.UnitPrice)
	}
	// #nosec G202 -- solo se concatenan placeholders generados ($1, $2, ...); los
	// valores viajan siempre parametrizados en args, nunca interpolados en el SQL.
	query := `
		INSERT INTO public.intake_items
			(intake_id, sku, label, customization, qty, unit_price)
		VALUES ` + strings.Join(placeholders, ", ")
	if _, err := ex.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: insertar líneas de solicitud: %w", err)
	}
	return nil
}

// CloseIntake cierra ATÓMICAMENTE la solicitud abierta del contacto e inserta sus
// líneas en la MISMA transacción (Plan 027 · Ola 1 · T4, cierra H4), vía el helper
// único postgres.WithTx (rollback inmune a panic + retry 40P01/40001). Bloquea la
// solicitud "open" con FOR UPDATE: dos cierres concurrentes del mismo contacto se
// serializan (el segundo la ve ya "closed" y no crea otra). Si no había solicitud
// abierta, crea una "closed" coherente. Garantiza que una solicitud closed nunca quede
// sin líneas.
//
// Devuelve el ID de la solicitud cerrada porque quien cierra necesita saber SOBRE
// QUÉ cerró: es lo que permite colgarle la revisión 1 (ADR-0031 §3). Sin él, el
// llamante tendría que releer "la última cerrada de este contacto", que es una
// carrera con el siguiente carrito.
func (r *PostgresRepository) CloseIntake(ctx context.Context, in IntakeClose) (string, error) {
	// Se declara FUERA de la clausura porque WithTx puede REEJECUTARLA ante un
	// deadlock: cada intento reasigna el id y el que sobrevive es el del intento
	// que confirmó.
	var closedID string
	err := postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var intakeID string
		err := tx.QueryRowContext(ctx, `
			SELECT id::text FROM public.intakes
			WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open'
			ORDER BY created_at DESC
			LIMIT 1
			FOR UPDATE
		`, in.TenantID, in.ContactID).Scan(&intakeID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			intakeID = uuid.NewString()
			if _, ierr := tx.ExecContext(ctx, `
				INSERT INTO public.intakes
					(id, tenant_id, contact_id, session_id, status, total, customer_note, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'closed', $5, $6, now(), now())
			`, intakeID, in.TenantID, in.ContactID, in.SessionID, in.Total, in.CustomerNote); ierr != nil {
				return fmt.Errorf("store: insertar solicitud cerrada: %w", ierr)
			}
		case err != nil:
			return fmt.Errorf("store: bloquear solicitud abierta: %w", err)
		default:
			// customer_note se escribe en el CIERRE y no al abrir la solicitud: el
			// cliente la teclea en el resumen, que es el último paso antes de
			// confirmar. La columna es NOT NULL, así que el vacío viaja igual que el
			// texto —"sin indicación" es un valor, no una omisión— y una solicitud
			// cerrada dos veces (reintento del 40P01) acaba con el mismo contenido.
			if _, uerr := tx.ExecContext(ctx, `
				UPDATE public.intakes
				SET status = 'closed', total = $2, customer_note = $3, updated_at = now()
				WHERE id = $1
			`, intakeID, in.Total, in.CustomerNote); uerr != nil {
				return fmt.Errorf("store: cerrar solicitud: %w", uerr)
			}
		}
		closedID = intakeID
		// REEMPLAZO, no INSERT: la solicitud puede llegar al cierre con las líneas que
		// la proyección de item_added ya materializó mientras estaba abierta (Plan 043 ·
		// Ola 3). Insertarlas otra vez las duplicaría todas.
		return replaceIntakeItemsTx(ctx, tx, intakeID, in.Items)
	})
	if err != nil {
		return "", err
	}
	return closedID, nil
}

// GetOpenIntake devuelve la solicitud "open" del contacto para (tenantID, contactID);
// found=false sin error si no hay (Plan 016 · T2/T3). Usa el índice intakes_open_idx.
func (r *PostgresRepository) GetOpenIntake(ctx context.Context, tenantID, contactID string) (Intake, bool, error) {
	var (
		o       Intake
		expires sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id::text, tenant_id, contact_id, session_id, status, total,
		       created_at, updated_at, expires_at
		FROM public.intakes
		WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID, contactID).Scan(
		&o.ID, &o.TenantID, &o.ContactID, &o.SessionID, &o.Status, &o.Total,
		&o.CreatedAt, &o.UpdatedAt, &expires,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Intake{}, false, nil
	case err != nil:
		return Intake{}, false, fmt.Errorf("store: leer solicitud abierta: %w", err)
	}
	if expires.Valid {
		o.ExpiresAt = expires.Time
	}
	return o, true, nil
}

// ListIntakeItems devuelve las líneas de la solicitud en el orden en que las ve el
// cliente (added_at, id), que es el MISMO ORDEN Y LA MISMA PROYECCIÓN que usa
// intakes.itemsOf: dos lecturas de la misma tabla que se contradijeran en el orden
// enseñarían el pedido de dos formas distintas según por dónde se mire.
//
// El UUID se valida ANTES de consultar para no depender del error 22P02 de Postgres
// (el repositorio en memoria no lo daría, y las dos implementaciones tienen que
// contestar lo mismo a la misma pregunta).
func (r *PostgresRepository) ListIntakeItems(ctx context.Context, intakeID string) (out []IntakeItem, err error) {
	if _, perr := uuid.Parse(intakeID); perr != nil {
		return nil, fmt.Errorf("store: listar líneas de solicitud: id %q inválido: %w", intakeID, perr)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT sku, label, customization, qty, unit_price, added_at
		FROM public.intake_items
		WHERE intake_id = $1
		ORDER BY added_at, id
	`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("store: listar líneas de solicitud: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("store: cerrar filas de líneas: %w", cerr)
		}
	}()

	out = make([]IntakeItem, 0)
	for rows.Next() {
		it := IntakeItem{IntakeID: intakeID}
		if serr := rows.Scan(&it.SKU, &it.Label, &it.Customization, &it.Qty, &it.UnitPrice, &it.AddedAt); serr != nil {
			return nil, fmt.Errorf("store: escanear línea de solicitud: %w", serr)
		}
		out = append(out, it)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("store: iterar líneas de solicitud: %w", rerr)
	}
	return out, nil
}

// MarkIntakeStatus transiciona el estado de una solicitud (por id) y fija su total,
// refrescando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled" |
// "expired".
func (r *PostgresRepository) MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.intakes
		SET status = $2, total = $3, updated_at = now()
		WHERE id = $1
	`, intakeID, status, total)
	if err != nil {
		return fmt.Errorf("store: marcar estado de solicitud: %w", err)
	}
	return nil
}

// GetTenantSettings devuelve la config del carrito para tenantID desde
// public.tenant_settings (Plan 016 · T0). Si el tenant no tiene fila, devuelve los
// DEFAULTS de DefaultTenantSettings SIN error (design.md §9.E/§9.G).
//
// HAY FILA vs NO HAY FILA SON DOS CAMINOS DISTINTOS, Y ESO ES EL PUNTO (Plan 043 ·
// T1.3). Con fila, los valores se devuelven TAL CUAL vienen de la columna, sin
// sustituir ceros por defaults: `event_inactivity_ttl_seconds = 0` es el override
// explícito «sin vencimiento» de una empresa (D-043.7 / E-6), no un hueco que
// rellenar. Como 0 es además el cero de Go, un `if x == 0 { x = Default }` aquí
// convertiría ese override en 2 h sin que nadie se entere: no lo introduzcas.
func (r *PostgresRepository) GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error) {
	var (
		pageSize        int
		ttlSecs         int
		convTTLSecs     int
		buyerFields     []byte
		evInactTTLSecs  int
		evHistoryTTLSec int
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT page_size, order_ttl_seconds, conversation_ttl_seconds, buyer_fields,
		       event_inactivity_ttl_seconds, event_history_ttl_seconds
		FROM public.tenant_settings
		WHERE tenant_id = $1
	`, tenantID).Scan(&pageSize, &ttlSecs, &convTTLSecs, &buyerFields,
		&evInactTTLSecs, &evHistoryTTLSec)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return DefaultTenantSettings(tenantID), nil
	case err != nil:
		return TenantSettings{}, fmt.Errorf("store: leer config de tenant: %w", err)
	}
	return TenantSettings{
		TenantID:           tenantID,
		PageSize:           pageSize,
		OrderTTL:           time.Duration(ttlSecs) * time.Second,
		ConversationTTL:    time.Duration(convTTLSecs) * time.Second,
		BuyerFields:        parseBuyerFields(buyerFields),
		EventInactivityTTL: time.Duration(evInactTTLSecs) * time.Second,
		EventHistoryTTL:    time.Duration(evHistoryTTLSec) * time.Second,
	}, nil
}

// parseBuyerFields decodifica la columna buyer_fields (JSONB, D-041.13). Es
// TOLERANTE a propósito: un blob ilegible o de otra forma devuelve el checklist
// VACÍO en vez de un error, y el carrito sigue vendiendo sin preguntar nada.
//
// La alternativa —propagar el error— dejaría al tenant sin poder cerrar un pedido
// por una config mal escrita a mano, que es exactamente el fallo que no se quiere:
// esta lectura está en el camino de CADA mensaje del cliente (la siembra de
// reanudación), no en un endpoint de administración donde un 400 sería útil.
func parseBuyerFields(raw []byte) []BuyerField {
	if len(raw) == 0 {
		return nil
	}
	var out []BuyerField
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
