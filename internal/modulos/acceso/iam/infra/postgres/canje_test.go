//go:build pendiente

package iampostgres

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

var _ func(*sql.DB, FeatureResolver) *InvitationRedeemRepo = NewInvitationRedeemRepo

// TestNewInvitationRedeemRepo_NilResolverAllowed: el resolver nil es válido (fail-closed, no un
// pánico) y construir no toca el pool.
func TestNewInvitationRedeemRepo_NilResolverAllowed(t *testing.T) {
	if NewInvitationRedeemRepo(downPool(t), nil) == nil {
		t.Fatal("NewInvitationRedeemRepo(db, nil) devolvió nil")
	}
}

// TestRedeem_InfraErrorIsNoVerdict: si la transacción no se puede abrir, el canje falla con su
// texto y la causa envuelta, y NO con uno de los veredictos de negocio (inexistente, caducada o
// ya usada): un fallo de infraestructura no puede contestarle a quien canjea algo sobre su token.
func TestRedeem_InfraErrorIsNoVerdict(t *testing.T) {
	err := NewInvitationRedeemRepo(downPool(t), &recordingResolver{has: true}).
		Redeem(t.Context(), domain.HashInvitationToken("token-de-prueba"), testUserID)
	wantInfraError(t, "Redeem", err, "iam: abrir tx de canje de invitación: ")
	for _, verdict := range []error{domain.ErrNotFound, domain.ErrInvitationExpired, domain.ErrConflict} {
		if errors.Is(err, verdict) {
			t.Errorf("Redeem con la base caída = %v; no es el veredicto %v", err, verdict)
		}
	}
}
