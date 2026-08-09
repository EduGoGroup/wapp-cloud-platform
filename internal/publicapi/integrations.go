package publicapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// integrations.go es la superficie de gestión del puente CRM (Plan 042 · T5.1,
// design §5): GET / PUT / DELETE de /api/v1/integrations. El dominio ya existía
// entero (internal/integrations); aquí solo se abre la puerta HTTP.
//
// La IDA del puente (webhook_outbox) y la VUELTA (el callback firmado) NO pasan
// por aquí: esto es la configuración que las dos leen.

// Vocabulario CERRADO de adaptadores, el mismo que acota el CHECK de la migración
// 0047. Se repite aquí porque la API tiene que rechazar ANTES de llegar a la BD:
// dejar que el CHECK sea quien valide convertiría un error del cliente en un 500.
const (
	adapterLocal   = "local"
	adapterHTTP    = "http"
	adapterWebhook = "webhook"
)

// Techos del PUT. El cuerpo es un objeto de cinco campos cortos: el límite existe
// para que un cliente roto no empuje memoria por un endpoint de configuración.
const (
	maxIntegrationBytes = 1 << 13 // 8 KiB de cuerpo
	maxEndpointURLLen   = 2000    // bytes de la URL del puente
	// minSecretLen es la longitud MÍNIMA del secreto de firma. No es burocracia:
	// la huella publicada por el GET son 32 bits de su SHA-256, y contra un
	// secreto corto y adivinable eso es un oráculo de confirmación offline. Con
	// 24 caracteres, un secreto generado al azar queda fuera del alcance de esa
	// comprobación. Es el mismo criterio que hace que el secreto sea write-only.
	minSecretLen = 24
	maxSecretLen = 256
)

// IntegrationsStore es el puerto MÍNIMO de la configuración de integraciones por
// tenant (public.tenant_integrations) que la API pública consume. Lo satisface
// *integrations.Postgres.
//
// TODAS las operaciones van acotadas al tenant (INV-8), y el aislamiento lo
// garantiza la firma: el tenant es un ARGUMENTO que sale del token, y la tabla
// tiene tenant_id como PRIMARY KEY. No hay forma de pedirle la fila de otro.
//
// SecretFingerprint devuelve la huella, NO el secreto: el valor en claro no tiene
// por qué existir nunca en esta capa (ver integrations/crud.go).
type IntegrationsStore interface {
	GetTenantIntegration(ctx context.Context, tenantID string) (integrations.TenantIntegration, bool, error)
	UpsertTenantIntegration(ctx context.Context, ti integrations.TenantIntegration, secret string) error
	DeleteTenantIntegration(ctx context.Context, tenantID string) error
	SecretFingerprint(ctx context.Context, tenantID string) (string, bool, error)
}

