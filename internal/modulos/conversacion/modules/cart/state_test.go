package cart_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// Los niveles son valores OBSERVABLES: viajan en el JSONB de flow_state.vars, en la
// etiqueta `nivel` de la telemetría y en Query.Level. No cambian ni un byte.
func TestLevels_AreTheStoredLiterals(t *testing.T) {
	levels := map[string]string{
		cart.LevelCategories:    "categories",
		cart.LevelArticles:      "articles",
		cart.LevelArticle:       "article",
		cart.LevelVariant:       "variant",
		cart.LevelQuantity:      "quantity",
		cart.LevelContinue:      "continue",
		cart.LevelSummary:       "summary",
		cart.LevelClosed:        "closed",
		cart.LevelCancelled:     "cancelled",
		cart.LevelItemNoteScope: "item_note_scope",
		cart.LevelItemNote:      "item_note",
		cart.LevelOrderNote:     "order_note",
		cart.LevelBuyerData:     "buyer_data",
	}
	if len(levels) != 13 {
		t.Fatalf("hay %d niveles distintos, quiero 13: dos constantes comparten valor", len(levels))
	}
	for got, want := range levels {
		if got != want {
			t.Errorf("nivel = %q, quiero %q", got, want)
		}
	}
}

// El tipo de nodo, el tamaño de página por defecto y la clave de Vars del page_size.
func TestStateConstants(t *testing.T) {
	if cart.NodeTypeCart != "cart" {
		t.Errorf("NodeTypeCart = %q, quiero \"cart\"", cart.NodeTypeCart)
	}
	if cart.DefaultPageSize != 5 {
		t.Errorf("DefaultPageSize = %d, quiero 5", cart.DefaultPageSize)
	}
	if cart.VarPageSize != "cart_page_size" {
		t.Errorf("VarPageSize = %q, quiero \"cart_page_size\"", cart.VarPageSize)
	}
}
