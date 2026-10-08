// Porta internal/intakes/revalidate.go @ 64c181a

// revalidate.go es la REVALIDACIÓN CONTRA EL CATÁLOGO VIGENTE (Plan 041 · T4.9,
// REQ-35/REQ-35b, D-041.25): antes de devolverle al cliente un pedido que quedó a
// medias hace quince días, sus líneas se contrastan con el catálogo de HOY — lo que
// cambió de precio se re-precia, lo que ya no se vende se retira, el total se
// recalcula y al cliente se le CUENTA, con una revisión que deja constancia de que
// se le contó.
//
// Las dos alternativas están descartadas por escrito (D-041.25): arrastrar el precio
// congelado deja al negocio vendiendo a precio de hace quince días; revalidar en
// SILENCIO le pone al cliente otro total delante sin explicación. Por eso se
// re-precia y por eso se avisa.
//
// Es INFORMATIVO, no bloqueante: no hay pantalla de aprobación ni estado nuevo. Y si
// NADA cambió, no se dice nada: ni mensaje ni revisión.
//
// El texto que se le manda al cliente NO se arma aquí sino en el módulo del carrito:
// aquí vive la ARITMÉTICA y el RASTRO; allí, las palabras.
//
// Este fichero lleva solo lo PURO. La escritura (`Service.ApplyRevalidation`) nace
// con el `Service`, y la revisión `revalidated` que arma cada store, con los stores.

package intakes

