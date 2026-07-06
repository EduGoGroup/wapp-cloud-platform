package trigger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PostgresStore implementa TriggerStore con SQL raw sobre public.flow_triggers.
// Todas las queries están parametrizadas por tenant_id (INV-8).
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore construye el store sobre el pool dado.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Insert persiste una regla nueva; el trigger_id lo asigna Postgres
// (gen_random_uuid, RETURNING). r.TriggerID del argumento se ignora.
func (s *PostgresStore) Insert(ctx context.Context, r Rule) (Rule, error) {
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO public.flow_triggers
			(tenant_id, kind, keyword, match_type, flow_id, priority, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING trigger_id
	`,
		r.TenantID,
		string(r.Kind),
		nullStr(r.Keyword),
		string(r.MatchType),
		nullStr(r.FlowID),
		r.Priority,
		r.Enabled,
	).Scan(&r.TriggerID)
	if err != nil {
		return Rule{}, fmt.Errorf("trigger: insertar regla: %w", err)
	}
	return r, nil
}

// List devuelve todas las reglas del tenant.
func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, trigger_id, kind, keyword, match_type, flow_id, priority, enabled
		FROM public.flow_triggers
		WHERE tenant_id = $1
		ORDER BY trigger_id
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("trigger: listar reglas: %w", err)
	}
	return scanRules(rows)
}

// ListByKind devuelve las reglas del tenant de un kind dado.
func (s *PostgresStore) ListByKind(ctx context.Context, tenantID string, k Kind) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, trigger_id, kind, keyword, match_type, flow_id, priority, enabled
		FROM public.flow_triggers
		WHERE tenant_id = $1 AND kind = $2
		ORDER BY trigger_id
	`, tenantID, string(k))
	if err != nil {
		return nil, fmt.Errorf("trigger: listar reglas por kind: %w", err)
	}
	return scanRules(rows)
}

// Get devuelve una regla por (tenant_id, trigger_id); ErrTriggerNotFound si no existe.
func (s *PostgresStore) Get(ctx context.Context, tenantID, triggerID string) (Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, trigger_id, kind, keyword, match_type, flow_id, priority, enabled
		FROM public.flow_triggers
		WHERE tenant_id = $1 AND trigger_id = $2
	`, tenantID, triggerID)
	r, err := scanRule(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Rule{}, ErrTriggerNotFound
	case err != nil:
		return Rule{}, fmt.Errorf("trigger: leer regla: %w", err)
	}
	return r, nil
}

// Delete borra una regla por (tenant_id, trigger_id); ErrTriggerNotFound si no existía.
func (s *PostgresStore) Delete(ctx context.Context, tenantID, triggerID string) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM public.flow_triggers
		WHERE tenant_id = $1 AND trigger_id = $2
	`, tenantID, triggerID)
	if err != nil {
		return fmt.Errorf("trigger: borrar regla: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("trigger: filas afectadas: %w", err)
	}
	if n == 0 {
		return ErrTriggerNotFound
	}
	return nil
}

// scanner abstrae *sql.Row y *sql.Rows para reusar el escaneo de una fila.
type scanner interface {
	Scan(dest ...any) error
}

// scanRule mapea una fila de flow_triggers a Rule (keyword/flow_id NULL → "").
func scanRule(sc scanner) (Rule, error) {
	var (
		r       Rule
		kind    string
		keyword sql.NullString
		match   string
		flowID  sql.NullString
	)
	if err := sc.Scan(&r.TenantID, &r.TriggerID, &kind, &keyword, &match, &flowID, &r.Priority, &r.Enabled); err != nil {
		return Rule{}, err
	}
	r.Kind = Kind(kind)
	r.Keyword = keyword.String
	r.MatchType = MatchType(match)
	r.FlowID = flowID.String
	return r, nil
}

// scanRules consume un *sql.Rows a []Rule y lo cierra.
func scanRules(rows *sql.Rows) ([]Rule, error) {
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()
	out := make([]Rule, 0)
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("trigger: escanear regla: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trigger: iterar reglas: %w", err)
	}
	return out, nil
}

// nullStr mapea "" → NULL para columnas nullable (keyword/flow_id).
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
