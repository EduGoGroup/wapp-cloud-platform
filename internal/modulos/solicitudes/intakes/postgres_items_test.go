//go:build pendiente

package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte del bloqueo, del DELETE/INSERT de líneas,
// de la lectura de la última revisión y de los UPDATE/DELETE de la revalidación.

// pgEditable son los estados desde los que estos tests dejan editar.
var pgEditable = []string{StatusPendingApproval}

// pgNewItems son dos líneas de cliente para reemplazar.
var pgNewItems = []Item{
	{SKU: "A", Label: "Empanada", Customization: "sin ají", Qty: 2, UnitPrice: 10},
	{SKU: "B", Label: "Bebida", Qty: 1, UnitPrice: 5},
}

// scriptReplaceItems guioniza un ReplaceItems entero de n líneas: bloqueo, borrado, n INSERT,
// total, relectura de líneas, [última revisión], revisión escrita y relectura de revisiones.
func scriptReplaceItems(fake *pgFake, n int, lastRevision *pgReply) {
	fake.script(pgOne(StatusPendingApproval), pgReply{})
	for range n {
		fake.script(pgReply{})
	}
	fake.script(pgOne(pgIntakeRow(StatusPendingApproval, 25)...),
		pgReply{rows: [][]driver.Value{pgItemRow("A", 2, 10), pgItemRow("B", 1, 5)}})
	if lastRevision != nil {
		fake.script(*lastRevision)
	}
	fake.script(pgInserted(2, RevisionKindCorrected, `{"v":1}`, nil, RevisionByOwner),
		pgReply{rows: [][]driver.Value{pgRevisionRow(2, `{"v":1}`, pgYoungAge)}})
}

// TestPostgres_ReplaceItems_RejectsBeforeWriting: id que no es UUID (sin tocar la base), cabecera
// que no existe (ErrNotFound) y estado almacenado fuera de los esperados (ErrConflict). Las dos
// últimas solo llegan a bloquear y revierten.
func TestPostgres_ReplaceItems_RejectsBeforeWriting(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, err := store.ReplaceItems(t.Context(), pgTenant, "x", pgNewItems, pgEditable, EditPlain); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReplaceItems(id inválido) = %v, quería ErrNotFound", err)
	}
	requirePgUntouched(t, fake)

	cases := []struct {
		name string
		lock pgReply
		want error
	}{
		{"not in this tenant", pgReply{}, ErrNotFound},
		{"moved to another status", pgOne(StatusOpen), ErrConflict},
		{"stored status is compared as stored", pgOne(StatusClosedLegacy), ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.lock)
			d, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, pgNewItems, []string{StatusPendingApproval, StatusConfirmed}, EditPlain)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(d, Detail{}) {
				t.Errorf("ReplaceItems = (%+v, %v), quería (Detail{}, %v)", d, err, tc.want)
			}
			requirePgKinds(t, fake, pgBegin, pgQuery, pgRollback)
		})
	}
}

// TestPostgres_ReplaceItems_PlainEdit: una transacción con el orden del contrato. El borrado
// excluye las líneas del sistema por el prefijo reservado, cada línea es un INSERT en el orden
// recibido, la revisión es `corrected` y SIN señal (no se lee la última revisión), y el Detail
// sale de lo releído dentro de la transacción.
func TestPostgres_ReplaceItems_PlainEdit(t *testing.T) {
	store, fake := newFakePostgres(t)
	scriptReplaceItems(fake, len(pgNewItems), nil)
	d, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, pgNewItems, pgEditable, EditPlain)
	if err != nil {
		t.Fatalf("ReplaceItems: error inesperado %v", err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgExec, pgExec, pgExec, pgQuery, pgQuery, pgQuery, pgQuery, pgCommit)
	stmts := fake.statements()
	for i, s := range stmts {
		if !s.inTx {
			t.Errorf("la sentencia %d salió fuera de la transacción", i)
		}
	}
	if want := []driver.Value{pgTenant, pgIntakeID}; !reflect.DeepEqual(stmts[0].args, want) {
		t.Errorf("argumentos del bloqueo = %v, quería %v", stmts[0].args, want)
	}
	if want := []driver.Value{pgIntakeID, ReservedSKUPrefix}; !reflect.DeepEqual(stmts[1].args, want) {
		t.Errorf("argumentos del borrado = %v, quería %v", stmts[1].args, want)
	}
	if want := []driver.Value{pgIntakeID, "A", "Empanada", "sin ají", 2, float64(10)}; !reflect.DeepEqual(stmts[2].args, want) {
		t.Errorf("argumentos de la primera línea = %v, quería %v", stmts[2].args, want)
	}
	if got := stmts[3].args[1]; got != "B" {
		t.Errorf("la segunda línea escrita es %v, quería B (orden recibido)", got)
	}
	revision := stmts[6].args
	payload, _ := revision[2].([]byte)
	if revision[1] != RevisionKindCorrected || strings.Contains(string(payload), KeyAsCorrection) {
		t.Errorf("revisión escrita: kind %v y payload %s, quería corrected y sin señal", revision[1], payload)
	}
	if d.Total != 25 || len(d.Items) != 2 || len(d.Revisions) != 1 || d.Revisions[0].RevisionNo != 2 || d.BuyerDataPresent {
		t.Errorf("Detail = %+v, quería total 25, dos líneas y la revisión 2", d)
	}
}

