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
	"errors"
	"slices"
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

func caseListTenantsTieBreak(t *testing.T, m Montaje) {
	seeded := m.State.SeedTenantsCreatedAt(t, 3, tieAt)
	full := listTenants(t, m, 500, 0)
	assertTenantOrder(t, full)
	for _, id := range seeded {
		if n := countTenant(full, id); n != 1 {
			t.Fatalf("la empresa sembrada %s aparece %d veces en el listado completo, quiero 1", id, n)
		}
	}
	// Página a página (limit=1) se ve EXACTAMENTE la misma secuencia que de una vez: ninguna
	// empresa empatada se repite ni se salta entre dos páginas consecutivas.
	last := 0
	for i, it := range full {
		if slices.Contains(seeded, it.ID) {
			last = i
		}
	}
	for off := 0; off <= last; off++ {
		page := listTenants(t, m, 1, off)
		if len(page) != 1 || page[0].ID != full[off].ID {
			t.Fatalf("ListTenants(1, %d) = %v, quiero [%s] (la misma fila que en el listado completo)", off, ids(page), full[off].ID)
		}
	}
}

func caseListTenantsClamp(t *testing.T, m Montaje) {
	// 51 empresas garantizan que hay más de 50: el límite por defecto se nota.
	m.State.SeedTenantsCreatedAt(t, 51, tieAt.Add(time.Hour))
	def := listTenants(t, m, 0, -3)
	if len(def) != 50 {
		t.Fatalf("ListTenants(0, -3) devolvió %d empresas, quiero 50 (limit ≤ 0 ⇒ 50)", len(def))
	}
	want := listTenants(t, m, 50, 0)
	if !slices.Equal(ids(def), ids(want)) {
		t.Fatalf("ListTenants(0, -3) = %v, quiero lo mismo que ListTenants(50, 0) = %v (offset < 0 ⇒ 0)", ids(def), ids(want))
	}
	neg := listTenants(t, m, -1, 0)
	if !slices.Equal(ids(neg), ids(want)) {
		t.Fatal("ListTenants(-1, 0) tiene que ser ListTenants(50, 0)")
	}
}

func caseListTenantsPastTheEnd(t *testing.T, m Montaje) {
	page, err := m.Tenants.ListTenants(bg(), 10, 1_000_000)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if page == nil || len(page) != 0 {
		t.Fatalf("ListTenants pasado el final = %#v, quiero un arreglo vacío y no nil", page)
	}
}

func caseGetTenantMissing(t *testing.T, m Montaje) {
	detail, err := m.Tenants.GetTenant(bg(), m.MissingTenant)
	if !errors.Is(err, platformadmin.ErrNotFound) {
		t.Fatalf("GetTenant(inexistente) = %v, quiero ErrNotFound", err)
	}
	if detail.ID != "" {
		t.Fatalf("con error no se devuelve detalle: %+v", detail)
	}
}

func caseGetTenantCreated(t *testing.T, m Montaje) {
	slug := newSlug()
	created := mustCreateTenant(t, m, slug, "Empresa de contrato", nil)
	if created.Slug != slug {
		t.Fatalf("CreateTenant devolvió slug %q, quiero %q", created.Slug, slug)
	}
	if _, err := uuid.Parse(created.ID); err != nil {
		t.Fatalf("CreateTenant devolvió id %q, que no es un UUID: %v", created.ID, err)
	}
	d, err := m.Tenants.GetTenant(bg(), created.ID)
	if err != nil {
		t.Fatalf("GetTenant(recién creada): %v", err)
	}
	switch {
	case d.ID != created.ID || d.Slug != slug || d.DisplayName != "Empresa de contrato":
		t.Fatalf("GetTenant = %+v, quiero id %s, slug %s y display_name «Empresa de contrato»", d, created.ID, slug)
	case d.PlanID != nil || d.RevokedAt != nil:
		t.Fatalf("una empresa recién creada sin plan tiene plan_id y revoked_at NULL: %+v", d)
	case d.CreatedAt.IsZero() || d.UpdatedAt.IsZero():
		t.Fatalf("GetTenant sin created_at/updated_at: %+v", d)
	case d.InstallationsCount != 0:
		t.Fatalf("InstallationsCount = %d sin sesiones, quiero 0", d.InstallationsCount)
	case d.Features == nil:
		t.Fatal("Features es nil: tiene que ser un arreglo (vacío si no hay ninguna)")
	case !slices.IsSorted(d.Features) || len(slices.Compact(slices.Clone(d.Features))) != len(d.Features):
		t.Fatalf("Features = %v, quiero ordenadas y sin repetir", d.Features)
	}
}

