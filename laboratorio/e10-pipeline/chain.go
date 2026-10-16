//go:build laboratorio

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/evidence"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const session = "lab-session-e10"

// result es lo que dejó un caso: lo observado (para el banco) y cómo fue el viaje.
type result struct {
	obs common.Observed
	// p2Attempts y p4Attempts son los intentos que gastó cada etapa (0 si no se llegó).
	p2Attempts, p4Attempts int
	// dropped es cuántas ideas tiró el anclaje de P2.
	dropped int
	// ideas es cuántas ideas dejó vivas P2.
	ideas    int
	duration time.Duration
}

// runPhrase pasa un caso por P1 y por las cinco etapas, como lo haría el worker con un job ya
// reclamado, y devuelve lo observado. No aborta el programa: un fallo de etapa queda en obs.Failed.
func (l *lab) runPhrase(ctx context.Context, p common.Phrase) result {
	start := time.Now()
	res := result{}
	literal := composeLiteral(p.Messages)
	if l.bare {
		literal = p.Text()
	}
	job, err := l.claim(ctx, p)
	if err != nil {
		res.obs.Failed = err.Error()
		return res
	}
	if l.verbose {
		l.n.Step(fmt.Sprintf("El caso %q (%s)", p.ID, p.Category))
		l.n.Note("%s", p.Why)
		label := "el literal que verán las etapas (runtime.ComposeSourceText, el mismo formato que deja el compositor)"
		if l.bare {
			label = "el texto PELADO que verán las etapas (-bare: NO es lo que manda producción)"
		}
		l.n.Show(label, literal)
		l.n.Note("Job %s reclamado de la máquina en memoria: pending → processing. Las etapas solo escriben si sigue en processing.", job.ID)
	}

	l.classify(ctx, p, &res)

	ideas := l.p2(ctx, job, literal, &res)
	if ideas == nil {
		res.duration = time.Since(start)
		return res
	}
	specs := l.p3(ctx, job, literal, ideas, &res)
	if specs == nil {
		res.duration = time.Since(start)
		return res
	}
	quantities := l.p4(ctx, job, literal, specs, ideas.DeliveryHint, &res)
	if quantities == nil {
		res.duration = time.Since(start)
		return res
	}
	res.obs.Date = quantities.DeliveryDate
	match := l.match(ctx, job, quantities, &res)
	if match == nil {
		res.duration = time.Since(start)
		return res
	}
	res.obs.Lines = match.Lines
	l.draft(ctx, job, literal, match, quantities.DeliveryDate, &res)
	res.duration = time.Since(start)
	return res
}

// composeLiteral arma el source_text con la función PURA del compositor real: los mensajes del
// cliente, rotulados y entre las cabeceras que P2 ve en producción.
func composeLiteral(messages []string) string {
	entries := make([]events.ThreadEntry, 0, len(messages))
	for i, m := range messages {
		entries = append(entries, events.ThreadEntry{Seq: i + 1, Role: events.RoleClient, Kind: events.KindMessage, Text: m})
	}
	return runtime.ComposeSourceText(entries).Text
}

// claim siembra un job `pending` para el caso y lo reclama: así las etapas lo ven en processing.
func (l *lab) claim(ctx context.Context, p common.Phrase) (intake.ClaimedJob, error) {
	key := intake.WindowKey{TenantID: common.LabTenant, SessionID: session, ContactID: "lab-contact-" + p.ID, EventID: uuid.NewString()}
	l.machine.Seed(intakehelpertest.Row{Key: key, Status: intake.StatusPending, MessageTS: l.base})
	job, found, err := l.machine.ClaimNext(ctx)
	if err != nil || !found {
		return intake.ClaimedJob{}, fmt.Errorf("reclamar el job del caso %s: found=%v err=%v", p.ID, found, err)
	}
	return job, nil
}

