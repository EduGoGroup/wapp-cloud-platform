// Copia de internal/bootstrap/arranque/delegated_auth_test.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS.
package arranque

import (
	"io"
	"strings"
	"testing"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// stackForDelegation arma el mínimo authStack que wireDelegatedAuth necesita:
// opcionalmente, un canje ya construido.
func stackForDelegation(withExchange bool) *authStack {
	s := &authStack{}
	if withExchange {
		s.exchangeSvc = &iamusecase.ExchangeService{}
	}
	return s
}

func quietLogger() sharedlogger.Logger {
	return sharedlogger.New(sharedlogger.WithWriter(io.Discard))
}

// TestWireDelegatedAuth_SinURLElReleSeQuedaSinAutenticador: tras la Ola 5 no hay
// IAM local al que caer, así que sin WAPP_IDENTITY_URL el relé del Edge se queda
// SIN autenticador y el gateway responde "auth no disponible".
//
// El nil tiene que ser un nil DE VERDAD, no un puntero nil dentro de una
// interface: el gateway decide comparando contra nil, y el clásico tropiezo de
// Go convertiría ese guard en un nil-pointer dereference en el primer login.
func TestWireDelegatedAuth_SinURLElReleSeQuedaSinAutenticador(t *testing.T) {
	s := stackForDelegation(true)
	if err := s.wireDelegatedAuth(config.AppConfig{}, sharedjwt.NewJWTManager("s", "i"), quietLogger()); err != nil {
		t.Fatalf("wireDelegatedAuth: %v", err)
	}
	if s.edgeAuthSvc != nil {
		t.Error("sin WAPP_IDENTITY_URL no debe construirse el delegado")
	}
	// Lo que recibe el gateway viejo (fase4_gateway.go) es el adaptador de bridge_iam.go
	// alrededor del delegado: sin delegado, un nil de verdad.
	if got := newAuthenticatorBridge(s.edgeAuthSvc); got != nil {
		t.Errorf("newAuthenticatorBridge(edgeAuthSvc) = %T (%v), want un nil de verdad", got, got)
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
	bridge, ok := newAuthenticatorBridge(s.edgeAuthSvc).(*authenticatorBridge)
	if !ok || bridge.next != s.edgeAuthSvc {
		t.Errorf("newAuthenticatorBridge(edgeAuthSvc) no envuelve el delegado: %T", bridge)
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
