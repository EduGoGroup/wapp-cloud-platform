// Porta internal/publicapi/integrations.go @ ed60c24 (líneas 34-46, 91-108 y 263-374: los techos
// del PUT, su cuerpo, decodeIntegration, validateIntegration, validateEndpointURL y
// validateLiveBridge).
//
// integrations_validate.go — LA LECTURA Y LA VALIDACIÓN DEL CUERPO DE G15
// (PUT /api/v1/integrations). Es un trozo de integrations.go, partido por tema (05 E-13) solo
// moviendo declaraciones; las promesas observables están escritas en el comentario de
// MountIntegrations.
//
// No exporta nada: nació entero con el verde, y su test es interno (05 E-4, P6).

package apipublica

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Techos del PUT. El cuerpo es un objeto de cinco campos cortos: el límite existe para que un
// cliente roto no empuje memoria por un endpoint de configuración.
const (
	integrationMaxBodyBytes   = 1 << 13 // 8 KiB de cuerpo
	integrationMaxEndpointLen = 2000    // bytes de la URL del puente
	// integrationMinSecretLen es la longitud MÍNIMA del secreto de firma. No es burocracia: la
	// huella publicada por el GET son 32 bits de su SHA-256, y contra un secreto corto y
	// adivinable eso es un oráculo de confirmación offline. Con 24 caracteres, un secreto
	// generado al azar queda fuera del alcance de esa comprobación. Es el mismo criterio que
	// hace que el secreto sea write-only.
	integrationMinSecretLen = 24
	integrationMaxSecretLen = 256
)

// integrationRequest es el cuerpo del PUT.
//
// NO TIENE CAMPO tenant_id, y esa ausencia ES el mecanismo de INV-8: un cuerpo que traiga
// `tenant_id` de otro tenant lo descarta encoding/json sin ruido, y la operación va contra el
// tenant del token. No hace falta comprobarlo ni rechazarlo — no hay dónde guardarlo.
//
// `secret` es WRITE-ONLY y opcional: ausente o vacío deja el secreto EXISTENTE intacto (es lo
// que manda un formulario cuyo campo de secreto se dejó en blanco, que es el caso normal al
// reconfigurar el endpoint). Para dejar de firmar se borra la integración entera.
type integrationRequest struct {
	CatalogAdapter string `json:"catalog_adapter"`
	EventsAdapter  string `json:"events_adapter"`
	EndpointURL    string `json:"endpoint_url"`
	Secret         string `json:"secret"`
	Enabled        bool   `json:"enabled"`
}

// decodeIntegration lee el cuerpo del PUT con el techo aplicado ANTES de deserializar y
// normaliza los adaptadores vacíos a su default. Devuelve el status + el cuerpo de error ya
// armado (nil = todo bien); el cuerpo de error es `any` porque el 413 lleva max_bytes (ver
// limits.go).
//
// Recorta los bordes de los adaptadores y del endpoint; el secreto NO se toca: un espacio en un
// secreto es parte del secreto.
func decodeIntegration(body io.Reader) (integrationRequest, int, any) {
	raw, err := io.ReadAll(io.LimitReader(body, integrationMaxBodyBytes+1))
	if err != nil {
		return integrationRequest{}, http.StatusBadRequest, errorBody("no se pudo leer el cuerpo")
	}
	if len(raw) > integrationMaxBodyBytes {
		return integrationRequest{}, http.StatusRequestEntityTooLarge, tooLarge("el cuerpo", integrationMaxBodyBytes)
	}
	var req integrationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return integrationRequest{}, http.StatusBadRequest,
			errorBody("el cuerpo debe ser un JSON {catalog_adapter, events_adapter, endpoint_url, secret, enabled}")
	}
	req.CatalogAdapter = strings.TrimSpace(req.CatalogAdapter)
	req.EventsAdapter = strings.TrimSpace(req.EventsAdapter)
	req.EndpointURL = strings.TrimSpace(req.EndpointURL)
	if req.CatalogAdapter == "" {
		req.CatalogAdapter = integrationAdapterLocal
	}
	if req.EventsAdapter == "" {
		req.EventsAdapter = integrationAdapterLocal
	}
	return req, 0, nil
}