// classify pide P1 como el adelanto (intakeahead): una vez a temperatura 0 y, si la calidad falla,
// otra a 0,3. Aquí se clasifica el texto ENTERO; en producción se clasifica cada mensaje al llegar.
func (l *lab) classify(ctx context.Context, p common.Phrase, res *result) {
	prov, err := l.sel.For(ctx, common.LabTenant, session)
	if err != nil {
		return
	}
	in := common.AmberP1Input(p.Text())
	var c *llm.Classification
	for _, temp := range []float64{llm.TemperatureGreedy, llm.TemperatureRetry} {
		raw, err := prov.ClassifyRequest(ctx, in, llm.Options{Temperature: temp})
		if err == nil {
			c, err = llm.ParseClassification(raw, in)
		}
		if err == nil || !errors.Is(err, llm.ErrLLMQuality) {
			break
		}
	}
	if c == nil {
		if l.verbose {
			l.n.Step("P1 · la intención (referencia: la pide el adelanto, no el pipeline)")
			l.n.Fail(nil, "P1 no dio una clasificación válida")
		}
		return
	}
	res.obs.Intent, res.obs.Confidence = c.Intent, c.Confidence
	if !l.verbose {
		return
	}
	l.n.Step("P1 · la intención (referencia: la pide el adelanto, no el pipeline)")
	l.n.OK("%q con confianza %.2f; evidencia %q.", c.Intent, c.Confidence, clip(c.Evidence, 90))
	l.n.Note("🔴 El pipeline NO mira P1: corre sobre toda ventana que cierre, sea un pedido o un saludo. P1 solo ADELANTA")
	l.n.Note("  el cierre (E12). Si P1 dice «desconocido», P2 corre igual cuando la ventana cierra por su reloj.")
}

// p2 pide las ideas, con los reintentos de calidad del worker imitados.
func (l *lab) p2(ctx context.Context, job intake.ClaimedJob, literal string, res *result) *llm.MainIdeas {
	if l.verbose {
		l.n.Step("P2 · las ideas principales (stages.P2.Run)")
	}
	var (
		ideas *llm.MainIdeas
		err   error
		raws  []string
	)
	for a := 1; a <= l.attempts; a++ {
		res.p2Attempts = a
		ideas, err = l.stages.P2.Run(ctx, job, literal)
		trip, _ := l.spy.Last()
		raws = append(raws, trip.Output)
		if l.verbose {
			l.reportAttempt("P2", a, trip, err, raws)
		}
		if err == nil || !errors.Is(err, llm.ErrLLMQuality) {
			break
		}
	}
	if err != nil {
		res.obs.Failed = shortErr(err)
		if l.verbose {
			l.n.Fail(nil, "P2 agotó sus intentos. El worker marcaría el job `failed` (causa calidad) y NO habría borrador.")
		}
		return nil
	}
	res.ideas = len(ideas.Wants)
	res.dropped = l.showAnchoring(raws[len(raws)-1], literal, ideas)
	return ideas
}

// reportAttempt narra un intento de una etapa de UNA llamada (P2, P4).
func (l *lab) reportAttempt(stage string, a int, trip common.Trip, err error, raws []string) {
	l.n.Note("Intento %d/%d · temperatura %.1f · %s.", a, l.attempts, trip.Request.Temperature, trip.Duration.Round(time.Millisecond))
	l.n.Raw("salida cruda del modelo", trip.Output)
	if a > 1 && raws[a-1] == raws[a-2] {
		l.n.Note("Byte a byte IGUAL que el intento anterior: a temperatura 0 el modelo es determinista, y la etapa no la sube.")
	}
	if err != nil {
		l.n.Fail(err, "%s", stage)
	}
}

