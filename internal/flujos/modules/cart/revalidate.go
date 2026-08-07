// revalidate.go es la mitad del carrito de la REVALIDACIÓN DEL RESCATE (Plan 041 ·
// T4.9, REQ-35 / D-041.25): el catálogo aplanado a precios vigentes y el MENSAJE con
// el que se le cuenta al cliente qué cambió mientras su pedido estuvo parado.
//
// El reparto con el dominio de solicitudes no es arbitrario. La aritmética —qué se
// re-precia, qué se retira, cuánto suma— es de `intakes` (intakes/revalidate.go),
// que es dueño de las líneas. Las PALABRAS son de aquí, porque el mensaje es la
// cabecera de cambios más el `screenSummary` que este módulo ya renderiza hoy, y ese
// renderizador no puede mudarse: `intakes` no puede importar al cart (el cart ya lo
// importa: sería un ciclo) y duplicar el resumen daría dos textos para el mismo
// pedido, que es la manera segura de que un día digan cosas distintas.
//
// TODO lo de este archivo es PURO —sin BD, sin reloj, sin red—, como el resto del
// módulo: el instante y el catálogo entran como argumentos.
//
// ⚠️ Quien llame a esto NO existe todavía, y está dicho dónde: el gancho del rescate
// es del Plan 043 · T3.6 (ver la cabecera de internal/intakes/revalidate.go, que
// explica por qué es un ciclo entre planes y no un orden). En particular, el
// `cartState` del carrito vivo —las `Lines` que este mismo módulo serializa en
// flow_state— lo tiene que reescribir ese gancho, DENTRO de la transacción del
// rescate y junto con la escritura de `intake_items`: si se actualiza una sola cara,
// el cliente y la bandeja ven totales distintos, que es el bug que D-041.25 existe
// para evitar.
package cart

