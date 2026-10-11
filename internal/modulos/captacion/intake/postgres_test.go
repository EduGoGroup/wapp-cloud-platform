package intake

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"
)

// Tres de las cuatro sentencias de la cola (la cuarta, la de CloseWithSourceText, está con sus
// tests en postgres_close_test.go), escritas APARTE y byte a byte (sangría y saltos de línea
// incluidos): son las del paquete viejo, y un cambio en el SQL de producción tiene que romper
// aquí. 🔴 Sin Postgres, este texto es lo ÚNICO que custodia las guardas que viven en SQL (el
// predicado del `ON CONFLICT` y el `status = 'aggregating'` del cierre): su conducta la prueba
// intakehelpertest.ContratoQueue contra Postgres real, en los procesos de F9 (hallazgo 63).
const (
	wantOpenOrAppendSQL = `
INSERT INTO public.intake_jobs
       (tenant_id, session_id, contact_id, event_id, status, message_ts, source_refs)
VALUES ($1, $2, $3, $4::uuid, 'aggregating', $5, $6::jsonb)
ON CONFLICT (tenant_id, session_id, contact_id, event_id) WHERE status = 'aggregating'
DO UPDATE SET
       source_refs = intake_jobs.source_refs || EXCLUDED.source_refs,
       updated_at  = now()
`
	wantCloseWindowSQL = `
UPDATE public.intake_jobs
   SET status = 'pending', updated_at = now()
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
   AND status = 'aggregating'
`
	wantListAggregatingSQL = `
SELECT id::text, tenant_id, session_id, contact_id, event_id::text,
       updated_at, created_at
  FROM public.intake_jobs
 WHERE status = 'aggregating'
 ORDER BY created_at
 LIMIT $1
`
)

// pgKey es la clave de ventana de los tests del adaptador.
var pgKey = WindowKey{TenantID: "tenant-1", SessionID: "session-1", ContactID: "contact-1", EventID: "11111111-1111-4111-8111-111111111111"}

// incompleteKeys son las cuatro formas de dejar la clave a medias.
func incompleteKeys() map[string]WindowKey {
	noTenant, noSession, noContact, noEvent := pgKey, pgKey, pgKey, pgKey
	noTenant.TenantID, noSession.SessionID, noContact.ContactID, noEvent.EventID = "", "", "", ""
	return map[string]WindowKey{"no tenant": noTenant, "no session": noSession, "no contact": noContact, "no event": noEvent}
}

// TestNewPostgres_DoesNotQuery: construir el store no toca la base.
func TestNewPostgres_DoesNotQuery(t *testing.T) {
	store, fake := newFakePostgres(t)
	if store == nil {
		t.Fatal("NewPostgres devolvió nil")
	}
	fake.requireUntouched(t)
}

// TestPostgres_Queue_NilReceiverOrNilDB_IsANoOp: un *Postgres nil, o uno sin base, no tiene dónde
// escribir: las cuatro operaciones de la cola son un no-op sin error y sin panic.
func TestPostgres_Queue_NilReceiverOrNilDB_IsANoOp(t *testing.T) {
	ctx := context.Background()
	for name, p := range map[string]*Postgres{"nil receiver": nil, "nil db": NewPostgres(nil)} {
		t.Run(name, func(t *testing.T) {
			if err := p.OpenOrAppend(ctx, Append{Key: pgKey}); err != nil {
				t.Errorf("OpenOrAppend = %v, quería nil", err)
			}
			if ok, err := p.CloseWindow(ctx, pgKey); ok || err != nil {
				t.Errorf("CloseWindow = (%v, %v), quería (false, nil)", ok, err)
			}
			if got, err := p.ListAggregating(ctx, 10); got != nil || err != nil {
				t.Errorf("ListAggregating = (%v, %v), quería (nil, nil)", got, err)
			}
			if ok, err := p.CloseWithSourceText(ctx, pgSeen, pgEnvelope); ok || err != nil {
				t.Errorf("CloseWithSourceText = (%v, %v), quería (false, nil)", ok, err)
			}
		})
	}
}

// TestPostgres_IncompleteKey_RejectedBeforeTheDatabase: una clave a medias no es «una ventana
// rara», es un INSERT que revienta: las dos escrituras por tupla la rechazan en Go, cada una con
// su texto, sin mandar nada a la base. (La tercera, CloseWithSourceText, en postgres_close_test.go.)
func TestPostgres_IncompleteKey_RejectedBeforeTheDatabase(t *testing.T) {
	ctx := context.Background()
	for name, k := range incompleteKeys() {
		t.Run(name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			err := store.OpenOrAppend(ctx, Append{Key: k})
			if err == nil || err.Error() != "intake: clave de ventana incompleta (tenant/session/contact/event)" {
				t.Errorf("OpenOrAppend = %v, quería el rechazo de la clave", err)
			}
			ok, err := store.CloseWindow(ctx, k)
			if ok || err == nil || err.Error() != "intake: clave de ventana incompleta al cerrar" {
				t.Errorf("CloseWindow = (%v, %v), quería (false, el rechazo de la clave)", ok, err)
			}
			fake.requireUntouched(t)
		})
	}
}

