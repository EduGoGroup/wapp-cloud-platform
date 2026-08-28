package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// testCallerKey es la clave del contexto con la que estos tests entregan la
// identidad del llamante. Es DELIBERADO que el tenant viaje por el contexto y no
// por un campo del fixture: lo que se está probando es INV-04 —el tenant sale
// del contexto—, y un fixture que lo llevara fijo probaría otra cosa.
type testCallerKey struct{}

// withCaller devuelve un contexto que porta la identidad dada.
func withCaller(ctx context.Context, c in.Caller) context.Context {
	return context.WithValue(ctx, testCallerKey{}, c)
}

// testResolver es el CallerResolver de los tests: lee del contexto lo que
// withCaller puso, igual que en producción leerá lo que dejó el middleware.
var testResolver = in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
	c, ok := ctx.Value(testCallerKey{}).(in.Caller)
	return c, ok
})

// roleFixture arma un RoleService sobre los dobles en memoria.
type roleFixture struct {
	svc   *usecase.RoleService
	store *memory.Store
}

func newRoleFixture(t *testing.T) roleFixture {
	t.Helper()
	store := memory.NewStore()
	svc, err := usecase.NewRoleService(testResolver, store.Roles, store.Grants, store.Memberships)
	if err != nil {
		t.Fatalf("NewRoleService: %v", err)
	}
	return roleFixture{svc: svc, store: store}
}

// ctxOf devuelve un contexto de llamante para el tenant dado.
func ctxOf(tenantID string) context.Context {
	return withCaller(context.Background(), in.Caller{TenantID: tenantID, UserID: "operador-" + tenantID})
}

// seedMemberOf da de alta un UUID de identity como miembro del tenant.
func (f roleFixture) seedMemberOf(t *testing.T, tenantID string) string {
	t.Helper()
	userID := uuid.NewString()
	if err := f.store.Memberships.Add(context.Background(), userID, tenantID); err != nil {
		t.Fatalf("sembrar membresía: %v", err)
	}
	return userID
}

// Los cuatro lectores de abajo existen para que un fallo del doble no se
// disfrace de "el estado no era el esperado": leen el store y abortan con el
// error real si lo hay, en vez de tragárselo con `_`.

// rolesDe devuelve los roles que el usuario tiene EN ese tenant.
func (f roleFixture) rolesDe(t *testing.T, userID, tenantID string) []domain.Role {
	t.Helper()
	roles, err := f.store.Roles.RolesOfUser(context.Background(), userID, tenantID)
	if err != nil {
		t.Fatalf("RolesOfUser(%s, %s): %v", userID, tenantID, err)
	}
	return roles
}

// rolesVisibles devuelve los roles que el tenant ve (propios + plantillas).
func (f roleFixture) rolesVisibles(t *testing.T, tenantID string) []domain.Role {
	t.Helper()
	roles, err := f.store.Roles.List(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("List(%s): %v", tenantID, err)
	}
	return roles
}

// grantsDelRol devuelve los grants DIRECTOS de un rol (sin herencia).
func (f roleFixture) grantsDelRol(t *testing.T, roleID string) []domain.Grant {
	t.Helper()
	gs, err := f.store.Roles.GrantsOf(context.Background(), roleID)
	if err != nil {
		t.Fatalf("GrantsOf(%s): %v", roleID, err)
	}
	return gs
}

// overridesDe devuelve los overrides de grants de una persona.
func (f roleFixture) overridesDe(t *testing.T, userID string) []domain.Grant {
	t.Helper()
	gs, err := f.store.Grants.GrantsOfUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("GrantsOfUser(%s): %v", userID, err)
	}
	return gs
}

// TestListRoles_SoloLoVisibleDelTenantDelContexto: los roles del tenant A más
// las plantillas globales, y NUNCA los de otra empresa.
func TestListRoles_SoloLoVisibleDelTenantDelContexto(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	propio := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "propio"}, nil)
	global := f.store.Roles.Seed(domain.Role{Name: "plantilla-global"}, nil)
	ajeno := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "ajeno"}, nil)

	roles, err := f.svc.ListRoles(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	visto := make(map[string]bool, len(roles))
	for _, r := range roles {
		visto[r.ID] = true
	}
	if !visto[propio.ID] || !visto[global.ID] {
		t.Errorf("faltan roles visibles: propio=%v global=%v", visto[propio.ID], visto[global.ID])
	}
	if visto[ajeno.ID] {
		t.Error("se coló el rol de otra empresa en la lista del tenant")
	}
}

