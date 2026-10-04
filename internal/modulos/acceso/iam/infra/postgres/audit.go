// Porta internal/iam/infra/postgres/audit.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// AuditRepo implementa out.AuditRepo sobre public.audit_events (append-only). REGLA DURA
// (INV-5): CERO PII; el repo no lo valida (confía en el usecase), pero solo materializa lo que
// recibe.
type AuditRepo struct {
	db *sql.DB
}

// NewAuditRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewAuditRepo(db *sql.DB) *AuditRepo { return &AuditRepo{db: db} }

var _ out.AuditRepo = (*AuditRepo)(nil)

// Record implementa out.AuditRepo: añade el evento, nunca reescribe uno anterior. Meta se
// serializa a JSONB (nil → {}). Un Meta que no se puede serializar se rechaza ANTES de tocar la
// base, con el texto "iam: serializar meta de auditoría: …" envolviendo el error de JSON; un
// fallo de la base sale envuelto con "iam: registrar auditoría: …".
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

// List implementa out.AuditRepo: los eventos de tenantID y solo de él, más recientes primero
// (`at DESC, id DESC`), con límite y desplazamiento. Un fallo de la base sale (nil, err), con el
// texto "iam: listar auditoría: …"; nunca una lista parcial.
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
