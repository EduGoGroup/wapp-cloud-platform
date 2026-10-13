package grpc

// El registro de una sesión (onSessionRegistered → registerSession; R-G2, R-G5, R-G8, R-G17):
// se rastrea, se audita, se marca online, recibe su lease inicial y después su config, todo
// INLINE y bajo UN reloj (el presupuesto de trabajo) colgado del ctx del stream.

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// providerFunc es un ConfigProvider que deja ver el ctx con el que se le pregunta.
type providerFunc func(ctx context.Context, tenantID string) ([]ConfigPayload, error)

func (f providerFunc) ConfigsForConnect(ctx context.Context, tenantID string) ([]ConfigPayload, error) {
	return f(ctx, tenantID)
}

var connectCfgs = []ConfigPayload{jwksCfg, intentsCfg, filtersCfg}

const (
	handshakeBudgetLine = `msg="handshake: el registro de la sesión no terminó dentro de su presupuesto"`
	handshakeGoneLine   = `msg="handshake: el stream se fue antes de terminar el registro de la sesión"`
)

func requireLogHas(t *testing.T, log *logBuffer, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !log.contains(want) {
			t.Errorf("al log le falta %q: %q", want, log.String())
		}
	}
}

// requireInitialLease afirma que frame es el LeaseUpdate inicial de esa sesión: sin command_id,
// con contador 1 persistido, y con un blob con el que el Edge puede operar.
func (r *routeRig) requireInitialLease(t *testing.T, frame *cloudlinkv1.CloudToEdge, cc connCtx) {
	t.Helper()
	lu := frame.GetLeaseUpdate()
	if lu == nil || frame.GetSessionId() != cc.sessionID || frame.GetCommandId() != "" {
		t.Fatalf("el frame no es el LeaseUpdate de %s: %v", cc.sessionID, frame)
	}
	validator := cllease.NewValidator(r.mgr.PublicKey())
	if err := validator.Apply(lu); err != nil || !validator.CanOperate(true) {
		t.Errorf("el Edge no podría operar con su lease inicial (Apply=%v)", err)
	}
	if got := r.leaseCounter(t, cc); got != 1 {
		t.Errorf("contador del lease inicial = %d, se esperaba 1", got)
	}
}

// El registro completo, en su orden: cuando el lease inicial llega al Edge la sesión YA está
// rastreada, auditada y online; y la config va DESPUÉS del lease. Todo con plazo.
func TestSessionRegisteredGoesOnlineThenGetsItsLeaseThenItsConfig(t *testing.T) {
	t.Parallel()
	providerBounded := false
	rig := newRouteRig(t, WithConfigProvider(providerFunc(func(ctx context.Context, tenantID string) ([]ConfigPayload, error) {
		_, providerBounded = ctx.Deadline()
		if tenantID != "tenant-1" {
			t.Errorf("al proveedor se le preguntó por %q", tenantID)
		}
		return connectCfgs, nil
	})))
	cc := phone("tenant-1", "edge-1", "s-1")
	live := rig.goLive(t, "s-1")
	var seenAtFirstFrame string
	live.onSend = func() {
		if seenAtFirstFrame == "" {
			seenAtFirstFrame = rig.tl.String() + " | tracked=" + sortedSessions(rig.srv, "tenant-1", "edge-1")
		}
	}

	rig.srv.onSessionRegistered(context.Background(), cc)

	if want := "MarkOnline edges=0 > Upsert edges=0 | tracked=s-1"; seenAtFirstFrame != want {
		t.Fatalf("al llegar el primer frame al Edge iba %q, se esperaba %q", seenAtFirstFrame, want)
	}
	rig.tl.requireBounded(t)
	if !providerBounded {
		t.Error("al proveedor de config se le preguntó con un ctx sin plazo")
	}
	frames := live.received()
	if len(frames) != 4 {
		t.Fatalf("el Edge recibió %d frames, se esperaban el lease y 3 configs", len(frames))
	}
	rig.requireInitialLease(t, frames[0], cc)
	requireConfigs(t, frames[1:], "s-1", connectCfgs)
	if row := rig.row(t, cc); row.State != fleet.StateOnline {
		t.Errorf("fila de flota en %q, se esperaba online", row.State)
	}
	events := rig.audit.recorded()
	if len(events) != 1 || events[0].Action != "edge.session.open" || events[0].Actor != "edge-1" ||
		events[0].TenantID != "tenant-1" || events[0].Meta["session_id"] != "s-1" {
		t.Errorf("auditoría = %+v, se esperaba UNA apertura de sesión firmada por el Edge", events)
	}
	if rig.log.contains("handshake:") {
		t.Errorf("un registro limpio no deja aviso de handshake: %q", rig.log.String())
	}
}

