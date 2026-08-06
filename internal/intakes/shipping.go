package intakes

import (
	"encoding/json"
	"fmt"
)

// ShippingSKU es el sku de la LÍNEA ESTÁNDAR DE ENVÍO (D-041.11). Empieza por `_`
// porque ese prefijo está RESERVADO para las líneas que pone wApp: el validador
// del import rechaza de plano cualquier artículo de catálogo que lo use y el
// parseo de runtime lo descarta con aviso (cart.SystemSKUPrefix, T3.1). Esa es
// toda la razón por la que este espacio de nombres es seguro — ningún catálogo de
// ningún tenant puede colisionar con él, así que una línea `_shipping` es SIEMPRE
// de la plataforma y jamás algo que pidiera el cliente.
//
// El literal se declara AQUÍ y no se importa del módulo cart porque el cart ya
// importa este paquete (proyección del cierre): tomarlo prestado en la otra
// dirección sería un ciclo. La atadura entre los dos la muerde un test del lado
// del cart, que es quien es dueño del prefijo.
const ShippingSKU = "_shipping"

// Cómo se llama la línea de envío en el pedido. La etiqueta NO es cosmética: es la
// MARCA de D-041.11. No hay columna «pendiente de precificar» —ni la hay que
// inventar: la cabecera del export está fijada en un sitio único y `_shipping` es
// una línea más—, así que lo que distingue «esto ya está cobrado» de «esto lo
// tiene que precificar el dueño» es lo que el dueño LEE en la bandeja y en el CSV.
const (
	// ShippingPendingLabel marca la línea que espera precio humano (v1: «el dueño
	// precifica», D-041.11).
	ShippingPendingLabel = "Envío por confirmar"
	// shippingZonePrefix encabeza la línea cuando la zona SÍ dicta el precio:
	// "Envío — Providencia".
	shippingZonePrefix = "Envío — "
)

// shippingQty es la cantidad de la línea de envío: un envío por pedido. No es
// configurable a propósito — «dos envíos» no es una cantidad, es otro pedido.
const shippingQty = 1

// ShippingZone es una zona de envío del tenant, tal como viaja en
// tenant_settings.shipping_zones (JSONB, migración 0045):
// `[{"code":"z1","label":"Providencia","price":3000}]`.
type ShippingZone struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Price float64 `json:"price"`
}

// name es cómo se nombra la zona en la línea del pedido: la etiqueta si la hay y
// si no el código. Una zona sin ninguna de las dos no se puede nombrar, y una
// línea que dijera "Envío — " no le dice nada a nadie: DesiredShippingLine la
// descarta.
func (z ShippingZone) name() string {
	if z.Label != "" {
		return z.Label
	}
	return z.Code
}

// ShippingLine es la línea de envío que la CONFIGURACIÓN del tenant dicta. No es
// la línea guardada (esa es un Item como cualquier otro): es lo que debería haber.
type ShippingLine struct {
	Label     string
	UnitPrice float64
	// Priced dice si el precio lo pone la configuración (zona resuelta) o si la
	// línea sale «por confirmar» esperando a que el dueño la precifique. Es lo que
	// decide si la configuración puede PISAR una línea que ya existe (Supersedes).
	Priced bool
}

// ShippingPolicy dice CUÁNDO se materializa la línea de envío. Son dos momentos
// distintos con dos reglas distintas (T4.3), y por eso el llamante la elige:
type ShippingPolicy int

const (
	// ShippingAlways materializa la línea SIEMPRE. Es la regla del presupuesto:
	// una solicitud que entra a `pending_approval` lleva su línea de envío aunque
	// el tenant no haya configurado nada, porque el ciclo de aprobación es
	// exactamente el momento en que el dueño le pone precio (D-041.11).
	ShippingAlways ShippingPolicy = iota
	// ShippingOnlyIfZones la materializa solo si el tenant configuró zonas. Es la
	// regla del cierre del carrito, que va directo a `confirmed` SIN ciclo de
	// aprobación: meterle ahí una línea «por confirmar» a un tenant que no cobra
	// envío sería colarle una línea que nadie va a mirar ni a precificar.
	ShippingOnlyIfZones
)

