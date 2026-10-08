//go:build pendiente

package apipublica_test

// integrations_put_test.go — la semántica de G15 (PUT /api/v1/integrations) del contrato de
// MountIntegrations: el upsert completo, el secreto write-only, el tenant del token y, rama por
// rama, lo que se rechaza. Es un trozo de integrations_test.go, partido por tema (05 E-13).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

const (
	integrationMsgShape     = "el cuerpo debe ser un JSON {catalog_adapter, events_adapter, endpoint_url, secret, enabled}"
	integrationMsgHTTP      = "catalog.pull diferido: el adaptador de catálogo «http» todavía no está implementado; usa «local»"
	integrationMsgCatalog   = "catalog_adapter debe ser «local» o «webhook»"
	integrationMsgEvents    = "events_adapter debe ser «local» o «webhook»"
	integrationMsgLongURL   = "endpoint_url es demasiado larga"
	integrationMsgBadURL    = "endpoint_url debe ser una URL absoluta http(s)"
	integrationMsgSignerLen = "el secreto de firma debe tener entre 24 y 256 caracteres"
	integrationMsgNoURL     = "un puente webhook encendido necesita endpoint_url"
	integrationMsgNoSigner  = "un puente webhook encendido necesita un secreto de firma"
	integrationMsgCheck     = "no se pudo comprobar la integración actual"
	integrationMsgSave      = "no se pudo guardar la integración"
	integrationMsgReread    = "integración guardada, pero no se pudo releer"
)

// integrationValidBody es un PUT admisible de un puente webhook encendido y completo, como mapa
// para que cada test lo retoque.
func integrationValidBody() map[string]any {
	return map[string]any{
		"catalog_adapter": "local", "events_adapter": "webhook",
		"endpoint_url": integrationEndpoint, "secret": integrationFakeSigner, "enabled": true,
	}
}

// integrationJSON serializa el cuerpo de un PUT.
func integrationJSON(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("serializando el cuerpo de prueba: %v", err)
	}
	return string(raw)
}

// putIntegration hace el PUT como tenantA con el permiso de escritura.
func putIntegration(h *apipublicahelpertest.Harness, cara http.Handler, body string) *httptest.ResponseRecorder {
	return h.Call(cara, h.With(tenantA, integrationWritePerm), http.MethodPut, integrationTarget, body)
}

// TestMountIntegrations_PutIsAFullUpsert: crea y responde la fila RELEÍDA en la forma de G13; un
// segundo PUT sin secreto conserva el que había (misma huella) y lo que no viene vuelve a su
// default.
func TestMountIntegrations_PutIsAFullUpsert(t *testing.T) {
	h, store, cara := integrationSetup(t)
	fingerprint := integrations.Fingerprint(integrationFakeSigner)

	rec := putIntegration(h, cara, integrationJSON(t, integrationValidBody()))
	wantCode(t, "G15 alta", rec, http.StatusOK)
	wantExactBody(t, "G15 alta", rec, `{"configured":true,"catalog_adapter":"local","events_adapter":"webhook",`+
		`"endpoint_url":"`+integrationEndpoint+`","enabled":true,"secret_set":true,"secret_fingerprint":"`+fingerprint+`",`+
		`"created_at":"2026-09-01T12:00:00Z","updated_at":"2026-09-01T12:00:00Z"}`)

	// Reconfigurar el endpoint SIN reenviar el secreto: el silencio es «déjalo como está».
	rec = putIntegration(h, cara, `{"events_adapter":"webhook","endpoint_url":"http://localhost:9009/otro","secret":"","enabled":true}`)
	wantCode(t, "G15 sin secreto", rec, http.StatusOK)
	if body := rec.Body.String(); !strings.Contains(body, `"secret_fingerprint":"`+fingerprint+`"`) ||
		!strings.Contains(body, `"endpoint_url":"http://localhost:9009/otro"`) {
		t.Errorf("el PUT sin secreto debe conservar la huella y cambiar el endpoint: %s", body)
	}

	// Lo que no viene toma el default: {} deja local/local apagado y sin endpoint, pero el PUT
	// nunca borra el secreto.
	rec = putIntegration(h, cara, `{}`)
	wantCode(t, "G15 vacío", rec, http.StatusOK)
	row, found := store.IntegrationRow(tenantA)
	if !found || row.CatalogAdapter != "local" || row.EventsAdapter != "local" || row.EndpointURL != "" || row.Enabled || !row.HasSecret {
		t.Errorf("tras {} quiero local/local apagado, sin endpoint y CON el secreto de antes: %+v (found=%v)", row, found)
	}
	if body := rec.Body.String(); strings.Contains(body, "endpoint_url") || !strings.Contains(body, `"secret_set":true`) {
		t.Errorf("la respuesta de {} no es la fila releída: %s", body)
	}
}

