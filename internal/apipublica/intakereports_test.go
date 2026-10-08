//go:build pendiente

package apipublica_test

// intakereports_test.go — cubre el contrato de intakereports.go (IntakeReportService,
// IntakeReportsDeps, MountIntakeReports): qué se monta y con qué cadena y gates. El
// comportamiento de cada ruta lo cubren export_test.go (G9), summary_test.go (G10) y
// quotesuggestion_test.go (G7).
//
// Aquí viven también los dobles y auxiliares que comparten esos tres (report…).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

const (
	reportPerm = "intakes.read"

	exportTarget  = "/api/v1/intakes/export"
	summaryTarget = "/api/v1/intakes/summary.json"
	quoteIntakeID = "33333333-3333-4333-8333-333333333333"
	quoteTarget   = "/api/v1/intakes/" + quoteIntakeID + "/quote-suggestion"

	patternExport  = "GET /api/v1/intakes/export"
	patternSummary = "GET /api/v1/intakes/summary.json"
	patternQuote   = "POST /api/v1/intakes/{id}/quote-suggestion"

	reportExportDenied = `{"error":"feature_not_enabled","feature":"intakes_export"}`
	reportCartDenied   = `{"error":"feature_not_enabled","feature":"cart_basic"}`
	reportLLMDenied    = `{"error":"feature_not_enabled","feature":"llm_intake"}`
	reportTooLarge     = "el filtro abarca más de 5000 solicitudes: acótalo con from/to"

	// reportQuoteDeadline es un plazo cualquiera, distinto del de producción a propósito: la
	// cara no conoce ninguna cifra, usa la que le cablean.
	reportQuoteDeadline = 37 * time.Second
)

// Los puertos los cumplen las piezas REALES del módulo solicitudes nuevo: sin estas líneas, los
// dobles de abajo probarían un contrato que nadie implementa.
var (
	_ apipublica.IntakeReportService = (*intakes.Service)(nil)
	_ apipublica.QuoteSuggester      = (*quotetext.Service)(nil)
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

// quoteSuggesterSpy es QuoteSuggester: devuelve out o err y apunta lo que recibió, el plazo de
// su contexto (-1 = sin plazo) y lo que diga probe en el momento de la llamada.
type quoteSuggesterSpy struct {
	out       quotetext.Suggestion
	err       error
	calls     int
	tenant    string
	intake    string
	remaining time.Duration
	probe     func() int
	probed    int
}

var _ apipublica.QuoteSuggester = (*quoteSuggesterSpy)(nil)

func (s *quoteSuggesterSpy) Suggest(ctx context.Context, tenantID, intakeID string) (quotetext.Suggestion, error) {
	s.calls++
	s.tenant, s.intake, s.remaining = tenantID, intakeID, -1
	if dl, ok := ctx.Deadline(); ok {
		s.remaining = time.Until(dl)
	}
	if s.probe != nil {
		s.probed = s.probe()
	}
	return s.out, s.err
}

// reportDeps son las dependencias de G9 y G10 con `intakes_export` encendida y el reloj fijo.
func reportDeps(svc apipublica.IntakeReportService) apipublica.IntakeReportsDeps {
	return apipublica.IntakeReportsDeps{
		Intakes:      svc,
		Entitlements: withFeatures(entitlements.FeatureIntakesExport),
		Now:          reportNow,
	}
}

// quoteDeps son las dependencias de G7 (y de G9/G10) con las tres features encendidas.
func quoteDeps(suggester apipublica.QuoteSuggester) apipublica.IntakeReportsDeps {
	return apipublica.IntakeReportsDeps{
		Intakes: &reportServiceSpy{},
		Entitlements: withFeatures(entitlements.FeatureIntakesExport,
			entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake),
		QuoteSuggestions:   suggester,
		QuoteWriteDeadline: reportQuoteDeadline,
		Now:                reportNow,
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

func TestMountIntakeReports_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := reportCara(h.Common(), quoteDeps(&quoteSuggesterSpy{}))
	wantPatterns(t, "G7, G9 y G10", cara, []string{patternExport, patternSummary, patternQuote})
	checkChain(t, h, cara, routeCase{id: "G9", method: http.MethodGet, target: exportTarget, perm: reportPerm, want: http.StatusOK})
	checkChain(t, h, cara, routeCase{id: "G10", method: http.MethodGet, target: summaryTarget, perm: reportPerm, want: http.StatusOK})
	// G7 es POST y aun así es R: no escribe nada y no deja registro de auditoría.
	checkChain(t, h, cara, routeCase{id: "G7", method: http.MethodPost, target: quoteTarget, perm: reportPerm, want: http.StatusOK})
}

// TestMountIntakeReports_NothingWithoutServiceOrResolver: sin el puerto o sin el resolver no
// existe NINGUNA de las tres (404 de ruta inexistente, T-11), aunque haya generador.
func TestMountIntakeReports_NothingWithoutServiceOrResolver(t *testing.T) {
	with := func(mutate func(*apipublica.IntakeReportsDeps)) apipublica.IntakeReportsDeps {
		d := quoteDeps(&quoteSuggesterSpy{})
		mutate(&d)
		return d
	}
	for name, d := range map[string]apipublica.IntakeReportsDeps{
		"without_service":  with(func(d *apipublica.IntakeReportsDeps) { d.Intakes = nil }),
		"without_resolver": with(func(d *apipublica.IntakeReportsDeps) { d.Entitlements = nil }),
		"without_both":     with(func(d *apipublica.IntakeReportsDeps) { d.Intakes, d.Entitlements = nil, nil }),
		"empty":            {},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := reportCara(h.Common(), d)
			wantPatterns(t, name, cara, nil)
			token := h.With(tenantA, reportPerm)
			wantCode(t, name+" G9", h.Call(cara, token, http.MethodGet, exportTarget, ""), http.StatusNotFound)
			wantCode(t, name+" G10", h.Call(cara, token, http.MethodGet, summaryTarget, ""), http.StatusNotFound)
			wantCode(t, name+" G7", h.Call(cara, token, http.MethodPost, quoteTarget, ""), http.StatusNotFound)
		})
	}
}

