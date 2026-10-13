package platformadminhelpertest

// Parte de contrato.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): los casos de las solicitudes de acceso (alta, listado, rechazo) y sus helpers.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// ── AccessRequestStore ───────────────────────────────────────────────────────────────────────

func caseCreateRequestInvalid(t *testing.T, m Montaje) {
	user := newUser()
	for _, c := range []struct{ name, user, email, origin string }{
		{"EmptyUser", "", "ana@x.com", "bff"},
		{"EmptyEmail", user, "", "bff"},
		{"UnknownOrigin", user, "ana@x.com", "web"},
		{"EmptyOrigin", user, "ana@x.com", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := m.Requests.CreateAccessRequest(bg(), c.user, c.email, c.origin)
			if !errors.Is(err, platformadmin.ErrInvalidInput) {
				t.Fatalf("CreateAccessRequest(%q, %q, %q) = %v, quiero ErrInvalidInput", c.user, c.email, c.origin, err)
			}
		})
	}
	if it := findRequest(t, m, "pending", user); it != nil {
		t.Fatalf("una solicitud rechazada por validación quedó escrita: %+v", *it)
	}
}

func caseCreateRequestListed(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "edge")
	it := findRequest(t, m, "", user) // "" ⇒ pending
	switch {
	case it == nil:
		t.Fatal("ListAccessRequests(\"\") no devuelve la solicitud recién creada (\"\" se toma como pending)")
	case it.Email != "ana@x.com" || it.Origin != "edge" || it.Status != "pending":
		t.Fatalf("solicitud = %+v, quiero ana@x.com, edge, pending", *it)
	case it.CreatedAt.IsZero():
		t.Fatalf("solicitud sin created_at: %+v", *it)
	case it.Systems == nil || len(it.Systems) != 0 || it.SystemsKnown:
		t.Fatalf("Systems = %#v, SystemsKnown = %v; quiero [] y false (C-05)", it.Systems, it.SystemsKnown)
	}
	if _, err := uuid.Parse(it.ID); err != nil {
		t.Fatalf("id de solicitud %q no es un UUID: %v", it.ID, err)
	}
	row := m.State.Request(t, it.ID)
	if row.Reason != nil || row.DecidedBy != nil || row.DecidedAt != nil {
		t.Fatalf("una solicitud pendiente no tiene motivo ni decisión: %+v", row)
	}
}

func caseCreateRequestIdempotent(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	first := findRequest(t, m, "pending", user)
	if err := m.Requests.CreateAccessRequest(bg(), user, "otra@x.com", "edge"); err != nil {
		t.Fatalf("una segunda solicitud pendiente de la misma persona no es error: %v", err)
	}
	items := listRequests(t, m, "pending")
	if n := countRequests(items, user); n != 1 {
		t.Fatalf("la persona tiene %d solicitudes pendientes, quiero 1", n)
	}
	again := findRequest(t, m, "pending", user)
	if again.ID != first.ID || again.Email != "ana@x.com" || again.Origin != "bff" {
		t.Fatalf("la pendiente cambió: %+v, quiero la primera %+v sin tocar", *again, *first)
	}
}

// caseListRequestsOrder fija que el orden lo pone created_at y no el orden de alta: la segunda
// solicitud se deja con una fecha anterior a la primera y tiene que listarse antes. Sin esto, un
// listado sin ordenar pasa, porque el orden físico de inserción coincide con el de creación
// (mutante vivo de F2-05).
func caseListRequestsOrder(t *testing.T, m Montaje) {
	first, second := newUser(), newUser()
	mustCreateRequest(t, m, first, "primera@x.com", "bff")
	mustCreateRequest(t, m, second, "segunda@x.com", "bff")
	r1 := findRequest(t, m, "pending", first)
	r2 := findRequest(t, m, "pending", second)
	m.State.SetRequestCreatedAt(t, r2.ID, r1.CreatedAt.Add(-time.Hour))
	pending := listRequests(t, m, "pending")
	if indexOfUser(pending, second) > indexOfUser(pending, first) {
		t.Fatal("pending: la solicitud con created_at anterior se lista antes, aunque se diera de alta después")
	}
}

