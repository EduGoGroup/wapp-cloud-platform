package integrations_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/sigv1"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// TestRun_Delivery_SignedPOST: la entrega es un POST al endpoint del tenant, con JSON, firmado
// sobre el cuerpo EXACTO que se envía y con el instante del reloj del worker; las tres cabeceras
// llevan sus nombres literales. La firma es recomputable con el secreto del tenant (D-042.5).
func TestRun_Delivery_SignedPOST(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, rigTenant, crm.srv.URL)
	id := rig.enqueueTemplate(t)
	stamp := rig.clock.Now().Unix()

	rig.runPolls(t, 1)

	req := crm.only(t)
	if req.method != http.MethodPost {
		t.Errorf("método = %s, quería POST", req.method)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, quería application/json", got)
	}
	if got, want := req.header.Get("X-Wapp-Delivery"), strconv.FormatInt(id, 10); got != want {
		t.Errorf("X-Wapp-Delivery = %q, quería el id de la fila (%s)", got, want)
	}
	if got, want := req.header.Get("X-Wapp-Timestamp"), strconv.FormatInt(stamp, 10); got != want {
		t.Errorf("X-Wapp-Timestamp = %q, quería los segundos Unix del reloj del worker (%s)", got, want)
	}
	signature := req.header.Get("X-Wapp-Signature")
	if want := sigv1.SignatureHeader(sigv1.Sign(rigSecret, stamp, req.body)); signature != want {
		t.Errorf("X-Wapp-Signature = %q, quería la firma del cuerpo recibido con el secreto del tenant (%q)", signature, want)
	}
	if !sigv1.Verify(rigSecret, stamp, req.body, strings.TrimPrefix(signature, "v1=")) {
		t.Error("el receptor no puede verificar la firma con el secreto, el instante y el cuerpo que recibió")
	}
	if strings.Contains(string(req.body), rigSecret) {
		t.Error("FUGA: el secreto de firma viaja en el cuerpo")
	}
}

// TestRun_Delivery_2xxClosesTheRowAndCounts: cualquier 2xx cierra la fila con el claim que dio el
// almacén, vacía su payload, cuenta «delivered» y no deja ERROR.
func TestRun_Delivery_2xxClosesTheRowAndCounts(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent, 299} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			rig := newWorkerRig()
			crm := newBridge(t, status)
			rig.integrate(t, rigTenant, crm.srv.URL)
			id := rig.enqueueTemplate(t)

			rig.runPolls(t, 1)

			row := rig.row(t, id)
			if row.Status != integrations.StatusDelivered || row.Attempts != 0 || !row.ClaimedAt.IsZero() {
				t.Errorf("fila = (status=%q, attempts=%d, claimed_at=%v), quería (delivered, 0, sin claim)", row.Status, row.Attempts, row.ClaimedAt)
			}
			if string(row.Payload) != `{}` {
				t.Errorf("payload de la fila entregada = %s, quería {}", row.Payload)
			}
			if got := rig.recorded(); !slices.Equal(got, []string{"delivered"}) {
				t.Errorf("métrica = %v, quería [delivered]", got)
			}
			marks := rig.store.marked()
			if len(marks) != 1 || marks[0].op != opDelivered || marks[0].claim.ID != id || marks[0].claim.ClaimedAt.IsZero() {
				t.Errorf("cierres pedidos al almacén = %+v, quería un MarkWebhookDelivered con el claim sellado", marks)
			}
			rig.log.requireNoErrors(t)
		})
	}
}