// integrationDTO es el contrato de GET y de la respuesta del PUT (la MISMA forma
// en los dos: la pantalla que lo pinta no tiene por qué saber cuál acaba de
// llamar).
//
// `configured` distingue las dos maneras de estar en local/local: no tener fila
// (el default de la plataforma) y tenerla puesta a mano en local. Sin ese
// booleano, un DELETE seguido de un GET sería indistinguible de un tenant que
// nunca configuró nada, y la pantalla no sabría si ofrecer «borrar».
//
// El secreto sale en DOS campos y en ninguno va el valor: `secret_set` dice si
// hay, `secret_fingerprint` permite compararlo con el que el puente tiene
// configurado (D-042.7 / REQ-13).
type integrationDTO struct {
	Configured        bool   `json:"configured"`
	CatalogAdapter    string `json:"catalog_adapter"`
	EventsAdapter     string `json:"events_adapter"`
	EndpointURL       string `json:"endpoint_url,omitempty"`
	Enabled           bool   `json:"enabled"`
	SecretSet         bool   `json:"secret_set"`
	SecretFingerprint string `json:"secret_fingerprint,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

// integrationRequest es el cuerpo del PUT.
//
// NO TIENE CAMPO tenant_id, y esa ausencia ES el mecanismo de INV-8: un cuerpo
// que traiga `tenant_id` de otro tenant lo descarta encoding/json sin ruido, y la
// operación va contra el tenant del token. No hace falta comprobarlo ni
// rechazarlo — no hay dónde guardarlo.
//
// `secret` es WRITE-ONLY y opcional: ausente o vacío deja el secreto EXISTENTE
// intacto (es lo que manda un formulario cuyo campo de secreto se dejó en blanco,
// que es el caso normal al reconfigurar el endpoint). Para dejar de firmar se
// borra la integración entera.
type integrationRequest struct {
	CatalogAdapter string `json:"catalog_adapter"`
	EventsAdapter  string `json:"events_adapter"`
	EndpointURL    string `json:"endpoint_url"`
	Secret         string `json:"secret"`
	Enabled        bool   `json:"enabled"`
}

// defaultIntegration es lo que responde el GET de un tenant SIN fila: local/local
// apagado. No es una invención de la API — es literalmente lo que dice la
// migración 0047 («sin fila = local/local»), dicho en JSON.
func defaultIntegration() integrationDTO {
	return integrationDTO{
		Configured:     false,
		CatalogAdapter: adapterLocal,
		EventsAdapter:  adapterLocal,
	}
}

// toIntegrationDTO arma la respuesta a partir de la fila y de la huella. La fila
// ya viene SIN el secreto (integrations.TenantIntegration solo trae HasSecret).
func toIntegrationDTO(ti integrations.TenantIntegration, fingerprint string) integrationDTO {
	dto := integrationDTO{
		Configured:        true,
		CatalogAdapter:    ti.CatalogAdapter,
		EventsAdapter:     ti.EventsAdapter,
		EndpointURL:       ti.EndpointURL,
		Enabled:           ti.Enabled,
		SecretSet:         ti.HasSecret,
		SecretFingerprint: fingerprint,
	}
	if !ti.CreatedAt.IsZero() {
		dto.CreatedAt = ti.CreatedAt.UTC().Format(rfc3339)
	}
	if !ti.UpdatedAt.IsZero() {
		dto.UpdatedAt = ti.UpdatedAt.UTC().Format(rfc3339)
	}
	return dto
}

// readIntegration lee la configuración del tenant y su huella de una vez. found
// false ⇒ el tenant no tiene fila (default local/local).
//
// La huella se pide SOLO si la fila dice que hay secreto: así el descifrado no se
// intenta cuando se sabe que no hay nada que descifrar.
func readIntegration(ctx context.Context, is IntegrationsStore, tenantID string) (integrationDTO, bool, error) {
	ti, found, err := is.GetTenantIntegration(ctx, tenantID)
	if err != nil || !found {
		return integrationDTO{}, found, err
	}
	var fingerprint string
	if ti.HasSecret {
		fp, ok, ferr := is.SecretFingerprint(ctx, tenantID)
		if ferr != nil {
			return integrationDTO{}, true, ferr
		}
		if ok {
			fingerprint = fp
		}
	}
	return toIntegrationDTO(ti, fingerprint), true, nil
}

// getIntegrationHandler devuelve GET /api/v1/integrations: la configuración del
// puente del tenant del token (INV-8).
//
// 200 SIEMPRE que se pueda leer, también sin fila: un tenant sin integración
// responde el default local/local con `configured:false`, no un 404. «No tengo
// puente» es una respuesta —y es la que la pantalla necesita para dibujar el
// formulario vacío—, no un fallo.
//
// El secreto NUNCA sale: solo `secret_set` y la huella corta (D-042.7).
// Respuestas: 200; 401 sin identidad; 403 sin la feature (lo pone el middleware);
// 500 si el store falla.
func getIntegrationHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if is == nil {
			writeError(w, http.StatusInternalServerError, "store de integraciones no configurado")
			return
		}
		dto, found, err := readIntegration(r.Context(), is, id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo leer la integración")
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, defaultIntegration())
			return
		}
		writeJSON(w, http.StatusOK, dto)
	})
}

// putIntegrationHandler devuelve PUT /api/v1/integrations.
//
// SEMÁNTICA: upsert COMPLETO. El cuerpo es la foto entera de la configuración —lo
// que no venga toma el default de la tabla (local/local, apagado)—, con UNA
// excepción declarada: `secret`, que es write-only y cuyo silencio significa
// «déjalo como está». Sin esa excepción sería imposible cambiar el endpoint sin
// reenviar el secreto, porque el GET no lo devuelve para poder reenviarlo.
//
// El tenant sale del token (INV-8): un `tenant_id` en el cuerpo no existe para
// este handler (ver integrationRequest).
//
// Respuestas:
//
//   - 200 con la configuración RESULTANTE releída (misma forma que el GET).
//   - 400 cuerpo no-JSON, adaptador desconocido, URL inválida, secreto corto/largo,
//     o webhook encendido sin endpoint/secreto.
//   - 401 sin identidad; 403 sin la feature (middleware); 413 cuerpo excesivo.
//   - 422 catalog_adapter='http' — el verbo catalog.pull está DIFERIDO (design §2).
//   - 500 fallo del store.
func putIntegrationHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if is == nil {
			writeError(w, http.StatusInternalServerError, "store de integraciones no configurado")
			return
		}
		req, code, errBody := decodeIntegration(r.Body)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		if code, msg := validateIntegration(r.Context(), is, id.TenantID, req); msg != "" {
			writeError(w, code, msg)
			return
		}

		ti := integrations.TenantIntegration{
			TenantID:       id.TenantID, // INV-8: del token, jamás del cuerpo
			CatalogAdapter: req.CatalogAdapter,
			EventsAdapter:  req.EventsAdapter,
			EndpointURL:    req.EndpointURL,
			Enabled:        req.Enabled,
		}
		if err := is.UpsertTenantIntegration(r.Context(), ti, req.Secret); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo guardar la integración")
			return
		}
		// Se RELEE en vez de devolver lo que se acaba de mandar: así la respuesta
		// trae los timestamps de la fila y la huella del secreto que quedó
		// guardado (que puede ser el de antes, si el PUT no traía uno).
		dto, found, err := readIntegration(r.Context(), is, id.TenantID)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "integración guardada, pero no se pudo releer")
			return
		}
		writeJSON(w, http.StatusOK, dto)
	})
}

// decodeIntegration lee el cuerpo del PUT con el techo aplicado ANTES de
// deserializar y normaliza los adaptadores vacíos a su default. Devuelve el
// status + el cuerpo de error ya armado (nil = todo bien); el cuerpo es `any` por
// lo mismo que en decodeImportBody (el 413 lleva max_bytes, ver limits.go).
func decodeIntegration(body io.Reader) (integrationRequest, int, any) {
	raw, err := io.ReadAll(io.LimitReader(body, maxIntegrationBytes+1))
	if err != nil {
		return integrationRequest{}, http.StatusBadRequest, errorBody("no se pudo leer el cuerpo")
	}
	if len(raw) > maxIntegrationBytes {
		return integrationRequest{}, http.StatusRequestEntityTooLarge, tooLarge("el cuerpo", maxIntegrationBytes)
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
		req.CatalogAdapter = adapterLocal
	}
	if req.EventsAdapter == "" {
		req.EventsAdapter = adapterLocal
	}
	return req, 0, nil
}

// validateIntegration comprueba lo que la BD no puede comprobar sola. Devuelve
// (status, mensaje); mensaje vacío = configuración admisible.
//
// El orden importa: primero lo que es del vocabulario (y por tanto no depende de
// nada), y al final la coherencia del puente ENCENDIDO, que es lo único que
// necesita consultar el estado actual.
func validateIntegration(ctx context.Context, is IntegrationsStore, tenantID string, req integrationRequest) (int, string) {
	switch req.CatalogAdapter {
	case adapterLocal, adapterWebhook:
	case adapterHTTP:
		// 422 y no 400: la petición está bien formada y el valor es del
		// vocabulario (el CHECK de la 0047 lo admite) — lo que no existe es la
		// implementación. Es exactamente la distinción que separa «no te
		// entiendo» de «te entiendo y no puedo».
		return http.StatusUnprocessableEntity,
			"catalog.pull diferido: el adaptador de catálogo «http» todavía no está implementado; usa «local»"
	default:
		return http.StatusBadRequest, "catalog_adapter debe ser «local» o «webhook»"
	}
	if req.EventsAdapter != adapterLocal && req.EventsAdapter != adapterWebhook {
		return http.StatusBadRequest, "events_adapter debe ser «local» o «webhook»"
	}
	if code, msg := validateEndpointURL(req.EndpointURL); msg != "" {
		return code, msg
	}
	if req.Secret != "" && (len(req.Secret) < minSecretLen || len(req.Secret) > maxSecretLen) {
		return http.StatusBadRequest, "el secreto de firma debe tener entre 24 y 256 caracteres"
	}
	return validateLiveBridge(ctx, is, tenantID, req)
}

// validateEndpointURL exige una URL ABSOLUTA http/https cuando viene. Vacía es
// admisible (un tenant puede guardar local/local sin endpoint).
//
// No se restringe a https: el e2e y el desarrollo del puente corren contra un
// receptor local en http, y prohibirlo aquí obligaría a mentirle a la API para
// poder probar. La firma HMAC del cuerpo es lo que autentica la entrega; el
// canal es decisión del tenant, que es quien pone el endpoint.
func validateEndpointURL(raw string) (int, string) {
	if raw == "" {
		return 0, ""
	}
	if len(raw) > maxEndpointURLLen {
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
// Es la única regla que mira el estado actual, y existe porque el worker exige
// las tres cosas a la vez —enabled, endpoint_url y secreto (worker.go:427-437)—:
// sin ellas cada entrega falla, reintenta con backoff y acaba en `dead`. O sea
// que sin esta comprobación la configuración se guardaría «bien» y el fallo
// aparecería horas después, en una tabla que nadie mira. Se comprueba donde el
// operador puede corregirlo.
//
// Solo aplica con enabled=true: un puente APAGADO a medio configurar es un estado
// legítimo (y es como se prepara uno antes de encenderlo).
func validateLiveBridge(ctx context.Context, is IntegrationsStore, tenantID string, req integrationRequest) (int, string) {
	if !req.Enabled || req.EventsAdapter != adapterWebhook {
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

// deleteIntegrationHandler devuelve DELETE /api/v1/integrations: borra la fila del
// tenant, que vuelve al default local/local (sin CRM, con la experiencia completa
// de wApp — migración 0047).
//
// 204 SIEMPRE que la operación se complete, también si no había fila:
// IDEMPOTENTE. A diferencia de tenant-content —donde el 404 dice «esa ref no es
// tuya o no existe»— aquí el recurso es único por tenant y el resultado pedido es
// un estado, no un objeto: tras el DELETE el tenant está en local/local, que es
// lo que se pidió. Un 404 obligaría a la pantalla a distinguir dos desenlaces que
// significan lo mismo.
//
// Borra la fila ENTERA, y con ella el secreto cifrado: es la única forma de
// retirar el secreto (el PUT nunca lo borra, ver integrationRequest).
func deleteIntegrationHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if is == nil {
			writeError(w, http.StatusInternalServerError, "store de integraciones no configurado")
			return
		}
		if err := is.DeleteTenantIntegration(r.Context(), id.TenantID); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo borrar la integración")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
