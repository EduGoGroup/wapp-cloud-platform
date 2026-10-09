//go:build pendiente

package pipeline_test

// pipeline_chain_test.go — la cadena de un job, tal como la promete RunOnce: el orden de
// las etapas, lo que cada una recibe y las dos lecturas por job (catálogo y zonas). El
// sobre, la reanudación y las cero ideas están en pipeline_chain_resume_test.go.
//
// 🔴 POR QUÉ HACEN FALTA. Un worker que llamara a `match` con la entrada vacía, o que no
// llegara a `draft`, terminaría igual cada job en `done` sin un error en ningún log. Lo
// que aquí se prueba es esa costura, no las etapas (que tienen sus tests en `stages`).

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// allStages son las cinco etapas en el orden de la cadena.
var allStages = []string{intake.StageP2, intake.StageP3, intake.StageP4, intake.StageMatch, intake.StageDraft}

// tenantSpy es un lector por tenant (catálogo y zonas) que apunta con qué tenant se le
// preguntó y puede fallar.
type tenantSpy struct {
	index *indice.Indice
	zones []intakes.ShippingZone
	err   error
	// onAsk corre en cada lectura: es por donde un test hace que la lectura «tarde».
	onAsk func()

	mu      sync.Mutex
	tenants []string
}

func (s *tenantSpy) note(tenant string) {
	if s.onAsk != nil {
		s.onAsk()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants = append(s.tenants, tenant)
}

func (s *tenantSpy) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tenants...)
}

func (s *tenantSpy) Obtener(_ context.Context, tenant string) (*indice.Indice, error) {
	s.note(tenant)
	return s.index, s.err
}

func (s *tenantSpy) ShippingZones(_ context.Context, tenant string) ([]intakes.ShippingZone, error) {
	s.note(tenant)
	return s.zones, s.err
}

// run drena una vez y exige que el job quede en ese estado.
func (r *rig) run(t *testing.T, id, status string) {
	t.Helper()
	r.drain(context.Background())
	if row := r.row(t, id); row.Status != status {
		t.Fatalf("el job quedó %q (error=%q), se esperaba %q:\n%s", row.Status, row.Error, status, r.log.dump())
	}
}

// TestChain_RunsTheFiveStagesInOrderAndClosesWithTheIntakeID: las cinco se llaman UNA vez
// y EN ORDEN, los cinco artefactos quedan persistidos y el `intake_id` que devolvió
// `draft` llega a la fila — el valor EXACTO, no «uno no vacío».
func TestChain_RunsTheFiveStagesInOrderAndClosesWithTheIntakeID(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	if got := r.trace.list(); !reflect.DeepEqual(got, allStages) {
		t.Fatalf("las etapas se llamaron en el orden %v, se esperaba %v", got, allStages)
	}
	row := r.row(t, id)
	for _, stage := range allStages {
		if raw := row.Artifacts[stage]; len(raw) == 0 {
			t.Errorf("falta el artefacto de la etapa %q", stage)
		}
	}
	if row.Stage != intake.StageDraft {
		t.Errorf("la última etapa del job es %q, se esperaba %q", row.Stage, intake.StageDraft)
	}
	if row.IntakeID != draftIntakeID {
		t.Errorf("el job terminó con intake_id=%q, se esperaba %q: el id del borrador NO llegó al cierre", row.IntakeID, draftIntakeID)
	}
	if row.Attempts != 0 {
		t.Errorf("un job que sale a la primera no consume intentos; attempts=%d", row.Attempts)
	}
	done := r.log.one(t, "INFO", "pipeline: job DONE")
	if done.fields["intake_id"] != draftIntakeID || done.fields["job_id"] != id || done.fields["intento"] != 1 {
		t.Errorf("la línea del cierre lleva %v", done.fields)
	}
	r.log.requireNoErrors(t)
}

