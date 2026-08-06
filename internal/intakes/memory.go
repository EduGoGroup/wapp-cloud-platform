package intakes

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore es un Store en memoria para tests. Reproduce las MISMAS semánticas
// que el store Postgres —rango [From, To), variantes legadas del estado, orden por
// created_at descendente con desempate por id, paginación y total sin paginar— para
// que un test de handler contra este store diga algo verdadero sobre producción.
// Guarda el estado EN CRUDO (como la BD, con su `closed` legado) y normaliza al
// leer: así el camino de normalización se ejercita de verdad.
type MemoryStore struct {
	mu        sync.Mutex
	rows      map[string][]row // por tenant
	items     map[string][]Item
	revisions map[string][]Revision     // por solicitud, en orden de escritura
	zones     map[string][]ShippingZone // por tenant; imita tenant_settings.shipping_zones
}

// row es una solicitud almacenada con su estado tal cual (sin normalizar).
type row struct {
	intake Intake
	status string
}

// NewMemoryStore construye un store en memoria vacío.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		rows:      map[string][]row{},
		items:     map[string][]Item{},
		revisions: map[string][]Revision{},
		zones:     map[string][]ShippingZone{},
	}
}

// SetShippingZones siembra las zonas de envío del tenant, como haría un UPDATE de
// tenant_settings.shipping_zones. Sin llamarla, el tenant no tiene zonas — que es
// el DEFAULT de la columna y el estado de arranque de cualquier tenant real.
func (m *MemoryStore) SetShippingZones(tenantID string, zones ...ShippingZone) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.zones[tenantID] = slices.Clone(zones)
}

// SetShippingPrice precifica a mano la línea de envío de una solicitud y cuadra el
// total, que es lo que hará el dueño desde la consola cuando le ponga precio a un
// «Envío por confirmar» (D-041.11: v1, el dueño precifica). Es un mutador para los
// tests —como Add—, no parte de ningún puerto. Sin línea de envío no hace nada.
func (m *MemoryStore) SetShippingPrice(tenantID, intakeID string, price float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		for j, it := range m.items[intakeID] {
			if it.SKU != ShippingSKU {
				continue
			}
			m.items[intakeID][j].UnitPrice = price
			m.recomputeTotalLocked(tenantID, i, intakeID)
			return
		}
		return
	}
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

	matched := m.matching(tenantID, f)
	total := len(matched)
	start := f.Offset()
	if start >= total {
		return []Intake{}, total, nil
	}
	end := min(start+f.PageSize, total)
	return matched[start:end], total, nil
}