// showAnchoring enseña qué ideas tiró el anclaje: las que el modelo dijo y P2 no dejó vivas.
func (l *lab) showAnchoring(raw, literal string, alive *llm.MainIdeas) int {
	said := 0
	if js, err := llm.ExtractJSON(raw); err == nil {
		if all, err := llm.ParseMainIdeas(js); err == nil {
			said = len(all.Wants)
			if l.verbose {
				norm := evidence.Normalize(literal)
				for i, w := range all.Wants {
					if evidence.Contains(norm, w.Evidence) {
						l.n.OK("idea %d %q · evidencia %q → VIVA (está en el literal).", i, w.Idea, w.Evidence)
					} else {
						l.n.Fail(nil, "idea %d %q · evidencia %q → DESCARTADA: no está en el literal.", i, w.Idea, w.Evidence)
					}
				}
			}
		}
	}
	if l.verbose {
		if alive.DeliveryHint != nil {
			l.n.OK("pista de entrega %q (evidencia %q) → viva: P4 la convertirá en fecha.", alive.DeliveryHint.Text, alive.DeliveryHint.Evidence)
		} else {
			l.n.Note("Sin pista de entrega viva: no habrá fecha.")
		}
		l.n.Note("El anclaje tira lo que el literal no respalda SIN tumbar el job: con 0 ideas vivas sale un borrador vacío.")
	}
	if said < len(alive.Wants) {
		return 0
	}
	return said - len(alive.Wants)
}

// p3 especifica cada idea: una llamada por ítem (la etapa ya reintenta a 0,3 y aísla por su cuenta).
func (l *lab) p3(ctx context.Context, job intake.ClaimedJob, literal string, ideas *llm.MainIdeas, res *result) *stages.P3Artifact {
	specs, err := l.stages.P3.Run(ctx, job, literal, ideas.Wants)
	if l.verbose {
		l.n.Step("P3 · la especificación por ítem (stages.P3.Run: una llamada por idea)")
	}
	if err != nil {
		res.obs.Failed = shortErr(err)
		if l.verbose {
			l.n.Fail(err, "P3")
		}
		return nil
	}
	if l.verbose {
		for _, it := range specs.Items {
			l.n.OK("%q · variante %q · evidencia %q", it.Product, it.Variant, it.Evidence)
		}
		for _, iso := range specs.Isolated {
			l.n.Fail(nil, "idea %d aislada: %s (no tumba el job; se pierde ESE ítem)", iso.IdeaPos, iso.Reason)
		}
		if len(ideas.Wants) == 0 {
			l.n.Note("Cero ideas: P3 no llama al modelo y persiste el artefacto vacío.")
		}
	}
	return specs
}

// p4 normaliza cantidades y calcula la fecha en Go, con los reintentos de calidad imitados.
func (l *lab) p4(ctx context.Context, job intake.ClaimedJob, literal string, specs *stages.P3Artifact, hint *llm.Hint, res *result) *llm.Quantities {
	if l.verbose {
		l.n.Step("P4 · cantidades y fecha (stages.P4.Run)")
	}
	var (
		q    *llm.Quantities
		err  error
		raws []string
	)
	for a := 1; a <= l.attempts; a++ {
		res.p4Attempts = a
		_, before := l.spy.Last()
		q, err = l.stages.P4.Run(ctx, job, literal, specs.Items, hint)
		trip, after := l.spy.Last()
		if l.verbose && after > before {
			raws = append(raws, trip.Output)
			l.reportAttempt("P4", a, trip, err, raws)
		}
		if err == nil || !errors.Is(err, llm.ErrLLMQuality) {
			break
		}
	}
	if err != nil {
		res.obs.Failed = shortErr(err)
		if l.verbose {
			l.n.Fail(nil, "P4 agotó sus intentos: el job moriría `failed` por calidad.")
		}
		return nil
	}
	if l.verbose {
		if len(specs.Items) == 0 {
			l.n.Note("Cero ítems: P4 no llama al modelo, pero la fecha la calcula Go igual.")
		}
		for _, it := range q.Items {
			l.n.OK("%q × %d · unidad %q · paquete de %d · rango %s", it.Product, it.Qty, it.UnitKind, it.PackageSize, rangeText(it.Range))
		}
		if proposed := proposedDate(raws); proposed != "" && proposed != q.DeliveryDate {
			l.n.Note("El modelo propuso delivery_date %q; P4 la lee solo para compararla y la descarta (con un Warn).", proposed)
		}
		if q.DeliveryDate != "" {
			l.n.OK("fecha de entrega %s (%s): la calculó stages.ResolveDate en Go, NO el modelo.", q.DeliveryDate, q.DeliveryDateBasis)
		} else {
			l.n.Note("Sin fecha: no hubo pista viva, o Go no reconoció la expresión.")
		}
	}
	return q
}

