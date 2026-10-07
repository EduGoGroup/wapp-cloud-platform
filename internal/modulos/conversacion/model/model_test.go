package model_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// invalidPrefix es lo que precede a todo motivo de rechazo: el texto de
// ErrInvalidFlow y la envoltura de fmt.
const invalidPrefix = "definición de flujo inválida: "

func strPtr(s string) *string { return &s }

// validFlow es una definición correcta de partida que cada caso muta para
// provocar UN defecto concreto (con dos nodos rotos, cuál se informa no está fijado).
func validFlow() model.Flow {
	return model.Flow{
		FlowID:  "menu-soporte",
		Version: 1,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  "¿En qué te ayudo?\n1) Ventas\n2) Soporte",
				Options: map[string]string{"1": "ventas", "2": "soporte"},
			},
			"ventas":  {Type: model.NodeTypeMessage, Text: "Te paso con Ventas."},
			"soporte": {Type: model.NodeTypeMessage, Text: "Cuéntame."},
		},
	}
}

// mutateNode cambia un nodo de la definición (los nodos son valores del mapa).
func mutateNode(f *model.Flow, id string, change func(n *model.Node)) {
	n := f.Nodes[id]
	change(&n)
	f.Nodes[id] = n
}

// asSurvey convierte el nodo raíz en una pregunta de encuesta válida.
func asSurvey(f *model.Flow) {
	mutateNode(f, "root", func(n *model.Node) {
		n.Type = model.NodeTypeSurveyQuestion
		n.QuestionID = "q1"
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T) = error %v", v, err)
	}
	return string(b)
}

// assertInvalid exige el rechazo sobre ErrInvalidFlow con el texto literal.
func assertInvalid(t *testing.T, err error, wantReason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba el rechazo %q y no hubo error", wantReason)
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Errorf("el error no envuelve ErrInvalidFlow: %v", err)
	}
	if want := invalidPrefix + wantReason; err.Error() != want {
		t.Errorf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
}

// TestNodeTerminal_IsPrintableText blinda el centinela: se persiste en la columna
// TEXT flow_state.current_node y PostgreSQL rechaza el byte 0x00.
func TestNodeTerminal_IsPrintableText(t *testing.T) {
	if model.NodeTerminal != "__wapp_flow_end__" {
		t.Fatalf("NodeTerminal = %q; es un valor persistido y no puede cambiar", model.NodeTerminal)
	}
	if strings.IndexByte(model.NodeTerminal, 0) != -1 {
		t.Fatal("NodeTerminal contiene el byte 0x00, que PostgreSQL rechaza en columnas TEXT")
	}
	if !utf8.ValidString(model.NodeTerminal) {
		t.Fatalf("NodeTerminal no es UTF-8 válido: %q", model.NodeTerminal)
	}
	for i, r := range model.NodeTerminal {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			t.Fatalf("NodeTerminal lleva un carácter no imprimible %U en %d", r, i)
		}
	}
}

// TestConstants_KeepTheirPersistedValues: son literales del JSON de
// flow_definitions y de flow_state.vars.
func TestConstants_KeepTheirPersistedValues(t *testing.T) {
	texts := map[string]string{
		model.NodeTypeMenu:              "menu",
		model.NodeTypeMessage:           "message",
		model.NodeTypeSurveyQuestion:    "survey_question",
		model.VarOutcome:                "flow_outcome",
		string(model.OutcomeUndeclared): "",
		string(model.OutcomeCompleted):  "completed",
		string(model.OutcomeCancelled):  "cancelled",
	}
	for got, want := range texts {
		if got != want {
			t.Errorf("constante = %q; se esperaba %q", got, want)
		}
	}
	var zero model.Outcome
	if zero != model.OutcomeUndeclared {
		t.Errorf("el cero de Outcome = %q; debe ser «sin declarar»", zero)
	}
}

func TestErrInvalidFlow_KeepsItsText(t *testing.T) {
	if got := model.ErrInvalidFlow.Error(); got != "definición de flujo inválida" {
		t.Errorf("ErrInvalidFlow = %q; el texto es observable y no cambia", got)
	}
}