func caseListRequestsByStatus(t *testing.T, m Montaje) {
	u1, u2, u3 := newUser(), newUser(), newUser()
	mustCreateRequest(t, m, u1, "u1@x.com", "bff")
	mustCreateRequest(t, m, u2, "u2@x.com", "bff")
	mustCreateRequest(t, m, u3, "u3@x.com", "edge")
	r1 := findRequest(t, m, "pending", u1)
	if err := m.Requests.RejectAccessRequest(bg(), r1.ID, "duplicada", newUser()); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	pending := listRequests(t, m, "pending")
	if countRequests(pending, u1) != 0 || countRequests(pending, u2) != 1 || countRequests(pending, u3) != 1 {
		t.Fatalf("pending: quiero u2 y u3, no u1: %+v", pending)
	}
	if indexOfUser(pending, u2) > indexOfUser(pending, u3) {
		t.Fatal("pending: u2 se creó antes que u3 y tiene que listarse antes (created_at ascendente)")
	}
	rejected := listRequests(t, m, "rejected")
	if countRequests(rejected, u1) != 1 || countRequests(rejected, u2) != 0 {
		t.Fatalf("rejected: quiero u1 y no u2: %+v", rejected)
	}
	if it := rejected[indexOfUser(rejected, u1)]; it.Status != "rejected" || it.Systems == nil {
		t.Fatalf("rejected: %+v", it)
	}
}

func caseLookupStatus(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	gotUser, status, err := m.Requests.LookupAccessRequestStatus(bg(), r.ID)
	if err != nil || gotUser != user || status != "pending" {
		t.Fatalf("LookupAccessRequestStatus = (%q, %q, %v), quiero (%q, pending, nil)", gotUser, status, err, user)
	}
	gotUser, status, err = m.Requests.LookupAccessRequestStatus(bg(), uuid.NewString())
	if !errors.Is(err, platformadmin.ErrNotFound) || gotUser != "" || status != "" {
		t.Fatalf("LookupAccessRequestStatus(inexistente) = (%q, %q, %v), quiero (\"\", \"\", ErrNotFound)", gotUser, status, err)
	}
}

func caseResolveRoleID(t *testing.T, m Montaje) {
	for _, role := range []Role{m.RoleA, m.RoleB} {
		for _, key := range []string{role.Name, role.ID} {
			got, err := m.Requests.ResolveRoleID(bg(), key)
			if err != nil || got != role.ID {
				t.Fatalf("ResolveRoleID(%q) = (%q, %v), quiero (%q, nil)", key, got, err, role.ID)
			}
		}
	}
	for _, unknown := range []string{"contract_no_such_role_" + strings.ReplaceAll(newUser(), "-", ""), uuid.NewString()} {
		got, err := m.Requests.ResolveRoleID(bg(), unknown)
		if !errors.Is(err, platformadmin.ErrInvalidInput) || got != "" {
			t.Fatalf("ResolveRoleID(%q) = (%q, %v), quiero (\"\", ErrInvalidInput)", unknown, got, err)
		}
	}
}

func caseRejectPending(t *testing.T, m Montaje) {
	user, operator := newUser(), newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.RejectAccessRequest(bg(), r.ID, "  no es cliente  ", operator); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	row := m.State.Request(t, r.ID)
	switch {
	case row.Status != "rejected":
		t.Fatalf("status = %q, quiero rejected", row.Status)
	case row.Reason == nil || *row.Reason != "  no es cliente  ":
		t.Fatalf("reason = %v, quiero el motivo tal cual", deref(row.Reason))
	case row.DecidedBy == nil || *row.DecidedBy != operator:
		t.Fatalf("decided_by = %v, quiero %s", deref(row.DecidedBy), operator)
	case row.DecidedAt == nil:
		t.Fatal("decided_at es NULL tras rechazar")
	case row.UserID != user || row.Email != "ana@x.com" || row.Origin != "bff":
		t.Fatalf("rechazar tocó columnas que no son suyas: %+v", row)
	}
	if member, roles := m.State.Access(t, user, m.TenantA); member || len(roles) != 0 {
		t.Fatalf("rechazar no da acceso: member=%v roles=%v", member, roles)
	}
}

