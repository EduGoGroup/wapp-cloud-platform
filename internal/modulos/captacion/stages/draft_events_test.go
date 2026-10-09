//go:build pendiente

package stages_test

// draft_events_test.go — el contrato de draft_events.go: las dos filas de `flow_events`
// (`intake_draft_created`, `intake_reanalyzed`), sus payloads literales de design §10 y
// el cronómetro de `elapsed_ms`.

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// El doble del outbox satisface el puerto de la etapa: comprobado en compilación.
var _ stages.EventWriter = (*fakeEventWriter)(nil)

// payloadKeys son las claves de un payload, ordenadas.
func payloadKeys(ev store.FlowEvent) []string {
	keys := make([]string, 0, len(ev.Payload))
	for k := range ev.Payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertEnvelope fija lo común a las dos filas: el flujo SINTÉTICO del pipeline, la
// clase de telemetría y el contacto OPACO de la clave de ventana.
func assertEnvelope(t *testing.T, ev store.FlowEvent) {
	t.Helper()
	if ev.TenantID != tenantID || ev.ContactID != contactID {
		t.Fatalf("la fila %q va con tenant %q y contacto %q", ev.Name, ev.TenantID, ev.ContactID)
	}
	if ev.FlowID != stages.IntakeFlowID || ev.FlowVersion != stages.IntakeFlowVersion || ev.Kind != "event" {
		t.Fatalf("la fila %q va firmada %q v%d kind %q", ev.Name, ev.FlowID, ev.FlowVersion, ev.Kind)
	}
}

// TestEventVocabulary_ValuesAreLiteral: los nombres de evento y la firma son claves de
// `flow_events` que ya leen consultas y paneles.
func TestEventVocabulary_ValuesAreLiteral(t *testing.T) {
	if stages.EventDraftCreated != "intake_draft_created" || stages.EventReanalyzed != "intake_reanalyzed" {
		t.Fatalf("eventos = %q, %q", stages.EventDraftCreated, stages.EventReanalyzed)
	}
	if stages.IntakeFlowID != "_intake_llm" || stages.IntakeFlowVersion != 1 {
		t.Fatalf("firma = %q v%d", stages.IntakeFlowID, stages.IntakeFlowVersion)
	}
}

// ---------------------------------------------------------------------------
// `intake_draft_created`
// ---------------------------------------------------------------------------

// TestDraftRun_Ambar_MetricIsTheLiteralOfDesign10: UNA fila, cuatro contadores con sus
// tipos, y el envío no es ni `matched` ni `unmatched`.
func TestDraftRun_Ambar_MetricIsTheLiteralOfDesign10(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	b.runAmbar(t, ambarP4Job())

	if got := b.events.names(); !reflect.DeepEqual(got, []string{"intake_draft_created"}) {
		t.Fatalf("filas del pipeline normal = %v; se esperaba UNA, la del borrador", got)
	}
	ev := b.events.only(t, stages.EventDraftCreated)
	assertEnvelope(t, ev)
	want := map[string]any{"elapsed_ms": int64(174000), "lines": 4, "matched": 2, "unmatched": 1}
	if !reflect.DeepEqual(ev.Payload, want) {
		t.Fatalf("payload = %#v\nse esperaba %#v", ev.Payload, want)
	}
	raw, err := json.Marshal(ev.Payload)
	if err != nil || string(raw) != `{"elapsed_ms":174000,"lines":4,"matched":2,"unmatched":1}` {
		t.Fatalf("payload serializado = %s (%v)", raw, err)
	}
}

// TestDraftRun_MetricCarriesNoClientWord barre el payload YA SERIALIZADO: `flow_events`
// es una tabla en claro. El fixture SÍ mete texto del cliente (el literal, las
// evidencias, las etiquetas): sin eso el barrido sería vacuo.
func TestDraftRun_MetricCarriesNoClientWord(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.runAmbar(t, reanalysisJob())
	if _, p := b.lastRevision(t, art.IntakeID); p.SourceText != ambarText {
		t.Fatal("el fixture no llevó el literal a la etapa: el barrido no probaría nada")
	}

	for _, name := range []string{stages.EventDraftCreated, stages.EventReanalyzed} {
		ev := b.events.only(t, name)
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("serializar la fila: %v", err)
		}
		assertNoClientWords(t, "la fila "+name, string(raw))
	}
}

