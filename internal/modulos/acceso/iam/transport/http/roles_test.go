package iamhttp

// Una aserción por promesa de R-H7 y R-H9 (roles.go), con dobles de in.RoleAdmin e
// in.MembershipAdmin. Las rutas con comodín se sirven a través de un mux con los patrones
// método+ruta de Go 1.22, que es como se montan en el arranque.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// fakeRoleAdmin devuelve siempre lo mismo y guarda el método llamado y su entrada.
type fakeRoleAdmin struct {
	roles  []domain.Role
	role   domain.Role
	err    error
	calls  int
	method string
	create in.CreateRoleInput
	// last es la entrada de la última llamada con entrada comparable (asignaciones y grants).
	last any
}

var _ in.RoleAdmin = (*fakeRoleAdmin)(nil)

func (f *fakeRoleAdmin) note(m string, input any) {
	f.calls++
	f.method = m
	f.last = input
}

func (f *fakeRoleAdmin) ListRoles(context.Context) ([]domain.Role, error) {
	f.note("ListRoles", nil)
	return f.roles, f.err
}

func (f *fakeRoleAdmin) CreateRole(_ context.Context, input in.CreateRoleInput) (domain.Role, error) {
	f.note("CreateRole", nil)
	f.create = input
	return f.role, f.err
}

func (f *fakeRoleAdmin) AssignRole(_ context.Context, input in.RoleAssignmentInput) error {
	f.note("AssignRole", input)
	return f.err
}

func (f *fakeRoleAdmin) UnassignRole(_ context.Context, input in.RoleAssignmentInput) error {
	f.note("UnassignRole", input)
	return f.err
}

func (f *fakeRoleAdmin) GrantToRole(_ context.Context, input in.RoleGrantInput) error {
	f.note("GrantToRole", input)
	return f.err
}

func (f *fakeRoleAdmin) RevokeFromRole(_ context.Context, input in.RoleGrantInput) error {
	f.note("RevokeFromRole", input)
	return f.err
}

func (f *fakeRoleAdmin) GrantToUser(_ context.Context, input in.UserGrantInput) error {
	f.note("GrantToUser", input)
	return f.err
}

func (f *fakeRoleAdmin) RevokeFromUser(_ context.Context, input in.UserGrantInput) error {
	f.note("RevokeFromUser", input)
	return f.err
}

// fakeMembershipAdmin devuelve siempre lo mismo y guarda la entrada.
type fakeMembershipAdmin struct {
	members  []domain.Membership
	err      error
	calls    int
	method   string
	received in.MembershipInput
}

var _ in.MembershipAdmin = (*fakeMembershipAdmin)(nil)

func (f *fakeMembershipAdmin) ListMembers(context.Context) ([]domain.Membership, error) {
	f.calls++
	f.method = "ListMembers"
	return f.members, f.err
}

func (f *fakeMembershipAdmin) AddMember(_ context.Context, input in.MembershipInput) error {
	f.calls++
	f.method = "AddMember"
	f.received = input
	return f.err
}

func (f *fakeMembershipAdmin) RemoveMember(_ context.Context, input in.MembershipInput) error {
	f.calls++
	f.method = "RemoveMember"
	f.received = input
	return f.err
}

// newRoleHandlers construye los dos handlers de este fichero.
func newRoleHandlers(roles in.RoleAdmin, members in.MembershipAdmin) (*RoleAdminHandler, *MembershipHandler) {
	return NewRoleAdminHandler(roles), NewMembershipHandler(members)
}

// rolesMux monta los handlers con los patrones del arranque.
func rolesMux(roles in.RoleAdmin, members in.MembershipAdmin) *http.ServeMux {
	rh, mh := newRoleHandlers(roles, members)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/roles", rh.List())
	mux.Handle("POST /api/v1/roles", rh.Create())
	mux.Handle("POST /api/v1/roles/{id}/grants", rh.AddRoleGrant())
	mux.Handle("DELETE /api/v1/roles/{id}/grants", rh.RemoveRoleGrant())
	mux.Handle("POST /api/v1/members/{user_id}/roles", rh.AssignRole())
	mux.Handle("DELETE /api/v1/members/{user_id}/roles/{role_id}", rh.UnassignRole())
	mux.Handle("POST /api/v1/members/{user_id}/grants", rh.AddUserGrant())
	mux.Handle("DELETE /api/v1/members/{user_id}/grants", rh.RemoveUserGrant())
	mux.Handle("GET /api/v1/members", mh.List())
	mux.Handle("POST /api/v1/members", mh.Add())
	mux.Handle("DELETE /api/v1/members/{user_id}", mh.Remove())
	return mux
}

func strPtr(s string) *string { return &s }

