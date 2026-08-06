package runtime

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// discardWebhookLogger es un logger que descarta la salida (white-box, sin depender
// de los helpers del paquete runtime_test).
func discardWebhookLogger() logger.Logger {
	return logger.New(logger.WithWriter(io.Discard))
}

// cartClosedEffect es el efecto de cierre con DOS líneas, una personalizada y otra
// no (D-041.17): el contrato tiene que llevar el «sin azúcar» de la primera y dejar
// la segunda vacía.
func cartClosedEffect() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"items": []map[string]any{
				{"sku": "A1", "label": "Café", "customization": "sin azúcar", "qty": 2, "unit_price": 9.9},
				{"sku": "B2", "label": "Té", "qty": 1, "unit_price": 5.0},
			},
			"total": 24.8,
		},
	}
}

// TestWebhookSink_Handle_NoEntregaNiAborta: el stub no entrega nada por red y
// NUNCA aborta (devuelve nil) para cart_closed y para un efecto de navegación.
func TestWebhookSink_Handle_NoEntregaNiAborta(t *testing.T) {
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed")
	ec := EffectContext{TenantID: "t-1", ContactID: "c-opaco", FlowID: "f-1", FlowVersion: 1}

	if err := sink.Handle(context.Background(), ec, cartClosedEffect()); err != nil {
		t.Fatalf("Handle(cart_closed) no debe fallar: %v", err)
	}
	nav := modules.Effect{Kind: "event", Name: "category_selected", Payload: map[string]any{"category_code": "bebidas"}}
	if err := sink.Handle(context.Background(), ec, nav); err != nil {
		t.Fatalf("Handle(navegación) no debe fallar: %v", err)
	}
}

// TestWebhookSink_Handle_NilSeguro: un sink nil o sin logger no panica.
func TestWebhookSink_Handle_NilSeguro(t *testing.T) {
	var sink *WebhookSink
	if err := sink.Handle(context.Background(), EffectContext{}, cartClosedEffect()); err != nil {
		t.Fatalf("Handle sobre nil no debe fallar: %v", err)
	}
	if err := (&WebhookSink{}).Handle(context.Background(), EffectContext{}, cartClosedEffect()); err != nil {
		t.Fatalf("Handle sin logger no debe fallar: %v", err)
	}
}

// TestBuildCRMOrderPayload_Contrato verifica la FORMA JSON del contrato al CRM
// (§9.I) sin red: tenant, contact opaco, order_id, items[{sku,label,qty,unit_price}],
// total y timestamp determinista.
func TestBuildCRMOrderPayload_Contrato(t *testing.T) {
	ec := EffectContext{TenantID: "tenant-abc", ContactID: "contact-opaco-xyz", FlowID: "f-1", FlowVersion: 3}
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)

	got := buildCRMOrderPayload(ec, cartClosedEffect(), now)

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	want := `{"tenant":"tenant-abc","contact":"contact-opaco-xyz","order_id":"",` +
		`"items":[{"sku":"A1","label":"Café","customization":"sin azúcar","qty":2,"unit_price":9.9},` +
		`{"sku":"B2","label":"Té","customization":"","qty":1,"unit_price":5}],` +
		`"total":24.8,"timestamp":"2026-07-03T10:00:00Z"}`
	if string(body) != want {
		t.Fatalf("payload del CRM no coincide con el contrato §9.I\n got: %s\nwant: %s", body, want)
	}
}

// TestBuildCRMOrderPayload_Personalización es el QUINTO de los cinco caminos de
// T4.1b (D-041.17, REQ-31b): la personalización cruza la frontera hacia el CRM/POS
// —el esbozo del `intake.push` del Plan 042—. Es el camino que más importa del
// invariante INV-12: cuando el pedido lo prepara un sistema de terceros, este JSON
// es TODO lo que la cocina va a ver.
//
// El golden de arriba ya fija la forma entera; esto lo afirma por separado para que
// romperlo diga QUÉ se perdió y no solo "el JSON cambió", y añade lo que el golden
// no puede decir: que el dinero es el mismo.
func TestBuildCRMOrderPayload_Personalización(t *testing.T) {
	got := buildCRMOrderPayload(
		EffectContext{TenantID: "t-1", ContactID: "c-opaco"},
		cartClosedEffect(),
		time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	)

	if len(got.Items) != 2 {
		t.Fatalf("items=%d, quiero 2", len(got.Items))
	}
	if got.Items[0].Customization != "sin azúcar" {
		t.Fatalf("items[0].customization=%q, quiero %q: el CRM no recibe la instrucción de producción",
			got.Items[0].Customization, "sin azúcar")
	}
	if got.Items[1].Customization != "" {
		t.Fatalf("items[1].customization=%q; esa línea no llevaba personalización", got.Items[1].Customization)
	}
	// INV-13: personalizar no cobra, ni en la línea ni en el total del pedido.
	if got.Items[0].UnitPrice != 9.9 || got.Total != 24.8 {
		t.Fatalf("unit_price=%v total=%v; la personalización movió el dinero",
			got.Items[0].UnitPrice, got.Total)
	}
}

// TestBuildCRMOrderPayload_SinPersonalización: un efecto de cierre SIN la clave
// —el que produce hoy el carrito, hasta que T4.1c le dé la tecla 3, y el que
// escribió cualquier conversación anterior a esta tarea— cruza igual, con la
// personalización vacía. Cero regresión: no hay rama especial ni versión de payload
// que mantener.
func TestBuildCRMOrderPayload_SinPersonalización(t *testing.T) {
	eff := modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"items": []map[string]any{{"sku": "A1", "label": "Café", "qty": 1, "unit_price": 9.9}},
			"total": 9.9,
		},
	}
	got := buildCRMOrderPayload(EffectContext{TenantID: "t", ContactID: "c"}, eff, time.Unix(0, 0).UTC())

	if len(got.Items) != 1 || got.Items[0].Customization != "" {
		t.Fatalf("items=%+v; sin la clave, la personalización debe salir vacía", got.Items)
	}
	if got.Items[0].SKU != "A1" || got.Total != 9.9 {
		t.Fatalf("el resto del payload se degradó: %+v", got)
	}
}

// TestBuildCRMOrderPayload_RoundTripJSON: el builder tolera la forma round-trip
// JSON del efecto (items como []any de map, números como float64), igual que el
// PersistSink.
func TestBuildCRMOrderPayload_RoundTripJSON(t *testing.T) {
	eff := modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"items": []any{
				map[string]any{"sku": "A1", "label": "Café", "qty": float64(2), "unit_price": float64(9.9)},
			},
			"total":    float64(19.8),
			"order_id": "ord-123",
		},
	}
	got := buildCRMOrderPayload(EffectContext{TenantID: "t", ContactID: "c"}, eff, time.Unix(0, 0).UTC())

	if len(got.Items) != 1 || got.Items[0].SKU != "A1" || got.Items[0].Qty != 2 || got.Items[0].UnitPrice != 9.9 {
		t.Fatalf("items round-trip mal parseados: %+v", got.Items)
	}
	if got.Total != 19.8 {
		t.Fatalf("total round-trip: got %v want 19.8", got.Total)
	}
	if got.OrderID != "ord-123" {
		t.Fatalf("order_id: got %q want ord-123", got.OrderID)
	}
}