// TestMountIntegrations_PutTrimsAdaptersAndEndpointNotTheSecret: los bordes de los adaptadores
// y del endpoint se recortan antes de validar y de guardar; el secreto se guarda tal cual.
func TestMountIntegrations_PutTrimsAdaptersAndEndpointNotTheSecret(t *testing.T) {
	h, store, cara := integrationSetup(t)
	spaced := "  " + integrationFakeSigner + "  "
	body := integrationJSON(t, map[string]any{
		"catalog_adapter": "  ", "events_adapter": "\twebhook\n", "endpoint_url": "  " + integrationEndpoint + " ",
		"secret": spaced, "enabled": true,
	})
	rec := putIntegration(h, cara, body)
	wantCode(t, "G15 con espacios", rec, http.StatusOK)
	row, _ := store.IntegrationRow(tenantA)
	if row.CatalogAdapter != "local" || row.EventsAdapter != "webhook" || row.EndpointURL != integrationEndpoint {
		t.Errorf("no se recortó lo que tocaba: %+v", row)
	}
	if stored, _, err := store.GetTenantSecret(context.Background(), tenantA); err != nil || stored != spaced {
		t.Error("el secreto se guardó recortado: un espacio en un secreto es parte del secreto")
	}
}

// TestMountIntegrations_PutTenantComesFromTheToken (INV-8): un tenant_id en el cuerpo o en la
// query no existe para G15.
func TestMountIntegrations_PutTenantComesFromTheToken(t *testing.T) {
	h, store, cara := integrationSetup(t)
	store.seed(t, integrations.TenantIntegration{TenantID: tenantB, CatalogAdapter: "local", EventsAdapter: "local"}, "")
	body := integrationValidBody()
	body["tenant_id"] = tenantB
	rec := h.Call(cara, h.With(tenantA, integrationWritePerm), http.MethodPut, integrationTarget+"?tenant_id="+tenantB,
		integrationJSON(t, body))
	wantCode(t, "G15", rec, http.StatusOK)
	if row, found := store.IntegrationRow(tenantA); !found || row.EventsAdapter != "webhook" {
		t.Errorf("la fila de tenantA no quedó escrita: %+v (found=%v)", row, found)
	}
	if row, _ := store.IntegrationRow(tenantB); row.EventsAdapter != "local" || row.Enabled || row.HasSecret {
		t.Errorf("el PUT de tenantA escribió en tenantB: %+v", row)
	}
	if strings.Contains(rec.Body.String(), tenantB) {
		t.Errorf("la respuesta nombra al otro tenant: %s", rec.Body.String())
	}
}