func caseGetTenantCountsEdges(t *testing.T, m Montaje) {
	seen := fixedTime()
	m.State.SeedFleetSession(t, m.TenantA, "edge-1", "s1", &seen)
	m.State.SeedFleetSession(t, m.TenantA, "edge-1", "s2", nil)
	m.State.SeedFleetSession(t, m.TenantA, "edge-2", "s1", nil)
	m.State.SeedFleetSession(t, m.TenantB, "edge-3", "s1", nil)
	d, err := m.Tenants.GetTenant(bg(), m.TenantA)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if d.InstallationsCount != 2 {
		t.Fatalf("InstallationsCount = %d, quiero 2 (edges DISTINTOS de esa empresa)", d.InstallationsCount)
	}
}

func caseExistsTenant(t *testing.T, m Montaje) {
	for _, c := range []struct {
		id   string
		want bool
	}{{m.TenantA, true}, {m.TenantB, true}, {m.MissingTenant, false}} {
		got, err := m.Tenants.ExistsTenant(bg(), c.id)
		if err != nil {
			t.Fatalf("ExistsTenant(%s): %v", c.id, err)
		}
		if got != c.want {
			t.Fatalf("ExistsTenant(%s) = %v, quiero %v", c.id, got, c.want)
		}
	}
}

func caseCreateTenantDuplicate(t *testing.T, m Montaje) {
	slug := newSlug()
	first := mustCreateTenant(t, m, slug, "Primera", nil)
	created, err := m.Tenants.CreateTenant(bg(), slug, "Segunda", nil)
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("CreateTenant(slug repetido) = %v, quiero un error que envuelva ErrConflict", err)
	}
	if created != (platformadmin.CreatedTenant{}) {
		t.Fatalf("con error no se devuelve empresa: %+v", created)
	}
	d, err := m.Tenants.GetTenant(bg(), first.ID)
	if err != nil || d.DisplayName != "Primera" {
		t.Fatalf("la primera empresa tiene que seguir intacta: %+v, %v", d, err)
	}
	if n := countSlug(listTenants(t, m, 500, 0), slug); n != 1 {
		t.Fatalf("el slug %s aparece %d veces, quiero 1", slug, n)
	}
}

func caseCreateTenantEmptyFields(t *testing.T, m Montaje) {
	for _, c := range []struct{ name, slug, display string }{
		{"EmptySlug", "", "Empresa"},
		{"EmptyDisplayName", newSlug(), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := m.Tenants.CreateTenant(bg(), c.slug, c.display, nil)
			if !errors.Is(err, platformadmin.ErrInvalidInput) {
				t.Fatalf("CreateTenant(%q, %q) = %v, quiero ErrInvalidInput", c.slug, c.display, err)
			}
			if c.slug != "" && countSlug(listTenants(t, m, 500, 0), c.slug) != 0 {
				t.Fatalf("CreateTenant rechazado escribió la empresa %s", c.slug)
			}
		})
	}
}

