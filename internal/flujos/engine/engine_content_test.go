package engine_test

import (
	"context"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
)

// No-regresión T1: con el default (static, PURO) el engine renderiza el menú
// byte-a-byte igual que el node.Prompt, sin BD.
func TestDefaultStaticRendersNodePromptByteForByte(t *testing.T) {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	e := engine.New(reg) // sin opts ⇒ default static

	_, outs, err := e.Enter(context.Background(), flowMenu(), model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if got := texts(outs); !slices.Equal(got, []string{rootPrompt}) {
		t.Fatalf("render default = %q, quiero %q (byte-a-byte)", got, []string{rootPrompt})
	}
}

// fakeSource sustituye el Prompt para probar que WithContentSource se honra: la
// resolución ocurre ANTES del Render, así que el módulo emite el Prompt resuelto.
type fakeSource struct{ prompt string }

func (f fakeSource) Resolve(_ context.Context, _ string, node model.Node) (model.Content, error) {
	return model.Content{Prompt: f.prompt, Options: node.Options}, nil
}

func TestWithContentSourceIsHonored(t *testing.T) {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	var src content.Source = fakeSource{prompt: "PROMPT-RESUELTO"}
	e := engine.New(reg, engine.WithContentSource(src))

	_, outs, err := e.Enter(context.Background(), flowMenu(), model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if got := texts(outs); !slices.Equal(got, []string{"PROMPT-RESUELTO"}) {
		t.Fatalf("render = %q, quiero el prompt resuelto por la fuente inyectada", got)
	}
}
