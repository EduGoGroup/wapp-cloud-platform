package grpc

// El contrato de auth.go por su cara exportada: las dos opciones que inyectan el puerto de
// autenticación y el auditor. Lo que el gateway HACE con ellos (login, refresh y logout en
// banda, R-G9; quién firma cada evento, R-G10; dos Edge a la vez, R3.4.a) no tiene cara
// exportada hasta que exista Connect: se afirma por dentro, sobre lo que llamará el carril,
// en auth_inband_test.go, auth_audit_test.go y auth_multiedge_test.go, que nacen con el verde.
//
// Aquí viven también los dobles y el banco que comparten esos tres ficheros.

import (
	"context"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// scriptedAuth es un in.Authenticator de respuestas programadas que apunta lo que le piden.
// Sin función programada, cada operación devuelve su cero sin error.
type scriptedAuth struct {
	login   func(in.LoginInput) (domain.AuthResult, error)
	refresh func(in.RefreshInput) (domain.AuthResult, error)
	logout  func(in.LogoutInput) error

	mu       sync.Mutex
	logins   []in.LoginInput
	refreshs []in.RefreshInput
	logouts  []in.LogoutInput
}

var _ in.Authenticator = (*scriptedAuth)(nil)

func (a *scriptedAuth) Login(_ context.Context, req in.LoginInput) (domain.AuthResult, error) {
	a.mu.Lock()
	a.logins = append(a.logins, req)
	a.mu.Unlock()
	if a.login == nil {
		return domain.AuthResult{}, nil
	}
	return a.login(req)
}

func (a *scriptedAuth) Refresh(_ context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	a.mu.Lock()
	a.refreshs = append(a.refreshs, req)
	a.mu.Unlock()
	if a.refresh == nil {
		return domain.AuthResult{}, nil
	}
	return a.refresh(req)
}

func (a *scriptedAuth) Logout(_ context.Context, req in.LogoutInput) error {
	a.mu.Lock()
	a.logouts = append(a.logouts, req)
	a.mu.Unlock()
	if a.logout == nil {
		return nil
	}
	return a.logout(req)
}

func (*scriptedAuth) Verify(context.Context, string) (in.VerifyResult, error) {
	return in.VerifyResult{}, nil
}

// calls dice cuántas veces se llamó al puerto, sumando las tres operaciones.
func (a *scriptedAuth) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.logins) + len(a.refreshs) + len(a.logouts)
}

// auditLog es un in.Auditor que guarda los eventos que recibe y puede fallar.
type auditLog struct {
	err error

	mu     sync.Mutex
	events []in.AuditInput
}

var _ in.Auditor = (*auditLog)(nil)

func (l *auditLog) Record(_ context.Context, ev in.AuditInput) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return l.err
}

func (*auditLog) ListAudit(context.Context, string, int, int) ([]domain.AuditEvent, error) {
	return nil, nil
}

func (l *auditLog) recorded() []in.AuditInput {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]in.AuditInput(nil), l.events...)
}

// authRig es un Server con el puerto de autenticación dado (nil = sin puerto), un auditor y, en el Registry,
// el canal de control de OTRA empresa ocupando la clave compartida `__wapp_control__`: lo que
// T-5 dice que no debe recibir jamás la respuesta de nadie.
type authRig struct {
	srv      *Server
	reg      *session.Registry
	log      *logBuffer
	authn    *scriptedAuth
	audit    *auditLog
	intruder *liveSession
	// evict da de baja al intruso del Registry, como haría el cierre de su stream.
	evict func()
}

func newAuthRig(t *testing.T, authn *scriptedAuth, regOpts ...session.RegistryOption) *authRig {
	t.Helper()
	log, buf := debugLog()
	reg := session.NewRegistry(regOpts...)
	audit := &auditLog{}
	intruder := &liveSession{id: "el canal de control de otra empresa"}
	var once sync.Once
	release := reg.Register(cltransport.ControlSessionID, intruder)
	evict := func() { once.Do(release) }
	t.Cleanup(evict)
	opts := []Option{WithAuthAuditor(audit)}
	if authn != nil { // un *scriptedAuth nil dentro de la interfaz no sería «sin puerto»
		opts = append(opts, WithAuthenticator(authn))
	}
	return &authRig{
		srv: New(reg, log, opts...),
		reg: reg, log: buf, authn: authn, audit: audit, intruder: intruder, evict: evict,
	}
}

