package contact_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en store/lease).
const dsnEnv = "WAPP_TEST_DB_DSN"

// newTestKeyProvider construye un KeyProvider con una KEK de test fija (32B) para
// que los tests cifren/deduzcan el índice ciego de forma determinista.
func newTestKeyProvider(t *testing.T) crypto.KeyProvider {
	t.Helper()
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		MasterB64: base64.StdEncoding.EncodeToString(master),
	})
	if err != nil {
		t.Fatalf("KeyProvider de test: %v", err)
	}
	return kp
}

// newTestResolver construye el PostgresResolver con el cifrado de PII cableado
// (KEK de test), como en producción pero con clave fija.
func newTestResolver(t *testing.T, db *sql.DB) *contact.PostgresResolver {
	t.Helper()
	kp := newTestKeyProvider(t)
	return contact.NewPostgresResolver(db, crypto.NewFieldCipher(kp), kp)
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-contacts-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Contacts Resolver Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// seedFlowState inserta una fila mínima de flow_state para (tenant, session,
// contactID), simulando una conversación viva que la fusión debe migrar.
func seedFlowState(t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars)
		VALUES ($1, $2, $3, 'f1', 1, 'root', '{}')
	`, tenantID, sessionID, contactID)
	if err != nil {
		t.Fatalf("insertar flow_state: %v", err)
	}
}

func flowStateContactID(t *testing.T, db *sql.DB, tenantID, sessionID string) (string, bool) {
	t.Helper()
	var cid string
	err := db.QueryRowContext(context.Background(), `
		SELECT contact_id::text FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID).Scan(&cid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false
	case err != nil:
		t.Fatalf("leer flow_state: %v", err)
	}
	return cid, true
}

func mustRef(t *testing.T, kind, value string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(kind, value)
	if err != nil {
		t.Fatalf("NewRef(%s, %q): %v", kind, value, err)
	}
	return ref
}

func TestPG_Resolve_ReusaYAta(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	phone := mustRef(t, contact.KindPhoneE164, "573001112233")
	lid := mustRef(t, contact.KindWALID, "88887777")

	id1, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "Juan")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	// misma ref → mismo id
	id2, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil || id2 != id1 {
		t.Fatalf("ref existente debe reusar: id=%q err=%v (quiero %q)", id2, err, id1)
	}
	// phone + lid nueva → mismo id, lid atado
	id3, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil || id3 != id1 {
		t.Fatalf("phone+lid debe reusar id1: id=%q err=%v", id3, err)
	}
	id4, err := r.Resolve(ctx, tenant, []contact.Ref{lid}, "")
	if err != nil || id4 != id1 {
		t.Fatalf("lid quedó atado a otro id: %q vs %q (err=%v)", id4, id1, err)
	}
}

func TestPG_Resolve_FusionMigraFlowState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	phone := mustRef(t, contact.KindPhoneE164, "573009998877")
	lid := mustRef(t, contact.KindWALID, "77776666")

	c1, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	// C2 más nuevo (creado después) por LID; sembramos su flow_state.
	c2, err := r.Resolve(ctx, tenant, []contact.Ref{lid}, "")
	if err != nil {
		t.Fatalf("Resolve lid: %v", err)
	}
	if c1 == c2 {
		t.Fatal("precondición: C1 y C2 distintos")
	}
	seedFlowState(t, db, tenant, "s-huerfano", c2)

	// Fusión: canónico = C1 (más antiguo); el flow_state de C2 migra a C1.
	canonical, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve fusión: %v", err)
	}
	if canonical != c1 {
		t.Fatalf("canónico debe ser el más antiguo C1=%q, fue %q", c1, canonical)
	}
	got, ok := flowStateContactID(t, db, tenant, "s-huerfano")
	if !ok {
		t.Fatal("el flow_state desapareció tras la fusión")
	}
	if got != c1 {
		t.Fatalf("flow_state quedó en %q, debía migrar a canónico %q", got, c1)
	}
}

func TestPG_Resolve_FusionConflictoConservaCanonico(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	phone := mustRef(t, contact.KindPhoneE164, "573001110000")
	lid := mustRef(t, contact.KindWALID, "66665555")

	c1, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	c2, err := r.Resolve(ctx, tenant, []contact.Ref{lid}, "")
	if err != nil {
		t.Fatalf("Resolve lid: %v", err)
	}
	// AMBOS tienen flow_state en la MISMA sesión → conflicto de PK al fundir.
	seedFlowState(t, db, tenant, "s-conflicto", c1)
	seedFlowState(t, db, tenant, "s-conflicto", c2)

	canonical, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve fusión: %v", err)
	}
	// Política: se CONSERVA el estado del canónico y se descarta el del huérfano.
	got, ok := flowStateContactID(t, db, tenant, "s-conflicto")
	if !ok || got != canonical {
		t.Fatalf("tras conflicto debe quedar SOLO el estado del canónico %q, quedó %q (ok=%v)", canonical, got, ok)
	}
}

