package intakes

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
)

// Este fichero cierra las promesas del literal y de la revalidación que la medición combinada de
// F9-05 (D-42) encontró sin ejecutar: el payload que SplitLiteral rechaza al escribir, el literal
// que no cabe en su revisión al leer —en el adaptador y en el doble— y los tres fallos de
// ApplyRevalidation posteriores al total. Todo sobre el driver de mentira: sin Postgres.

// TestPostgres_InsertRevision_PayloadSplitLiteralRejects_DoesNotQuery: un payload cuyo
// `source_text` no es una cadena no es del contrato §7.4. InsertRevision devuelve el error de
// SplitLiteral TAL CUAL —sin prefijo propio— y no emite ninguna sentencia: dejarlo pasar sería
// persistir en claro algo que se llama `source_text`.
func TestPostgres_InsertRevision_PayloadSplitLiteralRejects_DoesNotQuery(t *testing.T) {
	payload := []byte(`{"v":1,"source_text":7}`)
	_, _, want := SplitLiteral(payload)
	if want == nil {
		t.Fatal("SplitLiteral aceptó un source_text numérico: el caso ya no prueba nada")
	}
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)))
	rev, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindInterpreted, Payload: payload,
	})
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("error = %v, quería el de SplitLiteral tal cual (%v)", err, want)
	}
	if prefix := "intakes: source_text del payload no es una cadena: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, quería el prefijo %q", err.Error(), prefix)
	}
	if !reflect.DeepEqual(rev, Revision{}) {
		t.Errorf("con error devolvió %+v, quería Revision{}", rev)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_Revisions_LiteralThatDoesNotFitFailsTheRead: el sobre abre y trae un literal bien
// formado, pero su evidencia apunta a una línea que la revisión no tiene. La lectura falla
// nombrando la revisión y la solicitud, seguida del error de MergeLiteral tal cual: devolver «la
// revisión, pero sin su original» dejaría al dueño comparando contra un hueco.
func TestPostgres_Revisions_LiteralThatDoesNotFitFailsTheRead(t *testing.T) {
	store, fake := newFakePostgres(t, WithLiteralCipher(newPgCipher(t)))
	scriptGetWithRevisions(fake, pgSealedRow(t, 2, `{"v":1,"lines":[{"sku":"a"}]}`, `{"evidence":{"3":"dos de carne"}}`, pgYoungAge))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	const want = "intakes: revisión 2 de la solicitud " + pgIntakeID + ": " +
		`intakes: la evidencia "3" no corresponde a ninguna de las 1 líneas de la revisión`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, quería %q", err, want)
	}
	if !reflect.DeepEqual(d, Detail{}) {
		t.Errorf("Get con error devolvió %+v, quería Detail{}", d)
	}
}

// TestPostgres_ApplyRevalidation_RevisionAndRereadErrors: los fallos de DESPUÉS del total —la
// escritura de la revisión y las dos relecturas— salen con el prefijo de su dueño y revierten la
// transacción, igual que en ReplaceItems: los cambios de precio y su rastro se confirman juntos o
// no se confirma ninguno.
func TestPostgres_ApplyRevalidation_RevisionAndRereadErrors(t *testing.T) {
	lock := pgOne(StatusPendingApproval)
	total := pgOne(pgIntakeRow(StatusPendingApproval, 24)...)
	inserted := pgInserted(3, RevisionKindRevalidated, `{"v":1}`, "Cambió un precio", nil)
	items := pgReply{rows: [][]driver.Value{pgItemRow("A", 2, 12)}}
	boom := pgReply{err: errPgBoom}
	cases := []struct {
		name   string
		script []pgReply
		prefix string
	}{
		{"revision insert fails", []pgReply{lock, {}, {}, total, boom}, "intakes: insertar revisión: "},
		{"items reread fails", []pgReply{lock, {}, {}, total, inserted, boom}, "intakes: listar líneas: "},
		{"revisions reread fails", []pgReply{lock, {}, {}, total, inserted, items, boom}, "intakes: listar revisiones: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			d, err := store.ApplyRevalidation(t.Context(), pgTenant, pgIntakeID, pgRevalidation, "Cambió un precio", pgEditable)
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			if !reflect.DeepEqual(d, Detail{}) {
				t.Errorf("ApplyRevalidation con error devolvió %+v, quería Detail{}", d)
			}
			kinds := fake.kinds()
			if kinds[len(kinds)-1] != pgRollback {
				t.Errorf("conversación = %v, quería acabar en rollback", kinds)
			}
		})
	}
}

