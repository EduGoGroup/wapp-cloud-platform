// Package fleet lleva el registro durable del estado online/offline de las
// sesiones CloudLink de cada Edge (tabla public.fleet_sessions). El estado es
// DERIVADO del stream vivo: el Gateway marca online al conectar una sesión y
// offline al caer. La fuente viva (para empujar comandos) está en memoria en
// session.Registry; esta capa solo durabiliza el estado para auditoría/admin.
//
// Repository tiene impl memory (unit CI-safe) y postgres (integración).
package fleet

import (
	"context"
	"sync"
	"time"
)

// State es el conjunto de estados posibles de una sesión.
type State string

const (
	// StateOnline indica que el stream de la sesión está vivo.
	StateOnline State = "online"
	// StateOffline indica que el stream de la sesión cayó.
	StateOffline State = "offline"
)

// Session refleja una fila de public.fleet_sessions. Capabilities se omite a
// propósito: el contrato CloudLink v0.1.0 no transporta capacidades aún.
type Session struct {
	TenantID        string
	EdgeID          string
	SessionID       string
	State           State
	LastConnectedAt time.Time
	LastSeenAt      time.Time
}

// Repository persiste el estado de las sesiones. La clave lógica es
// (TenantID, EdgeID, SessionID).
type Repository interface {
	// MarkOnline registra/actualiza la sesión como online (last_connected_at y
	// last_seen_at = ahora).
	MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error
	// MarkOffline marca la sesión como offline (last_seen_at = ahora). No falla si
	// la sesión no existía.
	MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error
	// Get devuelve la sesión y si existe.
	Get(ctx context.Context, tenantID, edgeID, sessionID string) (s Session, found bool, err error)
	// List devuelve las sesiones de un tenant (para tests/diagnóstico).
	List(ctx context.Context, tenantID string) ([]Session, error)
}

// MemoryRepository es una implementación en memoria de Repository, segura
// para concurrencia. Pensada para tests unitarios CI-safe (sin BD).
type MemoryRepository struct {
	mu       sync.Mutex
	sessions map[string]Session
	now      func() time.Time
}

// NewMemoryRepository crea un repositorio en memoria vacío con reloj wall-clock.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{sessions: make(map[string]Session), now: time.Now}
}

func memKey(tenantID, edgeID, sessionID string) string {
	return tenantID + "\x00" + edgeID + "\x00" + sessionID
}

// MarkOnline implementa Repository.
func (r *MemoryRepository) MarkOnline(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.sessions[memKey(tenantID, edgeID, sessionID)] = Session{
		TenantID:        tenantID,
		EdgeID:          edgeID,
		SessionID:       sessionID,
		State:           StateOnline,
		LastConnectedAt: now,
		LastSeenAt:      now,
	}
	return nil
}

// MarkOffline implementa Repository.
func (r *MemoryRepository) MarkOffline(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		s = Session{TenantID: tenantID, EdgeID: edgeID, SessionID: sessionID}
	}
	s.State = StateOffline
	s.LastSeenAt = now
	r.sessions[key] = s
	return nil
}

// Get implementa Repository.
func (r *MemoryRepository) Get(_ context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[memKey(tenantID, edgeID, sessionID)]
	return s, ok, nil
}

// List implementa Repository.
func (r *MemoryRepository) List(_ context.Context, tenantID string) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.TenantID == tenantID {
			out = append(out, s)
		}
	}
	return out, nil
}
