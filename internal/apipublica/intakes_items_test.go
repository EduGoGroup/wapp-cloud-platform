package apipublica_test

// intakes_items_test.go — G4 (PUT /api/v1/intakes/{id}/items) del contrato de MountIntakes: la
// edición manual de las líneas del presupuesto, que es también la acción «Corregir» del 044. Los
// dobles están en intakes_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	intakeItemsTarget = intakeTarget + "/items"

	intakeMsgItemsMissing = "items es obligatorio (manda [] para dejar la solicitud sin líneas)"
	intakeMsgLabelLong    = "la etiqueta pasa del máximo de 280 caracteres"
	intakeMsgCustomLong   = "la personalización pasa del máximo de 280 caracteres"
	intakeMsgLabelEmpty   = "la etiqueta es obligatoria: es lo que se lee en el pedido, en la comanda y en el CSV"
	intakeMsgSKUEmpty     = "el sku es obligatorio: es lo que identifica al artículo en el pedido"
	intakeMsgQty          = "la cantidad tiene que ser 1 o más; para quitar la línea, mándala fuera de la lista"
)

// intakeInvalidItems es el cuerpo del 400 por líneas mal formadas.
type intakeInvalidItems struct {
	Error  string               `json:"error"`
	Errors []intakes.LineDefect `json:"errors"`
}

// TestMountIntakes_Items_SanitizesAndPassesTheLines: lo que llega al servicio es el conjunto que
// mandó el dueño con el sku recortado y el texto libre SANEADO por la puerta del carrito —el
// salto de línea y el tabulador se vuelven UN espacio (si no, rompen la celda del CSV y parten la
// comanda) y lo invisible se cae—. El precio viaja en el cuerpo y no se resuelve contra nada.
func TestMountIntakes_Items_SanitizesAndPassesTheLines(t *testing.T) {
	body := `{"items":[` +
		`{"sku":"  HAMB ","label":"Hamburguesa\ndoble","customization":"sin\tcebolla  \u200by sin  maní","qty":2,"unit_price":8.5},` +
		`{"sku":"QUESO-X","label":"  Queso extra  ","qty":1,"unit_price":0,"added_at":"2020-01-01T00:00:00Z"},` +
		`{"sku":"HAMB","label":"Hamburguesa","customization":"\u200b\u200b","qty":1,"unit_price":8.5}]}`
	svc := &intakeServiceSpy{detail: intakeDetailFixture()}
	rec := intakeDo(t, svc, http.MethodPut, intakeItemsTarget, body)
	wantCode(t, "G4", rec, http.StatusOK)
	wantExactBody(t, "G4", rec, intakeDetailBody)
	intakeWantCalls(t, "G4", svc, "ReplaceItems")
	want := []intakes.Item{
		{SKU: "HAMB", Label: "Hamburguesa doble", Customization: "sin cebolla y sin maní", Qty: 2, UnitPrice: 8.5},
		{SKU: "QUESO-X", Label: "Queso extra", Qty: 1, UnitPrice: 0},
		{SKU: "HAMB", Label: "Hamburguesa", Qty: 1, UnitPrice: 8.5},
	}
	if !reflect.DeepEqual(svc.items, want) {
		t.Errorf("ReplaceItems recibió\n  %+v\nquiero\n  %+v", svc.items, want)
	}
	if svc.mode != intakes.EditPlain {
		t.Errorf("sin as_correction el modo es %d, quiero EditPlain", svc.mode)
	}
}

