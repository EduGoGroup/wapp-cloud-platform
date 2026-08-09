// effects.go declara los efectos de negocio que el carrito emite en
// Result.Effects (design.md §3.3/§9.J). El módulo es PURO: DECLARA los efectos,
// no los ejecuta; el runtime los despacha por el EventSink (Plan 015) y el
// PersistSink los materializa en flow_events y proyecta cart_closed a
// intakes/intake_items. contact_id OPACO, payload de CÓDIGOS de negocio, cero PII.
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
	// EffectNoteAdded lo emite la captura de una indicación del cliente (T4.1c,
	// D-041.19/D-041.20). Es navegación/telemetría: sirve para saber cuánto se usa
	// la tecla 3 y cuántas veces obliga a partir una línea.
	EffectNoteAdded = "note_added"
	// EffectBuyerDataCaptured lleva UN campo del checklist del comprador (T4.5,
	// D-041.13). Es el ÚNICO efecto del carrito con Kind modules.KindPrivate, y por
	// tanto el único que NO se escribe en flow_events: su payload es dato personal
	// y su destino es la fila cifrada de intake_buyer_data.
	EffectBuyerDataCaptured = "buyer_data_captured"
	// EffectCartExpired es un efecto SIN PRODUCTOR desde T4.7: D-041.16 derogó el
	// vencimiento por tiempo y con él la política que lo sintetizaba al reanudar
	// (cart/resume.go). Se conserva la DEFINICIÓN —y el case del proyector— porque
	// hay filas históricas en public.flow_events con este nombre y un replay tiene
	// que seguir sabiendo materializarlas; lo que murió es quien lo emitía, no lo
	// que significa. Nadie debe volver a emitirlo: si una solicitud tiene que morir,
	// la mata una persona (D-041.18), no un reloj.
	EffectCartExpired = "cart_expired"
)

// Kinds de los efectos (design.md §3.3): "event" = navegación/telemetría (el
// PersistSink solo lo escribe en flow_events); "persist" = además proyecta una
// tabla tipada (cart_closed → intakes/intake_items).
const (
	kindEvent   = "event"
	kindPersist = "persist"
)

// event construye un efecto de navegación/telemetría (Kind "event").
func event(name string, payload map[string]any) modules.Effect {
	return modules.Effect{Kind: kindEvent, Name: name, Payload: payload}
}

// buyerDataEffect declara UN campo del checklist del comprador ya capturado (T4.5,
// D-041.13). Es lo ÚNICO que saca del módulo el valor que tecleó el cliente, y por
// eso lleva Kind modules.KindPrivate: con ese Kind el PersistSink NO lo escribe en
// flow_events y lo pasa directo al proyector, que lo cifra.
//
// Compárese con noteAddedEffect, justo arriba: aquélla publica el texto de una
// indicación de línea porque es dato de PRODUCCIÓN (nivel 1 del ADR-0034,
// cuantificable, no identifica a nadie). Esto es lo contrario —el RUT y la
// dirección identifican a una persona— y por eso no hay aquí ninguna variante que
// publique nada: ni el valor, ni su largo, ni un contador. Lo único que se sabrá
// del checklist fuera de la fila cifrada es que existe (buyer_data_present).
func buyerDataEffect(key, value string) modules.Effect {
	return modules.Effect{
		Kind:    modules.KindPrivate,
		Name:    EffectBuyerDataCaptured,
		Payload: map[string]any{"key": key, "value": value},
	}
}

// noteScope es el ÁMBITO de una indicación del cliente (D-041.19): la línea o el
// pedido entero. scopeItemSplit no es un tercer ámbito —sigue siendo la
// indicación de UNA línea— sino la variante que además parte la línea (D-041.20);
// se distingue aquí porque cambia el acuse y el payload del efecto, no el destino.
type noteScope int

const (
	scopeItem noteScope = iota
	scopeItemSplit
	scopeOrder
)

// key es el literal que viaja en el payload del efecto. El split NO tiene clave
// propia: quien mida esta rama lo hace por "split_from_qty", que además dice
// cuántas unidades tenía la línea, y no por un ámbito inventado.
func (s noteScope) key() string {
	if s == scopeOrder {
		return "order"
	}
	return "item"
}

