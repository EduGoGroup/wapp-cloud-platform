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
// en intakes.session_id).
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
// MemoryRepository (sin BD): dos item_added abren UNA sola solicitud "open"; cart_closed
// la cierra con el total e inserta las 2 líneas; flow_events acumula todos los
// efectos.
func TestPersistSink_Cart_MemoryProjection(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo)
	ctx := context.Background()
	ec := cartEC("t-cart", "c-1", "sess-1", "carrito")

	must := func(eff modules.Effect) {
		if err := sink.Handle(ctx, ec, eff); err != nil {
			t.Fatalf("Handle %s: %v", eff.Name, err)
		}
	}

	// Dos item_added → UNA solicitud open (idempotencia de la identidad de negocio).
	must(itemAdded("CAFE", "Café", 2, 2.5))
	must(itemAdded("FLAN", "Flan", 1, 3.0))
	intakes := repo.Intakes()
	if len(intakes) != 1 || intakes[0].Status != "open" {
		t.Fatalf("esperaba 1 solicitud open, got %+v", intakes)
	}
	if intakes[0].SessionID != "sess-1" {
		t.Fatalf("session_id no cableado en intakes: %+v", intakes[0])
	}

	// cart_closed → cierra la solicitud con total 8.00 e inserta las 2 líneas.
	items := []map[string]any{
		{"sku": "CAFE", "label": "Café", "qty": 2, "unit_price": 2.5},
		{"sku": "FLAN", "label": "Flan", "qty": 1, "unit_price": 3.0},
	}
	must(cartClosed(items, 8.0))

	intakes = repo.Intakes()
	if len(intakes) != 1 || intakes[0].Status != "closed" || intakes[0].Total != 8.0 {
		t.Fatalf("esperaba 1 solicitud closed total 8.0, got %+v", intakes)
	}
	lines := repo.IntakeItems(intakes[0].ID)
	if len(lines) != 2 {
		t.Fatalf("esperaba 2 intake_items, got %d (%+v)", len(lines), lines)
	}

	// flow_events: 2 item_added + 1 cart_closed = 3 (bitácora completa).
	if evs := repo.FlowEvents(); len(evs) != 3 {
		t.Fatalf("esperaba 3 flow_events, got %d", len(evs))
	}
}

// TestPersistSink_Cart_MemoryCancel: cart_cancelled transiciona la solicitud open a
// cancelled.
func TestPersistSink_Cart_MemoryCancel(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo)
	ctx := context.Background()
	ec := cartEC("t-cancel", "c-2", "sess-2", "carrito")

	if err := sink.Handle(ctx, ec, itemAdded("CAFE", "Café", 1, 2.5)); err != nil {
		t.Fatalf("Handle item_added: %v", err)
	}
	cancel := modules.Effect{Kind: "event", Name: "cart_cancelled", Payload: map[string]any{}}
	if err := sink.Handle(ctx, ec, cancel); err != nil {
		t.Fatalf("Handle cart_cancelled: %v", err)
	}
	intakes := repo.Intakes()
	if len(intakes) != 1 || intakes[0].Status != "cancelled" {
		t.Fatalf("esperaba solicitud cancelled, got %+v", intakes)
	}
}

// TestPersistSink_Cart_MemoryMenuSurveyNoIntakes: efectos de menú/encuesta NO
// tocan intakes (no-regresión: solo el carrito proyecta intakes).
func TestPersistSink_Cart_MemoryMenuSurveyNoIntakes(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := persistSinkWith(repo)
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
	if intakes := repo.Intakes(); len(intakes) != 0 {
		t.Fatalf("menú/encuesta NO deben crear intakes, got %+v", intakes)
	}
}