// TestPostgres_ReplaceItems_AsCorrection_ReadsTheLastRevisionUnderTheLock: declarada corrección,
// la última revisión se lee DENTRO de la transacción, entre la relectura de líneas y la
// escritura, y la revisión lleva la señal con el número y el tipo de la que corrige.
func TestPostgres_ReplaceItems_AsCorrection_ReadsTheLastRevisionUnderTheLock(t *testing.T) {
	store, fake := newFakePostgres(t)
	last := pgOne(int64(1), RevisionKindInterpreted)
	scriptReplaceItems(fake, len(pgNewItems), &last)
	if _, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, pgNewItems, pgEditable, EditAsCorrection); err != nil {
		t.Fatalf("ReplaceItems: error inesperado %v", err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgExec, pgExec, pgExec, pgQuery, pgQuery, pgQuery, pgQuery, pgQuery, pgCommit)
	stmts := fake.statements()
	if !stmts[6].inTx || !reflect.DeepEqual(stmts[6].args, []driver.Value{pgIntakeID}) {
		t.Errorf("lectura de la última revisión = %+v, quería la solicitud dentro de la transacción", stmts[6])
	}
	payload, _ := stmts[7].args[2].([]byte)
	for _, want := range []string{`"` + KeyAsCorrection + `":true`, `"` + KeyCorrectsRevisionNo + `":1`, `"` + KeyCorrectsKind + `":"` + RevisionKindInterpreted + `"`} {
		if !strings.Contains(string(payload), want) {
			t.Errorf("el payload de la revisión no lleva %s: %s", want, payload)
		}
	}
}

// TestPostgres_ReplaceItems_EmptyItems_LeavesNoClientLines: una lista vacía es válida: borra y no
// inserta nada.
func TestPostgres_ReplaceItems_EmptyItems_LeavesNoClientLines(t *testing.T) {
	store, fake := newFakePostgres(t)
	scriptReplaceItems(fake, 0, nil)
	if _, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, nil, pgEditable, EditPlain); err != nil {
		t.Fatalf("ReplaceItems: error inesperado %v", err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgExec, pgQuery, pgQuery, pgQuery, pgQuery, pgCommit)
}

// TestPostgres_ReplaceItems_Errors: cada fallo sale con su prefijo y revierte TODO: la edición y
// su rastro se confirman juntos o no se confirma ninguno.
func TestPostgres_ReplaceItems_Errors(t *testing.T) {
	lock := pgOne(StatusPendingApproval)
	total := pgOne(pgIntakeRow(StatusPendingApproval, 25)...)
	boom := pgReply{err: errPgBoom}
	cases := []struct {
		name   string
		mode   EditMode
		script []pgReply
		prefix string
	}{
		{"lock fails", EditPlain, []pgReply{boom}, "intakes: bloquear la solicitud para editarla: "},
		{"delete fails", EditPlain, []pgReply{lock, boom}, "intakes: retirar las líneas de la solicitud: "},
		{"second insert fails", EditPlain, []pgReply{lock, {}, {}, boom}, `intakes: escribir la línea "B" de la solicitud: `},
		{"total recompute fails", EditPlain, []pgReply{lock, {}, {}, {}, boom}, "intakes: leer solicitud: "},
		{"items reread fails", EditPlain, []pgReply{lock, {}, {}, {}, total, boom}, "intakes: listar líneas: "},
		{"last revision read fails", EditAsCorrection, []pgReply{lock, {}, {}, {}, total, {}, boom}, "intakes: leer la revisión que se está corrigiendo: "},
		{"revision insert fails", EditPlain, []pgReply{lock, {}, {}, {}, total, {}, boom}, "intakes: insertar revisión: "},
		{"revisions reread fails", EditPlain, []pgReply{lock, {}, {}, {}, total, {}, pgInserted(2, RevisionKindCorrected, `{"v":1}`, nil, nil), boom}, "intakes: listar revisiones: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			d, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, pgNewItems, pgEditable, tc.mode)
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			if !reflect.DeepEqual(d, Detail{}) {
				t.Errorf("ReplaceItems con error devolvió %+v, quería Detail{}", d)
			}
			kinds := fake.kinds()
			if kinds[len(kinds)-1] != pgRollback {
				t.Errorf("conversación = %v, quería acabar en rollback", kinds)
			}
		})
	}
}

// TestPostgres_ReplaceItems_UniqueViolationInTheTx_IsNotRetried: dentro de la transacción la
// numeración de la revisión NO se reintenta: un 23505 la aborta entera.
func TestPostgres_ReplaceItems_UniqueViolationInTheTx_IsNotRetried(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(StatusPendingApproval), pgReply{}, pgOne(pgIntakeRow(StatusPendingApproval, 0)...), pgReply{}, pgReply{err: errPgUnique})
	_, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, nil, pgEditable, EditPlain)
	requirePgWrapped(t, err, "intakes: insertar revisión: ", errPgUnique)
	requirePgKinds(t, fake, pgBegin, pgQuery, pgExec, pgQuery, pgQuery, pgQuery, pgRollback)
}

