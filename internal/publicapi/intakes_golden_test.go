package publicapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// updateGoldenAPI regenera los golden de internal/publicapi/testdata en vez de
// compararlos:
//
//	go test ./internal/publicapi/ -run Golden -update
//
// 🔴 ESTOS GOLDEN SON EL CONTRATO QUE CONSUME LA APP KMP DEL PLAN 045 (T4.1,
// criterio (c)). Si uno falla, la pregunta es si el cambio del cuerpo es
// DELIBERADO: regenerarlo por costumbre borra justamente la prueba de que el
// contrato no se movió solo. Y la PAREJA —con y sin `llm_intake`— es el gate en sí:
// lo que uno tiene y el otro no es exactamente lo que la feature paga.
var updateGoldenAPI = flag.Bool("update", false, "regenera los golden de internal/publicapi/testdata")

// assertGoldenAPI compara `got` con el golden, o lo reescribe con -update. Es la
// única puerta de escritura de testdata/ en este paquete.
func assertGoldenAPI(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGoldenAPI {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("escribiendo golden %s: %v", path, err)
		}
		t.Logf("golden regenerado: %s", path)
		return
	}
	//nolint:gosec // G304: la ruta es un nombre de golden bajo testdata/, no entrada externa
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leyendo golden %s (¿falta generarlo con -update?): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s NO coincide.\n--- golden (esperado) ---\n%s\n--- obtenido ---\n%s", path, want, got)
	}
}

// cuerpoIndentado normaliza el cuerpo de una respuesta para congelarlo. Indenta y
// NADA MÁS: no reordena claves ni reescribe valores, así que el golden sigue siendo
// el cuerpo REAL del wire y no una proyección suya. El orden es determinista
// —structs de Go en orden de declaración, mapas en orden alfabético— y por eso el
// golden se puede comparar byte a byte.
func cuerpoIndentado(t *testing.T, body []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		t.Fatalf("el cuerpo no es JSON válido (%s): %v", body, err)
	}
	return buf.String() + "\n"
}

// intakeLLM es la solicitud del pipeline del 044 que congelan los golden: una en
// `pending_approval` con su revisión `interpreted`. Va aparte de las cinco de
// seedIntakes porque ninguna de ellas la produce el pipeline LLM.
const intakeLLM = "77777777-7777-7777-7777-777777777777"

// payloadInterpretado arma el payload §7.4 CON LOS STRUCTS REALES del productor
// (stages.PayloadRevision), no con un JSON escrito a mano.
//
// Eso es deliberado: un literal escrito a mano congela lo que el autor del test
// CREÍA que produce el pipeline, y envejece en silencio. Construyéndolo con el
// struct, el día que la etapa añada, quite o renombre un campo del contrato, el
// golden se pone rojo — que es exactamente lo que un contrato congelado tiene que
// hacer.
//
// La línea 1 lleva `variant_options` (dos presentaciones, `unit_price` nil porque
// elegir por el cliente es inventar) y la 2 NO las lleva: con una sola línea con
// variantes no se podría distinguir «se borraron las de todas las líneas» de «se
// borró la clave raíz y la primera línea de paso».
func payloadInterpretado(t *testing.T) json.RawMessage {
	t.Helper()
	precio := 21000.0
	rev := stages.PayloadRevision{
		Version:      intakes.RevisionPayloadVersion,
		SourceText:   "hola! quiero una torta de 10 o 12 porciones para el sábado y 2 kilos de tequeños",
		MessageTS:    time.Date(2026, 8, 1, 9, 55, 0, 0, time.UTC),
		Analysis:     stages.Analisis{Provider: "local", Model: "qwen3:8b", Source: stages.OrigenHiloDelEvento},
		DeliveryDate: "2026-08-08",
		Lines: []stages.LineaRevision{
			{Linea: stages.Linea{
				Kind:  stages.KindMatched,
				SKU:   "TORTA-CHOC",
				Label: "Torta de chocolate",
				Qty:   1,
				VariantOptions: []stages.OpcionVariante{
					{SKU: "TORTA-CHOC#V1", Label: "Torta de chocolate — 10 porciones", Price: 18000},
					{SKU: "TORTA-CHOC#V2", Label: "Torta de chocolate — 12 porciones", Price: precio},
				},
				Match:    &stages.ProcedenciaMatch{Strategy: stages.EstrategiaVariante, Confidence: 1},
				Evidence: "una torta de 10 o 12 porciones",
			}},
			{Linea: stages.Linea{
				Kind:      stages.KindUnmatched,
				Label:     "tequeños",
				Qty:       2,
				UnitPrice: nil,
				Evidence:  "2 kilos de tequeños",
			}},
		},
		SuggestedQuestions: []string{
			"¿La torta la prefieres de 10 o de 12 porciones?",
			"Los tequeños los tenemos por bandeja, ¿te sirven 2 bandejas?",
		},
	}
	raw, err := json.Marshal(rev)
	if err != nil {
		t.Fatalf("serializando el payload §7.4: %v", err)
	}
	return raw
}