import (
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// maxChangeBullets acota cuántos cambios se listan en el aviso. Pasado el tope se
// cierra con «…y N más», mismo criterio que la lista de rescatables del Plan 043:
// por WhatsApp JAMÁS se pagina. Un cliente que perdió doce artículos no necesita
// leerlos uno a uno para entender que su pedido ya no es el que era.
const maxChangeBullets = 5

// PriceListOf aplana el catálogo del tenant a lo único que la revalidación
// necesita: `sku → etiqueta y precio vigentes`. Marca la lista como RESUELTA, así
// que solo debe llamarse con un catálogo que SÍ se leyó (ParseCatalog sin error);
// quien no lo consiga pasa el valor cero de intakes.PriceList y la revalidación
// queda en no-op, sin borrarle una línea a nadie.
//
// Qué publica, y qué no:
//
//   - Un artículo SIN variantes publica su sku, su etiqueta y su precio.
//   - Un artículo CON variantes publica UNA entrada por variante, con el sku
//     compuesto ("TORTA-CHOC#V2"), la etiqueta compuesta y el precio de la variante:
//     es exactamente lo que newLine escribe en la línea del pedido.
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
func PriceListOf(cat Catalog) intakes.PriceList {
	entries := map[string]intakes.CatalogEntry{}
	put := func(sku, label string, price float64) {
		if _, dup := entries[sku]; dup {
			return
		}
		entries[sku] = intakes.CatalogEntry{Label: label, Price: price}
	}
	for _, c := range cat.Categories {
		for _, a := range c.Items {
			if !a.HasVariants() {
				put(a.SKU, a.Label, a.Price)
				continue
			}
			for _, v := range a.Variants {
				put(a.SKU+variantSKUSuffix+v.Code, lineLabel(a, v, true), v.Price)
			}
		}
	}
	return intakes.NewPriceList(entries)
}

// RevalidationMessage arma el mensaje EXACTO que se le manda al cliente al rescatar
// un pedido que cambió: la cabecera con las viñetas y, debajo, el resumen de
// siempre con el total anterior colgado del TOTAL.
//
// Devuelve la CADENA VACÍA cuando no hubo cambios, y eso es el criterio (b) del
// plan hecho tipo: sin cambios no hay nada que mandar y no hay revisión que
// escribir. Que la misma llamada conteste las dos cosas es lo que impide que un día
// se mande un aviso sin dejar rastro, o al revés.
//
// El texto que devuelve es el que hay que persistir tal cual en
// `intake_revisions.rendered_text` (REQ-35b): es la única defensa el día que el
// cliente diga «a mí me dijeron $2.00», y solo defiende si es LITERAL. Por eso
// ApplyRevalidation recibe el texto en vez de renderizarlo: quien manda y quien
// guarda tienen que estar mirando la misma cadena.
//
// `orderedAt` se formatea TAL CUAL llega, sin convertir de zona: quien llame decide
// en qué huso vive el cliente. Un rescate fechado con la medianoche UTC de un tenant
// chileno diría el día siguiente, y eso lo arregla el llamante, no el renderizador.
//
// `note` es la indicación del cliente para todo el pedido (D-041.19); va donde va
// siempre, bajo el total, porque el resumen es el mismo de siempre.
func RevalidationMessage(rv intakes.Revalidation, orderedAt time.Time, note string) string {
	if !rv.Changed() {
		return ""
	}
	return revalidationHeader(rv, orderedAt) + "\n\n" + revalidationBody(rv, note)
}

// revalidationHeader es el bloque nuevo: qué pedido se recuperó y qué le pasó. Una
// viñeta por cambio, precio viejo → precio nuevo, sin porcentajes y sin jerga de
// tarifas; y lo retirado DICE que se quitó, en vez de un «no disponible» que deja al
// cliente sin saber si sigue en su pedido o no (D-041.25 §c).
func revalidationHeader(rv intakes.Revalidation, orderedAt time.Time) string {
	var b strings.Builder
	b.WriteString("Recuperé tu pedido del " + longDate(orderedAt) + " 🧾")
	b.WriteString("\nOjo, cambiaron cosas desde entonces:")

	shown := rv.Changes
	if len(shown) > maxChangeBullets {
		shown = shown[:maxChangeBullets]
	}
	for _, c := range shown {
		b.WriteString("\n• " + changeBullet(c))
	}
	if rest := len(rv.Changes) - len(shown); rest > 0 {
		b.WriteString("\n…y " + strconv.Itoa(rest) + " más")
	}
	return b.String()
}

// changeBullet es UNA viñeta. El artículo se nombra por su ETIQUETA y no por su
// sku: el cliente pidió «Pan», no «PAN-01».
func changeBullet(c intakes.LineChange) string {
	if c.Removed {
		return c.Label + ": ya no lo tenemos, lo quité del pedido"
	}
	return c.Label + ": " + money(c.From) + " → " + money(c.To)
}

// revalidationBody es el segundo bloque: el resumen de siempre, con el total
// anterior pegado al TOTAL.
//
// Con el pedido VACÍO no se pinta el resumen, y no es un atajo: un resumen sin
// renglones seguiría ofreciendo «1) Confirmar y finalizar», es decir, una tecla para
// confirmar un pedido que no existe. Se dice lo que pasó y se para ahí. El arranque
// del carrito que D-041.25 §e pone «por delante para empezar» es del rescate (Plan
// 043 · T3.6), que es quien decide a qué nivel vuelve la conversación; cuando lo
// añada, lo que mande y lo que guarde en `rendered_text` tienen que seguir siendo la
// MISMA cadena.
func revalidationBody(rv intakes.Revalidation, note string) string {
	if len(rv.Items) == 0 {
		return "🧾 Ya no queda nada de lo que tenías en el pedido (antes " + money(rv.TotalBefore) + ")."
	}
	return summaryWith(cartLinesOf(rv.Items), note, "   (antes "+money(rv.TotalBefore)+")")
}

// cartLinesOf traduce las líneas de la solicitud a las del carrito para poder
// renderizarlas con el resumen de siempre. Es una traducción y no una conversión de
// dominio: `added_at` no se pinta y el resto de los campos son los mismos.
//
// Pinta TODAS las líneas que le den, incluida la de envío si la solicitud la tiene:
// es una línea del pedido, tiene una etiqueta escrita para que la lean («Envío por
// confirmar») y suma en el total. Esconderla mientras se cobra sería enseñar un
// TOTAL que no cuadra con sus propios renglones.
func cartLinesOf(items []intakes.Item) []cartLine {
	out := make([]cartLine, 0, len(items))
	for _, it := range items {
		out = append(out, cartLine{
			SKU:           it.SKU,
			Label:         it.Label,
			Qty:           it.Qty,
			UnitPrice:     it.UnitPrice,
			Customization: it.Customization,
		})
	}
	return out
}

// monthsES son los meses en la voz del cliente. La fecha del rescate se dice
// «1 de enero» y no «01/01/2026» porque esto es un WhatsApp a una persona que está
// recordando qué pidió, no un comprobante. El AÑO no se dice: un pedido rescatable
// es de hace días o semanas, y nombrar el año sonaría a que se le está devolviendo
// algo del siglo pasado.
var monthsES = [...]string{
	"enero", "febrero", "marzo", "abril", "mayo", "junio",
	"julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre",
}

// longDate escribe la fecha como se dice en voz alta: «1 de enero».
func longDate(t time.Time) string {
	return strconv.Itoa(t.Day()) + " de " + monthsES[int(t.Month())-1]
}
