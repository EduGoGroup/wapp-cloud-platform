package apipublica

// intakes_approve_internal_test.go — lo que de intakes_approve.go no se alcanza desde una ruta
// montada (05 E-4, P6: nace con el verde): la defensa de los dos handlers ante una petición SIN
// identidad con empresa. Importa aquí más que en ninguna otra puerta: son las dos que le escriben
// al cliente. El doble y el auxiliar están en intakes_status_internal_test.go.

import (
	"net/http"
	"testing"
)

func TestIntakeOwnerDoorHandlers_IdentityDefense(t *testing.T) {
	svc := &intakeCountingService{}
	intakeWantIdentityDefense(t, "G5", svc, intakeApproveHandler(svc, &intakeGateResolver{}, intakeFixedClock),
		http.MethodPost, `{"rendered_text":"Son $9.00"}`)
	svc = &intakeCountingService{}
	intakeWantIdentityDefense(t, "G6", svc, intakeRequestInfoHandler(svc, &intakeGateResolver{}, intakeFixedClock),
		http.MethodPost, `{"question":"¿Para cuándo?"}`)
}
