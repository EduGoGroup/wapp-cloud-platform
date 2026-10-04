// Porta internal/iam/infra/postgres/invitations.go @ 9a77307

package iampostgres

// invitations.go — EL ADAPTADOR DE public.tenant_invitations (migración 0085; Plan 047 · Ola A ·
// T-A2 y T-A8).
//
// Nada aquí sabe qué es un token: recibe y devuelve el DIGEST de 32 bytes que
// domain.HashInvitationToken produce. El texto en claro no entra ni sale por este fichero, y por
// eso no hay forma de recuperarlo desde la base.

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// InvitationRepo implementa out.InvitationRepo sobre public.tenant_invitations. La tabla no lleva
// FK hacia el usuario (created_by/redeemed_by): esa identidad vive en identity, en otra base de
// datos.
type InvitationRepo struct{}

// NewInvitationRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewInvitationRepo(db *sql.DB) *InvitationRepo {
	panic(pendiente.Implementar("iampostgres.NewInvitationRepo"))
}

var _ out.InvitationRepo = (*InvitationRepo)(nil)

// Create implementa out.InvitationRepo: escribe la invitación PENDIENTE (las cuatro columnas de
// estado nacen NULL) y devuelve la fila que la base escribió, con su id y su created_at reales.
//
// Un digest que no mida 32 bytes lo rechaza la base (CHECK
// tenant_invitations_token_hash_len_check) y sale como error, no como fila. Un digest repetido →
// error que envuelve domain.ErrConflict y dice que se revise el generador; cualquier otro fallo
// de la base → "iam: emitir invitación: …", nunca ErrConflict.
func (r *InvitationRepo) Create(ctx context.Context, inv domain.Invitation) (domain.Invitation, error) {
	panic(pendiente.Implementar("iampostgres.InvitationRepo.Create"))
}

// ListByTenant implementa out.InvitationRepo: las invitaciones de tenantID y solo de él, en
// orden (created_at DESC, id DESC) —lo más nuevo arriba, con desempate estable—, en todos sus
// estados. Sin invitaciones, lista vacía no nil. Fallo de la base → (nil, "iam: listar
// invitaciones: …").
func (r *InvitationRepo) ListByTenant(ctx context.Context, tenantID string) ([]domain.Invitation, error) {
	panic(pendiente.Implementar("iampostgres.InvitationRepo.ListByTenant"))
}

// Revoke implementa out.InvitationRepo (T-A8): marca revoked_at y NO borra la fila. La condición
// (pendiente y de ese tenant) viaja DENTRO del UPDATE: es lo único que resuelve la carrera con el
// canje. Si no se revocó nada, el motivo: inexistente o de otra empresa → domain.ErrNotFound (el
// mismo, anti-oráculo); ya canjeada → error que envuelve domain.ErrConflict; ya revocada → nil
// (idempotente). Un fallo de la base → "iam: revocar invitación: …", nunca ErrNotFound ni nil.
func (r *InvitationRepo) Revoke(ctx context.Context, id, tenantID string) error {
	panic(pendiente.Implementar("iampostgres.InvitationRepo.Revoke"))
}
