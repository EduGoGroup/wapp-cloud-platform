package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// Service es la capa de dominio de las solicitudes: aplica las reglas (paginación
// acotada, normalización de estados, máquina de estados) sobre un Store. No sabe
// de HTTP y no toma decisiones de transporte; el handler traduce sus errores a
// códigos.
type Service struct {
	store    Store
	notifier StatusNotifier
	deposits DepositTouch
	expiry   ExpiryTouch
	crm      CRMPusher
	quotes   QuoteSender
	// metricas es por donde la bandeja publica su telemetría (T5.2). OPCIONAL: nil es
	// «no se publica nada» y el dominio funciona entero.
	metricas PublicadorDeMetricas
	// log es dónde avisa lo BEST-EFFORT de este servicio cuando falla (hoy, la
	// telemetría). Nunca es nil: NewService pone logger.Default() y WithMetrics lo
	// sustituye por el del proceso.
	log logger.Logger
	// ahora es el reloj con el que se mide `elapsed_from_draft_ms`. Inyectable con
	// WithMetricsClock, que existe para los tests (ver metricas.go).
	ahora func() time.Time
	// revisions es el MISMO store, visto por su puerto de escritura de revisiones.
	// No es una dependencia aparte y no se cablea: sale de una aserción de tipo en
	// NewService (ver allí por qué no es un Option ni un método más de Store).
	revisions RevisionWriter
}

// StatusNotifier avisa al CLIENTE de que su solicitud cambió de estado (D-041.14).
// Lo satisface *Notifier.
//
// NO devuelve error, y eso es el contrato entero: un aviso que no sale no puede
// tumbar una transición que ya está escrita en la base. Si esta firma ganara un
// error, el primer llamante que lo propagara le devolvería un 500 al dueño por un
// pedido que SÍ cambió de estado — y el dueño reintentaría, chocaría con un 422 por
// estar ya en el destino, y acabaría creyendo que no se aplicó. Ver notifier.go.
type StatusNotifier interface {
	NotifyStatus(ctx context.Context, tenantID string, in Intake, from string)
}

// DepositTouch evalúa el recordatorio PEREZOSO de la seña sobre las solicitudes que
// una lectura acaba de tocar (D-041.12, T4.4). Lo satisface *DepositReminder.
//
// NO devuelve error, exactamente por lo mismo que StatusNotifier: si pudiera, el
// primer llamante que lo propagara convertiría el LISTADO del dueño en un 500 porque
// el teléfono de un cliente estaba apagado. Una lectura no puede fallar por un
// mensaje que no salió.
type DepositTouch interface {
	Remind(ctx context.Context, tenantID string, touched []Intake)
}

// ExpiryTouch evalúa el recordatorio PEREZOSO del PLAZO DEL PRESUPUESTO sobre las
// solicitudes que una lectura acaba de tocar (Plan 044 · T4.5, D-044.50). Lo
// satisface *ExpiryReminder.
//
// Es el HERMANO de DepositTouch y no una versión suya: aquél le habla al CLIENTE
// para recordarle una seña, éste le habla al DUEÑO para recordarle una decisión que
// no ha tomado. Dos destinatarios, dos marcas en la base y dos motivos —y por eso
// son dos puertos y no un método más del primero: un tenant puede tener cableado
// uno y no el otro, y de hecho hoy el segundo emite a un sumidero de traza.
//
// NO devuelve error, exactamente por lo mismo que sus dos hermanos.
type ExpiryTouch interface {
	RemindOverdue(ctx context.Context, tenantID string, touched []Intake)
}

