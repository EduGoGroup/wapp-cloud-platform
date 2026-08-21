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

// Rotación de KEK sobre los CINCO SOBRES del censo, repartidos en CUATRO tablas
// (Plan 042 · hallazgo #3; la cuarta tabla, public.fleet_sessions, entró con el Plan
// 046 · T4.1; el quinto sobre, contacts.push_name, con el Plan 046 · T4.2). Hasta el
// arreglo del 042, Rekey y PendingByKeyID barrían SOLO public.contacts: el mapa vacío
// de "rotación completa" convivía con intake_buyer_data y tenant_integrations enteras
// todavía envueltas por la KEK vieja, y retirar esa KEK —el paso que ese mapa
// autoriza (§10.F)— las habría dejado ilegibles.
//
// 🔴 CINCO SOBRES EN CUATRO TABLAS, Y ESE MATIZ ES LA LECCIÓN DE T4.2. Una entrada
// del censo NO describe una tabla: describe UN SOBRE. La fila de public.contacts
// tiene DOS —el del identificador (value_enc/value_dek/value_kek_id, Plan 011) y el
// del nombre (push_name_*, migración 0069)—, con DEK distintas y rotables por
// separado, y el barrido de uno no ve al otro. Por eso esta suite cuenta SOBRES y no
// filas: Rekey.Processed = 5 con solo 4 filas sembradas, y la fila de contacts aporta
// +2 ella sola. Quien lea "5" como "5 contactos" se equivoca; son 5 sobres.
//
// 🔴 LA CUARTA TABLA SE SIEMBRA DE VERDAD, Y NO SIEMPRE FUE ASÍ. La T4.1 metió
// fleet_sessions en rekeyTargets y añadió su limpieza a wipeCifradas, pero NINGÚN
// caso llegaba a sembrar una fila suya con un sobre viejo: los dos tests seguían
// contando tres tablas, así que el censo podía haber quedado mal escrito —nombre de
// columna equivocado, PK incompleta— y los dos habrían pasado igual. La limpieza sin
// el sembrado daba falsa impresión de cobertura, que es peor que no tener ninguna.
// El criterio (d) de T4.1 se cierra AQUÍ: la fila aparece pendiente con su KEK vieja,
// desaparece tras rotar, y —lo que de verdad importa— el número SIGUE DESCIFRÁNDOSE
// después. Rotar y perder el dato es peor que no rotar. El criterio (c) de T4.2 se
// cierra igual, con el sobre del nombre montado SOBRE LA MISMA FILA FÍSICA que el del
// identificador: ese es el caso nuevo, y no dos filas distintas.
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
//
// 🔴 EL SOBRE DE push_name (T4.2) NO AÑADE NADA AQUÍ, Y SE DICE EN VEZ DE DEJAR EL
// SILENCIO. Es un sobre nuevo del censo, así que la pregunta es obligada — pero vive
// en las MISMAS filas de public.contacts que el sobre de value, y esas filas se
// borran enteras (`DELETE FROM public.contacts`, sin WHERE). Un DELETE se lleva la
// fila con sus DOS sobres: no hay forma de que sobreviva un push_name_kek_id ajeno a
// una fila ya borrada. Si algún día contacts pasara a limpiarse por NULIFICACIÓN
// —como fleet_sessions, para no tirar estado de otros tests— ENTONCES sí habría que
// añadir las tres columnas del nombre a ese UPDATE, y olvidarlo dejaría sobres
// huérfanos bajo una KEK ausente que abortarían el scan de todos los demás tests.
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
	`, slug, "Rekey del censo").Scan(&id); err != nil {
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

// sobreCifrado es UN SOBRE sembrado: dónde vive y qué claro debe seguir dando.
//
// 🔴 SE LLAMA «SOBRE» Y NO «FILA» DESDE T4.2, Y NO ES COSMÉTICA. Dos elementos de
// esta lista pueden apuntar a la MISMA fila física de public.contacts (el sobre de
// value y el de push_name), igual que dos entradas del censo apuntan a la misma
// tabla. Contar filas aquí daría 4 donde Rekey.Processed da 5.
type sobreCifrado struct {
	sobre  string // "tabla.columna_enc", para los mensajes de fallo
	claro  string // plaintext original que debe seguir recuperándose
	enc    []byte // dato cifrado sembrado (debe quedar INTACTO tras rotar)
	leerFn func(t *testing.T, db *sql.DB) (enc, dek []byte, kekID string)
}

// selfPnDePrueba es el número propio (ya NORMALIZADO, E.164 sin '+') de la fila de
// public.fleet_sessions que entra al censo. Es PII de mentira sobre una base efímera,
// y aun así no se loguea en ningún sitio: los mensajes de fallo de esta suite hablan
// de tablas y de key_ids, nunca del valor.
const selfPnDePrueba = "56984467443"

// pushNameDePrueba es el nombre del contacto que va dentro del QUINTO sobre. Igual
// que el anterior: PII de mentira, y aun así no aparece en ningún mensaje de fallo.
// NO se normaliza (0069: es texto libre, no un identificador) y por eso lleva espacio
// y mayúsculas — si alguien añadiera una normalización, este valor la delataría.
const pushNameDePrueba = "Nombre De Prueba"

// seedLosCincoSobres siembra los CINCO SOBRES del censo sobre CUATRO filas —una en
// cada tabla—, todos envueltos por la KEK vigente del cipher, más una fila de
// tenant_integrations SIN secreto (trío NULL) que la rotación debe ignorar sin
// romperse.
//
// 🔴 LA FILA DE public.contacts LLEVA LOS DOS SOBRES: el del identificador (Plan 011)
// y el del nombre (T4.2, migración 0069). Es UNA sola fila física con DOS DEK
// independientes, y ese es exactamente el caso que T4.2 viene a ejercitar — no dos
// filas distintas, que no probarían nada nuevo. Por eso devuelve 5 elementos con 4
// INSERT: el orden de la lista NO importa (Rekey barre por censo, no por esta lista),
// pero su LONGITUD sí, porque es el número que se compara contra Report.Processed.
func seedLosCincoSobres(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, tenant string) []sobreCifrado {
	t.Helper()
	ctx := context.Background()

	// --- public.contacts, LOS DOS SOBRES DE LA MISMA FILA ---
	// value (Plan 011, migraciones 0006/0007) + push_name (T4.2, migración 0069).
	// Un solo INSERT: las siete columnas del par de sobres van juntas porque son la
	// misma fila. Las DEK son DISTINTAS (cada Encrypt genera la suya) aunque las
	// envuelva hoy la misma KEK.
	const telefono = "573001110001"
	encC, dekC, kidC := mustEncrypt(t, cipher, telefono)
	encN, dekN, kidN := mustEncrypt(t, cipher, pushNameDePrueba)
	// Guarda contra el copy-paste, que en un seed tan denso como este es el fallo
	// probable: si alguien reusara encC/dekC para las columnas del nombre, los dos
	// "sobres" serían el mismo y rotarlos por separado no probaría nada. Solo puede
	// dispararse por ese error de escritura — dos Encrypt distintos jamás coinciden.
	if bytes.Equal(dekC, dekN) || bytes.Equal(encC, encN) {
		t.Fatal("los dos sobres de la fila de contacts comparten DEK envuelta o dato cifrado: tienen " +
			"que venir de dos Encrypt independientes, o el test no ejercita nada de T4.2")
	}
	bidx := kp.BlindIndex(tenant, telefono)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.contacts
			(tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id,
			 push_name_enc, push_name_dek, push_name_kek_id)
		VALUES ($1, 'phone_e164', $2, $3, $4, $5, $6, $7, $8)
	`, tenant, bidx, encC, dekC, kidC, encN, dekN, kidN); err != nil {
		t.Fatalf("sembrar contacts (los dos sobres): %v", err)
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

	return []sobreCifrado{
		sesion,
		{sobre: "public.contacts.value_enc", claro: telefono, enc: encC, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db,
				`SELECT value_enc, value_dek, value_kek_id FROM public.contacts WHERE tenant_id = $1 AND value_bidx = $2`,
				tenant, bidx)
		}},
		// EL QUINTO SOBRE: misma fila que el anterior (misma PK), otras tres columnas.
		{sobre: "public.contacts.push_name_enc", claro: pushNameDePrueba, enc: encN, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db,
				`SELECT push_name_enc, push_name_dek, push_name_kek_id FROM public.contacts WHERE tenant_id = $1 AND value_bidx = $2`,
				tenant, bidx)
		}},
		{sobre: "public.intake_buyer_data.data_enc", claro: buyer, enc: encB, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db, `SELECT data_enc, data_dek, data_kek_id FROM public.intake_buyer_data WHERE intake_id = $1`, intakeID)
		}},
		{sobre: "public.tenant_integrations.secret_enc", claro: firmaDelPuente, enc: encI, leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db, `SELECT secret_enc, secret_dek, secret_kek_id FROM public.tenant_integrations WHERE tenant_id = $1`, tenant)
		}},
	}
}

