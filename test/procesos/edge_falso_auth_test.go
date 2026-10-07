//go:build integracion

package procesos

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// La auth de operador del Edge de prueba (ADR-0025, ADR-0048): el Edge real no valida a su operador,
// RELAYA sus credenciales a la nube por el stream que ya tiene abierto —UserLogin, UserRefresh,
// UserLogout— y espera el UserAuthResponse con el mismo command_id. Esos frames estampan el
// session_id del CANAL DE CONTROL (edgeSesionControl), porque el operador puede entrar antes de
// emparejar ningún teléfono: no hay sesión de verdad que poner.
//
// Como el resto del doble, está partido en núcleo y transporte. El NÚCLEO (handleUserAuthResponse,
// el buzón y las esperas) se prueba sin servidor; el TRANSPORTE añade connectControlOnly, la
// conexión del Edge que aún no tiene teléfono: abre el stream con su certificado y NO late, así
// que no registra sesión ni pide lease.
//
// Lo que el doble hace distinto del Edge real, a propósito: el real DESCARTA una respuesta cuyo
// command_id no espera; el doble las guarda TODAS (AuthReplies), para que un test pueda afirmar que
// a este Edge no le llegó la respuesta de otro.

// edgeTopeAuth es lo que se espera, como máximo, la respuesta a una petición de auth: por encima
// del presupuesto del trabajo en el servidor (5 s) y del plazo de su llamada a identity (10 s).
const edgeTopeAuth = 15 * time.Second

// edgeAuthReply es un UserAuthResponse tal como llegó. CommandID y SessionID son los del SOBRE
// (CloudToEdge); InnerCommandID e InnerSessionID, los de dentro de la respuesta. IsError dice qué
// rama del oneof trajo: con true valen Code y Message; con false, los cuatro campos del token (que
// en un logout correcto llegan vacíos: es la convención del contrato).
type edgeAuthReply struct {
	CommandID, SessionID           string
	InnerCommandID, InnerSessionID string
	IsError                        bool
	Code, Message                  string
	AccessToken, RefreshToken      string
	TokenType                      string
	ExpiresAt                      int64
}

// edgeAuthInbox es el buzón de las respuestas de auth que llegaron por el stream, en orden.
type edgeAuthInbox struct {
	mu      sync.Mutex
	replies []edgeAuthReply
}

// handleUserAuthResponse guarda la respuesta en el buzón y NO la acusa: un UserAuthResponse es él
// mismo la contestación a una petición del Edge, y el Edge real tampoco le manda un Ack. Una
// respuesta sin ninguna de las dos ramas se guarda como error con Code vacío.
func (e *edge) handleUserAuthResponse(cmd *cloudlinkv1.CloudToEdge, resp *cloudlinkv1.UserAuthResponse) {
	r := edgeAuthReply{
		CommandID:      cmd.GetCommandId(),
		SessionID:      cmd.GetSessionId(),
		InnerCommandID: resp.GetCommandId(),
		InnerSessionID: resp.GetSessionId(),
	}
	if tk := resp.GetTokens(); tk != nil {
		r.AccessToken, r.RefreshToken = tk.GetAccessToken(), tk.GetRefreshToken()
		r.TokenType, r.ExpiresAt = tk.GetTokenType(), tk.GetExpiresAt()
	} else {
		r.IsError = true
		r.Code, r.Message = resp.GetError().GetCode(), resp.GetError().GetMessage()
	}
	e.auth.mu.Lock()
	defer e.auth.mu.Unlock()
	e.auth.replies = append(e.auth.replies, r)
}

// AuthReplies devuelve una copia de TODAS las respuestas de auth que este Edge recibió, en orden de
// llegada y desde que existe (no se vacía al reconectar): las que esperaba y las que no.
func (e *edge) AuthReplies() []edgeAuthReply {
	e.auth.mu.Lock()
	defer e.auth.mu.Unlock()
	return slices.Clone(e.auth.replies)
}

// authReplyFor devuelve la primera respuesta del buzón cuyo command_id de sobre es commandID.
func (e *edge) authReplyFor(commandID string) (edgeAuthReply, bool) {
	e.auth.mu.Lock()
	defer e.auth.mu.Unlock()
	for _, r := range e.auth.replies {
		if r.CommandID == commandID {
			return r, true
		}
	}
	return edgeAuthReply{}, false
}

// edgeAuthCommandID genera un command_id único para una petición de auth: el prefijo y 16
// hexadecimales aleatorios. Devuelve error (no falla el test) para poder llamarse desde goroutines.
func edgeAuthCommandID(prefix string) (string, error) {
	azar := make([]byte, 8)
	if _, err := rand.Read(azar); err != nil {
		return "", fmt.Errorf("generar el command_id: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(azar), nil
}

// edgeAuthLoginFrame arma el UserLogin que relaya el Edge: la sesión va en el sobre y dentro (como
// el Edge real, que estampa la misma en los dos sitios) y las credenciales, tal cual.
func edgeAuthLoginFrame(session, commandID, email, password string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: session,
		Payload: &cloudlinkv1.EdgeToCloud_UserLogin{UserLogin: &cloudlinkv1.UserLoginRequest{
			CommandId: commandID, SessionId: session, Email: email, Password: password,
		}},
	}
}

// edgeAuthRefreshFrame arma el UserRefresh: el refresh token a canjear por un par nuevo.
func edgeAuthRefreshFrame(session, commandID, refreshToken string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: session,
		Payload: &cloudlinkv1.EdgeToCloud_UserRefresh{UserRefresh: &cloudlinkv1.UserRefreshRequest{
			CommandId: commandID, SessionId: session, RefreshToken: refreshToken,
		}},
	}
}

