package intake

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Las siete sentencias de la máquina, escritas APARTE y byte a byte: son las del paquete viejo,
// y un cambio en el SQL de producción tiene que romper aquí. 🔴 Sin Postgres, este texto es lo
// ÚNICO que custodia las guardas que viven en SQL —el `status = 'pending'` y el
// `next_attempt_at <= now()` del reclamo, el `tenant_id = $1` del reclamo por evento, el
// `status = 'processing'` de las cinco transiciones, el `array_position` del avance y el
// vaciado de las tres columnas del sobre—: su conducta la prueba
// intakehelpertest.ContratoMachine contra Postgres real, en los procesos de F9 (hallazgo 63).
const (
	wantClaimReturning = `
RETURNING j.id::text, j.tenant_id, j.session_id, j.contact_id, j.event_id::text,
          COALESCE(j.stage, ''), j.message_ts, j.source_refs, j.artifacts,
          j.source_text_enc, j.source_text_dek, COALESCE(j.source_text_kek_id, ''),
          j.attempts,
          COALESCE(j.requested_by, ''), COALESCE(j.reanalysis_via, ''),
          COALESCE(j.reanalysis_source, ''), COALESCE(j.reanalyzed_from, 0)
`
	wantClaimNextSQL = `
UPDATE public.intake_jobs AS j
   SET status = 'processing', updated_at = now()
  FROM (
        SELECT id
          FROM public.intake_jobs
         WHERE status = 'pending' AND next_attempt_at <= now()
         ORDER BY next_attempt_at, created_at
         LIMIT 1
         FOR UPDATE SKIP LOCKED
       ) AS c
 WHERE j.id = c.id` + wantClaimReturning
	wantClaimIgnoringBackoffSQL = `
UPDATE public.intake_jobs AS j
   SET status = 'processing', updated_at = now()
  FROM (
        SELECT id
          FROM public.intake_jobs
         WHERE status = 'pending' AND tenant_id = $1
         ORDER BY next_attempt_at, created_at
         LIMIT 1
         FOR UPDATE SKIP LOCKED
       ) AS c
 WHERE j.id = c.id` + wantClaimReturning
	wantSaveStageSQL = `
UPDATE public.intake_jobs AS j
   SET stage      = $2,
       artifacts  = j.artifacts || jsonb_build_object($2::text, $3::jsonb),
       updated_at = now()
 WHERE j.id = $1::uuid
   AND j.status = 'processing'
   AND (j.stage IS NULL
        OR array_position(ARRAY['p2','p3','p4','match','draft']::text[], j.stage)
           <= array_position(ARRAY['p2','p3','p4','match','draft']::text[], $2::text))
`
	wantReleaseSQL = `
UPDATE public.intake_jobs
   SET status = 'pending', updated_at = now()
 WHERE id = $1::uuid AND status = 'processing'
`
	wantRetrySQL = `
UPDATE public.intake_jobs
   SET status          = 'pending',
       attempts        = attempts + 1,
       next_attempt_at = $2,
       updated_at      = now()
 WHERE id = $1::uuid AND status = 'processing'
`
	wantFinishSQL = `
UPDATE public.intake_jobs AS j
   SET status             = 'done',
       intake_id          = COALESCE($2::uuid, j.intake_id),
       source_text_enc    = NULL,
       source_text_dek    = NULL,
       source_text_kek_id = NULL,
       updated_at         = now()
 WHERE j.id = $1::uuid AND j.status = 'processing'
`
	wantFailSQL = `
UPDATE public.intake_jobs AS j
   SET status             = 'failed',
       error              = $2,
       source_text_enc    = NULL,
       source_text_dek    = NULL,
       source_text_kek_id = NULL,
       updated_at         = now()
 WHERE j.id = $1::uuid AND j.status = 'processing'
`
)

const (
	pgJobID    = "22222222-2222-4222-8222-222222222222"
	pgIntakeID = "33333333-3333-4333-8333-333333333333"
)

// pgNext es la marca de reintento de los tests, en una zona que NO es UTC.
var pgNext = time.Date(2026, 10, 8, 9, 30, 0, 0, time.FixedZone("lima", -5*3600))

// validArtifact es un artefacto que pasa la puerta.
func validArtifact() Artifact {
	return Artifact{Stage: StageP3, Payload: json.RawMessage(`{"version":1,"items":[]}`)}
}

