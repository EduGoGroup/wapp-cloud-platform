package publicapi_test

// intakes_requestinfo_test.go — POST /api/v1/intakes/{id}/request-info y el campo
// `as_correction` del PUT (Plan 044 · Ola 4 · T4.4).
//
// Las DOS puertas de T4.4 por HTTP: la que no existía y la que ya existía y solo gana
// un campo. Reusa la escena y el canal falso de intakes_approve_test.go —misma
// solicitud por aprobar, mismo tenant `Basic`— porque las tres acciones del dueño
// operan sobre la misma pantalla.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// preguntaDelDueño es el texto que viaja en el cuerpo, con acentos y dos renglones.
const preguntaDelDueño = "¿La torta la querías de 15 porciones o de 20?\n¿Y para qué día?"

func pedirInfo(t *testing.T, api *testAPI, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+id+"/request-info", body)
}

func corregirLíneas(t *testing.T, api *testAPI, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+id+"/items", body)
}

// --- PUERTA 2: request-info -------------------------------------------------

// TestRequestInfo_200_PreguntaYQuedaEsperando es el camino feliz completo por HTTP,
// con el tenant `Basic` (cart_basic, sin llm_intake): ni un 403 por el camino, que es
// el criterio de D-044.49 §3.
func TestRequestInfo_200_PreguntaYQuedaEsperando(t *testing.T) {
	canal := &canalFalso{}
	api := newAPI(depsAprobar(bandejaPorAprobar(), canal), intakesKeys())

	rec := pedirInfo(t, api, intakePorAprobar, `{"question":`+strconv.Quote(preguntaDelDueño)+`}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())
	if detalle.Status != intakes.StatusNeedsInfo {
		t.Fatalf("status=%q, quiero needs_info", detalle.Status)
	}
	if len(detalle.Revisions) != 0 {
		t.Fatalf("revisiones=%d, quiero 0: preguntar no cambia el presupuesto", len(detalle.Revisions))
	}
	preguntas := canal.preguntasEnviadas()
	if len(preguntas) != 1 || preguntas[0] != preguntaDelDueño {
		t.Fatalf("preguntas enviadas=%v; quiero exactamente la del dueño, byte a byte", preguntas)
	}
	if got := len(canal.mensajes()); got != 0 {
		t.Fatalf("salieron %d cotizaciones por una petición de información", got)
	}
}

// TestRequestInfo_400_SinPregunta: «jamás sale sola» (criterio explícito de T4.4). Los
// tres cuerpos que significan lo mismo dan el mismo 400, y ninguno mueve la solicitud.
func TestRequestInfo_400_SinPregunta(t *testing.T) {
	casos := map[string]string{
		"clave ausente": `{}`,
		"cadena vacía":  `{"question":""}`,
		"en blanco":     `{"question":"   \n  "}`,
	}
	for nombre, body := range casos {
		t.Run(nombre, func(t *testing.T) {
			canal := &canalFalso{}
			st := bandejaPorAprobar()
			api := newAPI(depsAprobar(st, canal), intakesKeys())

			rec := pedirInfo(t, api, intakePorAprobar, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
			}
			if got := len(canal.preguntasEnviadas()); got != 0 {
				t.Fatalf("salieron %d preguntas con un cuerpo sin pregunta", got)
			}
			// Y la solicitud sigue por aprobar: el rechazo ocurre antes de escribir.
			leído := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakePorAprobar, "")
			if got := decodeDetalle(t, leído.Body.Bytes()).Status; got != intakes.StatusPendingApproval {
				t.Fatalf("status=%q; una petición rechazada no puede mover la solicitud", got)
			}
		})
	}
}

// TestRequestInfo_422_NoEstáPorAprobar: el cuerpo es el del CICLO DE VIDA y no uno
// propio, porque esta puerta no estrecha la máquina de estados — lo útil ahí es saber
// adónde SÍ puede ir la solicitud.
func TestRequestInfo_422_NoEstáPorAprobar(t *testing.T) {
	canal := &canalFalso{}
	api := newAPI(depsAprobar(bandejaPorAprobar(), canal), intakesKeys())

	// Dos peticiones seguidas: es el caso REAL (dos pestañas, o un doble clic). La
	// segunda encuentra la solicitud ya en `needs_info`.
	if rec := pedirInfo(t, api, intakePorAprobar, `{"question":"la primera"}`); rec.Code != http.StatusOK {
		t.Fatalf("la primera petición tiene que ir bien; code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := pedirInfo(t, api, intakePorAprobar, `{"question":"la segunda"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	var body invalidTransitionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 422: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "invalid_transition" || body.Status != intakes.StatusNeedsInfo {
		t.Fatalf("cuerpo del 422 = %+v; quiero invalid_transition sobre needs_info", body)
	}
	if body.Requested != intakes.StatusNeedsInfo {
		t.Fatalf("requested=%q, quiero needs_info", body.Requested)
	}
	// Y el cliente recibió UNA pregunta, no dos: el segundo intento no llegó a hablar.
	if got := len(canal.preguntasEnviadas()); got != 1 {
		t.Fatalf("preguntas al cliente = %d tras dos POST, quiero 1", got)
	}
}

// TestRequestInfo_404_SolicitudAjena: 404 opaco, nunca 403 (INV-8).
func TestRequestInfo_404_SolicitudAjena(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())
	rec := pedirInfo(t, api, "99999999-9999-9999-9999-999999999999", `{"question":"hola"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRequestInfo_403_SinFeature: el gate es `cart_basic` y NO `llm_intake`
// (D-044.49 §3) — el mismo argumento que ya vale para el PUT del 041 y para approve.
func TestRequestInfo_403_SinFeature(t *testing.T) {
	d := publicapi.Deps{
		Intakes:      intakes.NewService(bandejaPorAprobar(), intakes.WithQuoteSender(&canalFalso{})),
		Entitlements: entitlements.NewFake(), // ninguna feature encendida
	}
	rec := pedirInfo(t, newAPI(d, intakesKeys()), intakePorAprobar, `{"question":"hola"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 403: %v; body=%s", err, rec.Body.String())
	}
	if body["feature"] != entitlements.FeatureCartBasic {
		t.Fatalf("el 403 pide %q; el gate de request-info es cart_basic (D-044.49 §3)", body["feature"])
	}
}

// TestRequestInfo_403_SinScope: con la feature pero sin `intakes.write` no se escribe.
// Son dos guardias y ninguno sustituye al otro.
func TestRequestInfo_403_SinScope(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())
	rec := call(api, keyARead, http.MethodPost, "/api/v1/intakes/"+intakePorAprobar+"/request-info",
		`{"question":"hola"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin el scope; body=%s", rec.Code, rec.Body.String())
	}
}

// --- PUERTA 1: `correct` es el PUT con un campo -----------------------------

// TestCorrect_NoHayRutaPropia: la ACCIÓN «Corregir» del 044 NO tiene endpoint propio
// (D-044.48 §1). Es un candado, no una curiosidad: el día que alguien «complete la
// API» añadiendo POST …/correct habrá dos puertas dejando la misma revisión
// `corrected`, que es el duplicado que este plan ya pagó en el hallazgo #24.
func TestCorrect_NoHayRutaPropia(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakePorAprobar+"/correct",
		`{"items":[]}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d: /correct RESPONDE. `correct` es el PUT …/items con `as_correction` "+
			"(D-044.48 §1), no una ruta nueva; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCorrect_ConElCampoDejaLaSeñalEnLaRevisión: el PUT con `"as_correction": true`
// responde lo mismo que sin él —200 con el detalle— y además deja la señal few-shot
// dentro del payload de la revisión que ya escribía.
func TestCorrect_ConElCampoDejaLaSeñalEnLaRevisión(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())

	rec := corregirLíneas(t, api, intakePorAprobar, `{"as_correction":true,"items":[
		{"sku":"torta-v1","label":"Torta 15 porciones","qty":1,"unit_price":22000}
	]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())
	if len(detalle.Revisions) != 1 {
		t.Fatalf("revisiones=%d, quiero 1 (la corrección)", len(detalle.Revisions))
	}
	rev := detalle.Revisions[0]
	if rev.Kind != intakes.RevisionKindCorrected || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión kind=%q created_by=%q; quiero corrected/owner", rev.Kind, rev.CreatedBy)
	}
	señal := señalDelPayload(t, rev.Payload)
	if !señal.AsCorrection {
		t.Fatalf("el campo del wire NO llegó al payload de la revisión: %s", rev.Payload)
	}
	// Y la solicitud sigue por aprobar: la «vuelta a pending_approval» ya es cierta.
	if detalle.Status != intakes.StatusPendingApproval {
		t.Fatalf("status=%q, quiero pending_approval", detalle.Status)
	}
}

// TestCorrect_SinElCampoElPUTDel041NoCambia es la cero-regresión por el borde del
// wire: el MISMO cuerpo sin el campo tiene que dejar la revisión sin una sola clave de
// la señal. Es lo que garantiza que un cliente del 041 —que jamás mandará
// `as_correction`— no vea cambiar nada.
func TestCorrect_SinElCampoElPUTDel041NoCambia(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())

	rec := corregirLíneas(t, api, intakePorAprobar, `{"items":[
		{"sku":"torta-v1","label":"Torta 15 porciones","qty":1,"unit_price":22000}
	]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())
	if len(detalle.Revisions) != 1 {
		t.Fatalf("revisiones=%d, quiero 1", len(detalle.Revisions))
	}
	var raíz map[string]json.RawMessage
	if err := json.Unmarshal(detalle.Revisions[0].Payload, &raíz); err != nil {
		t.Fatalf("unmarshal del payload: %v; payload=%s", err, detalle.Revisions[0].Payload)
	}
	for _, clave := range []string{intakes.ClaveAsCorrection, intakes.ClaveCorrectsRevisionNo, intakes.ClaveCorrectsKind} {
		if _, hay := raíz[clave]; hay {
			t.Fatalf("el PUT del 041 dejó %q en el payload: %s", clave, detalle.Revisions[0].Payload)
		}
	}
}

// señalDelPayload lee la señal few-shot del payload crudo de una revisión.
func señalDelPayload(t *testing.T, payload json.RawMessage) intakes.CorrectionSignal {
	t.Helper()
	var señal intakes.CorrectionSignal
	if err := json.Unmarshal(payload, &señal); err != nil {
		t.Fatalf("no se pudo leer la señal del payload (%v): %s", err, payload)
	}
	return señal
}
