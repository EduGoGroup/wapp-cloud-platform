package intakes

import (
	"context"
	"time"
)

// ============================================================================
// Recordatorio de la SEÑA — PEREZOSO (D-041.12, Plan 041 · T4.4)
//
// CERO RELOJ (ADR-0003 / D-041.16). No hay cron, ni ticker, ni goroutine de fondo,
// ni barrido: nadie recorre la tabla buscando señas vencidas. El recordatorio se
// evalúa cuando alguien TOCA la solicitud, y "tocar" son exactamente tres cosas:
//
//   - el DUEÑO abre su bandeja      → Service.List
//   - el DUEÑO abre una solicitud   → Service.Get
//   - el CLIENTE vuelve a escribir  → runtime.HandleIncoming (RemindContact)
//
// De ahí que esto no sea un servicio con vida propia sino un colaborador OPCIONAL
// que los tres caminos invocan y que, sin cablear, no hace absolutamente nada.
//
// UN SOLO RECORDATORIO, y lo sostiene la BD, no una comprobación en memoria: la
// marca (deposit_reminded_at) se escribe con un COMPARE-AND-SWAP contra NULL ANTES
// de mandar nada. De N toques simultáneos —dos pestañas del dueño y un mensaje del
// cliente a la vez— exactamente uno gana el UPDATE y exactamente uno manda. Los
// demás no llegan ni a resolver el destino.
//
// ORDEN (mirar si hay algo que decir → marcar → enviar) y el error que se elige: si
// el envío falla después de marcar, ese cliente se queda SIN recordatorio para
// siempre. Es deliberado y es la lectura de D-041.12 («un solo recordatorio en v1; el
// dueño decide cancelar o esperar»): el pecado que no se puede cometer es el goteo de
// mensajes a alguien que quizá ya pagó. Al revés —enviar y luego marcar— cualquier
// fallo de escritura se convierte en un segundo WhatsApp, y en un tercero al
// siguiente toque. Lo único que va ANTES de la marca es la lectura de config, que no
// escribe nada y evita gastarla en un tenant que todavía no puede decir nada.
//
// LO QUE NO HACE: no cambia el estado de nada. Pasarse de deposit_due_at NO mata la
// solicitud (D-041.16, nada vence por tiempo) — esto solo AVISA. No confundir con
// el reloj que T4.7 derogó: aquel mataba pedidos.
// ============================================================================

// depositReminderPreamble es la respuesta ENLATADA del recordatorio (D-041.12). Va
// SEGUIDA de la plantilla de seña del tenant, y esa composición es la decisión:
//
//   - la parte de arriba es de la plataforma y por eso está en el código;
//   - los datos para pagar son del tenant y no pueden vivir aquí (notifier.go), así
//     que se reusa su plantilla en vez de mandar un recordatorio que obliga al
//     cliente a rebuscar el mensaje anterior en el chat.
//
// Como consecuencia GRATIS, un tenant sin plantilla no recuerda nada: es la misma
// decisión de producto de T4.2 —«te pedimos una seña» sin decir dónde pagarla es
// peor que el silencio— aplicada sin repetir la regla.
const depositReminderPreamble = "⏰ Te recordamos que tu pedido sigue esperando la seña para quedar reservado. " +
	"Si ya la pagaste, avísanos por aquí y lo confirmamos."

// maxRemindersPerTouch acota cuántos recordatorios sale a mandar UN toque. Es 1, y
// no es timidez: el toque es SÍNCRONO dentro de un GET del dueño, y cada envío
// espera el Ack del Edge. Sin cota, la primera vez que un tenant abre su bandeja con
// veinte señas vencidas su listado se quedaría colgado veinte round-trips —y si el
// Edge está lento, el GET muere por timeout y la consola deja de funcionar por culpa
// de una cortesía—.
//
// No se pierde nada: lo perezoso es eventual por definición. La solicitud que no
// entró en este toque entra en el siguiente, y el dueño toca su bandeja muchas veces
// al día. La cota es de LATENCIA, no de política.
const maxRemindersPerTouch = 1

