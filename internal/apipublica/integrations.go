// Porta internal/publicapi/integrations.go @ ed60c24 (493 líneas: de aquí salen las líneas 1-261
// y 376-493; las 263-374, la lectura y la validación del cuerpo del PUT, van en
// integrations_validate.go por 05 E-13) y su registro (internal/publicapi/publicapi.go @
// ed60c24, registerIntegrations, líneas 887-934).
//
// integrations.go — LA CONFIGURACIÓN DEL PUENTE CRM (Plan 042 · T5.1 y T5.2, design §5; mapa
// §2.7, G13–G16): GET / PUT / DELETE de /api/v1/integrations, más el
// GET /api/v1/integrations/outbox que enseña cómo va la cola. El dominio vive en
// internal/modulos/solicitudes/integrations; aquí solo se abre la puerta HTTP.
//
// La IDA del puente (webhook_outbox) y la VUELTA (el callback firmado, crmcallback.go) NO pasan
// por aquí: esto es la configuración que las dos leen, y —desde el endpoint del outbox— el
// resumen de cómo le está yendo a la ida. El resumen son CONTADORES: esta puerta no abre las
// entregas ni su contenido.
//
// 🔴 EL SECRETO DE FIRMA ES WRITE-ONLY. Entra por el PUT y no sale por ninguna puerta: ni en una
// respuesta, ni en un log, ni en la auditoría. Es un secreto del envelope de NEGOCIO de esta
// pieza (el de la firma del puente); no tiene nada que ver con las llaves del Edge.
//
// En el rojo solo existían el puerto, IntegrationsDeps y MountIntegrations; los handlers y sus
// auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// IntegrationsStore es el puerto MÍNIMO de la configuración del puente por tenant
// (public.tenant_integrations) que la cara consume. Lo satisface *integrations.Postgres del
// módulo solicitudes NUEVO.
//
// TODAS las operaciones van acotadas al tenant (INV-8), y el aislamiento lo garantiza la firma:
// el tenant es un ARGUMENTO que sale del token, y la tabla tiene tenant_id como PRIMARY KEY. No
// hay forma de pedirle la fila de otro.
//
// 🔴 NO TIENE MÉTODO QUE DEVUELVA EL SECRETO, y la ausencia es el mecanismo: SecretFingerprint
// devuelve la huella, NO el valor, que así no existe nunca en una variable de esta capa —la que
// serializa respuestas y escribe logs—. Y CountOutbox devuelve números, NO entregas: el mismo
// criterio aplicado a la cola.
type IntegrationsStore interface {
	// GetTenantIntegration devuelve la fila de tenantID SIN el secreto (solo HasSecret). found
	// false ⇒ el tenant no tiene fila.
	GetTenantIntegration(ctx context.Context, tenantID string) (integrations.TenantIntegration, bool, error)
	// UpsertTenantIntegration guarda la configuración entera. secret vacío ⇒ conserva el que
	// hubiera.
	UpsertTenantIntegration(ctx context.Context, ti integrations.TenantIntegration, secret string) error
	// DeleteTenantIntegration borra la fila entera, secreto incluido. Borrar lo que no hay no es
	// un error.
	DeleteTenantIntegration(ctx context.Context, tenantID string) error
	// SecretFingerprint devuelve la huella corta del secreto. found false ⇒ no hay secreto.
	SecretFingerprint(ctx context.Context, tenantID string) (string, bool, error)
	// CountOutbox devuelve cuántas entregas de tenantID hay en cada estado.
	CountOutbox(ctx context.Context, tenantID string) (integrations.OutboxCounts, error)
}

// IntegrationsDeps es lo que G13–G16 necesitan. En la cara vieja eran los campos Integrations y
// Entitlements de publicapi.Deps.
type IntegrationsDeps struct {
	// Integrations guarda y lee la configuración del puente. nil ⇒ G13–G16 no se montan.
	Integrations IntegrationsStore
	// Entitlements es el resolver de derechos del módulo acceso NUEVO, el MISMO (una sola caché)
	// que gatea el resto de la plataforma. nil ⇒ G13–G16 no se montan.
	Entitlements entitlements.Resolver
}

