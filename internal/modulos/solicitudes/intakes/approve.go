// Porta internal/intakes/approve.go @ 64c181a

// approve.go — LA ACCIÓN APROBAR DEL DUEÑO (D-044.49).
//
// Es el acto en el que un presupuesto deja de ser un borrador: el dueño escribe la
// cotización, la aprueba, y en ese mismo acto el cliente la recibe por WhatsApp, la
// solicitud pasa a `confirmed` y el puente CRM se entera. Lo que aporta este fichero
// es la PUERTA, las precondiciones y, sobre todo, el ORDEN.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL ORDEN DE LAS OPERACIONES, QUE ES LA DECISIÓN DE ESTE FICHERO
// ════════════════════════════════════════════════════════════════════════════
//
// `RenderedText` es «el texto EXACTO que se le mandó al cliente». Con dos escrituras
// y un envío, algún fallo parcial tiene que ser posible, y elegir el orden es elegir
// CUÁL:
//
//  1. componer el texto entero (cotización del dueño + plantilla de seña);
//  2. la TRANSICIÓN, con compare-and-swap (SetStatus);
//  3. la REVISIÓN `approved`, que se lleva ese texto;
//  4. el ENVÍO al cliente;
//  5. el empuje al CRM con el revision_no REAL;
//  6. la métrica de la aprobación.
//
// POR QUÉ EL TEXTO SE COMPONE ANTES DE ESCRIBIR NADA (1 antes que 2). Componer lee
// la config del tenant, que es I/O. Haciéndolo después, un fallo dejaría una
// solicitud CONFIRMADA cuya cotización no se llegó a armar.
//
// POR QUÉ LA TRANSICIÓN VA PRIMERO (2 antes que 3). El compare-and-swap es lo que
// hace que de dos aprobaciones simultáneas gane UNA: la perdedora se lleva
// ErrConflict —o un TransitionError si la otra ya confirmó— y sale sin escribir ni
// mandar nada. Con la revisión primero, las dos habrían dejado su `approved` y el
// cliente habría recibido dos cotizaciones.
//
// POR QUÉ EL ENVÍO VA DESPUÉS DE LA REVISIÓN (4 después de 3). Los dos órdenes
// mienten en algún fallo:
//
//   - enviar y luego escribir: si la escritura falla, se le mandó al cliente una
//     cotización que NO CONSTA, y el reintento del dueño manda una SEGUNDA. La
//     revisión existe para ser la defensa el día que el cliente diga «a mí me
//     dijeron $X», y este orden produce justo el caso que la destruye.
//   - escribir y luego enviar: si el envío falla, la revisión afirma un mensaje que
//     no llegó. Es un falso positivo inofensivo —el cliente no puede reclamar lo
//     que nunca leyó— y queda en el log.
//
// Se elige escribir primero: el fallo del segundo orden es recuperable mirando el
// log; el del primero destruye la única prueba escrita.
//
// LOS FALLOS PARCIALES QUE QUEDAN, dichos sin adornos:
//
//   - falla (2) ⇒ no pasó NADA: el borrador sigue donde estaba.
//   - falla (3) ⇒ la solicitud queda CONFIRMADA sin su revisión y SIN mandar nada al
//     cliente (no se envía lo que no se registró), sin empuje y sin métrica. El
//     dueño recibe un error que lo dice con esas palabras, y su reintento chocará
//     con que la solicitud ya está en el destino: tiene que escribirle al cliente a
//     mano. Es el precio de no tener las dos escrituras en una transacción.
//   - falla (4) ⇒ todo está escrito y el cliente no se enteró. La respuesta al dueño
//     sigue siendo un éxito: notificar no puede tumbar una transición aplicada.
//   - falla (5) ⇒ el CRM no recibe esa revisión y no se reintenta (ver CRMPusher).
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE ESTA PUERTA NO HACE
// ════════════════════════════════════════════════════════════════════════════
//
// NO MATERIALIZA las líneas del borrador en las líneas de la solicitud. El pipeline
// escribe sus líneas en la REVISIÓN, y volcarlas aquí borraría la personalización
// («sin cebolla») que quien prepara el pedido tiene que leer. Quien materializa es
// la corrección de líneas del dueño, que es donde el precio se pone. Lo que hace
// esta puerta es NEGARSE a vender un presupuesto que no está materializado
// (ErrEmptyQuote) en vez de mandar una cotización de cero líneas.
//
// NO COBRA. La solicitud queda en `confirmed`, no en `deposit_requested`: la
// plantilla de seña se ADJUNTA al texto (D-044.49 §1), que es otra cosa que pedirla.

