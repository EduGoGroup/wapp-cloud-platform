//go:build pendiente

package apipublica_test

// tenantllm_put_test.go — la SEMÁNTICA de F2 (PUT /api/v1/tenant-llm) del contrato de
// MountTenantLLM: los dos ejes (vía y proveedor), el orden de las comprobaciones, lo que llega al
// almacén y los fallos de este. Los dobles están en tenantllm_test.go.

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

const (
	msgLLMConsent = `{"detail":"hay que consentir explícitamente (consented:true) que el texto de las conversaciones salga hacia el proveedor externo","error":"consent_required"}`
	msgLLMModel   = `{"error":"model es obligatorio y no puede pasar de 128 caracteres"}`
	msgLLMKey     = `{"error":"api_key es obligatoria en cada PUT y debe tener entre 16 y 512 caracteres"}`
)

// TestMountTenantLLM_Put_APIRouteStores: el camino feliz de la vía api. Al almacén llegan el
// tenant del token, los dos ejes, la clave tal cual y el instante del consentimiento puesto por
// el servidor; la respuesta es la fila RELEÍDA.
func TestMountTenantLLM_Put_APIRouteStores(t *testing.T) {
	h, store, cara := llmSetup(t)
	before := time.Now()
	rec := putLLM(h, cara, llmJSON(t, validLLMBody()))
	after := time.Now()

	wantCode(t, "F2", rec, http.StatusOK)
	if len(store.upserts) != 1 {
		t.Fatalf("Upsert se llamó %d veces, quiero 1", len(store.upserts))
	}
	got := store.upserts[0]
	want := tenantllm.Config{TenantID: tenantA, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: llmModel}
	if got.cfg != want {
		t.Errorf("Upsert recibió %+v, quiero %+v", got.cfg, want)
	}
	if got.apiKey != llmKey {
		t.Error("Upsert no recibió la clave tal cual vino en el cuerpo")
	}
	if got.consentedAt.Before(before) || got.consentedAt.After(after) || got.consentedAt.Location() != time.UTC {
		t.Errorf("consentedAt = %s; quiero el instante de la petición, en UTC (entre %s y %s)", got.consentedAt, before, after)
	}

	var body map[string]any
	wantJSON(t, "F2", rec, &body)
	for key, want := range map[string]any{"configured": true, "via": "api", "provider": "anthropic", "model": llmModel, "key_set": true} {
		if body[key] != want {
			t.Errorf("F2: %q = %v, quiero %v", key, body[key], want)
		}
	}
	// Los instantes salen de la fila releída: si el handler devolviera lo que mandó, faltarían.
	for _, key := range []string{"consented_at", "created_at", "updated_at"} {
		if _, err := time.Parse(time.RFC3339, body[key].(string)); err != nil {
			t.Errorf("F2: %q = %v, quiero un instante RFC 3339 de la fila releída", key, body[key])
		}
	}
	if len(body) != 8 {
		t.Errorf("F2: el cuerpo tiene %d claves (%v), quiero las 8 del contrato", len(body), body)
	}
}

// TestMountTenantLLM_Put_ReplacesTheWholeRow: un PUT sobre una fila existente la sustituye; la
// clave es obligatoria en cada uno, no hay forma de conservar la anterior.
func TestMountTenantLLM_Put_ReplacesTheWholeRow(t *testing.T) {
	h, store, cara := llmSetup(t)
	wantCode(t, "primer F2", putLLM(h, cara, llmJSON(t, validLLMBody())), http.StatusOK)

	second := validLLMBody()
	second["provider"], second["model"], second["api_key"] = tenantllm.ProviderGemini, "otro-modelo", "otra-clave-de-prueba-9876543210"
	rec := putLLM(h, cara, llmJSON(t, second))
	wantCode(t, "segundo F2", rec, http.StatusOK)
	var body map[string]any
	wantJSON(t, "segundo F2", rec, &body)
	if body["provider"] != tenantllm.ProviderGemini || body["model"] != "otro-modelo" {
		t.Errorf("segundo F2: responde %v, quiero el proveedor y el modelo nuevos", body)
	}
	if got := store.upserts[1].apiKey; got != "otra-clave-de-prueba-9876543210" {
		t.Error("el segundo Upsert no recibió la clave nueva")
	}

	// Sin clave no hay «conserva la anterior»: es 400 y la fila queda como estaba.
	delete(second, "api_key")
	rec = putLLM(h, cara, llmJSON(t, second))
	wantCode(t, "tercer F2, sin clave", rec, http.StatusBadRequest)
	if len(store.upserts) != 2 {
		t.Errorf("el PUT sin clave llegó al almacén (%d upserts, quiero 2)", len(store.upserts))
	}
}

