package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// revalidate.go es la REVALIDACIÓN CONTRA EL CATÁLOGO VIGENTE (Plan 041 · T4.9,
// REQ-35/REQ-35b, D-041.25): antes de devolverle al cliente un pedido que quedó a
// medias hace quince días, sus líneas se contrastan con el catálogo de HOY — lo que
// cambió de precio se re-precia, lo que ya no se vende se retira, el total se
// recalcula y al cliente se le CUENTA, con una revisión que deja constancia de que
// se le contó.
//
// Las dos alternativas están descartadas por escrito (D-041.25) y conviene que se
// lea aquí: arrastrar el precio congelado deja al negocio vendiendo a precio de hace
// quince días y solo avisa a quien cobra, cuando ya es tarde; revalidar en SILENCIO
// le pone al cliente otro total delante sin explicación. Por eso se re-precia y por
// eso se avisa.
//
// Es INFORMATIVO, no bloqueante: no hay pantalla de aprobación, no hay estado nuevo
// y no se le pide al cliente que confirme nada. Si no le gusta, cancela con la tecla
// 9 que ya existe (cart/state.go). Y si NADA cambió, no se dice nada: ni mensaje ni
// revisión — un aviso de «no cambió nada» es una pantalla que nadie pidió.
//
// El texto que se le manda al cliente NO se arma aquí sino en el módulo cart
// (cart/revalidate.go): el mensaje es la cabecera de cambios + el `screenSummary`
// que ya se renderiza hoy, y ese renderizador es del cart. Este paquete no puede
// importarlo (el cart ya importa éste: sería un ciclo) y duplicarlo daría dos textos
// para el mismo pedido. Aquí vive la ARITMÉTICA y el RASTRO; allí, las palabras.
//
// ⚠️ LO QUE ESTA TAREA NO ENTREGA, y de quién es. El GANCHO en sí —el punto de
// llamada, entre «el cliente elige rescatar» y «se le devuelve el evento con su
// pedido»— es del **Plan 043 · T3.6**, y no por prioridad sino porque su materia
// prima no existe: el rescate cuelga de public.conversation_events y de
// flow_state.event_id, que no están en NINGÚN repo del ecosistema (043/design.md
// :1005-1020 y :1118 los crean). Tampoco se puede «hacer el 043 primero»:
// 043/tasks.md:396 necesita el estado `abandoned` que publica ESTE plan — es un
// ciclo, no un orden. Lo que queda pendiente, en concreto:
//
//   - EL PUNTO DE LLAMADA. Quién invoca Revalidate y ApplyRevalidation, y con qué
//     catálogo. Hoy nadie: las dos nacen sin llamantes, como nació DiscardableStatuses.
//   - LA UNIDAD DE TRABAJO CONJUNTA con el `cartState` de flow_state. D-041.25 exige
//     que las DOS caras —intake_items y las `Lines` del carrito vivo— se escriban
//     juntas, porque si se actualiza una sola el cliente y la bandeja ven totales
//     distintos, que es el bug que la decisión existe para evitar. Aquí se escribe
//     UNA: la otra vive en una tabla cuya transacción abre el rescate, y el rescate
//     no existe. Quien cablee el gancho tiene que meter esta escritura DENTRO de esa
//     transacción, no llamarla al lado.
//   - EL EFECTO A flow_events con contadores y sin texto (criterio (h) del plan):
//     solo se puede observar cuando lo emite el rescate real.

// CatalogEntry es lo ÚNICO que la revalidación necesita saber de un artículo del
// catálogo vigente: cómo se llama hoy y cuánto cuesta hoy. No es el artículo entero
// —ni categorías, ni descripción, ni etiquetas— porque nada de eso cambia el pedido.
type CatalogEntry struct {
	Label string
	Price float64
}

// PriceList es el catálogo VIGENTE del tenant aplanado a `sku → CatalogEntry`. Lo
// arma quien sabe leerlo (cart.PriceListOf, que conoce el árbol y sus variantes);
// aquí solo se consulta.
//
// Es un struct y no un `map` desnudo por una razón que vale toda la tarea: hay que
// poder distinguir «el catálogo dice que no queda NADA» de «el catálogo NO SE PUDO
// LEER», y un mapa vacío no distingue las dos. Confundirlas borraría el pedido
// entero de un cliente por un fallo de lectura nuestro — REQ-35: «jamás deberá
// borrarse una línea por un fallo de lectura propio».
//
// Por eso el VALOR CERO es «no resolvió» y el único modo de obtener una lista
// resuelta es NewPriceList: quien no consiga leer el catálogo pasa PriceList{} —o
// simplemente no la construye— y la revalidación queda en no-op. El camino del
// olvido es el seguro.
type PriceList struct {
	entries  map[string]CatalogEntry
	resolved bool
}