// TestRun_Delivery_CompletesTheThreeFreshFields: el cuerpo que llega al puente lleva buyer_data
// (descifrado por el lector), customer_note y variables{} leídos AHORA —no la plantilla desnuda
// que se encoló— y la fila NUNCA los recibe de vuelta (INV-02; D-042.9, D-042.11).
func TestRun_Delivery_CompletesTheThreeFreshFields(t *testing.T) {
	const note = "dejarlo en porteria calle Mayor 14 qqz"
	rig := newWorkerRig()
	crm := newBridge(t, http.StatusInternalServerError) // falla: así la fila conserva su payload
	rig.integrate(t, rigTenant, crm.srv.URL)
	rig.buyer.set(rigIntake, intakes.BuyerData{"documento": "12.345.678-5"})
	rig.notes.set(rigTenant, rigIntake, note)
	rig.vars.set(rigTenant, tenantvars.Variable{Key: "moneda", Value: "Bs"}, tenantvars.Variable{Key: "tasa_dia", Value: "36,50"})
	id := rig.enqueueTemplate(t)
	queued := string(rig.row(t, id).Payload)

	rig.runPolls(t, 1)

	body := crm.only(t).decoded(t)
	if bd, ok := body["buyer_data"].(map[string]any); !ok || len(bd) != 1 || bd["documento"] != "12.345.678-5" {
		t.Errorf("buyer_data entregado = %v, quería {documento: 12.345.678-5}", body["buyer_data"])
	}
	if body["customer_note"] != note {
		t.Errorf("customer_note entregada = %v, quería %q", body["customer_note"], note)
	}
	if vars, ok := body["variables"].(map[string]any); !ok || len(vars) != 2 || vars["moneda"] != "Bs" || vars["tasa_dia"] != "36,50" {
		t.Errorf("variables entregadas = %v, quería {moneda: Bs, tasa_dia: 36,50}", body["variables"])
	}
	stored := string(rig.row(t, id).Payload)
	if stored != queued {
		t.Errorf("el worker reescribió el payload de la fila:\n%s\nquería el encolado:\n%s", stored, queued)
	}
	for _, leak := range []string{note, "12.345.678-5", "buyer_data", "customer_note", "variables"} {
		if strings.Contains(stored, leak) {
			t.Errorf("FUGA: %q acabó escrito en la fila de webhook_outbox", leak)
		}
	}
}

// TestRun_Delivery_AsksEachSourceWithTheRightKey: buyer_data se pide por el intake_id de la
// plantilla; la nota y las variables, por el tenant DE LA FILA —no por el `tenant` del cuerpo—. Si
// alguien invierte esos argumentos, un puente recibiría la indicación de otra empresa.
func TestRun_Delivery_AsksEachSourceWithTheRightKey(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, rigTenant, crm.srv.URL)
	// La MISMA solicitud tiene nota, pero registrada bajo el tenant que dice el cuerpo.
	rig.notes.set("tenant-del-cuerpo", rigIntake, "nota ajena")
	rig.vars.set("tenant-del-cuerpo", tenantvars.Variable{Key: "ajena", Value: "sí"})
	rig.enqueue(t, rigTenant, templatePayload(t, rigIntake, "tenant-del-cuerpo"))

	rig.runPolls(t, 1)

	if got := rig.buyer.calls(); !slices.Equal(got, []string{rigIntake}) {
		t.Errorf("buyer_data se pidió por %v, quería [%s]", got, rigIntake)
	}
	if got := rig.notes.calls(); len(got) != 1 || got[0] != [2]string{rigTenant, rigIntake} {
		t.Errorf("la nota se pidió por %v, quería [(%s, %s)]: el tenant es el de la fila", got, rigTenant, rigIntake)
	}
	if got := rig.vars.calls(); !slices.Equal(got, []string{rigTenant}) {
		t.Errorf("las variables se pidieron por %v, quería [%s]: el tenant es el de la fila", got, rigTenant)
	}
	body := crm.only(t).decoded(t)
	if body["customer_note"] != "" {
		t.Errorf("customer_note = %v: la nota de otro tenant cruzó la frontera", body["customer_note"])
	}
	if vars, ok := body["variables"].(map[string]any); !ok || len(vars) != 0 {
		t.Errorf("variables = %v: las de otro tenant cruzaron la frontera", body["variables"])
	}
}

// TestRun_Delivery_EmptySources: sin datos del comprador, sin nota y sin variables, los tres campos
// van igualmente —el esquema los exige— como objeto vacío, cadena vacía y objeto vacío; nunca null
// ni ausentes.
func TestRun_Delivery_EmptySources(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, rigTenant, crm.srv.URL)
	rig.enqueueTemplate(t)

	rig.runPolls(t, 1)

	requireEmptyFreshFields(t, crm.only(t).decoded(t))
}

