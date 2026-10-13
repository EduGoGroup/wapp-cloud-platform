package grpc

// La renovación del lease en el latido (R-G6; ADR-0007): el contador que trae el latido se
// renueva a contador+1, se persiste y el LeaseUpdate se empuja a la sesión por el Registry —es
// un comando que nace en la nube, no la respuesta a una petición—. Un Edge revocado no
// resucita: recibe otra vez su revocación.

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// onlyLeaseUpdate afirma que la sesión recibió UN frame: un LeaseUpdate dirigido a ella y sin
// command_id (es un push del servidor, no un comando con Ack).
func onlyLeaseUpdate(t *testing.T, live *liveSession) *cloudlinkv1.LeaseUpdate {
	t.Helper()
	frames := live.received()
	if len(frames) != 1 {
		t.Fatalf("la sesión %s recibió %d frames, se esperaba 1", live.id, len(frames))
	}
	lu := frames[0].GetLeaseUpdate()
	if lu == nil {
		t.Fatalf("el frame de %s no es un LeaseUpdate: %T", live.id, frames[0].GetPayload())
	}
	if frames[0].GetSessionId() != live.id || frames[0].GetCommandId() != "" {
		t.Errorf("sobre del LeaseUpdate = (session_id %q, command_id %q), se esperaba (%q, vacío)",
			frames[0].GetSessionId(), frames[0].GetCommandId(), live.id)
	}
	return lu
}

// El latido renueva: se persiste contador+1 sin revocar, y el Edge recibe un LeaseUpdate que
// su validador acepta y con el que puede seguir operando.
func TestRenewLeasePersistsTheNextCounterAndPushesIt(t *testing.T) {
	t.Parallel()
	rig := newRevokeRig(t)
	live := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)

	rig.srv.renewLease(context.Background(), phone("tenant-1", "edge-1", "s-1"), 41)

	st, found, err := rig.leaseRepo.Get(t.Context(), "tenant-1", "edge-1")
	if err != nil || !found || st.Counter != 42 || st.Revoked {
		t.Fatalf("estado del lease = (%+v, found=%v, err=%v), se esperaba contador 42 sin revocar", st, found, err)
	}
	validator := cllease.NewValidator(rig.mgr.PublicKey())
	if err := validator.Apply(onlyLeaseUpdate(t, live)); err != nil || !validator.CanOperate(true) {
		t.Errorf("el Edge no podría operar con el lease renovado (Apply=%v, CanOperate=%v)", err, validator.CanOperate(true))
	}
}

// T-8, visto desde el latido: un Edge revocado que sigue latiendo NO recupera su lease. Lo que
// recibe es otra vez la revocación, y el estado persistido sigue revocado.
func TestRenewLeaseOfARevokedEdgePushesTheRevocationAgain(t *testing.T) {
	t.Parallel()
	rig := newRevokeRig(t)
	if _, err := rig.mgr.Revoke(t.Context(), "tenant-1", "edge-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	live := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)

	rig.srv.renewLease(context.Background(), phone("tenant-1", "edge-1", "s-1"), 41)

	rig.requireRevocation(t, live)
	if found, revoked := rig.leaseState(t, "tenant-1", "edge-1"); !found || !revoked {
		t.Errorf("el latido resucitó un lease revocado (found=%v, revoked=%v)", found, revoked)
	}
}

// Sin gestor de leases, sin identidad mTLS o sin session_id no se renueva nada: ni se toca el
// repositorio ni se empuja.
func TestRenewLeaseDegradesToANoOp(t *testing.T) {
	t.Parallel()
	t.Run("without lease manager", func(t *testing.T) {
		t.Parallel()
		reg := session.NewRegistry()
		live := &liveSession{id: "s-1"}
		t.Cleanup(reg.Register("s-1", live))

		New(reg, quietLog()).renewLease(context.Background(), phone("tenant-1", "edge-1", "s-1"), 41)

		requireNothing(t, live)
	})
	for name, cc := range degradedChannels() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newRevokeRig(t)
			live := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)

			rig.srv.renewLease(context.Background(), cc, 41)

			requireNothing(t, live)
			if found, _ := rig.leaseState(t, "tenant-1", "edge-1"); found {
				t.Error("se persistió un lease para un canal sin identidad o sin sesión")
			}
		})
	}
}

