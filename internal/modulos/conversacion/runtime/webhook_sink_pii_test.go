//go:build pendiente

package runtime

// webhook_sink_pii_test.go — lo que el sink LEE del efecto y lo que NO deja salir. Partido
// de webhook_sink_test.go por tamaño (E-13); usa sus dobles.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// Los literales son raros a propósito, para buscarlos como subcadena sin falsos positivos.
// La nota es una dirección: la PII de manual.
const (
	webhookOrderNote     = "dejarlo en porteria calle Mayor 14 qqz"
	webhookBuyerDocument = "12.345.678-5-qqz"
	webhookVariableValue = "moneda-qqz"
)

// TestWebhookSink_Handle_LifecycleStatusComesFromTheEffect: el estado es un DATO del
// efecto, no una constante. La clave legada `closed` sale `confirmed` (el contrato jamás
// emite `closed`) y cualquier otro estado sale tal cual.
func TestWebhookSink_Handle_LifecycleStatusComesFromTheEffect(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"closed", "confirmed"},
		{"confirmed", "confirmed"},
		{"pending_approval", "pending_approval"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			eff := webhookCartClosed()
			eff.Payload["lifecycle_status"] = c.raw

			_, doc := deliver(t, eff)
			if doc["lifecycle_status"] != c.want {
				t.Errorf("lifecycle_status = %v, quería %q", doc["lifecycle_status"], c.want)
			}
		})
	}
}

// TestWebhookSink_Handle_RevisionNoComesFromTheEffect: la N-ésima revisión viaja como N,
// llegue el número como int (en proceso), float64 (round-trip JSON) o int64. El sink no lo
// corrige, ni lo acota, ni lo sustituye.
func TestWebhookSink_Handle_RevisionNoComesFromTheEffect(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  float64
	}{
		{"int", 4, 4},
		{"float64 from a JSON round trip", float64(4), 4},
		{"int64", int64(4), 4},
		{"first revision", 1, 1},
		{"third revision", 3, 3},
		{"seventeenth revision", 17, 17},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff := webhookCartClosed()
			eff.Payload["revision_no"] = c.value

			_, doc := deliver(t, eff)
			if got, ok := doc["revision_no"]; !ok || got != c.want {
				t.Errorf("revision_no = %#v (presente=%v), quería %v: el puente hace UPSERT por (intake_id, revision_no)", got, ok, c.want)
			}
		})
	}
}

// TestWebhookSink_Handle_MissingRevisionNoIsZeroNeverOne: sin número en el efecto se
// ENCOLA igual, y con 0 —el único valor que el schema rechaza—, nunca con un 1 de respaldo.
func TestWebhookSink_Handle_MissingRevisionNoIsZeroNeverOne(t *testing.T) {
	eff := webhookCartClosed()
	delete(eff.Payload, "revision_no")

	_, doc := deliver(t, eff)
	got, ok := doc["revision_no"]
	if !ok {
		t.Fatal("revision_no desapareció del documento: el schema lo declara requerido y el worker no lo completa")
	}
	if got == float64(1) {
		t.Fatal("sin revision_no el sink emitió un 1: un 1 de respaldo es indistinguible de la primera revisión real")
	}
	if got != float64(0) {
		t.Errorf("revision_no = %#v, quería 0 (el valor que el schema rechaza)", got)
	}
}

// TestWebhookSink_Handle_IntakeIDIsReadNeverInvented: el intake_id es el que el proyector
// anotó en el payload. Si no está, o no es una cadena, viaja vacío y se encola igual: el
// sink no lo inventa ni lo busca.
func TestWebhookSink_Handle_IntakeIDIsReadNeverInvented(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(payload map[string]any)
		want   string
	}{
		{"annotated by the projector", func(p map[string]any) { p["intake_id"] = "intake-late-9" }, "intake-late-9"},
		{"missing", func(p map[string]any) { delete(p, "intake_id") }, ""},
		{"not a string", func(p map[string]any) { p["intake_id"] = 77 }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff := webhookCartClosed()
			c.mutate(eff.Payload)

			_, doc := deliver(t, eff)
			if got, ok := doc["intake_id"]; !ok || got != c.want {
				t.Errorf("intake_id = %#v (presente=%v), quería %q", got, ok, c.want)
			}
		})
	}
}

