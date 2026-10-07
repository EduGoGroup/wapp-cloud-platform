package tenantllmhelpertest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// Memoria es una implementación en memoria de tenantllm.Store, segura para concurrencia. Es
// NUEVA: el paquete viejo no tenía doble (es uno de los doce de 05 E-6) y sus consumidores se
// escribían un espía cada uno. Pensada para tests unitarios sin BD, y cumple la misma suite que
// tenantllm.Postgres (Contrato).
//
// 🔴 GUARDA LA CLAVE EN CLARO, y por eso vive aquí y no en producción: no cifra, no tiene sobre
// ni KEK. Lo que el adaptador hace con el cifrador (T-15) no se puede ver con este doble.
//
// Espeja al adaptador en lo que el puerto deja ver: las mismas tres guardas previas a la
// escritura, con los mismos textos; el reemplazo completo; el eje api vacío en la vía local;
// created_at que no se pisa.
type Memoria struct {
	mu          sync.Mutex
	rows        map[string]memoryRow
	apiKeyCalls int
	now         func() time.Time
}

// memoryRow es una fila de public.tenant_llm tal como la ve el doble. En la vía local,
// provider, model, apiKey y consentedAt están en su valor cero: es el NULL de la tabla.
type memoryRow struct {
	via         string
	provider    string
	model       string
	apiKey      string
	consentedAt time.Time
	createdAt   time.Time
	updatedAt   time.Time
}

// NewMemoria crea un almacén en memoria vacío, con el reloj del sistema para created_at y
// updated_at.
func NewMemoria() *Memoria {
	return &Memoria{rows: make(map[string]memoryRow), now: time.Now}
}

// Get implementa tenantllm.Store. Sin fila: (Config cero, false, nil).
func (m *Memoria) Get(_ context.Context, tenantID string) (tenantllm.Config, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[tenantID]
	if !ok {
		return tenantllm.Config{}, false, nil
	}
	return tenantllm.Config{
		TenantID:    tenantID,
		Via:         r.via,
		Provider:    r.provider,
		Model:       r.model,
		HasAPIKey:   r.apiKey != "",
		ConsentedAt: r.consentedAt,
		CreatedAt:   r.createdAt,
		UpdatedAt:   r.updatedAt,
	}, true, nil
}

// Upsert implementa tenantllm.Store, con las tres guardas del adaptador y sus textos: un doble
// que aceptara lo que Postgres rechaza dejaría ciegos a los tests que se apoyan en él.
func (m *Memoria) Upsert(_ context.Context, cfg tenantllm.Config, apiKey string, consentedAt time.Time) error {
	// La misma guarda que el adaptador, con la misma función: si el vocabulario de vías cambia,
	// el doble cambia con él y no por su cuenta.
	if !tenantllm.ValidVia(cfg.Via) {
		return fmt.Errorf("tenantllm: upsert de %s con vía %q: fuera del vocabulario (%s|%s)",
			cfg.TenantID, cfg.Via, tenantllm.ViaLocal, tenantllm.ViaAPI)
	}
	// La vía local no lleva nada del eje api, venga lo que venga en cfg.
	next := memoryRow{via: cfg.Via}
	if cfg.Via == tenantllm.ViaAPI {
		if apiKey == "" {
			return fmt.Errorf("tenantllm: upsert de %s en vía %s sin API key: esa vía no existe sin credencial",
				cfg.TenantID, tenantllm.ViaAPI)
		}
		if consentedAt.IsZero() {
			return fmt.Errorf("tenantllm: upsert de %s en vía %s sin consentimiento: la fila no puede existir sin él",
				cfg.TenantID, tenantllm.ViaAPI)
		}
		next.provider, next.model = cfg.Provider, cfg.Model
		next.apiKey = apiKey
		next.consentedAt = consentedAt.UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	next.createdAt, next.updatedAt = now, now
	if prev, ok := m.rows[cfg.TenantID]; ok {
		next.createdAt = prev.createdAt // el alta es el alta: espeja el DO UPDATE, que no la nombra
	}
	m.rows[cfg.TenantID] = next
	return nil
}

// Delete implementa tenantllm.Store. Borrar lo que no hay no es un error.
func (m *Memoria) Delete(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, tenantID)
	return nil
}

// APIKey implementa tenantllm.Store: la clave de una fila de la vía api, o ErrNotConfigured si
// no hay fila o la fila es de otra vía. Cuenta la llamada (APIKeyCalls).
func (m *Memoria) APIKey(_ context.Context, tenantID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiKeyCalls++
	r, ok := m.rows[tenantID]
	if !ok || r.via != tenantllm.ViaAPI || r.apiKey == "" {
		return "", tenantllm.ErrNotConfigured
	}
	return r.apiKey, nil
}

// APIKeyCalls dice cuántas veces se pidió la credencial, con o sin éxito. No es del puerto: es
// para los tests que afirman que la clave NO se pide (R4.4.b: un tenant sin fila va a la vía
// local sin tocar la credencial).
func (m *Memoria) APIKeyCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apiKeyCalls
}

// Row es el observador de estado que el Montaje de la suite pide (Montaje.Row): qué columnas del
// eje api tiene la fila del tenant. El doble no tiene sobre, así que las tres columnas del sobre
// dicen lo mismo que la clave: las tres o ninguna, como exige la tabla.
func (m *Memoria) Row(tenantID string) (Row, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[tenantID]
	if !ok {
		return Row{}, false
	}
	hasKey := r.apiKey != ""
	return Row{
		Via:         r.via,
		HasProvider: r.provider != "",
		HasModel:    r.model != "",
		HasKeyEnc:   hasKey,
		HasKeyDEK:   hasKey,
		HasKEKID:    hasKey,
		HasConsent:  !r.consentedAt.IsZero(),
	}, true
}
