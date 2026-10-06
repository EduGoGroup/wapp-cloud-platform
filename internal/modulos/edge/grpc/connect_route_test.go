package grpc

// El reparto de route (R-G1, R3.5.d): qué se resuelve en la goroutine del bucle Recv y qué se
// suelta al carril. connect_route.go no tiene exportados: nace en verde (T-17) y se prueba por
// dentro, llamando a route como lo llamará el bucle de Connect, con un carril de verdad.
//
// La prueba de «inline» es de SINCRONÍA, no de tiempo: con el worker de la sesión TAPADO,
// cuando route vuelve lo inline ya ocurrió y lo del carril todavía no. Aquí van el banco, el
// censo y las ramas inline; lo que entra al carril está en connect_route_lane_test.go, el job
// del latido en connect_route_heartbeat_test.go y el entrante sellado en
// connect_route_incoming_test.go.

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// timeline apunta, EN ORDEN y desde cualquier goroutine, lo que el gateway le pide a fleet y a
// lease. Es lo que deja afirmar un orden ENTRE puertos (la salud antes del lease, el offline
// después de todo), que ningún doble ve por separado. Cada entrada es el método y, si hay
// sonda, lo que la sonda dice en ese instante.
type timeline struct {
	probe func() string

	mu        sync.Mutex
	events    []string
	unbounded []string // llamadas que llegaron con un ctx SIN plazo
	dead      []string // llamadas que llegaron con un ctx YA terminado
}

// at apunta la llamada y cómo venía su ctx: con o sin plazo, vivo o ya terminado.
func (tl *timeline) at(ctx context.Context, method string) {
	event := method
	if tl.probe != nil {
		event += " " + tl.probe()
	}
	tl.mu.Lock()
	tl.events = append(tl.events, event)
	if _, ok := ctx.Deadline(); !ok {
		tl.unbounded = append(tl.unbounded, method)
	}
	if ctx.Err() != nil {
		tl.dead = append(tl.dead, method)
	}
	tl.mu.Unlock()
}

func (tl *timeline) String() string {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return strings.Join(tl.events, " > ")
}

// requireBounded afirma que toda llamada apuntada llegó con un ctx CON plazo (R-G2).
func (tl *timeline) requireBounded(t *testing.T) {
	t.Helper()
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if len(tl.unbounded) != 0 {
		t.Errorf("llamadas con un ctx SIN plazo: %v", tl.unbounded)
	}
}

// timedFleet es el espía de fleet apuntando en la línea de tiempo sus cinco escrituras. Las
// dos del enlace (MarkOnline, MarkOffline) también pueden fallar por su nombre.
type timedFleet struct {
	*spyFleet
	tl *timeline
}

var _ fleet.Repository = timedFleet{}

func (f timedFleet) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	f.tl.at(ctx, "MarkOnline")
	if err := f.fail["MarkOnline"]; err != nil {
		return err
	}
	return f.spyFleet.MarkOnline(ctx, tenantID, edgeID, sessionID)
}

func (f timedFleet) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	f.tl.at(ctx, "MarkOffline")
	if err := f.fail["MarkOffline"]; err != nil {
		return err
	}
	return f.spyFleet.MarkOffline(ctx, tenantID, edgeID, sessionID)
}

func (f timedFleet) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	f.tl.at(ctx, "MarkLoggedOut")
	return f.spyFleet.MarkLoggedOut(ctx, tenantID, edgeID, sessionID)
}

func (f timedFleet) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	f.tl.at(ctx, "SetSelfPn")
	return f.spyFleet.SetSelfPn(ctx, tenantID, edgeID, sessionID, selfPn)
}

func (f timedFleet) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
	f.tl.at(ctx, "SaveHealth")
	return f.spyFleet.SaveHealth(ctx, tenantID, edgeID, sessionID, h)
}

// timedLease es el espía de lease apuntando en la línea de tiempo cada emisión persistida.
type timedLease struct {
	*spyLeaseRepo
	tl *timeline
}

var _ lease.Repository = timedLease{}

func (r timedLease) Upsert(ctx context.Context, s lease.State) error {
	r.tl.at(ctx, "Upsert")
	return r.spyLeaseRepo.Upsert(ctx, s)
}

// routeRig es un Server con TODOS los puertos puestos (fleet, lease, parte de inferencia, sink
// de acuses, diagnóstico, auth y auditoría) y un carril como el que monta Connect. La sonda de
// la línea de tiempo dice cuántos Edge tiene el almacén de inferencia en cada instante.
type routeRig struct {
	srv       *Server
	reg       *session.Registry
	log       *logBuffer
	tl        *timeline
	fleet     *spyFleet
	leaseRepo *spyLeaseRepo
	mgr       *lease.Manager
	stats     *inferstats.Store
	sink      *recordingSink
	diag      *spyReceiver
	authn     *scriptedAuth
	audit     *auditLog
	lane      *workLane
}

