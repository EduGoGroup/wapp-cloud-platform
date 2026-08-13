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
	// Upsert registra una emisión/renovación con el counter y la expiración de
	// s. CONTRATO SOBRE Revoked (D-055.1 · T2.1), idéntico en las dos
	// implementaciones: Upsert NUNCA escribe el estado de revocación. Una fila
	// NUEVA nace no revocada; una fila EXISTENTE conserva su Revoked previo.
	// s.Revoked se IGNORA -- ni resucita un lease revocado ni sirve para
	// revocar (para eso está MarkRevoked). Así, aunque alguien llamase a
	// Upsert directamente sobre un Edge cortado, el kill-switch aguanta.
	Upsert(ctx context.Context, s State) error
	// MarkRevoked marca el lease del Edge como revocado (pegajoso) conservando el
	// counter; crea la fila si no existía.
	MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error
	// Get devuelve el estado del Edge y si existe. found=false sin error si no hay
	// fila.
	Get(ctx context.Context, tenantID, edgeID string) (state State, found bool, err error)

	// TenantRevoked consulta si el TENANT (no el Edge) está revocado -- el
	// kill-switch COMERCIAL de D-055.2 (public.tenants.revoked_at), independiente
	// de State.Revoked que es por-instalación (anti-clon, ADR-0007). Un tenant
	// desconocido (sin fila, no debería ocurrir en producción salvo un tenant_id
	// mal formado) NO se considera revocado: mismo criterio que Get con
	// found=false -- la ausencia de estado no es un "sí".
	TenantRevoked(ctx context.Context, tenantID string) (bool, error)
	// MarkTenantRevoked marca el tenant como revocado (pegajoso hasta
	// RestoreTenant). NO toca ninguna fila de leases: los dos sujetos de corte
	// (D-055.2) son independientes -- revocar la EMPRESA no marca cada
	// instalación individualmente, así que RestoreTenant puede reactivarlas a
	// todas de una vez sin que un "reverso" por-instalación (que hoy no existe)
	// se interponga.
	MarkTenantRevoked(ctx context.Context, tenantID string) error
	// RestoreTenant reactiva un tenant previamente revocado (revoked_at = NULL).
	// No re-emite leases vigentes por sí mismo: el siguiente IssueInitial/Renew de
	// cada instalación pasa por Manager.wasRevoked, que ya verá el tenant activo.
	RestoreTenant(ctx context.Context, tenantID string) error
}

// MemoryRepository es una implementación en memoria de Repository, segura
// para concurrencia. Pensada para tests unitarios CI-safe (sin BD).
type MemoryRepository struct {
	mu      sync.Mutex
	leases  map[string]State
	tenants map[string]bool // tenantID -> revocado (D-055.2); ausencia = activo.
	now     func() time.Time
}

// NewMemoryRepository crea un repositorio en memoria vacío con reloj wall-clock.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		leases:  make(map[string]State),
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
func (r *MemoryRepository) Upsert(_ context.Context, s State) error {
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

// TenantRevoked implementa Repository. Ausencia de entrada = tenant activo
// (mismo criterio que Get con found=false).
func (r *MemoryRepository) TenantRevoked(_ context.Context, tenantID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tenants[tenantID], nil
}

// MarkTenantRevoked implementa Repository.
func (r *MemoryRepository) MarkTenantRevoked(_ context.Context, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tenants == nil {
		r.tenants = make(map[string]bool)
	}
	r.tenants[tenantID] = true
	return nil
}

// RestoreTenant implementa Repository.
func (r *MemoryRepository) RestoreTenant(_ context.Context, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tenants == nil {
		r.tenants = make(map[string]bool)
	}
	r.tenants[tenantID] = false
	return nil
}
