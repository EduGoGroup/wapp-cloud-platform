package memory

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// AuditStore es el doble en memoria de audit_events.
type AuditStore struct {
	mu     sync.Mutex
	events []domain.AuditEvent
	seq    int64
}

// NewAuditStore crea un AuditStore vacío.
func NewAuditStore() *AuditStore { return &AuditStore{} }

var _ out.AuditRepo = (*AuditStore)(nil)

// Record implementa out.AuditRepo.
func (s *AuditStore) Record(_ context.Context, e domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.ID = s.seq
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.events = append(s.events, e)
	return nil
}

// List implementa out.AuditRepo (más recientes primero, paginado).
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
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], nil
}

// Events devuelve todos los eventos registrados (inspección en tests).
func (s *AuditStore) Events() []domain.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.AuditEvent(nil), s.events...)
}
