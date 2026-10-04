package usecase

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// testLoginPhrase es la contraseña que los tests del relé presentan al doble de identity. wApp
// no la comprueba: solo viaja de paso.
const testLoginPhrase = "una-frase-de-acceso-larga"

// quietLogger es un logger real que no escribe (lo reutiliza memberships_test.go).
func quietLogger() sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(io.Discard))
}

// spyIdentity es el doble propio de out.IdentityClient: registra la COREOGRAFÍA (qué se llama,
// en qué orden y con qué) y devuelve lo que se le diga.
type spyIdentity struct {
	mu           sync.Mutex
	calls        []string
	loginSystem  string
	lastRefresh  string
	lastBearer   string
	loginErr     error
	refreshErr   error
	logoutErr    error
	logoutAllErr error
	session      domain.IdentitySession
}

var _ out.IdentityClient = (*spyIdentity)(nil)

func (s *spyIdentity) Login(_ context.Context, _, _, system string) (domain.IdentitySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "login")
	s.loginSystem = system
	if s.loginErr != nil {
		return domain.IdentitySession{}, s.loginErr
	}
	return s.session, nil
}

func (s *spyIdentity) Refresh(_ context.Context, refreshToken string) (domain.IdentitySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "refresh")
	s.lastRefresh = refreshToken
	if s.refreshErr != nil {
		return domain.IdentitySession{}, s.refreshErr
	}
	return s.session, nil
}

func (s *spyIdentity) Logout(_ context.Context, refreshToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "logout")
	s.lastRefresh = refreshToken
	return s.logoutErr
}

func (s *spyIdentity) LogoutAll(_ context.Context, identityToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "logout-all")
	s.lastBearer = identityToken
	return s.logoutAllErr
}

// spyExchanger es el doble del canje: devuelve un resultado fijo y recuerda qué Identity
// Tokens le entregaron.
type spyExchanger struct {
	seen []string
	res  in.ExchangeResult
	err  error
}

func (s *spyExchanger) Exchange(_ context.Context, req in.ExchangeInput) (in.ExchangeResult, error) {
	s.seen = append(s.seen, req.IdentityToken)
	if s.err != nil {
		return in.ExchangeResult{}, s.err
	}
	return s.res, nil
}

const (
	identityTokenValue = "identity.token.firmado"
	rotatedRefresh     = "rft_rotado_por_identity_0123456789"
	contextTokenValue  = "context.token.de.wapp"
)

type delegatedFixture struct {
	svc      *DelegatedAuthService
	identity *spyIdentity
	exchange *spyExchanger
}

func newDelegatedFixture(t *testing.T, log sharedlogger.Logger) delegatedFixture {
	t.Helper()
	identity := &spyIdentity{session: domain.IdentitySession{
		SessionID:     "sess-1",
		IdentityToken: identityTokenValue,
		RefreshToken:  rotatedRefresh,
		ExpiresAt:     time.Date(2026, 10, 4, 12, 15, 0, 0, time.UTC),
	}}
	exchange := &spyExchanger{res: in.ExchangeResult{
		ContextToken: contextTokenValue,
		ExpiresAt:    time.Date(2026, 10, 4, 12, 10, 0, 0, time.UTC),
		Context:      domain.IdentityContext{TenantID: testTenant, UserID: "user-1", Roles: []string{"operator"}},
	}}
	svc, err := NewDelegatedAuthService(identity, exchange, sharedjwt.NewJWTManager(testSigningKey, testIssuer), SystemWappEdge, log)
	if err != nil {
		t.Fatalf("NewDelegatedAuthService: %v", err)
	}
	return delegatedFixture{svc: svc, identity: identity, exchange: exchange}
}

// ---------------------------------------------------------------------------
// Constructor (R-U14 y fail-fast)
// ---------------------------------------------------------------------------

func TestNewDelegatedAuthService_RequiresWappSystem(t *testing.T) {
	validator := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	const want = "iam: DelegatedAuthService requiere una aplicación de wApp (wapp.bff o wapp.edge)"
	for _, system := range []string{"", "edugo.kmp", "wapp", SystemWappPlatform} {
		t.Run("rejects_"+system, func(t *testing.T) {
			svc, err := NewDelegatedAuthService(&spyIdentity{}, &spyExchanger{}, validator, system, nil)
			if err == nil || svc != nil || err.Error() != want {
				t.Fatalf("system %q = %v, %v; quiere nil y el literal %q", system, svc, err, want)
			}
		})
	}
	for _, system := range []string{SystemWappBFF, SystemWappEdge} {
		t.Run("accepts_"+system, func(t *testing.T) {
			if svc, err := NewDelegatedAuthService(&spyIdentity{}, &spyExchanger{}, validator, system, nil); err != nil || svc == nil {
				t.Fatalf("system %q = %v, %v; quiere un servicio (el logger es opcional)", system, svc, err)
			}
		})
	}
}