// List: forma exacta, `global` derivada, opcionales omitidos, instantes en UTC.
func TestRoleList_ShapeAndDerivedGlobal(t *testing.T) {
	created := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	f := &fakeRoleAdmin{roles: []domain.Role{
		{ID: "r-global", Name: "viewer"},
		{ID: "r-own", Name: "ventas", TenantID: strPtr(tenantA), ParentRoleID: strPtr("r-global"), CreatedAt: created},
	}}
	rec := serve(t, rolesMux(f, &fakeMembershipAdmin{}), http.MethodGet, "/api/v1/roles", "")
	want := `[{"role_id":"r-global","name":"viewer","global":true},` +
		`{"role_id":"r-own","name":"ventas","tenant_id":"` + tenantA + `","parent_role_id":"r-global","global":false,"created_at":"2030-01-02T02:04:05Z"}]`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 200 %s", rec.Code, rec.Body.String(), want)
	}
}

// List de roles y de miembros: vacío es `[]`, nunca `null`.
func TestLists_EmptyIsArray(t *testing.T) {
	mux := rolesMux(&fakeRoleAdmin{}, &fakeMembershipAdmin{})
	for _, path := range []string{"/api/v1/roles", "/api/v1/members"} {
		if rec := serve(t, mux, http.MethodGet, path, ""); rec.Code != http.StatusOK || rec.Body.String() != `[]` {
			t.Errorf("%s: respuesta = %d %s; se esperaba 200 []", path, rec.Code, rec.Body.String())
		}
	}
}

// Create: campos recortados; padre vacío ⇒ raíz (nil); 201 con el rol.
func TestRoleCreate_TrimsAndRootParent(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantName   string
		wantParent *string
	}{
		{"blank_parent_is_root", `{"name":"  ventas  ","parent_role_id":"   "}`, "ventas", nil},
		{"parent_is_trimmed", `{"name":"ventas","parent_role_id":" r-1 "}`, "ventas", strPtr("r-1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRoleAdmin{role: domain.Role{ID: "r-new", Name: "ventas", TenantID: strPtr(tenantA)}}
			rec := serve(t, rolesMux(f, &fakeMembershipAdmin{}), http.MethodPost, "/api/v1/roles", c.body)
			if rec.Code != http.StatusCreated || rec.Body.String() != `{"role_id":"r-new","name":"ventas","tenant_id":"`+tenantA+`","global":false}` {
				t.Errorf("respuesta = %d %s; se esperaba 201 con el rol", rec.Code, rec.Body.String())
			}
			got := f.create
			if got.Name != c.wantName || (got.ParentRoleID == nil) != (c.wantParent == nil) ||
				(got.ParentRoleID != nil && *got.ParentRoleID != *c.wantParent) {
				t.Errorf("el puerto recibió %+v; se esperaba nombre %q y padre %v", got, c.wantName, c.wantParent)
			}
		})
	}
}

// Las rutas, la query y el cuerpo llegan al puerto (recortados) y el éxito es 204 sin cuerpo.
func TestRoleAdmin_RoutesReachPort(t *testing.T) {
	allowFlows := domain.Grant{Pattern: "flows.*", Effect: domain.EffectAllow}
	denySessions := domain.Grant{Pattern: "sessions.*", Effect: domain.EffectDeny}
	cases := []struct {
		name       string
		method     string
		target     string
		body       string
		wantMethod string
		wantInput  any
	}{
		{"add_role_grant_from_body", http.MethodPost, "/api/v1/roles/r-1/grants", `{"pattern":" flows.* ","effect":" allow "}`,
			"GrantToRole", in.RoleGrantInput{RoleID: "r-1", Grant: allowFlows}},
		{"remove_role_grant_from_query", http.MethodDelete, "/api/v1/roles/r-1/grants?pattern=%20sessions.*%20&effect=deny", "",
			"RevokeFromRole", in.RoleGrantInput{RoleID: "r-1", Grant: denySessions}},
		{"assign_role", http.MethodPost, "/api/v1/members/u-1/roles", `{"role_id":" r-1 "}`,
			"AssignRole", in.RoleAssignmentInput{UserID: "u-1", RoleID: "r-1"}},
		{"unassign_role", http.MethodDelete, "/api/v1/members/u-1/roles/r-1", "",
			"UnassignRole", in.RoleAssignmentInput{UserID: "u-1", RoleID: "r-1"}},
		{"add_user_grant_from_body", http.MethodPost, "/api/v1/members/u-1/grants", `{"pattern":"flows.*","effect":"allow"}`,
			"GrantToUser", in.UserGrantInput{UserID: "u-1", Grant: allowFlows}},
		{"remove_user_grant_from_query", http.MethodDelete, "/api/v1/members/u-1/grants?pattern=sessions.*&effect=%20deny%20", "",
			"RevokeFromUser", in.UserGrantInput{UserID: "u-1", Grant: denySessions}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRoleAdmin{}
			rec := serve(t, rolesMux(f, &fakeMembershipAdmin{}), c.method, c.target, c.body)
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Errorf("respuesta = %d %q; se esperaba 204 sin cuerpo", rec.Code, rec.Body.String())
			}
			if f.calls != 1 || f.method != c.wantMethod || f.last != c.wantInput {
				t.Errorf("puerto: %d llamadas a %s con %+v; se esperaba 1 a %s con %+v", f.calls, f.method, f.last, c.wantMethod, c.wantInput)
			}
		})
	}
}

