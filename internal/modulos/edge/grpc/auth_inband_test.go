package grpc

// Login, refresh y logout en banda (R-G9; ADR-0025): el tenant es el del canal mTLS, nunca el
// del mensaje; un par emitido para otro tenant no se entrega; los errores del puerto salen como
// códigos estables y nada más; y el logout correcto es un UserTokens vacío. Se llama a lo que
// llamará el carril de connect.go, con el connCtx ya armado y un stream en memoria como cable.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// tenantBoundOps son las dos operaciones atadas al tenant del canal: comparten el guard de
// tenant cruzado, el rechazo sin identidad mTLS y el mapa de errores.
var tenantBoundOps = []struct {
	name   string
	action string
	// script programa en el puerto lo que esa operación devolverá.
	script func(a *scriptedAuth, res domain.AuthResult, err error)
	run    func(srv *Server, cc connCtx, cmdID string)
}{
	{
		name: "login", action: "edge.auth.login",
		script: func(a *scriptedAuth, res domain.AuthResult, err error) {
			a.login = func(in.LoginInput) (domain.AuthResult, error) { return res, err }
		},
		run: func(srv *Server, cc connCtx, cmdID string) {
			srv.handleUserLogin(context.Background(), cc, loginRequest(cmdID))
		},
	},
	{
		name: "refresh", action: "edge.auth.refresh",
		script: func(a *scriptedAuth, res domain.AuthResult, err error) {
			a.refresh = func(in.RefreshInput) (domain.AuthResult, error) { return res, err }
		},
		run: func(srv *Server, cc connCtx, cmdID string) {
			srv.handleUserRefresh(context.Background(), cc, refreshRequest(cmdID, "refresh-viejo"))
		},
	},
}

// requireNoSecrets afirma que en text no aparece ninguna credencial ni token de los que
// manejan estos tests (🔒 zero-knowledge: ni en el log ni en la auditoría).
func requireNoSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range []string{"access-", "refresh-", operatorTyped, operatorEmail} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contiene %q: ni credenciales ni tokens salen de la respuesta: %q", where, secret, text)
		}
	}
}

// El login se acota al tenant del CANAL (el mensaje no trae tenant), con el email y la clave
// del operador tal cual, y el par emitido vuelve intacto por el stream que lo pidió. Nada pasa
// por el Registry, y nada queda en el log.
func TestUserLoginIsScopedToTheChannelTenantAndDeliversThePair(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))
	stream := &liveSession{id: "el stream del Edge"}
	cc := controlChannel("tenant-1", "edge-1", stream)

	rig.srv.handleUserLogin(context.Background(), cc, loginRequest("cmd-1"))

	requireTokens(t, onlyAuthResponse(t, stream, cc, "cmd-1"), tokensFor("tenant-1", "user-1"))
	want := in.LoginInput{Email: operatorEmail, Password: operatorTyped, TenantID: "tenant-1"}
	if len(rig.authn.logins) != 1 || rig.authn.logins[0] != want {
		t.Errorf("al puerto le llegó %+v, se esperaba una vez %+v", rig.authn.logins, want)
	}
	requireNothing(t, rig.intruder)
	if rig.log.String() != "" {
		t.Errorf("un login limpio no deja rastro en el log: %q", rig.log.String())
	}
}

// El refresh canjea el refresh token del mensaje y entrega el par rotado. Vale igual por una
// sesión real que por el canal de control: la respuesta lleva el session_id del frame.
func TestUserRefreshDeliversTheRotatedPair(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, echoTenantAuth("user-1"))
	stream := &liveSession{id: "el stream del Edge"}
	cc := phone("tenant-1", "edge-1", "s-1")
	cc.sender = stream

	rig.srv.handleUserRefresh(context.Background(), cc, refreshRequest("cmd-2", "refresh-tenant-1"))

	requireTokens(t, onlyAuthResponse(t, stream, cc, "cmd-2"), tokensFor("tenant-1", "user-1"))
	if len(rig.authn.refreshs) != 1 || rig.authn.refreshs[0] != (in.RefreshInput{RefreshToken: "refresh-tenant-1"}) {
		t.Errorf("al puerto le llegó %+v, se esperaba una vez el refresh token del mensaje", rig.authn.refreshs)
	}
	requireNothing(t, rig.intruder)
}

