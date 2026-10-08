package intakehelpertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Las piezas de la clave de ventana que comparten los casos.
const (
	sessionA = "session-a"
	sessionB = "session-b"
	contactA = "contact-a"
	contactB = "contact-b"
)

// firstMessageTS es el instante del primer mensaje que siembran los casos: un pasado FIJO, a
// propósito lejos de cualquier reloj de implementación, para que `message_ts` no pueda
// confundirse con `created_at` ni con `updated_at` (el fallo silencioso de medir plazos contra
// el reloj del cliente).
var firstMessageTS = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

// newKey devuelve una clave de ventana del tenant con un evento NUEVO (un UUID).
func newKey(tenant string) intake.WindowKey {
	return intake.WindowKey{TenantID: tenant, SessionID: sessionA, ContactID: contactA, EventID: uuid.NewString()}
}

// envelope devuelve un sobre completo y distinguible por su etiqueta.
func envelope(tag string) intake.SourceText {
	return intake.SourceText{Enc: []byte("enc-" + tag), DEK: []byte("dek-" + tag), KEKID: "kek-" + tag}
}

// artifact devuelve un artefacto válido de esa etapa, distinguible por su etiqueta.
func artifact(stage, tag string) intake.Artifact {
	return intake.Artifact{Stage: stage, Payload: json.RawMessage(fmt.Sprintf(`{"version":1,"tag":%q}`, tag))}
}

// diffRow lista las columnas en que got y want difieren, SIN mirar `updated_at` (cada llamante
// decide si debía moverse). Es la marca de estado: la fila entera.
func diffRow(got, want Row) []string {
	var diffs []string
	add := func(column string, g, w any) {
		diffs = append(diffs, fmt.Sprintf("%s = %v, quería %v", column, g, w))
	}
	if got.ID != want.ID {
		add("id", got.ID, want.ID)
	}
	if got.Key != want.Key {
		add("la clave de ventana", got.Key, want.Key)
	}
	if got.Status != want.Status {
		add("status", got.Status, want.Status)
	}
	if got.Stage != want.Stage {
		add("stage", got.Stage, want.Stage)
	}
	if !got.MessageTS.Equal(want.MessageTS) {
		add("message_ts", got.MessageTS, want.MessageTS)
	}
	// Un array vacío y uno nil son la misma columna `[]`.
	if !(len(got.SourceRefs) == 0 && len(want.SourceRefs) == 0) && !slices.Equal(got.SourceRefs, want.SourceRefs) {
		add("source_refs", got.SourceRefs, want.SourceRefs)
	}
	if !bytes.Equal(got.SourceText.Enc, want.SourceText.Enc) {
		add("source_text_enc", got.SourceText.Enc, want.SourceText.Enc)
	}
	if !bytes.Equal(got.SourceText.DEK, want.SourceText.DEK) {
		add("source_text_dek", got.SourceText.DEK, want.SourceText.DEK)
	}
	if got.SourceText.KEKID != want.SourceText.KEKID {
		add("source_text_kek_id", got.SourceText.KEKID, want.SourceText.KEKID)
	}
	if !sameArtifacts(got.Artifacts, want.Artifacts) {
		add("artifacts", artifactsText(got.Artifacts), artifactsText(want.Artifacts))
	}
	if got.Error != want.Error {
		add("error", got.Error, want.Error)
	}
	if got.IntakeID != want.IntakeID {
		add("intake_id", got.IntakeID, want.IntakeID)
	}
	if got.Attempts != want.Attempts {
		add("attempts", got.Attempts, want.Attempts)
	}
	if !got.NextAttemptAt.Equal(want.NextAttemptAt) {
		add("next_attempt_at", got.NextAttemptAt, want.NextAttemptAt)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		add("created_at", got.CreatedAt, want.CreatedAt)
	}
	if got.Reanalysis != want.Reanalysis {
		add("las columnas del re-análisis", got.Reanalysis, want.Reanalysis)
	}
	return diffs
}

