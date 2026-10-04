// Package platformadminhelpertest es la suite de contrato de los dos puertos de platformadmin
// (TenantStore y AccessRequestStore) y su doble en memoria, Fake. Ningún código de producción lo
// importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, State, Contrato y los casos de los dos puertos, que se
//     leen de una pasada.
//   - fake.go: Fake, los dos puertos en memoria (y State), alineado con Postgres.
//
// La suite la corren las dos implementaciones: Fake en unitario (fake_test.go) y
// platformadmin.Repository en los procesos de F9 (test/procesos/platformadmin_contrato_test.go),
// con el arnés de testcontainers.
//
// Nuevo (D-F2-3): no tiene fichero viejo. Los casos salen de diseño F2 §2 y §4 (R-A3, R-A6) y de
// los tests viejos de internal/platformadmin @ 9a77307 (postgres_test.go, access_requests_test.go,
// executeapprovaltx_internal_test.go), leídos, no portados.
package platformadminhelpertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso. Tiene que venir limpio
// en lo que el caso toca: las dos empresas recién creadas, sin sesiones, sin miembros y sin
// multi_empresa. Contrato llama a nuevo una vez por caso. Con Postgres los casos pueden compartir
// base: la suite usa personas nuevas (UUID) en cada caso y filtra los listados globales.
type Montaje struct {
	// Tenants y Requests son las implementaciones bajo prueba. Pueden ser el mismo valor (Fake y
	// Repository implementan los dos), y tienen que ver la MISMA base: ExecuteApprovalTx escribe
	// membresías de empresas de Tenants.
	Tenants  platformadmin.TenantStore
	Requests platformadmin.AccessRequestStore
	// TenantA y TenantB son dos empresas que existen, distintas, con forma de UUID, sin sesiones,
	// sin miembros y SIN multi_empresa (con Postgres, filas de public.tenants: las FK las exigen;
	// el resolver con el que se construyó el adaptador contesta que no a toda feature).
	TenantA, TenantB string
	// MissingTenant es un UUID bien formado que no es ninguna empresa.
	MissingTenant string
	// RoleA y RoleB son dos roles que existen, distintos, que se pueden asignar en una empresa
	// (con Postgres, plantillas globales de public.iam_roles, p. ej. operator y viewer). Sus
	// nombres no los comparte ningún otro rol.
	RoleA, RoleB Role
	// State siembra y observa lo que los puertos no dejan escribir ni ver.
	State State
}

// Role es un rol por su id y su nombre: ResolveRoleID resuelve los dos.
type Role struct {
	ID, Name string
}

// State es la vista que la suite necesita de lo que los puertos no exponen. Con Fake lo
// implementa el propio Fake; con Postgres, un adaptador sobre public.tenants,
// public.fleet_sessions, public.leases, public.access_requests, public.tenant_members y
// public.iam_user_roles de la misma base. Cada método falla el test t si no puede.
//
// Hallazgo 35 de F1: Request devuelve TODAS las columnas que una operación de la bandeja puede
// tocar (status, reason, decided_by, decided_at, y las que no deben cambiar), y Access todo lo que
// ExecuteApprovalTx escribe fuera de la solicitud (membresía y roles en esa empresa).
type State interface {
	// SeedTenantsCreatedAt crea n empresas con el MISMO created_at (at) y devuelve sus ids. Es el
	// empate que el desempate por id tiene que resolver (R-A3).
	SeedTenantsCreatedAt(t *testing.T, n int, at time.Time) []string
	// SeedFleetSession siembra la sesión sessionID del edge edgeID de la empresa tenantID, con su
	// última señal (nil = nunca dio señal).
	SeedFleetSession(t *testing.T, tenantID, edgeID, sessionID string, lastSeenAt *time.Time)
	// SeedLease siembra el lease del edge edgeID de la empresa tenantID, revocado o no.
	SeedLease(t *testing.T, tenantID, edgeID string, revoked bool)
	// SeedMembership hace a userID miembro de tenantID con esos roles EN esa empresa, sin pasar
	// por la regla de una sola empresa (es una siembra: deja a una persona en dos empresas, que
	// el puerto solo permite con multi_empresa). Repetirla no duplica nada.
	SeedMembership(t *testing.T, userID, tenantID string, roleIDs ...string)
	// Request devuelve la fila completa de la solicitud requestID; falla el test si no existe.
	Request(t *testing.T, requestID string) RequestRow
	// Access devuelve si userID es miembro de tenantID y los ids de los roles que tiene EN esa
	// empresa, ordenados (vacío si ninguno).
	Access(t *testing.T, userID, tenantID string) (member bool, roleIDs []string)
}

// RequestRow es una fila de la bandeja con todas sus columnas. Los punteros son nil cuando la
// columna es NULL.
type RequestRow struct {
	UserID, Email, Origin, Status string
	Reason                        *string
	DecidedBy                     *string
	DecidedAt                     *time.Time
	CreatedAt                     time.Time
}

