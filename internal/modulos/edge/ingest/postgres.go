// Porta internal/ingest/postgres.go @ 8896f13

package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"
)

// Parámetros por defecto del dedupe persistente.
const (
	// defaultRetention es la ventana que se conservan las claves. Solo debe cubrir
	// el horizonte de reenvío del outbox del Edge (un frame más viejo que esto ya no
	// puede llegar duplicado); 7 días lo cubre con holgura y mantiene la tabla chica.
	defaultRetention = 7 * 24 * time.Hour
	// defaultSweepEvery es cada cuántos Seen se dispara UNA poda perezosa. Amortiza
	// el coste del GC casi a cero en el camino caliente (~1 DELETE acotado por cada
	// 512 inserts).
	defaultSweepEvery = 512
	// defaultSweepBatch acota cuántas filas viejas borra cada poda (barrido en lote).
	defaultSweepBatch = 1000
)

// PostgresDeduper implementa el dedupe persistente sobre public.ingest_dedupe
// (migración 0031). Deduplica por (session_id, wa_message_id) con INSERT ... ON
// CONFLICT DO NOTHING (una fila por clave) y poda las filas fuera de la ventana de
// retención de forma PEREZOSA y throttled (fuera del camino caliente). Es seguro
// para uso concurrente.
//
// Valores por defecto: retención de 7 días (solo debe cubrir el horizonte de reenvío
// del outbox del Edge), una poda cada 512 claves NUEVAS y hasta 1000 filas por poda.
type PostgresDeduper struct {
	db         *sql.DB
	retention  time.Duration
	sweepEvery uint64
	sweepBatch int
	tick       atomic.Uint64
}

// Option ajusta el PostgresDeduper (retención/cadencia de poda). Los defaults son
// sanos; los tests bajan la cadencia para ejercitar la poda.
type Option func(*PostgresDeduper)

// WithRetention fija la ventana de retención de las claves. Un valor <= 0 se ignora:
// queda la que hubiera.
func WithRetention(d time.Duration) Option {
	return func(p *PostgresDeduper) {
		if d > 0 {
			p.retention = d
		}
	}
}

// WithSweep fija la cadencia (cada cuántas claves nuevas) y el lote (cuántas filas
// como mucho) de la poda perezosa. Cada uno de los dos valores se ignora por separado
// si es <= 0: queda el que hubiera.
func WithSweep(every uint64, batch int) Option {
	return func(p *PostgresDeduper) {
		if every > 0 {
			p.sweepEvery = every
		}
		if batch > 0 {
			p.sweepBatch = batch
		}
	}
}

// NewPostgresDeduper construye el deduper sobre el pool dado, con los valores por
// defecto y, encima, las opciones en el orden en que llegan. No consulta la base.
func NewPostgresDeduper(db *sql.DB, opts ...Option) *PostgresDeduper {
	p := &PostgresDeduper{
		db:         db,
		retention:  defaultRetention,
		sweepEvery: defaultSweepEvery,
		sweepBatch: defaultSweepBatch,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
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
	res, err := p.db.ExecContext(ctx, `
		INSERT INTO public.ingest_dedupe (session_id, wa_message_id)
		VALUES ($1, $2)
		ON CONFLICT (session_id, wa_message_id) DO NOTHING
	`, sessionID, waMessageID)
	if err != nil {
		return false, fmt.Errorf("ingest: registrar dedupe: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("ingest: filas afectadas del dedupe: %w", err)
	}
	// affected==0 ⇒ la fila ya existía ⇒ duplicado. affected==1 ⇒ primer avistamiento.
	if affected == 0 {
		return true, nil
	}
	p.maybeSweep(ctx)
	return false, nil
}

// maybeSweep dispara la poda perezosa 1 de cada sweepEvery llamadas: borra en lote
// las filas más viejas que la ventana de retención. Best-effort: cualquier error se
// ignora (la próxima poda reintenta); NUNCA afecta el resultado del dedupe.
func (p *PostgresDeduper) maybeSweep(ctx context.Context) {
	if p.sweepEvery == 0 || p.tick.Add(1)%p.sweepEvery != 0 {
		return
	}
	cutoff := time.Now().Add(-p.retention).UTC()
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM public.ingest_dedupe
		WHERE ctid IN (
			SELECT ctid FROM public.ingest_dedupe
			WHERE first_seen_at < $1
			LIMIT $2
		)
	`, cutoff, p.sweepBatch); err != nil {
		_ = err // best-effort: la próxima poda reintenta; nunca afecta el dedupe
	}
}
