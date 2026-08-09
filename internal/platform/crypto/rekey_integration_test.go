package crypto_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/google/uuid"
)

// Rotación de KEK sobre las TRES tablas cifradas (Plan 042 · hallazgo #3). Hasta
// este arreglo, Rekey y PendingByKeyID barrían SOLO public.contacts: el mapa vacío
// de "rotación completa" convivía con intake_buyer_data y tenant_integrations
// enteras todavía envueltas por la KEK vieja, y retirar esa KEK —el paso que ese
// mapa autoriza (§10.F)— las habría dejado ilegibles.
//
// IMPORTANTE (higiene de BD compartida): Rekey escanea GLOBALMENTE (WHERE
// kek_id <> current, sin filtrar tenant — así rota TODA la flota, correcto en
// producción). Bajo `go test ./...` con un WAPP_TEST_DB_DSN único, otros paquetes
// de integración escriben en estas mismas tablas y una fila ajena (envuelta por una
// KEK ausente de este keyring) haría abortar el scan por fail-safe §10.J. Por eso
// la integración se corre SERIALIZADA (`go test -p 1`, como hace el Makefile) y
// cada test empieza con un tablero limpio.

const dsnEnv = "WAPP_TEST_DB_DSN"

// kek32 devuelve el base64 de una KEK de 32B rellena con fill (determinista).
func kek32(fill byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return base64.StdEncoding.EncodeToString(b)
}

// idxKeyB64 es la indexKey EXPLÍCITA y estable compartida por los providers del
// test: el índice ciego NO debe cambiar al rotar la KEK (§10.C).
func idxKeyB64() string { return kek32(0x44) }

func keyringA() string  { return "A:" + kek32(0x11) }
func keyringAB() string { return "A:" + kek32(0x11) + ",B:" + kek32(0x22) }
func keyringB() string  { return "B:" + kek32(0x22) }

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

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD: la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
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

// wipeCifradas deja las TRES tablas del censo de rotación vacías: Rekey y
// PendingByKeyID escanean globalmente, así que sin tablero limpio los conteos no
// son deterministas y una fila ajena con una KEK ausente abortaría el scan.
func wipeCifradas(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, borrado := range []string{
		`DELETE FROM public.contacts`,
		`DELETE FROM public.intake_buyer_data`,
		`DELETE FROM public.tenant_integrations`,
	} {
		if _, err := db.ExecContext(ctx, borrado); err != nil {
			t.Fatalf("limpiar (%s): %v", borrado, err)
		}
	}
}

