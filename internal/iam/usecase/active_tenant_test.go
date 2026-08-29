package usecase_test

// active_tenant_test.go — LA ELECCIÓN DE EMPRESA (Plan 047 · Ola 5 · T5.1).
//
// Lo que estos tests fijan, y conviene decirlo junto: elegir una empresa exige
// SER MIEMBRO de ella en ese momento, y no serlo se contesta como si la empresa
// no existiera. La otra mitad —que la elección guardada NO concede nada después—
// no se prueba aquí sino en exchange_test.go, donde está el lector: probarla aquí
// sería probar la escritura contra sí misma.

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

// activeTenantFixture arma el servicio sobre los dobles en memoria.
type activeTenantFixture struct {
	svc   *usecase.ActiveTenantService
	store *memory.Store
}

func newActiveTenantFixture(t *testing.T) activeTenantFixture {
	t.Helper()
	store := memory.NewStore()
	svc, err := usecase.NewActiveTenantService(testResolver, store.Memberships, store.ActiveTenants)
	if err != nil {
		t.Fatalf("NewActiveTenantService: %v", err)
	}
	return activeTenantFixture{svc: svc, store: store}
}

// ctxSinEmpresa devuelve el contexto de quien llega a este endpoint EN LA VIDA
// REAL: acreditado, y con el tenant VACÍO.
//
// 🔴 No es una comodidad del test: es el caso normal. Quien tiene dos membresías
// y ninguna elegida recibe hoy un Context Token sin empresa y sin un solo grant,
// así que si el usecase exigiera `Caller.TenantID != ""` —como hacen los demás de
// este paquete, y hacen bien— rechazaría a todo el mundo para quien existe.
func ctxSinEmpresa(userID string) context.Context {
	return withCaller(context.Background(), in.Caller{UserID: userID})
}

// TestSelectActiveTenant_ElMiembroElige es el camino feliz, y comprueba lo que se
// GUARDÓ, no lo que devolvió: un método que devolviera nil sin escribir nada
// pasaría cualquier test que solo mirara el error.
func TestSelectActiveTenant_ElMiembroElige(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)

	if err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), testTenantB); err != nil {
		t.Fatalf("SelectActiveTenant: %v (el token de quien elige NO trae empresa, y eso es lo normal)", err)
	}
	activo, ok, err := f.store.ActiveTenants.ActiveTenantOf(context.Background(), userID)
	if err != nil || !ok {
		t.Fatalf("no se guardó nada (ok=%v, err=%v)", ok, err)
	}
	if activo != testTenantB {
		t.Fatalf("empresa activa = %q, want %q", activo, testTenantB)
	}
}

// TestSelectActiveTenant_ElReemplazoNoAcumula: elegir otra empresa SUSTITUYE la
// elección anterior. Es la semántica de la PK por user_id de la tabla, y si el
// doble o el adaptador acumularan, el canje volvería a tener que elegir entre dos
// —que es el problema que esta tabla resuelve—.
func TestSelectActiveTenant_ElReemplazoNoAcumula(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)
	ctx := ctxSinEmpresa(userID)

	if err := f.svc.SelectActiveTenant(ctx, testTenant); err != nil {
		t.Fatalf("primera elección: %v", err)
	}
	if err := f.svc.SelectActiveTenant(ctx, testTenantB); err != nil {
		t.Fatalf("segunda elección: %v", err)
	}
	activo, _, err := f.store.ActiveTenants.ActiveTenantOf(context.Background(), userID)
	if err != nil {
		t.Fatalf("ActiveTenantOf: %v", err)
	}
	if activo != testTenantB {
		t.Fatalf("empresa activa = %q, want %q: la segunda elección no reemplazó a la primera", activo, testTenantB)
	}
}

// TestSelectActiveTenant_LaEmpresaAjenaEsUnNoEncontrado es el requisito
// anti-oráculo: quien NO es miembro no puede distinguir «no eres de ahí» de «esa
// empresa no existe». Las dos ramas se comprueban JUNTAS, porque un 404 solo en
// una de las dos seguiría siendo un oráculo.
//
// 🔴 Y se comprueba además que NO SE ESCRIBIÓ NADA: un servicio que rechazara con
// el error correcto pero guardara igual dejaría una preferencia hacia una empresa
// ajena — inerte hoy (el canje la descartaría), pero armada para el día que
// alguien le dé de alta ahí por otra vía.
func TestSelectActiveTenant_LaEmpresaAjenaEsUnNoEncontrado(t *testing.T) {
	t.Parallel()
	ajena := uuid.NewString() // una empresa que NO existe en ninguna parte

	casos := []struct {
		nombre string
		pedida string
	}{
		{"empresa que existe pero no es suya", testTenantB},
		{"empresa que no existe", ajena},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			f := newActiveTenantFixture(t)
			userID := uuid.NewString()
			f.store.Memberships.Seed(userID, testTenant) // solo A

			err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), c.pedida)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound (un 403 confirmaría que esa empresa existe)", err)
			}
			if _, ok, aerr := f.store.ActiveTenants.ActiveTenantOf(context.Background(), userID); aerr != nil || ok {
				t.Fatalf("se guardó una empresa que no es suya (ok=%v, err=%v)", ok, aerr)
			}
		})
	}
}

// TestSelectActiveTenant_LaEntradaInvalida cubre las dos formas de llegar sin lo
// mínimo. Las dos son 400 y no 401: a este método solo se llega DETRÁS de
// Authenticate, así que un contexto sin identidad es un fallo de cableado del
// servidor, no una credencial que falte.
func TestSelectActiveTenant_LaEntradaInvalida(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)

	if err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("sin tenant: err = %v, want ErrInvalidInput", err)
	}
	if err := f.svc.SelectActiveTenant(context.Background(), testTenant); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("contexto sin identidad: err = %v, want ErrInvalidInput", err)
	}
}

// TestSelectActiveTenant_LaUnicaEmpresaTambienSeElige: alguien con UNA sola
// membresía puede fijarla. No es un caso raro —es lo que hace la consola de quien
// acaba de perder su segunda empresa— y el desenlace tiene que ser normal, no un
// error por «no hay nada que elegir».
func TestSelectActiveTenant_LaUnicaEmpresaTambienSeElige(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.Seed(userID, testTenant)

	if err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), testTenant); err != nil {
		t.Fatalf("SelectActiveTenant: %v", err)
	}
}

// TestNewActiveTenantService_ExigeSusDependencias: las tres son estructurales y
// un nil es error de cableado, no un modo degradado.
func TestNewActiveTenantService_ExigeSusDependencias(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()

	if _, err := usecase.NewActiveTenantService(nil, store.Memberships, store.ActiveTenants); err == nil {
		t.Error("sin CallerResolver debería fallar: no se sabría a nombre de quién guardar")
	}
	if _, err := usecase.NewActiveTenantService(testResolver, nil, store.ActiveTenants); err == nil {
		t.Error("sin MembershipRepo debería fallar: sin él no se puede comprobar nada y cualquiera elegiría cualquier empresa")
	}
	if _, err := usecase.NewActiveTenantService(testResolver, store.Memberships, nil); err == nil {
		t.Error("sin ActiveTenantRepo debería fallar")
	}
}
