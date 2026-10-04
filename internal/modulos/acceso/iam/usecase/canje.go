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
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// RedeemService implementa in.InvitationRedeemer.
type RedeemService struct {
	caller in.CallerResolver
	repo   out.InvitationRedeemRepo
}

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
//
// ⚠️ Aquí NO hay la asimetría del `systems` de MembershipService, y no es un olvido: sin resolver
// quién llama no se puede escribir la membresía a nombre de nadie, y sin repositorio no hay canje.
// Un nil en cualquiera de las dos es un error de cableado, igual que en NewMembershipService.
func NewRedeemService(caller in.CallerResolver, repo out.InvitationRedeemRepo) (*RedeemService, error) {
	if caller == nil {
		return nil, errors.New("iam: RedeemService requiere un CallerResolver (INV-04: quien canjea sale del contexto)")
	}
	if repo == nil {
		return nil, errors.New("iam: RedeemService requiere un InvitationRedeemRepo")
	}
	return &RedeemService{caller: caller, repo: repo}, nil
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
//
// 🔴 POR QUÉ NO SE LLAMA A tenantOf. El resto de este paquete resuelve el Caller con `tenantOf`,
// que rechaza con domain.ErrNoTenant cuando `TenantID` viene vacío — y hace bien: administrar
// roles o miembros sin empresa no significa nada. Pero el canje es justo el endpoint que ESTRENA
// la empresa de esa persona, así que este método comprueba solo el sujeto.
//
// ⚠️ POR QUÉ NO SE ACREDITA `wapp.bff` EN IDENTITY, que sí hace AddMember. Allí era
// imprescindible: el administrador da de alta a una persona que puede no haber entrado nunca, y
// sin la aplicación acreditada quedaría siendo miembro y sin poder entrar. Aquí es imposible que
// falte: para llegar a este método hay que traer un Context Token de wApp, y ese token solo
// existe si esa persona ya pasó el System Gate de identity y canjeó su Identity Token. Quien
// canjea YA está dentro. Además, una llamada de red a identity dentro del canje tendría que
// ocurrir con la transacción de los cuatro pasos abierta, o fuera de ella y sin poder deshacerse:
// las dos opciones son peores que no necesitarla.
func (s *RedeemService) RedeemInvitation(ctx context.Context, token string) error {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.UserID == "" {
		// Sin sujeto acreditado no hay a nombre de quién escribir la membresía. Es 400 y no 401:
		// a este método solo se llega DETRÁS de Authenticate, así que un contexto sin identidad
		// es un error de cableado del servidor, no una credencial que falte.
		return fmt.Errorf("%w: el contexto no acredita a nadie", domain.ErrInvalidInput)
	}
	if token == "" {
		return fmt.Errorf("%w: token vacío", domain.ErrInvalidInput)
	}
	// 🔴 EL HASH SE CALCULA SIEMPRE, Y ES PARTE DEL ANTI-ORÁCULO. Aquí no hay —ni puede haber—
	// una validación de FORMA que rechace un token «que no tiene pinta» antes de llegar a la base:
	// un atajo así respondería 404 sin haber hasheado ni consultado, y esa diferencia de tiempo
	// distinguiría «token mal formado» de «token bien formado que no existe». Todo lo que no esté
	// vacío paga el mismo camino completo.
	//
	// La normalización (TrimSpace + ToUpper) va DENTRO de HashInvitationToken, en el único sitio
	// que hashea, para que el emisor y el canje no puedan discrepar. No la repitas aquí.
	return s.repo.Redeem(ctx, domain.HashInvitationToken(token), c.UserID)
}
