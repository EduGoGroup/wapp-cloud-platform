package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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
//   - 502 si la sesión está offline; 504 si expira el ack; 500 en otro fallo.
//
// Los tres errores de envío llevan el command_id cuando ya estaba asignado, y TODO
// desenlace deja traza en el log. Antes no dejaba ninguna: un envío que colgaba
// contra un Edge saturado se iba sin 504, sin log y sin cuerpo (2026-08-06).
func messagesHandler(sender MessageSender, sessions SessionLister, log sharedlogger.Logger) http.Handler {
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

		// Aislamiento por tenant (INV-8, R6): la sesión debe ser del tenant del token.
		belongs, err := sessionBelongsToTenant(r.Context(), sessions, id.TenantID, req.SessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo verificar la sesión")
			return
		}
		if !belongs {
			// 404 (no 403): no se revela si la sesión existe en OTRO tenant.
			writeError(w, http.StatusNotFound, "sesión no encontrada para el tenant")
			return
		}

		ack, err := sender.SendText(r.Context(), req.SessionID, req.To, req.Text)
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

// sessionBelongsToTenant indica si sessionID figura entre las sesiones (durables)
// del tenant. Reusa fleet.List (tenant-scoped): fleet_sessions guarda una fila por
// cada sesión que se ha conectado alguna vez (online u offline), de modo que una
// sesión de OTRO tenant nunca aparece. Nota: una sesión que jamás se conectó no
// tiene fila; el envío igualmente fallaría con 502 (offline) al no haber stream vivo.
func sessionBelongsToTenant(ctx context.Context, sessions SessionLister, tenantID, sessionID string) (bool, error) {
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
// 502, timeout/cancelación esperando el Ack -> 504, resto -> 500 (mismo criterio que
// httpapi/admin.go). Adjunta el command_id al cuerpo y LOGUEA el fallo.
//
// La diferencia entre el 502 y el 504 no es cosmética y conviene no borrarla al
// tocar esto: en el 502 el comando NO llegó a salir (no hay stream), mientras que
// en el 504 el comando YA viajó al Edge y el mensaje pudo haberse enviado — el 504
// dice «no sé si llegó», no «no llegó». Por eso el 504 es el que más necesita el
// command_id: es el único modo de resolver la duda después, contra el outbox del
// Edge o los acuses del Plan 013.
func writeSendError(w http.ResponseWriter, err error, log sharedlogger.Logger, sessionID string) {
	cmdID := commandIDFrom(err)
	code, msg := http.StatusInternalServerError, "no se pudo enviar el texto"
	switch {
	case errors.Is(err, session.ErrSessionOffline):
		code, msg = http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge"
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
	writeJSON(w, code, sendErrorResponse{Error: msg, CommandID: cmdID})
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
