package cart_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
)

// Los nombres de los efectos son el contrato con el PersistSink, el WebhookSink, el
// proyector y las filas ya escritas en public.flow_events: no cambian ni un byte.
func TestEffectNames_AreTheStoredLiterals(t *testing.T) {
	names := []struct{ got, want string }{
		{cart.EffectCartStarted, "cart_started"},
		{cart.EffectCategorySelected, "category_selected"},
		{cart.EffectItemViewed, "item_viewed"},
		{cart.EffectItemAdded, "item_added"},
		{cart.EffectCartClosed, "cart_closed"},
		{cart.EffectCartCancelled, "cart_cancelled"},
		{cart.EffectNoteAdded, "note_added"},
		{cart.EffectBuyerDataCaptured, "buyer_data_captured"},
		{cart.EffectCartExpired, "cart_expired"},
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n.got != n.want {
			t.Errorf("efecto = %q, quiero %q", n.got, n.want)
		}
		if seen[n.got] {
			t.Errorf("el nombre %q está repetido", n.got)
		}
		seen[n.got] = true
	}
}
