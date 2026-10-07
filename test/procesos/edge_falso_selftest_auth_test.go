//go:build integracion

package procesos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"google.golang.org/protobuf/proto"
)

// TestArnes_EdgeAuth prueba, sin servidor, la auth de operador del Edge de prueba
// (edge_falso_auth_test.go): la forma de los tres frames que relaya, que un UserAuthResponse se
// guarda en el buzón con sus dos ramas y NO se acusa, y que la espera de una petición devuelve SU
// respuesta —la del mismo command_id— aunque antes llegue la de otro, y un error si no llega o si
// no hay enlace. El recorrido contra el servidor real es el proceso P1
// (TestP1_OperatorLoginOverControlChannel).
func TestArnes_EdgeAuth(t *testing.T) {
	t.Parallel()
	t.Run("los tres frames estampan la sesión y el command_id en el sobre y dentro", edgeAuthCheckFrames)
	t.Run("UserAuthResponse: se guarda con su rama y no se acusa", edgeAuthCheckInbox)
	t.Run("la espera devuelve la respuesta del mismo command_id", edgeAuthCheckRoundTrip)
	t.Run("sin respuesta o sin enlace, la espera devuelve un error", edgeAuthCheckRoundTripErrors)
}

// edgeAuthCheckFrames comprueba la forma de UserLogin, UserRefresh y UserLogout contra el frame
// esperado ENTERO: la sesión del sobre es la de dentro, el command_id va dentro (el sobre de
// EdgeToCloud no tiene), las credenciales y el refresh token viajan tal cual, y el logout es de una
// sola sesión (all_sessions en falso). Y que dos command_id generados no se repiten.
func edgeAuthCheckFrames(t *testing.T) {
	t.Parallel()
	password := "clave-" + edgeAleatorioHex(t, 6) // generada: en el arnés no hay credenciales escritas
	for name, c := range map[string]struct{ got, want *cloudlinkv1.EdgeToCloud }{
		"UserLogin": {
			edgeAuthLoginFrame(edgeSesionControl, "c-1", "ana@procesos.test", password),
			&cloudlinkv1.EdgeToCloud{SessionId: edgeSesionControl, Payload: &cloudlinkv1.EdgeToCloud_UserLogin{UserLogin: &cloudlinkv1.UserLoginRequest{
				CommandId: "c-1", SessionId: edgeSesionControl, Email: "ana@procesos.test", Password: password,
			}}},
		},
		"UserRefresh": {
			edgeAuthRefreshFrame("sesion-x", "c-2", "rt-1"),
			&cloudlinkv1.EdgeToCloud{SessionId: "sesion-x", Payload: &cloudlinkv1.EdgeToCloud_UserRefresh{UserRefresh: &cloudlinkv1.UserRefreshRequest{
				CommandId: "c-2", SessionId: "sesion-x", RefreshToken: "rt-1",
			}}},
		},
		"UserLogout": {
			edgeAuthLogoutFrame(edgeSesionControl, "c-3", "rt-2"),
			&cloudlinkv1.EdgeToCloud{SessionId: edgeSesionControl, Payload: &cloudlinkv1.EdgeToCloud_UserLogout{UserLogout: &cloudlinkv1.UserLogoutRequest{
				CommandId: "c-3", SessionId: edgeSesionControl, RefreshToken: "rt-2", AllSessions: false,
			}}},
		},
	} {
		if !proto.Equal(c.got, c.want) {
			t.Errorf("%s = %v, quería %v", name, c.got, c.want)
		}
	}
	a, errA := edgeAuthCommandID("login")
	b, errB := edgeAuthCommandID("login")
	if errA != nil || errB != nil {
		t.Fatalf("edgeAuthCommandID: %v, %v", errA, errB)
	}
	if a == b || !strings.HasPrefix(a, "login-") || len(a) != len("login-")+16 {
		t.Errorf("edgeAuthCommandID = %q y %q; quería dos distintos «login-<16 hex>»", a, b)
	}
}

// edgeAuthResponse arma el comando con el que el servidor contesta a una petición de auth: el
// command_id y la sesión en el sobre y dentro, y la rama que ponga fill.
func edgeAuthResponse(commandID, session string, fill func(*cloudlinkv1.UserAuthResponse)) *cloudlinkv1.CloudToEdge {
	resp := &cloudlinkv1.UserAuthResponse{CommandId: commandID, SessionId: session}
	fill(resp)
	return edgeComando(commandID, session, func(cmd *cloudlinkv1.CloudToEdge) {
		cmd.Payload = &cloudlinkv1.CloudToEdge_UserAuthResponse{UserAuthResponse: resp}
	})
}

// edgeAuthTokens rellena la rama de tokens de una respuesta; edgeAuthError, la de error.
func edgeAuthTokens(access, refresh string, expiresAt int64) func(*cloudlinkv1.UserAuthResponse) {
	return func(r *cloudlinkv1.UserAuthResponse) {
		r.Result = &cloudlinkv1.UserAuthResponse_Tokens{Tokens: &cloudlinkv1.UserTokens{
			AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresAt: expiresAt,
		}}
	}
}