// Sin identidad mTLS no hay a quién atribuir nada: ni se rastrea, ni se audita, ni flota, ni
// lease, ni config.
func TestSessionRegisteredDoesNothingWithoutIdentity(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{cfgs: connectCfgs}
	rig := newRouteRig(t, WithConfigProvider(provider))
	live := rig.goLive(t, "s-1")
	anonymous := connCtx{sessionID: "s-1"}

	rig.srv.onSessionRegistered(context.Background(), anonymous)

	if rig.tl.String() != "" || len(rig.audit.recorded()) != 0 || len(rig.srv.edgeSessions) != 0 || provider.askedFor() != "" {
		t.Errorf("un stream anónimo dejó rastro: escrituras %q, auditoría %d, seguimiento %v, proveedor %q",
			rig.tl.String(), len(rig.audit.recorded()), rig.srv.edgeSessions, provider.askedFor())
	}
	requireNothing(t, live)
}

// MP-11: el id del canal de control NO produce fila de flota —no hay teléfono detrás—, aunque
// llegara hasta aquí; el resto del registro no lo distingue. (Connect ya no lo trae: ADR-0048.)
func TestRegisterSessionNeverPutsTheControlChannelInTheFleet(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	cc := phone("tenant-1", "edge-1", cltransport.ControlSessionID)

	rig.srv.onSessionRegistered(context.Background(), cc)

	if got := rig.tl.String(); got != "Upsert edges=0" {
		t.Errorf("escrituras = %q, se esperaba el lease y NINGÚN MarkOnline", got)
	}
	rows, err := rig.fleet.List(t.Context(), "tenant-1")
	if err != nil || len(rows) != 0 {
		t.Errorf("la flota tiene %d filas (err=%v), se esperaba ninguna", len(rows), err)
	}
}

// bareServer es un Server con las opciones dadas y nada más, y una sesión viva.
func bareServer(t *testing.T, opts ...Option) (*Server, *liveSession, *logBuffer) {
	t.Helper()
	log, buf := debugLog()
	reg := session.NewRegistry()
	live := &liveSession{id: "s-1"}
	t.Cleanup(reg.Register("s-1", live))
	return New(reg, log, opts...), live, buf
}

// Sin gestor de leases no hay lease que empujar, pero la config al conectar es independiente:
// llega igual.
func TestRegisterSessionPushesConfigEvenWithoutLease(t *testing.T) {
	t.Parallel()
	srv, live, _ := bareServer(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))

	srv.onSessionRegistered(context.Background(), phone("tenant-1", "edge-1", "s-1"))

	requireConfigs(t, live.received(), "s-1", connectCfgs)
	if got := sortedSessions(srv, "tenant-1", "edge-1"); got != "s-1" {
		t.Errorf("seguimiento = %q, se esperaba s-1", got)
	}
}

