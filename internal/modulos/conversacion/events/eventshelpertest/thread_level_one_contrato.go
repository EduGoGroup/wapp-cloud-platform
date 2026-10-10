package eventshelpertest

import (
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// El texto de las entradas de NIVEL 1 en el hilo: el render del resumen (events.Summary.Render) o
// nada. Llevó la etiqueta `pendiente` mientras events/summary.go estuvo en rojo.

// levelOneThreadCases son los casos que renderizan un payload de nivel 1.
func levelOneThreadCases() []contractCase {
	return []contractCase{
		{"ListThread_SummaryEntry_RendersAsTheClientReadIt", caseThreadSummary},
		{"ListThread_DecisionEntry_EmptyText", caseThreadDecision},
	}
}

// caseThreadSummary: un resumen se entrega RENDERIZADO —el mismo texto que el cliente leyó al
// reanudar—, no como su JSON; con su rol system y su grado summary, que es lo que permite a quien
// lee tratarlo como contexto. Un resumen vacío (sin líneas ni respuestas) da texto vacío.
func caseThreadSummary(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	summary := events.BuildCartSummary(events.CartState{Level: "continue", Lines: []events.SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
		{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2},
	}})
	body, err := summary.Encode()
	requireNoError(t, "Summary.Encode", err)
	ctx := t.Context()

	mustMessage(t, m, ev.ID, events.RoleClient, clientText)
	_, err = m.Store.AppendSummary(ctx, ev.ID, body)
	requireNoError(t, "AppendSummary", err)
	_, err = m.Store.AppendSummary(ctx, ev.ID, json.RawMessage(`{"kind":"menu"}`))
	requireNoError(t, "AppendSummary vacío", err)

	want := "Esto es lo que ya habías decidido en tu pedido:\n" +
		"Café x2  $5.00\n" +
		"   ✏️ sin azúcar\n" +
		"Té x1  $2.00\n" +
		"TOTAL  $7.00\n" +
		"Te quedaste decidiendo si agregar algo más."
	if want != summary.Render() {
		t.Fatalf("el render del resumen cambió:\n%s\n--- quería ---\n%s", summary.Render(), want)
	}
	requireThread(t, "ListThread", mustThread(t, m, ev.ID, 10), []events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: clientText},
		{Seq: 2, Role: events.RoleSystem, Kind: events.KindSummary, Text: want},
		{Seq: 3, Role: events.RoleSystem, Kind: events.KindSummary, Text: ""},
	})
}

// caseThreadDecision: una decisión es estructura, no prosa: sale con su rol client y su grado
// decision y con el texto VACÍO, aunque su payload traiga claves que un resumen también usa.
func caseThreadDecision(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	ctx := t.Context()
	requireNoError(t, "AppendDecision", m.Store.AppendDecision(ctx, ev.ID, []byte(decisionBody)))
	requireNoError(t, "AppendDecision", m.Store.AppendDecision(ctx, ev.ID, []byte(`{"kind":"cart","sku":"CAFE"}`)))

	requireThread(t, "ListThread", mustThread(t, m, ev.ID, 10), []events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindDecision, Text: ""},
		{Seq: 2, Role: events.RoleClient, Kind: events.KindDecision, Text: ""},
	})
}
