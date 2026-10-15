//go:build laboratorio

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// quote.go — P5: la sugerencia de cotización (quotetext.Service).

// quoteTimeout es el plazo de LA llamada a P5, el mismo que pone el arranque (quoteCallTimeout =
// pipeline.CallTimeoutFloor, internal/arranque/fase5_captacion.go).
const quoteTimeout = pipeline.CallTimeoutFloor

// styleSeed es la semilla de estilo del tenant (tenant_content, ref quote_style_examples): dos
// cotizaciones que la dueña escribió a mano para que el modelo imite su voz. Hoy ningún tenant real
// la tiene escrita (fase5_captacion.go); aquí se pone para que P5 tenga ejemplos.
var styleSeed = []string{
	"¡Hola, Marta! 🎂 Te cuento: torta de frutilla de 12 porciones $2400. Total $2400. Si te parece, te la reservo para el sábado.",
	"¡Hola! Para tu pedido: pan de masa madre $120 y pan integral $130. Total $250. ¡Gracias por elegirnos!",
}

// seedReader es el doble del repositorio de contenido del tenant: solo sabe devolver la semilla.
type seedReader struct{ examples []string }

func (s seedReader) GetTenantContent(_ context.Context, _ string, ref string) ([]byte, error) {
	if ref != quotetext.SeedStyleRef || len(s.examples) == 0 {
		return nil, errors.New("tenant_content: no existe")
	}
	return json.Marshal(map[string]any{"examples": s.examples})
}

// newQuoteService arma el generador como el arranque: lector de solicitudes y del historial = el
// MISMO store de la bandeja, el MISMO selector que las etapas, el plazo de P5 y, opcional, la semilla.
func (l *lab) newQuoteService(seed []string) (*quotetext.Service, error) {
	opts := []quotetext.Option{quotetext.WithTimeout(quoteTimeout)}
	if seed != nil {
		opts = append(opts, quotetext.WithSeed(seedReader{examples: seed}))
	}
	return quotetext.NewService(l.box.Log, l.box.Intakes, l.box.Intakes, l.box.Selector, opts...)
}

// suggestWithoutExamples: sin historial aprobado ni semilla, NO se llama al modelo.
func (l *lab) suggestWithoutExamples(id string) error {
	l.n.Step("Sugerir SIN ejemplos: el modelo ni se entera")
	svc, err := l.newQuoteService(nil)
	if err != nil {
		l.n.Fail(err, "quotetext.NewService")
		return err
	}
	det, _ := l.box.Service.Get(l.ctx, common.LabTenant, id)
	draft := quotetext.DraftOf(det.Items)
	l.n.Note("DraftOf(líneas): %d línea(s), total %.2f. ExpectedSequence (lo que un texto DEBE decir, en orden): %v.",
		len(draft.Lines), draft.Total, quotetext.ExpectedSequence(draft))
	_, before := l.spy.Last()
	s, err := svc.Suggest(l.ctx, common.LabTenant, id)
	if err != nil {
		l.n.Fail(err, "Suggest")
		return err
	}
	_, after := l.spy.Last()
	l.n.OK("Source %q · Reason %q · inferencias por el frame: %d.", s.Source, s.Reason, after-before)
	l.n.Show("el texto determinista (quotetext.Render)", s.Text)
	l.n.Note("Sin ejemplos no hay voz que imitar: Suggest ni pide el provider al selector (paso 5 del contrato).")
	l.n.Note("Y no escribe nada: ni revisión, ni estado, ni mensaje. Lo que devuelve, la dueña lo edita y lo aprueba (E13).")
	return nil
}

