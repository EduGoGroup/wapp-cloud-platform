//go:build pendiente

package stages_test

// draft_doubles_test.go — los dobles y el atrezo de los tests de la etapa `draft` (sin
// fichero de producción gemelo): los dos puertos del almacén de flujos, el banco y el
// artefacto del match del caso Ambar. El store de artefactos, el job y el log son los
// comunes de doubles_test.go; el job con `message_ts`, el de p4_test.go.
//
// 🔴 EL ARTEFACTO DEL MATCH VA ESCRITO A MANO, y no sale de correr la etapa `match`: lo
// que aquí se fija es el contrato de `draft` ante un artefacto con esa forma. El
// encadenado real match → draft lo recorre el proceso P4 de F9.
//
// 🔴 PUENTE 1: `store.Intake` y `store.FlowEvent` son del almacén VIEJO de flujos; son
// los tipos de los puertos de producción (draft.go, draft_events.go) y entran aquí solo
// como datos. Los dobles son propios: no se usa `store.MemoryRepository`.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ambarElapsed son los 174 s que design §10 usa de ejemplo (`{"elapsed_ms":174000,…}`).
// El reloj de la etapa se fija en `message_ts` + esto: así el número se AFIRMA.
const ambarElapsed = 174 * time.Second

// ambarIntakeID es el id que la etapa deriva del evento del fixture (`e-p2`): el UUIDv5
// del evento en el espacio fijo 6f8f5b2e-3d61-5a4c-9a1e-0b7c4d2f8a13. Va LITERAL —y no
// recalculado con la fórmula— para que un cambio del espacio de nombres caiga aquí.
const ambarIntakeID = "0361672d-70d6-52d9-b664-00a3a35f047b"

// ambarNow es el reloj fijado del caso.
func ambarNow() time.Time { return ambarMessageTS.Add(ambarElapsed) }

// ---------------------------------------------------------------------------
// LOS DOS PUERTOS DEL ALMACÉN DE FLUJOS
// ---------------------------------------------------------------------------

// fakeIntakeStore es `public.intakes` visto por la etapa. Como la tabla real, la lectura
// por evento NO filtra por estado, y el upsert es por id.
type fakeIntakeStore struct {
	rows    []store.Intake
	asked   []string // "tenant/evento" de cada lectura
	upserts int
	getErr  error
	putErr  error
}

func (s *fakeIntakeStore) GetIntakeByEvent(_ context.Context, tenant, event string) (store.Intake, bool, error) {
	s.asked = append(s.asked, tenant+"/"+event)
	if s.getErr != nil {
		return store.Intake{}, false, s.getErr
	}
	for _, r := range s.rows {
		if r.TenantID == tenant && r.EventID == event {
			return r, true, nil
		}
	}
	return store.Intake{}, false, nil
}

func (s *fakeIntakeStore) UpsertIntake(_ context.Context, in store.Intake) error {
	s.upserts++
	if s.putErr != nil {
		return s.putErr
	}
	for i := range s.rows {
		if s.rows[i].ID == in.ID {
			s.rows[i] = in
			return nil
		}
	}
	s.rows = append(s.rows, in)
	return nil
}

// fakeEventWriter es el outbox de `flow_events`. `failOn` hace fallar SOLO las filas de
// ese nombre; `err` sin `failOn`, todas. `calls` cuenta también las que fallan.
type fakeEventWriter struct {
	events []store.FlowEvent
	calls  []string
	err    error
	failOn string
}

func (w *fakeEventWriter) InsertFlowEvent(_ context.Context, ev store.FlowEvent) error {
	w.calls = append(w.calls, ev.Name)
	if w.err != nil && (w.failOn == "" || w.failOn == ev.Name) {
		return w.err
	}
	w.events = append(w.events, ev)
	return nil
}

// names son los nombres de las filas escritas, en orden.
func (w *fakeEventWriter) names() []string {
	out := make([]string, 0, len(w.events))
	for _, ev := range w.events {
		out = append(out, ev.Name)
	}
	return out
}

