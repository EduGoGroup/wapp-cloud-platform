package publicapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// tenantllm.go es la superficie de configuración de la vía LLM API por tenant
// (Plan 044 · Ola 0 · T0.3, design §8): GET / PUT / DELETE de
// /api/v1/tenant-llm. El dominio vive en internal/tenantllm; aquí solo se abre
// la puerta HTTP, igual que integrations.go hace con el puente CRM.
//
// Lo que este fichero NO hace, y es la mitad de su trabajo: NUNCA tiene la API
// key en una variable después de guardarla. El puerto TenantLLMStore no expone
// método que la devuelva (ver internal/tenantllm/tenantllm.go), así que un
// futuro `log.Printf("%+v", cfg)` en esta capa no puede filtrarla.

// Techos del PUT. El cuerpo es un objeto de cuatro campos cortos: el límite
// existe para que un cliente roto no empuje memoria por un endpoint de
// configuración. Mismos órdenes de magnitud que maxIntegrationBytes.
const (
	maxTenantLLMBytes = 1 << 13 // 8 KiB de cuerpo
	// maxLLMModelLen acota el identificador de modelo. wApp NO valida el modelo
	// contra una lista de valores conocidos —esa lista caduca cada pocas semanas
	// y una lista caducada rechaza modelos válidos—, así que lo único que se
	// comprueba es que sea texto y no un ensayo.
	maxLLMModelLen = 128
	// minLLMAPIKeyLen / maxLLMAPIKeyLen acotan la credencial. El mínimo NO es
	// una política de fortaleza —la clave no la elige el tenant, se la da el
	// proveedor— sino un filtro contra el error de dedo: un campo con tres
	// caracteres es un formulario mal rellenado, y guardarlo cifrado dejaría al
	// tenant creyendo que configuró algo que va a fallar en el primer job.
	minLLMAPIKeyLen = 16
	maxLLMAPIKeyLen = 512
)

// TenantLLMStore es el puerto MÍNIMO de la configuración LLM por tenant
// (public.tenant_llm) que la API pública consume. Lo satisface
// *tenantllm.Postgres.
//
// 🔴 ES UN SUBCONJUNTO ESTRICTO de tenantllm.Store, y le falta EXACTAMENTE un
// método: APIKey. Esa ausencia es el mecanismo por el que la credencial en claro
// no cruza la frontera de este paquete — el mismo criterio que mantiene el
// secreto HMAC del puente fuera de aquí (integrations/crud.go:38-42). Quien
// necesite el valor es el pipeline (O2), y le pedirá el puerto completo.
//
// TODAS las operaciones van acotadas al tenant (INV-7): el tenant es un
// ARGUMENTO que sale del token, y la tabla tiene tenant_id como PRIMARY KEY. No
// hay forma de pedirle la fila de otro.
type TenantLLMStore interface {
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
	Upsert(ctx context.Context, cfg tenantllm.Config, apiKey string, consentedAt time.Time) error
	Delete(ctx context.Context, tenantID string) error
}