// ListDetails implementa Store: el MISMO predicado y orden que List —comparten
// `matching`, así que no pueden divergir— sin paginar, acotado a `limit`, con las
// líneas de cada solicitud colgadas.
func (m *MemoryStore) ListDetails(_ context.Context, tenantID string, f Filter, limit int) ([]Detail, error) {
	if limit <= 0 {
		return []Detail{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Ojo con Normalized(): acota PageSize a MaxPageSize, y la cota del export es
	// otra (MaxExportIntakes). Aquí solo interesa la normalización del ESTADO, así
	// que la paginación se descarta y el corte lo hace `limit`.
	matched := m.matching(tenantID, f.Normalized())
	if len(matched) > limit {
		matched = matched[:limit]
	}

	out := make([]Detail, 0, len(matched))
	for _, in := range matched {
		items := slices.Clone(m.items[in.ID])
		if items == nil {
			items = []Item{}
		}
		out = append(out, Detail{Intake: in, Items: items})
	}
	return out, nil
}

// matching filtra y ordena las solicitudes del tenant SIN paginar. Reproduce el
// predicado del store Postgres: rango [From, To), variantes legadas del estado,
// sesión exacta, más recientes primero con desempate por id. El llamante tiene el
// candado tomado.
func (m *MemoryStore) matching(tenantID string, f Filter) []Intake {
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
	return matched
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
		return Detail{
			Intake:    in,
			Items:     slices.Clone(m.items[intakeID]),
			Revisions: slices.Clone(m.revisions[intakeID]),
		}, nil
	}
	return Detail{}, ErrNotFound
}

// InsertRevision implementa RevisionWriter con la MISMA numeración que el store
// Postgres: el siguiente correlativo de esa solicitud, 1 para la primera.
//
// No comprueba que la solicitud exista: la FK de la tabla real sí lo hace, pero un
// store de tests que exigiera sembrar la cabecera obligaría a montar media bandeja
// para probar una revisión suelta.
func (m *MemoryStore) InsertRevision(_ context.Context, rev Revision) (Revision, error) {
	if len(rev.Payload) == 0 {
		return Revision{}, ErrEmptyRevisionPayload
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rev.RevisionNo = len(m.revisions[rev.IntakeID]) + 1
	if rev.CreatedAt.IsZero() {
		rev.CreatedAt = time.Now()
	}
	m.revisions[rev.IntakeID] = append(m.revisions[rev.IntakeID], rev)
	return rev, nil
}

// Revisions devuelve las revisiones sembradas/escritas para una solicitud, en
// orden. Es un mirador para los tests, no parte de ningún puerto.
func (m *MemoryStore) Revisions(intakeID string) []Revision {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.revisions[intakeID])
}

// UpdateStatus implementa Store con el mismo compare-and-swap que el Postgres —y,
// como él, deja puesta la línea de envío al entrar a `pending_approval` (D-041.11).
// Esa paridad es la razón de ser de este store: un test de handler contra él solo
// dice algo verdadero sobre producción si las dos escrituras van juntas aquí
// también.
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
		if NormalizeStatus(to) == StatusPendingApproval {
			if _, err := m.ensureShippingLocked(tenantID, i, ShippingAlways); err != nil {
				return Intake{}, err
			}
		}
		updated := m.rows[tenantID][i].intake
		updated.Status = NormalizeStatus(to)
		return updated, nil
	}
	return Intake{}, ErrNotFound
}

// EnsureShippingLine implementa Store con las MISMAS reglas que el Postgres: una
// sola línea de envío, la política decide si se materializa, y el total de la
// cabecera queda cuadrado con las líneas.
func (m *MemoryStore) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		_, err := m.ensureShippingLocked(tenantID, i, policy)
		return err
	}
	return ErrNotFound
}

// ensureShippingLocked es el cuerpo compartido por UpdateStatus y
// EnsureShippingLine; el llamante tiene el candado tomado y ya resolvió la fila
// (índice `i` dentro de m.rows[tenantID]).
func (m *MemoryStore) ensureShippingLocked(tenantID string, i int, policy ShippingPolicy) (bool, error) {
	zones := m.zones[tenantID]
	if !policy.applies(zones) {
		return false, nil
	}
	desired := DesiredShippingLine(zones)

	intakeID := m.rows[tenantID][i].intake.ID
	items := m.items[intakeID]
	for j, it := range items {
		if it.SKU != ShippingSKU {
			continue
		}
		if !desired.Supersedes(it) {
			return false, nil
		}
		line := desired.item()
		items[j].Label, items[j].Qty, items[j].UnitPrice = line.Label, line.Qty, line.UnitPrice
		m.recomputeTotalLocked(tenantID, i, intakeID)
		return true, nil
	}

	line := desired.item()
	line.AddedAt = time.Now()
	m.items[intakeID] = append(items, line)
	m.recomputeTotalLocked(tenantID, i, intakeID)
	return true, nil
}

// recomputeTotalLocked cuadra el total de la cabecera con la suma de sus líneas,
// igual que el UPDATE del store Postgres.
func (m *MemoryStore) recomputeTotalLocked(tenantID string, i int, intakeID string) {
	var total float64
	for _, it := range m.items[intakeID] {
		total += float64(it.Qty) * it.UnitPrice
	}
	m.rows[tenantID][i].intake.Total = total
}