package intakes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ApprovableStatus es el ÚNICO estado desde el que se aprueba: el presupuesto por
// aprobar (D-044.49). Es más estrecho que la máquina de estados a propósito —
// CanTransition también admite `open → confirmed`, que es como cierra el carrito
// numérico—, y esa diferencia no es cosmética: una solicitud `open` es un carrito
// VIVO al que el cliente le está añadiendo líneas y que todavía no tiene su línea de
// envío. Aprobarla sería cerrarle el pedido al cliente por debajo y cotizarle un
// envío que nadie calculó.
//
// Es el gemelo exacto de EditableStatus: la misma respuesta a «¿sobre qué estado
// opera el dueño?», dada por la otra puerta de la misma pantalla.
const ApprovableStatus = StatusPendingApproval

// LineKeyUnitPrice y LineKeyLabel son las dos claves del contrato §7.4 que ESTA
// puerta lee de cada línea del borrador. Son literales porque el contrato es del
// CABLE y no de un struct de Go: lo que se lee es el payload que ya está escrito en
// la revisión. Eran `ClaveLineaUnitPrice` y `ClaveLineaLabel` en el paquete viejo.
//
// `unit_price` es un número NULLABLE y siempre presente en el productor: §7.4
// escribe `"unit_price": null` para la línea que el dueño tiene que precificar, y un
// 0 significa otra cosa (un artículo de regalo). Toda la precondición de la
// aprobación vive en esa diferencia.
//
// ⚠️ Lo que impide que se desincronicen del productor es un candado que compara
// estas constantes contra las etiquetas JSON reales del borrador del pipeline. Ese
// candado NO puede vivir aquí (este paquete no importa a quien lo produce): es de
// quien reconstruya el productor.
const (
	LineKeyUnitPrice = "unit_price"
	LineKeyLabel     = "label"
)

// ErrEmptyQuoteText es la aprobación sin texto. El texto es OBLIGATORIO (D-044.49):
// el dueño es el autor de lo que sale, y una aprobación muda dejaría al cliente con
// un pedido confirmado del que nunca le contaron el precio. No se sustituye por un
// texto genérico de la plataforma — el genérico es justo lo que esta puerta apaga.
var ErrEmptyQuoteText = errors.New("la aprobación no trae el texto de la cotización que se le manda al cliente")

// ErrEmptyQuote es la aprobación de una solicitud SIN LÍNEAS DE CLIENTE.
//
// No es una precondición decorativa: el borrador del pipeline vive en la revisión y
// NO en las líneas, así que una solicitud recién interpretada tiene cero líneas
// hasta que el dueño las guarda. Sin esta guarda, aprobar ese borrador confirmaría
// un pedido de total 0 y le empujaría al CRM un documento vacío, en silencio.
var ErrEmptyQuote = errors.New("la solicitud no tiene ni una línea de cliente que cotizar")

// ErrNoQuoteSender es el servicio sin canal para responderle al cliente (R-02). Es
// un ERROR y no un silencio —al revés que WithNotifier o WithCRMPusher, que son
// efectos best-effort— porque aquí el mensaje ES el acto: el botón se llama «Aprobar
// y responder», y aprobar sin poder responder confirmaría el pedido dejando al
// cliente sin enterarse. Falla ANTES de escribir nada. Lo devuelven Approve y
// RequestInfo.
var ErrNoQuoteSender = errors.New("intakes: el servicio no tiene canal para responderle al cliente (falta WithQuoteSender)")

// ErrNoRevisionWriter es el servicio cuyo store no sabe escribir revisiones. Mismo
// criterio: una aprobación sin rastro no es una aprobación (D-041.26), así que se
// corta antes de tocar el estado.
var ErrNoRevisionWriter = errors.New("intakes: el store cableado no sabe escribir revisiones (RevisionWriter)")

// PendingPriceLine es UNA línea del borrador que sigue sin precio, con lo justo para
// que el dueño la encuentre en su pantalla: la POSICIÓN en el borrador (base 0) y la
// etiqueta.
//
// La posición y no el sku, y eso importa: una línea que el catálogo no reconoció
// —que es justo la que suele no tener precio— NO TIENE SKU. La posición es lo único
// que identifica una línea dentro de su revisión, y el orden de `lines` es contrato.
type PendingPriceLine struct {
	Index int    `json:"index"`
	Label string `json:"label"`
}

