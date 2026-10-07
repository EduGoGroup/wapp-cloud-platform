package tenantllm_test

// Los tests de fichero de tenantllm.Postgres, con un driver de database/sql de mentira
// (fakedb_test.go) y un cifrador de verdad sobre un keyring de prueba: la validación previa al
// SQL con sus tres textos exactos, el texto EXACTO de cada sentencia, sus argumentos, el mapeo
// de filas y errores, y el sobre (qué se cifra y con qué KEK se descifra, T-15). Que ese SQL
// haga en un Postgres de verdad lo que el puerto promete lo prueba tenantllmhelpertest.Contrato
// en los procesos de F9. Ningún test usa una clave real ni toca la red (T-16).

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Las cuatro sentencias, byte a byte, escritas aquí a mano y NO copiadas de una constante de
// producción: si alguien toca el SQL del adaptador, este fichero lo dice. Los espacios en blanco
// (el salto inicial, las tabulaciones de sangría, la tabulación final) son los del literal del
// paquete viejo, internal/tenantllm/postgres.go @ ebf4eb7.
const (
	// Get: la credencial no se selecciona, solo si existe.
	sqlGet = "\n" +
		"\t\tSELECT tenant_id, via, provider, model, api_key_enc IS NOT NULL, consented_at, created_at, updated_at\n" +
		"\t\tFROM public.tenant_llm\n" +
		"\t\tWHERE tenant_id = $1\n" +
		"\t"
	// Upsert: reemplaza las siete columnas de negocio; created_at no está en el SET.
	sqlUpsert = "\n" +
		"\t\tINSERT INTO public.tenant_llm\n" +
		"\t\t\t(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at, updated_at)\n" +
		"\t\tVALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())\n" +
		"\t\tON CONFLICT (tenant_id) DO UPDATE SET\n" +
		"\t\t\tvia            = EXCLUDED.via,\n" +
		"\t\t\tprovider       = EXCLUDED.provider,\n" +
		"\t\t\tmodel          = EXCLUDED.model,\n" +
		"\t\t\tapi_key_enc    = EXCLUDED.api_key_enc,\n" +
		"\t\t\tapi_key_dek    = EXCLUDED.api_key_dek,\n" +
		"\t\t\tapi_key_kek_id = EXCLUDED.api_key_kek_id,\n" +
		"\t\t\tconsented_at   = EXCLUDED.consented_at,\n" +
		"\t\t\tupdated_at     = now()\n" +
		"\t"
	sqlDelete = "\n" +
		"\t\tDELETE FROM public.tenant_llm WHERE tenant_id = $1\n" +
		"\t"
	// APIKey: lee la vía además del sobre; la guarda de la vía va en Go, no en el WHERE.
	sqlAPIKey = "\n" +
		"\t\tSELECT via, api_key_enc, api_key_dek, api_key_kek_id\n" +
		"\t\tFROM public.tenant_llm\n" +
		"\t\tWHERE tenant_id = $1\n" +
		"\t"
)

const (
	pgTenant = "5e0b3a52-6a52-4a44-8b1c-2f0d6a3f9c11"
	pgModel  = "claude-sonnet-4-5"
	// La credencial de las pruebas lleva el prefijo público del proveedor a propósito: es la
	// cadena que se busca en el blob, y buscar una con la forma de la de verdad hace creíble la
	// búsqueda. Es inventada.
	fakeAPIKey = "sk-ant-api03-CLAVE-FALSA-DE-PRUEBA-0044" // #nosec G101 -- clave de prueba inventada, no una credencial real

	kekOldID  = "test-kek-old"
	kekNewID  = "test-kek-new"
	kekOldB64 = "ERERERERERERERERERERERERERERERERERERERERERE="
	kekNewB64 = "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI="
	indexB64  = "RERERERERERERERERERERERERERERERERERERERERES="
	keyring   = kekOldID + ":" + kekOldB64 + "," + kekNewID + ":" + kekNewB64
)

var (
	// pgConsent va en una zona que no es UTC para ver que el adaptador la normaliza.
	pgConsent = time.Date(2026, 9, 1, 10, 30, 0, 0, time.FixedZone("x", -3*3600))
	pgCreated = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	pgUpdated = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	getColumns    = []string{"tenant_id", "via", "provider", "model", "?column?", "consented_at", "created_at", "updated_at"}
	apiKeyColumns = []string{"via", "api_key_enc", "api_key_dek", "api_key_kek_id"}
)

