package publicapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// Tests de PUT /api/v1/intakes/{id}/items — la EDICIÓN MANUAL de líneas
// (Plan 041 · T4.10, REQ-36 / D-041.26).

// intakeQueso es la solicitud de la escena de D-041.26 §e: la hamburguesa cerrada
// a $8.00 con «con queso extra» anotado y sin cobrar.
const intakeQueso = "77777777-7777-7777-7777-777777777777"

// bandejaConEscena siembra SOLO la solicitud de la escena, en `confirmed` (que es
// donde la deja el cart) con su línea personalizada.
func bandejaConEscena() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID: intakeQueso, ContactID: contactoOpaco, SessionID: "sess-a",
		Status: intakes.StatusConfirmed, Total: 8,
		CreatedAt: día(6), UpdatedAt: día(6),
	}, intakes.Item{
		SKU: "HAMB", Label: "Hamburguesa", Customization: "con queso extra",
		Qty: 1, UnitPrice: 8,
	})
	return st
}

// depsSinLLM arma unas Deps con cart_basic encendida y NINGUNA feature de LLM.
// Es la condición del criterio (c) de T4.10 tal como lo reescribió la enmienda del
// 2026-08-06: la escena entera se completa sin `llm_intent` —la única feature de
// LLM con gate real— encendida en ninguna parte.
func depsSinLLM(store *intakes.MemoryStore) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	return publicapi.Deps{Intakes: intakes.NewService(store), Entitlements: fake}
}

func decodeDetalle(t *testing.T, body []byte) intakeDetailDTO {
	t.Helper()
	var out intakeDetailDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal del detalle: %v; body=%s", err, body)
	}
	return out
}

// líneasPorSKUDTO indexa las líneas del detalle por sku (una lista por sku: dos
// líneas pueden compartirlo, D-041.20).
func líneasPorSKUDTO(d intakeDetailDTO) map[string][]intakeItemDTO {
	out := map[string][]intakeItemDTO{}
	for _, it := range d.Items {
		out[it.SKU] = append(out[it.SKU], it)
	}
	return out
}

// TestIntakeItems_EscenaDelQuesoExtra_SinLLM recorre por HTTP el paso 5 de
// D-041.26 §e, de punta a punta y con un tenant SIN ninguna feature de LLM:
//
//	confirmed → pending_approval → PUT items (queso a mano) → confirmed
//
// Ni un solo 403 por el camino, total final $9.00 y revisión `corrected` de
// `owner`. Es el criterio (b) y el (c) de T4.10 en una sola escena, que es como
// ocurre en la vida real.
func TestIntakeItems_EscenaDelQuesoExtra_SinLLM(t *testing.T) {
	api := newAPI(depsSinLLM(bandejaConEscena()), intakesKeys())

	// (ii) la transición NUEVA: vuelve a un estado editable.
	rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"pending_approval"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed → pending_approval: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	// (iii) la línea del queso, a mano. La hamburguesa viaja de vuelta con su
	// personalización: el cuerpo es el conjunto COMPLETO de líneas de cliente.
	rec = call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items", `{"items":[
		{"sku":"HAMB","label":"Hamburguesa","customization":"con queso extra","qty":1,"unit_price":8},
		{"sku":"QUESO-EX","label":"Queso extra","qty":1,"unit_price":1}
	]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT items: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())
	if detalle.Total != 9 {
		t.Fatalf("total=%v, quiero 9.00 (8 de la hamburguesa + 1 del queso)", detalle.Total)
	}
	porSKU := líneasPorSKUDTO(detalle)
	if len(porSKU["HAMB"]) != 1 || porSKU["HAMB"][0].Customization != "con queso extra" {
		t.Fatalf("la hamburguesa quedó %+v; su personalización tiene que sobrevivir", porSKU["HAMB"])
	}
	if len(porSKU["QUESO-EX"]) != 1 || porSKU["QUESO-EX"][0].UnitPrice != 1 {
		t.Fatalf("el queso quedó %+v; quiero una línea a 1.00", porSKU["QUESO-EX"])
	}

	// La revisión de la corrección viaja en la MISMA respuesta: la consola no
	// necesita un segundo GET para pintar el rastro.
	if len(detalle.Revisions) != 1 {
		t.Fatalf("revisiones=%d, quiero 1 (la corrección)", len(detalle.Revisions))
	}
	rev := detalle.Revisions[0]
	if rev.Kind != intakes.RevisionKindCorrected || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión kind=%q created_by=%q; quiero corrected/owner", rev.Kind, rev.CreatedBy)
	}
	if rev.RevisionNo != 1 {
		t.Fatalf("revision_no=%d, quiero 1", rev.RevisionNo)
	}

	// (iv) y se cierra: pending_approval → confirmed, que ya existía.
	rec = call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"confirmed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending_approval → confirmed: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var cabecera intakeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &cabecera); err != nil {
		t.Fatalf("unmarshal de la cabecera: %v", err)
	}
	if cabecera.Total != 9 {
		t.Fatalf("total tras confirmar=%v, quiero 9.00", cabecera.Total)
	}
}

