package platformadminhelpertest

// Parte de fake.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): State del doble (siembras y lecturas por debajo del puerto).

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// ── State ────────────────────────────────────────────────────────────────────────────────────

// SeedTenantsCreatedAt implementa State.
func (f *Fake) SeedTenantsCreatedAt(t *testing.T, n int, at time.Time) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, n)
	for range n {
		slug := "seed-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		out = append(out, f.addTenantLocked(slug, "Sembrada", nil, at))
	}
	return out
}

// SeedFleetSession implementa State. Repetir la misma sesión reemplaza su última señal. Falla el
// test si la empresa no existe (en Postgres, la clave foránea).
func (f *Fake) SeedFleetSession(t *testing.T, tenantID, edgeID, sessionID string, lastSeenAt *time.Time) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tenants[tenantID]; !ok {
		t.Fatalf("Fake.SeedFleetSession: la empresa %s no existe", tenantID)
	}
	var seen *time.Time
	if lastSeenAt != nil {
		at := *lastSeenAt
		seen = &at
	}
	f.sessions[sessionKey{tenantID, edgeID, sessionID}] = seen
}

// SeedLease implementa State. Falla el test si la empresa no existe.
func (f *Fake) SeedLease(t *testing.T, tenantID, edgeID string, revoked bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tenants[tenantID]; !ok {
		t.Fatalf("Fake.SeedLease: la empresa %s no existe", tenantID)
	}
	f.leases[edgeKey{tenantID, edgeID}] = revoked
}

// SeedMembership implementa State. Falla el test si la empresa o algún rol no existen.
func (f *Fake) SeedMembership(t *testing.T, userID, tenantID string, roleIDs ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tenants[tenantID]; !ok {
		t.Fatalf("Fake.SeedMembership: la empresa %s no existe", tenantID)
	}
	k := accessKey{userID, tenantID}
	f.members[k] = true
	for _, roleID := range roleIDs {
		if !slices.ContainsFunc(f.roles, func(r Role) bool { return r.ID == roleID }) {
			t.Fatalf("Fake.SeedMembership: el rol %s no existe", roleID)
		}
		if !slices.Contains(f.userRoles[k], roleID) {
			f.userRoles[k] = append(f.userRoles[k], roleID)
		}
	}
	slices.Sort(f.userRoles[k])
}

// Request implementa State: devuelve una COPIA de la fila.
func (f *Fake) Request(t *testing.T, requestID string) RequestRow {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.findRequestLocked(requestID)
	if r == nil {
		t.Fatalf("Fake.Request: la solicitud %s no existe", requestID)
	}
	row := *r
	row.Reason = clonePtr(r.Reason)
	row.DecidedBy = clonePtr(r.DecidedBy)
	row.DecidedAt = clonePtr(r.DecidedAt)
	return row
}

// Access implementa State.
func (f *Fake) Access(t *testing.T, userID, tenantID string) (bool, []string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	k := accessKey{userID, tenantID}
	roles := slices.Clone(f.userRoles[k])
	if roles == nil {
		roles = []string{}
	}
	return f.members[k], roles
}

// copyTenant copia la fila, punteros incluidos: quien la recibe no puede tocar el estado.
func copyTenant(it platformadmin.TenantListItem) platformadmin.TenantListItem {
	it.PlanID = clonePtr(it.PlanID)
	it.RevokedAt = clonePtr(it.RevokedAt)
	return it
}

func clonePtr[V any](p *V) *V {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
