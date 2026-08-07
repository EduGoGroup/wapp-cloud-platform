package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// flowEventStore es lo único que el PersistSink necesita del almacén tras extraer la
// proyección a los módulos (Plan 027 · Ola 3 · T8, ISP): el outbox append-only
// flow_events. La proyección tipada la aportan los modules.Projector inyectados.
type flowEventStore interface {
	InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error
}

// PersistSink es el EventSink que MATERIALIZA cada efecto en el outbox append-only
// flow_events y delega la PROYECCIÓN tipada a los modules.Projector registrados (Plan
// 027 · Ola 3 · T8, cierra H10). Ya NO conoce los efectos de ningún módulo: el switch
// central (survey_answer→survey_results, cart_*→intakes/intake_items) se movió a cada
// módulo (modules/survey, modules/cart). Añadir un módulo con proyección NO obliga a
// tocar este archivo (OCP): basta registrar su Projector en el arranque.
//
// El outbox flow_events es la bitácora completa (base de la telemetría por-paso, Δt
// entre efectos); la proyección es la vista consultable (GROUP BY, joins) que un
// JSONB no da con índice. La idempotencia es HEREDADA de la dedupe por
// last_wa_message_id del runtime.
type PersistSink struct {
	repo       flowEventStore
	projectors []modules.Projector
}

// NewPersistSink construye el sink con el almacén del outbox y los proyectores por
// módulo. Sin proyectores, solo escribe flow_events (los efectos de negocio no se
// proyectan a sus tablas tipadas).
func NewPersistSink(repo flowEventStore, projectors ...modules.Projector) *PersistSink {
	return &PersistSink{repo: repo, projectors: projectors}
}

// Handle persiste el efecto en flow_events y, si algún Projector registrado lo
// reconoce (Handles), delega en él la proyección tipada. Un fallo del
// INSERT/proyección se devuelve (el dispatcher del runtime lo LOGUEA; NO aborta el
// avance: el estado ya está persistido). Efectos sin proyector (navegación/telemetría)
// solo quedan en flow_events.
//
// La ÚNICA excepción a "siempre en flow_events" es modules.KindPrivate (Plan 041 ·
// T4.5): ese efecto lleva datos personales y flow_events es un outbox append-only,
// en claro y sin poda. Se salta el INSERT y va DIRECTO al proyector, que lo cifra.
// La regla vive aquí y no en el módulo a propósito: si dependiera de que cada
// módulo se acuerde de no emitir PII, bastaría un módulo nuevo distraído para
// escribirla; siendo del sink, la garantía es de la plataforma.
func (s *PersistSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	if eff.Kind != modules.KindPrivate {
		fe := store.FlowEvent{
			TenantID:    ec.TenantID,
			ContactID:   ec.ContactID,
			FlowID:      ec.FlowID,
			FlowVersion: ec.FlowVersion,
			Kind:        eff.Kind,
			Name:        eff.Name,
			Payload:     eff.Payload,
		}
		if err := s.repo.InsertFlowEvent(ctx, fe); err != nil {
			return err
		}
	}

	meta := modules.EffectMeta{
		TenantID:    ec.TenantID,
		ContactID:   ec.ContactID,
		SessionID:   ec.SessionID,
		FlowID:      ec.FlowID,
		FlowVersion: ec.FlowVersion,
	}
	for _, p := range s.projectors {
		if p.Handles(eff.Name) {
			return p.Project(ctx, meta, eff)
		}
	}
	return nil
}