// noteAddedEffect declara la captura de una indicación (T4.1c). Es telemetría de
// nivel 1 que acaba en flow_events, y de ahí sale su asimetría, que NO es un
// descuido (REQ-33g, precisado el 2026-08-06):
//
//   - scope "item": lleva el texto. Es la personalización de una línea —dato de
//     producción cuantificable, la misma clase que intake_items.customization— y
//     REQ-19b ya dice que la personalización de una línea viaja en claro como
//     decisión.
//   - scope "order": NO lleva el texto, solo su LARGO. customer_note es donde de
//     verdad se cuela la PII («déjalo en portería, calle Mayor 14»), y flow_events
//     no lo poda nadie: el literal sobreviviría a la retención de la propia
//     solicitud (Plan 046). Lo que se mide —que se usó la ranura, y cuánto se
//     escribió— no necesita el contenido.
//
// splitFromQty > 0 solo cuando hubo split: es la cantidad que tenía la línea antes
// de partirse, un entero sin PII, para poder medir cuánto se usa esa rama.
func noteAddedEffect(scope noteScope, sku, text string, splitFromQty int) modules.Effect {
	payload := map[string]any{"scope": scope.key()}
	if scope == scopeOrder {
		payload["len"] = len([]rune(text))
		return event(EffectNoteAdded, payload)
	}
	payload["sku"] = sku
	payload["text"] = text
	if splitFromQty > 0 {
		payload["split_from_qty"] = splitFromQty
	}
	return event(EffectNoteAdded, payload)
}

// closedEffect construye el efecto cart_closed (Kind "persist"): lleva las líneas
// del pedido y el total (design.md §3.3). Incluye "label" además de
// sku/qty/unit_price porque el destino intake_items.label es NOT NULL y el módulo
// es el único que conoce la etiqueta del catálogo (el PersistSink no lo resuelve);
// enriquecimiento retro-compatible sobre el payload mínimo del diseño.
// Las DOS indicaciones del cliente (T4.1c) viajan aquí porque este efecto es lo
// único que cruza del módulo puro al mundo: si no salen por el cierre, no llegan a
// intake_items.customization ni a intakes.customer_note y quien prepara nunca las
// lee (INV-12). No tocan el dinero: `total` se calcula de qty × unit_price y de
// nada más (INV-13).
//
// Las claves se OMITEN cuando están vacías, y es deliberado: quien lee el payload
// ya trata la ausencia como "" —lo hacen tanto la proyección como el sink del CRM,
// cada uno con su comentario—, así que publicarlas vacías solo engordaría cada
// flow_events del carrito sin decir nada nuevo. La prueba de que la omisión es
// segura está en el golden de la transcripción v1: un pedido sin indicaciones
// produce EXACTAMENTE el mismo payload que antes de esta tarea, byte por byte.
func closedEffect(lines []cartLine, note string) modules.Effect {
	items := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		item := map[string]any{
			"sku":        l.SKU,
			"label":      l.Label,
			"qty":        l.Qty,
			"unit_price": l.UnitPrice,
		}
		if l.Customization != "" {
			item["customization"] = l.Customization
		}
		items = append(items, item)
	}
	payload := map[string]any{
		"items": items,
		"total": total(lines),
	}
	// customer_note viaja en el payload —el proyector la escribe en
	// intakes.customer_note y el sink del CRM la manda al puente— pero se DECLARA
	// privada para que el PersistSink no la deje en public.flow_events.
	//
	// Sin esto, la asimetría de noteAddedEffect (arriba: el ámbito "order" publica
	// el LARGO, nunca el texto) se cumplía al pie de la letra y el literal entraba
	// igual por esta puerta: el requisito cumplía su letra y no su propósito. Es el
	// defecto A2 del cierre del Plan 041, confirmado en producción — un cart_closed
	// con «Dejarlo en portería» en claro, mientras el RUT (que sí va por
	// KindPrivate) salía 0 de 16.
	var private []string
	if note != "" {
		payload["customer_note"] = note
		private = []string{"customer_note"}
	}
	return modules.Effect{
		Kind:        kindPersist,
		Name:        EffectCartClosed,
		PrivateKeys: private,
		Payload:     payload,
	}
}
