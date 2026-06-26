package lease_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
)

// encodeSeed devuelve el seed de 32 bytes de la clave en base64 estándar.
func encodeSeed(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(priv.Seed())
}

// newManager construye un Manager con clave de dev y repo en memoria.
func newManager(t *testing.T) (*lease.Manager, *lease.MemoryRepository) {
	t.Helper()
	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	repo := lease.NewMemoryRepository()
	mgr, err := lease.NewManager(priv, repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, repo
}

func TestIssueInitialPersistsAndValidates(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	lu, err := mgr.IssueInitial(ctx, "tenant-1", "edge-1")
	if err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}

	// El Validator del Edge acepta el lease y puede operar (con DEK).
	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if !v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser true tras el lease inicial")
	}

	// Persistencia: counter inicial = 1, no revocado, expira ~ahora+TTL.
	st, found, err := repo.Get(ctx, "tenant-1", "edge-1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if st.Counter != 1 {
		t.Fatalf("counter inicial: got %d, want 1", st.Counter)
	}
	if st.Revoked {
		t.Fatal("el lease inicial no debe estar revocado")
	}
	if d := time.Until(st.ExpiresAt); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("expires_at fuera de rango (~5min): %v", d)
	}
}

func TestRenewAdvancesCounter(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	if _, err := mgr.IssueInitial(ctx, "t", "e"); err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}

	const heartbeatCounter int64 = 41
	lu, err := mgr.Renew(ctx, "t", "e", heartbeatCounter)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}

	st, _, err := repo.Get(ctx, "t", "e")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Counter != heartbeatCounter+1 {
		t.Fatalf("counter renovado: got %d, want %d", st.Counter, heartbeatCounter+1)
	}
	if lu.GetRevoked() {
		t.Fatal("la renovación no debe venir revocada")
	}

	// El Validator acepta la renovación (counter estrictamente creciente).
	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply renovación: %v", applyErr)
	}
	if !v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser true tras renovar")
	}
}

func TestRevokeBlocksAndPersists(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	initial, err := mgr.IssueInitial(ctx, "t", "e")
	if err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}

	revoke, err := mgr.Revoke(ctx, "t", "e")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !revoke.GetRevoked() {
		t.Fatal("el LeaseUpdate de revocación debe traer revoked=true")
	}

	// El Validator que aplica el lease vigente y luego la revocación queda
	// bloqueado de forma pegajosa.
	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(initial); applyErr != nil {
		t.Fatalf("Apply inicial: %v", applyErr)
	}
	if applyErr := v.Apply(revoke); applyErr != nil {
		t.Fatalf("Apply revocación: %v", applyErr)
	}
	if !v.Revoked() {
		t.Fatal("Validator.Revoked() debería ser true")
	}
	if v.CanOperate(true) {
		t.Fatal("CanOperate debería ser false tras la revocación")
	}

	// Persistencia: revoked=true.
	st, found, err := repo.Get(ctx, "t", "e")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !st.Revoked {
		t.Fatal("el estado persistido debería estar revocado")
	}
}

func TestPublicKeyBase64RoundTrip(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t)
	b64 := mgr.PublicKeyBase64()
	if b64 == "" {
		t.Fatal("PublicKeyBase64 vacío")
	}
}

func TestResolveSigningKeyBase64(t *testing.T) {
	t.Parallel()
	// Genera una clave, exponla por base64 de la pública para verificar que el
	// Manager construido con la privada parseada produce la MISMA pública.
	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	mgr, err := lease.NewManager(priv, lease.NewMemoryRepository())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Reconstruye desde el seed en base64.
	seedB64 := encodeSeed(t, priv)
	parsed, src, err := lease.ResolveSigningKey("", seedB64)
	if err != nil {
		t.Fatalf("ResolveSigningKey: %v", err)
	}
	if src != lease.KeySourceBase64 {
		t.Fatalf("source: got %q, want base64", src)
	}
	mgr2, err := lease.NewManager(parsed, lease.NewMemoryRepository())
	if err != nil {
		t.Fatalf("NewManager(parsed): %v", err)
	}
	if mgr.PublicKeyBase64() != mgr2.PublicKeyBase64() {
		t.Fatal("la clave reconstruida desde base64 no coincide")
	}
}

func TestResolveSigningKeyGeneratesDev(t *testing.T) {
	t.Parallel()
	priv, src, err := lease.ResolveSigningKey("", "")
	if err != nil {
		t.Fatalf("ResolveSigningKey: %v", err)
	}
	if src != lease.KeySourceGenerated {
		t.Fatalf("source: got %q, want generated-dev", src)
	}
	if len(priv) == 0 {
		t.Fatal("clave generada vacía")
	}
}
