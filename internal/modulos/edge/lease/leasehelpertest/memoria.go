// Porta internal/gateway/lease/repository.go @ 8896f13 (MemoryRepository)

package leasehelpertest

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
)

// Memoria es una implementación en memoria de lease.Repository, segura para
// concurrencia. Pensada para tests unitarios CI-safe (sin BD). En el paquete
// viejo era lease.MemoryRepository y vivía en producción (D-F3-1).
type Memoria struct {
	mu      sync.Mutex
	leases  map[string]lease.State
	tenants map[string]bool // tenantID -> revocado (D-055.2); ausencia = activo.
	now     func() time.Time
}

// NewMemoria crea un repositorio en memoria vacío con reloj wall-clock (en el
// paquete viejo, NewMemoryRepository).
func NewMemoria() *Memoria {
	return &Memoria{
		leases:  make(map[string]lease.State),
		tenants: make(map[string]bool),
		now:     time.Now,
	}
}

func memKey(tenantID, edgeID string) string { return tenantID + "\x00" + edgeID }

// Upsert implementa Repository respetando el contrato de la interface sobre
// Revoked (D-055.1 · T2.1) y ESPEJANDO exactamente al PostgresRepository: allí
// el ON CONFLICT DO UPDATE no menciona la columna revoked (fila existente ->
// conserva su valor) y el INSERT la fija a false (fila nueva -> no revocada).
// Aquí se hace lo mismo a mano, porque `r.leases[key] = s` machacaría la
// estructura entera con el s del llamante: sin estas dos líneas, un Upsert con
// s.Revoked=false RESUCITARÍA un lease revocado en memoria mientras Postgres
// lo mantiene cortado -- una divergencia que dejaría ciegos precisamente a los
// tests unitarios que cubren el kill-switch.
func (r *Memoria) Upsert(_ context.Context, s lease.State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(s.TenantID, s.EdgeID)
	prev, ok := r.leases[key]
	s.UpdatedAt = now
	if ok {
		s.IssuedAt = prev.IssuedAt
		s.Revoked = prev.Revoked // pegajoso: Upsert no des-revoca (espeja el ON CONFLICT)
	} else {
		s.IssuedAt = now
		s.Revoked = false // fila nueva: espeja el `VALUES (..., false, ...)` del INSERT
	}
	r.leases[key] = s
	return nil
}

// MarkRevoked implementa Repository.
func (r *Memoria) MarkRevoked(_ context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID)
	s, ok := r.leases[key]
	if !ok {
		s = lease.State{TenantID: tenantID, EdgeID: edgeID, IssuedAt: now}
	}
	s.Revoked = true
	s.ExpiresAt = expiresAt
	s.UpdatedAt = now
	r.leases[key] = s
	return nil
}

// Get implementa Repository.
func (r *Memoria) Get(_ context.Context, tenantID, edgeID string) (lease.State, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.leases[memKey(tenantID, edgeID)]
	return s, ok, nil
}

// TenantRevoked implementa Repository. Ausencia de entrada = tenant activo
// (mismo criterio que Get con found=false).
func (r *Memoria) TenantRevoked(_ context.Context, tenantID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tenants[tenantID], nil
}

// MarkTenantRevoked implementa Repository. Diferencia documentada con
// Postgres: el doble no sabe qué tenants existen, así que marca también uno
// desconocido (allí el UPDATE no toca ninguna fila).
func (r *Memoria) MarkTenantRevoked(_ context.Context, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tenants == nil {
		r.tenants = make(map[string]bool)
	}
	r.tenants[tenantID] = true
	return nil
}

// RestoreTenant implementa Repository.
func (r *Memoria) RestoreTenant(_ context.Context, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tenants == nil {
		r.tenants = make(map[string]bool)
	}
	r.tenants[tenantID] = false
	return nil
}
