//go:build pendiente

package apipublica_test

// intakes_llm_gate_test.go — EL GATE POR CAMPO del detalle (Plan 044 · T4.1) del contrato de
// MountIntakes: sin `llm_intake`, de cada revisión desaparecen `suggested_questions` y las
// `variant_options` de sus líneas, y nada más. La pareja de golden de testdata/ es el contrato
// que lee la app del Plan 045. Los dobles están en intakes_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	// intakeLLMPayload es un payload §7.4 con los dos campos del pipeline: la clave RAÍZ y la
	// anidada en UNA de sus dos líneas (con una sola línea no se distinguiría «se borraron las
	// de todas» de «se borró la raíz y la primera de paso»).
	intakeLLMPayload = `{"version":1,"source_text":"quiero una torta y tequeños","lines":[` +
		`{"kind":"matched","sku":"TORTA","qty":1,"variant_options":[{"sku":"TORTA#1","price":18000},{"sku":"TORTA#2","price":21000}]},` +
		`{"kind":"unmatched","label":"tequeños","qty":2}],"suggested_questions":["¿De 10 o de 12 porciones?"]}`
	// intakeLLMPayloadHidden es lo que queda de él sin la feature.
	intakeLLMPayloadHidden = `{"version":1,"source_text":"quiero una torta y tequeños","lines":[` +
		`{"kind":"matched","sku":"TORTA","qty":1},{"kind":"unmatched","label":"tequeños","qty":2}]}`
)

// intakeFeatureResolver es un entitlements.Resolver con respuesta POR FEATURE: hace falta para
// abrir la puerta (`cart_basic`) y, a la vez, fallar o negar en el campo (`llm_intake`).
type intakeFeatureResolver struct {
	on   map[string]bool
	errs map[string]error
}

var _ entitlements.Resolver = intakeFeatureResolver{}

func (r intakeFeatureResolver) Has(_ context.Context, _, feature string) (bool, error) {
	return r.on[feature], r.errs[feature]
}

func (r intakeFeatureResolver) ListEffective(context.Context, string) (string, []string, error) {
	return "", nil, nil
}

func (intakeFeatureResolver) CacheTTL() time.Duration { return 0 }

// intakeRevisionsOf arma un detalle con una revisión por payload.
func intakeRevisionsOf(payloads ...string) intakes.Detail {
	d := intakes.Detail{Intake: intakeDue()}
	for i, p := range payloads {
		d.Revisions = append(d.Revisions, intakes.Revision{
			IntakeID: d.ID, RevisionNo: i + 1, Kind: intakes.RevisionKindInterpreted,
			Payload: json.RawMessage(p), CreatedAt: intakeNow,
		})
	}
	return d
}

// intakeDetailWith pide por la ruta r el detalle que sirve el doble, con el resolver dado.
func intakeDetailWith(t *testing.T, resolver entitlements.Resolver, detail intakes.Detail, r intakeRoute) []byte {
	t.Helper()
	h := apipublicahelpertest.New(t)
	d := apipublica.IntakesDeps{Intakes: &intakeServiceSpy{detail: detail}, Entitlements: resolver, Now: intakeClock}
	rec := h.Call(intakeCara(h.Common(), d), h.With(tenantA, intakeReadPerm, intakeWritePerm), r.method, r.target, r.body)
	// 200 y no 403: el gate va sobre los CAMPOS, no sobre la puerta (D-044.47 §1).
	wantCode(t, r.id, rec, http.StatusOK)
	return rec.Body.Bytes()
}

// intakePayloads saca del cuerpo el payload crudo de cada revisión.
func intakePayloads(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var detail struct {
		Revisions []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("el cuerpo del detalle no es JSON (%s): %v", body, err)
	}
	out := make([]json.RawMessage, 0, len(detail.Revisions))
	for _, r := range detail.Revisions {
		out = append(out, r.Payload)
	}
	return out
}

// intakeSameJSON dice si dos JSON llevan el mismo contenido (el orden de las claves no cuenta).
func intakeSameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var ta, tb any
	if err := json.Unmarshal(a, &ta); err != nil {
		t.Fatalf("JSON ilegible (%s): %v", a, err)
	}
	if err := json.Unmarshal(b, &tb); err != nil {
		t.Fatalf("JSON ilegible (%s): %v", b, err)
	}
	return reflect.DeepEqual(ta, tb)
}

