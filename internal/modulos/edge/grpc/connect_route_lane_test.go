package grpc

// Lo que route SUELTA al carril (R-G1, R-G3, R-G19, R3.4.a): el acuse, el bundle de
// diagnóstico y las tres de auth. Con el worker de la sesión tapado no ha ocurrido nada cuando
// route vuelve; al soltarlo, cada trabajo llega a su puerto con el ctx del job. Y submitJob,
// la puerta única al carril, que nunca pierde un trabajo en silencio.

import (
	"context"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
)

// Los acuses van al carril y NUNCA se coalescen: dos acuses con el worker ocupado son dos
// jobs, y el sink los recibe los dos, en orden y con un ctx con plazo.
func TestRouteSendsReceiptsToTheLaneWithoutCoalescing(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	rig.srv.route(rig.lane, cc, receiptFrame("s-1", "cmd-1"))
	rig.srv.route(rig.lane, cc, receiptFrame("s-1", "cmd-2"))

	if n := len(rig.sink.got); n != 0 {
		t.Fatalf("con el carril tapado el sink ya tenía %d acuses: el acuse se resolvió inline", n)
	}
	if n := queueLen(rig.lane, "s-1"); n != 2 {
		t.Fatalf("dos acuses dejaron %d jobs, se esperaban 2 (no se coalescen)", n)
	}
	release()
	closeLane(t, rig.lane)

	if len(rig.sink.got) != 2 || rig.sink.got[0].GetCommandId() != "cmd-1" || rig.sink.got[1].GetCommandId() != "cmd-2" {
		t.Fatalf("el sink recibió %v, se esperaban cmd-1 y cmd-2 en ese orden", rig.sink.got)
	}
	for i, ctx := range rig.sink.ctxs {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("el acuse %d llegó al sink con un ctx sin plazo", i)
		}
	}
}

// El bundle de diagnóstico va al carril y se almacena con la identidad del stream y el
// session_id del frame.
func TestRouteSendsTheDiagnosticsBundleToTheLane(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	rig.srv.route(rig.lane, phone("tenant-1", "edge-1", "s-1"), bundleFrame("s-1", "cmd-d"))

	if n := len(rig.diag.calls); n != 0 {
		t.Fatalf("con el carril tapado ya se habían guardado %d bundles", n)
	}
	release()
	closeLane(t, rig.lane)

	if len(rig.diag.calls) != 1 {
		t.Fatalf("se guardaron %d bundles, se esperaba 1", len(rig.diag.calls))
	}
	got := rig.diag.calls[0]
	if got.tenantID != "tenant-1" || got.sessionID != "s-1" || got.commandID != "cmd-d" || got.bundle.LogTail != "log tail" {
		t.Errorf("bundle guardado = %+v", got)
	}
}

// Las tres de auth van al carril y contestan por el CABLE del stream que preguntó (el sender
// del connCtx), nunca por el Registry: quien ocupe allí la clave de control no recibe nada.
func TestRouteAnswersAuthFromTheLaneOnTheStreamThatAsked(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	intruder := rig.goLive(t, cltransport.ControlSessionID)
	stream := &liveSession{id: "el stream del Edge"}
	cc := controlChannel("tenant-1", "edge-1", stream)
	release := plugLane(t, rig.lane, cltransport.ControlSessionID, jobReceipt)

	rig.srv.route(rig.lane, cc, loginFrame(cltransport.ControlSessionID, "cmd-login"))
	rig.srv.route(rig.lane, cc, refreshFrame(cltransport.ControlSessionID, "cmd-refresh", "refresh-tenant-1"))
	rig.srv.route(rig.lane, cc, logoutFrame(cltransport.ControlSessionID, "cmd-logout"))

	if n := len(stream.received()); n != 0 || rig.authn.calls() != 0 {
		t.Fatalf("con el carril tapado ya había %d respuestas y %d llamadas al puerto", n, rig.authn.calls())
	}
	release()
	closeLane(t, rig.lane)

	frames := stream.received()
	if len(frames) != 3 {
		t.Fatalf("el stream recibió %d frames, se esperaban las 3 respuestas", len(frames))
	}
	requireTokens(t, requireAuthResponse(t, frames[0], cc, "cmd-login"), tokensFor("tenant-1", "user-1"))
	requireTokens(t, requireAuthResponse(t, frames[1], cc, "cmd-refresh"), tokensFor("tenant-1", "user-1"))
	if tokens := requireAuthResponse(t, frames[2], cc, "cmd-logout").GetTokens(); tokens == nil || tokens.GetAccessToken() != "" {
		t.Errorf("el logout contestó %v, se esperaba un UserTokens vacío", frames[2].GetUserAuthResponse())
	}
	requireNothing(t, intruder)
}

