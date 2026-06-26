package lease

import (
	"context"
	"sync"
	"time"
)

// State es el estado de autorización persistido de un Edge (refleja una fila de
// public.leases). No contiene la DEK ni el blob firmado: solo metadatos.
type State struct {
	TenantID  string
	EdgeID    string
	Counter   int64
	ExpiresAt time.Time
	Revoked   bool
	IssuedAt  time.Time
	UpdatedAt time.Time
}

// Repository persiste el estado del lease por Edge. La clave lógica es
// (TenantID, EdgeID). Implementaciones: MemoryRepository (unit CI-safe) y
// PostgresRepository (integración).
type Repository interface {
	// Upsert registra una emisión/renovación vigente (revoked=false) con el
	// counter y la expiración dados.
	Upsert(ctx context.Context, s State) error
	// MarkRevoked marca el lease del Edge como revocado (pegajoso) conservando el
	// counter; crea la fila si no existía.
	MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error
	// Get devuelve el estado del Edge y si existe. found=false sin error si no hay
	// fila.
	Get(ctx context.Context, tenantID, edgeID string) (state State, found bool, err error)
}

// MemoryRepository es una implementación en memoria de Repository, segura
// para concurrencia. Pensada para tests unitarios CI-safe (sin BD).
type MemoryRepository struct {
	mu     sync.Mutex
	leases map[string]State
	now    func() time.Time
}

// NewMemoryRepository crea un repositorio en memoria vacío con reloj wall-clock.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{leases: make(map[string]State), now: time.Now}
}

func memKey(tenantID, edgeID string) string { return tenantID + "\x00" + edgeID }

// Upsert implementa Repository.
func (r *MemoryRepository) Upsert(_ context.Context, s State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(s.TenantID, s.EdgeID)
	prev, ok := r.leases[key]
	s.Revoked = false
	s.UpdatedAt = now
	if ok {
		s.IssuedAt = prev.IssuedAt
	} else {
		s.IssuedAt = now
	}
	r.leases[key] = s
	return nil
}

// MarkRevoked implementa Repository.
func (r *MemoryRepository) MarkRevoked(_ context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID)
	s, ok := r.leases[key]
	if !ok {
		s = State{TenantID: tenantID, EdgeID: edgeID, IssuedAt: now}
	}
	s.Revoked = true
	s.ExpiresAt = expiresAt
	s.UpdatedAt = now
	r.leases[key] = s
	return nil
}

// Get implementa Repository.
func (r *MemoryRepository) Get(_ context.Context, tenantID, edgeID string) (State, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.leases[memKey(tenantID, edgeID)]
	return s, ok, nil
}