// NewPriceList construye la lista de precios VIGENTE a partir del catálogo ya
// leído. Marca la lista como RESUELTA: llamarla con un mapa vacío significa «el
// catálogo se leyó y no vende nada», que es una respuesta legítima y distinta de
// no haber podido leerlo.
func NewPriceList(entries map[string]CatalogEntry) PriceList {
	if entries == nil {
		entries = map[string]CatalogEntry{}
	}
	return PriceList{entries: entries, resolved: true}
}

// Resolved dice si el catálogo llegó a leerse. En falso, Revalidate no toca nada.
func (p PriceList) Resolved() bool { return p.resolved }

// Lookup devuelve la entrada vigente del sku. ok=false cuando el artículo ya no
// está en el catálogo (o cuando la lista ni siquiera resolvió).
func (p PriceList) Lookup(sku string) (CatalogEntry, bool) {
	if !p.resolved {
		return CatalogEntry{}, false
	}
	e, ok := p.entries[sku]
	return e, ok
}

// LineChange es UN cambio que la revalidación le hace a UNA línea del pedido: o le
// cambió el precio, o desapareció. Los dos casos viven en el mismo tipo porque el
// mensaje al cliente los lista JUNTOS y EN EL ORDEN DE SUS LÍNEAS: leer el aviso al
// lado del resumen exige que las viñetas sigan el mismo orden que los renglones, y
// dos listas separadas obligarían a intercalarlas al renderizar.
type LineChange struct {
	SKU   string
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

// Changed dice si hay algo que contarle al cliente. Es la condición EXACTA de
// REQ-35b: sin cambios no hay mensaje, no hay revisión y no se escribe nada.
//
// Un cambio de ETIQUETA no cuenta como cambio, y es deliberado (D-041.25): el
// cliente pidió un producto, no una cadena de texto. La etiqueta vigente sí queda
// aplicada en Items — para que la escritura que ocurra por otro motivo la lleve—,
// pero por sí sola no despierta ni un aviso ni una revisión.
func (r Revalidation) Changed() bool { return len(r.Changes) > 0 }

// Repriced son los cambios de precio, en el orden de las líneas.
func (r Revalidation) Repriced() []LineChange { return r.changesWhere(false) }

// Removed son las líneas retiradas, en el orden que tenían en el pedido.
func (r Revalidation) Removed() []LineChange { return r.changesWhere(true) }

func (r Revalidation) changesWhere(removed bool) []LineChange {
	out := make([]LineChange, 0, len(r.Changes))
	for _, c := range r.Changes {
		if c.Removed == removed {
			out = append(out, c)
		}
	}
	return out
}

// Revalidate contrasta las líneas de una solicitud con el catálogo vigente y
// devuelve cómo quedan y qué cambió. Es PURA: sin BD, sin reloj y sin mutar la
// entrada.
//
// La regla, línea por línea (D-041.25 §a):
//
//   - el sku existe y el precio es el mismo ⇒ nada;
//   - el sku existe y el precio cambió ⇒ se re-precia al vigente, SUBA O BAJE (una
//     bajada también se avisa: el cliente tiene derecho a saber que hoy paga menos);
//   - el sku ya no está ⇒ se retira la línea, con su `customization` (sin línea, la
//     indicación no tiene a qué pegarse);
//   - el sku existe y cambió su etiqueta ⇒ se toma la vigente y NO se avisa;
//   - una línea de LA PLATAFORMA (prefijo reservado: hoy `_shipping`, D-041.11) ⇒ se
//     respeta tal cual. No está en el catálogo por diseño y su precio lo pone el
//     dueño a mano: re-preciarla contra un catálogo que no la conoce la retiraría
//     siempre, y borrar el envío de un pedido sería cobrarle de menos al tenant en
//     cada rescate.
//
// Y la regla que las gobierna a todas: si el catálogo NO RESOLVIÓ, no se toca nada.
// Se devuelven las líneas tal cual, sin cambios y sin resumen (REQ-35). Un fallo de
// lectura nuestro no puede vaciarle el pedido a nadie.
func Revalidate(items []Item, catalog PriceList) Revalidation {
	out := Revalidation{
		Items:       make([]Item, 0, len(items)),
		Changes:     []LineChange{},
		TotalBefore: itemsTotal(items),
	}
	if !catalog.Resolved() {
		out.Items = append(out.Items, items...)
		out.TotalAfter = out.TotalBefore
		return out
	}

	for _, it := range items {
		if strings.HasPrefix(it.SKU, ReservedSKUPrefix) {
			out.Items = append(out.Items, it)
			continue
		}
		entry, live := catalog.Lookup(it.SKU)
		if !live {
			out.Changes = append(out.Changes, LineChange{
				SKU: it.SKU, Label: it.Label, Qty: it.Qty, From: it.UnitPrice, Removed: true,
			})
			continue
		}
		if !sameMoney(it.UnitPrice, entry.Price) {
			out.Changes = append(out.Changes, LineChange{
				SKU: it.SKU, Label: entry.Label, Qty: it.Qty, From: it.UnitPrice, To: entry.Price,
			})
		}
		it.Label, it.UnitPrice = entry.Label, entry.Price
		out.Items = append(out.Items, it)
	}

	out.TotalAfter = itemsTotal(out.Items)
	return out
}

// sameMoney compara dos importes con la tolerancia de UN CÉNTIMO. No es paranoia
// numérica gratuita: los precios llegan de dos sitios distintos —una columna
// NUMERIC y un número de JSON— y basta un bit de diferencia para que la
// revalidación declare «cambió de precio» y le mande al cliente la viñeta absurda
// «Pan: $2.00 → $2.00». Por debajo del céntimo no hay cambio que contar, porque no
// hay céntimo que cobrar.
func sameMoney(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// itemsTotal suma qty × unit_price. La personalización NO entra jamás (INV-13): el
// perro caliente no es más barato por quitarle la cebolla.
func itemsTotal(items []Item) float64 {
	var t float64
	for _, it := range items {
		t += float64(it.Qty) * it.UnitPrice
	}
	return t
}

// --- rastro: la revisión `revalidated` -------------------------------------

// ErrEmptyRevalidationText lo devuelve ApplyRevalidation cuando hay cambios que
// escribir pero no se le pasa el texto con el que se avisó al cliente.
//
// No se acepta vacío y no es una formalidad: la revisión `revalidated` existe para
// una sola cosa —ser la defensa el día que el cliente diga «a mí me dijeron $2.00»
// (D-041.25 §d)— y una fila que registre el cambio pero no lo que se dijo no defiende
// de nada. Escribirla sin texto sería fabricar la prueba de una conversación que no
// se puede citar.
var ErrEmptyRevalidationText = errors.New("la revalidación cambió el pedido pero no trae el texto que se le mandó al cliente")

// repricedEntry y removedEntry son las dos formas del payload v1 (D-041.25 §d).
// Llevan campos DISTINTOS y no es un descuido: del re-preciado importa el salto
// (`from`→`to`) sobre una línea que sigue ahí y se puede mirar en intake_items; de
// la retirada importa TODO lo que se perdió, porque esa línea ya no está en ninguna
// otra parte y el payload es la última foto que queda de ella.
//
// Ninguna de las dos lleva `customization`. Esa ausencia es el criterio (g) del
// plan: la personalización es texto que escribe el cliente y es por donde se cuela
// lo personal («para la Sra. Pérez, 3.º B»). El payload es dato de negocio —skus,
// etiquetas de catálogo y números, nivel 1 del ADR-0034— y así se queda.
type repricedEntry struct {
	SKU  string  `json:"sku"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

type removedEntry struct {
	SKU       string  `json:"sku"`
	Label     string  `json:"label"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
}

// revalidatedRevisionPayload es la forma canónica del payload de la revisión de
// revalidación. Las etiquetas json son contrato versionado: cambiarlas exige subir
// RevisionPayloadVersion.
type revalidatedRevisionPayload struct {
	Version     int             `json:"version"`
	Repriced    []repricedEntry `json:"repriced"`
	Removed     []removedEntry  `json:"removed"`
	TotalBefore float64         `json:"total_before"`
	TotalAfter  float64         `json:"total_after"`
}

// RevalidatedRevisionPayload arma el payload de la revisión de REVALIDACIÓN
// (`{"version":1,"repriced":[…],"removed":[…],"total_before":…,"total_after":…}`).
//
// Las dos listas se serializan `[]` y nunca `null`, igual que en el payload del
// carrito: quien lea la revisión debe poder distinguir «no se retiró nada» de «aquí
// no se registró qué se retiró», y `null` no dice cuál de las dos es.
func RevalidatedRevisionPayload(rv Revalidation) (json.RawMessage, error) {
	repriced := make([]repricedEntry, 0, len(rv.Changes))
	for _, c := range rv.Repriced() {
		repriced = append(repriced, repricedEntry{SKU: c.SKU, From: c.From, To: c.To})
	}
	removed := make([]removedEntry, 0, len(rv.Changes))
	for _, c := range rv.Removed() {
		removed = append(removed, removedEntry{SKU: c.SKU, Label: c.Label, Qty: c.Qty, UnitPrice: c.From})
	}

	raw, err := json.Marshal(revalidatedRevisionPayload{
		Version:     RevisionPayloadVersion,
		Repriced:    repriced,
		Removed:     removed,
		TotalBefore: rv.TotalBefore,
		TotalAfter:  rv.TotalAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("intakes: serializar payload de la revisión de revalidación: %w", err)
	}
	return raw, nil
}

// revalidatedRevision arma la revisión que deja UNA revalidación con cambios. Vive
// aquí y no en cada store para que las dos implementaciones no puedan divergir: un
// MemoryStore que escribiera otra foto haría que los tests de handler dijeran algo
// falso sobre producción (mismo criterio que correctedRevision y discardedRevision).
//
// `created_by` es `system` —y no `owner` como en el descarte— porque aquí no decide
// nadie: es el catálogo del tenant el que ya cambió y la plataforma la que se limita
// a contarlo. Sigue siendo un ROL y jamás una persona (CERO PII).
func revalidatedRevision(intakeID string, rv Revalidation, renderedText string) (Revision, error) {
	payload, err := RevalidatedRevisionPayload(rv)
	if err != nil {
		return Revision{}, err
	}
	return Revision{
		IntakeID:     intakeID,
		Kind:         RevisionKindRevalidated,
		Payload:      payload,
		RenderedText: renderedText,
		CreatedBy:    RevisionBySystem,
	}, nil
}

// --- la escritura ----------------------------------------------------------

// ApplyRevalidation ESCRIBE el resultado de una revalidación: deja `intake_items`
// como dice el diff, cuadra el total de la cabecera y escribe UNA revisión
// `revalidated` con el texto exacto que se le mandó al cliente — todo en la misma
// unidad de trabajo.
//
// SIN CAMBIOS NO ESCRIBE NADA (REQ-35b: «ninguna si no hubo cambios»): devuelve la
// solicitud tal como está, con una lectura y cero escrituras. Así el llamante puede
// invocarla siempre, sin replicar la condición.
//
// El estado NO se toca, y ése es el criterio (i) del plan: aunque se retiren TODAS
// las líneas, la solicitud se queda `open` con cero líneas hasta que un humano la
// mueva (E-7 / INV-14 / D-041.16 — nada muere por tiempo ni por efecto colateral).
//
// Solo revalida sobre una solicitud `open`, y quien la encuentre en otro estado
// recibe ErrConflict. No es una restricción caprichosa: el rescate solo ofrece
// solicitudes `open` (INV-17), y re-preciar una `confirmed` cambiaría lo que el
// cliente ya aceptó sin que nadie se lo dijera — exactamente lo que EditableStatus
// prohíbe en la edición manual. ErrConflict es además la respuesta útil: quien la
// recibe relee y decide, en vez de reintentar.
//
// ⚠️ Esto escribe UNA de las dos caras de D-041.25. La otra —las `Lines` del
// `cartState` en flow_state— es del Plan 043 · T3.6 y tiene que ir DENTRO de la
// misma transacción del rescate; ver la cabecera de este archivo.
func (s *Service) ApplyRevalidation(ctx context.Context, tenantID, intakeID string, rv Revalidation, renderedText string) (Detail, error) {
	if !rv.Changed() {
		return s.store.Get(ctx, tenantID, intakeID)
	}
	if renderedText == "" {
		return Detail{}, ErrEmptyRevalidationText
	}
	return s.store.ApplyRevalidation(ctx, tenantID, intakeID, rv, renderedText, StoredVariants(StatusOpen))
}