// TestIntakeItems_EnvíoIntacto: la edición NO menciona la línea de envío y tras
// ella sigue habiendo EXACTAMENTE UNA, con su precio, contando en el total. Es la
// frontera con T4.3 vista desde el wire.
func TestIntakeItems_EnvíoIntacto(t *testing.T) {
	st := bandejaConEscena()
	st.SetShippingZones(tenantA, intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3})
	api := newAPI(depsSinLLM(st), intakesKeys())

	if rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"pending_approval"}`); rec.Code != http.StatusOK {
		t.Fatalf("a pending_approval: code=%d; body=%s", rec.Code, rec.Body.String())
	}

	rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items",
		`{"items":[{"sku":"HAMB","label":"Hamburguesa","qty":2,"unit_price":8}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT items: code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())

	envíos := líneasPorSKUDTO(detalle)[intakes.ShippingSKU]
	if len(envíos) != 1 {
		t.Fatalf("líneas de envío=%d, quiero 1: la edición no la duplica ni la borra", len(envíos))
	}
	if envíos[0].UnitPrice != 3 {
		t.Fatalf("el envío quedó a %v, quiero 3: la edición no le toca el precio", envíos[0].UnitPrice)
	}
	if detalle.Total != 19 {
		t.Fatalf("total=%v, quiero 19 (2×8 + 3 de envío)", detalle.Total)
	}
}

