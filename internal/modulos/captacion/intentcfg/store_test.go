//go:build pendiente

package intentcfg_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg/intentcfghelpertest"
)

// Este test es EXTERNO (package intentcfg_test): intentcfghelpertest importa intentcfg, así que
// un test interno que importara la suite daría un ciclo de imports.

// Aserciones de compilación: las dos implementaciones del puerto lo son, y MemoryStore tiene un
// constructor sin argumentos ni error.
var (
	_ intentcfg.Store               = (*intentcfg.MemoryStore)(nil)
	_ intentcfg.Store               = (*intentcfg.PostgresStore)(nil)
	_ func() *intentcfg.MemoryStore = intentcfg.NewMemoryStore

	_ func(*intentcfg.MemoryStore, context.Context, string) (intentcfg.Config, error) = (*intentcfg.MemoryStore).Get
	_ func(*intentcfg.MemoryStore, context.Context, string, string, []byte) error     = (*intentcfg.MemoryStore).Upsert
)

// TestKind_IsTheWireLiteral: el kind viaja en el ConfigUpdate hacia el Edge; su valor es contrato.
func TestKind_IsTheWireLiteral(t *testing.T) {
	if intentcfg.Kind != "intents" {
		t.Errorf("Kind = %q, quería \"intents\"", intentcfg.Kind)
	}
}

// TestErrNotFound_TextAndIdentity: el texto es el del paquete viejo, y se reconoce con errors.Is
// también envuelto (como lo devuelve el Postgres).
func TestErrNotFound_TextAndIdentity(t *testing.T) {
	if got := intentcfg.ErrNotFound.Error(); got != "config de intents no encontrada" {
		t.Errorf("ErrNotFound = %q, quería \"config de intents no encontrada\"", got)
	}
	wrapped := fmt.Errorf("%w: tenant=%s", intentcfg.ErrNotFound, "tenant-1")
	if !errors.Is(wrapped, intentcfg.ErrNotFound) {
		t.Errorf("errors.Is(%v, ErrNotFound) = false, quería true", wrapped)
	}
}

// TestConfig_CarriesVersionBlobAndMark: un Config es la version de entidad, el blob y su marca.
func TestConfig_CarriesVersionBlobAndMark(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	c := intentcfg.Config{Version: "abc123", Blob: []byte(`{"version":"v1"}`), UpdatedAt: at}
	if c.Version != "abc123" || string(c.Blob) != `{"version":"v1"}` || !c.UpdatedAt.Equal(at) {
		t.Errorf("Config = %+v, quería (abc123, {\"version\":\"v1\"}, %v)", c, at)
	}
}

// recordingStore es un Store mínimo: apunta lo que recibe y devuelve lo que se le sembró.
type recordingStore struct {
	config                  intentcfg.Config
	getTenant               string
	upsertTenant, upsertVer string
	upsertBlob              []byte
}

func (s *recordingStore) Get(_ context.Context, tenantID string) (intentcfg.Config, error) {
	s.getTenant = tenantID
	return s.config, nil
}

func (s *recordingStore) Upsert(_ context.Context, tenantID, version string, blob []byte) error {
	s.upsertTenant, s.upsertVer, s.upsertBlob = tenantID, version, blob
	return nil
}

// TestStore_IsUsableThroughThePort: un consumidor que solo conoce el puerto lee y guarda por él,
// con el tenant como argumento de cada llamada (INV-8).
func TestStore_IsUsableThroughThePort(t *testing.T) {
	impl := &recordingStore{config: intentcfg.Config{Version: "abc123"}}
	var store intentcfg.Store = impl

	got, err := store.Get(context.Background(), "tenant-1")
	if err != nil || got.Version != "abc123" || impl.getTenant != "tenant-1" {
		t.Errorf("Get = (%+v, %v) con el tenant %q; quería la config sembrada y tenant-1", got, err, impl.getTenant)
	}
	if err := store.Upsert(context.Background(), "tenant-2", "def456", []byte(`{}`)); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	if impl.upsertTenant != "tenant-2" || impl.upsertVer != "def456" || string(impl.upsertBlob) != `{}` {
		t.Errorf("Upsert llegó con (%q, %q, %s); quería (tenant-2, def456, {})", impl.upsertTenant, impl.upsertVer, impl.upsertBlob)
	}
}

// memoryCases numera los Montajes para que cada caso tenga sus dos tenants.
var memoryCases atomic.Int64

// waitForTheClock espera a que el reloj del proceso —el que usa MemoryStore, que no admite otro—
// pase del instante en que estaba. No duerme: time.Now() lleva lectura monótona y avanza solo.
func waitForTheClock(*testing.T) {
	for start := time.Now(); !time.Now().After(start); {
	}
}

// TestMemoryStore_Contrato corre la suite del puerto contra MemoryStore, sin BD: cada caso monta
// un store nuevo.
func TestMemoryStore_Contrato(t *testing.T) {
	intentcfghelpertest.Contrato(t, func(*testing.T) intentcfghelpertest.Montaje {
		n := memoryCases.Add(1)
		return intentcfghelpertest.Montaje{
			Store:   intentcfg.NewMemoryStore(),
			TenantA: fmt.Sprintf("tenant-%02d-a", n),
			TenantB: fmt.Sprintf("tenant-%02d-b", n),
			Advance: waitForTheClock,
		}
	})
}