// newCipher arma un cifrador de campo de verdad sobre el keyring de prueba, con currentID como
// la KEK que envuelve. Las dos KEK están siempre en el keyring: es una rotación a medias.
func newCipher(t *testing.T, currentID string) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{KeyringB64: keyring, CurrentID: currentID, IndexB64: indexB64})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// brokenKeyProvider es un KeyProvider que no sabe envolver: hace fallar a FieldCipher.Encrypt.
type brokenKeyProvider struct{ cause error }

func (b brokenKeyProvider) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", b.cause }
func (b brokenKeyProvider) UnwrapDEK([]byte, string) ([]byte, error) { return nil, b.cause }
func (brokenKeyProvider) BlindIndex(string, string) string           { return "" }
func (brokenKeyProvider) CurrentKeyID() string                       { return kekNewID }

// newPostgres monta el adaptador sobre el driver de mentira, con kekNewID como KEK current.
func newPostgres(t *testing.T) (*tenantllm.Postgres, *fakeDB) {
	t.Helper()
	fake, db := openFakeDB(t)
	return tenantllm.NewPostgres(db, newCipher(t, kekNewID)), fake
}

func apiConfig() tenantllm.Config {
	return tenantllm.Config{TenantID: pgTenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: pgModel}
}

// requireOnlyQuery afirma que al driver llegó UNA sentencia con ese texto exacto, y devuelve sus
// argumentos.
func requireOnlyQuery(t *testing.T, fake *fakeDB, wantQuery string) []driver.Value {
	t.Helper()
	stmts := fake.statements()
	if len(stmts) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(stmts), stmts)
	}
	if stmts[0].query != wantQuery {
		t.Errorf("SQL emitido:\n%q\nquería, byte a byte:\n%q", stmts[0].query, wantQuery)
	}
	return stmts[0].args
}

// requireOnlyStatement es requireOnlyQuery más los argumentos exactos (número, orden, tipo, valor).
func requireOnlyStatement(t *testing.T, fake *fakeDB, wantQuery string, wantArgs ...driver.Value) {
	t.Helper()
	if args := requireOnlyQuery(t, fake, wantQuery); !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("argumentos = %#v, quería %#v", args, wantArgs)
	}
}

// requireWrapped afirma que err envuelve la causa y empieza por el prefijo del adaptador.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, quería uno que envuelva %v", err, cause)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, quería el prefijo %q", err, prefix)
	}
}

// TestNewPostgres_DoesNotTouchTheDatabase: construir no abre ni consulta nada.
func TestNewPostgres_DoesNotTouchTheDatabase(t *testing.T) {
	store, fake := newPostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("construir el adaptador emitió %d sentencias, quería 0", n)
	}
}

// TestPostgresUpsert_RejectsBeforeSQL (R4.4.e): los tres rechazos, con su texto exacto y su
// orden (vía, clave, consentimiento), sin que llegue una sola sentencia al driver. Mutantes que
// mata: quitar cualquiera de las tres guardas, cambiar su orden, o comparar la vía sin
// distinguir mayúsculas.
func TestPostgresUpsert_RejectsBeforeSQL(t *testing.T) {
	const noKey = "tenantllm: upsert de " + pgTenant + " en vía api sin API key: esa vía no existe sin credencial"
	const noConsent = "tenantllm: upsert de " + pgTenant + " en vía api sin consentimiento: la fila no puede existir sin él"
	outOfVocabulary := func(via string) string {
		return "tenantllm: upsert de " + pgTenant + " con vía " + via + ": fuera del vocabulario (local|api)"
	}
	cases := []struct {
		name    string
		via     string
		apiKey  string
		consent time.Time
		want    string
	}{
		{"empty via", "", fakeAPIKey, pgConsent, outOfVocabulary(`""`)},
		{"unknown via", "remota", fakeAPIKey, pgConsent, outOfVocabulary(`"remota"`)},
		{"upper case via", "API", fakeAPIKey, pgConsent, outOfVocabulary(`"API"`)},
		{"via with a trailing space", "api ", fakeAPIKey, pgConsent, outOfVocabulary(`"api "`)},
		{"via with a quote is escaped", `a"b`, fakeAPIKey, pgConsent, outOfVocabulary(`"a\"b"`)},
		{"unknown via wins over the missing key and consent", "remota", "", time.Time{}, outOfVocabulary(`"remota"`)},
		{"api without key", tenantllm.ViaAPI, "", pgConsent, noKey},
		{"api without key wins over the missing consent", tenantllm.ViaAPI, "", time.Time{}, noKey},
		{"api without consent", tenantllm.ViaAPI, fakeAPIKey, time.Time{}, noConsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			cfg := apiConfig()
			cfg.Via = c.via
			err := store.Upsert(context.Background(), cfg, c.apiKey, c.consent)
			if err == nil {
				t.Fatal("Upsert devolvió nil, quería el rechazo previo al SQL")
			}
			if err.Error() != c.want {
				t.Errorf("error =\n%q\nquería, byte a byte:\n%q", err, c.want)
			}
			if n := len(fake.statements()); n != 0 {
				t.Errorf("el rechazo emitió %d sentencias, quería 0: la validación va antes del SQL", n)
			}
		})
	}
}