// seedTenant crea un tenant (las contacts lo exigen por FK) y devuelve su UUID.
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	slug := "tenant-rekey-" + uuid.NewString()[:8]
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text
	`, slug, "Rekey tres tablas").Scan(&id); err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, id); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})
	return id
}

// filaCifrada es una fila sembrada: dónde vive y qué claro debe seguir dando.
type filaCifrada struct {
	tabla  string // nombre de la tabla, para los mensajes de fallo
	claro  string // plaintext original que debe seguir recuperándose
	enc    []byte // dato cifrado sembrado (debe quedar INTACTO tras rotar)
	leerFn func(t *testing.T, db *sql.DB) (enc, dek []byte, kekID string)
}

// seedLasTres siembra UNA fila cifrada con cipher en cada una de las tres tablas
// del censo, más una fila de tenant_integrations SIN secreto (trío NULL) que la
// rotación debe ignorar sin romperse.
func seedLasTres(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, tenant string) []filaCifrada {
	t.Helper()
	ctx := context.Background()

	// --- public.contacts (PII del contacto, Plan 011) ---
	const telefono = "573001110001"
	encC, dekC, kidC := mustEncrypt(t, cipher, telefono)
	bidx := kp.BlindIndex(tenant, telefono)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id)
		VALUES ($1, 'phone_e164', $2, $3, $4, $5)
	`, tenant, bidx, encC, dekC, kidC); err != nil {
		t.Fatalf("sembrar contacts: %v", err)
	}

	// --- public.intake_buyer_data (datos del comprador, Plan 041) ---
	intakeID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total)
		VALUES ($1, $2, 'contacto-opaco', 'sesion-rekey', 'open', 0)
	`, intakeID, tenant); err != nil {
		t.Fatalf("sembrar intakes: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE id = $1`, intakeID); err != nil {
			t.Logf("limpiando intake: %v", err)
		}
	})
	const buyer = `{"rut":"11.111.111-1"}`
	encB, dekB, kidB := mustEncrypt(t, cipher, buyer)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_buyer_data (intake_id, data_enc, data_dek, data_kek_id)
		VALUES ($1, $2, $3, $4)
	`, intakeID, encB, dekB, kidB); err != nil {
		t.Fatalf("sembrar intake_buyer_data: %v", err)
	}

	// --- public.tenant_integrations (secreto HMAC del puente, Plan 042) ---
	const firmaDelPuente = "valor-de-prueba-de-la-firma-del-puente"
	encI, dekI, kidI := mustEncrypt(t, cipher, firmaDelPuente)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_integrations
			(tenant_id, catalog_adapter, events_adapter, endpoint_url, secret_enc, secret_dek, secret_kek_id, enabled)
		VALUES ($1, 'local', 'webhook', 'https://puente.example/hook', $2, $3, $4, true)
	`, tenant, encI, dekI, kidI); err != nil {
		t.Fatalf("sembrar tenant_integrations: %v", err)
	}

	// Fila SIN secreto (el trío NULL): el barrido debe ignorarla sin fallar y sin
	// contarla como pendiente — `NULL <> 'x'` no es TRUE en SQL.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_integrations (tenant_id, catalog_adapter, events_adapter)
		VALUES ($1, 'local', 'local')
	`, "tenant-sin-secreto-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("sembrar tenant_integrations sin secreto: %v", err)
	}

	return []filaCifrada{
		{tabla: "public.contacts", claro: telefono, enc: encC, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db, `SELECT value_enc, value_dek, value_kek_id FROM public.contacts WHERE tenant_id = $1`, tenant)
		}},
		{tabla: "public.intake_buyer_data", claro: buyer, enc: encB, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db, `SELECT data_enc, data_dek, data_kek_id FROM public.intake_buyer_data WHERE intake_id = $1`, intakeID)
		}},
		{tabla: "public.tenant_integrations", claro: firmaDelPuente, enc: encI, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db, `SELECT secret_enc, secret_dek, secret_kek_id FROM public.tenant_integrations WHERE tenant_id = $1`, tenant)
		}},
	}
}

func mustEncrypt(t *testing.T, cipher *crypto.FieldCipher, claro string) (enc, dek []byte, kekID string) {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(claro)
	if err != nil {
		t.Fatalf("Encrypt(%q): %v", claro, err)
	}
	return enc, dek, kekID
}

func leerCifrado(t *testing.T, db *sql.DB, query string, arg any) (enc, dek []byte, kekID string) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, arg).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leer fila cifrada (%s): %v", query, err)
	}
	return enc, dek, kekID
}

// TestRekey_LasTresTablas_Integration es el criterio del hallazgo #3: una pasada
// de rotación deja las TRES tablas cifradas en la KEK current, sin re-cifrar el
// dato, y PendingByKeyID solo declara "completa" cuando lo está de verdad.
//
// Con el barrido antiguo (solo public.contacts) este test falla en dos puntos
// distintos: Processed = 1 en vez de 3, y las filas de intake_buyer_data y
// tenant_integrations siguen en la KEK "A".
func TestRekey_LasTresTablas_Integration(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	// t0: KEK_A es la current; una fila cifrada en cada tabla del censo.
	kpA := mustKP(t, keyringA(), "A")
	filas := seedLasTres(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)

	// t1: keyring {A,B} con B current. batch=1 fuerza varias pasadas por tabla.
	kpAB := mustKP(t, keyringAB(), "B")
	cipherAB := crypto.NewFieldCipher(kpAB)
	rep, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 1)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != len(filas) {
		t.Fatalf("Rekey processed = %d, want %d (una fila por tabla del censo: contacts, intake_buyer_data, tenant_integrations)",
			rep.Processed, len(filas))
	}
	if rep.CurrentKeyID != "B" {
		t.Fatalf("Rekey current = %q, want B", rep.CurrentKeyID)
	}
	if len(rep.PendingByKeyID) != 0 {
		t.Fatalf("tras rotar: pendientes = %v, want vacío", rep.PendingByKeyID)
	}

	// Cada tabla: key_id en la current, dato cifrado INTACTO byte-a-byte y claro
	// recuperable con la KEK nueva.
	for _, f := range filas {
		enc, dek, kekID := f.leerFn(t, db)
		if kekID != "B" {
			t.Fatalf("%s: kek_id = %q tras rotar, want B (¿la tabla entró en el barrido?)", f.tabla, kekID)
		}
		if !bytes.Equal(enc, f.enc) {
			t.Fatalf("%s: el dato cifrado cambió tras rotar (la rotación NO debe re-cifrar)", f.tabla)
		}
		claro, derr := cipherAB.Decrypt(enc, dek, kekID)
		if derr != nil {
			t.Fatalf("%s: Decrypt tras rotar: %v", f.tabla, derr)
		}
		if claro != f.claro {
			t.Fatalf("%s: el claro cambió tras rotar", f.tabla)
		}
	}

	// 2ª pasada = no-op (idempotente).
	rep2, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 1)
	if err != nil {
		t.Fatalf("Rekey (2ª pasada): %v", err)
	}
	if rep2.Processed != 0 {
		t.Fatalf("2ª pasada processed = %d, want 0 (idempotente)", rep2.Processed)
	}

	// Retiro seguro (§10.F): pendientes vacío ⇒ KEK_A retirable. Con el keyring
	// solo {B}, las tres filas siguen legibles.
	kpB := mustKP(t, keyringB(), "B")
	cipherB := crypto.NewFieldCipher(kpB)
	for _, f := range filas {
		enc, dek, kekID := f.leerFn(t, db)
		claro, derr := cipherB.Decrypt(enc, dek, kekID)
		if derr != nil {
			t.Fatalf("%s: Decrypt con la KEK_A ya retirada: %v", f.tabla, derr)
		}
		if claro != f.claro {
			t.Fatalf("%s: el claro cambió con la KEK_A retirada", f.tabla)
		}
	}
}

// TestPendingByKeyID_AgregaLasTresTablas es la otra mitad del hallazgo #3: el
// conteo de pendientes tiene que SUMAR las tres tablas. Es lo que decide si una KEK
// vieja se puede retirar del keyring; con el conteo antiguo (solo contacts) este
// test devuelve {"A":1} en vez de {"A":3} — y con las dos tablas nuevas vacías de
// contacts habría devuelto el mapa VACÍO, autorizando un retiro que deja filas
// ilegibles.
func TestPendingByKeyID_AgregaLasTresTablas(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	kpA := mustKP(t, keyringA(), "A")
	filas := seedLasTres(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)

	pending, err := crypto.PendingByKeyID(ctx, db, "B")
	if err != nil {
		t.Fatalf("PendingByKeyID: %v", err)
	}
	if got := pending["A"]; got != len(filas) {
		t.Fatalf("pendientes en la KEK A = %d, want %d (contacts + intake_buyer_data + tenant_integrations); mapa completo: %v",
			got, len(filas), pending)
	}
	if len(pending) != 1 {
		t.Fatalf("pendientes = %v, want solo la KEK A (la fila sin secreto NO debe contar)", pending)
	}

	// Con A como current no queda nada pendiente: el mapa vacío significa
	// "retirable" y solo debe salir cuando lo es en las TRES tablas.
	vacio, err := crypto.PendingByKeyID(ctx, db, "A")
	if err != nil {
		t.Fatalf("PendingByKeyID(current=A): %v", err)
	}
	if len(vacio) != 0 {
		t.Fatalf("pendientes con current=A = %v, want vacío", vacio)
	}
}