func TestNewDelegatedAuthService_RequiresDependencies(t *testing.T) {
	validator := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	cases := []struct {
		name  string
		build func() (*DelegatedAuthService, error)
		want  string
	}{
		{"nil_identity", func() (*DelegatedAuthService, error) {
			return NewDelegatedAuthService(nil, &spyExchanger{}, validator, SystemWappEdge, nil)
		}, "iam: DelegatedAuthService requiere un cliente de identity"},
		{"nil_exchange", func() (*DelegatedAuthService, error) {
			return NewDelegatedAuthService(&spyIdentity{}, nil, validator, SystemWappEdge, nil)
		}, "iam: DelegatedAuthService requiere el canje (Exchanger)"},
		{"nil_validator", func() (*DelegatedAuthService, error) {
			return NewDelegatedAuthService(&spyIdentity{}, &spyExchanger{}, nil, SystemWappEdge, nil)
		}, "iam: DelegatedAuthService requiere un TokenValidator"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := c.build()
			if err == nil || svc != nil || err.Error() != c.want {
				t.Fatalf("= %v, %v; quiere nil y el literal %q", svc, err, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U10 · login delegado
// ---------------------------------------------------------------------------

func TestDelegatedLogin_ValidatesInIdentityAndExchangesInWapp(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	res, err := f.svc.Login(context.Background(), in.LoginInput{Email: testEmail, Password: testLoginPhrase, TenantID: testTenantB})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !slices.Equal(f.identity.calls, []string{"login"}) || f.identity.loginSystem != SystemWappEdge {
		t.Fatalf("identity = %v con system %q; quiere [login] con %q", f.identity.calls, f.identity.loginSystem, SystemWappEdge)
	}
	if !slices.Equal(f.exchange.seen, []string{identityTokenValue}) {
		t.Fatalf("el canje recibió %v; quiere el identity token de la sesión", f.exchange.seen)
	}
	want := domain.AuthResult{
		AccessToken:  contextTokenValue,
		RefreshToken: rotatedRefresh,
		TokenType:    "Bearer",
		ExpiresAt:    f.exchange.res.ExpiresAt,
		Context:      f.exchange.res.Context,
	}
	if res.AccessToken != want.AccessToken || res.RefreshToken != want.RefreshToken || res.TokenType != want.TokenType ||
		!res.ExpiresAt.Equal(want.ExpiresAt) || res.Context.TenantID != testTenant {
		t.Fatalf("resultado = %+v; quiere %+v (el TenantID pedido se ignora)", res, want)
	}
}

func TestDelegatedLogin_IdentityRejectionPropagatesWithoutExchange(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"invalid_credentials", domain.ErrInvalidCredentials},
		{"system_gate_denied", domain.ErrUserInactive},
		{"identity_down", domain.ErrIdentityUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDelegatedFixture(t, quietLogger())
			f.identity.loginErr = c.err
			if _, err := f.svc.Login(context.Background(), in.LoginInput{Email: testEmail, Password: testLoginPhrase}); !errors.Is(err, c.err) {
				t.Fatalf("err = %v; quiere %v", err, c.err)
			}
			if len(f.exchange.seen) != 0 {
				t.Errorf("se canjeó %v con el login rechazado", f.exchange.seen)
			}
		})
	}
}

func TestDelegatedLogin_UnmigratedSubjectDoesNotEnter(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	f.exchange.err = domain.ErrUserNotMigrated
	if _, err := f.svc.Login(context.Background(), in.LoginInput{Email: testEmail, Password: testLoginPhrase}); !errors.Is(err, domain.ErrUserNotMigrated) {
		t.Fatalf("err = %v; quiere ErrUserNotMigrated", err)
	}
}

func TestDelegatedLogin_EmptyCredentialsAreInvalidInput(t *testing.T) {
	for name, input := range map[string]in.LoginInput{
		"no_email":    {Password: testLoginPhrase},
		"no_password": {Email: testEmail},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDelegatedFixture(t, quietLogger())
			if _, err := f.svc.Login(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v; quiere ErrInvalidInput", err)
			}
			if len(f.identity.calls) != 0 {
				t.Errorf("se llamó a identity: %v", f.identity.calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U11 · refresh y logout
// ---------------------------------------------------------------------------

func TestDelegatedRefresh_RotatesInIdentityAndReexchanges(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	res, err := f.svc.Refresh(context.Background(), in.RefreshInput{RefreshToken: "rft_presentado"})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !slices.Equal(f.identity.calls, []string{"refresh"}) || f.identity.lastRefresh != "rft_presentado" {
		t.Fatalf("identity = %v (refresh %q); quiere [refresh] con el presentado", f.identity.calls, f.identity.lastRefresh)
	}
	if len(f.exchange.seen) != 1 || res.AccessToken != contextTokenValue || res.RefreshToken != rotatedRefresh {
		t.Fatalf("canjes = %v, resultado = %+v; quiere un re-canje y el refresh rotado", f.exchange.seen, res)
	}
}

// Un rechazo de identity al rotar sube tal cual y no se re-canjea nada.
func TestDelegatedRefresh_IdentityRejectionPropagatesWithoutExchange(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	f.identity.refreshErr = domain.ErrRefreshInvalid
	if _, err := f.svc.Refresh(context.Background(), in.RefreshInput{RefreshToken: "rft_quemado"}); !errors.Is(err, domain.ErrRefreshInvalid) { //nolint:gosec // token de mentira de un test
		t.Fatalf("err = %v; quiere ErrRefreshInvalid", err)
	}
	if len(f.exchange.seen) != 0 {
		t.Errorf("se re-canjeó %v con el refresh rechazado", f.exchange.seen)
	}
}

func TestDelegatedRefreshAndLogout_EmptyTokenIsInvalidInput(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	if _, err := f.svc.Refresh(context.Background(), in.RefreshInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("Refresh: err = %v; quiere ErrInvalidInput", err)
	}
	if err := f.svc.Logout(context.Background(), in.LogoutInput{AllSessions: true}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("Logout: err = %v; quiere ErrInvalidInput", err)
	}
	if len(f.identity.calls) != 0 {
		t.Errorf("se llamó a identity: %v", f.identity.calls)
	}
}

func TestDelegatedLogout_ClosesOnlyThisApplicationSession(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	if err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado"}); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !slices.Equal(f.identity.calls, []string{"logout"}) || f.identity.lastRefresh != "rft_presentado" {
		t.Fatalf("identity = %v (refresh %q); quiere [logout] con el presentado", f.identity.calls, f.identity.lastRefresh)
	}
}

func TestDelegatedLogout_AllSessionsRevokesWithoutCarryingUserID(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", UserID: "otro-sujeto", AllSessions: true})
	if err != nil {
		t.Fatalf("Logout(all): %v", err)
	}
	if !slices.Equal(f.identity.calls, []string{"refresh", "logout-all"}) {
		t.Fatalf("identity = %v; quiere [refresh logout-all]", f.identity.calls)
	}
	if f.identity.lastBearer != identityTokenValue {
		t.Errorf("bearer del logout-all = %q; quiere el identity token recién obtenido, no un user_id", f.identity.lastBearer)
	}
}

func TestDelegatedLogout_AllSessionsWithDeadRefreshRevokesNothing(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	f.identity.refreshErr = domain.ErrRefreshInvalid
	err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_quemado", AllSessions: true}) //nolint:gosec // token de mentira de un test
	if !errors.Is(err, domain.ErrRefreshInvalid) {
		t.Fatalf("err = %v; quiere ErrRefreshInvalid", err)
	}
	if !slices.Equal(f.identity.calls, []string{"refresh"}) {
		t.Fatalf("identity = %v; sin Identity Token no se intenta el logout-all", f.identity.calls)
	}
}

// ---------------------------------------------------------------------------
// R-U12 · fallo parcial del logout-all
// ---------------------------------------------------------------------------

// TestLogoutAll_PartialFailureClosesRotatedSession es el invariante cruzado R-U12: si el
// logout-all falla tras rotar, se cierra la sesión rotada con el refresh NUEVO y el error es el
// del logout-all. La mitigación no depende del logger.
func TestLogoutAll_PartialFailureClosesRotatedSession(t *testing.T) {
	for name, log := range map[string]sharedlogger.Logger{"with_logger": quietLogger(), "without_logger": nil} {
		t.Run(name, func(t *testing.T) {
			f := newDelegatedFixture(t, log)
			f.identity.logoutAllErr = domain.ErrIdentityUnavailable
			err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true})
			if !errors.Is(err, domain.ErrIdentityUnavailable) {
				t.Fatalf("err = %v; quiere el del logout-all (no se disfraza de éxito)", err)
			}
			if !slices.Equal(f.identity.calls, []string{"refresh", "logout-all", "logout"}) {
				t.Fatalf("identity = %v; quiere [refresh logout-all logout]", f.identity.calls)
			}
			if f.identity.lastRefresh != rotatedRefresh {
				t.Errorf("el cierre usó %q; quiere el refresh rotado", f.identity.lastRefresh)
			}
		})
	}
}

func TestLogoutAll_TotalFailureStillReturnsLogoutAllError(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	f.identity.logoutAllErr = domain.ErrIdentityUnavailable
	f.identity.logoutErr = domain.ErrRefreshInvalid
	err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true})
	if !errors.Is(err, domain.ErrIdentityUnavailable) || errors.Is(err, domain.ErrRefreshInvalid) {
		t.Fatalf("err = %v; quiere el del logout-all y no el del cierre best-effort", err)
	}
	if len(f.identity.calls) != 3 {
		t.Fatalf("identity = %v; quiere los tres intentos", f.identity.calls)
	}
}

// El rastro cuenta el estado real y nombra el refresh rotado truncado, nunca entero.
func TestLogoutAll_LogNamesStateWithTruncatedToken(t *testing.T) {
	cases := []struct {
		name      string
		logoutErr error
		want      string
	}{
		{"rotated_closed", nil, "solo se cerró la sesión rotada"},
		{"rotated_not_closed", domain.ErrRefreshInvalid, "tampoco se pudo cerrar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			f := newDelegatedFixture(t, sharedlogger.New(sharedlogger.WithWriter(&buf)))
			f.identity.logoutAllErr = domain.ErrIdentityUnavailable
			f.identity.logoutErr = c.logoutErr
			err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true})
			if !errors.Is(err, domain.ErrIdentityUnavailable) {
				t.Fatalf("err = %v; quiere el del logout-all", err)
			}
			written := buf.String()
			if !strings.Contains(written, c.want) {
				t.Fatalf("el rastro tiene que decir %q y dice: %s", c.want, written)
			}
			if !strings.Contains(written, rotatedRefresh[:12]) || strings.Contains(written, rotatedRefresh) {
				t.Fatalf("el rastro tiene que llevar el refresh truncado a 12 y nunca entero: %s", written)
			}
		})
	}
}

