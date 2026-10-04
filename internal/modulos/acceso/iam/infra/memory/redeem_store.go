// Nuevo: el gemelo en memoria de out.InvitationRedeemRepo (diseño F2 §2, tareas T2.9). El viejo
// no tenía: reproduce internal/iam/infra/postgres/canje.go @ 9a77307 y la parte de
// GrantTenantAccess (memberships.go) que el canje recorre.

package memory

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// AccessRequest es la solicitud de acceso de una persona en el doble: lo que de
// public.access_requests puede tocar un canje. La devuelve
// RedeemStore.AccessRequestOf.
type AccessRequest struct {
	// Status es "pending" o "approved".
	Status string
	// DecidedAt es cuándo dejó de estar pendiente (nil mientras lo está).
	DecidedAt *time.Time
}

// RedeemStore implementa out.InvitationRedeemRepo en memoria, sobre los MISMOS
// dobles que sirven a los otros puertos —InvitationStore (tenant_invitations),
// MembershipStore (tenant_members, con su resolver de multi_empresa) y RoleStore
// (iam_user_roles)— más su propia bandeja de solicitudes (access_requests), que
// ningún otro puerto del IAM toca. Pasa outhelpertest.ContratoInvitationRedeemRepo,
// la misma suite que el adaptador Postgres.
//
// Reproduce los cuatro pasos de infra/postgres/canje.go así:
//
//  1. LEER: busca la fila por su digest y la juzga con domain.EvaluateRedemption
//     contra el reloj del InvitationStore, que hace aquí de `now()` de la base
//     —el mismo reloj que escribió created_at y, al sembrar, expires_at—.
//     Inexistente → domain.ErrNotFound; caducada → domain.ErrInvitationExpired;
//     canjeada o revocada → domain.ErrConflict. Ninguno escribe.
//  2. DAR EL ACCESO, en el tenant DE LA FILA (inv.TenantID; el puerto no recibe
//     otro): la guarda de ámbito del rol (checkAssignmentScope), la guarda de la
//     segunda empresa y la membresía (MembershipStore.grantLocked, la misma que
//     Add: sin multi_empresa en el destino, o con el resolver caído, el 409 de
//     siempre) y, si la invitación promete rol, la asignación acotada a ese tenant.
//  3. MARCARLA: redeemed_by = quien canjea, redeemed_at = el reloj.
//  4. CERRAR la solicitud 'pending' de quien canjea: 'approved' con decided_at (y
//     sin decidido-por: no decidió ningún operador). Sin solicitud, nada; no es un
//     fallo.
//
// 🔴 TODO O NADA, Y SIN ROLLBACK. Postgres lo consigue con una transacción y con
// el ORDEN (el paso 2, que es el que puede rechazar, va antes que el 3: un
// rechazo no quema la invitación). Aquí Redeem toma los candados de los tres
// dobles y el suyo durante los cuatro pasos —siempre en el mismo orden:
// invitaciones, membresías, roles, solicitudes; ninguna otra operación toma más
// de uno, así que no hay interbloqueo—, y todo lo que puede rechazar (el
// veredicto y las dos guardas) se decide ANTES de la primera escritura. Así un
// canje rechazado no deja rastro, y el «un solo uso» del UPDATE condicionado del
// paso 3 —`redeemed_at IS NULL AND revoked_at IS NULL`— se cumple solo: con el
// candado de las invitaciones tomado desde el paso 1, ni otro canje ni una
// revocación caben entre la lectura y la marca.
type RedeemStore struct {
	invitations *InvitationStore
	memberships *MembershipStore
	roles       *RoleStore
	// mu guarda requests y se toma el ÚLTIMO de los cuatro candados.
	mu       sync.Mutex
	requests map[string]AccessRequest // userID → su solicitud de acceso
}

