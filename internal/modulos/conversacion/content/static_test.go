package content_test

import (
	"context"
	"maps"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// El adapter estático copia Prompt/Options del propio nodo y deja Items y Raw en nil,
// declare o no el nodo un `content`.
func TestStatic_Resolve_CopiesNodeFields(t *testing.T) {
	static := content.NewStatic()

	node := model.Node{
		Type:    model.NodeTypeMenu,
		Prompt:  "¿En qué te ayudo?\n1) Ventas\n2) Soporte",
		Options: map[string]string{"1": "ventas", "2": "soporte"},
		Content: &model.ContentRef{Source: "json", Ref: "ignorada"},
	}

	got, err := static.Resolve(context.Background(), "tenant-x", node)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != node.Prompt {
		t.Errorf("Prompt = %q, quiero %q", got.Prompt, node.Prompt)
	}
	if !maps.Equal(got.Options, node.Options) {
		t.Errorf("Options = %v, quiero %v", got.Options, node.Options)
	}
	if got.Items != nil || got.Raw != nil {
		t.Errorf("Items = %v y Raw = %v, quiero los dos nil", got.Items, got.Raw)
	}
}

// Un nodo vacío resuelve a un Content vacío, sin error.
func TestStatic_Resolve_EmptyNode(t *testing.T) {
	got, err := content.Static{}.Resolve(context.Background(), "", model.Node{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != "" || got.Options != nil || got.Items != nil || got.Raw != nil {
		t.Errorf("Content = %+v, quiero el valor cero", got)
	}
}