func (l *lab) match(ctx context.Context, job intake.ClaimedJob, q *llm.Quantities, res *result) *stages.MatchArtifact {
	art, err := l.stages.Match.Run(ctx, job, stages.MatchInput{Quantities: q, Index: l.index, Note: stages.NoOrderNote})
	if l.verbose {
		l.n.Step("Match · el cruce con el catálogo (stages.Match.Run, sin modelo)")
	}
	if err != nil {
		res.obs.Failed = shortErr(err)
		if l.verbose {
			l.n.Fail(err, "match")
		}
		return nil
	}
	if l.verbose {
		for _, ln := range art.Lines {
			l.n.Note("%-9s %-18s %-36q × %d %s", ln.Kind, ln.SKU, ln.Label, ln.Qty, provenance(ln))
		}
		total, pending := art.PartialTotal()
		l.n.Note("Total parcial %.0f · %d línea(s) sin precio (las precifica el dueño).", total, pending)
	}
	return art
}

func (l *lab) draft(ctx context.Context, job intake.ClaimedJob, literal string, m *stages.MatchArtifact, date string, res *result) {
	art, err := l.stages.Draft.Run(ctx, job, stages.DraftInput{Match: m, SourceText: literal, DeliveryDate: date})
	if l.verbose {
		l.n.Step("Draft · el borrador (stages.Draft.Run)")
	}
	if err != nil {
		res.obs.Failed = shortErr(err)
		if l.verbose {
			l.n.Fail(err, "draft")
		}
		return
	}
	_, _ = l.machine.Finish(ctx, job.ID, art.IntakeID)
	if !l.verbose {
		return
	}
	head, _, _ := l.stages.Flow.GetIntakeByEvent(ctx, job.Key.TenantID, job.Key.EventID)
	l.n.OK("Solicitud %s en estado %q · revisión %d con %d línea(s).", art.IntakeID, head.Status, art.RevisionNo, art.Lines)
	l.n.Note("El id de la solicitud sale del evento (UUID v5): reanudar el job no crea otra. El literal va en la revisión,")
	l.n.Note("  por el almacén de solicitudes, que en producción lo cifra (R-06).")
}

func rangeText(r *llm.Range) string {
	if r == nil {
		return "—"
	}
	return fmt.Sprintf("%d–%d %s", r.Min, r.Max, r.Unit)
}

func provenance(ln stages.Line) string {
	if ln.Match == nil {
		return ""
	}
	return fmt.Sprintf("(%s %.2f)", ln.Match.Strategy, ln.Match.Confidence)
}

// shortErr deja un error en una línea, sin el sufijo de la familia de calidad, que se repite.
func shortErr(err error) string {
	return strings.ReplaceAll(err.Error(), llm.ErrLLMQuality.Error()+": ", "")
}

// clip recorta un texto largo para una línea de narración.
func clip(s string, max int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " ⏎ "))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max-1]) + "…"
}

// proposedDate es la delivery_date que el modelo escribió en su última salida de P4 («» si ninguna).
func proposedDate(raws []string) string {
	if len(raws) == 0 {
		return ""
	}
	js, err := llm.ExtractJSON(raws[len(raws)-1])
	if err != nil {
		return ""
	}
	q, err := llm.ParseQuantities(js)
	if err != nil {
		return ""
	}
	return q.DeliveryDate
}
