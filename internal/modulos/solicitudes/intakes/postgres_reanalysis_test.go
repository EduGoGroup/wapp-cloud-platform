package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
)

// TestPostgres_ReanalysisTargetOf_NotFoundWithoutQuerying: un receptor nil y un id que no es UUID
// son ErrNotFound sin tocar la base (y sin panic).
func TestPostgres_ReanalysisTargetOf_NotFoundWithoutQuerying(t *testing.T) {
	var nilStore *Postgres
	if got, err := nilStore.ReanalysisTargetOf(t.Context(), pgTenant, pgIntakeID); !errors.Is(err, ErrNotFound) || got != (ReanalysisTarget{}) {
		t.Errorf("receptor nil = (%+v, %v), quería (ReanalysisTarget{}, ErrNotFound)", got, err)
	}
	store, fake := newFakePostgres(t)
	if got, err := store.ReanalysisTargetOf(t.Context(), pgTenant, "no-es-un-uuid"); !errors.Is(err, ErrNotFound) || got != (ReanalysisTarget{}) {
		t.Errorf("id inválido = (%+v, %v), quería (ReanalysisTarget{}, ErrNotFound)", got, err)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_ReanalysisTargetOf_ReadsTheSnapshot: UNA lectura suelta acotada por tenant; el
// estado sale normalizado; una solicitud legada sin evento y sin revisiones sigue siendo su fila.
func TestPostgres_ReanalysisTargetOf_ReadsTheSnapshot(t *testing.T) {
	cases := []struct {
		name string
		row  []driver.Value
		want ReanalysisTarget
	}{
		{"with event and revisions", []driver.Value{pgSession, pgContact, pgEventID, StatusClosedLegacy, int64(4)},
			ReanalysisTarget{SessionID: pgSession, ContactID: pgContact, EventID: pgEventID, Status: StatusConfirmed, LastRevisionNo: 4}},
		{"legacy intake without event or revisions", []driver.Value{pgSession, pgContact, "", StatusOpen, int64(0)},
			ReanalysisTarget{SessionID: pgSession, ContactID: pgContact, Status: StatusOpen}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(pgOne(tc.row...))
			got, err := store.ReanalysisTargetOf(t.Context(), pgTenant, pgIntakeID)
			if err != nil || got != tc.want {
				t.Errorf("ReanalysisTargetOf = (%+v, %v), quería (%+v, nil)", got, err, tc.want)
			}
			requirePgKinds(t, fake, pgQuery)
			if stmt := fake.statements()[0]; stmt.inTx || !reflect.DeepEqual(stmt.args, []driver.Value{pgTenant, pgIntakeID}) {
				t.Errorf("sentencia = %+v, quería una suelta acotada por tenant e id", stmt)
			}
		})
	}
}

// TestPostgres_ReanalysisTargetOf_NoRowAndFailure: sin fila en ese tenant es ErrNotFound; un fallo
// de la base sale envuelto con su prefijo.
func TestPostgres_ReanalysisTargetOf_NoRowAndFailure(t *testing.T) {
	store, fake := newFakePostgres(t)
	if got, err := store.ReanalysisTargetOf(t.Context(), pgTenant, pgIntakeID); !errors.Is(err, ErrNotFound) || got != (ReanalysisTarget{}) {
		t.Errorf("sin fila = (%+v, %v), quería (ReanalysisTarget{}, ErrNotFound)", got, err)
	}
	fake.script(pgReply{err: errPgBoom})
	got, err := store.ReanalysisTargetOf(t.Context(), pgTenant, pgIntakeID)
	requirePgWrapped(t, err, "intakes: leer la solicitud a re-analizar: ", errPgBoom)
	if got != (ReanalysisTarget{}) {
		t.Errorf("con error devolvió %+v, quería ReanalysisTarget{}", got)
	}
}

// Las sentencias, escritas APARTE y byte a byte (sangría y saltos de línea incluidos): son las
// del paquete viejo, y un cambio en el SQL de producción tiene que romper aquí.

// wantReanalysisTargetSQL es la lectura de la foto: el COALESCE del evento y la subconsulta de la última revisión.
const wantReanalysisTargetSQL = `
	SELECT i.session_id, i.contact_id, COALESCE(i.event_id::text, ''), i.status,
	       COALESCE((SELECT MAX(r.revision_no)
	                   FROM public.intake_revisions r
	                  WHERE r.intake_id = i.id), 0)
	  FROM public.intakes i
	 WHERE i.tenant_id = $1 AND i.id = $2
`

// TestPostgres_ReanalysisTargetOf_SQLIsTheOldOneByteForByte: la lectura sale con el texto del
// paquete viejo.
func TestPostgres_ReanalysisTargetOf_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(pgSession, pgContact, "", StatusOpen, int64(0)))
	if _, err := store.ReanalysisTargetOf(t.Context(), pgTenant, pgIntakeID); err != nil {
		t.Fatalf("ReanalysisTargetOf: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantReanalysisTargetSQL)
}
