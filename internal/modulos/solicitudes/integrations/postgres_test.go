package integrations_test

// postgres.go se prueba en tres ficheros (E-13), con un driver de mentira (postgres_fakedb_test.go)
// y las sentencias literales (postgres_sql_test.go):
//   - postgres_test.go: las firmas, el constructor, el encolado y el reclamo.
//   - postgres_close_test.go: las tres transiciones con su valla y el rescate.
//   - postgres_tenant_test.go: la configuración, el secreto, su huella y el recuento de la cola.
//
// Sin base no se puede ver lo que el SQL HACE: eso lo afirma la suite
// integrationshelpertest.Contrato contra Postgres, en test/procesos. Aquí se afirma lo que se ve
// sin ella: qué sentencia se emite y con qué argumentos, y cómo se mapean filas y errores.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Aserciones de compilación de lo que postgres.go promete: el constructor, los diez métodos del
// puerto y los dos que no son del puerto (D-F6-6).
var (
	_ func(*sql.DB, *crypto.FieldCipher) *integrations.Postgres = integrations.NewPostgres

	_ func(*integrations.Postgres, context.Context, string, string, json.RawMessage) (int64, error)       = (*integrations.Postgres).EnqueueWebhook
	_ func(*integrations.Postgres, context.Context, int) ([]integrations.WebhookOutbox, error)            = (*integrations.Postgres).ClaimWebhookBatch
	_ func(*integrations.Postgres, context.Context, integrations.WebhookOutbox) error                     = (*integrations.Postgres).MarkWebhookDelivered
	_ func(*integrations.Postgres, context.Context, integrations.WebhookOutbox, time.Time, string) error  = (*integrations.Postgres).MarkWebhookFailed
	_ func(*integrations.Postgres, context.Context, integrations.WebhookOutbox, string) error             = (*integrations.Postgres).MarkWebhookDead
	_ func(*integrations.Postgres, context.Context, time.Duration) (int, error)                           = (*integrations.Postgres).RecoverOrphanDeliveries
	_ func(*integrations.Postgres, context.Context, string) (integrations.TenantIntegration, bool, error) = (*integrations.Postgres).GetTenantIntegration
	_ func(*integrations.Postgres, context.Context, string) (string, bool, error)                         = (*integrations.Postgres).GetTenantSecret
	_ func(*integrations.Postgres, context.Context, integrations.TenantIntegration, string) error         = (*integrations.Postgres).UpsertTenantIntegration
	_ func(*integrations.Postgres, context.Context, string) error                                         = (*integrations.Postgres).DeleteTenantIntegration
	_ func(*integrations.Postgres, context.Context, string) (string, bool, error)                         = (*integrations.Postgres).SecretFingerprint
	_ func(*integrations.Postgres, context.Context, string) (integrations.OutboxCounts, error)            = (*integrations.Postgres).CountOutbox
)

// Material de clave de prueba. 🔴 Homónimo: esta KEK y la «DEK» que envuelve son las del envelope de
// dato de negocio (crypto.FieldCipher), NO la DEK del ADR-0007. Dos KEK en el keyring: una
// rotación a medias.
const (
	pgKEKOldID  = "test-kek-old"
	pgKEKNewID  = "test-kek-new"
	pgKEKOldB64 = "ERERERERERERERERERERERERERERERERERERERERERE="
	pgKEKNewB64 = "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI="
	pgIndexB64  = "RERERERERERERERERERERERERERERERERERERERERES="
	pgKeyring   = pgKEKOldID + ":" + pgKEKOldB64 + "," + pgKEKNewID + ":" + pgKEKNewB64

	pgTenant = "tenant-del-adaptador"
	// pgSecret es un secreto RARO a propósito: se busca como subcadena en argumentos y errores.
	pgSecret = "secreto-de-firma-del-puente-jjx-2026" // #nosec G101 -- material de test
)

// errDB es el fallo de la base en los tests del adaptador.
var errDB = errors.New("base caída")

// newCipher arma un cifrador de campo de verdad sobre el keyring de prueba, con currentID como la
// KEK que envuelve.
func newCipher(t *testing.T, currentID string) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{KeyringB64: pgKeyring, CurrentID: currentID, IndexB64: pgIndexB64})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// newPostgres monta el adaptador sobre el driver de mentira, con la KEK nueva como current.
func newPostgres(t *testing.T) (*integrations.Postgres, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	return integrations.NewPostgres(fake.open(t), newCipher(t, pgKEKNewID)), fake
}

// only devuelve la ÚNICA sentencia que llegó al driver, comprobando su clase y su texto.
func only(t *testing.T, fake *fakeDB, kind, query string) event {
	t.Helper()
	seen := fake.seen()
	if len(seen) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(seen), seen)
	}
	if seen[0].kind != kind {
		t.Errorf("la sentencia llegó como %q, quería %q", seen[0].kind, kind)
	}
	if seen[0].query != query {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", seen[0].query, query)
	}
	return seen[0]
}

