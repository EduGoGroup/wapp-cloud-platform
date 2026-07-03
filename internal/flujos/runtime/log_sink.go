package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// LogSink es la implementación log-only y DEFAULT de EventSink (Plan 015 · T2),
// análoga a gatewaygrpc.LogReceiptSink. Registra el efecto en log estructurado
// —SOLO metadatos (kind, name, tenant, contact_id opaco, flow_id, version)— y
// NO persiste. Higiene §10.G: NUNCA loguea eff.Payload (dato de negocio) ni PII.
type LogSink struct {
	log logger.Logger
}

// NewLogSink construye el sink log-only con el logger dado.
func NewLogSink(log logger.Logger) *LogSink {
	return &LogSink{log: log}
}

// Handle registra el efecto en log estructurado (sin payload) y no falla nunca:
// es el enganche mínimo por defecto a la espera de un sink que persista.
func (s *LogSink) Handle(_ context.Context, ec EffectContext, eff modules.Effect) error {
	if s == nil || s.log == nil {
		return nil
	}
	s.log.Info("runtime: efecto despachado (log-only)",
		"kind", eff.Kind,
		"name", eff.Name,
		"tenant", ec.TenantID,
		"contact_id", ec.ContactID,
		"flow_id", ec.FlowID,
		"version", ec.FlowVersion,
	)
	return nil
}
