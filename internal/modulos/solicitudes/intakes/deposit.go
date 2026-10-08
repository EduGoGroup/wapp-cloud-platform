// Porta internal/intakes/deposit.go @ 64c181a.
//
// deposit.go es el RECORDATORIO DE LA SEÑA, y es PEREZOSO (D-041.12).
//
// CERO RELOJ (ADR-0003 / D-041.16). No hay cron, ni ticker, ni goroutine de fondo,
// ni barrido: nadie recorre la tabla buscando señas vencidas. El recordatorio se
// evalúa cuando alguien TOCA la solicitud, y «tocar» son exactamente tres cosas:
//
//   - el DUEÑO abre su bandeja      → Service.List   (Remind)
//   - el DUEÑO abre una solicitud   → Service.Get    (Remind)
//   - el CLIENTE vuelve a escribir  → el motor de flujos (RemindContact)
//
// De ahí que esto no sea un servicio con vida propia sino un colaborador OPCIONAL
// que los tres caminos invocan y que, sin cablear, no hace absolutamente nada.
//
// UN SOLO RECORDATORIO, y lo sostiene la BD, no una comprobación en memoria: la
// marca (deposit_reminded_at) se escribe con un COMPARE-AND-SWAP contra NULL ANTES
// de mandar nada. De N toques simultáneos —dos pestañas del dueño y un mensaje del
// cliente a la vez— exactamente uno gana la escritura y exactamente uno manda.
//
// ORDEN (mirar si hay algo que decir → marcar → enviar) y el error que se elige: si
// el envío falla después de marcar, ese cliente se queda SIN recordatorio para
// siempre. Es deliberado («un solo recordatorio en v1; el dueño decide cancelar o
// esperar»): el pecado que no se puede cometer es el goteo de mensajes a alguien que
// quizá ya pagó. Al revés —enviar y luego marcar— cualquier fallo de escritura se
// convierte en un segundo WhatsApp, y en un tercero al siguiente toque.
//
// LO QUE NO HACE: no cambia el estado de nada. Pasarse de deposit_due_at NO mata la
// solicitud (nada vence por tiempo) — esto solo AVISA.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DepositStore es lo que el recordatorio necesita de la persistencia, y solo eso
// (ISP): no ve el listado, ni el export, ni las transiciones. Lo satisfacen el
// adaptador Postgres (producción) y *MemoryStore (tests).
type DepositStore interface {
	// MarkDepositReminded intenta ganarse el derecho a recordar UNA solicitud: marca
	// deposit_reminded_at = `at` si y solo si la solicitud sigue en
	// `deposit_requested`, tiene fecha límite, esa fecha ya pasó (según `at`) y NADIE
	// la marcó antes. Devuelve la solicitud con la marca puesta y `true` si escribió;
	// `false` —sin error— si no le tocaba, que es el caso normal y no una avería.
	//
	// Es un COMPARE-AND-SWAP y ahí está toda la garantía de «un solo recordatorio»:
	// no vale leer-y-decidir en memoria porque entre la lectura y el envío caben otro
	// toque y otro mensaje.
	//
	// La condición de ESTADO no es decorativa: si el dueño ya marcó la seña como
	// recibida (deposit_paid) o canceló, la fila deja de casar y al cliente no se le
	// recuerda algo que ya hizo.
	MarkDepositReminded(ctx context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error)

	// PendingDepositReminders devuelve las solicitudes de ESE contacto a las que
	// tocaría recordarles la seña a fecha `at` (vencidas y sin recordar), como mucho
	// `limit`. Existe para el toque del mensaje entrante, que es el único de los tres
	// que NO tiene la solicitud delante: el motor de flujos solo sabe quién habló.
	PendingDepositReminders(ctx context.Context, tenantID, contactID string, at time.Time, limit int) ([]Intake, error)
}

// DepositReminder evalúa y manda el recordatorio de la seña. Es seguro para uso
// concurrente (no guarda estado propio). Satisface el puerto DepositTouch del
// Service (Remind).
//
// Reusa el Notifier ENTERO en vez de duplicar su camino de salida: el texto de la
// seña sale de la config del tenant, el destino se resuelve por la vía custodiada de
// PII (ADR-0017) y la entrega es el mismo envío, con sus mismas reglas de log (cero
// PII: el número del contacto se usa y se suelta, jamás se registra). Aquí no hay
// una segunda puerta hacia WhatsApp: hay un segundo motivo para abrir la misma.
//
// EL TEXTO que recibe el cliente es observable, y son dos partes separadas por una
// línea en blanco ("\n\n"):
//
//   - el PREÁMBULO, que es de la plataforma y va literal: "⏰ Te recordamos que tu
//     pedido sigue esperando la seña para quedar reservado. Si ya la pagaste,
//     avísanos por aquí y lo confirmamos.";
//   - la PLANTILLA DE SEÑA del tenant, renderizada como en la petición de la seña
//     ({total}, {fecha_limite}, {plazo}) con la fila que devolvió el
//     compare-and-swap: los datos para pagar son del tenant y no pueden vivir en el
//     código, y sin ellos el recordatorio obligaría al cliente a rebuscar el mensaje
//     anterior en el chat.
//
// Como consecuencia, un tenant SIN plantilla no recuerda nada —«te pedimos una seña»
// sin decir dónde pagarla es peor que el silencio— y, sobre todo, NO GASTA LA MARCA:
// la config se lee ANTES del compare-and-swap, así que el día que el tenant
// configure su plantilla sus clientes de hoy todavía tienen su único recordatorio.
//
// UN TOQUE MANDA COMO MUCHO UN RECORDATORIO, aunque haya veinte señas vencidas: el
// toque es SÍNCRONO dentro de una lectura del dueño y cada envío espera el Ack del
// Edge. Es una cota de LATENCIA, no de política: la solicitud que no entró en este
// toque entra en el siguiente.
type DepositReminder struct{}

