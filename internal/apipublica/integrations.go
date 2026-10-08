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
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Vocabulario CERRADO de adaptadores, el mismo que acota el CHECK de la migración 0047. Se
// repite aquí porque la API tiene que rechazar ANTES de llegar a la BD: dejar que el CHECK sea
// quien valide convertiría un error del cliente en un 500.
const (
	integrationAdapterLocal   = "local"
	integrationAdapterHTTP    = "http"
	integrationAdapterWebhook = "webhook"
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
	// Sin store o sin resolver de features las rutas NO se montan: mejor un 404 de ruta
	// inexistente que una configuración que responde 500 a medio camino o, peor, que se guarda
	// sin poder comprobar el plan.
	if d.Integrations == nil || d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountIntegrations")

	// RequireFeature va SIEMPRE después de Authenticate y RequirePermission —antes no habría
	// identidad de la que sacar el tenant y el gate cortaría fail-closed a todo el mundo—.
	crmBridge := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCRMBridge)

	// SCOPES PROPIOS (integrations.read / integrations.write), y aquí SÍ se justifican en vez
	// de reusar content.* como hicieron las variables de empresa. La diferencia no es de nombre:
	// esta fila guarda el SECRETO de firma del puente y la URL a la que se entregan todos los
	// pedidos del tenant. Quien pueda escribirla puede repuntar el destino a un host propio y
	// quedarse con el flujo entero. Ese poder no puede venir incluido en «puede editar el
	// catálogo». El reparto NO necesita migración de grants: tenant_admin ('*') hace las tres;
	// viewer ('*.read') solo lee; operator no alcanza ninguna.
	c.Handle("GET /api/v1/integrations", protectRead(k, "integrations.read",
		crmBridge(getIntegrationHandler(d.Integrations))))
	// El estado de la cola cuelga de la MISMA ruta y va con las mismas dos guardias: es la otra
	// mitad de la pantalla del puente (la configuración dice a dónde se entrega; esto, si está
	// llegando). Lectura pura, así que protectRead sin auditar — mirar un contador no cambia
	// nada.
	c.Handle("GET /api/v1/integrations/outbox", protectRead(k, "integrations.read",
		crmBridge(getOutboxHandler(d.Integrations))))
	c.Handle("PUT /api/v1/integrations", protect(k, "integrations.write", "integration",
		crmBridge(putIntegrationHandler(d.Integrations))))
	c.Handle("DELETE /api/v1/integrations", protect(k, "integrations.write", "integration",
		crmBridge(deleteIntegrationHandler(d.Integrations))))
}

// integrationDTO es el contrato de GET y de la respuesta del PUT (la MISMA forma en los dos: la
// pantalla que lo pinta no tiene por qué saber cuál acaba de llamar).
//
// `configured` distingue las dos maneras de estar en local/local: no tener fila (el default de
// la plataforma) y tenerla puesta a mano en local. Sin ese booleano, un DELETE seguido de un GET
// sería indistinguible de un tenant que nunca configuró nada, y la pantalla no sabría si ofrecer
// «borrar».
//
// El secreto sale en DOS campos y en ninguno va el valor: `secret_set` dice si hay,
// `secret_fingerprint` permite compararlo con el que el puente tiene configurado (D-042.7 /
// REQ-13).
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

// integrationDefault es lo que responde el GET de un tenant SIN fila: local/local apagado. No
// es una invención de la API — es literalmente lo que dice la migración 0047 («sin fila =
// local/local»), dicho en JSON.
func integrationDefault() integrationDTO {
	return integrationDTO{
		Configured:     false,
		CatalogAdapter: integrationAdapterLocal,
		EventsAdapter:  integrationAdapterLocal,
	}
}

// toIntegrationDTO arma la respuesta a partir de la fila y de la huella. La fila ya viene SIN
// el secreto (integrations.TenantIntegration solo trae HasSecret). formatInstant da "" para el
// instante cero, que con omitempty no sale.
func toIntegrationDTO(ti integrations.TenantIntegration, fingerprint string) integrationDTO {
	return integrationDTO{
		Configured:        true,
		CatalogAdapter:    ti.CatalogAdapter,
		EventsAdapter:     ti.EventsAdapter,
		EndpointURL:       ti.EndpointURL,
		Enabled:           ti.Enabled,
		SecretSet:         ti.HasSecret,
		SecretFingerprint: fingerprint,
		CreatedAt:         formatInstant(ti.CreatedAt),
		UpdatedAt:         formatInstant(ti.UpdatedAt),
	}
}

// integrationRead lee la configuración del tenant y su huella de una vez. found false ⇒ el
// tenant no tiene fila (default local/local).
//
// La huella se pide SOLO si la fila dice que hay secreto: así el descifrado no se intenta cuando
// se sabe que no hay nada que descifrar.
func integrationRead(ctx context.Context, is IntegrationsStore, tenantID string) (integrationDTO, bool, error) {
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

// getIntegrationHandler sirve G13: la configuración del puente del tenant del token (INV-8).
//
// 200 SIEMPRE que se pueda leer, también sin fila: «no tengo puente» es una respuesta —y es la
// que la pantalla necesita para dibujar el formulario vacío—, no un fallo.
//
// La rama vieja «store nil ⇒ 500 store de integraciones no configurado» no se porta en ninguno
// de los cuatro handlers: las rutas solo se montan con un almacén no nil (MountIntegrations),
// así que era inalcanzable.
func getIntegrationHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		dto, found, err := integrationRead(r.Context(), is, id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo leer la integración")
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, integrationDefault())
			return
		}
		writeJSON(w, http.StatusOK, dto)
	})
}

