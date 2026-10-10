// Porta internal/flujos/runtime/log_sink.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// LogSink es la implementación log-only y DEFAULT de EventSink (Plan 015 · T2),
// análoga al sink de acuses log-only del gateway. Registra el efecto en log
// estructurado —SOLO metadatos— y NO persiste: no recibe almacén alguno.
//
// No implementa PhasedSink: corre en PhaseProject, donde corría antes de que las
// fases existieran.
//
// En el rojo no lleva campos. El verde le pone uno: el logger.
type LogSink struct{}

// NewLogSink construye el sink log-only con el logger dado. Nunca devuelve nil, tampoco
// con un logger nil (ese sink queda mudo: ver Handle).
func NewLogSink(log logger.Logger) *LogSink {
	panic(pendiente.Implementar("runtime.NewLogSink"))
}

// Handle registra el efecto y no falla NUNCA: devuelve siempre nil. Es el enganche
// mínimo por defecto a la espera de un sink que persista.
//
// Escribe UNA línea en nivel Info con el mensaje literal
// "runtime: efecto despachado (log-only)" y exactamente estas seis claves:
//
//   - "kind" = eff.Kind
//   - "name" = eff.Name
//   - "tenant" = ec.TenantID
//   - "contact_id" = ec.ContactID (opaco)
//   - "flow_id" = ec.FlowID
//   - "version" = ec.FlowVersion
//
// Higiene §10.G: NUNCA loguea eff.Payload —ni sus claves ni sus valores: es dato de
// negocio— ni PII. Tampoco loguea session_id ni event_id: no están entre las seis.
//
// No toca el efecto: no escribe en eff.Payload (lo comparten los demás sinks del
// fan-out). No usa el ctx.
//
// Sobre un receptor nil, o un sink construido con logger nil, devuelve nil sin
// escribir nada y sin entrar en pánico.
func (s *LogSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	panic(pendiente.Implementar("runtime.LogSink.Handle"))
}
