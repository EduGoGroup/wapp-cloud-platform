package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// keyAEventTelemetry: tenantA con events_telemetry.read.
const keyAEventTelemetry = "key-a-event-telemetry"

func eventTelemetryKeys() map[string]testIdentity {
	keys := apiKeys()
	keys[keyAEventTelemetry] = testIdentity{TenantID: tenantA, Subject: "ops-a", Grants: []string{"events_telemetry.read"}}
	return keys
}

// fakeEventTelemetryReader captura el tenant y el filtro con el que se llamó,
// y devuelve las filas fijadas en `rows` (todas, sin paginar de verdad: el
// fake no simula el store, solo el puerto).
type fakeEventTelemetryReader struct {
	rows      []publicapi.EventTelemetryRow
	err       error
	gotTenant string
	gotFilter publicapi.EventTelemetryFilter
	called    bool
}

func (f *fakeEventTelemetryReader) ListEventTelemetry(_ context.Context, tenantID string, filter publicapi.EventTelemetryFilter) ([]publicapi.EventTelemetryRow, error) {
	f.called = true
	f.gotTenant = tenantID
	f.gotFilter = filter
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func rowAt(id int64, name, kind string, at time.Time) publicapi.EventTelemetryRow {
	return publicapi.EventTelemetryRow{
		ID: id, Name: name, EventKind: kind,
		Payload:   json.RawMessage(`{"history_id":"h","kind":"` + kind + `"}`),
		CreatedAt: at,
	}
}

// TestEventTelemetry_SinAuth_401 fija INV-8: sin identidad, ni una fila.
func TestEventTelemetry_SinAuth_401(t *testing.T) {
	reader := &fakeEventTelemetryReader{}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, "", http.MethodGet, "/api/v1/events/telemetry", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("el store NO debe consultarse sin identidad")
	}
}