// seedIntakeLLM siembra la solicitud del 044 con su revisión `interpreted`.
//
// La fecha de la revisión se fija a mano: sin eso la pone el reloj del store y el
// golden sería distinto en cada corrida.
func seedIntakeLLM(t *testing.T, st *intakes.MemoryStore) {
	t.Helper()
	st.Add(tenantA, intakes.Intake{
		ID: intakeLLM, ContactID: contactoOpaco, SessionID: "sess-a",
		Status: intakes.StatusPendingApproval, Total: 21000,
		CustomerNote: "dejar en portería",
		CreatedAt:    día(7), UpdatedAt: día(7),
	}, intakes.Item{SKU: "TORTA-CHOC#V2", Label: "Torta de chocolate — 12 porciones",
		Qty: 1, UnitPrice: 21000})

	if _, err := st.InsertRevision(context.Background(), intakes.Revision{
		IntakeID:     intakeLLM,
		Kind:         intakes.RevisionKindInterpreted,
		Payload:      payloadInterpretado(t),
		RenderedText: "Torta de chocolate — 12 porciones · 1 × $21.000",
		CreatedBy:    intakes.RevisionBySystem,
		CreatedAt:    día(7),
	}); err != nil {
		t.Fatalf("sembrar la revisión interpretada: %v", err)
	}
}

// depsIntakesLLM arma unas Deps con la bandeja sembrada y las features que se le
// pidan ENCENDIDAS para el tenant A. `cart_basic` va siempre: es la que abre las
// siete rutas desde el Plan 041 y `llm_intake` NO la sustituye (D-044.47 §1).
func depsIntakesLLM(store *intakes.MemoryStore, extra ...string) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	fake.Enable(tenantB, entitlements.FeatureCartBasic)
	for _, f := range extra {
		fake.Enable(tenantA, f)
	}
	return publicapi.Deps{Intakes: intakes.NewService(store), Entitlements: fake}
}