// TestCreateRole_NaceEnElTenantDelContexto: el rol creado queda acotado a la
// empresa del llamante, no global ni de otra.
func TestCreateRole_NaceEnElTenantDelContexto(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	role, err := f.svc.CreateRole(ctxOf(testTenant), in.CreateRoleInput{Name: "supervisor"})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.TenantID == nil {
		t.Fatal("el rol nació GLOBAL: por esta vía no se crean plantillas")
	}
	if *role.TenantID != testTenant {
		t.Errorf("tenant del rol = %s, quiero %s (el del contexto)", *role.TenantID, testTenant)
	}
}

// TestCreateRole_ParentDeOtraEmpresaSeRechaza: heredar de un rol ajeno copiaría
// sus grants a esta empresa por la cadena de herencia.
func TestCreateRole_ParentDeOtraEmpresaSeRechaza(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	ajeno := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "ajeno"},
		[]domain.Grant{{Pattern: "flows.*", Effect: domain.EffectAllow}})

	_, err := f.svc.CreateRole(ctxOf(testTenant), in.CreateRoleInput{Name: "hijo", ParentRoleID: ptr(ajeno.ID)})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, quiero ErrNotFound", err)
	}
}

// TestAssignRole_LaAsignacionQuedaAcotadaAlTenantDelContexto es EL test del
// invariante INV-04 de esta tarea, y el que la mutación tiene que poner rojo.
//
// El rol asignado es una PLANTILLA GLOBAL (tenant_id NULL) a propósito: es el
// caso donde el tenant del recurso —que llega en el cuerpo, dentro del role_id—
// y el tenant del contexto no coinciden. Si el usecase asignara con el tenant
// del ROL en vez de con el del CONTEXTO, la fila saldría con tenant_id NULL, que
// RoleRepo.RolesOfUser resuelve como válida en TODAS las empresas: un rol
// concedido en una se convertiría en permisos en todas.
func TestAssignRole_LaAsignacionQuedaAcotadaAlTenantDelContexto(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	global := f.store.Roles.Seed(domain.Role{Name: "plantilla-operator"},
		[]domain.Grant{{Pattern: "messages.send", Effect: domain.EffectAllow}})
	userID := f.seedMemberOf(t, testTenant)

	if err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userID, RoleID: global.ID}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	enSuTenant, err := f.store.Roles.RolesOfUser(context.Background(), userID, testTenant)
	if err != nil || len(enSuTenant) != 1 || enSuTenant[0].ID != global.ID {
		t.Fatalf("en su propia empresa debía verse el rol asignado: err=%v roles=%+v", err, enSuTenant)
	}

	enOtroTenant, err := f.store.Roles.RolesOfUser(context.Background(), userID, testTenantB)
	if err != nil {
		t.Fatalf("RolesOfUser (otra empresa): %v", err)
	}
	if len(enOtroTenant) != 0 {
		t.Fatalf("la asignación se filtró a otra empresa (%+v): el tenant salió del ROL y no del CONTEXTO", enOtroTenant)
	}
}

// TestAssignRole_RechazaRolDeOtraEmpresa: el role_id lo elige el llamante, así
// que se acota antes de tocarlo. ErrNotFound (opaco), no "prohibido".
func TestAssignRole_RechazaRolDeOtraEmpresa(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	ajeno := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "ajeno"}, nil)
	userID := f.seedMemberOf(t, testTenant)

	err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userID, RoleID: ajeno.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, quiero ErrNotFound", err)
	}
	roles := f.rolesDe(t, userID, testTenant)
	if len(roles) != 0 {
		t.Fatalf("no debía escribirse nada: %+v", roles)
	}
}

// TestAssignRole_RechazaAQuienNoEsMiembro: el user_id también viene del cuerpo.
func TestAssignRole_RechazaAQuienNoEsMiembro(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "propio"}, nil)
	ajeno := f.seedMemberOf(t, testTenantB) // miembro de OTRA empresa

	err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: ajeno, RoleID: role.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, quiero ErrNotFound", err)
	}
}

// TestUnassignRole_NoTocaLaAsignacionDeOtraEmpresa: retirar en A no puede
// llevarse por delante la fila de B (la del rol global compartido).
func TestUnassignRole_NoTocaLaAsignacionDeOtraEmpresa(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	global := f.store.Roles.Seed(domain.Role{Name: "plantilla-compartida"}, nil)
	userA := f.seedMemberOf(t, testTenant)
	userB := f.seedMemberOf(t, testTenantB)

	if err := f.svc.AssignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userA, RoleID: global.ID}); err != nil {
		t.Fatalf("AssignRole A: %v", err)
	}
	if err := f.svc.AssignRole(ctxOf(testTenantB), in.RoleAssignmentInput{UserID: userB, RoleID: global.ID}); err != nil {
		t.Fatalf("AssignRole B: %v", err)
	}

	if err := f.svc.UnassignRole(ctxOf(testTenant), in.RoleAssignmentInput{UserID: userA, RoleID: global.ID}); err != nil {
		t.Fatalf("UnassignRole: %v", err)
	}

	quedanA := f.rolesDe(t, userA, testTenant)
	if len(quedanA) != 0 {
		t.Errorf("la asignación de A debía irse: %+v", quedanA)
	}
	quedanB := f.rolesDe(t, userB, testTenantB)
	if len(quedanB) != 1 {
		t.Errorf("la asignación de B no se tocaba: %+v", quedanB)
	}
}

