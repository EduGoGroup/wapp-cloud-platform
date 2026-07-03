package model_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// TestNodeTerminalIsTextSafe blinda el centinela de fin de flujo: cuando una
// conversación termina, Conversation.CurrentNode = model.NodeTerminal se persiste
// en la columna TEXT flow_state.current_node. PostgreSQL rechaza el byte nulo
// 0x00 ("invalid byte sequence for encoding UTF8", SQLSTATE 22021). El
// MemoryRepository (mapas Go) toleraba un centinela con \x00 y enmascaró el bug;
// solo el e2e real contra PostgreSQL lo destapó. Este guard (sin BD, CI-safe)
// evita que recurra: el centinela debe ser UTF-8 válido, sin byte nulo ni
// caracteres de control.
func TestNodeTerminalIsTextSafe(t *testing.T) {
	if model.NodeTerminal == "" {
		t.Fatal("NodeTerminal no debe ser vacío (debe distinguirse de un id de nodo ausente)")
	}
	if i := strings.IndexByte(model.NodeTerminal, 0); i != -1 {
		t.Fatalf("NodeTerminal contiene byte nulo 0x00 en %d: PostgreSQL lo rechaza en columnas TEXT (SQLSTATE 22021)", i)
	}
	if !utf8.ValidString(model.NodeTerminal) {
		t.Fatalf("NodeTerminal no es UTF-8 válido: %q", model.NodeTerminal)
	}
	for i, r := range model.NodeTerminal {
		if unicode.IsControl(r) {
			t.Fatalf("NodeTerminal contiene carácter de control %U en %d: %q", r, i, model.NodeTerminal)
		}
		if !unicode.IsPrint(r) {
			t.Fatalf("NodeTerminal contiene carácter no imprimible %U en %d: %q", r, i, model.NodeTerminal)
		}
	}
}

func ptr(s string) *string { return &s }

// validFlow es una definición correcta de partida (la del ejemplo §4) que cada
// caso de test puede mutar para provocar un fallo concreto.
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
			"ventas":  {Type: model.NodeTypeMessage, Text: "Te paso con Ventas.", Next: nil},
			"soporte": {Type: model.NodeTypeMessage, Text: "Cuéntame.", Next: nil},
		},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(f *model.Flow)
		wantErr bool
	}{
		{
			name:    "válida aceptada",
			mutate:  func(*model.Flow) {},
			wantErr: false,
		},
		{
			name:    "válida con encadenado message->message",
			mutate:  func(f *model.Flow) { n := f.Nodes["ventas"]; n.Next = ptr("soporte"); f.Nodes["ventas"] = n },
			wantErr: false,
		},
		{
			name:    "flow_id vacío",
			mutate:  func(f *model.Flow) { f.FlowID = "" },
			wantErr: true,
		},
		{
			name:    "version inválida",
			mutate:  func(f *model.Flow) { f.Version = 0 },
			wantErr: true,
		},
		{
			name:    "nodes vacío",
			mutate:  func(f *model.Flow) { f.Nodes = map[string]model.Node{} },
			wantErr: true,
		},
		{
			name:    "initial vacío",
			mutate:  func(f *model.Flow) { f.Initial = "" },
			wantErr: true,
		},
		{
			name:    "initial ausente en nodes",
			mutate:  func(f *model.Flow) { f.Initial = "no-existe" },
			wantErr: true,
		},
		{
			name:    "menu sin options",
			mutate:  func(f *model.Flow) { n := f.Nodes["root"]; n.Options = nil; f.Nodes["root"] = n },
			wantErr: true,
		},
		{
			name:    "opción a nodo inexistente",
			mutate:  func(f *model.Flow) { n := f.Nodes["root"]; n.Options["9"] = "fantasma"; f.Nodes["root"] = n },
			wantErr: true,
		},
		{
			name:    "message next a nodo inexistente",
			mutate:  func(f *model.Flow) { n := f.Nodes["ventas"]; n.Next = ptr("fantasma"); f.Nodes["ventas"] = n },
			wantErr: true,
		},
		{
			name:    "tipo de nodo desconocido",
			mutate:  func(f *model.Flow) { n := f.Nodes["ventas"]; n.Type = "carrusel"; f.Nodes["ventas"] = n },
			wantErr: true,
		},
		{
			name:    "id de nodo reservado (centinela)",
			mutate:  func(f *model.Flow) { f.Nodes[model.NodeTerminal] = model.Node{Type: model.NodeTypeMessage, Text: "x"} },
			wantErr: true,
		},
		{
			name: "survey_question válido aceptado",
			mutate: func(f *model.Flow) {
				n := f.Nodes["root"]
				n.Type = model.NodeTypeSurveyQuestion
				n.QuestionID = "q1"
				f.Nodes["root"] = n
			},
			wantErr: false,
		},
		{
			name: "survey_question sin question_id",
			mutate: func(f *model.Flow) {
				n := f.Nodes["root"]
				n.Type = model.NodeTypeSurveyQuestion
				n.QuestionID = ""
				f.Nodes["root"] = n
			},
			wantErr: true,
		},
		{
			name: "survey_question sin options",
			mutate: func(f *model.Flow) {
				n := f.Nodes["root"]
				n.Type = model.NodeTypeSurveyQuestion
				n.QuestionID = "q1"
				n.Options = nil
				f.Nodes["root"] = n
			},
			wantErr: true,
		},
		{
			name: "survey_question con destino inexistente",
			mutate: func(f *model.Flow) {
				n := f.Nodes["root"]
				n.Type = model.NodeTypeSurveyQuestion
				n.QuestionID = "q1"
				n.Options["9"] = "fantasma"
				f.Nodes["root"] = n
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := validFlow()
			tc.mutate(&f)
			err := model.Validate(f)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("esperaba error, obtuve nil")
				}
				if !errors.Is(err, model.ErrInvalidFlow) {
					t.Fatalf("error no envuelve ErrInvalidFlow: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("no esperaba error, obtuve: %v", err)
			}
		})
	}
}

