package contact_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// kek32 devuelve el base64 de una KEK de 32B rellena con fill (determinista).
func kek32(fill byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return base64.StdEncoding.EncodeToString(b)
}

// idxKeyB64 es la indexKey EXPLÍCITA y estable compartida por todos los providers
// del test: así value_bidx (PK) es idéntico bajo KEK_A y KEK_B (§10.C, el punto que
// hace barata la rotación). Derivarla de la KEK haría cambiar el bidx al rotar.
func idxKeyB64() string { return kek32(0x44) }

// mustKP construye un KeyProvider con keyring explícito e indexKey estable.
func mustKP(t *testing.T, keyring, current string) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: keyring,
		CurrentID:  current,
		IndexB64:   idxKeyB64(),
	})
	if err != nil {
		t.Fatalf("NewEnvKeyProvider(%s current=%s): %v", keyring, current, err)
	}
	return kp
}

// keyrings del test: A sola (t0), {A,B} con B current (t1-t2), B sola (t4 retiro).
func keyringA() string  { return "A:" + kek32(0x11) }
func keyringAB() string { return "A:" + kek32(0x11) + ",B:" + kek32(0x22) }
func keyringB() string  { return "B:" + kek32(0x22) }

// countKEK cuenta filas de un tenant con un value_kek_id dado.
func countKEK(t *testing.T, db *sql.DB, tenantID, kekID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM public.contacts WHERE tenant_id = $1 AND value_kek_id = $2
	`, tenantID, kekID).Scan(&n); err != nil {
		t.Fatalf("countKEK(%s): %v", kekID, err)
	}
	return n
}

// wipeContacts limpia la tabla contacts: Rekey escanea GLOBALMENTE (WHERE
// value_kek_id <> current, sin filtrar tenant), así que el test necesita un tablero
// limpio para que PendingByKeyID sea determinista y para no toparse con filas de
// otros tests envueltas por KEKs ausentes de este keyring. contact_id no es blanco
// de ninguna FK, así que borrar contacts es seguro.
func wipeContacts(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM public.contacts`); err != nil {
		t.Fatalf("limpiar contacts: %v", err)
	}
}

