// contract_body_test.go cierra el círculo que ni el sink ni el worker cierran por
// separado: que el JSON que SALE por HTTP hacia el puente sigue validando contra
// wapp-crm-v1 después de que tres campos dejaran de viajar congelados en la
// plantilla (buyer_data, variables{} y —desde la Ola 5— customer_note).
//
// El riesgo que cubre es exactamente el que un plan como este produce: los tres
// campos son REQUERIDOS por el schema, así que sacarlos de la plantilla y olvidarse
// de completar uno en el worker no rompe ninguna compilación ni ningún test de
// unidad — rompe al puente del cliente, en producción, en silencio. Los tests del
// sink miran lo que se persiste y los del worker miran claves sueltas; solo aquí se
// mira el documento entero contra su contrato.
//
// La plantilla NO se escribe a mano: se DERIVA del ejemplo publicado quitándole los
// tres campos que hoy completa el worker. Así el test no puede divergir del
// contrato sin que el contrato cambie.
package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// contractDir apunta al contrato PUBLICADO, no a una copia (mismo criterio que
// internal/contracts): si alguien edita el schema, este test lo ve sin que nadie
// sincronice nada.
const contractDir = "../../docs/contracts/wapp-crm-v1"

// camposQueCompletaElWorker son los que el sink NO congela en webhook_outbox y el
// worker rellena justo antes del POST. Los tres son `required` en el schema.
var camposQueCompletaElWorker = []string{"buyer_data", "variables", "customer_note"}

// camposCongeladosAlEncolar es la OTRA mitad del reparto, la que hasta hoy no
// tenía quien la vigilara: los fija el sink en el instante del cierre del carrito
// y el worker no los vuelve a mirar.
//
// El que muerde es `timestamp`: es el del ENCOLADO y NUNCA se recalcula, así que
// un reintento diferido entrega las líneas, el total y la hora de entonces junto a
// la nota y las variables de ahora. Es intencional (D-042.9/D-042.11: lo fresco
// exige descifrado o consulta, prohibidos en línea con el mensaje por INV-02) y
// está dicho al integrador en docs/manuales/integrador-puente-crm.md §4. Lo que
// esta lista impide es que alguien lo "arregle" sin darse cuenta.
var camposCongeladosAlEncolar = []string{"items", "total", "timestamp"}

// plantillaDesdeElEjemplo carga el ejemplo publicado y le quita esos tres campos:
// eso es, por definición, la plantilla que hoy se encola.
func plantillaDesdeElEjemplo(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir, "examples", "intake.push.json")) // #nosec G304 -- ruta fija de test
	if err != nil {
		t.Fatalf("leer el ejemplo publicado: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsear el ejemplo publicado: %v", err)
	}
	for _, k := range camposQueCompletaElWorker {
		if _, hay := v[k]; !hay {
			t.Fatalf("el ejemplo publicado ya no trae %q: o el contrato cambió, o este test "+
				"está sintetizando una plantilla que no se parece a la real", k)
		}
		delete(v, k)
	}
	plantilla, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-serializar la plantilla: %v", err)
	}
	return plantilla
}

