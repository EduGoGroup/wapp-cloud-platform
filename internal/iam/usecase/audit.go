package usecase

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// defaultAuditLimit acota el listado de auditoría cuando el caller no pide límite.
const defaultAuditLimit = 100

// AuditService implementa in.Auditor: registro y consulta de la bitácora. REGLA
// DURA (INV-5): CERO PII. El servicio NO valida el contenido semánticamente
// (confía en que el caller pase ids opacos), pero su contrato lo exige: Actor/
// Resource ids, Meta contexto no sensible.
type AuditService struct {
	audit out.AuditRepo
}

// compile-time: AuditService satisface el puerto de entrada.
var _ in.Auditor = (*AuditService)(nil)

// NewAuditService construye el servicio de auditoría. Valida deps nil.
func NewAuditService(audit out.AuditRepo) (*AuditService, error) {
	if audit == nil {
		return nil, errors.New("iam: AuditService requiere el repositorio de auditoría")
	}
	return &AuditService{audit: audit}, nil
}

// Record registra un evento. TenantID vacío se persiste como NULL (evento
// pre-auth).
func (s *AuditService) Record(ctx context.Context, req in.AuditInput) error {
	if req.Action == "" {
		return domain.ErrInvalidInput
	}
	var tid *string
	if req.TenantID != "" {
		tid = &req.TenantID
	}
	return s.audit.Record(ctx, domain.AuditEvent{
		TenantID: tid,
		Actor:    req.Actor,
		Action:   req.Action,
		Resource: req.Resource,
		Result:   req.Result,
		Meta:     req.Meta,
	})
}

// ListAudit devuelve los eventos del tenant, más recientes primero. limit<=0
// toma el default; offset<0 se normaliza a 0.
func (s *AuditService) ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	if offset < 0 {
		offset = 0
	}
	return s.audit.List(ctx, tenantID, limit, offset)
}