// TestRekey_Integration cubre el humo de rotación del Plan 012 (§7, §10.D/F/J):
// cifra N contactos con KEK_A, rota a KEK_B con Rekey, y verifica que 0 filas
// quedan en A, todas siguen legibles, value_enc/PK NO cambian, la 2ª pasada es
// no-op, la rotación es reanudable (solo toca filas fuera de la current), la KEK_A
// es retirable a 0 pendientes y una KEK ausente referenciada falla claro.
func TestRekey_Integration(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeContacts(t, db)
	tenant := seedTenant(t, db)

	// t0: KEK_A es la current. Cifra N contactos (una ref phone cada uno).
	kpA := mustKP(t, keyringA(), "A")
	resolverA := contact.NewPostgresResolver(db, crypto.NewFieldCipher(kpA), kpA)

	const n = 5
	phones := make([]string, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		phones[i] = fmt.Sprintf("57300111%04d", i)
		ref := mustRef(t, contact.KindPhoneE164, phones[i])
		id, err := resolverA.Resolve(ctx, tenant, []contact.Ref{ref}, "")
		if err != nil {
			t.Fatalf("Resolve(%s): %v", phones[i], err)
		}
		ids[i] = id
	}
	if got := countKEK(t, db, tenant, "A"); got != n {
		t.Fatalf("tras cifrar con A: filas KEK_A = %d, want %d", got, n)
	}

	// Captura value_enc por PK ANTES de rotar (para el assert byte-a-byte).
	encBefore := snapshotEnc(t, db, tenant)

	// t1-t2: keyring {A, B} con B current. Rekey con batch pequeño (varias pasadas
	// internas) → todas las filas pasan de A a B.
	kpAB := mustKP(t, keyringAB(), "B")
	cipherAB := crypto.NewFieldCipher(kpAB)
	rep, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 2)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != n {
		t.Fatalf("Rekey processed = %d, want %d", rep.Processed, n)
	}
	if rep.CurrentKeyID != "B" {
		t.Fatalf("Rekey current = %q, want B", rep.CurrentKeyID)
	}
	if len(rep.PendingByKeyID) != 0 {
		t.Fatalf("tras rotar: pendientes = %v, want vacío", rep.PendingByKeyID)
	}
	if got := countKEK(t, db, tenant, "A"); got != 0 {
		t.Fatalf("tras rotar: filas KEK_A = %d, want 0", got)
	}
	if got := countKEK(t, db, tenant, "B"); got != n {
		t.Fatalf("tras rotar: filas KEK_B = %d, want %d", got, n)
	}

	// value_enc INTACTO byte-a-byte + PK sin cambio (la rotación NO re-cifra).
	assertSameBlobs(t, encBefore, snapshotEnc(t, db, tenant))

	// Todas legibles con la current B (Destino descifra con el key_id de la fila).
	resolverB := contact.NewPostgresResolver(db, cipherAB, kpAB)
	assertAllReadable(t, resolverB, tenant, ids, phones)

	// 2ª pasada = no-op (idempotente): 0 filas procesadas.
	rep2, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 2)
	if err != nil {
		t.Fatalf("Rekey (2ª pasada): %v", err)
	}
	if rep2.Processed != 0 {
		t.Fatalf("2ª pasada processed = %d, want 0 (idempotente)", rep2.Processed)
	}

	// Retiro seguro (§10.F): pendientes(current=B) vacío ⇒ KEK_A retirable. Con el
	// keyring solo {B}, las lecturas siguen OK (todo ya es B).
	kpBonly := mustKP(t, keyringB(), "B")
	resolverBonly := contact.NewPostgresResolver(db, crypto.NewFieldCipher(kpBonly), kpBonly)
	assertAllReadable(t, resolverBonly, tenant, ids, phones)

	// Fail-safe §10.J: una fila con un key_id ausente del keyring → Decrypt falla
	// claro (nunca corrupción ni panic).
	if _, err := db.ExecContext(ctx, `
		UPDATE public.contacts SET value_kek_id = 'ZZZ'
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenant, ids[0]); err != nil {
		t.Fatalf("forzar key_id ausente: %v", err)
	}
	if _, derr := resolverBonly.Destino(ctx, tenant, ids[0]); derr == nil {
		t.Fatalf("esperaba error de Destino con key_id ausente del keyring (fail-safe)")
	}
}

// TestRekey_Resumable comprueba que Rekey solo toca filas fuera de la current: si
// el tablero mezcla filas ya en B (current) con filas nuevas en A, la pasada
// procesa SOLO las de A y deja las B intactas (value_dek sin cambio) — reanudable
// por estado (§10.D).
func TestRekey_Resumable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeContacts(t, db)
	tenant := seedTenant(t, db)

	kpAB := mustKP(t, keyringAB(), "B")
	cipherAB := crypto.NewFieldCipher(kpAB)

	// Primer lote cifrado con A y ya rotado a B.
	kpA := mustKP(t, keyringA(), "A")
	resolverA := contact.NewPostgresResolver(db, crypto.NewFieldCipher(kpA), kpA)
	const firstBatch = 3
	for i := 0; i < firstBatch; i++ {
		ref := mustRef(t, contact.KindPhoneE164, fmt.Sprintf("57300222%04d", i))
		if _, err := resolverA.Resolve(ctx, tenant, []contact.Ref{ref}, ""); err != nil {
			t.Fatalf("Resolve lote1(%d): %v", i, err)
		}
	}
	if _, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 10); err != nil {
		t.Fatalf("Rekey lote1: %v", err)
	}
	// value_dek de las filas ya-B, para verificar que la próxima pasada NO las toca.
	dekAfterFirst := snapshotDEK(t, db, tenant)

	// Segundo lote nuevo cifrado con A (el tablero queda mezclado A + B).
	const secondBatch = 2
	for i := 0; i < secondBatch; i++ {
		ref := mustRef(t, contact.KindPhoneE164, fmt.Sprintf("57300333%04d", i))
		if _, err := resolverA.Resolve(ctx, tenant, []contact.Ref{ref}, ""); err != nil {
			t.Fatalf("Resolve lote2(%d): %v", i, err)
		}
	}
	if got := countKEK(t, db, tenant, "A"); got != secondBatch {
		t.Fatalf("antes de reanudar: filas KEK_A = %d, want %d", got, secondBatch)
	}

	// Reanudar: solo debe procesar el segundo lote (las A), no las B ya rotadas.
	rep, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 10)
	if err != nil {
		t.Fatalf("Rekey reanudado: %v", err)
	}
	if rep.Processed != secondBatch {
		t.Fatalf("reanudado processed = %d, want %d (solo las filas fuera de la current)", rep.Processed, secondBatch)
	}
	if got := countKEK(t, db, tenant, "A"); got != 0 {
		t.Fatalf("tras reanudar: filas KEK_A = %d, want 0", got)
	}

	// Las filas ya-B del primer lote quedaron intactas (mismo value_dek): Rekey NO
	// re-toca lo que ya está en la current. snapshotDEK del primer lote ⊆ el actual
	// (el segundo lote añade filas nuevas), así que comparamos solo las del primero.
	dekAfterResume := snapshotDEK(t, db, tenant)
	subset := make(map[string][]byte, len(dekAfterFirst))
	for pk := range dekAfterFirst {
		subset[pk] = dekAfterResume[pk]
	}
	assertSameBlobs(t, dekAfterFirst, subset)
}

// assertAllReadable verifica que cada contact_id descifra a su phone esperado.
func assertAllReadable(t *testing.T, r *contact.PostgresResolver, tenantID string, ids, phones []string) {
	t.Helper()
	for i, id := range ids {
		ref, derr := r.Destino(context.Background(), tenantID, id)
		if derr != nil {
			t.Fatalf("Destino(%s): %v", id, derr)
		}
		if ref.Value != phones[i] {
			t.Fatalf("Destino(%s): got %q want %q", id, ref.Value, phones[i])
		}
	}
}

// assertSameBlobs verifica que dos snapshots (PK → blob) son idénticos: mismas PK
// y mismos bytes (usado para value_enc: intacto byte-a-byte tras rotar).
func assertSameBlobs(t *testing.T, before, after map[string][]byte) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("cambió el nº de filas (PK): before=%d after=%d", len(before), len(after))
	}
	for pk, blob := range before {
		got, ok := after[pk]
		if !ok {
			t.Fatalf("PK %q desapareció tras rotar (la rotación NO debe tocar la PK)", pk)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("blob de %q cambió tras rotar (debe quedar intacto byte-a-byte)", pk)
		}
	}
}

// snapshotEnc mapea PK "kind|value_bidx" → value_enc para un tenant.
func snapshotEnc(t *testing.T, db *sql.DB, tenantID string) map[string][]byte {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT kind, value_bidx, value_enc FROM public.contacts WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("snapshot value_enc: %v", err)
	}
	return scanBlobs(t, rows)
}

// snapshotDEK mapea PK "kind|value_bidx" → value_dek para un tenant.
func snapshotDEK(t *testing.T, db *sql.DB, tenantID string) map[string][]byte {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT kind, value_bidx, value_dek FROM public.contacts WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("snapshot value_dek: %v", err)
	}
	return scanBlobs(t, rows)
}

// scanBlobs consume filas (kind, value_bidx, blob) en un mapa PK → blob (copia).
func scanBlobs(t *testing.T, rows *sql.Rows) map[string][]byte {
	t.Helper()
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Fatalf("cerrar snapshot: %v", cerr)
		}
	}()
	out := make(map[string][]byte)
	for rows.Next() {
		var (
			kind, bidx string
			blob       []byte
		)
		if serr := rows.Scan(&kind, &bidx, &blob); serr != nil {
			t.Fatalf("snapshot scan: %v", serr)
		}
		out[kind+"|"+bidx] = append([]byte(nil), blob...)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("snapshot iter: %v", rerr)
	}
	return out
}
