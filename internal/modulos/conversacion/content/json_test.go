package content_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// newJSON fija la firma del constructor: de un content.Store sale un *content.JSON.
func newJSON(store content.Store) *content.JSON { return content.NewJSON(store) }

// El adapter JSON lee (tenant, ref) del Store y deserializa Prompt, Options e Items;
// el blob entero queda además en Raw, con las claves que el contrato no tipa.
func TestJSON_Resolve_DecodesBlob(t *testing.T) {
	var store content.Store = &memStore{blobs: map[string][]byte{
		"t1|catalogo": []byte(`{"prompt":"Hola\n1) A","options":{"1":"a"},` +
			`"items":[{"code":"1","sku":"CAF","label":"Café","price":2.5}],"categories":["bebidas"]}`),
	}}
	adapter := newJSON(store)

	got, err := adapter.Resolve(context.Background(), "t1", model.Node{Type: model.NodeTypeMenu, Content: jsonRef("catalogo")})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Prompt != "Hola\n1) A" {
		t.Errorf("Prompt = %q", got.Prompt)
	}
	if !maps.Equal(got.Options, map[string]string{"1": "a"}) {
		t.Errorf("Options = %v", got.Options)
	}
	want := []model.ContentItem{{Code: "1", SKU: "CAF", Label: "Café", Price: 2.5}}
	if !slices.Equal(got.Items, want) {
		t.Errorf("Items = %v, quiero %v", got.Items, want)
	}
	categories, ok := got.Raw["categories"].([]any)
	if !ok || len(categories) != 1 || categories[0] != "bebidas" {
		t.Errorf("Raw[categories] = %v, quiero [bebidas]", got.Raw["categories"])
	}
	if got.Raw["prompt"] != "Hola\n1) A" {
		t.Errorf("Raw[prompt] = %v, quiero el blob entero también en Raw", got.Raw["prompt"])
	}
}

// El blob se pide con el tenant y la ref del nodo, y el de otro tenant no se ve.
func TestJSON_Resolve_ReadsByTenantAndRef(t *testing.T) {
	store := &memStore{blobs: map[string][]byte{"t1|menu": []byte(`{"prompt":"del t1"}`)}}
	adapter := content.NewJSON(store)

	if _, err := adapter.Resolve(context.Background(), "t2", model.Node{Content: jsonRef("menu")}); err == nil {
		t.Error("el tenant t2 resolvió un blob que solo tiene t1")
	}
	if !slices.Equal(store.calls, []string{"t2|menu"}) {
		t.Errorf("consultas al Store = %v, quiero [t2|menu]", store.calls)
	}
}

// Sin `content` o con la ref vacía, el error es el literal y el Store no se consulta.
func TestJSON_Resolve_RequiresRef(t *testing.T) {
	cases := map[string]struct {
		node model.Node
		want string
	}{
		"node without content": {
			model.Node{Type: model.NodeTypeMenu},
			invalidPrefix + "el adapter json exige node.content con una ref",
		},
		"empty ref": {
			model.Node{Content: jsonRef("")},
			invalidPrefix + "el adapter json exige una ref no vacía",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &memStore{}
			got, err := content.NewJSON(store).Resolve(context.Background(), "t1", tc.node)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, quiero %q", err, tc.want)
			}
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Errorf("error no envuelto en ErrInvalidFlow: %v", err)
			}
			if len(store.calls) != 0 {
				t.Errorf("el Store se consultó %d veces, quiero 0", len(store.calls))
			}
			if got.Prompt != "" || got.Options != nil || got.Items != nil || got.Raw != nil {
				t.Errorf("Content = %+v, quiero el valor cero", got)
			}
		})
	}
}

// Un fallo del Store se envuelve en ErrInvalidFlow sin perder el error original.
func TestJSON_Resolve_StoreErrorIsWrapped(t *testing.T) {
	boom := errors.New("boom")
	adapter := content.NewJSON(&memStore{err: boom})

	_, err := adapter.Resolve(context.Background(), "t1", model.Node{Content: jsonRef("x")})
	want := invalidPrefix + `leer contenido "x" del tenant: boom`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, quiero %q", err, want)
	}
	if !errors.Is(err, model.ErrInvalidFlow) {
		t.Errorf("error no envuelto en ErrInvalidFlow: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("el error del Store se perdió: %v", err)
	}
}

// Un blob que no es JSON válido para el contrato da error controlado, con el error
// de encoding/json envuelto.
func TestJSON_Resolve_MalformedBlob(t *testing.T) {
	blobs := map[string][]byte{
		"syntax error":        []byte(`{not-json`),
		"not an object":       []byte(`[1,2]`),
		"wrong type in field": []byte(`{"options":"no es un mapa"}`),
	}
	for name, blob := range blobs {
		t.Run(name, func(t *testing.T) {
			adapter := content.NewJSON(&memStore{blobs: map[string][]byte{"t1|bad": blob}})

			_, err := adapter.Resolve(context.Background(), "t1", model.Node{Content: jsonRef("bad")})
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Fatalf("error = %v, quiero uno envuelto en ErrInvalidFlow", err)
			}
			// El detalle final es el de encoding/json: se lee del propio error para no
			// atar el test al texto de una versión de Go.
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			var detail string
			switch {
			case errors.As(err, &syntaxErr):
				detail = syntaxErr.Error()
			case errors.As(err, &typeErr):
				detail = typeErr.Error()
			default:
				t.Fatalf("el error de encoding/json se perdió: %v", err)
			}
			want := invalidPrefix + `blob de contenido "bad" mal formado: ` + detail
			if err.Error() != want {
				t.Errorf("error = %q, quiero %q", err.Error(), want)
			}
		})
	}
}

// Un blob `null` o `{}` es válido: Content vacío, sin error.
func TestJSON_Resolve_EmptyBlobs(t *testing.T) {
	for _, blob := range []string{`null`, `{}`} {
		adapter := content.NewJSON(&memStore{blobs: map[string][]byte{"t1|vacio": []byte(blob)}})
		got, err := adapter.Resolve(context.Background(), "t1", model.Node{Content: jsonRef("vacio")})
		if err != nil {
			t.Fatalf("blob %s: Resolve: %v", blob, err)
		}
		if got.Prompt != "" || len(got.Options) != 0 || len(got.Items) != 0 || len(got.Raw) != 0 {
			t.Errorf("blob %s: Content = %+v, quiero vacío", blob, got)
		}
	}
}
