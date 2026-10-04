// Package entitlementshelpertest es la suite de contrato del puerto entitlements.Resolver y su
// doble en memoria, Fake (D-F2-4: el Fake vivía en el paquete de producción viejo). Ningún código
// de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, Seed, ContratoResolver y la tabla de casos.
//   - fake.go: Fake, el Resolver en memoria que usan los tests de los consumidores del puerto.
//
// La suite la corren las dos implementaciones del puerto: Fake en unitario (fake_test.go) y
// entitlements.Postgres en los procesos de F9, con el arnés de testcontainers.
//
// Nuevo: no tiene fichero viejo. Los casos salen de plan/F2-acceso/diseno.md §2 y de los tests
// viejos de internal/entitlements @ 9a77307 (TestFake_*, TestIntegration_*), leídos, no portados.
package entitlementshelpertest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/google/uuid"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso. Tiene que venir limpio
// —los tenants con plan NULL y sin overrides, sin fallo de infraestructura— porque los casos
// reutilizan las mismas claves: ContratoResolver llama a nuevo una vez por caso.
type Montaje struct {
	// Resolver es la implementación bajo prueba.
	Resolver entitlements.Resolver
	// TenantA y TenantB son dos tenants que EXISTEN, distintos y con forma de UUID (con Postgres,
	// filas de public.tenants: tenant_features los referencia por clave foránea). Nacen con plan
	// NULL y sin overrides.
	TenantA, TenantB string
	// MissingTenant es un UUID bien formado que NO es un tenant, distinto de los otros dos.
	MissingTenant string
	// TTL es el TTL con el que se construyó Resolver: CacheTTL tiene que devolverlo tal cual. Es
	// positivo.
	TTL time.Duration
	// Seed siembra el estado que el puerto no deja escribir.
	Seed Seed
}

// Seed escribe lo que el Resolver lee: el catálogo de planes, el plan de cada tenant y sus
// overrides, y sabe romper la infraestructura. Con Postgres son filas de public.plans,
// public.plan_features, public.tenants.plan_id y public.tenant_features; con el Fake, que guarda
// el resultado YA RESUELTO, el Seed de su test aplica la resolución al sembrar.
//
// La suite siembra TODO antes de la primera consulta del caso: la implementación Postgres cachea
// por TTL, y una siembra posterior podría no verse dentro de él. Cada método falla el test t si
// no puede sembrar.
type Seed interface {
	// AddPlanFeatures añade features al plan, creándolo si no existe; sin features, solo lo crea.
	// Añadir a un plan que ya existe (el 'basic' de las migraciones) conserva lo que ya tenía.
	AddPlanFeatures(t *testing.T, plan string, features ...string)
	// SetPlan fija el plan del tenant; "" deja el plan en NULL (que se resuelve como 'basic').
	SetPlan(t *testing.T, tenantID, plan string)
	// SetOverride escribe el override del tenant para la feature: enabled=true la activa y
	// enabled=false la apaga, con independencia del plan. Repetirlo reemplaza el anterior.
	SetOverride(t *testing.T, tenantID, feature string, enabled bool)
	// BreakInfra deja la infraestructura caída: desde ahí, toda consulta del Resolver falla.
	BreakInfra(t *testing.T)
}