// TestIntakeItems_422_NoEditable: editar un pedido `confirmed` se rechaza diciendo
// dónde está y desde dónde SÍ se edita — que es lo que la consola necesita para
// ofrecer «re-presupuestar» en vez de un error sin salida.
func TestIntakeItems_422_NoEditable(t *testing.T) {
	api := newAPI(depsSinLLM(bandejaConEscena()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items",
		`{"items":[{"sku":"HAMB","label":"Hamburguesa","qty":1,"unit_price":8}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error      string   `json:"error"`
		Status     string   `json:"status"`
		EditableIn []string `json:"editable_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 422: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "not_editable" || body.Status != intakes.StatusConfirmed {
		t.Fatalf("cuerpo=%+v; quiero not_editable sobre confirmed", body)
	}
	if len(body.EditableIn) != 1 || body.EditableIn[0] != intakes.StatusPendingApproval {
		t.Fatalf("editable_in=%v; quiero [pending_approval]", body.EditableIn)
	}
}

// TestIntakeItems_400_LíneasInválidas: los defectos se contestan TODOS de una vez,
// con su línea y su campo. Incluye el sku reservado: la línea de envío no se edita
// por esta puerta.
func TestIntakeItems_400_LíneasInválidas(t *testing.T) {
	api := newAPI(depsSinLLM(bandejaConEscena()), intakesKeys())
	if rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"pending_approval"}`); rec.Code != http.StatusOK {
		t.Fatalf("a pending_approval: code=%d; body=%s", rec.Code, rec.Body.String())
	}

	rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items", `{"items":[
		{"sku":"","label":"","qty":0,"unit_price":-1},
		{"sku":"_shipping","label":"Envío gratis","qty":1,"unit_price":0}
	]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Errors []struct {
			Index int    `json:"index"`
			Field string `json:"field"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 400: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "invalid_items" {
		t.Fatalf("error=%q, quiero invalid_items", body.Error)
	}
	visto := map[string]bool{}
	for _, e := range body.Errors {
		visto[e.Field] = true
		if e.Index != 0 && e.Index != 1 {
			t.Fatalf("defecto en la línea %d, que no existe: %+v", e.Index, e)
		}
	}
	for _, f := range []string{"sku", "label", "qty", "unit_price"} {
		if !visto[f] {
			t.Errorf("falta el defecto del campo %q; los defectos se acumulan: %+v", f, body.Errors)
		}
	}

	// Y no se escribió nada: la solicitud sigue con su línea original.
	rec = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakeQueso, "")
	detalle := decodeDetalle(t, rec.Body.Bytes())
	if len(líneasPorSKUDTO(detalle)["HAMB"]) != 1 || len(detalle.Revisions) != 0 {
		t.Fatalf("la edición rechazada dejó rastro: items=%+v revisiones=%d", detalle.Items, len(detalle.Revisions))
	}
}

// TestIntakeItems_400_CuerpoSinItems: `{}` NO vacía el presupuesto. Distinguir la
// clave ausente de la lista vacía es lo que impide que un fallo de la UI borre las
// líneas en silencio.
func TestIntakeItems_400_CuerpoSinItems(t *testing.T) {
	api := newAPI(depsSinLLM(bandejaConEscena()), intakesKeys())
	if rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"pending_approval"}`); rec.Code != http.StatusOK {
		t.Fatalf("a pending_approval: code=%d; body=%s", rec.Code, rec.Body.String())
	}

	for _, cuerpo := range []string{`{}`, `no-es-json`} {
		rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items", cuerpo)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cuerpo %q → code=%d, quiero 400; body=%s", cuerpo, rec.Code, rec.Body.String())
		}
	}

	// La lista VACÍA sí se aplica: quitar la última línea es una edición legítima.
	// Lo que queda es la línea de la PLATAFORMA —el envío «por confirmar» que puso
	// la transición—, que no es del cliente y por eso no se va con ellas.
	rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items", `{"items":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("items=[] → code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())
	porSKU := líneasPorSKUDTO(detalle)
	if len(porSKU["HAMB"]) != 0 || len(porSKU[intakes.ShippingSKU]) != 1 || len(detalle.Items) != 1 {
		t.Fatalf("tras vaciar: items=%+v; quiero solo la línea de envío", detalle.Items)
	}
	if detalle.Total != 0 {
		t.Fatalf("total=%v, quiero 0: el envío sin zonas va a 0 «por confirmar»", detalle.Total)
	}
}