// pgTransition es una de las cinco transiciones contra el adaptador, con argumentos válidos.
type pgTransition struct {
	name string
	run  func(p *Postgres, jobID string) (bool, error)
	// sql y args son la sentencia y los argumentos que tiene que mandar para pgJobID.
	sql  string
	args []driver.Value
	// what es el texto de operador con el que envuelve un fallo de la base.
	what string
	// noID es su rechazo de un id de job vacío.
	noID string
}

func pgTransitions() []pgTransition {
	ctx := context.Background()
	a := validArtifact()
	return []pgTransition{
		{"SaveStage", func(p *Postgres, id string) (bool, error) { return p.SaveStage(ctx, id, a) },
			wantSaveStageSQL, []driver.Value{pgJobID, StageP3, string(a.Payload)},
			"guardar la etapa p3 del job " + pgJobID, "intake: guardar etapa sin id de job"},
		{"Release", func(p *Postgres, id string) (bool, error) { return p.Release(ctx, id) },
			wantReleaseSQL, []driver.Value{pgJobID},
			"devolver a la cola el job " + pgJobID, "intake: devolver a la cola sin id de job"},
		{"Retry", func(p *Postgres, id string) (bool, error) { return p.Retry(ctx, id, pgNext) },
			wantRetrySQL, []driver.Value{pgJobID, pgNext.UTC()},
			"reencolar con backoff el job " + pgJobID, "intake: reencolar con backoff sin id de job"},
		{"Finish", func(p *Postgres, id string) (bool, error) { return p.Finish(ctx, id, pgIntakeID) },
			wantFinishSQL, []driver.Value{pgJobID, pgIntakeID},
			"terminar el job " + pgJobID, "intake: terminar sin id de job"},
		{"Fail", func(p *Postgres, id string) (bool, error) { return p.Fail(ctx, id, "causa=infra") },
			wantFailSQL, []driver.Value{pgJobID, "causa=infra"},
			"fallar el job " + pgJobID, "intake: fallar sin id de job"},
	}
}

// TestPostgres_Machine_NilReceiverOrNilDB_IsANoOp: un *Postgres nil, o uno sin base, no tiene
// cola: los dos reclamos y las cinco transiciones son «no aplicó», sin error y sin panic.
func TestPostgres_Machine_NilReceiverOrNilDB_IsANoOp(t *testing.T) {
	ctx := context.Background()
	for name, p := range map[string]*Postgres{"nil receiver": nil, "nil db": NewPostgres(nil)} {
		t.Run(name, func(t *testing.T) {
			if job, ok, err := p.ClaimNext(ctx); ok || err != nil || job.ID != "" {
				t.Errorf("ClaimNext = (%+v, %v, %v), quería (ClaimedJob{}, false, nil)", job, ok, err)
			}
			if job, ok, err := p.ClaimNextIgnoringBackoff(ctx, "tenant-1"); ok || err != nil || job.ID != "" {
				t.Errorf("ClaimNextIgnoringBackoff = (%+v, %v, %v), quería (ClaimedJob{}, false, nil)", job, ok, err)
			}
			for _, tr := range pgTransitions() {
				if ok, err := tr.run(p, pgJobID); ok || err != nil {
					t.Errorf("%s = (%v, %v), quería (false, nil)", tr.name, ok, err)
				}
			}
		})
	}
}

// claimRow es la fila que devuelve el RETURNING de un reclamo, con sus 17 columnas en orden.
func claimRow() []driver.Value {
	return []driver.Value{
		pgJobID, "tenant-1", "session-1", "contact-1", pgKey.EventID,
		"p2", time.Date(2026, 8, 22, 5, 0, 0, 0, time.FixedZone("lima", -5*3600)),
		[]byte(`["wamid.one","media.one"]`), []byte(`{"p2":{"version":1}}`),
		[]byte("enc"), []byte("dek"), "k1",
		int64(2),
		"owner", "api", "pasted_text", int64(3),
	}
}

