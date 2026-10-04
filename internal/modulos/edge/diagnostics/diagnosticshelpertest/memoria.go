// Porta internal/diagnostics/diagnostics.go @ 8896f13 (líneas 99-203: MemoryStore, que pasa a
// este paquete por D-F3-1 y se llama Memoria).

package diagnosticshelpertest

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
)

// pending es la fila en memoria de la Memoria.
type pending struct {
	tenantID string
	rec      diagnostics.Record
	status   string // pending | ready
	expiry   time.Time
}

// Memoria es una implementación en memoria de diagnostics.Store, segura para
// concurrencia. Era diagnostics.MemoryStore. Pensada para tests unitarios CI-safe
// (sin BD). El consentimiento default es ON.
//
// ⚠️ Diferencias con diagnostics.Postgres, a propósito: repetir un command_id en
// CreateRequest pisa la solicitud anterior (en Postgres es la clave primaria y
// falla), y el tenant puede ser cualquier texto (en Postgres es un UUID con clave
// foránea).
type Memoria struct {
	mu       sync.Mutex
	byCmd    map[string]pending
	optedOut map[string]bool // tenants que se excluyeron (enabled=false)
	now      func() time.Time
}

// NewMemoria crea un store en memoria vacío con reloj wall-clock. Era
// diagnostics.NewMemoryStore.
func NewMemoria() *Memoria {
	return &Memoria{byCmd: make(map[string]pending), optedOut: make(map[string]bool), now: time.Now}
}

var _ diagnostics.Store = (*Memoria)(nil)

// SetConsent fija el consentimiento de un tenant (helper de tests): enabled=false lo
// excluye (opt-out); enabled=true lo vuelve a consentir (o simplemente deja el default).
func (m *Memoria) SetConsent(tenantID string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if enabled {
		delete(m.optedOut, tenantID)
	} else {
		m.optedOut[tenantID] = true
	}
}

// Expire hace vencer la solicitud (tenant, command_id), pendiente o lista, llevando
// su vencimiento al pasado (helper de tests, nuevo: sustituye al reloj privado que
// los tests viejos movían desde dentro del paquete). Devuelve false, sin tocar nada,
// si el tenant no tiene esa solicitud.
func (m *Memoria) Expire(tenantID, commandID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byCmd[commandID]
	if !ok || p.tenantID != tenantID {
		return false
	}
	p.expiry = m.now().Add(-time.Minute)
	m.byCmd[commandID] = p
	return true
}

// ConsentEnabled implementa Store: default ON, opt-out por SetConsent(false).
func (m *Memoria) ConsentEnabled(_ context.Context, tenantID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.optedOut[tenantID], nil
}

// CreateRequest implementa Store: registra una solicitud pending y purga vencidas.
func (m *Memoria) CreateRequest(_ context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for id, p := range m.byCmd {
		if !p.expiry.After(now) {
			delete(m.byCmd, id) // limpieza perezosa de vencidas
		}
	}
	m.byCmd[commandID] = pending{
		tenantID: tenantID,
		rec: diagnostics.Record{
			CommandID:   commandID,
			SessionID:   sessionID,
			RequestedBy: requestedBy,
			RequestedAt: now,
		},
		status: "pending",
		expiry: expiresAt,
	}
	return nil
}

// DeleteRequest implementa Store: borra la solicitud del tenant (rollback).
func (m *Memoria) DeleteRequest(_ context.Context, tenantID, commandID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.byCmd[commandID]; ok && p.tenantID == tenantID {
		delete(m.byCmd, commandID)
	}
	return nil
}

// SaveBundle implementa BundleReceiver: correlaciona por command_id + (tenant, sesión).
func (m *Memoria) SaveBundle(_ context.Context, tenantID, sessionID, commandID string, b diagnostics.Bundle) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byCmd[commandID]
	if !ok || p.status != "pending" || p.tenantID != tenantID || p.rec.SessionID != sessionID || !p.expiry.After(m.now()) {
		return false, nil // huérfano/expirado/mismatch: se ignora
	}
	p.status = "ready"
	p.rec.Bundle = b
	p.rec.ReceivedAt = m.now()
	m.byCmd[commandID] = p
	return true, nil
}

// GetBundle implementa Store: resuelve el estado de la descarga.
func (m *Memoria) GetBundle(_ context.Context, tenantID, commandID string) (diagnostics.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byCmd[commandID]
	if !ok || p.tenantID != tenantID {
		return diagnostics.Record{}, diagnostics.ErrNotFound
	}
	if !p.expiry.After(m.now()) {
		delete(m.byCmd, commandID) // borrado perezoso de la vencida
		return diagnostics.Record{}, diagnostics.ErrExpired
	}
	if p.status != "ready" {
		return diagnostics.Record{}, diagnostics.ErrPending
	}
	return p.rec, nil
}
