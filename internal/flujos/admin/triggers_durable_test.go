package admin_test

// triggers_durable_test.go — Plan 054 · F3.
//
// T2.6 (marca derivada flow_needs_event) y T2.7 (validación 422, D-054.8, LAS DOS
// DIRECCIONES: alta de un fallback durable sin red de event_start, y baja de la
// ÚLTIMA event_start que sostiene un fallback durable). Usa stubDurableChecker
// (durable_flow_test.go) para no tener que levantar un Registry/engine completo
// en cada caso — EngineDurableFlowChecker ya se prueba aparte contra el motor
// real.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

const flowDurable = "carrito-durable"
const flowPlain = "menu-plano"

// ---------------------------------------------------------------------------
// T2.7 dirección (i): alta de kind='fallback' hacia un flujo durable.
// ---------------------------------------------------------------------------

// TestCreateTrigger_422_FallbackDurableSinRedEventStart: cero reglas event_start
// vivas + fallback hacia un flujo durable ⇒ 422 con motivo, y la fila NO se
// escribe (criterio (i) de T2.7, MD-054.2).
func TestCreateTrigger_422_FallbackDurableSinRedEventStart(t *testing.T) {
	store := trigger.NewMemoryStore()
	checker := stubDurableChecker{flowDurable: true}
	h := admin.CreateTriggerHandler(store, checker)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"fallback","flow_id":"`+flowDurable+`"}`, true)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event_start") || !strings.Contains(rec.Body.String(), "D-054.8") {
		t.Fatalf("el 422 debe explicar el motivo (event_start / D-054.8), got %q", rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("el 422 NO debe persistir la fila, got %+v", rules)
	}
}

// TestCreateTrigger_201_FallbackNoDurable_Regresion: un fallback hacia un flujo
// SIN nodos durables se acepta con 201 exactamente como hoy, aunque el tenant no
// tenga ninguna event_start — la guarda de T2.7 no toca este caso.
func TestCreateTrigger_201_FallbackNoDurable_Regresion(t *testing.T) {
	store := trigger.NewMemoryStore()
	checker := stubDurableChecker{flowDurable: true} // flowPlain NO está en el mapa ⇒ false
	h := admin.CreateTriggerHandler(store, checker)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"fallback","flow_id":"`+flowPlain+`"}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("debe persistir la fila, got %+v", rules)
	}
}

// TestCreateTrigger_201_FallbackDurableConEventStartViva_Regresion: el mismo
// fallback durable del caso 422, pero con una event_start viva en el tenant ⇒
// 201, como hoy — la red ya existe, no hay corner que cerrar.
func TestCreateTrigger_201_FallbackDurableConEventStartViva_Regresion(t *testing.T) {
	store := trigger.NewMemoryStore()
	if _, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true,
	}); err != nil {
		t.Fatalf("seed event_start: %v", err)
	}
	checker := stubDurableChecker{flowDurable: true}
	h := admin.CreateTriggerHandler(store, checker)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"fallback","flow_id":"`+flowDurable+`"}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("debe persistir la fila nueva junto a la event_start sembrada, got %+v", rules)
	}
}

// TestCreateTrigger_201_FallbackFlowIDInexistente_FailOpen: un flow_id que no
// existe (checker no lo conoce ⇒ false) se trata como NO durable — fail-open
// (H2 del contrato): el alta sigue aceptándose con 201, SIN 422, aunque no haya
// ninguna event_start. Este plan no introduce validación de existencia de flujo.
func TestCreateTrigger_201_FallbackFlowIDInexistente_FailOpen(t *testing.T) {
	store := trigger.NewMemoryStore()
	checker := stubDurableChecker{} // vacío: CUALQUIER flow_id resuelve a "no durable"
	h := admin.CreateTriggerHandler(store, checker)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"fallback","flow_id":"flujo-que-no-existe"}`, true)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201 (fail-open); body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreateTrigger_500_CheckerFalla: si el checker no puede responder (algo
// DISTINTO de "flujo inexistente", que ya es fail-open), el alta NO se acepta a
// ciegas: 500, y la fila tampoco se escribe.
func TestCreateTrigger_500_CheckerFalla(t *testing.T) {
	store := trigger.NewMemoryStore()
	checker := erroringDurableChecker{err: errFalloDeliberado}
	h := admin.CreateTriggerHandler(store, checker)

	rec := doTrigger(h, http.MethodPost, "/admin/triggers", ctxTenant,
		`{"kind":"fallback","flow_id":"`+flowDurable+`"}`, true)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("un checker que falla no debe dejar persistir la fila, got %+v", rules)
	}
}

// ---------------------------------------------------------------------------
// T2.7 dirección (ii): baja de la ÚLTIMA event_start viva («la puerta de atrás»).
// ---------------------------------------------------------------------------

// seedEventStartYFallbackDurable siembra UNA regla event_start habilitada y UN
// fallback habilitado hacia flowDurable (checker la marca durable). Devuelve el
// trigger_id de la event_start, la única fila que las pruebas de esta sección
// intentan borrar.
func seedEventStartYFallbackDurable(t *testing.T, store *trigger.MemoryStore) string {
	t.Helper()
	es, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed event_start: %v", err)
	}
	if _, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindFallback, FlowID: flowDurable, Enabled: true,
	}); err != nil {
		t.Fatalf("seed fallback: %v", err)
	}
	return es.TriggerID
}

