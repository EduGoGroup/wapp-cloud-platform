package model_test

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

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