// CRMPusher empuja al puente CRM del tenant la revisión de una solicitud que
// ACABA de escribirse (Plan 044 · Ola 4 · Tanda 2). Lo satisface
// *crmpush.RevisionPusher.
//
// 🔴 ES UN PUERTO LOCAL, Y NO UN IMPORT, POR UN CICLO REAL. La pieza que arma y
// encola el contrato vive en internal/integrations/crmpush, y ESE paquete importa
// a éste (necesita NormalizeStatus: el contrato wapp-crm-v1 jamás emite `closed`).
// Si el Service importara crmpush para llamarlo, el ciclo sería directo —
// intakes → crmpush → intakes— y no compilaría. Declarar aquí la forma que se
// necesita es exactamente lo que ya hacen StatusNotifier, DepositTouch,
// Destinations y SettingsReader: este paquete describe a sus colaboradores, no los
// importa.
//
// NO devuelve error, y el motivo es MÁS FUERTE que el de sus dos hermanos. Ahí el
// argumento es «una escritura aplicada no se deshace porque el teléfono del cliente
// esté apagado»; aquí, además, las escrituras que disparan el empuje NO SON
// IDEMPOTENTES: ReplaceItems escribe una revisión NUEVA en cada llamada. Un error
// propagado le devolvería un 500 al dueño por un encolado fallido, el dueño
// reintentaría, y el reintento escribiría la revisión N+1 — dos revisiones para una
// sola corrección. Perder un empuje es malo; fabricar una revisión de más por
// intentar recuperarlo es peor.
//
// ⚠️ LA CONSECUENCIA HAY QUE SABERLA: un encolado que falla NO se reintenta. La
// durabilidad de webhook_outbox empieza cuando la fila ENTRA; antes de eso no hay
// red. El fallo queda en el log con su intake_id.
type CRMPusher interface {
	PushRevision(ctx context.Context, tenantID string, d Detail, revisionNo int)
}

// QuoteSender es LA VOZ DEL DUEÑO hacia el cliente, por la misma sesión con la que el
// cliente armó el pedido: hoy la cotización al aprobar (Plan 044 · T4.3) y la pregunta
// al pedir información (T4.4). Lo satisface *Notifier, que es también quien satisface
// StatusNotifier: es una sola salida hacia WhatsApp con varios motivos, igual que el
// recordatorio de la seña reusa ese mismo notificador.
//
// POR QUÉ ES UN PUERTO APARTE Y NO DOS MÉTODOS MÁS EN StatusNotifier. Son dos
// contratos con dos dueños del texto: en aquél habla LA PLATAFORMA (el aviso genérico
// del estado destino) y en éste habla LA DUEÑA (su cotización, palabra por palabra).
// Fundirlos obligaría a todo implementador del aviso automático a saber componer una
// cotización, y borraría en el tipo la distinción que D-044.49 acaba de establecer.
//
// POR QUÉ DOS MÉTODOS Y NO UNO. Porque el texto se GUARDA antes de MANDARSE: la
// revisión `approved` tiene que llevar exactamente lo que sale por el cable
// (RenderedText), y eso solo se puede si componer y entregar son dos actos separables.
// Un único SendQuote(ownerText) compondría por dentro y el llamante nunca sabría qué
// se envió de verdad.
type QuoteSender interface {
	// QuoteText compone el mensaje ENTERO: el texto del dueño con la plantilla de
	// seña del tenant adjunta. Sin plantilla configurada devuelve el texto del dueño
	// tal cual — es la decisión de producto que ya gobierna el aviso de la seña
	// (notifier.go): un «te pedimos una seña» que no dice dónde pagarla es peor que
	// el silencio.
	//
	// No devuelve error: un fallo leyendo la config del tenant acaba en la cotización
	// sola, que es una respuesta completa, y queda en el log de quien lo intentó.
	QuoteText(ctx context.Context, tenantID string, in Intake, ownerText string) string
	// SendQuote entrega ese texto por la sesión de la solicitud. NO devuelve error,
	// por lo mismo que StatusNotifier: un mensaje que no sale no puede tumbar una
	// aprobación que ya está escrita en la base.
	SendQuote(ctx context.Context, tenantID string, in Intake, text string)
	// SendQuestion entrega la PREGUNTA que escribió el dueño al pedir más
	// información (T4.4). Tampoco devuelve error, y por lo mismo.
	//
	// POR QUÉ AQUÍ Y NO EN UN PUERTO NUEVO. Es la misma salida, el mismo dueño del
	// texto y las mismas reglas; lo único que cambia es el motivo. Un puerto aparte
	// sería un segundo cableado que alguien puede olvidar en el arranque, con el
	// efecto de que pedir información dejaría de preguntar sin que nada lo dijera.
	//
	// POR QUÉ NO REUSA SendQuote TAL CUAL, que era lo tentador: aquél ADJUNTA la
	// plantilla de seña del tenant vía QuoteText y se registra en el log como
	// `accion=approve`. Adjuntarle instrucciones de pago a una pregunta sería
	// pedirle la seña a quien todavía no sabe qué va a costar, y un log que dice
	// «approve» sobre una petición de información es la misma clase de defecto que
	// un mensaje que afirma un estado que no es. Lo COMÚN —la vía custodiada de PII,
	// el Ack, el cero PII en los logs— sí se reusa entero: las dos entregas bajan al
	// mismo `deliver` (notifier.go).
	SendQuestion(ctx context.Context, tenantID string, in Intake, question string)
}

