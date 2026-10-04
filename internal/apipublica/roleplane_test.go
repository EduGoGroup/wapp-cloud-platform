//go:build pendiente

package apipublica_test

// roleplane_test.go — cubre el contrato de roleplane.go (MountRolePlane, RolePlaneDeps): el
// grupo de roles B1–B8, los miembros B9–B11 (con el 503 del alta sin M2M, RX.2.e, contra el
// usecase REAL), las invitaciones B12–B14, los patrones de los tres grupos, el 404 sin servicio,
// el 405 y el mapeo de errores del dominio.
//
// Los dobles de los puertos de entrada (in.RoleAdmin, in.MembershipAdmin, in.InvitationAdmin)
// apuntan cada llamada con sus argumentos: así se prueba que el comodín del patrón llega con el
// nombre que el handler lee (r.PathValue("user_id")…), que es lo que un patrón mal copiado rompe.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// callLog apunta las llamadas de un doble como "Método arg1 arg2…".
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

func (l *callLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.calls) == 0 {
		return ""
	}
	return l.calls[len(l.calls)-1]
}

// roleAdminFake es in.RoleAdmin: apunta cada llamada y devuelve err en todas.
type roleAdminFake struct {
	callLog
	err error
}

var _ in.RoleAdmin = (*roleAdminFake)(nil)

func (f *roleAdminFake) ListRoles(context.Context) ([]domain.Role, error) {
	f.add("ListRoles")
	if f.err != nil {
		return nil, f.err
	}
	tenant := tenantA
	return []domain.Role{{ID: "r1", TenantID: &tenant, Name: "ventas"}, {ID: "g1", Name: "viewer"}}, nil
}

func (f *roleAdminFake) CreateRole(_ context.Context, input in.CreateRoleInput) (domain.Role, error) {
	f.add("CreateRole %s", input.Name)
	tenant := tenantA
	return domain.Role{ID: "r-new", TenantID: &tenant, Name: input.Name}, f.err
}

func (f *roleAdminFake) AssignRole(_ context.Context, input in.RoleAssignmentInput) error {
	f.add("AssignRole %s %s", input.UserID, input.RoleID)
	return f.err
}

func (f *roleAdminFake) UnassignRole(_ context.Context, input in.RoleAssignmentInput) error {
	f.add("UnassignRole %s %s", input.UserID, input.RoleID)
	return f.err
}

func (f *roleAdminFake) GrantToRole(_ context.Context, input in.RoleGrantInput) error {
	f.add("GrantToRole %s %s %s", input.RoleID, input.Grant.Pattern, input.Grant.Effect)
	return f.err
}

func (f *roleAdminFake) RevokeFromRole(_ context.Context, input in.RoleGrantInput) error {
	f.add("RevokeFromRole %s %s %s", input.RoleID, input.Grant.Pattern, input.Grant.Effect)
	return f.err
}

func (f *roleAdminFake) GrantToUser(_ context.Context, input in.UserGrantInput) error {
	f.add("GrantToUser %s %s %s", input.UserID, input.Grant.Pattern, input.Grant.Effect)
	return f.err
}

func (f *roleAdminFake) RevokeFromUser(_ context.Context, input in.UserGrantInput) error {
	f.add("RevokeFromUser %s %s %s", input.UserID, input.Grant.Pattern, input.Grant.Effect)
	return f.err
}

// Los patrones de cada grupo, byte a byte como el mapa §2.2.
var (
	rolePatterns = []string{
		"GET /api/v1/roles",
		"POST /api/v1/roles",
		"POST /api/v1/roles/{id}/grants",
		"DELETE /api/v1/roles/{id}/grants",
		"POST /api/v1/members/{user_id}/roles",
		"DELETE /api/v1/members/{user_id}/roles/{role_id}",
		"POST /api/v1/members/{user_id}/grants",
		"DELETE /api/v1/members/{user_id}/grants",
	}
	memberPatterns = []string{
		"GET /api/v1/members",
		"POST /api/v1/members",
		"DELETE /api/v1/members/{user_id}",
	}
	invitationPatterns = []string{
		"GET /api/v1/invitations",
		"POST /api/v1/invitations",
		"DELETE /api/v1/invitations/{id}",
	}
)

