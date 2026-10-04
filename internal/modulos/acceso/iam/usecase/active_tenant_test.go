//go:build pendiente

package usecase

import (
	"context"
	"errors"
	"slices"
	"testing"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// testCallerKey es la clave del contexto con la que los tests entregan la identidad del
// llamante: el tenant viaja por el contexto (INV-04), como en producción. Vive aquí porque
// active_tenant es el primero de sus consumidores en pasar a verde (roles, memberships,
// invitations y canje también la usan).
type testCallerKey struct{}

// withCaller devuelve un contexto que porta la identidad dada.
func withCaller(ctx context.Context, c in.Caller) context.Context {
	return context.WithValue(ctx, testCallerKey{}, c)
}

// testResolver es el CallerResolver de los tests: lee lo que withCaller puso.
var testResolver = in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
	c, ok := ctx.Value(testCallerKey{}).(in.Caller)
	return c, ok
})

// ctxNoCompany es el contexto de quien llega a elegir empresa en la vida real: acreditado y
// con el tenant VACÍO.
func ctxNoCompany(userID string) context.Context {
	return withCaller(context.Background(), in.Caller{UserID: userID})
}

type activeTenantFixture struct {
	svc   *ActiveTenantService
	store *memory.Store
}

func newActiveTenantFixture(t *testing.T) activeTenantFixture {
	t.Helper()
	store := memory.NewStore()
	return activeTenantFixture{svc: newActiveTenantOver(t, store.Memberships, store.ActiveTenants), store: store}
}

func newActiveTenantOver(t *testing.T, members out.MembershipRepo, active out.ActiveTenantRepo) *ActiveTenantService {
	t.Helper()
	svc, err := NewActiveTenantService(testResolver, members, active)
	if err != nil {
		t.Fatalf("NewActiveTenantService: %v", err)
	}
	return svc
}

// stored devuelve la empresa activa guardada, abortando si el doble falla.
func (f activeTenantFixture) stored(t *testing.T, userID string) (string, bool) {
	t.Helper()
	active, ok, err := f.store.ActiveTenants.ActiveTenantOf(context.Background(), userID)
	if err != nil {
		t.Fatalf("ActiveTenantOf: %v", err)
	}
	return active, ok
}

// ---------------------------------------------------------------------------
// R-U19 · constructor
// ---------------------------------------------------------------------------

func TestNewActiveTenantService_RequiresDependencies(t *testing.T) {
	store := memory.NewStore()
	cases := []struct {
		name  string
		build func() (*ActiveTenantService, error)
		want  string
	}{
		{"nil_caller", func() (*ActiveTenantService, error) {
			return NewActiveTenantService(nil, store.Memberships, store.ActiveTenants)
		}, "iam: ActiveTenantService requiere un CallerResolver (quien elige sale del contexto)"},
		{"nil_memberships", func() (*ActiveTenantService, error) {
			return NewActiveTenantService(testResolver, nil, store.ActiveTenants)
		}, "iam: ActiveTenantService requiere un MembershipRepo (elegir empresa exige ser miembro)"},
		{"nil_active_tenant", func() (*ActiveTenantService, error) {
			return NewActiveTenantService(testResolver, store.Memberships, nil)
		}, "iam: ActiveTenantService requiere un ActiveTenantRepo"},
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
// R-U15 / R-U16 · elegir empresa
// ---------------------------------------------------------------------------

// Se comprueba lo GUARDADO, no solo el nil.
func TestSelectActiveTenant_MemberChoosesAndItIsStored(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)

	if err := f.svc.SelectActiveTenant(ctxNoCompany(userID), testTenantB); err != nil {
		t.Fatalf("SelectActiveTenant: %v (el token de quien elige no trae empresa, y es lo normal)", err)
	}
	if active, ok := f.stored(t, userID); !ok || active != testTenantB {
		t.Fatalf("guardada = %q (ok=%v); quiere %q", active, ok, testTenantB)
	}
}

func TestSelectActiveTenant_ReplacesDoesNotAccumulate(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)
	for _, tid := range []string{testTenant, testTenantB} {
		if err := f.svc.SelectActiveTenant(ctxNoCompany(userID), tid); err != nil {
			t.Fatalf("SelectActiveTenant(%s): %v", tid, err)
		}
	}
	if active, _ := f.stored(t, userID); active != testTenantB {
		t.Fatalf("guardada = %q; quiere %q: la segunda elección reemplaza a la primera", active, testTenantB)
	}
}

