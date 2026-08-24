// postgres_integration_test.go verifica el store real contra Postgres. Custodia
// el criterio de T0.3 que NINGÚN doble en memoria puede demostrar: «PUT válido ⇒
// fila con BYTEA cifrado (verificar por SQL que no hay clave en claro)». Un fake
// guarda un string; solo la base enseña un blob.
//
// Mismo contrato que el resto de los *_integration_test.go del repo (sin
// WAPP_TEST_DB_DSN se salta) y mismo criterio que
// integrations/postgres_integration_test.go:1-5 y buyerdata_integration_test.go.
package tenantllm_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

// La credencial de las pruebas lleva el prefijo público real de Anthropic a
// propósito: es la cadena que el barrido de fugas busca, y buscar una con la
// forma de la de verdad es lo que hace al barrido creíble.
const claveDePrueba = "sk-ant-api03-CLAVE-FALSA-DE-PRUEBA-0044" // #nosec G101 -- clave de prueba inventada, no una credencial real

const tenantDePrueba = "t-llm-integracion"

// openTestDB abre la BD de integración y aplica el esquema — mismo contrato que
// el resto de los *_integration_test.go del repo: sin DSN se salta.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
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

// Material de clave de prueba: mismo patrón que buyerdata_integration_test.go y
// que integrations/postgres_integration_test.go.
const (
	kekDePruebaB64   = "ERERERERERERERERERERERERERERERERERERERERERE="
	indexDePruebaB64 = "RERERERERERERERERERERERERERERERERERERERERES="
	kekIDDePrueba    = "test-kek-1"
)

