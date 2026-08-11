package publicapi_test

// intents_aditividad_test.go — Plan 043 · T5.3, D-043.9. La config de intents
// (PUT /api/v1/intents) ADMITE un `event_kind` opcional por intent en el blob (el
// texto del plan): el body sigue validando con o sin él. Lo que este fichero NO
// prueba —y no puede probar por HTTP— es que ese campo haga algo: es INERTE en el
// Cloud a propósito (D1 del CONTRATO-OLA5: el scoping real sale de
// flow_triggers.event_kind en la regla kind='llm', ver putIntentsHandler en
// intents.go). Cero código de producción nuevo: la aditividad ya la daba
// wapp-shared/intents (parser tolerante a claves desconocidas, sin
// DisallowUnknownFields); esto es constancia con test.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// validIntentsJSONConEventKind es el MISMO contrato de validIntentsJSON (misma
// intención, mismos campos obligatorios) más un `event_kind` por intent — la forma
// que el texto del plan («la config de intents gana event_kind opcional por
// intent») describe.
const validIntentsJSONConEventKind = `{"version":"v1","umbral_confianza":0.7,"intents":[{"name":"pedir_pizza","descripcion":"pedir comida","params":["cantidad"],"ejemplos":[{"mensaje":"quiero una pizza"}],"event_kind":"cart"}]}`

// TestIntentsPut_200_BlobViejoSinEventKind fija que un blob del Plan 029 (sin
// event_kind en ningún intent) sigue validando y persistiendo tal cual: la
// aditividad no rompe el contrato de siempre.
func TestIntentsPut_200_BlobViejoSinEventKind(t *testing.T) {
	pusher := &fakePusher{}
	d := intentsDeps(pusher)
	mux := newAPI(d, intentsKeys())

	rec := call(mux, keyAIntents, http.MethodPut, "/api/v1/intents", validIntentsJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntentsPut_200_BlobNuevoConEventKindPorIntent fija que un blob que SÍ trae
// event_kind por intent también valida (200) y persiste el JSON tal cual lo mandó
// el tenant — el Cloud no lo interpreta, pero tampoco lo rechaza ni lo recorta.
func TestIntentsPut_200_BlobNuevoConEventKindPorIntent(t *testing.T) {
	pusher := &fakePusher{}
	d := intentsDeps(pusher)
	mux := newAPI(d, intentsKeys())

	rec := call(mux, keyAIntents, http.MethodPut, "/api/v1/intents", validIntentsJSONConEventKind)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// GET confirma que el blob persistido conserva event_kind VERBATIM (el store
	// no lo despoja) — es informativo, no se procesa, pero tampoco se pierde.
	getRec := call(mux, keyAIntents, http.MethodGet, "/api/v1/intents", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET code=%d, quiero 200; body=%s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), `"event_kind":"cart"`) {
		t.Fatalf("el blob persistido debe conservar event_kind verbatim: %s", getRec.Body.String())
	}
}
