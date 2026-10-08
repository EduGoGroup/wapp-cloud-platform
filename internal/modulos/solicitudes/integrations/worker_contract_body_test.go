package integrations_test

// worker_contract_body_test.go cierra el círculo que ni el sink ni el worker cierran por separado:
// que el JSON que SALE por HTTP hacia el puente sigue validando contra wapp-crm-v1 después de que
// tres campos dejaran de viajar congelados en la plantilla (buyer_data, variables{} y
// customer_note).
//
// Porta la regla de internal/integrations/contract_body_test.go @ 36d5a04.
//
// El riesgo que cubre: los tres campos son REQUERIDOS por el esquema, así que sacarlos de la
// plantilla y olvidarse de completar uno en el worker no rompe ninguna compilación ni ningún test
// de unidad — rompe al puente del cliente, en producción, en silencio. Los tests del sink miran lo
// que se persiste y los del worker miran claves sueltas; solo aquí se mira el documento entero
// contra su contrato.
//
// La plantilla NO se escribe a mano: se DERIVA del ejemplo publicado quitándole los tres campos
// que hoy completa el worker. Así el test no puede divergir del contrato sin que el contrato
// cambie.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// El tenant y la solicitud del ejemplo publicado.
const (
	exampleTenant = "acme-panaderia"
	exampleIntake = "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077"
)

// freshFields son los campos que el sink NO congela en webhook_outbox y el worker rellena justo
// antes del POST. Los tres son `required` en el esquema.
var freshFields = []string{"buyer_data", "variables", "customer_note"}

// frozenFields es la OTRA mitad del reparto: los fija el sink en el instante del cierre del
// carrito y el worker no los vuelve a mirar.
//
// El que muerde es `timestamp`: es el del ENCOLADO y NUNCA se recalcula, así que un reintento
// diferido entrega las líneas, el total y la hora de entonces junto a la nota y las variables de
// ahora. Es intencional (D-042.9/D-042.11: lo fresco exige descifrado o consulta, prohibidos en
// línea con el mensaje por INV-02) y está dicho al integrador en
// docs/manuales/integrador-puente-crm.md §4. Lo que esta lista impide es que alguien lo «arregle»
// sin darse cuenta.
var frozenFields = []string{"items", "total", "timestamp"}

// templateFromExample carga el ejemplo publicado y le quita los tres campos frescos: eso es, por
// definición, la plantilla que hoy se encola. Devuelve también la plantilla ya parseada.
func templateFromExample(t *testing.T) (json.RawMessage, map[string]any) {
	t.Helper()
	doc := loadExample(t, pushExample)
	for _, field := range freshFields {
		if _, present := doc[field]; !present {
			t.Fatalf("el ejemplo publicado ya no trae %q: o el contrato cambió, o este test "+
				"está sintetizando una plantilla que no se parece a la real", field)
		}
		delete(doc, field)
	}
	for _, field := range frozenFields {
		if _, present := doc[field]; !present {
			t.Fatalf("la plantilla derivada del ejemplo ya no trae %q: este test no probaría nada", field)
		}
	}
	template, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-serializar la plantilla: %v", err)
	}
	return template, doc
}

// newExampleRig monta el banco con el tenant del ejemplo integrado contra un puente que contesta
// esos códigos, y con las tres fuentes que el worker consulta devolviendo los MISMOS valores que
// el ejemplo publicado traía congelados.
func newExampleRig(t *testing.T, statuses ...int) (*workerRig, *bridge) {
	t.Helper()
	rig := newWorkerRig()
	crm := newBridge(t, statuses...)
	rig.integrate(t, exampleTenant, crm.srv.URL)
	rig.buyer.set(exampleIntake, intakes.BuyerData{"documento": "12.345.678-5", "direccion_entrega": "Av. Libertador 742, piso 3"})
	rig.notes.set(exampleTenant, exampleIntake, "dejarlo en portería")
	rig.vars.set(exampleTenant,
		tenantvars.Variable{Key: "moneda", Value: "Bs"},
		tenantvars.Variable{Key: "tasa_dia", Value: "36,50"},
		tenantvars.Variable{Key: "pie_de_nota", Value: "Gracias por su compra"},
	)
	return rig, crm
}

