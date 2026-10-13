package store

import (
	"context"
	"database/sql/driver"
	"slices"
	"testing"
)

// Aserciones de compilación de lo que promete repository_postgres_tenant_content.go.
var (
	_ func(*PostgresRepository, context.Context, string, string) ([]byte, error)              = (*PostgresRepository).GetTenantContent
	_ func(*PostgresRepository, context.Context, string, string, []byte) error                = (*PostgresRepository).UpsertTenantContent
	_ func(*PostgresRepository, context.Context, string, string, []byte, string) (int, error) = (*PostgresRepository).ReplaceTenantContentVersioned
	_ func(*PostgresRepository, context.Context, string) ([]TenantContentSummary, error)      = (*PostgresRepository).ListTenantContent
	_ func(*PostgresRepository, context.Context, string, string) error                        = (*PostgresRepository).DeleteTenantContent
)

const (
	pgRef = "catalogo"
	// pgContentNotFound es el texto de ErrTenantContentNotFound para el tenant y la ref de estos tests.
	pgContentNotFound = "contenido de tenant no encontrado: tenant=" + pgTenant + " ref=" + pgRef
	pgOldBlob         = `{"v": 1}`
	pgNewBlob         = `{"v": 2}`
)

// kinds devuelve solo la clase de cada evento, en orden: es la forma de la conversación.
func (f *pgFake) kinds() []string {
	seen := f.seen()
	out := make([]string, 0, len(seen))
	for _, e := range seen {
		out = append(out, e.kind)
	}
	return out
}

// requirePgKinds exige la forma de la conversación con la base: las clases de evento, en orden.
func requirePgKinds(t *testing.T, fake *pgFake, want ...string) {
	t.Helper()
	if got := fake.kinds(); !slices.Equal(got, want) {
		t.Errorf("conversación con la base = %v, quería %v", got, want)
	}
}

// requirePgRolledBack exige que la conversación con la base abriera una transacción y la revirtiera.
func requirePgRolledBack(t *testing.T, fake *pgFake) {
	t.Helper()
	kinds := fake.kinds()
	if len(kinds) < 2 || kinds[0] != pgBegin || kinds[len(kinds)-1] != pgRollback {
		t.Errorf("conversación con la base = %v, quería abrir una transacción y REVERTIRLA", kinds)
	}
}

// TestPostgres_TenantContent_DatabaseFailure_Wrapped: los cuatro métodos de una sentencia suelta
// envuelven el fallo de la base con su prefijo literal.
func TestPostgres_TenantContent_DatabaseFailure_Wrapped(t *testing.T) {
	runWrappedCases(t, []wrappedCase{
		{"GetTenantContent", "store: leer contenido de tenant: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.GetTenantContent(t.Context(), pgTenant, pgRef)
			return err
		}},
		{"UpsertTenantContent", "store: upsert contenido de tenant: ", func(t *testing.T, r *PostgresRepository) error {
			return r.UpsertTenantContent(t.Context(), pgTenant, pgRef, []byte(`{}`))
		}},
		{"ListTenantContent", "store: listar contenido de tenant: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.ListTenantContent(t.Context(), pgTenant)
			return err
		}},
		{"DeleteTenantContent", "store: borrar contenido de tenant: ", func(t *testing.T, r *PostgresRepository) error {
			return r.DeleteTenantContent(t.Context(), pgTenant, pgRef)
		}},
	})
}

