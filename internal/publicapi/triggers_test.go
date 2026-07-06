package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// triggerKeys añade a las api-keys base un juego con scopes triggers.* para
// tenantA/tenantB y una key de solo lectura de flujos (sin triggers).
func triggerKeys() map[string]in.ServiceIdentity {
	keys := apiKeys()
	keys["key-a-trig"] = in.ServiceIdentity{TenantID: tenantA, ClientID: "trig-a", Scopes: []string{"triggers.create", "triggers.read", "triggers.delete"}}
	keys["key-b-trig"] = in.ServiceIdentity{TenantID: tenantB, ClientID: "trig-b", Scopes: []string{"triggers.create", "triggers.read", "triggers.delete"}}
	return keys
}

const (
	keyATrig = "key-a-trig"
	keyBTrig = "key-b-trig"
)

func seedTrigger(t *testing.T, store *trigger.MemoryStore, tenantID, keyword string) trigger.Rule {
	t.Helper()
	r, err := store.Insert(context.Background(), trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindKeyword, Keyword: keyword,
		MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true,
	})
	if err != nil {
		t.Fatalf("seedTrigger: %v", err)
	}
	return r
}

func TestTriggersCreate_OK_WithScope(t *testing.T) {
	store := trigger.NewMemoryStore()
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	rec := call(mux, keyATrig, http.MethodPost, "/api/v1/triggers",
		`{"kind":"keyword","keyword":"pedido","flow_id":"carrito"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d, quiero 201; body=%s", rec.Code, rec.Body.String())
	}
	// Persistido bajo tenantA (del token).
	rules, err := store.List(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].TenantID != tenantA {
		t.Fatalf("no persistió bajo tenantA: %+v", rules)
	}
}

func TestTriggersCreate_401_NoToken(t *testing.T) {
	store := trigger.NewMemoryStore()
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	rec := call(mux, "", http.MethodPost, "/api/v1/triggers",
		`{"kind":"keyword","keyword":"pedido","flow_id":"carrito"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

func TestTriggersCreate_403_NoScope(t *testing.T) {
	store := trigger.NewMemoryStore()
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	// keyARead: flows.read NO cubre triggers.create.
	rec := call(mux, keyARead, http.MethodPost, "/api/v1/triggers",
		`{"kind":"keyword","keyword":"pedido","flow_id":"carrito"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTriggersCreate_400_InvalidBody(t *testing.T) {
	store := trigger.NewMemoryStore()
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	// kind keyword sin flow_id → 400.
	rec := call(mux, keyATrig, http.MethodPost, "/api/v1/triggers",
		`{"kind":"keyword","keyword":"pedido"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTriggersList_OK_TenantIsolation(t *testing.T) {
	store := trigger.NewMemoryStore()
	seedTrigger(t, store, tenantA, "pedido")
	seedTrigger(t, store, tenantB, "hola")
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	rec := call(mux, keyATrig, http.MethodGet, "/api/v1/triggers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out []struct {
		Keyword string `json:"keyword"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].Keyword != "pedido" {
		t.Fatalf("lista A = %+v, quiero solo [pedido] (aislamiento)", out)
	}
}

func TestTriggersDelete_OK_And_CrossTenant404(t *testing.T) {
	store := trigger.NewMemoryStore()
	ruleA := seedTrigger(t, store, tenantA, "pedido")
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	// tenantB intenta borrar la regla de tenantA → 404 (no filtra existencia).
	if rec := call(mux, keyBTrig, http.MethodDelete, "/api/v1/triggers/"+ruleA.TriggerID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	// tenantA la borra → 204.
	if rec := call(mux, keyATrig, http.MethodDelete, "/api/v1/triggers/"+ruleA.TriggerID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete code=%d, quiero 204; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTriggersDelete_403_NoScope(t *testing.T) {
	store := trigger.NewMemoryStore()
	ruleA := seedTrigger(t, store, tenantA, "pedido")
	mux := newAPI(publicapi.Deps{Triggers: store}, triggerKeys())

	// keyARead no tiene triggers.delete → 403 (antes de tocar el store).
	if rec := call(mux, keyARead, http.MethodDelete, "/api/v1/triggers/"+ruleA.TriggerID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

// glob del rol viewer: '*.read' cubre triggers.read.
func TestTriggersRead_ViewerGlob(t *testing.T) {
	store := trigger.NewMemoryStore()
	seedTrigger(t, store, tenantA, "pedido")
	keys := triggerKeys()
	keys["key-a-viewer"] = in.ServiceIdentity{TenantID: tenantA, ClientID: "viewer-a", Scopes: []string{"*.read"}}
	mux := newAPI(publicapi.Deps{Triggers: store}, keys)

	if rec := call(mux, "key-a-viewer", http.MethodGet, "/api/v1/triggers", ""); rec.Code != http.StatusOK {
		t.Fatalf("viewer glob code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	// pero *.read NO cubre triggers.create → 403.
	if rec := call(mux, "key-a-viewer", http.MethodPost, "/api/v1/triggers",
		`{"kind":"keyword","keyword":"x","flow_id":"f"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create code=%d, quiero 403", rec.Code)
	}
}
