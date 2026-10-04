//go:build pendiente

package lease_test

// Los tests de lease.Manager: construcción, claves, emisión y renovación (R-L1, R-L7, R-L8).
// Las reglas de revocación (R-L2…R-L6) están en lease_revocation_test.go. El repositorio es el
// doble leasehelpertest.Memoria, que cumple la misma suite de contrato que Postgres.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease/leasehelpertest"
)

const (
	tenantOne = "tenant-1"
	edgeOne   = "edge-1"
)

// spyRepo envuelve un Repository: cuenta las llamadas y puede hacer fallar cada operación. Es lo
// que deja afirmar «no escribió nada» y «no leyó nada», y simular la base caída.
type spyRepo struct {
	lease.Repository

	upserts, markRevokeds, gets            atomic.Int64
	tenantReads, tenantMarks, tenantResets atomic.Int64

	errUpsert, errMarkRevoked, errGet                 error
	errTenantRevoked, errMarkTenant, errRestoreTenant error
}

func (r *spyRepo) Upsert(ctx context.Context, s lease.State) error {
	r.upserts.Add(1)
	if r.errUpsert != nil {
		return r.errUpsert
	}
	return r.Repository.Upsert(ctx, s)
}

func (r *spyRepo) MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	r.markRevokeds.Add(1)
	if r.errMarkRevoked != nil {
		return r.errMarkRevoked
	}
	return r.Repository.MarkRevoked(ctx, tenantID, edgeID, expiresAt)
}

func (r *spyRepo) Get(ctx context.Context, tenantID, edgeID string) (lease.State, bool, error) {
	r.gets.Add(1)
	if r.errGet != nil {
		return lease.State{}, false, r.errGet
	}
	return r.Repository.Get(ctx, tenantID, edgeID)
}

func (r *spyRepo) TenantRevoked(ctx context.Context, tenantID string) (bool, error) {
	r.tenantReads.Add(1)
	if r.errTenantRevoked != nil {
		return false, r.errTenantRevoked
	}
	return r.Repository.TenantRevoked(ctx, tenantID)
}

func (r *spyRepo) MarkTenantRevoked(ctx context.Context, tenantID string) error {
	r.tenantMarks.Add(1)
	if r.errMarkTenant != nil {
		return r.errMarkTenant
	}
	return r.Repository.MarkTenantRevoked(ctx, tenantID)
}

func (r *spyRepo) RestoreTenant(ctx context.Context, tenantID string) error {
	r.tenantResets.Add(1)
	if r.errRestoreTenant != nil {
		return r.errRestoreTenant
	}
	return r.Repository.RestoreTenant(ctx, tenantID)
}

// writes es cuántas escrituras, de cualquier clase, llegaron al repositorio.
func (r *spyRepo) writes() int64 {
	return r.upserts.Load() + r.markRevokeds.Load() + r.tenantMarks.Load() + r.tenantResets.Load()
}

// newManager construye un Manager con una clave nueva sobre un espía de Memoria.
func newManager(t *testing.T, opts ...lease.Option) (*lease.Manager, *spyRepo) {
	t.Helper()
	repo := &spyRepo{Repository: leasehelpertest.NewMemoria()}
	mgr, err := lease.NewManager(newKey(t), repo, opts...)
	if err != nil {
		t.Fatalf("NewManager: error inesperado %v", err)
	}
	return mgr, repo
}

// mustState devuelve la fila persistida del Edge, que tiene que existir.
func mustState(t *testing.T, repo lease.Repository, tenantID, edgeID string) lease.State {
	t.Helper()
	st, found, err := repo.Get(context.Background(), tenantID, edgeID)
	if err != nil || !found {
		t.Fatalf("Get(%s, %s) = (found=%v, err=%v), quería la fila", tenantID, edgeID, found, err)
	}
	return st
}

// requireNoState afirma que el Edge no tiene fila.
func requireNoState(t *testing.T, repo lease.Repository, tenantID, edgeID string) {
	t.Helper()
	st, found, err := repo.Get(context.Background(), tenantID, edgeID)
	if err != nil || found {
		t.Errorf("Get(%s, %s) = (%+v, found=%v, err=%v), quería que no hubiera fila", tenantID, edgeID, st, found, err)
	}
}

