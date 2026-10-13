package apipublica

// reanalyze_internal_test.go — lo que de reanalyze.go no se alcanza desde una ruta montada (05
// E-4, P6: nace con el verde): la defensa del handler ante una petición SIN identidad con
// empresa. Por la cadena no llega nunca —RequirePermission corta antes—, así que se prueba
// llamando al handler a pelo.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// reanalyzeCountingService es un ReanalysisService que contesta el cero y cuenta las llamadas.
type reanalyzeCountingService struct{ calls int }

var _ ReanalysisService = (*reanalyzeCountingService)(nil)

func (s *reanalyzeCountingService) Reanalyze(context.Context, reanalisis.Request) (reanalisis.Result, error) {
	s.calls++
	return reanalisis.Result{}, nil
}

func TestReanalyzeHandler_IdentityDefense(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"without_identity":        context.Background(),
		"identity_without_tenant": httpapi.WithIdentity(context.Background(), httpapi.Identity{}),
	} {
		t.Run(name, func(t *testing.T) {
			svc := &reanalyzeCountingService{}
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/intakes/x/reanalyze", strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			reanalyzeHandler(svc).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Body.String() != `{"error":"autenticación requerida"}` {
				t.Errorf("código %d y cuerpo %s; quiero 401 {\"error\":\"autenticación requerida\"}", rec.Code, rec.Body.String())
			}
			if svc.calls != 0 {
				t.Errorf("el servicio recibió %d llamadas sin identidad, quiero 0", svc.calls)
			}
		})
	}
}
