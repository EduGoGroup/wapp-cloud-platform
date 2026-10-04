//go:build pendiente

package usecase

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// fakeUserSystems es el doble propio de out.UserSystemsClient: imita el PUT declarativo de
// identity y anota cada lectura y cada conjunto que viajó.
type fakeUserSystems struct {
	mu      sync.Mutex
	current []string
	errGet  error
	errPut  error
	gets    int
	puts    [][]string
}

var _ out.UserSystemsClient = (*fakeUserSystems)(nil)

func (f *fakeUserSystems) GetUserSystems(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.errGet != nil {
		return nil, f.errGet
	}
	return slices.Clone(f.current), nil
}

func (f *fakeUserSystems) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, slices.Clone(systems))
	if f.errPut != nil {
		return domain.IdentitySystemsDiff{}, f.errPut
	}
	f.current = slices.Clone(systems)
	return domain.IdentitySystemsDiff{Systems: slices.Clone(systems), Granted: []string{}, Revoked: []string{}}, nil
}

func (f *fakeUserSystems) written() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.puts)
}

// addCountingRepo cuenta las llamadas a Add: «no se escribió» se mide, no se deduce.
type addCountingRepo struct {
	out.MembershipRepo
	mu   sync.Mutex
	adds int
}

func (r *addCountingRepo) Add(ctx context.Context, userID, tenantID string) error {
	r.mu.Lock()
	r.adds++
	r.mu.Unlock()
	return r.MembershipRepo.Add(ctx, userID, tenantID)
}

func (r *addCountingRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.adds
}

type membershipFixture struct {
	svc      *MembershipService
	store    *memory.Store
	identity *fakeUserSystems
	adds     *addCountingRepo
}

func newMembershipFixture(t *testing.T) membershipFixture {
	t.Helper()
	store := memory.NewStore()
	identity := &fakeUserSystems{}
	adds := &addCountingRepo{MembershipRepo: store.Memberships}
	svc, err := NewMembershipService(testResolver, adds, identity, quietLogger())
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	return membershipFixture{svc: svc, store: store, identity: identity, adds: adds}
}

func (f membershipFixture) tenantsOf(t *testing.T, userID string) []string {
	t.Helper()
	tenants, err := f.store.Memberships.TenantsOfUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	return tenants
}

func (f membershipFixture) add(t *testing.T, tenantID, userID string) {
	t.Helper()
	if err := f.svc.AddMember(ctxOf(tenantID), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember(%s): %v", tenantID, err)
	}
}

func TestNewMembershipService_RequiresDependencies(t *testing.T) {
	store := memory.NewStore()
	if svc, err := NewMembershipService(nil, store.Memberships, &fakeUserSystems{}, nil); err == nil || svc != nil ||
		err.Error() != "iam: MembershipService requiere un CallerResolver (INV-04: el tenant sale del contexto)" {
		t.Errorf("sin CallerResolver = %v, %v; quiere nil y el literal", svc, err)
	}
	if svc, err := NewMembershipService(testResolver, nil, &fakeUserSystems{}, nil); err == nil || svc != nil ||
		err.Error() != "iam: MembershipService requiere un MembershipRepo" {
		t.Errorf("sin MembershipRepo = %v, %v; quiere nil y el literal", svc, err)
	}
	if svc, err := NewMembershipService(testResolver, store.Memberships, nil, nil); err != nil || svc == nil {
		t.Errorf("sin cliente M2M ni logger = %v, %v; quiere un servicio (despliegue legítimo)", svc, err)
	}
}

// ---------------------------------------------------------------------------
// R-U24 · alta y baja en la empresa del contexto
// ---------------------------------------------------------------------------

func TestAddMember_JoinsCallerCompany(t *testing.T) {
	f := newMembershipFixture(t)
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	if tenants := f.tenantsOf(t, userID); !slices.Equal(tenants, []string{testTenant}) {
		t.Fatalf("membresías = %v; quiere [%s]", tenants, testTenant)
	}
}

func TestAddMember_IsIdempotent(t *testing.T) {
	f := newMembershipFixture(t)
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	f.add(t, testTenant, userID)
	if tenants := f.tenantsOf(t, userID); len(tenants) != 1 {
		t.Fatalf("membresías = %v; quiere una sola", tenants)
	}
}

func TestAddMember_SecondCompanyWithoutMultiCompanyIsConflict(t *testing.T) {
	f := newMembershipFixture(t)
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	if err := f.svc.AddMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v; quiere ErrConflict", err)
	}
	if tenants := f.tenantsOf(t, userID); !slices.Equal(tenants, []string{testTenant}) {
		t.Fatalf("membresías = %v; la original debía quedar intacta", tenants)
	}
}