import (
	"encoding/json"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// CatalogEntry es lo ÚNICO que la revalidación necesita saber de un artículo del
// catálogo vigente: cómo se llama hoy (Label) y cuánto cuesta hoy (Price). No es el
// artículo entero —ni categorías, ni descripción, ni etiquetas— porque nada de eso
// cambia el pedido.
type CatalogEntry struct {
	Label string
	Price float64
}

// PriceList es el catálogo VIGENTE del tenant aplanado a `sku → CatalogEntry`. Lo
// arma quien sabe leerlo (el carrito, que conoce el árbol y sus variantes); aquí
// solo se consulta.
//
// Es un struct y no un `map` desnudo por una razón que vale toda la tarea: hay que
// poder distinguir «el catálogo dice que no queda NADA» de «el catálogo NO SE PUDO
// LEER», y un mapa vacío no distingue las dos. Confundirlas borraría el pedido
// entero de un cliente por un fallo de lectura nuestro — REQ-35: «jamás deberá
// borrarse una línea por un fallo de lectura propio».
//
// Por eso el VALOR CERO (`PriceList{}`) es «no resolvió» y el único modo de obtener
// una lista resuelta es NewPriceList: quien no consiga leer el catálogo pasa el
// valor cero —o simplemente no la construye— y la revalidación queda en no-op. El
// camino del olvido es el seguro.
type PriceList struct{}

// NewPriceList construye la lista de precios VIGENTE a partir del catálogo ya
// leído. Marca la lista como RESUELTA siempre, también con un mapa `nil` o vacío:
// eso significa «el catálogo se leyó y no vende nada», que es una respuesta legítima
// y distinta de no haber podido leerlo.
//
// El mapa NO se copia: la lista consulta el mismo mapa que recibe, así que una
// entrada que el llamante añada después se ve desde Lookup. Las claves se usan TAL
// CUAL (ver Lookup).
func NewPriceList(entries map[string]CatalogEntry) PriceList {
	panic(pendiente.Implementar("intakes.NewPriceList"))
}

// Resolved dice si el catálogo llegó a leerse: true en toda lista salida de
// NewPriceList, false en el valor cero. En falso, Revalidate no toca nada.
func (p PriceList) Resolved() bool {
	panic(pendiente.Implementar("intakes.PriceList.Resolved"))
}

// Lookup devuelve la entrada vigente del sku. ok=false —con la entrada en su valor
// cero— cuando el artículo ya no está en el catálogo o cuando la lista ni siquiera
// resolvió.
//
// La búsqueda es EXACTA, byte a byte: no pliega mayúsculas ni recorta espacios
// («pan», « PAN» y «PAN » no son «PAN»), y el sku vacío es una clave como otra
// cualquiera. Normalizar es cosa de quien arma la lista.
func (p PriceList) Lookup(sku string) (CatalogEntry, bool) {
	panic(pendiente.Implementar("intakes.PriceList.Lookup"))
}

// LineChange es UN cambio que la revalidación le hace a UNA línea del pedido: o le
// cambió el precio, o desapareció. Los dos casos viven en el mismo tipo porque el
// mensaje al cliente los lista JUNTOS y EN EL ORDEN DE SUS LÍNEAS: leer el aviso al
// lado del resumen exige que las viñetas sigan el mismo orden que los renglones, y
// dos listas separadas obligarían a intercalarlas al renderizar.
type LineChange struct {
	SKU string
	// Label es la etiqueta VIGENTE en una línea re-preciada y la que TENÍA la línea
	// en una retirada (ya no hay otra).
	Label string
	Qty   int
	// From es el precio unitario que tenía la línea; To el vigente. En una línea
	// RETIRADA no hay precio vigente y To vale 0: el precio que importa es el que
	// tenía, que es por el que el cliente creía estar pagando.
	From float64
	To   float64
	// Removed distingue el retiro del re-precio. Es un booleano y no dos tipos
	// porque el resto de los campos son los mismos y quien recorre la lista tiene
	// que poder hacerlo una vez.
	Removed bool
}

// Revalidation es el resultado de contrastar las líneas de una solicitud con el
// catálogo vigente: cómo quedan las líneas, qué cambió y cuánto costaba antes.
//
// Items son las líneas YA APLICADAS —re-preciadas, sin las retiradas y en el mismo
// orden que entraron—, no una propuesta: quien las escriba no tiene que volver a
// aplicar el diff.
type Revalidation struct {
	Items       []Item
	Changes     []LineChange
	TotalBefore float64
	TotalAfter  float64
}

// Changed dice si hay algo que contarle al cliente: true si y solo si Changes trae
// al menos un cambio. Es la condición EXACTA de REQ-35b: sin cambios no hay mensaje,
// no hay revisión y no se escribe nada.
//
// Un cambio de ETIQUETA no cuenta como cambio, y es deliberado (D-041.25): el
// cliente pidió un producto, no una cadena de texto. La etiqueta vigente sí queda
// aplicada en Items —para que la escritura que ocurra por otro motivo la lleve—,
// pero por sí sola no despierta ni un aviso ni una revisión.
func (r Revalidation) Changed() bool {
	panic(pendiente.Implementar("intakes.Revalidation.Changed"))
}

// Repriced son los cambios de precio (Removed=false), en el orden de las líneas.
// Sin ninguno devuelve una lista VACÍA y nunca `nil`, también sobre el valor cero.
func (r Revalidation) Repriced() []LineChange {
	panic(pendiente.Implementar("intakes.Revalidation.Repriced"))
}

// Removed son las líneas retiradas (Removed=true), en el orden que tenían en el
// pedido. Sin ninguna devuelve una lista VACÍA y nunca `nil`, también sobre el
// valor cero.
func (r Revalidation) Removed() []LineChange {
	panic(pendiente.Implementar("intakes.Revalidation.Removed"))
}

// Revalidate contrasta las líneas de una solicitud con el catálogo vigente y
// devuelve cómo quedan y qué cambió. Es PURA: sin BD, sin reloj y sin mutar la
// entrada.
//
// La regla, línea por línea (D-041.25 §a):
//
//   - el sku existe y el precio es el mismo ⇒ nada;
//   - el sku existe y el precio cambió ⇒ se re-precia al vigente, SUBA O BAJE (una
//     bajada también se avisa: el cliente tiene derecho a saber que hoy paga menos),
//     también si el vigente es CERO. El cambio lleva la etiqueta vigente;
//   - el sku ya no está ⇒ se retira la línea, con su `customization` (sin línea, la
//     indicación no tiene a qué pegarse). El cambio lleva la etiqueta y el precio
//     que TENÍA la línea, y To=0;
//   - el sku existe y cambió su etiqueta ⇒ se toma la vigente y NO se avisa;
//   - una línea de LA PLATAFORMA (sku que EMPIEZA por el prefijo reservado `_`: hoy
//     `_shipping`, D-041.11) ⇒ se respeta tal cual, aunque el catálogo traiga una
//     entrada con ese mismo sku. No está en el catálogo por diseño y su precio lo
//     pone el dueño a mano: re-preciarla contra un catálogo que no la conoce la
//     retiraría siempre, y borrar el envío de un pedido sería cobrarle de menos al
//     tenant en cada rescate. El prefijo se mira al PRINCIPIO y sin recortar:
//     « _shipping» y «PAN_» son líneas de cliente.
//
// «El mismo precio» es con la tolerancia de UN CÉNTIMO: dos importes que distan
// menos de 0.005 no son un cambio. Los precios llegan de dos sitios distintos —una
// columna NUMERIC y un número de JSON— y basta un bit de diferencia para mandarle al
// cliente la viñeta absurda «Pan: $2.00 → $2.00». Aun así la línea SE LLEVA el
// precio vigente, así que TotalAfter puede diferir de TotalBefore por debajo del
// céntimo sin que Changed() sea true.
//
// El sku se compara EXACTO (ver PriceList.Lookup) y cada línea se juzga por
// separado: dos líneas con el mismo sku dan dos cambios, cada una con su cantidad.
// La `customization` y la cantidad de una línea viva no se tocan.
//
// Los totales son la suma de `qty × unit_price` de las líneas de antes y de las que
// quedan; la línea de plataforma suma en los dos, y la personalización no entra
// jamás (INV-13).
//
// Y la regla que las gobierna a todas: si el catálogo NO RESOLVIÓ, no se toca nada.
// Se devuelven las líneas tal cual, sin cambios y con el mismo total (REQ-35). Un
// fallo de lectura nuestro no puede vaciarle el pedido a nadie.
//
// Items y Changes del resultado nunca son `nil`: sin líneas o sin cambios van
// vacíos, también con una entrada `nil`.
func Revalidate(items []Item, catalog PriceList) Revalidation {
	panic(pendiente.Implementar("intakes.Revalidate"))
}

// ErrEmptyRevalidationText lo devuelve la escritura de la revalidación cuando hay
// cambios que escribir pero no se le pasa el texto con el que se avisó al cliente.
//
// No se acepta vacío y no es una formalidad: la revisión `revalidated` existe para
// una sola cosa —ser la defensa el día que el cliente diga «a mí me dijeron $2.00»
// (D-041.25 §d)— y una fila que registre el cambio pero no lo que se dijo no defiende
// de nada. Escribirla sin texto sería fabricar la prueba de una conversación que no
// se puede citar.
var ErrEmptyRevalidationText = errors.New("la revalidación cambió el pedido pero no trae el texto que se le mandó al cliente")

// RevalidatedRevisionPayload arma el payload v1 de la revisión de REVALIDACIÓN
// (D-041.25 §d), con las claves en este orden:
//
//	{"version":1,"repriced":[{"sku":…,"from":…,"to":…}],
//	 "removed":[{"sku":…,"label":…,"qty":…,"unit_price":…}],
//	 "total_before":…,"total_after":…}
//
// `version` es RevisionPayloadVersion. Las claves json son contrato versionado:
// cambiarlas exige subir esa versión.
//
// Las dos formas llevan campos DISTINTOS y no es un descuido: del re-preciado
// importa el salto (`from`→`to`) sobre una línea que sigue ahí y se puede mirar en
// intake_items; de la retirada importa TODO lo que se perdió (`unit_price` es el
// From del cambio), porque esa línea ya no está en ninguna otra parte y el payload
// es la última foto que queda de ella. Cada lista sigue el orden de Changes.
//
// Ninguna de las dos lleva `customization`. Esa ausencia es el criterio (g) del
// plan: la personalización es texto que escribe el cliente y es por donde se cuela
// lo personal («para la Sra. Pérez, 3.º B»). El payload es dato de negocio —skus,
// etiquetas de catálogo y números, nivel 1 del ADR-0034— y así se queda.
//
// Las dos listas se serializan `[]` y nunca `null`, también sobre el valor cero:
// quien lea la revisión debe poder distinguir «no se retiró nada» de «aquí no se
// registró qué se retiró», y `null` no dice cuál de las dos es.
//
// Solo lee Changes y los dos totales; Items no entra. Devuelve error —y un payload
// `nil`— cuando un importe no es serializable (NaN o infinito), envuelto como
// "intakes: serializar payload de la revisión de revalidación: <causa>".
func RevalidatedRevisionPayload(rv Revalidation) (json.RawMessage, error) {
	panic(pendiente.Implementar("intakes.RevalidatedRevisionPayload"))
}