func cipherDePrueba(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: kekIDDePrueba + ":" + kekDePruebaB64,
		CurrentID:  kekIDDePrueba,
		IndexB64:   indexDePruebaB64,
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

func wipeTenantLLM(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM public.tenant_llm`); err != nil {
		t.Fatalf("limpiando public.tenant_llm: %v", err)
	}
}

// nuevoStore arma el store y deja la tabla vacía. Se extrae porque lo hacen
// todos los tests de este fichero y porque gocyclo mide también los tests.
func nuevoStore(t *testing.T) (*tenantllm.Postgres, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	wipeTenantLLM(t, db)
	return tenantllm.NewPostgres(db, cipherDePrueba(t)), db
}

// cfgDePrueba es la configuración de la VÍA API, que es la que exige credencial.
// Lleva `Via` explícita desde T1.5-2: el store rechaza una Config sin vía, y eso
// es deliberado (una fila sin vía es una fila que nadie sabe leer).
func cfgDePrueba() tenantllm.Config {
	return tenantllm.Config{
		TenantID: tenantDePrueba,
		Via:      tenantllm.ViaAPI,
		Provider: tenantllm.ProviderAnthropic,
		Model:    "claude-sonnet-4-5",
	}
}

// cfgLocalDePrueba es la otra vía: sin proveedor, sin modelo y sin credencial.
// Es una configuración COMPLETA, no una a medias (0073).
func cfgLocalDePrueba() tenantllm.Config {
	return tenantllm.Config{
		TenantID: tenantDePrueba,
		Via:      tenantllm.ViaLocal,
	}
}

// ejecutar corre una sentencia y falla el test si no. Mismo estilo que
// wipeTenantLLM (el ctx se arma dentro): así los helpers de este fichero tienen
// todos la misma firma y ninguno arrastra un context.Context por delante del
// *testing.T.
func ejecutar(t *testing.T, db *sql.DB, sentencia string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), sentencia, args...); err != nil {
		t.Fatalf("ejecutando %.60q…: %v", sentencia, err)
	}
}

// TestAPIKey_CifradaEnReposo es EL criterio de T0.3 que exige base de datos: un
// SELECT directo sobre las tres columnas del sobre no muestra la clave en claro,
// y el store la recupera con la llave.
//
// 🔬 MUTACIÓN QUE LO PONE ROJO: en Postgres.Upsert, pasar `apiKey` (el string)
// donde va `enc` y saltarse el `p.cipher.Encrypt` ⇒ el `strings.Contains` de
// abajo encuentra la clave dentro del BYTEA.
func TestAPIKey_CifradaEnReposo(t *testing.T) {
	store, db := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var enc, dek []byte
	var kekID string
	if err := db.QueryRowContext(ctx, `
		SELECT api_key_enc, api_key_dek, api_key_kek_id FROM public.tenant_llm WHERE tenant_id = $1
	`, tenantDePrueba).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leer por SQL directo: %v", err)
	}
	if strings.Contains(string(enc), claveDePrueba) {
		t.Fatalf("FUGA: api_key_enc contiene la clave en claro (%d bytes)", len(enc))
	}
	// El prefijo además del valor entero: un cifrado que solo ofuscara la cola
	// seguiría siendo una fuga y pasaría la comprobación de arriba.
	if strings.Contains(string(enc), "sk-ant-") {
		t.Fatalf("FUGA: api_key_enc contiene el prefijo de la clave (%d bytes)", len(enc))
	}
	if len(enc) == 0 || len(dek) == 0 || kekID == "" {
		t.Fatalf("la fila quedó sin material cifrado: enc=%dB dek=%dB kek_id=%q", len(enc), len(dek), kekID)
	}

	got, err := store.APIKey(ctx, tenantDePrueba)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != claveDePrueba {
		t.Fatalf("APIKey devolvió una clave distinta de la guardada")
	}
}

// TestGet_NoDevuelveLaClave: el puerto que consume la capa HTTP no tiene por
// dónde filtrarla — Config no la lleva, y HasAPIKey dice que existe.
//
// 🔬 MUTACIÓN: añadir un campo `APIKey string` a tenantllm.Config y rellenarlo en
// Get ⇒ este test no compilaría el assert, así que además hay que mirar el
// barrido de fugas de publicapi/tenantllm_test.go, que sí se pondría rojo.
func TestGet_NoDevuelveLaClave(t *testing.T) {
	store, _ := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	cfg, found, err := store.Get(ctx, tenantDePrueba)
	if err != nil || !found {
		t.Fatalf("Get devolvió (found=%v, err=%v), quiero (true, nil)", found, err)
	}
	if !cfg.HasAPIKey {
		t.Fatalf("HasAPIKey=false con clave guardada")
	}
	if cfg.ConsentedAt.IsZero() {
		t.Fatalf("consented_at quedó a cero: la columna es NOT NULL, algo no se escribió")
	}
}

// TestUpsert_ReemplazaLaClaveYRefrescaElConsentimiento: la decisión que separa
// esta tabla del molde de tenant_integrations (allí el secreto vacío se
// conserva; aquí cada PUT trae clave y la sustituye). Y `created_at` NO se pisa.
//
// 🔬 MUTACIÓN (a): quitar `api_key_enc = EXCLUDED.api_key_enc` del DO UPDATE ⇒ la
// clave leída tras el segundo Upsert seguiría siendo la primera.
// 🔬 MUTACIÓN (b): añadir `created_at = now()` al DO UPDATE ⇒ el assert de
// created_at se pone rojo.
func TestUpsert_ReemplazaLaClaveYRefrescaElConsentimiento(t *testing.T) {
	store, _ := nuevoStore(t)
	ctx := context.Background()

	primerConsent := time.Now().UTC().Add(-2 * time.Hour)
	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, primerConsent); err != nil {
		t.Fatalf("primer Upsert: %v", err)
	}
	antes, _, err := store.Get(ctx, tenantDePrueba)
	if err != nil {
		t.Fatalf("Get tras el primer Upsert: %v", err)
	}

	const claveRotada = "sk-ant-api03-CLAVE-FALSA-ROTADA-0044" // #nosec G101 -- clave de prueba inventada
	segundoConsent := time.Now().UTC()
	cfg := cfgDePrueba()
	cfg.Model = "claude-opus-4-1"
	if err := store.Upsert(ctx, cfg, claveRotada, segundoConsent); err != nil {
		t.Fatalf("segundo Upsert: %v", err)
	}

	got, err := store.APIKey(ctx, tenantDePrueba)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != claveRotada {
		t.Fatalf("la clave no se reemplazó en el upsert")
	}
	después, _, err := store.Get(ctx, tenantDePrueba)
	if err != nil {
		t.Fatalf("Get tras el segundo Upsert: %v", err)
	}
	if !después.ConsentedAt.After(antes.ConsentedAt) {
		t.Fatalf("consented_at no se refrescó: %v → %v", antes.ConsentedAt, después.ConsentedAt)
	}
	if !después.CreatedAt.Equal(antes.CreatedAt) {
		t.Fatalf("created_at se pisó en el upsert: %v → %v", antes.CreatedAt, después.CreatedAt)
	}
	if después.Model != "claude-opus-4-1" {
		t.Fatalf("model=%q, quiero claude-opus-4-1", después.Model)
	}
}

// TestUpsert_SinClaveNoEscribeFila: la guarda de programación del store. La API
// ya rechaza el cuerpo sin clave con un 400; esto afirma que aunque alguien
// llame al store directamente, la fila con sobre de no-valor que la 0071 declara
// imposible NO se crea.
//
// 🔧 DESDE T1.5-2 ESTO SOLO VALE PARA LA VÍA API, que es lo que `cfgDePrueba()`
// devuelve: en la vía local la clave vacía es LO NORMAL y la fila se escribe (ver
// TestUpsert_ViaLocalEsUnaFilaSinSobre). La guarda no se relajó: se mudó dentro
// del `if cfg.Via == ViaAPI`.
//
// 🔬 MUTACIÓN: quitar la guarda `if apiKey == ""` de Postgres.Upsert ⇒ se
// crearía la fila (el envelope cifra la cadena vacía sin quejarse).
func TestUpsert_SinClaveNoEscribeFila(t *testing.T) {
	store, _ := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgDePrueba(), "", time.Now().UTC()); err == nil {
		t.Fatalf("Upsert sin clave devolvió nil, quiero error")
	}
	if _, found, err := store.Get(ctx, tenantDePrueba); err != nil || found {
		t.Fatalf("Get devolvió (found=%v, err=%v), quiero (false, nil): el upsert fallido dejó fila", found, err)
	}
}

// TestDelete_RevocaYEsIdempotente: borrar la fila se lleva credencial y
// consentimiento de una vez, y borrar lo que no hay no es un error.
//
// 🔬 MUTACIÓN: cambiar el DELETE por un `UPDATE ... SET api_key_enc = NULL` ⇒
// reventaría contra `tenant_llm_via_api_completa_check`, que es la protección
// que la 0073 puso en el sitio donde la 0071 tenía tres NOT NULL. (El NOT NULL
// ya no está: la fila de la vía local existe sin sobre. La invariante no se
// perdió, cambió de forma — y esta mutación lo demuestra igual de bien.)
func TestDelete_RevocaYEsIdempotente(t *testing.T) {
	store, _ := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Delete(ctx, tenantDePrueba); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, err := store.Get(ctx, tenantDePrueba); err != nil || found {
		t.Fatalf("Get tras el Delete devolvió (found=%v, err=%v), quiero (false, nil)", found, err)
	}
	if _, err := store.APIKey(ctx, tenantDePrueba); !errors.Is(err, tenantllm.ErrNotConfigured) {
		t.Fatalf("APIKey tras el Delete devolvió %v, quiero ErrNotConfigured", err)
	}
	// Idempotente.
	if err := store.Delete(ctx, tenantDePrueba); err != nil {
		t.Fatalf("segundo Delete: %v", err)
	}
}

// TestAPIKey_SinFilaEsErrNotConfigured: el sentinel que el pipeline necesita para
// responder 422 `llm_credentials_missing` (design §8.1) en vez de 500. Sin este
// test, un cambio a `return "", nil` pasaría desapercibido y el pipeline
// llamaría al proveedor con una clave vacía.
//
// 🔬 MUTACIÓN: devolver `nil` en vez de ErrNotConfigured en la rama
// sql.ErrNoRows de Postgres.APIKey.
func TestAPIKey_SinFilaEsErrNotConfigured(t *testing.T) {
	store, _ := nuevoStore(t)
	if _, err := store.APIKey(context.Background(), "t-que-no-existe"); !errors.Is(err, tenantllm.ErrNotConfigured) {
		t.Fatalf("APIKey de un tenant sin fila devolvió %v, quiero ErrNotConfigured", err)
	}
}

// 🔴 LA ROTACIÓN DE KEK DE ESTA TABLA NO SE PRUEBA AQUÍ, Y NO ES UN OLVIDO. La
// entrada `public.tenant_llm` del censo (`rekeyTargets`, crypto/rekey.go) la
// custodia crypto/rekey_integration_test.go, que es donde vive el harness que
// puede hacerlo bien: `crypto.Rekey` barre TODAS las tablas del censo a la vez,
// así que un test que lo llamara desde este paquete fallaría en cuanto otra
// tabla tuviera una fila envuelta por una KEK que no esté en el keyring del test
// —fail-safe §10.J— y eso depende de qué otro paquete escribió antes en la misma
// base. Por eso el sexto sobre se siembra allí, con los otros cinco y tras el
// mismo `wipeCifradas`.

// TestINV7_CadaTenantSoloVeLoSuyo: el aislamiento en el store, no solo en el
// handler. La PK es tenant_id, así que la firma ya lo garantiza; este test lo
// AFIRMA, que es distinto.
//
// 🔬 MUTACIÓN: quitar el `WHERE tenant_id = $1` del SELECT de Get (dejando un
// LIMIT 1) ⇒ el segundo tenant vería la fila del primero.
func TestINV7_CadaTenantSoloVeLoSuyo(t *testing.T) {
	store, _ := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, found, err := store.Get(ctx, "t-otro-tenant"); err != nil || found {
		t.Fatalf("Get de otro tenant devolvió (found=%v, err=%v), quiero (false, nil)", found, err)
	}
}

// ============================================================
// Plan 044 · Ola 1.5 · T1.5-2 — EL EJE `via` (migración 0073, REQ-33)
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO: se escribieron en un entorno sin Go
// y sin Postgres. Cada uno lleva su MUTACIÓN, elegida para que COMPILE — una
// mutación que no compila no prueba nada.
// ============================================================

// rutaMigración0073 apunta al fichero REAL de la migración. Los tests del
// backfill lo LEEN y lo EJECUTAN en vez de repetir su SQL: un test que copiara la
// sentencia probaría la copia, y la copia sigue verde el día que alguien edite el
// original. El runner embebe el directorio con go:embed y `structureFS` es
// privado, así que la única forma de alcanzar el fichero desde otro paquete es
// por disco — y funciona porque `go test` corre con el cwd del paquete.
const rutaMigración0073 = "../platform/storage/postgres/migrations/structure/0073_tenant_llm_via.sql"

// TestUpsert_ViaLocalEsUnaFilaSinSobre: la forma que la 0071 declaraba imposible
// y que la 0073 hace legítima. Un tenant en la vía local tiene fila, tiene vía y
// NO tiene proveedor, modelo, credencial ni consentimiento — y eso no es una
// configuración a medias, es la configuración entera de quien ejecuta en su
// propio fierro.
//
// Se comprueba por SQL y no solo por Get(), porque lo que se afirma es la forma
// de la FILA: un Get que devolviera vacíos podría estar ocultando columnas
// llenas.
//
// 🔬 MUTACIÓN: en Postgres.Upsert, sacar el bloque del sobre del `if cfg.Via ==
// ViaAPI` y ejecutarlo siempre ⇒ la vía local reventaría al cifrar la cadena
// vacía o dejaría sobre donde no debe haberlo.
func TestUpsert_ViaLocalEsUnaFilaSinSobre(t *testing.T) {
	store, db := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgLocalDePrueba(), "", time.Time{}); err != nil {
		t.Fatalf("Upsert de la vía local: %v (esa vía NO exige credencial, REQ-33)", err)
	}

	var via string
	var nulos int
	if err := db.QueryRowContext(ctx, `
		SELECT via, num_nulls(provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		FROM public.tenant_llm WHERE tenant_id = $1
	`, tenantDePrueba).Scan(&via, &nulos); err != nil {
		t.Fatalf("leyendo la fila local: %v", err)
	}
	if via != tenantllm.ViaLocal {
		t.Fatalf("via=%q, quiero %q", via, tenantllm.ViaLocal)
	}
	if nulos != 6 {
		t.Fatalf("columnas NULL del eje api = %d, quiero 6: la vía local dejó rastro de la vía API", nulos)
	}

	cfg, found, err := store.Get(ctx, tenantDePrueba)
	if err != nil || !found {
		t.Fatalf("Get devolvió (found=%v, err=%v), quiero (true, nil): la fila local existe", found, err)
	}
	if cfg.Via != tenantllm.ViaLocal || cfg.HasAPIKey || cfg.Provider != "" || !cfg.ConsentedAt.IsZero() {
		t.Fatalf("Get de la fila local devolvió %+v: los NULL tienen que llegar como valor cero", cfg)
	}
	// Y la puerta de la credencial dice lo mismo que diría sin fila.
	if _, err := store.APIKey(ctx, tenantDePrueba); !errors.Is(err, tenantllm.ErrNotConfigured) {
		t.Fatalf("APIKey sobre una fila local devolvió %v, quiero ErrNotConfigured", err)
	}
}

// TestUpsert_DeApiALocalRetiraLaCredencial: «una sola vía activa» (REQ-33) escrito
// en la tabla. Cambiar de vía NO deja la credencial dormida esperando el regreso:
// el PUT es la foto entera, y la foto de la vía local no tiene sobre.
//
// 🔬 MUTACIÓN: quitar `api_key_enc = EXCLUDED.api_key_enc` (o cualquiera de las
// tres del sobre) del DO UPDATE ⇒ la credencial sobreviviría al cambio de vía y
// este test se pone rojo. 🔧 Desde `tenant_llm_local_sin_credencial_check`
// (0073 · f.4) se pone rojo ANTES y más fuerte: con `_enc` sin pisar, el propio
// UPDATE viola la constraint y el Upsert devuelve error, así que el test muere en
// «Upsert cambiando a la vía local» en vez de en el aserto. Los dos rojos dicen
// lo mismo; se anota para que nadie lea el mensaje y busque el defecto donde no
// está.
func TestUpsert_DeApiALocalRetiraLaCredencial(t *testing.T) {
	store, db := nuevoStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert de la vía API: %v", err)
	}
	if err := store.Upsert(ctx, cfgLocalDePrueba(), "", time.Time{}); err != nil {
		t.Fatalf("Upsert cambiando a la vía local: %v", err)
	}

	var quedaAlgo bool
	if err := db.QueryRowContext(ctx, `
		SELECT api_key_enc IS NOT NULL OR api_key_dek IS NOT NULL
		    OR api_key_kek_id IS NOT NULL OR consented_at IS NOT NULL
		FROM public.tenant_llm WHERE tenant_id = $1
	`, tenantDePrueba).Scan(&quedaAlgo); err != nil {
		t.Fatalf("leyendo la fila tras el cambio de vía: %v", err)
	}
	if quedaAlgo {
		t.Fatalf("tras pasar a la vía local quedó credencial o consentimiento vivos: una vía dormida es la que REQ-33 prohíbe")
	}
}

