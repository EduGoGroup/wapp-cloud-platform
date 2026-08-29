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
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
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

// ---------------------------------------------------------------------------
// EL LISTADO QUE HACE POSIBLE EL SELECTOR (in.TenantLister)
// ---------------------------------------------------------------------------

// TestTenantsOfCaller_DevuelveSusEmpresasConNombreYLaActiva.
func TestTenantsOfCaller_DevuelveSusEmpresasConNombreYLaActiva(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.SeedTenantName(testTenant, "Panadería Doña Rosa")
	f.store.Memberships.SeedTenantName(testTenantB, "Catering del Sur")
	f.store.Memberships.Seed(userID, testTenant)
	f.store.Memberships.Seed(userID, testTenantB)
	if err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), testTenantB); err != nil {
		t.Fatalf("SelectActiveTenant: %v", err)
	}

	tenants, activeID, err := f.svc.TenantsOfCaller(ctxSinEmpresa(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("devolvió %d empresas, quiero 2: %+v", len(tenants), tenants)
	}
	if tenants[0].ID != testTenant || tenants[1].ID != testTenantB {
		t.Errorf("orden inesperado: %+v", tenants)
	}
	// El NOMBRE, que es el motivo entero de que este método exista: sin él la
	// consola pinta UUIDs.
	if tenants[0].DisplayName != "Panadería Doña Rosa" || tenants[1].DisplayName != "Catering del Sur" {
		t.Errorf("los nombres legibles no llegaron: %+v", tenants)
	}
	if activeID != testTenantB {
		t.Errorf("activeID = %q, quiero %q", activeID, testTenantB)
	}
}

// TestTenantsOfCaller_SinEmpresasEsListaVaciaYNoUnError es D-056.12 en el
// listado, y el caso que la consola necesita para distinguir «pantalla de
// espera» de «selector»: el Context Token de este usuario es IDÉNTICO al de
// alguien con dos empresas sin elegir.
//
// 🔴 La lista tiene que ser NO NULA además de vacía: el transporte la serializa
// tal cual, y un `null` no es iterable en el cliente.
func TestTenantsOfCaller_SinEmpresasEsListaVaciaYNoUnError(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)

	tenants, activeID, err := f.svc.TenantsOfCaller(ctxSinEmpresa(uuid.NewString()))
	if err != nil {
		t.Fatalf("err = %v: cero empresas es un estado legítimo, no un fallo", err)
	}
	if tenants == nil {
		t.Error("la lista vacía llegó como nil: se serializaría como `null`, que el cliente no puede recorrer")
	}
	if len(tenants) != 0 {
		t.Errorf("devolvió %+v, quiero vacío", tenants)
	}
	if activeID != "" {
		t.Errorf("activeID = %q, quiero vacío", activeID)
	}
}

