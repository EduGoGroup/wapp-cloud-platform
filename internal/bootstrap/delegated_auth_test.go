package bootstrap

import (
	"io"
	"strings"
	"testing"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// stackForDelegation arma el mínimo authStack que wireDelegatedAuth necesita: el
// AuthService local (el de siempre) y, opcionalmente, un canje ya construido.
func stackForDelegation(withExchange bool) *authStack {
	s := &authStack{authSvc: &iamusecase.AuthService{}}
	if withExchange {
		s.exchangeSvc = &iamusecase.ExchangeService{}
	}
	return s
}

func quietLogger() sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(io.Discard))
}

func TestWireDelegatedAuth_SinURLElReleSigueEnLocal(t *testing.T) {
	s := stackForDelegation(true)
	if err := s.wireDelegatedAuth(config.AppConfig{}, sharedjwt.NewJWTManager("s", "i"), quietLogger()); err != nil {
		t.Fatalf("wireDelegatedAuth: %v", err)
	}
	if s.edgeAuthSvc != nil {
		t.Error("sin WAPP_IDENTITY_URL no debe construirse el delegado")
	}
	// Y el gateway sigue atendido por el IAM local, exactamente como hasta ahora.
	if got := s.edgeAuthenticator(); got != s.authSvc {
		t.Errorf("edgeAuthenticator = %T, want el AuthService local", got)
	}
}

func TestWireDelegatedAuth_ConURLElReleDelegaEnIdentity(t *testing.T) {
	s := stackForDelegation(true)
	cfg := config.AppConfig{Identity: config.IdentityConfig{URL: "http://localhost:8200"}}

	if err := s.wireDelegatedAuth(cfg, sharedjwt.NewJWTManager("s", "i"), quietLogger()); err != nil {
		t.Fatalf("wireDelegatedAuth: %v", err)
	}
	if s.edgeAuthSvc == nil {
		t.Fatal("con WAPP_IDENTITY_URL debe construirse el delegado")
	}
	if got := s.edgeAuthenticator(); got != s.edgeAuthSvc {
		t.Errorf("edgeAuthenticator = %T, want el delegado", got)
	}
}

// TestWireDelegatedAuth_DelegarSinPoderVerificarNoArranca: las dos variables son
// ejes distintos de la misma transición y delegar sin verificador es imposible
// —el canje lo necesita para emitir el Context Token—. Esa combinación tiene que
// morir en el arranque, no en el primer login de un operador.
func TestWireDelegatedAuth_DelegarSinPoderVerificarNoArranca(t *testing.T) {
	s := stackForDelegation(false)
	cfg := config.AppConfig{Identity: config.IdentityConfig{URL: "http://localhost:8200"}}

	err := s.wireDelegatedAuth(cfg, sharedjwt.NewJWTManager("s", "i"), quietLogger())
	if err == nil {
		t.Fatal("con URL y sin JWKS el arranque debería fallar")
	}
	// El error nombra las dos variables: es lo que el operador necesita para
	// saber qué le falta.
	if !strings.Contains(err.Error(), "WAPP_IDENTITY_URL") || !strings.Contains(err.Error(), "WAPP_IDENTITY_JWKS_URL") {
		t.Errorf("el error debería nombrar ambas variables: %v", err)
	}
}

func TestWireDelegatedAuth_URLInvalidaNoArranca(t *testing.T) {
	for _, url := range []string{"localhost:8200", "ftp://identity"} {
		s := stackForDelegation(true)
		cfg := config.AppConfig{Identity: config.IdentityConfig{URL: url}}
		if err := s.wireDelegatedAuth(cfg, sharedjwt.NewJWTManager("s", "i"), quietLogger()); err == nil {
			t.Errorf("URL %q debería rechazarse en el arranque", url)
		}
	}
}