// fullFlow usa todos los campos de Node y de ContentRef.
func fullFlow() model.Flow {
	return model.Flow{
		FlowID: "f", Version: 3, Initial: "a",
		Nodes: map[string]model.Node{
			"a": {Type: model.NodeTypeMenu, Prompt: "p", Options: map[string]string{"1": "b"}},
			"b": {Type: model.NodeTypeMessage, Text: "t", Next: strPtr("c")},
			"c": {Type: model.NodeTypeSurveyQuestion, QuestionID: "q", Options: map[string]string{"1": "d"}},
			"d": {Type: "media", Content: &model.ContentRef{
				Source: "static", Ref: "r", Key: "k", Filename: "f.pdf",
				Mime: "application/pdf", Kind: "document", Caption: "cap",
			}},
			"e": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "catalogo"}},
		},
	}
}

const fullFlowJSON = `{"flow_id":"f","version":3,"initial":"a","nodes":{` +
	`"a":{"type":"menu","prompt":"p","options":{"1":"b"}},` +
	`"b":{"type":"message","text":"t","next":"c"},` +
	`"c":{"type":"survey_question","options":{"1":"d"},"question_id":"q"},` +
	`"d":{"type":"media","content":{"source":"static","ref":"r","key":"k","filename":"f.pdf","mime":"application/pdf","kind":"document","caption":"cap"}},` +
	`"e":{"type":"cart","content":{"source":"json","ref":"catalogo"}}}}`

// TestMarshalDefinition_WritesTheStoredJSON: las etiquetas de Flow, Node y
// ContentRef son contrato con la BD; los opcionales vacíos no se escriben.
func TestMarshalDefinition_WritesTheStoredJSON(t *testing.T) {
	data, err := model.MarshalDefinition(fullFlow())
	if err != nil {
		t.Fatalf("MarshalDefinition = error %v", err)
	}
	if string(data) != fullFlowJSON {
		t.Errorf("MarshalDefinition =\n%s\nse esperaba\n%s", data, fullFlowJSON)
	}
}

func TestUnmarshalDefinition_RoundTrips(t *testing.T) {
	got, err := model.UnmarshalDefinition([]byte(fullFlowJSON))
	if err != nil {
		t.Fatalf("UnmarshalDefinition = error %v", err)
	}
	if want := fullFlow(); !reflect.DeepEqual(got, want) {
		t.Errorf("UnmarshalDefinition = %+v; se esperaba %+v", got, want)
	}
}

func TestUnmarshalDefinition_DoesNotValidate(t *testing.T) {
	t.Run("invalid schema passes", func(t *testing.T) {
		got, err := model.UnmarshalDefinition([]byte(`{"flow_id":"","nodes":{},"extra":true}`))
		if err != nil {
			t.Fatalf("un esquema inválido no es asunto de UnmarshalDefinition: %v", err)
		}
		if got.FlowID != "" || len(got.Nodes) != 0 {
			t.Errorf("definición = %+v; se esperaba vacía", got)
		}
	})
	t.Run("malformed JSON is not wrapped", func(t *testing.T) {
		_, err := model.UnmarshalDefinition([]byte("{no es json"))
		if err == nil {
			t.Fatal("un JSON mal formado debe dar error")
		}
		if errors.Is(err, model.ErrInvalidFlow) {
			t.Errorf("UnmarshalDefinition no envuelve en ErrInvalidFlow (eso es de ParseAndValidate): %v", err)
		}
	})
}

// TestContent_SerializesWithGoNames: Content y MediaRef no llevan etiquetas (no
// son JSON de la BD); Raw nunca se serializa; ContentItem sí las lleva.
func TestContent_SerializesWithGoNames(t *testing.T) {
	content := model.Content{
		Prompt:  "p",
		Options: map[string]string{"1": "a"},
		Items:   []model.ContentItem{{Code: "1", SKU: "S", Label: "L", Price: 2.5}},
		Raw:     map[string]any{"categories": []any{}},
	}
	want := `{"Prompt":"p","Options":{"1":"a"},"Items":[{"code":"1","sku":"S","label":"L","price":2.5}]}`
	if got := mustJSON(t, content); got != want {
		t.Errorf("Content = %s; se esperaba %s", got, want)
	}
	media := model.MediaRef{Key: "k", Filename: "f", Mime: "m", Kind: "image", Caption: "c"}
	want = `{"Key":"k","Filename":"f","Mime":"m","Kind":"image","Caption":"c"}`
	if got := mustJSON(t, media); got != want {
		t.Errorf("MediaRef = %s; se esperaba %s", got, want)
	}
}

