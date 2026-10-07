// Porta internal/publicapi/tenantllm.go @ 3c74b80 y el registro de F1–F3
// (internal/publicapi/publicapi.go @ 3c74b80, registerTenantLLM, líneas 940-991).
//
// tenantllm.go — LA CONFIGURACIÓN DE LA VÍA LLM POR TENANT (Plan 044 · Ola 0 · T0.3, design §8;
// mapa §2.6, F1–F3): GET / PUT / DELETE de /api/v1/tenant-llm. El dominio vive en
// internal/modulos/inferencia/tenantllm; aquí solo se abre la puerta HTTP.
//
// Lo que este fichero NO hace, y es la mitad de su trabajo: NUNCA tiene la API key en una
// variable después de guardarla. El puerto TenantLLMStore no expone método que la devuelva, así
// que un futuro `log.Printf("%+v", cfg)` en esta capa no puede filtrarla.
//
// En el rojo solo existían el puerto, TenantLLMDeps y MountTenantLLM; los tres handlers y sus
// auxiliares nacieron con el verde (05 E-4, P6). La forma del cuerpo del PUT y su lectura
// (tenantLLMRequest, decodeTenantLLM y los techos) viven en tenantllm_body.go (E-13).

package apipublica

import (
	"context"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// TenantLLMStore es el puerto MÍNIMO de la configuración LLM por tenant (public.tenant_llm) que
// la cara consume. Lo satisfacen *tenantllm.Postgres del módulo inferencia NUEVO y, en los
// tests, *tenantllmhelpertest.Memoria.
//
// 🔴 ES UN SUBCONJUNTO ESTRICTO de tenantllm.Store, y le falta EXACTAMENTE un método: APIKey.
// Esa ausencia es el mecanismo por el que la credencial en claro no cruza la frontera de este
// paquete (zero-knowledge de cara afuera): ningún método del puerto devuelve la clave. Quien
// necesite el valor es el pipeline, y le pedirá el puerto completo.
//
// TODAS las operaciones van acotadas al tenant (INV-7): el tenant es un ARGUMENTO que sale del
// token, y la tabla tiene tenant_id como PRIMARY KEY. No hay forma de pedirle la fila de otro.
type TenantLLMStore interface {
	// Get devuelve la configuración del tenant SIN la credencial (solo HasAPIKey); found false
	// ⇒ el tenant no tiene fila.
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
	// Upsert crea o reemplaza ENTERA la configuración del tenant (ver tenantllm.Store).
	Upsert(ctx context.Context, cfg tenantllm.Config, apiKey string, consentedAt time.Time) error
	// Delete borra la fila del tenant; borrar lo que no hay no es un error.
	Delete(ctx context.Context, tenantID string) error
}

// TenantLLMDeps es lo que F1–F3 necesitan. En la cara vieja eran los campos TenantLLM y
// Entitlements de publicapi.Deps.
type TenantLLMDeps struct {
	// TenantLLM es el almacén de la configuración. nil ⇒ no se monta ninguna de las tres.
	TenantLLM TenantLLMStore
	// Entitlements es el resolver de derechos del módulo acceso NUEVO, el MISMO (una sola caché)
	// que gatea el resto de la plataforma. nil ⇒ no se monta ninguna de las tres.
	Entitlements entitlements.Resolver
}

// MountTenantLLM registra en c las TRES rutas o NINGUNA: solo si d.TenantLLM y d.Entitlements
// son los dos distintos de nil. Si falta cualquiera, ninguna existe (404 de ruta inexistente):
// mejor eso que un endpoint de credenciales que responde 500 a medio camino o, peor, que guarda
// una clave sin poder comprobar el plan.
//
//   - F1 "GET /api/v1/tenant-llm": cadena R, permiso "llm.read";
//   - F2 "PUT /api/v1/tenant-llm": cadena W, permiso "llm.write", recurso de auditoría
//     "tenant_llm";
//   - F3 "DELETE /api/v1/tenant-llm": cadena W, permiso "llm.write", recurso "tenant_llm".
//
// La cadena es la de Common (401 sin token; 403 {"error":"permiso denegado"} sin el permiso o
// con un token sin empresa; ningún registro en esos dos) y, POR DENTRO de ella, el gate de la
// feature "api_llm" (entitlements.RequireFeature) EN LAS TRES, incluida la lectura: que el GET no
// devuelva la clave no lo hace inocuo, dice si el tenant tiene vía API y con qué proveedor.
// El orden es: Authenticate → RequirePermission → (solo W) auditoría → gate → handler. Sus
// consecuencias observables:
//
//   - sin el permiso Y sin la feature ⇒ el 403 es el del permiso, no el del gate;
//   - con el permiso y sin la feature ⇒ 403 con EXACTAMENTE el cuerpo
//     {"error":"feature_not_enabled","feature":"api_llm"}; el almacén no se toca. Tener
//     "llm_intake" NO la abre: "api_llm" gatea la VÍA —configurar y usar credenciales de un
//     proveedor externo—, no la capacidad (ADR-0044, D-044.28);
//   - el gate es fail-closed: un resolver que falla corta con ese MISMO 403, nunca con 500;
//   - en F2 y F3 el corte del gate SÍ deja su registro de auditoría (va por dentro de la
//     auditoría): uno, con Result "failure" y Meta {"status":403}. En F1, ninguno;
//   - se audita la ACCIÓN, jamás el cuerpo: el registro no lleva la credencial ni PII.
//
// El tenant es SIEMPRE el del token (INV-7): no viaja en la ruta ni en la query, y un
// "tenant_id" en el cuerpo del PUT se descarta sin ruido (el cuerpo no tiene dónde guardarlo).
//
// 🔴 LA CLAVE NO SALE POR NINGUNA DE LAS TRES: ninguna respuesta la lleva, ni entera ni
// truncada, ni su huella; lo único que se publica de ella es el booleano "key_set". Y ninguna
// de las tres pide la credencial al almacén (el puerto no tiene con qué).
//
// F1 lee la configuración: Get(tenant).
//
//   - sin fila ⇒ 200 {"configured":false,"via":"local","key_set":false}. No es un 404: «no
//     tengo vía API» es una respuesta, y dice "local" (no vacío) porque un tenant sin fila está
//     en la vía por defecto (REQ-33);
//   - con fila ⇒ 200 {"configured":true,"via","provider","model","key_set","consented_at",
//     "created_at","updated_at"}, en ese orden. "via" y "key_set" salen SIEMPRE; "provider" y
//     "model" se omiten si están vacíos (la fila de la vía local) y cada instante se omite si
//     es el cero; los instantes van en UTC, RFC 3339 con segundos;
//   - fallo de Get ⇒ 500 {"error":"no se pudo leer la configuración LLM"}.
//
// F2 es un upsert COMPLETO: el cuerpo {"via","provider","model","api_key","consented"} es la
// foto entera y reemplaza la que hubiera. En orden:
//
//   - cuerpo de más de 8192 bytes ⇒ 413 {"error":"el cuerpo excede el tamaño máximo de 8192
//     bytes","max_bytes":8192} (con 8192 exactos pasa); cuerpo ilegible ⇒ 400 {"error":"no se
//     pudo leer el cuerpo"}; cuerpo que no es ese JSON (vacío, otro tipo en un campo) ⇒ 400
//     {"error":"el cuerpo debe ser un JSON {via, provider, model, api_key, consented}"};
//   - "via", "provider" y "model" se recortan de espacios; "api_key" NO se recorta ni se
//     normaliza: llega al almacén byte a byte como vino;
//   - LA VÍA, antes que nada: ausente o fuera de {"local","api"} ⇒ 400
//     {"error":"invalid_via","via":"<lo recibido, recortado>"}. No tiene defecto: un cuerpo con
//     la forma vieja (sin "via") NO se acepta como vía API;
//   - "via":"local" NO EXIGE NADA (REQ-33): ni consentimiento, ni proveedor, ni modelo, ni
//     clave. Se guarda Upsert(Config{TenantID, Via:"local"}, "", instante cero): el proveedor,
//     el modelo y la clave que traiga el cuerpo NO se guardan y la clave ni cruza la llamada;
//   - "via":"api", en este orden: "consented" ausente o false ⇒ 400 {"detail":"hay que
//     consentir explícitamente (consented:true) que el texto de las conversaciones salga hacia
//     el proveedor externo","error":"consent_required"} (antes que el proveedor: un cuerpo sin
//     consentimiento Y con proveedor inválido falla por el consentimiento); "provider" fuera de
//     {"anthropic","gemini"} —"local" incluido— ⇒ 400 {"error":"invalid_provider",
//     "provider":"<lo recibido>"}; "model" vacío o de más de 128 bytes ⇒ 400 {"error":"model es
//     obligatorio y no puede pasar de 128 caracteres"}; "api_key" de menos de 16 o más de 512
//     bytes (ausente incluida: es obligatoria en CADA put) ⇒ 400 {"error":"api_key es
//     obligatoria en cada PUT y debe tener entre 16 y 512 caracteres"}, que no repite la clave
//     ni su longitud;
//   - ningún 400 ni 413 escribe: el almacén no recibe Upsert;
//   - válido en la vía api ⇒ Upsert(Config{TenantID, Via, Provider, Model}, api_key, ahora en
//     UTC): el instante del consentimiento lo pone el SERVIDOR, no el cuerpo. Si falla ⇒ 500
//     {"error":"no se pudo guardar la configuración LLM"};
//   - después RELEE con Get y responde 200 con la misma forma que F1 (los instantes y el
//     "key_set" son los de la fila, no los que el handler cree haber guardado). Si la relectura
//     falla o no encuentra fila ⇒ 500 {"error":"configuración LLM guardada, pero no se pudo
//     releer"}.
//
// F3 borra la fila: Delete(tenant) ⇒ 204 sin cuerpo, también si no había fila (IDEMPOTENTE, sin
// 404); revoca credencial y consentimiento de una vez. Fallo ⇒ 500 {"error":"no se pudo borrar
// la configuración LLM"}.
//
// Las tres llaman al almacén con el contexto de la petición, sin plazo propio, como en la cara
// vieja.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara a un handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common).
func MountTenantLLM(c *Cara, k Common, d TenantLLMDeps) {
	// Sin store o sin resolver de features las rutas NO se montan: mejor un 404 de ruta
	// inexistente que un endpoint de credenciales que responde 500 a medio camino o, peor, que
	// guarda una clave sin poder comprobar el plan.
	if d.TenantLLM == nil || d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountTenantLLM")

	// Gate `api_llm` en las TRES rutas, incluida la lectura. Que el GET no devuelva la clave no
	// lo convierte en inocuo: dice si el tenant tiene vía API y con qué proveedor, y eso es
	// exactamente la información del add-on que el gate acota.
	//
	// 🔴 Y ÉSTE ES EL ÚNICO SITIO DE LA CARA DONDE `api_llm` DECIDE ALGO (ADR-0044, D-044.28,
	// T1.5-1). No porque haya quedado suelto, sino porque es lo que la feature significa: gatea
	// la VÍA —configurar y usar credenciales de un proveedor externo—, no la CAPACIDAD. El
	// carril de captación mira `llm_intake` y solo `llm_intake`, y un tenant que tenga la
	// capacidad SIN la vía sigue recibiendo aquí los tres 403 de siempre: tener nivel no es
	// tener cuenta de pago.
	apiLLM := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureAPILLM)

	// SCOPES PROPIOS (llm.read / llm.write): esta fila guarda una credencial de pago de un
	// proveedor externo, y quien pueda escribirla puede apuntar el gasto del tenant a una cuenta
	// ajena. Ese poder no viene incluido en «puede editar el catálogo». El reparto no necesita
	// migración de grants: tenant_admin (`*`) y viewer (`*.read`) están sembrados con glob
	// (0015_iam_roles.sql:65,73) y cubren las dos claves; operator lleva lista EXPLÍCITA y no
	// alcanza ninguna de las dos, que es el reparto que se busca: configurar la credencial de
	// pago del tenant no es la faena del turno.
	//
	// Escrituras auditadas (llm.write / recurso `tenant_llm`) sin la credencial y sin PII: se
	// audita la ACCIÓN, jamás el cuerpo. El gate va POR DENTRO de la cadena, como en la cara
	// vieja: su 403 también queda en la bitácora de las escrituras.
	c.Handle("GET /api/v1/tenant-llm", protectRead(k, "llm.read", apiLLM(getTenantLLMHandler(d.TenantLLM))))
	c.Handle("PUT /api/v1/tenant-llm", protect(k, "llm.write", "tenant_llm", apiLLM(putTenantLLMHandler(d.TenantLLM))))
	c.Handle("DELETE /api/v1/tenant-llm", protect(k, "llm.write", "tenant_llm", apiLLM(deleteTenantLLMHandler(d.TenantLLM))))
}

