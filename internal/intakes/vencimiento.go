package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// ============================================================================
// EL PLAZO DEL PRESUPUESTO: AVISA Y NO MATA (Plan 044 · T4.5, REQ-25, D-044.50)
//
// Un presupuesto que lleva demasiado tiempo esperando al dueño se MARCA. No se
// mueve, no se cierra y no se le manda nada al cliente: sigue en
// `pending_approval` en la base, con sus mismos destinos posibles, y lo único que
// cambia es que la bandeja lo pinta distinto. Los objetos de negocio no mueren por
// tiempo, mueren por acción humana (ADR-0029 Enmienda 2, D-041.16): la salida
// sigue siendo aprobar, rechazar o descartar.
//
// Este fichero tiene DOS mitades independientes y conviene no confundirlas:
//
//   1. LA MARCA DERIVADA (Overdue). Pura, sin columna, sin transición y sin
//      efecto: se calcula AL LEER a partir de datos que ya están en la fila. Nadie
//      la persiste, así que no puede quedarse desincronizada de la verdad y no hay
//      backfill que hacer. Si mañana el plazo cambia, cambia el pasado también —y
//      eso es correcto, porque el plazo no es un hecho de la solicitud sino una
//      regla de la plataforma.
//
//   2. EL RECORDATORIO AL DUEÑO (ExpiryReminder). Ese SÍ tiene columna, porque
//      «una sola vez» no se puede sostener sin escribir en algún sitio que ya se
//      hizo.
//
// CERO RELOJ (ADR-0003 / D-041.16), igual que su gemelo deposit.go: no hay cron,
// ni ticker, ni goroutine de fondo, ni barrido. El recordatorio se evalúa cuando
// el DUEÑO toca su bandeja —Service.List y Service.Get, vía Service.touch— y en
// ningún otro sitio. NO cuelga de ListDetails ni de Summary por la razón que ya
// documenta service.go: descargar un CSV no puede disparar avisos.
//
// 🔴 EL EMISOR NO EXISTE TODAVÍA, Y ESO ESTÁ DECIDIDO (D-044.50 §2). El canal real
// del aviso al dueño es el push del Plan 045, que aún no se ha construido. Lo que
// se construye AQUÍ es todo lo demás —el plazo, el pre-filtro, el compare-and-swap
// y el orden en que van—, porque es la parte difícil y fácil de equivocar, y hoy
// se hace con el gemelo delante. El sumidero de hoy (LogOwnerNotice) solo deja
// traza en el log. NADIE PUEDE AFIRMAR QUE EL DUEÑO RECIBE EL RECORDATORIO: no lo
// recibe, y no lo recibirá hasta el Plan 045, que enchufa el emisor real sin tocar
// una línea de aquí.
//
// LO QUE NO HAY, Y NO ES UN OLVIDO: el evento de telemetría del vencimiento. Ni se
// emite, ni se declara, ni existe la transición a `expired` (status.go deja ese
// estado sin ningún origen a propósito: es legado del reloj que D-041.16 derogó).
//
// ⚠️ El NOMBRE de ese evento no se escribe en este fichero ni en ningún otro, y no
// es coquetería: el candado que lo vigila (inv_vencimiento_ast_test.go) barre el
// repo entero buscando el literal, y para no delatarse a sí mismo lo compone por
// concatenación. Escribirlo aquí «solo como comentario» pondría ese candado rojo —
// que es exactamente lo que se quiere, porque un literal en un comentario es el
// primer paso hacia un literal en un `emit`.
// ============================================================================