// TestTenantsOfCaller_SoloLasSuyas es el requisito anti-oráculo: existen otras
// empresas —con nombre y todo— y ninguna asoma.
//
// Se siembran DOS empresas ajenas Y sus nombres a propósito: sin ellas, el test
// pasaría sobre un store donde no hay nada más que devolver, que es una pared.
func TestTenantsOfCaller_SoloLasSuyas(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID, otro := uuid.NewString(), uuid.NewString()
	f.store.Memberships.SeedTenantName(testTenant, "La suya")
	f.store.Memberships.SeedTenantName(testTenantB, "La AJENA")
	f.store.Memberships.Seed(userID, testTenant)
	// Otra persona en otra empresa: existe, tiene nombre, y no es asunto suyo.
	f.store.Memberships.Seed(otro, testTenantB)

	tenants, _, err := f.svc.TenantsOfCaller(ctxSinEmpresa(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if len(tenants) != 1 || tenants[0].ID != testTenant {
		t.Fatalf("devolvió %+v, quiero SOLO %q: se filtró una empresa ajena", tenants, testTenant)
	}
	// Guarda anti-hueco: comprobamos que la ajena EXISTE de verdad en el store, o
	// el aserto de arriba no probaría nada.
	if ajenas, _, aerr := f.svc.TenantsOfCaller(ctxSinEmpresa(otro)); aerr != nil || len(ajenas) != 1 {
		t.Fatalf("la empresa ajena no existía (%+v, err=%v): el test de arriba vigilaba una pared", ajenas, aerr)
	}
}

// TestTenantsOfCaller_ConUnaSolaEmpresaMarcaEsaAunqueLaGuardadaSeaOtra es la
// coherencia SELECTOR ↔ TOKEN, y es el caso donde se rompería si el listado
// leyera la fila guardada en vez de aplicar la regla del canje.
//
// 🔴 Con UNA membresía el canje devuelve ESA empresa e ignora `user_active_tenant`
// (una fila que apunte a otra parte es basura de una baja anterior). Si el
// listado devolviera la fila cruda, el selector no marcaría nada —o marcaría una
// empresa que ni sale en la lista— mientras el token SÍ va acotado. El síntoma
// sería «la consola no sabe dónde estoy» sin que nada falle.
func TestTenantsOfCaller_ConUnaSolaEmpresaMarcaEsaAunqueLaGuardadaSeaOtra(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	userID := uuid.NewString()
	f.store.Memberships.SeedTenantName(testTenant, "La única")
	f.store.Memberships.Seed(userID, testTenant)
	// Basura de una baja anterior: apunta a una empresa que ya no es suya.
	if err := f.store.ActiveTenants.SetActiveTenant(context.Background(), userID, testTenantB); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}

	tenants, activeID, err := f.svc.TenantsOfCaller(ctxSinEmpresa(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("devolvió %+v, quiero una sola", tenants)
	}
	if activeID != testTenant {
		t.Fatalf("activeID = %q, quiero %q: con UNA membresía manda la membresía, "+
			"que es lo que el canje hace — el selector no puede discrepar del token", activeID, testTenant)
	}
}

// TestTenantsOfCaller_LaGuardadaQueYaNoEsSuyaNoSeMarca: tres empresas, se elige
// una, se pierde la membresía de esa, quedan dos. La lista trae las dos y NINGUNA
// marcada — que es el mismo desenlace que da el canje (token sin empresa).
func TestTenantsOfCaller_LaGuardadaQueYaNoEsSuyaNoSeMarca(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	ctx := context.Background()
	userID := uuid.NewString()
	const tercera = "33333333-3333-3333-3333-333333333333"
	for _, tid := range []string{testTenant, testTenantB, tercera} {
		f.store.Memberships.SeedTenantName(tid, "Empresa "+tid[:1])
		f.store.Memberships.Seed(userID, tid)
	}
	if err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), tercera); err != nil {
		t.Fatalf("SelectActiveTenant: %v", err)
	}
	// La baja. La empresa activa NO se toca: sigue escrita, y sigue siendo inerte.
	if err := f.store.Memberships.Remove(ctx, userID, tercera); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	tenants, activeID, err := f.svc.TenantsOfCaller(ctxSinEmpresa(userID))
	if err != nil {
		t.Fatalf("TenantsOfCaller: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("devolvió %d empresas, quiero 2 (la tercera ya no es suya): %+v", len(tenants), tenants)
	}
	if activeID != "" {
		t.Fatalf("activeID = %q, quiero vacío: guardar una empresa activa NO concede nada", activeID)
	}
	// Y la que perdió NO puede seguir en la lista.
	for _, tn := range tenants {
		if tn.ID == tercera {
			t.Fatalf("la empresa de la que se le dio de baja sigue en su lista: %+v", tenants)
		}
	}
}

// TestTenantsOfCaller_SinIdentidadEsEntradaInvalida: 400 y no 401, mismo criterio
// que el resto del plano (a este método solo se llega detrás de Authenticate).
func TestTenantsOfCaller_SinIdentidadEsEntradaInvalida(t *testing.T) {
	t.Parallel()
	f := newActiveTenantFixture(t)
	if _, _, err := f.svc.TenantsOfCaller(context.Background()); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestTenantsOfCaller_ElSelectorYElCanjeNuncaDiscrepan es EL test de la
// factorización: para cada forma de estar (cero, una, varias-sin-elegir,
// varias-con-elegida, varias-con-elegida-caduca), lo que el listado MARCA y lo
// que el canje ACOTA tienen que ser el mismo valor.
//
// 🔴 No compara contra una constante escrita a mano: llama a las DOS piezas y
// compara sus salidas entre sí. Si alguien duplica la regla en uno de los dos
// lados, este test lo caza sin que haya que enumerar el desenlace correcto —que
// es justo lo que un test con valores esperados a mano dejaría envejecer.
func TestTenantsOfCaller_ElSelectorYElCanjeNuncaDiscrepan(t *testing.T) {
	t.Parallel()
	const tercera = "33333333-3333-3333-3333-333333333333"

	casos := []struct {
		nombre    string
		empresas  []string
		elegir    string
		darDeBaja string
	}{
		{"cero empresas", nil, "", ""},
		{"una empresa", []string{testTenant}, "", ""},
		{"una empresa con guardada ajena", []string{testTenant}, "", ""},
		{"varias sin elegir", []string{testTenant, testTenantB}, "", ""},
		{"varias con elegida", []string{testTenant, testTenantB}, testTenantB, ""},
		{"varias con elegida que ya no es suya", []string{testTenant, testTenantB, tercera}, tercera, tercera},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			f := newActiveTenantFixture(t)
			ctx := context.Background()
			userID := uuid.NewString()
			for _, tid := range c.empresas {
				f.store.Memberships.SeedTenantName(tid, "E-"+tid[:1])
				f.store.Memberships.Seed(userID, tid)
			}
			if c.elegir != "" {
				if err := f.svc.SelectActiveTenant(ctxSinEmpresa(userID), c.elegir); err != nil {
					t.Fatalf("SelectActiveTenant: %v", err)
				}
			}
			if c.darDeBaja != "" {
				if err := f.store.Memberships.Remove(ctx, userID, c.darDeBaja); err != nil {
					t.Fatalf("Remove: %v", err)
				}
			}

			_, delSelector, err := f.svc.TenantsOfCaller(ctxSinEmpresa(userID))
			if err != nil {
				t.Fatalf("TenantsOfCaller: %v", err)
			}
			// El canje REAL, sobre los MISMOS dobles: es la OTRA PIEZA, no una
			// reimplementación de su regla dentro del test. Y se lee del token
			// FIRMADO, que es lo que de verdad acota lo que esa persona puede
			// hacer — no del DTO que el usecase devuelve por comodidad.
			delCanje := tenantDelCanje(t, f.store, userID)

			if delSelector != delCanje {
				t.Fatalf("el selector marcaría %q y el canje acotaría a %q: "+
					"la consola diría que estás en una empresa distinta de la que tu token permite",
					delSelector, delCanje)
			}
		})
	}
}