func newRouteRig(t *testing.T, opts ...Option) *routeRig {
	t.Helper()
	log, buf := debugLog()
	r := &routeRig{
		reg: session.NewRegistry(), log: buf, tl: &timeline{},
		fleet: newSpyFleet(), leaseRepo: newSpyLeaseRepo(), stats: inferstats.New(),
		sink: &recordingSink{}, diag: &spyReceiver{found: true},
		authn: echoTenantAuth("user-1"), audit: &auditLog{},
	}
	r.tl.probe = func() string { return "edges=" + strconv.Itoa(r.stats.Aggregated().Edges) }
	mgr, err := lease.NewManager(newSigningKey(t), timedLease{r.leaseRepo, r.tl})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	r.mgr = mgr
	opts = append([]Option{
		WithFleet(timedFleet{r.fleet, r.tl}), WithLease(mgr), WithInferenceStats(r.stats),
		WithReceiptSink(r.sink), WithDiagnosticsSink(r.diag),
		WithAuthenticator(r.authn), WithAuthAuditor(r.audit),
	}, opts...)
	r.srv = New(r.reg, log, opts...)
	r.lane = newWorkLane(context.Background(), r.srv.workQueue, r.srv.workBudget, log)
	t.Cleanup(func() { closeLane(t, r.lane) })
	return r
}

// goLive deja la sesión con stream en el Registry: por ahí vuelven el lease y la config.
func (r *routeRig) goLive(t *testing.T, sessionID string) *liveSession {
	t.Helper()
	live := &liveSession{id: sessionID}
	t.Cleanup(r.reg.Register(sessionID, live))
	return live
}

func heartbeatFrame(sessionID string, hb *cloudlinkv1.Heartbeat) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: hb}}
}

func ackFrame(sessionID, cmdID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.EdgeToCloud_Ack{Ack: &cloudlinkv1.Ack{AckedCommandId: cmdID, Ok: true}},
	}
}

func incomingFrame(sessionID string, m *cloudlinkv1.IncomingMessage) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: m}}
}

func pongFrame(sessionID string, nonce int64) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_Pong{Pong: &cloudlinkv1.Pong{Nonce: nonce}}}
}

func receiptFrame(sessionID, cmdID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_Receipt{
		Receipt: &cloudlinkv1.MessageReceipt{SessionId: sessionID, CommandId: cmdID, MessageIds: []string{"wamid." + cmdID}},
	}}
}

func bundleFrame(sessionID, cmdID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_DiagnosticsBundle{DiagnosticsBundle: sampleBundle(cmdID)}}
}

func loginFrame(sessionID, cmdID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_UserLogin{UserLogin: loginRequest(cmdID)}}
}

func refreshFrame(sessionID, cmdID, refreshToken string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_UserRefresh{UserRefresh: refreshRequest(cmdID, refreshToken)}}
}

func logoutFrame(sessionID, cmdID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{SessionId: sessionID, Payload: &cloudlinkv1.EdgeToCloud_UserLogout{UserLogout: logoutRequest(cmdID, "refresh-tenant-1", false)}}
}