// roleRoute es una ruta del plano con la llamada que debe llegar al puerto.
type roleRoute struct {
	routeCase
	call string
}

const grantBody = `{"pattern":" flows.read ","effect":" allow "}`

var roleRoutes = []roleRoute{
	{routeCase{"B1", http.MethodGet, "/api/v1/roles", "", "roles.read", "", http.StatusOK}, "ListRoles"},
	{routeCase{"B2", http.MethodPost, "/api/v1/roles", `{"name":"ventas"}`, "roles.write", "role", http.StatusCreated}, "CreateRole ventas"},
	{routeCase{"B3", http.MethodPost, "/api/v1/roles/r1/grants", grantBody, "roles.write", "role_grant", http.StatusNoContent}, "GrantToRole r1 flows.read allow"},
	{routeCase{"B4", http.MethodDelete, "/api/v1/roles/r1/grants?pattern=flows.read&effect=deny", "", "roles.write", "role_grant", http.StatusNoContent}, "RevokeFromRole r1 flows.read deny"},
	{routeCase{"B5", http.MethodPost, "/api/v1/members/u1/roles", `{"role_id":"r1"}`, "roles.write", "user_role", http.StatusNoContent}, "AssignRole u1 r1"},
	{routeCase{"B6", http.MethodDelete, "/api/v1/members/u1/roles/r2", "", "roles.write", "user_role", http.StatusNoContent}, "UnassignRole u1 r2"},
	{routeCase{"B7", http.MethodPost, "/api/v1/members/u1/grants", grantBody, "roles.write", "user_grant", http.StatusNoContent}, "GrantToUser u1 flows.read allow"},
	{routeCase{"B8", http.MethodDelete, "/api/v1/members/u1/grants?pattern=flows.read&effect=allow", "", "roles.write", "user_grant", http.StatusNoContent}, "RevokeFromUser u1 flows.read allow"},
}

// checkRoleRoutes recorre rutas sobre una cara recién montada por mount, comprobando la cadena
// (checkChain) y la llamada que llega al puerto con log.
func checkRoleRoutes(t *testing.T, routes []roleRoute, mount func(k apipublica.Common) (*apipublica.Cara, *callLog)) {
	t.Helper()
	for _, rr := range routes {
		t.Run(rr.id, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara, log := mount(h.Common())
			checkChain(t, h, cara, rr.routeCase)
			if got := log.last(); got != rr.call {
				t.Errorf("%s: el puerto recibió %q, quiero %q", rr.id, got, rr.call)
			}
		})
	}
}

func TestMountRolePlane_RoleRoutes(t *testing.T) {
	checkRoleRoutes(t, roleRoutes, func(k apipublica.Common) (*apipublica.Cara, *callLog) {
		f := &roleAdminFake{}
		return rolesCara(k, f), &f.callLog
	})
}

func TestMountRolePlane_RoleListBody(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(rolesCara(h.Common(), &roleAdminFake{}), h.With(tenantA, "roles.read"), http.MethodGet, "/api/v1/roles", "")
	var roles []struct {
		RoleID string `json:"role_id"`
		Name   string `json:"name"`
		Global bool   `json:"global"`
	}
	wantJSON(t, "B1", rec, &roles)
	if len(roles) != 2 || roles[0].RoleID != "r1" || roles[0].Global || roles[1].RoleID != "g1" || !roles[1].Global {
		t.Errorf("B1: cuerpo %+v, quiero r1 (de la empresa) y g1 (global)", roles)
	}
}

func TestMountRolePlane_PatternsPerGroup(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cases := []struct {
		name string
		deps apipublica.RolePlaneDeps
		want []string
	}{
		{"all_three", apipublica.RolePlaneDeps{Roles: &roleAdminFake{}, Members: &membershipFake{}, Invitations: &invitationFake{}},
			append(append(append([]string{}, rolePatterns...), memberPatterns...), invitationPatterns...)},
		{"only_roles", apipublica.RolePlaneDeps{Roles: &roleAdminFake{}}, rolePatterns},
		{"only_members", apipublica.RolePlaneDeps{Members: &membershipFake{}}, memberPatterns},
		{"only_invitations", apipublica.RolePlaneDeps{Invitations: &invitationFake{}}, invitationPatterns},
		{"none", apipublica.RolePlaneDeps{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := apipublica.Nueva()
			apipublica.MountRolePlane(c, h.Common(), tc.deps)
			wantPatterns(t, tc.name, c, tc.want)
		})
	}
}