// sameArtifacts compara `artifacts` por CONTENIDO: Postgres lo guarda en jsonb, que reordena
// las claves y normaliza los blancos. Un mapa vacío y uno nil son el mismo `{}`.
func sameArtifacts(got, want map[string]json.RawMessage) bool {
	if len(got) != len(want) {
		return false
	}
	for stage, w := range want {
		g, ok := got[stage]
		if !ok {
			return false
		}
		var gv, wv any
		if json.Unmarshal(g, &gv) != nil || json.Unmarshal(w, &wv) != nil || !reflect.DeepEqual(gv, wv) {
			return false
		}
	}
	return true
}

// artifactsText deja `artifacts` legible en un mensaje de fallo.
func artifactsText(a map[string]json.RawMessage) string {
	out := make(map[string]string, len(a))
	for stage, payload := range a {
		out[stage] = string(payload)
	}
	return fmt.Sprint(out)
}

// requireSameRow afirma que la fila NO SE TOCÓ: todas las columnas iguales, `updated_at`
// incluido. Es lo que separa «idempotente» de «no toca la fila».
func requireSameRow(t *testing.T, what string, got, want Row) {
	t.Helper()
	for _, d := range diffRow(got, want) {
		t.Errorf("%s: %s", what, d)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("%s: updated_at pasó de %v a %v y no debía moverse", what, want.UpdatedAt, got.UpdatedAt)
	}
}

// requireWrittenRow afirma que la fila es want —lo que el caso dice que tenía que quedar— y que
// su `updated_at` es ESTRICTAMENTE posterior al de want (el de antes de escribir).
func requireWrittenRow(t *testing.T, what string, got, want Row) {
	t.Helper()
	for _, d := range diffRow(got, want) {
		t.Errorf("%s: %s", what, d)
	}
	if !got.UpdatedAt.After(want.UpdatedAt) {
		t.Errorf("%s: updated_at = %v, quería uno posterior a %v", what, got.UpdatedAt, want.UpdatedAt)
	}
}

// --- la cola ---

// open abre o amplía la ventana de la clave, o falla el test.
func open(t *testing.T, m QueueMontaje, k intake.WindowKey, ts time.Time, refs ...string) {
	t.Helper()
	if err := m.Store.OpenOrAppend(context.Background(), intake.Append{Key: k, MessageTS: ts, Refs: refs}); err != nil {
		t.Fatalf("OpenOrAppend(%+v, %v): error inesperado %v", k, refs, err)
	}
}

// closeLive cierra la ventana viva de la clave y exige que ESTA llamada la cerrara.
func closeLive(t *testing.T, m QueueMontaje, k intake.WindowKey) {
	t.Helper()
	if ok, err := m.Store.CloseWindow(context.Background(), k); err != nil || !ok {
		t.Fatalf("CloseWindow(%+v) = (%v, %v), quería (true, nil)", k, ok, err)
	}
}

// rowsOf devuelve las filas de esa clave exacta.
func rowsOf(t *testing.T, m QueueMontaje, k intake.WindowKey) []Row {
	t.Helper()
	var out []Row
	for _, r := range m.Rows(t, k.TenantID) {
		if r.Key == k {
			out = append(out, r)
		}
	}
	return out
}

// onlyRow devuelve LA fila de la clave y exige que sea una sola.
func onlyRow(t *testing.T, m QueueMontaje, k intake.WindowKey) Row {
	t.Helper()
	rows := rowsOf(t, m, k)
	if len(rows) != 1 {
		t.Fatalf("la clave %+v tiene %d filas, quería exactamente 1: %+v", k, len(rows), rows)
	}
	return rows[0]
}

// rowWithRef devuelve la fila de la clave cuya primera referencia es ref: es como los casos
// distinguen dos ventanas de la misma tupla sin depender del orden de Rows.
func rowWithRef(t *testing.T, m QueueMontaje, k intake.WindowKey, ref string) Row {
	t.Helper()
	for _, r := range rowsOf(t, m, k) {
		if len(r.SourceRefs) > 0 && r.SourceRefs[0] == ref {
			return r
		}
	}
	t.Fatalf("la clave %+v no tiene ninguna fila que empiece por la referencia %q", k, ref)
	return Row{}
}

