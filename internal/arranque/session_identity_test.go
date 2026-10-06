package arranque

import (
	"errors"
	"testing"

	viejosession "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	edgesession "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// TestIdentidad_SessionOfflineIsOneSentinelAcrossBothTrees fija D-F3-2 (T3.27 = FX TX.10): el
// centinela de «sesión offline» del session NUEVO, el del VIEJO y el de platform son LA MISMA
// variable, no tres errores con el mismo texto.
//
// Por qué importa mientras conviven los dos árboles: el gateway nuevo devuelve el centinela de su
// paquete, y quien lo traduce a HTTP sigue siendo código viejo que compara contra el del suyo
// (publicapi/flows.go e internal/flujos/admin/handlers.go, hasta F8; httpapi.SendMessageHandler
// en J15). Si alguien «independizara» el nuevo con su propio errors.New, todo compilaría y una
// sesión offline pasaría de 502 a 500 sin un solo rojo. Este test es ese rojo.
func TestIdentidad_SessionOfflineIsOneSentinelAcrossBothTrees(t *testing.T) {
	if !errors.Is(edgesession.ErrSessionOffline, viejosession.ErrSessionOffline) {
		t.Errorf("el ErrSessionOffline nuevo no casa con el viejo: %v frente a %v",
			edgesession.ErrSessionOffline, viejosession.ErrSessionOffline)
	}
	if !errors.Is(viejosession.ErrSessionOffline, edgesession.ErrSessionOffline) {
		t.Errorf("el ErrSessionOffline viejo no casa con el nuevo")
	}
	// errors.Is sobre dos centinelas pelados ya es identidad, pero se afirma también el ancla:
	// los dos tienen que ser el de platform, que es el que comparan los handlers de :8100.
	if edgesession.ErrSessionOffline != httpapi.ErrSessionOffline { //nolint:errorlint // se afirma identidad, no parentesco
		t.Errorf("el ErrSessionOffline nuevo no es el de platform/httpapi")
	}
	if viejosession.ErrSessionOffline != httpapi.ErrSessionOffline { //nolint:errorlint // ídem
		t.Errorf("el ErrSessionOffline viejo no es el de platform/httpapi")
	}
}