// TestUpsert_ViaFueraDelVocabularioNoEscribe: la guarda de programación del eje.
// La API valida antes (400 invalid_via); esto afirma que llamar al store directo
// con una vía inventada —o vacía— no crea una fila que nadie sabría leer, y que
// el rechazo lo da Go y no el CHECK (un CHECK violado sería un 500).
//
// 🔬 MUTACIÓN: quitar el `if !ValidVia(cfg.Via)` de Postgres.Upsert ⇒ el INSERT
// llegaría a la base y fallaría contra tenant_llm_via_check.
//
// 🔧 Y POR ESO SE AFIRMA DE QUÉ CAPA VIENE EL RECHAZO, y no solo que lo haya
// (corrección del code review, 2026-08-23). Los tres valores chocan igual contra
// el CHECK de la base, así que un `err != nil` pelado seguía VERDE con la guarda
// de Go borrada: el test decía probar la capa y no la miraba. Se mira por el
// mensaje —el error de Go es una cadena, no un centinela exportable— con las dos
// mitades: que dice lo que dice el store, Y que NO nombra la constraint, que es
// lo que aparecería si el rechazo hubiera bajado a Postgres.
func TestUpsert_ViaFueraDelVocabularioNoEscribe(t *testing.T) {
	store, _ := nuevoStore(t)
	ctx := context.Background()

	for _, vía := range []string{"", "remota", "API"} {
		cfg := cfgDePrueba()
		cfg.Via = vía
		err := store.Upsert(ctx, cfg, claveDePrueba, time.Now().UTC())
		if err == nil {
			t.Fatalf("Upsert con via=%q devolvió nil, quiero error", vía)
		}
		if !strings.Contains(err.Error(), "fuera del vocabulario") {
			t.Errorf("via=%q rechazada con %v; se esperaba la guarda de Go («fuera del vocabulario»)", vía, err)
		}
		if strings.Contains(err.Error(), "tenant_llm_via_check") {
			t.Errorf("via=%q la rechazó POSTGRES (%v): la guarda de programación de Upsert no está, "+
				"y un CHECK violado sale por la API como 500 en vez de como el 400 que le toca", vía, err)
		}
		if _, found, err := store.Get(ctx, tenantDePrueba); err != nil || found {
			t.Fatalf("via=%q dejó fila (found=%v, err=%v)", vía, found, err)
		}
	}
}

