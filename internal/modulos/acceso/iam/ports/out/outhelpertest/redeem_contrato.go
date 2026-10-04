package outhelpertest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MontajeInvitationRedeemRepo es lo que cada implementación de out.InvitationRedeemRepo entrega a
// la suite para UN caso: el repositorio, sin invitaciones, membresías, asignaciones ni
// solicitudes de las personas que la suite inventa, sobre dos tenants que existen; un rol de
// TenantA; el interruptor del resolver de entitlements con el que se construyó; y las tablas que
// el canje toca, para sembrarlas y observarlas.
type MontajeInvitationRedeemRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.InvitationRedeemRepo
	// TenantA y TenantB son dos tenants que existen, distintos y con forma de UUID.
	TenantA, TenantB string
	// RoleA es el ID de un rol de TenantA que existe: el que una invitación promete.
	RoleA string
	// Features mueve el resolver que decide la segunda empresa. Llega sin multi_empresa en
	// ningún tenant y con el resolver funcionando.
	Features FeatureSwitch
	// Tables siembra y observa las cuatro tablas del canje.
	Tables RedeemTables
}

// InvitationSeed describe una invitación que sembrar para el canje.
type InvitationSeed struct {
	// TenantID es la empresa que invita.
	TenantID string
	// TokenHash es el digest, 32 bytes.
	TokenHash []byte
	// RoleID es el rol prometido (nil = sin rol).
	RoleID *string
	// ExpiresIn fija expires_at RELATIVO al reloj de la implementación en el instante de sembrar
	// (con Postgres, now() de la base, el mismo que luego la juzga): negativo o cero = ya vencida.
	ExpiresIn time.Duration
	// RedeemedBy, si no es vacío, la siembra ya canjeada por esa persona (redeemed_by y
	// redeemed_at, siempre juntos).
	RedeemedBy string
	// Revoked la siembra ya revocada.
	Revoked bool
}

// RoleAssignment es una fila de public.iam_user_roles vista por la suite: el rol y su ámbito
// ("" = global).
type RoleAssignment struct {
	RoleID, TenantID string
}

// AccessRequestRow es la solicitud de acceso de una persona (public.access_requests) en las
// columnas que el canje puede tocar.
type AccessRequestRow struct {
	// Status es 'pending', 'approved' o 'rejected'.
	Status string
	// DecidedAt es cuándo dejó de estar pendiente (nil = NULL).
	DecidedAt *time.Time
	// DecidedBy es el operador que la decidió (nil = NULL; el canje lo deja NULL: no decidió
	// ningún operador).
	DecidedBy *string
}

// RedeemState es la foto de TODO lo que un canje de una persona con un digest puede escribir
// (hallazgo 35 de F1): la fila entera de la invitación, las membresías de la persona con su
// created_at, sus asignaciones de rol y su solicitud de acceso.
type RedeemState struct {
	// Invitation es la fila del digest, entera; nil si no existe.
	Invitation *domain.Invitation
	// Memberships son las de la persona, en orden (created_at, tenant_id).
	Memberships []domain.Membership
	// Roles son las asignaciones de la persona, en cualquier orden (la suite las ordena).
	Roles []RoleAssignment
	// AccessRequest es su solicitud de acceso; nil si no tiene.
	AccessRequest *AccessRequestRow
}

// RedeemTables es lo que la suite del canje necesita hacer por debajo del puerto: sembrar las
// tablas y fotografiarlas. Con Postgres, SQL directo sobre public.tenant_invitations,
// public.tenant_members, public.iam_user_roles y public.access_requests; con memoria, los
// ayudantes de los dobles. Cada método falla el test t si no puede.
type RedeemTables interface {
	// SeedInvitation siembra una invitación como dice seed.
	SeedInvitation(t *testing.T, seed InvitationSeed)
	// SeedMembership da a userID una membresía en tenantID SIN pasar por la guarda.
	SeedMembership(t *testing.T, userID, tenantID string)
	// SeedPendingAccessRequest deja a userID una solicitud 'pending' en la bandeja del operador,
	// como la deja el signup público.
	SeedPendingAccessRequest(t *testing.T, userID string)
	// Snapshot fotografía lo que un canje de userID con tokenHash puede haber escrito.
	Snapshot(t *testing.T, tokenHash []byte, userID string) RedeemState
}

