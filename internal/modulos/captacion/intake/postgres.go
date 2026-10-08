// Porta internal/intake/postgres.go @ 8d875ab

package intake

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Postgres es la implementación real de JobStore sobre `public.intake_jobs`
// (migración 0072). No tiene cipher y NO DEBE TENERLO, ni siquiera desde que T1.4
// escribe el sobre: lo que llega a PutSourceText son bytes YA cifrados por el
// compositor del flush, que es quien tiene el KeyProvider. Un store sin cipher es
// un store que no puede escribir literal aunque alguien se lo pida — que es la
// forma barata de sostener D-044.26.
type Postgres struct{}

// NewPostgres construye el store sobre el pool compartido del proceso.
func NewPostgres(db *sql.DB) *Postgres {
	panic(pendiente.Implementar("intake.NewPostgres"))
}

// OpenOrAppend implementa JobStore. UNA sentencia, CERO lecturas, CERO cripto,
// CERO red más allá de esta ejecución (D-044.26).
func (p *Postgres) OpenOrAppend(ctx context.Context, a Append) error {
	panic(pendiente.Implementar("intake.Postgres.OpenOrAppend"))
}

// CloseWindow implementa JobStore.
func (p *Postgres) CloseWindow(ctx context.Context, k WindowKey) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.CloseWindow"))
}

// PutSourceText implementa JobStore.
func (p *Postgres) PutSourceText(ctx context.Context, k WindowKey, env SourceText) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.PutSourceText"))
}

// ListAggregating implementa JobStore.
func (p *Postgres) ListAggregating(ctx context.Context, limit int) (out []OpenJob, err error) {
	panic(pendiente.Implementar("intake.Postgres.ListAggregating"))
}
