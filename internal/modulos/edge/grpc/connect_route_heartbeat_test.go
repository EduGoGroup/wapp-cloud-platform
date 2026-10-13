package grpc

// El job del latido (submitHeartbeat; R-G2, R-G6, R-G18), visto desde route: UN job con las
// cuatro partes en su orden, coalescible; el logout aparte, en un job que ningún latido
// posterior borra; y lo que el Edge dice de su capacidad de inferencia, aprendido INLINE y
// ANTES de soltar nada al carril. Qué hace cada parte está en connect_heartbeat*_test.go.

import (
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// heartbeatTimeline es lo que un latido completo le pide a fleet y a lease, en orden: el
// número, la salud —con el parte de inferencia YA en el almacén— y el lease al final.
const heartbeatTimeline = "SetSelfPn edges=0 > SaveHealth edges=1 > Upsert edges=1"

// online deja la fila de flota de la sesión, como hace el handshake antes del primer latido
// (sin pasar por la línea de tiempo).
func (r *routeRig) online(t *testing.T, cc connCtx) {
	t.Helper()
	if err := r.fleet.MarkOnline(t.Context(), cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
		t.Fatalf("MarkOnline(%s): %v", cc.sessionID, err)
	}
}

func (r *routeRig) row(t *testing.T, cc connCtx) fleet.Session {
	t.Helper()
	s, found, err := r.fleet.Get(t.Context(), cc.tenantID, cc.edgeID, cc.sessionID)
	if err != nil || !found {
		t.Fatalf("Get(%s) = (found=%v, err=%v), se esperaba la fila", cc.sessionID, found, err)
	}
	return s
}

// leaseCounter devuelve el contador persistido del lease del Edge, o -1 si no hay estado.
func (r *routeRig) leaseCounter(t *testing.T, cc connCtx) int64 {
	t.Helper()
	st, found, err := r.leaseRepo.Get(t.Context(), cc.tenantID, cc.edgeID)
	if err != nil {
		t.Fatalf("Get del lease: %v", err)
	}
	if !found {
		return -1
	}
	return st.Counter
}

// El latido ALIMENTA los tres almacenes desde UN job del carril: nada ocurre mientras el
// worker de la sesión está tapado, y al soltarlo se persisten el número, el parte de
// inferencia, la salud y el lease, en ese orden y cada escritura con su plazo.
func TestRouteHeartbeatFeedsFleetStatsAndLeaseFromTheLane(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	live := rig.goLive(t, "s-1")
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", fullHeartbeat()))

	if got := rig.tl.String(); got != "" || len(live.received()) != 0 || rig.stats.Aggregated().Edges != 0 {
		t.Fatalf("con el carril tapado ya se había escrito (%q, %d frames): el latido no fue al carril", got, len(live.received()))
	}
	release()
	closeLane(t, rig.lane)

	if got := rig.tl.String(); got != heartbeatTimeline {
		t.Fatalf("el latido escribió %q, se esperaba %q", got, heartbeatTimeline)
	}
	rig.tl.requireBounded(t)
	row := rig.row(t, cc)
	if row.SelfPn != "573001112233" || row.WhatsappState != "connected" {
		t.Errorf("fila de flota = (self_pn %q, whatsapp %q), se esperaba el número y la salud del latido", row.SelfPn, row.WhatsappState)
	}
	if got := rig.stats.Aggregated().PorClase["lote"]; got != 1 {
		t.Errorf("el almacén de inferencia tiene lote=%d, se esperaba 1", got)
	}
	if got := rig.leaseCounter(t, cc); got != 8 {
		t.Errorf("contador del lease = %d, se esperaba 8 (el del latido más uno)", got)
	}
	validator := cllease.NewValidator(rig.mgr.PublicKey())
	if err := validator.Apply(onlyLeaseUpdate(t, live)); err != nil || !validator.CanOperate(true) {
		t.Errorf("el Edge no podría operar con el lease renovado (Apply=%v)", err)
	}
}

// Dos latidos con el worker ocupado son UN job: el segundo sustituye al primero entero (las
// cuatro partes viajan juntas, D-050.4) y solo se persiste lo del último.
func TestRouteHeartbeatsCoalesceAsOneJob(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	rig.goLive(t, "s-1")
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	first, last := fullHeartbeat(), fullHeartbeat()
	first.LeaseCounter, last.LeaseCounter = 20, 30
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", first))
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", last))

	if total, byKind := rig.lane.pending(); total != 1 || byKind["heartbeat"] != 1 {
		t.Fatalf("dos latidos dejaron %d jobs %v, se esperaba UNO de tipo heartbeat", total, byKind)
	}
	release()
	closeLane(t, rig.lane)

	if got := rig.tl.String(); got != heartbeatTimeline {
		t.Errorf("los dos latidos escribieron %q, se esperaba una sola pasada: %q", got, heartbeatTimeline)
	}
	if got := rig.leaseCounter(t, cc); got != 31 {
		t.Errorf("contador del lease = %d, se esperaba 31 (el del ÚLTIMO latido más uno)", got)
	}
}

// Un latido LOGGED_OUT marca la sesión zombie y NADA MÁS: ni número, ni salud, ni parte de
// inferencia, ni lease (sesión muerta). Va en un job propio que no se coalesce.
func TestRouteLoggedOutHeartbeatOnlyMarksTheZombie(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	live := rig.goLive(t, "s-1")
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	gone := fullHeartbeat()
	gone.State = cloudlinkv1.SessionState_SESSION_STATE_LOGGED_OUT
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", gone))

	if total, byKind := rig.lane.pending(); total != 1 || byKind["logout"] != 1 {
		t.Fatalf("el logout dejó %d jobs %v, se esperaba UNO de tipo logout", total, byKind)
	}
	release()
	closeLane(t, rig.lane)

	if got := rig.tl.String(); got != "MarkLoggedOut edges=0" {
		t.Errorf("el logout escribió %q, se esperaba solo MarkLoggedOut", got)
	}
	if row := rig.row(t, cc); row.State != fleet.StateLoggedOut || row.SelfPn != "" {
		t.Errorf("fila de flota = (%q, self_pn %q), se esperaba loggedout sin número", row.State, row.SelfPn)
	}
	if got := rig.leaseCounter(t, cc); got != -1 || len(live.received()) != 0 {
		t.Errorf("una sesión zombie renovó su lease (contador %d, %d frames)", got, len(live.received()))
	}
}

// El logout es un hecho TERMINAL: el latido normal que el Edge manda después NO lo borra de
// la cola. Se ejecutan los dos, y el logout primero.
func TestRouteALaterHeartbeatDoesNotEraseTheLogout(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	rig.goLive(t, "s-1")
	release := plugLane(t, rig.lane, "s-1", jobReceipt)

	gone := fullHeartbeat()
	gone.State = cloudlinkv1.SessionState_SESSION_STATE_LOGGED_OUT
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", gone))
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", fullHeartbeat()))
	release()
	closeLane(t, rig.lane)

	if got, want := rig.tl.String(), "MarkLoggedOut edges=0 > "+heartbeatTimeline; got != want {
		t.Errorf("se escribió %q, se esperaba %q", got, want)
	}
}

// Lo que el Edge DICE sobre su capacidad de inferencia se aprende INLINE y ANTES del submit:
// con la cola de la sesión llena —el submit del latido frena al bucle Recv— el flanco a READY
// ya disparó. Es lo que deja al disparador del registro (Connect) preguntar sin carrera.
func TestRouteLearnsReadinessBeforeTheHeartbeatBlocksOnAFullQueue(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t, WithWorkQueue(1))
	cc := phone("tenant-1", "edge-1", "s-1")
	release := plugLane(t, rig.lane, "s-1", jobReceipt)
	rig.srv.route(rig.lane, cc, receiptFrame("s-1", "cmd-fills-the-queue"))
	if n := queueLen(rig.lane, "s-1"); n != 1 {
		t.Fatalf("la cola tiene %d jobs, se esperaba 1 (llena)", n)
	}
	warmed := make(chan struct{}, 1)
	rig.srv.OnWarmup = func(string, string, string, string) { warmed <- struct{}{} }

	routed := make(chan struct{})
	go func() {
		defer close(routed)
		rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", &cloudlinkv1.Heartbeat{InferenceReadiness: saysReady}))
	}()
	await(t, warmed, "que el flanco a READY dispare con la cola llena")
	if _, byKind := rig.lane.pending(); byKind["heartbeat"] != 0 {
		t.Errorf("el latido entró en una cola llena: %v", byKind)
	}
	release()
	await(t, routed, "que route vuelva al hacerse sitio en la cola")
	closeLane(t, rig.lane)

	if got := rig.srv.readinessOf(cc); got != saysReady {
		t.Errorf("readiness aprendido = %v, se esperaba READY", got)
	}
}