func TestConversation_JSONTags(t *testing.T) {
	full := model.Conversation{
		TenantID: "t", SessionID: "s", ContactID: "c", FlowID: "f", FlowVersion: 2,
		CurrentNode: "n", Vars: map[string]any{"k": "v"}, LastWaMessageID: "w",
		EventID: "e", OwnerEventID: "o", UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	want := `{"tenant_id":"t","session_id":"s","contact_id":"c","flow_id":"f","flow_version":2,` +
		`"current_node":"n","vars":{"k":"v"},"last_wa_message_id":"w","event_id":"e",` +
		`"owner_event_id":"o","updated_at":"2026-01-02T03:04:05Z"}`
	if got := mustJSON(t, full); got != want {
		t.Errorf("Conversation = %s; se esperaba %s", got, want)
	}
	// Los tres punteros opcionales vacíos no se escriben; updated_at siempre.
	want = `{"tenant_id":"","session_id":"","contact_id":"","flow_id":"","flow_version":0,` +
		`"current_node":"","vars":null,"updated_at":"0001-01-01T00:00:00Z"}`
	if got := mustJSON(t, model.Conversation{}); got != want {
		t.Errorf("Conversation vacía = %s; se esperaba %s", got, want)
	}
}

func TestConversation_Finished(t *testing.T) {
	cases := map[string]struct {
		node string
		want bool
	}{
		"at the sentinel":     {model.NodeTerminal, true},
		"at a real node":      {"root", false},
		"no node":             {"", false},
		"sentinel with space": {model.NodeTerminal + " ", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := (model.Conversation{CurrentNode: tc.node}).Finished(); got != tc.want {
				t.Errorf("Finished() con CurrentNode %q = %v; se esperaba %v", tc.node, got, tc.want)
			}
		})
	}
}

func TestConversation_Outcome(t *testing.T) {
	cases := map[string]struct {
		vars map[string]any
		want model.Outcome
	}{
		"nil vars":            {nil, model.OutcomeUndeclared},
		"no key":              {map[string]any{"otra": "x"}, model.OutcomeUndeclared},
		"completed":           {map[string]any{model.VarOutcome: "completed"}, model.OutcomeCompleted},
		"cancelled":           {map[string]any{model.VarOutcome: "cancelled"}, model.OutcomeCancelled},
		"empty text":          {map[string]any{model.VarOutcome: ""}, model.OutcomeUndeclared},
		"unknown text":        {map[string]any{model.VarOutcome: "abandoned"}, model.OutcomeUndeclared},
		"different case":      {map[string]any{model.VarOutcome: "Cancelled"}, model.OutcomeUndeclared},
		"unicode space":       {map[string]any{model.VarOutcome: "\u00a0cancelled"}, model.OutcomeUndeclared},
		"not a text":          {map[string]any{model.VarOutcome: 1.0}, model.OutcomeUndeclared},
		"typed Outcome value": {map[string]any{model.VarOutcome: model.OutcomeCancelled}, model.OutcomeUndeclared},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := (model.Conversation{Vars: tc.vars}).Outcome(); got != tc.want {
				t.Errorf("Outcome() con Vars %v = %q; se esperaba %q", tc.vars, got, tc.want)
			}
		})
	}
}

