// Porta internal/flujos/modules/cart/revalidate.go @ 9d5a4b6

// revalidate.go es la mitad del carrito de la REVALIDACIÓN DEL RESCATE (Plan 041 ·
// T4.9, REQ-35 / D-041.25): el catálogo aplanado a precios vigentes y el MENSAJE con
// el que se le cuenta al cliente qué cambió mientras su pedido estuvo parado.
//
// El reparto con el dominio de solicitudes no es arbitrario. La aritmética —qué se
// re-precia, qué se retira, cuánto suma— es de `intakes` (intakes/revalidate.go),
// que es dueño de las líneas. Las PALABRAS son de aquí, porque el mensaje es la
// cabecera de cambios más el resumen que este módulo ya renderiza hoy, y ese
// renderizador no puede mudarse: `intakes` no puede importar al cart (el cart ya lo
// importa: sería un ciclo) y duplicar el resumen daría dos textos para el mismo
// pedido, que es la manera segura de que un día digan cosas distintas.
//
// TODO lo de este archivo es PURO —sin BD, sin reloj, sin red—, como el resto del
// módulo: el instante y el catálogo entran como argumentos.
//
// ⚠️ Quien llame a esto desde la conversación NO existe todavía: el gancho del
// rescate es del Plan 043 · T3.6 (ver la cabecera de intakes/revalidate.go, que
// explica por qué es un ciclo entre planes y no un orden). En particular, las
// líneas del carrito vivo —las `lines` que este mismo módulo serializa en
// flow_state— las tiene que reescribir ese gancho, DENTRO de la transacción del
// rescate y junto con la escritura de `intake_items`: si se actualiza una sola cara,
// el cliente y la bandeja ven totales distintos, que es el bug que D-041.25 existe
// para evitar.

package cart

import (
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PriceListOf aplana el catálogo del tenant a lo único que la revalidación
// necesita: `sku → etiqueta y precio vigentes`. Marca la lista como RESUELTA
// (PriceList.Resolved() == true, también con el catálogo vacío), así que solo debe
// llamarse con un catálogo que SÍ se leyó (catalogo.ParseCatalog sin error); quien
// no lo consiga pasa el valor cero de intakes.PriceList y la revalidación queda en
// no-op, sin borrarle una línea a nadie.
//
// Qué publica, y qué no:
//
//   - Un artículo SIN variantes publica su sku, su etiqueta y su precio.
//   - Un artículo CON variantes publica UNA entrada por variante, con el sku
//     compuesto ("TORTA-CHOC#V2"), la etiqueta compuesta ("Torta de chocolate —
//     25-30 porciones") y el precio de la variante: es exactamente lo que el carrito
//     escribe en la línea del pedido.
//   - Un artículo con variantes NO publica su sku pelado, y eso es una decisión: el
//     Price del artículo con variantes es solo una REFERENCIA (D-041.2, por eso la
//     ficha dice «desde $X»). Una línea vieja con el sku pelado —posible si el dueño
//     le añadió variantes al artículo después— se retira y se le dice al cliente, en
//     vez de re-preciarla a un precio de referencia que nadie ha decidido cobrar.
//     Inventar el precio es justo lo que el contrato v2 prohíbe.
//   - El sku REPETIDO gana la primera vez que aparece, recorriendo categorías y
//     artículos en su orden. El validador del import rechaza los duplicados (T3.1),
//     así que esto solo decide qué pasa con un catálogo cargado a mano; se fija el
//     criterio para que sea determinista y no dependa del recorrido de un mapa.
//   - Un combo (components) es un artículo sin variantes: una entrada, a su precio.
func PriceListOf(cat catalogo.Catalog) intakes.PriceList {
	panic(pendiente.Implementar("cart.PriceListOf"))
}

// RevalidationMessage arma el mensaje EXACTO que se le manda al cliente al rescatar
// un pedido que cambió: la cabecera con las viñetas y, debajo —separado por una
// línea en blanco—, el resumen de siempre con el total anterior colgado del TOTAL.
//
// Devuelve la CADENA VACÍA cuando no hubo cambios (rv.Changed() == false), y eso es
// el criterio (b) del plan hecho tipo: sin cambios no hay nada que mandar y no hay
// revisión que escribir. Que la misma llamada conteste las dos cosas es lo que
// impide que un día se mande un aviso sin dejar rastro, o al revés.
//
// La CABECERA:
//
//	Recuperé tu pedido del <día> de <mes> 🧾
//	Ojo, cambiaron cosas desde entonces:
//	• <etiqueta>: $<antes> → $<ahora>
//	• <etiqueta>: ya no lo tenemos, lo quité del pedido
//	…y N más
//
// La fecha se dice como en voz alta —«1 de enero», sin cero a la izquierda, mes en
// minúsculas y en castellano— y SIN el año. Una viñeta por cambio, en el orden de
// rv.Changes, nombrando el artículo por su ETIQUETA y no por su sku; el precio que
// baja se avisa igual que el que sube, en el mismo sentido (viejo → nuevo), y lo
// retirado DICE que se quitó (D-041.25 §c). Como mucho CINCO viñetas: pasado el
// tope se cierra con «…y N más», porque por WhatsApp jamás se pagina.
//
// EL CUERPO es el resumen del carrito de siempre —mismos renglones, mismas
// indicaciones de línea, mismo menú— sobre rv.Items, con «   (antes $<total
// anterior>)» pegado a la línea del TOTAL. Pinta TODAS las líneas que le den,
// incluida la de envío si la solicitud la tiene: es una línea del pedido y suma en
// el total. `note` es la indicación del cliente para todo el pedido (D-041.19) y va
// donde va siempre, bajo el total.
//
// Con el pedido VACÍO (rv.Items sin líneas) NO se pinta el resumen —seguiría
// ofreciendo «1) Confirmar y finalizar» sobre un pedido que no existe—: el cuerpo es
// «🧾 Ya no queda nada de lo que tenías en el pedido (antes $<total anterior>).».
//
// El texto que devuelve es el que hay que persistir tal cual en
// `intake_revisions.rendered_text` (REQ-35b): es la única defensa el día que el
// cliente diga «a mí me dijeron $2.00», y solo defiende si es LITERAL.
//
// `orderedAt` se formatea TAL CUAL llega, sin convertir de zona: quien llame decide
// en qué huso vive el cliente.
func RevalidationMessage(rv intakes.Revalidation, orderedAt time.Time, note string) string {
	panic(pendiente.Implementar("cart.RevalidationMessage"))
}