// TestPostgres_GetAndDeleteTenantContent_NotFound: sin fila, GetTenantContent es
// ErrTenantContentNotFound; DeleteTenantContent lo es cuando no afectó a ninguna fila. Con fila,
// el blob sale tal cual y el borrado no falla.
func TestPostgres_GetAndDeleteTenantContent_NotFound(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgOne([]byte(pgOldBlob)), pgReply{}, pgReply{noneAffected: true})

	got, err := h.repo.GetTenantContent(t.Context(), pgTenant, pgRef)
	requireSentinel(t, "GetTenantContent sin fila", err, ErrTenantContentNotFound, pgContentNotFound)
	requireEqual(t, "blob junto al error", string(got), "")
	got, err = h.repo.GetTenantContent(t.Context(), pgTenant, pgRef)
	requireNoError(t, "GetTenantContent", err)
	requireEqual(t, "blob", string(got), pgOldBlob)

	requireNoError(t, "DeleteTenantContent que afecta a una fila", h.repo.DeleteTenantContent(t.Context(), pgTenant, pgRef))
	requireSentinel(t, "DeleteTenantContent sin filas afectadas",
		h.repo.DeleteTenantContent(t.Context(), pgTenant, pgRef), ErrTenantContentNotFound, pgContentNotFound)

	for _, stmt := range loose(t, h.fake, 4) {
		requireArgs(t, "sentencia acotada por (tenant, ref)", stmt, pgTenant, pgRef)
	}
}

// TestPostgres_UpsertTenantContent_OneLooseStatement: una sentencia suelta con (tenant, ref, blob).
func TestPostgres_UpsertTenantContent_OneLooseStatement(t *testing.T) {
	h := newFakeRepository(t, pgReply{})
	requireNoError(t, "UpsertTenantContent", h.repo.UpsertTenantContent(t.Context(), pgTenant, pgRef, []byte(pgNewBlob)))
	requireArgs(t, "UpsertTenantContent", loose(t, h.fake, 1)[0], pgTenant, pgRef, pgNewBlob)
}

// pgContentRows son dos cabeceras del listado de contenido.
func pgContentRows() [][]driver.Value {
	return [][]driver.Value{{pgRef, pgAt, pgAt.Add(1)}, {"menu", pgAt, pgAt}}
}

// TestPostgres_ListTenantContent_MapsRowsAndClosesThem: el listado mapea ref y las dos marcas; sin
// filas, la lista vacía; y falla con su texto si falla el recorrido o el cierre (D-17).
func TestPostgres_ListTenantContent_MapsRowsAndClosesThem(t *testing.T) {
	h := newFakeRepository(t, pgReply{rows: pgContentRows()}, pgReply{},
		pgReply{rows: pgContentRows(), endErr: errPgBoom}, pgReply{rows: pgContentRows(), closeErr: errPgBoom})

	list, err := h.repo.ListTenantContent(t.Context(), pgTenant)
	requireNoError(t, "ListTenantContent", err)
	requireEqual(t, "filas", len(list), 2)
	requireEqual(t, "primera fila", list[0], TenantContentSummary{Ref: pgRef, CreatedAt: pgAt, UpdatedAt: pgAt.Add(1)})
	requireEqual(t, "segunda fila", list[1].Ref, "menu")

	list, err = h.repo.ListTenantContent(t.Context(), pgTenant)
	requireNoError(t, "ListTenantContent sin filas", err)
	requireEqual(t, "filas sin filas", len(list), 0)

	_, err = h.repo.ListTenantContent(t.Context(), pgTenant)
	requirePgWrapped(t, err, "store: iterar contenido de tenant: ", errPgBoom)
	_, err = h.repo.ListTenantContent(t.Context(), pgTenant)
	requirePgWrapped(t, err, "store: cerrar filas: ", errPgBoom)
	requireArgs(t, "ListTenantContent", loose(t, h.fake, 4)[0], pgTenant)
}

// TestPostgres_ReplaceVersioned_InvalidSource_NeverReachesTheDatabase: la procedencia se valida
// ANTES de abrir la transacción.
func TestPostgres_ReplaceVersioned_InvalidSource_NeverReachesTheDatabase(t *testing.T) {
	h := newFakeRepository(t)
	requirePgUntouched(t, h.fake)
	for _, source := range []string{"inventada", "", "IMPORT_JSON"} {
		archived, err := h.repo.ReplaceTenantContentVersioned(t.Context(), pgTenant, pgRef, []byte(pgNewBlob), source)
		requireSentinel(t, "procedencia "+source, err, ErrInvalidVersionSource,
			`procedencia de versión de contenido inválida: "`+source+`"`)
		requireEqual(t, "versión archivada junto al error", archived, 0)
	}
	requirePgUntouched(t, h.fake)
}

