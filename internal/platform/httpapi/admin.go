package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// LeaseRevoker dispara el kill-switch anti-clon (ADR-0007) de un Edge concreto.
// Lo satisface *gatewaygrpc.Server con su método RevokeLease.
type LeaseRevoker interface {
	RevokeLease(ctx context.Context, tenantID, edgeID string) error
}

// MessageSender empuja un SendText hacia una sesión viva del Edge y espera su
// Ack. Lo satisface *gatewaygrpc.Server con su método SendText.
type MessageSender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// revokeLeaseRequest es el cuerpo JSON del endpoint de revocación. El tenant_id
// NO viaja en el cuerpo (INV-8, Plan 018 · T4): sale de la Identity del token.
type revokeLeaseRequest struct {
	EdgeID string `json:"edge_id"`
}

// RevokeLeaseHandler devuelve el handler del endpoint admin de revocación de
// leases (kill-switch, ADR-0007). Acepta POST con cuerpo JSON {edge_id} y, al
// éxito, responde 204 No Content. La semántica del kill-switch (INV-2) NO cambia:
// solo gana autenticación.
//
// SEGURIDAD (Plan 018 · T4): el endpoint se monta DETRÁS de Authenticate →
// RequirePermission("leases.revoke"); el tenant_id sale de IdentityFromContext
// (INV-8), NUNCA del cuerpo, de modo que un operador solo puede revocar Edges de
// su propio tenant.
func RevokeLeaseHandler(revoker LeaseRevoker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		id, ok := IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeAuthError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req revokeLeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.EdgeID == "" {
			http.Error(w, "edge_id es requerido", http.StatusBadRequest)
			return
		}

		if err := revoker.RevokeLease(r.Context(), id.TenantID, req.EdgeID); err != nil {
			http.Error(w, "no se pudo revocar el lease", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// sendMessageRequest es el cuerpo JSON del endpoint de envío de texto.
type sendMessageRequest struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
	Text      string `json:"text"`
}

// sendMessageResponse es la respuesta JSON del endpoint de envío: refleja el Ack
// recibido del Edge (acked_command_id, ok) y, si lo hubo, el error reportado por
// el Edge al ejecutar el comando (Ack.ok=false).
type sendMessageResponse struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// SendMessageHandler devuelve el handler del endpoint admin de envío de texto.
// Acepta POST con cuerpo JSON {session_id, to, text}, empuja el SendText a la
// sesión viva del Edge y espera su Ack. Respuestas:
//
//   - 200 con {acked_command_id, ok, error} cuando se recibe un Ack (incluso si
//     ok=false: el Edge recibió el comando pero su ejecución falló).
//   - 502 si la sesión está offline (no hay stream vivo para empujar el comando).
//   - 504 si se agota el contexto/timeout esperando el Ack del Edge.
//   - 500 ante cualquier otro error del envío.
//   - 400 si el cuerpo JSON es inválido o falta algún campo; 405 si el método no
//     es POST.
//
// SEGURIDAD (Plan 018 · T4): el endpoint se monta DETRÁS de Authenticate →
// RequirePermission("messages.send"). El envío se dirige por session_id (no lleva
// tenant en el cuerpo); la autorización la impone el middleware.
func SendMessageHandler(sender MessageSender) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		var req sendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.SessionID == "" || req.To == "" || req.Text == "" {
			http.Error(w, "session_id, to y text son requeridos", http.StatusBadRequest)
			return
		}

		ack, err := sender.SendText(r.Context(), req.SessionID, req.To, req.Text)
		if err != nil {
			writeSendError(w, err)
			return
		}

		body, err := json.Marshal(sendMessageResponse{
			AckedCommandID: ack.GetAckedCommandId(),
			OK:             ack.GetOk(),
			Error:          ack.GetError(),
		})
		if err != nil {
			http.Error(w, "codificando respuesta de envío", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write(body); werr != nil {
			return
		}
	})
}

// writeSendError traduce el error de SendText a un código HTTP claro: sesión
// offline -> 502, timeout/cancelación esperando el Ack -> 504, resto -> 500.
func writeSendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrSessionOffline):
		http.Error(w, "sesión offline: no hay stream vivo para el Edge", http.StatusBadGateway)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		http.Error(w, "timeout esperando el ack del Edge", http.StatusGatewayTimeout)
	default:
		http.Error(w, "no se pudo enviar el texto", http.StatusInternalServerError)
	}
}