// TestRun_Delivery_TemplateWithoutIntakeID: una plantilla sin `intake_id` (o con uno que no es una
// cadena) no es un fallo: se entrega con buyer_data {} y customer_note "", y ni se pregunta a los
// lectores por una solicitud sin id. Las variables, que son del tenant, sí se leen.
func TestRun_Delivery_TemplateWithoutIntakeID(t *testing.T) {
	templates := map[string]string{
		"absent":       `{"verb":"intake.push","total":1}`,
		"not a string": `{"verb":"intake.push","intake_id":42,"total":1}`,
		"empty":        `{"verb":"intake.push","intake_id":"","total":1}`,
	}
	for name, template := range templates {
		t.Run(name, func(t *testing.T) {
			rig := newWorkerRig()
			crm := newBridge(t)
			rig.integrate(t, rigTenant, crm.srv.URL)
			rig.vars.set(rigTenant, tenantvars.Variable{Key: "moneda", Value: "Bs"})
			id := rig.enqueue(t, rigTenant, json.RawMessage(template))

			rig.runPolls(t, 1)

			if got := rig.row(t, id).Status; got != integrations.StatusDelivered {
				t.Fatalf("status = %q, quería delivered: la falta de intake_id no es motivo de fallo", got)
			}
			body := crm.only(t).decoded(t)
			if bd, ok := body["buyer_data"].(map[string]any); !ok || len(bd) != 0 {
				t.Errorf("buyer_data = %v, quería {}", body["buyer_data"])
			}
			if note, ok := body["customer_note"].(string); !ok || note != "" {
				t.Errorf("customer_note = %v, quería la cadena vacía", body["customer_note"])
			}
			if vars, ok := body["variables"].(map[string]any); !ok || vars["moneda"] != "Bs" {
				t.Errorf("variables = %v, quería las del tenant", body["variables"])
			}
			if len(rig.buyer.calls()) != 0 || len(rig.notes.calls()) != 0 {
				t.Errorf("sin intake_id se preguntó a los lectores (buyer=%v, notas=%v)", rig.buyer.calls(), rig.notes.calls())
			}
		})
	}
}

// TestRun_Delivery_UsesTheDestinationInForceAtDeliveryTime: el endpoint y el secreto son los
// vigentes del tenant en el momento de la entrega, no los de cuando se encoló.
func TestRun_Delivery_UsesTheDestinationInForceAtDeliveryTime(t *testing.T) {
	rig := newWorkerRig()
	old, current := newBridge(t), newBridge(t)
	rig.integrate(t, rigTenant, old.srv.URL)
	rig.enqueueTemplate(t)
	// Tras encolar, el dueño cambia el endpoint y rota el secreto.
	cfg := integrations.TenantIntegration{
		TenantID: rigTenant, CatalogAdapter: "local", EventsAdapter: "webhook", EndpointURL: current.srv.URL, Enabled: true,
	}
	if err := rig.mem.UpsertTenantIntegration(context.Background(), cfg, "secreto-rotado"); err != nil {
		t.Fatalf("reconfigurar la integración: %v", err)
	}
	stamp := rig.clock.Now().Unix()

	rig.runPolls(t, 1)

	if got := old.received(); len(got) != 0 {
		t.Errorf("el endpoint viejo recibió %d POST, quería 0", len(got))
	}
	req := current.only(t)
	if want := sigv1.SignatureHeader(sigv1.Sign("secreto-rotado", stamp, req.body)); req.header.Get("X-Wapp-Signature") != want {
		t.Error("la entrega no va firmada con el secreto vigente")
	}
}

// requireEmptyFreshFields afirma que los tres campos frescos están y van vacíos, con su tipo.
func requireEmptyFreshFields(t *testing.T, body map[string]any) {
	t.Helper()
	if bd, ok := body["buyer_data"].(map[string]any); !ok || len(bd) != 0 {
		t.Errorf("buyer_data = %#v, quería un objeto vacío", body["buyer_data"])
	}
	if note, ok := body["customer_note"].(string); !ok || note != "" {
		t.Errorf("customer_note = %#v, quería la cadena vacía", body["customer_note"])
	}
	if vars, ok := body["variables"].(map[string]any); !ok || len(vars) != 0 {
		t.Errorf("variables = %#v, quería un objeto vacío", body["variables"])
	}
}