// intakeDetailRoutes son las CUATRO rutas que responden el detalle: G2, G4, G5 y G6.
func intakeDetailRoutes() []intakeRoute {
	var out []intakeRoute
	for _, r := range intakeRoutes {
		if r.call == "Get" || r.call == "ReplaceItems" || r.call == "Approve" || r.call == "RequestInfo" {
			out = append(out, r)
		}
	}
	return out
}

// TestMountIntakes_LLMGate_HidesBothFieldsOnTheFourDetailRoutes: sin `llm_intake` las dos claves
// DESAPARECEN —no quedan en [], que ya significa «no hay nada que preguntar»— y el resto del
// payload sigue ahí. Si el gate solo estuviera en el GET, editar una línea devolvería por el PUT
// justo lo que el GET acaba de tapar.
func TestMountIntakes_LLMGate_HidesBothFieldsOnTheFourDetailRoutes(t *testing.T) {
	routes := intakeDetailRoutes()
	if len(routes) != 4 {
		t.Fatalf("hay %d rutas de detalle, quiero 4 (G2, G4, G5 y G6)", len(routes))
	}
	for _, r := range routes {
		t.Run(r.id, func(t *testing.T) {
			body := intakeDetailWith(t, withFeatures(entitlements.FeatureCartBasic), intakeRevisionsOf(intakeLLMPayload), r)
			for _, key := range []string{"suggested_questions", "variant_options"} {
				if bytes.Contains(body, []byte(key)) {
					t.Errorf("%s sin llm_intake: el cuerpo lleva %q (%s)", r.id, key, body)
				}
			}
			if got := intakePayloads(t, body); len(got) != 1 || !intakeSameJSON(t, got[0], []byte(intakeLLMPayloadHidden)) {
				t.Errorf("%s sin llm_intake: payload\n  %s\nquiero el original SIN las dos claves y nada más\n  %s", r.id, got, intakeLLMPayloadHidden)
			}
		})
	}
}

// TestMountIntakes_LLMGate_WithTheFeatureThePayloadIsWhole: con `llm_intake` los dos campos salen
// poblados y el payload no se reordena. Es la mitad que convierte «no aparecen» en un gate.
func TestMountIntakes_LLMGate_WithTheFeatureThePayloadIsWhole(t *testing.T) {
	for _, r := range intakeDetailRoutes() {
		body := intakeDetailWith(t, withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake), intakeRevisionsOf(intakeLLMPayload), r)
		if got := intakePayloads(t, body); len(got) != 1 || string(got[0]) != intakeLLMPayload {
			t.Errorf("%s con llm_intake: payload\n  %s\nquiero el del servicio, tal cual\n  %s", r.id, got, intakeLLMPayload)
		}
	}
}

// TestMountIntakes_LLMGate_FailsClosed: un fallo al resolver la feature no puede abrir un campo
// de pago. Ante la duda se tapa, igual que hace RequireFeature con la puerta.
func TestMountIntakes_LLMGate_FailsClosed(t *testing.T) {
	door := map[string]bool{entitlements.FeatureCartBasic: true}
	for name, resolver := range map[string]entitlements.Resolver{
		"resolver_fails_on_the_field": intakeFeatureResolver{on: door, errs: map[string]error{entitlements.FeatureLLMIntake: errors.New("bd caída")}},
		"true_with_an_error_is_still_closed": intakeFeatureResolver{
			on:   map[string]bool{entitlements.FeatureCartBasic: true, entitlements.FeatureLLMIntake: true},
			errs: map[string]error{entitlements.FeatureLLMIntake: errors.New("caché a medias")}},
		"other_features_do_not_open_it": intakeFeatureResolver{on: map[string]bool{
			entitlements.FeatureCartBasic: true, entitlements.FeatureAPILLM: true, entitlements.FeatureIntakesExport: true}},
	} {
		body := intakeDetailWith(t, resolver, intakeRevisionsOf(intakeLLMPayload), intakeRoutes[1])
		if bytes.Contains(body, []byte("suggested_questions")) || bytes.Contains(body, []byte("variant_options")) {
			t.Errorf("%s: el cuerpo publica los campos del pipeline (%s)", name, body)
		}
	}
}

