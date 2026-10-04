// Porta internal/iam/infra/postgres/canje.go @ 9a77307

package iampostgres

// canje.go — LOS CUATRO PASOS DEL CANJE, EN UNA SOLA TRANSACCIÓN (Plan 047 · Ola A · T-A3 + T-A4
// + T-A5). Conserva el nombre del fichero viejo (T-15): lo buscan por nombre los candados
// redeem_order y redeem_single_query.

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// InvitationRedeemRepo implementa out.InvitationRedeemRepo sobre public.tenant_invitations
// (migración 0085), public.tenant_members (0037) y public.access_requests (0060).
type InvitationRedeemRepo struct{}

// NewInvitationRedeemRepo construye el repositorio sobre el pool dado. No toca el pool.
//
// `features` es obligatorio por la misma razón que en NewMembershipRepo: canjear una invitación
// DA DE ALTA, y el desenlace de un alta depende del entitlement multi_empresa del tenant que
// invitó. Un nil aquí no desactiva el gate: lo deja contestando que no (fail-closed).
func NewInvitationRedeemRepo(db *sql.DB, features FeatureResolver) *InvitationRedeemRepo {
	panic(pendiente.Implementar("iampostgres.NewInvitationRedeemRepo"))
}

var _ out.InvitationRedeemRepo = (*InvitationRedeemRepo)(nil)

// Redeem implementa out.InvitationRedeemRepo (R-P5…R-P8): canjea la invitación del digest
// tokenHash para userID, con los cuatro pasos en UNA transacción —todo o nada—:
//
//  1. LEER la invitación por su digest en UNA sola consulta, que trae también el `now()` de la
//     base (R-P6): «no existe» y «caducada» cuestan lo mismo, y la caducidad se mide con el
//     reloj que escribió `expires_at`, no con el del proceso. El veredicto lo da
//     domain.EvaluateRedemption: inexistente → domain.ErrNotFound; caducada →
//     domain.ErrInvitationExpired; canjeada o revocada → error que envuelve domain.ErrConflict
//     («la invitación ya no se puede usar»). En los tres casos no se escribe nada.
//  2. DAR EL ACCESO con GrantTenantAccess —membresía y, si la invitación trae rol, ese rol— en
//     el tenant DE LA INVITACIÓN (ni del cuerpo de la petición ni del token de quien canjea),
//     dentro de la transacción. Si ya es miembro de otra empresa y el tenant no tiene
//     multi_empresa → ErrConflict y la invitación queda EXACTAMENTE como estaba: viva y usable.
//  3. MARCARLA canjeada (redeemed_at, redeemed_by) con un UPDATE condicionado a
//     `redeemed_at IS NULL AND revoked_at IS NULL` (R-P8): ahí vive el «un solo uso» y la
//     carrera con la revocación. Cero filas → ErrConflict y el rollback deshace el paso 2.
//     🔴 Va DESPUÉS del paso 2 (R-P5, candado redeem_order).
//  4. CERRAR la solicitud de acceso `pending` que el invitado dejó al registrarse ('approved',
//     decided_at = now(), decided_by NULL). Que no haya ninguna no es un fallo.
//
// No abrir la transacción → "iam: abrir tx de canje de invitación: …"; no confirmarla → "iam:
// confirmar el canje de la invitación: …".
func (r *InvitationRedeemRepo) Redeem(ctx context.Context, tokenHash []byte, userID string) error {
	panic(pendiente.Implementar("iampostgres.InvitationRedeemRepo.Redeem"))
}
