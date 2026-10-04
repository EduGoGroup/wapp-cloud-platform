// Porta internal/iam/usecase/audit.go @ 9a77307

package usecase

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// defaultAuditLimit acota el listado de auditoría cuando el llamante no pide límite.
const defaultAuditLimit = 100

// AuditService implementa in.Auditor: registro y consulta de la bitácora. REGLA DURA (INV-5):
// CERO PII. El servicio NO valida el contenido semánticamente (confía en que el llamante pase
// ids opacos), pero su contrato lo exige: Actor/Resource ids, Meta contexto no sensible.
type AuditService struct {
	audit out.AuditRepo
}

// compile-time: AuditService satisface el puerto de entrada.
var _ in.Auditor = (*AuditService)(nil)

// NewAuditService construye el servicio de auditoría. Un repositorio nil se rechaza al
// arrancar (fail-fast) con "iam: AuditService requiere el repositorio de auditoría".
func NewAuditService(audit out.AuditRepo) (*AuditService, error) {
	if audit == nil {
		return nil, errors.New("iam: AuditService requiere el repositorio de auditoría")
	}
	return &AuditService{audit: audit}, nil
}

// Record registra un evento: copia Actor, Action, Resource, Result y Meta tal cual al
// repositorio. TenantID vacío se persiste como NULL (TenantID nil: evento pre-auth); uno no
// vacío, como puntero a ese valor. Action vacía es domain.ErrInvalidInput y no se escribe
// nada. Un error del repositorio se devuelve sin tocar.
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

// ListAudit devuelve los eventos del tenant, más recientes primero (el orden y el filtrado
// por tenant los pone el repositorio). R-U30: limit<=0 toma el límite por defecto, 100;
// offset<0 se normaliza a 0; cualquier otro valor viaja tal cual.
func (s *AuditService) ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	if offset < 0 {
		offset = 0
	}
	return s.audit.List(ctx, tenantID, limit, offset)
}