// requireSealedEnvelope afirma el sobre ($5..$7) de un upsert de la vía api: la clave viaja
// cifrada, envuelta por la KEK current, y el sobre se abre con el cifrador y la devuelve. Va
// aparte de TestPostgresUpsert_API_SealsTheKey solo para que ese test quepa en el tope de gocyclo.
func requireSealedEnvelope(t *testing.T, args []driver.Value) {
	t.Helper()
	enc, okEnc := args[4].([]byte)
	dek, okDEK := args[5].([]byte)
	kekID, okKEK := args[6].(string)
	if !okEnc || !okDEK || !okKEK || len(enc) == 0 || len(dek) == 0 {
		t.Fatalf("el sobre ($5..$7) = %T %T %T, quería []byte, []byte y string no vacíos", args[4], args[5], args[6])
	}
	if bytes.Contains(enc, []byte(fakeAPIKey)) || bytes.Contains(enc, []byte("sk-ant-")) {
		t.Errorf("FUGA: api_key_enc lleva la clave o su prefijo en claro (%d bytes)", len(enc))
	}
	if bytes.Contains(dek, []byte(fakeAPIKey)) {
		t.Errorf("FUGA: api_key_dek lleva la clave en claro (%d bytes)", len(dek))
	}
	if kekID != kekNewID {
		t.Errorf("api_key_kek_id = %q, quería la KEK current %q", kekID, kekNewID)
	}
	if plain, err := newCipher(t, kekNewID).Decrypt(enc, dek, kekID); err != nil || plain != fakeAPIKey {
		t.Errorf("el sobre que llegó al driver no devuelve la clave al abrirlo (err=%v)", err)
	}
}

// TestPostgresUpsert_API_SealsTheKey: la sentencia exacta y sus ocho argumentos. La clave viaja
// CIFRADA —ni entera ni su prefijo aparecen en el blob—, envuelta por la KEK current, y el sobre
// que llegó al driver se abre con el cifrador y devuelve la clave. El consentimiento va en UTC.
func TestPostgresUpsert_API_SealsTheKey(t *testing.T) {
	store, fake := newPostgres(t)
	if err := store.Upsert(context.Background(), apiConfig(), fakeAPIKey, pgConsent); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	args := requireOnlyQuery(t, fake, sqlUpsert)
	if len(args) != 8 {
		t.Fatalf("llegaron %d argumentos, quería 8: %#v", len(args), args)
	}
	if want := []driver.Value{pgTenant, tenantllm.ViaAPI, tenantllm.ProviderAnthropic, pgModel}; !reflect.DeepEqual(args[:4], want) {
		t.Errorf("argumentos $1..$4 = %#v, quería %#v", args[:4], want)
	}
	requireSealedEnvelope(t, args)
	consent, ok := args[7].(time.Time)
	if !ok || !consent.Equal(pgConsent) || consent.Location() != time.UTC {
		t.Errorf("consented_at ($8) = %#v, quería %v en UTC", args[7], pgConsent.UTC())
	}
}