// Un frame SIN session_id no llega a encolarse: queda un Warn que nombra al Edge y el tipo de
// trabajo, el trabajo no corre y no nace ninguna cola bajo la llave vacía.
func TestSubmitJobRejectsAFrameWithoutSessionID(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "")

	rig.srv.route(rig.lane, cc, receiptFrame("", "cmd-1"))
	rig.srv.route(rig.lane, cc, heartbeatFrame("", fullHeartbeat()))

	if n := queueLen(rig.lane, ""); n != -1 {
		t.Errorf("nació una cola bajo la llave vacía (con %d jobs)", n)
	}
	closeLane(t, rig.lane)
	if len(rig.sink.got) != 0 || rig.tl.String() != "" {
		t.Errorf("un frame sin session_id llegó a su puerto (acuses %d, escrituras %q)", len(rig.sink.got), rig.tl.String())
	}
	const warn = `msg="carril: frame sin session_id; el trabajo no se encola (no hay sesión a la que atribuirlo)"`
	if got := strings.Count(rig.log.String(), warn); got != 2 {
		t.Fatalf("avisos por frame sin session_id = %d, se esperaban 2: %q", got, rig.log.String())
	}
	for _, want := range []string{"level=WARN", "edge_id=edge-1", "kind=receipt", "kind=heartbeat"} {
		if !rig.log.contains(want) {
			t.Errorf("al aviso le falta %q: %q", want, rig.log.String())
		}
	}
}

// Con el carril ya sellado, el trabajo no se pierde en silencio: no corre, y queda un Warn con
// la sesión, el Edge, el tipo y el motivo.
func TestSubmitJobSaysWhenTheLaneRejectsTheWork(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	closeLane(t, rig.lane)
	ran := false

	rig.srv.submitJob(rig.lane, phone("tenant-1", "edge-1", "s-1"), jobAuth, func(context.Context) { ran = true })

	if ran {
		t.Error("el trabajo corrió con el carril sellado")
	}
	for _, want := range []string{
		"level=WARN", `msg="carril: el trabajo no se encoló"`, "session_id=s-1", "edge_id=edge-1", "kind=auth", errLaneSealed.Error(),
	} {
		if !rig.log.contains(want) {
			t.Errorf("al aviso le falta %q: %q", want, rig.log.String())
		}
	}
}

// El caso normal: el trabajo corre en el carril de SU sesión, con el ctx del job (con plazo),
// y no deja rastro en el log.
func TestSubmitJobRunsTheWorkOnTheSessionLane(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	bounded := make(chan bool, 1)

	rig.srv.submitJob(rig.lane, phone("tenant-1", "edge-1", "s-1"), jobDiagnostics, func(ctx context.Context) {
		_, ok := ctx.Deadline()
		bounded <- ok
	})

	if !await(t, bounded, "que el trabajo corra") {
		t.Error("el trabajo corrió con un ctx sin plazo")
	}
	if n := queueLen(rig.lane, "s-1"); n == -1 {
		t.Error("el trabajo no pasó por la cola de su sesión")
	}
	if rig.log.String() != "" {
		t.Errorf("un submit limpio no deja rastro: %q", rig.log.String())
	}
}

// Sin sender en el connCtx no hay cable por el que contestar: la respuesta de auth NO cae al
// Registry (T-5), aunque allí haya alguien bajo ese session_id.
func TestRouteAuthWithoutASenderNeverFallsBackToTheRegistry(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	intruder := rig.goLive(t, cltransport.ControlSessionID)
	cc := controlChannel("tenant-1", "edge-1", nil)

	rig.srv.route(rig.lane, cc, &cloudlinkv1.EdgeToCloud{
		SessionId: cltransport.ControlSessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_UserLogin{UserLogin: loginRequest("cmd-login")},
	})
	closeLane(t, rig.lane)

	requireNothing(t, intruder)
}
