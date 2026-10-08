// Porta internal/intakes/vencimiento.go @ 64c181a.
//
// vencimiento.go es EL PLAZO DEL PRESUPUESTO: AVISA Y NO MATA (REQ-25, D-044.50;
// regla R-06).
//
// Un presupuesto que lleva demasiado tiempo esperando al dueño se MARCA. No se
// mueve, no se cierra y no se le manda nada al cliente: sigue en `pending_approval`
// en la base, con sus mismos destinos posibles, y lo único que cambia es que la
// bandeja lo pinta distinto. Los objetos de negocio no mueren por tiempo, mueren por
// acción humana (ADR-0029 Enmienda 2, D-041.16): la salida sigue siendo aprobar,
// rechazar o descartar.
//
// El fichero tiene DOS mitades independientes:
//
//  1. LA MARCA DERIVADA (Overdue). Pura, sin columna, sin transición y sin efecto:
//     se calcula AL LEER a partir de datos que ya están en la fila. Nadie la
//     persiste, así que no puede quedarse desincronizada de la verdad.
//  2. EL RECORDATORIO AL DUEÑO (ExpiryReminder). Ese SÍ tiene columna
//     (expiry_reminded_at), porque «una sola vez» no se puede sostener sin escribir
//     en algún sitio que ya se hizo.
//
// CERO RELOJ (ADR-0003 / D-041.16), igual que su gemelo deposit.go: no hay cron, ni
// ticker, ni goroutine de fondo, ni barrido. El recordatorio se evalúa cuando el
// DUEÑO toca su bandeja —Service.List y Service.Get— y en ningún otro sitio:
// descargar un CSV o pedir el resumen no puede disparar avisos.
//
// 🔴 EL EMISOR NO EXISTE TODAVÍA, Y ESO ESTÁ DECIDIDO (D-044.50 §2; trampa T-8). El
// canal real del aviso al dueño es un push que aún no se ha construido. Lo que hay
// aquí es todo lo demás —el plazo, el pre-filtro, el compare-and-swap y el orden en
// que van—, y el sumidero de hoy (LogOwnerNotice) solo deja traza en el log. NADIE
// PUEDE AFIRMAR QUE EL DUEÑO RECIBE EL RECORDATORIO: no lo recibe.
//
// LO QUE NO HAY, Y NO ES UN OLVIDO: el evento de telemetría del vencimiento. Ni se
// emite, ni se declara, ni existe la transición a `expired`. ⚠️ El NOMBRE de ese
// evento no se escribe entero en este fichero ni en ningún otro (trampa T-4): el
// candado que lo vigila barre el repo como texto, y un literal en un comentario es
// el primer paso hacia un literal en una emisión.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// QuoteDeadline es EL PLAZO: cuánto puede esperar un presupuesto en
// `pending_approval` antes de que la bandeja lo marque. Son 24 horas.
//
// Es una CONSTANTE DE PLATAFORMA y no un ajuste por tenant (D-044.50 §1, R-06):
//
//   - `tenant_settings.order_ttl_seconds` NO se reusa. Existe y se lee, pero su
//     migración afirma que ningún código actúa sobre ese valor desde que D-041.16 lo
//     derogó como causa de muerte. Obedecerlo aquí convertiría en FALSA una
//     afirmación que hoy es cierta y está vigilada. 🔴 Si alguien encuentra código
//     que actúe sobre esa columna, es un defecto y no una evolución.
//   - Un ajuste nuevo tampoco: nadie ha pedido afinarlo. Se paga el día que se pida,
//     y ese día esta constante se convierte en el espejo del DEFAULT de la columna
//     nueva sin deshacer nada.
const QuoteDeadline = 24 * time.Hour

// Overdue es LA MARCA DERIVADA: ¿este presupuesto lleva el plazo o más esperando al
// dueño, a fecha `at`? Pura y sin efectos — no consulta, no escribe y no notifica.
//
// Se calcula al leer, en cada lectura, y por eso no hay ninguna columna que pueda
// mentir. La consumen dos sitios que tienen que decir lo MISMO: la proyección al
// wire (lo que pinta la bandeja) y el pre-filtro del recordatorio. Un tercer sitio
// que reimplemente la regla es un defecto en cuanto discrepen.
//
// La regla, entera:
//
//   - Solo `pending_approval` (estado normalizado, que NO recorta espacios ni pliega
//     mayúsculas). `false` para todo lo demás: un pedido confirmado, uno con la seña
//     pedida, uno cancelado o un `expired` legado no esperan la decisión de nadie.
//   - LA BASE ES UpdatedAt: entrar en `pending_approval` la escribe, así que para
//     una solicitud que nadie ha tocado desde entonces es «cuándo quedó en manos del
//     dueño». Corregir las líneas también la escribe, así que una corrección
//     REINICIA el plazo, y es deliberado: el dueño acaba de actuar sobre ese
//     presupuesto. 🔴 De ahí que marcar el recordatorio NO pueda tocar updated_at:
//     reiniciaría el plazo que el propio recordatorio acaba de constatar.
//   - CreatedAt es el SUPLENTE, solo cuando UpdatedAt es cero. Con las dos, manda
//     UpdatedAt aunque CreatedAt sea más vieja.
//   - Sin ninguna de las dos fechas NO se marca: «no sé desde cuándo espera» se
//     contesta callando, no marcando todo lo que tenga la fecha en cero.
//   - Vencido es `base + QuoteDeadline <= at`: el instante EXACTO del plazo ya
//     cuenta, y un nanosegundo antes todavía no. Se comparan instantes, no relojes
//     de pared: la zona horaria de las fechas no cambia la respuesta.
//   - NO mira ExpiryRemindedAt: la marca dice «lleva demasiado esperando», y eso no
//     deja de ser verdad porque ya se haya avisado una vez.
func Overdue(in Intake, at time.Time) bool {
	panic(pendiente.Implementar("intakes.Overdue"))
}