// DepositStore es lo que el recordatorio necesita de la persistencia, y solo eso
// (ISP): no ve el listado, ni el export, ni las transiciones. Lo satisface *Postgres
// (producción) y *MemoryStore (tests).
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
// concurrente (no guarda estado propio).
//
// Reusa el Notifier ENTERO en vez de duplicar su camino de salida: el texto de la
// seña sale de la config del tenant como en T4.2, el destino se resuelve por la vía
// custodiada de PII (ADR-0017) y la entrega es el mismo SendText, con sus mismas
// reglas de log (cero PII, command_id en los fallos). Aquí no hay una segunda puerta
// hacia WhatsApp: hay un segundo motivo para abrir la misma.
type DepositReminder struct {
	notifier *Notifier
	store    DepositStore
	now      func() time.Time
}

// ReminderOption configura el DepositReminder al construirlo.
type ReminderOption func(*DepositReminder)

// WithReminderClock inyecta el reloj con el que se decide si la seña venció. Sin
// él, time.Now.
//
// Existe porque la regla ES una comparación de tiempos: sin reloj inyectable, probar
// «vencida y sin recordar ⇒ un recordatorio» exigiría dormir tres días o mentirle a
// la fila poniéndole fechas del pasado en cada test. El reloj entra por UN sitio y
// se usa en LOS DOS extremos de la comparación (el `at` que se compara contra
// deposit_due_at y el que se escribe en deposit_reminded_at), así que un test no
// puede quedarse con medio tiempo falso.
func WithReminderClock(now func() time.Time) ReminderOption {
	return func(r *DepositReminder) {
		if now != nil {
			r.now = now
		}
	}
}

