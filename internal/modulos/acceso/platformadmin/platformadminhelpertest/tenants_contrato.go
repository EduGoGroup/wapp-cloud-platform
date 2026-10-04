package platformadminhelpertest

// Parte de contrato.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): los casos de TenantStore (tenants e instalaciones) y sus helpers.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

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
