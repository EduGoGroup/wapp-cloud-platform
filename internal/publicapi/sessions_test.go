package publicapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// sessionRow refleja la forma del DTO de GET /api/v1/sessions para los asserts.
type sessionRow struct {
	SessionID       string `json:"session_id"`
	EdgeID          string `json:"edge_id"`
	State           string `json:"state"`
	Role            string `json:"role"`
	SelfPn          string `json:"self_pn"`
	LastConnectedAt string `json:"last_connected_at"`
	LastSeenAt      string `json:"last_seen_at"`
}

// sessionsFixture arma sesiones para tenantA y tenantB en un fakeSessions.
func sessionsFixture() fakeSessions {
	ts := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	return fakeSessions{byTenant: map[string][]fleet.Session{
		tenantA: {{
			TenantID: tenantA, EdgeID: "edge-a", SessionID: "sess-a",
			State: fleet.StateOnline, Role: fleet.RoleBot, SelfPn: "15551234567",
			LastConnectedAt: ts, LastSeenAt: ts,
		}},
		tenantB: {{
			TenantID: tenantB, EdgeID: "edge-b", SessionID: "sess-b",
			State: fleet.StateOffline, Role: fleet.RolePassive,
		}},
	}}
}

func TestSessionsList_OK_WithScope(t *testing.T) {
	mux := newAPI(publicapi.Deps{Sessions: sessionsFixture()}, apiKeys())

	rec := call(mux, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var rows []sessionRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("filas=%d, quiero 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.SessionID != "sess-a" || got.EdgeID != "edge-a" || got.State != "online" ||
		got.Role != "bot" || got.SelfPn != "15551234567" {
		t.Fatalf("DTO inesperado: %+v", got)
	}
	if got.LastConnectedAt == "" || got.LastSeenAt == "" {
		t.Fatalf("timestamps ausentes: %+v", got)
	}
}

func TestSessionsList_401_NoToken(t *testing.T) {
	mux := newAPI(publicapi.Deps{Sessions: sessionsFixture()}, apiKeys())

	rec := call(mux, "", http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionsList_403_NoScope(t *testing.T) {
	mux := newAPI(publicapi.Deps{Sessions: sessionsFixture()}, apiKeys())

	// keyARead solo tiene flows.read → sessions.read denegado.
	rec := call(mux, keyARead, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionsList_TenantIsolation(t *testing.T) {
	mux := newAPI(publicapi.Deps{Sessions: sessionsFixture()}, apiKeys())

	// tenantB (con sessions.read) SOLO ve sus propias sesiones, nunca las de A.
	rec := call(mux, keyBSessions, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var rows []sessionRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "sess-b" {
		t.Fatalf("tenantB debe ver solo sess-b: %+v", rows)
	}
	for _, r := range rows {
		if r.SessionID == "sess-a" || r.EdgeID == "edge-a" {
			t.Fatalf("tenantB no debe ver sesiones de tenantA: %+v", rows)
		}
	}
}
