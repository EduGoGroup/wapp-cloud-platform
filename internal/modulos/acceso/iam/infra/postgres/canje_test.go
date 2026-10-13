package iampostgres

import (
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

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

// TestInvitationFromRow es R-P7 (D-F2-1, el 2.º test de canje_una_consulta_ast_test.go hecho
// conducta): las cuatro NULLables de la fila llegan a la entidad —nil si NULL, puntero a su valor
// si no— y con ellas el veredicto del canje. Perder revoked_at clasificaría como pendiente una
// invitación revocada; perder redeemed_at, una canjeada.
func TestInvitationFromRow(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	expires, redeemed, revoked := now.Add(48*time.Hour), now.Add(-time.Hour), now.Add(-2*time.Hour)
	roleID, redeemer := companyRoleID, "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"
	pending := invitationRow{ID: testInvitationID, TenantID: testTenantID, ExpiresAt: expires}

	withRole := pending
	withRole.RoleID = sql.NullString{String: roleID, Valid: true}
	redeemedRow := pending
	redeemedRow.RedeemedBy = sql.NullString{String: redeemer, Valid: true}
	redeemedRow.RedeemedAt = sql.NullTime{Time: redeemed, Valid: true}
	revokedRow := pending
	revokedRow.RevokedAt = sql.NullTime{Time: revoked, Valid: true}

	base := domain.Invitation{ID: testInvitationID, TenantID: testTenantID, ExpiresAt: expires}
	wantRole, wantRedeemed, wantRevoked := base, base, base
	wantRole.RoleID = &roleID
	wantRedeemed.RedeemedBy, wantRedeemed.RedeemedAt = &redeemer, &redeemed
	wantRevoked.RevokedAt = &revoked

	cases := []struct {
		name        string
		row         invitationRow
		want        domain.Invitation
		wantVerdict domain.RedemptionVerdict
	}{
		{"pending_nullables_are_nil", pending, base, domain.RedemptionProceeds},
		{"role_id_carried", withRole, wantRole, domain.RedemptionProceeds},
		{"redeemed_by_and_at_carried", redeemedRow, wantRedeemed, domain.RedemptionConsumed},
		{"revoked_at_carried", revokedRow, wantRevoked, domain.RedemptionConsumed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := invitationFromRow(c.row)
			if describeInvitation(got) != describeInvitation(c.want) {
				t.Errorf("invitationFromRow = %s;\nquiere            %s", describeInvitation(got), describeInvitation(c.want))
			}
			if verdict := domain.EvaluateRedemption(&got, now); verdict != c.wantVerdict {
				t.Errorf("el veredicto de la invitación trasladada es %v; quiere %v", verdict, c.wantVerdict)
			}
		})
	}
}

// TestCloseAccessRequest: cerrar la solicitud pendiente es UNA sentencia; que no toque ninguna
// fila (no había solicitud) NO es un fallo, y un fallo de la base se propaga con su texto.
func TestCloseAccessRequest(t *testing.T) {
	t.Run("zero_rows_is_not_an_error", func(t *testing.T) {
		exec := &recordingExecutor{pool: downPool(t)}
		if err := closeAccessRequest(t.Context(), exec, testUserID); err != nil {
			t.Fatalf("closeAccessRequest sin solicitud pendiente = %v; quiere nil", err)
		}
		if !slices.Equal(exec.calls, []string{"exec"}) {
			t.Errorf("operaciones = %v; quiere una sola sentencia [exec]", exec.calls)
		}
	})
	t.Run("infra_error_wrapped", func(t *testing.T) {
		exec := &recordingExecutor{pool: downPool(t), execErr: errPoolDown}
		err := closeAccessRequest(t.Context(), exec, testUserID)
		wantInfraError(t, "closeAccessRequest", err, "iam: cerrar la solicitud de acceso del invitado: ")
	})
}
