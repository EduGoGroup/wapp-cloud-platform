package gatewaygrpc

import (
	"context"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
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
//
// El ctx es el del handler HTTP del PUT y desde el Plan 050 · T1.5-bis SÍ se usa:
// viaja hasta el Push de cada sesión (antes se descartaba con `_`). Las goroutines
// lo capturan y wg.Wait() las espera, así que nunca sobreviven al ctx que acotan.
func (s *Server) PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error {
	var wg sync.WaitGroup
	for _, sid := range s.sessionsForTenant(tenantID) {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if err := s.pushConfig(ctx, sid, kind, version, payload); err != nil {
				s.log.Debug("config push: a sesión", "session_id", sid, "kind", kind, "error", err)
			}
		}(sid)
	}
	wg.Wait()
	// 🔴 EL CONFIGUPDATE ES LO QUE ENFRÍA LA CACHÉ, y por eso el calentamiento cuelga
	// de AQUÍ y no solo del handshake: el prefijo del prompt de un tenant se arma
	// desde su catálogo de intenciones, así que publicarlo INVALIDA el prefijo que
	// estuviera caliente en el Ollama de cada Edge. Sin esto, el siguiente mensaje del
	// cliente vuelve a pagar el prefill frío (~50 s) aunque el Edge lleve horas
	// conectado — que es justo el caso que T1.7-4 viene a quitar.
	//
	// 🔴 UNO POR EDGE, NO UNO POR SESIÓN: ver calentarEdges.
	//
	// ⚠️ Va DESPUÉS del wg.Wait() a propósito: calentar con el ConfigUpdate todavía en
	// vuelo dejaría cacheado el prefijo VIEJO en el Edge que aún no lo ha aplicado, o
	// sea, exactamente lo contrario de lo que se busca.
	s.calentarEdges(tenantID, kind)
	return nil
}

// calentarEdges avisa de que la caché de prefijo de cada Edge del tenant PUEDE haberse
// quedado fría, UNA VEZ POR EDGE.
//
// «Puede» y no «se quedó»: el kind viaja hacia arriba sin interpretarse porque este
// paquete no sabe cuál de los tres que empuja forma el prompt. Ver OnWarmup.
//
// 🔴 POR EDGE Y NO POR SESIÓN, que es el error fácil aquí: un Edge multiplexa TODAS
// las sesiones del tenant sobre UN stream (ADR-0008) y tiene UN Ollama con UNA plaza.
// Avisar por sesión dispararía N calentamientos idénticos contra esa única plaza — un
// tenant con cinco teléfonos en una máquina pagaría cinco prefills fríos seguidos por
// publicar su catálogo, y durante esos ~250 s ninguna inferencia real cabría. El
// cerrojo «uno en vuelo por Edge» del consumidor tapa la mayor parte, pero apoyarse en
// él sería resolver aguas abajo un dato que aquí se tiene exacto.
//
// La sesión que se elige dentro de cada Edge es indiferente y por eso no se ordena:
// las del mismo Edge son literalmente el mismo cable. Lo que importa es que sea UNA.
func (s *Server) calentarEdges(tenantID, kind string) {
	if s.OnWarmup == nil {
		return
	}
	for k, sid := range s.unaSesionPorEdge(tenantID) {
		s.OnWarmup(tenantID, k, sid, kind)
	}
}

// unaSesionPorEdge devuelve, por cada Edge vivo del tenant, UNA de sus sesiones
// (edge_id -> session_id). Recorre el mismo índice que sessionsForTenant, bajo su
// candado, y no llama a nadie mientras lo tiene tomado.
func (s *Server) unaSesionPorEdge(tenantID string) map[string]string {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	out := make(map[string]string)
	for k, set := range s.edgeSessions {
		if k.tenantID != tenantID {
			continue
		}
		for sid := range set {
			out[k.edgeID] = sid
			break
		}
	}
	return out
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
		if err := s.pushConfig(ctx, cc.sessionID, c.Kind, c.Version, c.Payload); err != nil {
			s.log.Debug("config push: inicial a sesión", "session_id", cc.sessionID,
				"kind", c.Kind, "error", err)
		}
	}
}

