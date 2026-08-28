package publicapi_test

// quotesuggestion_test.go — POST /api/v1/intakes/{id}/quote-suggestion
// (Plan 044 · Ola 5 · T5.1).
//
// Lo que se prueba aquí es lo que es de ESTA capa: los códigos, el cuerpo, el gate de
// features y —lo más importante— que la sugerencia NO APRUEBA NADA. La lógica del
// generador (few-shot, verificador de precios, fallback) vive en
// `internal/intakes/quotetext` y se prueba allí; aquí el dominio entra por un doble.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// sugeridorFalso es el doble del generador. Anota con qué se le llamó: es como se
// comprueba que el tenant sale del TOKEN y nunca de la URL ni del cuerpo (INV-7).
type sugeridorFalso struct {
	out      quotetext.Sugerencia
	err      error
	tenants  []string
	solicits []string
}

func (s *sugeridorFalso) Sugerir(_ context.Context, tenantID, intakeID string) (quotetext.Sugerencia, error) {
	s.tenants = append(s.tenants, tenantID)
	s.solicits = append(s.solicits, intakeID)
	return s.out, s.err
}

// quoteSuggestionDTO espeja el contrato del 200.
type quoteSuggestionDTO struct {
	RenderedText   string `json:"rendered_text"`
	Source         string `json:"source"`
	FallbackReason string `json:"fallback_reason"`
}

// depsSugerir arma unas Deps con las DOS features que la ruta exige.
func depsSugerir(st *intakes.MemoryStore, sug publicapi.QuoteSuggester) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	fake.Enable(tenantA, entitlements.FeatureLLMIntake)
	return publicapi.Deps{
		Intakes:          intakes.NewService(st),
		QuoteSuggestions: sug,
		Entitlements:     fake,
	}
}

func sugerir(t *testing.T, api *testAPI, id string) *httptest.ResponseRecorder {
	t.Helper()
	return call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+id+"/quote-suggestion", "")
}

func decodeSugerencia(t *testing.T, body []byte) quoteSuggestionDTO {
	t.Helper()
	var out quoteSuggestionDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decodificando la sugerencia: %v (body=%s)", err, body)
	}
	return out
}

// TestQuoteSuggestion_200_DevuelveElTextoYSuOrigen es el camino feliz.
func TestQuoteSuggestion_200_DevuelveElTextoYSuOrigen(t *testing.T) {
	sug := &sugeridorFalso{out: quotetext.Sugerencia{
		Texto: "Hola! Torta $18000. Total $18000", Origen: quotetext.OrigenLLM,
	}}
	api := newAPI(depsSugerir(bandejaPorAprobar(), sug), intakesKeys())

	rec := sugerir(t, api, intakePorAprobar)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeSugerencia(t, rec.Body.Bytes())
	if got.RenderedText != sug.out.Texto || got.Source != quotetext.OrigenLLM {
		t.Fatalf("cuerpo=%+v; se esperaba el texto del generador con source=llm", got)
	}
	if got.FallbackReason != "" {
		t.Errorf("fallback_reason=%q; un texto del modelo no lo trae", got.FallbackReason)
	}
	// INV-7: el tenant sale del token, la solicitud de la ruta.
	if len(sug.tenants) != 1 || sug.tenants[0] != tenantA || sug.solicits[0] != intakePorAprobar {
		t.Fatalf("el generador recibió tenants=%v solicitudes=%v", sug.tenants, sug.solicits)
	}
}

// TestQuoteSuggestion_200_ConFallbackLoDice: el determinista se publica CON su motivo,
// y el motivo es del vocabulario cerrado.
func TestQuoteSuggestion_200_ConFallbackLoDice(t *testing.T) {
	sug := &sugeridorFalso{out: quotetext.Sugerencia{
		Texto:  "Hola! Te paso el presupuesto:\n• Torta — $18000\n\nTotal: $18000",
		Origen: quotetext.OrigenDeterminista, Motivo: quotetext.MotivoImporteAjeno,
	}}
	api := newAPI(depsSugerir(bandejaPorAprobar(), sug), intakesKeys())

	rec := sugerir(t, api, intakePorAprobar)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeSugerencia(t, rec.Body.Bytes())
	if got.Source != quotetext.OrigenDeterminista || got.FallbackReason != quotetext.MotivoImporteAjeno {
		t.Fatalf("cuerpo=%+v; se esperaba (deterministic, %s)", got, quotetext.MotivoImporteAjeno)
	}
}

