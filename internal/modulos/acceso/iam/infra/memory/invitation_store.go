// Porta internal/iam/infra/memory/invitation_store.go @ 9a77307

package memory

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// invitationDigestSize es lo que mide un token_hash válido: el CHECK
// tenant_invitations_token_hash_len_check de la migración 0085 (SHA-256, 32 bytes).
const invitationDigestSize = 32

// InvitationStore es el doble en memoria de public.tenant_invitations (Plan 047
// · Ola A). Imita la semántica del adaptador Postgres allí donde un test podría
// darla por buena y la base no:
//
//   - el CHECK de 32 bytes del digest → error (no un centinela, como el de la base);
//   - el índice ÚNICO sobre token_hash → domain.ErrConflict;
//   - el UPDATE atómico condicionado de la revocación, con sus TRES desenlaces
//     (revocada, ya revocada, ya canjeada) y su 404 para la de otra empresa;
//   - el orden estable del listado (created_at DESC, id DESC);
//   - la FK role_id ON DELETE SET NULL (DetachRole).
//
// Si el doble fuera más permisivo que la tabla, los contract tests de la API
// darían verde sobre un comportamiento que en campo falla — que es justo lo que
// pasa cuando el doble se escribe «para que pase el test». Pasa
// outhelpertest.ContratoInvitationRepo, la misma suite que el adaptador Postgres.
type InvitationStore struct {
	mu sync.RWMutex
	// byID conserva las filas (era porID). El orden del listado NO sale del
	// recorrido de este mapa (en Go es aleatorio a propósito) sino del sort
	// explícito.
	byID map[string]domain.Invitation
	// now es el reloj de la «base»: created_at, revoked_at y, en el canje
	// (RedeemStore), el instante que juzga la caducidad y fecha redeemed_at. Por
	// defecto time.Now; WithClock lo sustituye.
	now func() time.Time
}

// NewInvitationStore crea el store vacío, con el reloj real.
func NewInvitationStore() *InvitationStore {
	return &InvitationStore{byID: make(map[string]domain.Invitation), now: time.Now}
}

// WithClock sustituye el reloj del store y lo devuelve, para encadenarlo tras el constructor
// (antes de usarlo: no es seguro cambiar el reloj con el store en uso). Un now nil deja el que
// tenía.
func (s *InvitationStore) WithClock(now func() time.Time) *InvitationStore {
	if now != nil {
		s.now = now
	}
	return s
}

var _ out.InvitationRepo = (*InvitationStore)(nil)

// Create implementa out.InvitationRepo. Asigna id y created_at como haría la
// base con sus defaults; el resto de la fila va como viene. Un digest que no mide
// 32 bytes es un error (el CHECK de la tabla) y uno repetido, domain.ErrConflict
// (el índice único); ninguno escribe.
func (s *InvitationStore) Create(_ context.Context, inv domain.Invitation) (domain.Invitation, error) {
	if len(inv.TokenHash) != invitationDigestSize {
		return domain.Invitation{}, fmt.Errorf("iam: emitir invitación: el digest mide %d bytes y la tabla exige %d (tenant_invitations_token_hash_len_check)",
			len(inv.TokenHash), invitationDigestSize)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.byID {
		if bytes.Equal(ex.TokenHash, inv.TokenHash) {
			return domain.Invitation{}, fmt.Errorf("%w: ya existe una invitación con ese digest", domain.ErrConflict)
		}
	}
	inv.ID = uuid.NewString()
	inv.CreatedAt = s.now().UTC()
	inv.TokenHash = bytes.Clone(inv.TokenHash)
	s.byID[inv.ID] = inv
	return inv, nil
}

// Seed inserta una invitación ya formada (helper de tests) para fabricar el
// estado de partida: una vencida, una canjeada, una revocada, dos con el mismo
// created_at. A diferencia de Create NO comprueba el digest (ni su tamaño ni que
// se repita) ni pisa el id ni el created_at si vienen puestos; vacíos, los pone
// como Create. Devuelve la fila escrita.
func (s *InvitationStore) Seed(inv domain.Invitation) domain.Invitation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv.ID == "" {
		inv.ID = uuid.NewString()
	}
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = s.now().UTC()
	}
	s.byID[inv.ID] = inv
	return inv
}

// ListByTenant implementa out.InvitationRepo: las invitaciones de UNA empresa,
// las más recientes primero y, con el mismo created_at, el id mayor primero —el
// mismo ORDER BY created_at DESC, id DESC del adaptador Postgres—. Vacía, no nil.
//
// 🔧 El doble viejo desempataba por un ordinal de emisión («el reloj de Go puede
// repetir valor entre dos llamadas seguidas»). El puerto promete id DESC y la
// suite lo afirma con dos filas del mismo instante, así que el doble desempata
// ahora como la base; el reloj inyectable cubre lo que el ordinal cubría.
func (s *InvitationStore) ListByTenant(_ context.Context, tenantID string) ([]domain.Invitation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]domain.Invitation, 0)
	for _, inv := range s.byID {
		if inv.TenantID == tenantID {
			rows = append(rows, inv)
		}
	}
	slices.SortFunc(rows, func(a, b domain.Invitation) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return rows, nil
}

// Revoke implementa out.InvitationRepo con los MISMOS tres desenlaces que el
// UPDATE condicionado de Postgres. El candado de escritura hace aquí el papel de
// la atomicidad de la base: la comprobación y la escritura no se pueden entrelazar
// con otra revocación ni con un canje (RedeemStore toma el mismo candado).
func (s *InvitationStore) Revoke(_ context.Context, id, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.byID[id]
	if !ok || inv.TenantID != tenantID {
		// No existe, o es de otra empresa: mismo código, no se confirma que exista
		// fuera.
		return domain.ErrNotFound
	}
	if inv.RedeemedAt != nil {
		return fmt.Errorf("%w: la invitación ya fue canjeada y revocarla no deshace la membresía", domain.ErrConflict)
	}
	if inv.RevokedAt != nil {
		return nil // idempotente: se queda la primera marca
	}
	now := s.now().UTC()
	inv.RevokedAt = &now
	s.byID[id] = inv
	return nil
}

// Get devuelve una fila por id (helper de tests): es como se comprueba que la
// revocación quedó ESCRITA en vez de deducirlo de la respuesta HTTP.
func (s *InvitationStore) Get(id string) (domain.Invitation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.byID[id]
	return inv, ok
}

// DetachRole reproduce el borrado del rol roleID visto desde esta tabla (helper de
// tests): la FK role_id es ON DELETE SET NULL, así que las invitaciones que lo
// prometían siguen vivas y pasan a no prometer ninguno. El doble no tiene tabla
// de roles: quien borra el rol en su test llama a esto.
func (s *InvitationStore) DetachRole(roleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, inv := range s.byID {
		if inv.RoleID != nil && *inv.RoleID == roleID {
			inv.RoleID = nil
			s.byID[id] = inv
		}
	}
}
