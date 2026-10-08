// Porta internal/intakes/memory.go @ 64c181a (las escrituras).
//
// Regla común a todas las de aquí: lo que se RECHAZA no escribe NADA —ni cabecera, ni
// líneas, ni revisión, ni evento—, y ninguna toca otra solicitud ni otro tenant. La
// suite compara la fila entera antes y después (hallazgo 35 de F1).

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// InsertRevision implementa RevisionWriter con la MISMA numeración que el store
// Postgres: el siguiente correlativo de ESA solicitud, 1 para la primera;
// rev.RevisionNo se ignora. Es el ÚNICO camino de escritura de revisiones del doble:
// por él pasan también la corrección, la revalidación y el descarte, y por eso todas
// parten el literal igual.
//
//   - Un Payload vacío se rechaza con ErrEmptyRevisionPayload y no escribe nada.
//   - El literal del cliente sale del Payload (SplitLiteral) y se guarda APARTE: la
//     revisión que se devuelve —y la que queda guardada— lleva el payload ya limpio. Si
//     el payload no se puede partir, se devuelve ese error y no se escribe nada.
//   - CreatedAt es el instante del reloj del store si la revisión llega sin fecha; si
//     trae una, se respeta (es como los tests siembran una revisión antigua).
//   - LiteralPrunedAt se devuelve siempre en cero.
//   - No es idempotente: dos llamadas iguales son dos revisiones.
//
// No comprueba que la solicitud exista. La FK de la tabla real sí, y es una
// divergencia consciente: exigirlo obligaría a montar la cabecera para probar una
// revisión suelta. No toca la cabecera (tampoco su UpdatedAt).
func (m *MemoryStore) InsertRevision(_ context.Context, rev Revision) (Revision, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.InsertRevision"))
}

// UpdateStatus implementa Store con el mismo compare-and-swap que el Postgres: escribe
// `to` (tal cual, sin normalizar) solo si el estado ALMACENADO es uno de `expected`.
// ErrNotFound si la solicitud no es del tenant; ErrConflict si existe y su estado ya
// no es el esperado. Devuelve la cabecera ya escrita, con el Status normalizado.
//
// Al escribir refresca UpdatedAt con el reloj del store y, según el destino
// (normalizado):
//
//   - `pending_approval`: garantiza la línea de envío con ShippingAlways en la MISMA
//     escritura (D-041.11), y la cabecera devuelta ya trae el total con ella. Sin zona
//     resuelta NO pisa el precio que el dueño le puso a una línea que ya estaba:
//     re-presupuestar es rutina (D-041.26);
//   - `deposit_requested`: fija DepositDueAt = reloj + los días de la seña del tenant
//     (DefaultDepositDueDays si no hay configuración o es ≤ 0) y LIMPIA
//     DepositRemindedAt, para que la marca de una seña anterior no silencie la nueva;
//   - cualquier otro: solo el estado. Las líneas y las revisiones no se tocan (un
//     abandono conserva la negociación auditada).
//
// No valida la transición ni que `to` sea un estado conocido: eso es del dominio
// (Service.SetStatus y su TransitionError).
func (m *MemoryStore) UpdateStatus(_ context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.UpdateStatus"))
}

// EnsureShippingLine implementa Store con las MISMAS reglas que el Postgres: deja
// EXACTAMENTE UNA línea de envío (ShippingSKU) y el total de la cabecera cuadrado con
// la suma de las líneas. ErrNotFound si la solicitud no es del tenant.
//
//   - La política decide si se materializa: con ShippingOnlyIfZones y un tenant sin
//     zonas no se toca nada.
//   - Si no hay línea, se añade AL FINAL la que dicta la configuración
//     (DesiredShippingLine): la de la zona con su tarifa, o «Envío por confirmar» a 0.
//   - Si ya la hay, solo se sustituye —EN SU SITIO, conservando su AddedAt— cuando la
//     configuración manda sobre ella (ShippingLine.Supersedes): con zona resuelta y
//     distinta etiqueta, precio o cantidad. Sin zona resuelta el precio es del dueño y
//     no se pisa.
//
// Es idempotente: N llamadas dejan lo mismo que una. Cuando no cambia nada no escribe
// nada, tampoco UpdatedAt; cuando cambia, lo refresca junto con el total.
func (m *MemoryStore) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	panic(pendiente.Implementar("intakes.MemoryStore.EnsureShippingLine"))
}

// ReplaceItems implementa Store con las MISMAS reglas que el Postgres. ErrNotFound si
// la solicitud no es del tenant; ErrConflict si su estado almacenado no está en
// `expected`. Si escribe:
//
//   - las líneas del SISTEMA (ReservedSKUPrefix) sobreviven intactas y van PRIMERO, en
//     su orden y con su AddedAt; detrás, las de `items` en el orden dado, fechadas al
//     escribir (AddedAt no es cero). Repetir la edición nunca duplica el envío;
//   - el Total se recalcula ENTERO desde las líneas que quedan (Qty × UnitPrice; la
//     personalización no cobra, INV-13) y UpdatedAt se refresca: una corrección
//     REINICIA el plazo del presupuesto, a propósito;
//   - se escribe UNA revisión `corrected` de `owner` con la foto de lo persistido
//     (CorrectedRevisionPayload), numerada como la siguiente. El estado no cambia.
//
// La señal few-shot la decide `mode`: con EditPlain la revisión no lleva ninguna de
// las tres claves (el payload sale como antes de existir el parámetro); con
// EditAsCorrection lleva `as_correction` y el número y la clase de la revisión de
// número más alto que había ANTES de escribir ésta (ninguna de las dos si no había
// revisiones). Esa «última» se mira en crudo, sin podar ni fundir literales.
//
// Devuelve el detalle ya coherente —cabecera normalizada, líneas y todas las
// revisiones, la nueva incluida—, con BuyerDataPresent en false.
func (m *MemoryStore) ReplaceItems(_ context.Context, tenantID, intakeID string, items []Item, expected []string, mode EditMode) (Detail, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.ReplaceItems"))
}