func mustEncrypt(t *testing.T, cipher *crypto.FieldCipher, claro string) (enc, dek []byte, kekID string) {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(claro)
	if err != nil {
		// El claro NO va en el mensaje: desde T4.2 uno de los claros es un nombre
		// propio (PII). Basta con saber qué sobre falló, y eso lo dice el llamador.
		t.Fatalf("Encrypt: %v", err)
	}
	return enc, dek, kekID
}

// leerCifrado lee el trío (enc, dek, kek_id) de UN sobre. Es variadic desde T4.2:
// la fila de contacts se localiza por (tenant_id, value_bidx) y no solo por tenant,
// porque el test del contacto sin nombre siembra su propia fila en la misma tabla.
func leerCifrado(t *testing.T, db *sql.DB, query string, args ...any) (enc, dek []byte, kekID string) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leer sobre cifrado (%s): %v", query, err)
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
func sembrarSelfPnDeSesion(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, tenant string) sobreCifrado {
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
	return sobreCifrado{
		sobre: "public.fleet_sessions.self_pn_enc", claro: selfPnDePrueba, enc: enc,
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

// TestRekey_LosCincoSobres_Integration es el criterio del hallazgo #3, ampliado al
// criterio (d) del Plan 046 · T4.1 y al criterio (c) de T4.2: una pasada de rotación
// deja los CINCO SOBRES del censo —repartidos en CUATRO tablas— en la KEK current,
// sin re-cifrar ningún dato, y PendingByKeyID solo declara "completa" cuando lo está
// de verdad.
//
// 🔴 Processed CUENTA SOBRES, NO FILAS. Se siembran 4 filas y se esperan 5, porque la
// fila de public.contacts aporta DOS (value y push_name, DEK independientes). Con el
// barrido antiguo (solo el sobre de value de contacts) este test falla en dos puntos
// distintos: Processed = 1 en vez de 5, y los otros cuatro sobres siguen en la KEK "A".
//
// 💥 MUTACIONES QUE LO PONEN ROJO:
//   - LA MUTACIÓN PRESCRITA POR T4.2 — quitar la QUINTA entrada del censo, la de
//     public.contacts/push_name_dek/push_name_kek_id (rekey.go:153-158) ⇒ la aserción
//     de Processed falla con **Processed = 4, want 5**, y el sobre del nombre se queda
//     en la KEK "A". Y como la última fase descifra con el keyring solo-{B}, ese sobre
//     queda ILEGIBLE: verificarSobresLegiblesSinLaKEKVieja falla en
//     public.contacts.push_name_enc. Es el fallo exacto que el censo existe para
//     impedir, y que la PRIMERA entrada de contacts no tapa: su barrido filtra por
//     value_kek_id y no mira siquiera la columna del nombre.
//   - sacar la entrada de public.fleet_sessions de rekeyTargets (rekey.go:91-96) ⇒
//     Processed = 4 igualmente (mismo número, OTRO sobre roto: el del número propio),
//     y el keyring solo-{B} lo deja ilegible.
//   - equivocar dekCol/kekCol o la PK de cualquiera de las cinco entradas ⇒ el UPDATE
//     no localiza la fila y el kek_id no llega a "B".
//   - hacer que la rotación toque self_pn_bidx ⇒ la aserción de estabilidad del índice
//     ciego falla, y con ella se caería el anti-self-loop de toda la flota en la
//     siguiente rotación (§10.C: la indexKey NO rota con la KEK).
//   - hacer que la quinta entrada escriba también push_name_enc ⇒ falla la aserción de
//     "el dato cifrado cambió tras rotar": re-envolver la DEK NO re-cifra el dato (§7),
//     y esa es la propiedad que hace barata la rotación.
func TestRekey_LosCincoSobres_Integration(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	// t0: KEK_A es la current; los cinco sobres del censo sembrados bajo ella.
	kpA := mustKP(t, keyringA(), "A")
	sobres := seedLosCincoSobres(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)
	// El índice ciego se apunta ANTES: la rotación no puede haberlo tocado.
	bidxAntes := bidxDeLaSesion(t, db, tenant)

	// t1: keyring {A,B} con B current. batch=1 fuerza varias pasadas por entrada.
	kpAB := mustKP(t, keyringAB(), "B")
	cipherAB := crypto.NewFieldCipher(kpAB)
	rep, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 1)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != len(sobres) {
		t.Fatalf("Rekey processed = %d, want %d. Processed cuenta SOBRES, NO FILAS: son 5 sobres sobre "+
			"4 filas (fleet_sessions.self_pn, contacts.value, contacts.push_name, intake_buyer_data.data, "+
			"tenant_integrations.secret) y la fila de contacts aporta +2 ella sola. Un 4 aquí significa que "+
			"una entrada del censo no barrió; un 3, que dos no barrieron",
			rep.Processed, len(sobres))
	}
	if rep.CurrentKeyID != "B" {
		t.Fatalf("Rekey current = %q, want B", rep.CurrentKeyID)
	}
	if len(rep.PendingByKeyID) != 0 {
		t.Fatalf("tras rotar: pendientes = %v, want vacío", rep.PendingByKeyID)
	}

	verificarSobresTrasRotar(t, db, cipherAB, sobres)

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
	// {B}, los CINCO sobres siguen legibles —los DOS de la fila de contacts incluidos—.
	// Es la aserción que de verdad importa: rotar y perder el dato es peor que no rotar.
	verificarSobresLegiblesSinLaKEKVieja(t, db, crypto.NewFieldCipher(mustKP(t, keyringB(), "B")), sobres)
}

// verificarSobresTrasRotar comprueba, sobre a sobre, que la rotación hizo su trabajo y
// SOLO su trabajo: key_id en la current, dato cifrado INTACTO byte a byte (rotar es
// re-envolver la DEK, no re-cifrar el valor) y claro recuperable con la KEK nueva.
//
// Va extraída y NOMBRADA por gocyclo (umbral 15, que aplica también a los tests): con
// los dos bucles inline la función madre quedaba EXACTAMENTE en 15, así que la
// aserción del índice ciego la habría empujado al rojo.
func verificarSobresTrasRotar(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, sobres []sobreCifrado) {
	t.Helper()
	for _, s := range sobres {
		enc, dek, kekID := s.leerFn(t, db)
		if kekID != "B" {
			t.Fatalf("%s: kek_id = %q tras rotar, want B (¿el sobre entró en el censo? recuerda que una "+
				"entrada por TABLA no basta: contacts necesita DOS)", s.sobre, kekID)
		}
		if !bytes.Equal(enc, s.enc) {
			t.Fatalf("%s: el dato cifrado cambió tras rotar (la rotación NO debe re-cifrar, §7)", s.sobre)
		}
		claro, derr := cipher.Decrypt(enc, dek, kekID)
		if derr != nil {
			t.Fatalf("%s: Decrypt tras rotar: %v", s.sobre, derr)
		}
		if claro != s.claro {
			t.Fatalf("%s: el claro cambió tras rotar", s.sobre)
		}
	}
}

// verificarSobresLegiblesSinLaKEKVieja descifra los sobres recibidos con un keyring que
// YA NO tiene la KEK_A: es el retiro seguro del §10.F ejecutado de verdad, no supuesto a
// partir de un mapa de pendientes vacío. El test del censo le pasa los cinco; el del
// contacto sin nombre, solo el suyo.
func verificarSobresLegiblesSinLaKEKVieja(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, sobres []sobreCifrado) {
	t.Helper()
	for _, s := range sobres {
		enc, dek, kekID := s.leerFn(t, db)
		claro, derr := cipher.Decrypt(enc, dek, kekID)
		if derr != nil {
			t.Fatalf("%s: Decrypt con la KEK_A ya retirada: %v", s.sobre, derr)
		}
		if claro != s.claro {
			t.Fatalf("%s: el claro cambió con la KEK_A retirada", s.sobre)
		}
	}
}

// TestPendingByKeyID_AgregaLosCincoSobres es la otra mitad del hallazgo #3: el conteo
// de pendientes tiene que SUMAR las cinco entradas del censo. Es lo que decide si una
// KEK vieja se puede retirar del keyring; con el conteo antiguo (solo el sobre de
// value de contacts) este test devuelve {"A":1} en vez de {"A":5} — y con las tablas
// nuevas vacías de contacts habría devuelto el mapa VACÍO, autorizando un retiro que
// deja sobres ilegibles.
//
// 🔴 EL NÚMERO SE ASSERTA EXACTO, NO `> 0`. Es lo único que hace verificable la
// mutación de abajo: un `> 0` seguiría verde con la quinta entrada fuera del censo, y
// entonces el test no probaría nada de T4.2. Y el mapa cuenta SOBRES, no filas: aquí
// hay 5 sobres pendientes sobre 4 filas, porque la de contacts tiene los dos bajo la
// KEK vieja. Leer un futuro "pending[A] = 30" como "30 contactos" sería el error;
// pueden ser 15 contactos con los dos sobres.
//
// 💥 MUTACIONES QUE LO PONEN ROJO:
//   - LA MUTACIÓN PRESCRITA POR T4.2 — quitar la QUINTA entrada del censo, la de
//     public.contacts/push_name_dek/push_name_kek_id (rekey.go:153-158) ⇒ la primera
//     aserción falla con **pendientes en la KEK A = 4, want 5**, y el mapa completo
//     que imprime el fallo sale {"A":4} con el sobre del nombre todavía bajo la KEK
//     vieja y NADIE contándolo. Ese es el número que autoriza a retirar una KEK:
//     mentirlo por defecto deja todos los nombres ilegibles el día del retiro.
//   - sacar public.fleet_sessions de rekeyTargets (criterio (d) de T4.1) ⇒ el mismo
//     4-en-vez-de-5, con la fila de flota como víctima.
//   - dar un DEFAULT a push_name_kek_id en la 0069 ⇒ ver el test del contacto sin
//     nombre, que es el que protege esa invariante.
func TestPendingByKeyID_AgregaLosCincoSobres(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	kpA := mustKP(t, keyringA(), "A")
	sobres := seedLosCincoSobres(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)

	pending, err := crypto.PendingByKeyID(ctx, db, "B")
	if err != nil {
		t.Fatalf("PendingByKeyID: %v", err)
	}
	if got := pending["A"]; got != len(sobres) {
		t.Fatalf("pendientes en la KEK A = %d, want %d SOBRES (no filas): fleet_sessions.self_pn + "+
			"contacts.value + contacts.push_name + intake_buyer_data.data + tenant_integrations.secret, "+
			"sobre solo 4 filas; mapa completo: %v", got, len(sobres), pending)
	}
	if len(pending) != 1 {
		t.Fatalf("pendientes = %v, want solo la KEK A (la fila sin secreto NO debe contar)", pending)
	}

	// Con A como current no queda nada pendiente: el mapa vacío significa
	// "retirable" y solo debe salir cuando lo es en las CINCO entradas.
	vacio, err := crypto.PendingByKeyID(ctx, db, "A")
	if err != nil {
		t.Fatalf("PendingByKeyID(current=A): %v", err)
	}
	if len(vacio) != 0 {
		t.Fatalf("pendientes con current=A = %v, want vacío", vacio)
	}
}

// sembrarContactoSinNombre siembra UNA fila de public.contacts con el sobre de value
// bajo la KEK vigente del cipher y las TRES columnas del sobre de push_name a NULL —
// un contacto del que WhatsApp nunca reportó nombre, o cuyo backfill aún no ha
// corrido. Devuelve el sobre de value, que es el único que existe en esa fila.
//
// Las tres columnas del nombre se OMITEN del INSERT en vez de escribirles un NULL
// explícito, porque así es exactamente como nacen en producción: la 0069 las creó
// NULLables y SIN DEFAULT a propósito, y omitirlas es lo que ejercita esa ausencia.
func sembrarContactoSinNombre(t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, tenant string) sobreCifrado {
	t.Helper()
	const telefono = "573001110002"
	enc, dek, kid := mustEncrypt(t, cipher, telefono)
	bidx := kp.BlindIndex(tenant, telefono)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id)
		VALUES ($1, 'phone_e164', $2, $3, $4, $5)
	`, tenant, bidx, enc, dek, kid); err != nil {
		t.Fatalf("sembrar contacts sin nombre: %v", err)
	}
	return sobreCifrado{
		sobre: "public.contacts.value_enc (contacto sin nombre)", claro: telefono, enc: enc,
		leerFn: func(t *testing.T, db *sql.DB) ([]byte, []byte, string) {
			return leerCifrado(t, db,
				`SELECT value_enc, value_dek, value_kek_id FROM public.contacts WHERE tenant_id = $1 AND value_bidx = $2`,
				tenant, bidx)
		},
	}
}

// piezasDelSobreDelNombre cuenta cuántas de las TRES columnas del sobre de push_name
// están pobladas en la fila del tenant. Es literalmente la consulta (V4) de la 0069:
// la invariante «las tres o ninguna» no tiene CHECK que la vigile, así que se vigila
// desde aquí. Solo 0 y 3 son valores legítimos.
func piezasDelSobreDelNombre(t *testing.T, db *sql.DB, tenant string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT num_nonnulls(push_name_enc, push_name_dek, push_name_kek_id)
		  FROM public.contacts WHERE tenant_id = $1
	`, tenant).Scan(&n); err != nil {
		t.Fatalf("contar las piezas del sobre del nombre: %v", err)
	}
	return n
}