// TestCheck_LaViaApiIncompletaLaRechazaPostgres: la invariante que sustituye a
// los seis NOT NULL de la 0071. NO se prueba por el store —el store no sabe
// escribir eso— sino por SQL crudo, que es la única forma de afirmar que la
// protección está en la TABLA y no en el código que hoy la respeta.
//
// Es el mismo argumento por el que este fichero existe: un fake no puede
// demostrar un CHECK.
//
// 🔬 MUTACIÓN: borrar el bloque de `tenant_llm_via_api_completa_check` de la
// 0073 ⇒ los tres primeros INSERT pasarían y la base admitiría filas `api` a
// medias. Borrar el de `..._sobre_completo_check` o el de
// `..._local_sin_credencial_check` deja pasar el suyo.
//
// 🔧 CADA CASO AFIRMA QUÉ CONSTRAINT LO RECHAZÓ, y no solo que algo lo hiciera
// (corrección del code review, 2026-08-23, patrón de
// degradation/postgres_integration_test.go:263). Sin el nombre, un `err != nil`
// da por buena una fila rechazada por OTRA razón —y eso pasaba de verdad aquí: el
// caso «sobre a medias» estaba escrito con `via='local'` + `api_key_enc`, que
// desde f.4 lo rechaza `tenant_llm_local_sin_credencial_check` y NO el
// `..._sobre_completo_check` que el nombre del caso prometía. Se reescribió con
// las dos columnas que dejan el fallo aislado (DEK y kek_id sin `_enc`).
//
// 🔴 CADA FILA VIOLA UN SOLO CHECK, a propósito: con dos violados a la vez el
// nombre que Postgres devuelve depende del orden en que los evalúe, y el test
// sería intermitente por una razón que no tiene nada que ver con lo que prueba.
func TestCheck_LaViaApiIncompletaLaRechazaPostgres(t *testing.T) {
	_, db := nuevoStore(t)
	ctx := context.Background()

	casos := map[string]struct {
		sentencia  string
		constraint string
	}{
		"sin sobre": {
			`INSERT INTO public.tenant_llm (tenant_id, via, provider, model, consented_at)
		     VALUES ('t-check-1', 'api', 'anthropic', 'm', now())`,
			"tenant_llm_via_api_completa_check",
		},
		"sin consentimiento": {
			`INSERT INTO public.tenant_llm
		      (tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id)
		     VALUES ('t-check-2', 'api', 'anthropic', 'm', '\x01'::bytea, '\x02'::bytea, 'k')`,
			"tenant_llm_via_api_completa_check",
		},
		"sin proveedor": {
			`INSERT INTO public.tenant_llm
		      (tenant_id, via, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		     VALUES ('t-check-3', 'api', 'm', '\x01'::bytea, '\x02'::bytea, 'k', now())`,
			"tenant_llm_via_api_completa_check",
		},
		// DEK y kek_id SIN `_enc`: viola «las tres o ninguna» y nada más. Con
		// `_enc` puesto violaría además el CHECK de la vía local (la fila es
		// 'local'), y el nombre devuelto dejaría de ser predecible.
		"sobre a medias": {
			`INSERT INTO public.tenant_llm (tenant_id, via, api_key_dek, api_key_kek_id)
		     VALUES ('t-check-4', 'local', '\x02'::bytea, 'k')`,
			"tenant_llm_sobre_completo_check",
		},
		// La vía que declara no llamar a nadie NO puede arrastrar una credencial
		// viva (f.4). El sobre va ENTERO para que el de arriba no se dispare.
		"local con credencial": {
			`INSERT INTO public.tenant_llm
		      (tenant_id, via, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		     VALUES ('t-check-5', 'local', '\x01'::bytea, '\x02'::bytea, 'k', now())`,
			"tenant_llm_local_sin_credencial_check",
		},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := db.ExecContext(ctx, c.sentencia)
			if err == nil {
				t.Fatalf("Postgres aceptó una fila que los CHECK de la 0073 declaran imposible")
			}
			if !strings.Contains(err.Error(), c.constraint) {
				t.Errorf("la base rechazó por %v, se esperaba %s", err, c.constraint)
			}
		})
	}
}

// ============================================================
// EL BACKFILL DE LA 0073 — SOBRE ESTADO PREVIO FABRICADO
// ============================================================

