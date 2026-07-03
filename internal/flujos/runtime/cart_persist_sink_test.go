package runtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// cartEC construye un EffectContext del carrito (incluye SessionID, que aterriza
// en orders.session_id).
func cartEC(tenant, contact, session, flow string) runtime.EffectContext {
	return runtime.EffectContext{
		TenantID: tenant, ContactID: contact, SessionID: session, FlowID: flow, FlowVersion: 1,
	}
}

func itemAdded(sku, label string, qty int, unit float64) modules.Effect {
	return modules.Effect{Kind: "event", Name: "item_added", Payload: map[string]any{
		"sku": sku, "label": label, "qty": qty, "unit_price": unit,
	}}
}

func cartClosed(items []map[string]any, total float64) modules.Effect {
	return modules.Effect{Kind: "persist", Name: "cart_closed", Payload: map[string]any{
		"items": items, "total": total,
	}}
}

// TestPersistSink_Cart_MemoryProjection valida la proyección del carrito con el
// MemoryRepository (sin BD): dos item_added abren UNA sola orden "open"; cart_closed
// la cierra con el total e inserta las 2 líneas; flow_events acumula todos los
// efectos.
func TestPersistSink_Cart_MemoryProjection(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := runtime.NewPersistSink(repo)
	ctx := context.Background()
	ec := cartEC("t-cart", "c-1", "sess-1", "carrito")

	must := func(eff modules.Effect) {
		if err := sink.Handle(ctx, ec, eff); err != nil {
			t.Fatalf("Handle %s: %v", eff.Name, err)
		}
	}

	// Dos item_added → UNA orden open (idempotencia de la identidad de negocio).
	must(itemAdded("CAFE", "Café", 2, 2.5))
	must(itemAdded("FLAN", "Flan", 1, 3.0))
	orders := repo.Orders()
	if len(orders) != 1 || orders[0].Status != "open" {
		t.Fatalf("esperaba 1 orden open, got %+v", orders)
	}
	if orders[0].SessionID != "sess-1" {
		t.Fatalf("session_id no cableado en orders: %+v", orders[0])
	}

	// cart_closed → cierra la orden con total 8.00 e inserta las 2 líneas.
	items := []map[string]any{
		{"sku": "CAFE", "label": "Café", "qty": 2, "unit_price": 2.5},
		{"sku": "FLAN", "label": "Flan", "qty": 1, "unit_price": 3.0},
	}
	must(cartClosed(items, 8.0))

	orders = repo.Orders()
	if len(orders) != 1 || orders[0].Status != "closed" || orders[0].Total != 8.0 {
		t.Fatalf("esperaba 1 orden closed total 8.0, got %+v", orders)
	}
	lines := repo.OrderItems(orders[0].ID)
	if len(lines) != 2 {
		t.Fatalf("esperaba 2 order_items, got %d (%+v)", len(lines), lines)
	}

	// flow_events: 2 item_added + 1 cart_closed = 3 (bitácora completa).
	if evs := repo.FlowEvents(); len(evs) != 3 {
		t.Fatalf("esperaba 3 flow_events, got %d", len(evs))
	}
}

// TestPersistSink_Cart_MemoryCancel: cart_cancelled transiciona la orden open a
// cancelled.
func TestPersistSink_Cart_MemoryCancel(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := runtime.NewPersistSink(repo)
	ctx := context.Background()
	ec := cartEC("t-cancel", "c-2", "sess-2", "carrito")

	if err := sink.Handle(ctx, ec, itemAdded("CAFE", "Café", 1, 2.5)); err != nil {
		t.Fatalf("Handle item_added: %v", err)
	}
	cancel := modules.Effect{Kind: "event", Name: "cart_cancelled", Payload: map[string]any{}}
	if err := sink.Handle(ctx, ec, cancel); err != nil {
		t.Fatalf("Handle cart_cancelled: %v", err)
	}
	orders := repo.Orders()
	if len(orders) != 1 || orders[0].Status != "cancelled" {
		t.Fatalf("esperaba orden cancelled, got %+v", orders)
	}
}

// TestPersistSink_Cart_MemoryMenuSurveyNoOrders: efectos de menú/encuesta NO
// tocan orders (no-regresión: solo el carrito proyecta orders).
func TestPersistSink_Cart_MemoryMenuSurveyNoOrders(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := runtime.NewPersistSink(repo)
	ctx := context.Background()
	ec := cartEC("t-mix", "c-3", "sess-3", "flujo")

	effs := []modules.Effect{
		{Kind: "persist", Name: "survey_answer", Payload: map[string]any{"question_id": "q1", "answer_code": "si"}},
		{Kind: "event", Name: "menu_selected", Payload: map[string]any{"option": "1"}},
	}
	for _, eff := range effs {
		if err := sink.Handle(ctx, ec, eff); err != nil {
			t.Fatalf("Handle %s: %v", eff.Name, err)
		}
	}
	if orders := repo.Orders(); len(orders) != 0 {
		t.Fatalf("menú/encuesta NO deben crear orders, got %+v", orders)
	}
}