// TestPostgres_ReplaceVersioned_NoCurrentContent: sin blob vigente son DOS sentencias en UNA
// transacción —leer con bloqueo y escribir— y devuelve 0.
func TestPostgres_ReplaceVersioned_NoCurrentContent(t *testing.T) {
	h := newFakeRepository(t, pgReply{})
	archived, err := h.repo.ReplaceTenantContentVersioned(t.Context(), pgTenant, pgRef, []byte(pgNewBlob), VersionSourceImportJSON)
	requireNoError(t, "ReplaceTenantContentVersioned", err)
	requireEqual(t, "versión archivada", archived, 0)

	requirePgKinds(t, h.fake, pgBegin, pgQuery, pgExec, pgCommit)
	stmts := statements(t, h.fake, 2, true)
	requireArgs(t, "lectura con bloqueo", stmts[0], pgTenant, pgRef)
	requireArgs(t, "escritura", stmts[1], pgTenant, pgRef, pgNewBlob)
}

// TestPostgres_ReplaceVersioned_ArchivesTheCurrentContent: con blob vigente son CUATRO sentencias
// en UNA transacción —leer, numerar, archivar EL VIEJO con la procedencia, escribir el nuevo— y
// devuelve el número calculado.
func TestPostgres_ReplaceVersioned_ArchivesTheCurrentContent(t *testing.T) {
	h := newFakeRepository(t, pgOne([]byte(pgOldBlob)), pgOne(int64(4)))
	archived, err := h.repo.ReplaceTenantContentVersioned(t.Context(), pgTenant, pgRef, []byte(pgNewBlob), VersionSourceImportTabular)
	requireNoError(t, "ReplaceTenantContentVersioned", err)
	requireEqual(t, "versión archivada", archived, 4)

	requirePgKinds(t, h.fake, pgBegin, pgQuery, pgQuery, pgExec, pgExec, pgCommit)
	stmts := statements(t, h.fake, 4, true)
	requireArgs(t, "lectura con bloqueo", stmts[0], pgTenant, pgRef)
	requireArgs(t, "numeración", stmts[1], pgTenant, pgRef)
	requireArgs(t, "archivado", stmts[2], pgTenant, pgRef, 4, pgOldBlob, VersionSourceImportTabular)
	requireArgs(t, "escritura", stmts[3], pgTenant, pgRef, pgNewBlob)
}

// TestPostgres_ReplaceVersioned_FailureAtEachStep_RollsBack: falle la sentencia que falle, la
// transacción se revierte, se devuelve 0 y el error lleva el prefijo de ESE paso.
func TestPostgres_ReplaceVersioned_FailureAtEachStep_RollsBack(t *testing.T) {
	current, next := pgOne([]byte(pgOldBlob)), pgOne(int64(2))
	cases := []struct {
		name, prefix string
		script       []pgReply
	}{
		{"reading the current content", "store: leer contenido vigente para versionar: ", []pgReply{pgFails()}},
		{"numbering", "store: calcular siguiente versión de contenido: ", []pgReply{current, pgFails()}},
		{"archiving", "store: archivar versión de contenido: ", []pgReply{current, next, pgFails()}},
		{"writing over existing content", "store: escribir contenido versionado: ", []pgReply{current, next, {}, pgFails()}},
		{"writing the first content", "store: escribir contenido versionado: ", []pgReply{{}, pgFails()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeRepository(t, tc.script...)
			archived, err := h.repo.ReplaceTenantContentVersioned(t.Context(), pgTenant, pgRef, []byte(pgNewBlob), VersionSourceManual)
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			requireEqual(t, "versión archivada junto al error", archived, 0)
			requirePgRolledBack(t, h.fake)
		})
	}
}