// TestBackfill0073_RepartoSobreEstadoPrevioFabricado es EL criterio de T1.5-2 que
// no se puede aprobar de ninguna otra forma.
//
// 🔴 POR QUÉ NO SIRVE CORRER ESTO CONTRA UNA BASE RECIÉN MIGRADA: una base recién
// migrada no tiene filas anteriores al backfill, así que el UPDATE toca CERO
// filas y el criterio sale verde POR VACÍO — verde sin haber probado nada, que es
// la peor clase de verde. Este test FABRICA el estado previo: suelta el NOT NULL
// de `via` (que es exactamente como estaba la tabla un segundo antes de que la
// 0073 existiera), siembra las cuatro filas que importan, y APLICA EL FICHERO
// REAL de la migración.
//
// Las cuatro filas son los cuatro desenlaces de la regla, y ninguna sobra:
//
//	las SEIS del eje api                 ⇒ 'api'    (la excepción: no se le apaga
//	                                                 el servicio a quien paga)
//	nada                                 ⇒ 'local'  (el default de REQ-33)
//	credencial SIN consentimiento        ⇒ 'local'  (tener la clave no es haber
//	                                                 autorizado la salida del texto)
//	vía YA elegida a mano                ⇒ INTACTA  (la guarda `WHERE via IS NULL`:
//	                                                 rellenar ≠ pisar)
//
// 🔬 MUTACIÓN (a): quitar `WHERE via IS NULL` del UPDATE de la 0073.
// 🔧 ⚠️ ESTA MUTACIÓN YA NO PONE ROJO NADA, Y SE DEJA ESCRITO EN VEZ DE FINGIRLO
// (code review 2026-08-23). La ponía roja la fila «vía elegida a mano + CREDENCIAL»
// —la única que, sin la guarda, el CASE habría reescrito a 'api'—, y desde
// `tenant_llm_local_sin_credencial_check` (0073 · f.4) esa fila NO SE PUEDE
// INSERTAR: una vía local con credencial dejó de ser un estado representable. No
// queda ninguna otra: toda fila 'api' está completa (f.2) y ninguna 'local' puede
// cumplir la condición del CASE, así que el UPDATE sin guarda deja a las dos
// exactamente donde estaban. La guarda SE QUEDA —es lo que hace idempotente el
// UPDATE y lo que dice la intención—, pero hoy su protección es redundante con
// una constraint, y quien la retire no verá fallar este test. Es un precio del
// CHECK de f.4, aceptado a ojos abiertos.
// 🔬 MUTACIÓN (b): quitar `AND consented_at IS NOT NULL` de la condición.
// 🔧 NO SE PUEDE VER DESDE AQUÍ, y la promesa anterior («la tercera fila saldría
// 'api' y wApp mandaría texto a un tercero por una autorización que nadie dio»)
// era FALSA. La retiró el barrido del CLI (2026-08-23) al ejecutar este test por
// primera vez contra Postgres real: `t-bf-sin-consent` LLEVABA EL SOBRE ENTERO, y
// una fila así cae en el mismo agujero que el ⚠️ de la 0073 ya nombra para «sobre
// entero y consentimiento pero sin proveedor» — el backfill la manda a 'local'
// (le falta el consentimiento) y ahí la mata `tenant_llm_local_sin_credencial_
// check` (f.4). No es que la mutación pusiera el test rojo: es que el test salía
// ROJO SIN MUTACIÓN, con la migración abortando en su propio ADD CONSTRAINT.
// Por eso la fila conserva proveedor, modelo y la ausencia de consentimiento,
// pero PIERDE el sobre, que era lo único que la hacía inexpresable.
// EL PRECIO, DICHO ENTERO: así fabricada, la fila cae a 'local' por DOS carencias
// a la vez (sin sobre y sin consentimiento) y este test NO puede aislar cuál de
// las dos mandó. La garantía de que el consentimiento decide POR SÍ SOLO —«sin
// autorización no sale texto hacia un tercero»— no la sostiene este test; la
// sostiene TestBackfill0073_LaCondicionAisladaExigeLasSeis, más abajo, que
// ejercita el CASE del backfill fuera del alcance de los CHECK, que es el único
// sitio donde esa fila puede existir.
// 🔬 MUTACIÓN (c): añadir `DEFAULT 'local'` al `ADD COLUMN` de la 0073 ⇒ el
// `ALTER TABLE … ADD COLUMN IF NOT EXISTS via` es NO-OP sobre esta tabla (la
// columna ya existe: este test fabrica el estado previo aflojándola, no
// borrándola), así que el default NO se aplicaría y el test seguiría verde.
// 🔧 SE DICE ASÍ, y no como «pone rojo», porque lo segundo era falso. Para ver esa
// mutación hay que ejercitar la migración sobre una tabla SIN la columna, que es
// lo que hace TestBackfill0073_ColumnaAusenteNoNaceConDefault (abajo).
// 🔬 MUTACIÓN (d): quitar `AND provider IS NOT NULL` (o `model`) de la condición.
// TAMPOCO SE PUEDE VER DESDE AQUÍ, y por una razón que merece leerse: la única
// fila que distingue la condición de cuatro columnas de la de seis es «sobre
// entero + consentimiento y SIN proveedor», y esa fila es INEXPRESABLE tras la
// migración en las dos direcciones —como 'api' la rechaza f.2, como 'local' la
// rechaza f.4—. Se puede INSERTAR con `via` NULL, pero entonces la migración
// muere aplique la rama que aplique, así que el test no distinguiría la mutación
// de su ausencia: las dos matarían el arranque, solo que nombrando otra
// constraint. Por eso no hay quinta fila fabricada aquí.
func TestBackfill0073_RepartoSobreEstadoPrevioFabricado(t *testing.T) {
	_, db := nuevoStore(t)
	ctx := context.Background()

	sqlMigración, err := os.ReadFile(rutaMigración0073)
	if err != nil {
		t.Fatalf("leyendo %s: %v (¿se movió el fichero de la migración?)", rutaMigración0073, err)
	}

	// --- Fabricar el estado ANTERIOR a la 0073 -------------------------------
	// Soltar el NOT NULL y el DEFAULT deja la columna como estaba justo antes de
	// que esta migración corriera por primera vez. El Cleanup vuelve a aplicar el
	// fichero: si este test muere a medias, la tabla no se queda floja para los
	// demás paquetes que comparten la base de integración.
	t.Cleanup(func() {
		wipeTenantLLM(t, db)
		if _, cerr := db.ExecContext(context.Background(), string(sqlMigración)); cerr != nil {
			t.Fatalf("restaurando la 0073 tras el test: %v", cerr)
		}
	})
	ejecutar(t, db, `ALTER TABLE public.tenant_llm ALTER COLUMN via DROP NOT NULL`)
	ejecutar(t, db, `ALTER TABLE public.tenant_llm ALTER COLUMN via DROP DEFAULT`)
	ejecutar(t, db, `UPDATE public.tenant_llm SET via = NULL`)

	// Los bytes del sobre son basura a propósito: este test NO descifra nada, mira
	// el reparto. Lo que importa de ellos es que NO son NULL.
	//
	// 🔧 `t-bf-ya-elegida` PERDIÓ SU SOBRE (code review 2026-08-23): antes llevaba
	// credencial, y desde `tenant_llm_local_sin_credencial_check` (0073 · f.4) una
	// fila 'local' con `api_key_enc` NO SE PUEDE INSERTAR — este `ejecutar` moría
	// en la constraint antes de llegar a la migración. Conserva proveedor, modelo
	// y consentimiento (legales en una fila local: lo único prohibido es la
	// credencial viva) y sigue siendo la fila «ya elegida a mano», que es lo que
	// su nombre promete. Lo que ya no puede demostrar está dicho en la mutación (a).
	ejecutar(t, db, `
		INSERT INTO public.tenant_llm
			(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		VALUES
			('t-bf-api',        NULL, 'anthropic', 'm', '\x01'::bytea, '\x02'::bytea, 'k-vieja', now()),
			('t-bf-vacia',      NULL,  NULL,       NULL, NULL,          NULL,          NULL,      NULL),
			('t-bf-sin-consent',NULL, 'anthropic', 'm', NULL,          NULL,          NULL,      NULL),
			('t-bf-ya-elegida', 'local', 'anthropic', 'm', NULL,        NULL,          NULL,      now())
	`)

	// --- Aplicar la migración REAL -------------------------------------------
	if _, err := db.ExecContext(ctx, string(sqlMigración)); err != nil {
		t.Fatalf("aplicando la 0073 sobre el estado fabricado: %v", err)
	}

	// --- El reparto -----------------------------------------------------------
	quiero := map[string]string{
		"t-bf-api":         tenantllm.ViaAPI,
		"t-bf-vacia":       tenantllm.ViaLocal,
		"t-bf-sin-consent": tenantllm.ViaLocal,
		"t-bf-ya-elegida":  tenantllm.ViaLocal,
	}
	for tenant, esperada := range quiero {
		var via string
		if err := db.QueryRowContext(ctx,
			`SELECT via FROM public.tenant_llm WHERE tenant_id = $1`, tenant).Scan(&via); err != nil {
			t.Fatalf("leyendo la vía de %s: %v", tenant, err)
		}
		if via != esperada {
			t.Fatalf("%s quedó en via=%q, quiero %q", tenant, via, esperada)
		}
	}

	// Y no quedó ninguna sin vía: el `SET NOT NULL` guardado de la 0073 promovió
	// la columna, que es lo que prueba que el backfill las cubrió TODAS (si
	// hubiera quedado una NULL, el ALTER de arriba habría reventado y este test
	// habría muerto en «aplicando la 0073»).
	var nullable string
	if err := db.QueryRowContext(ctx, `
		SELECT is_nullable FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='tenant_llm' AND column_name='via'
	`).Scan(&nullable); err != nil {
		t.Fatalf("leyendo is_nullable de via: %v", err)
	}
	if nullable != "NO" {
		t.Fatalf("is_nullable(via)=%q, quiero NO: la promoción guardada no corrió", nullable)
	}
}

