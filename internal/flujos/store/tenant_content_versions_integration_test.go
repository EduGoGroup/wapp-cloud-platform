package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// cleanupVersions borra las filas archivadas del tenant. No hay método de
// repositorio para esto —el archivo es append-only por diseño— así que el test
// limpia lo suyo por SQL directo.
func cleanupVersions(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM public.tenant_content_versions WHERE tenant_id = $1`, tenantID); err != nil {
		t.Logf("cleanup de versiones (%s): %v", tenantID, err)
	}
}

// versionRows lee las versiones archivadas de (tenant, ref) ordenadas por número.
func versionRows(t *testing.T, db *sql.DB, tenantID, ref string) []store.TenantContentVersion {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT version, content::text, source, created_at
		FROM public.tenant_content_versions
		WHERE tenant_id = $1 AND ref = $2
		ORDER BY version
	`, tenantID, ref)
	if err != nil {
		t.Fatalf("leyendo versiones: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas: %v", cerr)
		}
	}()
	var out []store.TenantContentVersion
	for rows.Next() {
		var v store.TenantContentVersion
		var content string
		if err := rows.Scan(&v.Version, &content, &v.Source, &v.CreatedAt); err != nil {
			t.Fatalf("escaneando versión: %v", err)
		}
		v.Content = []byte(content)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando versiones: %v", err)
	}
	return out
}

// TestIntegration_ReplaceTenantContentVersioned ejercita contra Postgres real el
// acto atómico del import (Plan 041 · T3.3, D-041.8): archivar lo vigente y
// escribir lo nuevo. Es el ÚNICO camino que ejecuta de verdad la transacción, el
// FOR UPDATE, el MAX(version)+1 y la tabla de la migración 0044; el repositorio en
// memoria imita la semántica, pero no puede demostrar el SQL.
func TestIntegration_ReplaceTenantContentVersioned(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	tenantID := fmt.Sprintf("tcv-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupContent(t, repo, tenantID, "catalogo")
		cleanupVersions(t, db, tenantID)
	})

	// 1) Ref vacía: escribe y NO archiva (la versión 1 nace del segundo acto).
	if archived := mustReplaceVersioned(t, repo, tenantID, "v1", store.VersionSourceImportJSON); archived != 0 {
		t.Fatalf("archived=%d en el primer import, quiero 0: sin blob vigente no hay nada que archivar", archived)
	}
	if rows := versionRows(t, db, tenantID, "catalogo"); len(rows) != 0 {
		t.Fatalf("el primer import dejó %d versiones", len(rows))
	}

	// 2) Segundo import: archiva lo anterior como versión 1 y deja vigente lo nuevo.
	if archived := mustReplaceVersioned(t, repo, tenantID, "v2", store.VersionSourceImportJSON); archived != 1 {
		t.Fatalf("archived=%d, quiero 1", archived)
	}
	current, err := repo.GetTenantContent(ctx, tenantID, "catalogo")
	if err != nil {
		t.Fatalf("leyendo el vigente: %v", err)
	}
	if !strings.Contains(string(current), `"v2"`) {
		t.Fatalf("el vigente no es el nuevo: %s", current)
	}
	rows := versionRows(t, db, tenantID, "catalogo")
	if len(rows) != 1 || rows[0].Version != 1 {
		t.Fatalf("versiones=%+v, quiero solo la 1", rows)
	}
	if !strings.Contains(string(rows[0].Content), `"v1"`) {
		t.Fatalf("la versión 1 archivó el blob NUEVO en vez del viejo: %s", rows[0].Content)
	}
	if rows[0].Source != store.VersionSourceImportJSON || rows[0].CreatedAt.IsZero() {
		t.Fatalf("metadatos de la versión = %+v", rows[0])
	}

	// 3) Tercer import: el correlativo sigue, no se reinicia ni choca con la PK.
	if archived := mustReplaceVersioned(t, repo, tenantID, "v3", store.VersionSourceImportTabular); archived != 2 {
		t.Fatalf("archived=%d, quiero 2", archived)
	}
	if rows = versionRows(t, db, tenantID, "catalogo"); len(rows) != 2 || rows[1].Source != store.VersionSourceImportTabular {
		t.Fatalf("versiones=%+v, quiero 1 y 2 con la procedencia de cada acto", rows)
	}
}

// mustReplaceVersioned aplica un catálogo mínimo reconocible por su etiqueta y
// devuelve la versión archivada, fallando el test si el store devuelve error.
func mustReplaceVersioned(t *testing.T, repo *store.PostgresRepository, tenantID, label, source string) int {
	t.Helper()
	blob := fmt.Sprintf(`{"categories":[{"code":"1","label":%q,"items":[]}]}`, label)
	archived, err := repo.ReplaceTenantContentVersioned(context.Background(), tenantID, "catalogo", []byte(blob), source)
	if err != nil {
		t.Fatalf("import %s: %v", label, err)
	}
	return archived
}

// TestIntegration_ReplaceTenantContentVersioned_ProcedenciaInvalida comprueba que
// una procedencia fuera del conjunto se rechaza ANTES de tocar la BD, en vez de
// morir contra el CHECK de la 0044 con un error de driver. El CHECK sigue ahí como
// última línea; esto es que el error diga la verdad.
func TestIntegration_ReplaceTenantContentVersioned_ProcedenciaInvalida(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)
	tenantID := fmt.Sprintf("tcv-bad-%d", time.Now().UnixNano())

	_, err := repo.ReplaceTenantContentVersioned(context.Background(), tenantID, "catalogo",
		[]byte(`{"categories":[]}`), "inventada")
	if !errors.Is(err, store.ErrInvalidVersionSource) {
		t.Fatalf("err=%v, quiero ErrInvalidVersionSource", err)
	}
	// Y no escribió nada: la ref sigue sin contenido.
	if _, gerr := repo.GetTenantContent(context.Background(), tenantID, "catalogo"); !errors.Is(gerr, store.ErrTenantContentNotFound) {
		t.Fatalf("la procedencia inválida llegó a escribir contenido (err=%v)", gerr)
	}
}
