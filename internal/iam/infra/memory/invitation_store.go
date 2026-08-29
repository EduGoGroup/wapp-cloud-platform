package memory

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// InvitationStore es el doble en memoria de public.tenant_invitations (Plan 047
// · Ola A). Imita la semántica del adaptador Postgres allí donde un test podría
// darla por buena y la base no:
//
//   - el índice ÚNICO sobre token_hash → domain.ErrConflict;
//   - el UPDATE atómico condicionado de la revocación, con sus TRES desenlaces
//     (revocada, ya revocada, ya canjeada) y su 404 para la de otra empresa;
//   - el orden estable del listado (created_at DESC, id DESC).
//
// Si el doble fuera más permisivo que la tabla, los contract tests de la API
// darían verde sobre un comportamiento que en campo falla — que es justo lo que
// pasa cuando el doble se escribe «para que pase el test».
type InvitationStore struct {
	mu sync.RWMutex
	// porID conserva las filas. El orden del listado NO sale del recorrido de
	// este mapa (en Go es aleatorio a propósito) sino del sort explícito.
	porID map[string]domain.Invitation
	// siguiente es el ordinal de la próxima emisión. Desempata el listado cuando
	// dos filas comparten instante: el reloj de Go puede repetir valor entre dos
	// llamadas seguidas, y sin desempate el doble dejaría de ser determinista en
	// lo único que el puerto promete. El adaptador Postgres resuelve el mismo
	// empate con el id.
	siguiente uint64
	orden     map[string]uint64
}

// NewInvitationStore crea el store vacío.
func NewInvitationStore() *InvitationStore {
	return &InvitationStore{porID: make(map[string]domain.Invitation), orden: make(map[string]uint64), siguiente: 1}
}

var _ out.InvitationRepo = (*InvitationStore)(nil)

// Create implementa out.InvitationRepo. Asigna id y created_at como haría la
// base con sus defaults.
func (s *InvitationStore) Create(_ context.Context, inv domain.Invitation) (domain.Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.porID {
		if bytes.Equal(ex.TokenHash, inv.TokenHash) {
			return domain.Invitation{}, fmt.Errorf("%w: ya existe una invitación con ese digest", domain.ErrConflict)
		}
	}
	inv.ID = uuid.NewString()
	inv.CreatedAt = time.Now().UTC()
	s.porID[inv.ID] = inv
	s.orden[inv.ID] = s.siguiente
	s.siguiente++
	return inv, nil
}

// Seed inserta una invitación ya formada (helper de tests) para fabricar el
// estado de partida: una vencida, una canjeada, una revocada. A diferencia de
// Create NO comprueba el digest duplicado ni pisa el id si viene puesto.
func (s *InvitationStore) Seed(inv domain.Invitation) domain.Invitation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv.ID == "" {
		inv.ID = uuid.NewString()
	}
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = time.Now().UTC()
	}
	s.porID[inv.ID] = inv
	s.orden[inv.ID] = s.siguiente
	s.siguiente++
	return inv
}

// ListByTenant implementa out.InvitationRepo: las invitaciones de UNA empresa,
// las más recientes primero.
func (s *InvitationStore) ListByTenant(_ context.Context, tenantID string) ([]domain.Invitation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	filas := make([]domain.Invitation, 0)
	for _, inv := range s.porID {
		if inv.TenantID == tenantID {
			filas = append(filas, inv)
		}
	}
	sort.Slice(filas, func(i, j int) bool {
		if !filas[i].CreatedAt.Equal(filas[j].CreatedAt) {
			return filas[i].CreatedAt.After(filas[j].CreatedAt)
		}
		return s.orden[filas[i].ID] > s.orden[filas[j].ID]
	})
	return filas, nil
}

// Revoke implementa out.InvitationRepo con los MISMOS tres desenlaces que el
// UPDATE condicionado de Postgres. El candado de escritura hace aquí el papel de
// la atomicidad de la base: la comprobación y la escritura no se pueden entrelazar
// con otra revocación ni con un canje.
func (s *InvitationStore) Revoke(_ context.Context, id, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.porID[id]
	if !ok || inv.TenantID != tenantID {
		// No existe, o es de otra empresa: mismo código, no se confirma que exista
		// fuera.
		return domain.ErrNotFound
	}
	if inv.RedeemedAt != nil {
		return fmt.Errorf("%w: la invitación ya fue canjeada y revocarla no deshace la membresía", domain.ErrConflict)
	}
	if inv.RevokedAt != nil {
		return nil // idempotente
	}
	ahora := time.Now().UTC()
	inv.RevokedAt = &ahora
	s.porID[id] = inv
	return nil
}

// Get devuelve una fila por id (helper de tests): es como se comprueba que la
// revocación quedó ESCRITA en vez de deducirlo de la respuesta HTTP.
func (s *InvitationStore) Get(id string) (domain.Invitation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.porID[id]
	return inv, ok
}