// TestRun_DeliveredBody_ValidatesAgainstThePublishedSchema entrega la plantilla derivada del
// ejemplo con el worker real y valida el cuerpo que recibe el puente contra
// intake.push.schema.json. Con las fuentes devolviendo lo del ejemplo, el cuerpo entregado ES el
// ejemplo publicado.
func TestRun_DeliveredBody_ValidatesAgainstThePublishedSchema(t *testing.T) {
	rig, crm := newExampleRig(t)
	template, _ := templateFromExample(t)
	rig.enqueue(t, exampleTenant, template)

	rig.runPolls(t, 1)

	req := crm.only(t)
	var doc any
	if err := json.Unmarshal(req.body, &doc); err != nil {
		t.Fatalf("el cuerpo entregado no es JSON válido: %v", err)
	}
	if err := compileSchema(t, pushSchema).Validate(doc); err != nil {
		t.Fatalf("el cuerpo que recibe el puente YA NO valida contra wapp-crm-v1:\n%v\n\ncuerpo: %s", err, req.body)
	}
	if want := loadExample(t, pushExample); !reflect.DeepEqual(doc, any(want)) {
		t.Errorf("el cuerpo entregado no es el ejemplo publicado:\n%s\nquería:\n%v", req.body, want)
	}
}

// TestRun_DeliveredBody_ValidatesWithEmptySources: aunque el tenant no tenga datos del comprador,
// ni nota, ni variables, el cuerpo sigue validando: los tres campos requeridos van vacíos, no
// ausentes.
func TestRun_DeliveredBody_ValidatesWithEmptySources(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, exampleTenant, crm.srv.URL)
	template, _ := templateFromExample(t)
	rig.enqueue(t, exampleTenant, template)

	rig.runPolls(t, 1)

	req := crm.only(t)
	var doc any
	if err := json.Unmarshal(req.body, &doc); err != nil {
		t.Fatalf("el cuerpo entregado no es JSON válido: %v", err)
	}
	if err := compileSchema(t, pushSchema).Validate(doc); err != nil {
		t.Fatalf("con las fuentes vacías el cuerpo no valida contra wapp-crm-v1:\n%v\n\ncuerpo: %s", err, req.body)
	}
	requireEmptyFreshFields(t, req.decoded(t))
}

// TestRun_DeliveredBody_ExactSplitBetweenFrozenAndFresh fija el reparto ENTERO del payload mixto:
// el cuerpo entregado es el encolado MÁS exactamente los tres campos frescos. Ni un campo perdido,
// ni uno inventado, ni uno reescrito.
//
// La asimetría que deja pasar el cambio silencioso es comprobar solo que los frescos LLEGAN.
// Reescribir `timestamp` con la hora del POST es el «arreglo» intuitivo el día que alguien note
// que un reintento entrega una hora vieja — y pasaría en verde, cambiando el significado de un
// campo del contrato para todos los puentes ya escritos, sin tocar el esquema.
func TestRun_DeliveredBody_ExactSplitBetweenFrozenAndFresh(t *testing.T) {
	rig, crm := newExampleRig(t)
	template, queued := templateFromExample(t)
	rig.enqueue(t, exampleTenant, template)

	rig.runPolls(t, 1)

	delivered := crm.only(t).decoded(t)
	requireFrozenIntact(t, "la entrega", queued, delivered)
	for _, field := range freshFields {
		if _, persisted := queued[field]; persisted {
			t.Fatalf("%q está en el payload que se PERSISTE en webhook_outbox: es lo que INV-02 sacó de ahí (PII y coste)", field)
		}
		if _, arrived := delivered[field]; !arrived {
			t.Errorf("%q no llegó al puente: el esquema lo declara requerido y solo el worker puede ponerlo", field)
		}
	}
	// El barrido que cubre los campos que nadie nombró —contract_version, verb, tenant, contact,
	// intake_id, lifecycle_status, revision_no, event_history_id—: todo lo encolado llega idéntico,
	// y lo entregado no trae nada más que los tres frescos.
	for field, want := range queued {
		got, arrived := delivered[field]
		if !arrived {
			t.Errorf("el worker perdió %q entre el encolado y el POST", field)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("el worker reescribió %q: encolado=%v, entregado=%v", field, want, got)
		}
	}
	if want := len(queued) + len(freshFields); len(delivered) != want {
		t.Errorf("el cuerpo entregado tiene %d campos, quería %d (los %d encolados + los %d que completa el worker): "+
			"alguien añadió o quitó un campo del payload.\nencolado: %v\nentregado: %v",
			len(delivered), want, len(queued), len(freshFields), queued, delivered)
	}
}

