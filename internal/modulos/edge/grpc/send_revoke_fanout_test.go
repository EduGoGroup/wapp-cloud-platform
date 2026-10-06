package grpc

// El fan-out de la revocación a las sesiones vivas (R-G21 y su hermana por Edge). Nace con el
// verde: las sesiones vivas de un Edge se dejan con trackSession, como hace el registro de la
// sesión en Connect. Sin reloj: que los empujes son CONCURRENTES se afirma con una
// barrera que solo se abre cuando todos han entrado a la vez.

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// barrier retiene a quien llega hasta que han llegado `need` a la vez. Con un fan-out en
// serie el primero se quedaría esperando a los demás para siempre: es lo que delata.
type barrier struct {
	mu      sync.Mutex
	need    int
	arrived int
	open    chan struct{}
	once    sync.Once
}

func newBarrier(t *testing.T, need int) *barrier {
	t.Helper()
	b := &barrier{need: need, open: make(chan struct{})}
	t.Cleanup(b.forceOpen) // que ningún Send quede colgado si el test falla antes
	return b
}

func (b *barrier) forceOpen() { b.once.Do(func() { close(b.open) }) }

func (b *barrier) arrive() {
	b.mu.Lock()
	b.arrived++
	full := b.arrived >= b.need
	b.mu.Unlock()
	if full {
		b.forceOpen()
	}
	<-b.open
}

// liveSession es el Edge de una sesión viva: apunta los frames que recibe y, si tiene
// barrera, no devuelve el Send hasta que todos sus compañeros están dentro del suyo.
type liveSession struct {
	id      string
	barrier *barrier
	onSend  func()

	mu     sync.Mutex
	frames []*cloudlinkv1.CloudToEdge
}

func (l *liveSession) Send(msg *cloudlinkv1.CloudToEdge) error {
	if l.onSend != nil {
		l.onSend()
	}
	l.mu.Lock()
	l.frames = append(l.frames, msg)
	l.mu.Unlock()
	if l.barrier != nil {
		l.barrier.arrive()
	}
	return nil
}

func (l *liveSession) received() []*cloudlinkv1.CloudToEdge {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*cloudlinkv1.CloudToEdge(nil), l.frames...)
}

// goLive registra una sesión viva del Edge (en el Registry y en el seguimiento del Server).
func (r *revokeRig) goLive(t *testing.T, tenantID, edgeID, sessionID string, b *barrier) *liveSession {
	t.Helper()
	live := &liveSession{id: sessionID, barrier: b}
	t.Cleanup(r.reg.Register(sessionID, live))
	r.srv.trackSession(phone(tenantID, edgeID, sessionID))
	return live
}

// requireRevocation afirma que la sesión recibió UN frame: el LeaseUpdate(Revoked) dirigido
// a ella, sin command_id (es un push del servidor, no un comando con Ack) y con un blob que
// el Edge validaría con la clave pública del gestor.
func (r *revokeRig) requireRevocation(t *testing.T, live *liveSession) {
	t.Helper()
	frames := live.received()
	if len(frames) != 1 {
		t.Fatalf("la sesión %s recibió %d frames, se esperaba 1", live.id, len(frames))
	}
	frame := frames[0]
	if frame.GetSessionId() != live.id {
		t.Errorf("el frame de %s va dirigido a %q", live.id, frame.GetSessionId())
	}
	if frame.GetCommandId() != "" {
		t.Errorf("el LeaseUpdate de %s lleva command_id %q: no es un comando con Ack", live.id, frame.GetCommandId())
	}
	lu := frame.GetLeaseUpdate()
	if lu == nil || !lu.GetRevoked() {
		t.Fatalf("el frame de %s no es un LeaseUpdate(Revoked): %v", live.id, frame.GetPayload())
	}
	v := cllease.NewValidator(r.mgr.PublicKey())
	if err := v.Apply(lu); err != nil || !v.Revoked() {
		t.Errorf("el Edge de %s no daría por revocado su lease (Apply=%v, Revoked=%v)", live.id, err, v.Revoked())
	}
}

func requireNothing(t *testing.T, sessions ...*liveSession) {
	t.Helper()
	for _, live := range sessions {
		if n := len(live.received()); n != 0 {
			t.Errorf("la sesión %s recibió %d frames, se esperaba ninguno", live.id, n)
		}
	}
}

