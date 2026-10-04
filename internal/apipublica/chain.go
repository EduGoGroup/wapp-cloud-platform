// Porta internal/publicapi/publicapi.go @ 9a77307 (protect y protectRead, líneas 1126-1144) e
// internal/publicapi/accesslog.go @ 9a77307 (accessLog, anotarTenant, respuestaObservada).
//
// chain.go — LA CADENA DE CADA RUTA DE LA CARA NUEVA. En la spec FX era `cadena.go` y `Comun`
// (05 E-11: nombres en inglés; la correspondencia se anota en tareas.md de FX).
//
// En el rojo solo existía Common: los montadores de la cadena (protect, protectRead, accessLog,
// annotateTenant —anotarTenant en la cara vieja— y observedResponse —respuestaObservada—) son NO
// exportados y nacieron con el verde (05 E-4, P6). Sus promesas viven en el comentario de
// Common, y las prueba chain_test.go a través de los Mount* de las áreas, que son quienes los
// usan.
//
// El ACCESS-LOG (Plan 050 · Ola 5 · T5.4, cierra REQ-050.19).
//
// POR QUÉ EXISTE. Hasta esa tarea, cinco desenlaces de POST /api/v1/messages —401, 400 por JSON
// inválido, 400 por campos faltantes, 404 de tenant ajeno y 500 de la guarda— respondían sin
// emitir una sola línea, y ninguna capa de arriba los cubría: ni protect, ni AuditMiddleware, ni
// PublicRateLimit, ni InstrumentHTTP dejan rastro por petición. Lo hace este middleware y no cada
// handler: una línea por petición, en un solo sitio, es lo que hace que «toda petición deja
// rastro» no pueda volver a caducar al añadir un `return` nuevo.
//
// POR QUÉ AQUÍ Y NO EN EL ARRANQUE. El rate limit y la métrica envuelven el compuesto entero desde
// el arranque, que sería el sitio natural, pero los tests de la cara montan sus rutas con los
// Mount* y NO montan esa capa: un access-log puesto allí sería código sin cobertura desde donde
// se prueba el comportamiento. Aquí entra en la misma cadena que los handlers y se prueba con
// ellos.
//
// 🔴 QUÉ NO LLEVA. Ni el destino ni el texto del mensaje: CERO PII, igual que el resto de los
// logs de esta capa. Solo r.URL.Path (nunca la query, que sí podría traer datos) y el tenant de
// la Identity, que es opaco.
//
// 🔴 Y NO LLEVA command_id, a propósito: los desenlaces que este middleware cubre ocurren ANTES
// de que exista un command_id que reportar. El command_id sigue viniendo de las líneas de envío,
// que son las que lo tienen; esta línea aporta la otra mitad —que la petición existió y en qué
// acabó— y las dos se correlacionan por el tenant y el instante.

package apipublica

