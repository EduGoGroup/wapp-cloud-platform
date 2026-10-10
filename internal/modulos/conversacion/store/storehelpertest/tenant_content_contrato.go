package storehelpertest

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de TenantContentReader y de los tres métodos de contenido que los dos adaptadores
// ofrecen fuera de las interfaces: tenant_content.

// contentNotFoundText es el texto exacto de ErrTenantContentNotFound para (tenant, ref).
func contentNotFoundText(tenant, ref string) string {
	return "contenido de tenant no encontrado: tenant=" + tenant + " ref=" + ref
}

// requireContentNotFound afirma que err es ErrTenantContentNotFound con su texto exacto.
func requireContentNotFound(t *testing.T, what string, err error, tenant, ref string) {
	t.Helper()
	if !errors.Is(err, store.ErrTenantContentNotFound) || err.Error() != contentNotFoundText(tenant, ref) {
		t.Errorf("%s: err = %v, quería ErrTenantContentNotFound con el texto %q", what, err, contentNotFoundText(tenant, ref))
	}
}

// requireContent afirma que el blob vigente de (tenant, ref) tiene ese contenido.
func requireContent(t *testing.T, m Montaje, what, tenant, ref string, want []byte) {
	t.Helper()
	got, err := m.Store.GetTenantContent(ctx, tenant, ref)
	if err != nil {
		t.Fatalf("%s: GetTenantContent(%s, %s): %v", what, tenant, ref, err)
	}
	if !sameJSON(got, want) {
		t.Errorf("%s: contenido de (%s, %s) = %s, quería %s", what, tenant, ref, got, want)
	}
}

// summaryOf devuelve la cabecera del blob (tenant, ref) según ListTenantContent.
func summaryOf(t *testing.T, m Montaje, tenant, ref string) store.TenantContentSummary {
	t.Helper()
	list, err := m.Store.ListTenantContent(ctx, tenant)
	if err != nil {
		t.Fatalf("ListTenantContent(%s): %v", tenant, err)
	}
	for _, s := range list {
		if s.Ref == ref {
			return s
		}
	}
	t.Fatalf("la ref %q no está entre las del tenant %s: %+v", ref, tenant, list)
	return store.TenantContentSummary{}
}

// seedContentWitnesses escribe los dos testigos de un caso de contenido: OTRA ref del mismo tenant
// y la MISMA ref en el otro tenant.
func seedContentWitnesses(t *testing.T, m Montaje, ref, otherRef string) {
	t.Helper()
	mustUpsertContent(t, m, m.TenantA, otherRef, blob("otra ref"))
	mustUpsertContent(t, m, m.TenantB, ref, blob("otro tenant"))
}

// caseGetContentNotFound: una ref que no existe, y la ref de otro tenant, son
// ErrTenantContentNotFound con el tenant y la ref en el texto, y sin blob.
func caseGetContentNotFound(t *testing.T, m Montaje) {
	mustUpsertContent(t, m, m.TenantB, refCatalog, blob("otro tenant"))
	for _, ref := range []string{refCatalog, "no-existe"} {
		got, err := m.Store.GetTenantContent(ctx, m.TenantA, ref)
		requireContentNotFound(t, "GetTenantContent("+ref+")", err, m.TenantA, ref)
		if got != nil {
			t.Errorf("GetTenantContent(%s) devolvió %s junto al error", ref, got)
		}
	}
}

// caseUpsertContent: el primer UpsertTenantContent crea el blob con sus dos marcas de tiempo al
// instante de la escritura; el segundo lo SUSTITUYE (no duplica), conserva created_at y refresca
// updated_at. No archiva versión —eso es de ReplaceTenantContentVersioned— y no toca ni otra ref
// del tenant ni la misma ref del otro.
func caseUpsertContent(t *testing.T, m Montaje) {
	seedContentWitnesses(t, m, refCatalog, refMenu)
	before := take(t, m)

	t0 := m.Now(t)
	mustUpsertContent(t, m, m.TenantA, refCatalog, blob("v1"))
	t1 := m.Now(t)
	requireContent(t, m, "alta", m.TenantA, refCatalog, blob("v1"))
	created := summaryOf(t, m, m.TenantA, refCatalog)
	requireStamped(t, "alta: CreatedAt", created.CreatedAt, t0, t1)
	if !created.UpdatedAt.Equal(created.CreatedAt) {
		t.Errorf("alta: UpdatedAt = %v, quería el mismo instante que CreatedAt (%v)", created.UpdatedAt, created.CreatedAt)
	}
	afterCreate := take(t, m)
	requireRestUntouched(t, "alta", before, afterCreate, forgetContent(m.TenantA, refCatalog))

	m.Advance(t)
	afterCreate = take(t, m)
	t0 = m.Now(t)
	mustUpsertContent(t, m, m.TenantA, refCatalog, blob("v2"))
	t1 = m.Now(t)
	requireContent(t, m, "sustitución", m.TenantA, refCatalog, blob("v2"))
	replaced := summaryOf(t, m, m.TenantA, refCatalog)
	if !replaced.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("sustitución: CreatedAt = %v, quería el del alta (%v)", replaced.CreatedAt, created.CreatedAt)
	}
	if !replaced.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("sustitución: UpdatedAt no se refrescó: era %v y quedó %v", created.UpdatedAt, replaced.UpdatedAt)
	}
	requireStamped(t, "sustitución: UpdatedAt", replaced.UpdatedAt, t0, t1)
	if versions := m.ContentVersions(t, m.TenantA, refCatalog); len(versions) != 0 {
		t.Errorf("UpsertTenantContent archivó %d versiones, quería ninguna: %+v", len(versions), versions)
	}
	requireRestUntouched(t, "sustitución", afterCreate, take(t, m), forgetContent(m.TenantA, refCatalog))
}