// requireFrozenIntact comprueba, por nombre, que los campos del instante del cierre llegan tal
// cual. Por nombre y no solo por un barrido para que el fallo se lea sin descifrar un diff: es el
// que se pondrá rojo el día que alguien toque `timestamp`.
func requireFrozenIntact(t *testing.T, what string, queued, delivered map[string]any) {
	t.Helper()
	for _, field := range frozenFields {
		got, arrived := delivered[field]
		if !arrived {
			t.Errorf("%s: el worker PERDIÓ %q por el camino: el esquema lo declara requerido", what, field)
			continue
		}
		if !reflect.DeepEqual(got, queued[field]) {
			t.Errorf("%s: el worker REESCRIBIÓ %q, que se congela al encolar: encolado=%v, entregado=%v.\n"+
				"Si el cambio es deliberado, no basta con tocar el código: `timestamp` es hoy el instante "+
				"del ENCOLADO y así está documentado al integrador (manual §4). Cambiarlo cambia el "+
				"contrato de facto para todos los puentes ya escritos.", what, field, queued[field], got)
		}
	}
}

// TestRun_DeliveredBody_ARetryDoesNotRefreshWhatWasFrozen es el escenario del manual del
// integrador (§4) puesto a correr: primer intento fallido, el dueño corrige la nota y cambia una
// variable entre medias —y pasan horas—, y el reintento entrega.
//
// Lo fresco cambia —es lo que quiere quien prepara el pedido— y lo congelado no. Las DOS entregas
// se comparan contra el valor ENCOLADO, no una contra otra: un worker que recalculara `timestamp`
// en cada intento podría producir dos cadenas iguales, y el ancla tiene que ser el instante del
// cierre. Aquí además el reloj del worker se mueve tres horas entre los dos intentos.
func TestRun_DeliveredBody_ARetryDoesNotRefreshWhatWasFrozen(t *testing.T) {
	rig, crm := newExampleRig(t, http.StatusInternalServerError, http.StatusOK)
	template, queued := templateFromExample(t)
	id := rig.enqueue(t, exampleTenant, template)

	stop := rig.start(t)
	rig.store.waitPolls(t, 1)
	// Entre el fallo y el reintento pasa la vida: el dueño corrige la indicación y cambia la tasa.
	rig.notes.set(exampleTenant, exampleIntake, "dejarlo en el local, no en portería")
	rig.vars.set(exampleTenant, tenantvars.Variable{Key: "tasa_dia", Value: "37,80"})
	rig.clock.Advance(3 * time.Hour)
	rig.store.waitFreshPoll(t)
	stop()

	deliveries := crm.received()
	if len(deliveries) != 2 {
		t.Fatalf("el puente recibió %d POST, quería 2 (el que falla y el reintento)", len(deliveries))
	}
	if got := rig.row(t, id).Status; got != integrations.StatusDelivered {
		t.Fatalf("status final = %q, quería delivered", got)
	}
	first, retry := deliveries[0].decoded(t), deliveries[1].decoded(t)
	requireFrozenIntact(t, "el primer intento", queued, first)
	requireFrozenIntact(t, "el reintento", queued, retry)

	if first["customer_note"] != "dejarlo en portería" {
		t.Errorf("customer_note del primer intento = %v, quería la de entonces", first["customer_note"])
	}
	if retry["customer_note"] != "dejarlo en el local, no en portería" {
		t.Errorf("customer_note del reintento = %v: la nota se lee en el instante de la ENTREGA, "+
			"y la corregida es la que quien prepara el pedido tiene que leer", retry["customer_note"])
	}
	if vars, ok := retry["variables"].(map[string]any); !ok || len(vars) != 1 || vars["tasa_dia"] != "37,80" {
		t.Errorf("variables del reintento = %v, quería solo la tasa vigente (D-042.11: snapshot al entregar)", retry["variables"])
	}
	if deliveries[0].header.Get("X-Wapp-Timestamp") == deliveries[1].header.Get("X-Wapp-Timestamp") {
		t.Error("los dos intentos llevan el mismo X-Wapp-Timestamp: la CABECERA sí es el instante de cada entrega")
	}
}
