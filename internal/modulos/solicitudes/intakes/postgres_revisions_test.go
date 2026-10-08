package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero prueba la ESCRITURA de revisiones (InsertRevision). La lectura, con la retención
// del literal, va en postgres_revisions_read_test.go.
//
// El texto byte a byte del INSERT numerador se afirma al final, contra el del paquete viejo. El
// auxiliar que sella la poda en la revisión leída se prueba en postgres_revisions_read_test.go.

// errPgUnique es la violación de unicidad (SQLSTATE 23505) con la que pierde un numerador concurrente.
var errPgUnique = &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}

// pgInserted es la fila que devuelve el INSERT: las seis columnas de la revisión.
func pgInserted(no int64, kind, payload string, rendered, createdBy driver.Value) pgReply {
	return pgOne(no, kind, []byte(payload), rendered, createdBy, pgCreated)
}

// plainRevision es una revisión sin literal lista para escribir.
func plainRevision() Revision {
	return Revision{IntakeID: pgIntakeID, Kind: RevisionKindCart, Payload: []byte(`{"v":1,"total":10}`)}
}

// TestPostgres_InsertRevision_ValidatesBeforeQuerying: payload vacío y luego id que no es UUID,
// en ese orden y sin tocar la base.
func TestPostgres_InsertRevision_ValidatesBeforeQuerying(t *testing.T) {
	var writer RevisionWriter
	cases := []struct {
		name string
		rev  Revision
		want error
	}{
		{"empty payload", Revision{IntakeID: pgIntakeID, Kind: RevisionKindCart}, ErrEmptyRevisionPayload},
		{"empty payload wins over a bad id", Revision{IntakeID: "x", Kind: RevisionKindCart}, ErrEmptyRevisionPayload},
		{"id is not a uuid", Revision{IntakeID: "x", Kind: RevisionKindCart, Payload: []byte(`{}`)}, ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			writer = store
			got, err := writer.InsertRevision(t.Context(), tc.rev)
			if !errors.Is(err, tc.want) {
				t.Errorf("InsertRevision = %v, quería %v", err, tc.want)
			}
			if !reflect.DeepEqual(got, Revision{}) {
				t.Errorf("InsertRevision con error devolvió %+v, quería Revision{}", got)
			}
			requirePgUntouched(t, fake)
		})
	}
}

// TestPostgres_InsertRevision_OneStatementOutsideTx: numerar y escribir es UNA sentencia suelta.
// El RevisionNo de entrada no viaja; los textos vacíos y el sobre ausente viajan a NULL; lo que
// se devuelve es lo que dijo la base, con los NULL como cadena vacía.
func TestPostgres_InsertRevision_OneStatementOutsideTx(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgInserted(4, RevisionKindCart, `{"v":1,"total":10}`, nil, nil))
	rev := plainRevision()
	rev.RevisionNo = 99
	got, err := store.InsertRevision(t.Context(), rev)
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	want := Revision{IntakeID: pgIntakeID, RevisionNo: 4, Kind: RevisionKindCart, Payload: []byte(`{"v":1,"total":10}`), CreatedAt: pgCreated}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InsertRevision = %+v, quería %+v", got, want)
	}
	stmts := fake.statements()
	if len(stmts) != 1 || stmts[0].kind != pgQuery || stmts[0].inTx {
		t.Fatalf("sentencias = %+v, quería una consulta suelta", stmts)
	}
	wantArgs := []driver.Value{pgIntakeID, RevisionKindCart, []byte(`{"v":1,"total":10}`), nil, nil, nil, nil, nil}
	if !reflect.DeepEqual(stmts[0].args, wantArgs) {
		t.Errorf("argumentos = %v, quería %v", stmts[0].args, wantArgs)
	}
	requirePgKinds(t, fake, pgQuery)
}

// TestPostgres_InsertRevision_SendsAndReturnsTextAndAuthor: rendered_text y created_by con valor
// viajan como texto y vuelven tal cual.
func TestPostgres_InsertRevision_SendsAndReturnsTextAndAuthor(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgInserted(1, RevisionKindApproved, `{"v":1}`, "Tu pedido", RevisionByOwner))
	rev := Revision{IntakeID: pgIntakeID, Kind: RevisionKindApproved, Payload: []byte(`{"v":1}`), RenderedText: "Tu pedido", CreatedBy: RevisionByOwner}
	got, err := store.InsertRevision(t.Context(), rev)
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	if got.RenderedText != "Tu pedido" || got.CreatedBy != RevisionByOwner {
		t.Errorf("InsertRevision devolvió texto %q y autor %q", got.RenderedText, got.CreatedBy)
	}
	args := fake.statements()[0].args
	if args[3] != "Tu pedido" || args[4] != RevisionByOwner {
		t.Errorf("argumentos de texto y autor = %v, %v", args[3], args[4])
	}
}

// TestPostgres_InsertRevision_RetriesOnUniqueViolation: quien pierde la numeración contra el
// UNIQUE reintenta la sentencia entera y acaba escribiendo.
func TestPostgres_InsertRevision_RetriesOnUniqueViolation(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgReply{err: errPgUnique}, pgReply{err: errPgUnique}, pgInserted(3, RevisionKindCart, `{"v":1}`, nil, nil))
	got, err := store.InsertRevision(t.Context(), plainRevision())
	if err != nil || got.RevisionNo != 3 {
		t.Fatalf("InsertRevision = (%+v, %v), quería la revisión 3 al tercer intento", got, err)
	}
	requirePgKinds(t, fake, pgQuery, pgQuery, pgQuery)
}

