package iampostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// AuditRepo implementa out.AuditRepo sobre public.audit_events (append-only).
// REGLA DURA (INV-5): CERO PII; el repo no lo valida (confía en el usecase),
// pero solo materializa lo que recibe.
type AuditRepo struct {
	db *sql.DB
}

// NewAuditRepo construye el repositorio sobre el pool dado.
func NewAuditRepo(db *sql.DB) *AuditRepo { return &AuditRepo{db: db} }

var _ out.AuditRepo = (*AuditRepo)(nil)

// Record implementa out.AuditRepo. Meta se serializa a JSONB (nil → {}).
func (r *AuditRepo) Record(ctx context.Context, e domain.AuditEvent) error {
	meta := e.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("iam: serializar meta de auditoría: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO public.audit_events (tenant_id, actor, action, resource, result, meta)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, nullString(e.TenantID), e.Actor, e.Action, e.Resource, e.Result, metaRaw)
	if err != nil {
		return fmt.Errorf("iam: registrar auditoría: %w", err)
	}
	return nil
}

// List implementa out.AuditRepo (más recientes primero, paginado).
func (r *AuditRepo) List(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id::text, actor, action, resource, result, meta, at
		FROM public.audit_events
		WHERE tenant_id = $1
		ORDER BY at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("iam: listar auditoría: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var res []domain.AuditEvent
	for rows.Next() {
		var (
			e       domain.AuditEvent
			tenant  sql.NullString
			metaRaw []byte
		)
		if serr := rows.Scan(&e.ID, &tenant, &e.Actor, &e.Action, &e.Resource, &e.Result, &metaRaw, &e.At); serr != nil {
			return nil, fmt.Errorf("iam: escanear auditoría: %w", serr)
		}
		e.TenantID = strPtr(tenant)
		if len(metaRaw) > 0 {
			if uerr := json.Unmarshal(metaRaw, &e.Meta); uerr != nil {
				return nil, fmt.Errorf("iam: deserializar meta de auditoría: %w", uerr)
			}
		}
		res = append(res, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: iterar auditoría: %w", err)
	}
	return res, nil
}
