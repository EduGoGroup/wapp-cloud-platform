package cart_test

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// El carrito tiene la capacidad opcional de validar sus nodos en el alta admin.
var _ modules.NodeValidator = cart.Module{}

// ValidateNode exige una fuente de catálogo resoluble (H11): sin content.source, o
// con la fuente "json" sin ref, el nodo se rechaza envolviendo ErrInvalidCartNode.
func TestValidateNode(t *testing.T) {
	const base = "nodo cart inválido"
	if cart.ErrInvalidCartNode.Error() != base {
		t.Fatalf("ErrInvalidCartNode = %q, quiero %q", cart.ErrInvalidCartNode, base)
	}
	cases := []struct {
		name    string
		content *model.ContentRef
		wantErr string // "" ⇒ válido
	}{
		{name: "nil content", content: nil,
			wantErr: base + ": sin content.source (el catálogo no sería resoluble en runtime)"},
		{name: "empty source", content: &model.ContentRef{},
			wantErr: base + ": sin content.source (el catálogo no sería resoluble en runtime)"},
		{name: "empty source with ref", content: &model.ContentRef{Ref: "catalogo"},
			wantErr: base + ": sin content.source (el catálogo no sería resoluble en runtime)"},
		{name: "json without ref", content: &model.ContentRef{Source: "json"},
			wantErr: base + `: content.source "json" sin ref (falta la clave del catálogo en tenant_content)`},
		{name: "json with ref", content: &model.ContentRef{Source: "json", Ref: "catalogo"}},
		{name: "static without ref", content: &model.ContentRef{Source: "static"}},
		{name: "another source without ref", content: &model.ContentRef{Source: "JSON"}},
		{name: "blank source is not empty", content: &model.ContentRef{Source: " "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// El valor cero del Module valida igual: no depende de cómo se construyó.
			err := cart.Module{}.ValidateNode(model.Node{Type: cart.NodeTypeCart, Content: tc.content})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateNode = %v, quiero nil", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("ValidateNode = %v\nquiero %q", err, tc.wantErr)
			}
			if !errors.Is(err, cart.ErrInvalidCartNode) {
				t.Errorf("el error %v no envuelve ErrInvalidCartNode", err)
			}
		})
	}
}