// TestBackfill0073_ColumnaAusenteNoNaceConDefault es la mutación (c) del test de
// arriba, hecha ejecutable — porque allí no lo era.
//
// El otro test fabrica el estado previo AFLOJANDO la columna (`DROP NOT NULL` +
// `DROP DEFAULT` + `SET via = NULL`), así que cuando la migración corre, `via` YA
// EXISTE y su `ADD COLUMN IF NOT EXISTS` es NO-OP: añadirle un `DEFAULT 'local'`
// no cambia nada y el test sigue verde. La trampa que el encabezado de la 0073
// documenta —desde PostgreSQL 11 un `ADD COLUMN … DEFAULT x` RELLENA las filas
// que ya existen— solo se puede ver sobre una tabla a la que la columna le FALTA,
// y eso es lo que este test monta.
//
// `DROP COLUMN via` se lleva por delante los CHECK que la nombran (el del
// vocabulario, el de la vía api completa y el de la vía local sin credencial):
// Postgres los borra solo, y es exactamente el estado en el que la 0073 encontró
// la tabla la primera vez que corrió.
//
// 🔬 MUTACIÓN: en la 0073, cambiar el bloque (a) por
//
//	ALTER TABLE public.tenant_llm ADD COLUMN IF NOT EXISTS via TEXT DEFAULT 'local';
//
// ⇒ la fila nacería 'local', el backfill no encontraría ninguna con `via` NULL y
// este test se pone ROJO. Es el corte de servicio silencioso —el tenant que paga
// su cuenta de Anthropic despertando en la vía local— que el ORDEN de la
// migración existe para evitar.
func TestBackfill0073_ColumnaAusenteNoNaceConDefault(t *testing.T) {
	_, db := nuevoStore(t)
	ctx := context.Background()

	sqlMigración, err := os.ReadFile(rutaMigración0073)
	if err != nil {
		t.Fatalf("leyendo %s: %v (¿se movió el fichero de la migración?)", rutaMigración0073, err)
	}
	// Si este test muere a medias, la tabla se queda SIN la columna y sin tres de
	// sus CHECK: los demás paquetes comparten la base de integración, así que la
	// migración se re-aplica pase lo que pase.
	t.Cleanup(func() {
		wipeTenantLLM(t, db)
		if _, cerr := db.ExecContext(context.Background(), string(sqlMigración)); cerr != nil {
			t.Fatalf("restaurando la 0073 tras el test: %v", cerr)
		}
	})

	ejecutar(t, db, `ALTER TABLE public.tenant_llm DROP COLUMN via`)

	// La fila de un tenant que HOY paga su cuenta: las seis columnas del eje api
	// llenas, que es la única forma que la 0071 admitía.
	ejecutar(t, db, `
		INSERT INTO public.tenant_llm
			(tenant_id, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		VALUES ('t-bf-sin-columna', 'anthropic', 'm', '\x01'::bytea, '\x02'::bytea, 'k-vieja', now())
	`)

	if _, err := db.ExecContext(ctx, string(sqlMigración)); err != nil {
		t.Fatalf("aplicando la 0073 sobre una tabla sin la columna: %v", err)
	}

	var via string
	if err := db.QueryRowContext(ctx,
		`SELECT via FROM public.tenant_llm WHERE tenant_id = $1`, "t-bf-sin-columna").Scan(&via); err != nil {
		t.Fatalf("leyendo la vía tras la migración: %v", err)
	}
	if via != tenantllm.ViaAPI {
		t.Fatalf("via=%q, quiero %q: la columna nació con default y el backfill no vio la fila — "+
			"a un tenant con credencial y consentimiento se le apagó la vía API en una migración",
			via, tenantllm.ViaAPI)
	}
}

// TestBackfill0073_EsIdempotente: el full-replay aplica el fichero en CADA
// arranque que cambie el hash del directorio, así que la pregunta no es si el
// backfill funciona una vez, sino si el segundo pase deja los datos donde
// estaban. Se aplica la migración DOS veces más sobre una tabla con dos filas de
// vías distintas, y nada se mueve.
//
// 🔬 MUTACIÓN: quitar de la 0073 cualquiera de las tres asignaciones del sobre
// del `ON CONFLICT DO UPDATE` no aplica aquí (eso es del store); la que este test
// vigila de verdad es que el replay NO reescriba el sobre de la vía api — el
// último aserto, que descifra la clave después de dos pases.
// 🔧 LA MUTACIÓN QUE ESTE TEST LLEVABA —«quitar `WHERE via IS NULL` ⇒ la fila
// local con credencial se convertiría en 'api'»— YA NO ES EJECUTABLE, y no por
// descuido: desde `tenant_llm_local_sin_credencial_check` (0073 · f.4) esa fila
// no se puede insertar. Ver la mutación (a) del test de arriba, donde está
// razonado por qué la guarda quedó redundante con una constraint.
func TestBackfill0073_EsIdempotente(t *testing.T) {
	store, db := nuevoStore(t)
	ctx := context.Background()

	sqlMigración, err := os.ReadFile(rutaMigración0073)
	if err != nil {
		t.Fatalf("leyendo %s: %v", rutaMigración0073, err)
	}

	if err := store.Upsert(ctx, cfgDePrueba(), claveDePrueba, time.Now().UTC()); err != nil {
		t.Fatalf("Upsert de la vía API: %v", err)
	}
	// Una fila LOCAL con proveedor, modelo y consentimiento pero SIN sobre: es la
	// forma que una vía local puede tener en la base sin violar f.4. Lo que aporta
	// al test es que su `via` NO se mueve en dos replays estando la tabla llena de
	// columnas del eje api rellenas.
	// 🔧 Llevaba credencial hasta el 2026-08-23 y por eso hay que decirlo: esa fila
	// dejó de ser insertable con tenant_llm_local_sin_credencial_check puesto.
	ejecutar(t, db, `
		INSERT INTO public.tenant_llm
			(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		VALUES ('t-idem-local', 'local', 'anthropic', 'm', NULL, NULL, NULL, now())
	`)

	for pase := 1; pase <= 2; pase++ {
		if _, err := db.ExecContext(ctx, string(sqlMigración)); err != nil {
			t.Fatalf("pase %d de la 0073: %v", pase, err)
		}
	}

	for tenant, esperada := range map[string]string{
		tenantDePrueba: tenantllm.ViaAPI,
		"t-idem-local": tenantllm.ViaLocal,
	} {
		var via string
		if err := db.QueryRowContext(ctx,
			`SELECT via FROM public.tenant_llm WHERE tenant_id = $1`, tenant).Scan(&via); err != nil {
			t.Fatalf("leyendo la vía de %s: %v", tenant, err)
		}
		if via != esperada {
			t.Fatalf("tras dos replays %s quedó en via=%q, quiero %q", tenant, via, esperada)
		}
	}
	// La credencial de la vía API sobrevivió a los dos replays: una migración que
	// reescribiera el sobre sería peor que una que no corriera.
	if clave, err := store.APIKey(ctx, tenantDePrueba); err != nil || clave != claveDePrueba {
		t.Fatalf("tras dos replays APIKey devolvió (%q, %v): el replay tocó el sobre", clave, err)
	}
}