func TestConversation_SetOutcome(t *testing.T) {
	t.Run("declares on nil vars as plain text", func(t *testing.T) {
		var c model.Conversation
		c.SetOutcome(model.OutcomeCancelled)
		if v, ok := c.Vars[model.VarOutcome].(string); !ok || v != "cancelled" {
			t.Fatalf("Vars[%q] = %#v; se esperaba el string \"cancelled\"", model.VarOutcome, c.Vars[model.VarOutcome])
		}
		if c.Outcome() != model.OutcomeCancelled {
			t.Errorf("Outcome() = %q tras declarar cancelled", c.Outcome())
		}
	})
	t.Run("undeclared deletes the key and keeps the rest", func(t *testing.T) {
		c := model.Conversation{Vars: map[string]any{model.VarOutcome: "completed", "otra": "x"}}
		c.SetOutcome(model.OutcomeUndeclared)
		if _, present := c.Vars[model.VarOutcome]; present {
			t.Errorf("«sin declarar» debe BORRAR la clave, no escribir vacío: %v", c.Vars)
		}
		if c.Vars["otra"] != "x" {
			t.Errorf("las demás claves no se tocan: %v", c.Vars)
		}
	})
	t.Run("undeclared on nil vars leaves nil", func(t *testing.T) {
		var c model.Conversation
		c.SetOutcome(model.OutcomeUndeclared)
		if c.Vars != nil {
			t.Errorf("Vars = %v; «sin declarar» no crea el mapa", c.Vars)
		}
	})
	t.Run("overwrites a previous outcome", func(t *testing.T) {
		c := model.Conversation{Vars: map[string]any{model.VarOutcome: "cancelled"}}
		c.SetOutcome(model.OutcomeCompleted)
		if c.Outcome() != model.OutcomeCompleted || len(c.Vars) != 1 {
			t.Errorf("Vars = %v; se esperaba solo completed", c.Vars)
		}
	})
}

func TestValidate_AcceptsValidDefinitions(t *testing.T) {
	cases := map[string]func(f *model.Flow){
		"menu with two messages":   func(*model.Flow) {},
		"message chained to other": func(f *model.Flow) { mutateNode(f, "ventas", func(n *model.Node) { n.Next = strPtr("soporte") }) },
		"survey question":          asSurvey,
		// Lo que el viejo NO comprueba y el nuevo tampoco (contrato de Validate).
		"blank flow id is not trimmed": func(f *model.Flow) { f.FlowID = " \u00a0" },
		"message pointing to itself":   func(f *model.Flow) { mutateNode(f, "ventas", func(n *model.Node) { n.Next = strPtr("ventas") }) },
		"option with empty key":        func(f *model.Flow) { mutateNode(f, "root", func(n *model.Node) { n.Options[""] = "ventas" }) },
		"unreachable node":             func(f *model.Flow) { f.Nodes["isla"] = model.Node{Type: model.NodeTypeMessage} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := validFlow()
			mutate(&f)
			if err := model.Validate(f); err != nil {
				t.Errorf("Validate = %v; la definición debía aceptarse", err)
			}
		})
	}
}

