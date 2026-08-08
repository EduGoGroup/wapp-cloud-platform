package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
// la segunda vacía. intake_id ya está presente porque cart.Projector.closeIntake
// lo anota en el mapa ANTES de que el fan-out llegue al WebhookSink (ver el
// comentario de buildIntakePushTemplate).
func cartClosedEffect() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"intake_id": "intake-abc-123",
			"items": []map[string]any{
				{"sku": "A1", "label": "Café", "customization": "sin azúcar", "qty": 2, "unit_price": 9.9},
				{"sku": "B2", "label": "Té", "qty": 1, "unit_price": 5.0},
			},
			"total": 24.8,
		},
	}
}

// fakeGate deja pasar/bloquea según el mapa por tenant; sin entrada trata como
// cerrado (fail-closed, mismo criterio que el gate real).
type fakeGate struct {
	open map[string]bool
	err  error
}

func (g *fakeGate) Enabled(_ context.Context, tenantID string) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	return g.open[tenantID], nil
}

// fakeQueuer graba cada llamada a EnqueueWebhook para que el test inspeccione
// tenant/kind/payload sin tocar Postgres.
type fakeQueuer struct {
	calls []fakeQueuerCall
	err   error
}

type fakeQueuerCall struct {
	tenantID string
	kind     string
	payload  json.RawMessage
}

func (q *fakeQueuer) EnqueueWebhook(_ context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	q.calls = append(q.calls, fakeQueuerCall{tenantID: tenantID, kind: kind, payload: payload})
	return int64(len(q.calls)), nil
}

// TestWebhookSink_Handle_SinDependenciasNoOp: un sink construido sin sender/gate
// (patrón de tests unitarios que no ejercitan el camino de encolado) nunca falla
// ni panica, sea cual sea el efecto.
func TestWebhookSink_Handle_SinDependenciasNoOp(t *testing.T) {
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", nil, nil)
	ec := EffectContext{TenantID: "t-1", ContactID: "c-opaco"}
	if err := sink.Handle(context.Background(), ec, cartClosedEffect()); err != nil {
		t.Fatalf("Handle sin dependencias no debe fallar: %v", err)
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

// TestWebhookSink_Handle_EfectoNoCoincidenteNoEncola: un efecto que no es el
// deliverEffect configurado (navegación/telemetría) nunca llega al gate ni al
// queuer — un CRM no quiere saber de category_selected.
func TestWebhookSink_Handle_EfectoNoCoincidenteNoEncola(t *testing.T) {
	gate := &fakeGate{open: map[string]bool{"t-1": true}}
	q := &fakeQueuer{}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	nav := modules.Effect{Kind: "event", Name: "category_selected", Payload: map[string]any{"category_code": "bebidas"}}
	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1"}, nav); err != nil {
		t.Fatalf("Handle(navegación) no debe fallar: %v", err)
	}
	if len(q.calls) != 0 {
		t.Fatalf("un efecto de navegación no debe encolar nada, encoló %d", len(q.calls))
	}
}

// TestWebhookSink_Handle_GateCerradoNoEncola: tenant sin puente CRM activo
// (feature apagada o sin tenant_integrations habilitada) no encola.
func TestWebhookSink_Handle_GateCerradoNoEncola(t *testing.T) {
	gate := &fakeGate{open: map[string]bool{}} // ningún tenant habilitado
	q := &fakeQueuer{}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-sin-crm"}, cartClosedEffect()); err != nil {
		t.Fatalf("Handle con gate cerrado no debe fallar: %v", err)
	}
	if len(q.calls) != 0 {
		t.Fatalf("gate cerrado no debe encolar, encoló %d", len(q.calls))
	}
}

// TestWebhookSink_Handle_GateErrorFailClosed: un error del gate (p. ej. resolver
// de entitlements caído) se trata como "no" — mismo criterio fail-closed que
// entitlements.Resolver — y NO aborta el avance del flujo (Handle sigue
// devolviendo nil).
func TestWebhookSink_Handle_GateErrorFailClosed(t *testing.T) {
	gate := &fakeGate{err: errors.New("resolver caído")}
	q := &fakeQueuer{}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1"}, cartClosedEffect()); err != nil {
		t.Fatalf("un error del gate no debe abortar el flujo: %v", err)
	}
	if len(q.calls) != 0 {
		t.Fatalf("un gate en error no debe encolar (fail-closed), encoló %d", len(q.calls))
	}
}

