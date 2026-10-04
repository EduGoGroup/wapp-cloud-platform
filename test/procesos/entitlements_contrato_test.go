//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
)

// Este fichero corre la suite de contrato de entitlements.Resolver
// (entitlementshelpertest.ContratoResolver) contra entitlements.Postgres sobre una base clonada de
// la plantilla migrada (T2.4, F2-03). Como contact_contrato_test.go, no es un proceso de caja
// negra: es la excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite y el
// paquete del adaptador, del que solo usa el constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad las reglas de resolución que el Fake solo imita (hallazgo 15 de
// F2): el override en los dos sentidos, plan NULL ⇒ basic, el anti-join de los overrides que
// apagan, el orden por bytes y el tenant inexistente.

const (
	// entitlementsDefaultTTL es el TTL con el que se construye el Postgres: el de por defecto de
	// NewPostgres (60 s), porque el candado ProcessImports no deja usar entitlements.WithTTL desde
	// aquí (del paquete del puerto solo se usan sus New…). CacheTTL tiene que devolver este valor
	// y, como la suite siembra todo antes de la primera consulta de cada caso, ningún caso espera a
	// que la caché venza.
	entitlementsDefaultTTL = 60 * time.Second

	// entitlementsTimeout acota cada sentencia de siembra: la base es local al contenedor.
	entitlementsTimeout = 15 * time.Second
)

// entitlementsCases cuenta los Montajes pedidos en esta corrida: ContratoResolver llama a nuevo
// una vez por caso (en serie) y cada uno necesita una base con nombre propio.
var entitlementsCases atomic.Int64

// TestEntitlementsContrato_Postgres corre las promesas del puerto entitlements.Resolver, las mismas
// que pasan contra entitlementshelpertest.Fake, contra el adaptador Postgres. Cada caso recibe su
// base, sus dos tenants (filas reales de public.tenants, con plan NULL y sin overrides:
// tenant_features los referencia por clave foránea) y un Postgres nuevo, con las cachés vacías.
func TestEntitlementsContrato_Postgres(t *testing.T) {
	entitlementshelpertest.ContratoResolver(t, newEntitlementsMontaje)
}

// newEntitlementsMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase (que
// la borra en el Cleanup del subtest), la abre con el arnés, siembra los dos tenants por SQL y
// construye el Postgres sobre ese mismo *sql.DB, que comparte con el Seed.
func newEntitlementsMontaje(t *testing.T) entitlementshelpertest.Montaje {
	t.Helper()
	proceso := fmt.Sprintf("entitlements_contrato_%02d", entitlementsCases.Add(1))
	db := nuevaBase(t, proceso).Abrir(t)

	return entitlementshelpertest.Montaje{
		Resolver:      entitlements.NewPostgres(db),
		TenantA:       seedEntitlementsTenant(t, db, "derechos-a"),
		TenantB:       seedEntitlementsTenant(t, db, "derechos-b"),
		MissingTenant: uuid.NewString(),
		TTL:           entitlementsDefaultTTL,
		Seed:          &entitlementsSeed{db: db},
	}
}

// seedEntitlementsTenant inserta un tenant con el slug dado en public.tenants y devuelve su id. Solo
// rellena las columnas NOT NULL sin valor por defecto (0001_tenants.sql); plan_id queda NULL.
// Falla el test si no puede.
func seedEntitlementsTenant(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), entitlementsTimeout)
	defer cancel()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		slug, "Tenant "+slug,
	).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar el tenant %q: %v", slug, err)
	}
	return id
}

// entitlementsSeed es el entitlementshelpertest.Seed de Postgres: escribe por SQL en las tablas de
// la migración 0032 (public.plans, public.plan_features, public.tenants.plan_id y
// public.tenant_features) de la misma base que lee el Postgres. Todo es aditivo o idempotente: el
// 'basic' y los demás planes que siembran las migraciones (0032, 0039, 0053, 0074) conservan sus
// features.
type entitlementsSeed struct {
	db *sql.DB
}

var _ entitlementshelpertest.Seed = (*entitlementsSeed)(nil)

// exec ejecuta una sentencia de siembra, falla el test con what si no puede y devuelve cuántas
// filas tocó.
func (s *entitlementsSeed) exec(t *testing.T, what, query string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), entitlementsTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("entitlementsSeed.%s: %v", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("entitlementsSeed.%s: filas afectadas: %v", what, err)
	}
	return n
}

// AddPlanFeatures crea el plan si no existe (su nombre legible es el id) y le añade las features;
// lo que ya tuviera se queda.
func (s *entitlementsSeed) AddPlanFeatures(t *testing.T, plan string, features ...string) {
	t.Helper()
	s.exec(t, fmt.Sprintf("AddPlanFeatures(%q): crear el plan", plan),
		`INSERT INTO public.plans (id, name) VALUES ($1, $1) ON CONFLICT (id) DO NOTHING`, plan)
	for _, feature := range features {
		s.exec(t, fmt.Sprintf("AddPlanFeatures(%q, %q)", plan, feature),
			`INSERT INTO public.plan_features (plan_id, feature) VALUES ($1, $2) ON CONFLICT (plan_id, feature) DO NOTHING`,
			plan, feature)
	}
}

// SetPlan fija public.tenants.plan_id; "" lo deja en NULL. El plan tiene que existir (clave
// foránea): la suite lo crea antes con AddPlanFeatures. Falla el test si el tenant no existe.
func (s *entitlementsSeed) SetPlan(t *testing.T, tenantID, plan string) {
	t.Helper()
	what := fmt.Sprintf("SetPlan(%q, %q)", tenantID, plan)
	if n := s.exec(t, what,
		`UPDATE public.tenants SET plan_id = NULLIF($2, '') WHERE id = $1::uuid`, tenantID, plan); n != 1 {
		t.Fatalf("entitlementsSeed.%s: tocó %d filas de public.tenants; quería 1", what, n)
	}
}

// SetOverride escribe la fila de public.tenant_features del par; si ya había una, la reemplaza.
func (s *entitlementsSeed) SetOverride(t *testing.T, tenantID, feature string, enabled bool) {
	t.Helper()
	s.exec(t, fmt.Sprintf("SetOverride(%q, %q, %v)", tenantID, feature, enabled),
		`INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (tenant_id, feature) DO UPDATE SET enabled = EXCLUDED.enabled`,
		tenantID, feature, enabled)
}

// BreakInfra cierra el *sql.DB que comparten el Postgres y este Seed: desde ahí toda consulta falla
// con «sql: database is closed». El Close del Cleanup de Abrir lo repite sin error (es idempotente).
func (s *entitlementsSeed) BreakInfra(t *testing.T) {
	t.Helper()
	if err := s.db.Close(); err != nil {
		t.Fatalf("entitlementsSeed.BreakInfra: cerrar el *sql.DB: %v", err)
	}
}
