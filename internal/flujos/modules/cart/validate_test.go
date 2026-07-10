package cart

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// TestModule_ValidateNode cubre la validación estructural del nodo cart (Plan 027 ·
// Ola 1 · T6, cierra H11): un nodo sin content.source se rechaza (degradaría en
// runtime), "json" exige ref, y una declaración completa pasa.
func TestModule_ValidateNode(t *testing.T) {
	t.Parallel()
	m := New()

	cases := []struct {
		name    string
		node    model.Node
		wantErr bool
	}{
		{"sin content", model.Node{Type: NodeTypeCart}, true},
		{"content sin source", model.Node{Type: NodeTypeCart, Content: &model.ContentRef{}}, true},
		{"json sin ref", model.Node{Type: NodeTypeCart, Content: &model.ContentRef{Source: "json"}}, true},
		{"json con ref", model.Node{Type: NodeTypeCart, Content: &model.ContentRef{Source: "json", Ref: "catalogo-1"}}, false},
		{"static ok", model.Node{Type: NodeTypeCart, Content: &model.ContentRef{Source: "static", Ref: "x"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := m.ValidateNode(tc.node)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateNode(%+v) = nil, quiero error", tc.node)
				}
				if !errors.Is(err, ErrInvalidCartNode) {
					t.Fatalf("error %v no envuelve ErrInvalidCartNode", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateNode(%+v) = %v, quiero nil", tc.node, err)
			}
		})
	}
}
