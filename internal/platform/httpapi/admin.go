package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

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

// TenantRevoker dispara el kill-switch COMERCIAL (D-055.2, Plan 055) de un
// tenant completo: marca el tenant revocado y empuja el LeaseUpdate(Revoked)
// a TODAS las instalaciones conocidas y vivas de ese tenant. Lo satisface
// *gatewaygrpc.Server con su método RevokeTenant.
type TenantRevoker interface {
	RevokeTenant(ctx context.Context, tenantID string) error
}

// TenantRestorer reactiva un tenant previamente revocado (revoked_at = NULL,
// Plan 055 · T3.3). Lo satisface *gatewaygrpc.Server con su método
// RestoreTenant (que delega en el lease.Manager homónimo).
type TenantRestorer interface {
	RestoreTenant(ctx context.Context, tenantID string) error
}

// tenantTargetRequest es el cuerpo JSON de los endpoints del PLANO DE
// PLATAFORMA (revoke/restore de tenant). Aquí el tenant_id SÍ viaja en el
// cuerpo, y esa es toda la diferencia con revokeLeaseRequest.
//
// EXCEPCIÓN ADMINISTRATIVA A INV-8 (ADR-0039). La regla del Plan 018 · T4 --
// "el tenant sale del token, nunca del cuerpo" -- confunde dos sujetos que en
// el kill-switch COMERCIAL son distintos: quién EJECUTA el corte y a quién se
// le corta. Mientras coincidieron, el endpoint solo sabía revocar al llamante:
// wApp no podía cortar a un cliente moroso (REQ-055.7) y el cliente cortado se
// desrevocaba solo. La separación es: el TOKEN sigue identificando al ejecutor
// -- y solo el tenant de plataforma lo es -- y el CUERPO nombra al objetivo.
// INV-8 sigue rigiendo intacta en todo lo demás (leases, flujos, API pública):
// esto es una excepción de UN plano, no una puerta abierta.
type tenantTargetRequest struct {
	TenantID string `json:"tenant_id"`
}