// detalleDe pide el detalle de una solicitud y devuelve el cuerpo crudo.
func detalleDe(t *testing.T, d publicapi.Deps, intakeID string) []byte {
	t.Helper()
	api := newAPI(d, intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakeID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// TestIntakeDetailGolden_ConLLMIntake congela el cuerpo COMPLETO del detalle para
// un tenant que sí compró el nivel: es el contrato que la app KMP del Plan 045 va a
// leer, con `suggested_questions` y `variant_options` dentro.
func TestIntakeDetailGolden_ConLLMIntake(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	body := detalleDe(t, depsIntakesLLM(store, entitlements.FeatureLLMIntake), intakeLLM)
	assertGoldenAPI(t, "intake_detail_con_llm_intake.golden.json", cuerpoIndentado(t, body))
}

// TestIntakeDetailGolden_SinLLMIntake congela el MISMO cuerpo para un tenant con
// `cart_basic` y sin `llm_intake` —el plan Basic, que existe de verdad en UAT—.
//
// 🔴 LA PAREJA DE GOLDEN ES EL GATE: este fichero y el anterior salen de la MISMA
// solicitud sembrada, así que su diff es, literalmente, lo que la feature paga. Si
// algún día los dos coinciden, el gate dejó de funcionar y nadie lo habría notado
// por los tests de conducta, que miran una clave cada uno.
func TestIntakeDetailGolden_SinLLMIntake(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	body := detalleDe(t, depsIntakesLLM(store), intakeLLM)
	assertGoldenAPI(t, "intake_detail_sin_llm_intake.golden.json", cuerpoIndentado(t, body))
}

// TestIntakeDetailGolden_LosDosDifierenSoloEnLoQuePagaLaFeature es la mitad que un
// golden por separado no puede afirmar: que los dos cuerpos NO son iguales.
//
// Sin esto, un gate roto que dejara pasar todo se congelaría en los dos ficheros a
// la vez con -update y los dos tests seguirían verdes para siempre — el modo de
// fallo clásico de un golden regenerado sin mirar.
func TestIntakeDetailGolden_LosDosDifierenSoloEnLoQuePagaLaFeature(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	con := detalleDe(t, depsIntakesLLM(store, entitlements.FeatureLLMIntake), intakeLLM)
	sin := detalleDe(t, depsIntakesLLM(store), intakeLLM)

	if bytes.Equal(con, sin) {
		t.Fatal("el detalle con y sin llm_intake es IDÉNTICO: el gate por campo no está filtrando nada")
	}
	// Y la diferencia es SOLO la de los dos campos: todo lo demás del contrato del
	// 041 —cabecera, líneas, transiciones, revisiones— tiene que seguir igual.
	// Los dos lados pasan por el MISMO round-trip a árbol genérico, así que la
	// comparación mira el contenido y no el orden en que cada uno serializó.
	conSinCampos := arbolJSON(t, con)
	quitarCamposLLM(t, conSinCampos)
	if reserializar(t, conSinCampos) != reserializar(t, arbolJSON(t, sin)) {
		t.Fatalf("el cuerpo sin llm_intake difiere en algo MÁS que los dos campos del 044.\ncon (sin los campos)=%s\nsin=%s",
			reserializar(t, conSinCampos), reserializar(t, arbolJSON(t, sin)))
	}
}

// arbolJSON decodifica un cuerpo a un árbol genérico.
func arbolJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var raiz map[string]any
	if err := json.Unmarshal(body, &raiz); err != nil {
		t.Fatalf("cuerpo no es JSON (%s): %v", body, err)
	}
	return raiz
}

// reserializar vuelve a JSON un árbol genérico, con las claves de cada objeto en
// orden alfabético (lo hace encoding/json con los mapas). Es lo que permite
// comparar dos cuerpos por CONTENIDO.
func reserializar(t *testing.T, arbol map[string]any) string {
	t.Helper()
	out, err := json.MarshalIndent(arbol, "", "  ")
	if err != nil {
		t.Fatalf("reserializando: %v", err)
	}
	return string(out)
}

// quitarCamposLLM borra los dos campos del 044 del árbol, in situ. Es una
// implementación INDEPENDIENTE de la de producción —recorre el árbol genérico— a
// propósito: comparar el filtro contra sí mismo no probaría nada.
func quitarCamposLLM(t *testing.T, raiz map[string]any) {
	t.Helper()
	for _, r := range listaDe(t, raiz["revisions"], "revisions") {
		payload, hay := objetoDe(t, r, "una revisión")["payload"]
		if !hay {
			continue
		}
		obj := objetoDe(t, payload, "el payload de una revisión")
		delete(obj, "suggested_questions")
		lineas, hayLineas := obj["lines"]
		if !hayLineas {
			continue
		}
		for _, l := range listaDe(t, lineas, "lines") {
			delete(objetoDe(t, l, "una línea"), "variant_options")
		}
	}
}
