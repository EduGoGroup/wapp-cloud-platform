package gatewaygrpc

import (
	"context"
	"errors"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// Códigos de error tipados de UserAuthError (Plan 033 · T2.2, ADR-0025). Son
// CONTRATO estable con el Edge: strings fijos que el Edge mapea a mensajes de UI.
// El campo Message NUNCA filtra detalle sensible (existencia de cuenta, motivo
// exacto): los errores del IAM ya son opacos por diseño (ErrInvalidCredentials no
// distingue usuario inexistente de password mala).
const (
	authCodeInvalidCredentials = "invalid_credentials" //nolint:gosec // no es una credencial, es un código de error del contrato
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

	// auditActionSessionOpen es la apertura de una sesión CloudLink por un Edge:
	// el evento del plano de MÁQUINA, amparado por el mTLS del canal.
	auditActionSessionOpen = "edge.session.open"
	auditResourceSession   = "edge.session"

	// resultOK/resultError son los dos valores del campo Result de audit_events
	// (mismo vocabulario que AuthService.record del IAM).
	resultOK    = "ok"
	resultError = "error"
)

// Tipos de actor de la auditoría (identity Plan 003 · design.md Ola 3 §1.3).
//
// El Edge tiene DOS identidades separadas y esta ola las reparte, no las crea.
// Cada evento se etiqueta con cuál de las dos lo originó, y ese es el criterio
// de aceptación de T3.3: no basta con que cada llamada viaje con su identidad,
// tiene que poder distinguirse después «lo hizo el operador X» de «lo hizo el
// edge Y».
//
//   - actorOperator: la acción nace de una PERSONA en la consola local. El actor
//     es su `sub` —el UUID que le da identity— y el edge_id de meta dice
//     únicamente por qué canal entró.
//   - actorDaemon: la acción nace del PROCESO del Edge. El actor es el EdgeID,
//     que es el `CN` de su certificado mTLS: identidad criptográfica por-Edge, no
//     una credencial compartida. Ninguna llamada del daemon lleva jamás el token
//     de una persona.
const (
	actorTypeOperator = "operator"
	actorTypeDaemon   = "daemon"

	// metaActorType es la clave de meta (JSONB) donde va la etiqueta. Es lo que
	// permite filtrar la bitácora por tipo de actor sin adivinar por el formato
	// del campo actor.
	metaActorType = "actor_type"
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
// mTLS (nunca del mensaje). Delega en el puerto de autenticación acotando el login
// al tenant del canal y, tras autenticar, VERIFICA que el tenant de la identidad
// emitida coincida con el del canal (guard tenant cruzado, defensa en profundidad).
// Nunca entrega tokens de un tenant distinto al enrolado.
//
// Quién hay detrás del puerto lo decide el arranque (identity Plan 003 · Ola 3):
// con URL de identity, un delegado que valida las credenciales en el SSO del grupo
// y canjea la identidad por un Context Token; sin ella, el IAM local de siempre.
// El Edge no distingue los dos casos y no cambia ni una línea (REQ-A4): recibe el
// mismo par de tokens, y el access sigue siendo un Context Token que valida offline
// por `kid`. Con el delegado, el TenantID del input se ignora —identity no conoce
// tenants— pero el guard de abajo sigue comparando el tenant resuelto en wApp con
// el del canal, que es donde de verdad se sostiene la defensa.
func (s *Server) handleUserLogin(ctx context.Context, cc connCtx, req *cloudlinkv1.UserLoginRequest) {
	cmdID := req.GetCommandId()
	if s.authn == nil {
		s.pushAuthError(ctx, cc, cmdID, authCodeInternal, "auth no disponible")
		return
	}
	if !cc.hasIdentity {
		// Sin identidad mTLS no se conoce el tenant del canal: no se puede acotar
		// ni verificar coherencia ⇒ se rechaza sin tocar el IAM.
		s.recordEdgeAuth(ctx, cc, auditActionLogin, resultError, cmdID, "")
		s.pushAuthError(ctx, cc, cmdID, authCodeTenantMismatch, "canal sin identidad")
		return
	}
	res, err := s.authn.Login(ctx, in.LoginInput{
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
		TenantID: cc.tenantID, // ADR-0025: acota el login al tenant del canal mTLS.
	})
	if err != nil {
		s.recordEdgeAuth(ctx, cc, auditActionLogin, resultError, cmdID, "")
		s.pushAuthError(ctx, cc, cmdID, authErrorCode(err), "")
		return
	}
	if res.Context.TenantID != cc.tenantID {
		// Guard tenant cruzado (ADR-0025 §Principios): un token válido de otro
		// tenant NO entra por el canal de este Edge. No se entregan tokens.
		s.recordEdgeAuth(ctx, cc, auditActionLogin, resultError, cmdID, res.Context.UserID)
		s.pushAuthError(ctx, cc, cmdID, authCodeTenantMismatch, "")
		return
	}
	s.recordEdgeAuth(ctx, cc, auditActionLogin, resultOK, cmdID, res.Context.UserID)
	s.pushAuthTokens(ctx, cc, cmdID, res)
}

// handleUserRefresh atiende un UserRefresh relayado por el Edge: canjea el refresh
// token en el IAM (rota el par) y aplica el MISMO guard de tenant cruzado sobre la
// identidad re-resuelta (un refresh de otro tenant replayado por este canal se
// rechaza con tenant_mismatch).
func (s *Server) handleUserRefresh(ctx context.Context, cc connCtx, req *cloudlinkv1.UserRefreshRequest) {
	cmdID := req.GetCommandId()
	if s.authn == nil {
		s.pushAuthError(ctx, cc, cmdID, authCodeInternal, "auth no disponible")
		return
	}
	if !cc.hasIdentity {
		s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultError, cmdID, "")
		s.pushAuthError(ctx, cc, cmdID, authCodeTenantMismatch, "canal sin identidad")
		return
	}
	res, err := s.authn.Refresh(ctx, in.RefreshInput{RefreshToken: req.GetRefreshToken()})
	if err != nil {
		s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultError, cmdID, "")
		s.pushAuthError(ctx, cc, cmdID, authErrorCode(err), "")
		return
	}
	if res.Context.TenantID != cc.tenantID {
		s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultError, cmdID, res.Context.UserID)
		s.pushAuthError(ctx, cc, cmdID, authCodeTenantMismatch, "")
		return
	}
	s.recordEdgeAuth(ctx, cc, auditActionRefresh, resultOK, cmdID, res.Context.UserID)
	s.pushAuthTokens(ctx, cc, cmdID, res)
}