// pushConfig arma el frame ConfigUpdate con un command_id nuevo y lo empuja a la
// sesión. El command_id sirve al Ack idempotente del Edge (frame existente); la
// nube no espera el Ack (push del servidor, como el lease).
//
// Recibe ctx (Plan 050 · T1.5-bis) SOLO para pasárselo al Push: es el del PUT en el
// fan-out y el del stream en el push al conectar. Nunca se inventa aquí.
func (s *Server) pushConfig(ctx context.Context, sessionID, kind, version string, payload []byte) error {
	msg, err := buildConfigUpdate(sessionID, kind, version, payload)
	if err != nil {
		return err
	}
	return s.registry.Push(ctx, sessionID, msg)
}

// buildConfigUpdate arma el frame, sin enviarlo. Está separado del envío desde el
// Plan 057 · Ola 2 · T2.2 porque hay DOS caminos de escritura y solo uno pasa por el
// Registry: el fan-out del PUT resuelve la sesión (pushConfig, arriba) y el push al
// conectar de un Edge SIN NINGUNA SESIÓN escribe in-band sobre su propio stream
// (pushConfigsInBand). El frame es idéntico por los dos; lo que cambia es el cable.
func buildConfigUpdate(sessionID, kind, version string, payload []byte) (*cloudlinkv1.CloudToEdge, error) {
	cmdID, err := newCommandID()
	if err != nil {
		return nil, err
	}
	return &cloudlinkv1.CloudToEdge{
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
	}, nil
}

// pushConfigsInBand empuja las configs vigentes del tenant por el STREAM del Edge, sin
// pasar por el Registry (Plan 057 · Ola 2 · T2.2, REQ-057.7).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 ESTA FUNCIÓN EXISTE PARA TAPAR UN AGUJERO QUE ABRE LA MISMA OLA
// ════════════════════════════════════════════════════════════════════════════
//
// La Ola 2 deja de registrar el canal de control como sesión. Bien: era una clave
// global compartida por todos los Edge del planeta. Pero el push de config al conectar
// (ADR-0021) colgaba de que ALGUNA sesión se registrara —vive dentro de
// registerSession—, y para un Edge RECIÉN ARRANCADO SIN NINGÚN TELÉFONO EMPAREJADO el
// frame de auth era el ÚNICO que provocaba registro. Es decir: ese Edge recibía su
// catálogo de intenciones POR EL CANAL DE CONTROL, y solo por ahí.
//
// Retirar el registro sin esto lo dejaría arrancando con el catálogo VACÍO —
// clasificando contra nada— y el síntoma no habría sido un test rojo ni un log de
// error: simplemente el Edge no reconocería ninguna intención hasta que el operador
// emparejara un teléfono. Por eso el §5.2.2 del análisis de origen, que proponía dejar
// de empujar config al canal de control junto con el lease, se sigue solo a medias: el
// LEASE sí sobra (no hay teléfono ni DEK detrás, y el Edge lo descarta con un Warn);
// la CONFIG no.
//
// La diferencia con pushConfigsOnConnect es únicamente el cable: aquí no hay sesión que
// resolver, se escribe sobre el stream que acaba de hablar. Best-effort e idempotente
// en el Edge (handleConfigUpdate aplica por kind/version y ack-ea por command_id), así
// que un solape con el push de una sesión real no hace daño.
func (s *Server) pushConfigsInBand(ctx context.Context, cc connCtx) {
	if s.configProvider == nil || !cc.hasIdentity || cc.sender == nil {
		return
	}
	cfgs, err := s.configProvider.ConfigsForConnect(ctx, cc.tenantID)
	if err != nil {
		s.log.Error("config push: resolver configs al conectar (in-band)", "error", err,
			"tenant_id", cc.tenantID, "edge_id", cc.edgeID)
		return
	}
	for _, c := range cfgs {
		msg, buildErr := buildConfigUpdate(cc.sessionID, c.Kind, c.Version, c.Payload)
		if buildErr != nil {
			s.log.Error("config push: armar ConfigUpdate (in-band)", "error", buildErr,
				"kind", c.Kind, "edge_id", cc.edgeID)
			continue
		}
		if sendErr := session.SendAcotado(ctx, cc.sender, msg, s.registry.SendTimeout(), cc.edgeID); sendErr != nil {
			s.log.Debug("config push: inicial in-band", "edge_id", cc.edgeID,
				"kind", c.Kind, "error", sendErr)
		}
	}
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
