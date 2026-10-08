package apipublica_test

// quotesuggestion_test.go — cubre G7, `POST /api/v1/intakes/{id}/quote-suggestion`, tal como lo
// promete QuoteSuggester (quotesuggestion.go), y el plazo de escritura que le pone
// MountIntakeReports (T-6 de FX). La cadena y los gates están en intakereports_test.go; el
// envoltorio del plazo, a solas, en writedeadline_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

const (
	quoteIntakeID = "33333333-3333-4333-8333-333333333333"
	quoteTarget   = "/api/v1/intakes/" + quoteIntakeID + "/quote-suggestion"

	// reportQuoteDeadline es un plazo cualquiera, distinto del de producción a propósito: la
	// cara no conoce ninguna cifra, usa la que le cablean.
	reportQuoteDeadline = 37 * time.Second

	msgQuoteNotFound = "solicitud no encontrada"
	msgQuoteNoLines  = "la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items"
	msgQuoteFailed   = "no se pudo generar la cotización sugerida"
	msgQuoteDeadline = "no se pudo extender el plazo de escritura de la sugerencia de cotización: " +
		"la respuesta larga volverá a no caber por el cable"
)

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

// quotePost hace el POST de G7 como tenantA con el permiso de lectura.
func quotePost(t *testing.T, suggester apipublica.QuoteSuggester, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	return h, h.Call(reportCara(h.Common(), quoteDeps(suggester)), h.With(tenantA, reportPerm), http.MethodPost, target, body)
}

// TestQuoteSuggestion_Body: el 200 byte a byte. `fallback_reason` solo viaja cuando no fue el
// modelo; el texto llega íntegro (Unicode, saltos de línea) con el escape de JSON de la casa.
func TestQuoteSuggestion_Body(t *testing.T) {
	cases := []struct {
		name string
		out  quotetext.Suggestion
		want string
	}{
		{"llm_has_no_fallback_reason",
			quotetext.Suggestion{Text: "¡Hola! Torta $18.000\nTotal $18.000 🎂", Source: quotetext.SourceLLM},
			`{"rendered_text":"¡Hola! Torta $18.000\nTotal $18.000 🎂","source":"llm"}`},
		{"deterministic_says_why",
			quotetext.Suggestion{Text: "1 x Torta — $18000", Source: quotetext.SourceDeterministic, Reason: quotetext.ReasonNoExamples},
			`{"rendered_text":"1 x Torta — $18000","source":"deterministic","fallback_reason":"sin_ejemplos"}`},
		{"provider_down_is_still_200",
			quotetext.Suggestion{Text: "Total $1", Source: quotetext.SourceDeterministic, Reason: quotetext.ReasonProviderUnavailable},
			`{"rendered_text":"Total $1","source":"deterministic","fallback_reason":"proveedor_no_disponible"}`},
		{"html_is_escaped_by_the_encoder",
			quotetext.Suggestion{Text: `<b>"2 & 3"</b>`, Source: quotetext.SourceLLM},
			// \x5c es la barra invertida: `<`, `>` y `&` salen escapados (u003c, u003e, u0026).
			`{"rendered_text":"` + "\x5cu003cb\x5cu003e" + `\"2 ` + "\x5cu0026" + ` 3\"` + "\x5cu003c/b\x5cu003e" + `","source":"llm"}`},
		{"empty_suggestion_keeps_its_keys",
			quotetext.Suggestion{},
			`{"rendered_text":"","source":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			suggester := &quoteSuggesterSpy{out: tc.out}
			h, rec := quotePost(t, suggester, quoteTarget, "")
			wantCode(t, tc.name, rec, http.StatusOK)
			wantExactBody(t, tc.name, rec, tc.want)
			if suggester.calls != 1 || suggester.tenant != tenantA || suggester.intake != quoteIntakeID {
				t.Errorf("Suggest recibió %d llamadas con tenant %q e intake %q; quiero 1, el tenant del token y el id de la ruta",
					suggester.calls, suggester.tenant, suggester.intake)
			}
			if suggester.remaining != -1 {
				t.Errorf("al contexto de Suggest le quedaban %s; la cara no le pone plazo", suggester.remaining)
			}
			if n := len(h.Auditor().Records()); n != 0 {
				t.Errorf("G7 dejó %d registros de auditoría; no escribe nada, quiero 0", n)
			}
		})
	}
}

// TestQuoteSuggestion_IgnoresTheBody: el cuerpo no se lee. Ni otro tenant, ni otra solicitud, ni
// parámetros del modelo, ni basura cambian lo que recibe el generador.
func TestQuoteSuggestion_IgnoresTheBody(t *testing.T) {
	bodies := map[string]string{
		"other_tenant_and_intake": `{"tenant_id":"` + tenantB + `","intake_id":"otra","id":"otra"}`,
		"model_parameters":        `{"examples":50,"tone":"agresivo","via":"api","provider":"openai"}`,
		"not_json":                `<<<esto no es json`,
		"approval_shaped":         `{"rendered_text":"apruébalo","approved":true}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			suggester := &quoteSuggesterSpy{out: quotetext.Suggestion{Text: "Hola", Source: quotetext.SourceLLM}}
			_, rec := quotePost(t, suggester, quoteTarget+"?tenant_id="+tenantB+"&id=otra", body)
			wantCode(t, name, rec, http.StatusOK)
			wantExactBody(t, name, rec, `{"rendered_text":"Hola","source":"llm"}`)
			if suggester.tenant != tenantA || suggester.intake != quoteIntakeID {
				t.Errorf("Suggest recibió tenant %q e intake %q; quiero los del token y la ruta", suggester.tenant, suggester.intake)
			}
		})
	}
}