// RevokeLease empuja el LeaseUpdate(Revoked) a TODAS las sesiones vivas del Edge, a la vez,
// y a ninguna de otro Edge (ni del mismo tenant, ni del mismo edge_id en otro tenant). Cuando
// el frame llega, la revocación YA está persistida.
func TestRevokeLeaseNotifiesEveryLiveSessionOfTheEdgeAtOnce(t *testing.T) {
	t.Parallel()
	rig := newRevokeRig(t)
	ctx := context.Background()
	together := newBarrier(t, 4)
	mine := make([]*liveSession, 0, 4)
	var persistedFirst sync.Map
	for _, sid := range []string{"s-1", "s-2", "s-3", "s-4"} {
		live := rig.goLive(t, "tenant-1", "edge-1", sid, together)
		live.onSend = func() {
			st, found, err := rig.leaseRepo.Get(ctx, "tenant-1", "edge-1")
			persistedFirst.Store(sid, err == nil && found && st.Revoked)
		}
		mine = append(mine, live)
	}
	otherEdge := rig.goLive(t, "tenant-1", "edge-2", "s-5", nil)
	otherTenant := rig.goLive(t, "tenant-2", "edge-1", "s-6", nil)

	done := make(chan error, 1)
	go func() { done <- rig.srv.RevokeLease(ctx, "tenant-1", "edge-1") }()
	if err := await(t, done, "RevokeLease vuelve: los cuatro empujes entraron a la vez"); err != nil {
		t.Fatalf("RevokeLease = %v", err)
	}

	for _, live := range mine {
		rig.requireRevocation(t, live)
		if ok, _ := persistedFirst.Load(live.id); ok != true {
			t.Errorf("la sesión %s recibió el aviso antes de que la revocación estuviera persistida", live.id)
		}
	}
	requireNothing(t, otherEdge, otherTenant)
}

// Una sesión cuyo Edge no lee su stream no retrasa el aviso a las demás; y un empuje que no
// llega NO es error de RevokeLease: la revocación ya está persistida y el push es best-effort.
// Quien corta la espera del empuje atascado es el ctx (aquí, el del llamante).
func TestRevokeLeaseABlockedSessionDoesNotDelayTheRest(t *testing.T) {
	t.Parallel()
	rig := newRevokeRig(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	t.Cleanup(rig.reg.Register("s-stuck", funcSender(func(*cloudlinkv1.CloudToEdge) error {
		entered <- struct{}{}
		<-release
		return nil
	})))
	rig.srv.trackSession(phone("tenant-1", "edge-1", "s-stuck"))

	others := newBarrier(t, 3)
	healthy := make([]*liveSession, 0, 3)
	for _, sid := range []string{"s-2", "s-3", "s-4"} {
		healthy = append(healthy, rig.goLive(t, "tenant-1", "edge-1", sid, others))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rig.srv.RevokeLease(ctx, "tenant-1", "edge-1") }()

	await(t, entered, "el empuje a la sesión atascada entra")
	await(t, others.open, "las tres sesiones sanas reciben su aviso con la cuarta atascada")
	cancel()

	if err := await(t, done, "RevokeLease vuelve cuando el ctx corta el empuje atascado"); err != nil {
		t.Fatalf("RevokeLease con un empuje fallido = %v, se esperaba nil (best-effort)", err)
	}
	for _, live := range healthy {
		rig.requireRevocation(t, live)
	}
}

// El empuje que falla deja su línea de debug con la sesión, y nada más.
func TestRevokeLeaseLogsAFailedPushAtDebug(t *testing.T) {
	t.Parallel()
	buf := &logBuffer{}
	rig := newRevokeRig(t)
	rig.srv.log = logger.New(logger.WithWriter(buf), logger.WithLevel(slog.LevelDebug))
	rig.srv.trackSession(phone("tenant-1", "edge-1", "s-gone")) // en el seguimiento, pero sin stream

	if err := rig.srv.RevokeLease(context.Background(), "tenant-1", "edge-1"); err != nil {
		t.Fatalf("RevokeLease = %v", err)
	}
	if !buf.contains("revoke: push a sesión") || !buf.contains("session_id=s-gone") {
		t.Errorf("el empuje fallido no dejó su línea de debug: %q", buf.String())
	}
}

// R-G21: RevokeTenant notifica a TODOS los Edge vivos DE ESE tenant —todas sus sesiones, a la
// vez— y a ninguno más. «Conocidos» son los que lista fleet: una instalación conocida sin
// sesiones vivas no recibe nada (nacerá revocada al reconectar), y el corte no se filtra al
// otro tenant aunque comparta edge_id.
func TestRevokeTenantNotifiesAllLiveEdgesOfThatTenantOnly(t *testing.T) {
	t.Parallel()
	fleetRepo := fleethelpertest.NewMemoria()
	rig := newRevokeRig(t, WithFleet(fleetRepo))
	ctx := context.Background()
	known := func(tenantID, edgeID, sessionID string) {
		t.Helper()
		if err := fleetRepo.MarkOnline(ctx, tenantID, edgeID, sessionID); err != nil {
			t.Fatalf("MarkOnline: %v", err)
		}
	}

	together := newBarrier(t, 5)
	mine := make([]*liveSession, 0, 5)
	for _, s := range []struct{ edgeID, sessionID string }{
		{"edge-1", "s-1"}, {"edge-1", "s-2"}, {"edge-2", "s-3"}, {"edge-3", "s-4"}, {"edge-3", "s-5"},
	} {
		known("tenant-1", s.edgeID, s.sessionID)
		mine = append(mine, rig.goLive(t, "tenant-1", s.edgeID, s.sessionID, together))
	}
	known("tenant-1", "edge-offline", "s-old") // conocida, sin sesiones vivas ahora

	known("tenant-2", "edge-1", "s-6") // mismo edge_id, OTRO tenant
	known("tenant-2", "edge-9", "s-7")
	foreign := []*liveSession{
		rig.goLive(t, "tenant-2", "edge-1", "s-6", nil),
		rig.goLive(t, "tenant-2", "edge-9", "s-7", nil),
	}

	done := make(chan error, 1)
	go func() { done <- rig.srv.RevokeTenant(ctx, "tenant-1") }()
	if err := await(t, done, "RevokeTenant vuelve: los cinco empujes entraron a la vez"); err != nil {
		t.Fatalf("RevokeTenant = %v", err)
	}

	for _, live := range mine {
		rig.requireRevocation(t, live)
	}
	requireNothing(t, foreign...)
	if rig.tenantRevoked(t, "tenant-2") {
		t.Error("la revocación de tenant-1 se filtró a tenant-2")
	}
}

// Lo que NO hace el fan-out de RevokeTenant, afirmado tal cual lo hace el código de
// referencia: sin fleet no hay instalaciones «conocidas» y no se avisa a nadie (el corte sí
// se persiste); y una sesión viva cuyo Edge fleet no lista tampoco recibe aviso.
func TestRevokeTenantOnlyNotifiesEdgesKnownToFleet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("without fleet", func(t *testing.T) {
		t.Parallel()
		rig := newRevokeRig(t)
		live := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)
		if err := rig.srv.RevokeTenant(ctx, "tenant-1"); err != nil {
			t.Fatalf("RevokeTenant = %v", err)
		}
		requireNothing(t, live)
		if revoked, err := rig.leaseRepo.TenantRevoked(ctx, "tenant-1"); err != nil || !revoked {
			t.Error("sin fleet el corte no se persistió")
		}
	})

	t.Run("live edge unknown to fleet", func(t *testing.T) {
		t.Parallel()
		fleetRepo := fleethelpertest.NewMemoria()
		rig := newRevokeRig(t, WithFleet(fleetRepo))
		if err := fleetRepo.MarkOnline(ctx, "tenant-1", "edge-1", "s-1"); err != nil {
			t.Fatalf("MarkOnline: %v", err)
		}
		listed := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)
		unlisted := rig.goLive(t, "tenant-1", "edge-ghost", "s-2", nil)

		if err := rig.srv.RevokeTenant(ctx, "tenant-1"); err != nil {
			t.Fatalf("RevokeTenant = %v", err)
		}
		rig.requireRevocation(t, listed)
		requireNothing(t, unlisted)
	})
}