// TestPostgres_InsertRevision_GivesUpAfterFiveAttempts: cinco colisiones seguidas son un error
// con su texto exacto, que envuelve la última violación; ni un intento más.
func TestPostgres_InsertRevision_GivesUpAfterFiveAttempts(t *testing.T) {
	store, fake := newFakePostgres(t)
	for range 6 {
		fake.script(pgReply{err: errPgUnique})
	}
	got, err := store.InsertRevision(t.Context(), plainRevision())
	requirePgWrapped(t, err, "intakes: numerar la revisión tras 5 intentos: intakes: insertar revisión: ", errPgUnique)
	if !reflect.DeepEqual(got, Revision{}) {
		t.Errorf("InsertRevision agotado devolvió %+v, quería Revision{}", got)
	}
	if n := len(fake.statements()); n != 5 {
		t.Errorf("se lanzaron %d intentos, quería 5", n)
	}
}

// TestPostgres_InsertRevision_OtherErrorsDoNotRetry: cualquier otro fallo sale a la primera,
// envuelto con su prefijo.
func TestPostgres_InsertRevision_OtherErrorsDoNotRetry(t *testing.T) {
	foreignKey := &pgconn.PgError{Code: "23503", Message: "violates foreign key constraint"}
	for name, cause := range map[string]error{"driver error": errPgBoom, "foreign key": foreignKey} {
		t.Run(name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(pgReply{err: cause})
			_, err := store.InsertRevision(t.Context(), plainRevision())
			requirePgWrapped(t, err, "intakes: insertar revisión: ", cause)
			requirePgKinds(t, fake, pgQuery)
		})
	}
}

// TestPostgres_InsertRevision_CipherFailure_DoesNotQuery: si el cifrador falla, el literal no se
// escribe de ninguna forma: error con su prefijo y ninguna sentencia.
func TestPostgres_InsertRevision_CipherFailure_DoesNotQuery(t *testing.T) {
	cause := errors.New("kms caído")
	store, fake := newFakePostgres(t, WithLiteralCipher(crypto.NewFieldCipher(pgBrokenKeys{cause: cause})))
	_, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindInterpreted, Payload: []byte(`{"v":1,"source_text":"dos empanadas"}`),
	})
	const prefix = "intakes: cifrar el literal de la revisión: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, quería el prefijo %q envolviendo la causa", err, prefix)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_InsertRevision_EvidenceIsLiteralToo: la evidence de una línea es literal del
// cliente igual que source_text: tampoco viaja en claro en la columna payload.
func TestPostgres_InsertRevision_EvidenceIsLiteralToo(t *testing.T) {
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)))
	fake.script(pgInserted(1, RevisionKindInterpreted, `{"v":1}`, nil, nil))
	_, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindInterpreted,
		Payload: []byte(`{"v":1,"lines":[{"sku":"A","evidence":"la de carne"}]}`),
	})
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	args := fake.statements()[0].args
	if payload, _ := args[2].([]byte); strings.Contains(string(payload), "la de carne") {
		t.Errorf("el payload que va a la base lleva la evidence en claro: %q", payload)
	}
	if enc, _ := args[5].([]byte); len(enc) == 0 {
		t.Error("literal_enc viajó vacío con una evidence en el payload")
	}
}

// wantInsertRevisionSQL es el INSERT numerador: el texto del viejo (internal/intakes/postgres.go:746) seguido de su
// proyección (:461).
const wantInsertRevisionSQL = `
	INSERT INTO public.intake_revisions
		(intake_id, revision_no, kind, payload, rendered_text, created_by,
		 literal_enc, literal_dek, literal_kek_id)
	SELECT $1::uuid, COALESCE(MAX(revision_no), 0) + 1, $2, $3::jsonb, $4, $5, $6, $7, $8
	FROM public.intake_revisions WHERE intake_id = $1::uuid
	RETURNING revision_no, kind, payload, rendered_text, created_by, created_at`

// TestPostgres_InsertRevision_SQLIsTheOldOneByteForByte: el INSERT numerador sale con el texto del
// paquete viejo, y es el MISMO dentro de una transacción (ReplaceItems) que suelto.
func TestPostgres_InsertRevision_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgInserted(1, RevisionKindCart, `{"v":1}`, nil, nil))
	if _, err := store.InsertRevision(t.Context(), plainRevision()); err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantInsertRevisionSQL)
}

// TestPostgres_InsertRevision_LiteralEnvelopeTravelsWhole: con literal, las tres columnas del
// sobre viajan con valor; sin literal y CON cifrador, las tres viajan a NULL (el cifrador ni se
// toca: un sobre vacío escrito como bytes vacíos engañaría al guard de la poda).
func TestPostgres_InsertRevision_LiteralEnvelopeTravelsWhole(t *testing.T) {
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)))
	fake.script(pgInserted(1, RevisionKindInterpreted, `{"v":1}`, nil, nil), pgInserted(2, RevisionKindCart, `{"v":1}`, nil, nil))
	sealed := Revision{IntakeID: pgIntakeID, Kind: RevisionKindInterpreted, Payload: []byte(`{"v":1,"source_text":"dos empanadas"}`)}
	if _, err := store.InsertRevision(t.Context(), sealed); err != nil {
		t.Fatalf("InsertRevision con literal: error inesperado %v", err)
	}
	if _, err := store.InsertRevision(t.Context(), plainRevision()); err != nil {
		t.Fatalf("InsertRevision sin literal: error inesperado %v", err)
	}
	stmts := fake.statements()
	for i, arg := range stmts[0].args[5:] {
		if arg == nil {
			t.Errorf("con literal, la columna %d del sobre viajó a NULL", i)
		}
	}
	if got := stmts[1].args[5:]; !reflect.DeepEqual(got, []driver.Value{nil, nil, nil}) {
		t.Errorf("sin literal, el sobre viajó como %v, quería tres NULL", got)
	}
}
