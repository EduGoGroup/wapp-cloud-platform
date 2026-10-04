// Package leasehelpertest es la suite de contrato del puerto lease.Repository y su doble en
// memoria, Memoria (D-F3-1: el doble vivía en el paquete de producción viejo, como
// MemoryRepository). Ningún código de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, ContratoRepository, la tabla de casos y las ayudas.
//   - edge_contrato.go: los casos de las filas por Edge (Upsert, MarkRevoked, Get).
//   - tenant_contrato.go: los casos del corte por tenant y su independencia de las filas por Edge.
//   - memoria.go: Memoria, el Repository en memoria que usan los tests de lease.Manager.
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go)
// y lease.PostgresRepository en los procesos de F9 (sesión F3-05), con el arnés de
// testcontainers.
//
// Es la mitad servidora de la doble llave (ADR-0007): lo que esta suite fija es que la
// revocación es PEGAJOSA en el almacén —la segunda de las dos guardas del kill-switch; la
// primera es la lectura previa de lease.Manager— y que los dos sujetos de corte, el Edge y el
// tenant, son independientes.
//
// Los casos salen de plan/F3-edge/diseno.md §2 y de los tests viejos de internal/gateway/lease
// @ 8896f13 (TestMemoryUpsertNoResucitaLeaseRevocado, TestIntegration_*), leídos, no portados.
package leasehelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/google/uuid"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso. Tiene que venir
// limpio —sin filas de lease— porque ContratoRepository llama a nuevo una vez por caso.
type Montaje struct {
	// Repository es la implementación bajo prueba.
	Repository lease.Repository
	// SeedTenant crea un tenant que EXISTE, activo (no revocado), y devuelve su id, que tiene
	// forma de UUID y es distinto en cada llamada. Con Postgres es una fila de public.tenants
	// (public.leases la referencia por clave foránea y el corte por tenant es su columna
	// revoked_at); con Memoria basta un UUID nuevo, porque el doble no sabe de tenants que no
	// existen. Falla el test t si no puede sembrar.
	SeedTenant func(t *testing.T) string
}

// ContratoRepository ejecuta las promesas de lease.Repository contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada.
//
// Lo que la suite NO afirma, a propósito, porque las dos implementaciones divergen:
//   - MarkTenantRevoked sobre un tenant que NO existe: Memoria lo deja marcado; Postgres no
//     toca ninguna fila y TenantRevoked sigue diciendo false. La suite solo marca tenants
//     sembrados.
//   - los valores exactos de IssuedAt y UpdatedAt: en Postgres son el now() del servidor. Solo
//     se afirma que IssuedAt no cambia al renovar.
//   - un tenant_id que no es un UUID: Postgres devuelve un error de parseo y Memoria no.
func ContratoRepository(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("leasehelpertest.ContratoRepository: nuevo es nil; hace falta una función que devuelva un Montaje")
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
		{"Get_NeverSeenEdge_NotFound", caseGetNeverSeen},                             // found=false sin error
		{"Upsert_NewRow_IsNotRevoked", caseUpsertNewRow},                             // fila nueva: vigente
		{"Upsert_NewRow_IgnoresRevokedTrue", caseUpsertIgnoresRevokedTrue},           // Upsert no sirve para revocar
		{"Upsert_ExistingRow_UpdatesCounterAndExpiry", caseUpsertExisting},           // renueva; issued_at se conserva
		{"Upsert_DoesNotResurrectRevoked", caseUpsertDoesNotResurrect},               // «Upsert no resucita» (R-L2, T-8)
		{"MarkRevoked_IsStickyAndKeepsCounter", caseMarkRevokedSticky},               // pegajoso; conserva el counter
		{"MarkRevoked_NeverSeenEdge_CreatesRevokedRow", caseMarkRevokedNeverSeen},    // el kill-switch deja rastro
		{"Rows_AreIsolatedByTenantAndEdge", caseRowsIsolated},                        // clave (tenant, edge)
		{"ConcurrentUpsertAndMarkRevoked_EndsRevoked", caseConcurrentRevoke},         // ninguna carrera des-revoca
		{"TenantRevoked_ActiveOrUnknownTenant_False", caseTenantActiveOrUnknown},     // la ausencia no es un «sí»
		{"MarkTenantRevoked_IsStickyAndPerTenant", caseTenantRevokedSticky},          // pegajoso; por tenant
		{"RestoreTenant_ClearsTheTenantCut", caseRestoreTenant},                      // revoked_at = NULL
		{"TenantCut_DoesNotTouchEdgeRows", caseTenantCutLeavesEdgeRows},              // dos sujetos de corte (D-055.2)
		{"RestoreTenant_DoesNotRestoreARevokedEdge", caseRestoreKeepsEdgeRevocation}, // ídem, en el otro sentido
		{"EdgeRevocation_DoesNotRevokeTheTenant", caseEdgeCutLeavesTenant},           // ídem
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Repository == nil:
		t.Fatal("Montaje.Repository es nil")
	case m.SeedTenant == nil:
		t.Fatal("Montaje.SeedTenant es nil: la suite necesita sembrar tenants")
	}
}

