package grpc

// El contrato de send_revoke.go por la API exportada: lo que se persiste, los errores y el
// reloj de las tres entradas. El fan-out a las sesiones vivas (R-G21) necesita sesiones
// registradas en el Server, que hasta la tanda de connect.go solo se pueden sembrar por
// dentro: se afirma en send_revoke_fanout_test.go, que nace con el verde.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease/leasehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// errNoLease es el texto observable de las tres entradas sin gestor de leases.
const errNoLease = "gatewaygrpc: lease no configurado"

// spyLeaseRepo es un lease.Repository sobre Memoria que apunta el plazo del ctx con el que
// se le llama y puede fallar las escrituras de la revocación.
type spyLeaseRepo struct {
	*leasehelpertest.Memoria
	failWrites error

	mu        sync.Mutex
	deadlines []time.Duration // tiempo que quedaba hasta el plazo; -1 si el ctx no traía plazo
}

func newSpyLeaseRepo() *spyLeaseRepo {
	return &spyLeaseRepo{Memoria: leasehelpertest.NewMemoria()}
}

func (r *spyLeaseRepo) note(ctx context.Context) {
	left := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	r.mu.Lock()
	r.deadlines = append(r.deadlines, left)
	r.mu.Unlock()
}

func (r *spyLeaseRepo) lastDeadline(t *testing.T) time.Duration {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.deadlines) == 0 {
		t.Fatal("el repositorio de leases no llegó a ser llamado")
	}
	return r.deadlines[len(r.deadlines)-1]
}

func (r *spyLeaseRepo) MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	r.note(ctx)
	if r.failWrites != nil {
		return r.failWrites
	}
	return r.Memoria.MarkRevoked(ctx, tenantID, edgeID, expiresAt)
}

func (r *spyLeaseRepo) MarkTenantRevoked(ctx context.Context, tenantID string) error {
	r.note(ctx)
	if r.failWrites != nil {
		return r.failWrites
	}
	return r.Memoria.MarkTenantRevoked(ctx, tenantID)
}

func (r *spyLeaseRepo) RestoreTenant(ctx context.Context, tenantID string) error {
	r.note(ctx)
	if r.failWrites != nil {
		return r.failWrites
	}
	return r.Memoria.RestoreTenant(ctx, tenantID)
}

// failingListFleet es un fleet.Repository cuyo List falla siempre y apunta el plazo del ctx.
type failingListFleet struct {
	fleet.Repository
	err      error
	deadline time.Duration
}

func (f *failingListFleet) List(ctx context.Context, _ string) ([]fleet.Session, error) {
	f.deadline = -1
	if dl, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(dl)
	}
	return nil, f.err
}

// revokeRig es un Server con lease (sobre el espía) y, si se pide, fleet.
type revokeRig struct {
	srv       *Server
	reg       *session.Registry
	leaseRepo *spyLeaseRepo
	mgr       *lease.Manager
}

func newRevokeRig(t *testing.T, opts ...Option) *revokeRig {
	t.Helper()
	repo := newSpyLeaseRepo()
	mgr, err := lease.NewManager(newSigningKey(t), repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	reg := session.NewRegistry()
	srv := New(reg, quietLog(), append([]Option{WithLease(mgr)}, opts...)...)
	return &revokeRig{srv: srv, reg: reg, leaseRepo: repo, mgr: mgr}
}

// tenantRevoked dice si el corte del tenant está persistido.
func (r *revokeRig) tenantRevoked(t *testing.T, tenantID string) bool {
	t.Helper()
	revoked, err := r.leaseRepo.TenantRevoked(t.Context(), tenantID)
	if err != nil {
		t.Fatalf("TenantRevoked(%s): %v", tenantID, err)
	}
	return revoked
}

// leaseState dice si el Edge tiene estado de lease persistido y si está revocado.
func (r *revokeRig) leaseState(t *testing.T, tenantID, edgeID string) (found, revoked bool) {
	t.Helper()
	st, found, err := r.leaseRepo.Get(t.Context(), tenantID, edgeID)
	if err != nil {
		t.Fatalf("Get(%s, %s): %v", tenantID, edgeID, err)
	}
	return found, found && st.Revoked
}

// Sin gestor de leases (sin WithLease, o con nil) las tres entradas devuelven el mismo error
// literal y no hacen nada más.
func TestRevocationFamilyWithoutLeaseIsAnError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{name: "no option"},
		{name: "nil manager", opts: []Option{WithLease(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := New(session.NewRegistry(), quietLog(), tc.opts...)
			ctx := context.Background()
			calls := map[string]error{
				"RevokeLease":   srv.RevokeLease(ctx, "tenant-1", "edge-1"),
				"RevokeTenant":  srv.RevokeTenant(ctx, "tenant-1"),
				"RestoreTenant": srv.RestoreTenant(ctx, "tenant-1"),
			}
			for name, err := range calls {
				if err == nil || err.Error() != errNoLease {
					t.Errorf("%s sin lease = %v, se esperaba %q", name, err, errNoLease)
				}
			}
		})
	}
}