// tenantLLMDTO es el contrato de GET y de la respuesta del PUT (la MISMA forma
// en los dos: la pantalla que lo pinta no tiene por qué saber cuál acaba de
// llamar).
//
// `configured` distingue «no hay vía API» de «la hay»: sin ese booleano, un
// DELETE seguido de un GET sería indistinguible de un tenant que nunca configuró
// nada, y la pantalla no sabría si ofrecer «borrar».
//
// 🔴 LA CLAVE SALE EN UN SOLO CAMPO Y ES UN BOOLEANO: `key_set`. El criterio de
// T0.3 dice «el GET nunca devuelve la clave» y aquí eso es estructural, no una
// omisión al serializar: el struct no tiene dónde ponerla.
//
// 🔴 Y NO HAY HUELLA, al revés que integrationDTO. La huella del secreto HMAC
// existe porque el tenant tiene con qué compararla —el mismo secreto está
// configurado en SU puente, y comparar ocho hex es lo que le dice si coinciden
// (D-042.7)—. Una API key de Anthropic no tiene contraparte que comparar: nadie
// la teclea dos veces en dos sitios. Publicar su huella sería regalar un oráculo
// de confirmación offline sobre un valor de formato conocido y público
// (`sk-ant-…`) a cambio de una pregunta que nadie hace. Si algún día aparece esa
// pregunta, ESE día se añade el campo.
type tenantLLMDTO struct {
	Configured  bool   `json:"configured"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	KeySet      bool   `json:"key_set"`
	ConsentedAt string `json:"consented_at,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// tenantLLMRequest es el cuerpo del PUT.
//
// NO TIENE CAMPO tenant_id, y esa ausencia ES el mecanismo de INV-7: un cuerpo
// que traiga `tenant_id` de otro tenant lo descarta encoding/json sin ruido, y
// la operación va contra el tenant del token. No hace falta comprobarlo ni
// rechazarlo — no hay dónde guardarlo.
//
// `consented` es OBLIGATORIO y tiene que venir en `true`: es el consentimiento
// explícito a que el texto de las conversaciones salga hacia un proveedor
// externo (ADR-0030). Un `false` o su ausencia son 400, no un upsert silencioso
// con consentimiento apagado — la tabla no tiene forma de representar eso.
//
// `api_key` es OBLIGATORIA en cada PUT, y ahí este contrato se separa a
// propósito del de integrations (donde `secret` vacío conserva el existente):
// ver validateTenantLLM.
type tenantLLMRequest struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	Consented bool   `json:"consented"`
}

// notConfiguredTenantLLM es lo que responde el GET de un tenant SIN fila: la vía
// API no está configurada. No es un 404 — «no tengo vía API» es una respuesta, y
// es la que la pantalla necesita para dibujar el formulario vacío.
func notConfiguredTenantLLM() tenantLLMDTO {
	return tenantLLMDTO{Configured: false, KeySet: false}
}

// toTenantLLMDTO arma la respuesta a partir de la fila. La fila ya viene SIN la
// credencial (tenantllm.Config solo trae HasAPIKey).
func toTenantLLMDTO(cfg tenantllm.Config) tenantLLMDTO {
	dto := tenantLLMDTO{
		Configured: true,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		KeySet:     cfg.HasAPIKey,
	}
	if !cfg.ConsentedAt.IsZero() {
		dto.ConsentedAt = cfg.ConsentedAt.UTC().Format(rfc3339)
	}
	if !cfg.CreatedAt.IsZero() {
		dto.CreatedAt = cfg.CreatedAt.UTC().Format(rfc3339)
	}
	if !cfg.UpdatedAt.IsZero() {
		dto.UpdatedAt = cfg.UpdatedAt.UTC().Format(rfc3339)
	}
	return dto
}

// getTenantLLMHandler devuelve GET /api/v1/tenant-llm: la configuración LLM del
// tenant del token (INV-7).
//
// 200 SIEMPRE que se pueda leer, también sin fila (`configured:false`).
//
// La clave NUNCA sale: solo `key_set`. Respuestas: 200; 401 sin identidad; 403
// sin la feature (lo pone el middleware); 500 si el store falla.
func getTenantLLMHandler(ts TenantLLMStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if ts == nil {
			writeError(w, http.StatusInternalServerError, "store de configuración LLM no configurado")
			return
		}
		cfg, found, err := ts.Get(r.Context(), id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo leer la configuración LLM")
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, notConfiguredTenantLLM())
			return
		}
		writeJSON(w, http.StatusOK, toTenantLLMDTO(cfg))
	})
}