// ReminderOption configura el DepositReminder al construirlo.
type ReminderOption func(*DepositReminder)

// WithReminderClock inyecta el reloj con el que se decide si la seña venció. Sin la
// opción, o con un reloj `nil`, el recordatorio usa time.Now.
//
// Existe porque la regla ES una comparación de tiempos: sin reloj inyectable, probar
// «vencida y sin recordar ⇒ un recordatorio» exigiría dormir tres días. El reloj
// entra por UN sitio y se usa en LOS DOS extremos de la comparación (el instante que
// se compara contra deposit_due_at y el que se le pasa al store para escribir
// deposit_reminded_at), así que un test no puede quedarse con medio tiempo falso.
func WithReminderClock(now func() time.Time) ReminderOption {
	panic(pendiente.Implementar("intakes.WithReminderClock"))
}

// NewDepositReminder construye el recordatorio sobre el notificador y el store, y
// aplica las opciones en orden. Las dos dependencias son obligatorias —sin
// notificador no hay a quién avisar y sin store no hay forma de garantizar que se
// avisa UNA vez—, pero construirlo con alguna a `nil` NO falla: devuelve un
// recordatorio que se calla (ver Remind).
func NewDepositReminder(n *Notifier, store DepositStore, opts ...ReminderOption) *DepositReminder {
	panic(pendiente.Implementar("intakes.NewDepositReminder"))
}

// Remind evalúa el recordatorio sobre solicitudes YA LEÍDAS: es el toque del dueño
// (listado y detalle), donde la fila viene de la misma consulta que la pantalla y no
// cuesta un viaje extra a la BD.
//
// No devuelve error, y NO PUEDE REVENTAR: que un recordatorio no salga no puede
// convertir el listado del dueño en un 500. Un recordatorio a medias (receptor
// `nil`, sin store, sin notificador o con el notificador incompleto) no hace nada;
// un fallo del store o del envío se registra y se traga; y un PÁNICO en el store, en
// el resolver de PII o en el transporte se contiene y se registra como error.
//
// El PRE-FILTRO en memoria es lo que hace gratis este camino en casi todos los
// toques: solo se le pregunta al store por una fila que esté en `deposit_requested`
// (estado normalizado), tenga fecha límite, esa fecha NO sea posterior al reloj (el
// instante exacto del vencimiento ya cuenta) y NO esté recordada. Una bandeja sin
// señas vencidas no toca el store ni lee la config. Lo que decide de verdad sigue
// siendo el compare-and-swap: el pre-filtro mira la fila que trae el llamante, que
// puede estar vieja.
//
// Por cada candidata, en este orden: lee la config del tenant (sin plantilla no
// sigue, y la marca queda libre) → gana la marca (MarkDepositReminded con el
// instante del reloj; si no la gana, no envía) → envía. Tras el PRIMER recordatorio
// enviado corta: un toque manda como mucho uno. Una candidata que no llegó a
// enviarse no cuenta, y se sigue con la siguiente.
//
// A diferencia de RemindContact, no devuelve los textos: este toque lo dispara una
// pantalla de la consola, no una conversación, y no hay hilo al que pertenezcan.
func (r *DepositReminder) Remind(ctx context.Context, tenantID string, touched []Intake) {
	panic(pendiente.Implementar("intakes.DepositReminder.Remind"))
}

// RemindContact evalúa el recordatorio de las solicitudes de un contacto: es el
// toque del CLIENTE, que llega por un mensaje entrante y no trae ninguna solicitud
// consigo (el motor solo sabe quién habló). Por eso se le pregunta al store
// (PendingDepositReminders, con el instante del reloj y un límite de 1): la
// conversación viva y la solicitud son cosas distintas —el cliente puede escribir
// «hola» sin carrito abierto y seguir debiendo una seña de la semana pasada—.
//
// 🔑 DEVUELVE LOS TEXTOS QUE MANDÓ (D-044.24), y solo este de los tres toques lo
// hace: el recordatorio es un saliente FUERA DE TURNO y tiene que dejar rastro en
// el hilo del evento conversacional, pero este paquete no puede escribirlo; quien sí
// puede es el llamante, y por eso lo que cruza la frontera son las cadenas. Cada
// texto es EXACTAMENTE el que se le entregó al envío. `nil` = no se mandó nada, que
// es el caso normal.
//
// Se considera MANDADO en cuanto se ganó la marca y se intentó la entrega, aunque
// el envío falle: la marca ya se gastó y, a todos los efectos, ese recordatorio
// ocurrió.
//
// No devuelve error y no puede reventar, por lo mismo que Remind: un fallo aquí no
// puede tumbar el procesamiento del mensaje que el cliente acaba de mandar.
// Devuelve `nil` sin tocar el store si el recordatorio está a medias o si
// `tenantID` o `contactID` vienen vacíos; devuelve `nil` y lo registra como error
// si la consulta al store falla; y ⚠️ ante un PÁNICO contenido devuelve `nil`, NO lo
// acumulado hasta ese punto: una lista parcial de recordatorios de los que no se
// sabe si salieron sería inventar rastro, y se prefiere perderlo.
//
// Por lo demás sigue la misma secuencia que Remind (config → marca → envío) y la
// misma cota de uno por toque.
func (r *DepositReminder) RemindContact(ctx context.Context, tenantID, contactID string) []string {
	panic(pendiente.Implementar("intakes.DepositReminder.RemindContact"))
}
