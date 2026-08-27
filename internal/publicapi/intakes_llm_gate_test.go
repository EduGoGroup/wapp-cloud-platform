package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// revisionesDelDetalle decodifica el detalle y devuelve los payloads de sus
// revisiones como árboles genéricos: lo que hay que mirar es la FORMA que viaja por
// el cable, no un struct que ya sabe qué campos existen.
func revisionesDelDetalle(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var detail struct {
		Revisions []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("unmarshal del detalle: %v; body=%s", err, body)
	}
	out := make([]map[string]any, 0, len(detail.Revisions))
	for _, rev := range detail.Revisions {
		var payload map[string]any
		if err := json.Unmarshal(rev.Payload, &payload); err != nil {
			t.Fatalf("payload crudo no es un objeto JSON (%s): %v", rev.Payload, err)
		}
		out = append(out, payload)
	}
	return out
}

// TestIntakeDetail_SinLLMIntake_OcultaLosDosCamposDelPipeline es el criterio (a) de
// T4.1: un tenant con `cart_basic` y sin `llm_intake` —el plan Basic, real en UAT—
// sigue viendo lo del 041, y NO ve los dos campos del pipeline del 044.
//
// La ruta responde 200 y no 403: el gate va sobre los CAMPOS, no sobre la puerta
// (D-044.47 §1). Un 403 rompería una pantalla que el cliente ya paga.
func TestIntakeDetail_SinLLMIntake_OcultaLosDosCamposDelPipeline(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	body := detalleDe(t, depsIntakesLLM(store), intakeLLM)

	payloads := revisionesDelDetalle(t, body)
	if len(payloads) != 1 {
		t.Fatalf("revisiones=%d, quiero 1", len(payloads))
	}
	payload := payloads[0]

	if _, hay := payload["suggested_questions"]; hay {
		t.Fatalf("suggested_questions viaja al wire SIN llm_intake: %v", payload["suggested_questions"])
	}
	lineas := listaDe(t, payload["lines"], "lines")
	if len(lineas) != 2 {
		t.Fatalf("lines=%d, quiero las 2 sembradas (¿se perdieron al filtrar?)", len(lineas))
	}
	for i, l := range lineas {
		linea := objetoDe(t, l, "la línea")
		if _, hay := linea["variant_options"]; hay {
			t.Fatalf("variant_options viaja al wire SIN llm_intake en la línea %d", i)
		}
	}

	// Y lo del 041 sigue entero: el gate quita DOS campos, no poda el payload.
	// Sin esta mitad, un filtro que devolviera `{}` pasaría el test de arriba.
	for _, clave := range []string{"version", "analysis", "delivery_date", "message_ts", "source_text"} {
		if _, hay := payload[clave]; !hay {
			t.Fatalf("el filtro se llevó por delante %q, que no es del gate", clave)
		}
	}
	primera := objetoDe(t, lineas[0], "la línea 1")
	if primera["sku"] != "TORTA-CHOC" || primera["label"] != "Torta de chocolate" {
		t.Fatalf("la línea 1 llegó alterada: %v", primera)
	}
}

// TestIntakeDetail_SinLLMIntake_LaClaveDESAPARECE_NoQuedaEnListaVacia fija LA
// decisión de contrato de esta tarea, y por eso es un test propio y no una línea
// dentro de otro.
//
// `suggested_questions` no lleva `omitempty` en stages.PayloadRevision, así que al
// filtrarla había dos salidas: borrar la clave o dejarla en `[]`. Se BORRA, porque
// `[]` YA SIGNIFICA otra cosa en este contrato —«no hay nada que preguntar»— y
// servírsela a un tenant sin la feature le contaría esa respuesta en vez de la
// verdad, que es «este servidor no te publica ese campo». D-044.47 §1 lo dice
// igual: «no aparecen en el cuerpo».
func TestIntakeDetail_SinLLMIntake_LaClaveDESAPARECE_NoQuedaEnListaVacia(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	payload := revisionesDelDetalle(t, detalleDe(t, depsIntakesLLM(store), intakeLLM))[0]

	valor, hay := payload["suggested_questions"]
	if hay {
		t.Fatalf("la clave sigue en el cuerpo con valor %#v; el contrato es que DESAPAREZCA, no que quede vacía", valor)
	}
}