// TestDeleteTrigger_422_UltimaEventStartConFallbackDurable: borrar la ÚNICA
// event_start viva de un tenant que tiene un fallback durable ⇒ 422 con motivo,
// y la fila NO se borra (criterio (ii) de T2.7 — «la puerta de atrás»).
func TestDeleteTrigger_422_UltimaEventStartConFallbackDurable(t *testing.T) {
	store := trigger.NewMemoryStore()
	esID := seedEventStartYFallbackDurable(t, store)
	checker := stubDurableChecker{flowDurable: true}

	mux := http.NewServeMux()
	mux.Handle("DELETE /admin/triggers/{id}", admin.DeleteTriggerHandler(store, checker))
	req := httptest.NewRequest(http.MethodDelete, "/admin/triggers/"+esID, nil)
	req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: ctxTenant, Subject: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event_start") || !strings.Contains(rec.Body.String(), "D-054.8") {
		t.Fatalf("el 422 debe explicar el motivo (event_start / D-054.8), got %q", rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("el 422 NO debe borrar la fila, got %+v", rules)
	}
}

// TestDeleteTrigger_204_EventStartConOtraViva_Regresion: borrar una event_start
// cuando QUEDA otra viva ⇒ 204, como siempre — la red se sostiene sin esta fila.
func TestDeleteTrigger_204_EventStartConOtraViva_Regresion(t *testing.T) {
	store := trigger.NewMemoryStore()
	esID := seedEventStartYFallbackDurable(t, store)
	// Segunda event_start habilitada: ahora hay DOS.
	if _, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindEventStart, Keyword: "pedido",
		MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true,
	}); err != nil {
		t.Fatalf("seed segunda event_start: %v", err)
	}
	checker := stubDurableChecker{flowDurable: true}

	mux := http.NewServeMux()
	mux.Handle("DELETE /admin/triggers/{id}", admin.DeleteTriggerHandler(store, checker))
	req := httptest.NewRequest(http.MethodDelete, "/admin/triggers/"+esID, nil)
	req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: ctxTenant, Subject: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d, quiero 204; body=%s", rec.Code, rec.Body.String())
	}
	rules, err := store.List(context.Background(), ctxTenant)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("debe quedar la event_start restante + el fallback, got %+v", rules)
	}
}

// TestDeleteTrigger_204_UltimaEventStartSinFallbackDurable_Regresion: borrar la
// última event_start de un tenant SIN ningún fallback durable ⇒ 204 — nada que
// D-054.8 proteja si no hay fallback durable dependiendo de esa red.
func TestDeleteTrigger_204_UltimaEventStartSinFallbackDurable_Regresion(t *testing.T) {
	store := trigger.NewMemoryStore()
	es, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed event_start: %v", err)
	}
	checker := stubDurableChecker{} // ningún flujo es durable

	mux := http.NewServeMux()
	mux.Handle("DELETE /admin/triggers/{id}", admin.DeleteTriggerHandler(store, checker))
	req := httptest.NewRequest(http.MethodDelete, "/admin/triggers/"+es.TriggerID, nil)
	req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: ctxTenant, Subject: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d, quiero 204; body=%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// T2.6: la marca derivada flow_needs_event en GET .../triggers.
// ---------------------------------------------------------------------------

// TestListTriggers_FlowNeedsEvent: una regla keyword/fallback hacia un flujo
// durable sale marcada flow_needs_event=true; la equivalente hacia un flujo SIN
// nodos durables NO la lleva (regresión negativa). event_start no se marca
// (siempre trae su propio evento, D-054.5).
func TestListTriggers_FlowNeedsEvent(t *testing.T) {
	store := trigger.NewMemoryStore()
	seedRules := []trigger.Rule{
		{TenantID: ctxTenant, Kind: trigger.KindFallback, FlowID: flowDurable, Enabled: true},
		{TenantID: ctxTenant, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: flowDurable, Enabled: true},
		{TenantID: ctxTenant, Kind: trigger.KindKeyword, Keyword: "menu", MatchType: trigger.MatchExact, FlowID: flowPlain, Enabled: true},
		{TenantID: ctxTenant, Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, EventKind: "cart", Enabled: true},
	}
	for _, r := range seedRules {
		if _, err := store.Insert(context.Background(), r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	checker := stubDurableChecker{flowDurable: true} // flowPlain ausente ⇒ false

	rec := doTrigger(admin.ListTriggersHandler(store, checker), http.MethodGet, "/admin/triggers", ctxTenant, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out []struct {
		Kind           string `json:"kind"`
		FlowID         string `json:"flow_id"`
		FlowNeedsEvent bool   `json:"flow_needs_event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("esperaba 4 reglas, got %+v", out)
	}
	for _, dto := range out {
		want := dto.FlowID == flowDurable
		if dto.FlowNeedsEvent != want {
			t.Fatalf("kind=%s flow_id=%s flow_needs_event=%v, quiero %v", dto.Kind, dto.FlowID, dto.FlowNeedsEvent, want)
		}
	}
	if !strings.Contains(rec.Body.String(), `"flow_needs_event":true`) {
		t.Fatalf("al menos una fila debe salir marcada, body=%s", rec.Body.String())
	}
}

// TestListTriggers_500_CheckerFalla: si el checker no puede responder por algo
// DISTINTO de "flujo inexistente", el listado entero falla con 500 en vez de
// mentir omitiendo la marca de esa fila.
func TestListTriggers_500_CheckerFalla(t *testing.T) {
	store := trigger.NewMemoryStore()
	if _, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: ctxTenant, Kind: trigger.KindFallback, FlowID: flowDurable, Enabled: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	checker := erroringDurableChecker{err: errFalloDeliberado}

	rec := doTrigger(admin.ListTriggersHandler(store, checker), http.MethodGet, "/admin/triggers", ctxTenant, "", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
}
