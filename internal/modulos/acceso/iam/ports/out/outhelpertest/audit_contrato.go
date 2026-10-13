package outhelpertest

import (
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MontajeAuditRepo es lo que cada implementación de out.AuditRepo entrega a la suite para UN
// caso: el repositorio, sin eventos de TenantA ni de TenantB, que existen.
type MontajeAuditRepo struct {
	// Repo es la implementación bajo prueba.
	Repo out.AuditRepo
	// TenantA y TenantB son dos tenants que existen, distintos y con forma de UUID.
	TenantA, TenantB string
}

// ContratoAuditRepo ejecuta las promesas de out.AuditRepo contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso: Record append-only que conserva lo que se registra, y
// List acotado al tenant, más recientes primero, con límite y desplazamiento.
//
// Lo que NO afirma, a propósito: el instante (Postgres lo pone con now() y descarta el que trae
// el evento; la memoria conserva uno no cero), ni si un Meta vacío vuelve nil o vacío (Postgres
// guarda '{}'). Los eventos de la suite van sin instante y con Meta de cadenas.
func ContratoAuditRepo(t *testing.T, nuevo func(t *testing.T) MontajeAuditRepo) {
	t.Helper()
	runCases(t, "ContratoAuditRepo", nuevo, validateAuditMontaje, []testCase[MontajeAuditRepo]{
		{"Record_ThenList_KeepsTheEvent", auditRecordKeeps},
		{"Record_IsAppendOnly", auditAppendOnly},
		{"List_MostRecentFirst", auditMostRecentFirst},
		{"List_LimitAndOffset", auditLimitOffset},
		{"List_ScopedToTenant_WithoutPreAuthEvents", auditScoped},
	})
}

func validateAuditMontaje(t *testing.T, m MontajeAuditRepo) {
	t.Helper()
	if m.Repo == nil {
		t.Fatal("MontajeAuditRepo.Repo es nil")
	}
	validateTenants(t, m.TenantA, m.TenantB)
}

// auditEvent es un evento de la suite: del tenant (nil = pre-auth) y con esa acción.
func auditEvent(tenantID *string, action string) domain.AuditEvent {
	return domain.AuditEvent{
		TenantID: tenantID,
		Actor:    "contract-actor",
		Action:   action,
		Resource: "contract-resource",
		Result:   "ok",
		Meta:     map[string]any{"endpoint": "/contract"},
	}
}

// mustRecord registra los eventos en orden y falla el test si alguno no se puede.
func mustRecord(t *testing.T, repo out.AuditRepo, events ...domain.AuditEvent) {
	t.Helper()
	for _, e := range events {
		if err := repo.Record(bg(), e); err != nil {
			t.Fatalf("Record(%s): %v", e.Action, err)
		}
	}
}

// listActions devuelve las acciones de List(tenant, limit, offset) en el orden devuelto.
func listActions(t *testing.T, repo out.AuditRepo, tenantID string, limit, offset int) []string {
	t.Helper()
	events, err := repo.List(bg(), tenantID, limit, offset)
	if err != nil {
		t.Fatalf("List(%s, %d, %d): %v", tenantID, limit, offset, err)
	}
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	return actions
}

func auditRecordKeeps(t *testing.T, m MontajeAuditRepo) {
	want := auditEvent(ptr(m.TenantA), "contract.keep")
	mustRecord(t, m.Repo, want)
	events, err := m.Repo.List(bg(), m.TenantA, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("List = %+v, quiero un evento", events)
	}
	got := events[0]
	if got.ID == 0 || got.At.IsZero() {
		t.Fatalf("evento = %+v, quiero ID e instante asignados", got)
	}
	if got.TenantID == nil || *got.TenantID != m.TenantA || got.Actor != want.Actor || got.Action != want.Action ||
		got.Resource != want.Resource || got.Result != want.Result || len(got.Meta) != 1 || got.Meta["endpoint"] != "/contract" {
		t.Fatalf("evento = %+v, quiero lo registrado %+v", got, want)
	}
}

func auditAppendOnly(t *testing.T, m MontajeAuditRepo) {
	e := auditEvent(ptr(m.TenantA), "contract.twice")
	mustRecord(t, m.Repo, e, e)
	events, err := m.Repo.List(bg(), m.TenantA, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 || events[0].ID == events[1].ID {
		t.Fatalf("List = %+v, quiero DOS filas con ID distinto: la bitácora no deduplica", events)
	}
}

func auditMostRecentFirst(t *testing.T, m MontajeAuditRepo) {
	mustRecord(t, m.Repo, auditEvent(ptr(m.TenantA), "e1"), auditEvent(ptr(m.TenantA), "e2"), auditEvent(ptr(m.TenantA), "e3"))
	if got := listActions(t, m.Repo, m.TenantA, 10, 0); fmt.Sprint(got) != "[e3 e2 e1]" {
		t.Fatalf("List = %v, quiero [e3 e2 e1] (más recientes primero)", got)
	}
}

func auditLimitOffset(t *testing.T, m MontajeAuditRepo) {
	for i := 1; i <= 5; i++ {
		mustRecord(t, m.Repo, auditEvent(ptr(m.TenantA), fmt.Sprintf("e%d", i)))
	}
	for _, tc := range []struct {
		limit, offset int
		want          string
	}{
		{2, 0, "[e5 e4]"},
		{2, 1, "[e4 e3]"},
		{10, 3, "[e2 e1]"},
		{10, 5, "[]"},
		{10, 50, "[]"},
	} {
		if got := listActions(t, m.Repo, m.TenantA, tc.limit, tc.offset); fmt.Sprint(got) != tc.want {
			t.Fatalf("List(limit %d, offset %d) = %v, quiero %s", tc.limit, tc.offset, got, tc.want)
		}
	}
}

func auditScoped(t *testing.T, m MontajeAuditRepo) {
	mustRecord(t, m.Repo,
		auditEvent(ptr(m.TenantA), "mine"),
		auditEvent(ptr(m.TenantB), "foreign"),
		auditEvent(nil, "pre-auth"),
	)
	if got := listActions(t, m.Repo, m.TenantA, 10, 0); fmt.Sprint(got) != "[mine]" {
		t.Fatalf("List(A) = %v, quiero solo [mine]: ni eventos de otro tenant ni los pre-auth", got)
	}
	if got := listActions(t, m.Repo, m.TenantB, 10, 0); fmt.Sprint(got) != "[foreign]" {
		t.Fatalf("List(B) = %v, quiero solo [foreign]", got)
	}
}