// TestIntakeItems_400_TextoLibreSaneado: la etiqueta y la personalización pasan por
// el MISMO saneo que el carrito (cart.SanitizeNote): el salto de línea se convierte
// en espacio —si no, rompe la celda del CSV y parte la comanda— y pasarse del
// límite se rechaza en vez de truncar.
func TestIntakeItems_400_TextoLibreSaneado(t *testing.T) {
	api := newAPI(depsSinLLM(bandejaConEscena()), intakesKeys())
	if rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"pending_approval"}`); rec.Code != http.StatusOK {
		t.Fatalf("a pending_approval: code=%d; body=%s", rec.Code, rec.Body.String())
	}

	rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items",
		`{"items":[{"sku":"HAMB","label":"Hamburguesa\ndoble","customization":"sin\tcebolla","qty":1,"unit_price":8}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	línea := líneasPorSKUDTO(decodeDetalle(t, rec.Body.Bytes()))["HAMB"][0]
	if línea.Label != "Hamburguesa doble" || línea.Customization != "sin cebolla" {
		t.Fatalf("línea=%+v; el saneo convierte el salto y el tabulador en UN espacio", línea)
	}

	// Pasarse del límite se dice, no se recorta: recortar «…y sin maní» pierde el
	// alérgeno, que es justo lo que no se puede perder.
	largo := strconv.Quote(strings.Repeat("a", 300))
	rec = call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items",
		`{"items":[{"sku":"HAMB","label":"Hamburguesa","customization":`+largo+`,"qty":1,"unit_price":8}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("customization de 300 runas → code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "customization") {
		t.Fatalf("el 400 no dice qué campo se pasó: %s", rec.Body.String())
	}
}

// TestIntakeItems_404_OtroTenant: la solicitud del tenant A no existe para el B.
// 404 y NUNCA 403: un 403 confirmaría que el id existe (INV-8).
func TestIntakeItems_404_OtroTenant(t *testing.T) {
	st := bandejaConEscena()
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	fake.Enable(tenantB, entitlements.FeatureCartBasic) // el fallo tiene que ser por tenant, no por plan
	api := newAPI(publicapi.Deps{Intakes: intakes.NewService(st), Entitlements: fake}, intakesKeys())

	rec := call(api, keyBIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items",
		`{"items":[{"sku":"X","label":"X","qty":1,"unit_price":1}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (nunca 403); body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakeItems_403_SinScopeDeEscritura: leer la bandeja no autoriza a reescribir
// el presupuesto. El scope y la feature son dos guardias distintos y aquí falla el
// primero.
func TestIntakeItems_403_SinScopeDeEscritura(t *testing.T) {
	keys := intakesKeys()
	keys["solo-lectura"] = testIdentity{
		TenantID: tenantA, Subject: "mirona-a", Grants: []string{"intakes.read"},
	}
	api := newAPI(depsSinLLM(bandejaConEscena()), keys)

	rec := call(api, "solo-lectura", http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items",
		`{"items":[{"sku":"X","label":"X","qty":1,"unit_price":1}]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin intakes.write; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakeItems_DosEdicionesDosRevisiones: el PUT es idempotente en DATOS —mandar
// dos veces el mismo cuerpo deja las mismas líneas— pero NO en auditoría: dos
// ediciones son dos actos del dueño y la negociación tiene que verlos.
func TestIntakeItems_DosEdicionesDosRevisiones(t *testing.T) {
	api := newAPI(depsSinLLM(bandejaConEscena()), intakesKeys())
	if rec := call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+intakeQueso+"/status",
		`{"status":"pending_approval"}`); rec.Code != http.StatusOK {
		t.Fatalf("a pending_approval: code=%d; body=%s", rec.Code, rec.Body.String())
	}

	cuerpo := `{"items":[{"sku":"HAMB","label":"Hamburguesa","qty":3,"unit_price":8}]}`
	var último intakeDetailDTO
	for i := 0; i < 2; i++ {
		rec := call(api, keyAIntakes, http.MethodPut, "/api/v1/intakes/"+intakeQueso+"/items", cuerpo)
		if rec.Code != http.StatusOK {
			t.Fatalf("edición %d: code=%d; body=%s", i+1, rec.Code, rec.Body.String())
		}
		último = decodeDetalle(t, rec.Body.Bytes())
	}

	porSKU := líneasPorSKUDTO(último)
	if len(porSKU["HAMB"]) != 1 || porSKU["HAMB"][0].Qty != 3 || último.Total != 24 {
		t.Fatalf("tras dos PUT idénticos: items=%+v total=%v; quiero UNA línea de 3 y total 24 (el envío va a 0)",
			último.Items, último.Total)
	}
	if len(porSKU[intakes.ShippingSKU]) != 1 {
		t.Fatalf("líneas de envío=%d tras dos ediciones, quiero 1", len(porSKU[intakes.ShippingSKU]))
	}
	if len(último.Revisions) != 2 {
		t.Fatalf("revisiones=%d, quiero 2: dos ediciones son dos actos", len(último.Revisions))
	}
	if último.Revisions[0].RevisionNo != 1 || último.Revisions[1].RevisionNo != 2 {
		t.Fatalf("correlativos=%d,%d; quiero 1 y 2",
			último.Revisions[0].RevisionNo, último.Revisions[1].RevisionNo)
	}
	// La revisión está fechada: el rastro sin fecha no es rastro.
	if _, err := time.Parse(time.RFC3339, último.Revisions[1].CreatedAt); err != nil {
		t.Fatalf("created_at=%q no es RFC3339: %v", último.Revisions[1].CreatedAt, err)
	}
}
