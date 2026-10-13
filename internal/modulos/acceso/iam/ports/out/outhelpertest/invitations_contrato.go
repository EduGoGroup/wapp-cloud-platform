package outhelpertest

import (
	"bytes"
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

// MontajeInvitationRepo es lo que cada implementación de out.InvitationRepo entrega a la suite
// para UN caso: el repositorio, sin invitaciones, sobre dos tenants que existen, un rol de
// TenantA que existe (role_id tiene FK) y las tablas con que la suite siembra lo que el puerto no
// deja escribir.
type MontajeInvitationRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.InvitationRepo
	// TenantA y TenantB son dos tenants que existen, distintos y con forma de UUID.
	TenantA, TenantB string
	// RoleA es el ID de un rol de TenantA que existe: el que una invitación promete.
	RoleA string
	// Tables siembra filas y borra roles por debajo del puerto.
	Tables InvitationTables
}

// InvitationTables es lo que la suite de InvitationRepo necesita hacer por debajo del puerto. Con
// Postgres, SQL directo sobre public.tenant_invitations y public.iam_roles; con memoria, los
// ayudantes del doble. Cada método falla el test t si no puede.
type InvitationTables interface {
	// Seed inserta la invitación TAL CUAL —ID y CreatedAt incluidos si vienen; vacíos, los pone
	// la implementación— y devuelve la fila escrita. Sirve para fabricar lo que Create no puede:
	// una ya canjeada, o dos con el mismo created_at.
	Seed(t *testing.T, inv domain.Invitation) domain.Invitation
	// DeleteRole borra el rol roleID, con las consecuencias que su borrado tenga en las
	// invitaciones que lo prometen (FK ON DELETE SET NULL).
	DeleteRole(t *testing.T, roleID string)
}

// fixedExpiry es el vencimiento de las invitaciones de la suite: un instante FIJO, no relativo a
// ningún reloj, porque InvitationRepo no juzga la caducidad (eso es del canje).
var fixedExpiry = time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)

// fixedCreatedAt es el created_at compartido del caso de desempate.
var fixedCreatedAt = time.Date(2030, time.June, 1, 12, 0, 0, 0, time.UTC)

// ContratoInvitationRepo ejecuta las promesas de out.InvitationRepo contra la implementación
// que devuelve nuevo, con un Montaje limpio por caso (diseño F2 §2): nace pendiente con id y
// created_at; digest de 32 bytes y único; listado por empresa en orden (created_at DESC, id DESC)
// sin cruzar empresas; la revocación con sus tres desenlaces, que MARCA y no borra; y el rol que
// se borra deja la invitación viva y sin rol.
//
// Cada comprobación de «no escribió» o «solo marcó» compara la FILA ENTERA, columna a columna
// (hallazgo 35 de F1): una revocación que además tocara otra columna se ve.
func ContratoInvitationRepo(t *testing.T, nuevo func(t *testing.T) MontajeInvitationRepo) {
	t.Helper()
	runCases(t, "ContratoInvitationRepo", nuevo, validateInvitationMontaje, []testCase[MontajeInvitationRepo]{
		{"Create_BornPending_WithIDAndCreatedAt", invitationCreatePending},
		{"Create_WithRole_RoleTravels", invitationCreateWithRole},
		{"Create_DigestNot32Bytes_ErrorAndNothingWritten", invitationDigestLength},
		{"Create_DuplicateDigest_Conflict_EvenAcrossTenants", invitationDuplicateDigest},
		{"ListByTenant_NewestFirst", invitationNewestFirst},
		{"ListByTenant_SameCreatedAt_IDDescending", invitationTieBreak},
		{"ListByTenant_DoesNotCrossTenants_EmptyNotNil", invitationListScoped},
		{"Revoke_MarksAndDoesNotDelete", invitationRevokeMarks},
		{"Revoke_AlreadyRevoked_NilAndKeepsTheFirstMark", invitationRevokeTwice},
		{"Revoke_Redeemed_ConflictAndUntouched", invitationRevokeRedeemed},
		{"Revoke_OtherTenant_NotFoundAndStaysAlive", invitationRevokeForeign},
		{"Revoke_Missing_NotFound", invitationRevokeMissing},
		{"DeletedRole_InvitationStaysAliveWithoutRole", invitationRoleDeleted},
	})
}