// TestMountIntakes_LLMGate_UntouchedPayloadsAreByteIdentical: lo que no lleva ninguna de las dos
// claves EN SU NIVEL sale igual con y sin la feature, byte a byte. Un filtro que reserializara
// siempre daría a dos planes dos cuerpos distintos del MISMO dato (las claves en otro orden).
// Incluye lo que no es un objeto y las claves en el nivel equivocado, que no son las del contrato.
func TestMountIntakes_LLMGate_UntouchedPayloadsAreByteIdentical(t *testing.T) {
	detail := intakeRevisionsOf(
		`{"version":1,"total":9,"lines":[{"sku":"B","qty":1},{"sku":"A","qty":2}]}`,
		`{"zeta":1,"alfa":2}`,
		`[1,2,3]`,
		`"texto suelto"`,
		`7`,
		`null`,
		`{"version":1,"lines":"no-es-una-lista"}`,
		`{"version":1,"lines":[1,"x",null,[2],{"sku":"A"}]}`,
		`{"version":1,"lines":[]}`,
		// Las claves en el nivel que NO es el suyo no son las del contrato.
		`{"version":1,"variant_options":["raíz"],"lines":[{"suggested_questions":["línea"],"sku":"A"}]}`,
		`{"version":1,"nested":{"suggested_questions":["x"],"lines":[{"variant_options":[1]}]}}`,
	)
	with := intakeDetailWith(t, withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake), detail, intakeRoutes[1])
	without := intakeDetailWith(t, withFeatures(entitlements.FeatureCartBasic), detail, intakeRoutes[1])
	if !bytes.Equal(with, without) {
		t.Errorf("payloads sin las claves del pipeline: el cuerpo cambia con el plan\ncon llm_intake\n  %s\nsin\n  %s", with, without)
	}
	if n := len(intakePayloads(t, without)); n != len(detail.Revisions) {
		t.Errorf("salieron %d revisiones, quiero %d", n, len(detail.Revisions))
	}
}

// TestMountIntakes_LLMGate_EachKeyOnItsOwn: cada clave se tapa aunque la otra no esté, en
// cualquier línea, y una revisión tapada no arrastra a sus hermanas.
func TestMountIntakes_LLMGate_EachKeyOnItsOwn(t *testing.T) {
	cases := []struct{ name, payload, want string }{
		{"only_the_root_key", `{"version":1,"suggested_questions":[]}`, `{"version":1}`},
		{"root_key_with_null", `{"suggested_questions":null,"lines":[{"sku":"A"}]}`, `{"lines":[{"sku":"A"}]}`},
		{"only_the_second_line", `{"lines":[{"sku":"A"},{"sku":"B","variant_options":[{"sku":"B#1"}]}]}`, `{"lines":[{"sku":"A"},{"sku":"B"}]}`},
		{"every_line", `{"lines":[{"variant_options":[]},{"variant_options":null,"qty":2}]}`, `{"lines":[{},{"qty":2}]}`},
		{"lines_with_strangers", `{"lines":[7,{"variant_options":[1],"sku":"A"},"x"],"suggested_questions":["q"]}`, `{"lines":[7,{"sku":"A"},"x"]}`},
	}
	payloads := make([]string, 0, len(cases)+1)
	for _, tc := range cases {
		payloads = append(payloads, tc.payload)
	}
	sibling := `{"zeta":1,"alfa":2}`
	payloads = append(payloads, sibling)

	got := intakePayloads(t, intakeDetailWith(t, withFeatures(entitlements.FeatureCartBasic), intakeRevisionsOf(payloads...), intakeRoutes[1]))
	if len(got) != len(payloads) {
		t.Fatalf("salieron %d revisiones, quiero %d", len(got), len(payloads))
	}
	for i, tc := range cases {
		if !intakeSameJSON(t, got[i], []byte(tc.want)) {
			t.Errorf("%s: payload %s, quiero %s", tc.name, got[i], tc.want)
		}
	}
	if string(got[len(cases)]) != sibling {
		t.Errorf("la revisión hermana salió %s, quiero %s intacta", got[len(cases)], sibling)
	}
}