// TestChain_TheModelStagesReceiveWhatThePreviousOnesLeft: el literal descifrado va a las
// tres etapas LLM; P3 recibe las ideas de P2; P4, los ítems de P3 y la pista de entrega de
// P2. Cada uno de estos cables se puede perder SIN ERROR.
func TestChain_TheModelStagesReceiveWhatThePreviousOnesLeft(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.wants = []llm.Want{{Idea: "torta", Evidence: "torta"}, {Idea: "tequeños", Evidence: "tequeños"}}
	r.p2.hint = &llm.Hint{Text: "el viernes", Evidence: "para el viernes"}
	r.p3.items = []llm.ItemSpec{
		{Product: "torta", Evidence: "torta"}, {Product: "tequeños", Evidence: "tequeños"}, {Product: "jugo", Evidence: "jugo"},
	}
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	if r.p2.literal != clearLiteral || r.p3.literal != clearLiteral || r.p4.literal != clearLiteral {
		t.Errorf("literales recibidos: P2=%q P3=%q P4=%q; a las tres les llega el que descifró el worker",
			r.p2.literal, r.p3.literal, r.p4.literal)
	}
	if !reflect.DeepEqual(r.p3.wants, r.p2.wants) {
		t.Errorf("P3 recibió %v como ideas, se esperaban las de P2 %v", r.p3.wants, r.p2.wants)
	}
	if !reflect.DeepEqual(r.p4.items, r.p3.items) {
		t.Errorf("P4 recibió %v como ítems, se esperaban los de P3 %v", r.p4.items, r.p3.items)
	}
	if r.p4.hint == nil || *r.p4.hint != *r.p2.hint {
		t.Errorf("P4 recibió %v como pista de entrega, se esperaba la de P2 %v", r.p4.hint, r.p2.hint)
	}

}

// TestChain_MatchAndDraftReceiveWhatTheChainLeft: `match` recibe las cantidades de P4;
// `draft`, el artefacto del match, el literal descifrado y la fecha de entrega de P4.
func TestChain_MatchAndDraftReceiveWhatTheChainLeft(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p3.items = []llm.ItemSpec{
		{Product: "torta", Evidence: "torta"}, {Product: "tequeños", Evidence: "tequeños"}, {Product: "jugo", Evidence: "jugo"},
	}
	r.p4.date = "2026-09-04"
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	matchIn, ok := r.match.seen()
	if !ok {
		t.Fatal("la etapa `match` no se llamó")
	}
	if matchIn.Quantities == nil || len(matchIn.Quantities.Items) != 3 || matchIn.Quantities.DeliveryDate != "2026-09-04" {
		t.Errorf("el match recibió %+v como cantidades; se esperaba lo que dejó P4 (3 ítems, con su fecha)", matchIn.Quantities)
	}
	// La nota del pedido entero NO la produce nadie todavía: es un hueco declarado.
	if matchIn.Note != stages.NoOrderNote {
		t.Errorf("el match recibió una nota de pedido (%q) y hoy NADIE la produce", matchIn.Note)
	}

	draftIn, _, ok := r.draft.seen()
	if !ok {
		t.Fatal("la etapa `draft` no se llamó")
	}
	// Tres ítems más la línea de envío: lo que dejó ESTE match, no uno vacío.
	if draftIn.Match == nil || len(draftIn.Match.Lines) != 4 {
		t.Errorf("el draft recibió %+v como artefacto del match; se esperaban sus 4 líneas", draftIn.Match)
	}
	if draftIn.SourceText != clearLiteral {
		t.Errorf("el draft recibió %q como literal, se esperaba el que descifró el worker", draftIn.SourceText)
	}
	if draftIn.DeliveryDate != "2026-09-04" {
		t.Errorf("el draft recibió %q como fecha de entrega; la que dejó P4 era 2026-09-04", draftIn.DeliveryDate)
	}
}

// TestChain_DraftGetsNoMediaAndNoAnalysis: los DOS HUECOS DECLARADOS del worker, fijados
// para que se vean el día que dejen de serlo: no ancla adjuntos y no rellena la vía del
// análisis. Quien los cablee pondrá rojo este test, y es lo que tiene que actualizar.
func TestChain_DraftGetsNoMediaAndNoAnalysis(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	in, _, _ := r.draft.seen()
	if len(in.Media.ByLine) != 0 || len(in.Media.Request) != 0 {
		t.Errorf("el draft recibió un reparto de adjuntos (%+v) y el worker NO cablea el anclaje", in.Media)
	}
	if in.Analysis != (stages.Analysis{}) {
		t.Errorf("el draft recibió un análisis (%+v) y el worker NO lo rellena", in.Analysis)
	}
}

