package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// El vigésimo centinela: texto observable literal, reconocible envuelto y distinto de los dos
// genéricos que el canje también usa (ausencia y terminal-por-escritura).
func TestErrInvitationExpired_LiteralAndDistinct(t *testing.T) {
	if got, want := ErrInvitationExpired.Error(), "iam: invitación caducada"; got != want {
		t.Fatalf("ErrInvitationExpired = %q; quiere el literal %q", got, want)
	}
	wrapped := fmt.Errorf("canje: %w", ErrInvitationExpired)
	if !errors.Is(wrapped, ErrInvitationExpired) {
		t.Error("errors.Is no reconoce ErrInvitationExpired envuelto con %w")
	}
	for _, other := range errorsSentinels() {
		if errors.Is(wrapped, other.err) {
			t.Errorf("ErrInvitationExpired envuelto se reconoce también como %s", other.name)
		}
	}
}

// Los cuatro veredictos son valores distintos, y su orden es el del viejo (iota). Se fija para
// que un cambio de orden se decida y no se cuele: el valor cero del tipo es CanjeProcede.
func TestResultadoCanje_FourDistinctVerdicts(t *testing.T) {
	verdicts := []ResultadoCanje{CanjeProcede, CanjeAusente, CanjeCaducado, CanjeConsumido}
	for i, v := range verdicts {
		if int(v) != i {
			t.Errorf("veredicto %d vale %d; quiere %d (orden del viejo)", i, v, i)
		}
	}
}

// R-D5: los cuatro veredictos, con el reloj inyectado. nil es «no había fila», no un error del
// llamante; redeemed y revoked comparten veredicto (anti-chivato); la precedencia es la de
// Invitation.Status.
func TestEvaluarCanje_FourVerdicts(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	before := now.Add(-time.Hour)
	after := now.Add(time.Hour)
	someone := "usr-other"
	cases := []struct {
		name string
		inv  *Invitation
		want ResultadoCanje
	}{
		{"alive_proceeds", &Invitation{TenantID: "t-1", ExpiresAt: after}, CanjeProcede},
		{"nil_is_absent", nil, CanjeAusente},
		{"expired", &Invitation{TenantID: "t-1", ExpiresAt: before}, CanjeCaducado},
		{"expires_exactly_now", &Invitation{TenantID: "t-1", ExpiresAt: now}, CanjeCaducado},
		{"already_redeemed", &Invitation{TenantID: "t-1", ExpiresAt: after, RedeemedBy: &someone, RedeemedAt: &before}, CanjeConsumido},
		{"revoked", &Invitation{TenantID: "t-1", ExpiresAt: after, RevokedAt: &before}, CanjeConsumido},
		// Si esto saliera CanjeCaducado, un token ya usado daría 410 en vez de 409.
		{"redeemed_and_expired_is_consumed", &Invitation{TenantID: "t-1", ExpiresAt: before, RedeemedBy: &someone, RedeemedAt: &before}, CanjeConsumido},
		{"revoked_and_expired_is_consumed", &Invitation{TenantID: "t-1", ExpiresAt: before, RevokedAt: &before}, CanjeConsumido},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EvaluarCanje(c.inv, now); got != c.want {
				t.Errorf("EvaluarCanje = %d; quiere %d", got, c.want)
			}
		})
	}
}