// Option configura el Service al construirlo.
type Option func(*Service)

// WithNotifier cablea el aviso al cliente de cada transición aplicada (T4.2). Sin
// esta opción el servicio funciona igual y NO manda nada: es lo que hace que un
// test de dominio no le haga sonar el teléfono a nadie por accidente.
func WithNotifier(n StatusNotifier) Option {
	return func(s *Service) { s.notifier = n }
}

// WithDepositReminder cablea el recordatorio PEREZOSO de la seña a las LECTURAS del
// dueño (T4.4). Sin esta opción, List y Get son exactamente las lecturas puras que
// eran —ni una sentencia de más, ni un mensaje—, que es lo que mantiene honestos
// tanto los tests de dominio como cualquier consumidor que solo quiera leer.
//
// POR QUÉ AQUÍ Y NO EN EL HANDLER. «Tocar la solicitud dispara el recordatorio» es
// una regla del DOMINIO de las solicitudes, no del transporte: colgarla del handler
// HTTP la dejaría fuera de cualquier otro lector (el puente del Plan 042, una
// pantalla futura, un caso de uso interno) y la haría depender de que cada uno se
// acuerde de copiar la llamada. Colgada de la lectura, viaja con ella.
//
// El precio —una lectura deja de ser pura— se paga con la MISMA moneda que ya paga
// SetStatus con su notificador: colaborador opcional, no puede devolver error, no
// puede tumbar al llamante, y sin cablear no existe.
func WithDepositReminder(d DepositTouch) Option {
	return func(s *Service) { s.deposits = d }
}

// WithExpiryReminder cablea el recordatorio PEREZOSO del plazo del presupuesto a
// las LECTURAS del dueño (Plan 044 · T4.5, D-044.50 §2). Sin esta opción, List y Get
// no evalúan ningún plazo y no avisan a nadie.
//
// 🔴 NO SUSTITUYE NI DEPENDE DE WithDepositReminder, y esa independencia es la mitad
// del cableado: son dos colaboradores con dos marcas, dos destinatarios y dos
// motivos. Cablear uno solo tiene que funcionar —lo comprueba un test— porque
// mientras el emisor real del aviso al dueño no exista (Plan 045) es perfectamente
// posible que un despliegue lleve uno y no el otro. Ver Service.touch: su guarda
// pregunta por cada uno POR SEPARADO justamente por esto.
//
// El precio es el mismo que ya pagan sus hermanas: colaborador opcional, no puede
// devolver error, no puede tumbar al llamante, y sin cablear no existe.
//
// ⚠️ LO QUE ESTA OPCIÓN NO HACE, aunque su nombre lo sugiera: no marca nada como
// vencido y no cambia el estado de ninguna solicitud. La MARCA «vencido» es
// derivada, se calcula al leer (Overdue) y no necesita cableado ninguno; esto solo
// enciende el RECORDATORIO.
func WithExpiryReminder(e ExpiryTouch) Option {
	return func(s *Service) { s.expiry = e }
}

