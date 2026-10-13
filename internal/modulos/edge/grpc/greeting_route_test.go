package grpc

// El enganche del saludo al job del latido (submitHeartbeat, en connect_route.go): es la CUARTA
// parte del job y la ÚLTIMA, después de renewLease. Los demás tests del job
// (connect_route_heartbeat_test.go) montan una flota que no sabe saludar, así que su línea de
// tiempo no cambia; aquí se monta una que sí. Trozo de greeting_test.go, partido por tema.

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// greetingTimeline es el latido completo de una sesión pendiente de aviso: las tres escrituras
// de siempre y, DESPUÉS del lease, la consulta y la marca del saludo.
const greetingTimeline = heartbeatTimeline + " > PendingGreeting edges=1 > MarkGreeted edges=1"

// newGreetingRouteRig es el banco de route con una flota que sabe saludar —sus dos llamadas se
// apuntan en la MISMA línea de tiempo que fleet y lease— y un Edge que acusa en la sesión s-1.
// El número NO está sembrado: el saludo lo lee de la fila, donde lo deja persistSelfPn.
func newGreetingRouteRig(t *testing.T) (*routeRig, *greeterFleet, *ackingEdge) {
	t.Helper()
	greeter := &greeterFleet{}
	rig := newRouteRig(t, WithFleet(greeter))
	greeter.Repository = timedFleet{rig.fleet, rig.tl}
	greeter.tl = rig.tl
	greeter.lookup = func(ctx context.Context, tenantID, edgeID, sessionID string) string {
		row, found, err := rig.fleet.Get(ctx, tenantID, edgeID, sessionID)
		if err != nil || !found {
			return ""
		}
		return row.SelfPn
	}
	edge := &ackingEdge{srv: rig.srv}
	t.Cleanup(rig.reg.Register("s-1", edge))
	return rig, greeter, edge
}

// El saludo es la ÚLTIMA llamada del job del latido: después de persistSelfPn —lee el número
// que ese acaba de dejar en la fila— y después de renewLease —no le roba el presupuesto al
// lease del que depende para poder enviar—. Corre en el carril, con el plazo del job.
func TestRouteHeartbeatGreetsLastAfterRenewingTheLease(t *testing.T) {
	t.Parallel()
	rig, greeter, edge := newGreetingRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", fullHeartbeat()))

	if got := rig.tl.String(); got != "" || len(edge.sent()) != 0 {
		t.Fatalf("con el carril tapado ya se había saludado (%q, %d avisos): el saludo no fue al carril", got, len(edge.sent()))
	}
	release()
	closeLane(t, rig.lane)

	if got := rig.tl.String(); got != greetingTimeline {
		t.Fatalf("el latido escribió %q, se esperaba %q", got, greetingTimeline)
	}
	rig.tl.requireBounded(t)
	if got := edge.order(); got != "lease > text" {
		t.Errorf("el Edge recibió %q, se esperaba el lease renovado ANTES del aviso", got)
	}
	sent := edge.sent()
	if len(sent) != 1 || sent[0].GetSendText().GetTo() != ownNumber || sent[0].GetSendText().GetText() != passiveSessionNoticeV1 {
		t.Fatalf("avisos enviados = %v, se esperaba UNO, con el literal, al número que el latido acaba de persistir", sent)
	}
	if !greeter.isGreeted() {
		t.Error("la sesión no quedó marcada tras el Ack del aviso")
	}
	requireNoPII(t, rig.log)
}

// El segundo latido de una sesión ya saludada pregunta y se calla: un job, una consulta, cero
// avisos más.
func TestRouteHeartbeatOfAGreetedSessionOnlyAsks(t *testing.T) {
	t.Parallel()
	rig, greeter, edge := newGreetingRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	greeter.markedByAnother()

	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", fullHeartbeat()))
	closeLane(t, rig.lane)

	if got, want := rig.tl.String(), heartbeatTimeline+" > PendingGreeting edges=1"; got != want {
		t.Errorf("el latido escribió %q, se esperaba %q", got, want)
	}
	if n := len(edge.sent()); n != 0 {
		t.Errorf("una sesión ya saludada recibió %d avisos", n)
	}
}

// Un latido LOGGED_OUT no saluda: la sesión está muerta y su job es solo la marca de zombie.
func TestRouteLoggedOutHeartbeatDoesNotGreet(t *testing.T) {
	t.Parallel()
	rig, greeter, edge := newGreetingRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	if err := rig.fleet.SetSelfPn(t.Context(), cc.tenantID, cc.edgeID, cc.sessionID, ownNumber); err != nil {
		t.Fatalf("SetSelfPn: %v", err)
	}

	gone := fullHeartbeat()
	gone.State = cloudlinkv1.SessionState_SESSION_STATE_LOGGED_OUT
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", gone))
	closeLane(t, rig.lane)

	if got := rig.tl.String(); got != "MarkLoggedOut edges=0" {
		t.Errorf("el logout escribió %q, se esperaba solo MarkLoggedOut", got)
	}
	if n := len(edge.sent()); n != 0 || greeter.count("PendingGreeting") != 0 {
		t.Errorf("una sesión zombie fue saludada (%d avisos, %d consultas)", n, greeter.count("PendingGreeting"))
	}
}