// TestDraftRun_MetricCountsByKind: `lines` son todas; `matched` y `unmatched`, las de su
// clase; el envío no cuenta en ninguna, y un borrador vacío publica ceros.
func TestDraftRun_MetricCountsByKind(t *testing.T) {
	line := func(kind string) stages.Line { return stages.Line{Kind: kind, Label: "x", Qty: 1} }
	cases := []struct {
		name                      string
		lines                     []stages.Line
		total, matched, unmatched int
	}{
		{"empty draft", nil, 0, 0, 0},
		{"only shipping", []stages.Line{line(stages.KindShipping)}, 1, 0, 0},
		{"three matched, two unmatched, shipping", []stages.Line{
			line(stages.KindMatched), line(stages.KindUnmatched), line(stages.KindMatched),
			line(stages.KindMatched), line(stages.KindUnmatched), line(stages.KindShipping),
		}, 6, 3, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			b.run(t, ambarP4Job(), stages.DraftInput{Match: &stages.MatchArtifact{Version: 1, Lines: c.lines}})
			p := b.events.only(t, stages.EventDraftCreated).Payload
			if p["lines"] != c.total || p["matched"] != c.matched || p["unmatched"] != c.unmatched {
				t.Fatalf("contadores = %v; se esperaban %d/%d/%d", p, c.total, c.matched, c.unmatched)
			}
		})
	}
}