// ContratoResolver ejecuta las promesas de entitlements.Resolver contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada: un caso que no aplica a una implementación es un defecto del puerto, no de la suite.
//
// Las claves de feature de la suite son propias (prefijo contract_), no las del catálogo: así los
// casos no dependen de qué traiga cada plan sembrado por las migraciones. La única excepción es
// 'basic', que la suite necesita para el plan NULL y al que solo AÑADE una clave.
//
// Lo que la suite NO afirma, a propósito: la caché (que dentro del TTL no se re-consulta, que un
// false también se cachea, que la lista devuelta es una copia). Es de entitlements.Postgres, no
// del puerto, y la prueba su test unitario con reloj inyectado.
func ContratoResolver(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("entitlementshelpertest.ContratoResolver: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		{"PlanFeature_Has", casePlanFeatureHas},                                   // sin override manda el plan
		{"NotInPlan_FalseWithoutError", caseNotInPlan},                            // «no la tiene» = false, nil
		{"OverrideEnabled_WinsOverPlan", caseOverrideEnabledWins},                 // ADR-0022, sentido 1
		{"OverrideDisabled_WinsOverPlan", caseOverrideDisabledWins},               // ADR-0022, sentido 2
		{"NullPlan_ResolvesAsBasic", caseNullPlanIsBasic},                         // plan NULL ⇒ basic
		{"ListEffective_SortedUniqueWithoutDisabled", caseListSorted},             // orden y apagadas
		{"EmptyPlan_EmptyListWithoutError", caseEmptyPlan},                        // vacío no es error
		{"MissingTenant_NoRightsWithoutError", caseMissingTenant},                 // ("", nil, nil)
		{"Override_StaysInItsTenant", caseOverrideIsolation},                      // INV-8
		{"InfraFailure_HasReturnsErrorAndFalse", caseInfraFailureHas},             // fail-closed
		{"InfraFailure_ListEffectiveReturnsError", caseInfraFailureListEffective}, // se propaga
		{"CacheTTL_IsTheRealTTL", caseCacheTTL},                                   // el TTL del objeto
		{"ConcurrentReads_Consistent", caseConcurrentReads},                       // seguro en paralelo
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Resolver == nil:
		t.Fatal("Montaje.Resolver es nil")
	case m.Seed == nil:
		t.Fatal("Montaje.Seed es nil: la suite necesita sembrar planes y overrides")
	case m.TTL <= 0:
		t.Fatalf("Montaje.TTL = %v; tiene que ser el TTL positivo con el que se construyó el Resolver", m.TTL)
	case m.TenantA == m.TenantB || m.TenantA == m.MissingTenant || m.TenantB == m.MissingTenant:
		t.Fatalf("Montaje: TenantA (%q), TenantB (%q) y MissingTenant (%q) deben ser distintos",
			m.TenantA, m.TenantB, m.MissingTenant)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB, m.MissingTenant} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
	}
}

// Las claves y planes de la suite.
const (
	featureA      = "contract_a"
	featureAB     = "contract_ab"
	featureAUnder = "contract_a_b"
	featureB      = "contract_b"
	featureC      = "contract_c"
	featureD      = "contract_d"
	featureAbsent = "contract_absent"
	featureBasic  = "contract_basic_only"
	planBasic     = "basic"
	planMain      = "contract_plan_main"
	planEmpty     = "contract_plan_empty"
)

func casePlanFeatureHas(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	requireHas(t, m, m.TenantA, featureA, true)
}

func caseNotInPlan(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	requireHas(t, m, m.TenantA, featureAbsent, false)
}

func caseOverrideEnabledWins(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	m.Seed.SetOverride(t, m.TenantA, featureB, true)
	requireHas(t, m, m.TenantA, featureB, true)
	requireList(t, m, m.TenantA, planMain, []string{featureA, featureB})
}

func caseOverrideDisabledWins(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA, featureB)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	m.Seed.SetOverride(t, m.TenantA, featureB, false)
	requireHas(t, m, m.TenantA, featureB, false)
	requireList(t, m, m.TenantA, planMain, []string{featureA})
}

// caseNullPlanIsBasic usa el 'basic' real (con Postgres trae las features de las migraciones), así
// que la lista solo se afirma ordenada y con la clave añadida, no completa.
func caseNullPlanIsBasic(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planBasic, featureBasic)
	m.Seed.SetPlan(t, m.TenantA, "")
	requireHas(t, m, m.TenantA, featureBasic, true)
	plan, features, err := m.Resolver.ListEffective(context.Background(), m.TenantA)
	if err != nil {
		t.Fatalf("ListEffective con plan NULL: error inesperado %v", err)
	}
	if plan != planBasic {
		t.Errorf("ListEffective con plan NULL: plan = %q, quería %q", plan, planBasic)
	}
	if !slices.Contains(features, featureBasic) {
		t.Errorf("ListEffective con plan NULL: %v no trae %q, que es del plan basic", features, featureBasic)
	}
	if !slices.IsSorted(features) {
		t.Errorf("ListEffective con plan NULL: %v no está en orden alfabético", features)
	}
}

// caseListSorted mezcla plan, override que activa, override que apaga y override redundante. Las
// claves contract_a_b y contract_ab fijan el orden por bytes ('_' < 'b'), que es el que promete el
// puerto: un ORDER BY con el collation del servidor podría ordenarlas al revés.
func caseListSorted(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureC, featureAB, featureA, featureD)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	m.Seed.SetOverride(t, m.TenantA, featureB, true)      // activa una que el plan no trae
	m.Seed.SetOverride(t, m.TenantA, featureAUnder, true) // ídem
	m.Seed.SetOverride(t, m.TenantA, featureA, true)      // redundante: no la duplica
	m.Seed.SetOverride(t, m.TenantA, featureD, false)     // apaga una del plan
	requireList(t, m, m.TenantA, planMain,
		[]string{featureA, featureAUnder, featureAB, featureB, featureC})
}

func caseEmptyPlan(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planEmpty)
	m.Seed.SetPlan(t, m.TenantA, planEmpty)
	requireList(t, m, m.TenantA, planEmpty, nil)
	requireHas(t, m, m.TenantA, featureA, false)
}

