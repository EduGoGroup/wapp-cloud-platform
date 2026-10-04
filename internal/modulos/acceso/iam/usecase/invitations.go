// Porta internal/iam/usecase/invitations.go @ 9a77307

package usecase

// invitations.go — LA EMISIÓN, EL LISTADO Y LA REVOCACIÓN DE INVITACIONES (Plan 047 · Ola A ·
// T-A2 y T-A8, D-047.11).
//
// La dueña de una empresa emite un código opaco, se lo pasa por WhatsApp a quien quiere dentro y
// esa persona lo canjea después de registrarse ella misma (canje.go). Ni correo, ni SMTP, ni
// mailer: aquí no se teclea el correo de nadie. Este fichero NO canjea.

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// InvitationService implementa in.InvitationAdmin.
//
// Regla común: la empresa sale del CONTEXTO (INV-04). Sin identidad o con identidad sin empresa
// ⇒ domain.ErrNoTenant y no se ejecuta nada (ni se emite, ni se lista, ni se revoca).
type InvitationService struct{}

// compile-time: InvitationService satisface el puerto de entrada.
var _ in.InvitationAdmin = (*InvitationService)(nil)

// NewInvitationService construye el servicio. Aquí NINGUNA dependencia admite nil (fail-fast,
// servicio nil y textos literales):
//
//   - caller nil ⇒ "iam: InvitationService requiere un CallerResolver (INV-04: el tenant sale
//     del contexto)";
//   - invitations nil ⇒ "iam: InvitationService requiere un InvitationRepo";
//   - roles nil ⇒ "iam: InvitationService requiere un RoleRepo para validar el rol prometido".
func NewInvitationService(caller in.CallerResolver, invitations out.InvitationRepo, roles out.RoleRepo) (*InvitationService, error) {
	panic(pendiente.Implementar("usecase.NewInvitationService"))
}

// IssueInvitation implementa in.InvitationAdmin: emite una invitación para la empresa del
// CONTEXTO y devuelve la fila y el token EN CLARO, por única vez (R-U27).
//
//   - La fila nace con TenantID = la empresa del Caller y CreatedBy = su UserID.
//   - Se guarda SOLO el digest: TokenHash = domain.HashInvitationToken(token), 32 bytes; el
//     token en claro (domain.NewInvitationToken, prefijo "WAPP-INV-") no se persiste.
//   - Rol prometido: nil o cadena vacía (también solo espacios) ⇒ alta sin rol (RoleID nil), y
//     es legítimo. Uno no vacío tiene que ser VISIBLE para la empresa —suyo o plantilla global—;
//     de otra empresa o inexistente ⇒ domain.ErrNotFound (nunca «prohibido») y no se escribe
//     ninguna fila ni se genera token.
//   - TTL: el default y el clamp viven en UN solo sitio, este servicio. TTLSeconds <= 0 significa
//     AUSENTE ⇒ 24 h (86400 s); después se acota a [60 s, 30 días]. ExpiresAt = ahora (UTC) +
//     esa vida.
//   - Un fallo generando el token sale envuelto como "iam: emitir invitación: <causa>"; un
//     error del repositorio se propaga.
//
// El orden es empresa → rol → secreto: una emisión rechazada no deja ni fila ni token suelto.
func (s *InvitationService) IssueInvitation(ctx context.Context, input in.IssueInvitationInput) (in.IssuedInvitation, error) {
	panic(pendiente.Implementar("usecase.InvitationService.IssueInvitation"))
}

// ListInvitations implementa in.InvitationAdmin (R-U28): las invitaciones de la empresa del
// CONTEXTO y solo de ella, las más recientes primero (el orden lo pone el repositorio). Una lista
// vacía no es error.
func (s *InvitationService) ListInvitations(ctx context.Context) ([]domain.Invitation, error) {
	panic(pendiente.Implementar("usecase.InvitationService.ListInvitations"))
}

// RevokeInvitation implementa in.InvitationAdmin (T-A8, R-U28): anula una invitación VIVA de la
// empresa del CONTEXTO; el tenant entra en el WHERE del repositorio, no en un `if` posterior.
//
//   - nil ⇒ la fila sigue existiendo (revocar marca, no borra) con RevokedAt puesto y estado
//     domain.InvitationRevoked.
//   - Idempotente sobre una ya revocada (nil).
//   - Ya canjeada ⇒ domain.ErrConflict y la fila NO queda además revocada: revocarla no
//     deshace la membresía que el canje escribió.
//   - De otra empresa ⇒ domain.ErrNotFound y la de esa empresa sigue viva.
//   - id vacío ⇒ domain.ErrInvalidInput (envuelto con «id vacío»); id que no es un UUID ⇒
//     domain.ErrNotFound sin consultar (el mismo destino que uno que no existe, y lo mismo en
//     memoria que en Postgres).
func (s *InvitationService) RevokeInvitation(ctx context.Context, id string) error {
	panic(pendiente.Implementar("usecase.InvitationService.RevokeInvitation"))
}