func validateInvitationMontaje(t *testing.T, m MontajeInvitationRepo) {
	t.Helper()
	switch {
	case m.Repo == nil:
		t.Fatal("MontajeInvitationRepo.Repo es nil")
	case m.Tables == nil:
		t.Fatal("MontajeInvitationRepo.Tables es nil")
	}
	validateTenants(t, m.TenantA, m.TenantB)
	if _, err := uuid.Parse(m.RoleA); err != nil {
		t.Fatalf("MontajeInvitationRepo.RoleA %q no es un UUID: %v", m.RoleA, err)
	}
}

// newInvitation es una invitación de la suite para tenantID, sin rol y con digest nuevo.
func newInvitation(tenantID string) domain.Invitation {
	return domain.Invitation{TenantID: tenantID, TokenHash: newDigest(), ExpiresAt: fixedExpiry, CreatedBy: newUser()}
}

// mustCreateInvitation emite la invitación y falla el test si no puede.
func mustCreateInvitation(t *testing.T, repo out.InvitationRepo, inv domain.Invitation) domain.Invitation {
	t.Helper()
	created, err := repo.Create(bg(), inv)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created
}

// listInvitations devuelve ListByTenant y falla el test si hay error.
func listInvitations(t *testing.T, repo out.InvitationRepo, tenantID string) []domain.Invitation {
	t.Helper()
	invs, err := repo.ListByTenant(bg(), tenantID)
	if err != nil {
		t.Fatalf("ListByTenant(%s): %v", tenantID, err)
	}
	return invs
}

// invitationByID busca la fila id en el listado de tenantID y falla el test si no está.
func invitationByID(t *testing.T, repo out.InvitationRepo, tenantID, id string) domain.Invitation {
	t.Helper()
	for _, inv := range listInvitations(t, repo, tenantID) {
		if inv.ID == id {
			return inv
		}
	}
	t.Fatalf("la invitación %s no está en el listado de %s", id, tenantID)
	return domain.Invitation{}
}

// listedIDs devuelve los ID del listado, en su orden.
func listedIDs(t *testing.T, repo out.InvitationRepo, tenantID string) []string {
	t.Helper()
	invs := listInvitations(t, repo, tenantID)
	ids := make([]string, 0, len(invs))
	for _, inv := range invs {
		ids = append(ids, inv.ID)
	}
	return ids
}

// invitationDiff compara dos filas columna a columna (los instantes con Equal) y devuelve las
// columnas distintas, vacío si son la misma fila.
func invitationDiff(a, b domain.Invitation) string {
	var diffs []string
	add := func(column string, equal bool) {
		if !equal {
			diffs = append(diffs, column)
		}
	}
	add("id", a.ID == b.ID)
	add("tenant_id", a.TenantID == b.TenantID)
	add("token_hash", bytes.Equal(a.TokenHash, b.TokenHash))
	add("role_id", equalPtr(a.RoleID, b.RoleID, func(x, y string) bool { return x == y }))
	add("expires_at", a.ExpiresAt.Equal(b.ExpiresAt))
	add("created_by", a.CreatedBy == b.CreatedBy)
	add("redeemed_by", equalPtr(a.RedeemedBy, b.RedeemedBy, func(x, y string) bool { return x == y }))
	add("redeemed_at", equalPtr(a.RedeemedAt, b.RedeemedAt, time.Time.Equal))
	add("revoked_at", equalPtr(a.RevokedAt, b.RevokedAt, time.Time.Equal))
	add("created_at", a.CreatedAt.Equal(b.CreatedAt))
	return strings.Join(diffs, ", ")
}

// equalPtr compara dos punteros por valor: nil solo es igual a nil.
func equalPtr[V any](a, b *V, equal func(x, y V) bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return equal(*a, *b)
}

