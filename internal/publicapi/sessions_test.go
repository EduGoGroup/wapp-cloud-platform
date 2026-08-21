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
	Profile         string `json:"profile"`
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
			State: fleet.StateOnline, Profile: fleet.ProfileActive, SelfPn: "15551234567",
			LastConnectedAt: ts, LastSeenAt: ts,
		}},
		tenantB: {{
			TenantID: tenantB, EdgeID: "edge-b", SessionID: "sess-b",
			State: fleet.StateOffline, Profile: fleet.ProfilePassive,
		}},
	}}
}

func TestSessionsList_OK_WithScope(t *testing.T) {
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessionsFixture()}}, apiKeys())

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
		got.Profile != "active" || got.SelfPn != "15551234567" {
		t.Fatalf("DTO inesperado: %+v", got)
	}
	// Plan 046 · T1.2: el DTO publica `profile` SIN dejar de publicar `role`. Los
	// dos viajan juntos durante el ciclo de deprecación — el BFF y la plataforma no
	// se despliegan a la vez, y un BFF viejo se quedaría sin nada que pintar.
	if got.Profile != "active" {
		t.Fatalf("profile=%q, quiero \"active\" (¿se perdió el campo nuevo?): %+v", got.Profile, got)
	}
	if got.LastConnectedAt == "" || got.LastSeenAt == "" {
		t.Fatalf("timestamps ausentes: %+v", got)
	}
}

func TestSessionsList_401_NoToken(t *testing.T) {
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessionsFixture()}}, apiKeys())

	rec := call(mux, "", http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSessionsList_403_NoScope(t *testing.T) {
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessionsFixture()}}, apiKeys())

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
			State: fleet.StateOnline, Profile: fleet.ProfileActive,
			LastConnectedAt: ts, LastSeenAt: ts,
			// Socket muerto sostenido (degraded_since hace 6m) pero salud fresca (30s).
			WhatsappState: "dead", DegradedReason: "dek_load_timeout",
			DegradedSince: now.Add(-6 * time.Minute), LastHealthAt: now.Add(-30 * time.Second),
			OutboxDepth: 3, BinaryVersion: "v0.9.0",
		}},
	}}
	alerter := &recordingAlerter{}
	mux := newAPI(publicapi.Deps{
		SessionDeps: publicapi.SessionDeps{Sessions: sessions},
		Health:      publicapi.HealthRules{DegradedAfter: 5 * time.Minute, StaleAfter: 2 * time.Minute, Now: func() time.Time { return now }},
		Alerter:     alerter,
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
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessionsFixture()}, Alerter: alerter}, apiKeys())

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

// workerHealthKeys son las claves del bloque del worker (Plan 051 · T4.3) en el
// DTO de GET /api/v1/sessions.
var workerHealthKeys = []string{
	"worker_taskset", "intent_p50_ms", "intent_omitted_by_reason",
	"stuck_heads", "stuck_head_polls", "failed_seal_dispatch", "failed_seal_budget",
}

// rawRows decodifica la respuesta como objetos crudos para poder afirmar sobre la
// AUSENCIA de una clave (que es lo que significa «no lo sé»), no solo sobre su cero.
func rawRows(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("unmarshal crudo: %v", err)
	}
	return rows
}

// TestSessionsList_WorkerHealthUnknownIsOmitted es el test central de la regla de
// lectura del Plan 051 · T4.3 en la API: cuando el Edge NO SABE el bloque del
// worker, las claves NO APARECEN en el JSON. Nunca salen como 0 ni como "",
// porque un consumidor pintaría "0 ms" o "disjunta" sobre un dato inexistente.
func TestSessionsList_WorkerHealthUnknownIsOmitted(t *testing.T) {
	sessions := fakeSessions{byTenant: map[string][]fleet.Session{
		tenantA: {{
			TenantID: tenantA, EdgeID: "edge-a", SessionID: "sess-a",
			State: fleet.StateOnline, Profile: fleet.ProfileActive,
			WhatsappState: "connected", BinaryVersion: "v0.12.0",
			// Bloque del worker entero en «no lo sé»: mapa nil incluido.
		}},
	}}
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessions}}, apiKeys())

	rec := call(mux, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	rows := rawRows(t, rec.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("filas=%d, quiero 1", len(rows))
	}
	for _, k := range workerHealthKeys {
		if v, ok := rows[0][k]; ok {
			t.Fatalf("%q desconocido debe OMITIRSE, no viajar como %v", k, v)
		}
	}
	// intent_circuit vacío tampoco puede viajar: ausente ≠ "closed".
	if v, ok := rows[0]["intent_circuit"]; ok {
		t.Fatalf("intent_circuit vacío debe omitirse (ausente ≠ closed): %v", v)
	}
}