// TestPostgresUpsert_Local_WritesSixNulls: en la vía local las seis columnas del eje api viajan
// como NULL (nil sin tipo), VENGA LO QUE VENGA en cfg, en la clave y en el consentimiento. Mutante
// que mata: sacar el relleno del eje api de dentro del `if cfg.Via == ViaAPI`.
func TestPostgresUpsert_Local_WritesSixNulls(t *testing.T) {
	carrying := apiConfig()
	carrying.Via, carrying.HasAPIKey = tenantllm.ViaLocal, true
	cases := []struct {
		name    string
		cfg     tenantllm.Config
		apiKey  string
		consent time.Time
	}{
		{"bare local config", tenantllm.Config{TenantID: pgTenant, Via: tenantllm.ViaLocal}, "", time.Time{}},
		{"local config carrying the api axis", carrying, fakeAPIKey, pgConsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			if err := store.Upsert(context.Background(), c.cfg, c.apiKey, c.consent); err != nil {
				t.Fatalf("Upsert de la vía local: error inesperado %v (esa vía no exige nada)", err)
			}
			requireOnlyStatement(t, fake, sqlUpsert, pgTenant, tenantllm.ViaLocal, nil, nil, nil, nil, nil, nil)
		})
	}
}

// TestPostgresUpsert_StatementReplacesSevenColumnsAndKeepsCreatedAt: la guarda dicha sobre el
// texto: el SET pisa la vía y las seis del eje api, y NO nombra created_at.
func TestPostgresUpsert_StatementReplacesSevenColumnsAndKeepsCreatedAt(t *testing.T) {
	_, set, found := strings.Cut(sqlUpsert, "DO UPDATE SET")
	if !found {
		t.Fatal("la sentencia del Upsert no tiene DO UPDATE SET")
	}
	for _, column := range []string{"via", "provider", "model", "api_key_enc", "api_key_dek", "api_key_kek_id", "consented_at"} {
		if !strings.Contains(set, "= EXCLUDED."+column+",") {
			t.Errorf("el SET no reemplaza %s: el upsert dejaría de ser la foto entera", column)
		}
	}
	if strings.Contains(set, "created_at") {
		t.Errorf("el SET nombra created_at: el alta se pisaría en cada upsert:\n%s", set)
	}
}

// TestPostgresUpsert_CipherFailure_IsWrappedAndWritesNothing: si no se puede cifrar, no se
// escribe; el error nombra al tenant y no lleva la clave.
func TestPostgresUpsert_CipherFailure_IsWrappedAndWritesNothing(t *testing.T) {
	cause := errors.New("kms caído")
	fake, db := openFakeDB(t)
	store := tenantllm.NewPostgres(db, crypto.NewFieldCipher(brokenKeyProvider{cause: cause}))
	err := store.Upsert(context.Background(), apiConfig(), fakeAPIKey, pgConsent)
	requireWrapped(t, err, cause, "tenantllm: cifrar la API key de "+pgTenant+": ")
	if strings.Contains(err.Error(), fakeAPIKey) {
		t.Error("FUGA: el error del cifrado lleva la clave")
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("un cifrado fallido emitió %d sentencias, quería 0", n)
	}
}

// TestPostgresGet_MapsTheRow: la sentencia exacta y cada columna a su campo; los tres NULL de
// la vía local llegan como valor cero.
func TestPostgresGet_MapsTheRow(t *testing.T) {
	cases := []struct {
		name string
		row  []driver.Value
		want tenantllm.Config
	}{
		{
			"api row",
			[]driver.Value{pgTenant, "api", "anthropic", pgModel, true, pgConsent, pgCreated, pgUpdated},
			tenantllm.Config{
				TenantID: pgTenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: pgModel,
				HasAPIKey: true, ConsentedAt: pgConsent, CreatedAt: pgCreated, UpdatedAt: pgUpdated,
			},
		},
		{
			"local row",
			[]driver.Value{pgTenant, "local", nil, nil, false, nil, pgCreated, pgUpdated},
			tenantllm.Config{TenantID: pgTenant, Via: tenantllm.ViaLocal, CreatedAt: pgCreated, UpdatedAt: pgUpdated},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.answer(getColumns, c.row)
			cfg, found, err := store.Get(context.Background(), pgTenant)
			if err != nil || !found {
				t.Fatalf("Get = (found=%v, err=%v), quería la fila", found, err)
			}
			if cfg != c.want {
				t.Errorf("Get = %+v, quería %+v", cfg, c.want)
			}
			requireOnlyStatement(t, fake, sqlGet, pgTenant)
		})
	}
}

