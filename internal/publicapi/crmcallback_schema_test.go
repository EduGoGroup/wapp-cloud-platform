package publicapi

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// crmContractDir apunta al contrato PUBLICADO, no a una copia: si alguien edita el
// schema, este test lo ve sin que nadie tenga que sincronizar nada.
const crmContractDir = "../../docs/contracts/wapp-crm-v1"

// TestCRMCallbackValidator_CoincideConElSchemaPublicado ata las dos representaciones
// del contrato que conviven en el repo: el `intake.status.schema.json` publicado —lo
// que el autor de un puente lee— y el validador Go de la frontera —lo que de verdad
// decide el 422—.
//
// Existen las dos por una razón: el schema es el contrato para quien integra, y el
// validador es lo que corre por petición sin compilar JSON Schema en caliente. Lo
// que no puede pasar es que se separen, porque entonces el documento diría una cosa
// y el endpoint haría otra — y el que se entera es el cliente, en producción, con un
// 422 que su schema decía que no debía ocurrir (o peor: con un 200 que el schema
// decía que no).
//
// El test no compara implementaciones: les da los MISMOS cuerpos y exige el MISMO
// veredicto. Un caso nuevo aquí protege a los dos lados a la vez.
func TestCRMCallbackValidator_CoincideConElSchemaPublicado(t *testing.T) {
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(filepath.Join(crmContractDir, "intake.status.schema.json"))
	if err != nil {
		t.Fatalf("compilar el schema publicado: %v", err)
	}

	const id = "11111111-2222-3333-4444-555555555555"
	casos := []struct {
		nombre string
		body   string
	}{
		{"válido mínimo", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","status":"paid","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"válido con external_ref", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","status":"delivered","external_ref":"F-2026-0001","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"los otros dos canónicos", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","status":"preparing","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"rejected, que existe en los DOS vocabularios", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","status":"rejected","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"estado de CRM plausible pero no canónico", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","status":"shipped","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"tenant en el cuerpo (el error instintivo)", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","status":"paid","occurred_at":"2026-08-08T12:00:00Z","tenant":"acme"}`},
		{"sin occurred_at", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id + `","status":"paid"}`},
		{"sin intake_id", `{"contract_version":"1","verb":"intake.status","status":"paid","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"sin status", `{"contract_version":"1","verb":"intake.status","intake_id":"` + id +
			`","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"verbo de otro mensaje del contrato", `{"contract_version":"1","verb":"intake.push","intake_id":"` + id +
			`","status":"paid","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"versión de contrato futura", `{"contract_version":"2","verb":"intake.status","intake_id":"` + id +
			`","status":"paid","occurred_at":"2026-08-08T12:00:00Z"}`},
		{"intake_id vacío", `{"contract_version":"1","verb":"intake.status","intake_id":"","status":"paid",` +
			`"occurred_at":"2026-08-08T12:00:00Z"}`},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			// Veredicto del schema publicado.
			var inst any
			if err := json.Unmarshal([]byte(c.body), &inst); err != nil {
				t.Fatalf("el caso no es JSON: %v", err)
			}
			schemaOK := sch.Validate(inst) == nil

			// Veredicto del validador de la frontera (decodificación + reglas).
			dec := json.NewDecoder(strings.NewReader(c.body))
			dec.DisallowUnknownFields()
			var req crmCallbackRequest
			goOK := dec.Decode(&req) == nil && req.validate() == ""

			if schemaOK != goOK {
				t.Fatalf("VEREDICTOS DISTINTOS para el mismo cuerpo — el contrato publicado y el endpoint "+
					"se han separado:\n  schema=%v  validador Go=%v\n  %s", schemaOK, goOK, c.body)
			}
		})
	}
}
