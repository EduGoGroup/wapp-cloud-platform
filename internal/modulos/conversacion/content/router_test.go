package content_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

func newRouter(store content.Store) content.Router {
	return content.NewRouter(content.NewStatic(), content.NewJSON(store))
}

// Sin `content`, o con source "", "static" o "inline", el Router resuelve del propio
// nodo (rama Static) y no consulta el Store, aunque el nodo traiga una ref.
func TestRouter_Resolve_StaticSources(t *testing.T) {
	refs := map[string]*model.ContentRef{
		"nil content":   nil,
		"empty source":  {Source: "", Ref: "catalogo"},
		"static source": {Source: "static", Ref: "catalogo"},
		"inline source": {Source: "inline", Ref: "catalogo"},
	}
	for name, ref := range refs {
		t.Run(name, func(t *testing.T) {
			store := &memStore{blobs: map[string][]byte{"t1|catalogo": []byte(`{"prompt":"del blob"}`)}}
			node := model.Node{
				Type:    model.NodeTypeMenu,
				Prompt:  "Hola\n1) A",
				Options: map[string]string{"1": "a"},
				Content: ref,
			}

			got, err := newRouter(store).Resolve(context.Background(), "t1", node)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Prompt != node.Prompt || !maps.Equal(got.Options, node.Options) {
				t.Errorf("Content = %+v, quiero el Prompt y las Options del nodo", got)
			}
			if got.Items != nil || got.Raw != nil {
				t.Errorf("Items = %v y Raw = %v, quiero nil (rama static)", got.Items, got.Raw)
			}
			if len(store.calls) != 0 {
				t.Errorf("el Store se consultó %d veces, quiero 0", len(store.calls))
			}
		})
	}
}

// Con source "json" el Router delega en el adapter JSON: el contenido sale del blob,
// no del nodo.
func TestRouter_Resolve_JSONSource(t *testing.T) {
	store := &memStore{blobs: map[string][]byte{
		"t1|catalogo": []byte(`{"prompt":"Menú","options":{"1":"caf"},"items":[{"code":"1","sku":"CAF","label":"Café","price":2.5}]}`),
	}}
	node := model.Node{Type: model.NodeTypeMenu, Prompt: "del nodo", Content: jsonRef("catalogo")}

	got, err := newRouter(store).Resolve(context.Background(), "t1", node)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != "Menú" || !maps.Equal(got.Options, map[string]string{"1": "caf"}) {
		t.Errorf("Content = %+v, quiero el del blob", got)
	}
	want := []model.ContentItem{{Code: "1", SKU: "CAF", Label: "Café", Price: 2.5}}
	if !slices.Equal(got.Items, want) {
		t.Errorf("Items = %v, quiero %v", got.Items, want)
	}
	if !slices.Equal(store.calls, []string{"t1|catalogo"}) {
		t.Errorf("consultas al Store = %v, quiero [t1|catalogo]", store.calls)
	}
}

// El error del adapter JSON sube tal cual por el Router.
func TestRouter_Resolve_JSONWithoutRef(t *testing.T) {
	_, err := newRouter(&memStore{}).Resolve(context.Background(), "t1", model.Node{Content: jsonRef("")})
	want := invalidPrefix + "el adapter json exige una ref no vacía"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, quiero %q", err, want)
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Errorf("error no envuelto en ErrInvalidFlow: %v", err)
	}
}

// Una fuente que el switch no conoce da el literal, con la fuente entre comillas. La
// comparación es exacta: "JSON" o " json" tampoco valen.
func TestRouter_Resolve_UnsupportedSource(t *testing.T) {
	for _, source := range []string{"http", "JSON", " json", "Static"} {
		t.Run(source, func(t *testing.T) {
			store := &memStore{}
			node := model.Node{Prompt: "P", Content: &model.ContentRef{Source: source, Ref: "x"}}

			got, err := newRouter(store).Resolve(context.Background(), "t1", node)
			want := invalidPrefix + `content source "` + source + `" no soportado`
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, quiero %q", err, want)
			}
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Errorf("error no envuelto en ErrInvalidFlow: %v", err)
			}
			if got.Prompt != "" || len(store.calls) != 0 {
				t.Errorf("Content = %+v con %d consultas, quiero vacío y 0", got, len(store.calls))
			}
		})
	}
}