// truncateSecret (auxiliar nuevo del verde, E-4/P6): nunca deja ver más de 12 caracteres, y un
// token que no pasa de 12 no deja ver ninguno (su prefijo sería el token entero).
func TestTruncateSecret_NeverRevealsMoreThanThePrefix(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"long_token_keeps_12_chars", rotatedRefresh, rotatedRefresh[:12] + "…"},
		{"exactly_12_reveals_nothing", "abcdefghijkl", "…"},
		{"short_reveals_nothing", "abc", "…"},
		{"empty_reveals_nothing", "", "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncateSecret(c.token); got != c.want {
				t.Fatalf("truncateSecret(%q) = %q; quiere %q", c.token, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-U13 · verify sin identity
// ---------------------------------------------------------------------------

func TestDelegatedVerify_ValidatesContextTokensWithoutIdentity(t *testing.T) {
	f := newDelegatedFixture(t, quietLogger())
	res, err := f.svc.Verify(context.Background(), mustToken(t))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid || res.Subject != "user-1" || res.TenantID != testTenant {
		t.Fatalf("Verify = %+v; quiere válido para user-1 en %q", res, testTenant)
	}
	bad, err := f.svc.Verify(context.Background(), "not-a-token")
	if err != nil || bad.Valid {
		t.Fatalf("Verify(inválido) = %+v, %v; quiere Valid=false sin error", bad, err)
	}
	if len(f.identity.calls) != 0 {
		t.Errorf("Verify llamó a identity: %v", f.identity.calls)
	}
}
