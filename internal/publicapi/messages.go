package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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
func messagesHandler(sender MessageSender, sessions SessionLister) http.Handler {
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
			writeSendError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sendMessageResponse{
			AckedCommandID: ack.GetAckedCommandId(),
			OK:             ack.GetOk(),
			Error:          ack.GetError(),
		})
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
// httpapi/admin.go).
func writeSendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrSessionOffline):
		writeError(w, http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusGatewayTimeout, "timeout esperando el ack del Edge")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo enviar el texto")
	}
}

// writeError responde un error como JSON tipado {error} (formato del listener
// público, coherente con el middleware de auth de T3).
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