// TestPostgresGet_NoRow_NotFound: sin fila no es un error: Config cero, found=false, nil.
func TestPostgresGet_NoRow_NotFound(t *testing.T) {
	store, fake := newPostgres(t)
	cfg, found, err := store.Get(context.Background(), pgTenant)
	if err != nil || found || cfg != (tenantllm.Config{}) {
		t.Errorf("Get sin fila = (%+v, found=%v, err=%v), quería (Config cero, false, nil)", cfg, found, err)
	}
	requireOnlyStatement(t, fake, sqlGet, pgTenant)
}

// TestPostgresGet_NullVia_IsAnError: una fila con `via` NULL falla ruidosamente; no es un «no
// encontrado» ni una vía vacía que nadie sabría leer.
func TestPostgresGet_NullVia_IsAnError(t *testing.T) {
	store, fake := newPostgres(t)
	fake.answer(getColumns, []driver.Value{pgTenant, nil, nil, nil, false, nil, pgCreated, pgUpdated})
	cfg, found, err := store.Get(context.Background(), pgTenant)
	if err == nil || found || cfg != (tenantllm.Config{}) {
		t.Fatalf("Get de una fila con via NULL = (%+v, found=%v, err=%v), quería (Config cero, false, error)", cfg, found, err)
	}
	if prefix := "tenantllm: leer configuración de " + pgTenant + ": "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, quería el prefijo %q", err, prefix)
	}
}

// TestPostgresDelete: la sentencia exacta, con el tenant como único argumento.
func TestPostgresDelete(t *testing.T) {
	store, fake := newPostgres(t)
	if err := store.Delete(context.Background(), pgTenant); err != nil {
		t.Fatalf("Delete: error inesperado %v", err)
	}
	requireOnlyStatement(t, fake, sqlDelete, pgTenant)
}

// seal cifra la clave de prueba con la KEK currentID y devuelve el sobre.
func seal(t *testing.T, currentID string) (enc, dek []byte, kekID string) {
	t.Helper()
	enc, dek, kekID, err := newCipher(t, currentID).Encrypt(fakeAPIKey)
	if err != nil {
		t.Fatalf("cifrando la clave de prueba: %v", err)
	}
	return enc, dek, kekID
}

// TestPostgresAPIKey_DecryptsWithTheRowKEK (T-15): la fila la envolvió la KEK vieja y la
// current del adaptador es la nueva; se descifra con la de la FILA. Mutante que mata: pasar a
// Decrypt la KEK current en vez de api_key_kek_id.
func TestPostgresAPIKey_DecryptsWithTheRowKEK(t *testing.T) {
	for _, rowKEK := range []string{kekOldID, kekNewID} {
		t.Run(rowKEK, func(t *testing.T) {
			enc, dek, kekID := seal(t, rowKEK)
			if kekID != rowKEK {
				t.Fatalf("el sobre de prueba salió envuelto por %q, quería %q", kekID, rowKEK)
			}
			store, fake := newPostgres(t)
			fake.answer(apiKeyColumns, []driver.Value{"api", enc, dek, kekID})
			got, err := store.APIKey(context.Background(), pgTenant)
			if err != nil {
				t.Fatalf("APIKey: error inesperado %v", err)
			}
			if got != fakeAPIKey {
				t.Error("APIKey devolvió una clave distinta de la guardada")
			}
			requireOnlyStatement(t, fake, sqlAPIKey, pgTenant)
		})
	}
}