// TestMountIntegrations_PutRejections: lo que la BD no puede rechazar sola, rama por rama, con
// entradas adversarias. Nada de lo rechazado se guarda.
func TestMountIntegrations_PutRejections(t *testing.T) {
	set := func(key string, value any) func(map[string]any) {
		return func(m map[string]any) { m[key] = value }
	}
	cases := []struct {
		name   string
		change func(map[string]any)
		code   int
		msg    string
	}{
		{"catalog_http_is_deferred", set("catalog_adapter", "http"), http.StatusUnprocessableEntity, integrationMsgHTTP},
		{"catalog_unknown", set("catalog_adapter", "ftp"), http.StatusBadRequest, integrationMsgCatalog},
		{"catalog_uppercase_is_not_the_vocabulary", set("catalog_adapter", "LOCAL"), http.StatusBadRequest, integrationMsgCatalog},
		{"catalog_http_wins_over_bad_events", func(m map[string]any) { m["catalog_adapter"] = "http"; m["events_adapter"] = "kafka" },
			http.StatusUnprocessableEntity, integrationMsgHTTP},
		{"events_unknown", set("events_adapter", "kafka"), http.StatusBadRequest, integrationMsgEvents},
		{"events_http_is_not_an_events_adapter", set("events_adapter", "http"), http.StatusBadRequest, integrationMsgEvents},
		{"events_uppercase", set("events_adapter", "Webhook"), http.StatusBadRequest, integrationMsgEvents},
		{"events_with_inner_space", set("events_adapter", "web hook"), http.StatusBadRequest, integrationMsgEvents},
		{"url_of_2001_bytes", set("endpoint_url", "https://h.example.test/"+strings.Repeat("a", 2001-len("https://h.example.test/"))),
			http.StatusBadRequest, integrationMsgLongURL},
		{"url_relative_path", set("endpoint_url", "/solo/una/ruta"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_without_scheme", set("endpoint_url", "puente.example.test/wapp"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_scheme_relative", set("endpoint_url", "//puente.example.test/wapp"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_gopher", set("endpoint_url", "gopher://viejo.example.test/x"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_file", set("endpoint_url", "file:///etc/hosts"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_javascript", set("endpoint_url", "javascript:alert(1)"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_ws", set("endpoint_url", "wss://puente.example.test/wapp"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_http_without_host", set("endpoint_url", "http:///wapp"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_https_colon_only", set("endpoint_url", "https:"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_with_control_char", set("endpoint_url", "https://puente.example.test/\x7f"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_with_space_in_host", set("endpoint_url", "https://puente example.test/"), http.StatusBadRequest, integrationMsgBadURL},
		{"url_bad_port", set("endpoint_url", "https://puente.example.test:puerto/"), http.StatusBadRequest, integrationMsgBadURL},
		{"secret_of_23_bytes", set("secret", strings.Repeat("s", 23)), http.StatusBadRequest, integrationMsgSignerLen},
		{"secret_of_257_bytes", set("secret", strings.Repeat("s", 257)), http.StatusBadRequest, integrationMsgSignerLen},
		{"secret_of_23_bytes_in_12_runes", set("secret", strings.Repeat("ñ", 11)+"x"), http.StatusBadRequest, integrationMsgSignerLen},
		{"bad_url_wins_over_short_secret", func(m map[string]any) { m["endpoint_url"] = "nada"; m["secret"] = "corto" },
			http.StatusBadRequest, integrationMsgBadURL},
		{"live_webhook_without_endpoint", set("endpoint_url", ""), http.StatusBadRequest, integrationMsgNoURL},
		{"live_webhook_with_blank_endpoint", set("endpoint_url", " \t "), http.StatusBadRequest, integrationMsgNoURL},
		{"live_webhook_without_secret", func(m map[string]any) { delete(m, "secret") }, http.StatusBadRequest, integrationMsgNoSigner},
		{"live_webhook_with_empty_secret", set("secret", ""), http.StatusBadRequest, integrationMsgNoSigner},
		{"short_secret_wins_over_missing_endpoint", func(m map[string]any) { m["endpoint_url"] = ""; m["secret"] = "corto" },
			http.StatusBadRequest, integrationMsgSignerLen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := integrationSetup(t)
			body := integrationValidBody()
			tc.change(body)
			rec := putIntegration(h, cara, integrationJSON(t, body))
			wantCode(t, tc.name, rec, tc.code)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if store.upserts != 0 {
				t.Errorf("%s: una configuración rechazada llegó a guardarse (%d upserts)", tc.name, store.upserts)
			}
		})
	}
}

// TestMountIntegrations_PutAccepts: los bordes que SÍ entran, y el puente a medio configurar
// mientras no esté encendido.
func TestMountIntegrations_PutAccepts(t *testing.T) {
	set := func(key string, value any) func(map[string]any) {
		return func(m map[string]any) { m[key] = value }
	}
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"catalog_webhook", set("catalog_adapter", "webhook")},
		{"plain_http_endpoint", set("endpoint_url", "http://127.0.0.1:9009/hook")},
		{"uppercase_scheme", set("endpoint_url", "HTTPS://puente.example.test/wapp")},
		{"url_of_exactly_2000_bytes", set("endpoint_url", "https://h.example.test/"+strings.Repeat("a", 2000-len("https://h.example.test/")))},
		{"secret_of_exactly_24_bytes", set("secret", strings.Repeat("s", 24))},
		{"secret_of_exactly_256_bytes", set("secret", strings.Repeat("s", 256))},
		{"disabled_webhook_without_endpoint_or_secret", func(m map[string]any) {
			m["enabled"] = false
			delete(m, "endpoint_url")
			delete(m, "secret")
		}},
		{"enabled_local_events_without_endpoint_or_secret", func(m map[string]any) {
			m["events_adapter"] = "local"
			delete(m, "endpoint_url")
			delete(m, "secret")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := integrationSetup(t)
			body := integrationValidBody()
			tc.change(body)
			rec := putIntegration(h, cara, integrationJSON(t, body))
			wantCode(t, tc.name, rec, http.StatusOK)
			if store.upserts != 1 {
				t.Errorf("%s: %d upserts, quiero 1", tc.name, store.upserts)
			}
		})
	}
}