// TestGrantToRole_RechazaLaPlantillaGlobal: editar sus grants cambiaría los
// permisos de todas las empresas a la vez.
func TestGrantToRole_RechazaLaPlantillaGlobal(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	global := f.store.Roles.Seed(domain.Role{Name: "plantilla-global"}, nil)

	err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{
		RoleID: global.ID,
		Grant:  domain.Grant{Pattern: "tenants.*", Effect: domain.EffectAllow},
	})
	if !errors.Is(err, domain.ErrGlobalRoleImmutable) {
		t.Fatalf("err = %v, quiero ErrGlobalRoleImmutable", err)
	}
	gs := f.grantsDelRol(t, global.ID)
	if len(gs) != 0 {
		t.Fatalf("no debía escribirse el grant en la plantilla: %+v", gs)
	}
}

// TestGrantToRole_RechazaElRolDeOtraEmpresa.
func TestGrantToRole_RechazaElRolDeOtraEmpresa(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	ajeno := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenantB), Name: "ajeno"}, nil)

	err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{
		RoleID: ajeno.ID,
		Grant:  domain.Grant{Pattern: "flows.*", Effect: domain.EffectAllow},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, quiero ErrNotFound", err)
	}
	gs := f.grantsDelRol(t, ajeno.ID)
	if len(gs) != 0 {
		t.Fatalf("no debía escribirse nada en el rol ajeno: %+v", gs)
	}
}

// TestGrantToRole_EscribeEnElRolPropio: el camino feliz, para que los rechazos
// de arriba signifiquen algo (si nada escribiera nunca, todos pasarían).
func TestGrantToRole_EscribeEnElRolPropio(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	propio := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "propio"}, nil)
	g := domain.Grant{Pattern: "flows.read", Effect: domain.EffectAllow}

	if err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: propio.ID, Grant: g}); err != nil {
		t.Fatalf("GrantToRole: %v", err)
	}
	gs := f.grantsDelRol(t, propio.ID)
	if len(gs) != 1 || gs[0] != g {
		t.Fatalf("grants = %+v, quiero [%+v]", gs, g)
	}

	if err := f.svc.RevokeFromRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: propio.ID, Grant: g}); err != nil {
		t.Fatalf("RevokeFromRole: %v", err)
	}
	if gs := f.grantsDelRol(t, propio.ID); len(gs) != 0 {
		t.Fatalf("tras revocar deberían quedar 0: %+v", gs)
	}
}

// TestUserGrants_SoloSobreMiembrosDelTenant: iam_user_grants no tiene columna de
// tenant, así que lo único que acota el override es esta comprobación.
func TestUserGrants_SoloSobreMiembrosDelTenant(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	ajeno := f.seedMemberOf(t, testTenantB)
	g := domain.Grant{Pattern: "tenants.delete", Effect: domain.EffectAllow}

	err := f.svc.GrantToUser(ctxOf(testTenant), in.UserGrantInput{UserID: ajeno, Grant: g})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, quiero ErrNotFound", err)
	}
	if gs := f.overridesDe(t, ajeno); len(gs) != 0 {
		t.Fatalf("no debía escribirse el override a alguien de otra empresa: %+v", gs)
	}

	propio := f.seedMemberOf(t, testTenant)
	if err := f.svc.GrantToUser(ctxOf(testTenant), in.UserGrantInput{UserID: propio, Grant: g}); err != nil {
		t.Fatalf("GrantToUser (miembro propio): %v", err)
	}
	if gs := f.overridesDe(t, propio); len(gs) != 1 {
		t.Fatalf("el override sobre el miembro propio debía escribirse: %+v", gs)
	}
	if err := f.svc.RevokeFromUser(ctxOf(testTenant), in.UserGrantInput{UserID: propio, Grant: g}); err != nil {
		t.Fatalf("RevokeFromUser: %v", err)
	}
	if gs := f.overridesDe(t, propio); len(gs) != 0 {
		t.Fatalf("tras revocar deberían quedar 0: %+v", gs)
	}
}