// TestDraftRun_RequestedByMarksTheMetricOnlyWhenTheJobBringsIt: la quinta clave es
// CONDICIONAL. Sin ella, el KPI «tiempo a primer borrador» quedaría envenenado por
// re-análisis pedidos al día siguiente; con ella siempre, cambiarían todos los paneles.
// Entra cuando la marca del job no es vacía, con su valor tal cual.
func TestDraftRun_RequestedByMarksTheMetricOnlyWhenTheJobBringsIt(t *testing.T) {
	stranger := ambarP4Job()
	stranger.Reanalysis.RequestedBy = "crm"
	four := []string{"elapsed_ms", "lines", "matched", "unmatched"}
	five := []string{"elapsed_ms", "lines", "matched", "requested_by", "unmatched"}
	cases := []struct {
		name string
		job  intake.ClaimedJob
		keys []string
		mark any
	}{
		{"normal pipeline: the four counters, byte by byte", ambarP4Job(), four, nil},
		{"reanalysis asked by the owner", reanalysisJob(), five, intake.RequestedByOwner},
		{"any other non-empty mark travels as it is", stranger, five, "crm"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			b.runAmbar(t, c.job)
			ev := b.events.only(t, stages.EventDraftCreated)
			if got := payloadKeys(ev); !reflect.DeepEqual(got, c.keys) {
				t.Fatalf("claves = %v, se esperaban %v", got, c.keys)
			}
			if ev.Payload["requested_by"] != c.mark {
				t.Fatalf("requested_by = %#v, se esperaba %#v", ev.Payload["requested_by"], c.mark)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// `elapsed_ms`
// ---------------------------------------------------------------------------

// TestDraftRun_ElapsedMSComesFromTheMessageTS cubre el cronómetro con el reloj FIJADO,
// que es la única forma de afirmar un número —y lo que caería si la etapa leyera el reloj
// por otro camino que WithClock—:
//
//   - el caso del plan: 174 s desde el mensaje del cliente;
//   - el job REANUDADO horas después: el número CRECE, porque mide la espera del cliente
//     y no el tiempo de proceso;
//   - los relojes desalineados: un resultado negativo NO se publica;
//   - sin `message_ts` no hay desde dónde medir: 0, y el log dice que ese 0 no mide.
func TestDraftRun_ElapsedMSComesFromTheMessageTS(t *testing.T) {
	cases := []struct {
		name    string
		job     intake.ClaimedJob
		now     time.Time
		want    int64
		warning string
	}{
		{"the case of design 10", ambarP4Job(), ambarMessageTS.Add(ambarElapsed), 174000, ""},
		{"resumed three hours later: the client's wait is longer", ambarP4Job(), ambarMessageTS.Add(3 * time.Hour), 10800000, ""},
		{"sub-millisecond is truncated", ambarP4Job(), ambarMessageTS.Add(1999 * time.Microsecond), 1, ""},
		{"same instant", ambarP4Job(), ambarMessageTS, 0, ""},
		{"the edge clock is ahead of the cloud: never a negative", ambarP4Job(), ambarMessageTS.Add(-90 * time.Second), 0,
			"draft: elapsed_ms salió NEGATIVO; el reloj del Edge y el del cloud están desalineados"},
		{"no message_ts", ambarJob(), ambarMessageTS.Add(ambarElapsed), 0,
			"draft: el job no trae message_ts; elapsed_ms se publica como 0 y NO mide la espera del cliente"},
	}
	const anyWarning = "elapsed_ms"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, c.now)
			art := b.runAmbar(t, c.job)
			if art.ElapsedMS != c.want {
				t.Fatalf("elapsed_ms del artefacto = %d, se esperaba %d", art.ElapsedMS, c.want)
			}
			if got := b.events.only(t, stages.EventDraftCreated).Payload["elapsed_ms"]; got != c.want {
				t.Fatalf("elapsed_ms de la métrica = %#v, se esperaba int64(%d): es el MISMO número", got, c.want)
			}
			if c.warning == "" {
				b.assertLogLacks(t, anyWarning+" se publica")
				b.assertLogLacks(t, anyWarning+" salió")
				return
			}
			b.assertLogHas(t, c.warning)
		})
	}

	negative := newDraftBench(t, ambarMessageTS.Add(-90*time.Second))
	negative.runAmbar(t, ambarP4Job())
	negative.assertLogHas(t, "desfase_ms=-90000")
}

// ---------------------------------------------------------------------------
// `intake_reanalyzed`
// ---------------------------------------------------------------------------

// TestDraftRun_ReanalyzedEventIsTheLiteralOfDesign10 fija el payload ENTERO: cuatro
// claves y ninguna quinta, con `via` —NUNCA `provider`—.
//
// 🔴 EL FIXTURE LE DA HISTORIA A LA SOLICITUD: dos pasadas normales antes, así que
// `from_rev` es 1 y `to_rev` es 3. Con los dos a 1, dos campos intercambiados pasarían el
// golden, y un `to_rev = from_rev + 1` también.
func TestDraftRun_ReanalyzedEventIsTheLiteralOfDesign10(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	b.runAmbar(t, ambarP4Job())
	b.runAmbar(t, ambarP4Job())
	art := b.runAmbar(t, reanalysisJob())
	if art.RevisionNo != 3 {
		t.Fatalf("revision_no = %d; el store numera la siguiente, no el llamante", art.RevisionNo)
	}

	ev := b.events.only(t, stages.EventReanalyzed)
	assertEnvelope(t, ev)
	want := map[string]any{"via": "local", "from_rev": 1, "to_rev": 3, "source": "event_thread"}
	if !reflect.DeepEqual(ev.Payload, want) {
		t.Fatalf("payload = %#v\nse esperaba %#v", ev.Payload, want)
	}
	if _, has := ev.Payload["requested_by"]; has {
		t.Fatal("requested_by es de la métrica del borrador, no de este evento")
	}
	// Las dos filas del re-análisis, en su orden: primero el borrador, luego el desenlace.
	wantNames := []string{
		stages.EventDraftCreated, stages.EventDraftCreated, stages.EventDraftCreated, stages.EventReanalyzed,
	}
	if got := b.events.names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("filas = %v, se esperaban %v", got, wantNames)
	}
}