// TestWebhookSink_Handle_EncolaCuandoGateAbierto: el camino feliz — gate abierto,
// encola exactamente UNA vez con kind="intake.push" y el tenant correcto.
func TestWebhookSink_Handle_EncolaCuandoGateAbierto(t *testing.T) {
	gate := &fakeGate{open: map[string]bool{"t-1": true}}
	q := &fakeQueuer{}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1", ContactID: "c-opaco"}, cartClosedEffect()); err != nil {
		t.Fatalf("Handle no debe fallar: %v", err)
	}
	if len(q.calls) != 1 {
		t.Fatalf("gate abierto debe encolar exactamente 1 vez, encoló %d", len(q.calls))
	}
	call := q.calls[0]
	if call.tenantID != "t-1" {
		t.Fatalf("tenant encolado=%q, quiero t-1", call.tenantID)
	}
	if call.kind != "intake.push" {
		t.Fatalf("kind encolado=%q, quiero intake.push", call.kind)
	}
	var decoded intakePushTemplate
	if err := json.Unmarshal(call.payload, &decoded); err != nil {
		t.Fatalf("el payload encolado no es JSON válido de intakePushTemplate: %v", err)
	}
	if decoded.IntakeID != "intake-abc-123" {
		t.Fatalf("intake_id encolado=%q, quiero intake-abc-123", decoded.IntakeID)
	}
}

// TestWebhookSink_Handle_ErrorDeEncoladoNoAborta: un fallo del store (Postgres
// caído) se loguea y NO aborta el flujo — el mensaje de respuesta al cliente
// sigue saliendo igual (INV-02/contrato del EventSink).
func TestWebhookSink_Handle_ErrorDeEncoladoNoAborta(t *testing.T) {
	gate := &fakeGate{open: map[string]bool{"t-1": true}}
	q := &fakeQueuer{err: errors.New("conexión perdida")}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1"}, cartClosedEffect()); err != nil {
		t.Fatalf("un fallo de encolado no debe abortar el flujo: %v", err)
	}
}

// TestBuildIntakePushTemplate_Contrato verifica la FORMA JSON de la plantilla
// que se encola (contrato wapp-crm-v1 · intake.push, campos baratos/estables —
// SIN buyer_data ni variables, que las completa el worker).
func TestBuildIntakePushTemplate_Contrato(t *testing.T) {
	ec := EffectContext{TenantID: "tenant-abc", ContactID: "contact-opaco-xyz"}
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)

	got := buildIntakePushTemplate(ec, cartClosedEffect(), now)

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	want := `{"contract_version":"1","verb":"intake.push","tenant":"tenant-abc","contact":"contact-opaco-xyz",` +
		`"intake_id":"intake-abc-123","lifecycle_status":"confirmed","revision_no":1,"customer_note":"",` +
		`"items":[{"sku":"A1","label":"Café","customization":"sin azúcar","qty":2,"unit_price":9.9},` +
		`{"sku":"B2","label":"Té","customization":"","qty":1,"unit_price":5}],` +
		`"total":24.8,"timestamp":"2026-08-07T10:00:00Z"}`
	if string(body) != want {
		t.Fatalf("plantilla de intake.push no coincide con el contrato\n got: %s\nwant: %s", body, want)
	}
}

// TestBuildIntakePushTemplate_EventHistoryIDOmitido: el contrato lo declara
// opcional (MD-042.1) — mientras el Plan 043 no exista, no se emite, y la clave
// NO debe aparecer en el JSON (omitempty), no aparecer como "".
func TestBuildIntakePushTemplate_EventHistoryIDOmitido(t *testing.T) {
	got := buildIntakePushTemplate(EffectContext{TenantID: "t", ContactID: "c"}, cartClosedEffect(), time.Unix(0, 0).UTC())
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytesContains(body, `"event_history_id"`) {
		t.Fatalf("event_history_id no debe aparecer en el JSON mientras esté vacío: %s", body)
	}
}

func bytesContains(b []byte, s string) bool {
	return len(b) >= len(s) && string(b) != "" && (func() bool {
		for i := 0; i+len(s) <= len(b); i++ {
			if string(b[i:i+len(s)]) == s {
				return true
			}
		}
		return false
	})()
}

// TestBuildIntakePushTemplate_Personalización (D-041.17, REQ-31b): la
// personalización de línea cruza al contrato, tal como en el stub que este
// archivo reemplaza.
func TestBuildIntakePushTemplate_Personalización(t *testing.T) {
	got := buildIntakePushTemplate(
		EffectContext{TenantID: "t-1", ContactID: "c-opaco"},
		cartClosedEffect(),
		time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	)
	if len(got.Items) != 2 {
		t.Fatalf("items=%d, quiero 2", len(got.Items))
	}
	if got.Items[0].Customization != "sin azúcar" {
		t.Fatalf("items[0].customization=%q, quiero %q", got.Items[0].Customization, "sin azúcar")
	}
	if got.Items[1].Customization != "" {
		t.Fatalf("items[1].customization=%q; esa línea no llevaba personalización", got.Items[1].Customization)
	}
	// INV-13: personalizar no cobra.
	if got.Items[0].UnitPrice != 9.9 || got.Total != 24.8 {
		t.Fatalf("unit_price=%v total=%v; la personalización movió el dinero", got.Items[0].UnitPrice, got.Total)
	}
}