// TestGrantInvalido_NoSeEscribe: el efecto vacío no vale como "allow".
func TestGrantInvalido_NoSeEscribe(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)

	propio := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "propio"}, nil)

	casos := []domain.Grant{
		{Pattern: "", Effect: domain.EffectAllow},
		{Pattern: "flows.read", Effect: ""},
		{Pattern: "flows.read", Effect: domain.Effect("permitir")},
	}
	for _, g := range casos {
		if err := f.svc.GrantToRole(ctxOf(testTenant), in.RoleGrantInput{RoleID: propio.ID, Grant: g}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("grant %+v: err = %v, quiero ErrInvalidInput", g, err)
		}
	}
	if gs := f.grantsDelRol(t, propio.ID); len(gs) != 0 {
		t.Fatalf("ningún grant inválido debía escribirse: %+v", gs)
	}
}

// TestSinTenantEnElContexto_NadaSeEjecuta cubre las DOS formas de no tener
// tenant: sin identidad (no pasó por el middleware) y con identidad SIN empresa,
// que desde D-056.12 es un Context Token perfectamente válido.
func TestSinTenantEnElContexto_NadaSeEjecuta(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	role := f.store.Roles.Seed(domain.Role{TenantID: ptr(testTenant), Name: "propio"}, nil)
	userID := f.seedMemberOf(t, testTenant)

	contextos := map[string]context.Context{
		"sin identidad": context.Background(),
		"sin empresa":   withCaller(context.Background(), in.Caller{UserID: "sujeto-sin-empresa"}),
		"empresa vacía": withCaller(context.Background(), in.Caller{TenantID: "", UserID: "x"}),
	}
	g := domain.Grant{Pattern: "flows.read", Effect: domain.EffectAllow}

	for nombre, ctx := range contextos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := f.svc.ListRoles(ctx); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("ListRoles: err = %v, quiero ErrNoTenant", err)
			}
			if _, err := f.svc.CreateRole(ctx, in.CreateRoleInput{Name: "x"}); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("CreateRole: err = %v, quiero ErrNoTenant", err)
			}
			asig := in.RoleAssignmentInput{UserID: userID, RoleID: role.ID}
			if err := f.svc.AssignRole(ctx, asig); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("AssignRole: err = %v, quiero ErrNoTenant", err)
			}
			if err := f.svc.UnassignRole(ctx, asig); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("UnassignRole: err = %v, quiero ErrNoTenant", err)
			}
			rg := in.RoleGrantInput{RoleID: role.ID, Grant: g}
			if err := f.svc.GrantToRole(ctx, rg); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("GrantToRole: err = %v, quiero ErrNoTenant", err)
			}
			if err := f.svc.RevokeFromRole(ctx, rg); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("RevokeFromRole: err = %v, quiero ErrNoTenant", err)
			}
			ug := in.UserGrantInput{UserID: userID, Grant: g}
			if err := f.svc.GrantToUser(ctx, ug); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("GrantToUser: err = %v, quiero ErrNoTenant", err)
			}
			if err := f.svc.RevokeFromUser(ctx, ug); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("RevokeFromUser: err = %v, quiero ErrNoTenant", err)
			}
		})
	}

	// Y nada de lo anterior escribió: ni rol nuevo, ni asignación, ni override.
	if roles := f.rolesVisibles(t, testTenant); len(roles) != 1 {
		t.Errorf("no debía crearse ningún rol: %+v", roles)
	}
	if asignados := f.rolesDe(t, userID, testTenant); len(asignados) != 0 {
		t.Errorf("no debía asignarse ningún rol: %+v", asignados)
	}
	if gs := f.overridesDe(t, userID); len(gs) != 0 {
		t.Errorf("no debía escribirse ningún override: %+v", gs)
	}
}

// TestNewRoleService_RechazaDepsNil: fail-fast en el arranque, no un nil
// dereference en el primer request.
func TestNewRoleService_RechazaDepsNil(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()

	if _, err := usecase.NewRoleService(nil, store.Roles, store.Grants, store.Memberships); err == nil {
		t.Error("sin CallerResolver no puede construirse: no habría de dónde sacar el tenant")
	}
	if _, err := usecase.NewRoleService(testResolver, nil, store.Grants, store.Memberships); err == nil {
		t.Error("sin RoleRepo no puede construirse")
	}
	if _, err := usecase.NewRoleService(testResolver, store.Roles, nil, store.Memberships); err == nil {
		t.Error("sin GrantRepo no puede construirse")
	}
	if _, err := usecase.NewRoleService(testResolver, store.Roles, store.Grants, nil); err == nil {
		t.Error("sin MembershipRepo no puede construirse")
	}
}
