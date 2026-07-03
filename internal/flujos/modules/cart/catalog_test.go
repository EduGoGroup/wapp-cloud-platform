package cart

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// rawFromJSON simula el round-trip JSONB: decodifica un literal JSON a
// map[string]any (números como float64, claves string), tal como llega
// Content.Raw poblado por el adapter json desde la columna JSONB.
func rawFromJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("literal JSON de prueba inválido: %v", err)
	}
	return m
}

func TestParseCatalog(t *testing.T) {
	validBlob := `{
	  "categories": [
	    { "code": "1", "label": "Bebidas", "items": [
	      { "code": "1", "sku": "CAFE", "label": "Café", "price": 2.5, "description": "Espresso doble" },
	      { "code": "2", "sku": "TE",   "label": "Té",   "price": 2.0, "description": "Verde o negro" }
	    ] },
	    { "code": "2", "label": "Postres", "items": [
	      { "code": "1", "sku": "FLAN", "label": "Flan", "price": 3.0, "description": "Casero" }
	    ] }
	  ]
	}`

	tests := []struct {
		name     string
		content  model.Content
		wantErr  bool
		validate func(t *testing.T, got Catalog)
	}{
		{
			name:    "catálogo válido multinivel con números float64 (round-trip JSONB)",
			content: model.Content{Raw: rawFromJSON(t, validBlob)},
			validate: func(t *testing.T, got Catalog) {
				if len(got.Categories) != 2 {
					t.Fatalf("categorías = %d, quiero 2", len(got.Categories))
				}
				bebidas := got.Categories[0]
				if bebidas.Code != "1" || bebidas.Label != "Bebidas" {
					t.Errorf("categoría[0] = %+v", bebidas)
				}
				if len(bebidas.Items) != 2 {
					t.Fatalf("items de Bebidas = %d, quiero 2", len(bebidas.Items))
				}
				cafe := bebidas.Items[0]
				if cafe.Code != "1" || cafe.SKU != "CAFE" || cafe.Label != "Café" || cafe.Price != 2.5 || cafe.Description != "Espresso doble" {
					t.Errorf("artículo café = %+v", cafe)
				}
				postres := got.Categories[1]
				if postres.Code != "2" || len(postres.Items) != 1 || postres.Items[0].SKU != "FLAN" {
					t.Errorf("categoría Postres = %+v", postres)
				}
			},
		},
		{
			name:    "artículo sin description (campo opcional ausente)",
			content: model.Content{Raw: rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":1}]}]}`)},
			validate: func(t *testing.T, got Catalog) {
				if got.Categories[0].Items[0].Description != "" {
					t.Errorf("Description debería ser vacía, got %q", got.Categories[0].Items[0].Description)
				}
			},
		},
		{
			name:    "categoría sin items (items ausente → slice vacío, sin error)",
			content: model.Content{Raw: rawFromJSON(t, `{"categories":[{"code":"1","label":"Vacía"}]}`)},
			validate: func(t *testing.T, got Catalog) {
				if len(got.Categories) != 1 {
					t.Fatalf("categorías = %d, quiero 1", len(got.Categories))
				}
				if len(got.Categories[0].Items) != 0 {
					t.Errorf("items = %d, quiero 0", len(got.Categories[0].Items))
				}
			},
		},
		{
			name:    "Raw nil → error",
			content: model.Content{Raw: nil},
			wantErr: true,
		},
		{
			name:    "sin categorías → error",
			content: model.Content{Raw: rawFromJSON(t, `{"categories":[]}`)},
			wantErr: true,
		},
		{
			name:    "blob malformado (categories con tipo equivocado) → error",
			content: model.Content{Raw: rawFromJSON(t, `{"categories":"no-soy-un-array"}`)},
			wantErr: true,
		},
		{
			name:    "blob malformado (price no numérico) → error",
			content: model.Content{Raw: rawFromJSON(t, `{"categories":[{"code":"1","label":"X","items":[{"code":"1","sku":"A","label":"A","price":"gratis"}]}]}`)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCatalog(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("quería error, got nil (catalog=%+v)", got)
				}
				if !errors.Is(err, model.ErrInvalidFlow) {
					t.Errorf("error no envuelve model.ErrInvalidFlow: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if tt.validate != nil {
				tt.validate(t, got)
			}
		})
	}
}