// NewRedeemStore construye el canje sobre los tres dobles que escribe. Ninguno
// puede ser nil (un canje sin alguna de sus tablas no es un canje): con alguno
// nil, entra en pánico, porque es un error de cableado del test.
func NewRedeemStore(invitations *InvitationStore, memberships *MembershipStore, roles *RoleStore) *RedeemStore {
	if invitations == nil || memberships == nil || roles == nil {
		panic("memory: NewRedeemStore requiere los tres stores (invitaciones, membresías y roles)")
	}
	return &RedeemStore{
		invitations: invitations,
		memberships: memberships,
		roles:       roles,
		requests:    make(map[string]AccessRequest),
	}
}

var _ out.InvitationRedeemRepo = (*RedeemStore)(nil)

// SeedPendingAccessRequest deja a userID una solicitud 'pending' (helper de
// tests): la que el signup público deja al registrarse el invitado. Reemplaza la
// que tuviera.
func (r *RedeemStore) SeedPendingAccessRequest(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests[userID] = AccessRequest{Status: "pending"}
}

// AccessRequestOf devuelve una copia de la solicitud de userID y ok=false si no
// tiene (helper de tests).
func (r *RedeemStore) AccessRequestOf(userID string) (AccessRequest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, ok := r.requests[userID]
	if ok && req.DecidedAt != nil {
		at := *req.DecidedAt
		req.DecidedAt = &at
	}
	return req, ok
}

// Redeem implementa out.InvitationRedeemRepo: los cuatro pasos de la cabecera
// del tipo, bajo los cuatro candados.
func (r *RedeemStore) Redeem(ctx context.Context, tokenHash []byte, userID string) error {
	r.invitations.mu.Lock()
	defer r.invitations.mu.Unlock()
	r.memberships.mu.Lock()
	defer r.memberships.mu.Unlock()
	r.roles.mu.Lock()
	defer r.roles.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()

	// (1) LEER y juzgar, con el reloj de la «base».
	now := r.invitations.now().UTC()
	inv := r.invitations.byTokenHashLocked(tokenHash)
	switch domain.EvaluateRedemption(inv, now) {
	case domain.RedemptionMissing:
		return domain.ErrNotFound
	case domain.RedemptionExpired:
		return domain.ErrInvitationExpired
	case domain.RedemptionConsumed:
		return fmt.Errorf("%w: la invitación ya no se puede usar", domain.ErrConflict)
	case domain.RedemptionProceeds:
		// sigue abajo
	}

	// (2) DAR EL ACCESO en el tenant de la fila, ANTES de marcar nada: si la guarda
	// rechaza, la invitación queda intacta y usable.
	if inv.RoleID != nil {
		if err := checkAssignmentScope(*inv.RoleID, &inv.TenantID); err != nil {
			return err
		}
	}
	if err := r.memberships.grantLocked(ctx, userID, inv.TenantID); err != nil {
		return err
	}
	if inv.RoleID != nil {
		r.roles.assignLocked(userID, *inv.RoleID, &inv.TenantID)
	}

	// (3) MARCARLA canjeada. La condición del UPDATE de Postgres ya la garantiza el
	// candado: nadie la ha tocado desde el paso (1).
	inv.RedeemedBy = &userID
	inv.RedeemedAt = &now
	r.invitations.byID[inv.ID] = *inv

	// (4) CERRAR la solicitud pendiente, si la hay.
	if req, ok := r.requests[userID]; ok && req.Status == "pending" {
		r.requests[userID] = AccessRequest{Status: "approved", DecidedAt: &now}
	}
	return nil
}

// byTokenHashLocked devuelve una copia de la fila con ese digest, o nil si no hay:
// la ausencia entra en EvaluateRedemption por el mismo parámetro que la presencia.
// El llamante tiene el candado del InvitationStore.
func (s *InvitationStore) byTokenHashLocked(tokenHash []byte) *domain.Invitation {
	for _, inv := range s.byID {
		if bytes.Equal(inv.TokenHash, tokenHash) {
			return &inv
		}
	}
	return nil
}