func caseCreateTenantEmptyPlan(t *testing.T, m Montaje) {
	created := mustCreateTenant(t, m, newSlug(), "Sin plan", ptr(""))
	d, err := m.Tenants.GetTenant(bg(), created.ID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if d.PlanID != nil {
		t.Fatalf("plan_id = %q, quiero NULL (\"\" se guarda como NULL)", *d.PlanID)
	}
}

func caseListInstallations(t *testing.T, m Montaje) {
	early, late := fixedTime(), fixedTime().Add(time.Minute)
	m.State.SeedFleetSession(t, m.TenantA, "edge-b", "s1", &early)
	m.State.SeedFleetSession(t, m.TenantA, "edge-b", "s2", &late)
	m.State.SeedLease(t, m.TenantA, "edge-b", true)
	m.State.SeedFleetSession(t, m.TenantA, "edge-a", "s1", nil)
	m.State.SeedFleetSession(t, m.TenantA, "edge-c", "s1", nil)
	m.State.SeedLease(t, m.TenantA, "edge-c", false)
	m.State.SeedFleetSession(t, m.TenantB, "edge-a", "s9", &late) // otra empresa, mismo edge_id
	m.State.SeedLease(t, m.TenantB, "edge-a", true)               // su lease revocado no es el de A
	got, err := m.Tenants.ListInstallations(bg(), m.TenantA)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	want := []platformadmin.InstallationItem{
		{EdgeID: "edge-a", Sessions: 1},
		{EdgeID: "edge-b", Sessions: 2, LastSeenAt: &late, LeaseRevoked: true},
		{EdgeID: "edge-c", Sessions: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("ListInstallations = %+v, quiero %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.EdgeID != w.EdgeID || g.Sessions != w.Sessions || g.LeaseRevoked != w.LeaseRevoked || !sameTime(g.LastSeenAt, w.LastSeenAt) {
			t.Fatalf("instalación %d = %+v (last_seen %v), quiero %+v (last_seen %v)", i, g, g.LastSeenAt, w, w.LastSeenAt)
		}
	}
}

func caseListInstallationsNone(t *testing.T, m Montaje) {
	for _, id := range []string{m.TenantB, m.MissingTenant} {
		got, err := m.Tenants.ListInstallations(bg(), id)
		if err != nil {
			t.Fatalf("ListInstallations(%s): %v", id, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("ListInstallations(%s) = %#v, quiero un arreglo vacío y no nil", id, got)
		}
	}
}

// ── AccessRequestStore ───────────────────────────────────────────────────────────────────────

func caseCreateRequestInvalid(t *testing.T, m Montaje) {
	user := newUser()
	for _, c := range []struct{ name, user, email, origin string }{
		{"EmptyUser", "", "ana@x.com", "bff"},
		{"EmptyEmail", user, "", "bff"},
		{"UnknownOrigin", user, "ana@x.com", "web"},
		{"EmptyOrigin", user, "ana@x.com", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := m.Requests.CreateAccessRequest(bg(), c.user, c.email, c.origin)
			if !errors.Is(err, platformadmin.ErrInvalidInput) {
				t.Fatalf("CreateAccessRequest(%q, %q, %q) = %v, quiero ErrInvalidInput", c.user, c.email, c.origin, err)
			}
		})
	}
	if it := findRequest(t, m, "pending", user); it != nil {
		t.Fatalf("una solicitud rechazada por validación quedó escrita: %+v", *it)
	}
}

func caseCreateRequestListed(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "edge")
	it := findRequest(t, m, "", user) // "" ⇒ pending
	switch {
	case it == nil:
		t.Fatal("ListAccessRequests(\"\") no devuelve la solicitud recién creada (\"\" se toma como pending)")
	case it.Email != "ana@x.com" || it.Origin != "edge" || it.Status != "pending":
		t.Fatalf("solicitud = %+v, quiero ana@x.com, edge, pending", *it)
	case it.CreatedAt.IsZero():
		t.Fatalf("solicitud sin created_at: %+v", *it)
	case it.Systems == nil || len(it.Systems) != 0 || it.SystemsKnown:
		t.Fatalf("Systems = %#v, SystemsKnown = %v; quiero [] y false (C-05)", it.Systems, it.SystemsKnown)
	}
	if _, err := uuid.Parse(it.ID); err != nil {
		t.Fatalf("id de solicitud %q no es un UUID: %v", it.ID, err)
	}
	row := m.State.Request(t, it.ID)
	if row.Reason != nil || row.DecidedBy != nil || row.DecidedAt != nil {
		t.Fatalf("una solicitud pendiente no tiene motivo ni decisión: %+v", row)
	}
}

func caseCreateRequestIdempotent(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	first := findRequest(t, m, "pending", user)
	if err := m.Requests.CreateAccessRequest(bg(), user, "otra@x.com", "edge"); err != nil {
		t.Fatalf("una segunda solicitud pendiente de la misma persona no es error: %v", err)
	}
	items := listRequests(t, m, "pending")
	if n := countRequests(items, user); n != 1 {
		t.Fatalf("la persona tiene %d solicitudes pendientes, quiero 1", n)
	}
	again := findRequest(t, m, "pending", user)
	if again.ID != first.ID || again.Email != "ana@x.com" || again.Origin != "bff" {
		t.Fatalf("la pendiente cambió: %+v, quiero la primera %+v sin tocar", *again, *first)
	}
}

func caseListRequestsByStatus(t *testing.T, m Montaje) {
	u1, u2, u3 := newUser(), newUser(), newUser()
	mustCreateRequest(t, m, u1, "u1@x.com", "bff")
	mustCreateRequest(t, m, u2, "u2@x.com", "bff")
	mustCreateRequest(t, m, u3, "u3@x.com", "edge")
	r1 := findRequest(t, m, "pending", u1)
	if err := m.Requests.RejectAccessRequest(bg(), r1.ID, "duplicada", newUser()); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	pending := listRequests(t, m, "pending")
	if countRequests(pending, u1) != 0 || countRequests(pending, u2) != 1 || countRequests(pending, u3) != 1 {
		t.Fatalf("pending: quiero u2 y u3, no u1: %+v", pending)
	}
	if indexOfUser(pending, u2) > indexOfUser(pending, u3) {
		t.Fatal("pending: u2 se creó antes que u3 y tiene que listarse antes (created_at ascendente)")
	}
	rejected := listRequests(t, m, "rejected")
	if countRequests(rejected, u1) != 1 || countRequests(rejected, u2) != 0 {
		t.Fatalf("rejected: quiero u1 y no u2: %+v", rejected)
	}
	if it := rejected[indexOfUser(rejected, u1)]; it.Status != "rejected" || it.Systems == nil {
		t.Fatalf("rejected: %+v", it)
	}
}

func caseLookupStatus(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	gotUser, status, err := m.Requests.LookupAccessRequestStatus(bg(), r.ID)
	if err != nil || gotUser != user || status != "pending" {
		t.Fatalf("LookupAccessRequestStatus = (%q, %q, %v), quiero (%q, pending, nil)", gotUser, status, err, user)
	}
	gotUser, status, err = m.Requests.LookupAccessRequestStatus(bg(), uuid.NewString())
	if !errors.Is(err, platformadmin.ErrNotFound) || gotUser != "" || status != "" {
		t.Fatalf("LookupAccessRequestStatus(inexistente) = (%q, %q, %v), quiero (\"\", \"\", ErrNotFound)", gotUser, status, err)
	}
}

func caseResolveRoleID(t *testing.T, m Montaje) {
	for _, role := range []Role{m.RoleA, m.RoleB} {
		for _, key := range []string{role.Name, role.ID} {
			got, err := m.Requests.ResolveRoleID(bg(), key)
			if err != nil || got != role.ID {
				t.Fatalf("ResolveRoleID(%q) = (%q, %v), quiero (%q, nil)", key, got, err, role.ID)
			}
		}
	}
	for _, unknown := range []string{"contract_no_such_role_" + strings.ReplaceAll(newUser(), "-", ""), uuid.NewString()} {
		got, err := m.Requests.ResolveRoleID(bg(), unknown)
		if !errors.Is(err, platformadmin.ErrInvalidInput) || got != "" {
			t.Fatalf("ResolveRoleID(%q) = (%q, %v), quiero (\"\", ErrInvalidInput)", unknown, got, err)
		}
	}
}

func caseRejectPending(t *testing.T, m Montaje) {
	user, operator := newUser(), newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.RejectAccessRequest(bg(), r.ID, "  no es cliente  ", operator); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	row := m.State.Request(t, r.ID)
	switch {
	case row.Status != "rejected":
		t.Fatalf("status = %q, quiero rejected", row.Status)
	case row.Reason == nil || *row.Reason != "  no es cliente  ":
		t.Fatalf("reason = %v, quiero el motivo tal cual", deref(row.Reason))
	case row.DecidedBy == nil || *row.DecidedBy != operator:
		t.Fatalf("decided_by = %v, quiero %s", deref(row.DecidedBy), operator)
	case row.DecidedAt == nil:
		t.Fatal("decided_at es NULL tras rechazar")
	case row.UserID != user || row.Email != "ana@x.com" || row.Origin != "bff":
		t.Fatalf("rechazar tocó columnas que no son suyas: %+v", row)
	}
	if member, roles := m.State.Access(t, user, m.TenantA); member || len(roles) != 0 {
		t.Fatalf("rechazar no da acceso: member=%v roles=%v", member, roles)
	}
}

func caseRejectOperatorNotUUID(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.RejectAccessRequest(bg(), r.ID, "motivo", "operator-test"); err != nil {
		t.Fatalf("RejectAccessRequest con operador no UUID: %v", err)
	}
	if row := m.State.Request(t, r.ID); row.Status != "rejected" || row.DecidedBy != nil || row.DecidedAt == nil {
		t.Fatalf("fila = %+v (decided_by %v), quiero rejected, decided_by NULL y decided_at", row, deref(row.DecidedBy))
	}
}

func caseRejectBlankReason(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	for _, c := range []struct{ name, id, reason string }{
		{"EmptyReason", r.ID, ""},
		{"BlankReason", r.ID, " \t\n "},
		{"EmptyRequestID", "", "motivo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := m.Requests.RejectAccessRequest(bg(), c.id, c.reason, newUser())
			if !errors.Is(err, platformadmin.ErrInvalidInput) {
				t.Fatalf("RejectAccessRequest(%q, %q) = %v, quiero ErrInvalidInput", c.id, c.reason, err)
			}
		})
	}
	if row := m.State.Request(t, r.ID); row.Status != "pending" || row.Reason != nil || row.DecidedBy != nil || row.DecidedAt != nil {
		t.Fatalf("un rechazo inválido tocó la fila: %+v", row)
	}
}