// ContratoInvitationRedeemRepo ejecuta las promesas de out.InvitationRedeemRepo contra la
// implementación que devuelve nuevo, con un Montaje limpio por caso (diseño F2 §2, R-P1, R-P5,
// R-P8, R-D5): el camino feliz con la membresía en el tenant DE LA INVITACIÓN, el rol acotado y la
// solicitud cerrada; los cuatro rechazos (inexistente, caducada, canjeada, revocada) sin rastro
// escrito; un solo uso; la guarda de la segunda empresa, que NO quema la invitación; y la
// solicitud pendiente, que no es obligatoria.
//
// «Sin rastro» compara la foto ENTERA de antes y de después (RedeemState): invitación columna a
// columna, membresías, asignaciones y solicitud.
//
// Lo que NO afirma, a propósito: el coste igual de «no existe» y «caducada» (el anti-oráculo de
// T-A3 lo vigila el candado AST de infra/postgres), la atomicidad ante un fallo a mitad (se ve
// solo con Postgres, en F9) ni la carrera entre dos canjes simultáneos del mismo token.
func ContratoInvitationRedeemRepo(t *testing.T, nuevo func(t *testing.T) MontajeInvitationRedeemRepo) {
	t.Helper()
	runCases(t, "ContratoInvitationRedeemRepo", nuevo, validateRedeemMontaje, []testCase[MontajeInvitationRedeemRepo]{
		{"HappyPath_MembershipInTheInvitationTenant_RequestClosed", redeemHappyPath},
		{"HappyPath_WithRole_AssignsItScopedToTheTenant", redeemWithRole},
		{"Missing_NotFound_NoTrace", redeemMissing},
		{"Expired_ErrInvitationExpired_NoTrace", redeemExpired},
		{"AlreadyRedeemed_Conflict_NoTrace", redeemAlreadyRedeemed},
		{"Revoked_Conflict_NoTrace", redeemRevoked},
		{"SecondRedemption_Conflict_FirstStaysTheOwner", redeemSingleUse},
		{"MemberOfAnotherCompany_Conflict_InvitationNotBurned", redeemOtherCompany},
		{"MemberOfAnotherCompany_WithMultiCompany_Writes", redeemOtherCompanyWithFeature},
		{"NoPendingRequest_IsNotAFailure", redeemWithoutRequest},
	})
}

func validateRedeemMontaje(t *testing.T, m MontajeInvitationRedeemRepo) {
	t.Helper()
	switch {
	case m.Repo == nil:
		t.Fatal("MontajeInvitationRedeemRepo.Repo es nil")
	case m.Features == nil:
		t.Fatal("MontajeInvitationRedeemRepo.Features es nil: la guarda de la segunda empresa necesita un resolver")
	case m.Tables == nil:
		t.Fatal("MontajeInvitationRedeemRepo.Tables es nil: el canje toca tablas que el puerto no deja ver")
	}
	validateTenants(t, m.TenantA, m.TenantB)
	if _, err := uuid.Parse(m.RoleA); err != nil {
		t.Fatalf("MontajeInvitationRedeemRepo.RoleA %q no es un UUID: %v", m.RoleA, err)
	}
}

// liveSeed es una invitación viva (vence en una hora) de tenantID con un digest nuevo.
func liveSeed(tenantID string) InvitationSeed {
	return InvitationSeed{TenantID: tenantID, TokenHash: newDigest(), ExpiresIn: time.Hour}
}

// stateDiff compara dos fotos y devuelve qué cambió, vacío si nada.
func stateDiff(before, after RedeemState) string {
	var diffs []string
	switch {
	case (before.Invitation == nil) != (after.Invitation == nil):
		diffs = append(diffs, fmt.Sprintf("invitación %v → %v", before.Invitation != nil, after.Invitation != nil))
	case before.Invitation != nil:
		if diff := invitationDiff(*before.Invitation, *after.Invitation); diff != "" {
			diffs = append(diffs, "invitación ["+diff+"]")
		}
	}
	if !slices.EqualFunc(before.Memberships, after.Memberships, func(a, b domain.Membership) bool {
		return a.UserID == b.UserID && a.TenantID == b.TenantID && a.CreatedAt.Equal(b.CreatedAt)
	}) {
		diffs = append(diffs, fmt.Sprintf("membresías %+v → %+v", before.Memberships, after.Memberships))
	}
	if !slices.Equal(sortedAssignments(before.Roles), sortedAssignments(after.Roles)) {
		diffs = append(diffs, fmt.Sprintf("roles %+v → %+v", before.Roles, after.Roles))
	}
	if !equalPtr(before.AccessRequest, after.AccessRequest, func(a, b AccessRequestRow) bool {
		return a.Status == b.Status && equalPtr(a.DecidedAt, b.DecidedAt, time.Time.Equal) &&
			equalPtr(a.DecidedBy, b.DecidedBy, func(x, y string) bool { return x == y })
	}) {
		diffs = append(diffs, fmt.Sprintf("solicitud %+v → %+v", before.AccessRequest, after.AccessRequest))
	}
	return strings.Join(diffs, "; ")
}

