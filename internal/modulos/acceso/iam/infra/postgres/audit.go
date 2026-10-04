// Porta internal/iam/infra/postgres/audit.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// AuditRepo implementa out.AuditRepo sobre public.audit_events (append-only). REGLA DURA
// (INV-5): CERO PII; el repo no lo valida (confía en el usecase), pero solo materializa lo que
// recibe.
type AuditRepo struct{}

// NewAuditRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewAuditRepo(db *sql.DB) *AuditRepo {
	panic(pendiente.Implementar("iampostgres.NewAuditRepo"))
}

var _ out.AuditRepo = (*AuditRepo)(nil)

// Record implementa out.AuditRepo: añade el evento, nunca reescribe uno anterior. Meta se
// serializa a JSONB (nil → {}). Un Meta que no se puede serializar se rechaza ANTES de tocar la
// base, con el texto "iam: serializar meta de auditoría: …" envolviendo el error de JSON; un
// fallo de la base sale envuelto con "iam: registrar auditoría: …".
func (r *AuditRepo) Record(ctx context.Context, e domain.AuditEvent) error {
	panic(pendiente.Implementar("iampostgres.AuditRepo.Record"))
}

// List implementa out.AuditRepo: los eventos de tenantID y solo de él, más recientes primero
// (`at DESC, id DESC`), con límite y desplazamiento. Un fallo de la base sale (nil, err), con el
// texto "iam: listar auditoría: …"; nunca una lista parcial.
func (r *AuditRepo) List(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	panic(pendiente.Implementar("iampostgres.AuditRepo.List"))
}