// TestIntakeDetail_ConLLMIntake_LosPublica es la otra mitad del criterio, y la que
// convierte «no aparecen» en un gate y no en un borrado: con la feature, los dos
// campos salen POBLADOS.
func TestIntakeDetail_ConLLMIntake_LosPublica(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	payload := revisionesDelDetalle(t,
		detalleDe(t, depsIntakesLLM(store, entitlements.FeatureLLMIntake), intakeLLM))[0]

	preguntas := listaDe(t, payload["suggested_questions"], "suggested_questions")
	if len(preguntas) != 2 {
		t.Fatalf("suggested_questions=%v, quiero las 2 sembradas", payload["suggested_questions"])
	}
	lineas := listaDe(t, payload["lines"], "lines")
	primera := objetoDe(t, lineas[0], "la línea 1")
	opciones := listaDe(t, primera["variant_options"], "variant_options de la línea 1")
	if len(opciones) != 2 {
		t.Fatalf("variant_options de la línea 1 = %v, quiero las 2 presentaciones", primera["variant_options"])
	}
	// La línea 2 no tenía variantes y sigue sin tenerlas: `omitempty` decide eso, no
	// el gate. Si apareciera, el filtro estaría INVENTANDO la clave al reserializar.
	segunda := objetoDe(t, lineas[1], "la línea 2")
	if _, hay := segunda["variant_options"]; hay {
		t.Fatalf("la línea 2 no tenía variant_options y ahora las trae: %v", segunda["variant_options"])
	}
}

// resolverQueFalla dice que sí a `cart_basic` (para que la ruta se abra y el test
// llegue al handler) y se cae al preguntar por `llm_intake`.
//
// Hace falta un doble propio y no el Fake.Err: ese error corta ANTES, en
// RequireFeature, y el test se quedaría en el 403 sin llegar nunca al filtro.
type resolverQueFalla struct{ entitlements.Resolver }

func (r resolverQueFalla) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	if feature == entitlements.FeatureLLMIntake {
		return false, errors.New("resolver caído")
	}
	return r.Resolver.Has(ctx, tenantID, feature)
}

// TestIntakeDetail_ResolverCaido_TAPA_IgualQueRequireFeature: fail-closed.
//
// Un fallo transitorio al resolver la feature no puede abrir un campo de pago. Es
// la misma política que entitlements.RequireFeature aplica a la puerta, y tiene que
// ser la misma en el campo: si no, bastaría con esperar a que el resolver hipara
// para leer lo que no se compró.
func TestIntakeDetail_ResolverCaido_TAPA_IgualQueRequireFeature(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)

	base := entitlements.NewFake()
	base.Enable(tenantA, entitlements.FeatureCartBasic)
	base.Enable(tenantA, entitlements.FeatureLLMIntake) // la TIENE, pero no se puede averiguar
	deps := publicapi.Deps{Intakes: intakes.NewService(store), Entitlements: resolverQueFalla{base}}

	payload := revisionesDelDetalle(t, detalleDe(t, deps, intakeLLM))[0]
	if _, hay := payload["suggested_questions"]; hay {
		t.Fatal("con el resolver caído el campo salió igual: el gate NO es fail-closed")
	}
}