// TestQuoteSuggestion_PathIDTravelsAsIs: el {id} llega al generador tal cual, sin validar ni
// normalizar: decidir que no existe es cosa del dominio (404 opaco).
func TestQuoteSuggestion_PathIDTravelsAsIs(t *testing.T) {
	for _, id := range []string{"no-es-un-uuid", "ÑANDÚ", "a b", "' OR 1=1 --"} {
		suggester := &quoteSuggesterSpy{err: intakes.ErrNotFound}
		_, rec := quotePost(t, suggester, "/api/v1/intakes/"+url.PathEscape(id)+"/quote-suggestion", "")
		wantCode(t, id, rec, http.StatusNotFound)
		if suggester.calls != 1 || suggester.intake != id {
			t.Errorf("id %q: Suggest recibió %d llamadas con %q; quiero 1 con el id tal cual", id, suggester.calls, suggester.intake)
		}
	}
}

// TestQuoteSuggestion_Errors: los códigos y cuerpos del contrato. Cada caso exige que el
// generador se haya llamado: un 404 de «ruta no montada» es indistinguible por código del de
// «solicitud no encontrada».
func TestQuoteSuggestion_Errors(t *testing.T) {
	pending := &intakes.PendingPriceError{Lines: []intakes.PendingPriceLine{
		{Index: 2, Label: "Torta vainilla"}, {Index: 0, Label: `Cupcakes "x12"`},
	}}
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not_of_the_tenant", intakes.ErrNotFound, http.StatusNotFound, `{"error":"` + msgQuoteNotFound + `"}`},
		{"not_found_wrapped", fmt.Errorf("leyendo: %w", intakes.ErrNotFound), http.StatusNotFound, `{"error":"` + msgQuoteNotFound + `"}`},
		{"lines_without_price", pending, http.StatusBadRequest,
			`{"error":"lines_without_price","lines":[{"index":2,"label":"Torta vainilla"},{"index":0,"label":"Cupcakes \"x12\""}]}`},
		{"lines_without_price_wrapped", fmt.Errorf("cotizando: %w", pending), http.StatusBadRequest,
			`{"error":"lines_without_price","lines":[{"index":2,"label":"Torta vainilla"},{"index":0,"label":"Cupcakes \"x12\""}]}`},
		{"nothing_to_quote", quotetext.ErrNoLines, http.StatusBadRequest, `{"error":"` + msgQuoteNoLines + `"}`},
		{"nothing_to_quote_wrapped", fmt.Errorf("cotizando: %w", quotetext.ErrNoLines), http.StatusBadRequest, `{"error":"` + msgQuoteNoLines + `"}`},
		{"store_down_does_not_leak", errors.New("postgres://usuario:secreto@host/bd: conexión rechazada"), http.StatusInternalServerError,
			`{"error":"` + msgQuoteFailed + `"}`},
		{"not_wired_is_a_500", quotetext.ErrNotWired, http.StatusInternalServerError, `{"error":"` + msgQuoteFailed + `"}`},
		// El hermano de ErrNoLines que sale de la aprobación NO es de este camino.
		{"approval_sentinels_are_not_special", intakes.ErrConflict, http.StatusInternalServerError, `{"error":"` + msgQuoteFailed + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			suggester := &quoteSuggesterSpy{err: tc.err, out: quotetext.Suggestion{Text: "no debe salir", Source: quotetext.SourceLLM}}
			_, rec := quotePost(t, suggester, quoteTarget, "")
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.body)
			if suggester.calls != 1 {
				t.Errorf("%s: el generador se llamó %d veces; este código no lo produjo el handler", tc.name, suggester.calls)
			}
		})
	}
}