// El censo: se encamina un frame de CADA rama con el carril tapado y se cuenta la cola. Lo que
// quedó dentro es exactamente lo que se soltó al carril —el latido, el acuse, el bundle y las
// tres de auth—; lo que no aparece se resolvió en el bucle Recv. Discrimina en las dos
// direcciones: una rama pesada devuelta al bucle baja el total, una inline mandada al carril
// lo sube.
func TestRouteOnlyTheHeavyBranchesEnterTheLane(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	rig.srv.OnIncoming = func(string, *cloudlinkv1.IncomingMessage) {}
	plugLane(t, rig.lane, "s-1", jobReceipt)
	cc := phone("tenant-1", "edge-1", "s-1")
	cc.sender = &liveSession{id: "el stream del Edge"}

	frames := []*cloudlinkv1.EdgeToCloud{
		incomingFrame("s-1", &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.1", Text: "hola"}),
		ackFrame("s-1", "cmd-nobody-waits"),
		pongFrame("s-1", 7),
		{SessionId: "s-1"}, // sin payload: la rama default
		heartbeatFrame("s-1", fullHeartbeat()),
		receiptFrame("s-1", "cmd-r"),
		bundleFrame("s-1", "cmd-d"),
		loginFrame("s-1", "cmd-login"),
		refreshFrame("s-1", "cmd-refresh", "refresh-tenant-1"),
		logoutFrame("s-1", "cmd-logout"),
	}
	for _, frame := range frames {
		rig.srv.route(rig.lane, cc, frame)
	}

	total, byKind := rig.lane.pending()
	want := map[string]int{"heartbeat": 1, "receipt": 1, "diagnostics": 1, "auth": 3}
	if total != 6 || !reflect.DeepEqual(byKind, want) {
		t.Fatalf("jobs encolados = %d %v, se esperaban 6 %v", total, byKind, want)
	}
}

// El Ack se resuelve INLINE (ADR-0040 §Decisión.3): es la víctima del head-of-line, no su
// causa. Con el carril de su sesión tapado, cuando route vuelve el envío que esperaba ya
// tiene su Ack.
func TestRouteDeliversTheAckInlineWithTheLanePlugged(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	plugLane(t, rig.lane, "s-1", jobReceipt)
	waiting := seedAck(rig.srv, "cmd-1", "s-1")

	rig.srv.route(rig.lane, phone("tenant-1", "edge-1", "s-1"), ackFrame("s-1", "cmd-1"))

	ack, closed := ackState(waiting)
	if ack == nil || closed || ack.GetAckedCommandId() != "cmd-1" || !ack.GetOk() {
		t.Fatalf("al volver route, el envío en vuelo tiene (ack=%v, cerrado=%v): el Ack no se entregó inline", ack, closed)
	}
	if total, _ := rig.lane.pending(); total != 0 {
		t.Errorf("el Ack dejó %d jobs en el carril", total)
	}
}

// El entrante se entrega INLINE al hook, bajo el session_id DEL FRAME (el del connCtx), con el
// carril tapado. Sin hook, el frame se ignora sin más.
func TestRouteHandsTheIncomingToTheHookInline(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	plugLane(t, rig.lane, "s-2", jobReceipt)
	msg := &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.1", Text: "hola", From: "57300@s.whatsapp.net"}

	rig.srv.route(rig.lane, phone("tenant-1", "edge-1", "s-2"), incomingFrame("s-2", msg)) // sin hook

	var gotSession string
	var got *cloudlinkv1.IncomingMessage
	rig.srv.OnIncoming = func(sessionID string, m *cloudlinkv1.IncomingMessage) { gotSession, got = sessionID, m }
	rig.srv.route(rig.lane, phone("tenant-1", "edge-1", "s-2"), incomingFrame("s-2", msg))

	if gotSession != "s-2" || got != msg {
		t.Fatalf("OnIncoming recibió (%q, %v), se esperaba (s-2, el mensaje del frame) antes de que route volviera", gotSession, got)
	}
	if total, _ := rig.lane.pending(); total != 0 {
		t.Errorf("el entrante dejó %d jobs en el carril", total)
	}
}

// El Pong y el payload desconocido solo dejan su línea de debug, inline, y no tocan el carril.
func TestRouteLogsThePongAndTheUnknownPayload(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	plugLane(t, rig.lane, "s-1", jobReceipt)
	cc := phone("tenant-1", "edge-1", "s-1")

	rig.srv.route(rig.lane, cc, pongFrame("s-1", 7))
	for _, want := range []string{"level=DEBUG", `msg="pong recibido"`, "session_id=s-1", "nonce=7"} {
		if !rig.log.contains(want) {
			t.Errorf("al log del pong le falta %q: %q", want, rig.log.String())
		}
	}

	rig.srv.route(rig.lane, cc, &cloudlinkv1.EdgeToCloud{SessionId: "s-1"})
	if !rig.log.contains(`msg="payload EdgeToCloud desconocido"`) {
		t.Errorf("al log le falta la línea del payload desconocido: %q", rig.log.String())
	}
	if total, _ := rig.lane.pending(); total != 0 {
		t.Errorf("quedaron %d jobs en el carril", total)
	}
}

// El hook del latido corre INLINE y recibe el session_id del frame y el latido tal cual.
func TestRouteCallsTheHeartbeatHookInline(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	plugLane(t, rig.lane, "s-1", jobReceipt)
	hb := fullHeartbeat()
	var gotSession string
	var got *cloudlinkv1.Heartbeat
	rig.srv.OnHeartbeat = func(sessionID string, m *cloudlinkv1.Heartbeat) { gotSession, got = sessionID, m }

	rig.srv.route(rig.lane, phone("tenant-1", "edge-1", "s-1"), heartbeatFrame("s-1", hb))

	if gotSession != "s-1" || got != hb {
		t.Fatalf("OnHeartbeat recibió (%q, %v), se esperaba (s-1, el latido del frame) antes de que route volviera", gotSession, got)
	}
}
