package usecase_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// spyIdentity es el doble de identity-api: registra las llamadas que recibe para
// poder afirmar sobre la COREOGRAFÍA de la delegación (qué se llama y en qué
// orden), que es justo lo que el relé aporta.
type spyIdentity struct {
	calls []string

	loginSystem  string
	loginEmail   string
	lastRefresh  string
	lastBearer   string
	loginErr     error
	refreshErr   error
	logoutErr    error
	logoutAllErr error

	session domain.IdentitySession
}

func (s *spyIdentity) Login(_ context.Context, email, _, system string) (domain.IdentitySession, error) {
	s.calls = append(s.calls, "login")
	s.loginEmail, s.loginSystem = email, system
	if s.loginErr != nil {
		return domain.IdentitySession{}, s.loginErr
	}
	return s.session, nil
}

func (s *spyIdentity) Refresh(_ context.Context, refreshToken string) (domain.IdentitySession, error) {
	s.calls = append(s.calls, "refresh")
	s.lastRefresh = refreshToken
	if s.refreshErr != nil {
		return domain.IdentitySession{}, s.refreshErr
	}
	return s.session, nil
}

func (s *spyIdentity) Logout(_ context.Context, refreshToken string) error {
	s.calls = append(s.calls, "logout")
	s.lastRefresh = refreshToken
	return s.logoutErr
}

func (s *spyIdentity) LogoutAll(_ context.Context, identityToken string) error {
	s.calls = append(s.calls, "logout-all")
	s.lastBearer = identityToken
	return s.logoutAllErr
}

// spyExchanger es el doble del canje: devuelve un Context Token fijo y recuerda
// qué Identity Token le entregaron.
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

// quietLogger es un logger real que no escribe: el delegado registra el estado
// a medias de un fallo parcial y no queremos ese ruido en la salida del test.
func quietLogger() sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(io.Discard))
}

type delegatedFixture struct {
	svc      *usecase.DelegatedAuthService
	identity *spyIdentity
	exchange *spyExchanger
}

func newDelegatedFixture(t *testing.T) delegatedFixture {
	t.Helper()
	//nolint:gosec // no son credenciales: son los tokens de mentira del doble de identity
	identity := &spyIdentity{session: domain.IdentitySession{
		SessionID:     "sess-1",
		IdentityToken: "identity.token.firmado",
		RefreshToken:  "rft_de_identity",
		ExpiresAt:     time.Now().Add(15 * time.Minute),
	}}
	exchange := &spyExchanger{res: in.ExchangeResult{
		ContextToken: "context.token.de.wapp",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Context:      domain.IdentityContext{TenantID: testTenant, UserID: "user-1", Roles: []string{"operator"}},
	}}
	validator := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	svc, err := usecase.NewDelegatedAuthService(identity, exchange, validator, usecase.SystemWappEdge, quietLogger())
	if err != nil {
		t.Fatalf("NewDelegatedAuthService: %v", err)
	}
	return delegatedFixture{svc: svc, identity: identity, exchange: exchange}
}