func TestAddMember_SecondCompanyWithMultiCompanyIsWritten(t *testing.T) {
	f := newMembershipFixture(t)
	features := entitlementshelpertest.NewFake()
	features.Enable(testTenantB, entitlements.FeatureMultiCompany)
	f.store.Memberships.WithFeatures(features)
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	f.add(t, testTenantB, userID)
	tenants := f.tenantsOf(t, userID)
	if len(tenants) != 2 || !slices.Contains(tenants, testTenant) || !slices.Contains(tenants, testTenantB) {
		t.Fatalf("membresías = %v; quiere las dos empresas", tenants)
	}
}

// T-11: el resolver caído MANTIENE el rechazo (fail-closed invertido).
func TestAddMember_ResolverDownKeepsTheRejection(t *testing.T) {
	f := newMembershipFixture(t)
	features := entitlementshelpertest.NewFake()
	features.Enable(testTenantB, entitlements.FeatureMultiCompany) // la TIENE...
	features.Err = errors.New("la base de entitlements no contesta")
	f.store.Memberships.WithFeatures(features) // ...pero no se puede acreditar.
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	if err := f.svc.AddMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v; quiere ErrConflict: un resolver caído no concede", err)
	}
	if tenants := f.tenantsOf(t, userID); len(tenants) != 1 {
		t.Fatalf("membresías = %v; no se podía escribir nada", tenants)
	}
}

func TestRemoveMember_OnlyFromOwnCompany(t *testing.T) {
	f := newMembershipFixture(t)
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	if err := f.svc.RemoveMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("RemoveMember desde otra empresa: %v; quiere no-op sin error", err)
	}
	if tenants := f.tenantsOf(t, userID); len(tenants) != 1 {
		t.Fatalf("membresías = %v; la de A no era de B para borrarla", tenants)
	}
	if err := f.svc.RemoveMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if tenants := f.tenantsOf(t, userID); len(tenants) != 0 {
		t.Fatalf("membresías = %v; la baja debía dejarlo sin ninguna", tenants)
	}
}

func TestListMembers_OnlyCallerCompany(t *testing.T) {
	f := newMembershipFixture(t)
	mine, theirs := uuid.NewString(), uuid.NewString()
	f.store.Memberships.Seed(mine, testTenant)
	f.store.Memberships.Seed(theirs, testTenantB)
	members, err := f.svc.ListMembers(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 1 || members[0].UserID != mine {
		t.Fatalf("miembros = %+v; quiere solo %q", members, mine)
	}
}

func TestMembershipService_NoTenantInContextExecutesNothing(t *testing.T) {
	f := newMembershipFixture(t)
	userID := uuid.NewString()
	for name, ctx := range map[string]context.Context{
		"no_identity":              context.Background(),
		"identity_without_company": withCaller(context.Background(), in.Caller{UserID: "subject-without-company"}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.svc.ListMembers(ctx); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("ListMembers: err = %v; quiere ErrNoTenant", err)
			}
			if err := f.svc.AddMember(ctx, in.MembershipInput{UserID: userID}); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("AddMember: err = %v; quiere ErrNoTenant", err)
			}
			if err := f.svc.RemoveMember(ctx, in.MembershipInput{UserID: userID}); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("RemoveMember: err = %v; quiere ErrNoTenant", err)
			}
		})
	}
	if f.adds.calls() != 0 || f.identity.gets != 0 {
		t.Fatalf("Add=%d, lecturas en identity=%d; quiere 0 y 0", f.adds.calls(), f.identity.gets)
	}
}

func TestMembershipService_EmptyUserIDIsInvalidInput(t *testing.T) {
	f := newMembershipFixture(t)
	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("AddMember: err = %v; quiere ErrInvalidInput", err)
	}
	if err := f.svc.RemoveMember(ctxOf(testTenant), in.MembershipInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("RemoveMember: err = %v; quiere ErrInvalidInput", err)
	}
}

// ---------------------------------------------------------------------------
// R-U25 · acreditar en identity antes de escribir
// ---------------------------------------------------------------------------

func TestAddMember_AccreditationFailureWritesNoMembership(t *testing.T) {
	cases := []struct {
		name          string
		breakIdentity func(*fakeUserSystems)
		want          error
	}{
		{"identity_read_fails", func(f *fakeUserSystems) { f.errGet = domain.ErrIdentityUnavailable }, domain.ErrIdentityUnavailable},
		{"identity_write_rejected", func(f *fakeUserSystems) { f.errPut = domain.ErrSystemNotAllowed }, domain.ErrSystemNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMembershipFixture(t)
			c.breakIdentity(f.identity)
			userID := uuid.NewString()
			if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); !errors.Is(err, c.want) {
				t.Fatalf("err = %v; quiere %v", err, c.want)
			}
			if n := f.adds.calls(); n != 0 {
				t.Errorf("members.Add se llamó %d veces; quiere 0", n)
			}
			if tenants := f.tenantsOf(t, userID); len(tenants) != 0 {
				t.Errorf("quedó membresía tras el fallo: %v", tenants)
			}
		})
	}
}