// 🔴 TestQuoteSuggestion_NoApruebaNiEnvía es EL test de esta capa: sugerir no es
// aprobar. La solicitud sigue en `pending_approval`, sin revisiones nuevas, y el
// cliente no ha recibido nada.
//
// Que el canal no exista siquiera en estas Deps es la mitad del argumento —no hay por
// dónde enviar—; la otra mitad es que el estado no se mueve, y eso se lee del GET.
func TestQuoteSuggestion_NoApruebaNiEnvía(t *testing.T) {
	st := bandejaPorAprobar()
	sug := &sugeridorFalso{out: quotetext.Sugerencia{Texto: "lo que sea", Origen: quotetext.OrigenLLM}}
	api := newAPI(depsSugerir(st, sug), intakesKeys())

	if rec := sugerir(t, api, intakePorAprobar); rec.Code != http.StatusOK {
		t.Fatalf("code=%d; body=%s", rec.Code, rec.Body.String())
	}

	leído := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakePorAprobar, "")
	detalle := decodeDetalle(t, leído.Body.Bytes())
	if detalle.Status != intakes.StatusPendingApproval {
		t.Fatalf("status=%q; sugerir no puede mover la solicitud", detalle.Status)
	}
	if len(detalle.Revisions) != 0 {
		t.Fatalf("revisiones=%d; sugerir no escribe ninguna", len(detalle.Revisions))
	}
	if got := len(st.Revisions(intakePorAprobar)); got != 0 {
		t.Fatalf("el store tiene %d revisiones; sugerir no persiste nada", got)
	}
}

// TestQuoteSuggestion_Errores: los códigos del contrato.
//
// 🔴 CADA CASO EXIGE ADEMÁS QUE EL DOMINIO SE HAYA LLAMADO, y no es ceremonia: sin eso,
// el caso del 404 pasaría IGUAL con la ruta desregistrada —un 404 de «ruta no montada»
// es indistinguible por código de un 404 de «solicitud no encontrada»—, y el test
// estaría afirmando algo que no comprueba. El contador del doble es lo que separa las
// dos cosas.
func TestQuoteSuggestion_Errores(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		code   int
		clave  string
	}{
		{"no es del tenant", intakes.ErrNotFound, http.StatusNotFound, "solicitud no encontrada"},
		{"sin líneas que cotizar", quotetext.ErrSinLineas, http.StatusBadRequest, ""},
		{"líneas sin precio", &intakes.PendingPriceError{
			Lines: []intakes.PendingPriceLine{{Label: "Torta vainilla"}},
		}, http.StatusBadRequest, "lines_without_price"},
		{"el store se cayó", errStoreCaído, http.StatusInternalServerError, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			sug := &sugeridorFalso{err: c.err}
			api := newAPI(depsSugerir(bandejaPorAprobar(), sug), intakesKeys())

			rec := sugerir(t, api, intakePorAprobar)

			if rec.Code != c.code {
				t.Fatalf("code=%d, quiero %d; body=%s", rec.Code, c.code, rec.Body.String())
			}
			if len(sug.tenants) != 1 {
				t.Fatalf("el generador se llamó %d veces: la petición no llegó al dominio, "+
					"así que este código no lo produjo el handler", len(sug.tenants))
			}
			if c.clave != "" {
				var cuerpo struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil || cuerpo.Error != c.clave {
					t.Fatalf("error=%q (err=%v); se esperaba %q", cuerpo.Error, err, c.clave)
				}
			}
		})
	}
}