// WithCRMPusher cablea el empuje al puente CRM de las revisiones que el DUEÑO
// escribe desde su consola (Plan 044 · Ola 4 · Tanda 2). Sin esta opción el
// servicio funciona igual y no encola nada — lo mismo que promete WithNotifier
// para el teléfono del cliente.
//
// POR QUÉ AQUÍ Y NO EN EL HANDLER, que es la parte que decide el diseño. Hasta esta
// tarea el ÚNICO productor de `intake.push` era el WebhookSink del motor de flujos,
// que solo reacciona al cierre del carrito: cualquier ruta nueva que moviera una
// solicitud tenía que ACORDARSE de encolar por su cuenta, y «acordarse en cada
// sitio» es exactamente cómo nacen los defectos que este plan lleva dos olas
// pagando. Colgado del Service, el empuje viaja con la escritura y una puerta nueva
// lo hereda sin copiar una línea.
//
// El precio es el mismo que ya paga SetStatus con su notificador: colaborador
// opcional, no puede devolver error, no puede tumbar al llamante, y sin cablear no
// existe.
func WithCRMPusher(p CRMPusher) Option {
	return func(s *Service) { s.crm = p }
}

// WithQuoteSender cablea la salida por la que el DUEÑO le responde al cliente al
// aprobar un presupuesto (Plan 044 · T4.3). Se le pasa el MISMO *Notifier que
// WithNotifier: dos objetos serían dos criterios sobre la misma plantilla de seña y
// dos caminos distintos hacia el mismo teléfono.
//
// A diferencia de sus tres hermanas, su ausencia NO es un silencio: Service.Approve y
// Service.RequestInfo devuelven ErrNoQuoteSender antes de tocar nada. Aprobar es
// «aprobar y responder» y pedir información es «preguntar»: un servicio que no puede
// hablar no puede hacer ninguna de las dos (ver approve.go y requestinfo.go).
func WithQuoteSender(q QuoteSender) Option {
	return func(s *Service) { s.quotes = q }
}

// NewService construye el servicio sobre el store dado.
//
// El puerto de ESCRITURA de revisiones sale del propio store por aserción de tipo, y
// conviene saber por qué no es ninguna de las otras dos opciones que había:
//
//   - No es un Option (WithRevisionWriter): habría que pasarle en el arranque el
//     MISMO objeto que ya se pasa como store, y un cableado que se puede olvidar es
//     un cableado que se olvida — con el efecto de que aprobar dejaría de escribir su
//     rastro sin que nada lo dijera.
//   - No se mete InsertRevision en el puerto Store: ese puerto es la BANDEJA del
//     dueño, y RevisionWriter está separado a propósito para que los PRODUCTORES de
//     revisiones (el proyector del carrito, el pipeline del 044) no lo reciban entero
//     (ver revisions.go).
//
// Un store que no sepa escribir revisiones deja el campo en nil y Approve corta con
// ErrNoRevisionWriter en vez de aprobar sin rastro. Los dos stores reales —*Postgres
// y *MemoryStore— lo satisfacen.
func NewService(store Store, opts ...Option) *Service {
	// El log y el reloj nacen con un default utilizable y NO se exigen por parámetro:
	// son de lo BEST-EFFORT (la telemetría de T5.2), y un servicio sin ellos tendría
	// que ramificar por nil en cada aviso. El mismo criterio que ya usan *Postgres y
	// *MemoryStore con su logger.Default().
	s := &Service{store: store, log: logger.Default(), ahora: time.Now}
	if w, ok := store.(RevisionWriter); ok {
		s.revisions = w
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// List devuelve la página de solicitudes del tenant que casan con el filtro, con
// el total de coincidencias sin paginar. Sanea la paginación (Filter.Normalized)
// antes de consultar: el llamante no puede pedir 100k filas de un golpe.
//
// Es además uno de los tres TOQUES que evalúan el recordatorio de la seña (D-041.12,
// T4.4) y uno de los DOS que evalúan el del plazo del presupuesto (T4.5): el dueño
// abriendo su bandeja es lo que hace de reloj, porque en esta plataforma no hay
// ninguno (ADR-0003). El toque va DESPUÉS de tener la página —solo se evalúa lo que
// de verdad se leyó— y no puede alterar lo que se devuelve.
func (s *Service) List(ctx context.Context, tenantID string, f Filter) (Page, error) {
	f = f.Normalized()
	items, total, err := s.store.List(ctx, tenantID, f)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Intake{} // la UI itera sin ramificar por el nulo
	}
	s.touch(ctx, tenantID, items)
	return Page{Intakes: items, Page: f.Page, PageSize: f.PageSize, Total: total}, nil
}

// ListDetails devuelve TODAS las solicitudes del filtro con sus líneas, sin
// paginar: es lo que consumen el export y el summary. La cota de paginación no
// aplica aquí (una hoja de cálculo partida en páginas no sirve), así que la pone
// MaxExportIntakes.
//
// Se pide UNA solicitud MÁS que la cota justamente para poder distinguir "cabe
// justo" de "se pasa": si el store devolviera exactamente la cota no habría forma
// de saber si sobraban filas, y el export saldría recortado sin avisar.
func (s *Service) ListDetails(ctx context.Context, tenantID string, f Filter) ([]Detail, error) {
	details, err := s.store.ListDetails(ctx, tenantID, f, MaxExportIntakes+1)
	if err != nil {
		return nil, err
	}
	if len(details) > MaxExportIntakes {
		return nil, ErrTooLarge
	}
	return details, nil
}

// Summary agrega las solicitudes del filtro (totales, desglose por estado, ranking
// de artículos y el detalle crudo). Lee por el MISMO camino que el export —misma
// cota incluida— y delega la aritmética en BuildSummary, que es pura.
func (s *Service) Summary(ctx context.Context, tenantID string, f Filter) (Summary, error) {
	details, err := s.ListDetails(ctx, tenantID, f)
	if err != nil {
		return Summary{}, err
	}
	return BuildSummary(details, f.Normalized(), time.Now()), nil
}

// Get devuelve la solicitud con sus líneas. ErrNotFound si no es del tenant (404
// opaco, INV-8).
//
// Es el segundo TOQUE del recordatorio de la seña (ver List): abrir la solicitud
// concreta es la lectura más específica que existe y, si esa es justo la que tiene
// la seña vencida, es donde antes se nota.
func (s *Service) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	detail, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Detail{}, err
	}
	s.touch(ctx, tenantID, []Intake{detail.Intake})
	return detail, nil
}

