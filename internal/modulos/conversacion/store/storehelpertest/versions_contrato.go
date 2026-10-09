package storehelpertest

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de TenantContentVersioner: ReplaceTenantContentVersioned sobre tenant_content y
// tenant_content_versions. Su carrera está en concurrency_contrato.go.

// caseReplaceFirstWrite: el primer import sobre una ref vacía ESCRIBE y NO versiona: devuelve 0 y
// no deja ninguna fila archivada («no hay versiones» y «no hay contenido» son casos distintos).
func caseReplaceFirstWrite(t *testing.T, m Montaje) {
	seedContentWitnesses(t, m, refCatalog, refMenu)
	before := take(t, m)

	t0 := m.Now(t)
	archived := mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v1"), store.VersionSourceImportJSON)
	t1 := m.Now(t)
	if archived != 0 {
		t.Errorf("el primer import archivó la versión %d, quería 0 (no había nada que archivar)", archived)
	}
	requireContent(t, m, "primer import", m.TenantA, refCatalog, blob("v1"))
	if versions := m.ContentVersions(t, m.TenantA, refCatalog); len(versions) != 0 {
		t.Errorf("el primer import dejó %d versiones archivadas, quería ninguna: %+v", len(versions), versions)
	}
	summary := summaryOf(t, m, m.TenantA, refCatalog)
	requireStamped(t, "primer import: CreatedAt", summary.CreatedAt, t0, t1)
	requireStamped(t, "primer import: UpdatedAt", summary.UpdatedAt, t0, t1)
	requireRestUntouched(t, "primer import", before, take(t, m), forgetContent(m.TenantA, refCatalog))
}

// caseReplaceArchives: con blob vigente, cada import archiva EL QUE HABÍA (no el que escribe) como
// la versión siguiente, con la procedencia del acto y su fecha, y deja el nuevo vigente
// conservando el created_at del blob y refrescando su updated_at. Las tres procedencias valen.
func caseReplaceArchives(t *testing.T, m Montaje) {
	mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v1"), store.VersionSourceImportJSON)
	created := summaryOf(t, m, m.TenantA, refCatalog)
	previous := created

	steps := []struct {
		tag, source string
		archivedTag string
	}{
		{"v2", store.VersionSourceImportJSON, "v1"},
		{"v3", store.VersionSourceImportTabular, "v2"},
		{"v4", store.VersionSourceManual, "v3"},
	}
	want := make([]ContentVersion, 0, len(steps))
	for i, step := range steps {
		m.Advance(t)
		t0 := m.Now(t)
		archived := mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob(step.tag), step.source)
		t1 := m.Now(t)
		if archived != i+1 {
			t.Errorf("el import de %s archivó la versión %d, quería %d", step.tag, archived, i+1)
		}
		requireContent(t, m, "import de "+step.tag, m.TenantA, refCatalog, blob(step.tag))

		versions := m.ContentVersions(t, m.TenantA, refCatalog)
		if len(versions) != i+1 {
			t.Fatalf("%d versiones archivadas tras el import de %s, quería %d: %+v", len(versions), step.tag, i+1, versions)
		}
		newest := versions[i]
		requireStamped(t, "versión archivada: CreatedAt", newest.CreatedAt, t0, t1)
		want = append(want, ContentVersion{
			Version: i + 1, Content: blob(step.archivedTag), Source: step.source, CreatedAt: newest.CreatedAt,
		})
		requireSameVersions(t, "tras el import de "+step.tag, versions, want)

		summary := summaryOf(t, m, m.TenantA, refCatalog)
		if !summary.CreatedAt.Equal(created.CreatedAt) {
			t.Errorf("import de %s: CreatedAt del blob = %v, quería el del alta (%v)", step.tag, summary.CreatedAt, created.CreatedAt)
		}
		if !summary.UpdatedAt.After(previous.UpdatedAt) {
			t.Errorf("import de %s: UpdatedAt no se refrescó: era %v y quedó %v", step.tag, previous.UpdatedAt, summary.UpdatedAt)
		}
		requireStamped(t, "import de "+step.tag+": UpdatedAt", summary.UpdatedAt, t0, t1)
		previous = summary
	}
}

// caseReplaceOverUpsert: lo vigente se archiva lo haya escrito quien lo haya escrito: un blob
// puesto con UpsertTenantContent (que no versiona) es la versión 1 del primer import que lo pisa.
func caseReplaceOverUpsert(t *testing.T, m Montaje) {
	mustUpsertContent(t, m, m.TenantA, refCatalog, blob("a mano"))
	if got := mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("importado"), store.VersionSourceImportTabular); got != 1 {
		t.Errorf("archivó la versión %d, quería 1", got)
	}
	versions := m.ContentVersions(t, m.TenantA, refCatalog)
	if len(versions) != 1 {
		t.Fatalf("%d versiones archivadas, quería 1: %+v", len(versions), versions)
	}
	requireSameVersions(t, "import sobre un blob puesto a mano", versions, []ContentVersion{
		{Version: 1, Content: blob("a mano"), Source: store.VersionSourceImportTabular, CreatedAt: versions[0].CreatedAt},
	})
	requireContent(t, m, "import sobre un blob puesto a mano", m.TenantA, refCatalog, blob("importado"))
}