// TestEventTelemetry_SinGrant_403: la credencial existe pero no tiene el scope.
func TestEventTelemetry_SinGrant_403(t *testing.T) {
	reader := &fakeEventTelemetryReader{}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyARead, http.MethodGet, "/api/v1/events/telemetry", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEventTelemetry_OK_TenantDelTokenYDefaults fija INV-8 (el tenant SALE del
// token, nunca de la query) y los defaults de paginación.
func TestEventTelemetry_OK_TenantDelTokenYDefaults(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeEventTelemetryReader{rows: []publicapi.EventTelemetryRow{
		rowAt(1, "event_started", "cart", now),
	}}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet,
		"/api/v1/events/telemetry?tenant_id="+tenantB, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if reader.gotTenant != tenantA {
		t.Fatalf("tenant consultado=%q, quiero %q (el de la query NUNCA debe colarse, INV-8)", reader.gotTenant, tenantA)
	}
	if reader.gotFilter.Limit != 101 {
		// El handler pide Limit+1 (default 100) para saber si hay página siguiente.
		t.Fatalf("filter.Limit=%d, quiero 101 (default 100 + 1)", reader.gotFilter.Limit)
	}

	var resp struct {
		Events []struct {
			ID        int64           `json:"id"`
			Name      string          `json:"name"`
			EventKind string          `json:"event_kind"`
			Payload   json.RawMessage `json:"payload"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].Name != "event_started" || resp.Events[0].EventKind != "cart" {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
	if resp.NextCursor != "" {
		t.Fatalf("con 1 fila y limit=100 no debería haber next_cursor: %q", resp.NextCursor)
	}
	if resp.Limit != 100 {
		t.Fatalf("limit en la respuesta=%d, quiero el default 100", resp.Limit)
	}
}

// TestEventTelemetry_PaginaConCursor_DescartaLaFilaDeSobra fija el patrón
// "pide Limit+1, descarta la última si sobra": con limit=2 y 3 filas
// devueltas por el store, la respuesta debe traer SOLO 2 y un next_cursor no
// vacío que codifica la 2ª fila (NO la 3ª, que se descarta).
func TestEventTelemetry_PaginaConCursor_DescartaLaFilaDeSobra(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	reader := &fakeEventTelemetryReader{rows: []publicapi.EventTelemetryRow{
		rowAt(1, "event_started", "cart", base),
		rowAt(2, "event_started", "cart", base.Add(time.Second)),
		rowAt(3, "event_closed", "cart", base.Add(2*time.Second)),
	}}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet, "/api/v1/events/telemetry?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if reader.gotFilter.Limit != 3 {
		t.Fatalf("filter.Limit=%d, quiero 3 (limit=2 + 1)", reader.gotFilter.Limit)
	}

	var resp struct {
		Events []struct {
			ID int64 `json:"id"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 2 || resp.Events[0].ID != 1 || resp.Events[1].ID != 2 {
		t.Fatalf("página inesperada: %+v", resp.Events)
	}
	if resp.NextCursor == "" {
		t.Fatal("con 3 filas devueltas para limit=2 debe haber next_cursor")
	}

	// El cursor devuelto tiene que llevar a la SIGUIENTE fila (id=3), no a la
	// última recibida (que ya se sirvió).
	rec2 := call(mux, keyAEventTelemetry, http.MethodGet,
		"/api/v1/events/telemetry?limit=2&cursor="+resp.NextCursor, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("segunda página: code=%d, body=%s", rec2.Code, rec2.Body.String())
	}
	if reader.gotFilter.CursorID != 2 {
		t.Fatalf("cursor decodificado id=%d, quiero 2 (la última fila SERVIDA, no la descartada)", reader.gotFilter.CursorID)
	}
}

// TestEventTelemetry_LimitExcedeCota_422 fija la enmienda #3 (cota con ERROR,
// no truncado silencioso), reusando intakes.MaxExportIntakes.
func TestEventTelemetry_LimitExcedeCota_422(t *testing.T) {
	reader := &fakeEventTelemetryReader{}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet, "/api/v1/events/telemetry?limit=5001", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	if reader.called {
		t.Fatal("con limit por encima de la cota, el store NUNCA debe consultarse")
	}
}

// TestEventTelemetry_LimitJustoEnLaCota_OK: exactamente la cota sí pasa (sin
// este test, un `>=` en vez de `>` en la comparación pasaría inadvertido).
func TestEventTelemetry_LimitJustoEnLaCota_OK(t *testing.T) {
	reader := &fakeEventTelemetryReader{}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet, "/api/v1/events/telemetry?limit=5000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if !reader.called {
		t.Fatal("con limit exactamente en la cota, el store SÍ debe consultarse")
	}
}

// TestEventTelemetry_SinceInvalido_400 y TestEventTelemetry_CursorInvalido_400
// fijan que un parámetro mal escrito se DICE (400), nunca se ignora.
func TestEventTelemetry_SinceInvalido_400(t *testing.T) {
	d := publicapi.Deps{EventTelemetry: &fakeEventTelemetryReader{}}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet, "/api/v1/events/telemetry?since=ayer", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestEventTelemetry_CursorInvalido_400(t *testing.T) {
	d := publicapi.Deps{EventTelemetry: &fakeEventTelemetryReader{}}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet, "/api/v1/events/telemetry?cursor=no-es-base64-valido!!", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEventTelemetry_Since_SePasaAlFiltro fija que `since` SÍ llega al store.
func TestEventTelemetry_Since_SePasaAlFiltro(t *testing.T) {
	reader := &fakeEventTelemetryReader{}
	d := publicapi.Deps{EventTelemetry: reader}
	mux := newAPI(d, eventTelemetryKeys())

	rec := call(mux, keyAEventTelemetry, http.MethodGet,
		"/api/v1/events/telemetry?since=2026-08-01T00:00:00Z", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !reader.gotFilter.Since.Equal(want) {
		t.Fatalf("since=%v, quiero %v", reader.gotFilter.Since, want)
	}
}

// TestEventTelemetry_SinStore_NoSeMonta: sin EventTelemetry cableado, la ruta
// NO existe (404 de ruta inexistente, mejor que un 500 a medio camino — mismo
// criterio que el resto de rutas opcionales de este archivo).
func TestEventTelemetry_SinStore_NoSeMonta(t *testing.T) {
	mux := newAPI(publicapi.Deps{}, eventTelemetryKeys())
	rec := call(mux, keyAEventTelemetry, http.MethodGet, "/api/v1/events/telemetry", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (ruta no montada); body=%s", rec.Code, rec.Body.String())
	}
}