// pgRevalidation es una revalidación con un cambio de precio y una línea retirada.
var pgRevalidation = Revalidation{
	Items: []Item{{SKU: "A", Label: "Empanada nueva", Qty: 2, UnitPrice: 12}},
	Changes: []LineChange{
		{SKU: "B", Label: "Bebida", Qty: 1, From: 5, Removed: true},
		{SKU: "A", Label: "Empanada nueva", Qty: 2, From: 10, To: 12},
	},
	TotalBefore: 25, TotalAfter: 24,
}

// TestPostgres_ApplyRevalidation_AppliesChangesAndLeavesItsRevision: primero los cambios de precio
// y después las retiradas, cada uno sobre SU línea y nunca sobre una del sistema; luego el total,
// la revisión `revalidated` con el texto dado, y la relectura de líneas y revisiones.
func TestPostgres_ApplyRevalidation_AppliesChangesAndLeavesItsRevision(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(StatusPendingApproval), pgReply{}, pgReply{}, pgOne(pgIntakeRow(StatusPendingApproval, 24)...),
		pgInserted(3, RevisionKindRevalidated, `{"v":1}`, "Cambió un precio", nil),
		pgReply{rows: [][]driver.Value{pgItemRow("A", 2, 12)}},
		pgReply{rows: [][]driver.Value{pgRevisionRow(3, `{"v":1}`, pgYoungAge)}})
	d, err := store.ApplyRevalidation(t.Context(), pgTenant, pgIntakeID, pgRevalidation, "Cambió un precio", pgEditable)
	if err != nil {
		t.Fatalf("ApplyRevalidation: error inesperado %v", err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgExec, pgExec, pgQuery, pgQuery, pgQuery, pgQuery, pgCommit)
	stmts := fake.statements()
	if want := []driver.Value{pgIntakeID, "A", "Empanada nueva", float64(12), ReservedSKUPrefix}; !reflect.DeepEqual(stmts[1].args, want) {
		t.Errorf("argumentos del cambio de precio = %v, quería %v", stmts[1].args, want)
	}
	if want := []driver.Value{pgIntakeID, "B", ReservedSKUPrefix}; !reflect.DeepEqual(stmts[2].args, want) {
		t.Errorf("argumentos de la retirada = %v, quería %v", stmts[2].args, want)
	}
	if rev := stmts[4].args; rev[1] != RevisionKindRevalidated || rev[3] != "Cambió un precio" {
		t.Errorf("revisión escrita: kind %v y texto %v", rev[1], rev[3])
	}
	if d.Total != 24 || len(d.Items) != 1 || len(d.Revisions) != 1 || d.Revisions[0].RevisionNo != 3 {
		t.Errorf("Detail = %+v, quería total 24, una línea y la revisión 3", d)
	}
}

// TestPostgres_ApplyRevalidation_Rejections: mismas tres salidas sin escribir que ReplaceItems.
func TestPostgres_ApplyRevalidation_Rejections(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, err := store.ApplyRevalidation(t.Context(), pgTenant, "x", pgRevalidation, "", pgEditable); !errors.Is(err, ErrNotFound) {
		t.Errorf("ApplyRevalidation(id inválido) = %v, quería ErrNotFound", err)
	}
	requirePgUntouched(t, fake)
	for want, lock := range map[error]pgReply{ErrNotFound: {}, ErrConflict: pgOne(StatusConfirmed)} {
		store, fake := newFakePostgres(t)
		fake.script(lock)
		if _, err := store.ApplyRevalidation(t.Context(), pgTenant, pgIntakeID, pgRevalidation, "", pgEditable); !errors.Is(err, want) {
			t.Errorf("ApplyRevalidation = %v, quería %v", err, want)
		}
		requirePgKinds(t, fake, pgBegin, pgQuery, pgRollback)
	}
}

// TestPostgres_ApplyRevalidation_Errors: los fallos propios de la revalidación, con el SKU entre
// comillas, y con la transacción revertida.
func TestPostgres_ApplyRevalidation_Errors(t *testing.T) {
	lock := pgOne(StatusPendingApproval)
	boom := pgReply{err: errPgBoom}
	cases := []struct {
		name   string
		script []pgReply
		prefix string
	}{
		{"lock fails", []pgReply{boom}, "intakes: bloquear la solicitud para editarla: "},
		{"reprice fails", []pgReply{lock, boom}, `intakes: re-preciar la línea "A" de la solicitud: `},
		{"removal fails", []pgReply{lock, {}, boom}, `intakes: retirar la línea "B" de la solicitud: `},
		{"total recompute fails", []pgReply{lock, {}, {}, boom}, "intakes: leer solicitud: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			_, err := store.ApplyRevalidation(t.Context(), pgTenant, pgIntakeID, pgRevalidation, "", pgEditable)
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			kinds := fake.kinds()
			if kinds[len(kinds)-1] != pgRollback {
				t.Errorf("conversación = %v, quería acabar en rollback", kinds)
			}
		})
	}
}