// edgeAuthLogoutFrame arma el UserLogout de UNA sesión (all_sessions = false).
func edgeAuthLogoutFrame(session, commandID, refreshToken string) *cloudlinkv1.EdgeToCloud {
	return &cloudlinkv1.EdgeToCloud{
		SessionId: session,
		Payload: &cloudlinkv1.EdgeToCloud_UserLogout{UserLogout: &cloudlinkv1.UserLogoutRequest{
			CommandId: commandID, SessionId: session, RefreshToken: refreshToken,
		}},
	}
}

// authRoundTrip manda una petición de auth y espera SU respuesta: la del mismo command_id. Devuelve
// el error de emitir (errEdgeSinSalida sin enlace), o uno que dice que no llegó si pasa edgeTopeAuth
// o termina el contexto antes. No falla el test: se puede llamar desde varias goroutines a la vez
// (los envíos van serializados y cada espera mira solo su command_id).
func (e *edge) authRoundTrip(ctx context.Context, frame *cloudlinkv1.EdgeToCloud, commandID string) (edgeAuthReply, error) {
	if err := e.emitir(frame); err != nil {
		return edgeAuthReply{}, fmt.Errorf("emitir %T (%s): %w", frame.GetPayload(), commandID, err)
	}
	ctx, cancelar := context.WithTimeout(ctx, edgeTopeAuth)
	defer cancelar()
	var reply edgeAuthReply
	llego := edgeSondear(ctx, func() bool {
		var ok bool
		reply, ok = e.authReplyFor(commandID)
		return ok
	})
	if !llego {
		return edgeAuthReply{}, fmt.Errorf("no llegó el UserAuthResponse de %s (%T) al Edge %s: %w",
			commandID, frame.GetPayload(), e.EdgeID, context.Cause(ctx))
	}
	return reply, nil
}

// userLogin relaya el login de un operador por la sesión dada (edgeSesionControl es lo que usa el
// Edge real) con un command_id recién generado, y devuelve la respuesta que le corresponde. Mismos
// errores que authRoundTrip.
func (e *edge) userLogin(ctx context.Context, session, email, password string) (edgeAuthReply, error) {
	id, err := edgeAuthCommandID("login")
	if err != nil {
		return edgeAuthReply{}, err
	}
	return e.authRoundTrip(ctx, edgeAuthLoginFrame(session, id, email, password), id)
}

// userRefresh relaya por el canal de control el canje de un refresh token y devuelve su respuesta.
func (e *edge) userRefresh(ctx context.Context, refreshToken string) (edgeAuthReply, error) {
	id, err := edgeAuthCommandID("refresh")
	if err != nil {
		return edgeAuthReply{}, err
	}
	return e.authRoundTrip(ctx, edgeAuthRefreshFrame(edgeSesionControl, id, refreshToken), id)
}

// userLogout relaya por el canal de control el cierre de UNA sesión de operador y devuelve su
// respuesta (en un logout correcto: la rama de tokens, vacía).
func (e *edge) userLogout(ctx context.Context, refreshToken string) (edgeAuthReply, error) {
	id, err := edgeAuthCommandID("logout")
	if err != nil {
		return edgeAuthReply{}, err
	}
	return e.authRoundTrip(ctx, edgeAuthLogoutFrame(edgeSesionControl, id, refreshToken), id)
}

// connectControlOnly conecta como un Edge recién instalado, SIN ningún teléfono emparejado: abre el
// stream Connect con su certificado (mTLS) y lanza la recepción, pero no manda ningún latido, así
// que no hay session_id que el servidor registre ni lease que emita. Lo único que este Edge puede
// hacer es hablar por el canal de control. No espera nada del servidor: si el mTLS falla, lo dirá
// la primera petición (que no tendrá respuesta) y Errores. Registra el cierre en t.Cleanup. Falla
// (t.Fatalf) si no consigue abrir el enlace.
func (e *edge) connectControlOnly(t *testing.T) {
	t.Helper()
	if err := e.cerrarEnlace(); err != nil {
		t.Logf("cerrar la conexión anterior de %s: %v", e.EdgeID, err)
	}
	e.reiniciarLeases()
	en, err := e.abrirEnlace(t.Context())
	if err != nil {
		t.Fatalf("connectControlOnly (%s): %v", e.EdgeID, err)
	}
	e.adoptar(en)
	t.Cleanup(func() { e.cerrarYAnotar(t) })
	go e.recibir(en)
}