// TestPostgres_OpenOrAppend_EmitsOneStatement: UNA sentencia, la literal, sin lectura previa. El
// instante viaja en UTC y las referencias como un array JSON hecho con el marshaller (un
// identificador con comillas no rompe el JSON).
func TestPostgres_OpenOrAppend_EmitsOneStatement(t *testing.T) {
	lima := time.FixedZone("lima", -5*3600)
	ts := time.Date(2026, 8, 22, 5, 0, 0, 0, lima)
	store, fake := newFakePostgres(t)
	a := Append{Key: pgKey, MessageTS: ts, Refs: []string{"wamid.one", `me"dia`}}
	if err := store.OpenOrAppend(context.Background(), a); err != nil {
		t.Fatalf("OpenOrAppend: error inesperado %v", err)
	}
	stmt := fake.requireOnly(t, fakeExec)
	requireSQL(t, stmt, wantOpenOrAppendSQL)
	want := []driver.Value{pgKey.TenantID, pgKey.SessionID, pgKey.ContactID, pgKey.EventID, ts.UTC(), `["wamid.one","me\"dia"]`}
	if !reflect.DeepEqual(stmt.args, want) {
		t.Errorf("argumentos = %#v, quería %#v", stmt.args, want)
	}
	if sent, ok := stmt.args[4].(time.Time); !ok || sent.Location() != time.UTC {
		t.Errorf("message_ts viaja como %#v, quería un instante en UTC", stmt.args[4])
	}
}

// TestPostgres_OpenOrAppend_NoRefsNoTimestamp: sin referencias viaja el array vacío `[]` (nunca
// `null`: `[] || []` sigue siendo válido), y un instante cero viaja como NULL, no como el año 1.
func TestPostgres_OpenOrAppend_NoRefsNoTimestamp(t *testing.T) {
	for name, refs := range map[string][]string{"nil refs": nil, "empty refs": {}} {
		t.Run(name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			if err := store.OpenOrAppend(context.Background(), Append{Key: pgKey, Refs: refs}); err != nil {
				t.Fatalf("OpenOrAppend: error inesperado %v", err)
			}
			stmt := fake.requireOnly(t, fakeExec)
			if stmt.args[4] != nil {
				t.Errorf("message_ts = %#v, quería NULL para el instante cero", stmt.args[4])
			}
			if stmt.args[5] != "[]" {
				t.Errorf("source_refs = %#v, quería \"[]\"", stmt.args[5])
			}
		})
	}
}

// TestPostgres_OpenOrAppend_DatabaseFailure_IsWrapped: el fallo de la base sale envuelto con su
// prefijo.
func TestPostgres_OpenOrAppend_DatabaseFailure_IsWrapped(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(fakeReply{err: errFakeBoom})
	err := store.OpenOrAppend(context.Background(), Append{Key: pgKey})
	requireWrapped(t, err, errFakeBoom, "intake: abrir/ampliar ventana de captación: ")
}

// TestPostgres_CloseWindow_TrueOnlyIfItTouchedARow: UNA sentencia, la literal, con la tupla;
// true si afectó alguna fila y (false, nil) si ninguna —el segundo cierre—.
func TestPostgres_CloseWindow_TrueOnlyIfItTouchedARow(t *testing.T) {
	for affected, want := range map[int64]bool{0: false, 1: true} {
		store, fake := newFakePostgres(t)
		fake.script(fakeReply{affected: affected})
		ok, err := store.CloseWindow(context.Background(), pgKey)
		if err != nil || ok != want {
			t.Errorf("CloseWindow con %d filas afectadas = (%v, %v), quería (%v, nil)", affected, ok, err, want)
		}
		stmt := fake.requireOnly(t, fakeExec)
		requireSQL(t, stmt, wantCloseWindowSQL)
		wantArgs := []driver.Value{pgKey.TenantID, pgKey.SessionID, pgKey.ContactID, pgKey.EventID}
		if !reflect.DeepEqual(stmt.args, wantArgs) {
			t.Errorf("argumentos = %#v, quería %#v", stmt.args, wantArgs)
		}
	}
}

