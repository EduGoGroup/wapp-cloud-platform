// Porta internal/intentcfg/store.go @ 8d875ab

// Package intentcfg persiste el blob de configuración del clasificador de
// intenciones por tenant (Plan 029 · T5, tabla intent_configs). Aquí vive la
// configuración de P1 —el catálogo de intenciones—, que se edita por API y no por
// fichero como los prompts P2–P5.
//
// Es el lado Cloud del contrato compartido wapp-shared/intents: el paquete NO
// valida el blob (eso lo hace wapp-shared/intents en el PUT), solo lo guarda/lee
// acotado al tenant (INV-8) y guarda junto a él la version de ENTIDAD (hash del
// blob, que calcula el llamante) que viaja en el ConfigUpdate hacia el Edge
// (ADR-0021).
//
// El nombre del paquete es intentcfg (no "intents") a propósito: el contrato
// compartido ya ocupa el identificador `intents` y ambos se importan juntos en la
// cara HTTP.
//
// Las promesas del puerto las fija la suite intentcfghelpertest.Contrato, que
// corren las dos implementaciones: MemoryStore en unitario y PostgresStore en los
// procesos de F9.
package intentcfg

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Kind es el `kind` del ConfigUpdate (ADR-0021) para la config de intenciones, el
// primer kind del mecanismo de push de config Cloud→Edge. Su valor, "intents",
// viaja por el contrato con el Edge: no se cambia.
const Kind = "intents"

// ErrNotFound lo devuelve Get cuando el tenant no tiene config de intents. Se
// comprueba con errors.Is: una implementación puede devolverlo envuelto (el
// Postgres le añade el tenant).
var ErrNotFound = errors.New("config de intents no encontrada")

// Config es el blob de intents persistido de un tenant: la version de ENTIDAD
// (hash del blob, fijada por el servidor), el blob JSON crudo y su marca de
// actualización. No es el contrato del blob (ese es wapp-shared/intents.Config).
type Config struct {
	Version   string
	Blob      []byte
	UpdatedAt time.Time
}

// Store es el puerto de persistencia del blob de intents por tenant. Lo satisfacen
// *PostgresStore (producción) y *MemoryStore (tests). Toda operación va acotada al
// tenant (INV-8): cada tenant tiene, como mucho, UNA config.
type Store interface {
	// Get devuelve la config del tenant: la version y el blob del último Upsert y
	// el instante de ese Upsert. Si el tenant no tiene ninguna devuelve un Config
	// cero y un error que cumple errors.Is(err, ErrNotFound). Leer no cambia nada.
	//
	// El blob vuelve EQUIVALENTE como JSON al que se guardó, no necesariamente
	// byte a byte: el Postgres lo guarda como JSONB, que reordena claves y
	// normaliza espacios. Nada depende de la identidad de bytes: la version de
	// entidad se calcula antes de guardar y se persiste junto al blob.
	Get(ctx context.Context, tenantID string) (Config, error)
	// Upsert persiste el blob con la version de entidad dada: crea la config del
	// tenant si no la tenía y, si la tenía, SUSTITUYE version y blob enteros (no
	// mezcla). UpdatedAt pasa a ser el instante del Upsert SIEMPRE, también cuando
	// version y blob son los mismos que ya había. No toca a ningún otro tenant y no
	// se queda con el slice del llamante.
	//
	// El puerto no valida el blob ni comprueba que version sea su hash: eso es del
	// llamante. Un blob que no sea JSON válido queda fuera del contrato (el
	// Postgres lo rechaza, la columna es JSONB).
	Upsert(ctx context.Context, tenantID, version string, blob []byte) error
}

// MemoryStore es un Store en memoria para tests. Seguro para uso concurrente:
// cada método es atómico. Marca UpdatedAt con el reloj del proceso (no se le
// inyecta otro) y, a diferencia del Postgres, guarda y devuelve el blob BYTE A
// BYTE, sea o no JSON.
type MemoryStore struct{}

// NewMemoryStore construye un MemoryStore vacío: ningún tenant tiene config.
func NewMemoryStore() *MemoryStore {
	panic(pendiente.Implementar("intentcfg.NewMemoryStore"))
}

var _ Store = (*MemoryStore)(nil)

// Get implementa Store sobre el mapa en memoria. Sin config devuelve ErrNotFound
// tal cual, sin envolver. El blob devuelto es una COPIA: modificarlo no altera lo
// guardado.
func (s *MemoryStore) Get(ctx context.Context, tenantID string) (Config, error) {
	panic(pendiente.Implementar("intentcfg.MemoryStore.Get"))
}

// Upsert implementa Store sobre el mapa en memoria: guarda una COPIA del blob
// (modificar después el slice del llamante no altera lo guardado; un blob nil se
// guarda como vacío) y marca UpdatedAt con time.Now(). Nunca devuelve error.
func (s *MemoryStore) Upsert(ctx context.Context, tenantID, version string, blob []byte) error {
	panic(pendiente.Implementar("intentcfg.MemoryStore.Upsert"))
}
