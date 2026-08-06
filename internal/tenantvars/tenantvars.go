// Package tenantvars persiste las VARIABLES DE EMPRESA de un tenant: pares
// clave→valor de texto que el tenant define y que **wApp no interpreta** (Plan
// 041 · T2.1, design.md §D-041.1).
//
// Qué significa "no interpreta", en concreto: aquí no hay lista blanca de claves,
// ni tipos de valor, ni validación semántica de ninguna clase. `moneda` no se
// comprueba contra ISO-4217; `envio_gratis_desde` no se parsea como número. Un
// valor con acentos, con espacios o con un JSON dentro de la cadena se guarda y se
// devuelve VERBATIM. De ahí viene la decisión de raíz que sostiene esta tabla:
// **no hay moneda tipada en wApp** —ni columna `currency`, ni campo en el catálogo,
// ni en la API— porque un monto es un monto; si el tenant quiere enseñar `Bs` en
// sus menús, eso es presentación desde SU variable.
//
// Quién la usa y quién NO: la consumen el CRUD público
// (GET/PUT /api/v1/tenant-variables) y la pantalla de gestión del BFF; el Plan 042
// congelará estas variables en el snapshot de cada `intake.push` (D-18). El Motor
// de Flujos y el módulo cart NO las leen: si algún día un módulo empieza a
// interpretar una clave, se rompió D-041.1.
//
// Es CAPA TÉCNICA (ADR-0035), no una capacidad comercial: las rutas llevan scope
// pero NO gate de feature.
//
// Todo va acotado al tenant (INV-8): el tenant sale del token, jamás del cuerpo.
package tenantvars

import (
	"context"
	"time"
)

// Variable es una variable de empresa: su clave, su valor y cuándo CAMBIÓ por
// última vez. Ambos campos son texto opaco para wApp.
type Variable struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// Store es el puerto de persistencia de las variables de un tenant. Lo satisfacen
// *Postgres (producción) y *MemoryStore (tests). Las dos implementaciones
// comparten semántica —incluido el REEMPLAZO TOTAL de Replace y el updated_at que
// solo se mueve cuando el valor cambia de verdad— para que un test contra la de
// memoria diga algo verdadero sobre producción.
type Store interface {
	// List devuelve las variables del tenant ordenadas por clave (vacío si no
	// tiene ninguna: la ausencia de variables NO es un error).
	List(ctx context.Context, tenantID string) ([]Variable, error)
	// Replace deja el conjunto de variables del tenant EXACTAMENTE igual a vars:
	// altas, cambios y BORRADO de las que no vengan. Es atómico y idempotente.
	Replace(ctx context.Context, tenantID string, vars map[string]string) error
}