// TestValidate_ModuleType cubre el follow-up del Plan 016: un nodo cuyo Type es de
// un MÓDULO enchufable (p. ej. "cart") valida cuando ese tipo se declara en
// moduleTypes (validación laxa: sin exigir options/question_id), pero se rechaza si
// no se declara. Un tipo que no es ni core ni de módulo sigue fallando.
func TestValidate_ModuleType(t *testing.T) {
	// Un nodo "cart" (tipo de módulo, no core) en lugar de un message terminal.
	cartFlow := func() model.Flow {
		f := validFlow()
		n := f.Nodes["ventas"]
		n.Type = "cart"
		n.Text = ""
		f.Nodes["ventas"] = n
		return f
	}

	t.Run("cart aceptado cuando se declara como módulo", func(t *testing.T) {
		if err := model.Validate(cartFlow(), "cart"); err != nil {
			t.Fatalf("cart declarado como módulo debe validar; obtuve: %v", err)
		}
	})

	t.Run("cart rechazado si no se declara", func(t *testing.T) {
		err := model.Validate(cartFlow())
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("sin declararlo, cart es tipo desconocido: esperaba ErrInvalidFlow, obtuve: %v", err)
		}
	})

	t.Run("tipo ni core ni módulo sigue fallando", func(t *testing.T) {
		f := validFlow()
		n := f.Nodes["ventas"]
		n.Type = "carrusel"
		f.Nodes["ventas"] = n
		err := model.Validate(f, "cart") // se declaran otros módulos, pero no "carrusel"
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("un tipo desconocido debe fallar aunque haya módulos declarados; obtuve: %v", err)
		}
	})
}

func TestParseAndValidate(t *testing.T) {
	t.Run("JSON mal formado rechazado", func(t *testing.T) {
		_, err := model.ParseAndValidate([]byte("{no es json"))
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("esperaba ErrInvalidFlow por JSON mal formado, obtuve: %v", err)
		}
	})

	t.Run("JSON válido pero esquema inválido rechazado", func(t *testing.T) {
		// initial inexistente
		data := []byte(`{"flow_id":"f","version":1,"initial":"x","nodes":{"root":{"type":"message","text":"hola"}}}`)
		_, err := model.ParseAndValidate(data)
		if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("esperaba ErrInvalidFlow por esquema, obtuve: %v", err)
		}
	})

	t.Run("nodo de tipo módulo aceptado al declararlo", func(t *testing.T) {
		data := []byte(`{"flow_id":"tienda","version":1,"initial":"root","nodes":{"root":{"type":"cart"}}}`)
		if _, err := model.ParseAndValidate(data, "cart"); err != nil {
			t.Fatalf("con 'cart' declarado como módulo debe validar; obtuve: %v", err)
		}
		if _, err := model.ParseAndValidate(data); err == nil || !errors.Is(err, model.ErrInvalidFlow) {
			t.Fatalf("sin declarar 'cart' debe rechazar; obtuve: %v", err)
		}
	})

	t.Run("definición válida aceptada y round-trip", func(t *testing.T) {
		f := validFlow()
		data, err := model.MarshalDefinition(f)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got, err := model.ParseAndValidate(data)
		if err != nil {
			t.Fatalf("parse+validate: %v", err)
		}
		if got.FlowID != f.FlowID || got.Initial != f.Initial || len(got.Nodes) != len(f.Nodes) {
			t.Fatalf("round-trip alteró la definición: %+v", got)
		}
	})
}