// requireArgs afirma los argumentos de la sentencia, en orden.
func requireArgs(t *testing.T, got []driver.Value, want ...driver.Value) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("la sentencia lleva %d argumentos, quería %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !sameValue(got[i], want[i]) {
			t.Errorf("argumento $%d = %#v, quería %#v", i+1, got[i], want[i])
		}
	}
}

// sameValue compara dos valores de driver: los []byte por su contenido y los instantes con Equal.
func sameValue(got, want driver.Value) bool {
	switch w := want.(type) {
	case []byte:
		g, ok := got.([]byte)
		return ok && string(g) == string(w)
	case time.Time:
		g, ok := got.(time.Time)
		return ok && g.Equal(w)
	default:
		return got == want
	}
}

// requireWrapped afirma que err es EXACTAMENTE prefix + el texto de la causa, y que la envuelve.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, quería un error con el prefijo %q", prefix)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %q no envuelve la causa %q", err, cause)
	}
	if want := prefix + cause.Error(); err.Error() != want {
		t.Errorf("texto del error =\n%s\nquería, byte a byte:\n%s", err, want)
	}
}

// TestNewPostgres_DoesNotQuery: construir el store no toca la base.
func TestNewPostgres_DoesNotQuery(t *testing.T) {
	store, fake := newPostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el store emitió %d sentencias, quería 0: %+v", len(seen), seen)
	}
}

// TestPostgres_EnqueueWebhook_EmitsTheInsert: encolar es UNA sentencia, la literal, con el tenant,
// el verbo y el payload como bytes; devuelve el id que da la base.
func TestPostgres_EnqueueWebhook_EmitsTheInsert(t *testing.T) {
	store, fake := newPostgres(t)
	fake.script(reply{rows: [][]driver.Value{{int64(41)}}})
	id, err := store.EnqueueWebhook(context.Background(), pgTenant, "intake.push", json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: error inesperado %v", err)
	}
	if id != 41 {
		t.Errorf("id = %d, quería el que devolvió la base (41)", id)
	}
	got := only(t, fake, eventQuery, enqueueSQL)
	requireArgs(t, got.args, pgTenant, "intake.push", []byte(`{"n":1}`))
}

// TestPostgres_EnqueueWebhook_Error: el fallo sale con su prefijo, que nombra el verbo, y el id es
// cero.
func TestPostgres_EnqueueWebhook_Error(t *testing.T) {
	store, fake := newPostgres(t)
	fake.script(reply{err: errDB})
	id, err := store.EnqueueWebhook(context.Background(), pgTenant, "intake.push", json.RawMessage(`{}`))
	requireWrapped(t, err, errDB, "integrations: encolar entrega de intake.push: ")
	if id != 0 {
		t.Errorf("id = %d con error, quería 0", id)
	}
}

// claimedRow es una fila del RETURNING del reclamo, en el orden de sus diez columnas.
func claimedRow(id int64, tenant string, attempts int64, lastError string, next, created time.Time, claimedAt any) []driver.Value {
	return []driver.Value{id, tenant, "intake.push", []byte(`{"n":1}`), "delivering", attempts, next, created, lastError, claimedAt}
}

// TestPostgres_ClaimWebhookBatch_EmitsTheStatement: reclamar es UNA sentencia, la literal, con el
// estado al que pasa, el estado que busca y el límite, en ese orden.
func TestPostgres_ClaimWebhookBatch_EmitsTheStatement(t *testing.T) {
	store, fake := newPostgres(t)
	if _, err := store.ClaimWebhookBatch(context.Background(), 20); err != nil {
		t.Fatalf("ClaimWebhookBatch: error inesperado %v", err)
	}
	got := only(t, fake, eventQuery, claimSQL)
	requireArgs(t, got.args, "delivering", "pending", int64(20))
}

// TestPostgres_ClaimWebhookBatch_NoRows: sin filas, un lote vacío y ningún error.
func TestPostgres_ClaimWebhookBatch_NoRows(t *testing.T) {
	store, _ := newPostgres(t)
	batch, err := store.ClaimWebhookBatch(context.Background(), 20)
	if err != nil || len(batch) != 0 {
		t.Errorf("ClaimWebhookBatch sin filas = (%+v, %v), quería un lote vacío sin error", batch, err)
	}
}

