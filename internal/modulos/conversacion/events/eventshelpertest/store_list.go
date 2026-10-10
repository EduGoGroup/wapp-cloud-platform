package eventshelpertest

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// La mitad del doble que imita LA consulta de rescatables y el listado del dueño.

// rescuable resuelve la fila de la consulta para un evento: la marca «vencido» y el contenido
// derivado de la vista. Requiere el mutex tomado.
//
// «Vencido» es la misma expresión del SQL: el TTL del tenant (7200 s sin fila) es mayor que cero
// Y el instante del reloj menos la última actividad lo supera. No mira el estado del evento.
func (st *state) rescuable(ev events.Event, now time.Time) events.Rescuable {
	ttl, ok := st.ttlSeconds[ev.TenantID]
	if !ok {
		ttl = defaultTTL
	}
	content := st.content[ev.ID]
	return events.Rescuable{
		Event:        ev,
		Stale:        ttl > 0 && now.Sub(ev.LastActivityAt) > time.Duration(ttl)*time.Second,
		ContentState: content.state,
		ContentRef:   content.ref,
	}
}

// offerable es el predicado de contenido de los rescatables (INV-17): sin contenido o con el
// contenido vivo. Lo que cuajó o murió no se lista.
func offerable(r events.Rescuable) bool {
	return r.ContentState == "" || r.ContentState == ContentAlive
}

// byLastActivity es el orden de los rescatables: última actividad descendente y, a igual
// instante, id ascendente.
func byLastActivity(a, b events.Rescuable) int {
	return cmp.Or(b.LastActivityAt.Compare(a.LastActivityAt), cmp.Compare(a.ID, b.ID))
}

// ListRescuable devuelve los eventos vivos de la conversación que se pueden ofrecer (sin contenido
// o con el contenido vivo), por última actividad descendente, cada uno con su marca «vencido» y
// su contenido derivado. limit <= 0 es sin tope.
func (s *Store) ListRescuable(_ context.Context, tenantID, sessionID, contactID string, limit int) ([]events.Rescuable, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	now := s.state.clock()
	var out []events.Rescuable
	for _, ev := range s.state.events {
		if !ev.Alive() || ev.TenantID != tenantID || ev.SessionID != sessionID || ev.ContactID != contactID {
			continue
		}
		if r := s.state.rescuable(ev, now); offerable(r) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, byLastActivity)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// matchesContent aplica el filtro de contenido del listado. Los tres valores van sobre el
// predicado de rescatables: lo que cuajó o murió no casa con ninguno.
func matchesContent(r events.Rescuable, f events.ContentFilter) bool {
	switch f {
	case events.ContentAny:
		return offerable(r)
	case events.ContentNone:
		return r.ContentState == ""
	case events.ContentAlive:
		return r.ContentState == ContentAlive
	default:
		return false
	}
}

// ListEvents devuelve la página de eventos del tenant que casan con el filtro (ya normalizado),
// por última actividad descendente, con el total de coincidencias del filtro entero.
func (s *Store) ListEvents(_ context.Context, tenantID string, f events.ListFilter) (events.EventPage, error) {
	f = f.Normalized()
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	now := s.state.clock()
	var matched []events.Rescuable
	for _, ev := range s.state.events {
		if ev.TenantID != tenantID || ev.Status != f.Status {
			continue
		}
		if f.Kind != "" && ev.Kind != f.Kind {
			continue
		}
		if f.ContactID != "" && ev.ContactID != f.ContactID {
			continue
		}
		// nil es «sin filtro»; una lista vacía es «ningún tipo pasa».
		if f.Kinds != nil && !slices.Contains(f.Kinds, ev.Kind) {
			continue
		}
		r := s.state.rescuable(ev, now)
		if !matchesContent(r, f.Content) {
			continue
		}
		if f.Stale != nil && r.Stale != *f.Stale {
			continue
		}
		matched = append(matched, r)
	}
	slices.SortFunc(matched, byLastActivity)
	page := events.EventPage{Page: f.Page, PageSize: f.PageSize, Total: len(matched)}
	if start := f.Offset(); start < len(matched) {
		page.Events = matched[start:min(start+f.PageSize, len(matched))]
	}
	return page, nil
}
