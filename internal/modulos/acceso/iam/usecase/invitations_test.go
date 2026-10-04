package usecase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// tickingClock devuelve un reloj que avanza un segundo en cada lectura: el orden por
// created_at del doble no depende del reloj real.
func tickingClock() func() time.Time {
	var mu sync.Mutex
	at := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(time.Second)
		return at
	}
}

type invitationFixture struct {
	svc   *InvitationService
	store *memory.Store
}

func newInvitationFixture(t *testing.T) invitationFixture {
	t.Helper()
	store := memory.NewStore().WithClock(tickingClock())
	svc, err := NewInvitationService(testResolver, store.Invitations, store.Roles)
	if err != nil {
		t.Fatalf("NewInvitationService: %v", err)
	}
	return invitationFixture{svc: svc, store: store}
}

func (f invitationFixture) issue(t *testing.T, tenantID string, input in.IssueInvitationInput) in.IssuedInvitation {
	t.Helper()
	issued, err := f.svc.IssueInvitation(ctxOf(tenantID), input)
	if err != nil {
		t.Fatalf("IssueInvitation(%s): %v", tenantID, err)
	}
	return issued
}

func (f invitationFixture) rowsOf(t *testing.T, tenantID string) []domain.Invitation {
	t.Helper()
	rows, err := f.store.Invitations.ListByTenant(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	return rows
}

func TestNewInvitationService_RequiresDependencies(t *testing.T) {
	store := memory.NewStore()
	cases := []struct {
		name  string
		build func() (*InvitationService, error)
		want  string
	}{
		{"nil_caller", func() (*InvitationService, error) { return NewInvitationService(nil, store.Invitations, store.Roles) },
			"iam: InvitationService requiere un CallerResolver (INV-04: el tenant sale del contexto)"},
		{"nil_invitations", func() (*InvitationService, error) { return NewInvitationService(testResolver, nil, store.Roles) },
			"iam: InvitationService requiere un InvitationRepo"},
		{"nil_roles", func() (*InvitationService, error) { return NewInvitationService(testResolver, store.Invitations, nil) },
			"iam: InvitationService requiere un RoleRepo para validar el rol prometido"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := c.build()
			if err == nil || svc != nil || err.Error() != c.want {
				t.Fatalf("= %v, %v; quiere nil y el literal %q", svc, err, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U27 · emitir
// ---------------------------------------------------------------------------

func TestIssueInvitation_ForCallerCompanyStoresOnlyDigest(t *testing.T) {
	f := newInvitationFixture(t)
	issued := f.issue(t, testTenant, in.IssueInvitationInput{})

	inv := issued.Invitation
	if inv.TenantID != testTenant || inv.CreatedBy != "admin-"+testTenant {
		t.Fatalf("fila = %+v; quiere tenant %q y creada por quien llama", inv, testTenant)
	}
	if !strings.HasPrefix(issued.Token, domain.InvitationTokenPrefix) {
		t.Fatalf("token = %q; quiere el prefijo %q", issued.Token, domain.InvitationTokenPrefix)
	}
	want := domain.HashInvitationToken(issued.Token)
	if len(inv.TokenHash) != 32 || !bytes.Equal(inv.TokenHash, want) {
		t.Fatalf("token_hash = %x; quiere el digest de 32 bytes del token (%x)", inv.TokenHash, want)
	}
	stored, ok := f.store.Invitations.Get(inv.ID)
	if !ok || !bytes.Equal(stored.TokenHash, want) || bytes.Contains(stored.TokenHash, []byte(issued.Token)) {
		t.Fatalf("fila guardada = %+v; quiere solo el digest, nunca el token en claro", stored)
	}
}

func TestIssueInvitation_NoCompanyDoesNotIssue(t *testing.T) {
	f := newInvitationFixture(t)
	for name, ctx := range map[string]context.Context{
		"no_identity":              context.Background(),
		"identity_without_company": withCaller(context.Background(), in.Caller{UserID: "without-company"}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.IssueInvitation(ctx, in.IssueInvitationInput{}); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("IssueInvitation: err = %v; quiere ErrNoTenant", err)
			}
			if _, err := f.svc.ListInvitations(ctx); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("ListInvitations: err = %v; quiere ErrNoTenant", err)
			}
			if err := f.svc.RevokeInvitation(ctx, uuid.NewString()); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("RevokeInvitation: err = %v; quiere ErrNoTenant", err)
			}
		})
	}
	if rows := f.rowsOf(t, testTenant); len(rows) != 0 {
		t.Fatalf("se escribió %+v sin empresa", rows)
	}
}

// Default 24 h y clamp a [60 s, 30 días], acotado por rango (el servicio usa el reloj real).
func TestIssueInvitation_TTLDefaultAndClamp(t *testing.T) {
	const day = 86400 * time.Second
	cases := []struct {
		name string
		ttl  int
		want time.Duration
	}{
		{"absent_zero_is_one_day", 0, day},
		{"negative_is_absent_not_expired", -1, day},
		{"below_floor_is_sixty_seconds", 5, time.Minute},
		{"exactly_floor", 60, time.Minute},
		{"normal_value_is_kept", 3600, time.Hour},
		{"exactly_ceiling", 30 * 86400, 30 * day},
		{"above_ceiling_is_thirty_days", 365 * 86400, 30 * day},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newInvitationFixture(t)
			before := time.Now().UTC()
			issued := f.issue(t, testTenant, in.IssueInvitationInput{TTLSeconds: c.ttl})
			after := time.Now().UTC()
			exp := issued.Invitation.ExpiresAt
			if exp.Before(before.Add(c.want)) || exp.After(after.Add(c.want)) {
				t.Fatalf("expira %v; quiere entre %v y %v (vida %v)", exp, before.Add(c.want), after.Add(c.want), c.want)
			}
		})
	}
}