// MountIntegrations registra en c las CUATRO rutas de /api/v1/integrations, juntas o ninguna
// (T-2), solo si d.Integrations y d.Entitlements son los dos distintos de nil. Si falta
// cualquiera las rutas no existen (404 de ruta inexistente): mejor que una configuración que
// responde 500 a medio camino o, peor, que se guarda sin poder comprobar el plan.
//
//   - G13 "GET /api/v1/integrations": cadena R, permiso "integrations.read";
//   - G14 "GET /api/v1/integrations/outbox": cadena R, permiso "integrations.read";
//   - G15 "PUT /api/v1/integrations": cadena W, permiso "integrations.write", recurso de
//     auditoría "integration";
//   - G16 "DELETE /api/v1/integrations": cadena W, el mismo permiso y recurso.
//
// (Ver Common: 401 sin token; 403 {"error":"permiso denegado"} sin el permiso o con un token sin
// empresa; las R no dejan registro de auditoría y las W dejan exactamente uno, que lleva la
// ACCIÓN y jamás el cuerpo ni el secreto.)
//
// DOS GUARDIAS en las cuatro: el scope dice «puedes operar esto» y, POR DENTRO de la cadena, el
// gate de la feature "crm_bridge" (entitlements.RequireFeature) dice «tu plan lo incluye»
// (D-042.8). El orden es Authenticate → RequirePermission → (solo W) auditoría → gate → handler:
//
//   - sin el permiso Y sin la feature ⇒ el 403 es el del permiso, no el del gate;
//   - con el permiso y sin la feature ⇒ 403 con EXACTAMENTE el cuerpo
//     {"error":"feature_not_enabled","feature":"crm_bridge"}, también en los dos GET (la
//     configuración del puente no es información que un tenant sin puente deba consultar); el
//     puerto no se toca, y en las W ese 403 SÍ queda auditado (como "failure");
//   - el gate es fail-closed: un resolver que falla corta con ese MISMO 403, nunca con 500.
//
// Los permisos son PROPIOS ("integrations.*", no "content.*"): esta fila guarda el secreto de
// firma y la URL a la que se entregan todos los pedidos del tenant, y quien pueda escribirla
// puede repuntar el destino a un host suyo. Un token con "content.*" no alcanza ninguna de las
// cuatro; uno con "*.read" alcanza las dos lecturas y ninguna escritura.
//
// El tenant es SIEMPRE el del token (INV-8): no hay parámetro ni campo del cuerpo que lo cambie.
// Un `tenant_id` en el cuerpo del PUT se descarta sin ruido.
//
// G13 — leer la configuración:
//
//   - 200 {"configured","catalog_adapter","events_adapter","endpoint_url","enabled",
//     "secret_set","secret_fingerprint","created_at","updated_at"}, en ese orden;
//     "endpoint_url", "secret_fingerprint" y los dos instantes solo salen si tienen valor; los
//     instantes van en UTC, RFC 3339 con segundos;
//   - un tenant SIN fila responde 200 con EXACTAMENTE {"configured":false,
//     "catalog_adapter":"local","events_adapter":"local","enabled":false,"secret_set":false},
//     no un 404: «no tengo puente» es la respuesta que la pantalla necesita para dibujar el
//     formulario vacío. "configured" distingue esa ausencia de una fila puesta a mano en local;
//   - 🔴 el secreto NUNCA sale: solo "secret_set" y, si lo hay, "secret_fingerprint" (la huella
//     corta que da el puerto, D-042.7). La huella se pide SOLO si la fila dice que hay secreto;
//     si el puerto dice entonces que no lo encuentra, la huella se omite sin error;
//   - GetTenantIntegration o SecretFingerprint fallan ⇒ 500 {"error":"no se pudo leer la
//     integración"}, que NO repite el error del puerto.
//
// G14 — el estado de la cola de entregas:
//
//   - 200 {"pending","delivering","delivered","dead","oldest_pending_at"}: las cuatro claves
//     son los cuatro estados del CHECK de la migración 0046, con su nombre exacto, y salen
//     SIEMPRE, también a cero; "oldest_pending_at" (UTC, RFC 3339) solo sale si hay algo en
//     cola, y su ausencia significa justo eso. Sin payloads, sin last_error y sin ids;
//   - con todo a cero responde 200 {"pending":0,"delivering":0,"delivered":0,"dead":0}, no 404;
//   - CountOutbox falla ⇒ 500 {"error":"no se pudo leer el estado de la cola de entregas"}: un
//     contador que no se pudo leer NO puede parecerse a un cero, que se lee como «todo bien».
//
// G15 — guardar (upsert COMPLETO): el cuerpo es {"catalog_adapter","events_adapter",
// "endpoint_url","secret","enabled"}, la foto entera; lo que no venga toma el default (adaptador
// "local", sin endpoint, apagado), con UNA excepción: "secret" es write-only y su ausencia o su
// vacío significan «déjalo como está» (el GET no lo devuelve para poder reenviarlo). El PUT
// nunca borra el secreto: para dejar de firmar se borra la integración entera.
//
//   - 200 con la configuración RESULTANTE releída del puerto, en la forma de G13 (trae los
//     instantes de la fila y la huella del secreto que quedó, que puede ser el de antes);
//   - los adaptadores y el endpoint se recortan de espacios en los bordes ANTES de validar y de
//     guardar; el secreto NO se recorta;
//   - 413 {"error":"el cuerpo excede el tamaño máximo de 8192 bytes","max_bytes":8192} si el
//     cuerpo pasa de 8 KiB;
//   - 400 {"error":"el cuerpo debe ser un JSON {catalog_adapter, events_adapter, endpoint_url,
//     secret, enabled}"} si no es JSON o un campo no tiene su tipo; 400 {"error":"no se pudo
//     leer el cuerpo"} si la lectura falla;
//   - 422 {"error":"catalog.pull diferido: el adaptador de catálogo «http» todavía no está
//     implementado; usa «local»"} con catalog_adapter "http": el valor es del vocabulario y lo
//     que no existe es la implementación;
//   - 400 {"error":"catalog_adapter debe ser «local» o «webhook»"} y 400 {"error":
//     "events_adapter debe ser «local» o «webhook»"} con cualquier otro valor ("http" NO es un
//     adaptador de eventos); el vocabulario es exacto, en minúsculas;
//   - 400 {"error":"endpoint_url es demasiado larga"} con más de 2000 bytes, y 400
//     {"error":"endpoint_url debe ser una URL absoluta http(s)"} si no es una URL absoluta con
//     host y esquema exactamente "http" o "https". Vacía es admisible. No se exige https;
//   - 400 {"error":"el secreto de firma debe tener entre 24 y 256 caracteres"} si el secreto
//     viene y mide (en BYTES) menos de 24 o más de 256;
//   - 🔴 un puente webhook ENCENDIDO que no puede entregar no se guarda (events_adapter
//     "webhook" con enabled true): sin endpoint ⇒ 400 {"error":"un puente webhook encendido
//     necesita endpoint_url"}; sin secreto en el cuerpo NI guardado de antes ⇒ 400
//     {"error":"un puente webhook encendido necesita un secreto de firma"} (el secreto ya
//     guardado vale); y si esa consulta al puerto falla ⇒ 500 {"error":"no se pudo comprobar la
//     integración actual"}. El gate del módulo ya calla esas entregas (D-F6-11), pero esta
//     validación es la que AVISA al cliente, donde puede corregirlo. Con enabled false, o con
//     events_adapter "local", un puente a medio configurar SÍ se guarda;
//   - las validaciones van en ese orden, y la primera que falla decide la respuesta;
//   - una configuración rechazada NO se guarda: UpsertTenantIntegration no se llama;
//   - UpsertTenantIntegration falla ⇒ 500 {"error":"no se pudo guardar la integración"};
//     guardó pero la relectura falla o no encuentra la fila ⇒ 500 {"error":"integración
//     guardada, pero no se pudo releer"}.
//
// G16 — borrar: 204 sin cuerpo SIEMPRE que la operación se complete, también si no había fila
// (idempotente: el resultado pedido es un estado, local/local, no un objeto). Borra la fila
// ENTERA, y con ella el secreto. DeleteTenantIntegration falla ⇒ 500 {"error":"no se pudo
// borrar la integración"}.
//
// Ninguna de las cuatro lleva plazo de BD propio (como en la cara vieja).
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una
// identidad sin empresa que llegara a un handler recibiría 401 {"error":"autenticación
// requerida"}.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common).
func MountIntegrations(c *Cara, k Common, d IntegrationsDeps) {
	panic(pendiente.Implementar("apipublica.MountIntegrations"))
}
