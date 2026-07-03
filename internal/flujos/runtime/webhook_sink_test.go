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

func cartClosedEffect() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: effCartClosed,
		Payload: map[string]any{
			"items": []map[string]any{
				{"sku": "A1", "label": "Café", "qty": 2, "unit_price": 9.9},
				{"sku": "B2", "label": "Té", "qty": 1, "unit_price": 5.0},
			},
			"total": 24.8,
		},
	}
}

// TestWebhookSink_Handle_NoEntregaNiAborta: el stub no entrega nada por red y
// NUNCA aborta (devuelve nil) para cart_closed y para un efecto de navegación.
func TestWebhookSink_Handle_NoEntregaNiAborta(t *testing.T) {
	sink := NewWebhookSink(discardWebhookLogger())
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
		`"items":[{"sku":"A1","label":"Café","qty":2,"unit_price":9.9},` +
		`{"sku":"B2","label":"Té","qty":1,"unit_price":5}],` +
		`"total":24.8,"timestamp":"2026-07-03T10:00:00Z"}`
	if string(body) != want {
		t.Fatalf("payload del CRM no coincide con el contrato §9.I\n got: %s\nwant: %s", body, want)
	}
}

// TestBuildCRMOrderPayload_RoundTripJSON: el builder tolera la forma round-trip
// JSON del efecto (items como []any de map, números como float64), igual que el
// PersistSink.
func TestBuildCRMOrderPayload_RoundTripJSON(t *testing.T) {
	eff := modules.Effect{
		Kind: "persist",
		Name: effCartClosed,
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
