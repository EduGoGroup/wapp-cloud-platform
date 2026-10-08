// Porta internal/intake/machine_postgres.go @ 8d875ab

package intake

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// machine_postgres.go — el SQL de la máquina de estados (Plan 044 · Ola 2 · T2.1).
//
// Las CINCO sentencias de este fichero comparten forma, y la forma es el diseño:
// UNA sentencia por transición, con el estado de partida escrito en el `WHERE`.
// Nadie lee antes de escribir. Un `SELECT status … ; if status == "processing" {
// UPDATE … }` sería lo mismo salvo por el instante entre las dos sentencias, y ese
// instante es exactamente donde dos workers se pisan.

// ClaimNext implementa PipelineStore.
func (p *Postgres) ClaimNext(ctx context.Context) (ClaimedJob, bool, error) {
	panic(pendiente.Implementar("intake.Postgres.ClaimNext"))
}

// ClaimNextIgnoringBackoff implementa PipelineStore. Ver claimIgnoringBackoffSQL.
//
// Un `tenantID` vacío devuelve «no hay nada» sin ir a la base: no es un filtro que
// case con todo, es una llamada mal hecha, y dejarla pasar convertiría un flanco sin
// identidad en el barrido global que el `$1` existe para impedir.
func (p *Postgres) ClaimNextIgnoringBackoff(ctx context.Context, tenantID string) (ClaimedJob, bool, error) {
	panic(pendiente.Implementar("intake.Postgres.ClaimNextIgnoringBackoff"))
}

// SaveStage implementa PipelineStore.
func (p *Postgres) SaveStage(ctx context.Context, jobID string, a Artifact) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.SaveStage"))
}

// Release implementa PipelineStore.
func (p *Postgres) Release(ctx context.Context, jobID string) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.Release"))
}

// Retry implementa PipelineStore.
func (p *Postgres) Retry(ctx context.Context, jobID string, next time.Time) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.Retry"))
}

// Finish implementa PipelineStore.
func (p *Postgres) Finish(ctx context.Context, jobID, intakeID string) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.Finish"))
}

// Fail implementa PipelineStore.
func (p *Postgres) Fail(ctx context.Context, jobID, reason string) (bool, error) {
	panic(pendiente.Implementar("intake.Postgres.Fail"))
}
