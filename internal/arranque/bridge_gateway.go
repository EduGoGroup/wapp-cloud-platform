// Adaptador de tipos entre el gateway gRPC VIEJO (internal/gateway/grpc) y el NUEVO
// (internal/modulos/edge/grpc). No porta ningún fichero: nace en F3 (T3.24, arquitectura §4 del
// plan de F3) para que el arranque nuevo cablee un solo edge/grpc.Server sin tocar el selector de
// inferencia viejo (E-1).

package arranque

import (
	"context"

	viejo "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia/local"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// gatewayBridge presenta el edge/grpc.Server NUEVO como el local.Frame que pide el selector de
// inferencia viejo (llmvia.WithFrame, fase 5). Hace falta porque local.Frame nombra el
// InferRequest del paquete VIEJO, y los dos InferRequest son tipos distintos para Go aunque
// tengan los mismos nueve campos: un *edgegrpc.Server no satisface local.Frame. Es el único
// consumidor del gateway que lo necesita; el resto (runtime, notificador, filtercfg, la cara
// HTTP) recibe el servidor nuevo tal cual.
//
// Delega en gw, que es EL MISMO puntero que cablea el resto del arranque: con dos servidores en
// el proceso, el Edge se conectaría a uno y las inferencias lo buscarían en el otro (T-4).
//
// Promesas:
//   - Infer copia el viejo.InferRequest en un edgegrpc.InferRequest campo a campo, los nueve, sin
//     reinterpretar ninguno (Class es un string sin tipo con los mismos valores en los dos), y
//     delega en gw.Infer con el mismo ctx y tenantID. Devuelve la salida y el error de gw tal
//     cual: el *edgegrpc.InferError NO se traduce al viejo, porque quien lo lee (llmvia) lo hace
//     por duck-typing de Motivo() y por errors.Is contra centinelas que comparten identidad.
//   - PlazaDe delega en gw.PlazaDe. 🔴 Tiene que existir CON ESE NOMBRE: el aforo por Edge lo
//     busca por aserción de tipo sobre el objeto que viaja como Frame (llmvia.go, s.frame.
//     (enrutadorDeEdges)); sin él la aserción da false y el aforo queda apagado con solo un Warn
//     (T-1 de F3).
//
// Vida: nace en F3 · muere en F4, cuando el selector de inferencia nuevo recibe el
// edge/grpc.Server directamente. Mientras viva, edge no entra en Conmutados.
type gatewayBridge struct {
	// gw es el servidor nuevo en el que se delega todo; el adaptador no guarda más estado.
	gw *edgegrpc.Server
}

// gatewayBridge es un local.Frame: si la firma de Infer cambia en cualquiera de los dos lados,
// esto no compila.
var _ local.Frame = (*gatewayBridge)(nil)

// Infer implementa local.Frame: copia la petición al tipo nuevo y delega (ver gatewayBridge).
func (b *gatewayBridge) Infer(ctx context.Context, tenantID string, req viejo.InferRequest) (string, error) {
	return b.gw.Infer(ctx, tenantID, toEdgeInferRequest(req))
}

// PlazaDe dice por qué Edge saldría una inferencia del tenant: delega en el servidor nuevo (ver
// gatewayBridge).
func (b *gatewayBridge) PlazaDe(tenantID, originSessionID string) (string, bool) {
	return b.gw.PlazaDe(tenantID, originSessionID)
}

// toEdgeInferRequest copia un InferRequest viejo en uno nuevo, campo a campo. Es una función
// aparte para poder afirmar la copia de los nueve campos sin un Edge conectado.
func toEdgeInferRequest(req viejo.InferRequest) edgegrpc.InferRequest {
	return edgegrpc.InferRequest{
		Prompt:          req.Prompt,
		Format:          req.Format,
		Temperature:     req.Temperature,
		Timeout:         req.Timeout,
		OriginSessionID: req.OriginSessionID,
		TargetSessionID: req.TargetSessionID,
		MaxOutputTokens: req.MaxOutputTokens,
		Class:           req.Class,
		Warmup:          req.Warmup,
	}
}