// TestMountIntakeReports_QuoteNeedsItsSuggester: sin generador G7 no existe (404), y eso no deja
// a G9 ni a G10 sin montar. Un plazo de escritura cero no es un error si no hay generador.
func TestMountIntakeReports_QuoteNeedsItsSuggester(t *testing.T) {
	h := apipublicahelpertest.New(t)
	d := quoteDeps(nil)
	d.QuoteSuggestions, d.QuoteWriteDeadline = nil, 0
	cara := reportCara(h.Common(), d)
	wantPatterns(t, "sin generador", cara, []string{patternExport, patternSummary})
	token := h.With(tenantA, reportPerm)
	wantCode(t, "G7 sin generador", h.Call(cara, token, http.MethodPost, quoteTarget, ""), http.StatusNotFound)
	wantCode(t, "G9 sin generador", h.Call(cara, token, http.MethodGet, exportTarget, ""), http.StatusOK)
	wantCode(t, "G10 sin generador", h.Call(cara, token, http.MethodGet, summaryTarget, ""), http.StatusOK)
}

func TestMountIntakeReports_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountIntakeReports(apipublica.Nueva(), apipublica.Common{}, reportDeps(&reportServiceSpy{}))
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountIntakeReports") {
		t.Errorf("MountIntakeReports con MW nil: panic = %v; quiero un panic de cableado que nombre MountIntakeReports", v)
	}
}

// TestMountIntakeReports_QuoteWithoutWriteDeadlinePanicsAtMount: con generador y sin plazo de
// escritura no hay valor sano que poner; es un fallo de cableado y se dice al montar.
func TestMountIntakeReports_QuoteWithoutWriteDeadlinePanicsAtMount(t *testing.T) {
	for name, deadline := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			d := quoteDeps(&quoteSuggesterSpy{})
			d.QuoteWriteDeadline = deadline
			v := recuperar(func() { apipublica.MountIntakeReports(apipublica.Nueva(), h.Common(), d) })
			msg := fmt.Sprint(v)
			if v == nil || esPendiente(v) || !strings.Contains(msg, "MountIntakeReports") || !strings.Contains(msg, "QuoteWriteDeadline") {
				t.Errorf("plazo %s: panic = %v; quiero un panic de cableado que nombre MountIntakeReports y QuoteWriteDeadline", deadline, v)
			}
		})
	}
}

