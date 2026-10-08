//go:build pendiente

package intakes

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
)

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte del bloqueo, de la consulta del evento
// vivo, del UPDATE a abandoned, del cierre del contenedor y del CAS de AbandonByEvent.

// pgDiscardable son los estados almacenados desde los que estos tests dejan descartar.
var pgDiscardable = []string{StatusOpen, StatusPendingApproval}

// TestPostgres_Discard_NotFound: id que no es UUID (sin tocar la base) y cabecera que no existe
// en ese tenant (revierte tras el bloqueo).
func TestPostgres_Discard_NotFound(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, err := store.Discard(t.Context(), pgTenant, "x", pgDiscardable); !errors.Is(err, ErrNotFound) {
		t.Errorf("Discard(id inválido) = %v, quería ErrNotFound", err)
	}
	requirePgUntouched(t, fake)

	out, err := store.Discard(t.Context(), pgTenant, pgIntakeID, pgDiscardable)
	if !errors.Is(err, ErrNotFound) || out != (DiscardOutcome{}) {
		t.Errorf("Discard(sin cabecera) = (%+v, %v), quería (DiscardOutcome{}, ErrNotFound)", out, err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgRollback)
	if lock := fake.statements()[0]; !lock.inTx || !reflect.DeepEqual(lock.args, []driver.Value{pgTenant, pgIntakeID}) {
		t.Errorf("bloqueo = %+v, quería tenant e id dentro de la transacción", lock)
	}
}

// TestPostgres_Discard_Outcomes: los cuatro desenlaces sin error y la forma de cada conversación.
// Los dos rechazos no escriben; el estado que vuelve es el de ORIGEN, normalizado.
func TestPostgres_Discard_Outcomes(t *testing.T) {
	abandoned := pgOne(pgIntakeRow(StatusAbandoned, 30)...)
	revision := pgInserted(2, RevisionKindDiscarded, `{"v":1}`, nil, RevisionByOwner)
	cases := []struct {
		name      string
		script    []pgReply
		want      DiscardOutcome
		wantKinds []string
	}{
		{"status is not discardable: the event is not even checked",
			[]pgReply{pgOne(StatusClosedLegacy, pgEventID)},
			DiscardOutcome{Status: StatusConfirmed},
			[]string{pgBegin, pgQuery, pgCommit}},
		{"parent event still open",
			[]pgReply{pgOne(StatusOpen, pgEventID), pgOne(true)},
			DiscardOutcome{Status: StatusOpen, LiveEvent: true},
			[]string{pgBegin, pgQuery, pgQuery, pgCommit}},
		{"discarded: update, revision and container closed",
			[]pgReply{pgOne(StatusPendingApproval, pgEventID), pgOne(false), abandoned, revision, {}},
			DiscardOutcome{Status: StatusPendingApproval, Discarded: true},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgQuery, pgExec, pgCommit}},
		{"legacy intake without event: nothing to check or close",
			[]pgReply{pgOne(StatusOpen, nil), abandoned, revision},
			DiscardOutcome{Status: StatusOpen, Discarded: true},
			[]string{pgBegin, pgQuery, pgQuery, pgQuery, pgCommit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			out, err := store.Discard(t.Context(), pgTenant, pgIntakeID, pgDiscardable)
			if err != nil || out != tc.want {
				t.Errorf("Discard = (%+v, %v), quería (%+v, nil)", out, err, tc.want)
			}
			requirePgKinds(t, fake, tc.wantKinds...)
		})
	}
}

