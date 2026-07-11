package publicapi_test

import (
	"context"
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
	Health          string `json:"health"`
	WhatsappState   string `json:"whatsapp_state"`
	DegradedReason  string `json:"degraded_reason"`
	DegradedSince   string `json:"degraded_since"`
	LastHealthAt    string `json:"last_health_at"`
	OutboxDepth     int64  `json:"outbox_depth"`
	BinaryVersion   string `json:"binary_version"`
}

// recordingAlerter cuenta las invocaciones del seam del alerting push (ADR-0023).
type recordingAlerter struct{ calls []string }

func (a *recordingAlerter) Alert(_ context.Context, _, sessionID, state string) error {
	a.calls = append(a.calls, sessionID+":"+state)
	return nil
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

// TestSessionsList_HealthEnriched verifica que GET /api/v1/sessions expone el
// snapshot de salud y el estado derivado (Plan 031 · T4): una sesión degradada
// sostenida se sirve health=degraded con su whatsapp_state/motivo, y el seam del
// alerting push (no-op) se invoca por ella.
func TestSessionsList_HealthEnriched(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-10 * time.Minute)
	sessions := fakeSessions{byTenant: map[string][]fleet.Session{
		tenantA: {{
			TenantID: tenantA, EdgeID: "edge-a", SessionID: "sess-a",
			State: fleet.StateOnline, Role: fleet.RoleBot,
			LastConnectedAt: ts, LastSeenAt: ts,
			// Socket muerto sostenido (degraded_since hace 6m) pero salud fresca (30s).
			WhatsappState: "dead", DegradedReason: "dek_load_timeout",
			DegradedSince: now.Add(-6 * time.Minute), LastHealthAt: now.Add(-30 * time.Second),
			OutboxDepth: 3, BinaryVersion: "v0.9.0",
		}},
	}}
	alerter := &recordingAlerter{}
	mux := newAPI(publicapi.Deps{
		Sessions: sessions,
		Health:   publicapi.HealthRules{DegradedAfter: 5 * time.Minute, StaleAfter: 2 * time.Minute, Now: func() time.Time { return now }},
		Alerter:  alerter,
	}, apiKeys())

	rec := call(mux, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var rows []sessionRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("filas=%d, quiero 1", len(rows))
	}
	got := rows[0]
	if got.Health != "degraded" {
		t.Fatalf("health=%q, quiero degraded", got.Health)
	}
	if got.State != "online" {
		t.Fatalf("el link state debe seguir online (no ambiguo): %q", got.State)
	}
	if got.WhatsappState != "dead" || got.DegradedReason != "dek_load_timeout" ||
		got.DegradedSince == "" || got.LastHealthAt == "" ||
		got.OutboxDepth != 3 || got.BinaryVersion != "v0.9.0" {
		t.Fatalf("snapshot de salud incompleto: %+v", got)
	}
	if len(alerter.calls) != 1 || alerter.calls[0] != "sess-a:degraded" {
		t.Fatalf("el seam del alerting debe invocarse una vez por la sesión degradada: %v", alerter.calls)
	}
}

// TestSessionsList_HealthOmittedWhenAbsent verifica que una sesión sin salud
// reportada (Edge viejo) NO trae campos de salud ni estado derivado.
func TestSessionsList_HealthOmittedWhenAbsent(t *testing.T) {
	alerter := &recordingAlerter{}
	mux := newAPI(publicapi.Deps{Sessions: sessionsFixture(), Alerter: alerter}, apiKeys())

	rec := call(mux, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200", rec.Code)
	}
	var rows []sessionRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("filas=%d, quiero 1", len(rows))
	}
	if rows[0].Health != "" || rows[0].WhatsappState != "" || rows[0].LastHealthAt != "" {
		t.Fatalf("sesión sin salud no debe traer campos de salud: %+v", rows[0])
	}
	if len(alerter.calls) != 0 {
		t.Fatalf("sin salud derivada no debe invocarse el alerting: %v", alerter.calls)
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
