// Porta internal/intake/reanalisis.go @ 8d875ab

// postgres_reanalysis.go es el SQL del SEGUNDO PRODUCTOR DE JOBS (ver reanalysis.go,
// que lleva el porqué entero): la pregunta por el job vivo de un evento y la
// apertura del job del re-análisis. En el paquete viejo los dos métodos vivían en
// reanalisis.go; aquí nacen con el adaptador (D-F6-6 ampliada).
//
// 🔴 NINGUNO DE LOS DOS ES DE JobStore NI DE PipelineStore, a propósito: el sink no
// puede tener delante una lectura (D-044.26) y el worker no abre jobs. Los consume
// el endpoint del re-análisis por su propio puerto estrecho.

package intake

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// LiveJobOfEvent (antes `JobNoTerminalDeEvento`) devuelve el id del job vivo de ese
// evento, si lo hay.
//
// `(", false, nil)` significa «no hay ninguno»: NO es un error, es el caso normal —un
// evento cuyo pipeline ya terminó es exactamente el que se puede re-analizar.
func (p *Postgres) LiveJobOfEvent(ctx context.Context, tenantID, eventID string) (string, bool, error) {
	panic(pendiente.Implementar("intake.Postgres.LiveJobOfEvent"))
}

// OpenReanalysis (antes `AbrirReanalisis`) crea el job del re-análisis y devuelve su id.
//
// NO es idempotente y no puede serlo: dos re-análisis del mismo pedido son dos actos
// distintos y tienen que dejar dos revisiones. Quien impide el duplicado accidental
// es LiveJobOfEvent, arriba, y la comprobación va antes de llamar aquí.
func (p *Postgres) OpenReanalysis(ctx context.Context, s ReanalysisRequest) (string, error) {
	panic(pendiente.Implementar("intake.Postgres.OpenReanalysis"))
}
