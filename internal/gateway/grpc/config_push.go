package gatewaygrpc

import (
	"context"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// ConfigPayload es una config lista para empujar a un Edge por ConfigUpdate
// (ADR-0021): el kind (espacio de nombres, p.ej. "intents"), su version de entidad
// y el payload validado. El Gateway la trata de forma OPACA: no interpreta el
// payload ni conoce los kinds concretos (los aporta el ConfigProvider).
type ConfigPayload struct {
	Kind    string
	Version string
	Payload []byte
}

// ConfigProvider entrega las configs vigentes que deben empujarse a un Edge del
// tenant AL CONECTAR (ADR-0021), ya gateadas por entitlements (ADR-0022): solo
// devuelve los kinds cuya feature tiene el tenant y que tienen config. Lo cablea
// cmd/server componiendo el store de config + los entitlements; el Gateway queda
// genérico (no conoce "intents"). nil ⇒ no hay push al conectar.
type ConfigProvider interface {
	ConfigsForConnect(ctx context.Context, tenantID string) ([]ConfigPayload, error)
}

// WithConfigProvider inyecta el proveedor de config para el push al conectar
// (ADR-0021). Sin él, Connect no empuja config (comportamiento previo intacto).
func WithConfigProvider(p ConfigProvider) Option { return func(s *Server) { s.configProvider = p } }

// PushConfig empuja un ConfigUpdate (ADR-0021) a TODAS las sesiones vivas del
// tenant. Lo invoca el PUT de la API de intents tras persistir (fan-out de config a
// las sesiones conectadas). Es best-effort: cada Push ya está acotado por
// sendTimeout y los fallos se loguean sin abortar (la config quedó persistida y el
// push al conectar reconcilia; no hay reintentos aquí). Devuelve nil siempre: un
// fallo de entrega no debe propagarse como fallo del PUT.
//
// El push es CONCURRENTE (mismo patrón que RevokeLease): una sesión bloqueada no
// retrasa la entrega al resto.
func (s *Server) PushConfig(_ context.Context, tenantID, kind, version string, payload []byte) error {
	var wg sync.WaitGroup
	for _, sid := range s.sessionsForTenant(tenantID) {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if err := s.pushConfig(sid, kind, version, payload); err != nil {
				s.log.Debug("config push: a sesión", "session_id", sid, "kind", kind, "error", err)
			}
		}(sid)
	}
	wg.Wait()
	return nil
}

// pushConfigsOnConnect empuja al Edge recién conectado las configs vigentes de su
// tenant (ADR-0021). No hace nada sin identidad mTLS (no se conoce el tenant) o sin
// ConfigProvider. Best-effort: un fallo del proveedor o de un push se loguea y no
// tumba el registro de la sesión.
func (s *Server) pushConfigsOnConnect(ctx context.Context, cc connCtx) {
	if s.configProvider == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	cfgs, err := s.configProvider.ConfigsForConnect(ctx, cc.tenantID)
	if err != nil {
		s.log.Error("config push: resolver configs al conectar", "error", err,
			"tenant_id", cc.tenantID, "session_id", cc.sessionID)
		return
	}
	for _, c := range cfgs {
		if err := s.pushConfig(cc.sessionID, c.Kind, c.Version, c.Payload); err != nil {
			s.log.Debug("config push: inicial a sesión", "session_id", cc.sessionID,
				"kind", c.Kind, "error", err)
		}
	}
}

// pushConfig arma el frame ConfigUpdate con un command_id nuevo y lo empuja a la
// sesión. El command_id sirve al Ack idempotente del Edge (frame existente); la
// nube no espera el Ack (push del servidor, como el lease).
func (s *Server) pushConfig(sessionID, kind, version string, payload []byte) error {
	cmdID, err := newCommandID()
	if err != nil {
		return err
	}
	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_ConfigUpdate{
			ConfigUpdate: &cloudlinkv1.ConfigUpdate{
				CommandId: cmdID,
				SessionId: sessionID,
				Kind:      kind,
				Version:   version,
				Payload:   payload,
			},
		},
	}
	return s.registry.Push(sessionID, msg)
}

// sessionsForTenant devuelve una copia de las sesiones vivas de TODOS los Edges del
// tenant (el kill-switch y el lease operan por Edge; la config es por tenant). Se
// recorre el índice edgeSessions bajo su candado.
func (s *Server) sessionsForTenant(tenantID string) []string {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	var out []string
	for k, set := range s.edgeSessions {
		if k.tenantID != tenantID {
			continue
		}
		for sid := range set {
			out = append(out, sid)
		}
	}
	return out
}
