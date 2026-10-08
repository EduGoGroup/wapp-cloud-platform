package apipublica

// intakes_status_internal_test.go — lo que de intakes_status.go no se alcanza desde una ruta
// montada (05 E-4, P6: nace con el verde): la defensa de los dos handlers ante una petición SIN
// identidad con empresa. Por la cadena no llega nunca —RequirePermission corta antes—, así que
// se prueba llamando al handler a pelo. Aquí viven además el doble y el auxiliar que usan los
// demás tests internos de la bandeja.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// intakeCountingService es un IntakeService que contesta el cero y cuenta las llamadas.
type intakeCountingService struct{ calls int }

var _ IntakeService = (*intakeCountingService)(nil)

func (s *intakeCountingService) List(context.Context, string, intakes.Filter) (intakes.Page, error) {
	s.calls++
	return intakes.Page{}, nil
}

func (s *intakeCountingService) Get(context.Context, string, string) (intakes.Detail, error) {
	s.calls++
	return intakes.Detail{}, nil
}

func (s *intakeCountingService) SetStatus(context.Context, string, string, string, intakes.StatusNotice) (intakes.Intake, error) {
	s.calls++
	return intakes.Intake{}, nil
}

func (s *intakeCountingService) Discard(context.Context, string, []string) (intakes.DiscardResult, error) {
	s.calls++
	return intakes.DiscardResult{}, nil
}

func (s *intakeCountingService) ReplaceItems(context.Context, string, string, []intakes.Item, intakes.EditMode) (intakes.Detail, error) {
	s.calls++
	return intakes.Detail{}, nil
}

func (s *intakeCountingService) Approve(context.Context, string, string, string) (intakes.Detail, error) {
	s.calls++
	return intakes.Detail{}, nil
}

func (s *intakeCountingService) RequestInfo(context.Context, string, string, string) (intakes.Detail, error) {
	s.calls++
	return intakes.Detail{}, nil
}

// intakeFixedClock es el reloj de los tests internos.
func intakeFixedClock() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }

// intakeWantIdentityDefense llama al handler SIN pasar por la cadena, con un cuerpo válido, y
// exige que sin identidad —o con una sin empresa— responda el 401 de siempre sin tocar el
// servicio, y que con una identidad con empresa sí lo llame.
func intakeWantIdentityDefense(t *testing.T, what string, svc *intakeCountingService, h http.Handler, method, body string) {
	t.Helper()
	serve := func(ctx context.Context) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/", strings.NewReader(body)).WithContext(ctx))
		return rec
	}
	for name, ctx := range map[string]context.Context{
		"sin identidad":             context.Background(),
		"con identidad sin empresa": httpapi.WithIdentity(context.Background(), httpapi.Identity{Subject: "persona"}),
	} {
		rec := serve(ctx)
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != `{"error":"autenticación requerida"}` {
			t.Errorf("%s %s: %d %s; quiero 401 {\"error\":\"autenticación requerida\"}", what, name, rec.Code, rec.Body)
		}
		if svc.calls != 0 {
			t.Fatalf("%s %s: el servicio recibió %d llamadas, quiero 0", what, name, svc.calls)
		}
	}
	rec := serve(httpapi.WithIdentity(context.Background(), httpapi.Identity{TenantID: "tenant-x", Subject: "persona"}))
	if rec.Code != http.StatusOK || svc.calls != 1 {
		t.Errorf("%s con identidad: código %d y %d llamadas al servicio; quiero 200 y 1 (%s)", what, rec.Code, svc.calls, rec.Body)
	}
}

func TestIntakeStatusHandlers_IdentityDefense(t *testing.T) {
	svc := &intakeCountingService{}
	intakeWantIdentityDefense(t, "G3", svc, intakeSetStatusHandler(svc, intakeFixedClock), http.MethodPost, `{"status":"confirmed"}`)
	svc = &intakeCountingService{}
	intakeWantIdentityDefense(t, "G8", svc, intakeDiscardHandler(svc), http.MethodPost, `{"intake_ids":["a"]}`)
}