// handleUserLogout atiende un UserLogout relayado por el Edge: revoca el/los
// refresh token(s) (idempotente). Convención del contrato: éxito ⇒ rama Tokens
// con UserTokens VACÍO (todos los campos en cero); fallo ⇒ rama Error.
//
// El follow-up del Plan 033 («all_sessions degradado en el relé») queda disuelto
// con la delegación: la revocación global es competencia central de identity, que
// resuelve el titular server-side, así que el proto NO necesita ganar un user_id
// —que era justo lo que aquel follow-up temía tener que añadir—. El campo
// AllSessions sigue expresando la intención y ahora se honra.
func (s *Server) handleUserLogout(ctx context.Context, cc connCtx, req *cloudlinkv1.UserLogoutRequest) {
	cmdID := req.GetCommandId()
	if s.authn == nil {
		s.pushAuthError(ctx, cc, cmdID, authCodeInternal, "auth no disponible")
		return
	}
	if err := s.authn.Logout(ctx, in.LogoutInput{
		RefreshToken: req.GetRefreshToken(),
		AllSessions:  req.GetAllSessions(),
	}); err != nil {
		s.recordEdgeAuth(ctx, cc, auditActionLogout, resultError, cmdID, "")
		s.pushAuthError(ctx, cc, cmdID, authErrorCode(err), "")
		return
	}
	s.recordEdgeAuth(ctx, cc, auditActionLogout, resultOK, cmdID, "")
	// Éxito de logout: UserTokens VACÍO en la rama Tokens (convención del contrato).
	s.pushAuthResponse(ctx, cc, &cloudlinkv1.UserAuthResponse{
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

// pushAuthTokens responde con el par de tokens emitido (login/refresh ok). El ctx
// solo se transporta hasta el Push (Plan 050 · T1.5-bis); desde T1.9 es el del job
// del carril, no el del stream (ver pushAuthResponse).
func (s *Server) pushAuthTokens(ctx context.Context, cc connCtx, commandID string, res domain.AuthResult) {
	s.pushAuthResponse(ctx, cc, &cloudlinkv1.UserAuthResponse{
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

// pushAuthError responde con un UserAuthError tipado (fallo de auth). Mismo ctx y
// mismo motivo que pushAuthTokens.
func (s *Server) pushAuthError(ctx context.Context, cc connCtx, commandID, code, message string) {
	s.pushAuthResponse(ctx, cc, &cloudlinkv1.UserAuthResponse{
		CommandId: commandID,
		SessionId: cc.sessionID,
		Result:    &cloudlinkv1.UserAuthResponse_Error{Error: &cloudlinkv1.UserAuthError{Code: code, Message: message}},
	})
}

// pushAuthResponse devuelve la respuesta POR EL MISMO STREAM QUE TRAJO LA PETICIÓN
// (Plan 057 · Ola 1 · T1.3, REQ-057.1, INV-057.1). Best-effort: un fallo de entrega se
// loguea sin tumbar el stream (el Edge reintenta el login si no recibe respuesta).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ NO USA session.Registry, Y POR QUÉ ESO ERA UN FALLO DE SEGURIDAD
// ════════════════════════════════════════════════════════════════════════════
//
// Hasta el 2026-09-03 esta función hacía `s.registry.Push(ctx, cc.sessionID, msg)`.
// Parecía inocente —es lo que hace el resto de push del servidor— pero mezclaba dos
// cosas que no son la misma:
//
//   - session.Registry sirve para comandos que NACEN EN LA NUBE (un SendText de la API
//     pública, un LeaseUpdate, un ConfigUpdate): no tienen cable propio, así que hay
//     que ENCONTRAR el del Edge. Para eso está bien.
//   - Una respuesta de auth NO nace en la nube: es la contestación a una petición que
//     acaba de llegar por un socket que sigue abierto. Buscar su destino en un mapa es
//     desacoplar lo que ya venía acoplado.
//
// Y el mapa MIENTE, porque los frames de auth del Edge estampan
// `cltransport.ControlSessionID` (`__wapp_control__`), una constante IDÉNTICA en todos
// los Edge del planeta —el operador puede loguearse antes de emparejar ningún
// teléfono, así que no hay session_id real que poner—, mientras que el Registry indexa
// por session_id SIN TENANT y con política última-gana. El segundo Edge que conectaba
// PISABA la entrada del primero.
//
// Lo que se observó en UAT: con la Mac y el VPS conectados a la vez, el login en la
// consola del VPS se fue por el cable de la Mac, que lo descartó por command_id
// desconocido; el VPS agotó sus 20 s de relay y devolvió HTTP 503 relay_offline. Y al
// parar la Mac, su release() borró la clave compartida y el VPS se quedó SIN canal de
// control hasta reiniciarlo.
//
// 🔴 Lo grave no es eso: es que la colisión NUNCA estuvo acotada a un cliente. Con dos
// Edge de EMPRESAS DISTINTAS, el UserAuthResponse del operador de una —con su
// access_token y su refresh_token dentro— se escribía en el socket de la otra. Los dos
// tests de auth_multiedge_test.go congelan exactamente eso.
//
// Contestar por `cc.sender` no es una optimización: elimina la clase entera. No hay
// clave que confundir porque no hay búsqueda.
//
// ⚠️ NO HAY FALLBACK A registry.Push, y es una decisión (D-057.2). La tentación —«por
// si el connCtx no trae sender, en tests»— reintroduce el camino exacto del incidente
// y lo deja armado para el día en que alguien construya un connCtx incompleto en
// producción. Un connCtx sin sender es un error de programa, no un caso degradado:
// se GRITA y no se entrega. Las pruebas inyectan un sender falso, que es más barato
// que un fallback que miente.
//
// Sobre el ctx (Plan 050): T1.5-bis lo introdujo razonando que era el del stream y
// que, muerto el stream, no había a quién responder. T1.9 movió las tres ramas de auth
// al carril, así que HOY el ctx que llega aquí es el del JOB, cuya base es
// context.WithoutCancel(streamCtx): ya NO se cancela al morir el stream, y quien acota
// la espera es el presupuesto del job (workBudget, 5 s por defecto) por debajo del
// plazo del Registry (10 s), que SendAcotado sigue respetando por este camino.
//
// 🔴 Orden: como esta respuesta no sale de la goroutine del bucle Recv, su orden
// relativo respecto a un ConfigUpdate empujado por otra vía no está garantizado. El
// Edge no lo nota (el streamSender serializa las escrituras) y hoy nadie depende de
// ese orden; queda escrito para que nadie empiece.
func (s *Server) pushAuthResponse(ctx context.Context, cc connCtx, resp *cloudlinkv1.UserAuthResponse) {
	if cc.sender == nil {
		s.log.Error("auth: el connCtx no trae el stream emisor; la respuesta NO se entrega",
			"session_id", cc.sessionID, "command_id", resp.GetCommandId(), "edge_id", cc.edgeID)
		return
	}
	msg := &cloudlinkv1.CloudToEdge{
		CommandId: resp.GetCommandId(),
		SessionId: cc.sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_UserAuthResponse{UserAuthResponse: resp},
	}
	// El destino del mensaje de error es el edge_id y no el session_id: por este
	// camino el session_id suele ser la constante de control, que no identifica a
	// nadie — decir «no salió hacia __wapp_control__» no ayudaría a nadie a las 3 AM.
	if err := session.SendAcotado(ctx, cc.sender, msg, s.registry.SendTimeout(), cc.edgeID); err != nil {
		s.log.Debug("auth: la respuesta no salió por el stream que la pidió",
			"session_id", cc.sessionID, "command_id", resp.GetCommandId(),
			"edge_id", cc.edgeID, "error", err)
	}
}

// recordEdgeAuth registra un evento edge.auth.* en audit_events (best-effort). CERO
// PII (INV-5): actor = userID OPACO (o "" cuando el fallo es pre-identidad); el
// tenant va a su columna y edge_id/session_id/command_id/channel a meta (JSONB).
// NUNCA se registran email, password ni tokens.
//
// Estos eventos son SIEMPRE de OPERADOR: nacen de una persona que se autentica
// en la consola local, y el Edge solo relaya. Por eso el actor es su `sub` y la
// etiqueta es actorTypeOperator incluso cuando el sujeto no llegó a resolverse
// (un login fallido sigue siendo el intento de una persona, no del daemon).
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
			metaActorType: actorTypeOperator,
			"edge_id":     cc.edgeID,
			"session_id":  cc.sessionID,
			"command_id":  commandID,
			"channel":     "cloudlink",
		},
	})
	if err != nil {
		s.log.Debug("auth: registrar auditoría", "action", action, "error", err)
	}
}

// recordEdgeSession registra la apertura de una sesión CloudLink: el evento del
// plano de MÁQUINA (best-effort, CERO PII).
//
// Aquí el actor es el EdgeID —el `CN` del certificado mTLS con el que el daemon
// se presentó—, no una persona. Es la contraparte que hace verificable la
// frontera del §1.3: con estos eventos y los edge.auth.* en la misma bitácora,
// «lo hizo el edge Y» y «lo hizo el operador X» se distinguen por el actor y su
// etiqueta, sin tener que interpretar el formato del identificador.
func (s *Server) recordEdgeSession(ctx context.Context, cc connCtx) {
	if s.authAuditor == nil || !cc.hasIdentity {
		return
	}
	err := s.authAuditor.Record(ctx, in.AuditInput{
		TenantID: cc.tenantID,
		Actor:    cc.edgeID,
		Action:   auditActionSessionOpen,
		Resource: auditResourceSession,
		Result:   resultOK,
		Meta: map[string]any{
			metaActorType: actorTypeDaemon,
			"edge_id":     cc.edgeID,
			"session_id":  cc.sessionID,
			"channel":     "cloudlink",
		},
	})
	if err != nil {
		s.log.Debug("auth: registrar auditoría de sesión", "error", err)
	}
}
