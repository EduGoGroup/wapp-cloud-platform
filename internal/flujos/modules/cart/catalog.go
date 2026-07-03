// Package cart es el módulo del carrito conversacional (Plan 016): una
// sub-máquina de navegación por un catálogo de dos niveles (categorías →
// artículos) que acumula líneas de pedido. Los tipos de este archivo son
// PUROS (sin I/O ni dependencias externas más allá del contrato model): el
// árbol de catálogo del módulo y su parser desde model.Content.Raw.
//
// El contrato genérico model.Content (Plan 015) NO conoce el concepto de
// "categoría": se mantiene agnóstico del dominio. El árbol del carrito viaja en
// el blob crudo model.Content.Raw (map[string]any, poblado por el adapter json)
// y este módulo define sus propios tipos que lo deserializan (design.md §3.1).
package cart

import (
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Catalog es el árbol de catálogo del carrito: una lista ordenada de categorías,
// cada una con sus artículos. Tipo PROPIO del módulo (design.md §3.1).
type Catalog struct {
	Categories []Category
}

// Category es un grupo de artículos. Code es lo que teclea el usuario para
// elegir la categoría ("1", "2", …); Label es su etiqueta visible.
type Category struct {
	Code  string
	Label string
	Items []Article
}

// Article es un artículo del catálogo. Code es lo que teclea el usuario dentro
// de la categoría; SKU es el identificador de negocio (viaja en los efectos);
// Price es el precio unitario; Description es el detalle mostrado bajo "ver
// descripción" (opcional).
type Article struct {
	Code        string
	SKU         string
	Label       string
	Price       float64
	Description string
}

// catalogBlob es la forma JSON esperada del blob de catálogo por-tenant (tabla
// tenant_content del Plan 015, resuelto por el adapter json). El seed del e2e
// (T5) depende de esta forma exacta:
//
//	{
//	  "categories": [
//	    { "code": "1", "label": "Bebidas", "items": [
//	      { "code": "1", "sku": "CAFE", "label": "Café", "price": 2.5, "description": "Espresso doble" },
//	      { "code": "2", "sku": "TE",   "label": "Té",   "price": 2.0, "description": "Verde o negro" }
//	    ] },
//	    { "code": "2", "label": "Postres", "items": [
//	      { "code": "1", "sku": "FLAN", "label": "Flan", "price": 3.0, "description": "Casero" }
//	    ] }
//	  ]
//	}
//
// El campo "description" de cada artículo es OPCIONAL.
type catalogBlob struct {
	Categories []categoryBlob `json:"categories"`
}

type categoryBlob struct {
	Code  string        `json:"code"`
	Label string        `json:"label"`
	Items []articleBlob `json:"items"`
}

type articleBlob struct {
	Code        string  `json:"code"`
	SKU         string  `json:"sku"`
	Label       string  `json:"label"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
}

// ParseCatalog deserializa model.Content.Raw (el blob crudo que puebla el
// adapter json) al árbol tipado del catálogo. Es PURO: sin I/O.
//
// Tolera el round-trip JSONB de forma natural: Raw es un map[string]any (números
// como float64, claves como string) que se re-serializa a JSON y se decodifica
// sobre los tipos del blob. Devuelve un error envuelto sobre model.ErrInvalidFlow
// (inspeccionable con errors.Is) cuando:
//   - Raw es nil/ausente (el nodo no resolvió contenido con árbol de catálogo);
//   - el blob no cuadra con la forma esperada;
//   - el catálogo no tiene ninguna categoría.
func ParseCatalog(c model.Content) (Catalog, error) {
	if c.Raw == nil {
		return Catalog{}, fmt.Errorf("%w: el carrito exige content.raw con el árbol de catálogo, pero llegó vacío", model.ErrInvalidFlow)
	}

	// Re-serializar el map[string]any y decodificarlo sobre los tipos del blob:
	// absorbe el round-trip JSONB (float64/string) sin conversiones manuales.
	raw, err := json.Marshal(c.Raw)
	if err != nil {
		return Catalog{}, fmt.Errorf("%w: no se pudo re-serializar content.raw del catálogo: %w", model.ErrInvalidFlow, err)
	}

	var blob catalogBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return Catalog{}, fmt.Errorf("%w: blob de catálogo mal formado: %w", model.ErrInvalidFlow, err)
	}

	if len(blob.Categories) == 0 {
		return Catalog{}, fmt.Errorf("%w: el catálogo no tiene categorías", model.ErrInvalidFlow)
	}

	cat := Catalog{Categories: make([]Category, 0, len(blob.Categories))}
	for _, cb := range blob.Categories {
		category := Category{
			Code:  cb.Code,
			Label: cb.Label,
			Items: make([]Article, 0, len(cb.Items)),
		}
		for _, ab := range cb.Items {
			// articleBlob y Article tienen los mismos campos, mismo orden y
			// mismos tipos (solo difieren en los tags json de articleBlob):
			// la conversión de tipo es válida y evita el struct literal.
			category.Items = append(category.Items, Article(ab))
		}
		cat.Categories = append(cat.Categories, category)
	}
	return cat, nil
}

// loadCatalog reconstruye el catálogo desde el snapshot que vive en
// Vars["cart_catalog"] (misma forma que model.Content.Raw). Es la vía por la que
// Step —que NO recibe el content resuelto, a diferencia de Render— accede al
// catálogo sin hacer I/O (design.md §4.1, nota de ejecución). En T1 lo siembran
// los tests; en T2 el runtime. Un snapshot ausente/mal formado deriva en el
// mismo error que ParseCatalog (envuelto sobre model.ErrInvalidFlow).
func loadCatalog(vars map[string]any) (Catalog, error) {
	raw, ok := vars[catalogVarKey].(map[string]any)
	if !ok {
		// Ausente o de otro tipo: se trata igual que Raw nil (ParseCatalog
		// devuelve el error de "catálogo ausente").
		raw = nil
	}
	return ParseCatalog(model.Content{Raw: raw})
}