// TestQuoteSuggestion_403_SinLLMIntake: ésta es la única ruta de la bandeja que cobra
// la feature del pipeline, porque es literalmente la máquina que redacta sola
// (D-044.49 §3). Un tenant `Basic` —el que existe en UAT— no la tiene.
//
// El control del final es lo que hace que el assert no sea vacuo: con la feature
// puesta, la MISMA petición pasa.
func TestQuoteSuggestion_403_SinLLMIntake(t *testing.T) {
	sug := &sugeridorFalso{out: quotetext.Sugerencia{Texto: "x", Origen: quotetext.OrigenLLM}}
	soloBasic := entitlements.NewFake()
	soloBasic.Enable(tenantA, entitlements.FeatureCartBasic)
	api := newAPI(publicapi.Deps{
		Intakes:          intakes.NewService(bandejaPorAprobar()),
		QuoteSuggestions: sug,
		Entitlements:     soloBasic,
	}, intakesKeys())

	if rec := sugerir(t, api, intakePorAprobar); rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if len(sug.tenants) != 0 {
		t.Fatal("el gate tiene que cortar ANTES de llamar al generador")
	}

	api2 := newAPI(depsSugerir(bandejaPorAprobar(), sug), intakesKeys())
	if rec := sugerir(t, api2, intakePorAprobar); rec.Code != http.StatusOK {
		t.Fatalf("con llm_intake la misma petición tiene que pasar; code=%d body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestQuoteSuggestion_404_SinGenerador: sin la dependencia cableada la ruta NO se
// monta. Es preferible un 404 de ruta inexistente a un 500 a medio camino.
//
// 🔴 EL CÓDIGO NO BASTA PARA AFIRMAR ESTO. Un 404 de «ruta no montada» y un 404 de
// «solicitud no encontrada» son el mismo número, así que este test pasaría con la ruta
// registrada y el generador devolviendo ErrNotFound — es decir, afirmando algo que no
// mira. Se distinguen por el CUERPO: el del handler es el JSON `{"error":"solicitud no
// encontrada"}` y el del mux es texto plano. Y el control del final cierra la pinza:
// con el generador cableado, la MISMA petición da 200.
func TestQuoteSuggestion_404_SinGenerador(t *testing.T) {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	fake.Enable(tenantA, entitlements.FeatureLLMIntake)
	api := newAPI(publicapi.Deps{
		Intakes: intakes.NewService(bandejaPorAprobar()), Entitlements: fake,
	}, intakesKeys())

	rec := sugerir(t, api, intakePorAprobar)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	var cuerpo struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err == nil && cuerpo.Error != "" {
		t.Fatalf("el 404 trae el cuerpo del HANDLER (%q): la ruta SÍ se montó y este test "+
			"no está comprobando lo que dice", cuerpo.Error)
	}

	// Control: con el generador cableado la misma petición pasa.
	api2 := newAPI(depsSugerir(bandejaPorAprobar(),
		&sugeridorFalso{out: quotetext.Sugerencia{Texto: "x", Origen: quotetext.OrigenLLM}}), intakesKeys())
	if rec := sugerir(t, api2, intakePorAprobar); rec.Code != http.StatusOK {
		t.Fatalf("con el generador cableado la misma petición tiene que dar 200; code=%d body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestQuoteSuggestion_TenantAjeno: la solicitud de otro tenant no existe para éste
// (404 opaco, INV-8). El tenant que llega al dominio es SIEMPRE el del token.
func TestQuoteSuggestion_TenantAjeno(t *testing.T) {
	sug := &sugeridorFalso{err: intakes.ErrNotFound}
	fake := entitlements.NewFake()
	fake.Enable(tenantB, entitlements.FeatureCartBasic)
	fake.Enable(tenantB, entitlements.FeatureLLMIntake)
	api := newAPI(publicapi.Deps{
		Intakes:          intakes.NewService(bandejaPorAprobar()),
		QuoteSuggestions: sug,
		Entitlements:     fake,
	}, intakesKeys())

	rec := call(api, keyBIntakes, http.MethodPost,
		"/api/v1/intakes/"+intakePorAprobar+"/quote-suggestion", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	if len(sug.tenants) != 1 || sug.tenants[0] != tenantB {
		t.Fatalf("el generador recibió tenants=%v; el tenant sale del TOKEN (INV-7)", sug.tenants)
	}
}