// putTenantLLMHandler devuelve PUT /api/v1/tenant-llm.
//
// SEMÁNTICA: upsert COMPLETO, sin excepciones write-only. El cuerpo es la foto
// entera —proveedor, modelo, clave y consentimiento— y la reemplaza. Un PUT
// sobre una fila existente la sustituye entera y refresca `consented_at`; lo
// único que sobrevive es `created_at`.
//
// El tenant sale del token (INV-7): un `tenant_id` en el cuerpo no existe para
// este handler (ver tenantLLMRequest).
//
// Respuestas:
//
//   - 200 con la configuración RESULTANTE releída (misma forma que el GET).
//   - 400 cuerpo no-JSON, `consented` ausente o false, proveedor desconocido,
//     modelo vacío o largo, clave ausente / corta / larga.
//   - 401 sin identidad; 403 sin la feature (middleware); 413 cuerpo excesivo.
//   - 422 provider="local": la vía local NO está cableada en v1 (D-044.4).
//   - 500 fallo del store.
func putTenantLLMHandler(ts TenantLLMStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if ts == nil {
			writeError(w, http.StatusInternalServerError, "store de configuración LLM no configurado")
			return
		}
		req, code, errBody := decodeTenantLLM(r.Body)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		if code, body := validateTenantLLM(req); body != nil {
			writeJSON(w, code, body)
			return
		}

		cfg := tenantllm.Config{
			TenantID: id.TenantID, // INV-7: del token, jamás del cuerpo
			Provider: req.Provider,
			Model:    req.Model,
		}
		// El instante del consentimiento lo pone el SERVIDOR, no el cuerpo: el
		// cliente afirma que consiente (`consented:true`), y cuándo lo afirmó es
		// un hecho observado aquí. Un `consented_at` que viniera del cuerpo sería
		// una fecha que el tenant elige, y el registro de un consentimiento no
		// puede ser antedatable por quien lo da.
		if err := ts.Upsert(r.Context(), cfg, req.APIKey, time.Now().UTC()); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo guardar la configuración LLM")
			return
		}
		// Se RELEE en vez de devolver lo que se acaba de mandar: así la respuesta
		// trae los timestamps de la fila y el `key_set` que quedó de verdad
		// guardado, no el que el handler cree haber guardado.
		cfg, found, err := ts.Get(r.Context(), id.TenantID)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "configuración LLM guardada, pero no se pudo releer")
			return
		}
		writeJSON(w, http.StatusOK, toTenantLLMDTO(cfg))
	})
}

