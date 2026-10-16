//go:build laboratorio

package common

import (
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// IntakeStages son las CINCO etapas reales del pipeline de captación, armadas como las arma el
// arranque (internal/arranque/fase5_captacion.go, construirWorkerDelPipeline), con los almacenes en
// memoria donde producción tiene Postgres. Lo usan E10 (las etapas a mano) y E11 (el worker).
type IntakeStages struct {
	P2    *stages.P2
	P3    *stages.P3
	P4    *stages.P4
	Match *stages.Match
	Draft *stages.Draft
	// Flow es el almacén de flujos en memoria: la cabecera de la solicitud (intakes) y el
	// outbox de efectos (la métrica del borrador). En producción, *store.PostgresRepository.
	Flow *store.MemoryRepository
	// Intakes es el almacén de solicitudes en memoria: la revisión del borrador y las zonas de
	// envío. En producción, *intakes.Postgres, que cifra el literal; este doble lo guarda aparte
	// y en claro, porque muere con el proceso.
	Intakes *intakes.MemoryStore
}

// NewIntakeStages arma las cinco etapas sobre el selector sel (el de NewSelector) y el store de la
// máquina de intake_jobs st. Como en producción:
//
//   - P2, P3 y P4 llevan stages.WithCallTimeout(pipeline.CallTimeoutFloor) —48 s por llamada—;
//   - P4 trabaja en stages.DefaultZone (UTC);
//   - el match va SIN zona gris: la cascada determinista Exact → Fuzzy y nada de modelo;
//   - el borrador escribe la cabecera por Flow y la revisión por Intakes, con el reloj now.
//
// Lo que NO lleva: el empuje al CRM (solo actúa en un re-análisis pedido por el dueño).
func NewIntakeStages(log logger.Logger, sel stages.ProviderSelector, st intake.PipelineStore, now func() time.Time) (*IntakeStages, error) {
	if now == nil {
		now = time.Now
	}
	callTimeout := stages.WithCallTimeout(pipeline.CallTimeoutFloor)
	s := &IntakeStages{Flow: store.NewMemoryRepository(), Intakes: intakes.NewMemoryStore()}
	s.Intakes.SetClock(now)
	var err error
	if s.P2, err = stages.NewP2(log, sel, st, callTimeout); err != nil {
		return nil, fmt.Errorf("common: etapa P2: %w", err)
	}
	if s.P3, err = stages.NewP3(log, sel, st, callTimeout); err != nil {
		return nil, fmt.Errorf("common: etapa P3: %w", err)
	}
	if s.P4, err = stages.NewP4(log, sel, st, stages.DefaultZone, callTimeout); err != nil {
		return nil, fmt.Errorf("common: etapa P4: %w", err)
	}
	if s.Match, err = stages.NewMatch(log, st); err != nil {
		return nil, fmt.Errorf("common: etapa match: %w", err)
	}
	if s.Draft, err = stages.NewDraft(log, st, s.Flow, s.Intakes, s.Flow, stages.WithClock(now)); err != nil {
		return nil, fmt.Errorf("common: etapa draft: %w", err)
	}
	return s, nil
}