// requireLive afirma que lu es un lease VIGENTE firmado por mgr: un Validator nuevo lo acepta y,
// con la DEK, puede operar. Devuelve ese Validator.
func requireLive(t *testing.T, mgr *lease.Manager, lu *cloudlinkv1.LeaseUpdate) *cllease.Validator {
	t.Helper()
	if lu == nil {
		t.Fatal("LeaseUpdate nil, quería un lease vigente")
	}
	if lu.GetRevoked() {
		t.Fatal("el LeaseUpdate trae revoked=true, quería un lease vigente")
	}
	v := cllease.NewValidator(mgr.PublicKey())
	if err := v.Apply(lu); err != nil {
		t.Fatalf("el Validator del Edge rechazó el lease: %v", err)
	}
	if !v.CanOperate(true) {
		t.Fatal("con lease vigente y DEK, CanOperate debía ser true")
	}
	return v
}

// requireRevocation afirma que lu es una REVOCACIÓN firmada por mgr: un Validator que la aplica
// queda revocado y no puede operar ni con la DEK.
func requireRevocation(t *testing.T, mgr *lease.Manager, lu *cloudlinkv1.LeaseUpdate) {
	t.Helper()
	if lu == nil {
		t.Fatal("LeaseUpdate nil, quería el de revocación")
	}
	if !lu.GetRevoked() {
		t.Fatal("el LeaseUpdate trae revoked=false, quería el de revocación")
	}
	v := cllease.NewValidator(mgr.PublicKey())
	if err := v.Apply(lu); err != nil {
		t.Fatalf("el Validator del Edge rechazó la revocación: %v", err)
	}
	if !v.Revoked() || v.CanOperate(true) {
		t.Fatalf("tras aplicar la revocación: Revoked() = %v, CanOperate(true) = %v; quería true y false",
			v.Revoked(), v.CanOperate(true))
	}
}

// TestDefaultTTL_IsFifteenMinutes: R-L1, D-055.7. El valor es contrato: 15 minutos.
func TestDefaultTTL_IsFifteenMinutes(t *testing.T) {
	if lease.DefaultTTL != 15*time.Minute {
		t.Errorf("DefaultTTL = %v, quería 15m (D-055.7)", lease.DefaultTTL)
	}
}

// TestNewManager_RejectsBadKeyAndNilRepository: R-L7, con sus textos.
func TestNewManager_RejectsBadKeyAndNilRepository(t *testing.T) {
	repo := leasehelpertest.NewMemoria()
	badKeys := map[string]ed25519.PrivateKey{
		"nil key":        nil,
		"seed sized key": make(ed25519.PrivateKey, ed25519.SeedSize),
		"truncated key":  newKey(t)[:ed25519.PrivateKeySize-1],
	}
	for name, key := range badKeys {
		t.Run(name, func(t *testing.T) {
			mgr, err := lease.NewManager(key, repo)
			if err == nil || mgr != nil {
				t.Fatalf("NewManager = (%v, %v), quería (nil, error)", mgr, err)
			}
			if !strings.HasPrefix(err.Error(), "lease: construir issuer: ") {
				t.Errorf("error = %q, quería el prefijo %q", err, "lease: construir issuer: ")
			}
		})
	}

	mgr, err := lease.NewManager(newKey(t), nil)
	if err == nil || mgr != nil {
		t.Fatalf("NewManager con repo nil = (%v, %v), quería (nil, error)", mgr, err)
	}
	if err.Error() != "lease: repositorio nil" {
		t.Errorf("error = %q, quería %q", err, "lease: repositorio nil")
	}

	// La clave se comprueba antes que el repositorio.
	if _, err := lease.NewManager(nil, nil); err == nil || !strings.HasPrefix(err.Error(), "lease: construir issuer: ") {
		t.Errorf("NewManager(nil, nil): error = %v, quería el de la clave", err)
	}
}

