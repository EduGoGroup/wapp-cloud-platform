// Porta internal/ingest/postgres.go @ 8896f13

package ingest

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PostgresDeduper implementa el dedupe persistente sobre public.ingest_dedupe
// (migración 0031). Deduplica por (session_id, wa_message_id) con INSERT ... ON
// CONFLICT DO NOTHING (una fila por clave) y poda las filas fuera de la ventana de
// retención de forma PEREZOSA y throttled (fuera del camino caliente). Es seguro
// para uso concurrente.
//
// Valores por defecto: retención de 7 días (solo debe cubrir el horizonte de reenvío
// del outbox del Edge), una poda cada 512 claves NUEVAS y hasta 1000 filas por poda.
type PostgresDeduper struct{}

// Option ajusta el PostgresDeduper (retención/cadencia de poda). Los defaults son
// sanos; los tests bajan la cadencia para ejercitar la poda.
type Option func(*PostgresDeduper)

// WithRetention fija la ventana de retención de las claves. Un valor <= 0 se ignora:
// queda la que hubiera.
func WithRetention(d time.Duration) Option {
	panic(pendiente.Implementar("ingest.WithRetention"))
}

// WithSweep fija la cadencia (cada cuántas claves nuevas) y el lote (cuántas filas
// como mucho) de la poda perezosa. Cada uno de los dos valores se ignora por separado
// si es <= 0: queda el que hubiera.
func WithSweep(every uint64, batch int) Option {
	panic(pendiente.Implementar("ingest.WithSweep"))
}

// NewPostgresDeduper construye el deduper sobre el pool dado, con los valores por
// defecto y, encima, las opciones en el orden en que llegan. No consulta la base.
func NewPostgresDeduper(db *sql.DB, opts ...Option) *PostgresDeduper {
	panic(pendiente.Implementar("ingest.NewPostgresDeduper"))
}

var _ Deduper = (*PostgresDeduper)(nil)

// Seen registra la clave (session_id, wa_message_id) de forma IDEMPOTENTE y
// devuelve true si YA se había visto (⇒ el entrante es un duplicado del outbox y el
// runtime debe ignorarlo). El primer avistamiento inserta la fila y devuelve false.
//
// UN solo write en el camino caliente: el INSERT ... ON CONFLICT DO NOTHING; cero
// filas afectadas es «ya estaba» (true). No hay transacción ambiente en la ingesta
// (cada operación del runtime es su propia sentencia sobre el pool), así que el
// INSERT va como sentencia independiente sobre el MISMO pool, coherente con el
// resto del repo.
//
// La poda de filas viejas es PEREZOSA: solo una de cada «cadencia» claves NUEVAS la
// dispara (un duplicado no avanza la cuenta, y un INSERT fallido tampoco). Corre
// SÍNCRONA, dentro de la misma llamada y después del INSERT, como un DELETE acotado
// por el lote sobre las filas con first_seen_at anterior a (ahora − retención), en
// UTC. Su fallo se DESCARTA: NO afecta el resultado del dedupe (es GC best-effort; la
// próxima poda reintenta).
//
// Errores, siempre con false: "ingest: registrar dedupe: " (el INSERT) e "ingest:
// filas afectadas del dedupe: " (leer cuántas filas tocó).
func (p *PostgresDeduper) Seen(ctx context.Context, sessionID, waMessageID string) (bool, error) {
	panic(pendiente.Implementar("ingest.PostgresDeduper.Seen"))
}
