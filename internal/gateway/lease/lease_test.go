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

	// Persistencia: counter inicial = 1, no revocado, expira ~ahora+TTL (15 min).
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
	if d := time.Until(st.ExpiresAt); d < 14*time.Minute || d > 16*time.Minute {
		t.Fatalf("expires_at fuera de rango (~15min): %v", d)
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

// TestMemoryUpsertNoResucitaLeaseRevocado ataca DIRECTAMENTE el segundo sitio
// del defecto (T2.1): `MemoryRepository.Upsert` ya no debe poder des-revocar.
// Los tests de IssueInitial/Renew NO cubren esto: con la guarda de wasRevoked
// puesta, Upsert ni siquiera se llama en el camino revocado, así que una
// regresión en el repo en memoria pasaría inadvertida. Aquí se llama a Upsert
// a pelo sobre una fila ya revocada.
// Se pone rojo si: Upsert vuelve a copiar s.Revoked del llamante sobre una
// fila existente (que es lo que hacía el `s.Revoked = false` original y lo
// que volvería a hacer un `r.leases[key] = s` sin conservar prev.Revoked).
func TestMemoryUpsertNoResucitaLeaseRevocado(t *testing.T) {
	t.Parallel()
	repo := lease.NewMemoryRepository()
	ctx := context.Background()

	if err := repo.Upsert(ctx, lease.State{
		TenantID: "t", EdgeID: "e", Counter: 1, ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("Upsert inicial: %v", err)
	}
	if err := repo.MarkRevoked(ctx, "t", "e", time.Now()); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}

	// Un Upsert "vigente" (Revoked cero) sobre la fila revocada: NO debe des-revocar.
	if err := repo.Upsert(ctx, lease.State{
		TenantID: "t", EdgeID: "e", Counter: 2, ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("Upsert tras revocar: %v", err)
	}
	st, found, err := repo.Get(ctx, "t", "e")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !st.Revoked {
		t.Fatal("Upsert NO debe resucitar un lease revocado (D-055.1 · T2.1)")
	}
	if st.Counter != 2 {
		t.Fatalf("Upsert sí debe actualizar el counter: got %d, want 2", st.Counter)
	}

	// Y el reverso del mismo contrato: Upsert tampoco REVOCA (para eso está
	// MarkRevoked); sobre una fila nueva, s.Revoked=true se ignora.
	if err := repo.Upsert(ctx, lease.State{
		TenantID: "t", EdgeID: "otro", Counter: 1, ExpiresAt: time.Now().Add(time.Minute), Revoked: true,
	}); err != nil {
		t.Fatalf("Upsert fila nueva: %v", err)
	}
	nuevo, found, err := repo.Get(ctx, "t", "otro")
	if err != nil || !found {
		t.Fatalf("Get fila nueva: found=%v err=%v", found, err)
	}
	if nuevo.Revoked {
		t.Fatal("una fila NUEVA nace no revocada: Upsert ignora s.Revoked (espeja el INSERT de Postgres)")
	}
}

// TestRevokeThenIssueInitialStaysRevoked es la prueba unitaria exacta de
// REQ-055.3 (reinicio del Edge) sin necesitar un Edge real: revoca, y luego
// simula que el Edge vuelve a arrancar y pide IssueInitial de nuevo. El
// LeaseUpdate resultante, aplicado a un Validator fresco (como el que
// construiría el Edge recién reiniciado), NO debe permitir operar.
func TestRevokeThenIssueInitialStaysRevoked(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	if _, err := mgr.IssueInitial(ctx, "t", "e"); err != nil {
		t.Fatalf("IssueInitial inicial: %v", err)
	}
	if _, err := mgr.Revoke(ctx, "t", "e"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Simula el Edge reiniciando y volviendo a pedir su lease inicial.
	lu, err := mgr.IssueInitial(ctx, "t", "e")
	if err != nil {
		t.Fatalf("IssueInitial tras revocar: %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("IssueInitial tras revocar debería devolver un LeaseUpdate revocado")
	}

	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser false: el reinicio del Edge no debe des-revocar")
	}

	// La persistencia sigue marcando revoked=true (Upsert no se llamó).
	st, found, err := repo.Get(ctx, "t", "e")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !st.Revoked {
		t.Fatal("el estado persistido debería seguir revocado tras IssueInitial")
	}
}

// TestRevokeThenRenewStaysRevoked es la misma prueba que
// TestRevokeThenIssueInitialStaysRevoked pero para el camino de Renew
// (REQ-055.5): un heartbeat de un Edge previamente revocado tampoco debe
// des-revocarlo.
func TestRevokeThenRenewStaysRevoked(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	if _, err := mgr.IssueInitial(ctx, "t", "e"); err != nil {
		t.Fatalf("IssueInitial inicial: %v", err)
	}
	if _, err := mgr.Revoke(ctx, "t", "e"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	const heartbeatCounter int64 = 41
	lu, err := mgr.Renew(ctx, "t", "e", heartbeatCounter)
	if err != nil {
		t.Fatalf("Renew tras revocar: %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("Renew tras revocar debería devolver un LeaseUpdate revocado")
	}

	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser false: renovar no debe des-revocar")
	}

	st, found, err := repo.Get(ctx, "t", "e")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if !st.Revoked {
		t.Fatal("el estado persistido debería seguir revocado tras Renew")
	}
}

// TestIssueInitialTenantRevokedNeverSeenEdge cubre el criterio de aceptación
// exacto de T3.2: un tenant marcado revocado (D-055.2) y un edge_id que NUNCA
// apareció en el repo (Get devolvería found=false) -- IssueInitial NO debe
// emitir un lease vigente: nace revocada.
// Se pone rojo si: wasRevoked deja de consultar TenantRevoked antes/además de
// Get, o si IssueInitial vuelve a devolver un LeaseUpdate con Revoked=false
// para un edge nuevo bajo un tenant revocado.
func TestIssueInitialTenantRevokedNeverSeenEdge(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	if err := repo.MarkTenantRevoked(ctx, "t-comercial"); err != nil {
		t.Fatalf("MarkTenantRevoked: %v", err)
	}

	// "edge-nunca-visto" jamás llamó a IssueInitial/Renew: no hay fila en repo.
	lu, err := mgr.IssueInitial(ctx, "t-comercial", "edge-nunca-visto")
	if err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("IssueInitial de una instalación nueva bajo un tenant revocado debería devolver Revoked=true")
	}

	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser false: la instalación nace revocada bajo un tenant cortado")
	}

	// Upsert no debió tocarse: el repo no debe tener fila de lease para este edge.
	// El error se comprueba a propósito: si Get fallara, `found` sería false y el
	// test pasaría por la razón equivocada, sin haber mirado nada.
	_, found, getErr := repo.Get(ctx, "t-comercial", "edge-nunca-visto")
	if getErr != nil {
		t.Fatalf("repo.Get: %v", getErr)
	}
	if found {
		t.Fatal("no debería haberse persistido una fila de lease para un edge que nace revocado por su tenant")
	}
}

// TestIssueInitialTenantActivoEdgeNuevoSigueEmitiendoVigente es la regresión
// explícita del criterio de T3.2: un tenant NO revocado con un edge nuevo
// sigue emitiendo vigente igual que hoy (no-regresión de
// TestIssueInitialPersistsAndValidates).
// Se pone rojo si: el chequeo de TenantRevoked bloquea también a tenants
// activos (p. ej. por invertir la condición o por tratar found=false de
// TenantRevoked como revocado).
func TestIssueInitialTenantActivoEdgeNuevoSigueEmitiendoVigente(t *testing.T) {
	t.Parallel()
	mgr, _ := newManager(t)
	ctx := context.Background()

	lu, err := mgr.IssueInitial(ctx, "t-activo", "edge-nuevo")
	if err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}
	if lu.GetRevoked() {
		t.Fatal("un tenant activo con un edge nuevo NO debería nacer revocado")
	}

	v := cllease.NewValidator(mgr.PublicKey())
	if applyErr := v.Apply(lu); applyErr != nil {
		t.Fatalf("Validator.Apply: %v", applyErr)
	}
	if !v.CanOperate(true) {
		t.Fatal("CanOperate(true) debería ser true: tenant activo, edge nuevo")
	}
}

// TestRestoreTenantUnblocksFutureIssue cubre el reverso (T3.3): revocar y
// luego restaurar el tenant deja a una instalación NUEVA volver a emitir
// vigente -- RestoreTenant limpia el corte comercial sin necesitar tocar
// ninguna fila de leases.
// Se pone rojo si: RestoreTenant no limpia el estado en el repo, o si
// wasRevoked sigue leyendo el tenant como revocado tras RestoreTenant.
func TestRestoreTenantUnblocksFutureIssue(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	if err := mgr.RevokeTenant(ctx, "t-vaiven"); err != nil {
		t.Fatalf("RevokeTenant: %v", err)
	}
	revoked, err := repo.TenantRevoked(ctx, "t-vaiven")
	if err != nil || !revoked {
		t.Fatalf("TenantRevoked tras RevokeTenant: revoked=%v err=%v", revoked, err)
	}

	lu, err := mgr.IssueInitial(ctx, "t-vaiven", "edge-x")
	if err != nil {
		t.Fatalf("IssueInitial (tenant revocado): %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("con el tenant revocado, IssueInitial debería devolver Revoked=true")
	}

	if err := mgr.RestoreTenant(ctx, "t-vaiven"); err != nil {
		t.Fatalf("RestoreTenant: %v", err)
	}
	revoked, err = repo.TenantRevoked(ctx, "t-vaiven")
	if err != nil || revoked {
		t.Fatalf("TenantRevoked tras RestoreTenant: revoked=%v err=%v (debería ser false)", revoked, err)
	}

	// Una instalación NUEVA del mismo tenant, ya restaurado, sí emite vigente.
	lu2, err := mgr.IssueInitial(ctx, "t-vaiven", "edge-y")
	if err != nil {
		t.Fatalf("IssueInitial tras restaurar: %v", err)
	}
	if lu2.GetRevoked() {
		t.Fatal("tras RestoreTenant, una instalación nueva NO debería nacer revocada")
	}
}

// TestSignTenantRevocationDoesNotPersistPerEdge cubre la separación de los
// dos sujetos de corte (D-055.2): firmar la notificación de revocación de
// tenant para un edge concreto (el camino que usa el fan-out de
// gatewaygrpc.Server.RevokeTenant) NO debe dejar ese edge marcado como
// revocado en el repo de leases -- si lo hiciera, RestoreTenant no podría
// reactivarlo (leases no tiene reverso por-instalación).
// Se pone rojo si: SignTenantRevocation empieza a llamar a repo.MarkRevoked
// (o cualquier persistencia por-edge) además de firmar el blob.
func TestSignTenantRevocationDoesNotPersistPerEdge(t *testing.T) {
	t.Parallel()
	mgr, repo := newManager(t)
	ctx := context.Background()

	if _, err := mgr.IssueInitial(ctx, "t-fanout", "edge-vivo"); err != nil {
		t.Fatalf("IssueInitial: %v", err)
	}

	lu, err := mgr.SignTenantRevocation("edge-vivo", "t-fanout")
	if err != nil {
		t.Fatalf("SignTenantRevocation: %v", err)
	}
	if !lu.GetRevoked() {
		t.Fatal("el LeaseUpdate firmado por SignTenantRevocation debe traer Revoked=true")
	}

	st, found, err := repo.Get(ctx, "t-fanout", "edge-vivo")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if st.Revoked {
		t.Fatal("SignTenantRevocation no debe marcar leases.revoked del edge individual (D-055.2)")
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