// ExpiryStore es lo que el recordatorio del plazo necesita de la persistencia, y
// solo eso (ISP): no ve el listado, ni el export, ni las transiciones. Lo satisfacen
// el adaptador Postgres (producción) y *MemoryStore (tests).
//
// Un solo método, sin el hermano «pendientes por contacto» de su gemelo
// DepositStore: aquel existe porque el recordatorio de la SEÑA lo dispara también el
// mensaje entrante del cliente, que no trae ninguna solicitud consigo. Éste avisa al
// DUEÑO y solo lo disparan las lecturas del dueño, que siempre tienen las filas
// delante.
type ExpiryStore interface {
	// MarkExpiryReminded intenta ganarse el derecho a recordar UNA solicitud: marca
	// expiry_reminded_at = `at` si y solo si la solicitud sigue en
	// `pending_approval`, su plazo ya pasó (según `at`) y NADIE la marcó antes.
	// Devuelve la solicitud con la marca puesta y `true` si escribió; `false` —sin
	// error— si no le tocaba, que es el caso normal y no una avería.
	//
	// Es un COMPARE-AND-SWAP y ahí está toda la garantía de «un solo recordatorio»:
	// no vale leer-y-decidir en memoria porque entre la lectura y el aviso caben
	// otras dos pestañas del dueño.
	//
	// La condición de ESTADO no es decorativa: si el dueño ya aprobó, rechazó o
	// pidió información, la fila deja de casar y no se le recuerda algo que ya hizo.
	//
	// 🔴 Ganar la marca NO mueve el estado ni toca updated_at, que es la BASE del
	// plazo: si la tocara, la marca «vencido» de la bandeja se apagaría en el mismo
	// instante del aviso.
	MarkExpiryReminded(ctx context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error)
}

// OwnerNotice es EL EMISOR del recordatorio hacia el dueño, y es la pieza que HOY
// NO EXISTE de verdad (D-044.50 §2): el canal real es un push todavía sin construir.
//
// Está declarado como puerto —y no resuelto con una llamada directa— justamente
// para que enchufar el emisor real sea una línea en el arranque y no una reapertura
// de este fichero. El sumidero de hoy es LogOwnerNotice, que solo deja traza.
//
// NO devuelve error: un aviso que no sale no puede convertir el listado del dueño
// en un 500.
type OwnerNotice interface {
	// RemindOwner avisa al dueño de que esta solicitud lleva más del plazo
	// esperando su decisión. Recibe la solicitud tal como la devolvió el
	// compare-and-swap: la única versión que se sabe vigente.
	RemindOwner(ctx context.Context, tenantID string, in Intake)
}

// ExpiryReminder evalúa y emite el recordatorio del plazo. Es seguro para uso
// concurrente (no guarda estado propio). Satisface el puerto ExpiryTouch del
// Service (RemindOverdue).
type ExpiryReminder struct{}

// ExpiryOption configura el ExpiryReminder al construirlo. Es un tipo APARTE de
// ReminderOption (deposit.go) a propósito: son dos recordatorios con dos relojes y
// dos destinatarios, y un tipo común invitaría a pasarle a uno la opción del otro
// —que compilaría y no haría nada—.
type ExpiryOption func(*ExpiryReminder)

// WithExpiryClock inyecta el reloj con el que se decide si el plazo venció. Sin la
// opción, o con un reloj `nil`, el recordatorio usa time.Now.
//
// El reloj entra por UN sitio y se usa en LOS DOS extremos de la comparación (el
// instante que decide el vencimiento y el que se le pasa al store para escribir
// expiry_reminded_at), así que un test no puede quedarse con medio tiempo falso.
func WithExpiryClock(now func() time.Time) ExpiryOption {
	panic(pendiente.Implementar("intakes.WithExpiryClock"))
}

