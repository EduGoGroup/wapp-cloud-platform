//go:build pendiente

package grpc

// El contrato de config_push.go por la API exportada (R-G17): PushConfig llega a TODAS las
// sesiones vivas del tenant y a ninguna más, a la vez, sin fallar nunca, y avisa del
// calentamiento UNA VEZ POR EDGE cuando todos los empujes han terminado. Las sesiones vivas
// se siembran por dentro (seedEdgeSessions) hasta que connect.go las registre.
//
// El push al conectar —por la sesión (pushConfigsOnConnect) y por el propio stream
// (pushConfigsInBand, ADR-0048)— no tiene cara exportada hasta que exista Connect: se afirma
// en config_push_connect_test.go y config_push_inband_test.go, que nacen con el verde.

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// stubProvider es un ConfigProvider de respuesta fija que apunta por qué tenant le preguntan.
type stubProvider struct {
	cfgs []ConfigPayload
	err  error

	mu      sync.Mutex
	tenants []string
}

var _ ConfigProvider = (*stubProvider)(nil)

func (p *stubProvider) ConfigsForConnect(_ context.Context, tenantID string) ([]ConfigPayload, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tenants = append(p.tenants, tenantID)
	return p.cfgs, p.err
}

func (p *stubProvider) askedFor() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.tenants, ",")
}

// configRig es un Server con su Registry y un log que también captura debug (los empujes
// fallidos se registran ahí).
type configRig struct {
	srv *Server
	reg *session.Registry
	log *logBuffer
}

func newConfigRig(regOpts []session.RegistryOption, opts ...Option) *configRig {
	log, buf := debugLog()
	reg := session.NewRegistry(regOpts...)
	return &configRig{srv: New(reg, log, opts...), reg: reg, log: buf}
}

// goLive deja una sesión viva del Edge: con stream en el Registry y en el seguimiento.
func (r *configRig) goLive(t *testing.T, tenantID, edgeID, sessionID string, b *barrier) *liveSession {
	t.Helper()
	live := &liveSession{id: sessionID, barrier: b}
	t.Cleanup(r.reg.Register(sessionID, live))
	seedEdgeSessions(r.srv, tenantID, edgeID, sessionID)
	return live
}

// requireConfigUpdate afirma que frame es un ConfigUpdate dirigido a sessionID con esa config
// intacta, y devuelve su command_id: UUIDv4, el mismo en el sobre y dentro.
func requireConfigUpdate(t *testing.T, frame *cloudlinkv1.CloudToEdge, sessionID string, want ConfigPayload) string {
	t.Helper()
	cu := frame.GetConfigUpdate()
	if cu == nil {
		t.Fatalf("el frame de %s no es un ConfigUpdate: %v", sessionID, frame.GetPayload())
	}
	if frame.GetSessionId() != sessionID || cu.GetSessionId() != sessionID {
		t.Errorf("session_id del frame = (%q, %q), se esperaba %q en el sobre y dentro",
			frame.GetSessionId(), cu.GetSessionId(), sessionID)
	}
	if !uuidV4.MatchString(frame.GetCommandId()) || cu.GetCommandId() != frame.GetCommandId() {
		t.Errorf("command_id del frame de %s = (%q, %q), se esperaba un UUIDv4 repetido",
			sessionID, frame.GetCommandId(), cu.GetCommandId())
	}
	if cu.GetKind() != want.Kind || cu.GetVersion() != want.Version || !bytes.Equal(cu.GetPayload(), want.Payload) {
		t.Errorf("config de %s = (%q, %q, %q), se esperaba (%q, %q, %q): el gateway la trata OPACA",
			sessionID, cu.GetKind(), cu.GetVersion(), cu.GetPayload(), want.Kind, want.Version, want.Payload)
	}
	return frame.GetCommandId()
}

// requireOneConfigUpdate afirma que la sesión recibió exactamente ese ConfigUpdate.
func requireOneConfigUpdate(t *testing.T, live *liveSession, want ConfigPayload) string {
	t.Helper()
	frames := live.received()
	if len(frames) != 1 {
		t.Fatalf("la sesión %s recibió %d frames, se esperaba 1", live.id, len(frames))
	}
	return requireConfigUpdate(t, frames[0], live.id, want)
}

var intentsV2 = ConfigPayload{Kind: "intents", Version: "v2", Payload: []byte(`{"version":"v2"}`)}

func pushIntents(ctx context.Context, srv *Server, tenantID string) error {
	return srv.PushConfig(ctx, tenantID, intentsV2.Kind, intentsV2.Version, intentsV2.Payload)
}

// WithConfigProvider es una Option que New acepta, con proveedor o con nil.
func TestWithConfigProviderIsAnOptionNewAccepts(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]Option{
		"with provider": WithConfigProvider(&stubProvider{}),
		"nil provider":  WithConfigProvider(nil),
	} {
		if opt == nil {
			t.Fatalf("%s: WithConfigProvider devolvió una Option nil", name)
		}
		if srv := New(session.NewRegistry(), quietLog(), opt); srv == nil {
			t.Fatalf("%s: New devolvió nil", name)
		}
	}
}

