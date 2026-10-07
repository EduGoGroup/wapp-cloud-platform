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
// auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("apipublica.MountTenantLLM"))
}