// TestRekey_ContactoSinNombre_Integration protege la invariante «las tres o ninguna»
// (0069, regla (1) y consulta (V4)) por el lado de la rotación: un contacto SIN nombre
// tiene las tres columnas del sobre de push_name a NULL, y eso NO es un sobre a medias
// — es la ausencia de sobre.
//
// Las tres cosas que asegura, y por qué cada una:
//
//   - PendingByKeyID lo cuenta UNA sola vez, no dos. La QUINTA entrada del censo no
//     lo ve porque `NULL <> 'x'` no es TRUE en SQL, y la PRIMERA sí lo ve porque
//     value_kek_id es NOT NULL DEFAULT '1' desde la 0007. Ninguna fila se cae del
//     censo por completo, y ninguna se cuenta de más.
//   - Tras rotar, las tres columnas del nombre SIGUEN NULL: nadie inventa un sobre
//     vacío. Un sobre inventado sería un push_name_kek_id apuntando a una DEK que no
//     existe, y el ReWrap del día siguiente abortaría el batch entero por fail-safe
//     (§10.J), bloqueando la rotación de TODAS las demás filas detrás de esta.
//   - Su sobre de value SÍ rota. Que el contacto no tenga nombre no lo saca de la
//     rotación del identificador.
//
// 💥 MUTACIONES QUE LO PONEN ROJO:
//   - dar un DEFAULT al push_name_kek_id de la 0069 (copiando el molde de la 0007,
//     que es el error que esa migración advierte en su regla (1)) ⇒ esta fila entraría
//     al barrido de la quinta entrada con un push_name_dek NULL, y el fallo sale por
//     DOS sitios: con el DEFAULT de la 0007 el mapa de pendientes pasa a tener DOS
//     claves —{"A":1, "1":1}— y la aserción len(pending) = 1 falla con 2; y aunque el
//     default fuera la propia "A", Rekey devolvería error al intentar re-envolver una
//     DEK que no existe (ReWrap sobre un push_name_dek NULL), con el batch entero en
//     rollback por fail-safe §10.J.
//   - añadir a la quinta entrada un UPDATE incondicional (sin el filtro por kek_id)
//     ⇒ la aserción de "las tres siguen NULL" pasaría de 0 a 3 piezas pobladas.
//   - quitar la quinta entrada del censo NO pone rojo ESTE test, y es correcto: aquí
//     no hay sobre de nombre que rotar. Esa mutación la cazan los otros dos.
func TestRekey_ContactoSinNombre_Integration(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeCifradas(t, db)
	tenant := seedTenant(t, db)

	kpA := mustKP(t, keyringA(), "A")
	sobreDeValue := sembrarContactoSinNombre(t, db, crypto.NewFieldCipher(kpA), kpA, tenant)

	if piezas := piezasDelSobreDelNombre(t, db, tenant); piezas != 0 {
		t.Fatalf("el contacto sin nombre nace con %d de las 3 piezas del sobre pobladas, want 0: la 0069 "+
			"creó las tres columnas NULLables y SIN default a propósito", piezas)
	}

	// (1) UNA sola vez, no dos: el sobre inexistente NO es pendiente.
	pending, err := crypto.PendingByKeyID(ctx, db, "B")
	if err != nil {
		t.Fatalf("PendingByKeyID: %v", err)
	}
	if got := pending["A"]; got != 1 {
		t.Fatalf("pendientes en la KEK A = %d, want 1 (SOLO el sobre de value). Un 2 significa que la fila "+
			"se está contando también por el sobre del nombre, que NO EXISTE: o alguien puso un DEFAULT en "+
			"push_name_kek_id, o el censo dejó de apoyarse en que `NULL <> valor` no es TRUE en SQL; mapa "+
			"completo: %v", got, pending)
	}
	if len(pending) != 1 {
		t.Fatalf("pendientes = %v, want solo la KEK A", pending)
	}

	// t1: rotar a B.
	kpAB := mustKP(t, keyringAB(), "B")
	cipherAB := crypto.NewFieldCipher(kpAB)
	rep, err := crypto.Rekey(ctx, db, cipherAB, kpAB, 10)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != 1 {
		t.Fatalf("Rekey processed = %d, want 1 SOBRE (el de value). Un 2 significaría que la rotación se "+
			"inventó un sobre de nombre donde no había ninguno", rep.Processed)
	}
	if len(rep.PendingByKeyID) != 0 {
		t.Fatalf("tras rotar: pendientes = %v, want vacío", rep.PendingByKeyID)
	}

	// (2) las tres del nombre siguen NULL: nadie inventó un sobre vacío.
	if piezas := piezasDelSobreDelNombre(t, db, tenant); piezas != 0 {
		t.Fatalf("tras rotar, el contacto sin nombre tiene %d de las 3 piezas del sobre pobladas, want 0. "+
			"Un sobre inventado por la rotación apuntaría a una DEK inexistente, y el ReWrap de la siguiente "+
			"rotación abortaría el batch entero por fail-safe (§10.J), bloqueando a todas las filas detrás",
			piezas)
	}

	// (3) su sobre de value SÍ rotó, y sigue legible con la KEK_A ya retirada.
	verificarSobresTrasRotar(t, db, cipherAB, []sobreCifrado{sobreDeValue})
	verificarSobresLegiblesSinLaKEKVieja(t, db,
		crypto.NewFieldCipher(mustKP(t, keyringB(), "B")), []sobreCifrado{sobreDeValue})
}
