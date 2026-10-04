// Porta internal/publicapi/publicapi.go @ 9a77307 (protect y protectRead, líneas 1126-1144) e
// internal/publicapi/accesslog.go @ 9a77307 (accessLog, anotarTenant, respuestaObservada).
//
// chain.go — LA CADENA DE CADA RUTA DE LA CARA NUEVA. En la spec FX era `cadena.go` y `Comun`
// (05 E-11: nombres en inglés; la correspondencia se anota en tareas.md de FX).
//
// En el rojo solo existe Common: los montadores de la cadena (protect, protectRead, accessLog,
// annotateTenant y el ResponseWriter observado) son NO exportados y nacen con el verde (05 E-4,
// P6). Sus promesas viven aquí, en el comentario de Common, y las prueba chain_test.go a través
// de los Mount* de las áreas, que son quienes las usan.

package apipublica

import (
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Common es lo que TODAS las áreas de la cara comparten, y lo construye el arranque UNA vez para
// las dos caras (arquitectura.md §3): el middleware de autenticación y RBAC, el auditor y el
// logger. Cada Mount<Área>(c, k, d) lo recibe como k.
//
// Las cadenas que un Mount* arma con k son exactamente las de la cara vieja (mapa §1, «Cadena»):
//
//   - W (escritura), de fuera a dentro: access-log → MW.Authenticate → anotación del tenant en
//     el access-log → MW.RequirePermission(permiso) → httpapi.AuditMiddleware(Auditor, permiso,
//     recurso, Log) → handler. Consecuencias observables:
//     sin token (o con uno inválido) ⇒ 401 {"error":"autenticación requerida"} y CERO registros
//     de auditoría; con token sin el permiso (o sin empresa, D-056.12: un token sin tenant no
//     trae grants) ⇒ 403 {"error":"permiso denegado"} y CERO registros; con el permiso ⇒ el
//     handler responde y queda EXACTAMENTE UN registro con TenantID = el tenant del token
//     (INV-8), Actor = su subject, Action = el permiso, Resource = el recurso de la ruta,
//     Result "success" (estado < 400) o "failure" (≥ 400) y Meta {"status": estado}.
//   - R (lectura): la misma cadena SIN AuditMiddleware: los mismos 401 y 403, y CERO registros
//     también en el camino feliz (una lectura no tiene efecto que registrar).
//   - A (Authenticate a secas) y — (pública): solo las de autenticación (auth.go), que dicen
//     la suya; NO llevan access-log, como en el arranque viejo (internal/bootstrap/arranque/
//     http.go:81-171, que las registraba fuera de publicapi).
//
// El access-log de W y R va POR FUERA de Authenticate para ver también el 401 (REQ-050.19:
// «toda petición deja rastro»). Deja UNA línea por petición en Log:
//
//   - nivel Info, mensaje "petición pública", con los campos "method", "path", "status" y
//     "duration_ms"; "path" es r.URL.Path y NUNCA la query (que podría traer datos: CERO PII);
//     "status" es el código que recibió el cliente;
//   - "tenant_id" solo si Authenticate dejó una identidad con empresa (el 401 no lo lleva);
//   - si el Write de la respuesta falla (el incidente del 2026-08-06: el servidor cree haber
//     respondido y el cliente no recibió nada), la línea pasa a nivel Error con el mensaje
//     "petición pública sin entregar: la respuesta no se pudo escribir" y el campo
//     "write_error" con el error, en vez de la de Info.
//
// Campos:
//
//   - MW es obligatorio: un Mount* que registra al menos una ruta con cadena hace panic AL
//     MONTAR si MW es nil (fallo de cableado del arranque, no de una petición), con un mensaje
//     propio que nombra el Mount*;
//   - Auditor puede ser nil: las W se sirven igual y no dejan registro (lo que hace
//     httpapi.AuditMiddleware con un recorder nil);
//   - Log puede ser nil: no hay access-log (la cadena NO se envuelve) y las respuestas son las
//     mismas que con logger.
type Common struct {
	// MW autentica el Context Token (Authenticate) y evalúa los grants (RequirePermission).
	MW *httpapi.Middleware
	// Auditor graba la bitácora de las escrituras (el mismo que sirve GET /api/v1/audit).
	Auditor httpapi.AuditRecorder
	// Log recibe el access-log de las rutas W y R.
	Log sharedlogger.Logger
}
