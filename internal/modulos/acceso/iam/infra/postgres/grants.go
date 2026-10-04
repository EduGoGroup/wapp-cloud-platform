// Porta internal/iam/infra/postgres/grants.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// GrantRepo implementa out.GrantRepo sobre public.iam_user_grants (overrides por usuario que se
// mergean sobre los del rol al emitir el token). La tabla no tiene tenant: acotar el override a
// los miembros del tenant es cosa del usecase (R-U22).
type GrantRepo struct{}

// NewGrantRepo construye el repositorio sobre el pool dado. No valida el pool ni lo toca.
func NewGrantRepo(db *sql.DB) *GrantRepo {
	panic(pendiente.Implementar("iampostgres.NewGrantRepo"))
}

var _ out.GrantRepo = (*GrantRepo)(nil)

// GrantsOfUser implementa out.GrantRepo: los overrides (pattern, effect) de userID y solo de él.
// Fallo de la base → (nil, "iam: leer grants: …").
func (r *GrantRepo) GrantsOfUser(ctx context.Context, userID string) ([]domain.Grant, error) {
	panic(pendiente.Implementar("iampostgres.GrantRepo.GrantsOfUser"))
}

// AddUserGrant implementa out.GrantRepo (idempotente por (user_id, pattern, effect)). Fallo de la
// base → "iam: añadir override de grant: …".
func (r *GrantRepo) AddUserGrant(ctx context.Context, userID string, g domain.Grant) error {
	panic(pendiente.Implementar("iampostgres.GrantRepo.AddUserGrant"))
}

// RemoveUserGrant implementa out.GrantRepo (no-op si no estaba). Fallo de la base → "iam: quitar
// override de grant: …".
func (r *GrantRepo) RemoveUserGrant(ctx context.Context, userID string, g domain.Grant) error {
	panic(pendiente.Implementar("iampostgres.GrantRepo.RemoveUserGrant"))
}
