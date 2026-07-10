// Package ingest deduplica los mensajes ENTRANTES (Edge→Cloud) ante la semántica
// at-least-once del OUTBOX DURABLE del Edge (Plan 028 · T6, ADR-0003).
//
// El outbox del Edge (Plan 027 Ola 3) reenvía los frames no confirmados tras una
// reconexión con los MISMOS bytes, de modo que la ingesta de la nube puede recibir
// el MISMO mensaje de WhatsApp dos veces (o intercalado con otros). La idempotencia
// previa vivía en flow_state.last_wa_message_id (runtime) pero es CONSECUTIVA: solo
// corta la RE-ENTREGA INMEDIATA. Este paquete añade una memoria PERSISTENTE e
// independiente del estado del flujo, cerrada por la clave (session_id,
// wa_message_id).
//
// REGLA DURA (INV-5, zero-knowledge): CERO PII. session_id y wa_message_id son
// metadatos OPACOS del transporte (whatsmeow/CloudLink), NUNCA el número/JID del
// contacto ni el contenido del mensaje. NUNCA la DEK/lease del Edge.
package ingest

import (
	"context"
	"sync"
)

// MemoryDeduper es la implementación en memoria del dedupe (tests y arranques sin
// BD). Deduplica por la MISMA clave que Postgres: (session_id, wa_message_id). No
// poda (crecimiento no acotado): apto solo para tests/procesos efímeros.
type MemoryDeduper struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewMemoryDeduper construye el deduper en memoria vacío.
func NewMemoryDeduper() *MemoryDeduper { return &MemoryDeduper{seen: map[string]struct{}{}} }

func memKey(sessionID, waMessageID string) string {
	return sessionID + "\x00" + waMessageID
}

// Seen registra la clave y devuelve true si YA se había visto (⇒ duplicado). El
// primer avistamiento devuelve false. Nunca devuelve error.
func (d *MemoryDeduper) Seen(_ context.Context, sessionID, waMessageID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := memKey(sessionID, waMessageID)
	if _, ok := d.seen[k]; ok {
		return true, nil
	}
	d.seen[k] = struct{}{}
	return false, nil
}