func TestPG_Destino_Preferencia(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	phone := mustRef(t, contact.KindPhoneE164, "573002223344")
	lid := mustRef(t, contact.KindWALID, "55554444")

	id, err := r.Resolve(ctx, tenant, []contact.Ref{lid, phone}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dst, err := r.Destino(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Destino: %v", err)
	}
	if dst.Kind != contact.KindPhoneE164 {
		t.Fatalf("destino = %+v, quiero phone_e164", dst)
	}
	// Round-trip: el value descifrado por Destino coincide con el número original.
	if dst.Value != "573002223344" {
		t.Fatalf("Destino descifró %q, quiero 573002223344", dst.Value)
	}
}

// TestPG_ValueCifradoEnReposo verifica que la fila NO guarda el value en claro:
// no existe la columna `value` plano, value_enc/value_dek están poblados y el
// número original NO aparece en value_enc (design.md §4, criterio T1).
func TestPG_ValueCifradoEnReposo(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	const phoneNum = "573007776655"
	phone := mustRef(t, contact.KindPhoneE164, phoneNum)

	if _, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "Ana"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// (a) El esquema NO tiene columna `value` plano.
	var hasValue bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'contacts' AND column_name = 'value'
		)
	`).Scan(&hasValue); err != nil {
		t.Fatalf("consultar columnas: %v", err)
	}
	if hasValue {
		t.Fatal("la tabla contacts todavía tiene la columna `value` en claro (debe ser value_enc/value_dek/value_bidx)")
	}

	// (b) La fila guarda value_bidx/value_enc/value_dek poblados y el número NO
	// aparece en claro dentro de value_enc.
	var (
		bidx     string
		enc, dek []byte
	)
	if err := db.QueryRowContext(ctx, `
		SELECT value_bidx, value_enc, value_dek FROM public.contacts
		WHERE tenant_id = $1 AND kind = $2
	`, tenant, contact.KindPhoneE164).Scan(&bidx, &enc, &dek); err != nil {
		t.Fatalf("leer fila cifrada: %v", err)
	}
	if bidx == "" || len(enc) == 0 || len(dek) == 0 {
		t.Fatalf("columnas cifradas vacías: bidx=%q len(enc)=%d len(dek)=%d", bidx, len(enc), len(dek))
	}
	if bytes.Contains(enc, []byte(phoneNum)) {
		t.Fatal("el número aparece EN CLARO dentro de value_enc")
	}
}

// TestPG_ValueKekID_PersistidoYLeido verifica el discriminador de KEK del Plan 012
// (T1): el INSERT escribe value_kek_id con el key_id de la KEK que envolvió la DEK
// (en modo compat = "1"), la migración 0007 fijó el DEFAULT/backfill a ese mismo
// valor, y Destino descifra leyendo el value_kek_id de la fila (no la current). El
// dedup/lookup por value_bidx queda intacto (no depende del key_id).
func TestPG_ValueKekID_PersistidoYLeido(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	const phoneNum = "573004445566"
	phone := mustRef(t, contact.KindPhoneE164, phoneNum)
	lid := mustRef(t, contact.KindWALID, "44443333")

	id, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "Kek")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// (a) Ambas filas (insertNewContact + attachRef) escribieron value_kek_id="1"
	// (compatKeyID de la KEK maestra única del modo compat, = DEFAULT de 0007).
	rows, err := db.QueryContext(ctx, `
		SELECT kind, value_kek_id FROM public.contacts
		WHERE tenant_id = $1 AND contact_id = $2
		ORDER BY kind
	`, tenant, id)
	if err != nil {
		t.Fatalf("leer value_kek_id: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas: %v", cerr)
		}
	}()
	got := 0
	for rows.Next() {
		var kind, kekID string
		if err := rows.Scan(&kind, &kekID); err != nil {
			t.Fatalf("escanear: %v", err)
		}
		if kekID != "1" {
			t.Fatalf("kind %q: value_kek_id=%q, quiero \"1\" (compatKeyID)", kind, kekID)
		}
		got++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterar: %v", err)
	}
	if got != 2 {
		t.Fatalf("esperaba 2 filas (phone+lid) con value_kek_id, hubo %d", got)
	}

	// (b) Destino descifra usando el value_kek_id leído de la fila (round-trip).
	dst, err := r.Destino(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Destino: %v", err)
	}
	if dst.Kind != contact.KindPhoneE164 || dst.Value != phoneNum {
		t.Fatalf("Destino = %+v, quiero phone_e164 %q", dst, phoneNum)
	}

	// (c) Dedup por value_bidx INTACTO: re-resolver la misma phone reusa el id.
	id2, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, "")
	if err != nil || id2 != id {
		t.Fatalf("dedup por value_bidx roto: id2=%q err=%v (quiero %q)", id2, err, id)
	}
}

// TestPG_Migracion0007_Reaplicable comprueba que re-correr las migraciones (el
// runner es hash-replay full) tras 0007 es inocuo (aditiva + backfill idempotente),
// que la columna value_kek_id existe y que el backfill dejó las filas en "1".
func TestPG_Migracion0007_Reaplicable(t *testing.T) {
	db := openTestDB(t) // openTestDB ya corre Migrate una vez.
	ctx := context.Background()

	// La columna value_kek_id existe tras 0007.
	var hasCol bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'contacts' AND column_name = 'value_kek_id'
		)
	`).Scan(&hasCol); err != nil {
		t.Fatalf("consultar columna: %v", err)
	}
	if !hasCol {
		t.Fatal("0007 no creó la columna value_kek_id")
	}

	// Re-aplicar es inocuo: el runner marca Skipped (hash ya registrado) y no
	// altera datos. Insertamos un contacto, re-migramos y verificamos que su
	// value_kek_id sigue siendo el key_id inicial (backfill no lo pisa).
	tenant := seedTenant(t, db)
	r := newTestResolver(t, db)
	phone := mustRef(t, contact.KindPhoneE164, "573006667788")
	if _, err := r.Resolve(ctx, tenant, []contact.Ref{phone}, ""); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("re-migración: %v", err)
	}
	var kekID string
	if err := db.QueryRowContext(ctx, `
		SELECT value_kek_id FROM public.contacts
		WHERE tenant_id = $1 AND kind = $2
	`, tenant, contact.KindPhoneE164).Scan(&kekID); err != nil {
		t.Fatalf("leer value_kek_id tras re-migración: %v", err)
	}
	if kekID != "1" {
		t.Fatalf("re-migración pisó value_kek_id: %q (quiero \"1\")", kekID)
	}
}
