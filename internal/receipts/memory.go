package receipts

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore es la implementación en memoria de Store (tests y arranques sin
// BD). Dedupe por la MISMA clave que Postgres: (session_id, message_id, status).
type MemoryStore struct {
	mu   sync.Mutex
	seq  int64
	rows map[string]Stored // clave dedupe → fila
}

// NewMemoryStore construye el store en memoria vacío.
func NewMemoryStore() *MemoryStore { return &MemoryStore{rows: map[string]Stored{}} }

var _ Store = (*MemoryStore)(nil)

func dedupeKey(r Receipt) string {
	return r.SessionID + "\x00" + r.MessageID + "\x00" + string(r.Status)
}

// Save persiste idempotente: repetir el mismo acuse refresca la fila sin crear
// una nueva (mismo comportamiento que ON CONFLICT DO UPDATE en Postgres).
func (s *MemoryStore) Save(_ context.Context, r Receipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := dedupeKey(r)
	now := time.Now().UTC()
	if existing, ok := s.rows[key]; ok {
		existing.CommandID = r.CommandID
		existing.ReceiptAt = r.ReceiptAt
		existing.RecordedAt = now
		s.rows[key] = existing
		return nil
	}
	s.seq++
	s.rows[key] = Stored{
		Receipt:    r,
		ID:         s.seq,
		RecordedAt: now,
	}
	return nil
}

// List devuelve los acuses de la sesión, más recientes primero, paginados.
func (s *MemoryStore) List(_ context.Context, sessionID string, limit, offset int) ([]Stored, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var all []Stored
	for _, v := range s.rows {
		if v.SessionID == sessionID {
			all = append(all, v)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].RecordedAt.Equal(all[j].RecordedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].RecordedAt.After(all[j].RecordedAt)
	})
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}