// TestWorker_ElCuerpoEntregadoValidaContraElSchema entrega esa plantilla con el
// worker real y valida el body que recibe el puente contra intake.push.schema.json.
func TestWorker_ElCuerpoEntregadoValidaContraElSchema(t *testing.T) {
	const (
		tenant   = "acme-panaderia"
		intakeID = "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077"
		nota     = "dejarlo en portería"
	)
	ctx := context.Background()

	var recibido []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("leer el body recibido: %v", rerr)
		}
		recibido = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations[tenant] = enabledIntegration(tenant, srv.URL)
	store.secrets[tenant] = "s3cr3t"
	if _, err := store.EnqueueWebhook(ctx, tenant, "intake.push", plantillaDesdeElEjemplo(t)); err != nil {
		t.Fatalf("encolar: %v", err)
	}

	// Las tres fuentes que el worker consulta, con los MISMOS valores que el
	// ejemplo publicado traía congelados.
	buyer := fakeBuyerData{data: map[string]intakes.BuyerData{
		intakeID: {"documento": "12.345.678-5", "direccion_entrega": "Av. Libertador 742, piso 3"},
	}}
	notes := fakeCustomerNotes{notes: map[[2]string]string{{tenant, intakeID}: nota}}
	tvars := fakeTenantVars{vars: map[string][]tenantvars.Variable{tenant: {
		{Key: "moneda", Value: "Bs"},
		{Key: "tasa_dia", Value: "36,50"},
		{Key: "pie_de_nota", Value: "Gracias por su compra"},
	}}}

	w := NewWorker(store, buyer, notes, tvars, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	w.pollOnce(ctx)

	if len(recibido) == 0 {
		t.Fatal("el puente no recibió nada: sin body no hay contrato que validar")
	}

	var doc any
	if err := json.Unmarshal(recibido, &doc); err != nil {
		t.Fatalf("el body entregado no es JSON válido: %v", err)
	}

	c := jsonschema.NewCompiler()
	sch, err := c.Compile(filepath.Join(contractDir, "intake.push.schema.json"))
	if err != nil {
		t.Fatalf("compilar el schema publicado: %v", err)
	}
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("el cuerpo que recibe el puente YA NO valida contra wapp-crm-v1:\n%v\n\nbody: %s",
			err, recibido)
	}

	// Y el campo que motivó todo esto llega con su valor, no con el hueco.
	var got map[string]any
	if err := json.Unmarshal(recibido, &got); err != nil {
		t.Fatalf("re-parsear el body: %v", err)
	}
	if got["customer_note"] != nota {
		t.Fatalf("customer_note entregada = %v, quiero %q", got["customer_note"], nota)
	}
}

// TestWorker_ElRepartoExactoEntreCongeladoYFresco fija el reparto ENTERO del
// payload mixto, que hasta ahora solo estaba vigilado por una mitad: los tests
// existentes comprueban que los tres campos frescos LLEGAN, ninguno que el worker
// deje intacto todo lo demás.
//
// Esa asimetría es justo la que deja pasar el cambio silencioso. Reescribir
// `timestamp` con la hora del POST es el "arreglo" intuitivo el día que alguien
// note que un reintento entrega una hora vieja — y pasaría en verde, cambiando el
// significado de un campo del contrato para todos los puentes ya escritos, sin
// tocar el schema ni romper ninguna compilación.
//
// La regla que se fija aquí: tras completePayload, el payload entregado es el
// encolado MÁS exactamente camposQueCompletaElWorker. Ni un campo perdido, ni uno
// inventado, ni uno reescrito.
func TestWorker_ElRepartoExactoEntreCongeladoYFresco(t *testing.T) {
	const (
		tenant   = "acme-panaderia"
		intakeID = "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077"
	)
	ctx := context.Background()

	var recibido []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("leer el body recibido: %v", rerr)
		}
		recibido = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations[tenant] = enabledIntegration(tenant, srv.URL)
	store.secrets[tenant] = "s3cr3t"

	// La plantilla se guarda ANTES de entregar: la fila entregada queda con el
	// payload vacío (política de la Ola 5), así que después ya no hay con qué
	// comparar.
	plantilla := plantillaDesdeElEjemplo(t)
	if _, err := store.EnqueueWebhook(ctx, tenant, "intake.push", plantilla); err != nil {
		t.Fatalf("encolar: %v", err)
	}
	var encolado map[string]any
	if err := json.Unmarshal(plantilla, &encolado); err != nil {
		t.Fatalf("parsear la plantilla encolada: %v", err)
	}

	buyer := fakeBuyerData{data: map[string]intakes.BuyerData{intakeID: {"documento": "12.345.678-5"}}}
	notes := fakeCustomerNotes{notes: map[[2]string]string{{tenant, intakeID}: "dejarlo en portería"}}
	tvars := fakeTenantVars{vars: map[string][]tenantvars.Variable{tenant: {{Key: "moneda", Value: "Bs"}}}}

	w := NewWorker(store, buyer, notes, tvars, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	w.pollOnce(ctx)

	var entregado map[string]any
	if err := json.Unmarshal(recibido, &entregado); err != nil {
		t.Fatalf("el body entregado no es JSON válido: %v", err)
	}

	exigeLoCongeladoIntacto(t, encolado, entregado)
	exigeLoFrescoSoloEnLaEntrega(t, encolado, entregado)
	exigeQueNoSobreNiFalteNada(t, encolado, entregado)
}