func TestMountRolePlane_NoServiceIs404(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	apipublica.MountRolePlane(c, h.Common(), apipublica.RolePlaneDeps{}) // ningún servicio
	tok := h.With(tenantA, "roles.*", "members.*")
	for _, rr := range roleRoutes {
		wantCode(t, rr.id+" sin RoleAdmin", h.Call(c, tok, rr.method, rr.target, rr.body), http.StatusNotFound)
	}
	for _, rr := range memberRoutes {
		wantCode(t, rr.id+" sin MembershipAdmin", h.Call(c, tok, rr.method, rr.target, rr.body), http.StatusNotFound)
	}
	for _, rr := range invitationRoutes {
		wantCode(t, rr.id+" sin InvitationAdmin", h.Call(c, tok, rr.method, rr.target, rr.body), http.StatusNotFound)
	}
}

func TestMountRolePlane_WrongMethodIs405(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(rolesCara(h.Common(), &roleAdminFake{}), h.With(tenantA, "roles.*"), http.MethodPut, "/api/v1/roles", "")
	wantCode(t, "PUT /api/v1/roles", rec, http.StatusMethodNotAllowed)
	allow := rec.Header().Get("Allow")
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if !strings.Contains(allow, m) {
			t.Errorf("Allow = %q, quiero que incluya %s", allow, m)
		}
	}
}

func TestMountRolePlane_DomainErrorsMapped(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"invalid_input_400", domain.ErrInvalidInput, http.StatusBadRequest},
		{"no_tenant_403", domain.ErrNoTenant, http.StatusForbidden},
		{"not_found_404_never_403", domain.ErrNotFound, http.StatusNotFound},
		{"conflict_409", domain.ErrConflict, http.StatusConflict},
		{"global_template_422", domain.ErrGlobalRoleImmutable, http.StatusUnprocessableEntity},
		{"identity_not_configured_503", domain.ErrIdentityNotConfigured, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := rolesCara(h.Common(), &roleAdminFake{err: fmt.Errorf("envuelto: %w", tc.err)})
			rec := h.Call(cara, h.With(tenantA, "roles.write"), http.MethodPost, "/api/v1/roles/r1/grants", grantBody)
			wantCode(t, "B3 con "+tc.name, rec, tc.want)
		})
	}
}

// fixedNow es el instante de los dobles: los tests no leen el reloj real.
var fixedNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// membershipFake es in.MembershipAdmin: apunta cada llamada y devuelve err en todas.
type membershipFake struct {
	callLog
	err error
}

var _ in.MembershipAdmin = (*membershipFake)(nil)

func (f *membershipFake) ListMembers(context.Context) ([]domain.Membership, error) {
	f.add("ListMembers")
	if f.err != nil {
		return nil, f.err
	}
	return []domain.Membership{{UserID: "u1", TenantID: tenantA, CreatedAt: fixedNow}}, nil
}

func (f *membershipFake) AddMember(_ context.Context, input in.MembershipInput) error {
	f.add("AddMember %s", input.UserID)
	return f.err
}

func (f *membershipFake) RemoveMember(_ context.Context, input in.MembershipInput) error {
	f.add("RemoveMember %s", input.UserID)
	return f.err
}

var memberRoutes = []roleRoute{
	{routeCase{"B9", http.MethodGet, "/api/v1/members", "", "members.read", "", http.StatusOK}, "ListMembers"},
	{routeCase{"B10", http.MethodPost, "/api/v1/members", `{"user_id":" u1 "}`, "members.write", "member", http.StatusNoContent}, "AddMember u1"},
	{routeCase{"B11", http.MethodDelete, "/api/v1/members/u2", "", "members.write", "member", http.StatusNoContent}, "RemoveMember u2"},
}