// TestPublicKey_IsTheSigningKeyPublicHalf: PublicKey es la pública de la clave dada y
// PublicKeyBase64 es esa misma en base64 estándar.
func TestPublicKey_IsTheSigningKeyPublicHalf(t *testing.T) {
	priv := newKey(t)
	mgr, err := lease.NewManager(priv, leasehelpertest.NewMemoria())
	if err != nil {
		t.Fatalf("NewManager: error inesperado %v", err)
	}
	want, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("la pública de la clave del test no es Ed25519")
	}
	if !mgr.PublicKey().Equal(want) {
		t.Errorf("PublicKey() = %x, quería %x", mgr.PublicKey(), want)
	}
	raw, err := base64.StdEncoding.DecodeString(mgr.PublicKeyBase64())
	if err != nil {
		t.Fatalf("PublicKeyBase64() = %q no es base64 estándar: %v", mgr.PublicKeyBase64(), err)
	}
	if !bytes.Equal(raw, want) {
		t.Errorf("PublicKeyBase64() decodifica a %x, quería %x", raw, want)
	}
}

// TestIssueInitial_IssuesCounterOneWithDefaultTTL: R-L1. Counter 1, vigente, 15 minutos, y la
// fila persistida dice lo mismo que el lease firmado. Y la doble llave: el lease solo no basta.
func TestIssueInitial_IssuesCounterOneWithDefaultTTL(t *testing.T) {
	mgr, repo := newManager(t)
	before := time.Now()
	lu, err := mgr.IssueInitial(context.Background(), tenantOne, edgeOne)
	after := time.Now()
	if err != nil {
		t.Fatalf("IssueInitial: error inesperado %v", err)
	}
	v := requireLive(t, mgr, lu)
	if v.CanOperate(false) {
		t.Error("con lease vigente pero SIN la DEK, CanOperate debía ser false: hacen falta las dos llaves (ADR-0007)")
	}

	st := mustState(t, repo, tenantOne, edgeOne)
	if st.TenantID != tenantOne || st.EdgeID != edgeOne {
		t.Errorf("fila persistida de (%s, %s), quería (%s, %s)", st.TenantID, st.EdgeID, tenantOne, edgeOne)
	}
	if st.Counter != 1 {
		t.Errorf("counter inicial = %d, quería 1", st.Counter)
	}
	if st.Revoked {
		t.Error("el lease inicial quedó persistido como revocado")
	}
	if want := time.Unix(lu.GetExpiresUnix(), 0).UTC(); !st.ExpiresAt.Equal(want) || st.ExpiresAt.Location() != time.UTC {
		t.Errorf("expires_at persistido = %v, quería %v (el del lease firmado, en UTC)", st.ExpiresAt, want)
	}
	// El Issuer trunca a segundos: la expiración cae en [antes+TTL-1s, después+TTL].
	lo, hi := before.Add(lease.DefaultTTL-time.Second), after.Add(lease.DefaultTTL)
	if st.ExpiresAt.Before(lo) || st.ExpiresAt.After(hi) {
		t.Errorf("expires_at = %v fuera de [%v, %v]: el TTL por defecto son 15 minutos", st.ExpiresAt, lo, hi)
	}
	if repo.upserts.Load() != 1 || repo.markRevokeds.Load() != 0 {
		t.Errorf("escrituras: %d Upsert y %d MarkRevoked, quería 1 y 0", repo.upserts.Load(), repo.markRevokeds.Load())
	}
}