// PushConfig empuja UN ConfigUpdate a cada sesión viva del tenant —de todos sus Edge—, todas
// a la vez y cada una con su command_id; y a ninguna de otro tenant, aunque comparta edge_id.
// Tampoco al canal de control, que tiene stream pero nunca está en el seguimiento (R3.4.b).
func TestPushConfigReachesEveryLiveSessionOfTheTenantAtOnce(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil)
	together := newBarrier(t, 3)
	mine := []*liveSession{
		rig.goLive(t, "tenant-1", "edge-1", "s-1", together),
		rig.goLive(t, "tenant-1", "edge-1", "s-2", together),
		rig.goLive(t, "tenant-1", "edge-2", "s-3", together),
	}
	foreign := rig.goLive(t, "tenant-2", "edge-1", "s-4", nil)
	control := &liveSession{id: cltransport.ControlSessionID}
	t.Cleanup(rig.reg.Register(cltransport.ControlSessionID, control))

	res := make(chan error, 1)
	go func() { res <- pushIntents(context.Background(), rig.srv, "tenant-1") }()
	if err := await(t, res, "PushConfig: los empujes no son concurrentes"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}

	commandIDs := map[string]bool{}
	for _, live := range mine {
		commandIDs[requireOneConfigUpdate(t, live, intentsV2)] = true
	}
	if len(commandIDs) != len(mine) {
		t.Errorf("command_ids distintos = %d, se esperaba uno por sesión (%d)", len(commandIDs), len(mine))
	}
	requireNothing(t, foreign, control)
}

// PushConfig devuelve nil SIEMPRE: una sesión sin stream o un Edge que rechaza el frame se
// registran en debug y no impiden la entrega a las demás.
func TestPushConfigNeverFailsAndKeepsDelivering(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil)
	healthy := rig.goLive(t, "tenant-1", "edge-1", "s-ok", nil)
	seedEdgeSessions(rig.srv, "tenant-1", "edge-1", "s-gone") // en el seguimiento, sin stream
	t.Cleanup(rig.reg.Register("s-broken", funcSender(func(*cloudlinkv1.CloudToEdge) error {
		return errors.New("stream roto")
	})))
	seedEdgeSessions(rig.srv, "tenant-1", "edge-2", "s-broken")

	if err := pushIntents(context.Background(), rig.srv, "tenant-1"); err != nil {
		t.Fatalf("PushConfig = %v, se esperaba nil: un fallo de entrega no es fallo del PUT", err)
	}
	requireOneConfigUpdate(t, healthy, intentsV2)
	for _, want := range []string{"level=DEBUG", "config push: a sesión", "session_id=s-gone", "session_id=s-broken", "kind=intents", "stream roto"} {
		if !rig.log.contains(want) {
			t.Errorf("al log de los empujes fallidos le falta %q: %q", want, rig.log.String())
		}
	}
	if got := strings.Count(rig.log.String(), "config push: a sesión"); got != 2 {
		t.Errorf("líneas de empuje fallido = %d, se esperaban 2 (la sana no deja rastro)", got)
	}

	if err := pushIntents(context.Background(), rig.srv, "tenant-sin-sesiones"); err != nil {
		t.Fatalf("PushConfig a un tenant sin sesiones = %v, se esperaba nil", err)
	}
}

// El ctx del llamante viaja hasta cada empuje: con un Edge atascado, un llamante que se va
// deja de esperar (y PushConfig sigue devolviendo nil).
func TestPushConfigStopsWaitingWhenTheCallerLeaves(t *testing.T) {
	t.Parallel()
	rig := newConfigRig([]session.RegistryOption{session.WithSendTimeout(time.Hour)})
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	entered := make(chan struct{}, 1)
	t.Cleanup(rig.reg.Register("s-stuck", funcSender(func(*cloudlinkv1.CloudToEdge) error {
		entered <- struct{}{}
		<-stuck
		return nil
	})))
	seedEdgeSessions(rig.srv, "tenant-1", "edge-1", "s-stuck")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- pushIntents(ctx, rig.srv, "tenant-1") }()
	await(t, entered, "que el empuje llegue al Edge atascado")
	cancel()

	if err := await(t, res, "que PushConfig vuelva al irse el llamante"); err != nil {
		t.Fatalf("PushConfig = %v, se esperaba nil", err)
	}
	if !rig.log.contains(session.ErrPushAbandoned.Error()) {
		t.Errorf("el empuje abandonado no dejó su rastro: %q", rig.log.String())
	}
}