// membersCara monta solo el grupo de miembros con k y members.
func membersCara(k apipublica.Common, members in.MembershipAdmin) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountRolePlane(c, k, apipublica.RolePlaneDeps{Members: members})
	return c
}

func TestMountRolePlane_MemberRoutes(t *testing.T) {
	checkRoleRoutes(t, memberRoutes, func(k apipublica.Common) (*apipublica.Cara, *callLog) {
		f := &membershipFake{}
		return membersCara(k, f), &f.callLog
	})
}

func TestMountRolePlane_MemberListBody(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(membersCara(h.Common(), &membershipFake{}), h.With(tenantA, "members.read"), http.MethodGet, "/api/v1/members", "")
	var members []map[string]any
	wantJSON(t, "B9", rec, &members)
	if len(members) != 1 || members[0]["user_id"] != "u1" || members[0]["tenant_id"] != tenantA {
		t.Errorf("B9: cuerpo %v, quiero un miembro u1 de %s", members, tenantA)
	}
	// Lo que guarda tenant_members y NADA de la persona, que vive en identity (INV-02).
	for _, k := range []string{"email", "name"} {
		if _, ok := members[0][k]; ok {
			t.Errorf("B9: el listado trae %q, que es de identity", k)
		}
	}
}

// callerFromToken es el CallerResolver del arranque: la empresa y la persona salen del token.
var callerFromToken = in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
	id, ok := httpapi.IdentityFromContext(ctx)
	if !ok {
		return in.Caller{}, false
	}
	return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, true
})

// TestRolePlane_AddMemberWithoutM2MIs503 es RX.2.e (en la spec, TestRolePlane_AltaSinM2M_503):
// con el MembershipService REAL sin cliente M2M, el alta está montada y responde 503 —nunca
// 404—, y el listado hermano sigue en 200. El 503 nace en el usecase, no en un doble: un doble
// de in.MembershipAdmin probaría el mapeo contra sí mismo.
func TestRolePlane_AddMemberWithoutM2MIs503(t *testing.T) {
	h := apipublicahelpertest.New(t)
	svc, err := usecase.NewMembershipService(callerFromToken, memory.NewMembershipStore(), nil, nil)
	if err != nil {
		t.Fatalf("NewMembershipService sin M2M: %v", err)
	}
	cara := membersCara(h.Common(), svc)
	tok := h.With(tenantA, "members.*")

	rec := h.Call(cara, tok, http.MethodPost, "/api/v1/members", `{"user_id":"22222222-2222-2222-2222-222222222222"}`)
	wantCode(t, "B10 sin M2M", rec, http.StatusServiceUnavailable)
	wantErrorBody(t, "B10 sin M2M", rec, "identity_no_configurado")

	records := h.Auditor().Records()
	if len(records) != 1 || records[0].Resource != "member" || records[0].Result != "failure" {
		t.Errorf("B10 sin M2M: registros %+v, quiero uno de member con result failure", records)
	}

	wantCode(t, "B9 con el alta sin M2M", h.Call(cara, tok, http.MethodGet, "/api/v1/members", ""), http.StatusOK)
}

// invitationFake es in.InvitationAdmin: apunta cada llamada y devuelve err en todas.
type invitationFake struct {
	callLog
	err error
}

var _ in.InvitationAdmin = (*invitationFake)(nil)

func (f *invitationFake) invitation(id string) domain.Invitation {
	return domain.Invitation{ID: id, TenantID: tenantA, ExpiresAt: fixedNow.Add(72 * time.Hour), CreatedBy: subject}
}

func (f *invitationFake) IssueInvitation(_ context.Context, input in.IssueInvitationInput) (in.IssuedInvitation, error) {
	role := ""
	if input.RoleID != nil {
		role = *input.RoleID
	}
	f.add("IssueInvitation %s %d", role, input.TTLSeconds)
	//nolint:gosec // G101: no es una credencial, es el token opaco de un doble
	return in.IssuedInvitation{Invitation: f.invitation("i-new"), Token: "secreto-de-un-uso"}, f.err
}

