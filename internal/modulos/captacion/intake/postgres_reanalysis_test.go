package intake

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"
)

// Las dos sentencias del segundo productor, escritas APARTE y byte a byte: son las del paquete
// viejo. 🔴 Sin Postgres, este texto es lo ÚNICO que custodia lo que vive en SQL —que el job
// nace `'pending'`, que hereda `message_ts` del primer job del evento, que nace sin sobre y
// que la pregunta es por `event_id` y no por `intake_id`—: su conducta la prueba
// intakehelpertest.ContratoReanalysis contra Postgres real, en los procesos de F9.
const (
	wantLiveJobSQL = `
SELECT id::text
  FROM public.intake_jobs
 WHERE tenant_id = $1 AND event_id = $2::uuid
   AND status = ANY($3)
 ORDER BY created_at DESC
 LIMIT 1
`
	wantOpenReanalysisSQL = `
INSERT INTO public.intake_jobs
       (tenant_id, session_id, contact_id, event_id, status, message_ts, source_refs,
        intake_id, requested_by, reanalysis_via, reanalysis_source, reanalyzed_from)
VALUES ($1, $2, $3, $4::uuid, 'pending',
        COALESCE((SELECT j0.message_ts
                    FROM public.intake_jobs j0
                   WHERE j0.tenant_id = $1 AND j0.event_id = $4::uuid
                     AND j0.message_ts IS NOT NULL
                   ORDER BY j0.created_at
                   LIMIT 1), now()),
        '[]'::jsonb,
        $5::uuid, $6, $7, $8, $9)
RETURNING id::text
`
)

// pgRequest es una petición de re-análisis completa.
func pgRequest() ReanalysisRequest {
	return ReanalysisRequest{
		Key: pgKey, IntakeID: pgIntakeID,
		Context: Reanalysis{RequestedBy: RequestedByOwner, Via: "api", Source: "both", From: 3},
	}
}

// TestPostgres_Reanalysis_NilReceiverOrNilDB_IsANoOp: sin base no hay a quién preguntar ni dónde
// abrir: «no hay job vivo» y un id vacío, sin error y sin panic.
func TestPostgres_Reanalysis_NilReceiverOrNilDB_IsANoOp(t *testing.T) {
	ctx := context.Background()
	for name, p := range map[string]*Postgres{"nil receiver": nil, "nil db": NewPostgres(nil)} {
		t.Run(name, func(t *testing.T) {
			if id, ok, err := p.LiveJobOfEvent(ctx, "tenant-1", pgKey.EventID); id != "" || ok || err != nil {
				t.Errorf("LiveJobOfEvent = (%q, %v, %v), quería (\"\", false, nil)", id, ok, err)
			}
			if id, err := p.OpenReanalysis(ctx, pgRequest()); id != "" || err != nil {
				t.Errorf("OpenReanalysis = (%q, %v), quería (\"\", nil)", id, err)
			}
		})
	}
}

// TestPostgres_LiveJobOfEvent_MissingArgument_IsAnError: sin tenant o sin evento no es «no hay
// ninguno», es una llamada mal hecha: error con su texto, sin ir a la base.
func TestPostgres_LiveJobOfEvent_MissingArgument_IsAnError(t *testing.T) {
	const want = "intake: hacen falta tenant y evento para preguntar por el job vivo"
	for name, args := range map[string][2]string{"no tenant": {"", pgKey.EventID}, "no event": {"tenant-1", ""}} {
		store, fake := newFakePostgres(t)
		id, ok, err := store.LiveJobOfEvent(context.Background(), args[0], args[1])
		if id != "" || ok || err == nil || err.Error() != want {
			t.Errorf("LiveJobOfEvent (%s) = (%q, %v, %v), quería (\"\", false, %q)", name, id, ok, err, want)
		}
		fake.requireUntouched(t)
	}
}

// TestPostgres_LiveJobOfEvent_AsksForTheThreeLiveStatuses: UNA consulta, la literal, con el
// tenant, el evento y EXACTAMENTE los tres estados no terminales; con fila devuelve su id y sin
// fila «no hay ninguno», sin error.
func TestPostgres_LiveJobOfEvent_AsksForTheThreeLiveStatuses(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(fakeReply{rows: [][]driver.Value{{pgJobID}}})
	id, ok, err := store.LiveJobOfEvent(context.Background(), "tenant-1", pgKey.EventID)
	if id != pgJobID || !ok || err != nil {
		t.Errorf("LiveJobOfEvent = (%q, %v, %v), quería (%q, true, nil)", id, ok, err, pgJobID)
	}
	stmt := fake.requireOnly(t, fakeQuery)
	requireSQL(t, stmt, wantLiveJobSQL)
	want := []driver.Value{"tenant-1", pgKey.EventID, []string{"aggregating", "pending", "processing"}}
	if !reflect.DeepEqual(stmt.args, want) {
		t.Errorf("argumentos = %#v, quería %#v", stmt.args, want)
	}

	store, _ = newFakePostgres(t)
	if id, ok, err := store.LiveJobOfEvent(context.Background(), "tenant-1", pgKey.EventID); id != "" || ok || err != nil {
		t.Errorf("LiveJobOfEvent sin fila = (%q, %v, %v), quería (\"\", false, nil)", id, ok, err)
	}
}

// TestPostgres_LiveJobOfEvent_DatabaseFailure_IsWrapped: el fallo de la base sale envuelto con el
// evento en su prefijo, y nunca como «no hay ninguno».
func TestPostgres_LiveJobOfEvent_DatabaseFailure_IsWrapped(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(fakeReply{err: errFakeBoom})
	id, ok, err := store.LiveJobOfEvent(context.Background(), "tenant-1", pgKey.EventID)
	requireWrapped(t, err, errFakeBoom, "intake: buscar el job vivo del evento "+pgKey.EventID+": ")
	if id != "" || ok {
		t.Errorf("LiveJobOfEvent con error devolvió (%q, %v), quería (\"\", false)", id, ok)
	}
}