// ============================================================
// LA CONDICIÓN DEL BACKFILL, AISLADA DE LOS CHECK QUE LA TAPAN
// ============================================================

// nombreDelClon0073 es la tabla efímera que fabrica el test de abajo. Vive en
// `public` y NO es una TEMP a propósito: `database/sql` reparte las sentencias
// entre las conexiones del pool y una tabla temporal solo existe en la sesión que
// la creó, así que con TEMP el siguiente Exec podría caer en otra conexión y ver
// «relation does not exist».
const nombreDelClon0073 = "public.clon_condicion_backfill_0073"

// Las sentencias del clon son CONSTANTES —concatenación de constantes, sin un
// solo dato de fuera— para que ninguna consulta de este test se arme en tiempo de
// ejecución: la única cadena dinámica es el backfill, y ése se LEE de la migración.
const (
	crearElClon0073 = `CREATE TABLE ` + nombreDelClon0073 +
		` (LIKE public.tenant_llm INCLUDING DEFAULTS EXCLUDING CONSTRAINTS)`

	soltarElClon0073 = `DROP TABLE IF EXISTS ` + nombreDelClon0073

	// `LIKE` copia SIEMPRE los NOT NULL —no son opcionales como los CHECK— y con
	// `via` NOT NULL no hay forma de fabricar la fila que el backfill busca: su
	// guarda es `WHERE via IS NULL`. El DEFAULT 'local' se suelta por lo mismo,
	// aunque el INSERT de abajo escriba NULL explícito: dejarlo puesto invitaría
	// al siguiente lector a creer que la fila nace con vía.
	aflojarLaViaDelClon0073 = `ALTER TABLE ` + nombreDelClon0073 +
		` ALTER COLUMN via DROP NOT NULL, ALTER COLUMN via DROP DEFAULT`

	// `EXCLUDING CONSTRAINTS` deja fuera los CHECK. Esto lo COMPRUEBA en vez de
	// creérselo: de ese cero depende que las seis filas de este test existan, y el
	// día que una versión de Postgres los copiara el test se quedaría probando el
	// esquema real —o sea, nada— con el mismo verde de siempre.
	contarChecksDelClon0073 = `SELECT count(*) FROM pg_constraint
		 WHERE conrelid = '` + nombreDelClon0073 + `'::regclass AND contype = 'c'`

	insertarEnElClon0073 = `INSERT INTO ` + nombreDelClon0073 + `
		(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		VALUES ($1, NULL, $2, $3, $4, $5, $6, $7)`

	leerLaViaDelClon0073 = `SELECT via FROM ` + nombreDelClon0073 + ` WHERE tenant_id = $1`
)

// columnasDeLaViaAPI son las SEIS que el `CASE` del backfill exige para mandar una
// fila a 'api', EN EL MISMO ORDEN que los parámetros $2..$7 de
// `insertarEnElClon0073`. Ese índice compartido es lo único que ata el nombre del
// subtest con la columna que de verdad se pone a NULL: si alguien reordena una de
// las dos listas sin la otra, el rojo nombraría una columna y fallaría por otra.
var columnasDeLaViaAPI = []string{
	"provider", "model", "api_key_enc", "api_key_dek", "api_key_kek_id", "consented_at",
}

// filaCompletaDeLaViaAPI son los valores de esas seis columnas para la fila que SÍ
// cumple la condición entera. Los bytes del sobre son basura a propósito, igual
// que en los tests de arriba: aquí no se descifra nada, lo único que importa de
// ellos es que NO son NULL.
func filaCompletaDeLaViaAPI() []any {
	return []any{"anthropic", "m", []byte{0x01}, []byte{0x02}, "k-vieja", time.Now().UTC()}
}

// condiciónDelBackfill0073 EXTRAE del fichero real de la migración la sentencia
// `UPDATE public.tenant_llm … WHERE via IS NULL;` y le reapunta la tabla al clon.
//
// 🔑 Se extrae en vez de copiarse por lo mismo que los tres tests de arriba
// EJECUTAN el fichero: una condición tecleada aquí probaría la copia, y la copia
// sigue verde el día que alguien edite el original — que es justo el día en que
// este test tendría algo que decir. Solo se ancla en la FORMA (dónde empieza la
// sentencia y que conserve su guarda), NUNCA en los nombres de las seis columnas:
// asertar aquí que el `CASE` las nombra convertiría la mutación en un fallo de
// extracción y el rojo dejaría de señalar la columna.
func condiciónDelBackfill0073(t *testing.T, sqlMigración string) string {
	t.Helper()
	const ancla = "UPDATE public.tenant_llm"
	inicio := strings.Index(sqlMigración, ancla)
	if inicio < 0 {
		t.Fatalf("no encuentro %q en %s: la migración CAMBIÓ DE FORMA y este test dejó de leer su backfill",
			ancla, rutaMigración0073)
	}
	fin := strings.Index(sqlMigración[inicio:], ";")
	if fin < 0 {
		t.Fatalf("el %q de %s no termina en ';': la migración cambió de forma", ancla, rutaMigración0073)
	}
	bloque := sqlMigración[inicio : inicio+fin+1]
	if !strings.Contains(bloque, "WHERE via IS NULL") {
		t.Fatalf("el backfill extraído de %s perdió su guarda `WHERE via IS NULL`:\n%s\n"+
			"la migración cambió de forma y este test ya no está ejercitando lo que cree", rutaMigración0073, bloque)
	}
	return strings.Replace(bloque, "public.tenant_llm", nombreDelClon0073, 1)
}