// 🔴 Guard de tenant cruzado: una identidad válida de OTRO tenant no entra por el canal de
// este Edge. Responde tenant_mismatch, sin mensaje y sin tokens.
func TestAnIdentityFromAnotherTenantIsNotDelivered(t *testing.T) {
	t.Parallel()
	for _, op := range tenantBoundOps {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()
			authn := &scriptedAuth{}
			op.script(authn, tokensFor("tenant-ajeno", "user-ajeno"), nil)
			rig := newAuthRig(t, authn)
			stream := &liveSession{id: "el stream del Edge"}
			cc := controlChannel("tenant-1", "edge-1", stream)

			op.run(rig.srv, cc, "cmd-1")

			requireAuthError(t, onlyAuthResponse(t, stream, cc, "cmd-1"), "tenant_mismatch", "")
			requireNothing(t, rig.intruder)
		})
	}
}

// Sin identidad mTLS no se conoce el tenant del canal: se rechaza con tenant_mismatch y su
// mensaje, SIN llegar a preguntar al puerto.
func TestWithoutChannelIdentityThePortIsNeverAsked(t *testing.T) {
	t.Parallel()
	for _, op := range tenantBoundOps {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()
			rig := newAuthRig(t, echoTenantAuth("user-1"))
			stream := &liveSession{id: "el stream del Edge"}
			cc := connCtx{sessionID: "s-1", sender: stream}

			op.run(rig.srv, cc, "cmd-1")

			requireAuthError(t, onlyAuthResponse(t, stream, cc, "cmd-1"), "tenant_mismatch", "canal sin identidad")
			if n := rig.authn.calls(); n != 0 {
				t.Errorf("se llamó al puerto %d veces sin conocer el tenant del canal", n)
			}
		})
	}
}

// portErrors es el mapa completo de errores del puerto a códigos del contrato con el Edge.
// Un centinela envuelto se reconoce igual; lo que no se reconoce es `internal`.
var portErrors = []struct {
	name string
	err  error
	code string
}{
	{"invalid credentials", domain.ErrInvalidCredentials, "invalid_credentials"},
	{"inactive user", domain.ErrUserInactive, "user_inactive"},
	{"invalid refresh", domain.ErrRefreshInvalid, "refresh_invalid"},
	{"invalid input", domain.ErrInvalidInput, "invalid_input"},
	{"wrapped sentinel", fmt.Errorf("identity-core: %w", domain.ErrUserInactive), "user_inactive"},
	{"not found is not part of the contract", domain.ErrNotFound, "internal"},
	{"unknown error", errors.New("identity-core respondió 502 con el cuerpo secreto-interno"), "internal"},
}

// authErrorCode traduce cada error tipado a su código estable y cualquier otro a `internal`.
func TestAuthErrorCodeMapsPortErrorsToStableCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range portErrors {
		if got := authErrorCode(tc.err); got != tc.code {
			t.Errorf("%s: authErrorCode = %q, se esperaba %q", tc.name, got, tc.code)
		}
	}
}

// Un fallo del puerto sale como su código y NADA más: sin tokens, sin mensaje, y sin que el
// texto del error crudo llegue al Edge ni al log.
func TestAPortFailureIsAnsweredWithItsCodeAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, op := range tenantBoundOps {
		for _, tc := range portErrors {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				authn := &scriptedAuth{}
				// El puerto devuelve un par ADEMÁS del error: no debe entregarse.
				op.script(authn, tokensFor("tenant-1", "user-1"), tc.err)
				rig := newAuthRig(t, authn)
				stream := &liveSession{id: "el stream del Edge"}
				cc := controlChannel("tenant-1", "edge-1", stream)

				op.run(rig.srv, cc, "cmd-1")

				requireAuthError(t, onlyAuthResponse(t, stream, cc, "cmd-1"), tc.code, "")
				if rig.log.contains("secreto-interno") {
					t.Errorf("el error crudo del puerto llegó al log: %q", rig.log.String())
				}
			})
		}
	}
}

