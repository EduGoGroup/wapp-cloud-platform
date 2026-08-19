package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// sendMessageRequest es el cuerpo JSON de POST /api/v1/messages. El tenant_id NO
// viaja aquí (INV-8): sale de la Identity del token. session_id identifica la sesión
// del Edge por la que sale el mensaje; to es el destino (número/JID) y text el cuerpo.
type sendMessageRequest struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
	Text      string `json:"text"`
}

// sendMessageResponse refleja el Ack del Edge (mismo contrato que el envío admin).
type sendMessageResponse struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// sendErrorResponse es el error de un envío que YA tenía command_id asignado. El
// command_id es lo que convierte un 504 opaco en algo diagnosticable: es el hilo
// que correlaciona lo que la nube intentó con el outbox del Edge y con los acuses
// del Plan 013, y sin él la pregunta «¿le llegó al cliente?» no tiene respuesta.
// Se omite si el fallo ocurrió antes de asignarlo.
type sendErrorResponse struct {
	Error     string `json:"error"`
	CommandID string `json:"command_id,omitempty"`
}

// messagesHandler devuelve el handler de POST /api/v1/messages: envía un texto por
// una sesión del Edge. Toma el tenant de la Identity del token (INV-8) y, ANTES de
// empujar el comando, valida que la session_id pertenezca a ese tenant
// (sessionBelongsToTenant) — el guardia de aislamiento que /admin/messages/send (T4)
// no tenía. Respuestas:
//
//   - 200 con {acked_command_id, ok, error} cuando se recibe el Ack (incluso si
//     ok=false: el Edge recibió el comando pero su ejecución falló).
//   - 400 si el cuerpo JSON es inválido o falta algún campo.
//   - 401 si el request no llegó autenticado (sin Identity en el contexto).
//   - 404 si la sesión no pertenece al tenant del token (aislamiento, INV-8/R6).
//   - 502 si la sesión está offline; 504 si expira el ack, si el stream del Edge cae
//     mientras se espera, o si la guarda de tenant agota su plazo de BD (tres textos
//     distintos, mismo código); 500 en otro fallo.
//
// Los errores de envío llevan el command_id cuando ya estaba asignado, y TODO
// desenlace deja traza en el log. Antes no dejaba ninguna: un envío que colgaba
// contra un Edge saturado se iba sin 504, sin log y sin cuerpo (2026-08-06).
//
// ⚠️ Ese «TODO desenlace deja traza» fue FALSO desde que se escribió hasta el Plan 050 ·
// Ola 5 · T5.4: cinco salidas de este handler —el 401, los dos 400, el 404 y el 500 de
// la guarda— respondían mudas, porque el que loguea es writeSendError y ellas no pasan
// por ahí. Hoy la frase es verdad, pero NO la sostiene este handler: la sostiene el
// access-log de accesslog.go, que deja una línea por petición pase lo que pase. Si
// añades aquí un `return` nuevo, el rastro sigue existiendo sin que tengas que
// acordarte — que es justo lo que la versión anterior de esta frase daba por hecho y no
// era cierto.
//
// dbTimeout acota la guarda de tenant (Plan 050 · Ola 3 · T3.2): es el ÚNICO tramo
// de este handler que consulta a Postgres, y hasta esa tarea corría con el contexto
// pelado de la petición. <=0 cae al suelo de dbCtx.
//
// sendBudget es el PRESUPUESTO DE LA PETICIÓN (Plan 050 · Ola 5 · T5.4, REQ-050.19):
// el techo por encima de los relojes secuenciales de abajo. Sin él, la suma de los
// tres (1,5 + 10 + 8 = 19,5s) se pasaba del WriteTimeout del servidor y el cliente se
// quedaba con la conexión cerrada y sin cuerpo. Se DERIVA del WriteTimeout con
// SendBudgetFrom; <=0 ⇒ sin plazo. Ver sendCtx en publicapi.go.
func messagesHandler(sender MessageSender, sessions SessionLister, dbTimeout, sendBudget time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req sendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		if req.SessionID == "" || req.To == "" || req.Text == "" {
			writeError(w, http.StatusBadRequest, "session_id, to y text son requeridos")
			return
		}

		// El presupuesto nace AQUÍ, después de las validaciones baratas y antes del
		// primer tramo que puede colgarse. Cubre la guarda de tenant Y el envío: lo
		// que se pasaba del WriteTimeout era la SUMA de los dos, no cada uno.
		ctx, cancelar := sendCtx(r.Context(), sendBudget)
		defer cancelar()

		// Aislamiento por tenant (INV-8, R6): la sesión debe ser del tenant del token.
		// La consulta va ACOTADA (dbCtx, ver publicapi.go): sin plazo propio, una base
		// lenta cuelga al llamante ANTES de que arranque siquiera el reloj del Ack.
		belongs, err := sessionBelongsToTenant(ctx, sessions, id.TenantID, req.SessionID, dbTimeout)
		if err != nil {
			// El plazo vencido se distingue del resto (504, no 500): la guarda corre
			// ANTES de empujar nada, así que el mensaje NO salió y reintentar es seguro
			// —lo contrario del 504 del Ack, que avisa de justo lo opuesto—.
			if dbTimedOut504(w, log, err, msgGuardaVencida,
				"op", "messages.guarda_tenant", "tenant_id", id.TenantID, "session_id", req.SessionID) {
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudo verificar la sesión")
			return
		}
		if !belongs {
			// 404 (no 403): no se revela si la sesión existe en OTRO tenant.
			writeError(w, http.StatusNotFound, "sesión no encontrada para el tenant")
			return
		}

		ack, err := sender.SendText(ctx, req.SessionID, req.To, req.Text)
		if err != nil {
			writeSendError(w, err, log, req.SessionID)
			return
		}
		// CERO PII: ni el destino ni el texto salen al log; el command_id y el
		// session_id son opacos y son justo lo que se necesita para correlacionar.
		if log != nil {
			log.Info("mensaje enviado por la API pública",
				"command_id", ack.GetAckedCommandId(),
				"session_id", req.SessionID,
				"ok", ack.GetOk(),
			)
		}
		if werr := writeJSONErr(w, http.StatusOK, sendMessageResponse{
			AckedCommandID: ack.GetAckedCommandId(),
			OK:             ack.GetOk(),
			Error:          ack.GetError(),
		}); werr != nil && log != nil {
			// El envío SÍ ocurrió pero el cliente no llegó a leer la respuesta
			// (conexión cerrada, WriteTimeout vencido…). Sin esta línea el caso es
			// invisible desde el servidor y el operador solo ve un curl sin cuerpo.
			log.Error("no se pudo escribir la respuesta del envío",
				"command_id", ack.GetAckedCommandId(),
				"session_id", req.SessionID,
				"error", werr,
			)
		}
	})
}