func TestIssueInvitation_PromisedRoleMustBeVisible(t *testing.T) {
	f := newInvitationFixture(t)
	own := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "support-a"}, nil)
	template := f.store.Roles.Seed(domain.Role{Name: "viewer"}, nil)
	foreign := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "support-b"}, nil)

	for name, role := range map[string]domain.Role{"own_role": own, "global_template": template} {
		t.Run(name, func(t *testing.T) {
			issued := f.issue(t, testTenant, in.IssueInvitationInput{RoleID: ptr(role.ID)})
			if issued.Invitation.RoleID == nil || *issued.Invitation.RoleID != role.ID {
				t.Fatalf("role_id = %v; quiere %q", issued.Invitation.RoleID, role.ID)
			}
		})
	}
	for name, roleID := range map[string]string{"foreign_role": foreign.ID, "missing_role": uuid.NewString()} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.IssueInvitation(ctxOf(testTenant), in.IssueInvitationInput{RoleID: ptr(roleID)}); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v; quiere ErrNotFound", err)
			}
		})
	}
	if rows := f.rowsOf(t, testTenant); len(rows) != 2 {
		t.Fatalf("filas = %d; quiere 2: un rechazo no deja fila", len(rows))
	}
}

func TestIssueInvitation_NoRoleIsLegitimate(t *testing.T) {
	f := newInvitationFixture(t)
	for name, role := range map[string]*string{"nil": nil, "empty_string": ptr(""), "only_spaces": ptr("   ")} {
		t.Run(name, func(t *testing.T) {
			if issued := f.issue(t, testTenant, in.IssueInvitationInput{RoleID: role}); issued.Invitation.RoleID != nil {
				t.Fatalf("role_id = %q; quiere nil", *issued.Invitation.RoleID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U28 · listar y revocar
// ---------------------------------------------------------------------------

func TestListInvitations_OnlyTheirCompany(t *testing.T) {
	f := newInvitationFixture(t)
	ofA := f.issue(t, testTenant, in.IssueInvitationInput{})
	f.issue(t, testTenantB, in.IssueInvitationInput{})
	invs, err := f.svc.ListInvitations(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invs) != 1 || invs[0].ID != ofA.Invitation.ID {
		t.Fatalf("A ve %+v; quiere solo la suya", invs)
	}
}

func TestListInvitations_MostRecentFirst(t *testing.T) {
	f := newInvitationFixture(t)
	first := f.issue(t, testTenant, in.IssueInvitationInput{})
	second := f.issue(t, testTenant, in.IssueInvitationInput{})
	third := f.issue(t, testTenant, in.IssueInvitationInput{})
	invs, err := f.svc.ListInvitations(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	want := []string{third.Invitation.ID, second.Invitation.ID, first.Invitation.ID}
	got := make([]string, 0, len(invs))
	for _, inv := range invs {
		got = append(got, inv.ID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("orden = %v; quiere %v (las más recientes primero)", got, want)
	}
}

// Se lee la fila: revocar marca y no borra.
func TestRevokeInvitation_LeavesRowRevoked(t *testing.T) {
	f := newInvitationFixture(t)
	issued := f.issue(t, testTenant, in.IssueInvitationInput{})
	if before, _ := f.store.Invitations.Get(issued.Invitation.ID); before.RevokedAt != nil {
		t.Fatal("la invitación nació revocada: el test no probaría nada")
	}
	if err := f.svc.RevokeInvitation(ctxOf(testTenant), issued.Invitation.ID); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}
	after, ok := f.store.Invitations.Get(issued.Invitation.ID)
	if !ok || after.RevokedAt == nil || after.Status(time.Now()) != domain.InvitationRevoked {
		t.Fatalf("fila = %+v (existe=%v); quiere la fila viva y revocada", after, ok)
	}
}

func TestRevokeInvitation_IdempotentOnRevoked(t *testing.T) {
	f := newInvitationFixture(t)
	issued := f.issue(t, testTenant, in.IssueInvitationInput{})
	for i := range 3 {
		if err := f.svc.RevokeInvitation(ctxOf(testTenant), issued.Invitation.ID); err != nil {
			t.Fatalf("revocación nº %d: %v", i+1, err)
		}
	}
}

func TestRevokeInvitation_RedeemedConflicts(t *testing.T) {
	f := newInvitationFixture(t)
	now := time.Now().UTC()
	redeemer := uuid.NewString()
	redeemed := f.store.Invitations.Seed(domain.Invitation{
		TenantID:   testTenant,
		TokenHash:  domain.HashInvitationToken("WAPP-INV-yacanjeada"),
		ExpiresAt:  now.Add(time.Hour),
		CreatedBy:  uuid.NewString(),
		RedeemedBy: &redeemer,
		RedeemedAt: &now,
	})
	if err := f.svc.RevokeInvitation(ctxOf(testTenant), redeemed.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v; quiere ErrConflict", err)
	}
	if row, _ := f.store.Invitations.Get(redeemed.ID); row.RevokedAt != nil {
		t.Fatal("una invitación canjeada quedó además revocada")
	}
}

func TestRevokeInvitation_DoesNotReachOtherCompany(t *testing.T) {
	f := newInvitationFixture(t)
	ofB := f.issue(t, testTenantB, in.IssueInvitationInput{})
	if err := f.svc.RevokeInvitation(ctxOf(testTenant), ofB.Invitation.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v; quiere ErrNotFound (un 403 confirmaría que existe)", err)
	}
	if row, ok := f.store.Invitations.Get(ofB.Invitation.ID); !ok || row.RevokedAt != nil {
		t.Fatal("la invitación de B quedó revocada por alguien de A")
	}
}

func TestRevokeInvitation_InvalidIDs(t *testing.T) {
	f := newInvitationFixture(t)
	cases := []struct {
		name string
		id   string
		want error
	}{
		{"empty_is_invalid_input", "", domain.ErrInvalidInput},
		{"not_a_uuid_is_not_found", "no-soy-un-uuid", domain.ErrNotFound},
		{"digits_are_not_found", "1234", domain.ErrNotFound},
		{"injection_is_not_found", "'; DROP TABLE x;--", domain.ErrNotFound},
		{"unknown_uuid_is_not_found", uuid.NewString(), domain.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := f.svc.RevokeInvitation(ctxOf(testTenant), c.id); !errors.Is(err, c.want) {
				t.Fatalf("id %q: err = %v; quiere %v", c.id, err, c.want)
			}
		})
	}
}