// TestMountIntegrations_PutLiveBridgeUsesTheStoredSecret: encender sin secreto en el cuerpo
// vale si ya hay uno guardado; con fila pero sin secreto, no; y si no se puede averiguar, 500.
func TestMountIntegrations_PutLiveBridgeUsesTheStoredSecret(t *testing.T) {
	live := `{"events_adapter":"webhook","endpoint_url":"` + integrationEndpoint + `","enabled":true}`

	t.Run("stored_secret_is_enough", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.seed(t, integrations.TenantIntegration{TenantID: tenantA, CatalogAdapter: "local", EventsAdapter: "webhook"}, integrationFakeSigner)
		rec := putIntegration(h, cara, live)
		wantCode(t, "con secreto guardado", rec, http.StatusOK)
		if !strings.Contains(rec.Body.String(), integrations.Fingerprint(integrationFakeSigner)) {
			t.Errorf("la respuesta debe traer la huella del secreto que ya había: %s", rec.Body.String())
		}
	})
	t.Run("row_without_secret_is_400", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.seed(t, integrations.TenantIntegration{TenantID: tenantA, CatalogAdapter: "local", EventsAdapter: "webhook"}, "")
		rec := putIntegration(h, cara, live)
		wantCode(t, "fila sin secreto", rec, http.StatusBadRequest)
		wantErrorBody(t, "fila sin secreto", rec, integrationMsgNoSigner)
		if store.upserts != 0 {
			t.Errorf("se guardó un puente encendido sin secreto (%d upserts)", store.upserts)
		}
	})
	t.Run("secret_of_another_tenant_does_not_count", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.seed(t, integrationLive(tenantB), integrationFakeSigner)
		rec := putIntegration(h, cara, live)
		wantCode(t, "secreto ajeno", rec, http.StatusBadRequest)
		wantErrorBody(t, "secreto ajeno", rec, integrationMsgNoSigner)
	})
	t.Run("store_fails_while_checking_is_500", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.getErr = errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
		rec := putIntegration(h, cara, live)
		wantCode(t, "no se pudo comprobar", rec, http.StatusInternalServerError)
		wantErrorBody(t, "no se pudo comprobar", rec, integrationMsgCheck)
		if store.upserts != 0 {
			t.Errorf("se guardó sin poder comprobar el secreto (%d upserts)", store.upserts)
		}
	})
	t.Run("secret_in_body_skips_the_lookup", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.getErr = errors.New("caído")
		rec := putIntegration(h, cara, integrationJSON(t, integrationValidBody()))
		// Con secreto en el cuerpo la validación no consulta el puerto: el fallo aparece después,
		// en la relectura.
		wantCode(t, "con secreto en el cuerpo", rec, http.StatusInternalServerError)
		wantErrorBody(t, "con secreto en el cuerpo", rec, integrationMsgReread)
		if store.upserts != 1 || store.gets != 1 {
			t.Errorf("quiero 1 upsert y 1 sola lectura (la relectura): upserts=%d gets=%d", store.upserts, store.gets)
		}
	})
}