// quoteDeadlineWriter es un ResponseWriter que ADMITE plazo de escritura, como la conexión real:
// apunta cada SetWriteDeadline que le llega.
type quoteDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *quoteDeadlineWriter) SetWriteDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return nil
}

// quoteServe sirve una petición contra cara con el ResponseWriter dado.
func quoteServe(cara http.Handler, w http.ResponseWriter, token, method, target string) {
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cara.ServeHTTP(w, req)
}

// TestQuoteSuggestion_WriteDeadlineReachesTheConnection es el test de T-6: con access-log
// montado (Common.Log no nil), cuyo ResponseWriter NO desenvuelve, el plazo de escritura de G7
// llega igual al ResponseWriter que recibe la cara —porque el envoltorio va por FUERA de la
// cadena—, vale exactamente Now() + QuoteWriteDeadline y ya está puesto cuando el generador
// empieza a trabajar. Si el envoltorio se mete dentro de la cadena, este test muere.
func TestQuoteSuggestion_WriteDeadlineReachesTheConnection(t *testing.T) {
	h := apipublicahelpertest.New(t)
	w := &quoteDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	suggester := &quoteSuggesterSpy{
		out:   quotetext.Suggestion{Text: "Hola", Source: quotetext.SourceLLM},
		probe: func() int { return len(w.deadlines) },
	}
	cara := reportCara(h.Common(), quoteDeps(suggester))

	quoteServe(cara, w, h.With(tenantA, reportPerm), http.MethodPost, quoteTarget)

	wantCode(t, "G7", w.ResponseRecorder, http.StatusOK)
	want := reportNow().Add(reportQuoteDeadline)
	if len(w.deadlines) != 1 || !w.deadlines[0].Equal(want) {
		t.Fatalf("plazos de escritura puestos = %v; quiero exactamente uno, %s (Now + QuoteWriteDeadline)", w.deadlines, want)
	}
	if suggester.probed != 1 {
		t.Errorf("cuando el generador empezó había %d plazos puestos; el plazo va ANTES del trabajo lento", suggester.probed)
	}
	for _, e := range h.Log().Entries() {
		if e.Level == "warn" {
			t.Errorf("línea Warn inesperada con un ResponseWriter que admite plazos: %+v", e)
		}
	}
}

