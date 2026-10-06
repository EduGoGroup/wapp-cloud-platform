package grpc

// El cableado del resultado de la inferencia en el stream (R-G13): el InferenceResult que
// manda el Edge se entrega INLINE en el bucle Recv. Va aparte de los tests de
// connect_route.go y connect.go porque lo que afirma es de la inferencia.

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

func inferenceResultFrame(sessionID string, res *cloudlinkv1.InferenceResult) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_InferenceResult{InferenceResult: res}}
}

// inferOverStream lanza una inferencia por la sesión dada del stream y vuelve cuando su frame
// ya está saliendo por el cable, con su command_id.
func inferOverStream(t *testing.T, srv *Server, edge *memEdge, sessionID string) (cmdID string, res <-chan inferResult) {
	t.Helper()
	pushed := make(chan struct{}, 8)
	edge.onSend = func() { pushed <- struct{}{} }
	res = goInfer(context.Background(), srv, "tenant-1", InferRequest{Prompt: "p", OriginSessionID: sessionID})
	await(t, pushed, "que el InferenceRequest salga por el stream")
	for _, id := range pendingInferIDs(srv) {
		srv.infersMu.Lock()
		mine := srv.infers[id].sessionID == sessionID
		srv.infersMu.Unlock()
		if mine {
			return id, res
		}
	}
	t.Fatalf("no hay inferencia en vuelo por la sesión %s", sessionID)
	return "", nil
}

// El InferenceResult se resuelve INLINE, como el Ack (ADR-0040 §Decisión.3): con el carril de
// su sesión tapado, cuando route vuelve la inferencia que esperaba ya tiene su resultado —el
// mismo frame, sin abrir— y en el carril no entró nada.
func TestRouteDeliversTheInferenceResultInlineWithTheLanePlugged(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	plugLane(t, rig.lane, "s-1", jobReceipt)
	waiting := seedInfer(rig.srv, "cmd-1", "s-1")
	other := seedInfer(rig.srv, "cmd-2", "s-1")
	res := sealedResult("cmd-1", []byte("sellado que nadie abre en el bucle Recv"))

	rig.srv.route(rig.lane, phone("tenant-1", "edge-1", "s-1"), inferenceResultFrame("s-1", res))

	if got, closed := inferState(waiting); got != res || closed {
		t.Fatalf("al volver route, la inferencia en vuelo tiene (%v, cerrado=%v): el resultado no se entregó inline", got, closed)
	}
	if got, closed := inferState(other); got != nil || closed {
		t.Error("el resultado llegó a una inferencia que no era la suya")
	}
	if total, _ := rig.lane.pending(); total != 0 {
		t.Errorf("el InferenceResult dejó %d jobs en el carril", total)
	}
	if rig.log.contains("payload EdgeToCloud desconocido") {
		t.Error("el InferenceResult cayó en la rama del payload desconocido: el resultado se pierde")
	}
}

// El camino de vuelta de una inferencia, de extremo a extremo: el InferenceRequest sale por el
// stream que registró la sesión, y el InferenceResult que el Edge manda por ese stream
// desbloquea a Infer con la salida del modelo.
func TestConnectCarriesAnInferenceOutAndItsResultBack(t *testing.T) {
	t.Parallel()
	rig := newInferRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, pongFrame("s-1", 1))

	cmdID, res := inferOverStream(t, rig.srv, edge, "s-1")
	edge.send(t, inferenceResultFrame("s-1", sealedResult(cmdID, rig.seal(t, modelOutput))))

	if got := await(t, res, "que la inferencia reciba su resultado"); got.err != nil || got.out != modelOutput {
		t.Fatalf("Infer = (%q, %v), se esperaba la salida del modelo", got.out, got.err)
	}
	frames := edge.received()
	if frame := frames[len(frames)-1]; frame.GetInferenceRequest() == nil || frame.GetCommandId() != cmdID || frame.GetSessionId() != "s-1" {
		t.Errorf("lo último que recibió el Edge no es el InferenceRequest de s-1: %v", frame)
	}
	requireNoPendingInfers(t, rig.srv)
}