// TestPutIntakeItems_SinLLMIntake_TambienOculta cierra la SEGUNDA puerta por la que
// sale el mismo cuerpo.
//
// El PUT de líneas (041 · T4.10) responde el detalle completo para que la consola
// repinte sin un segundo GET. Si el gate solo estuviera en el GET, editar una línea
// devolvería por el PUT exactamente lo que el GET acaba de tapar — y la fuga
// seguiría abierta con el test del GET en verde.
func TestPutIntakeItems_SinLLMIntake_TambienOculta(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)
	api := newAPI(depsIntakesLLM(store), intakesKeys())

	cuerpo := `{"items":[{"sku":"TORTA-CHOC#V2","label":"Torta de chocolate — 12 porciones","customization":"","qty":1,"unit_price":21000}]}`
	rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeLLM+"/items", cuerpo)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	for i, payload := range revisionesDelDetalle(t, rec.Body.Bytes()) {
		if _, hay := payload["suggested_questions"]; hay {
			t.Fatalf("el PUT devolvió suggested_questions en la revisión %d sin llm_intake", i+1)
		}
		// El PUT deja DOS revisiones con formas DISTINTAS y las dos se revisan: la
		// `interpreted` del pipeline trae `lines` (§7.4) y la `corrected` que acaba
		// de escribir el 041 trae `items` (intakes.RevisionPayload). La segunda no
		// tiene dónde llevar `variant_options`, así que su ausencia de `lines` es el
		// caso normal y no un filtrado.
		lineas, hayLineas := payload["lines"]
		if !hayLineas {
			continue
		}
		for j, l := range listaDe(t, lineas, "lines") {
			if _, hay := objetoDe(t, l, "la línea")["variant_options"]; hay {
				t.Fatalf("el PUT devolvió variant_options (revisión %d, línea %d) sin llm_intake", i+1, j)
			}
		}
	}
}