// TestPostgres_Claims_EmitTheirStatementAndMapTheWholeJob: cada reclamo es UNA sentencia, la
// suya, byte a byte —el normal sin argumentos; el reclamo por evento con el tenant—, y los DOS
// leen el mismo RETURNING: las 17 columnas, cada una en su campo, con el instante en UTC.
func TestPostgres_Claims_EmitTheirStatementAndMapTheWholeJob(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		claim func(p *Postgres) (ClaimedJob, bool, error)
		sql   string
		args  []driver.Value
	}{
		{"ClaimNext", func(p *Postgres) (ClaimedJob, bool, error) { return p.ClaimNext(ctx) }, wantClaimNextSQL, []driver.Value{}},
		{"ClaimNextIgnoringBackoff", func(p *Postgres) (ClaimedJob, bool, error) { return p.ClaimNextIgnoringBackoff(ctx, "tenant-1") },
			wantClaimIgnoringBackoffSQL, []driver.Value{"tenant-1"}},
	}
	want := ClaimedJob{
		ID: pgJobID, Key: WindowKey{"tenant-1", "session-1", "contact-1", pgKey.EventID},
		Stage: "p2", MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		SourceRefs: []string{"wamid.one", "media.one"},
		SourceText: SourceText{Enc: []byte("enc"), DEK: []byte("dek"), KEKID: "k1"},
		Artifacts:  map[string]json.RawMessage{"p2": json.RawMessage(`{"version":1}`)},
		Attempts:   2,
		Reanalysis: Reanalysis{RequestedBy: "owner", Via: "api", Source: "pasted_text", From: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(fakeReply{rows: [][]driver.Value{claimRow()}})
			got, ok, err := tc.claim(store)
			if err != nil || !ok {
				t.Fatalf("%s = (_, %v, %v), quería un job", tc.name, ok, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("job reclamado = %+v\nquería %+v", got, want)
			}
			if got.MessageTS.Location() != time.UTC {
				t.Errorf("MessageTS sale en %v, quería UTC", got.MessageTS.Location())
			}
			stmt := fake.requireOnly(t, fakeQuery)
			requireSQL(t, stmt, tc.sql)
			if !reflect.DeepEqual(stmt.args, tc.args) {
				t.Errorf("argumentos = %#v, quería %#v", stmt.args, tc.args)
			}
		})
	}
}

// TestPostgres_Claim_EmptyQueueAndNulls: sin fila es la cola vacía —(ClaimedJob{}, false, nil),
// no un error—; y un `message_ts` NULL y un sobre NULL llegan como valor cero, sin inventar
// una fecha.
func TestPostgres_Claim_EmptyQueueAndNulls(t *testing.T) {
	store, _ := newFakePostgres(t)
	if job, ok, err := store.ClaimNext(context.Background()); ok || err != nil || !reflect.DeepEqual(job, ClaimedJob{}) {
		t.Errorf("ClaimNext sin filas = (%+v, %v, %v), quería (ClaimedJob{}, false, nil)", job, ok, err)
	}

	store, fake := newFakePostgres(t)
	row := claimRow()
	row[5], row[6], row[7], row[8] = "", nil, []byte(`[]`), []byte(`{}`)
	row[9], row[10], row[11] = nil, nil, ""
	row[13], row[14], row[15], row[16] = "", "", "", int64(0)
	fake.script(fakeReply{rows: [][]driver.Value{row}})
	job, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext = (_, %v, %v), quería un job", ok, err)
	}
	if job.Stage != "" || !job.MessageTS.IsZero() || job.SourceText.Complete() || job.SourceText.Enc != nil ||
		len(job.SourceRefs) != 0 || len(job.Artifacts) != 0 || job.Reanalysis != (Reanalysis{}) {
		t.Errorf("job con columnas NULL = %+v, quería sus valores cero", job)
	}
}

// TestPostgres_ClaimNextIgnoringBackoff_EmptyTenant_DoesNotQuery: un tenant vacío no es un
// filtro que case con todo: devuelve «no hay nada» SIN ir a la base.
func TestPostgres_ClaimNextIgnoringBackoff_EmptyTenant_DoesNotQuery(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(fakeReply{rows: [][]driver.Value{claimRow()}})
	if job, ok, err := store.ClaimNextIgnoringBackoff(context.Background(), ""); ok || err != nil || job.ID != "" {
		t.Errorf("ClaimNextIgnoringBackoff(\"\") = (%+v, %v, %v), quería (ClaimedJob{}, false, nil)", job, ok, err)
	}
	fake.requireUntouched(t)
}

