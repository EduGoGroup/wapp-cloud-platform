//go:build pendiente

package grpc

// El orden entre el latido y el calentamiento del registro, que lo pone el bucle de Connect
// (R-G16, R-G17; T-6, T-13): PRIMERO se encamina el frame —y con él se aprende, inline, lo que
// el Edge dice de su capacidad de inferencia— y DESPUÉS, solo si ese frame registró una
// sesión nueva, se pregunta si hay que calentar. El frame que registra es, en el caso normal,
// el primer latido de la sesión: con el orden inverso se calentaría a un Edge que acaba de
// decir que no puede.

import (
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// warmRig es el banco de route con los dos hooks del flanco apuntando lo que reciben. Sin
// candado A PROPÓSITO: corren inline en el bucle Recv y el test los lee con el bucle parado
// en Recv; si alguien los lanzara aparte, -race lo diría.
type warmRig struct {
	*routeRig
	warms   []warmCall
	readies []readyCall
}

func newWarmRig(t *testing.T) *warmRig {
	t.Helper()
	r := &warmRig{routeRig: newRouteRig(t)}
	r.srv.OnWarmup = func(tenantID, edgeID, sessionID, kind string) {
		r.warms = append(r.warms, warmCall{tenantID, edgeID, sessionID, kind})
	}
	r.srv.OnEdgeReady = func(tenantID, edgeID string) { r.readies = append(r.readies, readyCall{tenantID, edgeID}) }
	return r
}

func (r *warmRig) require(t *testing.T, when string, warmups, wakeups int) {
	t.Helper()
	if len(r.warms) != warmups || len(r.readies) != wakeups {
		t.Fatalf("%s: calentamientos = %d, avisos al pipeline = %d; se esperaban %d y %d",
			when, len(r.warms), len(r.readies), warmups, wakeups)
	}
}

func saying(sessionID string, readiness cloudlinkv1.InferenceReadiness) *cloudlinkv1.EdgeToCloud {
	return heartbeatFrame(sessionID, &cloudlinkv1.Heartbeat{InferenceReadiness: readiness})
}

// Orden 1 — el primer latido no dice nada (T-6: el cero NO es «no puede»): calienta el
// registro, con el kind vacío y la dirección de esa sesión. Y UNA vez: los latidos siguientes
// de la misma sesión ya no son un registro; una sesión nueva del mismo Edge callado, sí.
func TestConnectWarmsOnRegisterWhenTheFirstHeartbeatSaysNothing(t *testing.T) {
	t.Parallel()
	rig := newWarmRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))

	edge.send(t, saying("s-1", saysNothing))
	rig.require(t, "primer latido callado", 1, 0)
	if want := (warmCall{"tenant-1", "edge-1", "s-1", ""}); rig.warms[0] != want {
		t.Errorf("OnWarmup recibió %+v, se esperaba %+v", rig.warms[0], want)
	}

	edge.send(t, saying("s-1", saysNothing), pongFrame("s-1", 1), saying("s-1", saysNothing))
	rig.require(t, "más frames de la misma sesión", 1, 0)

	edge.send(t, saying("s-2", saysNothing))
	rig.require(t, "una sesión nueva del mismo Edge callado", 2, 0)
}

// Orden 2 — el primer latido dice DOWN: CERO calentamientos, ni por el registro ni por los
// latidos siguientes, hasta que diga READY; entonces uno, el del flanco, que además despierta
// al pipeline.
func TestConnectDoesNotWarmAnEdgeWhoseFirstHeartbeatSaysDown(t *testing.T) {
	t.Parallel()
	rig := newWarmRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))

	edge.send(t, saying("s-1", saysDown), saying("s-1", saysDown), saying("s-2", saysDown))
	rig.require(t, "Edge que dice DOWN", 0, 0)

	edge.send(t, saying("s-1", saysReady))
	rig.require(t, "el flanco a READY", 1, 1)
}

// Orden 3 — el primer latido dice READY: lo dispara la TRANSICIÓN y no el registro. Uno, no
// dos; y otra sesión del mismo Edge, ya READY, no añade ninguno.
func TestConnectWarmsOnceWhenTheFirstHeartbeatSaysReady(t *testing.T) {
	t.Parallel()
	rig := newWarmRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))

	edge.send(t, saying("s-1", saysReady))
	rig.require(t, "primer latido READY", 1, 1)

	edge.send(t, saying("s-2", saysReady), saying("s-1", saysReady))
	rig.require(t, "más sesiones y latidos del Edge ya READY", 1, 1)
}

// Lo aprendido muere con la ÚLTIMA sesión del Edge: tras colgar, un stream nuevo del mismo
// Edge que no dice nada vuelve a calentarse por el registro (tras DOWN no lo haría).
func TestConnectForgetsReadinessWhenTheStreamCloses(t *testing.T) {
	t.Parallel()
	rig := newWarmRig(t)
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.send(t, saying("s-1", saysDown), saying("s-2", saysDown))
	edge.hangUp(t)
	rig.require(t, "Edge que dijo DOWN y colgó", 0, 0)

	again := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	again.send(t, saying("s-1", saysNothing))
	rig.require(t, "el mismo Edge, de vuelta y callado", 1, 0)
}

// T-13: los hooks corren INLINE en la goroutine del bucle Recv. Mientras uno no vuelve, el
// bucle no pide el siguiente frame: por eso su contrato exige que disparen y vuelvan.
func TestConnectRunsTheHooksInsideTheRecvLoop(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	var edge *memEdge
	recvsSeen := map[string]int32{}
	inside := func(hook string) {
		letRun() // si el hook corriera aparte, el bucle ya habría vuelto a Recv
		recvsSeen[hook] = edge.recvs.Load()
	}
	rig.srv.OnIncoming = func(string, *cloudlinkv1.IncomingMessage) { inside("OnIncoming") }
	rig.srv.OnHeartbeat = func(string, *cloudlinkv1.Heartbeat) { inside("OnHeartbeat") }
	rig.srv.OnEdgeReady = func(string, string) { inside("OnEdgeReady") }
	rig.srv.OnWarmup = func(string, string, string, string) { inside("OnWarmup") }
	edge = openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	edge.settle(t)

	edge.send(t, incomingFrame("s-1", &cloudlinkv1.IncomingMessage{WaMessageId: "a"})) // 1.er Recv; registra y calienta
	if recvsSeen["OnIncoming"] != 1 || recvsSeen["OnWarmup"] != 1 {
		t.Fatalf("durante el primer frame el bucle ya había vuelto a Recv: %v", recvsSeen)
	}
	edge.send(t, saying("s-1", saysReady)) // 2.º Recv
	for _, hook := range []string{"OnHeartbeat", "OnWarmup", "OnEdgeReady"} {
		if recvsSeen[hook] != 2 {
			t.Errorf("%s corrió con el bucle ya en su Recv n.º %d: no es inline", hook, recvsSeen[hook])
		}
	}
}