// queueWitness es la ventana que un caso de la cola NO toca: su fila tal como quedó al abrirla.
type queueWitness struct {
	key intake.WindowKey
	row Row
}

// seedQueueWitness abre en TenantB una ventana viva con la MISMA sesión, contacto y evento que
// k —solo cambia el tenant— y deja pasar el reloj: si una escritura sobre k rozara a la del
// otro tenant, su fila saldría distinta.
func seedQueueWitness(t *testing.T, m QueueMontaje, k intake.WindowKey) queueWitness {
	t.Helper()
	other := k
	other.TenantID = m.TenantB
	open(t, m, other, firstMessageTS, "wamid.witness")
	w := queueWitness{key: other, row: onlyRow(t, m, other)}
	m.Advance(t)
	return w
}

// requireUntouched afirma que la ventana testigo sigue exactamente como se abrió.
func (w queueWitness) requireUntouched(t *testing.T, m QueueMontaje) {
	t.Helper()
	requireSameRow(t, "la ventana del otro tenant (testigo)", onlyRow(t, m, w.key), w.row)
}

// --- la máquina y el segundo productor ---

// base es el instante de referencia de un caso: el reloj de la implementación, en segundos
// enteros (Postgres guarda microsegundos).
func base(t *testing.T, tb Table) time.Time {
	t.Helper()
	return tb.Now(t).Truncate(time.Second)
}

// seedJob siembra un job del tenant en ese estado, con un evento nuevo, creado y tocado hace
// diez minutos y con la marca del backoff VENCIDA hace diez minutos. Los mutadores cambian lo
// que el caso necesite antes de sembrar. Devuelve su id.
//
// Las marcas van en el pasado a propósito: así el `updated_at` de cualquier escritura posterior
// es estrictamente mayor sin tener que adelantar el reloj.
func seedJob(t *testing.T, tb Table, tenant, status string, mutate ...func(*Row)) string {
	t.Helper()
	past := base(t, tb).Add(-10 * time.Minute)
	r := Row{Key: newKey(tenant), Status: status, CreatedAt: past, UpdatedAt: past, NextAttemptAt: past}
	for _, mut := range mutate {
		mut(&r)
	}
	id := tb.Seed(t, r)
	if id == "" {
		t.Fatal("Montaje.Seed devolvió un id vacío")
	}
	return id
}

// loaded es el mutador del job «con todo»: etapa y artefacto ya persistidos, sobre, referencias,
// instante del primer mensaje, intentos consumidos, borrador y contexto de re-análisis. Es el
// estado sobre el que una transición que toca de más se nota.
func loaded(r *Row) {
	r.Stage = intake.StageP2
	r.Artifacts = map[string]json.RawMessage{intake.StageP2: artifact(intake.StageP2, "seeded").Payload}
	r.MessageTS = firstMessageTS
	r.SourceRefs = []string{"wamid.one", "media.one"}
	r.SourceText = envelope("seeded")
	r.Attempts = 2
	r.IntakeID = uuid.NewString()
	r.Reanalysis = intake.Reanalysis{RequestedBy: intake.RequestedByOwner, Via: "api", Source: "pasted_text", From: 3}
}

// at devuelve el mutador que fija la marca del backoff y el instante de creación.
func at(nextAttempt, created time.Time) func(*Row) {
	return func(r *Row) { r.NextAttemptAt, r.CreatedAt, r.UpdatedAt = nextAttempt, created, created }
}

// witnesses son los jobs que un caso NO toca: sus filas tal como se sembraron.
type witnesses struct {
	rows []Row
}

// seedWitnesses siembra dos jobs «con todo» en `processing` —un hermano en TenantA y uno en
// TenantB— y guarda su marca de estado. En `processing` ningún reclamo se los lleva, y son
// exactamente el estado que una transición sin su `WHERE id =` estropearía.
func seedWitnesses(t *testing.T, tb Table) witnesses {
	t.Helper()
	var w witnesses
	for _, tenant := range []string{tb.TenantA, tb.TenantB} {
		w.rows = append(w.rows, tb.Row(t, seedJob(t, tb, tenant, intake.StatusProcessing, loaded)))
	}
	return w
}