// TestPostgres_Claim_Failures_AreWrapped: el fallo de la base y las dos columnas JSON que no se
// pueden decodificar salen envueltos, cada uno con su prefijo, y sin job a medias.
func TestPostgres_Claim_Failures_AreWrapped(t *testing.T) {
	badRefs, badArtifacts := claimRow(), claimRow()
	badRefs[7] = []byte(`{"no":"es un array"}`)
	badArtifacts[8] = []byte(`["no es un objeto"]`)
	cases := []struct {
		name   string
		reply  fakeReply
		prefix string
		cause  error
	}{
		{"query fails", fakeReply{err: errFakeBoom}, "intake: reclamar job del pipeline: ", errFakeBoom},
		{"source_refs is not an array", fakeReply{rows: [][]driver.Value{badRefs}},
			"intake: decodificar source_refs del job " + pgJobID + ": ", nil},
		{"artifacts is not an object", fakeReply{rows: [][]driver.Value{badArtifacts}},
			"intake: decodificar artifacts del job " + pgJobID + ": ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, byEvent := range []bool{false, true} {
				store, fake := newFakePostgres(t)
				fake.script(tc.reply)
				var (
					job ClaimedJob
					ok  bool
					err error
				)
				if byEvent {
					job, ok, err = store.ClaimNextIgnoringBackoff(context.Background(), "tenant-1")
				} else {
					job, ok, err = store.ClaimNext(context.Background())
				}
				requireWrapped(t, err, tc.cause, tc.prefix)
				if ok || !reflect.DeepEqual(job, ClaimedJob{}) {
					t.Errorf("reclamo con error (por evento: %v) devolvió (%+v, %v), quería (ClaimedJob{}, false)", byEvent, job, ok)
				}
			}
		})
	}
}

// TestPostgres_Transitions_EmitOneStatementAndMapRowsAffected: cada transición es UNA sentencia,
// la suya, byte a byte, con sus argumentos; devuelve true si afectó una fila y (false, nil) si
// ninguna —otro llegó antes, o el job ya estaba terminado—. La marca de Retry viaja en UTC.
func TestPostgres_Transitions_EmitOneStatementAndMapRowsAffected(t *testing.T) {
	for _, tr := range pgTransitions() {
		t.Run(tr.name, func(t *testing.T) {
			for affected, want := range map[int64]bool{0: false, 1: true} {
				store, fake := newFakePostgres(t)
				fake.script(fakeReply{affected: affected})
				ok, err := tr.run(store, pgJobID)
				if err != nil || ok != want {
					t.Errorf("%s con %d filas afectadas = (%v, %v), quería (%v, nil)", tr.name, affected, ok, err, want)
				}
				stmt := fake.requireOnly(t, fakeExec)
				requireSQL(t, stmt, tr.sql)
				if !reflect.DeepEqual(stmt.args, tr.args) {
					t.Errorf("argumentos = %#v, quería %#v", stmt.args, tr.args)
				}
			}
		})
	}
}

// TestPostgres_Transitions_Failures_AreWrapped: el fallo de la sentencia sale como
// "intake: <qué>: " y el de contar las filas como "intake: contar filas al <qué>: ", con el
// texto de operador de cada transición.
func TestPostgres_Transitions_Failures_AreWrapped(t *testing.T) {
	for _, tr := range pgTransitions() {
		t.Run(tr.name, func(t *testing.T) {
			cases := map[string]fakeReply{
				"intake: " + tr.what + ": ":                 {err: errFakeBoom},
				"intake: contar filas al " + tr.what + ": ": {affected: 1, affectedErr: errFakeBoom},
			}
			for prefix, reply := range cases {
				store, fake := newFakePostgres(t)
				fake.script(reply)
				ok, err := tr.run(store, pgJobID)
				requireWrapped(t, err, errFakeBoom, prefix)
				if ok {
					t.Errorf("%s con error devolvió true (%q)", tr.name, prefix)
				}
			}
		})
	}
}

// TestPostgres_Transitions_EmptyJobID_RejectedBeforeTheDatabase: sin id de job cada transición
// se rechaza con su texto, sin mandar nada a la base.
func TestPostgres_Transitions_EmptyJobID_RejectedBeforeTheDatabase(t *testing.T) {
	for _, tr := range pgTransitions() {
		store, fake := newFakePostgres(t)
		ok, err := tr.run(store, "")
		if ok || err == nil || err.Error() != tr.noID {
			t.Errorf("%s sin id = (%v, %v), quería (false, %q)", tr.name, ok, err, tr.noID)
		}
		fake.requireUntouched(t)
	}
}