// QuoteDeadline es EL PLAZO: cuánto puede esperar un presupuesto en
// `pending_approval` antes de que la bandeja lo marque.
//
// Es una CONSTANTE DE PLATAFORMA y no un ajuste por tenant, y eso lo decidió
// D-044.50 §1 sobre tres candidatas:
//
//   - `tenant_settings.order_ttl_seconds` NO se reusa. Existe y se lee, pero el
//     COMMENT de su migración (0013) afirma que «NO se obedece: ningun codigo actua
//     sobre este valor» desde que D-041.16 lo derogó como causa de muerte.
//     Obedecerlo aquí convertiría en FALSA una afirmación que hoy es cierta y está
//     vigilada. 🔴 Si alguien encuentra código que actúe sobre esa columna, es un
//     defecto y no una evolución.
//   - Un ajuste nuevo (`quote_ttl_seconds`) tampoco: una migración más columna,
//     struct, defaults, lectura y tests para una v1 en la que ningún tenant ha
//     pedido afinarlo. Se paga el día que alguien lo pida.
//
// El día que se pida, esta constante se convierte en el ESPEJO del DEFAULT de la
// columna nueva sin deshacer nada — que es exactamente la forma que ya tiene
// DefaultDepositDueDays respecto de tenant_settings.deposit_due_days
// (notifier.go). ⚠️ Y por eso mismo, del gemelo deposit.go se copia la FORMA pero
// no esa mitad: la seña sí tiene ajuste por tenant, el plazo del presupuesto no lo
// tiene y no lo va a tener en esta ola.
const QuoteDeadline = 24 * time.Hour

// Overdue es LA MARCA DERIVADA: ¿este presupuesto lleva más del plazo esperando al
// dueño, a fecha `at`? Pura y sin efectos — no consulta, no escribe y no notifica.
//
// Se calcula al leer, en cada lectura, y por eso no hay ninguna columna
// `is_overdue` que pueda mentir. La consumen dos sitios que tienen que decir lo
// MISMO: la proyección al wire (publicapi, que es lo que pinta la bandeja) y el
// pre-filtro del recordatorio de aquí abajo. Un tercer sitio que reimplemente la
// regla es un defecto en cuanto los dos discrepen.
//
// `false` para todo lo que no esté en `pending_approval`: un pedido confirmado, uno
// con la seña pedida o uno cancelado no están esperando la decisión de nadie.
func Overdue(in Intake, at time.Time) bool {
	deadline, ok := quoteDeadlineOf(in)
	if !ok {
		return false
	}
	return !deadline.After(at)
}

// quoteDeadlineOf devuelve el instante a partir del cual la solicitud está
// vencida, y si la pregunta tiene sentido siquiera.
//
// LA BASE ES UpdatedAt, y la elección tiene consecuencias que hay que saber:
//
//   - Entrar en `pending_approval` la escribe (Store.UpdateStatus), así que para
//     una solicitud que nadie ha tocado desde entonces UpdatedAt ES «cuándo quedó
//     en manos del dueño», que es justo lo que el plazo mide.
//   - Corregir las líneas también la escribe, así que una corrección REINICIA el
//     plazo. Es deliberado: el dueño acaba de actuar sobre ese presupuesto y la
//     bandeja no tiene por qué gritarle que lo tiene abandonado.
//   - 🔴 De ahí que el compare-and-swap del recordatorio NO toque updated_at (ver
//     markExpiryRemindedQuery). Si lo tocara, marcar el recordatorio reiniciaría el
//     plazo que el propio recordatorio acaba de constatar, y la marca se apagaría
//     sola justo después de encenderse.
//
// SE DESCARTÓ la base más exacta —el created_at de la última revisión, o sea
// «cuándo se produjo este presupuesto»— porque no está disponible en el camino de
// la BANDEJA: Detail trae revisiones, Intake no, y solo Get las puebla. Con esa
// base, la lista y el detalle podrían marcar cosas distintas sobre la misma fila,
// que es peor que una base aproximada pero igual en los dos caminos.
//
// CreatedAt es el suplente para filas que llegan sin UpdatedAt (dobles de test,
// proyecciones parciales). Sin ninguna de las dos NO se marca: «no sé desde cuándo
// espera» se contesta callando, no marcando todo lo que tenga la fecha en cero —
// que es lo que haría una resta contra el tiempo cero.
func quoteDeadlineOf(in Intake) (time.Time, bool) {
	if NormalizeStatus(in.Status) != StatusPendingApproval {
		return time.Time{}, false
	}
	base := in.UpdatedAt
	if base.IsZero() {
		base = in.CreatedAt
	}
	if base.IsZero() {
		return time.Time{}, false
	}
	return base.Add(QuoteDeadline), true
}