// Anti-oráculo: ajena e inexistente dan el MISMO ErrNotFound, y no se escribe nada.
func TestSelectActiveTenant_ForeignOrMissingCompanyIsSameNotFound(t *testing.T) {
	messages := map[string]string{}
	for name, requested := range map[string]string{"exists_but_not_theirs": testTenantB, "does_not_exist": uuid.NewString()} {
		t.Run(name, func(t *testing.T) {
			f := newActiveTenantFixture(t)
			userID := uuid.NewString()
			f.store.Memberships.Seed(userID, testTenant)
			f.store.Memberships.Seed(uuid.NewString(), testTenantB) // B existe, de otra persona

			err := f.svc.SelectActiveTenant(ctxNoCompany(userID), requested)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v; quiere ErrNotFound (un 403 confirmaría que la empresa existe)", err)
			}
			messages[name] = err.Error()
			if _, ok := f.stored(t, userID); ok {
				t.Fatal("se guardó una empresa que no es suya")
			}
		})
	}
	if messages["exists_but_not_theirs"] != messages["does_not_exist"] || messages["does_not_exist"] != domain.ErrNotFound.Error() {
		t.Fatalf("mensajes = %v; quiere los dos idénticos a ErrNotFound sin envolver", messages)
	}
}

// Sin sujeto o sin empresa pedida es 400, no 401.
func TestSelectActiveTenant_InvalidInputIsNotUnauthorized(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	cases := []struct {
		name   string
		ctx    context.Context
		tenant string
	}{
		{"no_identity", context.Background(), testTenant},
		{"identity_without_subject", withCaller(context.Background(), in.Caller{TenantID: testTenant}), testTenant},
		{"no_tenant_requested", ctxNoCompany(userID), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := f.svc.SelectActiveTenant(c.ctx, c.tenant); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
		})
	}
	if _, ok := f.stored(t, userID); ok {
		t.Fatal("una entrada inválida dejó una elección guardada")
	}
}

func TestSelectActiveTenant_MembershipReadFailurePropagates(t *testing.T) {
	store := memory.NewStore()
	broken := errors.New("tenant_members no contesta")
	svc := newActiveTenantOver(t, brokenMemberships{MembershipRepo: store.Memberships, err: broken}, store.ActiveTenants)
	if err := svc.SelectActiveTenant(ctxNoCompany(uuid.NewString()), testTenant); !errors.Is(err, broken) {
		t.Fatalf("err = %v; quiere %v", err, broken)
	}
}

// R-U16: con una sola membresía también se fija.
func TestSelectActiveTenant_SingleMembershipCanBeChosen(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	if err := f.svc.SelectActiveTenant(ctxNoCompany(userID), testTenant); err != nil {
		t.Fatalf("SelectActiveTenant: %v", err)
	}
	if active, ok := f.stored(t, userID); !ok || active != testTenant {
		t.Fatalf("guardada = %q (ok=%v); quiere %q", active, ok, testTenant)
	}
}

// ---------------------------------------------------------------------------
// R-U17 · el listado del selector
// ---------------------------------------------------------------------------

