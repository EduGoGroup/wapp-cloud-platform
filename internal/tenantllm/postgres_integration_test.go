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

func cfgDePrueba() tenantllm.Config {
	return tenantllm.Config{
		TenantID: tenantDePrueba,
		Provider: tenantllm.ProviderAnthropic,
		Model:    "claude-sonnet-4-5",
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
// reventaría contra el NOT NULL, que es exactamente la protección que la 0071
// puso ahí.
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