// sortedAssignments devuelve una copia ordenada por (rol, tenant).
func sortedAssignments(roles []RoleAssignment) []RoleAssignment {
	sorted := slices.Clone(roles)
	slices.SortFunc(sorted, func(a, b RoleAssignment) int {
		return strings.Compare(a.RoleID+"\x00"+a.TenantID, b.RoleID+"\x00"+b.TenantID)
	})
	return sorted
}

// wantNoTrace canjea esperando wantErr y exige que la foto no cambie.
func wantNoTrace(t *testing.T, m MontajeInvitationRedeemRepo, digest []byte, userID string, wantErr error) {
	t.Helper()
	before := m.Tables.Snapshot(t, digest, userID)
	if err := m.Repo.Redeem(bg(), digest, userID); !errors.Is(err, wantErr) {
		t.Fatalf("Redeem: err = %v, quiero %v", err, wantErr)
	}
	if diff := stateDiff(before, m.Tables.Snapshot(t, digest, userID)); diff != "" {
		t.Fatalf("un canje rechazado dejó rastro: %s", diff)
	}
}

// mustRedeem canjea y falla el test si no puede.
func mustRedeem(t *testing.T, repo out.InvitationRedeemRepo, digest []byte, userID string) {
	t.Helper()
	if err := repo.Redeem(bg(), digest, userID); err != nil {
		t.Fatalf("Redeem(%s): %v", userID, err)
	}
}

// membershipTenants devuelve los tenants de las membresías de la foto, en su orden.
func membershipTenants(state RedeemState) []string {
	tenants := make([]string, 0, len(state.Memberships))
	for _, ms := range state.Memberships {
		tenants = append(tenants, ms.TenantID)
	}
	return tenants
}

// wantRedeemedBy exige la invitación marcada por userID, con todas sus demás columnas como antes.
func wantRedeemedBy(t *testing.T, before, after RedeemState, userID string) {
	t.Helper()
	inv := after.Invitation
	if inv == nil || inv.RedeemedBy == nil || *inv.RedeemedBy != userID || inv.RedeemedAt == nil || inv.RevokedAt != nil {
		t.Fatalf("invitación tras el canje = %+v, quiero redeemed_by %s, redeemed_at y sin revocar", inv, userID)
	}
	want := *before.Invitation
	want.RedeemedBy, want.RedeemedAt = inv.RedeemedBy, inv.RedeemedAt
	wantSameInvitation(t, "el canje solo marca redeemed_by y redeemed_at", *inv, want)
}

func redeemHappyPath(t *testing.T, m MontajeInvitationRedeemRepo) {
	// La invitación es de B: la membresía tiene que salir en B, el tenant de la invitación, y en
	// ningún otro (el puerto no recibe tenant: no hay otro de dónde sacarlo).
	seed := liveSeed(m.TenantB)
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	m.Tables.SeedPendingAccessRequest(t, user)
	before := m.Tables.Snapshot(t, seed.TokenHash, user)
	mustRedeem(t, m.Repo, seed.TokenHash, user)
	after := m.Tables.Snapshot(t, seed.TokenHash, user)
	wantTenants(t, "membresías tras el canje", membershipTenants(after), m.TenantB)
	if len(after.Roles) != 0 {
		t.Fatalf("roles tras canjear una invitación sin rol = %+v, quiero ninguno", after.Roles)
	}
	wantRedeemedBy(t, before, after, user)
	if req := after.AccessRequest; req == nil || req.Status != "approved" || req.DecidedAt == nil || req.DecidedBy != nil {
		t.Fatalf("solicitud tras el canje = %+v, quiero status approved, decided_at puesto y decided_by NULL", req)
	}
}

func redeemWithRole(t *testing.T, m MontajeInvitationRedeemRepo) {
	seed := liveSeed(m.TenantA)
	seed.RoleID = ptr(m.RoleA)
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	mustRedeem(t, m.Repo, seed.TokenHash, user)
	after := m.Tables.Snapshot(t, seed.TokenHash, user)
	wantTenants(t, "membresías tras el canje", membershipTenants(after), m.TenantA)
	if got, want := sortedAssignments(after.Roles), []RoleAssignment{{RoleID: m.RoleA, TenantID: m.TenantA}}; !slices.Equal(got, want) {
		t.Fatalf("roles tras el canje = %+v, quiero %+v (el rol prometido, acotado a la empresa de la invitación)", got, want)
	}
}

