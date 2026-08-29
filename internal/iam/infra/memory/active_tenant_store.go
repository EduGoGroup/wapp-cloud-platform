package memory

import (
	"context"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// ActiveTenantStore implementa out.ActiveTenantRepo en memoria (tabla
// public.user_active_tenant). UN valor por usuario, como la PK de la tabla real:
// un mapa, y no una lista, es la forma en la que el doble no puede representar el
// estado ambiguo que la base tampoco admite.
type ActiveTenantStore struct {
	mu       sync.RWMutex
	byUserID map[string]string
}

// NewActiveTenantStore crea el store vacío.
func NewActiveTenantStore() *ActiveTenantStore {
	return &ActiveTenantStore{byUserID: make(map[string]string)}
}

var _ out.ActiveTenantRepo = (*ActiveTenantStore)(nil)

// ActiveTenantOf implementa out.ActiveTenantRepo: ok=false cuando no hay
// elección guardada, que NO es un error.
func (s *ActiveTenantStore) ActiveTenantOf(_ context.Context, userID string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tenantID, ok := s.byUserID[userID]
	return tenantID, ok, nil
}

// SetActiveTenant implementa out.ActiveTenantRepo: REEMPLAZA la elección
// anterior, como el ON CONFLICT (user_id) DO UPDATE de la tabla real.
//
// ⚠️ NO comprueba la membresía, igual que el adaptador Postgres: esa regla vive
// en el usecase. Un doble que la comprobara daría por buenos tests que la base
// no respalda — y, peor, escondería el caso que T5.1 tiene que poder fabricar:
// una empresa activa que YA NO es del usuario.
func (s *ActiveTenantStore) SetActiveTenant(_ context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byUserID[userID] = tenantID
	return nil
}
