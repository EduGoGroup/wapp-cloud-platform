// Porta internal/ingest/dedupe.go @ 8896f13 (MemoryDeduper, que pasa a este paquete por D-F3-1
// y se llama Memoria).

package ingesthelpertest

import (
	"context"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest"
)

// Memoria es la implementación en memoria del dedupe, para tests. Era
// ingest.MemoryDeduper. Deduplica por la MISMA clave que Postgres: (session_id,
// wa_message_id). No poda (crecimiento no acotado): apto solo para tests/procesos
// efímeros. Es segura para uso concurrente.
type Memoria struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewMemoria construye el deduper en memoria vacío. Era ingest.NewMemoryDeduper.
func NewMemoria() *Memoria { return &Memoria{seen: map[string]struct{}{}} }

var _ ingest.Deduper = (*Memoria)(nil)

func memKey(sessionID, waMessageID string) string {
	return sessionID + "\x00" + waMessageID
}

// Seen registra la clave y devuelve true si YA se había visto (⇒ duplicado). El
// primer avistamiento devuelve false. Nunca devuelve error.
func (d *Memoria) Seen(_ context.Context, sessionID, waMessageID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := memKey(sessionID, waMessageID)
	if _, ok := d.seen[k]; ok {
		return true, nil
	}
	d.seen[k] = struct{}{}
	return false, nil
}
