// Porta internal/gateway/grpc/config_push.go @ cbc5736

package grpc

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
// el arranque componiendo el store de config + los entitlements; el Gateway queda
// genérico (no conoce "intents"). nil ⇒ no hay push al conectar.
//
// El Gateway entrega TODO lo que el proveedor le da, en su orden y cada config en
// su propio frame: hoy son tres kinds (jwks, intents, filters) y dos para el tenant
// sin llm_intent (filters no se gatea por entitlement).
type ConfigProvider interface {
	ConfigsForConnect(ctx context.Context, tenantID string) ([]ConfigPayload, error)
}

// WithConfigProvider inyecta el proveedor de config para el push al conectar
// (ADR-0021). Sin él (o con nil), Connect no empuja config (comportamiento previo
// intacto).
func WithConfigProvider(_ ConfigProvider) Option {
	panic(pendiente.Implementar("grpc.WithConfigProvider"))
}

// PushConfig empuja un ConfigUpdate (ADR-0021) a TODAS las sesiones vivas del
// tenant —las de todos sus Edge— y a ninguna más: ni a las de otro tenant, aunque
// compartan edge_id, ni al canal de control, que nunca es una sesión viva (R3.4.b).
// Lo invoca el PUT de la API de intents tras persistir (fan-out de config a
// las sesiones conectadas). Es best-effort: cada Push ya está acotado por
// sendTimeout y los fallos se loguean (en debug, "config push: a sesión") sin
// abortar (la config quedó persistida y el push al conectar reconcilia; no hay
// reintentos aquí). Devuelve nil siempre: un fallo de entrega no debe propagarse
// como fallo del PUT.
//
// Cada sesión recibe UN frame, con un command_id nuevo (UUIDv4, repetido en el sobre
// y dentro del ConfigUpdate), su propio session_id, y el kind, la version y el
// payload tal cual se dieron.
//
// El push es CONCURRENTE (mismo patrón que RevokeLease): una sesión bloqueada no
// retrasa la entrega al resto. PushConfig espera a que todos terminen.
//
// Cuando todos han terminado —y nunca antes— avisa por OnWarmup, si está cableado,
// de que la caché de prefijo puede haberse enfriado: UNA VEZ POR EDGE del tenant, no
// una por sesión, con (tenant, Edge, una sesión cualquiera de ese Edge, kind). El
// aviso sale por cada Edge con sesiones vivas, haya llegado o no su ConfigUpdate.
//
// El ctx es el del handler HTTP del PUT y desde el Plan 050 · T1.5-bis SÍ se usa:
// viaja hasta el Push de cada sesión (antes se descartaba con `_`): un llamante que
// se va deja de esperar a un Edge atascado.
func (s *Server) PushConfig(_ context.Context, _, _, _ string, _ []byte) error {
	panic(pendiente.Implementar("grpc.Server.PushConfig"))
}