// putIntegrationHandler sirve G15: upsert COMPLETO de la configuración del tenant del token.
//
// El cuerpo es la foto entera —lo que no venga toma el default de la tabla (local/local,
// apagado)—, con UNA excepción declarada: `secret`, que es write-only y cuyo silencio significa
// «déjalo como está». Sin esa excepción sería imposible cambiar el endpoint sin reenviar el
// secreto, porque el GET no lo devuelve para poder reenviarlo.
func putIntegrationHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
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
		// Se RELEE en vez de devolver lo que se acaba de mandar: así la respuesta trae los
		// timestamps de la fila y la huella del secreto que quedó guardado (que puede ser el de
		// antes, si el PUT no traía uno).
		dto, found, err := integrationRead(r.Context(), is, id.TenantID)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "integración guardada, pero no se pudo releer")
			return
		}
		writeJSON(w, http.StatusOK, dto)
	})
}

// outboxCountsDTO es el contrato de GET /api/v1/integrations/outbox: cuántas entregas del
// tenant hay en cada estado del ciclo de vida, y desde cuándo espera la más vieja que sigue en
// cola.
//
// LAS CLAVES SON LOS CUATRO ESTADOS DEL CHECK de la migración 0046, con su nombre exacto.
// Traducirlos aquí a un vocabulario propio («en cola», «perdidas») sería inventar una segunda
// taxonomía que habría que mantener sincronizada con la tabla; quien traduce a lenguaje de
// negocio es la pantalla, que es donde vive el lector humano. Esto es una API.
//
// SIN payloads, sin last_error y sin ids. La pregunta que contesta este endpoint es «¿cómo va
// la cola?», no «¿qué hay dentro?». El payload de las entregadas se vacía al entregar
// (migración 0050) y el de las `dead` es lo único que queda del intento fallido: sacarlo por la
// puerta de los contadores devolvería por la ventana lo que la purga sacó por la puerta.
//
// `oldest_pending_at` va con omitempty porque su ausencia SIGNIFICA algo —no hay nada en cola—
// y un cero de time.Time serializado ("0001-01-01T00:00:00Z") lo diría peor: el cliente tendría
// que saber comparar contra esa fecha mágica en lugar de preguntar si el campo está.
type outboxCountsDTO struct {
	Pending         int64  `json:"pending"`
	Delivering      int64  `json:"delivering"`
	Delivered       int64  `json:"delivered"`
	Dead            int64  `json:"dead"`
	OldestPendingAt string `json:"oldest_pending_at,omitempty"`
}

// toOutboxCountsDTO pasa los contadores del dominio al cable.
func toOutboxCountsDTO(c integrations.OutboxCounts) outboxCountsDTO {
	return outboxCountsDTO{
		Pending:         c.Pending,
		Delivering:      c.Delivering,
		Delivered:       c.Delivered,
		Dead:            c.Dead,
		OldestPendingAt: formatInstant(c.OldestPendingAt),
	}
}

// getOutboxHandler sirve G14: el estado de la cola de entregas del tenant DEL TOKEN (INV-8).
//
// Existe porque sin él la consola no puede responder la única pregunta que un dueño se hace
// sobre su puente después de configurarlo: ¿está llegando lo que mando? La configuración se ve
// en el GET de al lado; que las entregas salgan, no se veía en ninguna parte.
//
// MISMAS DOS GUARDIAS que el CRUD (integrations.read + feature crm_bridge): es la misma sección
// del producto y el mismo reparto de poder. Un tenant sin puente no tiene cola de la que
// preguntar, y quien no puede leer la configuración tampoco tiene por qué saber cuántos pedidos
// salen de esta empresa —un contador de volumen es información de negocio.
//
// 200 SIEMPRE que se pueda contar, también con todo a cero: «no hay nada en cola» es una
// respuesta, y es justo la que la pantalla necesita para decir que va todo al día. Un 404
// obligaría a distinguir dos cosas que significan lo mismo (nunca encoló nada / ya se entregó
// todo).
func getOutboxHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		// INV-8: el tenant sale de la identidad del token. No se lee ningún parámetro de la
		// petición para decidir de quién se cuenta, y por eso no hay nada que validar ni que
		// rechazar aquí.
		counts, err := is.CountOutbox(r.Context(), id.TenantID)
		if err != nil {
			// El 500 importa: un contador que no se pudo leer NO puede parecerse a un cero,
			// porque un cero se lee como «todo bien».
			writeError(w, http.StatusInternalServerError, "no se pudo leer el estado de la cola de entregas")
			return
		}
		writeJSON(w, http.StatusOK, toOutboxCountsDTO(counts))
	})
}

// deleteIntegrationHandler sirve G16: borra la fila del tenant, que vuelve al default
// local/local (sin CRM, con la experiencia completa de wApp — migración 0047).
//
// 204 SIEMPRE que la operación se complete, también si no había fila: IDEMPOTENTE. A diferencia
// de tenant-content —donde el 404 dice «esa ref no es tuya o no existe»— aquí el recurso es
// único por tenant y el resultado pedido es un estado, no un objeto: tras el DELETE el tenant
// está en local/local, que es lo que se pidió. Un 404 obligaría a la pantalla a distinguir dos
// desenlaces que significan lo mismo.
//
// Borra la fila ENTERA, y con ella el secreto cifrado: es la única forma de retirar el secreto
// (el PUT nunca lo borra, ver integrationRequest).
func deleteIntegrationHandler(is IntegrationsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if err := is.DeleteTenantIntegration(r.Context(), id.TenantID); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo borrar la integración")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
