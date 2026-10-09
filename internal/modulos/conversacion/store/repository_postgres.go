// Porta internal/flujos/store/repository_postgres.go @ c0c0c03
//
// El fichero viejo medía 1.074 líneas y aquí NACE PARTIDO por tema (05 E-13), solo
// repartiendo declaraciones:
//
//   - repository_postgres.go (este): el tipo, el constructor y lo que no es de ningún
//     otro tema: el estado conversacional (flow_state), las definiciones
//     (flow_definitions), las encuestas (survey_results) y el outbox de efectos
//     (flow_events).
//   - repository_postgres_tenant_content.go: tenant_content y tenant_content_versions.
//   - repository_postgres_intakes.go: intakes e intake_items.
//   - repository_postgres_settings.go: tenant_settings y conversation_welcomes.
//
// Reglas que valen para TODOS los ficheros del adaptador:
//
//   - el SQL se porta BYTE A BYTE del paquete viejo y no se «mejora» al portar. Que
//     ese SQL haga en un Postgres de verdad lo que el puerto promete lo prueba
//     storehelpertest.Contrato en los procesos de F9
//     (test/procesos/flowstore_contrato_test.go); el test de cada fichero afirma, con
//     un driver de mentira, la forma: qué se valida antes de ir a la base, qué
//     sentencias salen, dentro o fuera de una transacción, y el mapeo de filas y
//     errores;
//   - todo fallo de la base vuelve envuelto con %w y con el prefijo literal que dice
//     el contrato de cada método, y con los valores de retorno a cero;
//   - las columnas anulables (last_wa_message_id, event_id, owner_event_id,
//     expires_at, welcomed_at) salen como el cero de Go, y el cero de Go se escribe
//     como NULL;
//   - D-17 (deuda que se porta tal cual, D-F8-6): los cuatro listados cierran sus
//     filas con el ritual `defer rows.Close()` que solo informa del fallo del cierre
//     si no había ya otro error; cada uno con su texto.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// surveyResultCols es el número de columnas por fila que escribe InsertResults
// (orden de survey_results salvo id y created_at, que usan sus DEFAULT).
const surveyResultCols = 7

