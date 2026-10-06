package arranque

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
)

// TestCableado_RealAccessServicesSpeakTheSentinelsTheGatewayClassifies sustituye a los dos
// «RealServiceInvalidInputIsOldSentinel» que murieron con bridge_iam.go (hallazgo 69).
//
// Hasta F3 el gateway VIEJO clasificaba los errores del login comparando contra los centinelas
// del iam VIEJO, y el adaptador traducía los del acceso nuevo para que casaran. El gateway NUEVO
// compara directamente contra los de internal/modulos/acceso/iam/domain, con el mismo switch y
// los mismos códigos de cable (lo fija su propio test de tabla, en edge/grpc). El eslabón que
// faltaba es este: que los servicios REALES de acceso, tal como llegan al gateway por las
// costuras del arranque, devuelven ESOS centinelas. Si un día los envolvieran en otro tipo o
// cambiaran de paquete, el operador vería «internal» donde hoy ve «invalid_input» o
// «invalid_credentials», sin ningún rojo en ninguno de los dos módulos.
func TestCableado_RealAccessServicesSpeakTheSentinelsTheGatewayClassifies(t *testing.T) {
	authn := edgeAuthenticatorPort(&usecase.DelegatedAuthService{})
	if _, err := authn.Login(t.Context(), in.LoginInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("Login vacío = %v, se esperaba que casara con el ErrInvalidInput de acceso", err)
	}
	if err := authn.Logout(t.Context(), in.LogoutInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("Logout vacío = %v, se esperaba que casara con el ErrInvalidInput de acceso", err)
	}

	auditor := edgeAuditorPort(&usecase.AuditService{})
	if err := auditor.Record(t.Context(), in.AuditInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("Record sin Action = %v, se esperaba que casara con el ErrInvalidInput de acceso", err)
	}
}