// caseReplaceInvalidSource: una procedencia que no es una de las tres se rechaza ANTES de escribir
// nada, con ErrInvalidVersionSource y la procedencia entre comillas en el texto: ni se archiva ni
// se sustituye el blob, lo haya o no.
func caseReplaceInvalidSource(t *testing.T, m Montaje) {
	mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v1"), store.VersionSourceImportJSON)
	mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v2"), store.VersionSourceImportJSON)
	before := take(t, m)

	for _, tc := range []struct{ ref, source, wantText string }{
		{refCatalog, "inventada", `procedencia de versión de contenido inválida: "inventada"`},
		{refCatalog, "", `procedencia de versión de contenido inválida: ""`},
		{refCatalog, "IMPORT_JSON", `procedencia de versión de contenido inválida: "IMPORT_JSON"`},
		{"ref-nueva", "inventada", `procedencia de versión de contenido inválida: "inventada"`},
	} {
		archived, err := m.Store.ReplaceTenantContentVersioned(ctx, m.TenantA, tc.ref, blob("no debe escribirse"), tc.source)
		if !errors.Is(err, store.ErrInvalidVersionSource) || err.Error() != tc.wantText {
			t.Errorf("procedencia %q: err = %v, quería ErrInvalidVersionSource con el texto %q", tc.source, err, tc.wantText)
		}
		if archived != 0 {
			t.Errorf("procedencia %q: archivó la versión %d junto al error, quería 0", tc.source, archived)
		}
	}
	requireUntouched(t, "procedencia inválida", before, take(t, m))
	if versions := m.ContentVersions(t, m.TenantA, "ref-nueva"); len(versions) != 0 {
		t.Errorf("la procedencia inválida dejó versiones de una ref que no existe: %+v", versions)
	}
}

// caseReplaceNumbersPerRef: la numeración es POR (tenant, ref). Las versiones de otra ref del
// mismo tenant y las de la misma ref en otro tenant ni cuentan ni se tocan, y el blob que se
// archiva es el de ESA ref de ESE tenant.
func caseReplaceNumbersPerRef(t *testing.T, m Montaje) {
	// Testigos con MÁS historia que la ref que se va a tocar: tres versiones en otra ref del
	// tenant y dos en la misma ref del otro.
	for _, tag := range []string{"m1", "m2", "m3", "m4"} {
		mustReplaceVersioned(t, m, m.TenantA, refMenu, blob(tag), store.VersionSourceImportJSON)
	}
	for _, tag := range []string{"b1", "b2", "b3"} {
		mustReplaceVersioned(t, m, m.TenantB, refCatalog, blob(tag), store.VersionSourceManual)
	}
	mustUpsertContent(t, m, m.TenantA, refCatalog, blob("propio"))
	before := take(t, m)

	if got := mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("nuevo"), store.VersionSourceImportJSON); got != 1 {
		t.Errorf("archivó la versión %d, quería 1: la numeración es por (tenant, ref)", got)
	}
	versions := m.ContentVersions(t, m.TenantA, refCatalog)
	if len(versions) != 1 {
		t.Fatalf("%d versiones archivadas, quería 1: %+v", len(versions), versions)
	}
	requireSameVersions(t, "versión archivada", versions, []ContentVersion{
		{Version: 1, Content: blob("propio"), Source: store.VersionSourceImportJSON, CreatedAt: versions[0].CreatedAt},
	})
	requireRestUntouched(t, "import sobre una ref", before, take(t, m), forgetContent(m.TenantA, refCatalog))
}

// caseReplaceAfterDelete: borrar el blob no borra su historia. El import siguiente sobre esa ref
// no tiene vigente que archivar (devuelve 0), y el de después archiva con el número que sigue al
// último que hubo: nunca reutiliza uno.
func caseReplaceAfterDelete(t *testing.T, m Montaje) {
	for _, tag := range []string{"v1", "v2", "v3"} {
		mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob(tag), store.VersionSourceImportJSON)
	}
	if err := m.Store.DeleteTenantContent(ctx, m.TenantA, refCatalog); err != nil {
		t.Fatalf("DeleteTenantContent: %v", err)
	}
	history := m.ContentVersions(t, m.TenantA, refCatalog)
	if len(history) != 2 {
		t.Fatalf("%d versiones archivadas tras borrar, quería las 2 que había: %+v", len(history), history)
	}

	if got := mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v4"), store.VersionSourceImportJSON); got != 0 {
		t.Errorf("el import sobre la ref borrada archivó la versión %d, quería 0", got)
	}
	requireSameVersions(t, "tras reescribir la ref borrada", m.ContentVersions(t, m.TenantA, refCatalog), history)

	if got := mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v5"), store.VersionSourceImportTabular); got != 3 {
		t.Errorf("el import siguiente archivó la versión %d, quería 3 (la que sigue a las dos que había)", got)
	}
	versions := m.ContentVersions(t, m.TenantA, refCatalog)
	if len(versions) != 3 {
		t.Fatalf("%d versiones archivadas, quería 3: %+v", len(versions), versions)
	}
	requireSameVersions(t, "historia completa", versions, append(history, ContentVersion{
		Version: 3, Content: blob("v4"), Source: store.VersionSourceImportTabular, CreatedAt: versions[2].CreatedAt,
	}))
}