// El logout revoca el refresh token del mensaje (o todos, con all_sessions), sin user_id —lo
// resuelve identity—, y contesta por la rama Tokens con un UserTokens VACÍO.
func TestUserLogoutAnswersWithEmptyTokens(t *testing.T) {
	t.Parallel()
	for name, allSessions := range map[string]bool{"one session": false, "all sessions": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newAuthRig(t, &scriptedAuth{})
			stream := &liveSession{id: "el stream del Edge"}
			cc := controlChannel("tenant-1", "edge-1", stream)

			rig.srv.handleUserLogout(context.Background(), cc, logoutRequest("cmd-3", "refresh-tenant-1", allSessions))

			resp := onlyAuthResponse(t, stream, cc, "cmd-3")
			tokens := resp.GetTokens()
			if tokens == nil {
				t.Fatalf("el logout correcto usa la rama Tokens: %v", resp.GetError())
			}
			if tokens.GetAccessToken() != "" || tokens.GetRefreshToken() != "" || tokens.GetTokenType() != "" || tokens.GetExpiresAt() != 0 {
				t.Errorf("el UserTokens del logout debía ir vacío: %v", tokens)
			}
			want := in.LogoutInput{RefreshToken: "refresh-tenant-1", AllSessions: allSessions}
			if len(rig.authn.logouts) != 1 || rig.authn.logouts[0] != want {
				t.Errorf("al puerto le llegó %+v, se esperaba una vez %+v", rig.authn.logouts, want)
			}
			requireNothing(t, rig.intruder)
		})
	}
}

// El logout NO exige identidad mTLS (a diferencia de login y refresh): revoca igual.
func TestUserLogoutDoesNotRequireChannelIdentity(t *testing.T) {
	t.Parallel()
	rig := newAuthRig(t, &scriptedAuth{})
	stream := &liveSession{id: "el stream del Edge"}
	cc := connCtx{sessionID: "s-1", sender: stream}

	rig.srv.handleUserLogout(context.Background(), cc, logoutRequest("cmd-3", "refresh-tenant-1", false))

	if resp := onlyAuthResponse(t, stream, cc, "cmd-3"); resp.GetTokens() == nil {
		t.Errorf("sin identidad el logout debía revocar igual: %v", resp.GetError())
	}
	if n := rig.authn.calls(); n != 1 {
		t.Errorf("llamadas al puerto = %d, se esperaba 1", n)
	}
}

// Un fallo del logout sale por la rama Error con su código.
func TestUserLogoutFailureIsAnsweredWithItsCode(t *testing.T) {
	t.Parallel()
	for _, tc := range portErrors {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newAuthRig(t, &scriptedAuth{logout: func(in.LogoutInput) error { return tc.err }})
			stream := &liveSession{id: "el stream del Edge"}
			cc := controlChannel("tenant-1", "edge-1", stream)

			rig.srv.handleUserLogout(context.Background(), cc, logoutRequest("cmd-3", "refresh-tenant-1", false))

			requireAuthError(t, onlyAuthResponse(t, stream, cc, "cmd-3"), tc.code, "")
		})
	}
}

// Sin puerto de autenticación las tres peticiones responden `internal` («auth no disponible»)
// y no se audita nada: no es el intento fallido de nadie, es un despliegue sin auth.
func TestWithoutAuthenticatorEveryRequestAnswersInternal(t *testing.T) {
	t.Parallel()
	requests := map[string]func(srv *Server, cc connCtx){
		"login": func(srv *Server, cc connCtx) { srv.handleUserLogin(context.Background(), cc, loginRequest("cmd-1")) },
		"refresh": func(srv *Server, cc connCtx) {
			srv.handleUserRefresh(context.Background(), cc, refreshRequest("cmd-1", "r"))
		},
		"logout": func(srv *Server, cc connCtx) {
			srv.handleUserLogout(context.Background(), cc, logoutRequest("cmd-1", "r", false))
		},
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newAuthRig(t, nil)
			stream := &liveSession{id: "el stream del Edge"}
			cc := controlChannel("tenant-1", "edge-1", stream)

			request(rig.srv, cc)

			requireAuthError(t, onlyAuthResponse(t, stream, cc, "cmd-1"), "internal", "auth no disponible")
			if got := rig.audit.recorded(); len(got) != 0 {
				t.Errorf("sin puerto no se audita nada: %+v", got)
			}
		})
	}
}
