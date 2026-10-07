// Porta internal/publicapi/tenantllm.go @ 3c74b80 (los techos del PUT, tenantLLMRequest y
// decodeTenantLLM).
//
// tenantllm_body.go — EL CUERPO DEL PUT DE /api/v1/tenant-llm: su forma, sus techos y su
// lectura. Es un trozo de tenantllm.go partido por tema (05 E-13): solo declaraciones movidas.
// No exporta nada y nació con el verde (05 E-4, P6); lo cubre tenantllm_body_test.go a través
// de MountTenantLLM.

package apipublica

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Techos del PUT. El cuerpo es un objeto de cuatro campos cortos: el límite existe para que un
// cliente roto no empuje memoria por un endpoint de configuración.
const (
	maxTenantLLMBytes = 1 << 13 // 8 KiB de cuerpo
	// maxLLMModelLen acota el identificador de modelo. wApp NO valida el modelo contra una
	// lista de valores conocidos —esa lista caduca cada pocas semanas y una lista caducada
	// rechaza modelos válidos—, así que lo único que se comprueba es que sea texto y no un
	// ensayo.
	maxLLMModelLen = 128
	// minLLMAPIKeyLen / maxLLMAPIKeyLen acotan la credencial. El mínimo NO es una política de
	// fortaleza —la clave no la elige el tenant, se la da el proveedor— sino un filtro contra
	// el error de dedo: un campo con tres caracteres es un formulario mal rellenado, y
	// guardarlo cifrado dejaría al tenant creyendo que configuró algo que va a fallar en el
	// primer job.
	minLLMAPIKeyLen = 16
	maxLLMAPIKeyLen = 512
)

// tenantLLMRequest es el cuerpo del PUT.
//
// NO TIENE CAMPO tenant_id, y esa ausencia ES el mecanismo de INV-7: un cuerpo que traiga
// `tenant_id` de otro tenant lo descarta encoding/json sin ruido, y la operación va contra el
// tenant del token. No hace falta comprobarlo ni rechazarlo — no hay dónde guardarlo.
//
// `consented` es OBLIGATORIO y tiene que venir en `true`: es el consentimiento explícito a que
// el texto de las conversaciones salga hacia un proveedor externo (ADR-0030). Un `false` o su
// ausencia son 400, no un upsert silencioso con consentimiento apagado — la tabla no tiene
// forma de representar eso.
//
// `api_key` es OBLIGATORIA en cada PUT DE LA VÍA API, y ahí este contrato se separa a propósito
// del de integrations (donde `secret` vacío conserva el existente): ver validateTenantLLM.
//
// 🔴 `via` ES OBLIGATORIO Y NO TIENE DEFECTO EN EL CUERPO (T1.5-2, REQ-33), y esto es una
// decisión, no un olvido. El default `local` vive en la COLUMNA: es el estado de quien todavía
// no ha elegido. Pero un PUT es LITERALMENTE el acto de elegir —«cambiar de vía es un acto de
// configuración del tenant», REQ-33—, y un acto de configuración que elige por ti la vía cuando
// callas es la forma más barata de acabar con un tenant en una vía que no pidió. Ausente o
// desconocido ⇒ 400 `invalid_via`.
//
// Efecto colateral BUSCADO: un cuerpo con la forma vieja —`{provider, model, api_key,
// consented}`, sin `via`— NO se acepta en silencio como vía API. Falla nombrado, que es lo que
// se le pide a un contrato que cambia en alpha.
type tenantLLMRequest struct {
	Via       string `json:"via"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
	Consented bool   `json:"consented"`
}

// decodeTenantLLM lee el cuerpo del PUT con el techo aplicado ANTES de deserializar y recorta
// los espacios de los campos de texto. Devuelve el status + el cuerpo de error ya armado (nil =
// todo bien); el cuerpo es `any` porque el 413 lleva max_bytes (ver limits.go).
//
// 🔴 NO recorta ni normaliza `api_key`: un espacio dentro de una credencial es parte de la
// credencial, y «arreglarla» aquí guardaría en silencio algo distinto de lo que el tenant pegó.
// Que un pegado con espacios falle al llamar al proveedor es información; que funcione a veces,
// no.
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
			errorBody("el cuerpo debe ser un JSON {via, provider, model, api_key, consented}")
	}
	req.Via = strings.TrimSpace(req.Via)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	return req, 0, nil
}