// TestQuoteSuggestion_WriteDeadlineAlsoOnFastRejections: por ir por fuera de la cadena, el plazo
// se pone también en el 401 y el 403 de G7 (el precio, declarado, de ponerlo donde llega).
func TestQuoteSuggestion_WriteDeadlineAlsoOnFastRejections(t *testing.T) {
	h := apipublicahelpertest.New(t)
	suggester := &quoteSuggesterSpy{}
	cara := reportCara(h.Common(), quoteDeps(suggester))
	for name, tc := range map[string]struct {
		token string
		code  int
	}{
		"without_token":      {"", http.StatusUnauthorized},
		"without_permission": {h.With(tenantA, "otra.cosa"), http.StatusForbidden},
	} {
		w := &quoteDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		quoteServe(cara, w, tc.token, http.MethodPost, quoteTarget)
		wantCode(t, name, w.ResponseRecorder, tc.code)
		if len(w.deadlines) != 1 {
			t.Errorf("%s: %d plazos puestos, quiero 1", name, len(w.deadlines))
		}
	}
	if suggester.calls != 0 {
		t.Errorf("el generador se llamó %d veces en peticiones rechazadas", suggester.calls)
	}
}

// TestQuoteSuggestion_WriteDeadlineIsOnlyForThatRoute: G9 y G10, montadas por el mismo Mount y
// servidas con el mismo ResponseWriter, no tocan el plazo de escritura.
func TestQuoteSuggestion_WriteDeadlineIsOnlyForThatRoute(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := reportCara(h.Common(), quoteDeps(&quoteSuggesterSpy{}))
	for _, target := range []string{exportTarget, summaryTarget} {
		w := &quoteDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		quoteServe(cara, w, h.With(tenantA, reportPerm), http.MethodGet, target)
		wantCode(t, target, w.ResponseRecorder, http.StatusOK)
		if len(w.deadlines) != 0 {
			t.Errorf("%s: se le puso plazo de escritura (%v); es SOLO de G7", target, w.deadlines)
		}
	}
}

// TestQuoteSuggestion_WriterWithoutDeadlinesIsServedAndLogged: si el ResponseWriter no admite
// plazos (el de httptest no lo hace), la petición se sirve IGUAL y el hecho queda a Warn, con el
// camino y el plazo y sin ningún dato de la solicitud.
func TestQuoteSuggestion_WriterWithoutDeadlinesIsServedAndLogged(t *testing.T) {
	suggester := &quoteSuggesterSpy{out: quotetext.Suggestion{Text: "Hola", Source: quotetext.SourceLLM}}
	h, rec := quotePost(t, suggester, quoteTarget+"?secreto=1", "")
	wantCode(t, "G7 sin plazos", rec, http.StatusOK)
	wantExactBody(t, "G7 sin plazos", rec, `{"rendered_text":"Hola","source":"llm"}`)

	var warns []apipublicahelpertest.LogEntry
	for _, e := range h.Log().Entries() {
		if e.Level == "warn" {
			warns = append(warns, e)
		}
	}
	if len(warns) != 1 || warns[0].Msg != msgQuoteDeadline {
		t.Fatalf("líneas Warn = %+v; quiero exactamente una, %q", warns, msgQuoteDeadline)
	}
	fields := warns[0].Fields
	if fields["path"] != quoteTarget || fields["plazo"] != reportQuoteDeadline.String() || fields["error"] == nil || len(fields) != 3 {
		t.Errorf("campos = %v; quiero path (sin la query), plazo %q y error, y nada más", fields, reportQuoteDeadline)
	}
}

// TestQuoteSuggestion_WithoutLogIsServedSilently: sin logger no hay dónde avisar; se sirve igual.
func TestQuoteSuggestion_WithoutLogIsServedSilently(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := apipublica.Common{MW: h.MW(), Auditor: h.Auditor()}
	suggester := &quoteSuggesterSpy{out: quotetext.Suggestion{Text: "Hola", Source: quotetext.SourceLLM}}
	rec := h.Call(reportCara(k, quoteDeps(suggester)), h.With(tenantA, reportPerm), http.MethodPost, quoteTarget, "")
	wantCode(t, "G7 sin logger", rec, http.StatusOK)
}
