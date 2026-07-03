package content_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// fakeStore es un ContentStore en memoria: mapea (tenant|ref) → blob, o un error
// inyectado si la ref no existe.
type fakeStore struct {
	blobs map[string][]byte
	err   error
}

func (f fakeStore) GetTenantContent(_ context.Context, tenantID, ref string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.blobs[tenantID+"|"+ref]
	if !ok {
		return nil, errors.New("ref inexistente")
	}
	return b, nil
}

func ref(s string) *model.ContentRef { return &model.ContentRef{Source: "json", Ref: s} }

// El adapter JSON deserializa Prompt/Options del blob por-tenant.
func TestJSONResolvePromptOptions(t *testing.T) {
	store := fakeStore{blobs: map[string][]byte{
		"t1|menu-root": []byte(`{"prompt":"Hola\n1) A","options":{"1":"a"}}`),
	}}
	var src content.Source = content.NewJSON(store)

	node := model.Node{Type: model.NodeTypeMenu, Content: ref("menu-root")}
	got, err := src.Resolve(context.Background(), "t1", node)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != "Hola\n1) A" {
		t.Fatalf("Prompt = %q", got.Prompt)
	}
	if !maps.Equal(got.Options, map[string]string{"1": "a"}) {
		t.Fatalf("Options = %v", got.Options)
	}
}

// El adapter JSON deserializa Items (catálogo de pedido).
func TestJSONResolveItems(t *testing.T) {
	store := fakeStore{blobs: map[string][]byte{
		"t1|catalogo": []byte(`{"items":[{"code":"1","sku":"CAF","label":"Café","price":2.5}]}`),
	}}
	src := content.NewJSON(store)

	node := model.Node{Type: model.NodeTypeMenu, Content: ref("catalogo")}
	got, err := src.Resolve(context.Background(), "t1", node)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []model.ContentItem{{Code: "1", SKU: "CAF", Label: "Café", Price: 2.5}}
	if len(got.Items) != 1 || got.Items[0] != want[0] {
		t.Fatalf("Items = %v, quiero %v", got.Items, want)
	}
}

// node.Content == nil ⇒ error controlado (el adapter json exige ref), envuelto en
// ErrInvalidFlow, nunca pánico.
func TestJSONResolveMissingRefIsControlledError(t *testing.T) {
	src := content.NewJSON(fakeStore{blobs: map[string][]byte{}})

	_, err := src.Resolve(context.Background(), "t1", model.Node{Type: model.NodeTypeMenu})
	if err == nil {
		t.Fatal("esperaba error por node.Content ausente")
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("error no envuelto en ErrInvalidFlow: %v", err)
	}
}

// Un store que falla (ref inexistente / error) ⇒ error controlado envuelto.
func TestJSONResolveStoreErrorIsWrapped(t *testing.T) {
	src := content.NewJSON(fakeStore{err: errors.New("boom")})

	_, err := src.Resolve(context.Background(), "t1", model.Node{Content: ref("x")})
	if err == nil {
		t.Fatal("esperaba error del store")
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("error no envuelto en ErrInvalidFlow: %v", err)
	}
}

// Un blob mal formado ⇒ error controlado envuelto, no pánico.
func TestJSONResolveMalformedBlob(t *testing.T) {
	store := fakeStore{blobs: map[string][]byte{"t1|bad": []byte(`{not-json`)}}
	src := content.NewJSON(store)

	_, err := src.Resolve(context.Background(), "t1", model.Node{Content: ref("bad")})
	if err == nil {
		t.Fatal("esperaba error por blob mal formado")
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("error no envuelto en ErrInvalidFlow: %v", err)
	}
}