// TestMountTenantLLM_Put_LocalRouteRequiresNothing: `via:"local"` no exige consentimiento,
// proveedor, modelo ni clave (REQ-33), y lo que el cuerpo traiga del eje api no se guarda: la
// clave ni cruza la llamada al almacén.
func TestMountTenantLLM_Put_LocalRouteRequiresNothing(t *testing.T) {
	withAPIAxis := validLLMBody()
	withAPIAxis["via"] = tenantllm.ViaLocal
	for name, body := range map[string]map[string]any{
		"bare":                     {"via": tenantllm.ViaLocal},
		"api_axis_is_not_stored":   withAPIAxis,
		"unknown_provider_ignored": {"via": tenantllm.ViaLocal, "provider": "inventado", "consented": false},
	} {
		t.Run(name, func(t *testing.T) {
			h, store, cara := llmSetup(t)
			rec := putLLM(h, cara, llmJSON(t, body))
			wantCode(t, name, rec, http.StatusOK)
			if len(store.upserts) != 1 {
				t.Fatalf("Upsert se llamó %d veces, quiero 1: la vía local se guarda", len(store.upserts))
			}
			got := store.upserts[0]
			if want := (tenantllm.Config{TenantID: tenantA, Via: tenantllm.ViaLocal}); got.cfg != want {
				t.Errorf("Upsert recibió %+v, quiero %+v (sin proveedor ni modelo)", got.cfg, want)
			}
			if got.apiKey != "" || !got.consentedAt.IsZero() {
				t.Errorf("a Upsert le llegó una clave (%t) o un consentimiento (%s); en la vía local no cruzan la llamada",
					got.apiKey != "", got.consentedAt)
			}
			var out map[string]any
			wantJSON(t, name, rec, &out)
			if out["configured"] != true || out["via"] != "local" || out["key_set"] != false {
				t.Errorf("%s: responde %v, quiero configured true, via local, key_set false", name, out)
			}
			for _, key := range []string{"provider", "model", "consented_at"} {
				if _, present := out[key]; present {
					t.Errorf("%s: la respuesta lleva %q; la fila local no tiene eje api", name, key)
				}
			}
		})
	}
}