// TestPostgres_SaveStage_InvalidArtifact_NeverReachesTheDatabase: LA VALIDACIÓN VA ANTES de tocar
// la base. El objeto JSON válido sin `version` es el que Postgres aceptaría: aquí no llega.
func TestPostgres_SaveStage_InvalidArtifact_NeverReachesTheDatabase(t *testing.T) {
	invalid := []Artifact{
		{Stage: StageP2, Payload: json.RawMessage(`{"ideas":[]}`)},
		{Stage: StageP2, Payload: json.RawMessage(`[1]`)},
		{Stage: "p5", Payload: json.RawMessage(`{"version":1}`)},
		{Stage: StageP2},
	}
	for _, a := range invalid {
		store, fake := newFakePostgres(t)
		fake.script(fakeReply{affected: 1})
		ok, err := store.SaveStage(context.Background(), pgJobID, a)
		if ok || err == nil {
			t.Errorf("SaveStage(%q, %s) = (%v, %v), quería (false, error)", a.Stage, a.Payload, ok, err)
		}
		if want := a.Validate(); want == nil || err == nil || err.Error() != want.Error() {
			t.Errorf("SaveStage devolvió %v, quería el error de Validate (%v)", err, want)
		}
		fake.requireUntouched(t)
	}
}

// TestPostgres_Retry_ZeroInstant_RejectedBeforeTheDatabase: una marca cero es el año 1; el
// backoff quedaría en el pasado. Se rechaza con su texto sin tocar la base.
func TestPostgres_Retry_ZeroInstant_RejectedBeforeTheDatabase(t *testing.T) {
	store, fake := newFakePostgres(t)
	ok, err := store.Retry(context.Background(), pgJobID, time.Time{})
	want := "intake: reencolar el job " + pgJobID + " sin marca de reintento: el backoff quedaría en el pasado"
	if ok || err == nil || err.Error() != want {
		t.Errorf("Retry sin marca = (%v, %v), quería (false, %q)", ok, err, want)
	}
	fake.requireUntouched(t)
}

// TestPostgres_Finish_EmptyIntakeID_TravelsAsNull: un borrador vacío viaja como NULL —el
// COALESCE deja la columna como estaba—, nunca como "" (que rompería el cast a uuid).
func TestPostgres_Finish_EmptyIntakeID_TravelsAsNull(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(fakeReply{affected: 1})
	if ok, err := store.Finish(context.Background(), pgJobID, ""); err != nil || !ok {
		t.Fatalf("Finish = (%v, %v), quería (true, nil)", ok, err)
	}
	stmt := fake.requireOnly(t, fakeExec)
	if !reflect.DeepEqual(stmt.args, []driver.Value{pgJobID, nil}) {
		t.Errorf("argumentos = %#v, quería [id, NULL]", stmt.args)
	}
}

// TestPostgres_Fail_EmptyReason_RejectedBeforeTheDatabase: un job muerto sin causa no es
// diagnosticable, y la columna es NULLable: se rechaza en Go, con su texto.
func TestPostgres_Fail_EmptyReason_RejectedBeforeTheDatabase(t *testing.T) {
	store, fake := newFakePostgres(t)
	ok, err := store.Fail(context.Background(), pgJobID, "")
	want := "intake: fallar el job " + pgJobID + " sin causa: `error` es para el operador y no puede ir vacío"
	if ok || err == nil || err.Error() != want {
		t.Errorf("Fail sin causa = (%v, %v), quería (false, %q)", ok, err, want)
	}
	fake.requireUntouched(t)
}

// TestSaveStageSQL_CarriesTheStageOrderTwice: el orden de la máquina está DUPLICADO —en
// stageOrder y en el array de la sentencia, porque `array_position` lo necesita dentro— y este
// test ata las dos copias. Si alguien añade una etapa en un lado y no en el otro,
// `array_position` devolvería NULL para ella y SaveStage diría «no aplicó» sin ninguna pista.
// Tienen que ser DOS apariciones: la comparación es entre dos posiciones del mismo array.
func TestSaveStageSQL_CarriesTheStageOrderTwice(t *testing.T) {
	quoted := make([]string, 0, len(stageOrder))
	for _, s := range stageOrder {
		quoted = append(quoted, "'"+s+"'")
	}
	fromGo := "ARRAY[" + strings.Join(quoted, ",") + "]"
	if fromGo != sqlStageArray {
		t.Fatalf("stageOrder da %s y sqlStageArray es %s: las dos copias del orden divergen", fromGo, sqlStageArray)
	}
	store, fake := newFakePostgres(t)
	if _, err := store.SaveStage(context.Background(), pgJobID, validArtifact()); err != nil {
		t.Fatalf("SaveStage: error inesperado %v", err)
	}
	emitted := fake.requireOnly(t, fakeExec).query
	if n := strings.Count(emitted, fromGo); n != 2 {
		t.Errorf("la sentencia de SaveStage lleva el orden de la máquina %d veces, quería 2 (dos posiciones que comparar)", n)
	}
}