func TestValidate_RejectsWithItsLiteralText(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *model.Flow)
		want   string
	}{
		{"empty flow id", func(f *model.Flow) { f.FlowID = "" }, "flow_id vacío"},
		{"version zero", func(f *model.Flow) { f.Version = 0 }, "version 0 inválida (debe ser >= 1)"},
		{"negative version", func(f *model.Flow) { f.Version = -7 }, "version -7 inválida (debe ser >= 1)"},
		{"empty nodes", func(f *model.Flow) { f.Nodes = map[string]model.Node{} }, "nodes vacío"},
		{"nil nodes", func(f *model.Flow) { f.Nodes = nil }, "nodes vacío"},
		{"sentinel as node id", func(f *model.Flow) {
			f.Nodes[model.NodeTerminal] = model.Node{Type: model.NodeTypeMessage, Text: "x"}
		}, "un id de nodo usa la clave reservada de fin de flujo"},
		{"empty initial", func(f *model.Flow) { f.Initial = "" }, "initial vacío"},
		{"initial not in nodes", func(f *model.Flow) { f.Initial = "no-existe" }, `initial "no-existe" no existe en nodes`},
		{"initial with unicode space is another id", func(f *model.Flow) { f.Initial = "root\u00a0" }, `initial "root\u00a0" no existe en nodes`},
		{"menu without options", func(f *model.Flow) {
			mutateNode(f, "root", func(n *model.Node) { n.Options = nil })
		}, `nodo menu "root" sin options`},
		{"menu option to missing node", func(f *model.Flow) {
			mutateNode(f, "root", func(n *model.Node) { n.Options = map[string]string{"9": "fantasma"} })
		}, `nodo menu "root": opción "9" apunta a nodo inexistente "fantasma"`},
		{"menu option to empty target", func(f *model.Flow) {
			mutateNode(f, "root", func(n *model.Node) { n.Options = map[string]string{"1": ""} })
		}, `nodo menu "root": opción "1" apunta a nodo inexistente ""`},
		{"menu option to the sentinel", func(f *model.Flow) {
			mutateNode(f, "root", func(n *model.Node) { n.Options = map[string]string{"1": model.NodeTerminal} })
		}, `nodo menu "root": opción "1" apunta a nodo inexistente "__wapp_flow_end__"`},
		{"message next to missing node", func(f *model.Flow) {
			mutateNode(f, "ventas", func(n *model.Node) { n.Next = strPtr("fantasma") })
		}, `nodo message "ventas": next apunta a nodo inexistente "fantasma"`},
		{"message next to the sentinel", func(f *model.Flow) {
			mutateNode(f, "ventas", func(n *model.Node) { n.Next = strPtr(model.NodeTerminal) })
		}, `nodo message "ventas": next apunta a nodo inexistente "__wapp_flow_end__"`},
		{"survey without question id", func(f *model.Flow) {
			asSurvey(f)
			mutateNode(f, "root", func(n *model.Node) { n.QuestionID = "" })
		}, `nodo "root" survey sin question_id`},
		{"survey without question id nor options", func(f *model.Flow) {
			asSurvey(f)
			mutateNode(f, "root", func(n *model.Node) { n.QuestionID, n.Options = "", nil })
		}, `nodo "root" survey sin question_id`},
		{"survey without options", func(f *model.Flow) {
			asSurvey(f)
			mutateNode(f, "root", func(n *model.Node) { n.Options = nil })
		}, `nodo survey "root" sin options`},
		{"survey option to missing node", func(f *model.Flow) {
			asSurvey(f)
			mutateNode(f, "root", func(n *model.Node) { n.Options = map[string]string{"9": "fantasma"} })
		}, `nodo survey "root": opción "9" apunta a nodo inexistente "fantasma"`},
		{"unknown node type", func(f *model.Flow) {
			mutateNode(f, "ventas", func(n *model.Node) { n.Type = "carrusel" })
		}, `nodo "ventas": tipo desconocido "carrusel"`},
		{"core type in another case", func(f *model.Flow) {
			mutateNode(f, "ventas", func(n *model.Node) { n.Type = "Message" })
		}, `nodo "ventas": tipo desconocido "Message"`},
		{"empty node type", func(f *model.Flow) {
			mutateNode(f, "ventas", func(n *model.Node) { n.Type = "" })
		}, `nodo "ventas": tipo desconocido ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := validFlow()
			tc.mutate(&f)
			assertInvalid(t, model.Validate(f), tc.want)
		})
	}
}

// TestValidate_ReportsTheFirstDefectInOrder: con varios defectos de cabecera a
// la vez manda el orden del contrato.
func TestValidate_ReportsTheFirstDefectInOrder(t *testing.T) {
	broken := model.Flow{}
	steps := []struct {
		want string
		fix  func(f *model.Flow)
	}{
		{"flow_id vacío", func(f *model.Flow) { f.FlowID = "f" }},
		{"version 0 inválida (debe ser >= 1)", func(f *model.Flow) { f.Version = 1 }},
		{"nodes vacío", func(f *model.Flow) {
			f.Nodes = map[string]model.Node{model.NodeTerminal: {Type: model.NodeTypeMessage}}
		}},
		{"un id de nodo usa la clave reservada de fin de flujo", func(f *model.Flow) {
			f.Nodes = map[string]model.Node{"root": {Type: model.NodeTypeMessage}}
		}},
		{"initial vacío", func(f *model.Flow) { f.Initial = "otro" }},
		{`initial "otro" no existe en nodes`, func(f *model.Flow) { f.Initial = "root" }},
	}
	for _, step := range steps {
		assertInvalid(t, model.Validate(broken), step.want)
		step.fix(&broken)
	}
	if err := model.Validate(broken); err != nil {
		t.Errorf("arreglados todos los defectos, Validate = %v", err)
	}
}

func TestValidate_ModuleTypes(t *testing.T) {
	withType := func(nodeType string) model.Flow {
		f := validFlow()
		mutateNode(&f, "ventas", func(n *model.Node) { n.Type, n.Text = nodeType, "" })
		return f
	}
	t.Run("declared module type is accepted loosely", func(t *testing.T) {
		if err := model.Validate(withType("cart"), "media", "cart"); err != nil {
			t.Errorf("un tipo declarado como módulo debe validar: %v", err)
		}
	})
	t.Run("undeclared module type is unknown", func(t *testing.T) {
		assertInvalid(t, model.Validate(withType("cart")), `nodo "ventas": tipo desconocido "cart"`)
	})
	t.Run("other declared modules do not help", func(t *testing.T) {
		assertInvalid(t, model.Validate(withType("carrusel"), "cart"), `nodo "ventas": tipo desconocido "carrusel"`)
	})
	t.Run("module type match is exact", func(t *testing.T) {
		assertInvalid(t, model.Validate(withType("cart\u00a0"), "cart"), `nodo "ventas": tipo desconocido "cart\u00a0"`)
	})
	t.Run("declaring a core type does not relax it", func(t *testing.T) {
		f := validFlow()
		mutateNode(&f, "root", func(n *model.Node) { n.Options = nil })
		assertInvalid(t, model.Validate(f, model.NodeTypeMenu), `nodo menu "root" sin options`)
	})
	t.Run("empty type declared as module is accepted", func(t *testing.T) {
		if err := model.Validate(withType(""), ""); err != nil {
			t.Errorf("el viejo acepta un tipo vacío si se declara como módulo: %v", err)
		}
	})
}

func TestParseAndValidate(t *testing.T) {
	t.Run("malformed JSON is wrapped", func(t *testing.T) {
		got, err := model.ParseAndValidate([]byte("{no es json"))
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("se esperaba ErrInvalidFlow por JSON mal formado; error = %v", err)
		}
		if !strings.HasPrefix(err.Error(), invalidPrefix+"JSON mal formado: ") {
			t.Errorf("texto del error = %q", err.Error())
		}
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Errorf("el error de encoding/json debe seguir en la cadena: %v", err)
		}
		if !reflect.DeepEqual(got, model.Flow{}) {
			t.Errorf("con error se devuelve el Flow cero; llegó %+v", got)
		}
	})
	t.Run("valid JSON with invalid schema returns the Validate error", func(t *testing.T) {
		data := []byte(`{"flow_id":"f","version":1,"initial":"x","nodes":{"root":{"type":"message","text":"hola"}}}`)
		got, err := model.ParseAndValidate(data)
		assertInvalid(t, err, `initial "x" no existe en nodes`)
		if !reflect.DeepEqual(got, model.Flow{}) {
			t.Errorf("con error se devuelve el Flow cero; llegó %+v", got)
		}
	})
	t.Run("JSON null is an empty definition", func(t *testing.T) {
		_, err := model.ParseAndValidate([]byte("null"))
		assertInvalid(t, err, "flow_id vacío")
	})
	t.Run("module types are passed to Validate", func(t *testing.T) {
		data := []byte(`{"flow_id":"tienda","version":1,"initial":"root","nodes":{"root":{"type":"cart"}}}`)
		if _, err := model.ParseAndValidate(data, "cart"); err != nil {
			t.Errorf("con cart declarado debe validar: %v", err)
		}
		_, err := model.ParseAndValidate(data)
		assertInvalid(t, err, `nodo "root": tipo desconocido "cart"`)
	})
	t.Run("round trip of a valid definition", func(t *testing.T) {
		want := fullFlow()
		data, err := model.MarshalDefinition(want)
		if err != nil {
			t.Fatalf("MarshalDefinition = error %v", err)
		}
		got, err := model.ParseAndValidate(data, "media", "cart")
		if err != nil {
			t.Fatalf("ParseAndValidate = error %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("la ida y vuelta alteró la definición: %+v", got)
		}
	})
}