// TestMountIntegrations_PutBodyShape: el techo de 8 KiB y los cuerpos que no son el objeto.
func TestMountIntegrations_PutBodyShape(t *testing.T) {
	padded := func(n int) string {
		const head, tail = `{"relleno":"`, `"}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	h, store, cara := integrationSetup(t)

	rec := putIntegration(h, cara, padded(8193))
	wantCode(t, "8 KiB + 1", rec, http.StatusRequestEntityTooLarge)
	wantExactBody(t, "8 KiB + 1", rec, `{"error":"el cuerpo excede el tamaño máximo de 8192 bytes","max_bytes":8192}`)

	for name, body := range map[string]string{
		"not_json":            `esto no es json`,
		"empty_body":          ``,
		"array":               `[]`,
		"trailing_garbage":    `{} basura`,
		"enabled_as_string":   `{"enabled":"true"}`,
		"secret_as_number":    `{"secret":123456789012345678901234}`,
		"endpoint_as_object":  `{"endpoint_url":{"href":"https://x.example.test"}}`,
		"adapter_as_a_number": `{"events_adapter":1}`,
	} {
		rec := putIntegration(h, cara, body)
		wantCode(t, name, rec, http.StatusBadRequest)
		wantErrorBody(t, name, rec, integrationMsgShape)
	}
	if store.upserts != 0 {
		t.Errorf("un cuerpo rechazado llegó a guardarse (%d upserts)", store.upserts)
	}

	rec = putIntegration(h, cara, padded(8192))
	wantCode(t, "8 KiB justos", rec, http.StatusOK)
}

// TestMountIntegrations_PutStoreErrors: los dos 500 de G15 se distinguen y no repiten el error.
func TestMountIntegrations_PutStoreErrors(t *testing.T) {
	boom := errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	t.Run("upsert_fails", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.upsertErr = boom
		rec := putIntegration(h, cara, integrationJSON(t, integrationValidBody()))
		wantCode(t, "el upsert falla", rec, http.StatusInternalServerError)
		wantErrorBody(t, "el upsert falla", rec, integrationMsgSave)
		if store.gets != 0 {
			t.Errorf("tras fallar el upsert se releyó %d veces, quiero 0", store.gets)
		}
	})
	t.Run("fingerprint_fails_on_reread", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.fingerprintErr = boom
		rec := putIntegration(h, cara, integrationJSON(t, integrationValidBody()))
		wantCode(t, "la huella falla", rec, http.StatusInternalServerError)
		wantErrorBody(t, "la huella falla", rec, integrationMsgReread)
	})
	t.Run("row_missing_on_reread", func(t *testing.T) {
		h, store, cara := integrationSetup(t)
		store.upsertLost = true
		rec := putIntegration(h, cara, integrationJSON(t, integrationValidBody()))
		wantCode(t, "la fila no aparece", rec, http.StatusInternalServerError)
		wantErrorBody(t, "la fila no aparece", rec, integrationMsgReread)
	})
}
