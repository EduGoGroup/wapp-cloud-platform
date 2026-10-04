// Porta internal/receipts/memory.go @ 8896f13 (MemoryStore, que pasa a este paquete por D-F3-1
// y se llama Memoria).

package receiptshelpertest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
)

// Memoria es la implementación en memoria de receipts.Store, para tests. Era
// receipts.MemoryStore. Dedupe por la MISMA clave que Postgres: (session_id,
// message_id, status). Es segura para uso concurrente.
//
// ⚠️ Diferencia con receipts.PostgresStore, a propósito: un ReceiptAt cero vuelve de
// List como cero; Postgres lo guarda NULL y lo lee como la época Unix.
type Memoria struct {
	mu   sync.Mutex
	seq  int64
	rows map[string]receipts.Stored // clave dedupe → fila
}

// NewMemoria construye el store en memoria vacío. Era receipts.NewMemoryStore.
func NewMemoria() *Memoria { return &Memoria{rows: map[string]receipts.Stored{}} }

var _ receipts.Store = (*Memoria)(nil)

func dedupeKey(r receipts.Receipt) string {
	return r.SessionID + "\x00" + r.MessageID + "\x00" + string(r.Status)
}

// Save persiste idempotente: repetir el mismo acuse refresca la fila sin crear
// una nueva (mismo comportamiento que ON CONFLICT DO UPDATE en Postgres).
func (s *Memoria) Save(_ context.Context, r receipts.Receipt) error {
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
	s.rows[key] = receipts.Stored{
		Receipt:    r,
		ID:         s.seq,
		RecordedAt: now,
	}
	return nil
}

// List devuelve los acuses de la sesión, más recientes primero, paginados.
func (s *Memoria) List(_ context.Context, sessionID string, limit, offset int) ([]receipts.Stored, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var all []receipts.Stored
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