// deleteTenantLLMHandler devuelve DELETE /api/v1/tenant-llm: borra la fila del
// tenant, que vuelve a no tener vía API (design §8: «revoca credenciales y
// consentimiento»). Las dos cosas se van juntas porque viven en la misma fila, y
// eso es lo correcto: un consentimiento que sobreviviera a la retirada de la
// clave sería un permiso vivo sin nada que lo ejerza.
//
// 204 SIEMPRE que la operación se complete, también si no había fila:
// IDEMPOTENTE. Es el mismo criterio que DELETE /api/v1/integrations (el recurso
// es único por tenant y lo que se pide es un ESTADO, no un objeto), y por la
// misma razón no hay 404: obligaría a la pantalla a distinguir dos desenlaces
// que significan lo mismo.
func deleteTenantLLMHandler(ts TenantLLMStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if ts == nil {
			writeError(w, http.StatusInternalServerError, "store de configuración LLM no configurado")
			return
		}
		if err := ts.Delete(r.Context(), id.TenantID); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo borrar la configuración LLM")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// decodeTenantLLM lee el cuerpo del PUT con el techo aplicado ANTES de
// deserializar y recorta los espacios de los campos de texto. Devuelve el status
// + el cuerpo de error ya armado (nil = todo bien); el cuerpo es `any` por lo
// mismo que en decodeIntegration (el 413 lleva max_bytes, ver limits.go).
//
// 🔴 NO recorta ni normaliza `api_key`: un espacio dentro de una credencial es
// parte de la credencial, y «arreglarla» aquí guardaría en silencio algo
// distinto de lo que el tenant pegó. Que un pegado con espacios falle al llamar
// al proveedor es información; que funcione a veces, no.
func decodeTenantLLM(body io.Reader) (tenantLLMRequest, int, any) {
	raw, err := io.ReadAll(io.LimitReader(body, maxTenantLLMBytes+1))
	if err != nil {
		return tenantLLMRequest{}, http.StatusBadRequest, errorBody("no se pudo leer el cuerpo")
	}
	if len(raw) > maxTenantLLMBytes {
		return tenantLLMRequest{}, http.StatusRequestEntityTooLarge, tooLarge("el cuerpo", maxTenantLLMBytes)
	}
	var req tenantLLMRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return tenantLLMRequest{}, http.StatusBadRequest,
			errorBody("el cuerpo debe ser un JSON {provider, model, api_key, consented}")
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	return req, 0, nil
}

// validateTenantLLM comprueba lo que la BD no puede comprobar sola, o lo que no
// debe llegar a comprobar ella. Devuelve (status, cuerpo); cuerpo nil =
// configuración admisible.
//
// EL ORDEN IMPORTA y es el mismo criterio que el de design §8.1: primero el
// CONSENTIMIENTO, que es lo que autoriza a que exista la fila; después el
// vocabulario; y al final la forma de los valores. Poner el consentimiento antes
// que el resto no es cosmético: es lo que hace que «PUT sin consentimiento ⇒
// 400» sea cierto SIEMPRE, también cuando el cuerpo trae además un proveedor
// inválido. Si el proveedor se comprobara primero, ese caso devolvería 400 por
// el motivo equivocado y el criterio de T0.3 pasaría por casualidad.
func validateTenantLLM(req tenantLLMRequest) (int, any) {
	if !req.Consented {
		return http.StatusBadRequest, map[string]string{
			"error":  "consent_required",
			"detail": "hay que consentir explícitamente (consented:true) que el texto de las conversaciones salga hacia el proveedor externo",
		}
	}
	if code, body := validateLLMProvider(req.Provider); body != nil {
		return code, body
	}
	if req.Model == "" || len(req.Model) > maxLLMModelLen {
		return http.StatusBadRequest, errorBody("model es obligatorio y no puede pasar de 128 caracteres")
	}
	if len(req.APIKey) < minLLMAPIKeyLen || len(req.APIKey) > maxLLMAPIKeyLen {
		// 🔴 EL MENSAJE NO REPITE LA CLAVE NI SU LONGITUD REAL. Decir «has
		// mandado 7 caracteres» convertiría este endpoint en un medidor, y el
		// mensaje acaba en el log de acceso del cliente.
		return http.StatusBadRequest, errorBody("api_key es obligatoria en cada PUT y debe tener entre 16 y 512 caracteres")
	}
	return 0, nil
}

// validateLLMProvider separa los TRES desenlaces del campo `provider`, que es la
// parte del contrato donde más fácil sería mezclarlos.
//
// Se extrae a función propia y no se deja inline en validateTenantLLM por
// gocyclo, y de paso porque el `local` merece su propio sitio: es el único valor
// que se entiende y no se puede.
func validateLLMProvider(provider string) (int, any) {
	switch provider {
	case tenantllm.ProviderAnthropic, tenantllm.ProviderGemini:
		return 0, nil
	case tenantllm.ProviderLocal:
		// 422 y no 400, y es la MISMA distinción que la 0047 hizo con
		// catalog_adapter='http' (integrations.go:301-307): la petición está bien
		// formada y el valor es del vocabulario del PRODUCTO —la vía local existe
		// en el ADR-0030 y el clasificador local del Edge es real— lo que no
		// existe es la implementación de esta vía en el pipeline del Plan 044
		// (D-044.4, D-044.21). Es «te entiendo y no puedo», no «no te entiendo».
		//
		// El código de error es el mismo que el design §8.1 fija para /reanalyze,
		// a propósito: el mismo «no» se dice con la misma palabra en las dos
		// puertas, y la UI puede tratarlo en un solo sitio.
		return http.StatusUnprocessableEntity, map[string]string{
			"error":    "llm_provider_unavailable",
			"provider": tenantllm.ProviderLocal,
		}
	default:
		return http.StatusBadRequest, map[string]string{
			"error":    "invalid_provider",
			"provider": provider,
		}
	}
}