// PendingPriceError es el rechazo de una aprobación cuyo borrador todavía tiene
// líneas sin precio.
//
// Lleva TODAS las líneas y no la primera, por el mismo motivo que InvalidItemsError
// acumula sus defectos: quien tiene tres renglones sin precificar tiene que verlos
// los tres, no descubrirlos de uno en uno a base de aprobaciones rechazadas.
type PendingPriceError struct {
	Lines []PendingPriceLine
}

// Error devuelve "el borrador tiene <n> líneas sin precio (<i>:<label>, …)": el
// recuento y, entre paréntesis, cada línea como `índice:etiqueta` separadas por
// ", " y en el orden de Lines. Sin concordancia de número («1 líneas») y sin
// escapar la etiqueta: es un texto observable y se conserva byte a byte.
func (e *PendingPriceError) Error() string {
	panic(pendiente.Implementar("intakes.PendingPriceError.Error"))
}

// NotApprovableError es el rechazo de una aprobación sobre una solicitud que no está
// por aprobar. Lleva el estado actual, ya normalizado, porque quien lo recibe
// necesita saber qué pasó: alguien la movió, o nunca llegó a ser un presupuesto.
//
// Es un tipo DISTINTO de NotEditableError y de TransitionError aunque los tres
// hablen de estados: si compartieran tipo, el llamante no podría distinguir «no
// puedes ir ahí» de «no puedes editar aquí» de «no puedes aprobar aquí».
type NotApprovableError struct {
	Status string
}

// Error devuelve `una solicitud en "<Status>" no se puede aprobar`, con el estado
// entre comillas al modo de %q (un estado raro sale escapado). Texto observable.
func (e *NotApprovableError) Error() string {
	panic(pendiente.Implementar("intakes.NotApprovableError.Error"))
}

// LastRevision devuelve la revisión de número MÁS ALTO, que es el borrador vigente.
// El bool distingue «no hay revisiones» (cero-valor y false) de «la primera». Si dos
// comparten el número más alto, gana la que aparece ANTES en la lista.
//
// Se busca el máximo en vez de tomar el último elemento aunque los stores devuelvan
// la lista ordenada: el orden es contrato de la CONSULTA, y colgar de él una
// decisión que dice qué se puede vender lo convertiría en contrato del dominio sin
// que nadie lo hubiera declarado. No muta la entrada.
func LastRevision(revisions []Revision) (Revision, bool) {
	panic(pendiente.Implementar("intakes.LastRevision"))
}

// PendingPriceLines son las líneas SIN PRECIO del borrador vigente de la solicitud:
// LinesWithoutPrice sobre el payload de LastRevision. Sin revisiones, o con el
// presupuesto entero, devuelve nil ⇒ se puede aprobar.
//
// Mira SOLO la última revisión y no el histórico, y es lo correcto: las anteriores
// son el rastro de cómo se llegó aquí, no lo que se vende. Una línea que nació sin
// precio en la rev 1 y que el dueño precificó en la rev 2 está resuelta, sea cual
// sea el orden en que el store devuelva la lista.
func PendingPriceLines(revisions []Revision) []PendingPriceLine {
	panic(pendiente.Implementar("intakes.PendingPriceLines"))
}

// LinesWithoutPrice recorre el payload de UNA revisión y devuelve, en orden, las
// líneas de `lines` (PayloadKeyLines) cuyo `unit_price` no es un número, cada una
// con su posición ORIGINAL en la lista y su `label`. Sin ninguna devuelve nil. Es
// PURA: sin BD, sin reloj y sin mutar la entrada.
//
// «Sin precio» es: `null`, la clave AUSENTE y cualquier valor que no sea un número
// que quepa en un float64 (una cadena `"12"`, un booleano, un objeto, una lista, un
// número desbordado). Ante la duda la línea está pendiente: el error de esa elección
// es un rechazo que el dueño arregla poniendo el precio; el de la contraria sería
// cotizarle al cliente una línea a 0. La clave ausente cuenta porque un número de Go
// no distingue «no vino» de «vino 0», y aquí esa diferencia separa «el dueño tiene
// que ponerle precio» de «es un regalo». Un 0 y un negativo SÍ son precio.
//
// Las claves se comparan EXACTAS (mayúsculas incluidas: `Unit_Price` no es
// `unit_price`); con una clave repetida manda la ÚLTIMA aparición. La etiqueta sale
// tal cual, sin recortar; vacía si no viene o no es una cadena: el índice ya
// identifica la línea, y una etiqueta inventada sería peor que ninguna.
//
// Lo que NO ENCUENTRA es tan importante como lo que encuentra, y no es un hueco:
//
//   - Un payload vacío, que no es JSON válido, que no es un objeto, o que no trae
//     `lines` como lista, devuelve nil. Es el caso de las revisiones `cart`,
//     `corrected` y `approved`, cuyo contrato es {"version","total","items"} con
//     precio NO nullable: por construcción no pueden tener una línea sin precio. La
//     consecuencia es la que se quiere: en cuanto el dueño guarda las líneas, la
//     revisión vigente pasa a ser `corrected` y la precondición queda satisfecha
//     porque de verdad lo está.
//   - Una línea que no es un objeto se salta (y sigue ocupando su posición). El
//     payload lo escribe un productor nuestro; si su forma cambió, lo que hay que
//     arreglar no es esta función.
func LinesWithoutPrice(payload json.RawMessage) []PendingPriceLine {
	panic(pendiente.Implementar("intakes.LinesWithoutPrice"))
}

