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

// Rotación de KEK sobre las CUATRO tablas cifradas (Plan 042 · hallazgo #3; la
// cuarta, public.fleet_sessions, entró con el Plan 046 · T4.1). Hasta el arreglo del
// 042, Rekey y PendingByKeyID barrían SOLO public.contacts: el mapa vacío de
// "rotación completa" convivía con intake_buyer_data y tenant_integrations enteras
// todavía envueltas por la KEK vieja, y retirar esa KEK —el paso que ese mapa
// autoriza (§10.F)— las habría dejado ilegibles.
//
// 🔴 LA CUARTA SE SIEMBRA DE VERDAD, Y NO SIEMPRE FUE ASÍ. La T4.1 metió
// fleet_sessions en rekeyTargets y añadió su limpieza a wipeCifradas, pero NINGÚN
// caso llegaba a sembrar una fila suya con un sobre viejo: los dos tests seguían
// contando tres tablas, así que el censo podía haber quedado mal escrito —nombre de
// columna equivocado, PK incompleta— y los dos habrían pasado igual. La limpieza sin
// el sembrado daba falsa impresión de cobertura, que es peor que no tener ninguna.
// El criterio (d) de T4.1 se cierra AQUÍ: la fila aparece pendiente con su KEK vieja,
// desaparece tras rotar, y —lo que de verdad importa— el número SIGUE DESCIFRÁNDOSE
// después. Rotar y perder el dato es peor que no rotar.
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

