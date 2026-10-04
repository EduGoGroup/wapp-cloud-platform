//go:build pendiente

package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// spyRedeemRepo es el doble propio de out.InvitationRedeemRepo: anota con qué se le llamó y
// devuelve lo que se le diga.
type spyRedeemRepo struct {
	calls  int
	hash   []byte
	userID string
	err    error
}

var _ out.InvitationRedeemRepo = (*spyRedeemRepo)(nil)

func (s *spyRedeemRepo) Redeem(_ context.Context, tokenHash []byte, userID string) error {
	s.calls++
	s.hash, s.userID = bytes.Clone(tokenHash), userID
	return s.err
}

func newRedeemService(t *testing.T, repo out.InvitationRedeemRepo) *RedeemService {
	t.Helper()
	svc, err := NewRedeemService(testResolver, repo)
	if err != nil {
		t.Fatalf("NewRedeemService: %v", err)
	}
	return svc
}

func TestNewRedeemService_RequiresDependencies(t *testing.T) {
	if svc, err := NewRedeemService(nil, &spyRedeemRepo{}); err == nil || svc != nil ||
		err.Error() != "iam: RedeemService requiere un CallerResolver (INV-04: quien canjea sale del contexto)" {
		t.Errorf("sin CallerResolver = %v, %v; quiere nil y el literal", svc, err)
	}
	if svc, err := NewRedeemService(testResolver, nil); err == nil || svc != nil ||
		err.Error() != "iam: RedeemService requiere un InvitationRedeemRepo" {
		t.Errorf("sin repositorio = %v, %v; quiere nil y el literal", svc, err)
	}
}

// R-U29: quien canjea sale del contexto, y basta el sujeto (sin empresa).
func TestRedeemInvitation_RedeemerComesFromContext(t *testing.T) {
	repo := &spyRedeemRepo{}
	svc := newRedeemService(t, repo)
	userID := uuid.NewString()
	if err := svc.RedeemInvitation(ctxNoCompany(userID), "WAPP-INV-0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("RedeemInvitation: %v (quien canjea no trae empresa, y es lo normal)", err)
	}
	if repo.calls != 1 || repo.userID != userID {
		t.Fatalf("Redeem llamado %d veces con %q; quiere una con el sujeto del contexto %q", repo.calls, repo.userID, userID)
	}
}

// Viaja el DIGEST de HashInvitationToken, nunca el token en claro; sin validación de forma.
func TestRedeemInvitation_PassesDigestNeverClearToken(t *testing.T) {
	for name, token := range map[string]string{
		"canonical":          "WAPP-INV-0123456789abcdef0123456789abcdef",
		"pasted_from_chat":   "  wapp-inv-0123456789abcdef0123456789abcdef \n",
		"no_shape_at_all":    "no-tiene-pinta",
		"single_character_x": "x",
	} {
		t.Run(name, func(t *testing.T) {
			repo := &spyRedeemRepo{}
			if err := newRedeemService(t, repo).RedeemInvitation(ctxNoCompany(uuid.NewString()), token); err != nil {
				t.Fatalf("RedeemInvitation: %v", err)
			}
			if !bytes.Equal(repo.hash, domain.HashInvitationToken(token)) {
				t.Fatalf("digest = %x; quiere HashInvitationToken(%q)", repo.hash, token)
			}
			if bytes.Contains(repo.hash, []byte(token)) {
				t.Fatal("el token en claro cruzó hacia el repositorio")
			}
		})
	}
}

func TestRedeemInvitation_InvalidInputDoesNotReachRepo(t *testing.T) {
	cases := []struct {
		name  string
		ctx   context.Context
		token string
	}{
		{"no_identity", context.Background(), "WAPP-INV-x"},
		{"identity_without_subject", withCaller(context.Background(), in.Caller{TenantID: testTenant}), "WAPP-INV-x"},
		{"empty_token", ctxNoCompany(uuid.NewString()), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := &spyRedeemRepo{}
			if err := newRedeemService(t, repo).RedeemInvitation(c.ctx, c.token); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
			if repo.calls != 0 {
				t.Fatalf("el repositorio se llamó %d veces; quiere 0", repo.calls)
			}
		})
	}
}

func TestRedeemInvitation_RepoOutcomesPropagate(t *testing.T) {
	for name, outcome := range map[string]error{
		"missing":  domain.ErrNotFound,
		"expired":  domain.ErrInvitationExpired,
		"consumed": fmt.Errorf("%w: la invitación ya no se puede usar", domain.ErrConflict),
	} {
		t.Run(name, func(t *testing.T) {
			svc := newRedeemService(t, &spyRedeemRepo{err: outcome})
			if err := svc.RedeemInvitation(ctxNoCompany(uuid.NewString()), "WAPP-INV-x"); !errors.Is(err, outcome) {
				t.Fatalf("err = %v; quiere %v", err, outcome)
			}
		})
	}
}

// De punta a punta con el doble: la empresa sale de la FILA, no de quien llama.
func TestRedeemInvitation_CompanyComesFromInvitationRow(t *testing.T) {
	store := memory.NewStore().WithClock(tickingClock())
	const token = "WAPP-INV-0123456789abcdef0123456789abcdef"
	store.Invitations.Seed(domain.Invitation{
		TenantID:  testTenantB,
		TokenHash: domain.HashInvitationToken(token),
		ExpiresAt: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		CreatedBy: uuid.NewString(),
	})
	userID := uuid.NewString()
	// El Caller trae (indebidamente) otra empresa: no puede influir en el canje.
	ctx := withCaller(context.Background(), in.Caller{TenantID: testTenant, UserID: userID})
	if err := newRedeemService(t, store.Redeem).RedeemInvitation(ctx, token); err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	tenants, err := store.Memberships.TenantsOfUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(tenants) != 1 || tenants[0] != testTenantB {
		t.Fatalf("membresías = %v; quiere [%s], la empresa de la invitación", tenants, testTenantB)
	}
}
