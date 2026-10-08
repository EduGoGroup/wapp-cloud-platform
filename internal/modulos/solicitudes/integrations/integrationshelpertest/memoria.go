package integrationshelpertest

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// Memoria es una implementación en memoria de integrations.Store, segura para concurrencia. Es
// NUEVA: no tiene fichero viejo —el paquete viejo no tenía doble (es uno de los doce de 05 E-6)—;
// su base es el `fakeStore` de internal/integrations/worker_test.go:35-190, que solo vivía en ese
// test. Pensada para tests unitarios sin BD (los del worker, los del gate y los del encolador), y
// cumple la misma suite que integrations.Postgres (Contrato).
//
// 🔴 GUARDA EL SECRETO EN CLARO, y por eso vive aquí y no en producción: no cifra, no tiene sobre
// ni KEK. Lo que el adaptador hace con el cifrador no se puede ver con este doble.
//
// Se aparta del fakeStore viejo en lo que aquel no cumplía del puerto (lo destapó la suite):
// elige el lote por next_attempt_at y no al azar del mapa; un claim sin sello no casa con una fila
// sin sello; el lease vence en estricto; rechaza un payload que no es JSON y guarda su propia
// copia; en la integración ignora HasSecret y las fechas del llamante y pone las suyas; y deja el
// last_error literal del rescate.
//
// Sella los claims con su reloj tal cual: con el reloj PARADO, dos claims seguidos de la misma
// fila llevan el mismo sello. Un test que rescate y vuelva a reclamar tiene que mover el reloj.
//
// No mira el contexto: nunca falla por cancelación.
type Memoria struct {
	mu           sync.Mutex
	now          func() time.Time
	rows         map[int64]*integrations.WebhookOutbox
	lastID       int64
	tenants      map[string]memoryTenant
	secretWrites int
}

// memoryTenant es una fila de public.tenant_integrations tal como la ve el doble.
type memoryTenant struct {
	// config lleva la configuración y las dos fechas; su HasSecret no se usa (se deduce de secret).
	config integrations.TenantIntegration
	// secret es el secreto EN CLARO; "" es «las tres columnas del sobre a NULL».
	secret string
	// secretSeq es el número de orden de la escritura del secreto (0 = sin secreto).
	secretSeq int
}

// orphanLastError es el last_error que deja el rescate: el mismo literal que el adaptador.
const orphanLastError = "claim vencido: el worker que reclamó la entrega no la resolvió dentro del lease"

// errInvalidPayload es la causa del rechazo de un payload que no es JSON (en Postgres la pone la
// columna jsonb).
var errInvalidPayload = errors.New("el payload no es JSON válido")

// NewMemoria crea un almacén en memoria vacío —sin entregas y sin integraciones—, con el reloj del
// sistema mientras no se le inyecte otro con SetClock.
func NewMemoria() *Memoria {
	return &Memoria{
		now:     time.Now,
		rows:    make(map[int64]*integrations.WebhookOutbox),
		tenants: make(map[string]memoryTenant),
	}
}

// SetClock fija el reloj del doble: de él salen created_at y next_attempt_at al encolar, el sello
// del claim, el instante contra el que se decide qué está vencido (reclamo y rescate) y las dos
// fechas de la integración. Con now == nil vuelve al reloj del sistema.
func (m *Memoria) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	m.now = now
}