// R-H7: los errores del puerto salen por el mapeo de diseño §5, con su texto.
func TestRoleAdmin_DomainErrorsMapping(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		body   string
		err    error
		status int
		msg    string
	}{
		{"list_without_tenant_403", http.MethodGet, "/api/v1/roles", "", domain.ErrNoTenant, http.StatusForbidden, "el token no trae empresa: no puede administrar roles ni miembros"},
		{"create_duplicate_409", http.MethodPost, "/api/v1/roles", `{"name":"x"}`, domain.ErrConflict, http.StatusConflict, "conflicto: el recurso ya existe o la persona ya pertenece a otra empresa"},
		{"create_foreign_parent_404", http.MethodPost, "/api/v1/roles", `{"name":"x","parent_role_id":"p"}`, domain.ErrNotFound, http.StatusNotFound, "recurso no encontrado"},
		{"grant_on_global_422", http.MethodPost, "/api/v1/roles/r-1/grants", `{"pattern":"a","effect":"allow"}`, domain.ErrGlobalRoleImmutable, http.StatusUnprocessableEntity, "las plantillas de rol globales no se modifican desde una empresa"},
		{"invalid_grant_400", http.MethodDelete, "/api/v1/roles/r-1/grants", "", domain.ErrInvalidInput, http.StatusBadRequest, "entrada inválida"},
		{"assign_to_non_member_404", http.MethodPost, "/api/v1/members/u-1/roles", `{"role_id":"r"}`, domain.ErrNotFound, http.StatusNotFound, "recurso no encontrado"},
		{"user_grant_infra_500", http.MethodPost, "/api/v1/members/u-1/grants", `{"pattern":"a","effect":"allow"}`, errors.New("caído"), http.StatusInternalServerError, "error interno"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, rolesMux(&fakeRoleAdmin{err: c.err}, &fakeMembershipAdmin{}), c.method, c.target, c.body)
			if rec.Code != c.status || rec.Body.String() != errorBody(c.msg) {
				t.Errorf("respuesta = %d %s; se esperaba %d %s", rec.Code, rec.Body.String(), c.status, errorBody(c.msg))
			}
		})
	}
}

// R-H7: JSON roto ⇒ 400 sin llamar al puerto, en todos los handlers con cuerpo.
func TestRoleAdmin_BrokenJSONIs400WithoutPort(t *testing.T) {
	for _, c := range []struct{ method, target string }{
		{http.MethodPost, "/api/v1/roles"},
		{http.MethodPost, "/api/v1/roles/r-1/grants"},
		{http.MethodPost, "/api/v1/members/u-1/roles"},
		{http.MethodPost, "/api/v1/members/u-1/grants"},
		{http.MethodPost, "/api/v1/members"},
	} {
		roles, members := &fakeRoleAdmin{}, &fakeMembershipAdmin{}
		rec := serve(t, rolesMux(roles, members), c.method, c.target, `{`)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != errorBody("cuerpo JSON inválido") || roles.calls+members.calls != 0 {
			t.Errorf("%s %s: %d %s (llamadas %d); se esperaba 400 sin llamar al puerto", c.method, c.target, rec.Code, rec.Body.String(), roles.calls+members.calls)
		}
	}
}

// R-H7: un comodín de ruta vacío (handler montado sin patrón) ⇒ 400 «<nombre> requerido en la
// ruta», sin llamar al puerto.
func TestHandlers_EmptyPathValueIs400(t *testing.T) {
	roles, members := &fakeRoleAdmin{}, &fakeMembershipAdmin{}
	rh, mh := NewRoleAdminHandler(roles), NewMembershipHandler(members)
	cases := []struct {
		name string
		h    http.Handler
		msg  string
	}{
		{"add_role_grant", rh.AddRoleGrant(), "id requerido en la ruta"},
		{"remove_role_grant", rh.RemoveRoleGrant(), "id requerido en la ruta"},
		{"assign_role", rh.AssignRole(), "user_id requerido en la ruta"},
		{"unassign_role", rh.UnassignRole(), "user_id requerido en la ruta"},
		{"add_user_grant", rh.AddUserGrant(), "user_id requerido en la ruta"},
		{"remove_user_grant", rh.RemoveUserGrant(), "user_id requerido en la ruta"},
		{"remove_member", mh.Remove(), "user_id requerido en la ruta"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, c.h, http.MethodPost, "/x", `{}`)
			if rec.Code != http.StatusBadRequest || rec.Body.String() != errorBody(c.msg) {
				t.Errorf("respuesta = %d %s; se esperaba 400 %s", rec.Code, rec.Body.String(), errorBody(c.msg))
			}
		})
	}
	if roles.calls+members.calls != 0 {
		t.Errorf("se llamó al puerto %d veces con un comodín vacío", roles.calls+members.calls)
	}
}

