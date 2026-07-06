package main

import (
	"context"
	"io"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const (
	itFlowID   = "menu-test"
	itContactA = "5491111111111"
	itContactB = "5492222222222"
	itContactC = "5493333333333"

	// itMenuPrompt es el prompt del nodo inicial (menu). El render del módulo lo
	// emite tal cual (las opciones ya vienen numeradas en el texto, corte actual).
	itMenuPrompt = "Hola 👋 ¿En qué te ayudo?\n1) Ventas\n2) Soporte"
	itTextOpt1   = "Te paso con Ventas."
	itTextOpt2   = "Cuéntame tu problema."
)

// itMenuDefinition es la definición JSON del flujo de menú (esquema B, §4):
// un nodo "menu" inicial con 2 opciones a 2 nodos "message" terminales.
const itMenuDefinition = `{
  "flow_id": "menu-test",
  "version": 1,
  "initial": "root",
  "nodes": {
    "root":    { "type": "menu", "prompt": "Hola 👋 ¿En qué te ayudo?\n1) Ventas\n2) Soporte", "options": { "1": "ventas", "2": "soporte" } },
    "ventas":  { "type": "message", "text": "Te paso con Ventas.", "next": null },
    "soporte": { "type": "message", "text": "Cuéntame tu problema.", "next": null }
  }
}`

// fakeTenantResolver resuelve cualquier session_id al tenant fijo del test
// (decisión §10.A); evita el PostgresTenantResolver, que requiere BD real.
type fakeTenantResolver struct{ tenantID string }

func (f fakeTenantResolver) ResolveTenant(_ context.Context, _ string) (string, string, error) {
	return f.tenantID, "bot", nil
}

// TestInProcessFlowsMenu ejerce el CAMINO DE FLUJOS de extremo a extremo con el
// Gateway real sobre bufconn + un Edge falso (edgeSim) + MemoryRepository +
// engine/menu reales + runtime, SIN BD real (CI-safe, corre bajo -race):
//
//  1. Se siembra una definición de menú y se enchufa rt.OnIncoming a gw.
//  2. rt.Start (contactos A y B) envía el prompt del menú (el Edge lo recibe).
//  3. El Edge emite "1" (m1) por A -> avanza al nodo destino (SendText).
//  4. El Edge emite "2" (m2) por B -> avanza al SUYO. Este paso PRUEBA que el
//     loop Recv del Gateway sigue vivo tras un OnIncoming que hace SendText: si
//     OnIncoming bloqueara ese loop (deadlock), m2 nunca se procesaría y este
//     assert agotaría el timeout.
//  5. Un entrante de OTRO contacto (C) SIN estado -> se IGNORA (decisión C).
//  6. Re-emitir m1 por A -> idempotencia: no hay nuevo avance ni envío.
func TestInProcessFlowsMenu(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h := startHarness(t)

	// Sesión viva del Edge (enroll -> connect mTLS -> heartbeat -> lease + fleet).
	resp, edgeKey := h.enroll(ctx, t)
	stream := h.connect(ctx, t, resp, edgeKey)
	edge := newEdgeSim(stream, h.mgr.PublicKey())
	edge.sentText = make(chan *cloudlinkv1.SendText, 8)
	edge.run()
	if err := edge.send(heartbeat(itSessionID, 1)); err != nil {
		t.Fatalf("Send heartbeat inicial: %v", err)
	}
	requireSignal(ctx, t, edge.leaseOK, "LeaseUpdate inicial válido")
	waitFleetOnline(t, h.fleetRepo, itSessionID)

	// Motor de Flujos: registro + engine + store en memoria + runtime, cableado
	// igual que cmd/server/main.go salvo el resolver (fake, sin BD).
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	eng := engine.New(reg)
	flowStore := flowstore.NewMemoryRepository()
	contactResolver := contact.NewMemoryResolver(flowStore)
	rt := flowruntime.New(flowStore, eng, h.gw, fakeTenantResolver{tenantID: itTenantID}, contactResolver, logger.New(logger.WithWriter(io.Discard)))
	// Se enchufa ANTES de inyectar entrantes (el setup solo mandó heartbeats).
	h.gw.OnIncoming = rt.OnIncoming

	// 1. Sembrar la definición (validada por el mismo camino del admin).
	flow, err := model.ParseAndValidate([]byte(itMenuDefinition))
	if err != nil {
		t.Fatalf("ParseAndValidate definición: %v", err)
	}
	if _, err := flowStore.InsertDefinition(ctx, itTenantID, flow); err != nil {
		t.Fatalf("InsertDefinition: %v", err)
	}

	// 2. Start (decisión C) de DOS conversaciones -> cada Edge recibe el prompt.
	refA, err := contact.NewRef(contact.KindPhoneE164, itContactA)
	if err != nil {
		t.Fatalf("NewRef A: %v", err)
	}
	refB, err := contact.NewRef(contact.KindPhoneE164, itContactB)
	if err != nil {
		t.Fatalf("NewRef B: %v", err)
	}
	if _, err := rt.Start(ctx, itTenantID, itFlowID, itSessionID, refA); err != nil {
		t.Fatalf("rt.Start A: %v", err)
	}
	requireSendText(ctx, t, edge.sentText, itContactA, itMenuPrompt)
	if _, err := rt.Start(ctx, itTenantID, itFlowID, itSessionID, refB); err != nil {
		t.Fatalf("rt.Start B: %v", err)
	}
	requireSendText(ctx, t, edge.sentText, itContactB, itMenuPrompt)

	// 3. Entrante "1" (m1) por A -> avanza al nodo "ventas".
	if err := edge.send(incomingID(itSessionID, itContactA, "1", "m1")); err != nil {
		t.Fatalf("Send entrante opción 1 (A): %v", err)
	}
	requireSendText(ctx, t, edge.sentText, itContactA, itTextOpt1)

	// 4. Entrante "2" (m2) por B -> avanza al SUYO. PRUEBA que el loop Recv del
	// Gateway sigue vivo tras un OnIncoming que envía (si bloqueara, timeout).
	if err := edge.send(incomingID(itSessionID, itContactB, "2", "m2")); err != nil {
		t.Fatalf("Send entrante opción 2 (B): %v", err)
	}
	requireSendText(ctx, t, edge.sentText, itContactB, itTextOpt2)

	// 5. Entrante de OTRO contacto (C) SIN estado -> ignorado (no se envía nada).
	if err := edge.send(incomingID(itSessionID, itContactC, "1", "m3")); err != nil {
		t.Fatalf("Send entrante sin estado (C): %v", err)
	}
	requireNoSendText(t, edge.sentText, 300*time.Millisecond)

	// 6. Re-emitir m1 por A -> idempotencia: no hay nuevo avance ni envío.
	if err := edge.send(incomingID(itSessionID, itContactA, "1", "m1")); err != nil {
		t.Fatalf("Re-enviar m1 (A): %v", err)
	}
	requireNoSendText(t, edge.sentText, 300*time.Millisecond)

	edge.assertNoErrors(t)
}

// incomingID es como incoming() pero fija el wa_message_id (idempotencia, §6).
func incomingID(sessionID, from, text, waID string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: sessionID,
		Payload: &cloudlinkv1.EdgeToCloud_Incoming{Incoming: &cloudlinkv1.IncomingMessage{
			From:        from,
			Text:        text,
			WaMessageId: waID,
			TsUnix:      time.Now().Unix(),
		}},
	}
}

func requireSendText(ctx context.Context, t *testing.T, ch <-chan *cloudlinkv1.SendText, wantTo, wantText string) {
	t.Helper()
	select {
	case st := <-ch:
		if st.GetTo() != wantTo {
			t.Fatalf("SendText.To: got %q, want %q", st.GetTo(), wantTo)
		}
		if st.GetText() != wantText {
			t.Fatalf("SendText.Text: got %q, want %q", st.GetText(), wantText)
		}
	case <-ctx.Done():
		t.Fatalf("timeout esperando SendText %q", wantText)
	}
}

func requireNoSendText(t *testing.T, ch <-chan *cloudlinkv1.SendText, within time.Duration) {
	t.Helper()
	select {
	case st := <-ch:
		t.Fatalf("no se esperaba SendText, llegó to=%q text=%q", st.GetTo(), st.GetText())
	case <-time.After(within):
	}
}
