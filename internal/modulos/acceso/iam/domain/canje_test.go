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

// Los cuatro veredictos son valores distintos y el valor cero del tipo es RedemptionMissing
// (D-F2-10): un veredicto sin calcular rechaza. RedemptionProceeds NO puede ser el cero.
func TestRedemptionVerdict_ZeroValueRejects(t *testing.T) {
	var unset RedemptionVerdict
	if unset != RedemptionMissing {
		t.Errorf("el valor cero de RedemptionVerdict es %d; quiere RedemptionMissing (%d)", unset, RedemptionMissing)
	}
	if unset == RedemptionProceeds {
		t.Error("el valor cero de RedemptionVerdict deja seguir: un veredicto sin calcular abriría el canje")
	}
	seen := map[RedemptionVerdict]string{}
	for name, v := range map[string]RedemptionVerdict{
		"RedemptionMissing": RedemptionMissing, "RedemptionProceeds": RedemptionProceeds,
		"RedemptionExpired": RedemptionExpired, "RedemptionConsumed": RedemptionConsumed,
	} {
		if other, dup := seen[v]; dup {
			t.Errorf("%s y %s valen lo mismo (%d)", name, other, v)
		}
		seen[v] = name
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
		want RedemptionVerdict
	}{
		{"alive_proceeds", &Invitation{TenantID: "t-1", ExpiresAt: after}, RedemptionProceeds},
		{"nil_is_absent", nil, RedemptionMissing},
		{"expired", &Invitation{TenantID: "t-1", ExpiresAt: before}, RedemptionExpired},
		{"expires_exactly_now", &Invitation{TenantID: "t-1", ExpiresAt: now}, RedemptionExpired},
		{"already_redeemed", &Invitation{TenantID: "t-1", ExpiresAt: after, RedeemedBy: &someone, RedeemedAt: &before}, RedemptionConsumed},
		{"revoked", &Invitation{TenantID: "t-1", ExpiresAt: after, RevokedAt: &before}, RedemptionConsumed},
		// Si esto saliera RedemptionExpired, un token ya usado daría 410 en vez de 409.
		{"redeemed_and_expired_is_consumed", &Invitation{TenantID: "t-1", ExpiresAt: before, RedeemedBy: &someone, RedeemedAt: &before}, RedemptionConsumed},
		{"revoked_and_expired_is_consumed", &Invitation{TenantID: "t-1", ExpiresAt: before, RevokedAt: &before}, RedemptionConsumed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EvaluateRedemption(c.inv, now); got != c.want {
				t.Errorf("EvaluateRedemption = %d; quiere %d", got, c.want)
			}
		})
	}
}
