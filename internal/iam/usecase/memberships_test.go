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

// membershipFixture arma un MembershipService sobre el doble en memoria.
type membershipFixture struct {
	svc   *usecase.MembershipService
	store *memory.Store
}

func newMembershipFixture(t *testing.T) membershipFixture {
	t.Helper()
	store := memory.NewStore()
	svc, err := usecase.NewMembershipService(testResolver, store.Memberships)
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	return membershipFixture{svc: svc, store: store}
}

// tenantsDe devuelve las empresas de las que la persona es miembro, abortando
// con el error real si el store falla en vez de tragárselo con `_`.
func (f membershipFixture) tenantsDe(t *testing.T, userID string) []string {
	t.Helper()
	tenants, err := f.store.Memberships.TenantsOfUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("TenantsOfUser(%s): %v", userID, err)
	}
	return tenants
}

// TestAddMember_DaDeAltaEnLaEmpresaDelContexto: el alta usa el tenant del
// contexto y el llamante no tiene por dónde elegir otro (MembershipInput no
// lleva empresa).
func TestAddMember_DaDeAltaEnLaEmpresaDelContexto(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	userID := uuid.NewString()

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	tenants, err := f.store.Memberships.TenantsOfUser(context.Background(), userID)
	if err != nil || len(tenants) != 1 || tenants[0] != testTenant {
		t.Fatalf("membresías = %v err=%v, quiero [%s]", tenants, err, testTenant)
	}
}

// TestAddMember_EsIdempotente: repetir el alta no duplica ni falla.
func TestAddMember_EsIdempotente(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	userID := uuid.NewString()

	for range 2 {
		if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
	}
	tenants := f.tenantsDe(t, userID)
	if len(tenants) != 1 {
		t.Fatalf("membresías = %v, quiero una sola", tenants)
	}
}

// TestAddMember_UnaSegundaEmpresaEsConflicto es la guarda que sostiene el canje.
//
// No se rechaza por política de producto: con dos filas en tenant_members el
// canje devuelve domain.ErrMultipleTenants y esa persona deja de poder entrar
// (MD-055.2). Una segunda membresía no le añade una empresa — le rompe el login.
func TestAddMember_UnaSegundaEmpresaEsConflicto(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	userID := uuid.NewString()

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember (primera): %v", err)
	}
	err := f.svc.AddMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero ErrConflict", err)
	}

	tenants := f.tenantsDe(t, userID)
	if len(tenants) != 1 || tenants[0] != testTenant {
		t.Fatalf("la membresía original debía quedar intacta: %v", tenants)
	}
}

// TestRemoveMember_SoloDeSuPropiaEmpresa: pasar el UUID de alguien de otra no
// borra nada, porque el DELETE lleva el tenant del contexto.
func TestRemoveMember_SoloDeSuPropiaEmpresa(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	userID := uuid.NewString()

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Un administrador de OTRA empresa intenta darlo de baja.
	if err := f.svc.RemoveMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("RemoveMember (ajeno) debía ser no-op, no error: %v", err)
	}
	if tenants := f.tenantsDe(t, userID); len(tenants) != 1 {
		t.Fatalf("la membresía no era suya para borrarla: %v", tenants)
	}

	// Su propia empresa sí puede.
	if err := f.svc.RemoveMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("RemoveMember (propio): %v", err)
	}
	if tenants := f.tenantsDe(t, userID); len(tenants) != 0 {
		t.Fatalf("la baja debía dejarlo sin membresías: %v", tenants)
	}
}

// TestMembershipService_SinTenantEnElContexto.
func TestMembershipService_SinTenantEnElContexto(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	userID := uuid.NewString()

	for nombre, ctx := range map[string]context.Context{
		"sin identidad": context.Background(),
		"sin empresa":   withCaller(context.Background(), in.Caller{UserID: "sujeto-sin-empresa"}),
	} {
		t.Run(nombre, func(t *testing.T) {
			if err := f.svc.AddMember(ctx, in.MembershipInput{UserID: userID}); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("AddMember: err = %v, quiero ErrNoTenant", err)
			}
			if err := f.svc.RemoveMember(ctx, in.MembershipInput{UserID: userID}); !errors.Is(err, domain.ErrNoTenant) {
				t.Errorf("RemoveMember: err = %v, quiero ErrNoTenant", err)
			}
		})
	}
	if tenants := f.tenantsDe(t, userID); len(tenants) != 0 {
		t.Fatalf("no debía darse de alta a nadie: %v", tenants)
	}
}

// TestAddMember_UserIDVacio: el UUID llega del cuerpo y vacío no es una persona.
func TestAddMember_UserIDVacio(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, quiero ErrInvalidInput", err)
	}
	if err := f.svc.RemoveMember(ctxOf(testTenant), in.MembershipInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, quiero ErrInvalidInput", err)
	}
}

// TestNewMembershipService_RechazaDepsNil.
func TestNewMembershipService_RechazaDepsNil(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()

	if _, err := usecase.NewMembershipService(nil, store.Memberships); err == nil {
		t.Error("sin CallerResolver no puede construirse")
	}
	if _, err := usecase.NewMembershipService(testResolver, nil); err == nil {
		t.Error("sin MembershipRepo no puede construirse")
	}
}