// TestNonTerminalStatuses_AreExactlyTheComplementOfIsTerminal: la lista de «lo que todavía puede
// producir una revisión» y IsTerminal dicen lo mismo desde dos sitios. Si divergieran, la
// guarda del `reanalysis_in_progress` dejaría de ver jobs vivos.
func TestNonTerminalStatuses_AreExactlyTheComplementOfIsTerminal(t *testing.T) {
	live := map[string]bool{}
	for _, s := range nonTerminalStatuses {
		live[s] = true
	}
	all := []string{StatusAggregating, StatusPending, StatusProcessing, StatusDone, StatusFailed}
	for _, s := range all {
		if live[s] == IsTerminal(s) {
			t.Errorf("el estado %q: en la lista de vivos = %v, IsTerminal = %v; tienen que ser contrarios", s, live[s], IsTerminal(s))
		}
	}
	if len(nonTerminalStatuses) != 3 {
		t.Errorf("nonTerminalStatuses = %v, quería exactamente los tres estados vivos", nonTerminalStatuses)
	}
}

// TestPostgres_OpenReanalysis_IncompleteRequest_SaysWhatIsMissing: una petición incompleta se
// rechaza antes de la base, y el error dice QUÉ falta sin volcar la clave (lleva el contacto).
func TestPostgres_OpenReanalysis_IncompleteRequest_SaysWhatIsMissing(t *testing.T) {
	noKey, noIntake, notOwner := pgRequest(), pgRequest(), pgRequest()
	noKey.Key.ContactID = ""
	noIntake.IntakeID = ""
	notOwner.Context.RequestedBy = ""
	cases := map[string]ReanalysisRequest{
		"intake: solicitud de re-análisis incompleta (ventana=false intake=true dueño=true)":   noKey,
		"intake: solicitud de re-análisis incompleta (ventana=true intake=false dueño=true)":   noIntake,
		"intake: solicitud de re-análisis incompleta (ventana=true intake=true dueño=false)":   notOwner,
		"intake: solicitud de re-análisis incompleta (ventana=false intake=false dueño=false)": {},
	}
	for want, req := range cases {
		store, fake := newFakePostgres(t)
		id, err := store.OpenReanalysis(context.Background(), req)
		if id != "" || err == nil || err.Error() != want {
			t.Errorf("OpenReanalysis = (%q, %v), quería (\"\", %q)", id, err, want)
		}
		fake.requireUntouched(t)
	}
}

// TestPostgres_OpenReanalysis_InsertsOneRowAndReturnsItsID: UNA sentencia, la literal, con la
// clave, la solicitud y las cuatro columnas del contexto; devuelve el id que da la base. Una
// revisión de origen 0 —no había ninguna— viaja como NULL, no como la revisión cero.
func TestPostgres_OpenReanalysis_InsertsOneRowAndReturnsItsID(t *testing.T) {
	for from, wantFrom := range map[int]driver.Value{3: 3, 1: 1, 0: nil, -2: nil} {
		store, fake := newFakePostgres(t)
		fake.script(fakeReply{rows: [][]driver.Value{{pgJobID}}})
		req := pgRequest()
		req.Context.From = from
		id, err := store.OpenReanalysis(context.Background(), req)
		if id != pgJobID || err != nil {
			t.Errorf("OpenReanalysis = (%q, %v), quería (%q, nil)", id, err, pgJobID)
		}
		stmt := fake.requireOnly(t, fakeQuery)
		requireSQL(t, stmt, wantOpenReanalysisSQL)
		want := []driver.Value{
			pgKey.TenantID, pgKey.SessionID, pgKey.ContactID, pgKey.EventID,
			pgIntakeID, "owner", "api", "both", wantFrom,
		}
		if !reflect.DeepEqual(stmt.args, want) {
			t.Errorf("argumentos con From=%d = %#v, quería %#v", from, stmt.args, want)
		}
	}
}

// TestPostgres_OpenReanalysis_DatabaseFailure_IsWrapped: el fallo de la base —o una inserción
// que no devuelve su id— sale envuelto con el evento en su prefijo, y sin id.
func TestPostgres_OpenReanalysis_DatabaseFailure_IsWrapped(t *testing.T) {
	prefix := "intake: abrir el job de re-análisis del evento " + pgKey.EventID + ": "
	for name, reply := range map[string]fakeReply{"query fails": {err: errFakeBoom}, "no row returned": {}} {
		store, fake := newFakePostgres(t)
		fake.script(reply)
		id, err := store.OpenReanalysis(context.Background(), pgRequest())
		var cause error
		if reply.err != nil {
			cause = errFakeBoom
		}
		requireWrapped(t, err, cause, prefix)
		if id != "" {
			t.Errorf("OpenReanalysis con error (%s) devolvió el id %q", name, id)
		}
	}
}

// TestNullableInt_ZeroIsNull: «no había revisión anterior» y «la anterior era la número cero» no
// son lo mismo, y la segunda no existe: los correlativos empiezan en 1.
func TestNullableInt_ZeroIsNull(t *testing.T) {
	for n, want := range map[int]any{0: nil, -1: nil, 1: 1, 42: 42} {
		if got := nullableInt(n); got != want {
			t.Errorf("nullableInt(%d) = %#v, quería %#v", n, got, want)
		}
	}
}