// TestMountIntakes_Items_EmptyListIsApplied: `"items":[]` quita todas las líneas y SÍ se aplica;
// es la clave ausente (o null) la que se rechaza, porque un {} por un fallo de la UI no puede
// vaciar el presupuesto en silencio.
func TestMountIntakes_Items_EmptyListIsApplied(t *testing.T) {
	svc := &intakeServiceSpy{}
	wantCode(t, "items vacío", intakeDo(t, svc, http.MethodPut, intakeItemsTarget, `{"items":[]}`), http.StatusOK)
	intakeWantCalls(t, "items vacío", svc, "ReplaceItems")
	if len(svc.items) != 0 {
		t.Errorf("ReplaceItems recibió %+v, quiero ninguna línea", svc.items)
	}

	for body, msg := range map[string]string{
		"":                                    intakeMsgBadJSON,
		"{no es json":                         intakeMsgBadJSON,
		`[]`:                                  intakeMsgBadJSON,
		`{"items":{}}`:                        intakeMsgBadJSON,
		`{"items":"HAMB"}`:                    intakeMsgBadJSON,
		`{"items":[{"qty":"dos"}]}`:           intakeMsgBadJSON,
		`{"items":[{"qty":1.5}]}`:             intakeMsgBadJSON,
		`{"items":[],"as_correction":"true"}`: intakeMsgBadJSON,
		`{}`:                                  intakeMsgItemsMissing,
		`null`:                                intakeMsgItemsMissing,
		`{"items":null}`:                      intakeMsgItemsMissing,
		`{"lines":[]}`:                        intakeMsgItemsMissing,
		`{"as_correction":true}`:              intakeMsgItemsMissing,
	} {
		svc := &intakeServiceSpy{}
		rec := intakeDo(t, svc, http.MethodPut, intakeItemsTarget, body)
		wantCode(t, body, rec, http.StatusBadRequest)
		wantErrorBody(t, body, rec, msg)
		intakeWantCalls(t, body, svc)
	}
}

// TestMountIntakes_Items_AsCorrectionIsTheOnlySwitch: el modo es EditAsCorrection SOLO con
// `"as_correction": true`. Un cliente del 041, que nunca manda el campo, no ve cambiar nada.
func TestMountIntakes_Items_AsCorrectionIsTheOnlySwitch(t *testing.T) {
	for body, want := range map[string]intakes.EditMode{
		`{"items":[],"as_correction":true}`:  intakes.EditAsCorrection,
		`{"items":[],"as_correction":false}`: intakes.EditPlain,
		`{"items":[],"as_correction":null}`:  intakes.EditPlain,
		`{"items":[]}`:                       intakes.EditPlain,
		`{"items":[],"correction":true}`:     intakes.EditPlain,
	} {
		svc := &intakeServiceSpy{}
		wantCode(t, body, intakeDo(t, svc, http.MethodPut, intakeItemsTarget, body), http.StatusOK)
		if svc.mode != want {
			t.Errorf("%s: modo %d, quiero %d", body, svc.mode, want)
		}
	}
}

// TestMountIntakes_Items_TooLongTextIsRejectedNotTruncated: recortar «…y sin maní» pierde justo
// el alérgeno. Los defectos salen TODOS de una vez —los del saneo y los del dominio—, ordenados
// por línea, y el servicio no se toca. 280 runas justas pasan.
func TestMountIntakes_Items_TooLongTextIsRejectedNotTruncated(t *testing.T) {
	long := strconv.Quote(strings.Repeat("ñ", 281))
	exact := strings.Repeat("ñ", 280)

	svc := &intakeServiceSpy{}
	rec := intakeDo(t, svc, http.MethodPut, intakeItemsTarget,
		`{"items":[{"sku":"A","label":`+strconv.Quote(exact)+`,"customization":`+strconv.Quote(exact)+`,"qty":1,"unit_price":1}]}`)
	wantCode(t, "280 runas", rec, http.StatusOK)
	if len(svc.items) != 1 || svc.items[0].Label != exact || svc.items[0].Customization != exact {
		t.Errorf("280 runas justas no llegaron enteras al servicio")
	}

	svc = &intakeServiceSpy{}
	rec = intakeDo(t, svc, http.MethodPut, intakeItemsTarget, `{"items":[`+
		`{"sku":"A","label":"Bien","qty":0,"unit_price":1},`+
		`{"sku":" ","label":`+long+`,"customization":`+long+`,"qty":1,"unit_price":1},`+
		`{"sku":"C","label":"Bien","customization":`+long+`,"qty":1,"unit_price":1}]}`)
	wantCode(t, "281 runas", rec, http.StatusBadRequest)
	intakeWantCalls(t, "281 runas", svc)
	var got intakeInvalidItems
	wantJSON(t, "281 runas", rec, &got)
	want := []intakes.LineDefect{
		{Index: 0, Field: "qty", Message: intakeMsgQty},
		{Index: 1, Field: "label", Message: intakeMsgLabelLong},
		{Index: 1, Field: "customization", Message: intakeMsgCustomLong},
		{Index: 1, Field: "sku", Message: intakeMsgSKUEmpty},
		// El texto que no pasó el saneo llega vacío a la validación del dominio.
		{Index: 1, Field: "label", Message: intakeMsgLabelEmpty},
		{Index: 2, Field: "customization", Message: intakeMsgCustomLong},
	}
	if got.Error != "invalid_items" || !reflect.DeepEqual(got.Errors, want) {
		t.Errorf("400 = %q con defectos\n  %+v\nquiero invalid_items con\n  %+v", got.Error, got.Errors, want)
	}
}