// only devuelve la ÚNICA fila con ese nombre y falla si hay ninguna o más de una.
func (w *fakeEventWriter) only(t *testing.T, name string) store.FlowEvent {
	t.Helper()
	var found []store.FlowEvent
	for _, ev := range w.events {
		if ev.Name == name {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("filas de %q = %d, se esperaba UNA; las escritas fueron %v", name, len(found), w.names())
	}
	return found[0]
}

// spyRevisions envuelve un escritor de revisiones y anota lo que se le pide. `err` hace
// fallar la escritura.
type spyRevisions struct {
	real stages.RevisionWriter
	got  []intakes.Revision
	err  error
}

func (s *spyRevisions) InsertRevision(ctx context.Context, rev intakes.Revision) (intakes.Revision, error) {
	s.got = append(s.got, rev)
	if s.err != nil {
		return intakes.Revision{}, s.err
	}
	return s.real.InsertRevision(ctx, rev)
}

// ---------------------------------------------------------------------------
// EL BANCO
// ---------------------------------------------------------------------------

// draftBench son la etapa y todo lo que deja ver. Las revisiones van contra el
// `intakes.MemoryStore` NUEVO —el doble del store con cipher del literal— porque es el
// único que parte el payload como producción (R-06).
type draftBench struct {
	stage     *stages.Draft
	intakes   *fakeIntakeStore
	revisions *intakes.MemoryStore
	spy       *spyRevisions
	events    *fakeEventWriter
	jobs      *fakeStore
	log       bytes.Buffer
}

// newDraftBench construye la etapa con el reloj FIJADO en `now`.
func newDraftBench(t *testing.T, now time.Time, opts ...stages.DraftOption) *draftBench {
	t.Helper()
	b := &draftBench{
		intakes:   &fakeIntakeStore{},
		revisions: intakes.NewMemoryStore(),
		events:    &fakeEventWriter{},
		jobs:      &fakeStore{},
	}
	b.spy = &spyRevisions{real: b.revisions}
	opts = append([]stages.DraftOption{stages.WithClock(func() time.Time { return now })}, opts...)
	stage, err := stages.NewDraft(captureLog(&b.log), b.jobs, b.intakes, b.spy, b.events, opts...)
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	b.stage = stage
	return b
}

// run corre la etapa y exige que no falle.
func (b *draftBench) run(t *testing.T, job intake.ClaimedJob, in stages.DraftInput) *stages.DraftArtifact {
	t.Helper()
	art, err := b.stage.Run(context.Background(), job, in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if art == nil {
		t.Fatal("Run devolvió artefacto nil sin error")
	}
	return art
}

// runAmbar es el caso común: el job dado sobre la entrada completa del caso.
func (b *draftBench) runAmbar(t *testing.T, job intake.ClaimedJob) *stages.DraftArtifact {
	t.Helper()
	return b.run(t, job, ambarDraftInput())
}

// theIntake devuelve la única cabecera escrita.
func (b *draftBench) theIntake(t *testing.T) store.Intake {
	t.Helper()
	if len(b.intakes.rows) != 1 {
		t.Fatalf("solicitudes = %d, se esperaba UNA, ni cero ni dos: %+v", len(b.intakes.rows), b.intakes.rows)
	}
	return b.intakes.rows[0]
}

// lastRevision devuelve la última revisión de la solicitud tal como la devuelve la
// LECTURA (con el literal de vuelta en su sitio) y su payload decodificado.
func (b *draftBench) lastRevision(t *testing.T, intakeID string) (intakes.Revision, stages.RevisionPayload) {
	t.Helper()
	revs := b.revisions.Revisions(intakeID)
	if len(revs) == 0 {
		t.Fatalf("la solicitud %s no tiene ninguna revisión", intakeID)
	}
	last := revs[len(revs)-1]
	var p stages.RevisionPayload
	if err := json.Unmarshal(last.Payload, &p); err != nil {
		t.Fatalf("el payload de la revisión no es un RevisionPayload: %v", err)
	}
	return last, p
}

// assertLogHas exige un mensaje en el log.
func (b *draftBench) assertLogHas(t *testing.T, fragment string) {
	t.Helper()
	if !strings.Contains(b.log.String(), fragment) {
		t.Fatalf("el log no dice %q:\n%s", fragment, b.log.String())
	}
}

// assertLogLacks exige que un texto NO esté en el log.
func (b *draftBench) assertLogLacks(t *testing.T, fragment string) {
	t.Helper()
	if strings.Contains(b.log.String(), fragment) {
		t.Fatalf("el log lleva %q y no debía:\n%s", fragment, b.log.String())
	}
}

// ---------------------------------------------------------------------------
// EL CASO AMBAR, YA CRUZADO CON EL CATÁLOGO
// ---------------------------------------------------------------------------

// ambarShippingLabel es la etiqueta del envío sin zona resuelta.
const ambarShippingLabel = "Envío por confirmar"

// ambarMatch es el artefacto del match del caso, el escenario EXACTO de design §7.4:
// cuatro líneas —torta de chocolate con sus dos variantes, torta de vainilla sin match,
// tequeños a $490 y el envío por confirmar— y el añadido convertido en `customization`
// porque el catálogo no lo vende. Es el que cuadra con la métrica de §10
// (`"lines":4,"matched":2,"unmatched":1`).
func ambarMatch() *stages.MatchArtifact {
	tequenosPrice := 490.0
	return &stages.MatchArtifact{
		Version: llm.ArtifactVersion,
		Lines: []stages.Line{
			{
				Kind: stages.KindMatched, SKU: "TORTA-CHOC", Label: "Torta chocolate húmedo + crema choc.", Qty: 1,
				Customization: "sin lactosa, decoración infantil",
				Range:         &llm.Range{Min: 10, Max: 12, Unit: "porciones"},
				VariantOptions: []stages.VariantOption{
					{SKU: "TORTA-CHOC#10", Label: "Torta chocolate húmedo + crema choc. — 10 porciones", Price: 2100},
					{SKU: "TORTA-CHOC#12", Label: "Torta chocolate húmedo + crema choc. — 12 porciones", Price: 2400},
				},
				Match:    &stages.MatchProvenance{Strategy: "zona_gris_falsa", Confidence: 0.91},
				Evidence: chocolateCakeEvidence,
			},
			{
				Kind: stages.KindUnmatched, Label: "torta de vainilla con lluvia de colores", Qty: 1,
				Range:    &llm.Range{Min: 25, Max: 30, Unit: "porciones"},
				Evidence: vanillaCakeEvidence,
			},
			{
				Kind: stages.KindMatched, SKU: "TEQ-30", Label: "Tequeños congelados", Qty: 1,
				UnitPrice: &tequenosPrice, UnitKind: "package", PackageSize: 30,
				Match:    &stages.MatchProvenance{Strategy: "exact", Confidence: 1},
				Evidence: tequenosEvidence,
			},
			{
				Kind: stages.KindShipping, SKU: intakes.ShippingSKU, Label: ambarShippingLabel, Qty: 1,
				Note: "por confirmar zona",
			},
		},
	}
}

// ambarDraftInput es la entrada completa del caso: el match, el literal del cliente, la
// fecha que calculó P4 y la vía por la que corrió.
func ambarDraftInput() stages.DraftInput {
	return stages.DraftInput{
		Match:        ambarMatch(),
		SourceText:   ambarText,
		DeliveryDate: "2026-07-22",
		Analysis:     stages.Analysis{Provider: "api", Model: "claude-x"},
	}
}

// cartIntakeID es el id SORTEADO de la solicitud que el carrito dejó sobre el evento
// antes de que llegara el pipeline (hallazgo #24): no coincide con el derivado.
const cartIntakeID = "b7d41f28-6c05-4e93-a1d2-3f8e0b5c9a17"

// cartIntakeRow es esa solicitud, en el estado dado.
func cartIntakeRow(status string) store.Intake {
	return store.Intake{
		ID: cartIntakeID, TenantID: tenantID, ContactID: contactID, SessionID: sessionID,
		Status: status, EventID: eventID, Total: 4200,
	}
}

// reanalysisJob es el job del caso CON la marca de la dueña: lo pidió ella, por vía
// local, con material del hilo, sucediendo a la revisión 1.
func reanalysisJob() intake.ClaimedJob {
	job := ambarP4Job()
	job.Reanalysis = intake.Reanalysis{
		RequestedBy: intake.RequestedByOwner,
		Via:         "local",
		Source:      stages.SourceEventThread,
		From:        1,
	}
	return job
}

// clientWords son los fragmentos que NO pueden aparecer donde no entra el cliente: su
// literal, sus evidencias y las etiquetas y skus de lo que pidió.
func clientWords() []string {
	return []string{
		"torta", "tequeños", "vainilla", "chocolate", "porciones", "lactosa",
		chocolateCakeEvidence, tequenosEvidence, ambarText,
		"TORTA-CHOC", "TEQ-30", "Envío",
	}
}

// assertNoClientWords barre un texto YA SERIALIZADO, sin distinguir mayúsculas. Se barre
// entero y no campo a campo: lo que hay que garantizar es que no hay DÓNDE se cuele.
func assertNoClientWords(t *testing.T, where, raw string) {
	t.Helper()
	lowered := strings.ToLower(raw)
	for _, word := range clientWords() {
		if strings.Contains(lowered, strings.ToLower(word)) {
			t.Fatalf("%s lleva %q: ahí no entra ni el literal ni el catálogo (ADR-0034)", where, word)
		}
	}
}
