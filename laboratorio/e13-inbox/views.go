//go:build laboratorio

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// views.go — cómo enseña el escalón lo que hay en la bandeja: la solicitud, sus revisiones, lo que
// le llegó al cliente y la telemetría. Nada de aquí cambia estado.

// showIntake pinta una solicitud como la ve la dueña: estado, total, líneas persistidas
// (intake_items) y el rastro de revisiones (intake_revisions).
func (l *lab) showIntake(d intakes.Detail) {
	l.n.Note("Solicitud %s · estado %q · total %.2f · %d línea(s) en intake_items.", short(d.ID), d.Status, d.Total, len(d.Items))
	for _, it := range d.Items {
		custom := ""
		if it.Customization != "" {
			custom = fmt.Sprintf(" · «%s»", it.Customization)
		}
		l.n.Note("  item  %-10s %-34q × %d · %.2f%s", it.SKU, it.Label, it.Qty, it.UnitPrice, custom)
	}
	for _, rev := range d.Revisions {
		extra := ""
		if rev.RenderedText != "" {
			extra = fmt.Sprintf(" · rendered_text de %d runas", len([]rune(rev.RenderedText)))
		}
		l.n.Note("  revisión %d · %-11s · created_by %-6s · %d bytes de payload%s", rev.RevisionNo, rev.Kind, rev.CreatedBy, len(rev.Payload), extra)
	}
	if pending := intakes.PendingPriceLines(d.Revisions); len(pending) > 0 {
		names := make([]string, 0, len(pending))
		for _, p := range pending {
			names = append(names, fmt.Sprintf("%d:%s", p.Index, p.Label))
		}
		l.n.Note("  La revisión VIGENTE tiene %d línea(s) sin precio: %s.", len(pending), strings.Join(names, ", "))
	}
	if allowed := intakes.AllowedTransitions(d.Status); len(allowed) > 0 {
		l.n.Note("  Desde %q la máquina de estados deja ir a: %s.", d.Status, strings.Join(allowed, ", "))
	} else {
		l.n.Note("  %q es TERMINAL: no transiciona a ninguna parte.", d.Status)
	}
}

// interpretedLine es lo que esta vista lee de cada línea del payload §7.4 del borrador.
type interpretedLine struct {
	Kind      string   `json:"kind"`
	SKU       string   `json:"sku"`
	Label     string   `json:"label"`
	Qty       int      `json:"qty"`
	UnitPrice *float64 `json:"unit_price"`
}

// showInterpreted enseña las líneas y las preguntas sugeridas de una revisión `interpreted`: es
// el borrador que dejó el pipeline, y vive en la REVISIÓN, no en intake_items.
func (l *lab) showInterpreted(rev intakes.Revision) {
	var p struct {
		DeliveryDate       string            `json:"delivery_date"`
		Lines              []interpretedLine `json:"lines"`
		SuggestedQuestions []string          `json:"suggested_questions"`
	}
	if err := json.Unmarshal(rev.Payload, &p); err != nil {
		l.n.Fail(err, "leyendo el payload de la revisión %d", rev.RevisionNo)
		return
	}
	l.n.Note("El borrador (revisión %d, %s): entrega %q.", rev.RevisionNo, rev.Kind, p.DeliveryDate)
	for i, ln := range p.Lines {
		price := "null ← la dueña tiene que ponerle precio"
		if ln.UnitPrice != nil {
			price = fmt.Sprintf("%.2f", *ln.UnitPrice)
		}
		l.n.Note("  línea %d · %-9s %-11s %-22q × %d · unit_price %s", i, ln.Kind, ln.SKU, ln.Label, ln.Qty, price)
	}
	for _, q := range p.SuggestedQuestions {
		l.n.Note("  pregunta sugerida: «%s»", q)
	}
	if len(p.SuggestedQuestions) == 0 {
		l.n.Note("  (sin preguntas sugeridas: el match no dejó dudas que preguntar)")
	}
}

// showSends enseña lo que el Edge habría mandado al cliente desde el envío nº from.
func (l *lab) showSends(from int, what string) int {
	sends := l.box.Sender.Sends()
	if len(sends) == from {
		l.n.Note("📵 Al cliente NO le llegó nada (%s).", what)
		return from
	}
	for _, s := range sends[from:] {
		l.n.Show(fmt.Sprintf("📱 Al cliente, por la sesión %s, al destino %s (%s)", s.SessionID, s.To, s.CommandID), s.Text)
	}
	return len(sends)
}

// showMetrics enseña las filas de flow_events que nacieron desde la nº from.
func (l *lab) showMetrics(from int) int {
	evs := l.box.Stages.Flow.FlowEvents()
	for _, ev := range evs[from:] {
		l.n.Note("📊 flow_events: %s %q · %v", ev.Kind, ev.Name, ev.Payload)
	}
	return len(evs)
}

// explain pinta un error del dominio con su TIPO, que es lo que la cara HTTP traduce a un código.
func (l *lab) explain(err error, what string) {
	var (
		pending  *intakes.PendingPriceError
		notApp   *intakes.NotApprovableError
		notEdit  *intakes.NotEditableError
		trans    *intakes.TransitionError
		invalid  *intakes.InvalidItemsError
		kindName string
	)
	switch {
	case errors.As(err, &pending):
		kindName = "*intakes.PendingPriceError (422)"
	case errors.As(err, &notApp):
		kindName = "*intakes.NotApprovableError (422)"
	case errors.As(err, &notEdit):
		kindName = "*intakes.NotEditableError (422)"
	case errors.As(err, &trans):
		kindName = "*intakes.TransitionError (422)"
	case errors.As(err, &invalid):
		kindName = "*intakes.InvalidItemsError (400)"
	case errors.Is(err, intakes.ErrEmptyQuoteText), errors.Is(err, intakes.ErrEmptyQuestion):
		kindName = "error centinela (400)"
	case errors.Is(err, intakes.ErrEmptyQuote):
		kindName = "intakes.ErrEmptyQuote (422)"
	default:
		kindName = fmt.Sprintf("%T", err)
	}
	l.n.Fail(err, "%s → %s", what, kindName)
}

// short recorta un UUID a sus 8 primeros caracteres, para leer.
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// mustGet lee la solicitud por el servicio (es un TOQUE de los recordatorios perezosos, que aquí no
// están cableados) y la pinta.
func (l *lab) mustGet(id string) intakes.Detail {
	d, err := l.box.Service.Get(l.ctx, common.LabTenant, id)
	if err != nil {
		l.n.Fail(err, "Get(%s)", short(id))
		return intakes.Detail{}
	}
	return d
}