// touch evalúa los recordatorios PEREZOSOS sobre lo que una lectura acaba de leer.
// Sin ninguna opción cableada (el default, y lo que usan todos los tests de dominio)
// no hace nada: la lectura sigue siendo tan pura como antes de T4.4.
//
// NO se llama desde ListDetails ni Summary a propósito, aunque también leen
// solicitudes: son el EXPORT y el resumen, caminos de datos masivos que un dueño
// dispara para llevarse una hoja de cálculo. Que descargar un CSV le mande WhatsApps
// a sus clientes sería una sorpresa desagradable, y encima sin cota útil (ahí no hay
// página: son hasta MaxExportIntakes solicitudes).
//
// 🔴 SON DOS COLABORADORES INDEPENDIENTES, y la guarda tiene que preguntarlo dos
// veces. Hasta T4.5 esto era `if s.deposits == nil || len(touched) == 0 { return }`,
// y ese `return` habría dejado el recordatorio del plazo MUDO Y EN VERDE en
// cualquier despliegue sin el recordatorio de la seña: el colaborador nuevo ni
// siquiera llegaba a mirar. Lo único COMPARTIDO es el corte por lista vacía, que no
// es de nadie: sin filas leídas no hay nada que evaluar.
//
// El ORDEN entre los dos no significa nada y no debe significarlo: hablan con
// personas distintas (el cliente y el dueño), escriben marcas distintas y ninguno
// puede ver lo que hizo el otro.
func (s *Service) touch(ctx context.Context, tenantID string, touched []Intake) {
	if len(touched) == 0 {
		return
	}
	if s.deposits != nil {
		s.deposits.Remind(ctx, tenantID, touched)
	}
	if s.expiry != nil {
		s.expiry.RemindOverdue(ctx, tenantID, touched)
	}
}

