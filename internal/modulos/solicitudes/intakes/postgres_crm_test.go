//go:build pendiente

package intakes

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte de la sentencia del reflejo (la CTE que
// bloquea, escribe y dice si cambió) y de la relectura.

// TestPostgres_ReflectCRMStatus_UnknownStatus_DoesNotQuery: un estado fuera del vocabulario
// canónico se rechaza con su texto exacto sin tocar la base.
func TestPostgres_ReflectCRMStatus_UnknownStatus_DoesNotQuery(t *testing.T) {
	store, fake := newFakePostgres(t)
	out, err := store.ReflectCRMStatus(t.Context(), pgTenant, pgIntakeID, "shipped", "", pgAt)
	const want = `intakes: "shipped" no es un estado canónico del CRM`
	if err == nil || err.Error() != want || !reflect.DeepEqual(out, CRMReflection{}) {
		t.Errorf("ReflectCRMStatus = (%+v, %v), quería (CRMReflection{}, %q)", out, err, want)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_ReflectCRMStatus_NoRecipient_RollsBackExplicitly: si la solicitud no existe o es
// de otro tenant, Found=false y sin error; y la transacción se REVIERTE antes de volver: no queda
// abandonada reteniendo la conexión. Tampoco se relee nada.
func TestPostgres_ReflectCRMStatus_NoRecipient_RollsBackExplicitly(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(int64(0), false))
	out, err := store.ReflectCRMStatus(t.Context(), pgTenant, pgIntakeID, CRMStatusPaid, "ref-1", pgAt)
	if err != nil || !reflect.DeepEqual(out, CRMReflection{}) {
		t.Errorf("ReflectCRMStatus = (%+v, %v), quería (CRMReflection{}, nil)", out, err)
	}
	requirePgKinds(t, fake, pgBegin, pgQuery, pgRollback)
}

// TestPostgres_ReflectCRMStatus_Found_ReflectsAndRereadsInOneTx: la sentencia del reflejo lleva
// tenant, id, estado, referencia y el instante del llamante; la cabecera se relee en la MISMA
// transacción y Changed es lo que dijo la base.
func TestPostgres_ReflectCRMStatus_Found_ReflectsAndRereadsInOneTx(t *testing.T) {
	for _, changed := range []bool{true, false} {
		store, fake := newFakePostgres(t)
		fake.script(pgOne(int64(1), changed), pgOne(pgIntakeRow(StatusClosedLegacy, 30)...))
		out, err := store.ReflectCRMStatus(t.Context(), pgTenant, pgIntakeID, CRMStatusPreparing, "ref-1", pgAt)
		if err != nil {
			t.Fatalf("ReflectCRMStatus: error inesperado %v", err)
		}
		if want := (CRMReflection{Found: true, Changed: changed, Intake: pgIntake(StatusConfirmed, 30)}); !reflect.DeepEqual(out, want) {
			t.Errorf("ReflectCRMStatus = %+v, quería %+v", out, want)
		}
		requirePgKinds(t, fake, pgBegin, pgQuery, pgQuery, pgCommit)
		stmts := fake.statements()
		if want := []driver.Value{pgTenant, pgIntakeID, CRMStatusPreparing, "ref-1", pgAt}; !stmts[0].inTx || !reflect.DeepEqual(stmts[0].args, want) {
			t.Errorf("argumentos del reflejo = %v, quería %v dentro de la transacción", stmts[0].args, want)
		}
		if want := []driver.Value{pgTenant, pgIntakeID}; !stmts[1].inTx || !reflect.DeepEqual(stmts[1].args, want) {
			t.Errorf("argumentos de la relectura = %v, quería %v dentro de la transacción", stmts[1].args, want)
		}
	}
}

// TestPostgres_ReflectCRMStatus_DoesNotValidateTheID: al revés que el resto del adaptador, un id
// que no es UUID llega a la base (lo valida la frontera).
func TestPostgres_ReflectCRMStatus_DoesNotValidateTheID(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(int64(0), false))
	if _, err := store.ReflectCRMStatus(t.Context(), pgTenant, "no-es-un-uuid", CRMStatusPaid, "", pgAt); err != nil {
		t.Fatalf("ReflectCRMStatus: error inesperado %v", err)
	}
	if stmts := fake.statements(); len(stmts) != 1 || stmts[0].args[1] != "no-es-un-uuid" {
		t.Errorf("sentencias = %+v, quería el reflejo con el id tal cual", stmts)
	}
}

// TestPostgres_ReflectCRMStatus_Errors: cada fallo con su texto exacto, y la transacción revertida
// salvo cuando lo que falla es abrirla o confirmarla.
func TestPostgres_ReflectCRMStatus_Errors(t *testing.T) {
	found := pgOne(int64(1), true)
	cases := []struct {
		name                    string
		begin, commit, rollback error
		script                  []pgReply
		want                    string
		cause                   error
		wantKinds               []string
	}{
		{name: "begin fails", begin: errPgBoom, want: "intakes: abrir transacción del reflejo: base caída", cause: errPgBoom},
		{name: "reflect fails", script: []pgReply{{err: errPgBoom}}, want: "intakes: reflejar el estado del CRM: base caída", cause: errPgBoom,
			wantKinds: []string{pgBegin, pgQuery, pgRollback}},
		{name: "reread fails", script: []pgReply{found, {err: errPgBoom}},
			want: "intakes: releer la solicitud reflejada: intakes: leer solicitud: base caída", cause: errPgBoom,
			wantKinds: []string{pgBegin, pgQuery, pgQuery, pgRollback}},
		{name: "reread finds no row", script: []pgReply{found, {}},
			want: "intakes: releer la solicitud reflejada: sql: no rows in result set", cause: sql.ErrNoRows,
			wantKinds: []string{pgBegin, pgQuery, pgQuery, pgRollback}},
		{name: "commit fails", commit: errPgBoom, script: []pgReply{found, pgOne(pgIntakeRow(StatusOpen, 1)...)},
			want: "intakes: confirmar el reflejo: base caída", cause: errPgBoom,
			wantKinds: []string{pgBegin, pgQuery, pgQuery}},
		{name: "rollback of a reflection without recipient fails", rollback: errPgBoom, script: []pgReply{pgOne(int64(0), false)},
			want: "intakes: cerrar la transacción de un reflejo sin destinatario: base caída", cause: errPgBoom,
			wantKinds: []string{pgBegin, pgQuery, pgRollback}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.failTx(tc.begin, tc.commit, tc.rollback)
			fake.script(tc.script...)
			out, err := store.ReflectCRMStatus(t.Context(), pgTenant, pgIntakeID, CRMStatusPaid, "", pgAt)
			if err == nil || err.Error() != tc.want || !errors.Is(err, tc.cause) {
				t.Errorf("error = %v, quería %q envolviendo su causa", err, tc.want)
			}
			if !reflect.DeepEqual(out, CRMReflection{}) {
				t.Errorf("con error devolvió %+v, quería CRMReflection{}", out)
			}
			requirePgKinds(t, fake, tc.wantKinds...)
		})
	}
}

// TestPostgres_ReflectCRMStatus_RollbackFailureIsJoined: si además del fallo falla su rollback,
// viajan los dos: el original y el del rollback con su prefijo.
func TestPostgres_ReflectCRMStatus_RollbackFailureIsJoined(t *testing.T) {
	rollbackBoom := errors.New("rollback roto")
	store, fake := newFakePostgres(t)
	fake.failTx(nil, nil, rollbackBoom)
	fake.script(pgReply{err: errPgBoom})
	_, err := store.ReflectCRMStatus(t.Context(), pgTenant, pgIntakeID, CRMStatusPaid, "", pgAt)
	if !errors.Is(err, errPgBoom) || !errors.Is(err, rollbackBoom) {
		t.Fatalf("error = %v, quería el fallo y el de su rollback unidos", err)
	}
	for _, want := range []string{"intakes: reflejar el estado del CRM: base caída", "intakes: rollback del reflejo: rollback roto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el error %q no lleva %q", err, want)
		}
	}
}