// cutoffDelPlazo traduce el reloj del llamante al CORTE que la consulta SQL compara
// contra `updated_at`: «la fecha a partir de la cual una solicitud tocada lleva
// demasiado esperando».
//
// 🔴 EXISTE PARA QUE LA REGLA TENGA UN SOLO DUEÑO EN LOS DOS LADOS. El pre-filtro de
// Go pregunta hacia delante —`updated_at + plazo <= at`— y el WHERE del
// compare-and-swap pregunta hacia atrás —`updated_at <= at - plazo`—: son la misma
// desigualdad despejada de dos maneras, y por eso pueden divergir en el signo sin que
// nada se queje. Con el corte en una función con nombre, esa equivalencia se puede
// AFIRMAR en un test (ver vencimiento_sql_test.go) en vez de confiarla a que quien
// lea las dos expresiones haga la resta de cabeza.
//
// Un signo invertido aquí sería especialmente traicionero: `updated_at <= at + plazo`
// es cierto para casi cualquier fila, así que el CAS aceptaría todo — y en el camino
// normal NO SE VERÍA, porque el pre-filtro de RemindOverdue ya habría descartado lo
// que no toca. Solo se notaría en un llamante que fuera directo al store.
func cutoffDelPlazo(at time.Time) time.Time { return at.Add(-QuoteDeadline) }

// ExpiryStore es lo que el recordatorio del plazo necesita de la persistencia, y
// solo eso (ISP): no ve el listado, ni el export, ni las transiciones. Lo satisface
// *Postgres (producción) y *MemoryStore (tests).
//
// Un solo método, y por eso no tiene el hermano PendingDepositReminders de su
// gemelo: aquel existe porque el recordatorio de la SEÑA lo dispara también el
// mensaje entrante del cliente, que no trae ninguna solicitud consigo. Éste avisa
// al DUEÑO y solo lo disparan las lecturas del dueño, que siempre tienen las filas
// delante. Preguntarle a la BD por candidatas que ya se acaban de leer sería un
// viaje de más en el camino más caliente de la consola.
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
	MarkExpiryReminded(ctx context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error)
}

// OwnerNotice es EL EMISOR del recordatorio hacia el dueño, y es la pieza que HOY
// NO EXISTE de verdad (D-044.50 §2): el canal real es el push del Plan 045.
//
// Está declarado como puerto —y no resuelto con una llamada directa— justamente
// para que el Plan 045 sea una línea en el arranque y no una reapertura de este
// fichero. El sumidero de hoy es LogOwnerNotice, que solo deja traza.
//
// NO devuelve error, por lo mismo que StatusNotifier y DepositTouch: un aviso que
// no sale no puede convertir el listado del dueño en un 500.
type OwnerNotice interface {
	// RemindOwner avisa al dueño de que esta solicitud lleva más del plazo
	// esperando su decisión. Recibe la solicitud tal como la devolvió el
	// compare-and-swap: la única versión que se sabe vigente.
	RemindOwner(ctx context.Context, tenantID string, in Intake)
}

// ExpiryReminder evalúa y emite el recordatorio del plazo. Es seguro para uso
// concurrente (no guarda estado propio).
type ExpiryReminder struct {
	notice OwnerNotice
	store  ExpiryStore
	log    logger.Logger
	now    func() time.Time
}

// ExpiryOption configura el ExpiryReminder al construirlo. Es un tipo APARTE de
// ReminderOption (deposit.go) a propósito: son dos recordatorios con dos relojes y
// dos destinatarios, y un tipo común invitaría a pasarle a uno la opción del otro
// —que compilaría y no haría nada—.
type ExpiryOption func(*ExpiryReminder)