// applies responde si esta política materializa la línea con las zonas dadas.
// Cuenta las zonas CONFIGURADAS, no las resolubles: un tenant con tres zonas cobra
// envío —aunque wApp todavía no sepa cuál le toca a este cliente y la línea salga
// «por confirmar»—, y el pedido tiene que llevarla.
func (p ShippingPolicy) applies(zones []ShippingZone) bool {
	return p == ShippingAlways || len(zones) > 0
}

// ParseShippingZones lee tenant_settings.shipping_zones. Un valor vacío o NULL es
// «sin zonas» (el DEFAULT de la columna), no un error.
//
// Un JSON ROTO sí es un error y se propaga: hoy esa columna no la escribe ningún
// endpoint —solo llega por SQL a mano—, y devolver «sin zonas» ante un blob
// ilegible haría que un tenant que SÍ cobra envío dejara de cobrarlo en silencio.
// Un fallo ruidoso en una columna que nadie toca es más barato que un pedido menos
// facturado por pedido.
func ParseShippingZones(raw []byte) ([]ShippingZone, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var zones []ShippingZone
	if err := json.Unmarshal(raw, &zones); err != nil {
		return nil, fmt.Errorf("intakes: zonas de envío del tenant ilegibles: %w", err)
	}
	return zones, nil
}

// DesiredShippingLine resuelve qué línea de envío dicta la configuración.
//
// La regla de v1, y por qué es así: la elección de zona NO es conversacional en
// este plan (la pregunta en chat es del 043/044), así que wApp solo sabe la zona
// del cliente cuando NO HAY NADA QUE ELEGIR.
//
//   - UNA zona configurada ⇒ no hay ambigüedad: es una tarifa plana y su precio va
//     en la línea, cobrado.
//   - NINGUNA o VARIAS ⇒ «Envío por confirmar» a 0. Con varias, wApp NO elige la
//     primera: cobrarle a alguien de Puente Alto la tarifa de Providencia porque
//     estaba antes en la lista es peor que no cobrarle nada y que el dueño lo mire.
//
// Es PURA: misma entrada, misma salida, sin BD ni reloj.
func DesiredShippingLine(zones []ShippingZone) ShippingLine {
	var named []ShippingZone
	for _, z := range zones {
		if z.name() != "" {
			named = append(named, z)
		}
	}
	if len(named) == 1 {
		return ShippingLine{
			Label:     shippingZonePrefix + named[0].name(),
			UnitPrice: named[0].Price,
			Priced:    true,
		}
	}
	return ShippingLine{Label: ShippingPendingLabel}
}

// Supersedes responde si la línea que dicta la configuración tiene que SUSTITUIR a
// la que ya está guardada. Es la mitad delicada de la idempotencia: garantizar que
// la línea esté es fácil; decidir cuándo se pisa una que ya está, no.
//
// La regla es de AUTORIDAD sobre el precio:
//
//   - Con zona resuelta (Priced), la autoridad es la configuración: si el tenant
//     cambió de zona o de tarifa, la línea vieja NO puede sobrevivir al cambio —
//     dejaría el pedido cobrando un envío que el tenant ya no cobra. Se sustituye
//     en el sitio, así que no hay dos envíos sumados: sigue habiendo UNA línea.
//   - Sin zona («por confirmar»), la autoridad es HUMANA: v1 es literalmente «el
//     dueño precifica» (D-041.11). Pisar aquí borraría de un plumazo el precio que
//     el dueño acaba de poner a mano, y basta con re-presupuestar el pedido
//     (`confirmed → pending_approval`, D-041.26) para que ocurra. Por eso sin zona
//     solo se garantiza la PRESENCIA de la línea, nunca su contenido.
func (l ShippingLine) Supersedes(stored Item) bool {
	if !l.Priced {
		return false
	}
	return stored.Label != l.Label || stored.UnitPrice != l.UnitPrice || stored.Qty != shippingQty
}

// item proyecta la línea dictada a la fila que se persiste. La personalización va
// vacía: «sin cebolla» es del cliente y esta línea no es suya (D-041.17).
func (l ShippingLine) item() Item {
	return Item{
		SKU:       ShippingSKU,
		Label:     l.Label,
		Qty:       shippingQty,
		UnitPrice: l.UnitPrice,
	}
}