// RestoreTenant NO empuja nada: la revocación previa no se retracta con un push, se deja de
// reafirmar en la siguiente emisión.
func TestRestoreTenantPushesNothing(t *testing.T) {
	t.Parallel()
	fleetRepo := fleethelpertest.NewMemoria()
	rig := newRevokeRig(t, WithFleet(fleetRepo))
	ctx := context.Background()
	live := make([]*liveSession, 0, 3)
	for _, sid := range []string{"s-1", "s-2", "s-3"} {
		if err := fleetRepo.MarkOnline(ctx, "tenant-1", "edge-1", sid); err != nil {
			t.Fatalf("MarkOnline: %v", err)
		}
		live = append(live, rig.goLive(t, "tenant-1", "edge-1", sid, nil))
	}

	if err := rig.srv.RestoreTenant(ctx, "tenant-1"); err != nil {
		t.Fatalf("RestoreTenant = %v", err)
	}
	requireNothing(t, live...)
}

// leaseToCloud dirige el LeaseUpdate a la sesión y no le pone command_id.
func TestLeaseToCloudAddressesTheSessionWithoutCommandID(t *testing.T) {
	t.Parallel()
	lu := &cloudlinkv1.LeaseUpdate{Revoked: true}
	msg := leaseToCloud("s-1", lu)
	if msg.GetSessionId() != "s-1" || msg.GetCommandId() != "" || msg.GetLeaseUpdate() != lu {
		t.Fatalf("leaseToCloud = %v", msg)
	}
	var _ session.Sender = (*liveSession)(nil)
}