// msgGuardaVencida es el cuerpo del 504 de la guarda de tenant, y dice lo contrario
// que msgStreamCaido a propósito: aquí la consulta ocurre ANTES de empujar nada al
// Edge, así que el mensaje NO salió y reintentar es la acción correcta —no puede
// duplicarle nada al cliente—. Confundir los dos 504 en un mismo texto le quitaría
// al llamante justo la información que le permite decidir.
const msgGuardaVencida = "la verificación de la sesión no respondió a tiempo: el mensaje NO se envió, " +
	"reintenta"

// sessionBelongsToTenant indica si sessionID figura entre las sesiones (durables)
// del tenant. Reusa fleet.List (tenant-scoped): fleet_sessions guarda una fila por
// cada sesión que se ha conectado alguna vez (online u offline), de modo que una
// sesión de OTRO tenant nunca aparece. Nota: una sesión que jamás se conectó no
// tiene fila; el envío igualmente fallaría con 502 (offline) al no haber stream vivo.
//
// El PLAZO vive aquí dentro y no en los llamantes (Plan 050 · Ola 3 · T3.2/T3.3): la
// guarda se invoca desde DOS sitios —el envío y el preflight de diagnóstico— y
// dejarlo fuera obligaría a repetir el mismo WithTimeout en los dos, con la garantía
// de que un tercer llamante futuro se lo olvide. <=0 cae al suelo de dbCtx.
func sessionBelongsToTenant(ctx context.Context, sessions SessionLister, tenantID, sessionID string, dbTimeout time.Duration) (bool, error) {
	ctx, cancel := dbCtx(ctx, dbTimeout)
	defer cancel()
	list, err := sessions.List(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for _, s := range list {
		if s.SessionID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

// writeSendError traduce el error de SendText a un código HTTP: sesión offline ->
// 502, stream caído esperando el Ack -> 504, timeout/cancelación esperando el Ack ->
// 504, resto -> 500 (mismo criterio que httpapi/admin.go). Adjunta el command_id al
// cuerpo y LOGUEA el fallo.
//
// La diferencia entre el 502 y el 504 no es cosmética y conviene no borrarla al
// tocar esto: en el 502 el comando NO llegó a salir (no hay stream), mientras que
// en el 504 el comando YA viajó al Edge y el mensaje pudo haberse enviado — el 504
// dice «no sé si llegó», no «no llegó». Por eso el 504 es el que más necesita el
// command_id: es el único modo de resolver la duda después, contra el outbox del
// Edge o los acuses del Plan 013.
//
// Los DOS casos del 504 comparten código y se separan por el TEXTO (Plan 050 · Ola 2 ·
// T2.4). El enunciado de esa tarea pedía un 502 para el stream caído y NO se siguió
// (decisión de Jhoan): cuando el stream muere esperando el Ack, el Push ya tuvo éxito
// —si hubiera fallado, SendText habría devuelto ErrSessionOffline antes de llegar a
// esperar—, o sea que el comando viajó y estamos exactamente en el «no sé si llegó»
// del párrafo anterior. Devolver 502 ahí borraría la distinción que ese párrafo pide
// no borrar y, peor, le diría al llamante «reintenta tranquilo» justo cuando reintentar
// puede duplicarle un mensaje de WhatsApp a un cliente real.
func writeSendError(w http.ResponseWriter, err error, log sharedlogger.Logger, sessionID string) {
	cmdID := commandIDFrom(err)
	code, msg := http.StatusInternalServerError, "no se pudo enviar el texto"
	switch {
	case errors.Is(err, session.ErrSessionOffline):
		code, msg = http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge"
	case streamCaidoFrom(err):
		code, msg = http.StatusGatewayTimeout, msgStreamCaido
	// Los DOS casos del empuje van ANTES del deadline genérico de abajo: los dos
	// envuelven un error de contexto o se le parecen, y el de abajo los absorbería
	// con un texto que aquí es falso (no se llegó a esperar ningún ack).
	case errors.Is(err, session.ErrPushTimeout):
		code, msg = http.StatusGatewayTimeout, msgEdgeNoLee
	case errors.Is(err, session.ErrPushAbandonado):
		code, msg = http.StatusGatewayTimeout, msgPresupuestoAgotado
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code, msg = http.StatusGatewayTimeout, "timeout esperando el ack del Edge"
	}
	if log != nil {
		// CERO PII (ni destino ni texto), como el resto de los logs de envío.
		log.Error("envío por la API pública fallido",
			"status", code,
			"command_id", cmdID,
			"session_id", sessionID,
			"error", err,
		)
	}
	// writeJSONErr y no writeJSON (Plan 050 · Ola 5 · T5.4): el fallo de ESCRITURA de
	// un error era el último silencio que quedaba de este camino. Sin esta línea, con
	// el deadline de escritura ya vencido el log afirmaba «status 500» sobre una
	// respuesta que el cliente nunca recibió — una traza que miente por omisión es peor
	// de auditar que la ventana muda del incidente, porque parece que todo se registró.
	// El camino del 200 ya lo hacía así desde el principio.
	if werr := writeJSONErr(w, code, sendErrorResponse{Error: msg, CommandID: cmdID}); werr != nil && log != nil {
		log.Error("no se pudo escribir la respuesta de error del envío",
			"status", code,
			"command_id", cmdID,
			"session_id", sessionID,
			"error", werr,
		)
	}
}

// msgStreamCaido es el cuerpo del 504 «se cayó», distinguible a simple vista del 504
// «no contestó a tiempo» en un log. Está redactado a propósito SIN la palabra «no se
// pudo enviar»: eso afirmaría algo falso —el comando ya viajó al Edge y el mensaje pudo
// llegar a WhatsApp— y la única acción honesta que le queda al llamante es verificar
// antes de reenviar, nunca reintentar a ciegas.
const msgStreamCaido = "el stream del Edge se cerró antes del ack: el comando YA viajó, " +
	"así que no se sabe si el mensaje salió. Verifica el envío (por el command_id, contra el " +
	"outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas puede duplicárselo al cliente"

// msgEdgeNoLee es el cuerpo del 504 del empuje vencido: el Edge sigue conectado pero
// dejó de leer su stream, así que el comando no cupo dentro del plazo del Push
// (session.ErrPushTimeout). Hasta el Plan 050 · Ola 5 · T5.4 este caso NO estaba en el
// switch y caía al 500 genérico «no se pudo enviar el texto», que además de opaco era
// el desenlace del incidente del 2026-08-06.
//
// 🔴 POR QUÉ NO DICE «reintenta», aunque el comando no haya salido todavía. Esa era la
// redacción evidente y es FALSA: la goroutine del Send del Registry sobrevive a la
// salida por timeout (session.Push, «Enmienda 1, regla 1»), de modo que el comando
// puede viajar al Edge DESPUÉS de que el llamante haya recibido este error — medido en
// el e2e de T5.4, no supuesto. Invitar a reintentar aquí le duplicaría el WhatsApp a un
// cliente real, que es exactamente lo que msgStreamCaido lleva escrito no hacer.
//
// Es 504 y no 502 a propósito: el 502 de este repo significa «no salió» y le pertenece
// a ErrSessionOffline. Aquí el Edge SÍ está —solo que atascado— y el mensaje puede
// acabar saliendo; devolver 502 borraría además la diferencia operativa entre «el Edge
// no está» y «el Edge está y no lee», que son dos problemas distintos de resolver.
const msgEdgeNoLee = "el Edge dejó de leer su stream y no aceptó el comando dentro del plazo: " +
	"puede salir aún, así que no se sabe si el mensaje llegará. Verifica el envío (por el " +
	"command_id, contra el outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas " +
	"puede duplicárselo al cliente"

// msgPresupuestoAgotado es el cuerpo del 504 cuando se acaba el PRESUPUESTO DE LA
// PETICIÓN mientras se empujaba el comando (session.ErrPushAbandonado). Se distingue
// de msgEdgeNoLee en la primera frase a propósito: el diagnóstico es otro —allí el
// Edge se atascó, aquí fuimos nosotros los que nos quedamos sin tiempo— y llevan a
// revisar sitios distintos.
//
// La segunda mitad es la misma, y por el mismo motivo: rendirse NO cancela el envío en
// vuelo, así que el comando puede salir después. Ver SendBudgetFrom (publicapi.go).
const msgPresupuestoAgotado = "se agotó el plazo de la petición mientras se empujaba el comando " +
	"al Edge: puede salir aún, así que no se sabe si el mensaje llegará. Verifica el envío (por " +
	"el command_id, contra el outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas " +
	"puede duplicárselo al cliente"

// streamCaidoFrom indica si el error viene de un stream que murió esperando el ack,
// por duck-typing (`interface{ StreamCaido() bool }`) y sin importar el paquete del
// Gateway — la hermana exacta de commandIDFrom, y por el mismo motivo: el contrato es
// la interfaz anónima, no un tipo compartido, y ese desacople es el que mantiene al
// Gateway fuera de los imports de estos handlers. Un `errors.Is(err,
// gatewaygrpc.ErrStreamClosed)` obligaría a importarlo y lo rompería.
//
// Falso NO significa «el envío fue bien»: significa que, si falló, fue por otra cosa.
func streamCaidoFrom(err error) bool {
	var caido interface{ StreamCaido() bool }
	return errors.As(err, &caido) && caido.StreamCaido()
}

// commandIDFrom extrae el command_id de un error de envío por duck-typing, sin
// importar el paquete del Gateway (el mismo contrato `interface{ CommandID() string }`
// que ya usa el notificador de solicitudes del Plan 041 · T4.2). Devuelve "" si el
// fallo ocurrió antes de que hubiera command_id que reportar.
func commandIDFrom(err error) string {
	var withID interface{ CommandID() string }
	if errors.As(err, &withID) {
		return withID.CommandID()
	}
	return ""
}

// writeError responde un error como JSON tipado {error} (formato del listener
// público, coherente con el middleware de auth de T3).
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody(msg))
}