func caseRejectMissing(t *testing.T, m Montaje) {
	err := m.Requests.RejectAccessRequest(bg(), uuid.NewString(), "motivo", newUser())
	if !errors.Is(err, platformadmin.ErrNotFound) {
		t.Fatalf("RejectAccessRequest(inexistente) = %v, quiero ErrNotFound", err)
	}
}

func caseRejectAlreadyDecided(t *testing.T, m Montaje) {
	// Rechazada dos veces: la segunda no pisa la primera.
	u1, first := newUser(), newUser()
	mustCreateRequest(t, m, u1, "u1@x.com", "bff")
	r1 := findRequest(t, m, "pending", u1)
	if err := m.Requests.RejectAccessRequest(bg(), r1.ID, "primero", first); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	before := m.State.Request(t, r1.ID)
	if err := m.Requests.RejectAccessRequest(bg(), r1.ID, "segundo", newUser()); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("rechazar una ya rechazada = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, r1.ID))

	// Aprobada: rechazarla tampoco la toca.
	u2 := newUser()
	r2 := mustApprove(t, m, u2, m.TenantA, m.RoleA.ID)
	before = m.State.Request(t, r2)
	if err := m.Requests.RejectAccessRequest(bg(), r2, "tarde", newUser()); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("rechazar una aprobada = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, r2))
}

func caseApproveGrants(t *testing.T, m Montaje) {
	user, operator := newUser(), newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.ExecuteApprovalTx(bg(), r.ID, m.TenantA, user, m.RoleA.ID, operator); err != nil {
		t.Fatalf("ExecuteApprovalTx: %v", err)
	}
	row := m.State.Request(t, r.ID)
	switch {
	case row.Status != "approved":
		t.Fatalf("status = %q, quiero approved", row.Status)
	case row.DecidedBy == nil || *row.DecidedBy != operator:
		t.Fatalf("decided_by = %v, quiero %s", deref(row.DecidedBy), operator)
	case row.DecidedAt == nil:
		t.Fatal("decided_at es NULL tras aprobar")
	case row.Reason != nil:
		t.Fatalf("aprobar no escribe motivo: %v", *row.Reason)
	}
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
	assertAccess(t, m, user, m.TenantB, false)
	if _, status, err := m.Requests.LookupAccessRequestStatus(bg(), r.ID); err != nil || status != "approved" {
		t.Fatalf("LookupAccessRequestStatus tras aprobar = %q, %v", status, err)
	}
}

func caseApproveNotPending(t *testing.T, m Montaje) {
	user := newUser()
	mustCreateRequest(t, m, user, "ana@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if err := m.Requests.RejectAccessRequest(bg(), r.ID, "no", newUser()); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	before := m.State.Request(t, r.ID)
	err := m.Requests.ExecuteApprovalTx(bg(), r.ID, m.TenantA, user, m.RoleA.ID, newUser())
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("aprobar una rechazada = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, r.ID))
	// O todo o nada: la membresía que el alta habría escrito antes del UPDATE tampoco queda.
	assertAccess(t, m, user, m.TenantA, false)

	missing := uuid.NewString()
	if err := m.Requests.ExecuteApprovalTx(bg(), missing, m.TenantA, user, m.RoleA.ID, newUser()); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("aprobar una solicitud inexistente = %v, quiero ErrConflict", err)
	}
	assertAccess(t, m, user, m.TenantA, false)
}

func caseApproveOtherCompany(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	mustCreateRequest(t, m, user, "ana@x.com", "edge")
	second := findRequest(t, m, "pending", user)
	before := m.State.Request(t, second.ID)
	err := m.Requests.ExecuteApprovalTx(bg(), second.ID, m.TenantB, user, m.RoleB.ID, newUser())
	if !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("aprobar hacia una segunda empresa sin multi_empresa = %v, quiero ErrConflict", err)
	}
	assertSameRow(t, before, m.State.Request(t, second.ID))
	assertAccess(t, m, user, m.TenantB, false)
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
}

func caseApproveSameCompanyAgain(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	second := mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	if row := m.State.Request(t, second); row.Status != "approved" {
		t.Fatalf("la segunda aprobación hacia la MISMA empresa tiene que pasar: status %q", row.Status)
	}
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
}

func caseCheckRetry(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantA, m.RoleA.ID); err != nil {
		t.Fatalf("reintento con la misma empresa y el mismo rol = %v, quiero nil (converge)", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantA, m.RoleB.ID); !errors.Is(err, platformadmin.ErrRetryRoleMismatch) {
		t.Fatalf("reintento con otro rol = %v, quiero ErrRetryRoleMismatch", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantB, m.RoleA.ID); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("reintento hacia otra empresa = %v, quiero ErrConflict", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), newUser(), m.TenantA, m.RoleA.ID); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("reintento de quien no es miembro = %v, quiero ErrConflict", err)
	}
	// Solo lee: el rol pedido en el reintento NO se escribe.
	assertAccess(t, m, user, m.TenantA, true, m.RoleA.ID)
	assertAccess(t, m, user, m.TenantB, false)
}

// El rol del reintento se mira EN la empresa de la solicitud: tener ese rol en OTRA empresa no
// hace converger (iam_user_roles es por empresa desde la 0060).
func caseCheckRetryRoleScoped(t *testing.T, m Montaje) {
	user := newUser()
	mustApprove(t, m, user, m.TenantA, m.RoleA.ID)
	m.State.SeedMembership(t, user, m.TenantB, m.RoleB.ID)
	assertAccess(t, m, user, m.TenantB, true, m.RoleB.ID)
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantA, m.RoleB.ID); !errors.Is(err, platformadmin.ErrRetryRoleMismatch) {
		t.Fatalf("reintento en A con el rol que tiene en B = %v, quiero ErrRetryRoleMismatch", err)
	}
	if err := m.Requests.CheckRetryApproved(bg(), user, m.TenantB, m.RoleB.ID); err != nil {
		t.Fatalf("reintento en B con su rol de B = %v, quiero nil", err)
	}
}

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

