package content_test

import (
	"context"
	"maps"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// El adapter estático es PURO: copia Prompt/Options del propio nodo sin I/O.
func TestStaticResolveCopiesNodeFields(t *testing.T) {
	var _ content.Source = content.NewStatic()

	node := model.Node{
		Type:    model.NodeTypeMenu,
		Prompt:  "¿En qué te ayudo?\n1) Ventas\n2) Soporte",
		Options: map[string]string{"1": "ventas", "2": "soporte"},
	}

	got, err := content.NewStatic().Resolve(context.Background(), "tenant-x", node)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != node.Prompt {
		t.Fatalf("Prompt = %q, quiero %q", got.Prompt, node.Prompt)
	}
	if !maps.Equal(got.Options, node.Options) {
		t.Fatalf("Options = %v, quiero %v", got.Options, node.Options)
	}
	if got.Items != nil {
		t.Fatalf("Items debe ser nil en static, fue %v", got.Items)
	}
}