// TestMountIntakeReports_ExportGate: G9 y G10 llevan el gate `intakes_export`. Tener la bandeja
// (`cart_basic`) no lo abre, un resolver caído corta igual (fail-closed), el puerto ni se
// consulta, y sin el permiso el 403 es el del permiso aunque tampoco haya feature.
func TestMountIntakeReports_ExportGate(t *testing.T) {
	down := entitlementshelpertest.NewFake()
	down.Err = errors.New("bd caída")
	cases := []struct {
		name     string
		resolver entitlements.Resolver
		grant    string
		code     int
		body     string
	}{
		{"feature_on", withFeatures(entitlements.FeatureIntakesExport), reportPerm, http.StatusOK, ""},
		{"cart_basic_does_not_open_it", withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake), reportPerm, http.StatusForbidden, reportExportDenied},
		{"no_features", withFeatures(), reportPerm, http.StatusForbidden, reportExportDenied},
		{"resolver_down_fails_closed", down, reportPerm, http.StatusForbidden, reportExportDenied},
		{"permission_goes_before_the_feature", withFeatures(), "otra.cosa", http.StatusForbidden, `{"error":"permiso denegado"}`},
	}
	for _, tc := range cases {
		for _, target := range []string{exportTarget, summaryTarget} {
			t.Run(tc.name+target, func(t *testing.T) {
				h := apipublicahelpertest.New(t)
				svc := &reportServiceSpy{}
				d := reportDeps(svc)
				d.Entitlements = tc.resolver
				rec := h.Call(reportCara(h.Common(), d), h.With(tenantA, tc.grant), http.MethodGet, target, "")
				wantCode(t, tc.name, rec, tc.code)
				if tc.body != "" {
					wantExactBody(t, tc.name, rec, tc.body)
				}
				if wantCalls := map[bool]int{true: 1, false: 0}[tc.code == http.StatusOK]; svc.calls != wantCalls {
					t.Errorf("%s: el puerto recibió %d llamadas, quiero %d", tc.name, svc.calls, wantCalls)
				}
				if n := len(h.Auditor().Records()); n != 0 {
					t.Errorf("%s: es R y dejó %d registros de auditoría, quiero 0", tc.name, n)
				}
			})
		}
	}
}

// TestMountIntakeReports_QuoteGate: G7 pide `cart_basic` Y `llm_intake`, en ese orden; es la
// única ruta de la bandeja que cobra el nivel LLM. `intakes_export` no pinta nada aquí.
func TestMountIntakeReports_QuoteGate(t *testing.T) {
	down := entitlementshelpertest.NewFake()
	down.Err = errors.New("bd caída")
	cases := []struct {
		name     string
		resolver entitlements.Resolver
		grant    string
		code     int
		body     string
	}{
		{"both_features", withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake), reportPerm, http.StatusOK, ""},
		{"without_llm_intake", withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureIntakesExport), reportPerm, http.StatusForbidden, reportLLMDenied},
		{"without_cart_basic", withFeatures(entitlements.FeatureLLMIntake, entitlements.FeatureIntakesExport), reportPerm, http.StatusForbidden, reportCartDenied},
		{"no_features_names_cart_basic_first", withFeatures(), reportPerm, http.StatusForbidden, reportCartDenied},
		{"resolver_down_fails_closed", down, reportPerm, http.StatusForbidden, reportCartDenied},
		{"permission_goes_before_the_features", withFeatures(), "otra.cosa", http.StatusForbidden, `{"error":"permiso denegado"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			suggester := &quoteSuggesterSpy{out: quotetext.Suggestion{Text: "Hola", Source: quotetext.SourceLLM}}
			d := quoteDeps(suggester)
			d.Entitlements = tc.resolver
			rec := h.Call(reportCara(h.Common(), d), h.With(tenantA, tc.grant), http.MethodPost, quoteTarget, "")
			wantCode(t, tc.name, rec, tc.code)
			if tc.body != "" {
				wantExactBody(t, tc.name, rec, tc.body)
			}
			if wantCalls := map[bool]int{true: 1, false: 0}[tc.code == http.StatusOK]; suggester.calls != wantCalls {
				t.Errorf("%s: el generador recibió %d llamadas, quiero %d", tc.name, suggester.calls, wantCalls)
			}
			if n := len(h.Auditor().Records()); n != 0 {
				t.Errorf("%s: G7 es R y dejó %d registros de auditoría, quiero 0", tc.name, n)
			}
		})
	}
}
