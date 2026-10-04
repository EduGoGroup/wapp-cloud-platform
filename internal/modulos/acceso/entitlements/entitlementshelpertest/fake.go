// Porta internal/entitlements/entitlements.go @ 9a77307 (líneas 203-296: el Fake, que pasa a
// este paquete por D-F2-4).

package entitlementshelpertest

import (
	"context"
	"slices"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
)

// Fake es Resolver: lo comprueba el compilador.
var _ entitlements.Resolver = (*Fake)(nil)

// defaultCacheTTL es el TTL que Fake.CacheTTL devuelve si nadie fija otro: el mismo valor por
// defecto que entitlements.Postgres (60 s). Se declara aquí porque el de Postgres no es exportado.
const defaultCacheTTL = 60 * time.Second

// Fake es un Resolver en memoria para tests: el conjunto de features habilitadas por tenant.
// Ausencia ⇒ false. Es seguro para lectura concurrente si no se muta tras construirlo.
//
// El mapa modela el resultado YA RESUELTO (plan ∪/∖ overrides), no las dos tablas: Enable deja la
// feature encendida y Disable la deja apagada explícitamente, que es lo que un override
// `enabled=false` produce en la BD. Por eso una feature con valor false NO aparece en
// ListEffective.
//
// ⚠️ Un tenant que el Fake no conoce se lista con plan "basic" (el criterio del plan NULL), no con
// el plan "" de un tenant inexistente que da entitlements.Postgres: el Fake no distingue «no
// existe» de «sin plan». Quien necesite el inexistente lo declara con SetPlan(tenant, "").
type Fake struct {
	// Enabled mapea tenantID → feature → encendida.
	Enabled map[string]map[string]bool
	// Plans mapea tenantID → plan efectivo que devuelve ListEffective. Ausencia ⇒ "basic" (mismo
	// criterio que la resolución real ante plan_id NULL).
	Plans map[string]string
	// TTL es el valor que devuelve CacheTTL. Cero o negativo ⇒ defaultCacheTTL (60 s), el mismo
	// que sirve la implementación Postgres.
	TTL time.Duration
	// Err, si no es nil, se devuelve en cada Has/ListEffective (simula fallo de infraestructura).
	Err error
}

// NewFake construye un Fake vacío listo para poblar. El valor cero, &Fake{}, también sirve.
func NewFake() *Fake {
	return &Fake{Enabled: make(map[string]map[string]bool)}
}

// Enable marca una feature como habilitada para un tenant.
func (f *Fake) Enable(tenantID, feature string) {
	f.set(tenantID, feature, true)
}

// Disable marca una feature como APAGADA para un tenant: modela el override `enabled=false`, que
// gana sobre el plan (ADR-0022). Distinto de no declararla: aquí queda registrada y
// explícitamente en false.
func (f *Fake) Disable(tenantID, feature string) {
	f.set(tenantID, feature, false)
}

// SetPlan fija el plan efectivo que ListEffective reporta para un tenant.
func (f *Fake) SetPlan(tenantID, plan string) {
	if f.Plans == nil {
		f.Plans = make(map[string]string)
	}
	f.Plans[tenantID] = plan
}

// set registra el valor de la feature, creando los mapas que falten (el valor cero de Fake no los
// trae).
func (f *Fake) set(tenantID, feature string, enabled bool) {
	if f.Enabled == nil {
		f.Enabled = make(map[string]map[string]bool)
	}
	set := f.Enabled[tenantID]
	if set == nil {
		set = make(map[string]bool)
		f.Enabled[tenantID] = set
	}
	set[feature] = enabled
}

// Has implementa Resolver sobre el mapa en memoria: con Err, (false, Err); si no, el valor
// registrado (ausencia ⇒ false).
func (f *Fake) Has(_ context.Context, tenantID, feature string) (bool, error) {
	if f.Err != nil {
		return false, f.Err
	}
	return f.Enabled[tenantID][feature], nil
}

// ListEffective implementa Resolver sobre el mapa en memoria: con Err, ("", nil, Err); si no, el
// plan del tenant (o "basic") y sus features ENCENDIDAS en orden alfabético.
func (f *Fake) ListEffective(_ context.Context, tenantID string) (string, []string, error) {
	if f.Err != nil {
		return "", nil, f.Err
	}
	plan, ok := f.Plans[tenantID]
	if !ok {
		plan = "basic"
	}
	features := make([]string, 0, len(f.Enabled[tenantID]))
	for feature, enabled := range f.Enabled[tenantID] {
		if enabled {
			features = append(features, feature)
		}
	}
	slices.Sort(features)
	return plan, features, nil
}

// CacheTTL implementa Resolver: el TTL configurado o, si no es positivo, defaultCacheTTL.
func (f *Fake) CacheTTL() time.Duration {
	if f.TTL > 0 {
		return f.TTL
	}
	return defaultCacheTTL
}