// RevokeTenantHandler devuelve el handler del endpoint admin de revocación de
// TENANT completo (kill-switch comercial, D-055.2). Acepta POST con cuerpo JSON
// {tenant_id} -- el tenant VÍCTIMA -- y, al éxito, responde 204 No Content.
//
// SEGURIDAD (ADR-0039), dos cerrojos independientes:
//
//   - El de ARRIBA, en el registro de la ruta: el permiso 'tenants.revoke.any',
//     que la migración 0059 concede SOLO al rol platform_admin y niega
//     explícitamente al '*' de tenant_admin (deny '*.any').
//   - El de AQUÍ, que no confía en el anterior: el tenant del token tiene que
//     ser platformTenantID. Un token de cliente con el permiso concedido por
//     error -- un rol custom, un grant manual, un IAM mal sembrado -- se queda
//     igualmente en 403. El agujero que este handler cierra era demasiado caro
//     para dejarlo colgando de una sola comprobación.
//
// El orden importa: 401 si no hay Identity, 403 si el llamante no es el plano de
// plataforma, y solo entonces se mira el cuerpo. Un tenant ajeno no aprende nada
// del formato del cuerpo ni de si el tenant objetivo existe.
func RevokeTenantHandler(revoker TenantRevoker, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		target, ok := PlatformTarget(w, r, platformTenantID)
		if !ok {
			return
		}

		if err := revoker.RevokeTenant(r.Context(), target); err != nil {
			http.Error(w, "no se pudo revocar el tenant", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// RestoreTenantHandler devuelve el handler del reverso de RevokeTenantHandler
// (Plan 055 · T3.3): reactiva el tenant NOMBRADO EN EL CUERPO (revoked_at =
// NULL). Mismo patrón y los MISMOS dos cerrojos que su hermano, y por la misma
// razón: si el corte lo decide la plataforma pero la reactivación la pudiera
// pedir el propio cortado, el kill-switch no cortaría nada.
func RestoreTenantHandler(restorer TenantRestorer, platformTenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		target, ok := PlatformTarget(w, r, platformTenantID)
		if !ok {
			return
		}

		if err := restorer.RestoreTenant(r.Context(), target); err != nil {
			http.Error(w, "no se pudo restaurar el tenant", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// EnforcePlatformCaller valida que el llamante esté autenticado y que su TenantID
// coincida con el platformTenantID configurado. Si falla, escribe 401 o 403 y devuelve false.
func EnforcePlatformCaller(w http.ResponseWriter, r *http.Request, platformTenantID string) bool {
	id, ok := IdentityFromContext(r.Context())
	if !ok || id.TenantID == "" {
		writeAuthError(w, http.StatusUnauthorized, "autenticación requerida")
		return false
	}
	if platformTenantID == "" || id.TenantID != platformTenantID {
		writeAuthError(w, http.StatusForbidden, "operación reservada al plano de plataforma")
		return false
	}
	return true
}

// PlatformTarget aplica los dos cerrojos del plano de plataforma y devuelve el
// tenant OBJETIVO leído del cuerpo. Si devuelve ok=false ya escribió la
// respuesta de error y el llamante solo tiene que volver.
//
// Exportada a propósito (T2.1 pedía dos funciones exportadas de este
// paquete): cualquier ruta futura que necesite "cerca de plataforma + tenant
// objetivo del cuerpo" la reutiliza en vez de copiarla -- la copia que la
// propia tarea prohíbe.
func PlatformTarget(w http.ResponseWriter, r *http.Request, platformTenantID string) (string, bool) {
	if !EnforcePlatformCaller(w, r, platformTenantID) {
		return "", false
	}

	var req tenantTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
		return "", false
	}
	if req.TenantID == "" {
		http.Error(w, "tenant_id es requerido", http.StatusBadRequest)
		return "", false
	}
	SetAuditTargetTenant(r.Context(), req.TenantID)
	return req.TenantID, true
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
//   - 504 si se agota el contexto/timeout esperando el Ack del Edge, y también si el
//     stream del Edge cae mientras se espera (mismo código, texto distinto).
//   - 500 ante cualquier otro error del envío.
//   - 400 si el cuerpo JSON es inválido o falta algún campo; 405 si el método no
//     es POST.
//
// SEGURIDAD (Plan 018 · T4): el endpoint se monta DETRÁS de Authenticate →
// RequirePermission("messages.send"). El envío se dirige por session_id (no lleva
// tenant en el cuerpo); la autorización la impone el middleware.
func SendMessageHandler(sender MessageSender, log sharedlogger.Logger) http.Handler {
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
			writeSendError(w, err, log, req.SessionID)
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
// offline -> 502, stream caído esperando el Ack -> 504, timeout/cancelación esperando
// el Ack -> 504, resto -> 500. Adjunta el command_id al mensaje cuando ya estaba
// asignado: un 504 sin él no permite averiguar después si el mensaje llegó a salir
// (ver el gemelo público en publicapi/messages.go, que responde JSON con el mismo dato).
//
// Los DOS casos del 504 comparten código y se separan por el TEXTO (Plan 050 · Ola 2 ·
// T2.4). El enunciado de esa tarea pedía un 502 para el stream caído y NO se siguió
// (decisión de Jhoan): el 502 de este repo significa «el comando NO llegó a salir» —es
// el de ErrSessionOffline, donde el fallo ocurre AL EMPUJAR—, y cuando el stream muere
// esperando el Ack el Push ya tuvo éxito, así que el comando viajó y el mensaje pudo
// haberse enviado. Mandarlo con 502 invitaría a reintentar justo cuando reintentar
// puede duplicarle un mensaje de WhatsApp a un cliente real.
//
// El gemelo público hace lo mismo con el mismo texto: si aquí cambia el criterio y allí
// no, la mitad del API queda mintiendo sobre el mismo suceso.
func writeSendError(w http.ResponseWriter, err error, log sharedlogger.Logger, sessionID string) {
	cmdID := commandIDFrom(err)
	code, msg := http.StatusInternalServerError, "no se pudo enviar el texto"
	switch {
	case errors.Is(err, session.ErrSessionOffline):
		code, msg = http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge"
	case streamCaidoFrom(err):
		code, msg = http.StatusGatewayTimeout, msgStreamCaido
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code, msg = http.StatusGatewayTimeout, "timeout esperando el ack del Edge"
	}
	if cmdID != "" {
		msg = fmt.Sprintf("%s (command_id: %s)", msg, cmdID)
	}
	if log != nil {
		// CERO PII: ni el destino ni el texto del mensaje.
		log.Error("envío admin fallido",
			"status", code, "command_id", cmdID, "session_id", sessionID, "error", err)
	}
	http.Error(w, msg, code)
}

// msgStreamCaido es el cuerpo del 504 «se cayó», distinguible a simple vista del 504
// «no contestó a tiempo» en un log. Duplicado a propósito en publicapi (mismo texto):
// es el mismo suceso visto por los dos APIs, y un operador debe reconocerlo igual en
// ambos. Redactado SIN la palabra «no se pudo enviar»: eso afirmaría algo falso —el
// comando ya viajó al Edge y el mensaje pudo llegar a WhatsApp— y la única acción
// honesta que le queda al llamante es verificar antes de reenviar, no reintentar.
const msgStreamCaido = "el stream del Edge se cerró antes del ack: el comando YA viajó, " +
	"así que no se sabe si el mensaje salió. Verifica el envío (por el command_id, contra el " +
	"outbox del Edge o los acuses) ANTES de reenviar: reenviar a ciegas puede duplicárselo al cliente"

// streamCaidoFrom indica si el error viene de un stream que murió esperando el ack,
// por duck-typing (`interface{ StreamCaido() bool }`). Hermana exacta de commandIDFrom
// y duplicada en publicapi por la misma razón que se explica ahí abajo: el contrato es
// la interfaz anónima, no un tipo compartido, y es lo que permite al Gateway no
// aparecer en los imports de estos handlers. Con errors.Is(err,
// gatewaygrpc.ErrStreamClosed) habría que importarlo y el desacople se perdería.
//
// Falso NO significa «el envío fue bien»: significa que, si falló, fue por otra cosa.
func streamCaidoFrom(err error) bool {
	var caido interface{ StreamCaido() bool }
	return errors.As(err, &caido) && caido.StreamCaido()
}

// commandIDFrom extrae el command_id de un error de envío por duck-typing, sin
// importar el paquete del Gateway. Está duplicado a propósito en publicapi: el
// contrato es la interfaz anónima, no un tipo compartido, y ese es justamente el
// desacople que permite al Gateway no aparecer en los imports de los handlers.
func commandIDFrom(err error) string {
	var withID interface{ CommandID() string }
	if errors.As(err, &withID) {
		return withID.CommandID()
	}
	return ""
}