// RevokeLease persiste la revocación de ESE Edge (y de ningún otro), aunque no tenga
// ninguna sesión viva a la que avisar.
func TestRevokeLeasePersistsTheRevocation(t *testing.T) {
	t.Parallel()
	rig := newRevokeRig(t)
	ctx := context.Background()

	if err := rig.srv.RevokeLease(ctx, "tenant-1", "edge-1"); err != nil {
		t.Fatalf("RevokeLease = %v", err)
	}

	st, found, err := rig.leaseRepo.Get(ctx, "tenant-1", "edge-1")
	if err != nil || !found || !st.Revoked {
		t.Fatalf("lease de edge-1 = (%+v, found=%v, err=%v), se esperaba revocado", st, found, err)
	}
	if found, _ := rig.leaseState(t, "tenant-1", "edge-2"); found {
		t.Error("la revocación de edge-1 tocó el lease de edge-2")
	}
	if rig.tenantRevoked(t, "tenant-1") {
		t.Error("RevokeLease cortó el tenant entero: ese es otro sujeto de corte (D-055.2)")
	}
}

// RevokeTenant persiste el corte del tenant SIN marcar el lease de cada instalación, y
// RestoreTenant lo deshace.
func TestRevokeTenantAndRestoreTenantPersistTheCut(t *testing.T) {
	t.Parallel()
	fleetRepo := fleethelpertest.NewMemoria()
	rig := newRevokeRig(t, WithFleet(fleetRepo))
	ctx := context.Background()
	for _, edgeID := range []string{"edge-1", "edge-2", "edge-3"} {
		if err := fleetRepo.MarkOnline(ctx, "tenant-1", edgeID, "s-"+edgeID); err != nil {
			t.Fatalf("MarkOnline: %v", err)
		}
	}

	if err := rig.srv.RevokeTenant(ctx, "tenant-1"); err != nil {
		t.Fatalf("RevokeTenant = %v", err)
	}
	if !rig.tenantRevoked(t, "tenant-1") {
		t.Fatal("RevokeTenant no persistió el corte del tenant")
	}
	if rig.tenantRevoked(t, "tenant-2") {
		t.Error("el corte de tenant-1 se filtró a tenant-2")
	}
	for _, edgeID := range []string{"edge-1", "edge-2", "edge-3"} {
		if _, revoked := rig.leaseState(t, "tenant-1", edgeID); revoked {
			t.Errorf("RevokeTenant marcó revocado el lease de %s: RestoreTenant ya no podría reactivarlo (D-055.2)", edgeID)
		}
	}

	if err := rig.srv.RestoreTenant(ctx, "tenant-1"); err != nil {
		t.Fatalf("RestoreTenant = %v", err)
	}
	if rig.tenantRevoked(t, "tenant-1") {
		t.Fatal("RestoreTenant no reactivó el tenant")
	}
}

// Sin fleet inyectado, RevokeTenant persiste el corte y no falla: no hay a quién avisar.
func TestRevokeTenantWithoutFleetStillPersists(t *testing.T) {
	t.Parallel()
	rig := newRevokeRig(t)
	if err := rig.srv.RevokeTenant(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("RevokeTenant sin fleet = %v", err)
	}
	if !rig.tenantRevoked(t, "tenant-1") {
		t.Fatal("RevokeTenant sin fleet no persistió el corte")
	}
}