// TestWithTTL_AppliesOnlyPositiveDurations: R-L1. d > 0 fija el TTL; d <= 0 conserva el de
// antes (el por defecto, o el de una opción anterior).
func TestWithTTL_AppliesOnlyPositiveDurations(t *testing.T) {
	cases := []struct {
		name string
		opts []lease.Option
		want time.Duration
	}{
		{"positive ttl applies", []lease.Option{lease.WithTTL(2 * time.Hour)}, 2 * time.Hour},
		{"zero ttl is ignored", []lease.Option{lease.WithTTL(0)}, lease.DefaultTTL},
		{"negative ttl is ignored", []lease.Option{lease.WithTTL(-time.Minute)}, lease.DefaultTTL},
		{"zero after positive keeps the positive", []lease.Option{lease.WithTTL(time.Hour), lease.WithTTL(0)}, time.Hour},
		{"last positive wins", []lease.Option{lease.WithTTL(time.Hour), lease.WithTTL(3 * time.Hour)}, 3 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, repo := newManager(t, c.opts...)
			before := time.Now()
			if _, err := mgr.IssueInitial(context.Background(), tenantOne, edgeOne); err != nil {
				t.Fatalf("IssueInitial: error inesperado %v", err)
			}
			after := time.Now()
			st := mustState(t, repo, tenantOne, edgeOne)
			lo, hi := before.Add(c.want-time.Second), after.Add(c.want)
			if st.ExpiresAt.Before(lo) || st.ExpiresAt.After(hi) {
				t.Errorf("expires_at = %v fuera de [%v, %v] para un TTL de %v", st.ExpiresAt, lo, hi, c.want)
			}
		})
	}
}

// TestRenew_IssuesHeartbeatCounterPlusOne: R-L1. counter = heartbeatCounter + 1, en la fila y en
// el lease FIRMADO (un Validator que ya aplicó ese counter rechaza por replay uno igual o menor).
func TestRenew_IssuesHeartbeatCounterPlusOne(t *testing.T) {
	mgr, repo := newManager(t)
	ctx := context.Background()
	initial, err := mgr.IssueInitial(ctx, tenantOne, edgeOne)
	if err != nil {
		t.Fatalf("IssueInitial: error inesperado %v", err)
	}
	v := requireLive(t, mgr, initial)

	// El Edge reporta el counter 1 que tiene: la renovación es la 2, y el Validator la acepta.
	second, err := mgr.Renew(ctx, tenantOne, edgeOne, 1)
	if err != nil {
		t.Fatalf("Renew(1): error inesperado %v", err)
	}
	if err := v.Apply(second); err != nil {
		t.Fatalf("el Validator rechazó la renovación con counter 2 tras el 1: %v", err)
	}
	if got := mustState(t, repo, tenantOne, edgeOne).Counter; got != 2 {
		t.Errorf("counter tras Renew(1) = %d, quería 2", got)
	}

	// Renew(0) firma counter 1: el Validator, que ya va por el 2, lo rechaza por replay.
	stale, err := mgr.Renew(ctx, tenantOne, edgeOne, 0)
	if err != nil {
		t.Fatalf("Renew(0): error inesperado %v", err)
	}
	if err := v.Apply(stale); !errors.Is(err, cllease.ErrStaleCounter) {
		t.Errorf("aplicar el lease de Renew(0) tras el counter 2: error = %v, quería ErrStaleCounter", err)
	}
	// Renew(1) otra vez firma el 2, que ya está aplicado: tampoco entra (estrictamente creciente).
	same, err := mgr.Renew(ctx, tenantOne, edgeOne, 1)
	if err != nil {
		t.Fatalf("Renew(1): error inesperado %v", err)
	}
	if err := v.Apply(same); !errors.Is(err, cllease.ErrStaleCounter) {
		t.Errorf("aplicar otra vez el counter 2: error = %v, quería ErrStaleCounter", err)
	}

	third, err := mgr.Renew(ctx, tenantOne, edgeOne, 41)
	if err != nil {
		t.Fatalf("Renew(41): error inesperado %v", err)
	}
	if err := v.Apply(third); err != nil {
		t.Fatalf("el Validator rechazó la renovación con counter 42: %v", err)
	}
	st := mustState(t, repo, tenantOne, edgeOne)
	if st.Counter != 42 || st.Revoked {
		t.Errorf("fila tras Renew(41) = counter %d, revoked %v; quería 42 y false", st.Counter, st.Revoked)
	}
	if want := time.Unix(third.GetExpiresUnix(), 0).UTC(); !st.ExpiresAt.Equal(want) {
		t.Errorf("expires_at persistido = %v, quería %v (el del lease firmado)", st.ExpiresAt, want)
	}
}

