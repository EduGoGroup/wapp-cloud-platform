//go:build pendiente

package intentcfg_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

// Aserciones de compilación de lo que PostgresStore promete: las firmas del puerto y un
// constructor que recibe el *sql.DB y no devuelve error.
var (
	_ func(*sql.DB) *intentcfg.PostgresStore                                            = intentcfg.NewPostgresStore
	_ func(*intentcfg.PostgresStore, context.Context, string) (intentcfg.Config, error) = (*intentcfg.PostgresStore).Get
	_ func(*intentcfg.PostgresStore, context.Context, string, string, []byte) error     = (*intentcfg.PostgresStore).Upsert
)

// Las dos sentencias del adaptador, byte a byte (con su sangría): son las del fichero viejo.
const (
	getSQL = `
		SELECT version, config::text, updated_at
		FROM public.intent_configs
		WHERE tenant_id = $1
	`
	upsertSQL = `
		INSERT INTO public.intent_configs (tenant_id, version, config, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (tenant_id) DO UPDATE
		SET version = EXCLUDED.version, config = EXCLUDED.config, updated_at = now()
	`
)

// newPostgresStore monta el adaptador sobre el driver de mentira.
func newPostgresStore(t *testing.T) (*intentcfg.PostgresStore, *fakeDB) {
	t.Helper()
	fake := &fakeDB{}
	return intentcfg.NewPostgresStore(fake.open(t)), fake
}

// TestNewPostgresStore_DoesNotQuery: construir el store no toca la base.
func TestNewPostgresStore_DoesNotQuery(t *testing.T) {
	store, fake := newPostgresStore(t)
	if store == nil {
		t.Fatal("NewPostgresStore devolvió nil")
	}
	if seen := fake.seen(); len(seen) != 0 {
		t.Errorf("construir el store emitió %d sentencias, quería 0: %+v", len(seen), seen)
	}
}

// TestPostgresStore_Get_EmitsTheQuery: un Get es UNA consulta, la literal, con el tenant como
// único argumento.
func TestPostgresStore_Get_EmitsTheQuery(t *testing.T) {
	store, fake := newPostgresStore(t)
	fake.script(reply{rows: [][]driver.Value{{"abc123", `{}`, time.Unix(1_700_000_000, 0).UTC()}}})
	if _, err := store.Get(context.Background(), "tenant-1"); err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	got := requireOne(t, fake, eventQuery)
	if got.query != getSQL {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", got.query, getSQL)
	}
	if len(got.args) != 1 || got.args[0] != "tenant-1" {
		t.Errorf("argumentos de Get = %v, quería [tenant-1]", got.args)
	}
}

// TestPostgresStore_Get_MapsTheRow: la fila sale con sus tres columnas en su sitio, y el blob es
// el texto del JSONB tal como lo da la base (el adaptador no lo reescribe), llegue como texto o
// como bytes.
func TestPostgresStore_Get_MapsTheRow(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	// Así canonicaliza Postgres un JSONB: un espacio tras ':' y tras ','.
	const canonical = `{"intents": [{"name": "x"}], "version": "v1"}`
	columns := map[string]driver.Value{"text": canonical, "bytes": []byte(canonical)}
	for name, column := range columns {
		t.Run(name, func(t *testing.T) {
			store, fake := newPostgresStore(t)
			fake.script(reply{rows: [][]driver.Value{{"abc123", column, at}}})
			got, err := store.Get(context.Background(), "tenant-1")
			if err != nil {
				t.Fatalf("Get: error inesperado %v", err)
			}
			if got.Version != "abc123" || string(got.Blob) != canonical || !got.UpdatedAt.Equal(at) {
				t.Errorf("Get = (%q, %s, %v), quería (abc123, %s, %v)", got.Version, got.Blob, got.UpdatedAt, canonical, at)
			}
		})
	}
}

// TestPostgresStore_Get_NoRow_ErrNotFoundWithTheTenant: sin fila, ErrNotFound envuelto con el
// tenant —el texto es el del fichero viejo— y un Config cero.
func TestPostgresStore_Get_NoRow_ErrNotFoundWithTheTenant(t *testing.T) {
	store, _ := newPostgresStore(t)
	got, err := store.Get(context.Background(), "tenant-1")
	if !errors.Is(err, intentcfg.ErrNotFound) {
		t.Fatalf("Get sin fila: err=%v, quería uno que cumpla errors.Is(ErrNotFound)", err)
	}
	if err.Error() != "config de intents no encontrada: tenant=tenant-1" {
		t.Errorf("Get sin fila: texto %q, quería \"config de intents no encontrada: tenant=tenant-1\"", err)
	}
	requireZeroConfig(t, got)
}

