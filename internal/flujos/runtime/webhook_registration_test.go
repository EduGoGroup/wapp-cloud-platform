package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// cartClosedEmit es el efecto cart_closed que declara el módulo de test para
// ejercer la proyección del PersistSink (orders/order_items) y, en paralelo, el
// WebhookSink stub.
func cartClosedEmit() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: "cart_closed",
		Payload: map[string]any{
			"items": []map[string]any{
				{"sku": "A1", "label": "Café", "qty": 2, "unit_price": 9.9},
			},
			"total": 19.8,
		},
	}
}

// runCartClosed arma un runtime con los sinks dados, siembra la definición y
// avanza una conversación que emite cart_closed; devuelve el repo para inspección.
func runCartClosed(t *testing.T, extra ...runtime.Option) *store.MemoryRepository {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	opts := append([]runtime.Option{runtime.WithEventSink(runtime.NewPersistSink(repo, cart.NewProjector(repo)))}, extra...)
	rt := runtime.New(repo, newEffectEngine([]modules.Effect{cartClosedEmit()}), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger(), opts...)
	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	return repo
}

// TestWebhookSink_Registrado_NoAlteraPersistSink (T4): añadir el WebhookSink al
// fan-out NO cambia lo que el PersistSink materializa. Se comparan dos corridas
// idénticas —solo PersistSink vs PersistSink+WebhookSink— y el estado persistido
// (flow_events, orders, order_items) debe ser equivalente.
func TestWebhookSink_Registrado_NoAlteraPersistSink(t *testing.T) {
	repoSolo := runCartClosed(t)
	repoConWebhook := runCartClosed(t, runtime.WithEventSink(runtime.NewWebhookSink(discardLogger(), cart.EffectCartClosed)))

	if got := len(repoSolo.FlowEvents()); got != len(repoConWebhook.FlowEvents()) {
		t.Fatalf("flow_events difieren: solo=%d con-webhook=%d", got, len(repoConWebhook.FlowEvents()))
	}
	oa, ob := repoSolo.Orders(), repoConWebhook.Orders()
	if len(oa) != 1 || len(ob) != 1 {
		t.Fatalf("orders debe ser 1 en ambas: solo=%d con-webhook=%d", len(oa), len(ob))
	}
	if oa[0].Status != ob[0].Status || oa[0].Total != ob[0].Total {
		t.Fatalf("orden difiere: solo=%+v con-webhook=%+v", oa[0], ob[0])
	}
	if got := len(repoSolo.OrderItems(oa[0].ID)); got != 1 || got != len(repoConWebhook.OrderItems(ob[0].ID)) {
		t.Fatalf("order_items difieren o no es 1: solo=%d con-webhook=%d", got, len(repoConWebhook.OrderItems(ob[0].ID)))
	}
}
