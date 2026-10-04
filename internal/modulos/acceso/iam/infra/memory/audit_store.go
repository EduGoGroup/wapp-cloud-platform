// Porta internal/iam/infra/memory/audit_store.go @ 9a77307

package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// AuditStore es el doble en memoria de audit_events. Pasa outhelpertest.ContratoAuditRepo.
type AuditStore struct {
	mu     sync.Mutex
	events []domain.AuditEvent
	seq    int64
	// now es el reloj con que se fecha un evento que llega sin instante (el DEFAULT now() de la
	// columna at). Por defecto time.Now; WithClock lo sustituye para que los tests no dependan
	// del reloj real.
	now func() time.Time
}

// NewAuditStore crea un AuditStore vacío, con el reloj real.
func NewAuditStore() *AuditStore { return &AuditStore{now: time.Now} }

// WithClock sustituye el reloj del store y lo devuelve, para encadenarlo tras el constructor
// (antes de usarlo: no es seguro cambiar el reloj con el store en uso). Un now nil deja el que
// tenía.
func (s *AuditStore) WithClock(now func() time.Time) *AuditStore {
	if now != nil {
		s.now = now
	}
	return s
}

var _ out.AuditRepo = (*AuditStore)(nil)

// Record implementa out.AuditRepo: append-only, con el ID siguiente y el instante del reloj si
// el evento no trae uno.
//
// ⚠️ Conserva un At no cero tal como llega, y ahí se aparta de Postgres, que fecha SIEMPRE con
// now(): es la conducta del doble viejo y ningún test de la suite depende de ella.
func (s *AuditStore) Record(_ context.Context, e domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.ID = s.seq
	if e.At.IsZero() {
		e.At = s.now()
	}
	s.events = append(s.events, e)
	return nil
}

// List implementa out.AuditRepo: los del tenant (nunca los pre-auth, sin tenant), del último
// registrado al primero, paginados. Más allá del final, nil.
func (s *AuditStore) List(_ context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var filtered []domain.AuditEvent
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.TenantID != nil && *e.TenantID == tenantID {
			filtered = append(filtered, e)
		}
	}
	if offset >= len(filtered) {
		return nil, nil
	}
	end := min(offset+limit, len(filtered))
	return filtered[offset:end], nil
}

// Events devuelve todos los eventos registrados, en orden de registro, también los pre-auth
// (inspección en tests). Es una copia.
func (s *AuditStore) Events() []domain.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.AuditEvent(nil), s.events...)
}