// TestMountIntakes_Items_Errors: la política de códigos de G4. El 422 dice dónde está la
// solicitud y desde dónde SÍ se edita, que es lo que la consola necesita para ofrecer
// «re-presupuestar» en vez de un error sin salida.
func TestMountIntakes_Items_Errors(t *testing.T) {
	defects := &intakes.InvalidItemsError{Defects: []intakes.LineDefect{{Index: 0, Field: "qty", Message: intakeMsgQty}}}
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not_found_is_404_never_403", intakes.ErrNotFound, http.StatusNotFound, `{"error":"solicitud no encontrada"}`},
		{"domain_defects_are_400", defects, http.StatusBadRequest,
			`{"error":"invalid_items","errors":[{"index":0,"field":"qty","message":"` + intakeMsgQty + `"}]}`},
		{"too_many_items_is_400", &intakes.TooManyItemsError{Count: 201, Max: 200}, http.StatusBadRequest,
			`{"error":"la edición trae 201 líneas y el máximo es 200"}`},
		{"not_editable_is_422", &intakes.NotEditableError{Status: "confirmed"}, http.StatusUnprocessableEntity,
			`{"error":"not_editable","status":"confirmed","editable_in":["pending_approval"]}`},
		{"wrapped_not_editable", fmt.Errorf("servicio: %w", &intakes.NotEditableError{Status: "needs_info"}), http.StatusUnprocessableEntity,
			`{"error":"not_editable","status":"needs_info","editable_in":["pending_approval"]}`},
		{"conflict_is_409", intakes.ErrConflict, http.StatusConflict, `{"error":"` + intakeMsgConflict + `"}`},
		// El rechazo del ciclo de vida no es de esta puerta: no tiene código propio aquí.
		{"transition_error_is_500", &intakes.TransitionError{From: "a", To: "b"}, http.StatusInternalServerError,
			`{"error":"no se pudieron guardar las líneas de la solicitud"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := intakeDo(t, &intakeServiceSpy{err: tc.err}, http.MethodPut, intakeItemsTarget, `{"items":[{"sku":"A","label":"A","qty":1,"unit_price":1}]}`)
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.body)
		})
	}
}

// intakeLastRevision devuelve la última revisión del detalle de un cuerpo de respuesta.
func intakeLastRevision(t *testing.T, body []byte) (kind, createdBy string, payload map[string]json.RawMessage) {
	t.Helper()
	var detail struct {
		Revisions []struct {
			Kind      string                     `json:"kind"`
			CreatedBy string                     `json:"created_by"`
			Payload   map[string]json.RawMessage `json:"payload"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(body, &detail); err != nil || len(detail.Revisions) == 0 {
		t.Fatalf("el detalle no trae revisiones (%s): %v", body, err)
	}
	last := detail.Revisions[len(detail.Revisions)-1]
	return last.Kind, last.CreatedBy, last.Payload
}

// TestMountIntakes_Items_WithTheRealService: la escena del queso extra con el servicio REAL del
// módulo y un tenant Basic (sin LLM de por medio, ni un 403 por el camino). Cada PUT deja SU
// revisión `corrected` de `owner` —idempotente en datos, no en auditoría—; con `as_correction`
// la revisión lleva la señal y sin él, ni una de sus claves; y un pedido `confirmed` no se edita.
func TestMountIntakes_Items_WithTheRealService(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.SetClock(intakeClock)
	pending, confirmed := intakeDue(), intakeDue()
	pending.ID, pending.Total = "ped-1", 8
	confirmed.ID, confirmed.Status = "ped-2", intakes.StatusConfirmed
	store.Add(tenantA, pending, intakes.Item{SKU: "HAMB", Label: "Hamburguesa", Qty: 1, UnitPrice: 8})
	store.Add(tenantA, confirmed)

	h := apipublicahelpertest.New(t)
	cara := intakeCara(h.Common(), intakeDeps(intakes.NewService(store), entitlements.FeatureCartBasic))
	token := h.With(tenantA, intakeWritePerm)
	const lines = `"items":[{"sku":"HAMB","label":"Hamburguesa","qty":1,"unit_price":8},{"sku":"QUESO-X","label":"Queso extra","qty":1,"unit_price":1}]`

	rec := h.Call(cara, token, http.MethodPut, intakesTarget+"/ped-1/items", `{`+lines+`}`)
	wantCode(t, "primer PUT", rec, http.StatusOK)
	var detail struct {
		Total float64 `json:"total"`
		Items []struct {
			SKU string `json:"sku"`
		} `json:"items"`
	}
	wantJSON(t, "primer PUT", rec, &detail)
	if detail.Total != 9 || len(detail.Items) != 2 {
		t.Errorf("tras el PUT: total %v y %d líneas, quiero 9 y 2", detail.Total, len(detail.Items))
	}
	kind, by, payload := intakeLastRevision(t, rec.Body.Bytes())
	if kind != intakes.RevisionKindCorrected || by != intakes.RevisionByOwner {
		t.Errorf("la revisión es %s de %s, quiero corrected de owner", kind, by)
	}
	for _, key := range []string{intakes.KeyAsCorrection, intakes.KeyCorrectsRevisionNo, intakes.KeyCorrectsKind} {
		if _, ok := payload[key]; ok {
			t.Errorf("sin as_correction la revisión lleva la clave %q de la señal", key)
		}
	}

	rec = h.Call(cara, token, http.MethodPut, intakesTarget+"/ped-1/items", `{`+lines+`,"as_correction":true}`)
	wantCode(t, "segundo PUT", rec, http.StatusOK)
	if _, _, payload = intakeLastRevision(t, rec.Body.Bytes()); string(payload[intakes.KeyAsCorrection]) != "true" {
		t.Errorf("con as_correction la revisión no lleva la señal: %v", payload)
	}
	if n := len(store.Revisions("ped-1")); n != 2 {
		t.Errorf("dos ediciones dejaron %d revisiones, quiero 2: son dos actos del dueño", n)
	}

	rec = h.Call(cara, token, http.MethodPut, intakesTarget+"/ped-2/items", `{`+lines+`}`)
	wantCode(t, "editar un confirmed", rec, http.StatusUnprocessableEntity)
	wantExactBody(t, "editar un confirmed", rec, `{"error":"not_editable","status":"confirmed","editable_in":["pending_approval"]}`)

	rec = h.Call(cara, h.With(apipublicahelpertest.TenantB, intakeWritePerm), http.MethodPut, intakesTarget+"/ped-1/items", `{`+lines+`}`)
	wantCode(t, "editar lo de otro tenant", rec, http.StatusNotFound)
	if detail, err := store.Get(context.Background(), tenantA, "ped-1"); err != nil || len(detail.Items) != 2 {
		t.Errorf("el PUT del tenant B tocó la solicitud del A: %+v, %v", detail.Items, err)
	}
}