func edgeAuthError(code, message string) func(*cloudlinkv1.UserAuthResponse) {
	return func(r *cloudlinkv1.UserAuthResponse) {
		r.Result = &cloudlinkv1.UserAuthResponse_Error{Error: &cloudlinkv1.UserAuthError{Code: code, Message: message}}
	}
}

// edgeAuthCheckInbox comprueba que el buzón guarda, en orden, una respuesta con tokens, una de
// error y una de logout (tokens vacíos, que NO es un error), con el sobre y el interior por
// separado; que ninguna se acusa (ni un frame emitido, aunque traigan command_id); que no dejan
// errores; y que AuthReplies devuelve una copia.
func edgeAuthCheckInbox(t *testing.T) {
	t.Parallel()
	e, c, _ := edgeDePrueba(t)
	e.manejar(edgeAuthResponse("c-ok", edgeSesionControl, edgeAuthTokens("acceso", "refresco", 1234)))
	e.manejar(edgeAuthResponse("c-mal", "sesion-x", edgeAuthError("invalid_credentials", "detalle")))
	e.manejar(edgeAuthResponse("c-fuera", edgeSesionControl, edgeAuthTokens("", "", 0)))

	want := []edgeAuthReply{
		{CommandID: "c-ok", SessionID: edgeSesionControl, InnerCommandID: "c-ok", InnerSessionID: edgeSesionControl,
			AccessToken: "acceso", RefreshToken: "refresco", TokenType: "Bearer", ExpiresAt: 1234},
		{CommandID: "c-mal", SessionID: "sesion-x", InnerCommandID: "c-mal", InnerSessionID: "sesion-x",
			IsError: true, Code: "invalid_credentials", Message: "detalle"},
		{CommandID: "c-fuera", SessionID: edgeSesionControl, InnerCommandID: "c-fuera", InnerSessionID: edgeSesionControl, TokenType: "Bearer"},
	}
	got := e.AuthReplies()
	if len(got) != len(want) {
		t.Fatalf("AuthReplies tiene %d respuestas, quería %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("respuesta %d = %+v, quería %+v", i, got[i], want[i])
		}
	}
	if frames := c.todos(); len(frames) != 0 || len(e.Errores()) != 0 {
		t.Errorf("un UserAuthResponse no se acusa ni es un error: %d frames emitidos, errores %v", len(frames), e.Errores())
	}
	got[0].AccessToken = "pisado"
	if e.AuthReplies()[0].AccessToken != "acceso" {
		t.Errorf("AuthReplies no devuelve una copia")
	}
}

// edgeAuthCheckRoundTrip pone como salida del Edge un «servidor» que, a cada UserLogin, contesta
// primero con la respuesta de OTRO command_id y después con la suya. userLogin tiene que devolver
// la suya, y la ajena queda en el buzón sin confundirse con ella.
func edgeAuthCheckRoundTrip(t *testing.T) {
	t.Parallel()
	e, _, _ := edgeDePrueba(t)
	e.salida = func(m *cloudlinkv1.EdgeToCloud) error {
		login := m.GetUserLogin()
		if login == nil {
			return errors.New("el Edge emitió algo que no es un UserLogin")
		}
		go func() {
			e.manejar(edgeAuthResponse("de-otro", m.GetSessionId(), edgeAuthTokens("ajeno", "ajeno", 1)))
			e.manejar(edgeAuthResponse(login.GetCommandId(), m.GetSessionId(), edgeAuthTokens("de-"+login.GetEmail(), "rt", 2)))
		}()
		return nil
	}
	reply, err := e.userLogin(t.Context(), edgeSesionControl, "ana@procesos.test", "clave")
	if err != nil {
		t.Fatalf("userLogin: %v", err)
	}
	if reply.IsError || reply.AccessToken != "de-ana@procesos.test" || reply.SessionID != edgeSesionControl ||
		!strings.HasPrefix(reply.CommandID, "login-") || reply.InnerCommandID != reply.CommandID {
		t.Errorf("userLogin devolvió %+v; quería la respuesta de su command_id", reply)
	}
	if all := e.AuthReplies(); len(all) != 2 || all[0].CommandID != "de-otro" {
		t.Errorf("el buzón debería guardar también la respuesta ajena, la primera: %+v", all)
	}
}

// edgeAuthCheckRoundTripErrors comprueba los dos fallos de la espera: sin enlace, el error de
// emitir (errEdgeSinSalida); con enlace y sin respuesta, un error al terminar el contexto —y no
// vale la respuesta de otro command_id que sí estaba en el buzón—.
func edgeAuthCheckRoundTripErrors(t *testing.T) {
	t.Parallel()
	e, _, _ := edgeDePrueba(t)
	e.manejar(edgeAuthResponse("de-otro", edgeSesionControl, edgeAuthTokens("ajeno", "ajeno", 1)))
	ctx, cancelar := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancelar()
	if reply, err := e.userRefresh(ctx, "rt-1"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("userRefresh sin respuesta = %+v, %v; quería un error por plazo vencido", reply, err)
	}
	e.salida = nil
	if _, err := e.userLogout(t.Context(), "rt-1"); !errors.Is(err, errEdgeSinSalida) {
		t.Errorf("userLogout sin enlace = %v; quería errEdgeSinSalida", err)
	}
}