// wantSameInvitation exige la misma fila, columna a columna.
func wantSameInvitation(t *testing.T, what string, got, want domain.Invitation) {
	t.Helper()
	if diff := invitationDiff(got, want); diff != "" {
		t.Fatalf("%s: columnas distintas [%s]\n got = %+v\nwant = %+v", what, diff, got, want)
	}
}

func invitationCreatePending(t *testing.T, m MontajeInvitationRepo) {
	in := newInvitation(m.TenantA)
	created := mustCreateInvitation(t, m.Repo, in)
	if created.ID == "" || created.CreatedAt.IsZero() {
		t.Fatalf("Create = %+v, quiero id y created_at asignados", created)
	}
	want := in
	want.ID, want.CreatedAt = created.ID, created.CreatedAt
	wantSameInvitation(t, "Create devuelve la fila emitida y pendiente", created, want)
	if status := created.Status(fixedExpiry.Add(-time.Hour)); status != domain.InvitationPending {
		t.Fatalf("estado al nacer = %q, quiero %q", status, domain.InvitationPending)
	}
	wantSameInvitation(t, "ListByTenant devuelve la fila entera", invitationByID(t, m.Repo, m.TenantA, created.ID), created)
}

func invitationCreateWithRole(t *testing.T, m MontajeInvitationRepo) {
	in := newInvitation(m.TenantA)
	in.RoleID = ptr(m.RoleA)
	created := mustCreateInvitation(t, m.Repo, in)
	if created.RoleID == nil || *created.RoleID != m.RoleA {
		t.Fatalf("Create con rol = %+v, quiero RoleID %s", created, m.RoleA)
	}
	wantSameInvitation(t, "ListByTenant con rol", invitationByID(t, m.Repo, m.TenantA, created.ID), created)
}

func invitationDigestLength(t *testing.T, m MontajeInvitationRepo) {
	for _, size := range []int{0, 31, 33} {
		inv := newInvitation(m.TenantA)
		inv.TokenHash = bytes.Repeat([]byte{0xAB}, size)
		if _, err := m.Repo.Create(bg(), inv); err == nil {
			t.Fatalf("Create con un digest de %d bytes: err = nil, quiero un error (la tabla exige 32)", size)
		}
	}
	if ids := listedIDs(t, m.Repo, m.TenantA); len(ids) != 0 {
		t.Fatalf("ListByTenant tras los rechazos = %v, quiero vacía: un digest inválido se escribió", ids)
	}
}

func invitationDuplicateDigest(t *testing.T, m MontajeInvitationRepo) {
	first := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantA))
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		dup := newInvitation(tenant)
		dup.TokenHash = first.TokenHash
		if _, err := m.Repo.Create(bg(), dup); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("digest repetido en %s: err = %v, quiero domain.ErrConflict", tenant, err)
		}
	}
	if ids := listedIDs(t, m.Repo, m.TenantA); len(ids) != 1 || ids[0] != first.ID {
		t.Fatalf("ListByTenant(A) = %v, quiero solo la primera %s", ids, first.ID)
	}
	if ids := listedIDs(t, m.Repo, m.TenantB); len(ids) != 0 {
		t.Fatalf("ListByTenant(B) = %v, quiero vacía", ids)
	}
}

func invitationNewestFirst(t *testing.T, m MontajeInvitationRepo) {
	var want []string
	for range 3 {
		created := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantA))
		want = append([]string{created.ID}, want...)
	}
	if got := listedIDs(t, m.Repo, m.TenantA); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ListByTenant = %v, quiero %v (las más recientes primero)", got, want)
	}
}

func invitationTieBreak(t *testing.T, m MontajeInvitationRepo) {
	ids := make([]string, 0, 3)
	for range 3 {
		inv := newInvitation(m.TenantA)
		inv.CreatedAt = fixedCreatedAt
		ids = append(ids, m.Tables.Seed(t, inv).ID)
	}
	// Mismo created_at: desempata el id, de mayor a menor.
	want := slices.Clone(ids)
	slices.Sort(want)
	slices.Reverse(want)
	if got := listedIDs(t, m.Repo, m.TenantA); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ListByTenant con el mismo created_at = %v, quiero %v (id DESC)", got, want)
	}
}

