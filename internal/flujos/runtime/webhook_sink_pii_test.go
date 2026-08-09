// webhook_sink_pii_test.go es la prueba de que la indicación del PEDIDO no llega
// a la cola de entregas (webhook_outbox.payload).
//
// Es el TERCER hermano de buyer_data_leak_test.go y customer_note_leak_test.go, y
// existe porque los dos anteriores tampoco bastaron. El arreglo del defecto A2 del
// Plan 041 sacó la nota de public.flow_events podándola por clave, y su test lo
// acreditó — pero solo miraba flow_events. La misma nota seguía entrando entera en
// public.webhook_outbox por la plantilla congelada del puente CRM, una tabla que
// además NO SE PODA NUNCA: la fila entregada se queda con su payload para siempre.
// Cerrar una puerta y no mirar la de al lado es exactamente cómo esta fuga lleva
// tres olas viva.
//
// La diferencia con flow_events es que aquí la nota SÍ tiene que llegar al puente
// (el contrato wapp-crm-v1 la exige): lo que se comprueba no es que desaparezca,
// sino que no se PERSISTE — la completa el worker en memoria justo antes del POST
// (ver TestWorker_CompletaCustomerNoteSinPersistirla, internal/integrations).
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// El literal es raro a propósito, para buscarlo como subcadena sin falsos
// positivos. Es una dirección: la PII de manual.
const notaDelPedidoEnElOutbox = "dejarlo en porteria calle Mayor 14 qqz"

// TestWebhookSink_LaNotaDelPedidoNoSeEncola encola un cierre de carrito CON
// indicación del cliente por el sink real y comprueba las DOS mitades:
//
//	(a) el literal de la nota NO aparece en el payload que se persiste;
//	(b) el resto del contrato sí está — intake_id, líneas y total —, porque un
//	    sink que se tragara el efecto entero pasaría la (a) y habría dejado al
//	    puente sin nada que recibir.
func TestWebhookSink_LaNotaDelPedidoNoSeEncola(t *testing.T) {
	gate := &fakeGate{open: map[string]bool{"t-1": true}}
	q := &fakeQueuer{}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	eff := cartClosedEffect()
	eff.Payload["customer_note"] = notaDelPedidoEnElOutbox

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1", ContactID: "c-opaco"}, eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(q.calls) != 1 {
		t.Fatalf("encolados=%d, esperaba 1: sin fila que inspeccionar el barrido no probaría nada", len(q.calls))
	}
	encolado := string(q.calls[0].payload)

	// (a) La nota no puede estar en lo que se escribe en la tabla.
	if strings.Contains(encolado, notaDelPedidoEnElOutbox) {
		t.Fatalf("FUGA: la indicación del cliente acabó CONGELADA en webhook_outbox.payload "+
			"(cola sin poda: la fila sobrevive a la entrega con el texto en claro). "+
			"Tiene que completarla el worker justo antes del POST, como buyer_data:\n%s", encolado)
	}
	if strings.Contains(encolado, `"customer_note"`) {
		t.Fatalf("la clave customer_note sigue en la plantilla persistida "+
			"(hoy vacía, mañana con la nota de vuelta):\n%s", encolado)
	}

	// (b) …pero lo que el puente necesita para correlacionar y cobrar sí está.
	var got map[string]any
	if err := json.Unmarshal(q.calls[0].payload, &got); err != nil {
		t.Fatalf("el payload encolado no es JSON válido: %v", err)
	}
	if got["intake_id"] != "intake-abc-123" {
		t.Fatalf("intake_id=%v: sin él el worker no sabría de qué solicitud leer la nota", got["intake_id"])
	}
	if got["total"] != 24.8 {
		t.Fatalf("total=%v: la poda se llevó por delante el dinero", got["total"])
	}
	if items, ok := got["items"].([]any); !ok || len(items) != 2 {
		t.Fatalf("items=%v: la poda se llevó las líneas", got["items"])
	}
}

// TestWebhookSink_NoMutaElPayloadCompartido: el mapa de eff.Payload lo comparten
// TODOS los sinks del fan-out (es el mismo puntero, ver dispatch en resume.go).
// Sacar la nota de la plantilla NO puede hacerse borrándola del efecto: el
// proyector del carrito corre en otra fase y la necesita para escribir
// intakes.customer_note, que es de donde el worker la va a releer. Fue un cuidado
// explícito del arreglo de A2 y aquí vale igual.
func TestWebhookSink_NoMutaElPayloadCompartido(t *testing.T) {
	gate := &fakeGate{open: map[string]bool{"t-1": true}}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", &fakeQueuer{}, gate)

	eff := cartClosedEffect()
	eff.Payload["customer_note"] = notaDelPedidoEnElOutbox

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1", ContactID: "c-opaco"}, eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if eff.Payload["customer_note"] != notaDelPedidoEnElOutbox {
		t.Fatalf("el sink MUTÓ el payload compartido (customer_note=%v): el proyector se quedaría "+
			"sin la nota que tiene que escribir en intakes.customer_note", eff.Payload["customer_note"])
	}
}

// TestBuildIntakePushTemplate_IgnoraLaNotaDelEfecto aísla la regla del builder del
// camino del sink: aunque el efecto traiga la nota (y la trae: el proyector la
// necesita), la plantilla no la mira.
func TestBuildIntakePushTemplate_IgnoraLaNotaDelEfecto(t *testing.T) {
	eff := cartClosedEffect()
	eff.Payload["customer_note"] = notaDelPedidoEnElOutbox

	body, err := json.Marshal(buildIntakePushTemplate(
		EffectContext{TenantID: "t-1", ContactID: "c-opaco"}, eff, time.Unix(0, 0).UTC()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), notaDelPedidoEnElOutbox) {
		t.Fatalf("FUGA: buildIntakePushTemplate volvió a copiar la nota del efecto a la plantilla:\n%s", body)
	}
}