// PostgresRepository implementa Repository, WelcomeStore y TenantContentVersioner con
// SQL raw sobre public.flow_state, public.flow_definitions, survey_results,
// public.flow_events, public.tenant_content, public.tenant_content_versions,
// public.intakes, public.intake_items, public.tenant_settings y
// public.conversation_welcomes. Los cuerpos flexibles (vars del estado, definition del
// flujo, payload de un efecto) viajan como JSONB y se (de)serializan con
// json.Marshal/Unmarshal ↔ []byte.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository construye el repositorio sobre el pool dado. No toca la base
// ni valida el pool: un db nil no falla aquí sino en la primera sentencia.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// Exists indica si ya hay una conversación viva para la clave.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: exists estado: %w"
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer estado: %w"
//   - "store: deserializar vars: %w"
func (r *PostgresRepository) Load(ctx context.Context, key Key) (model.Conversation, bool, error) {
	var (
		c            model.Conversation
		varsRaw      []byte
		lastWa       sql.NullString
		eventID      sql.NullString
		ownerEventID sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT tenant_id::text, session_id, contact_id::text, flow_id, flow_version,
		       current_node, vars, last_wa_message_id, updated_at, event_id::text,
		       owner_event_id::text
		FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID).Scan(
		&c.TenantID, &c.SessionID, &c.ContactID, &c.FlowID, &c.FlowVersion,
		&c.CurrentNode, &varsRaw, &lastWa, &c.UpdatedAt, &eventID, &ownerEventID,
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
	// owner_event_id NULL ⇒ OwnerEventID "" (esta conversación no tiene flujo con
	// dueño, Plan 053 · T1.4). Cubre DOS casos que se comportan igual y por eso no se
	// distinguen: el menú puro sin flujo (D-043.3) y la fila LEGADA que se escribió
	// antes de que existiera la columna. Se lee con ::text por lo mismo que event_id:
	// la columna es UUID y el dominio la quiere como cadena.
	if ownerEventID.Valid {
		c.OwnerEventID = ownerEventID.String
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: serializar vars: %w"
//   - "store: upsert estado: %w"
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
	// OwnerEventID "" ⇒ NULL, con el MISMO trato que event_id y por el mismo motivo: el
	// dueño también se apaga (el flujo termina y la fila deja de pertenecer a nadie), y
	// una columna fuera del DO UPDATE se queda pegada para siempre. Aquí eso sería peor
	// que en event_id: el dueño es lo que T1.6 usará para decidir a QUÉ evento mata el
	// cierre, así que un dueño fosilizado cerraría el evento equivocado.
	var ownerEventID sql.NullString
	if state.OwnerEventID != "" {
		ownerEventID = sql.NullString{String: state.OwnerEventID, Valid: true}
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars, last_wa_message_id, event_id, owner_event_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (tenant_id, session_id, contact_id) DO UPDATE
		SET flow_id = EXCLUDED.flow_id,
		    flow_version = EXCLUDED.flow_version,
		    current_node = EXCLUDED.current_node,
		    vars = EXCLUDED.vars,
		    last_wa_message_id = EXCLUDED.last_wa_message_id,
		    event_id = EXCLUDED.event_id,
		    owner_event_id = EXCLUDED.owner_event_id,
		    updated_at = now()
	`, state.TenantID, state.SessionID, state.ContactID, state.FlowID, state.FlowVersion,
		state.CurrentNode, varsRaw, lastWa, eventID, ownerEventID)
	if err != nil {
		return fmt.Errorf("store: upsert estado: %w", err)
	}
	return nil
}

// Delete elimina la conversación viva de la clave (Plan 019 · T4, escape global).
// Idempotente: un DELETE sin filas NO es error (la clave ya estaba libre).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: borrar estado: %w"
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s flow=%s"
//   - "store: leer definición: %w"
//   - "store: deserializar definición: %w"
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s flow=%s version=%d"
//   - "store: leer definición por versión: %w"
//   - "store: deserializar definición: %w"
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: serializar definición: %w"
//   - "store: insertar definición: %w"
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

// ListDefinitions devuelve el resumen (flow_id, última versión, alta) de cada
// flujo publicado por el tenant, ordenado por flow_id (Plan 018 · T5). Acota SIEMPRE
// por tenant_id (INV-8): un tenant NUNCA ve los flujos de otro. DISTINCT ON toma la
// fila de mayor versión por flow_id (la vigente). Lista vacía sin error si el tenant
// no tiene flujos.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: listar definiciones: %w"
//   - "store: cerrar filas: %w"
//   - "store: escanear resumen de definición: %w"
//   - "store: iterar definiciones: %w"
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

// InsertResults persiste en lote las respuestas de encuesta EN CLARO en
// survey_results (Plan 014 §10.D, ADR-0009). Un solo INSERT multi-fila con
// placeholders; created_at usa el DEFAULT now() de la tabla. len(rows)==0 es un
// no-op.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: insertar resultados de encuesta: %w"
func (r *PostgresRepository) InsertResults(ctx context.Context, rows []SurveyResult) error {
	if len(rows) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*surveyResultCols)
	for i, row := range rows {
		base := i * surveyResultCols
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7,
		))
		// EventID "" ⇒ NULL (D-043.21): solo lo produce un replay legado; una fila
		// nueva sin padre la rechaza el CHECK de la 0054, que es su trabajo.
		var eventID sql.NullString
		if row.EventID != "" {
			eventID = sql.NullString{String: row.EventID, Valid: true}
		}
		args = append(args,
			row.TenantID, row.ContactID, row.FlowID, row.FlowVersion, row.QuestionID, row.AnswerCode, eventID,
		)
	}
	// #nosec G202 -- solo se concatenan placeholders generados ($1, $2, ...); los
	// valores viajan siempre parametrizados en args, nunca interpolados en el SQL.
	query := `
		INSERT INTO survey_results
			(tenant_id, contact_id, flow_id, flow_version, question_id, answer_code, event_id)
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: listar resultados de encuesta: %w"
//   - "store: cerrar filas de resultados: %w"
//   - "store: escanear resultado de encuesta: %w"
//   - "store: iterar resultados de encuesta: %w"
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: serializar payload de efecto: %w"
//   - "store: insertar efecto de flujo: %w"
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