func TestDelegatedLogin_ValidaEnIdentityYCanjeaEnWapp(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)

	res, err := f.svc.Login(context.Background(), in.LoginInput{
		Email: testEmail, Password: testLoginPhrase, TenantID: testTenant,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	// La coreografía completa: credenciales a identity, identidad al canje.
	if len(f.identity.calls) != 1 || f.identity.calls[0] != "login" {
		t.Fatalf("llamadas a identity = %v, want [login]", f.identity.calls)
	}
	if f.identity.loginSystem != usecase.SystemWappEdge {
		t.Errorf("system = %q, want %q (el relé del Edge)", f.identity.loginSystem, usecase.SystemWappEdge)
	}
	if len(f.exchange.seen) != 1 || f.exchange.seen[0] != "identity.token.firmado" {
		t.Fatalf("el canje recibió %v", f.exchange.seen)
	}
	// El access que sale es el Context Token de wApp; el refresh, el de identity.
	if res.AccessToken != "context.token.de.wapp" {
		t.Errorf("access = %q, want el context token", res.AccessToken)
	}
	if res.RefreshToken != "rft_de_identity" {
		t.Errorf("refresh = %q, want el de identity", res.RefreshToken)
	}
	if res.Context.TenantID != testTenant {
		t.Errorf("tenant = %q, want %q (lo resuelve wApp, no identity)", res.Context.TenantID, testTenant)
	}
	// El Identity Token NO viaja al cliente: vive solo el instante del canje.
	if res.AccessToken == "identity.token.firmado" || res.RefreshToken == "identity.token.firmado" {
		t.Error("el identity token se filtró al resultado")
	}
	if res.ExpiresAt.Unix() != f.exchange.res.ExpiresAt.Unix() {
		t.Error("la expiración devuelta debe ser la del context token, ya acotada por el identity token")
	}
}

func TestDelegatedRefresh_RotaEnIdentityYRecanjea(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)

	if _, err := f.svc.Refresh(context.Background(), in.RefreshInput{RefreshToken: "rft_presentado"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(f.identity.calls) != 1 || f.identity.calls[0] != "refresh" {
		t.Fatalf("llamadas a identity = %v, want [refresh]", f.identity.calls)
	}
	if f.identity.lastRefresh != "rft_presentado" {
		t.Errorf("refresh presentado = %q", f.identity.lastRefresh)
	}
	// Re-canje: los grants se resuelven otra vez, así que un cambio de rol entra
	// sin que la persona vuelva a escribir su contraseña.
	if len(f.exchange.seen) != 1 {
		t.Fatalf("el refresh debe re-canjear, canjes = %d", len(f.exchange.seen))
	}
}

func TestDelegatedLogout_CierraSoloLaSesionDeEstaAplicacion(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)

	if err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado"}); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	// Modelo Google: cerrar la consola del Edge no cierra la web.
	if len(f.identity.calls) != 1 || f.identity.calls[0] != "logout" {
		t.Fatalf("llamadas a identity = %v, want [logout]", f.identity.calls)
	}
}

func TestDelegatedLogout_AllSessionsRevocaTodoSinTransportarUserID(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)

	if err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true}); err != nil {
		t.Fatalf("Logout(all): %v", err)
	}
	// El titular sale de un Identity Token que se obtiene rotando el refresh
	// presentado: el proto del Edge NO necesita ganar un user_id.
	if len(f.identity.calls) != 2 || f.identity.calls[0] != "refresh" || f.identity.calls[1] != "logout-all" {
		t.Fatalf("llamadas a identity = %v, want [refresh logout-all]", f.identity.calls)
	}
	if f.identity.lastBearer != "identity.token.firmado" {
		t.Errorf("bearer del logout-all = %q, want el identity token recién obtenido", f.identity.lastBearer)
	}
}

func TestDelegatedLogout_AllSessionsConRefreshMuertoNoRevocaNada(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)
	f.identity.refreshErr = domain.ErrRefreshInvalid

	//nolint:gosec // no es una credencial: es un refresh de mentira ya revocado
	err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_quemado", AllSessions: true})
	if !errors.Is(err, domain.ErrRefreshInvalid) {
		t.Fatalf("err = %v, want ErrRefreshInvalid", err)
	}
	if len(f.identity.calls) != 1 {
		t.Fatalf("no debe intentarse el logout-all sin token: %v", f.identity.calls)
	}
}

func TestDelegatedLogin_PropagaElRechazoDeIdentitySinCanjear(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "credenciales inválidas", err: domain.ErrInvalidCredentials},
		{name: "system gate denegado", err: domain.ErrUserInactive},
		{name: "identity caído", err: domain.ErrIdentityUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDelegatedFixture(t)
			f.identity.loginErr = tt.err

			_, err := f.svc.Login(context.Background(), in.LoginInput{Email: testEmail, Password: testLoginPhrase})
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if len(f.exchange.seen) != 0 {
				t.Error("no se canjea nada cuando identity rechaza el login")
			}
		})
	}
}

func TestDelegatedLogin_UnSujetoSinMigrarNoEntra(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)
	f.exchange.err = domain.ErrUserNotMigrated

	_, err := f.svc.Login(context.Background(), in.LoginInput{Email: testEmail, Password: testLoginPhrase})
	if !errors.Is(err, domain.ErrUserNotMigrated) {
		t.Fatalf("err = %v, want ErrUserNotMigrated", err)
	}
}

func TestDelegatedVerify_ValidaContextTokensSinSalirAIdentity(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)
	// Un Context Token emitido por wApp: el token que se mira lo firmó wApp, así
	// que verificarlo no necesita a identity.
	issuer := sharedjwt.NewJWTManager(testSigningKey, testIssuer)
	token, _, err := issuer.GenerateToken("user-1", testTenant, []string{"operator"}, sharedjwt.Grants{}, 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	res, err := f.svc.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid || res.Subject != "user-1" || res.TenantID != testTenant {
		t.Fatalf("verify = %+v", res)
	}
	if len(f.identity.calls) != 0 {
		t.Errorf("Verify no debe llamar a identity: %v", f.identity.calls)
	}
}