// TestBuildIntakePushTemplate_SinPersonalización: un efecto sin la clave cruza
// igual, con la personalización vacía (retro-compatibilidad, cero rama especial).
func TestBuildIntakePushTemplate_SinPersonalización(t *testing.T) {
	eff := modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"intake_id": "i-1",
			"items":     []map[string]any{{"sku": "A1", "label": "Café", "qty": 1, "unit_price": 9.9}},
			"total":     9.9,
		},
	}
	got := buildIntakePushTemplate(EffectContext{TenantID: "t", ContactID: "c"}, eff, time.Unix(0, 0).UTC())
	if len(got.Items) != 1 || got.Items[0].Customization != "" {
		t.Fatalf("items=%+v; sin la clave, la personalización debe salir vacía", got.Items)
	}
}

// TestBuildIntakePushTemplate_RoundTripJSON: el builder tolera la forma
// round-trip JSON del efecto (items como []any de map, números como float64).
func TestBuildIntakePushTemplate_RoundTripJSON(t *testing.T) {
	eff := modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"intake_id": "i-round-trip",
			"items": []any{
				map[string]any{"sku": "A1", "label": "Café", "qty": float64(2), "unit_price": float64(9.9)},
			},
			"total": float64(19.8),
		},
	}
	got := buildIntakePushTemplate(EffectContext{TenantID: "t", ContactID: "c"}, eff, time.Unix(0, 0).UTC())
	if len(got.Items) != 1 || got.Items[0].SKU != "A1" || got.Items[0].Qty != 2 || got.Items[0].UnitPrice != 9.9 {
		t.Fatalf("items round-trip mal parseados: %+v", got.Items)
	}
	if got.Total != 19.8 {
		t.Fatalf("total round-trip: got %v want 19.8", got.Total)
	}
	if got.IntakeID != "i-round-trip" {
		t.Fatalf("intake_id: got %q want i-round-trip", got.IntakeID)
	}
}

// TestBuildIntakePushTemplate_NotaDelPedido (D-041.19, REQ-33f): la indicación
// del PEDIDO cruza la frontera hacia el CRM.
func TestBuildIntakePushTemplate_NotaDelPedido(t *testing.T) {
	eff := cartClosedEffect()
	eff.Payload["customer_note"] = "dejarlo en portería"

	got := buildIntakePushTemplate(
		EffectContext{TenantID: "t-1", ContactID: "c-opaco"},
		eff,
		time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	)
	if got.CustomerNote != "dejarlo en portería" {
		t.Fatalf("customer_note=%q, quiero %q", got.CustomerNote, "dejarlo en portería")
	}
	if got.Items[0].Customization != "sin azúcar" {
		t.Fatalf("items[0].customization=%q", got.Items[0].Customization)
	}
	if got.Total != 24.8 {
		t.Fatalf("total=%v; la indicación del pedido movió el dinero", got.Total)
	}
}

// TestBuildIntakePushTemplate_SinNotaDelPedido: cruza igual, con la nota vacía.
func TestBuildIntakePushTemplate_SinNotaDelPedido(t *testing.T) {
	got := buildIntakePushTemplate(
		EffectContext{TenantID: "t-1", ContactID: "c-opaco"},
		cartClosedEffect(),
		time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	)
	if got.CustomerNote != "" {
		t.Fatalf("customer_note=%q, quiero vacía", got.CustomerNote)
	}
}

// TestBuildIntakePushTemplate_LifecycleStatusSiempreConfirmed: el contrato
// JAMÁS emite "closed" (intake.push.md): la plantilla ya sale normalizada.
func TestBuildIntakePushTemplate_LifecycleStatusSiempreConfirmed(t *testing.T) {
	got := buildIntakePushTemplate(EffectContext{TenantID: "t", ContactID: "c"}, cartClosedEffect(), time.Unix(0, 0).UTC())
	if got.LifecycleStatus != "confirmed" {
		t.Fatalf("lifecycle_status=%q, quiero confirmed (el contrato jamás emite closed)", got.LifecycleStatus)
	}
}
