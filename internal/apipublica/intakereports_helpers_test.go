package apipublica_test

// intakereports_helpers_test.go — los dobles y auxiliares (report…) que comparten los tests de
// G9, G10 y del montaje: intakereports_test.go, export_test.go y summary_test.go. Los de G7
// (quote…) viven en quotesuggestion_test.go.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	reportPerm     = "intakes.read"
	reportTooLarge = "el filtro abarca más de 5000 solicitudes: acótalo con from/to"
)

// reportNow es el reloj inyectado de los tests: un instante fijo, en una zona que NO es UTC para
// que la normalización se vea.
func reportNow() time.Time {
	return time.Date(2026, 10, 8, 6, 30, 5, 0, time.FixedZone("-03", -3*3600))
}

// reportServiceSpy es IntakeReportService: devuelve lo sembrado o err y apunta lo que recibió,
// incluido el plazo de su contexto (-1 = sin plazo).
type reportServiceSpy struct {
	details   []intakes.Detail
	summary   intakes.Summary
	err       error
	calls     int
	tenant    string
	filter    intakes.Filter
	remaining time.Duration
}

var _ apipublica.IntakeReportService = (*reportServiceSpy)(nil)

func (s *reportServiceSpy) note(ctx context.Context, tenantID string, f intakes.Filter) {
	s.calls++
	s.tenant, s.filter, s.remaining = tenantID, f, -1
	if dl, ok := ctx.Deadline(); ok {
		s.remaining = time.Until(dl)
	}
}

func (s *reportServiceSpy) ListDetails(ctx context.Context, tenantID string, f intakes.Filter) ([]intakes.Detail, error) {
	s.note(ctx, tenantID, f)
	return s.details, s.err
}

func (s *reportServiceSpy) Summary(ctx context.Context, tenantID string, f intakes.Filter) (intakes.Summary, error) {
	s.note(ctx, tenantID, f)
	return s.summary, s.err
}

// reportDeps son las dependencias de G9 y G10 con `intakes_export` encendida y el reloj fijo.
func reportDeps(svc apipublica.IntakeReportService) apipublica.IntakeReportsDeps {
	return apipublica.IntakeReportsDeps{
		Intakes:      svc,
		Entitlements: withFeatures(entitlements.FeatureIntakesExport),
		Now:          reportNow,
	}
}

// reportCara monta G7, G9 y G10 con k y d.
func reportCara(k apipublica.Common, d apipublica.IntakeReportsDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountIntakeReports(c, k, d)
	return c
}

// reportGet hace un GET como tenantA con el permiso de lectura.
func reportGet(t *testing.T, d apipublica.IntakeReportsDeps, target string) *httptest.ResponseRecorder {
	t.Helper()
	h := apipublicahelpertest.New(t)
	return h.Call(reportCara(h.Common(), d), h.With(tenantA, reportPerm), http.MethodGet, target, "")
}
