package usecase_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// membershipFixture arma un MembershipService sobre el doble en memoria.
//
// Desde la Ola B lleva dos piezas más, y ninguna es decorado: `identity` es el
// doble de la acreditación —sin él AddMember no llega a escribir— y `altas`
// envuelve el repositorio para CONTAR las llamadas a Add, que es lo que permite
// distinguir «no se escribió» de «se escribió y falló».
type membershipFixture struct {
	svc      *usecase.MembershipService
	store    *memory.Store
	identity *identidadDeMentira
	altas    *repoQueCuentaAltas
}

func newMembershipFixture(t *testing.T) membershipFixture {
	t.Helper()
	store := memory.NewStore()
	identity := &identidadDeMentira{}
	altas := &repoQueCuentaAltas{MembershipRepo: store.Memberships}
	svc, err := usecase.NewMembershipService(testResolver, altas, identity, quietLogger())
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	return membershipFixture{svc: svc, store: store, identity: identity, altas: altas}
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

// TestAddMember_UnaSegundaEmpresaEsConflicto — el caso SIN la feature, que es el
// de la inmensa mayoría de los tenants y el que NO puede cambiar.
//
// 🔧 Su porqué cambió dos veces en el mismo día y conviene leerlo entero:
// hasta el 2026-08-29 el 409 se defendía diciendo que dos filas en
// tenant_members rompían el canje; T5.1 lo desmintió (el canje resuelve por la
// empresa activa, D-047.14); y T5.2 le da su forma definitiva — la segunda
// empresa es una CAPACIDAD COMERCIAL (`multi_empresa`), así que quien no la paga
// sigue viendo exactamente este 409, con este sentinela y este cuerpo. El
// fixture no ata ningún resolver, que es el fail-closed llevado al extremo:
// sin resolver no hay derecho.
//
// 🔴 Su hermano positivo está justo debajo y no es opcional: sin él, este test
// lo pasaría también una guarda que rechazara SIEMPRE, que es la que había.
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

// TestAddMember_ConMultiEmpresaLaSegundaEmpresaSeEscribe es el HERMANO POSITIVO
// del test de arriba y la mitad nueva de T5.2 (Plan 047 · Ola 5): el tenant de
// DESTINO tiene `multi_empresa`, así que el alta de quien ya es miembro de otra
// empresa escribe y devuelve el mismo desenlace que un alta normal (nil ⇒ 204).
//
// 🔴 LA FEATURE SE ENCIENDE EN testTenantB Y NO EN testTenant, y es el punto
// entero del test: se pregunta por la empresa que RECIBE. Si la implementación
// preguntara por la de origen, este test se pondría rojo — y esa es exactamente
// la confusión que hay que impedir, porque las dos versiones «funcionan» en un
// escenario donde ambas empresas tienen el mismo plan.
func TestAddMember_ConMultiEmpresaLaSegundaEmpresaSeEscribe(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	feats := entitlements.NewFake()
	feats.Enable(testTenantB, entitlements.FeatureMultiEmpresa)
	f.store.Memberships.ConFeatures(feats)
	userID := uuid.NewString()

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember (primera): %v", err)
	}
	if err := f.svc.AddMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember (segunda, con multi_empresa): %v", err)
	}

	tenants := f.tenantsDe(t, userID)
	if len(tenants) != 2 {
		t.Fatalf("quiero DOS membresías, tengo %v", tenants)
	}
	if !slices.Contains(tenants, testTenant) || !slices.Contains(tenants, testTenantB) {
		t.Fatalf("las dos empresas tienen que estar: %v", tenants)
	}
}

// TestAddMember_ElResolverCaidoNoAbreLaSegundaEmpresa es el FAIL-CLOSED de T5.2,
// y aquí la política se lee al revés que en un gate normal: «si el derecho no se
// puede resolver, no se concede» significa MANTENER EL RECHAZO.
//
// Sin este test, la implementación más natural —`has, err := …; if err != nil {
// return err }`— parecería correcta, y no lo es del todo: convertiría un fallo
// transitorio de la base en un 500 donde el usuario esperaba un 409, y peor, la
// variante `if err == nil && has` mal escrita (`err != nil || has`) abriría la
// capacidad de pago con la base caída. El aserto mira las DOS cosas: que el
// error sea el conflicto de siempre y que NO se haya escrito nada.
func TestAddMember_ElResolverCaidoNoAbreLaSegundaEmpresa(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	feats := entitlements.NewFake()
	feats.Enable(testTenantB, entitlements.FeatureMultiEmpresa) // la TIENE...
	feats.Err = errors.New("la base de entitlements no contesta")
	f.store.Memberships.ConFeatures(feats) // ...pero no se puede acreditar.
	userID := uuid.NewString()

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember (primera): %v", err)
	}
	err := f.svc.AddMember(ctxOf(testTenantB), in.MembershipInput{UserID: userID})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero ErrConflict (fail-closed: un resolver caído no concede)", err)
	}
	if tenants := f.tenantsDe(t, userID); len(tenants) != 1 {
		t.Fatalf("no se podía escribir nada: %v", tenants)
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

	if _, err := usecase.NewMembershipService(nil, store.Memberships, &identidadDeMentira{}, nil); err == nil {
		t.Error("sin CallerResolver no puede construirse")
	}
	if _, err := usecase.NewMembershipService(testResolver, nil, &identidadDeMentira{}, nil); err == nil {
		t.Error("sin MembershipRepo no puede construirse")
	}
	// Y la asimetría deliberada: SIN cliente de identity SÍ se construye. Un
	// despliegue sin WAPP_IDENTITY_API_KEY es legítimo y su listado de miembros
	// tiene que seguir sirviendo; lo que no puede es dar de alta a medias, y de
	// eso responde AddMember con ErrIdentityNotConfigured (ver
	// TestAddMember_SinClienteM2MNoSeEscribeNada).
	if _, err := usecase.NewMembershipService(testResolver, store.Memberships, nil, nil); err != nil {
		t.Errorf("sin cliente de identity el servicio debe construirse igual: %v", err)
	}
}