// SetStatus aplica una transición del ciclo de vida y devuelve la solicitud ya
// transicionada. El orden importa:
//
//  1. lee el estado actual (ErrNotFound si la solicitud no es del tenant): el
//     recurso se resuelve ANTES que el cuerpo, para no revelar por el código de
//     error si una solicitud ajena existe;
//  2. valida la transición contra la máquina de estados (*TransitionError con el
//     estado actual y los destinos permitidos, que el handler publica en el 422).
//     Un destino DESCONOCIDO cae por aquí sin caso aparte: no está en el mapa, así
//     que no es alcanzable desde ningún origen, y el llamante recibe la misma
//     respuesta útil — dónde está y adónde puede ir;
//  3. escribe con compare-and-swap sobre el estado leído (ErrConflict si otro
//     operador se adelantó entre 1 y 3).
//
// La LÍNEA DE ENVÍO de `pending_approval` (D-041.11) no se ve en este método a
// propósito: no es un efecto colateral que se dispare después, sino parte de la
// escritura del estado, y por eso vive dentro de la misma transacción del store
// (ver Store.UpdateStatus). La Intake que se devuelve ya trae el total con ella.
//
// El AVISO AL CLIENTE (D-041.14, T4.2) es el paso 4 y va DESPUÉS de la escritura,
// nunca antes ni en paralelo: solo se le cuenta a alguien lo que ya es verdad en la
// base. No puede fallar hacia arriba —NotifyStatus no devuelve error— porque una
// transición aplicada no se deshace porque el teléfono del cliente esté apagado.
//
// `notice` dice QUIÉN da ese aviso (ver StatusNotice en notifier.go). El
// <select> de estado de la consola pasa NoticeToClient y se comporta exactamente
// como antes; una puerta que ya le escribe al cliente con su propio texto pasa
// NoticeByCaller y la plataforma se calla en esa transición. La transición se
// aplica IGUAL en los dos casos: callarse no es no registrar.
func (s *Service) SetStatus(ctx context.Context, tenantID, intakeID, to string, notice StatusNotice) (Intake, error) {
	to = NormalizeStatus(to)

	current, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Intake{}, err
	}
	from := NormalizeStatus(current.Status)
	if !CanTransition(from, to) {
		return Intake{}, &TransitionError{From: from, To: to, Allowed: AllowedTransitions(from)}
	}

	updated, err := s.store.UpdateStatus(ctx, tenantID, intakeID, to, StoredVariants(from))
	if err != nil {
		return Intake{}, err
	}
	s.notify(ctx, tenantID, updated, from, notice)
	return updated, nil
}

// AbandonByEvent deja en `abandoned` la solicitud que colgaba del evento `eventID`
// (D-043.21, Ola 4.5 · T4.5.5(a)). Es la puerta que el motor consume por el puerto
// IntakeAbandoner desde que la FK se invirtió: el runtime cancela un evento y pide
// abandonar SU contenido sin conocer ningún id de hijo — la columna
// conversation_events.intake_id de la que dependía el viejo Abandon(intakeID)
// murió en la 0054, y con ella se retiró ese método (Ola 4.5, cableado final).
//
// La idempotencia que aquel exigía vive ahora en el SQL: 0 filas —ya
// abandonada, ya resuelta, o un evento sin contenido (menu/survey)— es ÉXITO, y el
// guard `status='open'` del store garantiza que una `confirmed` jamás se abandona
// por aquí. NO notifica al cliente a propósito, como ninguna de las puertas del
// abandono por cancelación lo hacía de forma efectiva: el aviso de NotifyStatus
// cuelga de las transiciones del OPERADOR (SetStatus), y la muerte del evento ya se
// la contó al cliente el propio flujo.
//
// ⚠️ LEGADO REGISTRADO: una solicitud pre-0054 (event_id NULL) es INALCANZABLE por
// esta puerta — no declara padre, así que ningún eventID la encuentra. Ese hueco es
// el mismo que ya tenía: nadie podía llegar a ella desde un evento cuando la
// ligadura vivía (sin escribirse) en el padre.
func (s *Service) AbandonByEvent(ctx context.Context, tenantID, eventID string) error {
	return s.store.AbandonByEvent(ctx, tenantID, eventID)
}

