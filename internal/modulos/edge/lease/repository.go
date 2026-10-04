// Porta internal/gateway/lease/repository.go @ 8896f13

package lease

import (
	"context"
	"time"
)

// State es el estado de autorización persistido de un Edge (refleja una fila de
// public.leases). No contiene la DEK ni el blob firmado: solo metadatos (R-L8).
//
// Revoked es un bool cuyo valor CERO significa «vigente» (D-F2-10: anotado, no
// cambiado). Se porta igual que en el paquete viejo: lo que impide que ese cero
// autorice por accidente no es este campo, sino las dos guardas de la
// revocación pegajosa (la lectura previa de Manager y el contrato de Upsert).
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
// (TenantID, EdgeID). Implementaciones: PostgresRepository (producción) y
// leasehelpertest.Memoria (el doble de los tests, D-F3-1; en el paquete viejo
// era MemoryRepository y vivía aquí). Las dos corren la misma suite,
// leasehelpertest.ContratoRepository.
type Repository interface {
	// Upsert registra una emisión/renovación con el counter y la expiración de
	// s. CONTRATO SOBRE Revoked (D-055.1 · T2.1), idéntico en las dos
	// implementaciones: Upsert NUNCA escribe el estado de revocación. Una fila
	// NUEVA nace no revocada; una fila EXISTENTE conserva su Revoked previo.
	// s.Revoked se IGNORA -- ni resucita un lease revocado ni sirve para
	// revocar (para eso está MarkRevoked). Así, aunque alguien llamase a
	// Upsert directamente sobre un Edge cortado, el kill-switch aguanta (R-L2).
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
