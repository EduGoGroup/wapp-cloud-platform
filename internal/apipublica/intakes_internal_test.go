package apipublica

// intakes_internal_test.go — lo que de intakes.go no se alcanza desde una ruta montada (05 E-4,
// P6: nace con el verde): la defensa de las dos lecturas ante una petición SIN identidad con
// empresa. El doble y el auxiliar están en intakes_status_internal_test.go.

import (
	"net/http"
	"testing"
)

func TestIntakeReadHandlers_IdentityDefense(t *testing.T) {
	svc := &intakeCountingService{}
	intakeWantIdentityDefense(t, "G1", svc, intakeListHandler(svc, intakeFixedClock), http.MethodGet, "")
	svc = &intakeCountingService{}
	intakeWantIdentityDefense(t, "G2", svc, intakeGetHandler(svc, &intakeGateResolver{}, intakeFixedClock), http.MethodGet, "")
}
