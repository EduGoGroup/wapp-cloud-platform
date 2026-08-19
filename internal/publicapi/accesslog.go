package publicapi

import (
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// El ACCESS-LOG de la API pública (Plan 050 · Ola 5 · T5.4, cierra REQ-050.19).
//
// POR QUÉ EXISTE. Hasta esta tarea, cinco desenlaces de POST /api/v1/messages —401,
// 400 por JSON inválido, 400 por campos faltantes, 404 de tenant ajeno y 500 de la
// guarda— respondían sin emitir una sola línea, y ninguna capa de arriba los cubría:
// ni protect, ni AuditMiddleware, ni PublicRateLimit, ni InstrumentHTTP dejan rastro
// por petición. El docstring de messagesHandler afirmaba «TODO desenlace deja traza en
// el log» y era falso. Ahora lo es de verdad, pero lo hace este middleware y no el
// handler: una línea por petición, en un solo sitio, es lo que hace que la afirmación
// no pueda volver a caducar al añadir un `return` nuevo.
//
// POR QUÉ AQUÍ Y NO EN bootstrap. PublicRateLimit e InstrumentHTTP envuelven el mux
// entero desde internal/bootstrap/http.go, que sería el sitio natural — pero los tests
// de este paquete montan el mux con Register y NO montan esa capa, así que un
// access-log puesto allí sería código sin cobertura desde donde se prueba el
// comportamiento. Aquí entra en la misma cadena que los handlers y se prueba con ellos.
//
// 🔴 QUÉ NO LLEVA. Ni el destino ni el texto del mensaje: CERO PII, igual que el resto
// de los logs de esta capa. Solo se registra r.URL.Path (nunca la query, que sí podría
// traer datos) y el tenant de la Identity, que es opaco.
//
// 🔴 Y NO LLEVA command_id, a propósito. El criterio de T5.4 pide «toda petición deja
// rastro, con command_id», pero exigirlo aquí es imposible por construcción: los cinco
// desenlaces que este middleware viene a cubrir ocurren ANTES de que exista un
// command_id que reportar. El command_id sigue viniendo de las líneas de envío
// (messagesHandler y writeSendError), que son las que lo tienen; esta línea aporta la
// otra mitad —que la petición existió y en qué acabó— y las dos se correlacionan por
// el tenant y el instante.

// respuestaObservada envuelve el ResponseWriter para saber en qué acabó la petición:
// con qué código y —lo que el incidente del 2026-08-06 hizo importar— si la respuesta
// llegó a escribirse. Un Write que falla con el deadline de escritura ya vencido es el
// caso en el que el servidor cree haber respondido y el cliente no recibió nada.
type respuestaObservada struct {
	http.ResponseWriter
	status      int
	errEscribir error
	// tenantID lo rellena anotarTenant una vez que Authenticate ha puesto la Identity
	// en el contexto. No se lee del ctx al final porque el ctx enriquecido solo existe
	// DENTRO de la cadena, y este middleware envuelve por fuera para poder ver también
	// el 401 —que es justo uno de los desenlaces que antes no dejaban rastro—.
	tenantID string
}

// anotarTenant copia el tenant de la Identity al observador del access-log. Se monta
// DENTRO de Authenticate (que es quien pone la Identity) y es un no-op cuando el
// ResponseWriter no es el nuestro: un logger nil deja el handler sin envolver.
func anotarTenant(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if obs, ok := w.(*respuestaObservada); ok {
			if id, hay := httpapi.IdentityFromContext(r.Context()); hay {
				obs.tenantID = id.TenantID
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (r *respuestaObservada) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *respuestaObservada) Write(b []byte) (int, error) {
	if r.status == 0 {
		// Un Write sin WriteHeader previo es un 200 implícito (net/http).
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	if err != nil && r.errEscribir == nil {
		r.errEscribir = err
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
		inicio := time.Now()
		obs := &respuestaObservada{ResponseWriter: w}

		h.ServeHTTP(obs, r)

		// Un handler que no escribe nada deja al servidor emitir un 200 vacío.
		status := obs.status
		if status == 0 {
			status = http.StatusOK
		}
		campos := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(inicio).Milliseconds(),
		}
		if obs.tenantID != "" {
			campos = append(campos, "tenant_id", obs.tenantID)
		}
		if obs.errEscribir != nil {
			// El desenlace que el servidor cree haber dado NO es el que el cliente
			// recibió. Se registra como error porque es un fallo de entrega, no un
			// detalle: es el síntoma del incidente visto desde el servidor.
			campos = append(campos, "write_error", obs.errEscribir)
			log.Error("petición pública sin entregar: la respuesta no se pudo escribir", campos...)
			return
		}
		log.Info("petición pública", campos...)
	})
}