func listTenants(t *testing.T, m Montaje, limit, offset int) []platformadmin.TenantListItem {
	t.Helper()
	items, err := m.Tenants.ListTenants(bg(), limit, offset)
	if err != nil {
		t.Fatalf("ListTenants(%d, %d): %v", limit, offset, err)
	}
	return items
}

func ids(items []platformadmin.TenantListItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func countTenant(items []platformadmin.TenantListItem, id string) int {
	n := 0
	for _, it := range items {
		if it.ID == id {
			n++
		}
	}
	return n
}

func countSlug(items []platformadmin.TenantListItem, slug string) int {
	n := 0
	for _, it := range items {
		if it.Slug == slug {
			n++
		}
	}
	return n
}

// assertTenantOrder exige created_at DESC y, a igualdad, id DESC. El orden de los UUID en
// Postgres es el de sus bytes, que coincide con el de su texto canónico en minúsculas.
func assertTenantOrder(t *testing.T, items []platformadmin.TenantListItem) {
	t.Helper()
	for i := 1; i < len(items); i++ {
		prev, cur := items[i-1], items[i]
		if cur.CreatedAt.After(prev.CreatedAt) {
			t.Fatalf("posición %d: created_at %v va después de %v (quiero DESC)", i, cur.CreatedAt, prev.CreatedAt)
		}
		if cur.CreatedAt.Equal(prev.CreatedAt) && strings.Compare(cur.ID, prev.ID) >= 0 {
			t.Fatalf("posición %d: empate en created_at y el id %s no es menor que %s (quiero id DESC)", i, cur.ID, prev.ID)
		}
	}
}

func mustCreateTenant(t *testing.T, m Montaje, slug, display string, planID *string) platformadmin.CreatedTenant {
	t.Helper()
	created, err := m.Tenants.CreateTenant(bg(), slug, display, planID)
	if err != nil {
		t.Fatalf("CreateTenant(%s): %v", slug, err)
	}
	return created
}

func mustCreateRequest(t *testing.T, m Montaje, user, email, origin string) {
	t.Helper()
	if err := m.Requests.CreateAccessRequest(bg(), user, email, origin); err != nil {
		t.Fatalf("CreateAccessRequest(%s): %v", user, err)
	}
}

func listRequests(t *testing.T, m Montaje, status string) []platformadmin.AccessRequestItem {
	t.Helper()
	items, err := m.Requests.ListAccessRequests(bg(), status)
	if err != nil {
		t.Fatalf("ListAccessRequests(%q): %v", status, err)
	}
	if items == nil {
		t.Fatalf("ListAccessRequests(%q) devolvió nil: quiero un arreglo", status)
	}
	return items
}

// findRequest devuelve la solicitud de user con ese status, o nil si no hay.
func findRequest(t *testing.T, m Montaje, status, user string) *platformadmin.AccessRequestItem {
	t.Helper()
	items := listRequests(t, m, status)
	if i := indexOfUser(items, user); i >= 0 {
		return &items[i]
	}
	return nil
}

func indexOfUser(items []platformadmin.AccessRequestItem, user string) int {
	return slices.IndexFunc(items, func(it platformadmin.AccessRequestItem) bool { return it.UserID == user })
}

func countRequests(items []platformadmin.AccessRequestItem, user string) int {
	n := 0
	for _, it := range items {
		if it.UserID == user {
			n++
		}
	}
	return n
}

// mustApprove crea una solicitud de user y la aprueba hacia tenant con roleID; devuelve su id.
func mustApprove(t *testing.T, m Montaje, user, tenant, roleID string) string {
	t.Helper()
	mustCreateRequest(t, m, user, "aprobada@x.com", "bff")
	r := findRequest(t, m, "pending", user)
	if r == nil {
		t.Fatalf("no encuentro la solicitud pendiente de %s", user)
	}
	if err := m.Requests.ExecuteApprovalTx(bg(), r.ID, tenant, user, roleID, newUser()); err != nil {
		t.Fatalf("ExecuteApprovalTx(%s → %s): %v", user, tenant, err)
	}
	return r.ID
}

func assertAccess(t *testing.T, m Montaje, user, tenant string, wantMember bool, wantRoles ...string) {
	t.Helper()
	member, roles := m.State.Access(t, user, tenant)
	if member != wantMember || len(roles) != len(wantRoles) || !slices.Equal(roles, wantRoles) {
		t.Fatalf("acceso de %s a %s = (member %v, roles %v), quiero (member %v, roles %v)", user, tenant, member, roles, wantMember, wantRoles)
	}
}

func assertSameRow(t *testing.T, before, after RequestRow) {
	t.Helper()
	same := before.UserID == after.UserID && before.Email == after.Email && before.Origin == after.Origin &&
		before.Status == after.Status && deref(before.Reason) == deref(after.Reason) &&
		deref(before.DecidedBy) == deref(after.DecidedBy) && sameTime(before.DecidedAt, after.DecidedAt) &&
		before.CreatedAt.Equal(after.CreatedAt)
	if !same {
		t.Fatalf("la fila cambió:\nantes   %+v\ndespués %+v", before, after)
	}
}