func invitationListScoped(t *testing.T, m MontajeInvitationRepo) {
	empty := listInvitations(t, m.Repo, m.TenantA)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ListByTenant de una empresa sin invitaciones = %#v, quiero vacía y NO nil", empty)
	}
	mine := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantA))
	foreign := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantB))
	if ids := listedIDs(t, m.Repo, m.TenantA); len(ids) != 1 || ids[0] != mine.ID {
		t.Fatalf("ListByTenant(A) = %v, quiero solo %s (la de B es %s)", ids, mine.ID, foreign.ID)
	}
}

func invitationRevokeMarks(t *testing.T, m MontajeInvitationRepo) {
	created := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantA))
	if err := m.Repo.Revoke(bg(), created.ID, m.TenantA); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got := invitationByID(t, m.Repo, m.TenantA, created.ID)
	if got.RevokedAt == nil {
		t.Fatalf("tras Revoke, revoked_at sigue NULL: %+v", got)
	}
	want := created
	want.RevokedAt = got.RevokedAt
	wantSameInvitation(t, "Revoke solo marca revoked_at", got, want)
}

func invitationRevokeTwice(t *testing.T, m MontajeInvitationRepo) {
	created := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantA))
	if err := m.Repo.Revoke(bg(), created.ID, m.TenantA); err != nil {
		t.Fatalf("primer Revoke: %v", err)
	}
	first := invitationByID(t, m.Repo, m.TenantA, created.ID)
	if err := m.Repo.Revoke(bg(), created.ID, m.TenantA); err != nil {
		t.Fatalf("revocar una ya revocada: err = %v, quiero nil (es el estado que se pedía)", err)
	}
	wantSameInvitation(t, "la segunda revocación no reescribe la primera", invitationByID(t, m.Repo, m.TenantA, created.ID), first)
}

func invitationRevokeRedeemed(t *testing.T, m MontajeInvitationRepo) {
	inv := newInvitation(m.TenantA)
	inv.RedeemedBy, inv.RedeemedAt = ptr(newUser()), ptr(fixedCreatedAt)
	seeded := m.Tables.Seed(t, inv)
	if err := m.Repo.Revoke(bg(), seeded.ID, m.TenantA); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revocar una canjeada: err = %v, quiero domain.ErrConflict", err)
	}
	wantSameInvitation(t, "una canjeada no se toca", invitationByID(t, m.Repo, m.TenantA, seeded.ID), seeded)
}

func invitationRevokeForeign(t *testing.T, m MontajeInvitationRepo) {
	foreign := mustCreateInvitation(t, m.Repo, newInvitation(m.TenantB))
	if err := m.Repo.Revoke(bg(), foreign.ID, m.TenantA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revocar desde A una invitación de B: err = %v, quiero domain.ErrNotFound (no se confirma que exista fuera)", err)
	}
	wantSameInvitation(t, "la de B sigue viva", invitationByID(t, m.Repo, m.TenantB, foreign.ID), foreign)
}

func invitationRevokeMissing(t *testing.T, m MontajeInvitationRepo) {
	if err := m.Repo.Revoke(bg(), newUser(), m.TenantA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revocar una invitación que no existe: err = %v, quiero domain.ErrNotFound", err)
	}
}

func invitationRoleDeleted(t *testing.T, m MontajeInvitationRepo) {
	in := newInvitation(m.TenantA)
	in.RoleID = ptr(m.RoleA)
	created := mustCreateInvitation(t, m.Repo, in)
	m.Tables.DeleteRole(t, m.RoleA)
	want := created
	want.RoleID = nil
	wantSameInvitation(t, "borrar el rol deja la invitación viva y sin rol", invitationByID(t, m.Repo, m.TenantA, created.ID), want)
}
