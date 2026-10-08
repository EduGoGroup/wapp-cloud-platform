package integrations_test

// contrato_wapp_crm_v1_test.go valida que los ejemplos publicados de wapp-crm-v1 (Plan 042, T2.3)
// no diverjan en silencio de sus JSON Schema (REQ-07).
//
// Porta la regla de internal/contracts/contract_examples_test.go @ 36d5a04: el paquete `contracts`
// desaparece (05 §6, F6) y su validación pasa aquí. Es la excepción declarada a «un fichero, un
// test» (D-F6-3): es el test de un contrato EXTERNO y no tiene fichero de producción homónimo.
// docs/contracts/ no se toca: se lee desde aquí con otra ruta relativa.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// contractDir apunta al contrato PUBLICADO, no a una copia: si alguien edita el schema o el
// ejemplo real, este test lo ve sin que nadie tenga que sincronizar nada.
const contractDir = "../../../../docs/contracts/wapp-crm-v1"

// Los tres verbos del contrato, cada uno con su esquema y su ejemplo.
const (
	pushSchema    = "intake.push.schema.json"
	statusSchema  = "intake.status.schema.json"
	catalogSchema = "catalog.pull.schema.json"

	pushExample    = "intake.push.json"
	statusExample  = "intake.status.json"
	catalogExample = "catalog.pull.json"
)

// compileSchema compila el esquema publicado con ese nombre.
func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(filepath.Join(contractDir, name))
	if err != nil {
		t.Fatalf("compilar %s: %v", name, err)
	}
	return sch
}

// loadExample lee el ejemplo publicado con ese nombre.
func loadExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir, "examples", name)) // #nosec G304 -- ruta fija de test, nombres de este mismo fichero
	if err != nil {
		t.Fatalf("leer el ejemplo %s: %v", name, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsear el ejemplo %s: %v", name, err)
	}
	return doc
}

// cloneDoc evita que la mutación de un subtest contamine el mapa cargado por otro: el viaje de ida
// y vuelta por JSON es la copia profunda más simple de un map[string]any.
func cloneDoc(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("clonar: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("clonar: %v", err)
	}
	return out
}

// TestContractExamplesMatchSchemas: el ejemplo publicado de cada uno de los tres verbos valida
// contra su esquema.
func TestContractExamplesMatchSchemas(t *testing.T) {
	cases := []struct {
		verb    string
		schema  string
		example string
	}{
		{"intake.push", pushSchema, pushExample},
		{"intake.status", statusSchema, statusExample},
		{"catalog.pull", catalogSchema, catalogExample},
	}
	for _, c := range cases {
		t.Run(c.verb, func(t *testing.T) {
			if err := compileSchema(t, c.schema).Validate(loadExample(t, c.example)); err != nil {
				t.Fatalf("el ejemplo publicado %s NO valida contra %s: %v", c.example, c.schema, err)
			}
		})
	}
}

// firstPushItem devuelve la primera línea del ejemplo de intake.push, o falla el test.
func firstPushItem(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	items, ok := doc["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatal("el ejemplo intake.push no trae items[] o no es un array")
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatal("items[0] del ejemplo intake.push no es un objeto")
	}
	return first
}

// TestIntakePushNegativeCases: cada mutación del ejemplo debe hacer fallar la validación. Si
// alguna pasa, el esquema dejó de proteger lo que el contrato promete (REQ-07).
func TestIntakePushNegativeCases(t *testing.T) {
	sch := compileSchema(t, pushSchema)
	base := loadExample(t, pushExample)

	cases := []struct {
		name   string
		mutate func(t *testing.T, doc map[string]any)
	}{
		{"total as a string", func(_ *testing.T, doc map[string]any) { doc["total"] = "24" }},
		{"lifecycle_status outside the enum", func(_ *testing.T, doc map[string]any) { doc["lifecycle_status"] = "closed" }},
		{"undeclared currency (INV-09)", func(_ *testing.T, doc map[string]any) { doc["currency"] = "CLP" }},
		{"reserved contact_phone is never emitted", func(_ *testing.T, doc map[string]any) { doc["contact_phone"] = "+56912345678" }},
		{"items qty below the minimum", func(t *testing.T, doc map[string]any) { firstPushItem(t, doc)["qty"] = float64(0) }},
		{"revision_no below the minimum", func(_ *testing.T, doc map[string]any) { doc["revision_no"] = float64(0) }},
		{"customer_note missing (required)", func(_ *testing.T, doc map[string]any) { delete(doc, "customer_note") }},
		{"variables missing (required)", func(_ *testing.T, doc map[string]any) { delete(doc, "variables") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := cloneDoc(t, base)
			c.mutate(t, doc)
			if err := sch.Validate(doc); err == nil {
				t.Fatalf("la mutación %q debía romper la validación y no lo hizo", c.name)
			}
		})
	}
}

// TestIntakeStatusNegativeCases valida el verbo de vuelta: un estado fuera de los cuatro canónicos
// debe rechazarse (los vocabularios de lifecycle_status e intake.status son disjuntos —
// "confirmed" existe en el primero, no en el segundo).
func TestIntakeStatusNegativeCases(t *testing.T) {
	doc := cloneDoc(t, loadExample(t, statusExample))
	doc["status"] = "confirmed"
	if err := compileSchema(t, statusSchema).Validate(doc); err == nil {
		t.Fatal(`status:"confirmed" pertenece a lifecycle_status, no a intake.status, y debía rechazarse`)
	}
}

// TestIntakeStatusExternalRefIsOptional confirma que external_ref es opcional: un callback sin
// referencia del CRM sigue siendo un intake.status válido.
func TestIntakeStatusExternalRefIsOptional(t *testing.T) {
	doc := cloneDoc(t, loadExample(t, statusExample))
	if _, present := doc["external_ref"]; !present {
		t.Fatal("el ejemplo intake.status ya no trae external_ref: quitarlo no probaría nada")
	}
	delete(doc, "external_ref")
	if err := compileSchema(t, statusSchema).Validate(doc); err != nil {
		t.Fatalf("intake.status sin external_ref (opcional) debía ser válido: %v", err)
	}
}

// TestSchemasCompileAsDraft2020_12 confirma que los tres esquemas declaran el dialecto que el
// resto de la documentación promete (draft 2020-12), no draft-07 — el motivo por el que este repo
// usa jsonschema/v6 y no xeipuuv/gojsonschema.
func TestSchemasCompileAsDraft2020_12(t *testing.T) {
	for _, name := range []string{pushSchema, statusSchema, catalogSchema} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(contractDir, name)) // #nosec G304 -- ruta fija de test, nombres de este mismo fichero
			if err != nil {
				t.Fatalf("leer %s: %v", name, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parsear %s: %v", name, err)
			}
			if got, ok := doc["$schema"].(string); !ok || got != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("%s declara $schema=%q, se esperaba draft 2020-12", name, got)
			}
			compileSchema(t, name)
		})
	}
}