// exigeLoCongeladoIntacto comprueba, por nombre, que los campos del instante del
// cierre llegan tal cual. Por nombre y no solo por el barrido de abajo para que el
// fallo se lea sin descifrar un diff: es el que se pondrá rojo el día que alguien
// toque `timestamp`.
func exigeLoCongeladoIntacto(t *testing.T, encolado, entregado map[string]any) {
	t.Helper()
	for _, k := range camposCongeladosAlEncolar {
		quiero, hay := encolado[k]
		if !hay {
			t.Fatalf("la plantilla derivada del ejemplo ya no trae %q: este test no probaría nada", k)
		}
		tiene, llegó := entregado[k]
		if !llegó {
			t.Fatalf("el worker PERDIÓ %q por el camino: el schema lo declara requerido", k)
		}
		if !reflect.DeepEqual(tiene, quiero) {
			t.Fatalf("el worker REESCRIBIÓ %q, que se congela al encolar: encolado=%v, entregado=%v.\n"+
				"Si el cambio es deliberado, no basta con tocar el código: `timestamp` es hoy el instante "+
				"del ENCOLADO y así está documentado al integrador (manual §4). Cambiarlo cambia el "+
				"contrato de facto para todos los puentes ya escritos.", k, quiero, tiene)
		}
	}
}

// exigeLoFrescoSoloEnLaEntrega comprueba las dos mitades del otro lado del
// reparto: los tres campos NO viajan congelados (INV-02 y la PII de la Ola 5) y
// aun así llegan al puente, que es lo que el schema exige.
func exigeLoFrescoSoloEnLaEntrega(t *testing.T, encolado, entregado map[string]any) {
	t.Helper()
	for _, k := range camposQueCompletaElWorker {
		if _, hay := encolado[k]; hay {
			t.Fatalf("%q está en el payload que se PERSISTE en webhook_outbox: es lo que INV-02 "+
				"y la Ola 5 sacaron de ahí (PII y coste)", k)
		}
		if _, hay := entregado[k]; !hay {
			t.Fatalf("%q no llegó al puente: el schema lo declara requerido y solo el worker puede ponerlo", k)
		}
	}
}

// exigeQueNoSobreNiFalteNada es el barrido que cubre los campos que nadie nombró
// —hoy contract_version, verb, tenant, contact, intake_id, lifecycle_status,
// revision_no y event_history_id—: todo lo encolado llega idéntico, y lo entregado
// no trae nada más que los tres frescos.
func exigeQueNoSobreNiFalteNada(t *testing.T, encolado, entregado map[string]any) {
	t.Helper()
	for k, quiero := range encolado {
		tiene, hay := entregado[k]
		if !hay {
			t.Fatalf("el worker perdió %q entre el encolado y el POST", k)
		}
		if !reflect.DeepEqual(tiene, quiero) {
			t.Fatalf("el worker reescribió %q: encolado=%v, entregado=%v", k, quiero, tiene)
		}
	}
	if quiero := len(encolado) + len(camposQueCompletaElWorker); len(entregado) != quiero {
		t.Fatalf("el body entregado tiene %d campos, quiero %d (los %d encolados + los %d que completa "+
			"el worker): alguien añadió o quitó un campo del payload.\nencolado: %v\nentregado: %v",
			len(entregado), quiero, len(encolado), len(camposQueCompletaElWorker), encolado, entregado)
	}
}