// caseGetContentCopy: el blob que devuelve GetTenantContent es del llamante, y el que recibe
// UpsertTenantContent también: mutar uno u otro no cambia lo guardado.
func caseGetContentCopy(t *testing.T, m Montaje) {
	raw := blob("original")
	mustUpsertContent(t, m, m.TenantA, refCatalog, raw)
	for i := range raw {
		raw[i] = 'x'
	}
	got, err := m.Store.GetTenantContent(ctx, m.TenantA, refCatalog)
	if err != nil {
		t.Fatalf("GetTenantContent: %v", err)
	}
	for i := range got {
		got[i] = 'x'
	}
	requireContent(t, m, "tras mutar los blobs del llamante", m.TenantA, refCatalog, blob("original"))
}

// caseListContent: ListTenantContent da una cabecera por blob del tenant, ordenadas por ref, con
// sus dos marcas de tiempo, y nunca las de otro tenant. Sin blobs, la lista vacía sin error.
func caseListContent(t *testing.T, m Montaje) {
	if got, err := m.Store.ListTenantContent(ctx, m.TenantA); err != nil || len(got) != 0 {
		t.Fatalf("ListTenantContent de un tenant sin contenido = (%+v, %v), quería vacía sin error", got, err)
	}
	// Se escriben en orden inverso al alfabético: la salida es por ref, no por alta.
	for _, ref := range []string{"zona", refMenu, refCatalog} {
		mustUpsertContent(t, m, m.TenantA, ref, blob(ref))
	}
	mustUpsertContent(t, m, m.TenantB, "ajeno", blob("ajeno"))

	for tenant, want := range map[string][]string{
		m.TenantA: {refCatalog, refMenu, "zona"},
		m.TenantB: {"ajeno"},
	} {
		got, err := m.Store.ListTenantContent(ctx, tenant)
		if err != nil {
			t.Fatalf("ListTenantContent(%s): %v", tenant, err)
		}
		if len(got) != len(want) {
			t.Fatalf("ListTenantContent(%s) = %+v, quería las refs %v", tenant, got, want)
		}
		for i, ref := range want {
			if got[i].Ref != ref {
				t.Errorf("ListTenantContent(%s)[%d] = %q, quería %q", tenant, i, got[i].Ref, ref)
			}
			if got[i].CreatedAt.IsZero() || got[i].UpdatedAt.IsZero() {
				t.Errorf("ListTenantContent(%s)[%d] (%s) trae una marca de tiempo a cero: %+v", tenant, i, ref, got[i])
			}
		}
	}
}

// caseDeleteContent: DeleteTenantContent quita ESE blob y solo ese; borrarlo otra vez, o borrar
// una ref que nunca existió, es ErrTenantContentNotFound y no toca nada. Las versiones ya
// archivadas de la ref no se borran con ella.
func caseDeleteContent(t *testing.T, m Montaje) {
	seedContentWitnesses(t, m, refCatalog, refMenu)
	mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v1"), store.VersionSourceImportJSON)
	mustReplaceVersioned(t, m, m.TenantA, refCatalog, blob("v2"), store.VersionSourceImportJSON)
	archived := m.ContentVersions(t, m.TenantA, refCatalog)
	before := take(t, m)

	if err := m.Store.DeleteTenantContent(ctx, m.TenantA, refCatalog); err != nil {
		t.Fatalf("DeleteTenantContent: %v", err)
	}
	_, err := m.Store.GetTenantContent(ctx, m.TenantA, refCatalog)
	requireContentNotFound(t, "GetTenantContent tras borrar", err, m.TenantA, refCatalog)
	requireSameVersions(t, "versiones tras borrar el blob", m.ContentVersions(t, m.TenantA, refCatalog), archived)
	afterDelete := take(t, m)
	requireRestUntouched(t, "DeleteTenantContent", before, afterDelete, forgetContent(m.TenantA, refCatalog))

	afterDelete = take(t, m)
	requireContentNotFound(t, "segundo DeleteTenantContent",
		m.Store.DeleteTenantContent(ctx, m.TenantA, refCatalog), m.TenantA, refCatalog)
	requireContentNotFound(t, "DeleteTenantContent de una ref que nunca existió",
		m.Store.DeleteTenantContent(ctx, m.TenantB, "no-existe"), m.TenantB, "no-existe")
	requireUntouched(t, "DeleteTenantContent rechazado", afterDelete, take(t, m))
}