func TestAddMember_SystemsTravelAsUnion(t *testing.T) {
	cases := []struct {
		name    string
		current []string
		want    []string
	}{
		{"with_a_previous_app", []string{"edugo.web"}, []string{"edugo.web", "wapp.bff"}},
		{"with_the_edge_app", []string{"wapp.edge"}, []string{"wapp.edge", "wapp.bff"}},
		{"with_several", []string{"edugo.web", "wapp.edge"}, []string{"edugo.web", "wapp.edge", "wapp.bff"}},
		{"with_none", nil, []string{"wapp.bff"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMembershipFixture(t)
			f.identity.current = c.current
			f.add(t, testTenant, uuid.NewString())
			puts := f.identity.written()
			if len(puts) != 1 || !slices.Equal(puts[0], c.want) {
				t.Fatalf("PUT = %v; quiere exactamente uno con %v (lo que no viaja queda revocado)", puts, c.want)
			}
		})
	}
}

func TestAddMember_AlreadyAccreditedDoesNotWriteIdentity(t *testing.T) {
	f := newMembershipFixture(t)
	f.identity.current = []string{"wapp.bff"}
	userID := uuid.NewString()
	f.add(t, testTenant, userID)
	if puts := f.identity.written(); len(puts) != 0 {
		t.Fatalf("PUT = %v; con la aplicación ya concedida no se escribe en identity", puts)
	}
	if tenants := f.tenantsOf(t, userID); !slices.Equal(tenants, []string{testTenant}) {
		t.Fatalf("membresías = %v; quiere [%s]", tenants, testTenant)
	}
}

func TestAddMember_WithoutM2MWritesNothing(t *testing.T) {
	store := memory.NewStore()
	adds := &addCountingRepo{MembershipRepo: store.Memberships}
	svc, err := NewMembershipService(testResolver, adds, nil, quietLogger())
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	if err := svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: uuid.NewString()}); !errors.Is(err, domain.ErrIdentityNotConfigured) {
		t.Fatalf("err = %v; quiere ErrIdentityNotConfigured", err)
	}
	if n := adds.calls(); n != 0 {
		t.Fatalf("members.Add se llamó %d veces sin poder acreditar; quiere 0", n)
	}
}

// ---------------------------------------------------------------------------
// R-U26 · el rastro distingue la credencial del resto
// ---------------------------------------------------------------------------

func TestAccreditation_LogTellsCredentialFromOtherFailures(t *testing.T) {
	const (
		credentialMark = "identity.users.systems.read"
		genericMark    = "acreditacion_fallida"
	)
	cases := []struct {
		name       string
		failure    error
		want, deny string
	}{
		{"credential_without_scope", domain.ErrMachineCredentialInvalid, credentialMark, genericMark},
		{"identity_down", domain.ErrIdentityUnavailable, genericMark, credentialMark},
		{"person_does_not_exist", domain.ErrNotFound, genericMark, credentialMark},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			store := memory.NewStore()
			svc, err := NewMembershipService(testResolver, store.Memberships, &fakeUserSystems{errGet: c.failure},
				sharedlogger.New(sharedlogger.WithWriter(&buf)))
			if err != nil {
				t.Fatalf("NewMembershipService: %v", err)
			}
			userID := uuid.NewString()
			if err := svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); !errors.Is(err, c.failure) {
				t.Fatalf("err = %v; quiere %v (al llamante, el error tal cual)", err, c.failure)
			}
			written := buf.String()
			if !strings.Contains(written, c.want) || strings.Contains(written, c.deny) {
				t.Fatalf("el rastro tiene que decir %q y no %q: %s", c.want, c.deny, written)
			}
			if !strings.Contains(written, userID) || !strings.Contains(written, "leer_accesos") {
				t.Errorf("el rastro no lleva el user_id o el paso: %s", written)
			}
		})
	}
}

func TestAccreditation_WithoutLoggerDoesNotPanic(t *testing.T) {
	store := memory.NewStore()
	svc, err := NewMembershipService(testResolver, store.Memberships, &fakeUserSystems{errGet: domain.ErrMachineCredentialInvalid}, nil)
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	if err := svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: uuid.NewString()}); !errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Fatalf("err = %v; quiere ErrMachineCredentialInvalid", err)
	}
}
