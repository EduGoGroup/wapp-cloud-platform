package content_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// routerStore es un content.Store en memoria para la rama json del Router.
type routerStore struct {
	blobs map[string][]byte
}

func (s routerStore) GetTenantContent(_ context.Context, tenantID, ref string) ([]byte, error) {
	b, ok := s.blobs[tenantID+"|"+ref]
	if !ok {
		return nil, errors.New("ref inexistente")
	}
	return b, nil
}

func newRouter(store content.Store) content.Router {
	return content.NewRouter(content.NewStatic(), content.NewJSON(store))
}

// El Router implementa el puerto content.Source (chequeo de compilación).
var _ content.Source = content.Router{}

// Nodo SIN content: el Router delega en Static → Content{Prompt, Options} del
// propio nodo, byte-a-byte (no-regresión de menú/encuesta).
func TestRouterNilContentDelegatesToStatic(t *testing.T) {
	r := newRouter(routerStore{})
	node := model.Node{
		Type:    model.NodeTypeMenu,
		Prompt:  "Hola\n1) A",
		Options: map[string]string{"1": "a"},
	}

	got, err := r.Resolve(context.Background(), "t1", node)
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
		t.Fatalf("Items = %v, quiero nil (rama static)", got.Items)
	}
}

// Nodo con source "static" e "inline": misma resolución PURA que el nodo nil.
func TestRouterStaticAndInlineDelegateToStatic(t *testing.T) {
	r := newRouter(routerStore{})
	for _, source := range []string{"static", "inline", ""} {
		node := model.Node{
			Type:    model.NodeTypeMenu,
			Prompt:  "P",
			Options: map[string]string{"1": "x"},
			Content: &model.ContentRef{Source: source},
		}
		got, err := r.Resolve(context.Background(), "t1", node)
		if err != nil {
			t.Fatalf("source %q: Resolve: %v", source, err)
		}
		if got.Prompt != "P" || !maps.Equal(got.Options, map[string]string{"1": "x"}) {
			t.Fatalf("source %q: got %+v", source, got)
		}
	}
}

// Nodo con source "json" y ref sembrada: el Router delega en JSON y deserializa
// el contrato (Prompt/Options/Items) del blob.
func TestRouterJSONDelegatesToJSON(t *testing.T) {
	store := routerStore{blobs: map[string][]byte{
		"t1|catalogo": []byte(`{"prompt":"Menú","options":{"1":"caf"},"items":[{"code":"1","sku":"CAF","label":"Café","price":2.5}]}`),
	}}
	r := newRouter(store)
	node := model.Node{Type: model.NodeTypeMenu, Content: &model.ContentRef{Source: "json", Ref: "catalogo"}}

	got, err := r.Resolve(context.Background(), "t1", node)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != "Menú" {
		t.Fatalf("Prompt = %q", got.Prompt)
	}
	if !maps.Equal(got.Options, map[string]string{"1": "caf"}) {
		t.Fatalf("Options = %v", got.Options)
	}
	want := model.ContentItem{Code: "1", SKU: "CAF", Label: "Café", Price: 2.5}
	if len(got.Items) != 1 || got.Items[0] != want {
		t.Fatalf("Items = %v, quiero %v", got.Items, want)
	}
}

// Source no soportado (p. ej. "http"): error controlado envuelto en ErrInvalidFlow,
// nunca pánico.
func TestRouterUnsupportedSourceIsControlledError(t *testing.T) {
	r := newRouter(routerStore{})
	node := model.Node{Type: model.NodeTypeMenu, Content: &model.ContentRef{Source: "http", Ref: "x"}}

	_, err := r.Resolve(context.Background(), "t1", node)
	if err == nil {
		t.Fatal("esperaba error por source no soportado")
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("error no envuelto en ErrInvalidFlow: %v", err)
	}
}

// Source "json" sin ref: error controlado (heredado de json.Resolve), envuelto.
func TestRouterJSONWithoutRefIsControlledError(t *testing.T) {
	r := newRouter(routerStore{})
	node := model.Node{Type: model.NodeTypeMenu, Content: &model.ContentRef{Source: "json"}}

	_, err := r.Resolve(context.Background(), "t1", node)
	if err == nil {
		t.Fatal("esperaba error por json sin ref")
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("error no envuelto en ErrInvalidFlow: %v", err)
	}
}