func (f *invitationFake) ListInvitations(context.Context) ([]domain.Invitation, error) {
	f.add("ListInvitations")
	if f.err != nil {
		return nil, f.err
	}
	return []domain.Invitation{f.invitation("i1")}, nil
}

func (f *invitationFake) RevokeInvitation(_ context.Context, id string) error {
	f.add("RevokeInvitation %s", id)
	return f.err
}

var invitationRoutes = []roleRoute{
	{routeCase{"B12", http.MethodGet, "/api/v1/invitations", "", "members.read", "", http.StatusOK}, "ListInvitations"},
	{routeCase{"B13", http.MethodPost, "/api/v1/invitations", `{"role_id":"r1","ttl":3600}`, "members.write", "invitation", http.StatusCreated}, "IssueInvitation r1 3600"},
	{routeCase{"B14", http.MethodDelete, "/api/v1/invitations/i1", "", "members.write", "invitation", http.StatusNoContent}, "RevokeInvitation i1"},
}

// invitationsCara monta solo el grupo de invitaciones con k e invitations.
func invitationsCara(k apipublica.Common, invitations in.InvitationAdmin) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountRolePlane(c, k, apipublica.RolePlaneDeps{Invitations: invitations})
	return c
}

func TestMountRolePlane_InvitationRoutes(t *testing.T) {
	checkRoleRoutes(t, invitationRoutes, func(k apipublica.Common) (*apipublica.Cara, *callLog) {
		f := &invitationFake{}
		return invitationsCara(k, f), &f.callLog
	})
}

// TestMountRolePlane_InvitationTokenOnlyOnIssue: el código de un solo uso viaja en la respuesta
// de la emisión y NUNCA en el listado (T-A2).
func TestMountRolePlane_InvitationTokenOnlyOnIssue(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := invitationsCara(h.Common(), &invitationFake{})

	var issued map[string]any
	wantJSON(t, "B13", h.Call(cara, h.With(tenantA, "members.write"), http.MethodPost, "/api/v1/invitations", `{}`), &issued)
	if issued["token"] != "secreto-de-un-uso" || issued["id"] != "i-new" {
		t.Errorf("B13: cuerpo %v, quiero id i-new y el token", issued)
	}

	var listed []map[string]any
	wantJSON(t, "B12", h.Call(cara, h.With(tenantA, "members.read"), http.MethodGet, "/api/v1/invitations", ""), &listed)
	if len(listed) != 1 || listed[0]["id"] != "i1" {
		t.Fatalf("B12: cuerpo %v, quiero la invitación i1", listed)
	}
	if _, ok := listed[0]["token"]; ok {
		t.Error("B12: el listado trae el token de la invitación")
	}
}

// TestMountRolePlane_InvitationsCoexistWithAccept: B14 y A4 (de MountAuth) conviven en la misma
// cara sin conflicto (mapa §4.2) y cada una llega a su handler.
func TestMountRolePlane_InvitationsCoexistWithAccept(t *testing.T) {
	h := apipublicahelpertest.New(t)
	c := apipublica.Nueva()
	inv := &invitationFake{}
	redeemer := &redeemerFake{}
	apipublica.MountRolePlane(c, h.Common(), apipublica.RolePlaneDeps{Invitations: inv})
	apipublica.MountAuth(c, h.Common(), apipublica.AuthDeps{Verifier: &verifierFake{}, Redeemer: redeemer})

	wantCode(t, "A4 junto a B14", h.Call(c, h.Tenantless("nuevo"), http.MethodPost, "/api/v1/invitations/accept", `{"token":"t"}`), http.StatusNoContent)
	wantCode(t, "B14 junto a A4", h.Call(c, h.With(tenantA, "members.write"), http.MethodDelete, "/api/v1/invitations/accept", ""), http.StatusNoContent)
	if inv.last() != "RevokeInvitation accept" || redeemer.last() != "RedeemInvitation t" {
		t.Errorf("llamadas: invitaciones %q, canje %q; quiero RevokeInvitation accept y RedeemInvitation t", inv.last(), redeemer.last())
	}
}