// UnassignRole: con user_id pero sin role_id ⇒ 400 «role_id requerido en la ruta».
func TestUnassignRole_EmptyRoleIDIs400(t *testing.T) {
	f := &fakeRoleAdmin{}
	mux := http.NewServeMux()
	mux.Handle("DELETE /api/v1/members/{user_id}/roles/", NewRoleAdminHandler(f).UnassignRole())
	rec := serve(t, mux, http.MethodDelete, "/api/v1/members/u-1/roles/", "")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != errorBody("role_id requerido en la ruta") || f.calls != 0 {
		t.Errorf("respuesta = %d %s (llamadas %d); se esperaba 400 sin llamar al puerto", rec.Code, rec.Body.String(), f.calls)
	}
}

// Members List: exactamente user_id, tenant_id y created_at (si existe); ni name ni email.
func TestMemberList_ExactShape(t *testing.T) {
	created := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	f := &fakeMembershipAdmin{members: []domain.Membership{
		{UserID: "u-1", TenantID: tenantA, CreatedAt: created},
		{UserID: "u-2", TenantID: tenantA},
	}}
	rec := serve(t, rolesMux(&fakeRoleAdmin{}, f), http.MethodGet, "/api/v1/members", "")
	want := `[{"user_id":"u-1","tenant_id":"` + tenantA + `","created_at":"2030-01-02T02:04:05Z"},{"user_id":"u-2","tenant_id":"` + tenantA + `"}]`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 200 %s", rec.Code, rec.Body.String(), want)
	}
}

// Members Add y Remove: el user_id llega recortado (cuerpo) o por la ruta; 204 sin cuerpo.
func TestMembers_AddAndRemoveReachPort(t *testing.T) {
	f := &fakeMembershipAdmin{}
	mux := rolesMux(&fakeRoleAdmin{}, f)
	rec := serve(t, mux, http.MethodPost, "/api/v1/members", `{"user_id":"  u-1  "}`)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || f.method != "AddMember" || f.received != (in.MembershipInput{UserID: "u-1"}) {
		t.Errorf("alta: %d %q, %s %+v; se esperaba 204 y AddMember{u-1}", rec.Code, rec.Body.String(), f.method, f.received)
	}
	rec = serve(t, mux, http.MethodDelete, "/api/v1/members/u-2", "")
	if rec.Code != http.StatusNoContent || f.method != "RemoveMember" || f.received != (in.MembershipInput{UserID: "u-2"}) {
		t.Errorf("baja: %d, %s %+v; se esperaba 204 y RemoveMember{u-2}", rec.Code, f.method, f.received)
	}
}

// R-H7 y R-H9: los seis desenlaces del alta; sin M2M es 503 con su cuerpo PROPIO.
func TestMemberAdd_SixOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"ok_204", nil, http.StatusNoContent, ""},
		{"no_m2m_503", domain.ErrIdentityNotConfigured, http.StatusServiceUnavailable, errorBody("identity_no_configurado")},
		{"identity_down_503", domain.ErrIdentityUnavailable, http.StatusServiceUnavailable, errorBody("identity no está disponible")},
		{"system_not_allowed_502", domain.ErrSystemNotAllowed, http.StatusBadGateway, errorBody("system_no_acreditable")},
		{"unknown_uuid_404", domain.ErrNotFound, http.StatusNotFound, errorBody("recurso no encontrado")},
		{"other_company_409", domain.ErrConflict, http.StatusConflict, errorBody("conflicto: el recurso ya existe o la persona ya pertenece a otra empresa")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, rolesMux(&fakeRoleAdmin{}, &fakeMembershipAdmin{err: c.err}), http.MethodPost, "/api/v1/members", `{"user_id":"u-1"}`)
			if rec.Code != c.status || rec.Body.String() != c.body {
				t.Errorf("respuesta = %d %q; se esperaba %d %q", rec.Code, rec.Body.String(), c.status, c.body)
			}
		})
	}
}