// wipeCifradas deja el censo de rotación vacío: Rekey y PendingByKeyID escanean
// globalmente, así que sin tablero limpio los conteos no son deterministas y una
// fila ajena con una KEK ausente abortaría el scan.
//
// 🔴 public.fleet_sessions entró al censo con el Plan 046 · T4.1, y aquí se limpia
// DISTINTO: se NULIFICAN sus cuatro columnas de self_pn en vez de borrar la fila.
// Borrarla sería tirar el estado de flota que otros tests de la misma base dejaron
// sembrado, y lo que contamina el conteo no es la fila: es su sobre, envuelto por
// una KEK de OTRO test («test-kek-1») que este keyring no tiene.
func wipeCifradas(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, borrado := range []string{
		`DELETE FROM public.contacts`,
		`DELETE FROM public.intake_buyer_data`,
		`DELETE FROM public.tenant_integrations`,
		`UPDATE public.fleet_sessions
		    SET self_pn_enc = NULL, self_pn_dek = NULL,
		        self_pn_kek_id = NULL, self_pn_bidx = NULL
		  WHERE self_pn_kek_id IS NOT NULL`,
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

// selfPnDePrueba es el número propio (ya NORMALIZADO, E.164 sin '+') de la fila de
// public.fleet_sessions que entra al censo. Es PII de mentira sobre una base efímera,
// y aun así no se loguea en ningún sitio: los mensajes de fallo de esta suite hablan
// de tablas y de key_ids, nunca del valor.
const selfPnDePrueba = "56984467443"

// seedLasCuatro siembra UNA fila cifrada con cipher en cada una de las CUATRO tablas
// del censo, más una fila de tenant_integrations SIN secreto (trío NULL) que la
// rotación debe ignorar sin romperse.
func seedLasCuatro(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, tenant string) []filaCifrada {
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
	// La solicitud declara a su padre (D-043.21): desde la 0054 el CHECK
	// intakes_event_id_required_chk rechaza toda fila nueva sin event_id, así que
	// el seed monta la cadena tenant→evento→solicitud, no la solicitud sola.
	var eventID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		VALUES ($1, 'sesion-rekey', $2::uuid, 'cart', 'cart-2026-08-10-0001', 'open', 'flujo-rekey', 1)
		RETURNING id::text
	`, tenant, uuid.NewString()).Scan(&eventID); err != nil {
		t.Fatalf("sembrar el evento padre: %v", err)
	}
	intakeID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total, event_id)
		VALUES ($1, $2, 'contacto-opaco', 'sesion-rekey', 'open', 0, $3::uuid)
	`, intakeID, tenant, eventID); err != nil {
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

	// --- public.fleet_sessions (número propio de la sesión, Plan 046 · T4.1) ---
	sesion := sembrarSelfPnDeSesion(t, db, cipher, kp, tenant)

	return []filaCifrada{
		sesion,
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

// sembrarSelfPnDeSesion siembra la fila de public.fleet_sessions con el sobre del
// número propio envuelto por la KEK vigente del cipher. Es la CUARTA tabla del censo
// (Plan 046 · T4.1) y se siembra por SQL directo, como las otras tres: el repositorio
// de flota vive en otro paquete y usaría su propio keyring, que es justo lo que esta
// suite necesita controlar.
//
// Las cuatro columnas van JUNTAS —incluido self_pn_bidx, que la rotación NO toca— y
// esa es media prueba: el índice ciego se siembra aquí para poder afirmar después que
// sobrevivió intacto (§10.C, la indexKey no rota con la KEK).
func sembrarSelfPnDeSesion(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, tenant string) filaCifrada {
	t.Helper()
	enc, dek, kid := mustEncrypt(t, cipher, selfPnDePrueba)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx,
			 last_connected_at, last_seen_at, updated_at)
		VALUES ($1, 'edge-rekey', 'sesion-rekey', 'online', $2, $3, $4, $5, now(), now(), now())
	`, tenant, enc, dek, kid, kp.BlindIndex(tenant, selfPnDePrueba)); err != nil {
		t.Fatalf("sembrar fleet_sessions: %v", err)
	}
	return filaCifrada{
		tabla: "public.fleet_sessions", claro: selfPnDePrueba, enc: enc,
		leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db,
				`SELECT self_pn_enc, self_pn_dek, self_pn_kek_id FROM public.fleet_sessions WHERE tenant_id = $1`,
				tenant)
		},
	}
}

// bidxDeLaSesion lee el índice ciego CRUDO de la fila de flota sembrada. Se lee por
// SQL directo y no se recalcula: recalcularlo con el KeyProvider daría el mismo valor
// aunque la rotación hubiera reescrito la columna con basura.
func bidxDeLaSesion(t *testing.T, db *sql.DB, tenant string) string {
	t.Helper()
	var bidx string
	if err := db.QueryRowContext(context.Background(),
		`SELECT self_pn_bidx FROM public.fleet_sessions WHERE tenant_id = $1`, tenant).Scan(&bidx); err != nil {
		t.Fatalf("leer self_pn_bidx tras la rotación: %v", err)
	}
	return bidx
}

// TestRekey_LasCuatroTablas_Integration es el criterio del hallazgo #3, ampliado al
// criterio (d) del Plan 046 · T4.1: una pasada de rotación deja las CUATRO tablas
// cifradas en la KEK current, sin re-cifrar el dato, y PendingByKeyID solo declara
// "completa" cuando lo está de verdad.
//
// Con el barrido antiguo (solo public.contacts) este test falla en dos puntos
// distintos: Processed = 1 en vez de 4, y las filas de las otras tres siguen en la
// KEK "A".
//
// 💥 MUTACIONES QUE LO PONEN ROJO, por el lado de fleet_sessions:
//   - sacar la entrada de public.fleet_sessions de rekeyTargets (rekey.go:91-96) ⇒
//     Processed = 3 y la fila de flota se queda en la KEK "A"; y como la última fase
//     descifra con el keyring solo-{B}, el número propio queda ILEGIBLE — el fallo
//     exacto que el censo existe para impedir, y que la limpieza de wipeCifradas por
//     sí sola no delataba.
//   - equivocar dekCol/kekCol o la PK de esa entrada ⇒ el UPDATE no localiza la fila
//     y el kek_id no llega a "B".
//   - hacer que la rotación toque self_pn_bidx ⇒ la aserción de estabilidad del índice
//     ciego falla, y con ella se caería el anti-self-loop de toda la flota en la
//     siguiente rotación (§10.C: la indexKey NO rota con la KEK).
func TestRekey_LasCuatroTablas_Integration(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	// t0: KEK_A es la current; una fila cifrada en cada tabla del censo.
	kpA := mustKP(t, keyringA(), "A")
	filas := seedLasCuatro(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)
	// El índice ciego se apunta ANTES: la rotación no puede haberlo tocado.
	bidxAntes := bidxDeLaSesion(t, db, tenant)

	// t1: keyring {A,B} con B current. batch=1 fuerza varias pasadas por tabla.
	kpAB := mustKP(t, keyringAB(), "B")
	cipherAB := crypto.NewFieldCipher(kpAB)
	rep, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 1)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != len(filas) {
		t.Fatalf("Rekey processed = %d, want %d (una fila por tabla del censo: fleet_sessions, contacts, "+
			"intake_buyer_data, tenant_integrations)", rep.Processed, len(filas))
	}
	if rep.CurrentKeyID != "B" {
		t.Fatalf("Rekey current = %q, want B", rep.CurrentKeyID)
	}
	if len(rep.PendingByKeyID) != 0 {
		t.Fatalf("tras rotar: pendientes = %v, want vacío", rep.PendingByKeyID)
	}

	verificarFilasTrasRotar(t, db, cipherAB, filas)

	if bidxDespues := bidxDeLaSesion(t, db, tenant); bidxDespues != bidxAntes {
		t.Fatal("self_pn_bidx cambió al rotar la KEK: el índice ciego se calcula con la indexKey, que es " +
			"estable de por vida (§10.C). Si la rotación lo reescribe, ningún número vuelve a casar consigo " +
			"mismo y el anti-self-loop queda mudo para toda la flota")
	}

	// 2ª pasada = no-op (idempotente).
	rep2, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 1)
	if err != nil {
		t.Fatalf("Rekey (2ª pasada): %v", err)
	}
	if rep2.Processed != 0 {
		t.Fatalf("2ª pasada processed = %d, want 0 (idempotente)", rep2.Processed)
	}

	// Retiro seguro (§10.F): pendientes vacío ⇒ KEK_A retirable. Con el keyring solo
	// {B}, las CUATRO filas siguen legibles. Es la aserción que de verdad importa del
	// criterio (d): rotar y perder el dato es peor que no rotar.
	verificarFilasLegiblesSinLaKEKVieja(t, db, crypto.NewFieldCipher(mustKP(t, keyringB(), "B")), filas)
}

// verificarFilasTrasRotar comprueba, tabla a tabla, que la rotación hizo su trabajo y
// SOLO su trabajo: key_id en la current, dato cifrado INTACTO byte a byte (rotar es
// re-envolver la DEK, no re-cifrar el valor) y claro recuperable con la KEK nueva.
//
// Va extraída y NOMBRADA por gocyclo (umbral 15, que aplica también a los tests): con
// los dos bucles inline la función madre quedaba EXACTAMENTE en 15, así que la
// aserción del índice ciego la habría empujado al rojo.
func verificarFilasTrasRotar(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, filas []filaCifrada) {
	t.Helper()
	for _, f := range filas {
		enc, dek, kekID := f.leerFn(t, db)
		if kekID != "B" {
			t.Fatalf("%s: kek_id = %q tras rotar, want B (¿la tabla entró en el barrido?)", f.tabla, kekID)
		}
		if !bytes.Equal(enc, f.enc) {
			t.Fatalf("%s: el dato cifrado cambió tras rotar (la rotación NO debe re-cifrar)", f.tabla)
		}
		claro, derr := cipher.Decrypt(enc, dek, kekID)
		if derr != nil {
			t.Fatalf("%s: Decrypt tras rotar: %v", f.tabla, derr)
		}
		if claro != f.claro {
			t.Fatalf("%s: el claro cambió tras rotar", f.tabla)
		}
	}
}

// verificarFilasLegiblesSinLaKEKVieja descifra las cuatro filas con un keyring que YA
// NO tiene la KEK_A: es el retiro seguro del §10.F ejecutado de verdad, no supuesto a
// partir de un mapa de pendientes vacío.
func verificarFilasLegiblesSinLaKEKVieja(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, filas []filaCifrada) {
	t.Helper()
	for _, f := range filas {
		enc, dek, kekID := f.leerFn(t, db)
		claro, derr := cipher.Decrypt(enc, dek, kekID)
		if derr != nil {
			t.Fatalf("%s: Decrypt con la KEK_A ya retirada: %v", f.tabla, derr)
		}
		if claro != f.claro {
			t.Fatalf("%s: el claro cambió con la KEK_A retirada", f.tabla)
		}
	}
}

// TestPendingByKeyID_AgregaLasCuatroTablas es la otra mitad del hallazgo #3: el
// conteo de pendientes tiene que SUMAR las cuatro tablas. Es lo que decide si una KEK
// vieja se puede retirar del keyring; con el conteo antiguo (solo contacts) este
// test devuelve {"A":1} en vez de {"A":4} — y con las tablas nuevas vacías de
// contacts habría devuelto el mapa VACÍO, autorizando un retiro que deja filas
// ilegibles.
//
// 💥 MUTACIÓN QUE LO PONE ROJO (criterio (d) de T4.1): sacar public.fleet_sessions de
// rekeyTargets ⇒ el conteo baja a 3 con una fila de flota todavía bajo la KEK vieja.
// Ese es el número que autoriza a retirar una KEK: mentirlo por defecto deja a la
// flota entera sin número propio y sin forma de recuperarlo.
func TestPendingByKeyID_AgregaLasCuatroTablas(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	kpA := mustKP(t, keyringA(), "A")
	filas := seedLasCuatro(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)

	pending, err := crypto.PendingByKeyID(ctx, db, "B")
	if err != nil {
		t.Fatalf("PendingByKeyID: %v", err)
	}
	if got := pending["A"]; got != len(filas) {
		t.Fatalf("pendientes en la KEK A = %d, want %d (fleet_sessions + contacts + intake_buyer_data + "+
			"tenant_integrations); mapa completo: %v", got, len(filas), pending)
	}
	if len(pending) != 1 {
		t.Fatalf("pendientes = %v, want solo la KEK A (la fila sin secreto NO debe contar)", pending)
	}

	// Con A como current no queda nada pendiente: el mapa vacío significa
	// "retirable" y solo debe salir cuando lo es en las CUATRO tablas.
	vacio, err := crypto.PendingByKeyID(ctx, db, "A")
	if err != nil {
		t.Fatalf("PendingByKeyID(current=A): %v", err)
	}
	if len(vacio) != 0 {
		t.Fatalf("pendientes con current=A = %v, want vacío", vacio)
	}
}