// TestPostgres_ClaimWebhookBatch_MapsTheRows: cada fila sale con sus diez columnas en su sitio y
// en el orden en que las da la base; un claimed_at NULL es el instante cero y un last_error vacío
// (el COALESCE del SQL) es «nunca falló».
func TestPostgres_ClaimWebhookBatch_MapsTheRows(t *testing.T) {
	store, fake := newPostgres(t)
	created := time.Unix(1_700_000_000, 0).UTC()
	next, sealed := created.Add(time.Minute), created.Add(2*time.Minute)
	fake.script(reply{rows: [][]driver.Value{
		claimedRow(9, "tenant-b", 3, "respuesta 500 del puente", next, created, sealed),
		claimedRow(4, "tenant-a", 0, "", created, created, nil),
	}})
	batch, err := store.ClaimWebhookBatch(context.Background(), 20)
	if err != nil {
		t.Fatalf("ClaimWebhookBatch: error inesperado %v", err)
	}
	want := []integrations.WebhookOutbox{
		{ID: 9, TenantID: "tenant-b", Kind: "intake.push", Payload: json.RawMessage(`{"n":1}`), Status: "delivering",
			Attempts: 3, NextAttemptAt: next, CreatedAt: created, LastError: "respuesta 500 del puente", ClaimedAt: sealed},
		{ID: 4, TenantID: "tenant-a", Kind: "intake.push", Payload: json.RawMessage(`{"n":1}`), Status: "delivering",
			NextAttemptAt: created, CreatedAt: created},
	}
	if len(batch) != len(want) {
		t.Fatalf("el lote trae %d filas, quería %d: %+v", len(batch), len(want), batch)
	}
	for i := range want {
		requireSameClaim(t, i, batch[i], want[i])
	}
}

// requireSameClaim compara una fila del lote con la esperada, campo a campo.
func requireSameClaim(t *testing.T, i int, got, want integrations.WebhookOutbox) {
	t.Helper()
	if got.ID != want.ID || got.TenantID != want.TenantID || got.Kind != want.Kind || got.Status != want.Status ||
		got.Attempts != want.Attempts || got.LastError != want.LastError || string(got.Payload) != string(want.Payload) {
		t.Errorf("fila %d = %+v, quería %+v", i, got, want)
	}
	if !got.NextAttemptAt.Equal(want.NextAttemptAt) || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("fila %d: (next_attempt_at, created_at) = (%v, %v), quería (%v, %v)",
			i, got.NextAttemptAt, got.CreatedAt, want.NextAttemptAt, want.CreatedAt)
	}
	if !got.ClaimedAt.Equal(want.ClaimedAt) || got.ClaimedAt.IsZero() != want.ClaimedAt.IsZero() {
		t.Errorf("fila %d: claimed_at = %v, quería %v (NULL es el instante cero)", i, got.ClaimedAt, want.ClaimedAt)
	}
}

// TestPostgres_ClaimWebhookBatch_Errors: cada fallo sale con su prefijo y SIN filas a medias (el
// lote devuelto es nil), también cuando la primera fila ya se había leído bien.
func TestPostgres_ClaimWebhookBatch_Errors(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	good := claimedRow(1, "tenant-a", 0, "", at, at, at)
	bad := claimedRow(2, "tenant-a", 0, "", at, at, at)
	bad[5] = "no soy un número" // attempts
	cases := []struct {
		name   string
		reply  reply
		prefix string
		wraps  bool
	}{
		{"the statement fails", reply{err: errDB}, "integrations: reclamar lote: ", true},
		{"a row cannot be scanned", reply{rows: [][]driver.Value{good, bad}}, "integrations: escanear fila del lote: ", false},
		{"the iteration fails", reply{rows: [][]driver.Value{good}, endErr: errDB}, "integrations: iterar lote: ", true},
		{"closing the rows fails", reply{rows: [][]driver.Value{good}, closeErr: errDB}, "integrations: iterar lote: ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t)
			fake.script(c.reply)
			batch, err := store.ClaimWebhookBatch(context.Background(), 20)
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Fatalf("err = %v, quería el prefijo %q", err, c.prefix)
			}
			if c.wraps {
				requireWrapped(t, err, errDB, c.prefix)
			}
			if batch != nil {
				t.Errorf("con error el lote trae %d filas, quería nil (sin filas a medias)", len(batch))
			}
		})
	}
}

// TestPostgres_ClaimWebhookBatch_ScanErrorThenCloseError_WarnsAndKeepsTheScanError: si tras un
// error de escaneo además falla el cierre de las filas, el error devuelto sigue siendo el del
// escaneo y el del cierre no se calla (T-13): queda en el log estándar con su texto literal.
func TestPostgres_ClaimWebhookBatch_ScanErrorThenCloseError_WarnsAndKeepsTheScanError(t *testing.T) {
	var out strings.Builder
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	at := time.Unix(1_700_000_000, 0).UTC()
	bad := claimedRow(2, "tenant-a", 0, "", at, at, at)
	bad[5] = "no soy un número" // attempts
	store, fake := newPostgres(t)
	fake.script(reply{rows: [][]driver.Value{bad}, closeErr: errDB})

	batch, err := store.ClaimWebhookBatch(context.Background(), 20)
	if err == nil || !strings.HasPrefix(err.Error(), "integrations: escanear fila del lote: ") {
		t.Fatalf("err = %v, quería el del escaneo", err)
	}
	if errors.Is(err, errDB) {
		t.Errorf("el error devuelto envuelve el del cierre (%v): tenía que ser el del escaneo", err)
	}
	if batch != nil {
		t.Errorf("con error el lote trae %d filas, quería nil", len(batch))
	}
	if want := "[wapp][integrations][WARN] claim: cerrar filas tras error de escaneo: base caída\n"; out.String() != want {
		t.Errorf("log estándar =\n%q\nquería, byte a byte:\n%q", out.String(), want)
	}
}