// Contrato ejecuta las promesas de platformadmin.TenantStore y platformadmin.AccessRequestStore
// (ports.go) contra la implementación que devuelve nuevo, con un Montaje limpio por caso. No
// salta nada: un caso que no aplica a una implementación es un defecto del puerto, no de la suite.
//
// Lo que la suite NO afirma, a propósito:
//   - las features efectivas de GetTenant más allá de «ordenadas, sin repetir y no nil»: su
//     resolución (plan, overrides) es la de entitlements y la prueba su ContratoResolver;
//   - el límite superior de ListTenants (500): sembrar 501 empresas por caso no compensa; lo
//     prueba el test unitario de la función pura del adaptador (postgres_test.go);
//   - la atomicidad de ExecuteApprovalTx ante un fallo A MITAD (el INSERT del rol revienta tras
//     escribir la membresía, R-A7): no se puede provocar desde el puerto; es del proceso de F9;
//   - el orden de ListAccessRequests entre dos solicitudes con el MISMO created_at (el viejo no
//     desempata).
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("platformadminhelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa de un puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		// TenantStore
		{"ListTenants_TiedCreatedAt_StablePagesByIDDesc", caseListTenantsTieBreak},       // R-A3
		{"ListTenants_ClampsLimitAndOffset", caseListTenantsClamp},                       // 50 / 0
		{"ListTenants_PastTheEnd_EmptyNotNil", caseListTenantsPastTheEnd},                // nunca nil
		{"GetTenant_Missing_ErrNotFound", caseGetTenantMissing},                          // 404
		{"GetTenant_Created_RowWithoutPlan", caseGetTenantCreated},                       // la fila
		{"GetTenant_CountsDistinctEdges", caseGetTenantCountsEdges},                      // COUNT DISTINCT
		{"ExistsTenant_TrueAndFalse", caseExistsTenant},                                  // ligera
		{"CreateTenant_DuplicateSlug_ErrConflict", caseCreateTenantDuplicate},            // 409
		{"CreateTenant_EmptyFields_ErrInvalidInput", caseCreateTenantEmptyFields},        // 400
		{"CreateTenant_EmptyPlan_IsNull", caseCreateTenantEmptyPlan},                     // "" ⇒ NULL
		{"ListInstallations_GroupedByEdge_InOrder", caseListInstallations},               // agrupado
		{"ListInstallations_None_EmptyNotNil", caseListInstallationsNone},                // nunca nil
		{"CreateAccessRequest_InvalidInput_NothingWritten", caseCreateRequestInvalid},    // bff|edge
		{"CreateAccessRequest_ListedAsPending", caseCreateRequestListed},                 // la fila
		{"CreateAccessRequest_SecondPending_IsNoop", caseCreateRequestIdempotent},        // ON CONFLICT
		{"ListAccessRequests_FiltersByStatus_InCreationOrder", caseListRequestsByStatus}, // filtro
		{"LookupAccessRequestStatus_FoundAndMissing", caseLookupStatus},                  // ErrNotFound
		{"ResolveRoleID_ByNameOrID_UnknownInvalid", caseResolveRoleID},                   // ErrInvalidInput
		{"RejectAccessRequest_PendingBecomesRejected", caseRejectPending},                // ciclo
		{"RejectAccessRequest_OperatorNotUUID_DecidedByNull", caseRejectOperatorNotUUID}, // NULL
		{"RejectAccessRequest_BlankReason_ErrInvalidInput", caseRejectBlankReason},       // M-02
		{"RejectAccessRequest_Missing_ErrNotFound", caseRejectMissing},                   // M-06
		{"RejectAccessRequest_AlreadyDecided_ErrConflict", caseRejectAlreadyDecided},     // 409
		{"ExecuteApprovalTx_GrantsAccessAndApproves", caseApproveGrants},                 // ciclo
		{"ExecuteApprovalTx_NotPending_ErrConflictNothingWritten", caseApproveNotPending},
		{"ExecuteApprovalTx_OtherCompany_ErrConflictNothingWritten", caseApproveOtherCompany},
		{"ExecuteApprovalTx_SameCompanyAgain_Converges", caseApproveSameCompanyAgain},
		{"CheckRetryApproved_SameRole_DifferentRole_OtherTenant", caseCheckRetry}, // R-A6
		{"CheckRetryApproved_RoleHeldInAnotherCompany_Mismatch", caseCheckRetryRoleScoped},
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Tenants == nil:
		t.Fatal("Montaje.Tenants es nil")
	case m.Requests == nil:
		t.Fatal("Montaje.Requests es nil")
	case m.State == nil:
		t.Fatal("Montaje.State es nil")
	case m.RoleA.ID == "" || m.RoleA.Name == "" || m.RoleB.ID == "" || m.RoleB.Name == "":
		t.Fatalf("Montaje: RoleA y RoleB necesitan id y nombre: %+v, %+v", m.RoleA, m.RoleB)
	case m.RoleA.ID == m.RoleB.ID || m.RoleA.Name == m.RoleB.Name:
		t.Fatalf("Montaje: RoleA y RoleB deben ser distintos: %+v, %+v", m.RoleA, m.RoleB)
	}
	ids := []string{m.TenantA, m.TenantB, m.MissingTenant}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("Montaje: %q no es un UUID bien formado: %v", id, err)
		}
	}
	if m.TenantA == m.TenantB || m.TenantA == m.MissingTenant || m.TenantB == m.MissingTenant {
		t.Fatalf("Montaje: TenantA, TenantB y MissingTenant deben ser distintos: %v", ids)
	}
}

// ── TenantStore ──────────────────────────────────────────────────────────────────────────────

// tieAt es el created_at del empate: muy en el futuro, para que ninguna empresa creada con now()
// se meta entre las sembradas.
var tieAt = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)

// ── ayudas ───────────────────────────────────────────────────────────────────────────────────

func bg() context.Context { return context.Background() }

func newUser() string { return uuid.NewString() }

// newSlug devuelve un slug que no repite ningún otro caso (con Postgres pueden compartir base).
func newSlug() string { return "contract-" + strings.ReplaceAll(uuid.NewString(), "-", "") }

func ptr[V any](v V) *V { return &v }

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// fixedTime es un instante fijo con precisión de microsegundos (la de timestamptz).
func fixedTime() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