// suggestWithExamples: con la semilla de estilo, P5 de verdad, varias veces, midiendo.
func (l *lab) suggestWithExamples(id string) error {
	l.n.Step(fmt.Sprintf("Sugerir CON ejemplos: P5 de verdad (%d vez/veces)", l.runs))
	svc, err := l.newQuoteService(styleSeed)
	if err != nil {
		l.n.Fail(err, "quotetext.NewService")
		return err
	}
	for i, ex := range styleSeed {
		l.n.Note("Ejemplo semilla %d: «%s»", i+1, ex)
	}
	det, _ := l.box.Service.Get(l.ctx, common.LabTenant, id)
	draft := quotetext.DraftOf(det.Items)
	var walls []time.Duration
	sources := map[string]int{}
	for i := 1; i <= l.runs; i++ {
		start := time.Now()
		s, err := svc.Suggest(l.ctx, common.LabTenant, id)
		wall := time.Since(start)
		if err != nil {
			l.n.Fail(err, "Suggest %d", i)
			return err
		}
		walls = append(walls, wall)
		key := s.Source
		if s.Reason != "" {
			key += " (" + s.Reason + ")"
		}
		sources[key]++
		trip, _ := l.spy.Last()
		if i == 1 {
			l.n.Excerpt("el prompt de P5 que salió por el cable", trip.Request.Prompt, 60)
			l.n.Note("Petición: techo %d tokens · temperatura %.1f · format %q · plazo %s.",
				trip.Request.MaxOutputTokens, trip.Request.Temperature, short(trip.Request.Format, 40), trip.Request.Timeout.Round(time.Millisecond))
		}
		l.n.Note("— Sugerencia %d · %s de pared%s", i, wall.Round(time.Millisecond), l.measure())
		l.n.Raw("salida cruda del modelo", trip.Output)
		l.verify(draft, trip.Output)
		if s.Source == quotetext.SourceLLM {
			l.n.OK("Source llm: el texto del modelo pasa el verificador y es lo que la dueña verá para editar.")
		} else {
			l.n.Fail(nil, "Source %s · Reason %s: la dueña verá el DETERMINISTA, no el del modelo", s.Source, s.Reason)
		}
		l.n.Show("lo que devuelve Suggest", s.Text)
	}
	l.n.Note("Resumen de %d P5: %s · pared %s.", l.runs, tally(sources), timing(walls))
	return nil
}

// verify repite, a la vista, lo que hace Suggest con la salida: ExtractJSON → ParseQuoteText →
// Verify. Así se ve el MOTIVO, que Suggest solo deja en el log.
func (l *lab) verify(draft quotetext.Draft, raw string) {
	js, err := llm.ExtractJSON(raw)
	if err != nil {
		l.n.Fail(err, "ExtractJSON")
		return
	}
	art, err := llm.ParseQuoteText(js)
	if err != nil {
		l.n.Fail(err, "ParseQuoteText (salida_no_es_artefacto)")
		return
	}
	v := quotetext.Verify(draft, art.Text)
	if v.OK {
		l.n.OK("Verify (INV-2): los importes marcados son EXACTAMENTE la secuencia esperada.")
		return
	}
	l.n.Fail(nil, "Verify (INV-2): %s · %s", v.Reason, v.Detail)
}

// measure da el desglose de Ollama de la última llamada, si P5 fue al modelo.
func (l *lab) measure() string {
	if m := l.model(); m != nil {
		return " · " + m.Last().String()
	}
	return " · guion"
}

func tally(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func timing(ws []time.Duration) string {
	if len(ws) == 0 {
		return "—"
	}
	lo, hi, sum := ws[0], ws[0], time.Duration(0)
	for _, w := range ws {
		lo, hi, sum = min(lo, w), max(hi, w), sum+w
	}
	return fmt.Sprintf("mín %s · media %s · máx %s", lo.Round(time.Millisecond), (sum / time.Duration(len(ws))).Round(time.Millisecond), hi.Round(time.Millisecond))
}

func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// pendingAfter dice si una sugerencia choca con las líneas sin precio de la revisión vigente.
func pendingAfter(err error) (*intakes.PendingPriceError, bool) {
	var p *intakes.PendingPriceError
	return p, errors.As(err, &p)
}
