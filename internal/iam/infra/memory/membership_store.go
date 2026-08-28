package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// membresia es una fila de tenant_members en el doble. Guarda el instante de
// alta (columna created_at) y un ORDINAL monótono.
//
// El ordinal no es redundante con el instante: dos altas seguidas pueden caer en
// el mismo `time.Now()` según la resolución del reloj del sistema, y entonces
// ordenar solo por tiempo dejaría el orden a merced del recorrido del mapa —que
// en Go es aleatorio a propósito—. El doble se volvería no determinista justo en
// lo que el puerto promete: orden estable. El adaptador Postgres resuelve el
// mismo empate con el user_id como segundo criterio; aquí basta el ordinal.
type membresia struct {
	tenantID string
	at       time.Time
	orden    uint64
}

// MembershipStore implementa out.MembershipRepo en memoria (tabla
// tenant_members). Conserva el orden de alta, como el ORDER BY created_at de la
// implementación Postgres.
type MembershipStore struct {
	mu       sync.RWMutex
	byUserID map[string][]membresia
	// siguiente es el ordinal del próximo alta. Empieza en 1 para que el cero
	// valor de membresia nunca se confunda con una fila real.
	siguiente uint64
}

// NewMembershipStore crea el store vacío.
func NewMembershipStore() *MembershipStore {
	return &MembershipStore{byUserID: make(map[string][]membresia), siguiente: 1}
}

var _ out.MembershipRepo = (*MembershipStore)(nil)

// anotar escribe la membresía si no estaba. Devuelve false si ya existía (el
// alta es idempotente, como el ON CONFLICT DO NOTHING de la tabla real).
// El llamante ya tiene el candado.
func (s *MembershipStore) anotar(userID, tenantID string) bool {
	for _, existente := range s.byUserID[userID] {
		if existente.tenantID == tenantID {
			return false
		}
	}
	s.byUserID[userID] = append(s.byUserID[userID], membresia{
		tenantID: tenantID,
		at:       time.Now(),
		orden:    s.siguiente,
	})
	s.siguiente++
	return true
}

// Seed da de alta una membresía (helper de tests). Repetir la misma pareja no
// la duplica, igual que la PK compuesta de la tabla real.
//
// A diferencia de Add, NO aplica la guarda de una sola empresa por usuario: es
// para FABRICAR el estado de partida, incluido el que la guarda ya no dejaría
// escribir (dos empresas para la misma persona, que es la deuda MD-055.2 y
// existe en bases reales anteriores a la guarda).
func (s *MembershipStore) Seed(userID, tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anotar(userID, tenantID)
}

// Add implementa out.MembershipRepo con la MISMA guarda contable que el
// adaptador Postgres (iampostgres.CountOtherMemberships): membresía en otro
// tenant → domain.ErrConflict. Si el doble no la tuviera, los tests unitarios
// darían por buena una segunda empresa que la base rechaza.
func (s *MembershipStore) Add(_ context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existente := range s.byUserID[userID] {
		if existente.tenantID != tenantID {
			return fmt.Errorf("%w: el usuario ya es miembro de otra empresa", domain.ErrConflict)
		}
	}
	s.anotar(userID, tenantID) // idempotente: false si ya estaba
	return nil
}

// Remove implementa out.MembershipRepo. No-op si no estaba.
func (s *MembershipStore) Remove(_ context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]membresia, 0, len(s.byUserID[userID]))
	for _, existente := range s.byUserID[userID] {
		if existente.tenantID != tenantID {
			kept = append(kept, existente)
		}
	}
	s.byUserID[userID] = kept
	return nil
}

// TenantsOfUser implementa out.MembershipRepo.
func (s *MembershipStore) TenantsOfUser(_ context.Context, userID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	filas := s.byUserID[userID]
	if len(filas) == 0 {
		return nil, nil
	}
	tenants := make([]string, 0, len(filas))
	for _, f := range filas {
		tenants = append(tenants, f.tenantID)
	}
	return tenants, nil
}

// MembersOf implementa out.MembershipRepo: la lectura INVERSA, los miembros de
// un tenant. Recorre el mapa entero —el doble está indexado por usuario, como la
// PK de la tabla— y ORDENA por el ordinal de alta, que es lo que hace
// determinista el resultado pese al recorrido aleatorio del mapa de Go.
func (s *MembershipStore) MembersOf(_ context.Context, tenantID string) ([]domain.Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type fila struct {
		m     domain.Membership
		orden uint64
	}
	filas := make([]fila, 0)
	for userID, membresias := range s.byUserID {
		for _, m := range membresias {
			if m.tenantID != tenantID {
				continue
			}
			filas = append(filas, fila{
				m:     domain.Membership{UserID: userID, TenantID: tenantID, CreatedAt: m.at},
				orden: m.orden,
			})
		}
	}
	sort.Slice(filas, func(i, j int) bool { return filas[i].orden < filas[j].orden })

	miembros := make([]domain.Membership, 0, len(filas))
	for _, f := range filas {
		miembros = append(miembros, f.m)
	}
	return miembros, nil
}
