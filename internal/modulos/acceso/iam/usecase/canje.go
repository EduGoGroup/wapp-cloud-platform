// Porta internal/iam/usecase/canje.go @ 9a77307

package usecase

// canje.go — EL CANJE DE UNA INVITACIÓN (Plan 047 · Ola A · T-A3/T-A4/T-A5). El fichero
// conserva el nombre del viejo (T-15: el candado AST de iam/infra/postgres lo busca por nombre).
//
// Este servicio es DELGADO a propósito. Aporta las dos cosas que ni el transporte ni el
// adaptador pueden saber:
//
//  1. QUIÉN canjea — sale del contexto de identidad (INV-04), nunca del cuerpo.
//  2. Que el texto pegado desde WhatsApp se convierte en digest AQUÍ, con
//     domain.HashInvitationToken, y que el token en claro no cruza hacia la capa que compone SQL.
//
// La atomicidad de los cuatro pasos, su orden y el UPDATE condicionado que da el «un solo uso»
// viven en el adaptador (out.InvitationRedeemRepo), porque es donde vive la transacción.

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// RedeemService implementa in.InvitationRedeemer.
type RedeemService struct{}

// compile-time: RedeemService satisface el puerto de entrada.
var _ in.InvitationRedeemer = (*RedeemService)(nil)

// NewRedeemService construye el servicio. Las dos dependencias son estructurales y un nil se
// rechaza al arrancar (fail-fast, servicio nil y textos literales):
//
//   - caller nil ⇒ "iam: RedeemService requiere un CallerResolver (INV-04: quien canjea sale del
//     contexto)";
//   - repo nil ⇒ "iam: RedeemService requiere un InvitationRedeemRepo".
//
// No recibe cliente de identity: canjear no acredita nada allí (quien canjea ya trae un Context
// Token, luego ya pasó el System Gate).
func NewRedeemService(caller in.CallerResolver, repo out.InvitationRedeemRepo) (*RedeemService, error) {
	panic(pendiente.Implementar("usecase.NewRedeemService"))
}

// RedeemInvitation implementa in.InvitationRedeemer: canjea token a nombre del Caller.
//
// R-U29: quien canjea sale del CONTEXTO —repo.Redeem recibe el UserID del Caller y ningún
// otro—, y la empresa sale de la FILA de la invitación (la decide el adaptador), nunca de quien
// llama. Se exige el SUJETO y NO la empresa: quien canjea tiene, por definición, cero
// membresías y un Context Token SIN tenant (D-056.12), y exigírsela cerraría la puerta a los
// únicos que la necesitan.
//
//   - Contexto sin identidad o con UserID vacío ⇒ domain.ErrInvalidInput (envuelto con «el
//     contexto no acredita a nadie»), sin llamar al repositorio. Es 400 y no 401: a este
//     método solo se llega detrás de Authenticate.
//   - Token vacío ⇒ domain.ErrInvalidInput (envuelto con «token vacío»), sin llamar al
//     repositorio.
//   - Cualquier otro token —también uno «sin pinta»— se hashea con domain.HashInvitationToken
//     (que es quien normaliza; aquí no se repite) y se pasa al repositorio el DIGEST, nunca el
//     texto en claro. Sin validación de forma previa: un atajo así respondería antes sin
//     consultar y la diferencia de tiempo sería un oráculo.
//   - Los desenlaces del repositorio se devuelven tal cual: nil (ya es miembro de la empresa de
//     la invitación), domain.ErrNotFound, domain.ErrInvitationExpired, domain.ErrConflict.
func (s *RedeemService) RedeemInvitation(ctx context.Context, token string) error {
	panic(pendiente.Implementar("usecase.RedeemService.RedeemInvitation"))
}
