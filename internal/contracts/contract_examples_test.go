// Package contracts valida que los ejemplos publicados de wapp-crm-v1 (Plan 042, T2.3)
// no diverjan en silencio de sus JSON Schema (REQ-07). No es un paquete de dominio: no
// lo confundas con la futura API de integraciones de la Ola 5.
package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// contractDir apunta al contrato publicado, no a una copia: si alguien edita el schema
// o el ejemplo real, este test lo ve sin que nadie tenga que sincronizar nada.
const contractDir = "../../docs/contracts/wapp-crm-v1"

func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(filepath.Join(contractDir, name))
	if err != nil {
		t.Fatalf("compilar %s: %v", name, err)
	}
	return sch
}

func loadExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir, "examples", name)) // #nosec G304 -- ruta fija de test, nombres hardcodeados en este mismo fichero
	if err != nil {
		t.Fatalf("leer ejemplo %s: %v", name, err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsear ejemplo %s: %v", name, err)
	}
	return v
}

// clone evita que una mutación de un subtest contamine el mapa cargado por otro:
// json round-trip es la forma más simple de deep-copy de un map[string]any.
func clone(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("clonar: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("clonar: %v", err)
	}
	return out
}

func TestContractExamplesMatchSchemas(t *testing.T) {
	cases := []struct {
		verb    string
		schema  string
		example string
	}{
		{"intake.push", "intake.push.schema.json", "intake.push.json"},
		{"intake.status", "intake.status.schema.json", "intake.status.json"},
		{"catalog.pull", "catalog.pull.schema.json", "catalog.pull.json"},
	}
	for _, c := range cases {
		t.Run(c.verb, func(t *testing.T) {
			sch := compileSchema(t, c.schema)
			ex := loadExample(t, c.example)
			if err := sch.Validate(ex); err != nil {
				t.Fatalf("el ejemplo publicado %s NO valida contra %s: %v", c.example, c.schema, err)
			}
		})
	}
}

// TestIntakePushNegativeCases: cada mutación debe hacer fallar la validación. Si alguna
// pasa, el schema dejó de proteger lo que el contrato promete (REQ-07).
func TestIntakePushNegativeCases(t *testing.T) {
	sch := compileSchema(t, "intake.push.schema.json")
	base := loadExample(t, "intake.push.json")

	cases := map[string]func(t *testing.T, v map[string]any){
		"total como string": func(t *testing.T, v map[string]any) {
			v["total"] = "24"
		},
		"lifecycle_status fuera de enum": func(t *testing.T, v map[string]any) {
			v["lifecycle_status"] = "closed"
		},
		"currency no declarado (INV-09)": func(t *testing.T, v map[string]any) {
			v["currency"] = "CLP"
		},
		"contact_phone reservado no emitido": func(t *testing.T, v map[string]any) {
			v["contact_phone"] = "+56912345678"
		},
		"items[].qty por debajo del mínimo": func(t *testing.T, v map[string]any) {
			items, ok := v["items"].([]any)
			if !ok || len(items) == 0 {
				t.Fatal("el ejemplo intake.push no trae items[] o no es un array")
			}
			first, ok := items[0].(map[string]any)
			if !ok {
				t.Fatal("items[0] del ejemplo intake.push no es un objeto")
			}
			first["qty"] = float64(0)
		},
		"revision_no por debajo del mínimo": func(t *testing.T, v map[string]any) {
			v["revision_no"] = float64(0)
		},
		"customer_note ausente (requerido)": func(t *testing.T, v map[string]any) {
			delete(v, "customer_note")
		},
		"variables ausente (requerido)": func(t *testing.T, v map[string]any) {
			delete(v, "variables")
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := clone(t, base)
			mutate(t, v)
			if err := sch.Validate(v); err == nil {
				t.Fatalf("la mutación %q debía romper la validación y no lo hizo", name)
			}
		})
	}
}

// TestIntakeStatusNegativeCases valida el verbo de vuelta: un estado fuera de los
// cuatro canónicos debe rechazarse (los vocabularios de lifecycle_status e intake.status
// son disjuntos — "confirmed" existe en el primero, no en el segundo).
func TestIntakeStatusNegativeCases(t *testing.T) {
	sch := compileSchema(t, "intake.status.schema.json")
	base := loadExample(t, "intake.status.json")

	v := clone(t, base)
	v["status"] = "confirmed"
	if err := sch.Validate(v); err == nil {
		t.Fatal(`status:"confirmed" pertenece a lifecycle_status, no a intake.status, y debía rechazarse`)
	}
}

// TestIntakeStatusExternalRefIsOptional confirma que external_ref es opcional: un
// callback sin referencia del CRM sigue siendo un intake.status válido.
func TestIntakeStatusExternalRefIsOptional(t *testing.T) {
	sch := compileSchema(t, "intake.status.schema.json")
	v := clone(t, loadExample(t, "intake.status.json"))
	delete(v, "external_ref")
	if err := sch.Validate(v); err != nil {
		t.Fatalf("intake.status sin external_ref (opcional) debía ser válido: %v", err)
	}
}

// TestSchemasCompileAsDraft2020_12 confirma que los tres schemas declaran el dialecto
// que el resto de la documentación promete (draft 2020-12), no draft-07 — el motivo por
// el que este repo usa jsonschema/v6 y no xeipuuv/gojsonschema.
func TestSchemasCompileAsDraft2020_12(t *testing.T) {
	for _, name := range []string{"intake.push.schema.json", "intake.status.schema.json", "catalog.pull.schema.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(contractDir, name)) // #nosec G304 -- ruta fija de test, nombres hardcodeados en este mismo fichero
			if err != nil {
				t.Fatalf("leer %s: %v", name, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parsear %s: %v", name, err)
			}
			got, ok := doc["$schema"].(string)
			if !ok || got != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("%s declara $schema=%q, se esperaba draft 2020-12", name, got)
			}
			compileSchema(t, name)
		})
	}
}