func caseRejectOperatorNotUUID(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.RejectAccessRequest(bg(), r.ID, "motivo", "operator-test"); err != nil {
		t.Fatalf("RejectAccessRequest con operador no UUID: %v", err)
	}
	if row := m.State.Request(t, r.ID); row.Status != "rejected" || row.DecidedBy != nil || row.DecidedAt == nil {
		t.Fatalf("fila = %+v (decided_by %v), quiero rejected, decided_by NULL y decided_at", row, deref(row.DecidedBy))
	}
}

func caseRejectBlankReason(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	for _, c := range []struct{ name, id, reason string }{
		{"EmptyReason", r.ID, ""},
		{"BlankReason", r.ID, " \t\n "},
		{"EmptyRequestID", "", "motivo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := m.Requests.RejectAccessRequest(bg(), c.id, c.reason, newUser())
			if !errors.Is(err, platformadmin.ErrInvalidInput) {
				t.Fatalf("RejectAccessRequest(%q, %q) = %v, quiero ErrInvalidInput", c.id, c.reason, err)
			}
		})
	}
	if row := m.State.Request(t, r.ID); row.Status != "pending" || row.Reason != nil || row.DecidedBy != nil || row.DecidedAt != nil {
		t.Fatalf("un rechazo inválido tocó la fila: %+v", row)
	}
}

func caseRejectMissing(t *testing.T, m Montaje) {
	err := m.Requests.RejectAccessRequest(bg(), uuid.NewString(), "motivo", newUser())
	if !errors.Is(err, platformadmin.ErrNotFound) {
		t.Fatalf("RejectAccessRequest(inexistente) = %v, quiero ErrNotFound", err)
	}
}

func caseRejectAlreadyDecided(t *testing.T, m Montaje) {
	// Rechazada dos veces: la segunda no pisa la primera.
	u1, first := newUser(), newUser()
	mustCreateRequest(t, m, u1, "u1@x.com", "bff")
	r1 := findRequest(t, m, "pending", u1)
	if err := m.Requests.RejectAccessRequest(bg(), r1.ID, "primero", first); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	before := m.State.Request(t, r1.ID)
	if err := m.Requests.RejectAccessRequest(bg(), r1.ID, "segundo", newUser()); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("rechazar una ya rechazada = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, r1.ID))

	// Aprobada: rechazarla tampoco la toca.
	u2 := newUser()
	r2 := mustApprove(t, m, u2, m.TenantA, m.RoleA.ID)
	before = m.State.Request(t, r2)
	if err := m.Requests.RejectAccessRequest(bg(), r2, "tarde", newUser()); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("rechazar una aprobada = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, r2))
}

func mustCreateRequest(t *testing.T, m Montaje, user, email, origin string) {
	t.Helper()
	if err := m.Requests.CreateAccessRequest(bg(), user, email, origin); err != nil {
		t.Fatalf("CreateAccessRequest(%s): %v", user, err)
	}
}

func listRequests(t *testing.T, m Montaje, status string) []platformadmin.AccessRequestItem {
	t.Helper()
	items, err := m.Requests.ListAccessRequests(bg(), status)
	if err != nil {
		t.Fatalf("ListAccessRequests(%q): %v", status, err)
	}
	if items == nil {
		t.Fatalf("ListAccessRequests(%q) devolvió nil: quiero un arreglo", status)
	}
	return items
}

// findRequest devuelve la solicitud de user con ese status, o nil si no hay.
func findRequest(t *testing.T, m Montaje, status, user string) *platformadmin.AccessRequestItem {
	t.Helper()
	items := listRequests(t, m, status)
	if i := indexOfUser(items, user); i >= 0 {
		return &items[i]
	}
	return nil
}

func indexOfUser(items []platformadmin.AccessRequestItem, user string) int {
	return slices.IndexFunc(items, func(it platformadmin.AccessRequestItem) bool { return it.UserID == user })
}

func countRequests(items []platformadmin.AccessRequestItem, user string) int {
	n := 0
	for _, it := range items {
		if it.UserID == user {
			n++
		}
	}
	return n
}