// NewExpiryReminder construye el recordatorio del plazo y aplica las opciones en
// orden. Las tres dependencias son obligatorias —sin emisor no hay a quién avisar,
// sin store no hay forma de garantizar que se avisa UNA vez, y sin log un fallo
// desaparecería sin dejar rastro—, pero construirlo con alguna a `nil` NO falla:
// devuelve un recordatorio que se calla (ver RemindOverdue).
func NewExpiryReminder(notice OwnerNotice, store ExpiryStore, log logger.Logger, opts ...ExpiryOption) *ExpiryReminder {
	panic(pendiente.Implementar("intakes.NewExpiryReminder"))
}

// RemindOverdue evalúa el recordatorio sobre solicitudes YA LEÍDAS: es el toque del
// dueño (listado y detalle), donde la fila viene de la misma consulta que la
// pantalla y no cuesta un viaje extra a la BD.
//
// No devuelve error, y NO PUEDE REVENTAR: que un recordatorio no salga no puede
// convertir el listado del dueño en un 500. Un recordatorio a medias (receptor
// `nil`, sin store, sin emisor o sin log) no hace nada; un fallo del store se
// registra como error y no se avisa; y un PÁNICO en el store o en el emisor se
// contiene y se registra como error.
//
// El PRE-FILTRO en memoria son DOS preguntas y no una: Overdue —la MISMA marca que
// pinta la bandeja, sin una segunda copia de la regla— y que la fila NO esté ya
// avisada (ExpiryRemindedAt en cero). La segunda es la que hace que una bandeja con
// veinte vencidos YA avisados no toque el store ni una vez. Lo que decide de verdad
// sigue siendo el compare-and-swap: el pre-filtro mira la fila que trae el
// llamante, que puede estar vieja.
//
// Por cada candidata, en ESTE orden y no al revés:
//
//  1. GANA la marca (MarkExpiryReminded, con el instante del reloj). Si no la gana
//     —ya se avisó, el plazo no venció o el dueño ya decidió— no avisa, y no es una
//     avería.
//  2. EMITE (OwnerNotice.RemindOwner), con la fila que DEVOLVIÓ el store y no con
//     la que traía el llamante.
//
// Que la emisión falle después de marcar es el error ELEGIDO: al revés, cualquier
// fallo de escritura se convertiría en un segundo aviso, y en un tercero al
// siguiente toque. A diferencia de su gemelo de la seña, aquí no hay un paso previo
// de config: este aviso no depende de ninguna.
//
// Tras el PRIMER aviso emitido corta: un toque avisa como mucho de UNA solicitud,
// aunque haya cinco vencidas (cota de latencia; las demás entran en los toques
// siguientes). Una candidata que no llegó a emitirse no cuenta, y se sigue con la
// siguiente.
//
// 🔴 NO cambia el estado de nada: el plazo avisa y no mata.
func (r *ExpiryReminder) RemindOverdue(ctx context.Context, tenantID string, touched []Intake) {
	panic(pendiente.Implementar("intakes.ExpiryReminder.RemindOverdue"))
}

// LogOwnerNotice es el emisor PROVISIONAL del recordatorio al dueño: deja traza en
// el log y nada más (D-044.50 §2, T-8). No es un stub de test — es lo que corre en
// producción hasta que exista el push real—, y por eso mismo nadie puede afirmar
// que el dueño recibe el aviso: no lo recibe.
//
// 🔴 Con esto cableado, la marca expiry_reminded_at SÍ se escribe en la base: el
// recordatorio «ocurrió» a todos los efectos y no se repetirá. Es el precio aceptado
// a sabiendas —una marca por un aviso que hoy no llega a ninguna persona—, y es
// también lo que hace que el día que exista el emisor real no haya que reconstruir
// la idempotencia.
type LogOwnerNotice struct{}

// NewLogOwnerNotice construye el sumidero de traza sobre el log dado. Con un log
// `nil` no falla: devuelve un sumidero que no hace nada.
func NewLogOwnerNotice(log logger.Logger) *LogOwnerNotice {
	panic(pendiente.Implementar("intakes.NewLogOwnerNotice"))
}

// RemindOwner implementa OwnerNotice dejando UNA traza de nivel INFO, con el mensaje
// "recordatorio de plazo: el presupuesto lleva más del plazo esperando al dueño" y
// estos campos, en este orden: `intake_id` (el id de la solicitud), `tenant_id`,
// `plazo_horas` (QuoteDeadline en horas enteras: 24), `emisor` = "traza" y
// `pendiente` = "el canal real es el push del Plan 045; hoy nadie recibe esto".
//
// CERO PII: NI el contacto ni el total, que aquí no aportan nada y son exactamente
// lo que no tiene por qué acabar en un fichero de log.
//
// No hace nada —y no revienta— con el receptor `nil` o construido sin log. El
// contexto no se usa: no hay a quién llamar todavía.
func (s *LogOwnerNotice) RemindOwner(ctx context.Context, tenantID string, in Intake) {
	panic(pendiente.Implementar("intakes.LogOwnerNotice.RemindOwner"))
}