// Los Edge de la suite y sus instantes. Los instantes van en segundos enteros y en UTC para que
// sobrevivan sin pérdida a un timestamptz de Postgres.
const (
	edgeOne = "contract-edge-1"
	edgeTwo = "contract-edge-2"
)

var (
	expiryFirst  = time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	expirySecond = time.Date(2032, 4, 5, 6, 7, 8, 0, time.UTC)
	expiryThird  = time.Date(2033, 5, 6, 7, 8, 9, 0, time.UTC)
)

// seedTenant siembra un tenant y comprueba que su id es un UUID bien formado.
func seedTenant(t *testing.T, m Montaje) string {
	t.Helper()
	tenant := m.SeedTenant(t)
	if _, err := uuid.Parse(tenant); err != nil {
		t.Fatalf("Montaje.SeedTenant devolvió %q, que no es un UUID bien formado: %v", tenant, err)
	}
	return tenant
}

// upsert llama a Upsert y falla el test si devuelve error.
func upsert(t *testing.T, m Montaje, s lease.State) {
	t.Helper()
	if err := m.Repository.Upsert(context.Background(), s); err != nil {
		t.Fatalf("Upsert(%s, %s, counter=%d): error inesperado %v", s.TenantID, s.EdgeID, s.Counter, err)
	}
}

// markRevoked llama a MarkRevoked y falla el test si devuelve error.
func markRevoked(t *testing.T, m Montaje, tenant, edge string, expiresAt time.Time) {
	t.Helper()
	if err := m.Repository.MarkRevoked(context.Background(), tenant, edge, expiresAt); err != nil {
		t.Fatalf("MarkRevoked(%s, %s): error inesperado %v", tenant, edge, err)
	}
}

// mustGet devuelve la fila del Edge, que tiene que existir.
func mustGet(t *testing.T, m Montaje, tenant, edge string) lease.State {
	t.Helper()
	st, found, err := m.Repository.Get(context.Background(), tenant, edge)
	if err != nil {
		t.Fatalf("Get(%s, %s): error inesperado %v", tenant, edge, err)
	}
	if !found {
		t.Fatalf("Get(%s, %s): found = false, quería la fila", tenant, edge)
	}
	return st
}

// requireRow afirma el counter, la expiración y el estado de revocación de la fila del Edge, y
// que la fila dice de quién es.
func requireRow(t *testing.T, m Montaje, tenant, edge string, counter int64, expiresAt time.Time, revoked bool) lease.State {
	t.Helper()
	st := mustGet(t, m, tenant, edge)
	if st.TenantID != tenant || st.EdgeID != edge {
		t.Errorf("Get(%s, %s) devolvió la fila de (%s, %s)", tenant, edge, st.TenantID, st.EdgeID)
	}
	if st.Counter != counter {
		t.Errorf("Get(%s, %s): counter = %d, quería %d", tenant, edge, st.Counter, counter)
	}
	if !st.ExpiresAt.Equal(expiresAt) {
		t.Errorf("Get(%s, %s): expires_at = %v, quería %v", tenant, edge, st.ExpiresAt, expiresAt)
	}
	if st.Revoked != revoked {
		t.Errorf("Get(%s, %s): revoked = %v, quería %v", tenant, edge, st.Revoked, revoked)
	}
	return st
}

// requireTenantRevoked afirma lo que dice TenantRevoked del tenant.
func requireTenantRevoked(t *testing.T, m Montaje, tenant string, want bool) {
	t.Helper()
	got, err := m.Repository.TenantRevoked(context.Background(), tenant)
	if err != nil {
		t.Fatalf("TenantRevoked(%s): error inesperado %v", tenant, err)
	}
	if got != want {
		t.Errorf("TenantRevoked(%s) = %v, quería %v", tenant, got, want)
	}
}

// markTenantRevoked llama a MarkTenantRevoked y falla el test si devuelve error.
func markTenantRevoked(t *testing.T, m Montaje, tenant string) {
	t.Helper()
	if err := m.Repository.MarkTenantRevoked(context.Background(), tenant); err != nil {
		t.Fatalf("MarkTenantRevoked(%s): error inesperado %v", tenant, err)
	}
}

// restoreTenant llama a RestoreTenant y falla el test si devuelve error.
func restoreTenant(t *testing.T, m Montaje, tenant string) {
	t.Helper()
	if err := m.Repository.RestoreTenant(context.Background(), tenant); err != nil {
		t.Fatalf("RestoreTenant(%s): error inesperado %v", tenant, err)
	}
}