// TestWebhookSink_Handle_ItemsInBothPayloadShapes: las líneas se leen igual de la forma en
// proceso que de la del round-trip JSON, donde lo que no es un mapa se salta; sin la clave
// o con otra forma, `items` es una lista vacía y no null.
func TestWebhookSink_Handle_ItemsInBothPayloadShapes(t *testing.T) {
	oneItem := `[{"sku":"A1","label":"Café","customization":"","qty":2,"unit_price":9.9}]`
	cases := []struct {
		name  string
		items any
		set   bool
		want  string
	}{
		{"in process", []map[string]any{{"sku": "A1", "label": "Café", "qty": 2, "unit_price": 9.9}}, true, oneItem},
		{
			"JSON round trip, non-map entries skipped",
			[]any{"noise", map[string]any{"sku": "A1", "label": "Café", "qty": float64(2), "unit_price": float64(9.9)}, 7},
			true, oneItem,
		},
		{"key missing", nil, false, `[]`},
		{"nil value", nil, true, `[]`},
		{"unknown shape", "A1 x2", true, `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff := webhookCartClosed()
			delete(eff.Payload, "items")
			if c.set {
				eff.Payload["items"] = c.items
			}

			body, _ := deliver(t, eff)
			var doc struct {
				Items json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(body, &doc); err != nil {
				t.Fatalf("el documento encolado no es JSON válido: %v", err)
			}
			if string(doc.Items) != c.want {
				t.Errorf("items = %s, quería %s", doc.Items, c.want)
			}
		})
	}
}

// TestWebhookSink_Handle_TotalFromAJSONRoundTrip: el total llega igual como float64.
func TestWebhookSink_Handle_TotalFromAJSONRoundTrip(t *testing.T) {
	eff := webhookCartClosed()
	eff.Payload["total"] = float64(19.8)

	_, doc := deliver(t, eff)
	if doc["total"] != 19.8 {
		t.Errorf("total = %#v, quería 19.8", doc["total"])
	}
}

// TestWebhookSink_Handle_OnlyTenantAndContactLeaveTheContext: del EffectContext salen el
// tenant y el contacto opaco; la sesión, el flujo y el evento no viajan, y el documento no
// lleva event_history_id.
func TestWebhookSink_Handle_OnlyTenantAndContactLeaveTheContext(t *testing.T) {
	body, doc := deliver(t, webhookCartClosed())

	if doc["tenant"] != "t-1" || doc["contact"] != "c-opaque" {
		t.Errorf("tenant/contact = %v/%v, quería t-1/c-opaque", doc["tenant"], doc["contact"])
	}
	for _, forbidden := range []string{"session-zzq", "flow-zzq", "event-zzq", `"event_history_id"`} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("el documento encolado contiene %s, que no viaja:\n%s", forbidden, body)
		}
	}
	if len(doc) != 10 {
		t.Errorf("el documento lleva %d claves, quería las diez del contrato: %s", len(doc), body)
	}
}

// piiEffect es el cierre del carrito tal como llega de verdad: con la indicación del
// pedido, los datos del comprador y las variables dentro (el proyector los necesita).
func piiEffect() modules.Effect {
	eff := webhookCartClosed()
	eff.Payload["customer_note"] = webhookOrderNote
	eff.Payload["buyer_data"] = map[string]any{"documento": webhookBuyerDocument}
	eff.Payload["variables"] = map[string]any{"moneda": webhookVariableValue}
	return eff
}

// payloadSnapshot es el payload entero como JSON (claves ordenadas): dos fotos iguales
// son un payload que nadie tocó.
func payloadSnapshot(t *testing.T, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("no se pudo serializar el payload del efecto: %v", err)
	}
	return string(body)
}

// TestWebhookSink_Handle_PIINeverReachesTheOutboxOrTheLog: con la nota, los datos del
// comprador y las variables en el efecto, (a) ni sus textos ni sus claves aparecen en el
// documento que se persiste en webhook_outbox, (b) tampoco en ninguna línea de log, de
// ningún nivel, y (c) el resto del contrato SÍ está — un sink que se tragara el efecto
// entero pasaría (a) y dejaría al puente sin nada que recibir.
func TestWebhookSink_Handle_PIINeverReachesTheOutboxOrTheLog(t *testing.T) {
	queue, log := &webhookQueueFake{}, newSinkLogRecorder()
	sink := NewWebhookSink(log, webhookEffectName, queue, openWebhookGate("t-1"))
	eff := piiEffect()

	if err := sink.Handle(webhookCtx(), webhookContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}
	calls := queue.recorded()
	if len(calls) != 1 {
		t.Fatalf("encolados = %d, quería 1: sin fila que inspeccionar el barrido no prueba nada", len(calls))
	}
	body := string(calls[0].payload)
	logged := log.dump()
	if logged == "" {
		t.Fatal("no se escribió ninguna línea de log: el barrido del log no prueba nada")
	}

	forbidden := []string{
		webhookOrderNote, webhookBuyerDocument, webhookVariableValue,
		`customer_note`, `buyer_data`, `variables`,
	}
	for _, text := range forbidden {
		if strings.Contains(body, text) {
			t.Errorf("FUGA: %q quedó congelado en webhook_outbox.payload; lo completa el worker justo antes del POST:\n%s", text, body)
		}
		if strings.Contains(logged, text) {
			t.Errorf("FUGA: %q salió por el log:\n%s", text, logged)
		}
	}
	for _, line := range []string{"sin azúcar", "Café", "9.9"} {
		if strings.Contains(logged, line) {
			t.Errorf("el log lleva contenido del payload (%q); solo puede llevar tenant, efecto, outbox_id e intake_id:\n%s", line, logged)
		}
	}

	var doc map[string]any
	if err := json.Unmarshal(calls[0].payload, &doc); err != nil {
		t.Fatalf("el documento encolado no es JSON válido: %v", err)
	}
	if doc["intake_id"] != "intake-abc-123" {
		t.Errorf("intake_id = %v: sin él el worker no sabría de qué solicitud leer la nota", doc["intake_id"])
	}
	if doc["total"] != 24.8 {
		t.Errorf("total = %v: la poda se llevó por delante el dinero", doc["total"])
	}
	if items, ok := doc["items"].([]any); !ok || len(items) != 2 {
		t.Errorf("items = %v: la poda se llevó las líneas", doc["items"])
	}
}

// TestWebhookSink_Handle_DoesNotMutateTheSharedPayload: el mapa lo comparten todos los
// sinks del fan-out. Dejar la nota fuera del documento no se hace borrándola del efecto: el
// proyector la necesita para escribir intakes.customer_note.
func TestWebhookSink_Handle_DoesNotMutateTheSharedPayload(t *testing.T) {
	sink := NewWebhookSink(newSinkLogRecorder(), webhookEffectName, &webhookQueueFake{}, openWebhookGate("t-1"))
	eff := piiEffect()
	before := payloadSnapshot(t, eff.Payload)

	if err := sink.Handle(webhookCtx(), webhookContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}

	if after := payloadSnapshot(t, eff.Payload); after != before {
		t.Errorf("el sink MUTÓ el payload compartido\n antes: %s\ndespués: %s", before, after)
	}
	if eff.Payload["customer_note"] != webhookOrderNote {
		t.Errorf("customer_note = %v: el proyector se quedaría sin la nota que tiene que escribir", eff.Payload["customer_note"])
	}
}
