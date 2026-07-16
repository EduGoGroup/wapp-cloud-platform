package gatewaygrpc

import (
	"context"
	"errors"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// Códigos de error tipados de UserAuthError (Plan 033 · T2.2, ADR-0025). Son
// CONTRATO estable con el Edge: strings fijos que el Edge mapea a mensajes de UI.
// El campo Message NUNCA filtra detalle sensible (existencia de cuenta, motivo
// exacto): los errores del IAM ya son opacos por diseño (ErrInvalidCredentials no
// distingue usuario inexistente de password mala).
const (
	authCodeInvalidCredentials = "invalid_credentials"
	authCodeUserInactive       = "user_inactive"
	authCodeRefreshInvalid     = "refresh_invalid"
	authCodeInvalidInput       = "invalid_input"
	authCodeTenantMismatch     = "tenant_mismatch"
	authCodeInternal           = "internal"
)

// Acciones de auditoría del plano de auth de usuario del Edge (edge.auth.*). Se
// registran en audit_events con CERO PII (actor = userID opaco; edge/session/tenant
// en meta). Ver recordEdgeAuth.
const (
	auditActionLogin   = "edge.auth.login"
	auditActionRefresh = "edge.auth.refresh"
	auditActionLogout  = "edge.auth.logout"
	auditResourceAuth  = "edge.auth"

	// resultOK/resultError son los dos valores del campo Result de audit_events
	// (mismo vocabulario que AuthService.record del IAM).
	resultOK    = "ok"
	resultError = "error"
)

// WithAuthenticator inyecta el puerto de autenticación de usuario del IAM (Plan
// 033 · T2.2, ADR-0025). Con él, el gateway atiende UserLogin/UserRefresh/
// UserLogout relayados por el Edge, delegando en el AuthService existente. Sin él
// (nil), esas RPCs responden UserAuthError{internal} (auth no disponible en este
// despliegue). Mismo patrón que WithReceiptSink/WithDiagnosticsSink.
func WithAuthenticator(a in.Authenticator) Option { return func(s *Server) { s.authn = a } }

// WithAuthAuditor inyecta el auditor (in.Auditor) que registra los eventos
// edge.auth.* del plano de control del Edge. Sin él (nil), la auth funciona pero
// no se audita (best-effort, como el resto de la auditoría del IAM).
func WithAuthAuditor(a in.Auditor) Option { return func(s *Server) { s.authAuditor = a } }

// handleUserLogin atiende un UserLogin relayado por el Edge (ADR-0025 dec.1): el
// Edge transporta las credenciales del operador; el tenant es IMPLÍCITO del canal
// mTLS (nunca del mensaje). Delega en el IAM acotando el login al tenant del canal
// y, tras autenticar, VERIFICA que el tenant de la identidad emitida coincida con
// el del canal (guard tenant cruzado, defensa en profundidad). Nunca entrega
// tokens de un tenant distinto al enrolado.
func (s *Server) handleUserLogin(ctx context.Context, cc connCtx, req *cloudlinkv1.UserLoginRequest) {
	cmdID := req.GetCommandId()
	if s.authn == nil {
		s.pushAuthError(cc, cmdID, authCodeInternal, "auth no disponible")
		return
	}
	if !cc.hasIdentity {
		// Sin identidad mTLS no se conoce el tenant del canal: no se puede acotar
		// ni verificar coherencia ⇒ se rechaza sin tocar el IAM.
		s.recordEdgeAuth(ctx, cc, auditActionLogin, resultError, cmdID, "")
		s.pushAuthError(cc, cmdID, authCodeTenantMismatch, "canal sin identidad")
		return
	}
	res, err := s.authn.Login(ctx, in.LoginInput{
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
		TenantID: cc.tenantID, // ADR-0025: acota el login al tenant del canal mTLS.
	})
	if err != nil {
		s.recordEdgeAuth(ctx, cc, auditActionLogin, resultError, cmdID, "")
		s.pushAuthError(cc, cmdID, authErrorCode(err), "")
		return
	}
	if res.Context.TenantID != cc.tenantID {
		// Guard tenant cruzado (ADR-0025 §Principios): un token válido de otro
		// tenant NO entra por el canal de este Edge. No se entregan tokens.
		s.recordEdgeAuth(ctx, cc, auditActionLogin, resultError, cmdID, res.Context.UserID)
		s.pushAuthError(cc, cmdID, authCodeTenantMismatch, "")
		return
	}
	s.recordEdgeAuth(ctx, cc, auditActionLogin, resultOK, cmdID, res.Context.UserID)
	s.pushAuthTokens(cc, cmdID, res)
}

// handleUserRefresh atiende un UserRefresh relayado por el Edge: canjea el refresh
// token en el IAM (rota el par) y aplica el MISMO guard de tenant cruzado sobre la
// identidad re-resuelta (un refresh de otro tenant replayado por este canal se
// rechaza con tenant_mismatch).
func (s *Server) handleUserRefresh(ctx context.Context, cc connCtx, req *cloudlinkv1.UserRefreshRequest) {
	cmdID := req.GetCommandId()
	if s.authn == nil {
		s.pushAuthError(cc, cmdID, authCodeInternal, "auth no disponible")
		return
	}
	if !cc.hasIdentity {
		s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultError, cmdID, "")
		s.pushAuthError(cc, cmdID, authCodeTenantMismatch, "canal sin identidad")
		return
	}
	res, err := s.authn.Refresh(ctx, in.RefreshInput{RefreshToken: req.GetRefreshToken()})
	if err != nil {
		s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultError, cmdID, "")
		s.pushAuthError(cc, cmdID, authErrorCode(err), "")
		return
	}
	if res.Context.TenantID != cc.tenantID {
		s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultError, cmdID, res.Context.UserID)
		s.pushAuthError(cc, cmdID, authCodeTenantMismatch, "")
		return
	}
	s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultOK, cmdID, res.Context.UserID)
	s.pushAuthTokens(cc, cmdID, res)
}