// TestMemoryStore_Revisions_LiteralThatDoesNotFitIsLoggedAndTheReadGoesOn: si el literal guardado
// aparte no se puede devolver a su revisión, el doble NO tumba la lectura de la bandeja: lo deja
// en nivel Error —con la solicitud, el número y la causa, y sin el texto— y la revisión sale con
// su interpretación sola. Es la divergencia consciente con el adaptador, que ahí falla.
//
// Por la API pública no hay forma de llegar: lo que parte SplitLiteral siempre cabe de vuelta en
// el payload que él mismo limpió. Se descoloca el literal guardado a mano, que es el equivalente
// en este doble a una revisión que «cambió de forma bajo el sobre».
func TestMemoryStore_Revisions_LiteralThatDoesNotFitIsLoggedAndTheReadGoesOn(t *testing.T) {
	sink := &pgLogSink{}
	store := NewMemoryStore()
	store.SetRetentionLog(sink)
	written, err := store.InsertRevision(t.Context(), Revision{
		IntakeID: pgIntakeID, Kind: RevisionKindInterpreted,
		Payload: []byte(`{"v":1,"source_text":"dos empanadas","lines":[{"sku":"a"}]}`),
	})
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	const phrase = "frase que no tiene línea"
	store.mu.Lock()
	store.literals[pgIntakeID][written.RevisionNo] = LiteralRevision{Evidence: map[string]string{"7": phrase}}
	store.mu.Unlock()

	revs := store.Revisions(pgIntakeID)
	if len(revs) != 1 {
		t.Fatalf("Revisions devolvió %d revisiones, quería 1: la lectura no se cae", len(revs))
	}
	if got := string(revs[0].Payload); got != string(written.Payload) {
		t.Errorf("payload = %s, quería la interpretación sola (%s)", got, written.Payload)
	}
	logged := sink.logged()
	if len(logged) != 1 {
		t.Fatalf("se registraron %d entradas, quería 1: %+v", len(logged), logged)
	}
	wantLiteralRetentionEntry(t, logged[0], written.RevisionNo, phrase)
	// Lo guardado no se toca: la siguiente lectura vuelve a intentarlo (y vuelve a avisar).
	if persisted := store.PersistedRevisions(pgIntakeID); len(persisted) != 1 || string(persisted[0].Payload) != string(written.Payload) {
		t.Errorf("lo persistido cambió con la lectura: %+v", persisted)
	}
}

// wantLiteralRetentionEntry exige la entrada que promete el contrato cuando el literal no vuelve a
// su revisión: nivel Error, el mensaje, la solicitud, el número y la causa, y ni rastro del texto
// del cliente.
func wantLiteralRetentionEntry(t *testing.T, entry pgLogEntry, revisionNo int, phrase string) {
	t.Helper()
	if entry.level != "error" || entry.msg != "retención: no se pudo devolver el literal a la revisión" {
		t.Errorf("entrada = %s %q, quería un error con el mensaje del contrato", entry.level, entry.msg)
	}
	const cause = `intakes: la evidencia "7" no corresponde a ninguna de las 1 líneas de la revisión`
	if len(entry.args) != 6 || entry.args[0] != "intake_id" || entry.args[1] != pgIntakeID ||
		entry.args[2] != "revision_no" || entry.args[3] != revisionNo || entry.args[4] != "error" {
		t.Fatalf("campos = %v, quería intake_id, revision_no y error", entry.args)
	}
	if got, ok := entry.args[5].(error); !ok || got.Error() != cause {
		t.Errorf("causa registrada = %v, quería %q", entry.args[5], cause)
	}
	for _, arg := range entry.args {
		if s, ok := arg.(string); ok && strings.Contains(s, phrase) {
			t.Errorf("el log lleva texto del cliente: %q", s)
		}
	}
}