// Si el lease inicial no se puede emitir, el registro se queda ahí: queda el error con el Edge
// y NO se empuja la config (conducta del viejo: el Edge lo provoca reconectando).
func TestRegisterSessionStopsWhenTheInitialLeaseCannotBeIssued(t *testing.T) {
	t.Parallel()
	mgr, err := lease.NewManager(newSigningKey(t), brokenLeaseRepo{newSpyLeaseRepo()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	provider := &stubProvider{cfgs: connectCfgs}
	srv, live, log := bareServer(t, WithLease(mgr), WithConfigProvider(provider))

	srv.onSessionRegistered(context.Background(), phone("tenant-1", "edge-1", "s-1"))

	requireLogHas(t, log, "level=ERROR", `msg="lease: emitir inicial"`, "base caída", "edge_id=edge-1")
	requireNothing(t, live)
	if provider.askedFor() != "" {
		t.Errorf("sin lease inicial se pidió la config igualmente (%q)", provider.askedFor())
	}
}

// Los fallos que NO detienen el registro: si fleet no puede marcar online, queda el error y el
// lease se emite igual; si el lease no puede empujarse (la sesión no tiene stream), queda el
// error y la config se intenta igual.
func TestRegisterSessionLogsAndGoesOnPastFleetAndPushFailures(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{cfgs: connectCfgs}
	rig := newRouteRig(t, WithConfigProvider(provider))
	rig.fleet.fail["MarkOnline"] = errors.New("flota caída")
	cc := phone("tenant-1", "edge-1", "s-gone") // en ningún stream

	rig.srv.onSessionRegistered(context.Background(), cc)

	requireLogHas(t, rig.log,
		`msg="fleet: marcar online"`, "flota caída", "edge_id=edge-1", "session_id=s-gone",
		`msg="lease: push inicial"`, session.ErrSessionOffline.Error(),
		`msg="config push: inicial a sesión"`)
	if got := rig.leaseCounter(t, cc); got != 1 {
		t.Errorf("contador del lease = %d: un fallo de fleet no debe impedir la emisión", got)
	}
	if provider.askedFor() != "tenant-1" {
		t.Errorf("al proveedor se le preguntó por %q, se esperaba una vez por tenant-1", provider.askedFor())
	}
}

// R-G2: el handshake SE RINDE. Con una base que no contesta, el registro vuelve al vencer el
// presupuesto de trabajo —no retiene el bucle Recv— y deja un Warn que nombra la sesión y
// acusa al presupuesto.
func TestSessionRegisteredGivesUpWhenTheBudgetRunsOut(t *testing.T) {
	t.Parallel()
	log, buf := debugLog()
	stuck := fleethelpertest.NewSlow(fleethelpertest.NewMemoria(), time.Hour)
	srv := New(session.NewRegistry(), log, WithFleet(stuck), WithWorkTimeout(time.Millisecond))

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.onSessionRegistered(context.Background(), phone("tenant-1", "edge-1", "s-1"))
	}()
	await(t, done, "que el registro se rinda por su presupuesto")

	requireLogHas(t, buf, "level=WARN", handshakeBudgetLine, "session_id=s-1", "edge_id=edge-1", "budget=1ms",
		context.DeadlineExceeded.Error())
	if buf.contains(handshakeGoneLine) {
		t.Errorf("un plazo vencido se anotó como stream caído: %q", buf.String())
	}
}

// El reloj cuelga del ctx del STREAM: si el stream muere a mitad del handshake el registro se
// rinde con él, y eso se anota como lo que es —corriente, no una avería del presupuesto—.
func TestSessionRegisteredGivesUpWhenTheStreamGoesAway(t *testing.T) {
	t.Parallel()
	log, buf := debugLog()
	stuck := fleethelpertest.NewSlow(fleethelpertest.NewMemoria(), time.Hour)
	srv := New(session.NewRegistry(), log, WithFleet(stuck))
	streamCtx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.onSessionRegistered(streamCtx, phone("tenant-1", "edge-1", "s-1"))
	}()
	await(t, done, "que el registro se rinda con el stream")

	requireLogHas(t, buf, "level=INFO", handshakeGoneLine, "session_id=s-1", "edge_id=edge-1", context.Canceled.Error())
	if buf.contains(handshakeBudgetLine) {
		t.Errorf("un stream caído se anotó como presupuesto vencido: %q", buf.String())
	}
}
