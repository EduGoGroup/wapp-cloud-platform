package degradationhelpertest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
)

// Los techos de la página de List, los mismos que aplica el adaptador Postgres: sin límite (o
// con uno <= 0) son 50 avisos; por encima de 200 se recorta en silencio.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Memoria es una implementación en memoria de degradation.Store, segura para concurrencia. Es
// NUEVA: el paquete viejo no tenía doble (es uno de los doce de 05 E-6) y su test se escribía un
// `storeFalso` propio. Pensada para tests unitarios sin BD, y cumple la misma suite que
// degradation.Postgres (Contrato).
//
// Reproduce el ARBITRIO del índice único ux_owner_degradation_notices_ventana de la 0075: la
// clave del dedupe es (tenant, motivo, vía, inicio de ventana en UTC). Y espeja al adaptador en
// lo que el puerto deja ver: el contador que sube de uno en uno, el last_seen_at que no
// retrocede, el nacimiento que es el del primer fallo, el LastSeenAt cero que se toma como el
// fin de la ventana, y el orden y los techos de List.
//
// Como el puerto, NO valida el vocabulario: guarda el motivo y la vía que le den. Lo que en
// Postgres rechazarían los CHECK de la tabla aquí entra; quien custodia el vocabulario es
// degradation.Notifier, y por eso el doble CUENTA las llamadas (Saves): es lo que deja afirmar
// que un motivo sano no llegó ni a intentarse (R4.5.b).
type Memoria struct {
	mu    sync.Mutex
	rows  map[memoryKey]*degradation.Notice
	saves int
}

// memoryKey son las cuatro columnas del índice único. El inicio de la ventana va como instante
// (nanosegundos Unix), no como time.Time: dos time.Time del mismo instante en zonas distintas
// no son la misma clave de mapa.
type memoryKey struct {
	tenant      string
	reason      degradation.Reason
	via         string
	windowStart int64
}

// NewMemoria crea un almacén en memoria vacío.
func NewMemoria() *Memoria {
	return &Memoria{rows: make(map[memoryKey]*degradation.Notice)}
}

// Save implementa degradation.Store: crea el aviso o lo colapsa sobre el que ya cubre esa
// ventana, y cuenta la llamada. No falla nunca.
func (m *Memoria) Save(_ context.Context, n degradation.Notice) (bool, error) {
	lastSeen := n.LastSeenAt
	if lastSeen.IsZero() {
		lastSeen = n.WindowEnd // el mismo relleno que el adaptador: la fila no depende del reloj
	}
	lastSeen = lastSeen.UTC()
	key := memoryKey{tenant: n.TenantID, reason: n.Reason, via: n.Via, windowStart: n.WindowStart.UTC().UnixNano()}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	if row, ok := m.rows[key]; ok {
		// El DO UPDATE: solo el contador y el último visto, que no retrocede (GREATEST).
		row.Occurrences++
		if lastSeen.After(row.LastSeenAt) {
			row.LastSeenAt = lastSeen
		}
		return false, nil
	}
	m.rows[key] = &degradation.Notice{
		// Con forma de UUID, como el gen_random_uuid() de la tabla, y en orden de alta.
		ID:          fmt.Sprintf("00000000-0000-4000-8000-%012x", len(m.rows)+1),
		TenantID:    n.TenantID,
		Reason:      n.Reason,
		Via:         n.Via,
		WindowStart: n.WindowStart.UTC(),
		WindowEnd:   n.WindowEnd.UTC(),
		Occurrences: 1,
		CreatedAt:   lastSeen, // el nacimiento es el instante del primer fallo, no el de la escritura
		LastSeenAt:  lastSeen,
	}
	return true, nil
}

// List implementa degradation.Store: los avisos del tenant, el más reciente primero (inicio de
// ventana descendente, nacimiento descendente y, a igualdad, el id), con el filtro y la página
// de f. Devuelve una lista no nil aunque esté vacía.
func (m *Memoria) List(_ context.Context, tenantID string, f degradation.ListFilter) ([]degradation.Notice, error) {
	limit, offset := f.Limit, max(f.Offset, 0)
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)

	all := m.Rows(tenantID)
	if f.SoloSinLeer {
		all = slices.DeleteFunc(all, func(n degradation.Notice) bool { return !n.ReadAt.IsZero() })
	}
	slices.SortFunc(all, func(a, b degradation.Notice) int {
		return cmp.Or(b.WindowStart.Compare(a.WindowStart), b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	out := make([]degradation.Notice, 0, limit)
	for i := offset; i < len(all) && len(out) < limit; i++ {
		out = append(out, all[i])
	}
	return out, nil
}

// Saves dice cuántas veces se llamó a Save, creara el aviso o lo colapsara. No es del puerto:
// es para los tests que afirman que el store NO se tocó (R4.5.b) o que el colapso lo hace la
// clave y no el escritor.
func (m *Memoria) Saves() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saves
}

// Rows es el observador de estado que el Montaje de la suite pide (Montaje.Rows): TODAS las
// filas del tenant, enteras, sin filtro ni página y sin orden prometido. Devuelve copias.
func (m *Memoria) Rows(tenantID string) []degradation.Notice {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]degradation.Notice, 0, len(m.rows))
	for key, row := range m.rows {
		if key.tenant == tenantID {
			out = append(out, *row)
		}
	}
	return out
}

// MarkRead pone read_at al aviso id del tenant y dice si lo encontró. El puerto no tiene esta
// operación —hoy nada marca un aviso como leído; lo pide el Plan 045/047—: existe para que la
// suite pueda probar el filtro «solo sin leer» y que Save no pisa la lectura (Montaje.MarkRead).
func (m *Memoria) MarkRead(tenantID, id string, at time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, row := range m.rows {
		if key.tenant == tenantID && row.ID == id {
			row.ReadAt = at.UTC()
			return true
		}
	}
	return false
}