// El error del gestor de leases vuelve tal cual (envuelto por él, no por el gateway).
func TestRevocationFamilyReturnsTheLeaseError(t *testing.T) {
	t.Parallel()
	errRepo := errors.New("base caída")
	listed := &failingListFleet{err: errors.New("no debería listar"), deadline: -2}
	rig := newRevokeRig(t, WithFleet(listed))
	rig.leaseRepo.failWrites = errRepo
	ctx := context.Background()

	if err := rig.srv.RevokeLease(ctx, "tenant-1", "edge-1"); !errors.Is(err, errRepo) {
		t.Errorf("RevokeLease = %v, se esperaba el error del repositorio", err)
	}
	if err := rig.srv.RevokeTenant(ctx, "tenant-1"); !errors.Is(err, errRepo) {
		t.Errorf("RevokeTenant = %v, se esperaba el error del repositorio", err)
	}
	if listed.deadline != -2 {
		t.Error("RevokeTenant listó las instalaciones aunque el corte no se pudo persistir")
	}
	if err := rig.srv.RestoreTenant(ctx, "tenant-1"); !errors.Is(err, errRepo) {
		t.Errorf("RestoreTenant = %v, se esperaba el error del repositorio", err)
	}
}

// El error de fleet.List vuelve envuelto con su texto literal; el corte ya quedó persistido.
func TestRevokeTenantWrapsTheFleetListError(t *testing.T) {
	t.Parallel()
	errList := errors.New("select sin límite atascado")
	rig := newRevokeRig(t, WithFleet(&failingListFleet{err: errList}))

	err := rig.srv.RevokeTenant(context.Background(), "tenant-1")

	if !errors.Is(err, errList) {
		t.Fatalf("errors.Is(err, errList) = false; err = %v", err)
	}
	if want := "gatewaygrpc: listar instalaciones del tenant: " + errList.Error(); err.Error() != want {
		t.Errorf("error = %q, se esperaba %q", err.Error(), want)
	}
	if !rig.tenantRevoked(t, "tenant-1") {
		t.Error("el corte no quedó persistido antes de listar")
	}
}

// Las TRES entradas ponen su propio reloj sobre un contexto que llega pelado: el presupuesto
// de trabajo (5 s por defecto, o el de WithWorkTimeout). En RevokeTenant cubre además el
// fleet.List. Se afirma el plazo que VE la dependencia, no cuánto tarda nada.
func TestRevocationFamilyPutsItsOwnClock(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		opts   []Option
		budget time.Duration
	}{
		{name: "default budget", budget: 5 * time.Second},
		{name: "configured budget", opts: []Option{WithWorkTimeout(time.Hour)}, budget: time.Hour},
		{name: "zero budget falls back", opts: []Option{WithWorkTimeout(0)}, budget: 5 * time.Second},
	}
	within := func(t *testing.T, what string, left, budget time.Duration) {
		t.Helper()
		// El plazo se midió DESPUÉS de fijarlo, así que queda algo menos que el presupuesto;
		// el suelo (la mitad) solo distingue «este presupuesto» de «otro» o de «ninguno».
		if left <= budget/2 || left > budget {
			t.Errorf("%s vio un plazo de %v, se esperaba el presupuesto de %v", what, left, budget)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			listed := &failingListFleet{err: errors.New("corta aquí")}
			rig := newRevokeRig(t, append([]Option{WithFleet(listed)}, tc.opts...)...)
			ctx := context.Background() // sin deadline, como el del handler admin

			if err := rig.srv.RevokeLease(ctx, "tenant-1", "edge-1"); err != nil {
				t.Fatalf("RevokeLease = %v", err)
			}
			within(t, "RevokeLease (persistencia)", rig.leaseRepo.lastDeadline(t), tc.budget)

			if err := rig.srv.RevokeTenant(ctx, "tenant-1"); err == nil { // falla en List, a propósito
				t.Fatal("RevokeTenant no devolvió el error de fleet.List")
			}
			within(t, "RevokeTenant (persistencia)", rig.leaseRepo.lastDeadline(t), tc.budget)
			within(t, "RevokeTenant (fleet.List)", listed.deadline, tc.budget)

			if err := rig.srv.RestoreTenant(ctx, "tenant-1"); err != nil {
				t.Fatalf("RestoreTenant = %v", err)
			}
			within(t, "RestoreTenant", rig.leaseRepo.lastDeadline(t), tc.budget)
		})
	}
}