// TestPersistSink_Integracion_CartPedidoCompleto ejercita la proyección del
// carrito contra Postgres real (gated por WAPP_TEST_DB_DSN): un pedido de 2 líneas
// deja 1 fila en intakes (closed, total 8.00) + 2 en intake_items + 3 en flow_events;
// cancelar deja la solicitud en cancelled. SKIP limpio sin DSN.
func TestPersistSink_Integracion_CartPedidoCompleto(t *testing.T) {
	db := openTestDB(t) // migra incl. 0011/0012/0013
	repo := store.NewPostgresRepository(db)
	sink := persistSinkWith(repo)
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
	// La primera línea va PERSONALIZADA y la segunda no (D-041.17): así el aserto
	// de assertIntakeItems distingue "se guardó" de "se guardó en todas las filas".
	items := []map[string]any{
		{"sku": "CAFE", "label": "Café", "customization": "sin azúcar", "qty": 2, "unit_price": 2.5},
		{"sku": "FLAN", "label": "Flan", "qty": 1, "unit_price": 3.0},
	}
	must(cartClosed(items, 8.0))

	assertClosedIntake(t, db, tenant, contact)
	assertIntakeItems(t, db, tenant, contact)
	assertEventCount(t, db, flowID, 3)

	// Cancelar: nueva solicitud open + cart_cancelled → cancelled.
	ec2 := cartEC(tenant, fmt.Sprintf("c-cancel-%d", suffix), "sess-cart-2", flowID)
	if err := sink.Handle(ctx, ec2, itemAdded("TE", "Té", 1, 2.0)); err != nil {
		t.Fatalf("Handle item_added (cancel): %v", err)
	}
	cancel := modules.Effect{Kind: "event", Name: "cart_cancelled", Payload: map[string]any{}}
	if err := sink.Handle(ctx, ec2, cancel); err != nil {
		t.Fatalf("Handle cart_cancelled: %v", err)
	}
	assertIntakeStatus(t, db, tenant, ec2.ContactID, "cancelled")
}

// TestCartTTL_Integracion_ExpiresAtAndExpire ejercita el ciclo de vida del TTL
// contra Postgres real (gated por WAPP_TEST_DB_DSN): item_added fija expires_at a
// futuro (now + order_ttl); forzado el vencimiento (expires_at al pasado), el
// efecto cart_expired transiciona la solicitud a "expired" y deja la fila en
// flow_events. SKIP limpio sin DSN.
func TestCartTTL_Integracion_ExpiresAtAndExpire(t *testing.T) {
	db := openTestDB(t) // migra incl. 0011/0012/0013
	repo := store.NewPostgresRepository(db)
	sink := persistSinkWith(repo)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	tenant := fmt.Sprintf("tenant-ttl-%d", suffix)
	contactID := "c-opaco-ttl"
	flowID := fmt.Sprintf("carrito-ttl-%d", suffix)
	ec := cartEC(tenant, contactID, "sess-ttl", flowID)

	// item_added abre la solicitud y fija expires_at a futuro (TTL default 1h).
	if err := sink.Handle(ctx, ec, itemAdded("CAFE", "Café", 1, 2.5)); err != nil {
		t.Fatalf("Handle item_added: %v", err)
	}
	intake, found, err := repo.GetOpenIntake(ctx, tenant, contactID)
	if err != nil || !found {
		t.Fatalf("GetOpenIntake tras item_added: found=%v err=%v", found, err)
	}
	if intake.ExpiresAt.IsZero() || !intake.ExpiresAt.After(time.Now()) {
		t.Fatalf("expires_at debe ser futuro tras item_added: %v", intake.ExpiresAt)
	}

	// Fuerza el vencimiento: expires_at al pasado (misma semántica que el paso del
	// tiempo). UpsertIntake es idempotente por id.
	intake.ExpiresAt = time.Now().Add(-time.Minute)
	if err := repo.UpsertIntake(ctx, intake); err != nil {
		t.Fatalf("forzar vencimiento (UpsertIntake): %v", err)
	}

	// cart_expired (lo sintetiza el runtime al reanudar) → solicitud "expired" + fila
	// en flow_events (el mismo PersistSink lo materializa).
	expired := modules.Effect{Kind: "event", Name: "cart_expired", Payload: map[string]any{}}
	if err := sink.Handle(ctx, ec, expired); err != nil {
		t.Fatalf("Handle cart_expired: %v", err)
	}
	assertIntakeStatus(t, db, tenant, contactID, "expired")

	var nExpired int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.flow_events WHERE flow_id = $1 AND name = 'cart_expired'`,
		flowID).Scan(&nExpired); err != nil {
		t.Fatalf("SELECT flow_events cart_expired: %v", err)
	}
	if nExpired != 1 {
		t.Fatalf("esperaba 1 flow_event cart_expired, got %d", nExpired)
	}
}

// assertClosedIntake verifica 1 solicitud closed con total 8.00 y session_id cableado.
func assertClosedIntake(t *testing.T, db *sql.DB, tenant, contact string) {
	t.Helper()
	var (
		nIntakes  int
		status    string
		totalNum  float64
		sessionID string
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*), max(status), max(total), max(session_id)
		FROM public.intakes WHERE tenant_id = $1 AND contact_id = $2
	`, tenant, contact).Scan(&nIntakes, &status, &totalNum, &sessionID); err != nil {
		t.Fatalf("SELECT intakes: %v", err)
	}
	if nIntakes != 1 || status != "closed" || totalNum != 8.0 || sessionID != "sess-cart" {
		t.Fatalf("solicitud inesperada: n=%d status=%q total=%v session=%q", nIntakes, status, totalNum, sessionID)
	}
}