// TestMountIntakes_LLMGate_DoesNotHideTheLiteralPruneSeal: `literal_pruned_at` vive FUERA del
// payload y no es contenido del pipeline sino un hecho de retención. La primera mitad comprueba
// que el gate corrió: sin ella esto pasaría igual con el gate desconectado.
func TestMountIntakes_LLMGate_DoesNotHideTheLiteralPruneSeal(t *testing.T) {
	detail := intakeRevisionsOf(`{"version":1,"lines":[],"suggested_questions":["¿Para cuándo?"]}`)
	detail.Revisions[0].LiteralPrunedAt = time.Date(2026, 9, 1, 9, 0, 0, 0, intakeZone)
	body := intakeDetailWith(t, withFeatures(entitlements.FeatureCartBasic), detail, intakeRoutes[1])
	if bytes.Contains(body, []byte("suggested_questions")) {
		t.Fatalf("el gate no corrió: el cuerpo lleva suggested_questions (%s)", body)
	}
	if !bytes.Contains(body, []byte(`"literal_pruned_at":"2026-09-01T12:00:00Z"`)) {
		t.Errorf("sin llm_intake el sello de la poda no sale (%s); el gate solo borra claves DEL payload", body)
	}
}

// intakeLLMID es la solicitud del pipeline que congelan los golden.
const intakeLLMID = "77777777-7777-7777-7777-777777777777"

// intakeGoldenPayload es el payload §7.4 de la revisión `interpreted` de los golden, en el orden
// en que lo emite el pipeline. Está escrito a mano porque el productor (captación) sigue siendo
// código viejo hasta F7 y la cara no puede importarlo.
const intakeGoldenPayload = `{"version":1,` +
	`"source_text":"hola! quiero una torta de 10 o 12 porciones para el sábado y 2 kilos de tequeños",` +
	`"message_ts":"2026-08-01T09:55:00Z",` +
	`"analysis":{"provider":"local","model":"qwen3:8b","source":"event_thread","reanalyzed_from":null},` +
	`"delivery_date":"2026-08-08","lines":[` +
	`{"kind":"matched","sku":"TORTA-CHOC","label":"Torta de chocolate","qty":1,"unit_price":null,"variant_options":[` +
	`{"sku":"TORTA-CHOC#V1","label":"Torta de chocolate — 10 porciones","price":18000},` +
	`{"sku":"TORTA-CHOC#V2","label":"Torta de chocolate — 12 porciones","price":21000}],` +
	`"match":{"strategy":"variante","confidence":1},"evidence":"una torta de 10 o 12 porciones"},` +
	`{"kind":"unmatched","label":"tequeños","qty":2,"unit_price":null,"evidence":"2 kilos de tequeños"}],` +
	`"suggested_questions":["¿La torta la prefieres de 10 o de 12 porciones?",` +
	`"Los tequeños los tenemos por bandeja, ¿te sirven 2 bandejas?"]}`