// validateIntegration comprueba lo que la BD no puede comprobar sola. Devuelve (status,
// mensaje); mensaje vacío = configuración admisible.
//
// El orden importa: primero lo que es del vocabulario (y por tanto no depende de nada), y al
// final la coherencia del puente ENCENDIDO, que es lo único que necesita consultar el estado
// actual.
func validateIntegration(ctx context.Context, is IntegrationsStore, tenantID string, req integrationRequest) (int, string) {
	switch req.CatalogAdapter {
	case integrationAdapterLocal, integrationAdapterWebhook:
	case integrationAdapterHTTP:
		// 422 y no 400: la petición está bien formada y el valor es del vocabulario (el CHECK
		// de la 0047 lo admite) — lo que no existe es la implementación. Es exactamente la
		// distinción que separa «no te entiendo» de «te entiendo y no puedo».
		return http.StatusUnprocessableEntity,
			"catalog.pull diferido: el adaptador de catálogo «http» todavía no está implementado; usa «local»"
	default:
		return http.StatusBadRequest, "catalog_adapter debe ser «local» o «webhook»"
	}
	if req.EventsAdapter != integrationAdapterLocal && req.EventsAdapter != integrationAdapterWebhook {
		return http.StatusBadRequest, "events_adapter debe ser «local» o «webhook»"
	}
	if code, msg := validateIntegrationEndpoint(req.EndpointURL); msg != "" {
		return code, msg
	}
	if req.Secret != "" && (len(req.Secret) < integrationMinSecretLen || len(req.Secret) > integrationMaxSecretLen) {
		return http.StatusBadRequest, "el secreto de firma debe tener entre 24 y 256 caracteres"
	}
	return validateLiveBridge(ctx, is, tenantID, req)
}

// validateIntegrationEndpoint (validateEndpointURL en la cara vieja) exige una URL ABSOLUTA
// http/https cuando viene. Vacía es admisible (un tenant puede guardar local/local sin
// endpoint).
//
// No se restringe a https: el e2e y el desarrollo del puente corren contra un receptor local en
// http, y prohibirlo aquí obligaría a mentirle a la API para poder probar. La firma HMAC del
// cuerpo es lo que autentica la entrega; el canal es decisión del tenant, que es quien pone el
// endpoint.
func validateIntegrationEndpoint(raw string) (int, string) {
	if raw == "" {
		return 0, ""
	}
	if len(raw) > integrationMaxEndpointLen {
		return http.StatusBadRequest, "endpoint_url es demasiado larga"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return http.StatusBadRequest, "endpoint_url debe ser una URL absoluta http(s)"
	}
	return 0, ""
}

// validateLiveBridge impide guardar un puente ENCENDIDO que no puede entregar.
//
// Es la única regla que mira el estado actual, y existe porque el worker exige las tres cosas a
// la vez —enabled, endpoint_url y secreto (integrations/worker_delivery.go)—. En la cara vieja,
// sin ellas cada entrega fallaba, reintentaba con backoff y acababa en `dead`; desde D-F6-11 el
// gate del módulo (integrations/gate.go) ni siquiera las encola. O sea que sin esta
// comprobación la configuración se guardaría «bien» y el puente, sencillamente, no haría nada:
// el gate CALLA, y esta validación es la que AVISA, donde el operador puede corregirlo. Por eso
// no se omite aunque el gate ya proteja la cola.
//
// Solo aplica con enabled=true: un puente APAGADO a medio configurar es un estado legítimo (y
// es como se prepara uno antes de encenderlo).
func validateLiveBridge(ctx context.Context, is IntegrationsStore, tenantID string, req integrationRequest) (int, string) {
	if !req.Enabled || req.EventsAdapter != integrationAdapterWebhook {
		return 0, ""
	}
	if req.EndpointURL == "" {
		return http.StatusBadRequest, "un puente webhook encendido necesita endpoint_url"
	}
	if req.Secret != "" {
		return 0, ""
	}
	// Sin secreto en el cuerpo, vale el que ya estuviera guardado.
	ti, found, err := is.GetTenantIntegration(ctx, tenantID)
	if err != nil {
		return http.StatusInternalServerError, "no se pudo comprobar la integración actual"
	}
	if !found || !ti.HasSecret {
		return http.StatusBadRequest, "un puente webhook encendido necesita un secreto de firma"
	}
	return 0, ""
}
