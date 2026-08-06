package intakes

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// MemoryStore es un Store en memoria para tests. Reproduce las MISMAS semánticas
// que el store Postgres —rango [From, To), variantes legadas del estado, orden por
// created_at descendente con desempate por id, paginación y total sin paginar— para
// que un test de handler contra este store diga algo verdadero sobre producción.
// Guarda el estado EN CRUDO (como la BD, con su `closed` legado) y normaliza al
// leer: así el camino de normalización se ejercita de verdad.
type MemoryStore struct {
	mu    sync.Mutex
	rows  map[string][]row // por tenant
	items map[string][]Item
}

// row es una solicitud almacenada con su estado tal cual (sin normalizar).
type row struct {
	intake Intake
	status string
}

// NewMemoryStore construye un store en memoria vacío.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string][]row{}, items: map[string][]Item{}}
}

// Add siembra una solicitud del tenant con sus líneas. `in.Status` se guarda tal
// cual (puede ser la clave legada `closed`).
func (m *MemoryStore) Add(tenantID string, in Intake, items ...Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[tenantID] = append(m.rows[tenantID], row{intake: in, status: in.Status})
	if len(items) > 0 {
		m.items[in.ID] = append(m.items[in.ID], items...)
	}
}

// List implementa Store.
func (m *MemoryStore) List(_ context.Context, tenantID string, f Filter) ([]Intake, int, error) {
	f = f.Normalized()
	m.mu.Lock()
	defer m.mu.Unlock()

	var variants []string
	if f.Status != "" {
		variants = StoredVariants(f.Status)
	}

	matched := make([]Intake, 0, len(m.rows[tenantID]))
	for _, r := range m.rows[tenantID] {
		if !f.From.IsZero() && r.intake.CreatedAt.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && !r.intake.CreatedAt.Before(f.To) {
			continue // To es EXCLUSIVO, igual que el "< $3" del SQL
		}
		if variants != nil && !slices.Contains(variants, r.status) {
			continue
		}
		if f.SessionID != "" && r.intake.SessionID != f.SessionID {
			continue
		}
		in := r.intake
		in.Status = NormalizeStatus(r.status)
		matched = append(matched, in)
	}

	slices.SortFunc(matched, func(a, b Intake) int {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return b.CreatedAt.Compare(a.CreatedAt) // más recientes primero
		}
		return strings.Compare(b.ID, a.ID)
	})

	total := len(matched)
	start := f.Offset()
	if start >= total {
		return []Intake{}, total, nil
	}
	end := min(start+f.PageSize, total)
	return matched[start:end], total, nil
}

// Get implementa Store.
func (m *MemoryStore) Get(_ context.Context, tenantID, intakeID string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		in := r.intake
		in.Status = NormalizeStatus(r.status)
		return Detail{Intake: in, Items: slices.Clone(m.items[intakeID])}, nil
	}
	return Detail{}, ErrNotFound
}

// UpdateStatus implementa Store con el mismo compare-and-swap que el Postgres.
func (m *MemoryStore) UpdateStatus(_ context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		if !slices.Contains(expected, r.status) {
			return Intake{}, ErrConflict
		}
		m.rows[tenantID][i].status = to
		m.rows[tenantID][i].intake.Status = to
		updated := m.rows[tenantID][i].intake
		updated.Status = NormalizeStatus(to)
		return updated, nil
	}
	return Intake{}, ErrNotFound
}
