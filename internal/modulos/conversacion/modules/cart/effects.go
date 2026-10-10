// Porta internal/flujos/modules/cart/effects.go @ 9d5a4b6

// effects.go declara los efectos de negocio que el carrito emite en
// Result.Effects (design.md §3.3/§9.J). El módulo es PURO: DECLARA los efectos,
// no los ejecuta; el runtime los despacha por el EventSink (Plan 015) y el
// PersistSink los materializa en flow_events y los pasa al Projector del módulo.
// contact_id OPACO, payload de CÓDIGOS de negocio, cero PII salvo donde se dice.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ LLEVA CADA EFECTO (el payload es contrato: lo leen la telemetría, el
// proyector, el WebhookSink del CRM y los goldens)
// ════════════════════════════════════════════════════════════════════════════
//
// Dos Kind propios: "event" = navegación/telemetría (el PersistSink solo lo escribe
// en flow_events) y "persist" = además proyecta una tabla tipada. El tercero,
// modules.KindPrivate, es de la plataforma.
//
//	cart_started         event   {}                          — EXACTAMENTE una vez
//	category_selected    event   {category_code}
//	item_viewed          event   {sku}
//	item_added           event   {sku, label, qty, unit_price} + la FOTO privada
//	note_added           event   ámbito "item":  {scope:"item", sku, text} y, si
//	                             partió la línea, split_from_qty (la cantidad que
//	                             tenía antes de partirse) + la FOTO privada
//	                             ámbito "order": {scope:"order", len} + la FOTO
//	                             privada — el LARGO en runas, JAMÁS el texto
//	cart_closed          persist {items, total} y customer_note solo si hay nota
//	cart_cancelled       event   {}
//	buyer_data_captured  private {key, value}
//
// LA FOTO de líneas ("items"): los efectos que CAMBIAN las líneas del pedido
// (item_added, note_added) llevan la foto COMPLETA del carrito ya mutado, con la
// misma forma que cart_closed —una lista de {sku, label, qty, unit_price} y
// `customization` solo cuando la línea lleva indicación—, y la declaran en
// PrivateKeys. Las dos mitades importan:
//
//   - La FOTO, y no la línea suelta, porque el proyector REEMPLAZA el conjunto: así
//     el mismo efecto reentregado deja la tabla igual, y los cambios que no son un
//     "agregar" —la indicación de una línea, o el split ×N en ×(N-1)+×1 (D-041.20)—
//     llegan a intake_items cuando ocurren y no al cerrar (Plan 043 · Ola 3).
//   - PRIVADA, es decir fuera de public.flow_events, y NO por PII: aquí solo hay
//     códigos de catálogo. Es por el tamaño y por el contrato. El outbox es
//     append-only: publicar la foto entera en cada item_added reescribiría el
//     carrito N veces para un pedido de N líneas, y cambiaría el payload que
//     consumen la telemetría y los goldens. El flow_event de item_added sigue siendo
//     EXACTAMENTE el de antes, byte por byte (Effect.PublicPayload).
//
// En cart_closed la misma lista SÍ es pública: ocurre UNA vez por solicitud. Lo
// privado ahí es customer_note —la indicación de todo el pedido, donde de verdad se
// cuela la PII («déjalo en portería, calle Mayor 14»)—: viaja en el payload, porque
// el proyector la escribe en intakes.customer_note y el sink del CRM la manda al
// puente, y se declara en PrivateKeys para que no quede en flow_events (defecto A2
// del cierre del Plan 041). Sin nota no hay clave ni PrivateKeys. `total` sale de
// qty × unit_price y de nada más (INV-13).
//
// La asimetría de note_added NO es un descuido (REQ-33g): el texto de una
// indicación de LÍNEA es dato de producción (la misma clase que
// intake_items.customization) y viaja en claro; el de la indicación del PEDIDO no
// viaja, solo su largo.
//
// buyer_data_captured es lo ÚNICO que saca del módulo un valor que tecleó el
// cliente: no publica nada —ni el valor, ni su largo, ni un contador—; su destino
// es la fila cifrada de intake_buyer_data.

package cart

// Nombres lógicos de los efectos del carrito (modules.Effect.Name). Son el
// CONTRATO por el que el PersistSink, el WebhookSink y el Projector los reconocen.
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