// assertIntakeItems verifica 2 líneas y la agregación de negocio SUM(qty*unit_price).
func assertIntakeItems(t *testing.T, db *sql.DB, tenant, contact string) {
	t.Helper()
	var (
		intakeID string
		nItems   int
		sumTot   float64
	)
	if err := db.QueryRowContext(context.Background(), `
		SELECT o.id::text, count(oi.*), COALESCE(SUM(oi.qty * oi.unit_price), 0)
		FROM public.intakes o JOIN public.intake_items oi ON oi.intake_id = o.id
		WHERE o.tenant_id = $1 AND o.contact_id = $2
		GROUP BY o.id
	`, tenant, contact).Scan(&intakeID, &nItems, &sumTot); err != nil {
		t.Fatalf("SELECT intake_items: %v", err)
	}
	if nItems != 2 || sumTot != 8.0 {
		t.Fatalf("intake_items inesperado: n=%d suma=%v", nItems, sumTot)
	}

	// La personalización de la línea llegó HASTA LA COLUMNA (D-041.17): el efecto
	// la traía en `items[0]` y la fila del café la tiene; la del flan, que no la
	// pedía, queda con el vacío del DEFAULT — no con la del vecino. Y el SUM de
	// arriba, calculado sobre las mismas filas, no se movió (INV-13).
	rows, err := db.QueryContext(context.Background(), `
		SELECT sku, customization FROM public.intake_items
		WHERE intake_id = $1::uuid ORDER BY sku
	`, intakeID)
	if err != nil {
		t.Fatalf("SELECT customization: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas: %v", cerr)
		}
	}()
	got := map[string]string{}
	for rows.Next() {
		var sku, custom string
		if serr := rows.Scan(&sku, &custom); serr != nil {
			t.Fatalf("leer customization: %v", serr)
		}
		got[sku] = custom
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("recorrer customization: %v", rerr)
	}
	if got["CAFE"] != "sin azúcar" || got["FLAN"] != "" {
		t.Fatalf("customization en BD = %v; quiero CAFE=%q y FLAN vacía", got, "sin azúcar")
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

// assertIntakeStatus verifica el status de la (única) solicitud de un contacto.
func assertIntakeStatus(t *testing.T, db *sql.DB, tenant, contact, want string) {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(), `
		SELECT status FROM public.intakes WHERE tenant_id = $1 AND contact_id = $2
	`, tenant, contact).Scan(&status); err != nil {
		t.Fatalf("SELECT solicitud (%s): %v", want, err)
	}
	if status != want {
		t.Fatalf("esperaba solicitud %q, got %q", want, status)
	}
}