// TestMountTenantLLM_Put_Rejections: cada rechazo de forma, con su cuerpo byte a byte y en el
// orden del contrato (vía → consentimiento → proveedor → modelo → clave). Ninguno escribe.
func TestMountTenantLLM_Put_Rejections(t *testing.T) {
	cases := []struct {
		name string
		edit func(body map[string]any)
		want string
	}{
		{"old_shape_without_via", func(b map[string]any) { delete(b, "via") }, `{"error":"invalid_via","via":""}`},
		{"unknown_via", func(b map[string]any) { b["via"] = "remota" }, `{"error":"invalid_via","via":"remota"}`},
		{"via_is_case_sensitive", func(b map[string]any) { b["via"] = "API" }, `{"error":"invalid_via","via":"API"}`},
		{"via_goes_before_consent", func(b map[string]any) { delete(b, "via"); b["consented"] = false }, `{"error":"invalid_via","via":""}`},
		{"consent_missing", func(b map[string]any) { delete(b, "consented") }, msgLLMConsent},
		{"consent_false", func(b map[string]any) { b["consented"] = false }, msgLLMConsent},
		{"consent_goes_before_provider", func(b map[string]any) { b["consented"] = false; b["provider"] = "inventado" }, msgLLMConsent},
		{"unknown_provider", func(b map[string]any) { b["provider"] = "inventado" }, `{"error":"invalid_provider","provider":"inventado"}`},
		{"provider_missing", func(b map[string]any) { delete(b, "provider") }, `{"error":"invalid_provider","provider":""}`},
		{"provider_local_is_contradictory", func(b map[string]any) { b["provider"] = tenantllm.ProviderLocal }, `{"error":"invalid_provider","provider":"local"}`},
		{"provider_goes_before_model", func(b map[string]any) { b["provider"] = "inventado"; delete(b, "model") }, `{"error":"invalid_provider","provider":"inventado"}`},
		{"model_missing", func(b map[string]any) { delete(b, "model") }, msgLLMModel},
		{"model_blank", func(b map[string]any) { b["model"] = "   " }, msgLLMModel},
		{"model_129_bytes", func(b map[string]any) { b["model"] = strings.Repeat("m", 129) }, msgLLMModel},
		{"model_goes_before_key", func(b map[string]any) { delete(b, "model"); delete(b, "api_key") }, msgLLMModel},
		{"key_missing", func(b map[string]any) { delete(b, "api_key") }, msgLLMKey},
		{"key_empty", func(b map[string]any) { b["api_key"] = "" }, msgLLMKey},
		{"key_15_bytes", func(b map[string]any) { b["api_key"] = strings.Repeat("k", 15) }, msgLLMKey},
		{"key_513_bytes", func(b map[string]any) { b["api_key"] = strings.Repeat("k", 513) }, msgLLMKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := llmSetup(t)
			body := validLLMBody()
			tc.edit(body)
			rec := putLLM(h, cara, llmJSON(t, body))
			wantCode(t, tc.name, rec, http.StatusBadRequest)
			wantExactBody(t, tc.name, rec, tc.want)
			if len(store.upserts) != 0 {
				t.Errorf("%s: un PUT rechazado llegó al almacén (%d upserts, quiero 0)", tc.name, len(store.upserts))
			}
		})
	}
}

// TestMountTenantLLM_Put_AcceptedBoundaries: los bordes que SÍ pasan, y el otro proveedor.
func TestMountTenantLLM_Put_AcceptedBoundaries(t *testing.T) {
	cases := map[string]func(body map[string]any){
		"model_128_bytes": func(b map[string]any) { b["model"] = strings.Repeat("m", 128) },
		"key_16_bytes":    func(b map[string]any) { b["api_key"] = strings.Repeat("k", 16) },
		"key_512_bytes":   func(b map[string]any) { b["api_key"] = strings.Repeat("k", 512) },
		"provider_gemini": func(b map[string]any) { b["provider"] = tenantllm.ProviderGemini },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			h, store, cara := llmSetup(t)
			body := validLLMBody()
			edit(body)
			wantCode(t, name, putLLM(h, cara, llmJSON(t, body)), http.StatusOK)
			if len(store.upserts) != 1 {
				t.Errorf("%s: Upsert se llamó %d veces, quiero 1", name, len(store.upserts))
			}
		})
	}
}

// TestMountTenantLLM_Put_StoreFailures: guardar y releer fallan con mensajes distintos (el
// segundo dice que la fila SÍ quedó guardada), y los dos quedan auditados como fallo.
func TestMountTenantLLM_Put_StoreFailures(t *testing.T) {
	down := errors.New("postgres://usuario:secreto@host/bd: conexión rechazada")
	cases := []struct {
		name    string
		arrange func(s *llmStoreSpy)
		want    string
	}{
		{"upsert_fails", func(s *llmStoreSpy) { s.upsertErr = down }, "no se pudo guardar la configuración LLM"},
		{"reread_fails", func(s *llmStoreSpy) { s.rereadErr = down }, "configuración LLM guardada, pero no se pudo releer"},
		{"reread_finds_no_row", func(s *llmStoreSpy) { s.dropUpserts = true }, "configuración LLM guardada, pero no se pudo releer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := llmSetup(t)
			tc.arrange(store)
			rec := putLLM(h, cara, llmJSON(t, validLLMBody()))
			wantCode(t, tc.name, rec, http.StatusInternalServerError)
			wantErrorBody(t, tc.name, rec, tc.want)
			records := h.Auditor().Records()
			if len(records) != 1 || records[0].Result != "failure" || records[0].Meta["status"] != http.StatusInternalServerError {
				t.Errorf("%s: auditoría %+v, quiero exactamente un registro failure con status 500", tc.name, records)
			}
		})
	}
}
