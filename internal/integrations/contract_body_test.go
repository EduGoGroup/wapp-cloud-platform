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