// handleUserLogout atiende un UserLogout relayado por el Edge: revoca el/los
// refresh token(s) en el IAM (idempotente). Convención del contrato: éxito ⇒ rama
// Tokens con UserTokens VACÍO (todos los campos en cero); fallo ⇒ rama Error.
//
// NOTA (limitación conocida, follow-up Ola 3/4): el IAM revoca TODAS las sesiones
// del usuario solo con UserID informado, y el proto UserLogoutRequest no lo lleva
// (ni el puerto expone resolver el userID desde el refresh token). Por eso
// AllSessions se relaya pero hoy degrada a revocar el único refresh presentado.
func (s *Server) handleUserLogout(ctx context.Context, cc connCtx, req *cloudlinkv1.UserLogoutRequest) {
	cmdID := req.GetCommandId()
	if s.authn == nil {
		s.pushAuthError(cc, cmdID, authCodeInternal, "auth no disponible")
		return
	}
	if err := s.authn.Logout(ctx, in.LogoutInput{
		RefreshToken: req.GetRefreshToken(),
		AllSessions:  req.GetAllSessions(),
	}); err != nil {
		s.recordEdgeAuth(ctx, cc, auditActionLogout, resultError, cmdID, "")
		s.pushAuthError(cc, cmdID, authErrorCode(err), "")
		return
	}
	s.recordEdgeAuth(ctx, cc, auditActionLogout, resultOK, cmdID, "")
	// Éxito de logout: UserTokens VACÍO en la rama Tokens (convención del contrato).
	s.pushAuthResponse(cc, &cloudlinkv1.UserAuthResponse{
		CommandId: cmdID,
		SessionId: cc.sessionID,
		Result:    &cloudlinkv1.UserAuthResponse_Tokens{Tokens: &cloudlinkv1.UserTokens{}},
	})
}

// authErrorCode mapea los errores tipados del IAM al code estable de UserAuthError.
// Cualquier error no reconocido cae a "internal" (nunca se filtra el error crudo).
func authErrorCode(err error) string {
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		return authCodeInvalidCredentials
	case errors.Is(err, domain.ErrUserInactive):
		return authCodeUserInactive
	case errors.Is(err, domain.ErrRefreshInvalid):
		return authCodeRefreshInvalid
	case errors.Is(err, domain.ErrInvalidInput):
		return authCodeInvalidInput
	default:
		return authCodeInternal
	}
}

// pushAuthTokens responde con el par de tokens emitido (login/refresh ok).
func (s *Server) pushAuthTokens(cc connCtx, commandID string, res domain.AuthResult) {
	s.pushAuthResponse(cc, &cloudlinkv1.UserAuthResponse{
		CommandId: commandID,
		SessionId: cc.sessionID,
		Result: &cloudlinkv1.UserAuthResponse_Tokens{Tokens: &cloudlinkv1.UserTokens{
			AccessToken:  res.AccessToken,
			RefreshToken: res.RefreshToken,
			TokenType:    res.TokenType,
			ExpiresAt:    res.ExpiresAt.Unix(),
		}},
	})
}

// pushAuthError responde con un UserAuthError tipado (fallo de auth).
func (s *Server) pushAuthError(cc connCtx, commandID, code, message string) {
	s.pushAuthResponse(cc, &cloudlinkv1.UserAuthResponse{
		CommandId: commandID,
		SessionId: cc.sessionID,
		Result:    &cloudlinkv1.UserAuthResponse_Error{Error: &cloudlinkv1.UserAuthError{Code: code, Message: message}},
	})
}

// pushAuthResponse envuelve la respuesta en un CloudToEdge y la empuja a la sesión
// del Edge por el registry (correlacionada por command_id/session_id, igual que el
// resto de los push del servidor). Best-effort: un fallo de entrega se loguea sin
// tumbar el stream (el Edge reintenta el login si no recibe respuesta).
func (s *Server) pushAuthResponse(cc connCtx, resp *cloudlinkv1.UserAuthResponse) {
	msg := &cloudlinkv1.CloudToEdge{
		CommandId: resp.GetCommandId(),
		SessionId: cc.sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_UserAuthResponse{UserAuthResponse: resp},
	}
	if err := s.registry.Push(cc.sessionID, msg); err != nil {
		s.log.Debug("auth: push UserAuthResponse",
			"session_id", cc.sessionID, "command_id", resp.GetCommandId(), "error", err)
	}
}

// recordEdgeAuth registra un evento edge.auth.* en audit_events (best-effort). CERO
// PII (INV-5): actor = userID OPACO (o "" cuando el fallo es pre-identidad); el
// tenant va a su columna y edge_id/session_id/command_id/channel a meta (JSONB).
// NUNCA se registran email, password ni tokens.
func (s *Server) recordEdgeAuth(ctx context.Context, cc connCtx, action, result, commandID, actor string) {
	if s.authAuditor == nil {
		return
	}
	err := s.authAuditor.Record(ctx, in.AuditInput{
		TenantID: cc.tenantID,
		Actor:    actor,
		Action:   action,
		Resource: auditResourceAuth,
		Result:   result,
		Meta: map[string]any{
			"edge_id":    cc.edgeID,
			"session_id": cc.sessionID,
			"command_id": commandID,
			"channel":    "cloudlink",
		},
	})
	if err != nil {
		s.log.Debug("auth: registrar auditoría", "action", action, "error", err)
	}
}