import (
	"net/http"
	"time"

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

// mustHaveMW es la guarda de cableado de los Mount*: sin MW no hay cadena que armar, y montar
// rutas que respondieran con un nil-pointer en la primera petición escondería el fallo hasta el
// tráfico. Por eso hace panic AL MONTAR, nombrando el Mount* para que el arranque diga dónde.
func mustHaveMW(k Common, mount string) {
	if k.MW == nil {
		panic("apipublica." + mount + ": Common.MW es nil; la cadena de sus rutas necesita el middleware de autenticación")
	}
}

// protect compone la cadena de una ESCRITURA pública: Authenticate → identidad del
// token; RequirePermission(perm) → scope glob; AuditMiddleware → bitácora sin PII
// (action=perm, resource). Espeja adminHandler de cmd/server (T4) para /api/v1.
//
// El access-log va por FUERA de Authenticate para ver también el 401, y annotateTenant por
// DENTRO, que es donde ya existe la Identity.
func protect(k Common, perm, resource string, h http.Handler) http.Handler {
	h = httpapi.AuditMiddleware(k.Auditor, perm, resource, k.Log)(h)
	h = k.MW.RequirePermission(perm)(h)
	h = annotateTenant(h)
	return accessLog(k.Log, k.MW.Authenticate(h))
}

// protectRead compone la cadena de una LECTURA pública: Authenticate →
// RequirePermission(perm). No audita (lectura sin efecto).
//
// Recibe el logger (Plan 050 · Ola 5 · T5.4) y no por simetría cosmética: sin él, las rutas de
// lectura de la API pública quedarían fuera del access-log y el «toda petición deja rastro» de
// REQ-050.19 sería verdad solo para las escrituras.
func protectRead(k Common, perm string, h http.Handler) http.Handler {
	return accessLog(k.Log, k.MW.Authenticate(k.MW.RequirePermission(perm)(annotateTenant(h))))
}

// observedResponse (respuestaObservada en la cara vieja) envuelve el ResponseWriter para saber
// en qué acabó la petición: con qué código y —lo que el incidente del 2026-08-06 hizo importar—
// si la respuesta llegó a escribirse. Un Write que falla con el deadline de escritura ya vencido
// es el caso en el que el servidor cree haber respondido y el cliente no recibió nada.
type observedResponse struct {
	http.ResponseWriter
	status int
	// writeErr (errEscribir en la cara vieja) es el PRIMER fallo de Write.
	writeErr error
	// tenantID lo rellena annotateTenant una vez que Authenticate ha puesto la Identity
	// en el contexto. No se lee del ctx al final porque el ctx enriquecido solo existe
	// DENTRO de la cadena, y este middleware envuelve por fuera para poder ver también
	// el 401 —que es justo uno de los desenlaces que antes no dejaban rastro—.
	tenantID string
}

// annotateTenant (anotarTenant en la cara vieja) copia el tenant de la Identity al observador
// del access-log. Se monta DENTRO de Authenticate (que es quien pone la Identity) y es un no-op
// cuando el ResponseWriter no es el nuestro: un logger nil deja el handler sin envolver.
func annotateTenant(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if obs, ok := w.(*observedResponse); ok {
			if id, found := httpapi.IdentityFromContext(r.Context()); found {
				obs.tenantID = id.TenantID
			}
		}
		h.ServeHTTP(w, r)
	})
}

// WriteHeader apunta el PRIMER código (el que recibe el cliente) y lo deja pasar.
func (o *observedResponse) WriteHeader(code int) {
	if o.status == 0 {
		o.status = code
	}
	o.ResponseWriter.WriteHeader(code)
}

// Write apunta el 200 implícito y el primer fallo de escritura, y devuelve lo del Write real.
func (o *observedResponse) Write(b []byte) (int, error) {
	if o.status == 0 {
		// Un Write sin WriteHeader previo es un 200 implícito (net/http).
		o.status = http.StatusOK
	}
	n, err := o.ResponseWriter.Write(b)
	if err != nil && o.writeErr == nil {
		o.writeErr = err
	}
	return n, err
}

// accessLog envuelve un handler para que TODA petición deje una línea, gane o pierda.
// Un logger nil lo deja pasar sin envolver: no hay dónde escribir y envolver por nada
// solo añadiría una indirección en el camino caliente.
func accessLog(log sharedlogger.Logger, h http.Handler) http.Handler {
	if log == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		obs := &observedResponse{ResponseWriter: w}

		h.ServeHTTP(obs, r)

		// Un handler que no escribe nada deja al servidor emitir un 200 vacío.
		status := obs.status
		if status == 0 {
			status = http.StatusOK
		}
		fields := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if obs.tenantID != "" {
			fields = append(fields, "tenant_id", obs.tenantID)
		}
		if obs.writeErr != nil {
			// El desenlace que el servidor cree haber dado NO es el que el cliente
			// recibió. Se registra como error porque es un fallo de entrega, no un
			// detalle: es el síntoma del incidente visto desde el servidor.
			fields = append(fields, "write_error", obs.writeErr)
			log.Error("petición pública sin entregar: la respuesta no se pudo escribir", fields...)
			return
		}
		log.Info("petición pública", fields...)
	})
}