// TestDraftRun_ReanalyzedEventCarriesTheJobValuesAsTheyAre: ninguno de los cuatro
// valores se inventa. `from_rev` 0 es «no había revisión previa» y se publica 0.
func TestDraftRun_ReanalyzedEventCarriesTheJobValuesAsTheyAre(t *testing.T) {
	job := reanalysisJob()
	job.Reanalysis.Via = "api"
	job.Reanalysis.Source = stages.SourcePastedText
	job.Reanalysis.From = 0

	b := newDraftBench(t, ambarNow())
	b.runAmbar(t, job)

	want := map[string]any{"via": "api", "from_rev": 0, "to_rev": 1, "source": "pasted_text"}
	if got := b.events.only(t, stages.EventReanalyzed).Payload; !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %#v\nse esperaba %#v", got, want)
	}
}

// TestDraftRun_OnlyTheOwnersReanalysisPublishesReanalyzed es la mitad que protege: sin la
// marca de la dueña no hay evento. Emitir siempre contaría como «el LLM se equivocó» cada
// pedido que salió bien a la primera.
func TestDraftRun_OnlyTheOwnersReanalysisPublishesReanalyzed(t *testing.T) {
	stranger := reanalysisJob()
	stranger.Reanalysis.RequestedBy = "crm"
	for name, job := range map[string]intake.ClaimedJob{"normal pipeline": ambarP4Job(), "a mark that is not the owner's": stranger} {
		t.Run(name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			b.runAmbar(t, job)
			// Y la que SÍ tiene que estar sigue estando: no pasa por no publicar nada.
			if got := b.events.names(); !reflect.DeepEqual(got, []string{stages.EventDraftCreated}) {
				t.Fatalf("filas = %v; solo la del borrador", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BEST-EFFORT
// ---------------------------------------------------------------------------

// TestDraftRun_EventFailuresDoNotKillTheDraft: perder una fila de telemetría no puede
// costar el pedido de un cliente. La etapa termina bien, la revisión y la marca están
// escritas, la OTRA fila se intenta igual y el log dice qué se perdió.
func TestDraftRun_EventFailuresDoNotKillTheDraft(t *testing.T) {
	cases := []struct {
		name    string
		failOn  string
		warning string
		written []string
	}{
		{"the draft metric fails", stages.EventDraftCreated,
			"draft: no se pudo publicar la métrica del borrador; el borrador SÍ está creado", []string{stages.EventReanalyzed}},
		{"the reanalysis metric fails", stages.EventReanalyzed,
			"draft: no se pudo publicar la métrica del re-análisis; la revisión SÍ está escrita", []string{stages.EventDraftCreated}},
		{"the outbox is down", "", "draft: no se pudo publicar la métrica del borrador; el borrador SÍ está creado", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			b.events.err = errors.New("la base no está")
			b.events.failOn = c.failOn

			art := b.runAmbar(t, reanalysisJob())

			if art.IntakeID != ambarIntakeID || len(b.revisions.Revisions(art.IntakeID)) != 1 || len(b.jobs.saved) != 1 {
				t.Fatalf("la métrica es best-effort: artefacto %+v, marcas %d", *art, len(b.jobs.saved))
			}
			if got := b.events.names(); !reflect.DeepEqual(got, c.written) {
				t.Fatalf("filas escritas = %v, se esperaban %v", got, c.written)
			}
			wantCalls := []string{stages.EventDraftCreated, stages.EventReanalyzed}
			if !reflect.DeepEqual(b.events.calls, wantCalls) {
				t.Fatalf("intentos = %v; el fallo de una fila no ahorra la otra", b.events.calls)
			}
			b.assertLogHas(t, c.warning)
			b.assertLogHas(t, "la base no está")
		})
	}
}