func TestTenantsOfCaller_ReturnsTheirCompaniesWithNameAndActive(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.SeedTenantName(testTenant, "Panadería Doña Rosa")
	f.store.Memberships.SeedTenantName(testTenantB, "Catering del Sur")
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}

	tenants, activeID, err := f.svc.TenantsOfCaller(ctxNoCompany(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	got := map[string]string{}
	for _, tn := range tenants {
		got[tn.ID] = tn.DisplayName
	}
	if len(tenants) != 2 || got[testTenant] != "Panadería Doña Rosa" || got[testTenantB] != "Catering del Sur" {
		t.Fatalf("empresas = %+v; quiere las dos con su nombre legible", tenants)
	}
	if activeID != testTenantB {
		t.Errorf("activeID = %q; quiere %q", activeID, testTenantB)
	}
}

func TestTenantsOfCaller_NoCompaniesIsEmptyNotNil(t *testing.T) {
	f := newActiveTenantFixture(t)
	tenants, activeID, err := f.svc.TenantsOfCaller(ctxNoCompany(uuid.NewString()))
	if err != nil {
		t.Fatalf("err = %v: cero empresas es un estado legítimo, no un fallo", err)
	}
	if tenants == nil || len(tenants) != 0 || activeID != "" {
		t.Fatalf("= %#v, %q; quiere lista vacía NO nil y activeID vacío", tenants, activeID)
	}
}

// Anti-oráculo: la empresa ajena existe (con nombre) y no asoma.
func TestTenantsOfCaller_OnlyTheirs(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID, other := uuid.NewString(), uuid.NewString()
	f.store.Memberships.SeedTenantName(testTenant, "La suya")
	f.store.Memberships.SeedTenantName(testTenantB, "La ajena")
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(other, testTenantB)

	tenants, _, err := f.svc.TenantsOfCaller(ctxNoCompany(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if len(tenants) != 1 || tenants[0].ID != testTenant {
		t.Fatalf("empresas = %+v; quiere SOLO %q", tenants, testTenant)
	}
	// Guarda anti-hueco: la ajena existe de verdad.
	if foreign, _, err := f.svc.TenantsOfCaller(ctxNoCompany(other)); err != nil || len(foreign) != 1 {
		t.Fatalf("la empresa ajena no existía (%+v, %v): el aserto de arriba vigilaba una pared", foreign, err)
	}
}

func TestTenantsOfCaller_SingleMembershipMarksItEvenIfStoredIsOther(t *testing.T) {
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	_, activeID, err := f.svc.TenantsOfCaller(ctxNoCompany(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if activeID != testTenant {
		t.Fatalf("activeID = %q; quiere %q: con una membresía manda la membresía, como en el canje", activeID, testTenant)
	}
}

func TestTenantsOfCaller_StoredNoLongerTheirsIsNotMarked(t *testing.T) {
	f := newActiveTenantFixture(t)
	ctx := context.Background()
	userID := uuid.NewString()
	for _, tid := range []string{testTenant, testTenantB, testTenantC} {
		f.store.Memberships.Seed(userID, tid)
	}
	if err := f.store.ActiveTenants.SetActiveTenant(ctx, userID, testTenantC); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	if err := f.store.Memberships.Remove(ctx, userID, testTenantC); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tenants, activeID, err := f.svc.TenantsOfCaller(ctxNoCompany(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if activeID != "" {
		t.Fatalf("activeID = %q; quiere vacío: guardar una empresa activa no concede nada", activeID)
	}
	if len(tenants) != 2 || slices.ContainsFunc(tenants, func(tn domain.UserTenant) bool { return tn.ID == testTenantC }) {
		t.Fatalf("empresas = %+v; quiere las dos que le quedan, sin la perdida", tenants)
	}
}

func TestTenantsOfCaller_NoIdentityIsInvalidInput(t *testing.T) {
	f := newActiveTenantFixture(t)
	for name, ctx := range map[string]context.Context{
		"no_identity":              context.Background(),
		"identity_without_subject": withCaller(context.Background(), in.Caller{TenantID: testTenant}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := f.svc.TenantsOfCaller(ctx); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
		})
	}
}

func TestTenantsOfCaller_ActiveReadFailurePropagates(t *testing.T) {
	store := memory.NewStore()
	userID := uuid.NewString()
	store.Memberships.Seed(userID, testTenant)
	store.Memberships.Seed(userID, testTenantB)
	broken := errors.New("user_active_tenant no contesta")
	svc := newActiveTenantOver(t, store.Memberships, brokenActiveTenant{err: broken})
	if _, _, err := svc.TenantsOfCaller(ctxNoCompany(userID)); !errors.Is(err, broken) {
		t.Fatalf("err = %v; quiere %v: un fallo no se lee como «ninguna»", err, broken)
	}
}

// ---------------------------------------------------------------------------
// R-U18 · el selector y el canje nunca discrepan
// ---------------------------------------------------------------------------

// TestSelectorAndExchangeNeverDisagree es el invariante cruzado R-U18: para cada forma de
// estar, lo que el listado MARCA y lo que el canje REAL acota en el token FIRMADO, sobre los
// MISMOS dobles, son el mismo valor. No compara contra constantes escritas a mano.
func TestSelectorAndExchangeNeverDisagree(t *testing.T) {
	cases := []struct {
		name        string
		companies   []string
		storedRaw   string // se escribe directo en el repositorio (basura de una baja anterior)
		choose      string // se elige por el servicio
		removeAfter string
	}{
		{"zero_companies", nil, "", "", ""},
		{"one_company", []string{testTenant}, "", "", ""},
		{"one_company_with_foreign_stored", []string{testTenant}, testTenantB, "", ""},
		{"several_without_choice", []string{testTenant, testTenantB}, "", "", ""},
		{"several_with_choice", []string{testTenant, testTenantB}, "", testTenantB, ""},
		{"several_with_choice_no_longer_theirs", []string{testTenant, testTenantB, testTenantC}, "", testTenantC, testTenantC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newActiveTenantFixture(t)
			ctx := context.Background()
			userID := uuid.NewString()
			for _, tid := range c.companies {
				f.store.Memberships.Seed(userID, tid)
			}
			if c.storedRaw != "" {
				if err := f.store.ActiveTenants.SetActiveTenant(ctx, userID, c.storedRaw); err != nil {
					t.Fatalf("SetActiveTenant: %v", err)
				}
			}
			if c.choose != "" {
				if err := f.svc.SelectActiveTenant(ctxNoCompany(userID), c.choose); err != nil {
					t.Fatalf("SelectActiveTenant: %v", err)
				}
			}
			if c.removeAfter != "" {
				if err := f.store.Memberships.Remove(ctx, userID, c.removeAfter); err != nil {
					t.Fatalf("Remove: %v", err)
				}
			}

			_, fromSelector, err := f.svc.TenantsOfCaller(ctxNoCompany(userID))
			if err != nil {
				t.Fatalf("TenantsOfCaller: %v", err)
			}
			if fromExchange := tenantFromExchange(t, f.store, userID); fromSelector != fromExchange {
				t.Fatalf("el selector marcaría %q y el canje acotaría a %q", fromSelector, fromExchange)
			}
		})
	}
}

// tenantFromExchange canjea un Identity Token de userID con un ExchangeService REAL sobre los
// mismos dobles y devuelve el tenant del Context Token FIRMADO.
func tenantFromExchange(t *testing.T, store *memory.Store, userID string) string {
	t.Helper()
	issuer, verifier := newIdentityPair(t)
	f := exchangeFixture{issuer: issuer, verifier: verifier, contexts: sharedjwt.NewJWTManager(testSigningKey, testIssuer), store: store}
	res, err := f.exchange(t, f.serviceWith(t, store.ActiveTenants), userID)
	if err != nil {
		t.Fatalf("Exchange: %v (ningún número de membresías es un error)", err)
	}
	return f.claims(t, res.ContextToken).TenantID
}