// TestRenew_NeverSeenEdge_Issues: Renew no exige una emisión previa: emite hb+1 y crea la fila.
func TestRenew_NeverSeenEdge_Issues(t *testing.T) {
	mgr, repo := newManager(t)
	lu, err := mgr.Renew(context.Background(), tenantOne, edgeOne, 9)
	if err != nil {
		t.Fatalf("Renew: error inesperado %v", err)
	}
	requireLive(t, mgr, lu)
	if got := mustState(t, repo, tenantOne, edgeOne).Counter; got != 10 {
		t.Errorf("counter = %d, quería 10", got)
	}
}

// TestIssue_PersistFailure_ReturnsNoLease: si la emisión no se puede persistir, no se entrega
// lease: el estado de autorización y lo que se empuja al Edge no pueden divergir.
func TestIssue_PersistFailure_ReturnsNoLease(t *testing.T) {
	mgr, repo := newManager(t)
	cause := errors.New("base caída")
	repo.errUpsert = cause
	lu, err := mgr.IssueInitial(context.Background(), tenantOne, edgeOne)
	if lu != nil || !errors.Is(err, cause) {
		t.Fatalf("IssueInitial con Upsert caído = (%v, %v), quería (nil, error que envuelve la causa)", lu, err)
	}
	if !strings.HasPrefix(err.Error(), "lease: persistir emisión: ") {
		t.Errorf("error = %q, quería el prefijo %q", err, "lease: persistir emisión: ")
	}
}

// TestLease_CarriesNoKeyMaterial: R-L8. Ni el lease vigente ni el de revocación llevan la clave
// privada de firma (ni su semilla): el blob solo autoriza. La DEK ni siquiera existe a este lado.
func TestLease_CarriesNoKeyMaterial(t *testing.T) {
	priv := newKey(t)
	mgr, err := lease.NewManager(priv, leasehelpertest.NewMemoria())
	if err != nil {
		t.Fatalf("NewManager: error inesperado %v", err)
	}
	ctx := context.Background()
	live, err := mgr.IssueInitial(ctx, tenantOne, edgeOne)
	if err != nil {
		t.Fatalf("IssueInitial: error inesperado %v", err)
	}
	revocation, err := mgr.Revoke(ctx, tenantOne, edgeOne)
	if err != nil {
		t.Fatalf("Revoke: error inesperado %v", err)
	}
	seed := priv.Seed()
	secrets := map[string][]byte{
		"seed (raw)":         seed,
		"seed (base64)":      []byte(base64.StdEncoding.EncodeToString(seed)),
		"seed (raw url b64)": []byte(base64.RawURLEncoding.EncodeToString(seed)),
		"private key (b64)":  []byte(base64.StdEncoding.EncodeToString(priv)),
	}
	for name, lu := range map[string]*cloudlinkv1.LeaseUpdate{"live": live, "revocation": revocation} {
		for what, secret := range secrets {
			if bytes.Contains(lu.GetLease(), secret) {
				t.Errorf("el lease %s contiene %s de la clave de firma", name, what)
			}
		}
	}
}

// TestManager_ConcurrentUse: seguro para uso concurrente (con -race): N Edge emiten y renuevan
// a la vez y cada uno termina con su counter.
func TestManager_ConcurrentUse(t *testing.T) {
	mgr, repo := newManager(t)
	ctx := context.Background()
	edges := []string{"e0", "e1", "e2", "e3", "e4", "e5", "e6", "e7"}
	errs := make(chan error, len(edges))
	var wg sync.WaitGroup
	for _, edge := range edges {
		wg.Go(func() {
			if _, err := mgr.IssueInitial(ctx, tenantOne, edge); err != nil {
				errs <- err
				return
			}
			_, err := mgr.Renew(ctx, tenantOne, edge, 1)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("emisión concurrente: error inesperado %v", err)
		}
	}
	for _, edge := range edges {
		if st := mustState(t, repo, tenantOne, edge); st.Counter != 2 || st.Revoked {
			t.Errorf("%s: counter %d, revoked %v; quería 2 y false", edge, st.Counter, st.Revoked)
		}
	}
}