// TestPostgres_CloseWindow_Failures_AreWrapped: el fallo de la sentencia y el de contar las
// filas salen envueltos, cada uno con su prefijo, y nunca como un cierre hecho.
func TestPostgres_CloseWindow_Failures_AreWrapped(t *testing.T) {
	cases := map[string]fakeReply{
		"intake: cerrar ventana de captación: ": {err: errFakeBoom},
		"intake: contar filas cerradas: ":       {affected: 1, affectedErr: errFakeBoom},
	}
	for prefix, reply := range cases {
		store, fake := newFakePostgres(t)
		fake.script(reply)
		ok, err := store.CloseWindow(context.Background(), pgKey)
		requireWrapped(t, err, errFakeBoom, prefix)
		if ok {
			t.Errorf("CloseWindow con error devolvió true (%q)", prefix)
		}
	}
}

// TestPostgres_ListAggregating_NonPositiveLimit_DoesNotQuery: un límite cero o negativo es una
// lista vacía sin ir a la base.
func TestPostgres_ListAggregating_NonPositiveLimit_DoesNotQuery(t *testing.T) {
	store, fake := newFakePostgres(t)
	for _, limit := range []int{0, -3} {
		if got, err := store.ListAggregating(context.Background(), limit); got != nil || err != nil {
			t.Errorf("ListAggregating(%d) = (%v, %v), quería (nil, nil)", limit, got, err)
		}
	}
	fake.requireUntouched(t)
}

// TestPostgres_ListAggregating_MapsTheTwoAnchors: UNA consulta, la literal, con el límite; cada
// fila sale con su clave y sus DOS anclas en su sitio —la sexta columna es LastActivity
// (`updated_at`) y la séptima CreatedAt— y en el orden en que las da la base.
func TestPostgres_ListAggregating_MapsTheTwoAnchors(t *testing.T) {
	created := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	touched := created.Add(40 * time.Second)
	store, fake := newFakePostgres(t)
	fake.script(fakeReply{rows: [][]driver.Value{
		{"job-b", "tenant-2", "session-2", "contact-2", "event-2", touched, created},
		{"job-a", "tenant-1", "session-1", "contact-1", "event-1", created, created},
	}})
	got, err := store.ListAggregating(context.Background(), 25)
	if err != nil {
		t.Fatalf("ListAggregating: error inesperado %v", err)
	}
	want := []OpenJob{
		{ID: "job-b", Key: WindowKey{"tenant-2", "session-2", "contact-2", "event-2"}, LastActivity: touched, CreatedAt: created},
		{ID: "job-a", Key: WindowKey{"tenant-1", "session-1", "contact-1", "event-1"}, LastActivity: created, CreatedAt: created},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListAggregating = %+v, quería %+v", got, want)
	}
	stmt := fake.requireOnly(t, fakeQuery)
	requireSQL(t, stmt, wantListAggregatingSQL)
	if !reflect.DeepEqual(stmt.args, []driver.Value{25}) {
		t.Errorf("argumentos = %#v, quería el límite [25]", stmt.args)
	}
}

// TestPostgres_ListAggregating_Failures_AreWrapped: los cuatro fallos posibles salen envueltos,
// cada uno con su prefijo, y sin filas a medias. Un ancla NULL no se convierte en un instante
// cero —que cerraría la ventana al momento—: falla el scan. El fallo del cierre no pisa a uno
// anterior.
func TestPostgres_ListAggregating_Failures_AreWrapped(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	good := []driver.Value{"job-a", "t", "s", "c", "e", at, at}
	nullAnchor := []driver.Value{"job-b", "t", "s", "c", "e", nil, at}
	closeBoom := errors.New("cierre roto")
	cases := []struct {
		name   string
		reply  fakeReply
		prefix string
		cause  error
	}{
		{"query fails", fakeReply{err: errFakeBoom}, "intake: listar ventanas vivas: ", errFakeBoom},
		{"null anchor does not scan", fakeReply{rows: [][]driver.Value{good, nullAnchor}}, "intake: scan de ventana viva: ", nil},
		{"iteration fails", fakeReply{rows: [][]driver.Value{good}, endErr: errFakeBoom}, "intake: iterar ventanas vivas: ", errFakeBoom},
		{"close fails", fakeReply{rows: [][]driver.Value{good}, closeErr: closeBoom}, "intake: cerrar filas de ventanas vivas: ", closeBoom},
		{"close fails after a scan failure: the first error wins",
			fakeReply{rows: [][]driver.Value{nullAnchor}, closeErr: closeBoom}, "intake: scan de ventana viva: ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.reply)
			got, err := store.ListAggregating(context.Background(), 10)
			requireWrapped(t, err, tc.cause, tc.prefix)
			if got != nil {
				t.Errorf("ListAggregating con error devolvió %+v, quería nil", got)
			}
			if tc.cause == nil && errors.Is(err, closeBoom) {
				t.Errorf("err = %q arrastra el fallo del cierre; quería solo el primero", err)
			}
		})
	}
}