// TestPersistSink_Integracion_CartPedidoCompleto ejercita la proyección del
// carrito contra Postgres real (gated por WAPP_TEST_DB_DSN): un pedido de 2 líneas
// deja 1 fila en orders (closed, total 8.00) + 2 en order_items + 3 en flow_events;
// cancelar deja la orden en cancelled. SKIP limpio sin DSN.
func TestPersistSink_Integracion_CartPedidoCompleto(t *testing.T) {
	db := openTestDB(t) // migra incl. 0011/0012/0013
	repo := store.NewPostgresRepository(db)
	sink := runtime.NewPersistSink(repo)
	ctx := context.Background()

	// Aislamiento: tenant/contact/flow únicos por corrida.
	suffix := time.Now().UnixNano()
	tenant := fmt.Sprintf("tenant-cart-%d", suffix)
	contact := "c-opaco-cart"
	flowID := fmt.Sprintf("carrito-%d", suffix)
	ec := cartEC(tenant, contact, "sess-cart", flowID)

	must := func(eff modules.Effect) {
		if err := sink.Handle(ctx, ec, eff); err != nil {
			t.Fatalf("Handle %s (postgres): %v", eff.Name, err)
		}
	}
	must(itemAdded("CAFE", "Café", 2, 2.5))
	must(itemAdded("FLAN", "Flan", 1, 3.0))
	items := []map[string]any{
		{"sku": "CAFE", "label": "Café", "qty": 2, "unit_price": 2.5},
		{"sku": "FLAN", "label": "Flan", "qty": 1, "unit_price": 3.0},
	}
	must(cartClosed(items, 8.0))

	assertClosedOrder(t, db, tenant, contact)
	assertOrderItems(t, db, tenant, contact)
	assertEventCount(t, db, flowID, 3)

	// Cancelar: nueva orden open + cart_cancelled → cancelled.
	ec2 := cartEC(tenant, fmt.Sprintf("c-cancel-%d", suffix), "sess-cart-2", flowID)
	if err := sink.Handle(ctx, ec2, itemAdded("TE", "Té", 1, 2.0)); err != nil {
		t.Fatalf("Handle item_added (cancel): %v", err)
	}
	cancel := modules.Effect{Kind: "event", Name: "cart_cancelled", Payload: map[string]any{}}
	if err := sink.Handle(ctx, ec2, cancel); err != nil {
		t.Fatalf("Handle cart_cancelled: %v", err)
	}
	assertOrderStatus(t, db, tenant, ec2.ContactID, "cancelled")
}

// assertClosedOrder verifica 1 orden closed con total 8.00 y session_id cableado.
func assertClosedOrder(t *testing.T, db *sql.DB, tenant, contact string) {
	t.Helper()
	var (
		nOrders   int
		status    string
		totalNum  float64
		sessionID string
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*), max(status), max(total), max(session_id)
		FROM public.orders WHERE tenant_id = $1 AND contact_id = $2
	`, tenant, contact).Scan(&nOrders, &status, &totalNum, &sessionID); err != nil {
		t.Fatalf("SELECT orders: %v", err)
	}
	if nOrders != 1 || status != "closed" || totalNum != 8.0 || sessionID != "sess-cart" {
		t.Fatalf("orden inesperada: n=%d status=%q total=%v session=%q", nOrders, status, totalNum, sessionID)
	}
}

// assertOrderItems verifica 2 líneas y la agregación de negocio SUM(qty*unit_price).
func assertOrderItems(t *testing.T, db *sql.DB, tenant, contact string) {
	t.Helper()
	var (
		orderID string
		nItems  int
		sumTot  float64
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT o.id::text, count(oi.*), COALESCE(SUM(oi.qty * oi.unit_price), 0)
		FROM public.orders o JOIN public.order_items oi ON oi.order_id = o.id
		WHERE o.tenant_id = $1 AND o.contact_id = $2
		GROUP BY o.id
	`, tenant, contact).Scan(&orderID, &nItems, &sumTot); err != nil {
		t.Fatalf("SELECT order_items: %v", err)
	}
	if nItems != 2 || sumTot != 8.0 {
		t.Fatalf("order_items inesperado: n=%d suma=%v", nItems, sumTot)
	}
}

// assertEventCount verifica el número de filas en flow_events para un flujo.
func assertEventCount(t *testing.T, db *sql.DB, flowID string, want int) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM public.flow_events WHERE flow_id = $1`, flowID).Scan(&n); err != nil {
		t.Fatalf("SELECT flow_events: %v", err)
	}
	if n != want {
		t.Fatalf("esperaba %d flow_events, got %d", want, n)
	}
}

// assertOrderStatus verifica el status de la (única) orden de un contacto.
func assertOrderStatus(t *testing.T, db *sql.DB, tenant, contact, want string) {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(), `
		SELECT status FROM public.orders WHERE tenant_id = $1 AND contact_id = $2
	`, tenant, contact).Scan(&status); err != nil {
		t.Fatalf("SELECT orden (%s): %v", want, err)
	}
	if status != want {
		t.Fatalf("esperaba orden %q, got %q", want, status)
	}
}
