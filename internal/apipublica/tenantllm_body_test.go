//go:build pendiente

package apipublica_test

// tenantllm_body_test.go — la LECTURA DEL CUERPO de F2 (PUT /api/v1/tenant-llm) del contrato de
// MountTenantLLM: el techo de tamaño, el JSON que no lo es, el recorte de los campos de texto
// (y la clave, que NO se recorta) y el `tenant_id` del cuerpo, que no existe. Los dobles están
// en tenantllm_test.go.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

const (
	maxLLMBody   = 8192
	msgLLMTooBig = `{"error":"el cuerpo excede el tamaño máximo de 8192 bytes","max_bytes":8192}`
	msgLLMNotJS  = "el cuerpo debe ser un JSON {via, provider, model, api_key, consented}"
)

// paddedLLMBody devuelve un cuerpo VÁLIDO de exactamente size bytes: el JSON de siempre con
// espacios detrás, que encoding/json ignora.
func paddedLLMBody(t *testing.T, size int) string {
	t.Helper()
	body := llmJSON(t, validLLMBody())
	if len(body) > size {
		t.Fatalf("el cuerpo de prueba ya mide %d bytes, más que %d", len(body), size)
	}
	return body + strings.Repeat(" ", size-len(body))
}

// TestMountTenantLLM_Put_BodyCeiling: 8192 bytes pasan; uno más es 413 con la cifra, y no escribe.
func TestMountTenantLLM_Put_BodyCeiling(t *testing.T) {
	t.Run("exactly_the_ceiling_passes", func(t *testing.T) {
		h, store, cara := llmSetup(t)
		wantCode(t, "8192 bytes", putLLM(h, cara, paddedLLMBody(t, maxLLMBody)), http.StatusOK)
		if len(store.upserts) != 1 {
			t.Errorf("Upsert se llamó %d veces, quiero 1", len(store.upserts))
		}
	})
	t.Run("one_byte_over_is_413", func(t *testing.T) {
		h, store, cara := llmSetup(t)
		rec := putLLM(h, cara, paddedLLMBody(t, maxLLMBody+1))
		wantCode(t, "8193 bytes", rec, http.StatusRequestEntityTooLarge)
		wantExactBody(t, "8193 bytes", rec, msgLLMTooBig)
		if len(store.upserts) != 0 {
			t.Errorf("un cuerpo excesivo llegó al almacén (%d upserts, quiero 0)", len(store.upserts))
		}
	})
}

func TestMountTenantLLM_Put_NotTheExpectedJSON(t *testing.T) {
	for name, body := range map[string]string{
		"empty":                   "",
		"not_json":                "via=api",
		"truncated":               `{"via":"api"`,
		"array":                   `[]`,
		"via_is_a_number":         `{"via":5}`,
		"consented_is_a_string":   `{"via":"api","consented":"true"}`,
		"api_key_is_an_object":    `{"via":"api","api_key":{}}`,
		"two_documents_in_a_body": `{"via":"local"}{"via":"api"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, store, cara := llmSetup(t)
			rec := putLLM(h, cara, body)
			wantCode(t, name, rec, http.StatusBadRequest)
			wantErrorBody(t, name, rec, msgLLMNotJS)
			if len(store.upserts) != 0 {
				t.Errorf("%s: llegó al almacén (%d upserts, quiero 0)", name, len(store.upserts))
			}
		})
	}
}

// brokenBody es un cuerpo que falla al leerse.
type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("conexión cortada") }

func TestMountTenantLLM_Put_UnreadableBody(t *testing.T) {
	h, store, cara := llmSetup(t)
	req := httptest.NewRequest(http.MethodPut, llmTarget, io.NopCloser(brokenBody{}))
	req.Header.Set("Authorization", "Bearer "+h.With(tenantA, llmWritePerm))
	rec := httptest.NewRecorder()
	cara.ServeHTTP(rec, req)
	wantCode(t, "cuerpo ilegible", rec, http.StatusBadRequest)
	wantErrorBody(t, "cuerpo ilegible", rec, "no se pudo leer el cuerpo")
	if len(store.upserts) != 0 {
		t.Errorf("un cuerpo ilegible llegó al almacén (%d upserts, quiero 0)", len(store.upserts))
	}
}

// TestMountTenantLLM_Put_TrimsTextButNotTheKey: vía, proveedor y modelo se recortan; la clave
// llega al almacén byte a byte, con sus espacios: «arreglarla» guardaría en silencio algo
// distinto de lo que el tenant pegó.
func TestMountTenantLLM_Put_TrimsTextButNotTheKey(t *testing.T) {
	h, store, cara := llmSetup(t)
	const spacedKey = " " + llmKey + " \t"
	body := map[string]any{
		"via": "  api ", "provider": "\tanthropic\n", "model": "  " + llmModel + "  ",
		"api_key": spacedKey, "consented": true,
	}
	wantCode(t, "F2 con espacios", putLLM(h, cara, llmJSON(t, body)), http.StatusOK)
	if len(store.upserts) != 1 {
		t.Fatalf("Upsert se llamó %d veces, quiero 1", len(store.upserts))
	}
	got := store.upserts[0]
	want := tenantllm.Config{TenantID: tenantA, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: llmModel}
	if got.cfg != want {
		t.Errorf("Upsert recibió %+v, quiero %+v (vía, proveedor y modelo recortados)", got.cfg, want)
	}
	if got.apiKey != spacedKey {
		t.Error("la clave llegó al almacén recortada o normalizada; debe llegar byte a byte")
	}

	// El eco del rechazo también va recortado.
	rec := putLLM(h, cara, `{"via":"  remota "}`)
	wantExactBody(t, "vía desconocida con espacios", rec, `{"error":"invalid_via","via":"remota"}`)
}

// TestMountTenantLLM_Put_BodyTenantIsIgnored: un `tenant_id` ajeno en el cuerpo se descarta sin
// ruido y la operación va contra el tenant del token (INV-7).
func TestMountTenantLLM_Put_BodyTenantIsIgnored(t *testing.T) {
	h, store, cara := llmSetup(t)
	body := validLLMBody()
	body["tenant_id"] = tenantB
	wantCode(t, "F2 con tenant_id ajeno", putLLM(h, cara, llmJSON(t, body)), http.StatusOK)
	if got := store.upserts[0].cfg.TenantID; got != tenantA {
		t.Errorf("Upsert fue con el tenant %q, quiero el del token %q", got, tenantA)
	}
	if _, found := store.Row(tenantB); found {
		t.Error("el tenant_id del cuerpo escribió la fila de OTRO tenant")
	}
	if _, found := store.Row(tenantA); !found {
		t.Error("la fila del tenant del token no se escribió")
	}
}