// TestSessionsList_WorkerHealthExposed cubre el criterio literal de T4.3: el
// BREAKER ABIERTO y el TASKSET se ven sin entrar en la máquina. Además fija que un
// 0 MEDIDO sí viaja (puntero presente) y que el desglose de motivos llega clave a
// clave, sin ningún total agregado (INV-051.3).
func TestSessionsList_WorkerHealthExposed(t *testing.T) {
	cero := int64(0)
	p50 := int64(1450)
	heads := int64(3)
	razones := map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1}
	// El breaker ya NO viaja siempre vacío (cloudlink >= v0.13.0), y
	// FailedSealBudget se deja SIN reportar para probar en la misma respuesta que
	// un 0 medido viaja y un desconocido se omite.
	sessions := fakeSessions{byTenant: map[string][]fleet.Session{
		tenantA: {{
			TenantID: tenantA, EdgeID: "edge-a", SessionID: "sess-a",
			State: fleet.StateOnline, Profile: fleet.ProfileActive, WhatsappState: "connected",
			IntentCircuit: "open", WorkerTaskset: "cajero_sin_confinar",
			IntentP50Ms: &p50, IntentOmittedByReason: razones,
			StuckHeads: &heads, FailedSealDispatch: &cero,
		}},
	}}
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessions}}, apiKeys())

	rec := call(mux, keyASessions, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	rows := rawRows(t, rec.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("filas=%d, quiero 1", len(rows))
	}
	got := rows[0]

	// Las dos mitades leen la MISMA respuesta ya decodificada: no se tocan entre sí,
	// no llevan t.Parallel y no rehacen la llamada (el criterio es que TODO esto se
	// cumpla a la vez, en una única respuesta).
	t.Run("breaker, taskset y el 0 MEDIDO", func(t *testing.T) {
		assertWorkerHealthEscalares(t, got)
	})
	t.Run("desglose clave a clave, sin agregados", func(t *testing.T) {
		assertWorkerHealthDesglose(t, got)
	})
}

// assertWorkerHealthEscalares cubre el criterio literal de T4.3 sobre los campos
// planos: el breaker y el taskset se ven sin entrar en la máquina, un 0 MEDIDO viaja
// y, en la MISMA respuesta, el que no se reportó sigue ausente.
func assertWorkerHealthEscalares(t *testing.T, got map[string]any) {
	t.Helper()
	// Criterio de T4.3: breaker y taskset visibles.
	if got["intent_circuit"] != "open" {
		t.Fatalf("el breaker abierto debe verse en la consola: %v", got["intent_circuit"])
	}
	if got["worker_taskset"] != "cajero_sin_confinar" {
		t.Fatalf("el taskset debe verse en la consola: %v", got["worker_taskset"])
	}
	if v, ok := got["intent_p50_ms"].(float64); !ok || int64(v) != 1450 {
		t.Fatalf("intent_p50_ms medido debe viajar: %v", got["intent_p50_ms"])
	}
	// Un 0 MEDIDO viaja (puntero presente): «no ocurrió» ≠ «no lo sé».
	if v, ok := got["failed_seal_dispatch"].(float64); !ok || v != 0 {
		t.Fatalf("un 0 medido debe viajar como 0, no omitirse: %v (presente=%v)",
			got["failed_seal_dispatch"], ok)
	}
	// El que NO se reportó sigue ausente, en la misma respuesta.
	if v, ok := got["failed_seal_budget"]; ok {
		t.Fatalf("failed_seal_budget no reportado debe omitirse: %v", v)
	}
}