func TestNewDelegatedAuthService_ExigeUnaAplicacionDeWapp(t *testing.T) {
	t.Parallel()
	identity := &spyIdentity{}
	exchange := &spyExchanger{}
	validator := sharedjwt.NewJWTManager(testSigningKey, testIssuer)

	for _, system := range []string{"", "edugo.kmp", "wapp"} {
		if _, err := usecase.NewDelegatedAuthService(identity, exchange, validator, system, nil); err == nil {
			t.Errorf("system %q debería rechazarse", system)
		}
	}
	for _, system := range []string{usecase.SystemWappBFF, usecase.SystemWappEdge} {
		if _, err := usecase.NewDelegatedAuthService(identity, exchange, validator, system, nil); err != nil {
			t.Errorf("system %q: %v", system, err)
		}
	}
	if _, err := usecase.NewDelegatedAuthService(nil, exchange, validator, usecase.SystemWappEdge, nil); err == nil {
		t.Error("sin cliente de identity debería fallar")
	}
	if _, err := usecase.NewDelegatedAuthService(identity, nil, validator, usecase.SystemWappEdge, nil); err == nil {
		t.Error("sin canje debería fallar")
	}
}

// TestDelegatedLogout_AllSessionsConFalloParcialCierraLaSesionRotada cubre la
// ventana que abre la cadena refresh→logout-all: el refresh presentado ya se
// consumió en la rotación, así que si el logout-all falla ahí, quien pidió el
// logout se queda sin credencial para reintentar y con todas las sesiones
// vivas. Lo único que aún se puede cerrar es la sesión recién rotada, y se
// cierra.
func TestDelegatedLogout_AllSessionsConFalloParcialCierraLaSesionRotada(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)
	f.identity.logoutAllErr = domain.ErrIdentityUnavailable

	err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true})

	// El fallo NO se disfraza de éxito: una revocación global que no ocurrió
	// tiene que verse, o la persona cree que cerró todo sin cerrar nada.
	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, want el error del logout-all", err)
	}
	// Y la mitigación: se intentó cerrar la sesión rotada, con el refresh NUEVO
	// (el presentado ya no vale).
	if len(f.identity.calls) != 3 || f.identity.calls[2] != "logout" {
		t.Fatalf("llamadas a identity = %v, want [refresh logout-all logout]", f.identity.calls)
	}
	if f.identity.lastRefresh != "rft_de_identity" {
		t.Errorf("el cierre usó %q, want el refresh rotado", f.identity.lastRefresh)
	}
}

// TestDelegatedLogout_FalloParcialTotalSigueDevolviendoElErrorDelUsuario: si ni
// siquiera la sesión rotada se puede cerrar, el error que sale es el del
// logout-all —el que describe lo que se pidió y no se consiguió—, no el del
// intento de mitigación.
func TestDelegatedLogout_FalloParcialTotalSigueDevolviendoElErrorDelUsuario(t *testing.T) {
	t.Parallel()
	f := newDelegatedFixture(t)
	f.identity.logoutAllErr = domain.ErrIdentityUnavailable
	f.identity.logoutErr = domain.ErrRefreshInvalid

	err := f.svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true})
	if !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, want el error del logout-all, no el del cierre best-effort", err)
	}
	if len(f.identity.calls) != 3 {
		t.Fatalf("llamadas a identity = %v, want los tres intentos", f.identity.calls)
	}
}

// TestDelegatedLogout_LaMitigacionNoDependeDelLogger: el cierre de la sesión
// rotada es la mitigación, no la traza. Un despliegue sin logger no puede
// quedarse sin ella.
func TestDelegatedLogout_LaMitigacionNoDependeDelLogger(t *testing.T) {
	t.Parallel()
	identity := &spyIdentity{session: domain.IdentitySession{
		IdentityToken: "identity.token.firmado",
		RefreshToken:  "rft_rotado",
		ExpiresAt:     time.Now().Add(15 * time.Minute),
	}}
	identity.logoutAllErr = domain.ErrIdentityUnavailable
	svc, err := usecase.NewDelegatedAuthService(
		identity, &spyExchanger{}, sharedjwt.NewJWTManager(testSigningKey, testIssuer), usecase.SystemWappEdge, nil,
	)
	if err != nil {
		t.Fatalf("NewDelegatedAuthService: %v", err)
	}

	if lerr := svc.Logout(context.Background(), in.LogoutInput{RefreshToken: "rft_presentado", AllSessions: true}); lerr == nil {
		t.Fatal("el fallo del logout-all debía propagarse")
	}
	if len(identity.calls) != 3 || identity.calls[2] != "logout" {
		t.Fatalf("sin logger la mitigación desapareció: %v", identity.calls)
	}
}