// notify dispara el aviso al cliente de UNA transición efectivamente aplicada.
//
// Es el punto donde se sostiene «como mucho un mensaje por transición», y lo hace
// colgando el aviso de la ESCRITURA y no de la petición:
//
//   - la transición ya pasó por CanTransition, que rechaza from == to: pedir dos
//     veces `confirmed` sobre algo ya confirmado no llega hasta aquí;
//   - UpdateStatus es un compare-and-swap sobre el estado leído, así que de dos
//     operadores que piden lo mismo a la vez solo UNO escribe; el otro se lleva
//     ErrConflict y sale por el `return` de arriba sin avisar a nadie;
//   - y si aun así el store devolviera algo que no es el destino, la guarda de
//     abajo calla. Es defensa barata contra un store futuro que "arregle" una
//     transición imposible devolviendo el estado actual: al cliente le llegaría un
//     WhatsApp que no corresponde a ningún cambio.
//
// El CUARTO motivo para callar lo trae el llamante (D-044.49): con NoticeByCaller
// la transición se aplica y el aviso genérico no sale, porque quien la pidió ya le
// escribió al cliente con su propio texto. Va PRIMERO —antes del notificador y
// antes de la guarda del destino— porque es una decisión de producto y no una
// defensa: si el llamante habla, aquí no hay nada que evaluar.
func (s *Service) notify(ctx context.Context, tenantID string, updated Intake, from string, notice StatusNotice) {
	if notice.silencia() {
		return
	}
	if s.notifier == nil {
		return
	}
	if NormalizeStatus(updated.Status) == from {
		return
	}
	s.notifier.NotifyStatus(ctx, tenantID, updated, from)
}

// PushRevisionToCRM encola para el puente CRM del tenant la revisión `revisionNo`
// de la solicitud `d`, con su ciclo de vida REAL (Plan 044 · Ola 4 · Tanda 2).
//
// La llama la escritura que acaba de parir esa revisión —dentro de este Service, no
// desde el handler—: es el mismo criterio que notify, que cuelga del compare-and-swap
// ganador y no de la petición. Sin CRMPusher cableado no hace nada.
//
// 🔴 EL `revisionNo` ES OBLIGATORIO Y EXPLÍCITO, Y AHÍ ESTÁ TODO EL DISEÑO. El
// puente hace UPSERT por (intake_id, revision_no) y trata como DUPLICADO todo par
// repetido (manual del integrador §4), así que el número no es un adorno del
// documento: es lo que distingue «esto es un estado nuevo» de «esto ya lo sabía».
// Se pide por parámetro en vez de deducirlo de d.Revisions porque solo el llamante
// sabe cuál de ellas acaba de escribir, y porque un empuje sin revisión NUEVA es un
// empuje que el puente descarta en silencio.
//
// ⚠️ CONSECUENCIA QUE NO SE TAPA: webhook_outbox NO tiene unicidad por
// (intake_id, revision_no) —la 0046 deja la idempotencia al receptor, y su COMMENT
// lo dice—, así que llamar dos veces con el MISMO número encola dos filas y el
// puente recibe dos entregas. Lo que impide que eso pase no es la base: es colgar
// esta llamada de la escritura que numeró la revisión, una vez por revisión.
func (s *Service) PushRevisionToCRM(ctx context.Context, tenantID string, d Detail, revisionNo int) {
	if s.crm == nil {
		return
	}
	s.crm.PushRevision(ctx, tenantID, d, revisionNo)
}

// EnsureShippingLine garantiza la línea estándar de envío de una solicitud
// (D-041.11) y deja su total cuadrado con las líneas. Es idempotente.
//
// Existe suelta —y no solo dentro de SetStatus— porque hay un segundo momento en
// que la línea tiene que aparecer y no hay transición de por medio: el cierre del
// carrito, que va directo a `confirmed`. Ese llamante usa ShippingOnlyIfZones; el
// del presupuesto, ShippingAlways.
func (s *Service) EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	return s.store.EnsureShippingLine(ctx, tenantID, intakeID, policy)
}