func redeemMissing(t *testing.T, m MontajeInvitationRedeemRepo) {
	user := newUser()
	m.Tables.SeedPendingAccessRequest(t, user)
	wantNoTrace(t, m, newDigest(), user, domain.ErrNotFound)
}

func redeemExpired(t *testing.T, m MontajeInvitationRedeemRepo) {
	for name, expiresIn := range map[string]time.Duration{"an_hour_ago": -time.Hour, "at_the_edge": 0} {
		t.Run(name, func(t *testing.T) {
			seed := liveSeed(m.TenantA)
			seed.ExpiresIn = expiresIn
			m.Tables.SeedInvitation(t, seed)
			user := newUser()
			m.Tables.SeedPendingAccessRequest(t, user)
			wantNoTrace(t, m, seed.TokenHash, user, domain.ErrInvitationExpired)
		})
	}
}

func redeemAlreadyRedeemed(t *testing.T, m MontajeInvitationRedeemRepo) {
	seed := liveSeed(m.TenantA)
	seed.RedeemedBy = newUser()
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	m.Tables.SeedPendingAccessRequest(t, user)
	wantNoTrace(t, m, seed.TokenHash, user, domain.ErrConflict)
}

func redeemRevoked(t *testing.T, m MontajeInvitationRedeemRepo) {
	seed := liveSeed(m.TenantA)
	seed.Revoked = true
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	m.Tables.SeedPendingAccessRequest(t, user)
	wantNoTrace(t, m, seed.TokenHash, user, domain.ErrConflict)
}

func redeemSingleUse(t *testing.T, m MontajeInvitationRedeemRepo) {
	seed := liveSeed(m.TenantA)
	m.Tables.SeedInvitation(t, seed)
	first, second := newUser(), newUser()
	mustRedeem(t, m.Repo, seed.TokenHash, first)
	m.Tables.SeedPendingAccessRequest(t, second)
	wantNoTrace(t, m, seed.TokenHash, second, domain.ErrConflict)
	if inv := m.Tables.Snapshot(t, seed.TokenHash, first).Invitation; inv == nil || inv.RedeemedBy == nil || *inv.RedeemedBy != first {
		t.Fatalf("invitación = %+v, quiero redeemed_by %s: el segundo intento pisó al primero", inv, first)
	}
}

func redeemOtherCompany(t *testing.T, m MontajeInvitationRedeemRepo) {
	seed := liveSeed(m.TenantB)
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	m.Tables.SeedMembership(t, user, m.TenantA)
	m.Tables.SeedPendingAccessRequest(t, user)
	before := m.Tables.Snapshot(t, seed.TokenHash, user)
	err := m.Repo.Redeem(bg(), seed.TokenHash, user)
	wantSecondCompanyConflict(t, err)
	if diff := stateDiff(before, m.Tables.Snapshot(t, seed.TokenHash, user)); diff != "" {
		t.Fatalf("el rechazo por otra empresa dejó rastro (y una invitación marcada queda QUEMADA): %s", diff)
	}
	// Sigue sirviendo de verdad: otra persona la canjea.
	other := newUser()
	mustRedeem(t, m.Repo, seed.TokenHash, other)
	wantTenants(t, "membresías de quien la canjeó después", membershipTenants(m.Tables.Snapshot(t, seed.TokenHash, other)), m.TenantB)
}

func redeemOtherCompanyWithFeature(t *testing.T, m MontajeInvitationRedeemRepo) {
	m.Features.GrantMultiCompany(t, m.TenantB)
	seed := liveSeed(m.TenantB)
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	m.Tables.SeedMembership(t, user, m.TenantA)
	before := m.Tables.Snapshot(t, seed.TokenHash, user)
	mustRedeem(t, m.Repo, seed.TokenHash, user)
	after := m.Tables.Snapshot(t, seed.TokenHash, user)
	wantTenants(t, "membresías tras el canje con multi_empresa", membershipTenants(after), m.TenantA, m.TenantB)
	wantRedeemedBy(t, before, after, user)
}

func redeemWithoutRequest(t *testing.T, m MontajeInvitationRedeemRepo) {
	seed := liveSeed(m.TenantA)
	m.Tables.SeedInvitation(t, seed)
	user := newUser()
	mustRedeem(t, m.Repo, seed.TokenHash, user)
	after := m.Tables.Snapshot(t, seed.TokenHash, user)
	wantTenants(t, "membresías", membershipTenants(after), m.TenantA)
	if after.AccessRequest != nil {
		t.Fatalf("solicitud tras canjear sin solicitud = %+v, quiero ninguna", after.AccessRequest)
	}
}