// TestChain_TheJobReachesTheDraftAsItWasClaimed (R-07): el worker no decide nada sobre el
// re-análisis ni empuja al CRM; le pasa a `draft` el job con el contexto que trajo el
// reclamo, y es la etapa la que actúa.
func TestChain_TheJobReachesTheDraftAsItWasClaimed(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	row := r.healthyRow("")
	row.Reanalysis = intake.Reanalysis{RequestedBy: intake.RequestedByOwner, Via: "api", Source: "event_thread", From: 1}
	id := r.mem.Seed(row)
	r.run(t, id, intake.StatusDone)

	_, job, _ := r.draft.seen()
	if job.ID != id || job.Reanalysis != row.Reanalysis || job.Key != row.Key {
		t.Errorf("el draft recibió el job %+v; se esperaba el reclamado, con su contexto de re-análisis", job)
	}
}

// TestChain_StagesGetAContextWithoutDeadline: el plazo es POR LLAMADA y lo pone cada etapa
// (CallTimeoutFloor); el worker no acota el ctx por job ni por etapa, o el plazo se
// repartiría entre las N llamadas del fan-out de P3.
func TestChain_StagesGetAContextWithoutDeadline(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	for name, bounded := range map[string]bool{
		"p2": r.p2.anyBounded(), "p3": r.p3.anyBounded(), "p4": r.p4.anyBounded(),
		"match": r.match.anyBounded(), "draft": r.draft.anyBounded(),
	} {
		if bounded {
			t.Errorf("la etapa %q recibió un ctx con plazo; el worker no lo pone", name)
		}
	}
}

// TestChain_TheCatalogIsReadOncePerJobForItsTenant: UNA lectura por job, no una por ítem —
// por eso el pedido tiene VARIOS ítems—, con el tenant del job, y el índice leído es el que
// recibe `match`.
func TestChain_TheCatalogIsReadOncePerJobForItsTenant(t *testing.T) {
	r := newParts(t)
	index, err := r.catalogs.Obtener(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("leer el índice de prueba: %v", err)
	}
	spy := &tenantSpy{index: index}
	w, err := pipeline.NewWorker(r.log, r.store, r.p2, r.p3, r.p4, r.match, r.draft, spy,
		r.decrypter, pipeline.Config{}, pipeline.WithClock(r.clock.Now))
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	r.w = w
	items := make([]llm.ItemSpec, 0, 7)
	for range 7 {
		items = append(items, llm.ItemSpec{Product: "torta de chocolate", Evidence: "torta"})
	}
	r.p3.items = items
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	if got := spy.asked(); !reflect.DeepEqual(got, []string{tenantID}) {
		t.Fatalf("el catálogo se leyó para %v; para UN job de 7 ítems se esperaba una vez, con su tenant", got)
	}
	if in, _ := r.match.seen(); in.Index != index {
		t.Errorf("el match recibió el índice %p, se esperaba el leído (%p)", in.Index, index)
	}
}

// TestChain_ACatalogThatCannotBeRead_IsAStumbleOfTheMatchStage: sin índice NINGÚN ítem
// puede casar, y un borrador entero `unmatched` afirmaría que el tenant no vende nada de
// lo pedido. El job vuelve a la cola castigado; ni `match` ni `draft` corren.
func TestChain_ACatalogThatCannotBeRead_IsAStumbleOfTheMatchStage(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.catalogs.BreakRead(errors.New("tenant_content no contesta"))
	id := r.seed("")
	r.run(t, id, intake.StatusPending)

	row := r.row(t, id)
	if row.Attempts != 1 || row.IntakeID != "" {
		t.Errorf("el job quedó (attempts=%d, intake_id=%q), se esperaba el intento cobrado y sin borrador", row.Attempts, row.IntakeID)
	}
	if r.match.count() != 0 || r.draft.count() != 0 {
		t.Errorf("match se llamó %d veces y draft %d; sin índice no corre ninguna", r.match.count(), r.draft.count())
	}
	line := r.log.one(t, "WARN", "la etapa falló; el job vuelve a la cola con backoff")
	if line.fields["stage"] != intake.StageMatch || line.fields["causa"] != pipeline.CauseInfra {
		t.Errorf("el tropiezo lleva %v; se esperaba stage=match y causa=infra", line.fields)
	}
	if got := line.fields["error"]; got != "match: leer el catálogo del tenant: tenant_content no contesta" {
		t.Errorf("error = %v", got)
	}
}

// TestChain_ZonesReachTheMatch: las zonas del tenant del job llegan a la etapa.
func TestChain_ZonesReachTheMatch(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.zones.SetShippingZones(tenantID, intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3000})
	r.zones.SetShippingZones("tenant-2", intakes.ShippingZone{Code: "z9", Label: "Otra", Price: 1})
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	in, _ := r.match.seen()
	if len(in.Zones) != 1 || in.Zones[0].Label != "Providencia" {
		t.Fatalf("el match recibió %v como zonas; se esperaba la del tenant del job", in.Zones)
	}
}