// brokenLeaseRepo es un lease.Repository cuya emisión no se puede persistir.
type brokenLeaseRepo struct{ *spyLeaseRepo }

func (brokenLeaseRepo) Upsert(context.Context, lease.State) error { return errors.New("base caída") }

// Si la renovación no se puede persistir, NO se empuja nada —un lease que no quedó guardado no
// se le da por bueno al Edge— y se registra el error con el edge_id.
func TestRenewLeaseDoesNotPushWhatItCouldNotPersist(t *testing.T) {
	t.Parallel()
	mgr, err := lease.NewManager(newSigningKey(t), brokenLeaseRepo{newSpyLeaseRepo()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	log, buf := debugLog()
	reg := session.NewRegistry()
	live := &liveSession{id: "s-1"}
	t.Cleanup(reg.Register("s-1", live))

	New(reg, log, WithLease(mgr)).renewLease(context.Background(), phone("tenant-1", "edge-1", "s-1"), 41)

	requireNothing(t, live)
	for _, want := range []string{"level=ERROR", `msg="lease: renovar"`, "base caída", "edge_id=edge-1"} {
		if !buf.contains(want) {
			t.Errorf("al log le falta %q: %q", want, buf.String())
		}
	}
}

// leaseRigWithLog es el banco de la revocación con un log que captura debug y el Registry dado.
func leaseRigWithLog(t *testing.T, regOpts ...session.RegistryOption) (*Server, *session.Registry, *spyLeaseRepo, *logBuffer) {
	t.Helper()
	repo := newSpyLeaseRepo()
	mgr, err := lease.NewManager(newSigningKey(t), repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	log, buf := debugLog()
	reg := session.NewRegistry(regOpts...)
	return New(reg, log, WithLease(mgr)), reg, repo, buf
}

// Si la sesión ya no está en el Registry, el lease queda renovado en el servidor y el empuje
// fallido se anota en debug: el Edge lo reintenta en su siguiente latido.
func TestRenewLeaseLogsAPushToASessionThatIsGone(t *testing.T) {
	t.Parallel()
	srv, _, repo, buf := leaseRigWithLog(t)

	srv.renewLease(context.Background(), phone("tenant-1", "edge-1", "s-1"), 41)

	if st, found, err := repo.Get(t.Context(), "tenant-1", "edge-1"); err != nil || !found || st.Counter != 42 {
		t.Errorf("estado del lease = (%+v, found=%v, err=%v), se esperaba contador 42", st, found, err)
	}
	for _, want := range []string{"level=DEBUG", `msg="lease: push renovación"`, session.ErrSessionOffline.Error(), "session_id=s-1"} {
		if !buf.contains(want) {
			t.Errorf("al log le falta %q: %q", want, buf.String())
		}
	}
}

// El ctx del job manda sobre el reloj propio del Registry: con el Edge atascado y el
// presupuesto del job agotado, la renovación deja de esperar sin consumir el plazo de envío.
func TestRenewLeaseStopsWaitingWhenTheJobContextEnds(t *testing.T) {
	t.Parallel()
	srv, reg, _, buf := leaseRigWithLog(t, session.WithSendTimeout(time.Hour))
	stream, entered := stuckStream(t)
	t.Cleanup(reg.Register("s-1", stream))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.renewLease(ctx, phone("tenant-1", "edge-1", "s-1"), 41)
	}()
	await(t, entered, "que el LeaseUpdate llegue al Edge atascado")
	cancel()
	await(t, done, "que la renovación vuelva al terminar el ctx del job")

	if !buf.contains(session.ErrPushAbandoned.Error()) || !buf.contains("lease: push renovación") {
		t.Errorf("el empuje abandonado no dejó su rastro: %q", buf.String())
	}
}
