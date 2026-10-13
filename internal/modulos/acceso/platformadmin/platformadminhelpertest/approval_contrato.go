package platformadminhelpertest

// Parte de contrato.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): los casos de la aprobación transaccional y del reintento.

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

func caseApproveGrants(t *testing.T, m Montaje) {
	user, operator := newUser(), newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.ExecuteApprovalTx(bg(), r.ID, m.TenantA, user, m.RoleA.ID, operator); err != nil {
		t.Fatalf("ExecuteApprovalTx: %v", err)
	}
	row := m.State.Request(t, r.ID)
	switch {
	case row.Status != "approved":
		t.Fatalf("status = %q, quiero approved", row.Status)
	case row.DecidedBy == nil || *row.DecidedBy != operator:
		t.Fatalf("decided_by = %v, quiero %s", deref(row.DecidedBy), operator)
	case row.DecidedAt == nil:
		t.Fatal("decided_at es NULL tras aprobar")
	case row.Reason != nil:
		t.Fatalf("aprobar no escribe motivo: %v", *row.Reason)
	}
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
	assertAccess(t, m, user, m.TenantB, false)
	if _, status, err := m.Requests.LookupAccessRequestStatus(bg(), r.ID); err != nil || status != "approved" {
		t.Fatalf("LookupAccessRequestStatus tras aprobar = %q, %v", status, err)
	}
}

func caseApproveNotPending(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.RejectAccessRequest(bg(), r.ID, "no", newUser()); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	before := m.State.Request(t, r.ID)
	err := m.Requests.ExecuteApprovalTx(bg(), r.ID, m.TenantA, user, m.RoleA.ID, newUser())
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("aprobar una rechazada = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, r.ID))
	// O todo o nada: la membresía que el alta habría escrito antes del UPDATE tampoco queda.
	assertAccess(t, m, user, m.TenantA, false)

	missing := uuid.NewString()
	if err := m.Requests.ExecuteApprovalTx(bg(), missing, m.TenantA, user, m.RoleA.ID, newUser()); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("aprobar una solicitud inexistente = %v, quiero ErrConflict", err)
	}
	assertAccess(t, m, user, m.TenantA, false)
}

func caseApproveOtherCompany(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	mustCreateRequest(t, m, user, "ana@x.com", "edge")
	second := findRequest(t, m, "pending", user)
	before := m.State.Request(t, second.ID)
	err := m.Requests.ExecuteApprovalTx(bg(), second.ID, m.TenantB, user, m.RoleB.ID, newUser())
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("aprobar hacia una segunda empresa sin multi_empresa = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, second.ID))
	assertAccess(t, m, user, m.TenantB, false)
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
}

func caseApproveSameCompanyAgain(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	second := mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	if row := m.State.Request(t, second); row.Status != "approved" {
		t.Fatalf("la segunda aprobación hacia la MISMA empresa tiene que pasar: status %q", row.Status)
	}
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
}

func caseCheckRetry(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantA, m.RoleA.ID); err != nil {
		t.Fatalf("reintento con la misma empresa y el mismo rol = %v, quiero nil (converge)", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantA, m.RoleB.ID); !errors.Is(err, platformadmin.ErrRetryRoleMismatch) {
		t.Fatalf("reintento con otro rol = %v, quiero ErrRetryRoleMismatch", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantB, m.RoleA.ID); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("reintento hacia otra empresa = %v, quiero ErrConflict", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), newUser(), m.TenantA, m.RoleA.ID); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("reintento de quien no es miembro = %v, quiero ErrConflict", err)
	}
	// Solo lee: el rol pedido en el reintento NO se escribe.
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
	assertAccess(t, m, user, m.TenantB, false)
}

// El rol del reintento se mira EN la empresa de la solicitud: tener ese rol en OTRA empresa no
// hace converger (iam_user_roles es por empresa desde la 0060).
func caseCheckRetryRoleScoped(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	m.State.SeedMembership(t, user, m.TenantB, m.RoleB.ID)
	assertAccess(t, m, user, m.TenantB, true, m.RoleB.ID)
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantA, m.RoleB.ID); !errors.Is(err, platformadmin.ErrRetryRoleMismatch) {
		t.Fatalf("reintento en A con el rol que tiene en B = %v, quiero ErrRetryRoleMismatch", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantB, m.RoleB.ID); err != nil {
		t.Fatalf("reintento en B con su rol de B = %v, quiero nil", err)
	}
}

// mustApprove crea una solicitud de user y la aprueba hacia tenant con roleID; devuelve su id.
func mustApprove(t *testing.T, m Montaje, user, tenant, roleID string) string {
	t.Helper()
	mustCreateRequest(t, m, user, "aprobada@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if r == nil {
		t.Fatalf("no encuentro la solicitud pendiente de %s", user)
	}
	if err := m.Requests.ExecuteApprovalTx(bg(), r.ID, tenant, user, roleID, newUser()); err != nil {
		t.Fatalf("ExecuteApprovalTx(%s → %s): %v", user, tenant, err)
	}
	return r.ID
}

func assertAccess(t *testing.T, m Montaje, user, tenant string, wantMember bool, wantRoles ...string) {
	t.Helper()
	member, roles := m.State.Access(t, user, tenant)
	if member != wantMember || len(roles) != len(wantRoles) || !slices.Equal(roles, wantRoles) {
		t.Fatalf("acceso de %s a %s = (member %v, roles %v), quiero (member %v, roles %v)", user, tenant, member, roles, wantMember, wantRoles)
	}
}

func assertSameRow(t *testing.T, before, after RequestRow) {
	t.Helper()
	same := before.UserID == after.UserID && before.Email == after.Email && before.Origin == after.Origin &&
		before.Status == after.Status && deref(before.Reason) == deref(after.Reason) &&
		deref(before.DecidedBy) == deref(after.DecidedBy) && sameTime(before.DecidedAt, after.DecidedAt) &&
		before.CreatedAt.Equal(after.CreatedAt)
	if !same {
		t.Fatalf("la fila cambió:\nantes   %+v\ndespués %+v", before, after)
	}
}