// Approve APRUEBA el presupuesto: le manda al cliente la cotización que escribió el
// dueño —con la plantilla de seña del tenant adjunta—, deja la solicitud en
// `confirmed` con su revisión `approved`, y empuja esa revisión al puente CRM.
//
// 🔴 INV-1 (R-01) — NINGÚN CAMINO AUTOMÁTICO LLEGA AQUÍ. Este método lo llama UN solo
// sitio, el handler del POST de aprobación de la dueña, y nada más: ni el motor de
// flujos, ni el carrito, ni el pipeline, ni la cola, ni el propio dominio. No hay
// reloj que apruebe y no hay LLM que responda al cliente. Lo que sostiene esa frase
// no es este comentario sino el candado sobre el AST (inv1_aprobar_test.go).
//
// Las validaciones van TODAS antes de la primera escritura, en ESTE orden; la
// primera que falla decide el error y no se escribe ni se manda nada:
//
//  1. ErrNoQuoteSender — el servicio no tiene QuoteSender (R-02);
//  2. ErrNoRevisionWriter — el store no sabe escribir revisiones;
//  3. ErrEmptyQuoteText — `renderedText` vacío o solo espacios en blanco. El
//     recorte es solo para DECIDIR: lo que se compone, se guarda y se manda es el
//     original byte a byte;
//  4. ErrNotFound — la solicitud no es del tenant (404 opaco, INV-8);
//  5. *NotApprovableError — su estado normalizado no es ApprovableStatus;
//  6. *PendingPriceError — el borrador vigente tiene líneas sin precio
//     (PendingPriceLines), todas listadas;
//  7. ErrEmptyQuote — no tiene ni una línea de CLIENTE. Las de LA PLATAFORMA
//     (prefijo ReservedSKUPrefix: hoy el envío) no cuentan: un presupuesto que solo
//     lleva la línea de envío no es un pedido, es un envío de nada.
//
// Después, el orden de la cabecera del fichero:
//
//   - el texto es QuoteSender.QuoteText sobre la solicitud leída y el texto del
//     dueño, y es EL MISMO que se guarda y que se envía;
//   - la transición es SetStatus a `confirmed` con NoticeByCaller: el aviso genérico
//     del estado destino NO sale (lo dice mejor la cotización). Su error se devuelve
//     tal cual (*TransitionError, ErrConflict) y no se escribe ni se manda nada;
//   - la revisión es de clase `approved`, autor `owner` (aquí sí decide una persona;
//     sigue siendo un ROL, cero PII), con RenderedText = el texto compuesto y como
//     payload ApprovedRevisionPayload del total de la solicitud YA transicionada y
//     TODAS las líneas persistidas, la de envío incluida y sin la personalización.
//     Si no se escribe, el error —que envuelve al del store— dice que la solicitud
//     quedó CONFIRMADA, que la cotización NO se envió y trae el intake_id; no hay
//     envío, ni empuje, ni métrica;
//   - el envío es QuoteSender.SendQuote con la solicitud transicionada;
//   - el empuje es PushRevisionToCRM con el número que el store ASIGNÓ a esa
//     revisión (R-12), nunca una constante;
//   - la métrica es EventApproved (ver WithMetrics), y va la última.
//
// Devuelve el detalle compuesto con lo que ya está en la mano y NO con una
// relectura (releer podría fallar después de haber mandado el mensaje): la
// solicitud transicionada, las líneas leídas, el histórico leído con la revisión
// nueva AL FINAL —sobre una lista nueva, sin escribir en la que entregó el store— y
// BuyerDataPresent tal como se leyó. Es el mismo detalle que se empuja al CRM.
func (s *Service) Approve(ctx context.Context, tenantID, intakeID, renderedText string) (Detail, error) {
	panic(pendiente.Implementar("intakes.Service.Approve"))
}