// Tras el fan-out avisa del calentamiento UNA VEZ POR EDGE del tenant, no una por sesión, con
// el kind recién publicado y por una sesión de ESE Edge. Los Edge de otro tenant no se tocan.
func TestPushConfigWarmsOncePerEdgeNotPerSession(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil)
	for _, sid := range []string{"s-1", "s-2", "s-3", "s-4", "s-5"} {
		rig.goLive(t, "tenant-1", "edge-1", sid, nil)
	}
	rig.goLive(t, "tenant-1", "edge-2", "s-6", nil)
	rig.goLive(t, "tenant-2", "edge-1", "s-7", nil)
	rig.goLive(t, "tenant-2", "edge-3", "s-8", nil)
	var warms []warmedEdge
	rig.srv.OnWarmup = func(tenantID, edgeID, sessionID, kind string) {
		warms = append(warms, warmedEdge{tenantID, edgeID, sessionID, kind})
	}

	if err := pushIntents(context.Background(), rig.srv, "tenant-1"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}

	sort.Slice(warms, func(i, j int) bool { return warms[i].edgeID < warms[j].edgeID })
	if len(warms) != 2 {
		t.Fatalf("calentamientos = %d (%+v), se esperaban 2: uno por Edge del tenant", len(warms), warms)
	}
	for i, want := range []struct{ edgeID, sessions string }{{"edge-1", "s-1,s-2,s-3,s-4,s-5"}, {"edge-2", "s-6"}} {
		got := warms[i]
		if got.tenantID != "tenant-1" || got.edgeID != want.edgeID || got.kind != "intents" {
			t.Errorf("calentamiento %d = %+v, se esperaba (tenant-1, %s, …, intents)", i, got, want.edgeID)
		}
		if !strings.Contains(","+want.sessions+",", ","+got.sessionID+",") {
			t.Errorf("el calentamiento de %s va por %q, que no es una sesión suya (%s)", want.edgeID, got.sessionID, want.sessions)
		}
	}
}

// warmedEdge es una llamada a OnWarmup desde el fan-out.
type warmedEdge struct{ tenantID, edgeID, sessionID, kind string }

// El aviso va DESPUÉS de que todos los empujes terminen: calentar con el ConfigUpdate en vuelo
// dejaría cacheado el prefijo viejo. Los Edge retienen su frame hasta que el test los suelta;
// cuando OnWarmup se invoca, los tres ya lo han recibido.
func TestPushConfigWarmsOnlyAfterEveryPushHasLanded(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil)
	entered, gate := make(chan struct{}, 3), make(chan struct{})
	sessions := []*liveSession{
		rig.goLive(t, "tenant-1", "edge-1", "s-1", nil),
		rig.goLive(t, "tenant-1", "edge-1", "s-2", nil),
		rig.goLive(t, "tenant-1", "edge-2", "s-3", nil),
	}
	for _, live := range sessions {
		live.onSend = func() {
			entered <- struct{}{}
			<-gate
		}
	}
	var landedAtWarmup []int
	rig.srv.OnWarmup = func(_, _, _, _ string) {
		landed := 0
		for _, live := range sessions {
			landed += len(live.received())
		}
		landedAtWarmup = append(landedAtWarmup, landed)
	}

	res := make(chan error, 1)
	go func() { res <- pushIntents(context.Background(), rig.srv, "tenant-1") }()
	for range sessions {
		await(t, entered, "que los tres empujes estén en vuelo")
	}
	close(gate)
	if err := await(t, res, "que PushConfig termine"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}
	if len(landedAtWarmup) != 2 || landedAtWarmup[0] != 3 || landedAtWarmup[1] != 3 {
		t.Fatalf("frames entregados al avisar = %v, se esperaba [3 3]", landedAtWarmup)
	}
}

// Conducta heredada, afirmada tal cual: el aviso sale por cada Edge con sesiones en el
// seguimiento, HAYA LLEGADO O NO su ConfigUpdate (un Edge cuyo empuje falló se avisa igual).
// Sin sesiones no hay a quién avisar.
func TestPushConfigWarmsEveryTrackedEdgeEvenIfItsPushFailed(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil)
	seedEdgeSessions(rig.srv, "tenant-1", "edge-1", "s-gone") // sin stream: su empuje falla
	var warms []warmedEdge
	rig.srv.OnWarmup = func(tenantID, edgeID, sessionID, kind string) {
		warms = append(warms, warmedEdge{tenantID, edgeID, sessionID, kind})
	}

	if err := pushIntents(context.Background(), rig.srv, "tenant-1"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}
	if want := (warmedEdge{"tenant-1", "edge-1", "s-gone", "intents"}); len(warms) != 1 || warms[0] != want {
		t.Fatalf("calentamientos = %+v, se esperaba solo %+v", warms, want)
	}

	if err := pushIntents(context.Background(), rig.srv, "tenant-sin-sesiones"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}
	if len(warms) != 1 {
		t.Fatalf("un tenant sin sesiones disparó un calentamiento: %+v", warms)
	}
}