// TestPostgresAPIKey_NotConfigured (R4.4.c): sin fila, sin sobre o con otra vía es
// ErrNotConfigured, sin envolver y sin clave. El caso «local con sobre entero» es el cinturón
// y tirantes: la tabla lo prohíbe y el código, además, no lo usa. Mutante que mata: quitar la
// guarda `via != ViaAPI`.
func TestPostgresAPIKey_NotConfigured(t *testing.T) {
	enc, dek, kekID := seal(t, kekNewID)
	cases := []struct {
		name string
		rows [][]driver.Value
	}{
		{"no row", nil},
		{"local row without envelope", [][]driver.Value{{"local", nil, nil, nil}}},
		{"api row without kek id", [][]driver.Value{{"api", enc, dek, nil}}},
		{"local row with a whole envelope", [][]driver.Value{{"local", enc, dek, kekID}}},
		{"unknown via with a whole envelope", [][]driver.Value{{"API", enc, dek, kekID}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.answer(apiKeyColumns, c.rows...)
			got, err := store.APIKey(context.Background(), pgTenant)
			if !errors.Is(err, tenantllm.ErrNotConfigured) || got != "" {
				t.Fatalf("APIKey = (clave de %d bytes, %v), quería (\"\", ErrNotConfigured)", len(got), err)
			}
			if err.Error() != tenantllm.ErrNotConfigured.Error() {
				t.Errorf("error = %q, quería ErrNotConfigured sin envolver", err)
			}
			requireOnlyStatement(t, fake, sqlAPIKey, pgTenant)
		})
	}
}

// TestPostgresAPIKey_UnopenableEnvelope_IsWrapped: una KEK que no está en el keyring o un blob
// manipulado son un error envuelto, sin clave y sin material cifrado en el texto.
func TestPostgresAPIKey_UnopenableEnvelope_IsWrapped(t *testing.T) {
	enc, dek, kekID := seal(t, kekNewID)
	tampered := bytes.Clone(enc)
	tampered[len(tampered)-1] ^= 0xff
	cases := []struct {
		name string
		row  []driver.Value
	}{
		{"kek id missing from the keyring", []driver.Value{"api", enc, dek, "kek-retirada"}},
		{"tampered blob", []driver.Value{"api", tampered, dek, kekID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.answer(apiKeyColumns, c.row)
			got, err := store.APIKey(context.Background(), pgTenant)
			if err == nil || got != "" || errors.Is(err, tenantllm.ErrNotConfigured) {
				t.Fatalf("APIKey = (clave de %d bytes, %v), quería (\"\", error de descifrado)", len(got), err)
			}
			if prefix := "tenantllm: descifrar la API key de " + pgTenant + ": "; !strings.HasPrefix(err.Error(), prefix) {
				t.Errorf("error = %q, quería el prefijo %q", err, prefix)
			}
			if strings.Contains(err.Error(), string(enc)) || strings.Contains(err.Error(), fakeAPIKey) {
				t.Error("FUGA: el error del descifrado lleva el blob o la clave")
			}
		})
	}
}

// TestPostgres_DriverFailure_IsWrapped: un fallo del driver vuelve envuelto, con el texto de
// cada operación y el tenant, y las lecturas no inventan estado.
func TestPostgres_DriverFailure_IsWrapped(t *testing.T) {
	cause := errors.New("conexión rota")
	ctx := context.Background()
	broken := func() *tenantllm.Postgres {
		store, fake := newPostgres(t)
		fake.fail(cause)
		return store
	}

	requireWrapped(t, broken().Upsert(ctx, apiConfig(), fakeAPIKey, pgConsent), cause, "tenantllm: upsert de "+pgTenant+": ")
	requireWrapped(t, broken().Delete(ctx, pgTenant), cause, "tenantllm: borrar configuración de "+pgTenant+": ")

	cfg, found, err := broken().Get(ctx, pgTenant)
	requireWrapped(t, err, cause, "tenantllm: leer configuración de "+pgTenant+": ")
	if found || cfg != (tenantllm.Config{}) {
		t.Errorf("Get con el driver caído = (%+v, found=%v), quería (Config cero, false)", cfg, found)
	}
	key, err := broken().APIKey(ctx, pgTenant)
	requireWrapped(t, err, cause, "tenantllm: leer la API key de "+pgTenant+": ")
	if key != "" || errors.Is(err, tenantllm.ErrNotConfigured) {
		t.Errorf("APIKey con el driver caído = (clave de %d bytes, %v): un fallo de infraestructura no es «sin configurar»", len(key), err)
	}
}
