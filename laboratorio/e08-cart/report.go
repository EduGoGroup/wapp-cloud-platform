//go:build laboratorio

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/bench"
	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

func contextBackground() context.Context { return context.Background() }

// showCart narra el estado de la conversación y lo devuelve.
func showCart(n *common.Narrator, b *bench.Bench) model.Conversation {
	st, _ := b.ShowState(n, bench.Phone)
	return st
}

// outcomes explica cada desenlace del QueryObserver (engine/consulta.go).
var outcomes = map[string]string{
	engine.QueryOutcomeResolved:     "el modelo dio un valor válido y el carrito lo aplicó",
	engine.QueryOutcomeInconclusive: "el modelo contestó a tiempo pero no sirvió (usable:false, o fuera de rango, o JSON roto): el carrito repregunta",
	engine.QueryOutcomeFailure:      "la vía falló (el Edge no contestó, plazo agotado…): el carrito repregunta y es una DEGRADACIÓN",
	engine.QueryOutcomeNoResolver:   "no hay resolutor cableado en el engine: el carrito repregunta",
}

// showQuery narra la consulta del turno: lo que publicó el engine y lo que viajó por el cable.
func showQuery(n *common.Narrator, b *bench.Bench, spy *common.SpyFrame) {
	queries := b.Queries()
	if len(queries) == 0 {
		n.Note("El engine NO publicó ninguna consulta en este turno.")
	}
	for _, q := range queries {
		n.Show("consulta del engine", fmt.Sprintf("clase %s · nivel %s · desenlace %s", q.Class, q.Level, q.Outcome))
		if why, ok := outcomes[q.Outcome]; ok {
			n.Note("«%s»: %s.", q.Outcome, why)
		}
	}
	trip, calls := spy.Last()
	if calls == 0 || trip.Stage != common.StageTurn {
		n.Note("No viajó ninguna inferencia de turno (llamadas al frame: %d).", calls)
		return
	}
	req := trip.Request
	n.Note("Viajó por el cable: etapa %q · class %q · techo %d tokens · temperatura %.1f · plazo %s · prompt de %d bytes.",
		trip.Stage, req.Class, req.MaxOutputTokens, req.Temperature, req.Timeout, len(req.Prompt))
	n.Note("El format es el JSON Schema del turno (usable, value, reason): %d bytes que el Edge pasaría tal cual a Ollama.", len(req.Format))
	if i := strings.LastIndex(req.Prompt, "Ahora resolvé este caso:"); i >= 0 {
		n.Show("el caso, al final del prompt", strings.TrimSpace(req.Prompt[i:]))
	}
	if trip.Err != nil {
		n.Fail(trip.Err, "el frame devolvió error")
	} else {
		n.Raw("salida cruda del modelo", trip.Output)
	}
	n.Note("Tardó %s de pared.", trip.Duration.Round(1e6))
	if f, ok := spy.Inner().(*common.OllamaFrame); ok {
		n.Note("Desglose de Ollama: %s.", f.Last())
	}
	n.Note("Lo que el modelo diga no se cree a ciegas: turnoacotado valida en Go la forma y el rango (1..9999) y el carrito")
	n.Note("vuelve a admitirlo (admissibleCode). El «reason» del modelo no se mira nunca.")
}

// showIntakes narra las solicitudes del gemelo de flujos y sus líneas.
func showIntakes(n *common.Narrator, b *bench.Bench) {
	ins := b.Repo.Intakes()
	if len(ins) == 0 {
		n.Show("intakes", "(ninguna)")
	}
	for _, in := range ins {
		n.Show("intakes", fmt.Sprintf("%s · total %v · evento %s", in.Status, in.Total, cut(in.EventID)))
		for _, it := range b.Repo.IntakeItems(in.ID) {
			n.Show("   línea", fmt.Sprintf("%s · %s · x%d · %v", it.SKU, it.Label, it.Qty, it.UnitPrice))
		}
	}
}

// showClose narra lo que queda tras confirmar: evento, flow_events, solicitud, revisión, envío,
// hilo y ventana.
func showClose(n *common.Narrator, b *bench.Bench) {
	for _, ev := range b.Events.Events(common.LabTenant) {
		n.Show("conversation_events", fmt.Sprintf("%s · %s · flujo %s · history_id %s", ev.Kind, ev.Status, ev.FlowID, ev.HistoryID))
	}
	n.Show("flow_events", strings.Join(b.FlowEventNames(), ", "))
	for _, fe := range b.Repo.FlowEvents() {
		if fe.Name == "cart_closed" {
			n.Show("   payload de cart_closed", fe.Payload)
		}
	}
	showIntakes(n, b)
	for _, in := range b.Repo.Intakes() {
		for _, rev := range b.Intakes.Revisions(in.ID) {
			n.Show("   revisión", fmt.Sprintf("%d · %s · %s · %s", rev.RevisionNo, rev.Kind, rev.CreatedBy, string(rev.Payload)))
		}
	}
	if len(b.Shipping.Calls) > 0 {
		n.Show("garante de envío (doble)", strings.Join(b.Shipping.Calls, ", "))
		n.Note("El garante es un DOBLE: los dos gemelos en memoria no comparten la tabla intakes (ver bench.ShippingRecorder).")
	}
	threadSummary(n, b)
	for _, j := range b.Jobs.Jobs() {
		n.Show("intake_jobs", fmt.Sprintf("%s · %d referencias: %s", j.Status, len(j.SourceRefs), strings.Join(j.SourceRefs, ",")))
	}
}

// threadSummary cuenta el hilo del evento por rol y clase, y cuántas entradas van selladas.
func threadSummary(n *common.Narrator, b *bench.Bench) {
	for _, ev := range b.Events.Events(common.LabTenant) {
		entries := b.Events.Entries(ev.ID)
		sealed, decisions := 0, 0
		var order []string
		for _, e := range entries {
			if e.Sealed {
				sealed++
			}
			if e.Kind == "decision" {
				decisions++
				n.Show("   decisión en claro", string(e.Payload))
			}
			order = append(order, string(e.Role)+":"+string(e.Kind))
		}
		n.Show("hilo del evento", fmt.Sprintf("%d entradas · %d selladas (cifradas) · %d decisiones en claro", len(entries), sealed, decisions))
		n.Show("   en orden", strings.Join(order, ","))
	}
}

func cut(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	if id == "" {
		return "(sin evento)"
	}
	return id
}
