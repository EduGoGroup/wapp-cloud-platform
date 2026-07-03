// effects.go declara los efectos de negocio que el carrito emite en
// Result.Effects (design.md §3.3/§9.J). El módulo es PURO: DECLARA los efectos,
// no los ejecuta; el runtime los despacha por el EventSink (Plan 015) y el
// PersistSink los materializa en flow_events y proyecta cart_closed a
// orders/order_items. contact_id OPACO, payload de CÓDIGOS de negocio, cero PII.
package cart

import "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"

// Nombres lógicos de los efectos del carrito (modules.Effect.Name). Son el
// CONTRATO por el que el PersistSink los reconoce (que replica estos literales,
// igual que hace con "survey_answer" sin importar el módulo survey).
const (
	EffectCartStarted      = "cart_started"
	EffectCategorySelected = "category_selected"
	EffectItemViewed       = "item_viewed"
	EffectItemAdded        = "item_added"
	EffectCartClosed       = "cart_closed"
	EffectCartCancelled    = "cart_cancelled"
	// EffectCartExpired queda DEFINIDO aquí; su emisión es del runtime al reanudar
	// una orden vencida por TTL (design.md §4.3, T3), no del módulo puro.
	EffectCartExpired = "cart_expired"
)

// Kinds de los efectos (design.md §3.3): "event" = navegación/telemetría (el
// PersistSink solo lo escribe en flow_events); "persist" = además proyecta una
// tabla tipada (cart_closed → orders/order_items).
const (
	kindEvent   = "event"
	kindPersist = "persist"
)

// event construye un efecto de navegación/telemetría (Kind "event").
func event(name string, payload map[string]any) modules.Effect {
	return modules.Effect{Kind: kindEvent, Name: name, Payload: payload}
}

// closedEffect construye el efecto cart_closed (Kind "persist"): lleva las líneas
// del pedido y el total (design.md §3.3). Incluye "label" además de
// sku/qty/unit_price porque el destino order_items.label es NOT NULL y el módulo
// es el único que conoce la etiqueta del catálogo (el PersistSink no lo resuelve);
// enriquecimiento retro-compatible sobre el payload mínimo del diseño.
func closedEffect(lines []cartLine) modules.Effect {
	items := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		items = append(items, map[string]any{
			"sku":        l.SKU,
			"label":      l.Label,
			"qty":        l.Qty,
			"unit_price": l.UnitPrice,
		})
	}
	return modules.Effect{
		Kind: kindPersist,
		Name: EffectCartClosed,
		Payload: map[string]any{
			"items": items,
			"total": total(lines),
		},
	}
}
