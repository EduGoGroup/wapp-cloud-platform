package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Aserciones de compilación: los dos adaptadores leen el contenido y lo versionan. El versionado
// NO entra en Repository a propósito: solo lo necesita el import de catálogo.
var (
	_ store.TenantContentReader    = (*store.MemoryRepository)(nil)
	_ store.TenantContentReader    = (*store.PostgresRepository)(nil)
	_ store.TenantContentVersioner = (*store.MemoryRepository)(nil)
	_ store.TenantContentVersioner = (*store.PostgresRepository)(nil)
	_ store.TenantContentReader    = store.Repository(nil)
)

// TestVersionSources_MirrorTheCheckOfTheMigration: las tres procedencias son exactamente los
// literales del CHECK de la migración 0044. Si aquí cambiara una, Postgres la rechazaría y el
// gemelo en memoria no.
func TestVersionSources_MirrorTheCheckOfTheMigration(t *testing.T) {
	cases := map[string]string{
		store.VersionSourceImportJSON:    "import_json",
		store.VersionSourceImportTabular: "import_tabular",
		store.VersionSourceManual:        "manual",
	}
	if len(cases) != 3 {
		t.Fatalf("las tres procedencias no son distintas entre sí: %v", cases)
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("procedencia = %q, quería %q", got, want)
		}
	}
}

// TestTenantContentSentinels: los dos centinelas tienen su texto literal, son distintos y se
// reconocen con errors.Is a través de una envoltura.
func TestTenantContentSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		text string
	}{
		{"ErrTenantContentNotFound", store.ErrTenantContentNotFound, "contenido de tenant no encontrado"},
		{"ErrInvalidVersionSource", store.ErrInvalidVersionSource, "procedencia de versión de contenido inválida"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.text {
				t.Errorf("texto = %q, quería %q", got, tc.text)
			}
			if wrapped := fmt.Errorf("%w: detalle", tc.err); !errors.Is(wrapped, tc.err) {
				t.Error("errors.Is no reconoce el centinela envuelto")
			}
		})
	}
	if errors.Is(store.ErrTenantContentNotFound, store.ErrInvalidVersionSource) {
		t.Error("los dos centinelas del contenido se confunden entre sí")
	}
}

// TestTenantContentTypes_CarryTheirFields: la cabecera de un blob y una versión archivada llevan
// los campos que el puerto promete.
func TestTenantContentTypes_CarryTheirFields(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	summary := store.TenantContentSummary{Ref: "catalogo", CreatedAt: at, UpdatedAt: at.Add(time.Hour)}
	version := store.TenantContentVersion{Version: 3, Content: []byte(`{"v":2}`), Source: store.VersionSourceManual, CreatedAt: at}

	if summary.Ref != "catalogo" || !summary.CreatedAt.Equal(at) || !summary.UpdatedAt.After(summary.CreatedAt) {
		t.Errorf("TenantContentSummary = %+v", summary)
	}
	if version.Version != 3 || string(version.Content) != `{"v":2}` || version.Source != "manual" || !version.CreatedAt.Equal(at) {
		t.Errorf("TenantContentVersion = %+v", version)
	}
}