// intakeGoldenBody siembra esa solicitud en el almacén en memoria del módulo, la sirve con el
// servicio REAL —el literal sale y vuelve por la misma partición que en producción— y devuelve
// el cuerpo del detalle indentado. Indentar no reordena claves ni reescribe valores: el golden
// sigue siendo el cuerpo real del cable.
func intakeGoldenBody(t *testing.T, features ...string) []byte {
	t.Helper()
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := intakes.NewMemoryStore()
	store.SetClock(intakeClock)
	store.Add(tenantA, intakes.Intake{
		ID: intakeLLMID, ContactID: intakeContact, SessionID: "sess-a",
		Status: intakes.StatusPendingApproval, Total: 21000, CustomerNote: "dejar en portería",
		CreatedAt: at, UpdatedAt: at,
	}, intakes.Item{SKU: "TORTA-CHOC#V2", Label: "Torta de chocolate — 12 porciones", Qty: 1, UnitPrice: 21000})
	if _, err := store.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: intakeLLMID, Kind: intakes.RevisionKindInterpreted, Payload: json.RawMessage(intakeGoldenPayload),
		RenderedText: "Torta de chocolate — 12 porciones · 1 × $21.000", CreatedBy: intakes.RevisionBySystem, CreatedAt: at,
	}); err != nil {
		t.Fatalf("sembrando la revisión interpretada: %v", err)
	}

	h := apipublicahelpertest.New(t)
	cara := intakeCara(h.Common(), intakeDeps(intakes.NewService(store), append(features, entitlements.FeatureCartBasic)...))
	rec := h.Call(cara, h.With(tenantA, intakeReadPerm), http.MethodGet, intakesTarget+"/"+intakeLLMID, "")
	wantCode(t, "detalle del golden", rec, http.StatusOK)
	var out bytes.Buffer
	if err := json.Indent(&out, rec.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("el cuerpo no es JSON (%s): %v", rec.Body, err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// intakeWantGolden compara el cuerpo con el golden. NO hay modo de regenerarlo: si falla, la
// pregunta es si el cambio del contrato es deliberado, y entonces el fichero se edita a mano.
func intakeWantGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	//nolint:gosec // G304: la ruta es un nombre de golden bajo testdata/, no entrada externa
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("leyendo el golden %s: %v", name, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("el golden %s NO coincide.\n--- golden ---\n%s\n--- obtenido ---\n%s", name, want, got)
	}
}

// intakeTreeObject exige que v sea un objeto del árbol JSON genérico.
func intakeTreeObject(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s no es un objeto JSON: %v", what, v)
	}
	return obj
}

// intakeTreeList exige que v sea una lista del árbol JSON genérico.
func intakeTreeList(t *testing.T, v any, what string) []any {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("%s no es una lista JSON: %v", what, v)
	}
	return list
}

// TestMountIntakes_LLMGate_Goldens: la PAREJA es el gate. Los dos ficheros salen de la misma
// solicitud, así que su diferencia es, literalmente, lo que la feature paga.
func TestMountIntakes_LLMGate_Goldens(t *testing.T) {
	with := intakeGoldenBody(t, entitlements.FeatureLLMIntake)
	without := intakeGoldenBody(t)
	intakeWantGolden(t, "intake_detail_with_llm_intake.golden.json", with)
	intakeWantGolden(t, "intake_detail_without_llm_intake.golden.json", without)

	// Y difieren SOLO en los dos campos. Sin esto, un gate roto que dejara pasar todo se
	// congelaría en los dos ficheros a la vez. La poda de aquí es INDEPENDIENTE de la de
	// producción (recorre el árbol genérico): comparar el filtro contra sí mismo no probaría nada.
	if bytes.Equal(with, without) {
		t.Fatal("el detalle con y sin llm_intake es IDÉNTICO: el gate por campo no filtra nada")
	}
	var tree map[string]any
	if err := json.Unmarshal(with, &tree); err != nil {
		t.Fatalf("el cuerpo con llm_intake no es JSON: %v", err)
	}
	removed := 0
	for _, r := range intakeTreeList(t, tree["revisions"], "revisions") {
		payload := intakeTreeObject(t, intakeTreeObject(t, r, "una revisión")["payload"], "el payload")
		if _, ok := payload["suggested_questions"]; ok {
			delete(payload, "suggested_questions")
			removed++
		}
		for _, l := range intakeTreeList(t, payload["lines"], "lines") {
			line := intakeTreeObject(t, l, "una línea")
			if _, ok := line["variant_options"]; ok {
				delete(line, "variant_options")
				removed++
			}
		}
	}
	if removed != 2 {
		t.Fatalf("del cuerpo con llm_intake se quitaron %d campos del pipeline, quiero 2 (la raíz y una línea)", removed)
	}
	pruned, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("reserializando el árbol: %v", err)
	}
	if !intakeSameJSON(t, pruned, without) {
		t.Errorf("el cuerpo sin llm_intake difiere en algo MÁS que los dos campos:\n%s", strings.TrimSpace(string(without)))
	}
}