// TestWorker_ElReintentoNoRefrescaLoCongelado es el escenario del manual del
// integrador (§4) puesto a correr: primer intento fallido, el dueño corrige la
// nota y cambia una variable entre medias, y el reintento entrega.
//
// Lo fresco cambia —es lo que quiere quien prepara el pedido— y lo congelado no.
// Es el mismo invariante del test de arriba visto desde el otro lado: allí se mira
// un payload, aquí DOS entregas de la misma fila separadas en el tiempo, que es
// como se manifiesta de verdad (un puente caído tres horas, un re-encolado a mano
// tres días después).
func TestWorker_ElReintentoNoRefrescaLoCongelado(t *testing.T) {
	const (
		tenant   = "acme-panaderia"
		intakeID = "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077"
	)
	ctx := context.Background()

	var entregas []map[string]any
	var fallaElPrimero = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cuerpo map[string]any
		if derr := json.NewDecoder(r.Body).Decode(&cuerpo); derr != nil {
			t.Errorf("decodificar el body recibido: %v", derr)
		}
		entregas = append(entregas, cuerpo)
		if fallaElPrimero {
			fallaElPrimero = false
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations[tenant] = enabledIntegration(tenant, srv.URL)
	store.secrets[tenant] = "s3cr3t"
	plantilla := plantillaDesdeElEjemplo(t)
	id, err := store.EnqueueWebhook(ctx, tenant, "intake.push", plantilla)
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}
	var encolado map[string]any
	if err := json.Unmarshal(plantilla, &encolado); err != nil {
		t.Fatalf("parsear la plantilla encolada: %v", err)
	}

	notas := map[[2]string]string{{tenant, intakeID}: "dejarlo en portería"}
	variables := map[string][]tenantvars.Variable{tenant: {{Key: "tasa_dia", Value: "36,50"}}}
	w := NewWorker(store, fakeBuyerData{}, fakeCustomerNotes{notes: notas},
		fakeTenantVars{vars: variables}, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)

	w.pollOnce(ctx)

	// Entre el fallo y el reintento pasa la vida: el dueño corrige la indicación y
	// cambia la tasa del día.
	notas[[2]string{tenant, intakeID}] = "dejarlo en el local, no en portería"
	variables[tenant] = []tenantvars.Variable{{Key: "tasa_dia", Value: "37,80"}}

	forceRetriable(store, id)
	w.pollOnce(ctx)

	if len(entregas) != 2 {
		t.Fatalf("el puente recibió %d POST, quiero 2 (el que falla y el reintento)", len(entregas))
	}
	if status := store.statusOf(id); status != StatusDelivered {
		t.Fatalf("status final=%q, quiero delivered", status)
	}
	primera, reintento := entregas[0], entregas[1]

	// Se comparan las DOS entregas contra el valor ENCOLADO, no una contra otra:
	// dos entregas seguidas caen en el mismo segundo y `timestamp` es RFC3339 con
	// resolución de segundo, así que un worker que lo recalculara en cada intento
	// produciría dos cadenas IGUALES y un test que solo las cruzara se quedaría en
	// verde sin estar mirando. El ancla tiene que ser el instante del cierre.
	for _, k := range camposCongeladosAlEncolar {
		for i, entrega := range []map[string]any{primera, reintento} {
			if !reflect.DeepEqual(entrega[k], encolado[k]) {
				t.Fatalf("%q en la entrega %d = %v, quiero el valor congelado al encolar (%v).\n"+
					"Los campos congelados describen el instante del CIERRE; un reintento no los "+
					"refresca, y el integrador lee `timestamp` con esa promesa delante.",
					k, i+1, entrega[k], encolado[k])
			}
		}
	}
	if reintento["customer_note"] != "dejarlo en el local, no en portería" {
		t.Fatalf("customer_note del reintento = %v: la nota se lee en el instante de la ENTREGA, "+
			"y la corregida es la que quien prepara el pedido tiene que leer", reintento["customer_note"])
	}
	vars, ok := reintento["variables"].(map[string]any)
	if !ok || vars["tasa_dia"] != "37,80" {
		t.Fatalf("variables del reintento = %v, quiero la tasa vigente (D-042.11: snapshot al entregar)",
			reintento["variables"])
	}
}