// ApplyRevalidation implementa Store con la MISMA escritura QUIRÚRGICA que el Postgres
// (D-041.25). ErrNotFound si la solicitud no es del tenant; ErrConflict si su estado
// almacenado no está en `expected`. Si escribe, aplica rv.Changes POR SKU sobre las
// líneas guardadas —no reconstruye la lista desde rv.Items, que es lo que habría
// mentido respecto al store real—:
//
//   - un cambio con Removed borra las líneas de ese sku;
//   - uno sin Removed les pone Label y UnitPrice = To; cantidad, personalización y
//     AddedAt no se tocan;
//   - las líneas que sobreviven conservan su ORDEN y su AddedAt;
//   - las de LA PLATAFORMA (ReservedSKUPrefix) se saltan siempre, aunque un cambio
//     nombrara su sku: ni se re-precian ni se borran.
//
// El Total se recalcula desde las líneas que quedan, UpdatedAt se refresca y se escribe
// UNA revisión `revalidated` de `system` con `renderedText` como texto mandado al
// cliente (las reglas de esa revisión, ErrEmptyRevalidationText incluida, son las de
// revalidate.go; si falla no se escribe nada). El estado NO cambia, aunque se retiren
// todas las líneas.
//
// Devuelve el detalle ya coherente, revisión nueva incluida, con BuyerDataPresent en
// false.
func (m *MemoryStore) ApplyRevalidation(_ context.Context, tenantID, intakeID string, rv Revalidation, renderedText string, expected []string) (Detail, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.ApplyRevalidation"))
}

// Discard implementa Store con el MISMO orden de rechazo que el Postgres. ErrNotFound
// si la solicitud no es del tenant (con un DiscardOutcome a cero). Si existe, el
// outcome trae siempre en Status el estado normalizado en el que ESTABA al llegar, y:
//
//  1. si su estado almacenado no está en `discardable`: no escribe nada (Discarded y
//     LiveEvent en false). El estado se mira ANTES que el evento: una no descartable
//     con el evento vivo NO dice LiveEvent;
//  2. si el evento que ELLA declara está `open`: LiveEvent = true y no escribe nada,
//     ni en la solicitud ni en el evento. Que el tenant tenga OTRO evento `open` no
//     frena nada (DT-043.2), y una solicitud sin ligadura no tiene evento vivo;
//  3. si no: queda `abandoned`, con UpdatedAt refrescado y UNA revisión `discarded`
//     de `owner` (DiscardedRevisionPayload con el estado almacenado del que venía y su
//     total), y Discarded = true. Las líneas y el total no se tocan. El cierre del
//     contenedor es un CAS open→cancelled: un evento ya terminal no se pisa.
//
// Descartar dos veces deja el mismo estado y UNA sola revisión: la segunda llamada
// cae en el punto 1.
func (m *MemoryStore) Discard(_ context.Context, tenantID, intakeID string, discardable []string) (DiscardOutcome, error) {
	panic(pendiente.Implementar("intakes.MemoryStore.Discard"))
}

// AbandonByEvent implementa Store con el mismo CAS que el Postgres: la solicitud del
// tenant que declara `eventID` y está almacenada como `open` pasa a `abandoned`, con
// UpdatedAt refrescado y SIN revisión; sus líneas y el evento no se tocan. Cero
// coincidencias es ÉXITO IDEMPOTENTE (nil): un evento desconocido, de otro tenant, sin
// contenido, o cuya solicitud ya no está `open` (una `settled` no se abandona porque
// su evento muera; la ya abandonada no se vuelve a tocar). Con eventID "" devuelve nil
// sin mirar nada: no abandona las filas legadas sin ligadura. Nunca devuelve error.
func (m *MemoryStore) AbandonByEvent(_ context.Context, tenantID, eventID string) error {
	panic(pendiente.Implementar("intakes.MemoryStore.AbandonByEvent"))
}

// PutBuyerField imita PostgresBuyerData.PutBuyerField: FUSIONA el campo en el
// checklist del comprador de la solicitud (no sustituye el checklist; repetir una
// clave pisa su valor) y crea la entrada si no la había. Un valor vacío se guarda como
// cualquier otro. Con la clave vacía devuelve ErrBuyerFieldEmpty y no guarda nada. Sin
// cifrar — ver MemoryStore. No comprueba que la solicitud exista. Desde que hay un
// campo guardado, Get dice BuyerDataPresent.
func (m *MemoryStore) PutBuyerField(_ context.Context, intakeID, key, value string) error {
	panic(pendiente.Implementar("intakes.MemoryStore.PutBuyerField"))
}