// TestIntakeDetail_SinLLMIntake_UnaRevisionAjenaNoSeToca: la revisión `cart` que
// escribe el cierre del carrito (041) no tiene ninguna de las dos claves, así que
// el gate no debe REESCRIBIRLA — ni siquiera reordenando sus claves.
//
// Importa porque un filtro que reserializara siempre dejaría a los tenants sin
// `llm_intake` viendo un cuerpo con las claves en otro orden que el que ven los
// demás: dos planes, dos cuerpos del MISMO dato, sin que nadie lo hubiera decidido.
func TestIntakeDetail_SinLLMIntake_UnaRevisionAjenaNoSeToca(t *testing.T) {
	store := seedIntakes()
	// Orden de claves DELIBERADAMENTE no alfabético: si el gate reserializara,
	// saldría ordenado y la comparación byte a byte lo cazaría.
	crudo := `{"version":1,"total":48000,"items":[{"sku":"torta-v1","qty":1}]}`
	if _, err := store.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: intakeA1, Kind: intakes.RevisionKindCart,
		Payload: json.RawMessage(crudo), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("sembrar revisión: %v", err)
	}

	var detail struct {
		Revisions []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"revisions"`
	}
	body := detalleDe(t, depsIntakesLLM(store), intakeA1)
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("unmarshal del detalle: %v", err)
	}
	if got := string(detail.Revisions[0].Payload); got != crudo {
		t.Fatalf("la revisión `cart` salió REESCRITA por el gate.\n got=%s\nquiero=%s", got, crudo)
	}
}

// TestGateLLM_LasClavesSonLasDelContrato ata las dos claves que el gate borra a las
// etiquetas JSON REALES del productor.
//
// El filtro de producción usa literales a propósito (el contrato es del cable, no
// del struct de Go), y eso deja una grieta: si alguien renombra el campo en
// `stages`, el pipeline empezaría a emitir otra clave y el gate seguiría borrando
// la vieja — la fuga volvería con todos los tests de conducta en verde, porque
// sembrarían el payload con el nombre nuevo y comprobarían el viejo. Este test es
// el que cierra esa grieta: mira las etiquetas por reflexión.
func TestGateLLM_LasClavesSonLasDelContrato(t *testing.T) {
	etiqueta := func(tipo reflect.Type, campo string) string {
		t.Helper()
		f, ok := tipo.FieldByName(campo)
		if !ok {
			t.Fatalf("%s no tiene el campo %s: el contrato §7.4 cambió de forma", tipo, campo)
		}
		nombre, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		return nombre
	}

	if got := etiqueta(reflect.TypeOf(stages.PayloadRevision{}), "SuggestedQuestions"); got != "suggested_questions" {
		t.Fatalf("stages.PayloadRevision.SuggestedQuestions es %q y el gate borra \"suggested_questions\": actualiza internal/publicapi/intakes_llm_gate.go", got)
	}
	if got := etiqueta(reflect.TypeOf(stages.Linea{}), "VariantOptions"); got != "variant_options" {
		t.Fatalf("stages.Linea.VariantOptions es %q y el gate borra \"variant_options\": actualiza internal/publicapi/intakes_llm_gate.go", got)
	}

	// Y el nivel importa tanto como el nombre: `suggested_questions` es RAÍZ y
	// `variant_options` va dentro de CADA línea. Un filtro de dos claves planas
	// sería incorrecto, así que el test afirma también dónde vive cada una.
	if _, ok := reflect.TypeOf(stages.PayloadRevision{}).FieldByName("VariantOptions"); ok {
		t.Fatal("variant_options subió a la raíz del payload: el gate la busca dentro de lines[]")
	}
	if _, ok := reflect.TypeOf(stages.Linea{}).FieldByName("SuggestedQuestions"); ok {
		t.Fatal("suggested_questions bajó a la línea: el gate la busca en la raíz")
	}
}

// objetoDe y listaDe hacen la aserción de tipo sobre un árbol JSON genérico
// FALLANDO el test si la forma no es la esperada, en vez de tragarse el `ok`.
//
// No es solo por el `check-blank` de errcheck: un `x, _ :=` que se queda en nil
// convierte «el contrato cambió de forma» en «el campo salió vacío», y un test que
// afirma sobre nil pasa por razones equivocadas.
func objetoDe(t *testing.T, v any, que string) map[string]any {
	t.Helper()
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s no es un objeto JSON: %#v", que, v)
	}
	return obj
}

func listaDe(t *testing.T, v any, que string) []any {
	t.Helper()
	lista, ok := v.([]any)
	if !ok {
		t.Fatalf("%s no es una lista JSON: %#v", que, v)
	}
	return lista
}

// TestExportYSummary_NoPublicanElPayloadDeRevision cierra el inventario de puertas
// por las que un payload de revisión podría salir al wire.
//
// Hoy NO lo publican, y lo que lo impide NO es el gate: es que los dos proyectan a
// DTOs TIPADOS que no tienen campo de revisión (`summaryIntakeDTO`, `exportRows`),
// mientras que el detalle copia `Payload` crudo — que es justo por lo que la fuga
// existía solo ahí. Poblar `Detail.Revisions` en el store no cambia estos cuerpos:
// lo verifiqué mutándolo y siguieron verdes.
//
// Lo que este test vigila es la boca que SÍ está abierta: que alguien añada el
// campo al DTO del summary o una columna de revisión al export. Ese cuerpo saldría
// SIN pasar por aplicarGateLLMIntake y todos los tests del detalle seguirían verdes.
//
// Mira el CUERPO CRUDO en vez de decodificarlo: lo que importa no es qué campo es,
// sino que esas cadenas no aparezcan por ninguna vía. Se comprueba CON la feature
// encendida, que es el caso en el que habría algo que filtrar.
func TestExportYSummary_NoPublicanElPayloadDeRevision(t *testing.T) {
	store := seedIntakes()
	seedIntakeLLM(t, store)

	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	fake.Enable(tenantA, entitlements.FeatureIntakesExport)
	fake.Enable(tenantA, entitlements.FeatureLLMIntake)
	api := newAPI(publicapi.Deps{Intakes: intakes.NewService(store), Entitlements: fake}, intakesKeys())

	for _, ruta := range []string{
		"/api/v1/intakes/export?format=csv",
		"/api/v1/intakes/summary.json",
	} {
		rec := call(api, keyAIntakes, http.MethodGet, ruta, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d, quiero 200; body=%s", ruta, rec.Code, rec.Body.String())
		}
		cuerpo := rec.Body.String()
		for _, clave := range []string{"suggested_questions", "variant_options", "revisions"} {
			if strings.Contains(cuerpo, clave) {
				t.Fatalf("%s publica %q: ese camino NO pasa por aplicarGateLLMIntake y ahora es una fuga", ruta, clave)
			}
		}
	}
}