// EnqueueWebhook implementa integrations.Store.
func (m *Memoria) EnqueueWebhook(_ context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	if !json.Valid(payload) {
		return 0, fmt.Errorf("integrations: encolar entrega de %s: %w", kind, errInvalidPayload)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.lastID++
	m.rows[m.lastID] = &integrations.WebhookOutbox{
		ID: m.lastID, TenantID: tenantID, Kind: kind, Payload: slices.Clone(payload),
		Status: integrations.StatusPending, NextAttemptAt: now, CreatedAt: now,
	}
	return m.lastID, nil
}

// ClaimWebhookBatch implementa integrations.Store. Devuelve el lote ordenado por next_attempt_at
// (y por id a igualdad), que es el orden en que el worker lo entrega; la suite solo afirma QUÉ
// filas entran. Un límite negativo no reclama nada (Postgres lo rechaza).
func (m *Memoria) ClaimWebhookBatch(_ context.Context, limit int) ([]integrations.WebhookOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var due []*integrations.WebhookOutbox
	for _, r := range m.rows {
		if r.Status == integrations.StatusPending && !r.NextAttemptAt.After(now) {
			due = append(due, r)
		}
	}
	slices.SortFunc(due, func(a, b *integrations.WebhookOutbox) int {
		return cmp.Or(a.NextAttemptAt.Compare(b.NextAttemptAt), cmp.Compare(a.ID, b.ID))
	})
	if limit < 0 {
		limit = 0
	}
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]integrations.WebhookOutbox, 0, len(due))
	for _, r := range due {
		r.Status = integrations.StatusDelivering
		r.ClaimedAt = now
		out = append(out, copyRow(r))
	}
	return out, nil
}

// closeClaim reproduce la valla optimista del adaptador: solo cierra la fila si sigue en
// `delivering` CON el sello que presenta el llamante, y un sello ausente no casa con nada (en SQL,
// NULL no es igual a nada). Con la valla rota devuelve el mismo texto que el adaptador.
func (m *Memoria) closeClaim(claim integrations.WebhookOutbox, what string, apply func(*integrations.WebhookOutbox)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[claim.ID]
	if !ok || r.Status != integrations.StatusDelivering || r.ClaimedAt.IsZero() || !r.ClaimedAt.Equal(claim.ClaimedAt) {
		return fmt.Errorf("integrations: entrega %d hacia %s: %w", claim.ID, what, integrations.ErrClaimLost)
	}
	apply(r)
	r.ClaimedAt = time.Time{} // el claim se cierra
	return nil
}

// MarkWebhookDelivered implementa integrations.Store: además de cerrar la fila, VACÍA el payload.
// El doble tiene que hacerlo o mentiría por omisión: un test que asumiera que el payload sigue ahí
// pasaría en verde con la fuga puesta.
func (m *Memoria) MarkWebhookDelivered(_ context.Context, claim integrations.WebhookOutbox) error {
	return m.closeClaim(claim, "delivered", func(r *integrations.WebhookOutbox) {
		r.Status = integrations.StatusDelivered
		r.Payload = json.RawMessage(`{}`)
	})
}

// MarkWebhookFailed implementa integrations.Store.
func (m *Memoria) MarkWebhookFailed(_ context.Context, claim integrations.WebhookOutbox, nextAttemptAt time.Time, lastErr string) error {
	return m.closeClaim(claim, "reintento", func(r *integrations.WebhookOutbox) {
		r.Status = integrations.StatusPending
		r.Attempts++
		r.NextAttemptAt = nextAttemptAt
		r.LastError = lastErr
	})
}

// MarkWebhookDead implementa integrations.Store. El payload se conserva.
func (m *Memoria) MarkWebhookDead(_ context.Context, claim integrations.WebhookOutbox, lastErr string) error {
	return m.closeClaim(claim, "dead", func(r *integrations.WebhookOutbox) {
		r.Status = integrations.StatusDead
		r.Attempts++
		r.LastError = lastErr
	})
}

// RecoverOrphanDeliveries implementa integrations.Store: rescata SOLO las filas en vuelo cuyo
// sello es estrictamente anterior a (reloj − lease), más las que no tienen sello.
func (m *Memoria) RecoverOrphanDeliveries(_ context.Context, lease time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().Add(-lease)
	n := 0
	for _, r := range m.rows {
		if r.Status != integrations.StatusDelivering {
			continue
		}
		if !r.ClaimedAt.IsZero() && !r.ClaimedAt.Before(cutoff) {
			continue // claim VIGENTE: alguien la está entregando ahora mismo
		}
		r.Status = integrations.StatusPending
		r.Attempts++
		r.LastError = orphanLastError
		r.ClaimedAt = time.Time{}
		n++
	}
	return n, nil
}