// assertWorkerHealthDesglose fija que los motivos llegan clave a clave y que el DTO
// no cuela ningún total agregado (INV-051.3 / T3.12).
func assertWorkerHealthDesglose(t *testing.T, got map[string]any) {
	t.Helper()
	// Desglose clave a clave; ningún total agregado en el DTO.
	desglose, ok := got["intent_omitted_by_reason"].(map[string]any)
	if !ok {
		t.Fatalf("intent_omitted_by_reason debe viajar como objeto: %v", got["intent_omitted_by_reason"])
	}
	for k, want := range map[string]float64{"fastlane": 7, "presupuesto": 2, "breaker": 1} {
		if v, okk := desglose[k].(float64); !okk || v != want {
			t.Fatalf("motivo %q: got %v, want %v", k, desglose[k], want)
		}
	}
	if len(desglose) != 3 {
		t.Fatalf("el desglose no puede ganar ni perder claves (ni traer un total): %v", desglose)
	}
	for _, prohibida := range []string{"intent_omitted_total", "failed_seal_total", "stuck_total"} {
		if _, existe := got[prohibida]; existe {
			t.Fatalf("el DTO no puede exponer agregados (%q): rompe INV-051.3 / T3.12", prohibida)
		}
	}
}

func TestSessionsList_TenantIsolation(t *testing.T) {
	mux := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{Sessions: sessionsFixture()}}, apiKeys())

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

// --- Plan 046 · T1.2: las rutas de perfil, montadas de verdad ---

// apiConPerfiles monta la API con el eje de perfil cableado sobre un repo en
// memoria, y siembra la sesión sess-a del tenantA.
func apiConPerfiles(t *testing.T) (*testAPI, *fleet.MemoryRepository) {
	t.Helper()
	repo := fleet.NewMemoryRepository()
	if err := repo.MarkOnline(context.Background(), tenantA, "edge-a", "sess-a"); err != nil {
		t.Fatalf("seed sesión: %v", err)
	}
	api := newAPI(publicapi.Deps{SessionDeps: publicapi.SessionDeps{
		Sessions:        sessionsFixture(),
		SessionProfiles: repo,
		// ProfilePush queda nil: el hook nace apagado en T1.2 (lo enchufa T2.1).
	}}, apiKeys())
	return api, repo
}

// TestSessionProfile_RutaMontada: POST /api/v1/sessions/{id}/profile existe de
// verdad en el mux público (no solo el handler suelto) y fija el perfil.
func TestSessionProfile_RutaMontada(t *testing.T) {
	api, repo := apiConPerfiles(t)

	rec := call(api, keyASessionsW, http.MethodPost, "/api/v1/sessions/sess-a/profile", `{"profile":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	s, _, err := repo.Get(context.Background(), tenantA, "edge-a", "sess-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Profile != fleet.ProfileActive {
		t.Fatalf("no persistió active: %q", s.Profile)
	}
}

// TestSessionProfile_403_SinScopeDeEscritura: la ruta exige sessions.write, igual
// que tenía la ruta vieja. Una credencial de solo lectura no cambia perfiles.
func TestSessionProfile_403_SinScopeDeEscritura(t *testing.T) {
	api, _ := apiConPerfiles(t)

	rec := call(api, keyASessions, http.MethodPost, "/api/v1/sessions/sess-a/profile", `{"profile":"active"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSessionProfile_400_Bot_EnLaRutaReal: `bot` era el vocabulario de la ruta
// retirada en la 0064. Contra
// /profile es 400 también a través del mux montado.
func TestSessionProfile_400_Bot_EnLaRutaReal(t *testing.T) {
	api, _ := apiConPerfiles(t)

	rec := call(api, keyASessionsW, http.MethodPost, "/api/v1/sessions/sess-a/profile", `{"profile":"bot"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}
