// Package runtimehelpertest trae los dobles de los puertos del runtime del motor de flujos, los
// gemelos en memoria de sus dos adaptadores Postgres y las dos suites de contrato de esos
// adaptadores. Ningún código de producción lo importa: arrastra "testing".
//
// Los dobles, para los tests del runtime (registran lo que les llega, en orden, y aceptan errores
// inyectados; ninguno duerme ni mira el reloj):
//
//   - sender.go: Sender (runtime.Sender), con sus envíos de texto y de adjuntos en una sola lista.
//   - presigner.go: Presigner (runtime.Presigner).
//   - ports.go: TenantResolver, SelfNumbers e IngestDeduper (los tres puertos de runtime.go) y
//     ReplyLimiter y DepositReminder (los dos de runtime_engine.go).
//
// Los gemelos y las suites, para los dos adaptadores sin gemelo (puertos con BD, P4):
//
//   - fleet.go: FleetSessions, que es public.fleet_sessions en memoria, y sobre ella
//     MemoryTenantResolver y MemorySelfNumbers.
//   - contrato.go (este): lo que comparten las dos suites.
//   - tenant_resolver_contrato.go: ContratoTenantResolver, su MontajeTenantResolver y sus casos.
//   - self_numbers_contrato.go: ContratoSelfNumbers, su MontajeSelfNumbers y sus casos.
//
// Cada suite la corren las dos implementaciones: el gemelo en unitario (fleet_test.go) y el
// adaptador contra Postgres en los procesos de F9, con el arnés de testcontainers
// (test/procesos/runtime_tenant_resolver_contrato_test.go y runtime_self_numbers_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de runtime.PostgresTenantResolver y
// de runtime.PostgresSelfNumbers y de los tests viejos de internal/flujos/runtime @ e0159171
// (tenant_resolver_integration_test.go, self_numbers_integration_test.go, self_loop_guard_test.go
// y passive_guard_test.go), leídos y NO portados (05 E-8).
//
// Para añadir un caso: escribe su función en el fichero de su suite y añade su fila a la tabla.
package runtimehelpertest

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

// Las dos sesiones y los dos edges con que siembran los casos. Cada caso recibe una tabla vacía,
// así que no hace falta que sean únicos entre casos.
const (
	sessionOne = "sess-uno"
	sessionTwo = "sess-dos"
	edgeOne    = "edge-uno"
	edgeTwo    = "edge-dos"
)

// seedFunc y sessionsFunc son la siembra y el observador que traen los dos Montajes.
type (
	seedFunc     = func(t *testing.T, s Session)
	sessionsFunc = func(t *testing.T) []Session
)

// validateFleetMontaje exige lo que las dos suites dan por hecho de la parte común de un Montaje:
// siembra, observador y permiso de perfil presentes, dos tenants distintos con forma de UUID y la
// tabla VACÍA.
func validateFleetMontaje(t *testing.T, tenantA, tenantB string, seed seedFunc, allowAnyProfile func(*testing.T), sessions sessionsFunc) {
	t.Helper()
	switch {
	case seed == nil:
		t.Fatal("Montaje.Seed es nil")
	case allowAnyProfile == nil:
		t.Fatal("Montaje.AllowAnyProfile es nil")
	case sessions == nil:
		t.Fatal("Montaje.Sessions es nil")
	case tenantA == tenantB:
		t.Fatalf("Montaje: TenantA y TenantB deben ser distintos y son %q", tenantA)
	}
	for _, tenant := range []string{tenantA, tenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
	}
	if rows := sessions(t); len(rows) != 0 {
		t.Fatalf("Montaje: la tabla de sesiones tiene que venir vacía y trae %d filas: %v", len(rows), rows)
	}
}

// requireUntouched afirma que la tabla de sesiones está fila a fila como en before: los dos
// adaptadores solo leen, así que ninguna pregunta puede dejar rastro (ni una fila nueva, ni un
// estado, un perfil o un índice cambiados).
func requireUntouched(t *testing.T, sessions sessionsFunc, before []Session) {
	t.Helper()
	if after := sessions(t); !slices.Equal(before, after) {
		t.Errorf("la pregunta tocó public.fleet_sessions:\n antes   %v\n después %v", before, after)
	}
}