// TestPostgres_Discard_WritesTheGuardedUpdateTheRevisionAndTheContainer: los argumentos de las
// tres escrituras: el UPDATE lleva el destino y, otra vez, los estados descartables; la revisión
// es `discarded`; y el contenedor se cierra por el id del evento.
func TestPostgres_Discard_WritesTheGuardedUpdateTheRevisionAndTheContainer(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(StatusPendingApproval, pgEventID), pgOne(false), pgOne(pgIntakeRow(StatusAbandoned, 30)...),
		pgInserted(2, RevisionKindDiscarded, `{"v":1}`, nil, RevisionByOwner), pgReply{})
	if _, err := store.Discard(t.Context(), pgTenant, pgIntakeID, pgDiscardable); err != nil {
		t.Fatalf("Discard: error inesperado %v", err)
	}
	stmts := fake.statements()
	if want := []driver.Value{pgEventID}; !reflect.DeepEqual(stmts[1].args, want) {
		t.Errorf("argumentos del evento vivo = %v, quería %v", stmts[1].args, want)
	}
	if want := []driver.Value{pgTenant, pgIntakeID, StatusAbandoned, pgDiscardable}; !reflect.DeepEqual(stmts[2].args, want) {
		t.Errorf("argumentos del UPDATE = %v, quería %v", stmts[2].args, want)
	}
	if stmts[3].args[0] != pgIntakeID || stmts[3].args[1] != RevisionKindDiscarded {
		t.Errorf("revisión escrita = %v, quería una discarded de la solicitud", stmts[3].args[:2])
	}
	if want := []driver.Value{pgEventID}; stmts[4].kind != pgExec || !reflect.DeepEqual(stmts[4].args, want) {
		t.Errorf("cierre del contenedor = %+v, quería un exec con %v", stmts[4], want)
	}
	for i, s := range stmts {
		if !s.inTx {
			t.Errorf("la sentencia %d salió fuera de la transacción", i)
		}
	}
}

// TestPostgres_Discard_Errors: cada fallo sale con su prefijo y revierte. Un UPDATE que no casa
// ninguna fila es la rotura de un invariante: error, no rechazo silencioso.
func TestPostgres_Discard_Errors(t *testing.T) {
	lock := pgOne(StatusOpen, pgEventID)
	boom := pgReply{err: errPgBoom}
	abandoned := pgOne(pgIntakeRow(StatusAbandoned, 30)...)
	revision := pgInserted(2, RevisionKindDiscarded, `{"v":1}`, nil, nil)
	cases := []struct {
		name   string
		script []pgReply
		prefix string
		cause  error
	}{
		{"lock fails", []pgReply{boom}, "intakes: bloquear la solicitud para descartarla: ", errPgBoom},
		{"live event check fails", []pgReply{lock, boom}, "intakes: comprobar el evento vivo de la solicitud: ", errPgBoom},
		{"update fails", []pgReply{lock, pgOne(false), boom}, "intakes: descartar la solicitud: intakes: leer solicitud: ", errPgBoom},
		{"update matches no row", []pgReply{lock, pgOne(false), {}}, "intakes: descartar la solicitud: ", sql.ErrNoRows},
		{"revision insert fails", []pgReply{lock, pgOne(false), abandoned, boom}, "intakes: insertar revisión: ", errPgBoom},
		{"container close fails", []pgReply{lock, pgOne(false), abandoned, revision, boom}, "intakes: cerrar el contenedor de la solicitud descartada: ", errPgBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			out, err := store.Discard(t.Context(), pgTenant, pgIntakeID, pgDiscardable)
			requirePgWrapped(t, err, tc.prefix, tc.cause)
			if out != (DiscardOutcome{}) {
				t.Errorf("Discard con error devolvió %+v, quería DiscardOutcome{}", out)
			}
			kinds := fake.kinds()
			if kinds[len(kinds)-1] != pgRollback {
				t.Errorf("conversación = %v, quería acabar en rollback", kinds)
			}
		})
	}
}

// TestPostgres_AbandonByEvent: un CAS en UNA sentencia suelta, de open a abandoned y acotado por
// tenant y evento; 0 filas es éxito; un evento que no es UUID no toca la base; no escribe revisión.
func TestPostgres_AbandonByEvent(t *testing.T) {
	store, fake := newFakePostgres(t)
	if err := store.AbandonByEvent(t.Context(), pgTenant, "no-es-un-uuid"); err != nil {
		t.Errorf("AbandonByEvent(evento inválido) = %v, quería nil", err)
	}
	requirePgUntouched(t, fake)

	if err := store.AbandonByEvent(t.Context(), pgTenant, pgEventID); err != nil {
		t.Fatalf("AbandonByEvent: error inesperado %v", err)
	}
	requirePgKinds(t, fake, pgExec)
	want := []driver.Value{pgTenant, pgEventID, StatusAbandoned, StatusOpen}
	if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, want) {
		t.Errorf("sentencia = %+v, quería una suelta con %v", stmt, want)
	}

	fake.script(pgReply{err: errPgBoom})
	err := store.AbandonByEvent(t.Context(), pgTenant, pgEventID)
	requirePgWrapped(t, err, "intakes: abandonar la solicitud del evento "+pgEventID+": ", errPgBoom)
}