// clonSinChecks0073 fabrica la tabla clon, la deja capaz de sostener `via` NULL y
// COMPRUEBA que no heredó ningún CHECK. El DROP va también ANTES del CREATE: si un
// test anterior murió entre el CREATE y su Cleanup, la tabla seguiría ahí y este
// arrancaría con las siete filas del pase anterior.
func clonSinChecks0073(t *testing.T, db *sql.DB) {
	t.Helper()
	ejecutar(t, db, soltarElClon0073)
	ejecutar(t, db, crearElClon0073)
	// La base de integración es COMPARTIDA: la tabla se va pase lo que pase.
	t.Cleanup(func() {
		if _, cerr := db.ExecContext(context.Background(), soltarElClon0073); cerr != nil {
			t.Fatalf("soltando %s tras el test: %v", nombreDelClon0073, cerr)
		}
	})
	ejecutar(t, db, aflojarLaViaDelClon0073)

	var checks int
	if err := db.QueryRowContext(context.Background(), contarChecksDelClon0073).Scan(&checks); err != nil {
		t.Fatalf("contando los CHECK de %s: %v", nombreDelClon0073, err)
	}
	if checks != 0 {
		t.Fatalf("%s heredó %d CHECK de public.tenant_llm: `EXCLUDING CONSTRAINTS` dejó de excluirlos "+
			"y el clon ya no puede sostener las filas que este test necesita", nombreDelClon0073, checks)
	}
}

// insertarFilaDelClon0073 escribe una fila con `via` NULL —la que el backfill
// busca— completa salvo la columna `columnaANil` (índice en columnasDeLaViaAPI).
// Con -1 escribe la fila entera.
func insertarFilaDelClon0073(t *testing.T, db *sql.DB, tenant string, columnaANil int) {
	t.Helper()
	valores := filaCompletaDeLaViaAPI()
	if columnaANil >= 0 {
		valores[columnaANil] = nil
	}
	ejecutar(t, db, insertarEnElClon0073, append([]any{tenant}, valores...)...)
}

func viaDelClon0073(t *testing.T, db *sql.DB, tenant string) string {
	t.Helper()
	var via string
	if err := db.QueryRowContext(context.Background(), leerLaViaDelClon0073, tenant).Scan(&via); err != nil {
		t.Fatalf("leyendo la vía de %s en %s: %v", tenant, nombreDelClon0073, err)
	}
	return via
}

// TestBackfill0073_LaCondicionAisladaExigeLasSeis prueba LA CONDICIÓN del `CASE`
// del backfill de la 0073, y solo ella: que exige LAS SEIS columnas del eje api
// —`provider`, `model`, el trío del sobre y `consented_at`— para mandar una fila a
// 'api', y que la falta de CUALQUIERA de ellas, POR SÍ SOLA, la manda a 'local'.
//
// La garantía de fondo se lee en el subtest `sin-consented_at` y es de privacidad,
// no de forma: sin la autorización explícita del tenant (ADR-0030), wApp NO marca
// su fila para la vía que manda el texto de sus clientes a un tercero. Tener la
// credencial preparada no es haber autorizado la salida.
//
// 🔑 POR QUÉ NECESITA UNA TABLA CLON SIN LOS CHECK, y no puede probarse sobre
// `public.tenant_llm` como sus tres hermanos de arriba: las seis filas que aíslan
// cada columna son INEXPRESABLES en la tabla real, por el agujero que la propia
// 0073 nombra en el ⚠️ de (f.4). Una fila con el sobre entero a la que le falte
// `consented_at` —o `provider`, o `model`— no cabe en NINGUNA de las dos vías: como
// 'api' la rechaza `tenant_llm_via_api_completa_check`, como 'local' la rechaza
// `tenant_llm_local_sin_credencial_check`, así que la migración muere en su propio
// `ADD CONSTRAINT` antes de que nadie llegue a mirar el reparto. Eso es literalmente
// lo que le pasó a TestBackfill0073_RepartoSobreEstadoPrevioFabricado el 2026-08-23
// y por lo que su fila `t-bf-sin-consent` tuvo que perder el sobre — y con él, la
// capacidad de aislar CUÁL de las dos carencias la mandó a 'local'. El clon, al no
// tener CHECK, es el único sitio donde esas filas existen.
//
// ⚠️ LO QUE ESTE TEST NO DICE: no dice que la tabla real admita esas filas. No las
// admite, y eso es una virtud del esquema. Lo que prueba es la REGLA DE REPARTO
// —la que decide a qué vía despierta cada tenant en la migración— que es la única
// pieza que sobreviviría a que mañana se retirara uno de esos CHECK, y la que
// entonces sería lo ÚNICO entre un tenant sin consentir y una llamada a un tercero.
//
// 🔬 MUTACIÓN — EJECUTADA, no prometida (barrido del CLI, 2026-08-23): quitar
// `AND consented_at IS NOT NULL` del `CASE` de la 0073 ⇒ el subtest
// `sin-consented_at` se pone ROJO y los otros seis siguen verdes. Lo mismo con
// cualquiera de las otras cinco líneas de la condición: cada una tiene su subtest,
// así que el rojo nombra la columna que se cayó.
func TestBackfill0073_LaCondicionAisladaExigeLasSeis(t *testing.T) {
	_, db := nuevoStore(t)

	sqlMigración, err := os.ReadFile(rutaMigración0073)
	if err != nil {
		t.Fatalf("leyendo %s: %v (¿se movió el fichero de la migración?)", rutaMigración0073, err)
	}
	backfill := condiciónDelBackfill0073(t, string(sqlMigración))

	clonSinChecks0073(t, db)

	// La fila que cumple las seis, y las seis a las que les falta exactamente una.
	insertarFilaDelClon0073(t, db, "t-cond-completa", -1)
	for i, col := range columnasDeLaViaAPI {
		insertarFilaDelClon0073(t, db, "t-cond-sin-"+col, i)
	}

	// El backfill REAL, con la tabla reapuntada al clon. Nada más de la migración
	// corre aquí: ni los ALTER, ni los CHECK — precisamente por eso las filas viven.
	ejecutar(t, db, backfill)

	t.Run("completa", func(t *testing.T) {
		if via := viaDelClon0073(t, db, "t-cond-completa"); via != tenantllm.ViaAPI {
			t.Fatalf("la fila con LAS SEIS quedó en via=%q, quiero %q: el backfill apagaría la vía "+
				"API a un tenant que la tiene configurada y la paga", via, tenantllm.ViaAPI)
		}
	})
	for _, col := range columnasDeLaViaAPI {
		t.Run("sin-"+col, func(t *testing.T) {
			if via := viaDelClon0073(t, db, "t-cond-sin-"+col); via != tenantllm.ViaLocal {
				t.Fatalf("sin %s la fila quedó en via=%q, quiero %q: la condición del backfill dejó de "+
					"exigir esa columna y marcó para la vía API una fila incompleta", col, via, tenantllm.ViaLocal)
			}
		})
	}
}