// GetTenantIntegration implementa integrations.Store. Nunca trae el secreto: solo HasSecret.
func (m *Memoria) GetTenantIntegration(_ context.Context, tenantID string) (integrations.TenantIntegration, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.tenants[tenantID]
	if !ok {
		return integrations.TenantIntegration{}, false, nil
	}
	ti := row.config
	ti.HasSecret = row.secret != ""
	return ti, true, nil
}

// GetTenantSecret implementa integrations.Store.
func (m *Memoria) GetTenantSecret(_ context.Context, tenantID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.tenants[tenantID]
	if !ok || row.secret == "" {
		return "", false, nil
	}
	return row.secret, true, nil
}

// UpsertTenantIntegration implementa integrations.Store: reemplaza la configuración, conserva la
// fecha de alta y el secreto si no viene uno nuevo, y refresca updated_at siempre. De ti solo toma
// el tenant y las cuatro columnas de configuración.
func (m *Memoria) UpsertTenantIntegration(_ context.Context, ti integrations.TenantIntegration, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	next := memoryTenant{config: integrations.TenantIntegration{
		TenantID:       ti.TenantID,
		CatalogAdapter: ti.CatalogAdapter,
		EventsAdapter:  ti.EventsAdapter,
		EndpointURL:    ti.EndpointURL,
		Enabled:        ti.Enabled,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}
	if prev, ok := m.tenants[ti.TenantID]; ok {
		next.config.CreatedAt = prev.config.CreatedAt // el alta es el alta: el DO UPDATE no la nombra
		next.secret, next.secretSeq = prev.secret, prev.secretSeq
	}
	if secret != "" {
		m.secretWrites++
		next.secret, next.secretSeq = secret, m.secretWrites
	}
	m.tenants[ti.TenantID] = next
	return nil
}

// DeleteTenantIntegration implementa integrations.Store. Borrar lo que no hay no es un error.
func (m *Memoria) DeleteTenantIntegration(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tenants, tenantID)
	return nil
}

// OutboxRow es el observador de la cola que el Montaje de la suite pide (Montaje.OutboxRow): la
// fila entera con ese id, sin tocarla. No es del puerto.
func (m *Memoria) OutboxRow(id int64) (OutboxRow, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return OutboxRow{}, false
	}
	return copyRow(r), true
}

// IntegrationRow es el observador de la configuración que el Montaje de la suite pide
// (Montaje.IntegrationRow). El doble no tiene sobre: HasSecret dice si guarda un secreto y
// SecretSeal es el número de orden de su escritura, nunca el secreto. No es del puerto.
func (m *Memoria) IntegrationRow(tenantID string) (IntegrationRow, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.tenants[tenantID]
	if !ok {
		return IntegrationRow{}, false
	}
	out := IntegrationRow{
		TenantID:       row.config.TenantID,
		CatalogAdapter: row.config.CatalogAdapter,
		EventsAdapter:  row.config.EventsAdapter,
		EndpointURL:    row.config.EndpointURL,
		Enabled:        row.config.Enabled,
		CreatedAt:      row.config.CreatedAt,
		UpdatedAt:      row.config.UpdatedAt,
		HasSecret:      row.secret != "",
	}
	if out.HasSecret {
		out.SecretSeal = "escritura-" + strconv.Itoa(row.secretSeq)
	}
	return out, true
}

// SetClaimedAt es la siembra cruda que el Montaje de la suite pide (Montaje.SetClaimedAt): deja
// la fila con ese sello (el instante cero es «sin sello») sin tocar nada más. Devuelve false si
// la fila no existe. No es del puerto.
func (m *Memoria) SetClaimedAt(id int64, at time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return false
	}
	r.ClaimedAt = at
	return true
}

// copyRow devuelve la fila con su propia copia del payload: lo que sale del doble no comparte
// memoria con lo que guarda.
func copyRow(r *integrations.WebhookOutbox) integrations.WebhookOutbox {
	out := *r
	out.Payload = slices.Clone(r.Payload)
	return out
}
