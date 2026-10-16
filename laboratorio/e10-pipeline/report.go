//go:build laboratorio

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// verdict compara un caso con el banco y lo narra.
func (l *lab) verdict(p common.Phrase, res result) {
	l.n.Step("El veredicto contra el banco E03")
	want, _ := common.ResolveExpectedDate(p.DateToken(), l.base)
	l.n.Note("Esperado: intención %q · líneas %s · sin casar %d · fecha %s (%q).", p.Intent, expectedLines(p), p.Unmatched, orDash(want), p.DateToken())
	v := common.Compare(p, res.obs, l.base)
	if res.obs.Failed != "" {
		l.n.Fail(nil, "la cadena se cortó: %s", res.obs.Failed)
	}
	l.n.Note("intención %s · líneas %s · fecha %s · %s de pared.", common.Mark(v.Intent), common.Mark(v.Lines), common.Mark(v.Date), res.duration.Round(time.Millisecond))
	for _, note := range v.Notes {
		l.n.Note("  %s", note)
	}
	if v.OK() {
		l.n.OK("ACIERTA en los tres ejes.")
	}
}

// runAll pasa el banco entero y saca la tabla de aciertos.
func (l *lab) runAll(phrases []common.Phrase) error {
	l.n.Step(fmt.Sprintf("El banco entero: %d casos", len(phrases)))
	type row struct {
		p   common.Phrase
		res result
		v   common.Verdict
	}
	rows := make([]row, 0, len(phrases))
	for _, p := range phrases {
		res := l.runPhrase(context.Background(), p)
		v := common.Compare(p, res.obs, l.base)
		rows = append(rows, row{p: p, res: res, v: v})
		l.n.Note("%-26s %s %s %s · P2 %s · %s", p.ID, common.Mark(v.Intent), common.Mark(v.Lines), common.Mark(v.Date),
			p2Summary(res), res.duration.Round(time.Millisecond))
	}

	l.n.Step("La tabla de aciertos")
	l.n.Note("%-26s %-14s %-30s %-4s %-4s %-7s %s", "caso", "categoría", "intención (esperada→salió)", "lín", "fec", "P2", "por qué falla")
	var intents, lines, dates, all, p2ok int
	for _, r := range rows {
		if r.v.Intent {
			intents++
		}
		if r.v.Lines {
			lines++
		}
		if r.v.Date {
			dates++
		}
		if r.v.OK() {
			all++
		}
		if r.res.p2Attempts > 0 && !strings.HasPrefix(r.res.obs.Failed, "p2:") {
			p2ok++
		}
		intent := fmt.Sprintf("%s→%s %.2f", abbrev(r.p.Intent), abbrev(r.res.obs.Intent), r.res.obs.Confidence)
		why := strings.Join(r.v.Notes, "; ")
		if r.res.obs.Failed != "" {
			why = "CORTE: " + r.res.obs.Failed + "; " + why
		}
		l.n.Note("%-26s %-14s %s %-28s %-4s %-4s %-7s %s", r.p.ID, r.p.Category, common.Mark(r.v.Intent), intent,
			common.Mark(r.v.Lines), common.Mark(r.v.Date), p2Summary(r.res), why)
	}
	total := len(rows)
	l.n.Step("El resumen")
	l.n.Note("Intención %d/%d · líneas %d/%d · fecha %d/%d · los tres ejes %d/%d · P2 pasó el control de calidad en %d/%d.",
		intents, total, lines, total, dates, total, all, total, p2ok, total)
	if f, ok := l.spy.Inner().(*common.ScriptFrame); ok {
		l.n.Note("Con -frame script el «modelo» contesta SIEMPRE el caso Ámbar: lo que no es Ámbar lo tira el anclaje (sus")
		l.n.Note("  evidencias no están en el literal) y sale un borrador vacío. Es el anclaje funcionando, no el banco fallando.")
		if probs := f.Problems(); len(probs) > 0 {
			l.n.Note("El guion no supo atender %d llamada(s); la primera: %s", len(probs), probs[0])
		}
	}
	return nil
}

func p2Summary(res result) string {
	switch {
	case res.p2Attempts == 0:
		return "—"
	case strings.HasPrefix(res.obs.Failed, "p2:"):
		return fmt.Sprintf("✗×%d", res.p2Attempts)
	case res.dropped > 0:
		return fmt.Sprintf("✓ −%d", res.dropped)
	}
	return "✓"
}

func expectedLines(p common.Phrase) string {
	if len(p.Lines) == 0 {
		return "ninguna"
	}
	parts := make([]string, 0, len(p.Lines))
	for _, ln := range p.Lines {
		q := fmt.Sprint(ln.Qty)
		if ln.Qty == 0 {
			q = "?"
		}
		parts = append(parts, ln.SKU+"×"+q)
	}
	return strings.Join(parts, ", ")
}

func abbrev(intent string) string {
	switch intent {
	case "":
		return "—"
	case common.IntentIntakeRequest:
		return "intake"
	case common.IntentOpeningHours:
		return "horario"
	}
	return intent
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