// WithExpiryClock inyecta el reloj con el que se decide si el plazo venció. Sin él,
// time.Now.
//
// Existe porque la regla ES una comparación de tiempos: sin reloj inyectable,
// probar «vencido y sin recordar ⇒ un recordatorio» exigiría esperar un día o
// mentirle a la fila. El reloj entra por UN sitio y se usa en LOS DOS extremos de
// la comparación (el `at` que decide el vencimiento y el que se escribe en
// expiry_reminded_at), así que un test no puede quedarse con medio tiempo falso.
func WithExpiryClock(now func() time.Time) ExpiryOption {
	return func(r *ExpiryReminder) {
		if now != nil {
			r.now = now
		}
	}
}

// NewExpiryReminder construye el recordatorio del plazo. Las tres dependencias son
// obligatorias: sin emisor no hay a quién avisar, sin store no hay forma de
// garantizar que se avisa UNA vez, y sin log un fallo desaparecería sin dejar
// rastro (ver usable, que se calla si falta alguna).
func NewExpiryReminder(notice OwnerNotice, store ExpiryStore, log logger.Logger, opts ...ExpiryOption) *ExpiryReminder {
	r := &ExpiryReminder{notice: notice, store: store, log: log, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RemindOverdue evalúa el recordatorio sobre solicitudes YA LEÍDAS: es el toque del
// dueño (listado y detalle), donde la fila viene de la misma consulta que la
// pantalla y no cuesta un viaje extra a la BD.
//
// No devuelve error, y es el mismo contrato que Remind y NotifyStatus: que un
// recordatorio no salga no puede convertir el listado del dueño en un 500.
//
// El PRE-FILTRO en memoria (Overdue) es lo que hace que este camino sea gratis en
// el 99,9 % de los toques: una bandeja sin presupuestos vencidos no ejecuta ni una
// sentencia. Lo que decide de verdad sigue siendo el compare-and-swap del store —el
// pre-filtro solo evita preguntar por lo que no puede ser—.
//
// ⚠️ El pre-filtro son DOS preguntas y no una: Overdue —la MISMA marca que pinta la
// bandeja, sin una segunda copia de la regla— y yaAvisado. La segunda es la que
// hace que una bandeja con veinte vencidos YA avisados siga sin ejecutar una sola
// sentencia; sin ella, cada vez que el dueño refrescara pagaría veinte UPDATE que
// no escriben nada. Que Overdue no la incluya es correcto y deliberado: la MARCA
// dice «este presupuesto lleva demasiado esperando» y eso no deja de ser verdad
// porque ya se haya avisado una vez.
func (r *ExpiryReminder) RemindOverdue(ctx context.Context, tenantID string, touched []Intake) {
	if !r.usable() {
		return
	}
	defer r.contenerPánico("recordatorio de plazo sobre solicitudes ya leídas")

	at := r.now()
	sent := 0
	for _, in := range touched {
		if !Overdue(in, at) || yaAvisado(in) {
			continue
		}
		if r.remindOwnerOnce(ctx, tenantID, in.ID, at) {
			sent++
		}
		if sent >= maxRemindersPerTouch {
			return
		}
	}
}

// yaAvisado es la otra mitad del pre-filtro: esta solicitud ya gastó su
// recordatorio. Zero = nunca se avisó, igual que las dos marcas de la seña.
func yaAvisado(in Intake) bool {
	return !in.ExpiryRemindedAt.IsZero()
}

// usable dice si el recordatorio puede operar. Uno a medias no avisa, pero tampoco
// rompe al que lo invocó (mismo criterio que NotifyStatus).
func (r *ExpiryReminder) usable() bool {
	return r != nil && r.store != nil && r.notice != nil && r.log != nil
}

// contenerPánico hace ESTRUCTURAL la promesa de que tocar una solicitud no puede
// reventar por culpa del recordatorio: sin esto, un pánico en el store o en el
// emisor subiría por la pila hasta el handler y convertiría el LISTADO del dueño en
// un 500 — un listado que ni siquiera pidió mandar avisos. Se registra entero; lo
// que se contiene es el alcance.
func (r *ExpiryReminder) contenerPánico(dónde string) {
	rec := recover()
	if rec == nil {
		return
	}
	r.log.Error("recordatorio de plazo: pánico contenido; el toque de la solicitud sigue su curso",
		"donde", dónde, "panic", rec)
}

// remindOwnerOnce intenta recordar UNA solicitud y dice si lo hizo. Los dos pasos
// van en ESTE orden y no son intercambiables:
//
//  1. GANAR la marca (compare-and-swap). A partir de aquí, este toque —y ningún
//     otro— es el que avisa por esta solicitud.
//  2. EMITIR, con la fila que devolvió el CAS: es la única versión que se sabe
//     vigente.
//
// Que la emisión falle después de marcar es el error ELEGIDO, y es el mismo que
// razona la cabecera de deposit.go: al revés —emitir y luego marcar— cualquier
// fallo de escritura se convierte en un segundo aviso, y en un tercero al siguiente
// toque. El pecado que no se puede cometer es el goteo.
//
// ⚠️ Aquí NO hay el paso previo «¿puede decirse algo?» que su gemelo pone antes de
// la marca (allí se lee la plantilla de seña del tenant). Este aviso no depende de
// ninguna config: no hay nada que gastar la marca en vano.
func (r *ExpiryReminder) remindOwnerOnce(ctx context.Context, tenantID, intakeID string, at time.Time) bool {
	log := r.log.With("intake_id", intakeID, "tenant_id", tenantID)

	marked, won, err := r.store.MarkExpiryReminded(ctx, tenantID, intakeID, at)
	if err != nil {
		log.Error("recordatorio de plazo: no se pudo marcar la solicitud; no se avisa", "error", err)
		return false
	}
	if !won {
		// Lo normal: ya se avisó, el plazo no venció, o el dueño ya decidió. No es
		// una avería y no merece más que un debug.
		log.Debug("recordatorio de plazo: no procedía (ya avisado, no vencido o ya decidido)")
		return false
	}

	r.notice.RemindOwner(ctx, tenantID, marked)
	return true
}

// ----------------------------------------------------------------------------
// El sumidero de HOY
// ----------------------------------------------------------------------------

// LogOwnerNotice es el emisor PROVISIONAL del recordatorio al dueño: deja traza en
// el log y nada más (D-044.50 §2). No es un stub de test — es lo que corre en
// producción hasta que el Plan 045 traiga el push real.
//
// 🔴 Con esto cableado, la marca expiry_reminded_at SÍ se escribe en la base: el
// recordatorio «ocurrió» a todos los efectos y no se repetirá. Es el precio
// aceptado a sabiendas en D-044.50 — una migración y una marca por un aviso que hoy
// no llega a ninguna persona—, y es también lo que hace que el día que exista el
// emisor real no haya que reconstruir la idempotencia.
//
// CERO PII: se registran el id de la solicitud, el del tenant y su antigüedad. NI
// el contacto ni el total, que aquí no aportan nada y son exactamente lo que no
// tiene por qué acabar en un fichero de log.
type LogOwnerNotice struct{ log logger.Logger }

// NewLogOwnerNotice construye el sumidero de traza sobre el log dado.
func NewLogOwnerNotice(log logger.Logger) *LogOwnerNotice {
	return &LogOwnerNotice{log: log}
}

// RemindOwner implementa OwnerNotice dejando la traza. El contexto no se usa: no
// hay a quién llamar todavía.
func (s *LogOwnerNotice) RemindOwner(_ context.Context, tenantID string, in Intake) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Info("recordatorio de plazo: el presupuesto lleva más del plazo esperando al dueño",
		"intake_id", in.ID,
		"tenant_id", tenantID,
		"plazo_horas", int64(QuoteDeadline/time.Hour),
		"emisor", "traza",
		"pendiente", "el canal real es el push del Plan 045; hoy nadie recibe esto")
}