// tenantLLMDTO es el contrato de GET y de la respuesta del PUT (la MISMA forma en los dos: la
// pantalla que lo pinta no tiene por qué saber cuál acaba de llamar).
//
// `configured` distingue «no hay vía API» de «la hay»: sin ese booleano, un DELETE seguido de un
// GET sería indistinguible de un tenant que nunca configuró nada, y la pantalla no sabría si
// ofrecer «borrar».
//
// 🔴 LA CLAVE SALE EN UN SOLO CAMPO Y ES UN BOOLEANO: `key_set`. «El GET nunca devuelve la
// clave» es aquí estructural, no una omisión al serializar: el struct no tiene dónde ponerla.
//
// 🔴 Y NO HAY HUELLA, al revés que en la integración CRM. La huella del secreto HMAC existe
// porque el tenant tiene con qué compararla —el mismo secreto está configurado en SU puente
// (D-042.7)—. Una API key de Anthropic no tiene contraparte que comparar: nadie la teclea dos
// veces en dos sitios. Publicar su huella sería regalar un oráculo de confirmación offline
// sobre un valor de formato conocido y público (`sk-ant-…`) a cambio de una pregunta que nadie
// hace. Si algún día aparece esa pregunta, ESE día se añade el campo.
//
// 🔴 `via` SALE SIEMPRE Y SIN `omitempty` (T1.5-2, REQ-33), al revés que `provider`/`model`. Un
// tenant SIEMPRE tiene vía —la que no configuró es `local`, que es el default del producto y no
// «ninguna»—, así que un campo que desapareciera del JSON obligaría a la pantalla a inventar el
// valor ausente, y la mitad de las pantallas lo inventaría distinto. `provider` sí puede faltar,
// y su ausencia significa algo concreto: la vía local no llama a ningún tercero.
type tenantLLMDTO struct {
	Configured  bool   `json:"configured"`
	Via         string `json:"via"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	KeySet      bool   `json:"key_set"`
	ConsentedAt string `json:"consented_at,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// notConfiguredTenantLLM es lo que responde el GET de un tenant SIN fila: la vía API no está
// configurada. No es un 404 — «no tengo vía API» es una respuesta, y es la que la pantalla
// necesita para dibujar el formulario vacío.
//
// 🔴 Y RESPONDE `via:"local"`, NO VACÍO (REQ-33). Un tenant sin fila no está «sin vía»: está en
// la vía por defecto, que es la local. Decir vacío obligaría a la pantalla a traducir la
// ausencia, y esa traducción es justo la que no debe vivir en el cliente — vive aquí, en la
// única capa que conoce el default.
func notConfiguredTenantLLM() tenantLLMDTO {
	return tenantLLMDTO{Configured: false, Via: tenantllm.ViaLocal, KeySet: false}
}

// toTenantLLMDTO arma la respuesta a partir de la fila. La fila ya viene SIN la credencial
// (tenantllm.Config solo trae HasAPIKey).
func toTenantLLMDTO(cfg tenantllm.Config) tenantLLMDTO {
	dto := tenantLLMDTO{
		Configured: true,
		Via:        cfg.Via,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		KeySet:     cfg.HasAPIKey,
	}
	if !cfg.ConsentedAt.IsZero() {
		dto.ConsentedAt = cfg.ConsentedAt.UTC().Format(time.RFC3339)
	}
	if !cfg.CreatedAt.IsZero() {
		dto.CreatedAt = cfg.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !cfg.UpdatedAt.IsZero() {
		dto.UpdatedAt = cfg.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// getTenantLLMHandler devuelve GET /api/v1/tenant-llm: la configuración LLM del tenant del token
// (INV-7). 200 SIEMPRE que se pueda leer, también sin fila (`configured:false`). La clave NUNCA
// sale: solo `key_set`.
//
// La rama vieja «ts == nil ⇒ 500 store de configuración LLM no configurado» no se porta, ni
// aquí ni en los otros dos handlers: las rutas solo se montan con un almacén no nil
// (MountTenantLLM), así que era inalcanzable.
func getTenantLLMHandler(ts TenantLLMStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
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
// SEMÁNTICA: upsert COMPLETO, sin excepciones write-only. El cuerpo es la foto entera
// —proveedor, modelo, clave y consentimiento— y la reemplaza. Un PUT sobre una fila existente la
// sustituye entera y refresca `consented_at`; lo único que sobrevive es `created_at`.
//
// El tenant sale del token (INV-7): un `tenant_id` en el cuerpo no existe para este handler (ver
// tenantLLMRequest).
//
// LOS DOS EJES (T1.5-2, D-044.22 ratificada): el cuerpo trae `via` —quién ejecuta— y, SOLO si la
// vía es `api`, `provider` —a qué tercero se llama—. Un cuerpo con `provider` y sin `via` es 400
// (no se adivina la vía); un cuerpo con `via:"api"` y sin `provider` es 400 por el vocabulario
// del proveedor; y con `via:"local"` el `provider` que venga NO se guarda, porque la vía local
// no llama a ningún tercero y una fila local con proveedor sería una contradicción escrita en la
// base.
func putTenantLLMHandler(ts TenantLLMStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
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

		cfg, apiKey, consentedAt := upsertFrom(req, id.TenantID)
		if err := ts.Upsert(r.Context(), cfg, apiKey, consentedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo guardar la configuración LLM")
			return
		}
		// Se RELEE en vez de devolver lo que se acaba de mandar: así la respuesta trae los
		// timestamps de la fila y el `key_set` que quedó de verdad guardado, no el que el
		// handler cree haber guardado.
		cfg, found, err := ts.Get(r.Context(), id.TenantID)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "configuración LLM guardada, pero no se pudo releer")
			return
		}
		writeJSON(w, http.StatusOK, toTenantLLMDTO(cfg))
	})
}

// upsertFrom (upsertDesde en la cara vieja) traduce el cuerpo YA VALIDADO a los tres argumentos
// del store. Existe para que el handler no cargue con la rama de la vía (gocyclo mide el handler
// entero) y, sobre todo, para que la decisión quepa entera en un sitio.
//
// 🔴 EN LA VÍA LOCAL, LA CREDENCIAL NI SIQUIERA CRUZA LA LLAMADA. El store ya la ignoraría
// —escribe NULL en el sobre—, pero pasarla igual dejaría la clave en un argumento vivo de una
// operación que no la usa, y el criterio de este paquete es el contrario: lo que no hace falta,
// no se mueve. Lo mismo con el instante de consentimiento: en la vía local no hay a qué
// consentir, así que va el cero, y el cero significa NULL.
//
// El instante del consentimiento lo pone el SERVIDOR, no el cuerpo: el cliente afirma que
// consiente (`consented:true`), y cuándo lo afirmó es un hecho observado aquí. Un `consented_at`
// que viniera del cuerpo sería una fecha que el tenant elige, y el registro de un consentimiento
// no puede ser antedatable por quien lo da.
func upsertFrom(req tenantLLMRequest, tenantID string) (tenantllm.Config, string, time.Time) {
	cfg := tenantllm.Config{
		TenantID: tenantID, // INV-7: del token, jamás del cuerpo
		Via:      req.Via,
	}
	if req.Via != tenantllm.ViaAPI {
		return cfg, "", time.Time{}
	}
	cfg.Provider = req.Provider
	cfg.Model = req.Model
	return cfg, req.APIKey, time.Now().UTC()
}

// deleteTenantLLMHandler devuelve DELETE /api/v1/tenant-llm: borra la fila del tenant, que
// vuelve a no tener vía API (design §8: «revoca credenciales y consentimiento»). Las dos cosas
// se van juntas porque viven en la misma fila, y eso es lo correcto: un consentimiento que
// sobreviviera a la retirada de la clave sería un permiso vivo sin nada que lo ejerza.
//
// 204 SIEMPRE que la operación se complete, también si no había fila: IDEMPOTENTE. El recurso
// es único por tenant y lo que se pide es un ESTADO, no un objeto, y por eso no hay 404:
// obligaría a la pantalla a distinguir dos desenlaces que significan lo mismo.
func deleteTenantLLMHandler(ts TenantLLMStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if err := ts.Delete(r.Context(), id.TenantID); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo borrar la configuración LLM")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// validateTenantLLM comprueba lo que la BD no puede comprobar sola, o lo que no debe llegar a
// comprobar ella. Devuelve (status, cuerpo); cuerpo nil = configuración admisible.
//
// EL ORDEN IMPORTA y es el mismo criterio que el de design §8.1:
//
//  1. LA VÍA, antes que nada (T1.5-2). Es lo que decide QUÉ hay que exigir: sin saberla,
//     cualquier otra comprobación estaría preguntando por campos que quizá no apliquen. Un
//     cuerpo sin `via` no es «un PUT de vía API al que le falta un campo»: es un PUT del que no
//     se sabe qué es.
//  2. EL CONSENTIMIENTO, que es lo que autoriza a que exista la fila `api`.
//  3. El vocabulario del PROVEEDOR.
//  4. Y al final la forma de los valores.
//
// Poner el consentimiento por delante del resto (dentro de su vía) no es cosmético: es lo que
// hace que «PUT sin consentimiento ⇒ 400» sea cierto SIEMPRE, también cuando el cuerpo trae
// además un proveedor inválido. Si el proveedor se comprobara primero, ese caso devolvería 400
// por el motivo equivocado.
//
// 🔴 REQ-33 EN LA RAMA `local`: no se comprueba consentimiento, ni proveedor, ni modelo, ni
// clave — y esa AUSENCIA de comprobaciones ES la tarea, no un hueco. «Elegir local no exige
// nada» solo es cierto si aquí no hay nada que exigir.
func validateTenantLLM(req tenantLLMRequest) (int, any) {
	if !tenantllm.ValidVia(req.Via) {
		// El campo se NOMBRA en la respuesta, como hace el resto de esta API con el sujeto del
		// rechazo. Vacío incluido: `{"via":""}` le dice al cliente «no mandaste vía», que es
		// distinto de «mandaste una que no existe» y se lee igual de bien en un log.
		return http.StatusBadRequest, map[string]string{
			"error": "invalid_via",
			"via":   req.Via,
		}
	}
	if req.Via == tenantllm.ViaLocal {
		// 🔴 LA VÍA LOCAL YA ESTÁ CABLEADA (T1.6-3): aquí vivía el 422 «te entiendo y no
		// puedo», y su borrado es literalmente el criterio de la tarea. Lo que queda es lo que
		// REQ-33 pide: elegir local no exige NADA —ni consentimiento, ni proveedor, ni modelo,
		// ni clave— porque la vía local no manda texto a ningún tercero. Esta rama es un
		// `return` limpio a propósito: la ausencia de comprobaciones ES el requisito.
		return 0, nil
	}
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
		// 🔴 EL MENSAJE NO REPITE LA CLAVE NI SU LONGITUD REAL. Decir «has mandado 7
		// caracteres» convertiría este endpoint en un medidor, y el mensaje acaba en el log de
		// acceso del cliente.
		return http.StatusBadRequest, errorBody("api_key es obligatoria en cada PUT y debe tener entre 16 y 512 caracteres")
	}
	return 0, nil
}

// validateLLMProvider separa los DOS desenlaces del campo `provider`: el vocabulario admitido
// de la vía API, y todo lo demás.
//
// 🔧 ERAN TRES HASTA T1.6-3, y el que se fue merece explicación porque cambió una respuesta
// pública. `provider:"local"` devolvía 422 `llm_provider_unavailable` con el argumento «te
// entiendo y no puedo»: la vía local existía en el ADR-0030 pero no había pipeline que la
// ejecutara. Ese argumento CADUCÓ el día que nació el adaptador local: hoy la vía local sí se
// puede, solo que se pide por el eje que le corresponde —`via:"local"`— y no por este campo,
// que solo describe QUÉ PROVEEDOR EXTERNO se llama cuando la vía es `api`. Un cuerpo con
// `via:"api", provider:"local"` ya no es «entendible e imposible»: es contradictorio, y eso es
// un 400 `invalid_provider` como cualquier otro valor inventado. tenantllm.ProviderLocal
// sobrevive como constante DOCUMENTAL y no gobierna ninguna rama.
//
// Se extrae a función propia y no se deja inline en validateTenantLLM por gocyclo.
func validateLLMProvider(provider string) (int, any) {
	switch provider {
	case tenantllm.ProviderAnthropic, tenantllm.ProviderGemini:
		return 0, nil
	default:
		return http.StatusBadRequest, map[string]string{
			"error":    "invalid_provider",
			"provider": provider,
		}
	}
}