// NewDepositReminder construye el recordatorio sobre el notificador y el store. Las
// dos dependencias son obligatorias: sin notificador no hay a quién avisar y sin
// store no hay forma de garantizar que se avisa UNA vez (ver Remind, que se calla si
// falta alguna).
func NewDepositReminder(n *Notifier, store DepositStore, opts ...ReminderOption) *DepositReminder {
	r := &DepositReminder{notifier: n, store: store, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Remind evalúa el recordatorio sobre solicitudes YA LEÍDAS: es el toque del dueño
// (listado y detalle), donde la fila viene de la misma consulta que la pantalla y no
// cuesta un viaje extra a la BD.
//
// No devuelve error, y es el mismo contrato que NotifyStatus (notifier.go, regla 1):
// que un recordatorio no salga no puede convertir el listado del dueño en un 500. Un
// listado que falla porque un teléfono estaba apagado sería una consola inservible.
//
// El PRE-FILTRO en memoria (candidate) es lo que hace que este camino sea gratis en
// el 99,9 % de los toques: una bandeja sin señas vencidas no ejecuta ni una
// sentencia. Lo que decide de verdad sigue siendo el compare-and-swap del store —el
// pre-filtro solo evita preguntar por lo que no puede ser—.
func (r *DepositReminder) Remind(ctx context.Context, tenantID string, touched []Intake) {
	if !r.usable() {
		return
	}
	defer r.contenerPánico("recordatorio de seña sobre solicitudes ya leídas")

	at := r.now()
	sent := 0
	for _, in := range touched {
		if !candidate(in, at) {
			continue
		}
		if r.remindOne(ctx, tenantID, in.ID, at) {
			sent++
		}
		if sent >= maxRemindersPerTouch {
			return
		}
	}
}

// RemindContact evalúa el recordatorio de TODAS las solicitudes de un contacto: es
// el toque del CLIENTE, que llega por un mensaje entrante y no trae ninguna
// solicitud consigo (el motor solo sabe quién habló).
//
// Se le pregunta a la BD en vez de reusar lo que el motor ya tiene porque el motor no
// tiene NADA de esto: la conversación viva y la solicitud son cosas distintas —el
// cliente puede escribir «hola» sin carrito abierto y seguir debiendo una seña de un
// pedido de la semana pasada—.
//
// No devuelve error por la misma razón que Remind: un fallo aquí no puede tumbar el
// procesamiento del mensaje que el cliente acaba de mandar.
func (r *DepositReminder) RemindContact(ctx context.Context, tenantID, contactID string) {
	if !r.usable() || tenantID == "" || contactID == "" {
		return
	}
	defer r.contenerPánico("recordatorio de seña por mensaje entrante")

	at := r.now()
	pending, err := r.store.PendingDepositReminders(ctx, tenantID, contactID, at, maxRemindersPerTouch)
	if err != nil {
		r.notifier.log.Error("recordatorio de seña: no se pudo consultar las señas vencidas del contacto",
			"error", err, "tenant_id", tenantID, "contact_id", contactID)
		return
	}
	sent := 0
	for _, in := range pending {
		if r.remindOne(ctx, tenantID, in.ID, at) {
			sent++
		}
		if sent >= maxRemindersPerTouch {
			return
		}
	}
}

// usable dice si el recordatorio puede operar. Un recordatorio a medias no avisa,
// pero tampoco rompe al que lo invocó (mismo criterio que NotifyStatus).
func (r *DepositReminder) usable() bool {
	return r != nil && r.store != nil && r.notifier != nil && r.notifier.log != nil &&
		r.notifier.sender != nil && r.notifier.contacts != nil && r.notifier.settings != nil
}

// contenerPánico hace ESTRUCTURAL la promesa de que tocar una solicitud no puede
// reventar por culpa del recordatorio: sin esto, un pánico en el store, en el
// resolver de PII o en el transporte subiría por la pila hasta el handler y
// convertiría el LISTADO del dueño en un 500 — un listado que ni siquiera pidió
// mandar mensajes. Se registra entero; lo que se contiene es el alcance.
func (r *DepositReminder) contenerPánico(dónde string) {
	rec := recover()
	if rec == nil {
		return
	}
	r.notifier.log.Error("recordatorio de seña: pánico contenido; el toque de la solicitud sigue su curso",
		"donde", dónde, "panic", rec)
}

// candidate es el PRE-FILTRO sobre una fila ya leída: ¿tiene sentido siquiera
// preguntarle a la BD por esta solicitud? Reproduce la condición del
// compare-and-swap y NO la sustituye — lo que decide es el UPDATE, esto solo evita
// el viaje.
//
// Las cuatro condiciones son las de D-041.12: seña pedida (y no pagada ni
// cancelada), con fecha, vencida, y sin recordar.
func candidate(in Intake, at time.Time) bool {
	return NormalizeStatus(in.Status) == StatusDepositRequested &&
		!in.DepositDueAt.IsZero() &&
		!in.DepositDueAt.After(at) &&
		in.DepositRemindedAt.IsZero()
}

// remindOne intenta recordar UNA solicitud y dice si mandó algo. Los tres pasos van
// en ESTE orden y ninguno es intercambiable:
//
//  1. ¿PUEDE decirse algo? Se lee la config del tenant. Va primero justamente para
//     NO gastar la marca de un tenant sin plantilla de seña: si se marcara antes, ese
//     cliente perdería su único recordatorio para siempre, incluso después de que su
//     tenant configurara la plantilla. Este paso no escribe nada.
//  2. GANAR la marca (compare-and-swap). A partir de aquí, este toque —y ningún
//     otro— es el que recuerda esta solicitud.
//  3. ENVIAR, y renderizar con la fila que devolvió el CAS: es la única versión de la
//     solicitud que se sabe vigente, y de ella salen el total y la fecha del texto.
//     Que el envío falle después de marcar es el error elegido (cabecera del
//     fichero): antes un silencio que un goteo.
func (r *DepositReminder) remindOne(ctx context.Context, tenantID, intakeID string, at time.Time) bool {
	log := r.notifier.log.With("intake_id", intakeID, "tenant_id", tenantID)

	cfg, ok := r.notifier.depositSettings(ctx, tenantID, log)
	if !ok {
		return false // el silencio ya quedó registrado con su causa; la marca sigue libre
	}

	marked, won, err := r.store.MarkDepositReminded(ctx, tenantID, intakeID, at)
	if err != nil {
		log.Error("recordatorio de seña: no se pudo marcar la solicitud; no se envía", "error", err)
		return false
	}
	if !won {
		// Lo normal: la seña ya no está pendiente, no venció, o alguien recordó
		// primero. No es una avería y no merece más que un debug.
		log.Debug("recordatorio de seña: no procedía (ya recordada, no vencida o seña resuelta)")
		return false
	}

	text := depositReminderPreamble + "\n\n" + render(cfg.DepositTemplate, marked, cfg.DepositDueDays)
	r.notifier.deliver(ctx, tenantID, marked, text, log.With("motivo", "recordatorio_sena"))
	return true
}