// tokenExpiry es la expiración fija de los pares de mentira: ningún reloj real.
var tokenExpiry = time.Unix(1_790_000_000, 0)

// tokensFor arma el resultado de un login o refresh correcto para ese tenant y esa persona.
// Los tokens llevan el tenant dentro: es lo que deja afirmar QUÉ par recibió cada Edge.
func tokensFor(tenantID, userID string) domain.AuthResult {
	return domain.AuthResult{
		AccessToken:  "access-" + tenantID,
		RefreshToken: "refresh-" + tenantID,
		TokenType:    "Bearer",
		ExpiresAt:    tokenExpiry,
		Context:      domain.IdentityContext{TenantID: tenantID, UserID: userID},
	}
}

// echoTenantAuth es un puerto que acredita a todo el mundo en el tenant que le piden: el del
// login, y en el refresh el que va dentro del refresh token que emitió tokensFor.
func echoTenantAuth(userID string) *scriptedAuth {
	return &scriptedAuth{
		login: func(req in.LoginInput) (domain.AuthResult, error) { return tokensFor(req.TenantID, userID), nil },
		refresh: func(req in.RefreshInput) (domain.AuthResult, error) {
			return tokensFor(req.RefreshToken[len("refresh-"):], userID), nil
		},
	}
}

// Lo que teclea la operadora en la consola del Edge. De mentira, y lo que ningún log ni
// evento de auditoría puede contener.
const (
	operatorEmail = "operadora@example.com"
	operatorTyped = "la-clave-de-la-operadora"
)

func loginRequest(cmdID string) *cloudlinkv1.UserLoginRequest {
	return &cloudlinkv1.UserLoginRequest{CommandId: cmdID, Email: operatorEmail, Password: operatorTyped}
}

func refreshRequest(cmdID, refreshToken string) *cloudlinkv1.UserRefreshRequest {
	return &cloudlinkv1.UserRefreshRequest{CommandId: cmdID, RefreshToken: refreshToken}
}

func logoutRequest(cmdID, refreshToken string, allSessions bool) *cloudlinkv1.UserLogoutRequest {
	return &cloudlinkv1.UserLogoutRequest{CommandId: cmdID, RefreshToken: refreshToken, AllSessions: allSessions}
}

// requireAuthResponse afirma que frame es la respuesta de auth de esa petición: mismo
// command_id y el session_id del frame que la trajo, en el sobre y dentro.
func requireAuthResponse(t *testing.T, frame *cloudlinkv1.CloudToEdge, cc connCtx, cmdID string) *cloudlinkv1.UserAuthResponse {
	t.Helper()
	resp := frame.GetUserAuthResponse()
	if resp == nil {
		t.Fatalf("el frame no es un UserAuthResponse: %T", frame.GetPayload())
	}
	if frame.GetCommandId() != cmdID || resp.GetCommandId() != cmdID {
		t.Errorf("command_id = (%q, %q), se esperaba %q en el sobre y dentro", frame.GetCommandId(), resp.GetCommandId(), cmdID)
	}
	if frame.GetSessionId() != cc.sessionID || resp.GetSessionId() != cc.sessionID {
		t.Errorf("session_id = (%q, %q), se esperaba %q en el sobre y dentro", frame.GetSessionId(), resp.GetSessionId(), cc.sessionID)
	}
	return resp
}