// TestNewMemoryStore_IsEmpty: recién construido ningún tenant tiene config, y dos stores no
// comparten estado.
func TestNewMemoryStore_IsEmpty(t *testing.T) {
	ctx := context.Background()
	first, second := intentcfg.NewMemoryStore(), intentcfg.NewMemoryStore()
	if first == nil || second == nil {
		t.Fatal("NewMemoryStore devolvió nil")
	}
	if err := first.Upsert(ctx, "tenant-1", "abc123", []byte(`{}`)); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	if _, err := second.Get(ctx, "tenant-1"); !errors.Is(err, intentcfg.ErrNotFound) {
		t.Errorf("Get en un store recién construido: err=%v, quería ErrNotFound", err)
	}
}

// TestMemoryStore_Get_NotFound_IsTheBareSentinel: sin config devuelve ErrNotFound tal cual, sin
// envolver (el Postgres sí lo envuelve; por eso el puerto pide errors.Is).
func TestMemoryStore_Get_NotFound_IsTheBareSentinel(t *testing.T) {
	_, err := intentcfg.NewMemoryStore().Get(context.Background(), "tenant-x")
	//nolint:errorlint // se afirma la identidad a propósito: el contrato dice «sin envolver».
	if err != intentcfg.ErrNotFound {
		t.Errorf("Get sobre un store vacío: err=%v, quería ErrNotFound sin envolver", err)
	}
}

// TestMemoryStore_KeepsTheBlobByteForByte: a diferencia del Postgres (JSONB), el de memoria no
// interpreta el blob: lo devuelve byte a byte, sea JSON con espacios, texto que no es JSON o
// vacío. Un blob nil vuelve vacío.
func TestMemoryStore_KeepsTheBlobByteForByte(t *testing.T) {
	cases := []struct {
		name string
		blob []byte
	}{
		{"compact_json", []byte(`{"version":"v1"}`)},
		{"json_with_spaces_and_unordered_keys", []byte("{ \"b\" : 2,\n\t\"a\" : 1 }")},
		{"not_json", []byte("esto no es JSON")},
		{"binary", []byte{0x00, 0xff, 0x7b}},
		{"empty", []byte{}},
		{"nil", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			store := intentcfg.NewMemoryStore()
			if err := store.Upsert(ctx, "tenant-1", "abc123", c.blob); err != nil {
				t.Fatalf("Upsert: error inesperado %v", err)
			}
			got, err := store.Get(ctx, "tenant-1")
			if err != nil {
				t.Fatalf("Get: error inesperado %v", err)
			}
			if !bytes.Equal(got.Blob, c.blob) {
				t.Errorf("Blob = %q, quería %q byte a byte", got.Blob, c.blob)
			}
		})
	}
}

// TestMemoryStore_Upsert_MarksWithTheProcessClock: UpdatedAt es el time.Now() del Upsert, así que
// cae entre el instante de antes y el de después de la llamada.
func TestMemoryStore_Upsert_MarksWithTheProcessClock(t *testing.T) {
	ctx := context.Background()
	store := intentcfg.NewMemoryStore()
	before := time.Now()
	if err := store.Upsert(ctx, "tenant-1", "abc123", []byte(`{}`)); err != nil {
		t.Fatalf("Upsert: error inesperado %v", err)
	}
	after := time.Now()
	got, err := store.Get(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt = %v, quería un instante entre %v y %v", got.UpdatedAt, before, after)
	}
}

// TestMemoryStore_ConcurrentUse: varias goroutines escriben y leen a la vez sin carrera (lo mira
// -race) y, al acabar, cada tenant tiene la config que se le guardó.
func TestMemoryStore_ConcurrentUse(t *testing.T) {
	const tenants, rounds = 8, 50
	ctx := context.Background()
	store := intentcfg.NewMemoryStore()
	var wg sync.WaitGroup
	for i := range tenants {
		tenant, blob := fmt.Sprintf("tenant-%d", i), []byte(fmt.Sprintf(`{"n":%d}`, i))
		wg.Go(func() {
			for range rounds {
				if err := store.Upsert(ctx, tenant, tenant+"-hash", blob); err != nil {
					t.Errorf("Upsert(%s): error inesperado %v", tenant, err)
				}
				if _, err := store.Get(ctx, tenant); err != nil {
					t.Errorf("Get(%s): error inesperado %v", tenant, err)
				}
			}
		})
	}
	wg.Wait()
	for i := range tenants {
		tenant := fmt.Sprintf("tenant-%d", i)
		got, err := store.Get(ctx, tenant)
		if err != nil {
			t.Fatalf("Get(%s): error inesperado %v", tenant, err)
		}
		if got.Version != tenant+"-hash" || string(got.Blob) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Errorf("config de %s = (%q, %s), quería la suya", tenant, got.Version, got.Blob)
		}
	}
}