// TestPostgresStore_Get_Errors: un fallo de la consulta o del escaneo sale envuelto con el
// prefijo del fichero viejo, NO es un «no encontrada» y no deja un Config a medias.
func TestPostgresStore_Get_Errors(t *testing.T) {
	cause := errors.New("boom")
	t.Run("query_fails", func(t *testing.T) {
		store, fake := newPostgresStore(t)
		fake.script(reply{err: cause})
		got, err := store.Get(context.Background(), "tenant-1")
		requireWrapped(t, err, cause, "intentcfg: leer config: ")
		requireZeroConfig(t, got)
	})
	t.Run("row_cannot_be_scanned", func(t *testing.T) {
		store, fake := newPostgresStore(t)
		// updated_at llega como un texto que no es un instante: el escaneo falla.
		fake.script(reply{rows: [][]driver.Value{{"abc123", `{}`, "not-a-timestamp"}}})
		got, err := store.Get(context.Background(), "tenant-1")
		if err == nil || !strings.HasPrefix(err.Error(), "intentcfg: leer config: ") {
			t.Fatalf("Get con una fila ilegible: err=%v, quería el prefijo \"intentcfg: leer config: \"", err)
		}
		if errors.Is(err, intentcfg.ErrNotFound) {
			t.Errorf("Get con una fila ilegible devolvió ErrNotFound (%v): una fila rota no es «sin config»", err)
		}
		requireZeroConfig(t, got)
	})
}

// TestPostgresStore_Upsert_EmitsTheStatement: un Upsert es UNA sentencia, la literal, con el
// tenant, la version y el blob tal cual, en ese orden.
func TestPostgresStore_Upsert_EmitsTheStatement(t *testing.T) {
	store, fake := newPostgresStore(t)
	blob := []byte(`{ "version" : "v1" }`)
	if err := store.Upsert(context.Background(), "tenant-1", "abc123", blob); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	got := requireOne(t, fake, eventExec)
	if got.query != upsertSQL {
		t.Errorf("SQL emitido:\n%q\nquería:\n%q", got.query, upsertSQL)
	}
	if len(got.args) != 3 {
		t.Fatalf("Upsert mandó %d argumentos, quería 3: %v", len(got.args), got.args)
	}
	sent, isBytes := got.args[2].([]byte)
	if got.args[0] != "tenant-1" || got.args[1] != "abc123" || !isBytes || string(sent) != string(blob) {
		t.Errorf("argumentos de Upsert = %v, quería [tenant-1 abc123 %s]", got.args, blob)
	}
}

// TestPostgresStore_Upsert_Error: un fallo de la sentencia (también el de un blob que la base
// rechaza) sale envuelto con el prefijo del fichero viejo.
func TestPostgresStore_Upsert_Error(t *testing.T) {
	cause := errors.New("invalid input syntax for type json")
	store, fake := newPostgresStore(t)
	fake.script(reply{err: cause})
	err := store.Upsert(context.Background(), "tenant-1", "abc123", []byte("esto no es JSON"))
	requireWrapped(t, err, cause, "intentcfg: upsert config: ")
}

// requireOne exige que al driver le llegara UNA sola sentencia, de la clase pedida, y la devuelve.
func requireOne(t *testing.T, fake *fakeDB, kind string) event {
	t.Helper()
	seen := fake.seen()
	if len(seen) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(seen), seen)
	}
	if seen[0].kind != kind {
		t.Fatalf("llegó un %q, quería un %q", seen[0].kind, kind)
	}
	return seen[0]
}

// requireWrapped exige que err envuelva la causa (%w) y que su texto sea el prefijo más el de la
// causa.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, quería uno que envuelva %v", err, cause)
	}
	if want := prefix + cause.Error(); err.Error() != want {
		t.Errorf("texto del error %q, quería %q", err, want)
	}
	if errors.Is(err, intentcfg.ErrNotFound) {
		t.Errorf("err = %v cumple errors.Is(ErrNotFound), y un fallo de la base no es «sin config»", err)
	}
}

// requireZeroConfig exige el Config cero que acompaña a todo error de Get.
func requireZeroConfig(t *testing.T, got intentcfg.Config) {
	t.Helper()
	if got.Version != "" || got.Blob != nil || !got.UpdatedAt.IsZero() {
		t.Errorf("Get con error devolvió %+v, quería un Config cero", got)
	}
}