// onlyAuthResponse afirma que el stream recibió exactamente UNA respuesta, la de esa petición.
func onlyAuthResponse(t *testing.T, stream *liveSession, cc connCtx, cmdID string) *cloudlinkv1.UserAuthResponse {
	t.Helper()
	frames := stream.received()
	if len(frames) != 1 {
		t.Fatalf("el stream %s recibió %d frames, se esperaba 1", stream.id, len(frames))
	}
	return requireAuthResponse(t, frames[0], cc, cmdID)
}

// requireAuthError afirma que la respuesta es la rama Error con ese código y ese mensaje, y
// que no lleva tokens.
func requireAuthError(t *testing.T, resp *cloudlinkv1.UserAuthResponse, code, message string) {
	t.Helper()
	if resp.GetTokens() != nil {
		t.Errorf("una respuesta de error no entrega tokens: %v", resp.GetResult())
	}
	if got := resp.GetError(); got.GetCode() != code || got.GetMessage() != message {
		t.Errorf("error = (%q, %q), se esperaba (%q, %q)", got.GetCode(), got.GetMessage(), code, message)
	}
}

// requireTokens afirma que la respuesta es la rama Tokens con el par de want, tal cual.
func requireTokens(t *testing.T, resp *cloudlinkv1.UserAuthResponse, want domain.AuthResult) {
	t.Helper()
	got := resp.GetTokens()
	if got == nil {
		t.Fatalf("se esperaba la rama Tokens: %v", resp.GetError())
	}
	if got.GetAccessToken() != want.AccessToken || got.GetRefreshToken() != want.RefreshToken ||
		got.GetTokenType() != want.TokenType || got.GetExpiresAt() != want.ExpiresAt.Unix() {
		t.Errorf("tokens = (%q, %q, %q, %d), se esperaba (%q, %q, %q, %d)",
			got.GetAccessToken(), got.GetRefreshToken(), got.GetTokenType(), got.GetExpiresAt(),
			want.AccessToken, want.RefreshToken, want.TokenType, want.ExpiresAt.Unix())
	}
}

// Las dos opciones dejan DENTRO del Server lo que se les da (y nil sin ellas).
func TestAuthOptionsStoreTheirPorts(t *testing.T) {
	t.Parallel()
	authn, audit := &scriptedAuth{}, &auditLog{}
	srv := New(session.NewRegistry(), quietLog(), WithAuthenticator(authn), WithAuthAuditor(audit))
	if srv.authn != in.Authenticator(authn) || srv.authAuditor != in.Auditor(audit) {
		t.Errorf("las opciones no dejaron sus puertos: authn=%v, auditor=%v", srv.authn, srv.authAuditor)
	}
	if bare := New(session.NewRegistry(), quietLog()); bare.authn != nil || bare.authAuditor != nil {
		t.Errorf("sin opciones los dos puertos son nil: authn=%v, auditor=%v", bare.authn, bare.authAuditor)
	}
}

// WithAuthenticator es una Option que New acepta, con puerto o con nil.
func TestWithAuthenticatorIsAnOptionNewAccepts(t *testing.T) {
	t.Parallel()
	for name, authn := range map[string]in.Authenticator{"with authenticator": &scriptedAuth{}, "nil authenticator": nil} {
		opt := WithAuthenticator(authn)
		if opt == nil {
			t.Fatalf("%s: WithAuthenticator devolvió una Option nil", name)
		}
		if srv := New(session.NewRegistry(), quietLog(), opt); srv == nil {
			t.Fatalf("%s: New devolvió nil", name)
		}
	}
}

// WithAuthAuditor es una Option que New acepta, con auditor o con nil.
func TestWithAuthAuditorIsAnOptionNewAccepts(t *testing.T) {
	t.Parallel()
	for name, auditor := range map[string]in.Auditor{"with auditor": &auditLog{}, "nil auditor": nil} {
		opt := WithAuthAuditor(auditor)
		if opt == nil {
			t.Fatalf("%s: WithAuthAuditor devolvió una Option nil", name)
		}
		if srv := New(session.NewRegistry(), quietLog(), opt); srv == nil {
			t.Fatalf("%s: New devolvió nil", name)
		}
	}
}
