// Package intentcfg persiste el blob de configuración del clasificador de
// intenciones por tenant (Plan 029 · T5, tabla intent_configs). Es el lado Cloud
// del contrato compartido wapp-shared/intents: el paquete NO valida el blob (eso
// lo hace wapp-shared/intents en el PUT), solo lo guarda/lee acotado al tenant
// (INV-8) y fija la version de ENTIDAD (hash del blob) que viaja en el
// ConfigUpdate hacia el Edge (ADR-0021).
//
// El nombre del paquete es intentcfg (no "intents") a propósito: el contrato
// compartido ya ocupa el identificador `intents` y ambos se importan juntos en la
// capa publicapi.
package intentcfg

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Kind es el `kind` del ConfigUpdate (ADR-0021) para la config de intenciones, el
// primer kind del mecanismo de push de config Cloud→Edge.
const Kind = "intents"

// ErrNotFound lo devuelve Get cuando el tenant no tiene config de intents.
var ErrNotFound = errors.New("config de intents no encontrada")

// Config es el blob de intents persistido de un tenant: la version de ENTIDAD
// (hash del blob, fijada por el servidor), el blob JSON crudo y su marca de
// actualización. No es el contrato del blob (ese es wapp-shared/intents.Config).
type Config struct {
	Version   string
	Blob      []byte
	UpdatedAt time.Time
}

// Store es el puerto de persistencia del blob de intents por tenant. Lo satisface
// *PostgresStore (producción) y *MemoryStore (tests). Toda operación va acotada al
// tenant (INV-8).
type Store interface {
	// Get devuelve la config del tenant o ErrNotFound si no tiene ninguna.
	Get(ctx context.Context, tenantID string) (Config, error)
	// Upsert persiste el blob con la version de entidad dada (reemplaza la anterior).
	Upsert(ctx context.Context, tenantID, version string, blob []byte) error
}

// MemoryStore es un Store en memoria para tests. Seguro para uso concurrente.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Config
}

// NewMemoryStore construye un MemoryStore vacío.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: make(map[string]Config)}
}

// Get implementa Store sobre el mapa en memoria (copia el blob para no aliasar).
func (s *MemoryStore) Get(_ context.Context, tenantID string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[tenantID]
	if !ok {
		return Config{}, ErrNotFound
	}
	blob := make([]byte, len(c.Blob))
	copy(blob, c.Blob)
	return Config{Version: c.Version, Blob: blob, UpdatedAt: c.UpdatedAt}, nil
}

// Upsert implementa Store sobre el mapa en memoria.
func (s *MemoryStore) Upsert(_ context.Context, tenantID, version string, blob []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := make([]byte, len(blob))
	copy(stored, blob)
	s.m[tenantID] = Config{Version: version, Blob: stored, UpdatedAt: time.Now()}
	return nil
}
