// Porta internal/intentcfg/store_postgres.go @ 8d875ab

package intentcfg

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PostgresStore persiste el blob de intents en public.intent_configs (0033): una
// fila por tenant (tenant_id es la clave primaria, TEXT y sin clave foránea). Las
// reglas del puerto las fija la suite intentcfghelpertest.Contrato, que corre
// contra él en los procesos de F9; su test de fichero afirma, con un driver de
// mentira, el SQL que emite, sus argumentos y el mapeo de la fila y de los errores.
type PostgresStore struct{}

// NewPostgresStore construye el store sobre el *sql.DB ya abierto. No lo consulta.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	panic(pendiente.Implementar("intentcfg.NewPostgresStore"))
}

var _ Store = (*PostgresStore)(nil)

// Get lee la config del tenant con UNA consulta sobre el pool, sin transacción. El
// blob se lee como el texto del JSONB (`config::text`): es el JSON que guardó
// Upsert tal como lo canonicaliza Postgres, y así se devuelve al consumidor.
//
// Errores, con un Config cero:
//
//   - sin fila: ErrNotFound envuelto con el tenant, "config de intents no
//     encontrada: tenant=<tenantID>" (errors.Is(err, ErrNotFound) es true);
//   - "intentcfg: leer config: " + la causa (%w) — la consulta o el escaneo fallan.
func (s *PostgresStore) Get(ctx context.Context, tenantID string) (Config, error) {
	panic(pendiente.Implementar("intentcfg.PostgresStore.Get"))
}

// Upsert persiste (o reemplaza) el blob del tenant con la version de entidad dada,
// en UNA sentencia (INSERT ... ON CONFLICT (tenant_id) DO UPDATE), sin
// transacción. updated_at es el now() de la base tanto en el alta como en el
// reemplazo, sin condición: reescribir la misma config también lo mueve.
//
// Error: "intentcfg: upsert config: " + la causa (%w). Un blob que no es JSON
// válido lo rechaza la base (la columna es JSONB) y sale por aquí.
func (s *PostgresStore) Upsert(ctx context.Context, tenantID, version string, blob []byte) error {
	panic(pendiente.Implementar("intentcfg.PostgresStore.Upsert"))
}