// caseMissingTenant siembra una clave en 'basic' para distinguir «no existe» de «plan NULL»: un
// tenant que no existe no hereda basic.
func caseMissingTenant(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planBasic, featureBasic)
	requireHas(t, m, m.MissingTenant, featureBasic, false)
	requireList(t, m, m.MissingTenant, "", nil)
}

func caseOverrideIsolation(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	m.Seed.SetPlan(t, m.TenantB, planMain)
	m.Seed.SetOverride(t, m.TenantA, featureA, false)
	m.Seed.SetOverride(t, m.TenantA, featureB, true)
	requireHas(t, m, m.TenantB, featureA, true)
	requireHas(t, m, m.TenantB, featureB, false)
	requireList(t, m, m.TenantB, planMain, []string{featureA})
	requireList(t, m, m.TenantA, planMain, []string{featureB})
}

func caseInfraFailureHas(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA)
	m.Seed.SetPlan(t, m.TenantA, planMain) // con la infraestructura sana, la tendría
	m.Seed.BreakInfra(t)
	has, err := m.Resolver.Has(context.Background(), m.TenantA, featureA)
	if err == nil {
		t.Fatal("Has con la infraestructura caída devolvió err = nil; el fallo debía propagarse")
	}
	if has {
		t.Error("Has con la infraestructura caída devolvió true: un error nunca concede la feature")
	}
}

func caseInfraFailureListEffective(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	m.Seed.BreakInfra(t)
	plan, features, err := m.Resolver.ListEffective(context.Background(), m.TenantA)
	if err == nil {
		t.Fatal("ListEffective con la infraestructura caída devolvió err = nil; el fallo debía propagarse")
	}
	if plan != "" || features != nil {
		t.Errorf("ListEffective con error = (%q, %v); quería (\"\", nil)", plan, features)
	}
}

func caseCacheTTL(t *testing.T, m Montaje) {
	if got := m.Resolver.CacheTTL(); got != m.TTL {
		t.Errorf("CacheTTL() = %v, quería %v: tiene que ser el TTL real del objeto", got, m.TTL)
	}
}

// caseConcurrentReads lee en paralelo un estado ya sembrado. Bajo -race, cualquier escritura sin
// proteger dentro del Resolver (una caché, por ejemplo) se ve aquí.
func caseConcurrentReads(t *testing.T, m Montaje) {
	m.Seed.AddPlanFeatures(t, planMain, featureA, featureB)
	m.Seed.SetPlan(t, m.TenantA, planMain)
	m.Seed.SetOverride(t, m.TenantA, featureB, false)
	const readers = 16
	var wg sync.WaitGroup
	errs := make(chan string, readers*2)
	for range readers {
		wg.Go(func() {
			ctx := context.Background()
			if has, err := m.Resolver.Has(ctx, m.TenantA, featureA); err != nil || !has {
				errs <- "Has(featureA) en paralelo no devolvió (true, nil)"
			}
			plan, features, err := m.Resolver.ListEffective(ctx, m.TenantA)
			if err != nil || plan != planMain || !slices.Equal(features, []string{featureA}) {
				errs <- "ListEffective en paralelo no devolvió (" + planMain + ", [" + featureA + "], nil)"
			}
		})
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// requireHas afirma que Has devuelve (want, nil).
func requireHas(t *testing.T, m Montaje, tenantID, feature string, want bool) {
	t.Helper()
	got, err := m.Resolver.Has(context.Background(), tenantID, feature)
	if err != nil {
		t.Fatalf("Has(%q, %q): error inesperado %v («no la tiene» es false, nil)", tenantID, feature, err)
	}
	if got != want {
		t.Errorf("Has(%q, %q) = %v, quería %v", tenantID, feature, got, want)
	}
}

// requireList afirma que ListEffective devuelve exactamente (wantPlan, want, nil). Una lista vacía
// se compara por longitud: nil y vacía valen lo mismo para el puerto.
func requireList(t *testing.T, m Montaje, tenantID, wantPlan string, want []string) {
	t.Helper()
	plan, features, err := m.Resolver.ListEffective(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ListEffective(%q): error inesperado %v", tenantID, err)
	}
	if plan != wantPlan {
		t.Errorf("ListEffective(%q): plan = %q, quería %q", tenantID, plan, wantPlan)
	}
	if len(features) != len(want) || (len(want) > 0 && !slices.Equal(features, want)) {
		t.Errorf("ListEffective(%q): features = %v, quería %v (orden alfabético, sin apagadas ni repetidas)",
			tenantID, features, want)
	}
}