// tenantDelCanje emite un Identity Token para userID, lo canjea con un
// ExchangeService REAL montado sobre los MISMOS dobles, y devuelve el tenant que
// viaja en el Context Token FIRMADO.
//
// Existe para que el test de coherencia compare DOS PIEZAS y no una pieza contra
// una constante escrita a mano. La constante envejece en silencio; la otra pieza,
// no.
func tenantDelCanje(t *testing.T, store *memory.Store, userID string) string {
	t.Helper()
	issuerMgr, verifier := newIdentityPair(t)
	contexts := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	canje := mustExchangeSvc(t, verifier, store, contexts)

	// Fixture PARCIAL solo para emitir el Identity Token, mismo patrón que
	// membership_integration_test.go.
	emisor := exchangeFixture{issuer: issuerMgr, contexts: contexts}
	token, _ := emisor.identityToken(t, userID, usecase.SystemWappBFF, 15*time.Minute)

	res, err := canje.Exchange(context.Background(), in.ExchangeInput{IdentityToken: token})
	if err != nil {
		t.Fatalf("Exchange: %v (ningún número de membresías puede ser un error)", err)
	}
	claims, err := contexts.ValidateToken(res.ContextToken)
	if err != nil {
		t.Fatalf("validando el context token: %v", err)
	}
	return claims.TenantID
}