// TestChain_ZonesThatCannotBeRead_CostOneLineNotTheOrder: la asimetría deliberada con el
// catálogo. `tenant_settings` que no contesta cuesta el precio del envío, no el pedido: el
// job termina, `match` recibe cero zonas y queda EXACTAMENTE un aviso — sin él, la
// degradación sería indistinguible de un tenant sin zonas.
func TestChain_ZonesThatCannotBeRead_CostOneLineNotTheOrder(t *testing.T) {
	r := newParts(t)
	spy := &tenantSpy{err: errors.New("tenant_settings no contesta")}
	r.build(t, pipeline.Config{}, pipeline.WithShippingZones(spy), pipeline.WithClock(r.clock.Now))
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	if got := r.row(t, id).IntakeID; got != draftIntakeID {
		t.Errorf("el borrador tenía que nacer igual; intake_id=%q", got)
	}
	if in, _ := r.match.seen(); len(in.Zones) != 0 {
		t.Errorf("sin lectura no hay zonas que pasar; llegaron %v", in.Zones)
	}
	if got := spy.asked(); !reflect.DeepEqual(got, []string{tenantID}) {
		t.Errorf("las zonas se pidieron para %v, se esperaba una vez con el tenant del job", got)
	}
	line := r.log.one(t, "WARN", "no se pudieron leer las zonas de envío")
	line.requireKeys(t, "job_id", "stage", "tenant_id", "error")
	if line.fields["stage"] != intake.StageMatch {
		t.Errorf("stage = %v, se esperaba match", line.fields["stage"])
	}
	r.log.requireNoErrors(t)
}

// TestChain_WithoutAZonesReader_TheMatchGetsNoZones: el worker sin lector (ya lo gritó el
// arranque) sigue terminando jobs, con cero zonas y sin aviso por job.
func TestChain_WithoutAZonesReader_TheMatchGetsNoZones(t *testing.T) {
	r := newParts(t)
	r.zones.SetShippingZones(tenantID, intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3000})
	r.build(t, pipeline.Config{}, pipeline.WithClock(r.clock.Now))
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	if in, _ := r.match.seen(); in.Zones != nil {
		t.Errorf("sin lector el match recibió %v como zonas, se esperaba ninguna", in.Zones)
	}
	if got := r.log.find("no se pudieron leer las zonas de envío"); len(got) != 0 {
		t.Errorf("sin lector no hay lectura que falle:\n%s", r.log.dump())
	}
}

// TestChain_AfterAStumble_TheLaterStagesDoNotRun: la cadena se corta en la etapa que cae.
func TestChain_AfterAStumble_TheLaterStagesDoNotRun(t *testing.T) {
	for i, failing := range allStages {
		t.Run(failing, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			script := []step{{err: errors.New("el Edge no contesta")}}
			switch failing {
			case intake.StageP2:
				r.p2.script = script
			case intake.StageP3:
				r.p3.script = script
			case intake.StageP4:
				r.p4.script = script
			case intake.StageMatch:
				r.match.script = script
			case intake.StageDraft:
				r.draft.script = script
			}
			id := r.seed("")
			r.run(t, id, intake.StatusPending)

			if got := r.trace.list(); !reflect.DeepEqual(got, allStages[:i+1]) {
				t.Fatalf("se llamaron %v; tras caer %q se esperaba %v", got, failing, allStages[:i+1])
			}
			row := r.row(t, id)
			if row.IntakeID != "" {
				t.Errorf("el job no terminó y aun así tiene intake_id=%q", row.IntakeID)
			}
			// Lo anterior a la caída queda persistido: es lo que la reanudación se salta.
			if got := len(row.Artifacts); got != i {
				t.Errorf("quedaron %d artefactos, se esperaban los %d anteriores a %q", got, i, failing)
			}
			line := r.log.one(t, "WARN", "la etapa falló; el job vuelve a la cola con backoff")
			if line.fields["stage"] != failing {
				t.Errorf("el tropiezo dice stage=%v, se esperaba %q", line.fields["stage"], failing)
			}
			if strings.Contains(r.log.dump(), "job DONE") {
				t.Error("el job se anunció como DONE tras un tropiezo")
			}
		})
	}
}