// requireUntouched afirma que los testigos siguen exactamente como se sembraron.
func (w witnesses) requireUntouched(t *testing.T, tb Table) {
	t.Helper()
	for _, want := range w.rows {
		requireSameRow(t, "el job testigo del tenant "+want.Key.TenantID, tb.Row(t, want.ID), want)
	}
}

// transition es una de las cinco transiciones, con argumentos VÁLIDOS: lo único que puede
// impedir que aplique es el estado del job.
type transition struct {
	name string
	run  func(s intake.PipelineStore, jobID string) (bool, error)
}

// transitions devuelve las cinco. SaveStage va a la última etapa para que la guarda de no
// retroceder no sea la que la frene.
func transitions(next time.Time) []transition {
	ctx := context.Background()
	return []transition{
		{"SaveStage", func(s intake.PipelineStore, id string) (bool, error) {
			return s.SaveStage(ctx, id, artifact(intake.StageDraft, "late"))
		}},
		{"Release", func(s intake.PipelineStore, id string) (bool, error) { return s.Release(ctx, id) }},
		{"Retry", func(s intake.PipelineStore, id string) (bool, error) { return s.Retry(ctx, id, next) }},
		{"Finish", func(s intake.PipelineStore, id string) (bool, error) {
			return s.Finish(ctx, id, uuid.NewString())
		}},
		{"Fail", func(s intake.PipelineStore, id string) (bool, error) {
			return s.Fail(ctx, id, "causa=infra stage=p2: sin respuesta")
		}},
	}
}

// transitionNamed devuelve la transición de ese nombre.
func transitionNamed(t *testing.T, name string, next time.Time) transition {
	t.Helper()
	for _, tr := range transitions(next) {
		if tr.name == name {
			return tr
		}
	}
	t.Fatalf("no hay una transición llamada %q", name)
	return transition{}
}

// requireNoEffectOutsideProcessing afirma la convención del puerto para UNA transición: sobre
// un job que no está en `processing` —en cualquiera de los otros cuatro estados— y sobre un id
// que no existe devuelve (false, nil) y no toca nada.
func requireNoEffectOutsideProcessing(t *testing.T, m MachineMontaje, name string) {
	t.Helper()
	tr := transitionNamed(t, name, base(t, m.Table).Add(time.Hour))
	w := seedWitnesses(t, m.Table)
	for _, status := range []string{intake.StatusAggregating, intake.StatusPending, intake.StatusDone, intake.StatusFailed} {
		id := seedJob(t, m.Table, m.TenantA, status, loaded)
		before := m.Row(t, id)
		m.Advance(t)
		if ok, err := tr.run(m.Store, id); err != nil || ok {
			t.Errorf("%s sobre un job %s = (%v, %v), quería (false, nil)", name, status, ok, err)
		}
		requireSameRow(t, name+" sobre un job "+status, m.Row(t, id), before)
	}
	if ok, err := tr.run(m.Store, uuid.NewString()); err != nil || ok {
		t.Errorf("%s sobre un id que no existe = (%v, %v), quería (false, nil)", name, ok, err)
	}
	w.requireUntouched(t, m.Table)
}

// claimOf reclama con ClaimNext y exige que se lleve ESE job.
func claimOf(t *testing.T, m MachineMontaje, wantID string) intake.ClaimedJob {
	t.Helper()
	job, ok, err := m.Store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext = (_, %v, %v), quería llevarse el job %s", ok, err, wantID)
	}
	if job.ID != wantID {
		t.Fatalf("ClaimNext se llevó el job %s, quería el %s", job.ID, wantID)
	}
	return job
}

// requireNothingToClaim afirma que ClaimNext no encuentra nada, sin error y sin job a medias.
func requireNothingToClaim(t *testing.T, m MachineMontaje, why string) {
	t.Helper()
	job, ok, err := m.Store.ClaimNext(context.Background())
	if err != nil || ok {
		t.Errorf("%s: ClaimNext = (%+v, %v, %v), quería (ClaimedJob{}, false, nil)", why, job, ok, err)
	}
	if job.ID != "" {
		t.Errorf("%s: ClaimNext devolvió el job %q junto a ok=false", why, job.ID)
	}
}
