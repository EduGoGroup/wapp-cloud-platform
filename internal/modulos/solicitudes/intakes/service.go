// Porta internal/intakes/service.go @ 64c181a

// service.go — LA BANDEJA DEL DUEÑO: el `Service`, sus puertos y sus opciones.
//
// Es la capa de dominio de las solicitudes y la INSTANCIA ÚNICA que cablea el
// arranque: la consumen la cara HTTP, el motor de flujos, el pipeline y el puente
// CRM. No sabe de HTTP y no toma decisiones de transporte; quien la llama traduce
// sus errores a códigos.
//
// Las cuatro reglas del cableado que este contrato sostiene (`diseno.md` §4):
//
//   - R-02: sin `WithQuoteSender`, `Approve` y `RequestInfo` cortan con
//     ErrNoQuoteSender ANTES de tocar nada. Es la única opción cuya ausencia es un
//     error y no un silencio.
//   - R-03: el `CRMPusher` NO empuja nada por sí solo. Solo encola quien llama a
//     `PushRevisionToCRM` (o a su gemelo por id): `SetStatus`, `List`, `Get` y
//     `AbandonByEvent` jamás empujan. El productor de `intake.push` del pipeline
//     normal es el sink del carrito, que no pasa por aquí.
//   - R-04: sin `WithMetrics` el dominio funciona entero y NO publica nada. No es
//     un error.
//   - R-05: los dos recordatorios perezosos son INDEPENDIENTES: una lectura
//     pregunta por cada uno por separado, y cablear uno solo tiene que funcionar.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// Service es la capa de dominio de las solicitudes: aplica las reglas (paginación
// acotada, normalización de estados, máquina de estados) sobre un Store. Se
// construye con NewService y es seguro compartirlo: no guarda estado propio entre
// llamadas, solo sus colaboradores.
type Service struct {
	store    Store
	notifier StatusNotifier
	deposits DepositTouch
	expiry   ExpiryTouch
	crm      CRMPusher
	quotes   QuoteSender
	// metrics es por donde la bandeja publica su telemetría. OPCIONAL: nil es «no se
	// publica nada» y el dominio funciona entero. Era `metricas` en el viejo.
	metrics MetricsPublisher
	// log es dónde avisa lo BEST-EFFORT de este servicio cuando falla (hoy, la
	// telemetría). Nunca es nil: NewService pone logger.Default() y WithMetrics lo
	// sustituye por el del proceso.
	log logger.Logger
	// metricsNow es el reloj con el que se mide `elapsed_from_draft_ms`. Inyectable
	// con WithMetricsClock, que existe para los tests (ver service_metrics.go). Era
	// `ahora` en el viejo.
	metricsNow func() time.Time
	// now es el reloj con el que Summary fecha su resultado (D-F6-5). Inyectable con
	// WithClock. Es OTRO que metricsNow y ninguno mueve al otro. No existía en el
	// viejo, que llamaba a time.Now() directo.
	now func() time.Time
	// revisions es el MISMO store, visto por su puerto de escritura de revisiones.
	// No es una dependencia aparte y no se cablea: sale de una aserción de tipo en
	// NewService (ver allí por qué no es un Option ni un método más de Store).
	revisions RevisionWriter
}

// StatusNotifier avisa al CLIENTE de que su solicitud cambió de estado (D-041.14).
// Lo satisface *Notifier.
//
// `in` es la solicitud YA transicionada y `from` el estado canónico del que salió.
//
// NO devuelve error, y eso es el contrato entero: un aviso que no sale no puede
// tumbar una transición que ya está escrita en la base. Si esta firma ganara un
// error, el primer llamante que lo propagara le devolvería un 500 al dueño por un
// pedido que SÍ cambió de estado — y el dueño reintentaría, chocaría con un 422 por
// estar ya en el destino, y acabaría creyendo que no se aplicó.
type StatusNotifier interface {
	NotifyStatus(ctx context.Context, tenantID string, in Intake, from string)
}

// DepositTouch evalúa el recordatorio PEREZOSO de la seña sobre las solicitudes que
// una lectura acaba de tocar (D-041.12). Lo satisface *DepositReminder.
//
// NO devuelve error, exactamente por lo mismo que StatusNotifier: si pudiera, el
// primer llamante que lo propagara convertiría el LISTADO del dueño en un 500 porque
// el teléfono de un cliente estaba apagado. Una lectura no puede fallar por un
// mensaje que no salió.
type DepositTouch interface {
	Remind(ctx context.Context, tenantID string, touched []Intake)
}

// ExpiryTouch evalúa el recordatorio PEREZOSO del PLAZO DEL PRESUPUESTO sobre las
// solicitudes que una lectura acaba de tocar (D-044.50). Lo satisface
// *ExpiryReminder.
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
// ACABA de escribirse. Lo satisface el empujador de revisiones de `crmpush`.
//
// 🔴 ES UN PUERTO LOCAL, Y NO UN IMPORT, POR UN CICLO REAL. La pieza que arma y
// encola el contrato vive en `integrations/crmpush`, y ESE paquete importa a éste
// (necesita NormalizeStatus: el contrato wapp-crm-v1 jamás emite `closed`). Declarar
// aquí la forma que se necesita es lo que ya hacen StatusNotifier y DepositTouch:
// este paquete describe a sus colaboradores, no los importa.
//
// NO devuelve error, y el motivo es MÁS FUERTE que el de sus hermanos: las
// escrituras que disparan el empuje NO SON IDEMPOTENTES (ReplaceItems escribe una
// revisión NUEVA en cada llamada). Un error propagado le devolvería un 500 al dueño
// por un encolado fallido, el dueño reintentaría, y el reintento escribiría la
// revisión N+1 — dos revisiones para una sola corrección.
//
// ⚠️ LA CONSECUENCIA HAY QUE SABERLA: un encolado que falla NO se reintenta. La
// durabilidad de webhook_outbox empieza cuando la fila ENTRA; antes de eso no hay
// red. El fallo queda en el log con su intake_id.
//
// R-12: quien implementa NUNCA sustituye `revisionNo` ni el estado de `d` por una
// constante: el puente hace UPSERT por (intake_id, revision_no), y un `1` fijo lo
// dejaba en el primer estado para siempre.
type CRMPusher interface {
	PushRevision(ctx context.Context, tenantID string, d Detail, revisionNo int)
}

// QuoteSender es LA VOZ DEL DUEÑO hacia el cliente, por la misma sesión con la que
// el cliente armó el pedido: la cotización al aprobar y la pregunta al pedir
// información. Lo satisface *Notifier, que es también quien satisface
// StatusNotifier: una sola salida hacia WhatsApp con varios motivos.
//
// POR QUÉ ES UN PUERTO APARTE Y NO DOS MÉTODOS MÁS EN StatusNotifier. Son dos
// contratos con dos dueños del texto: en aquél habla LA PLATAFORMA (el aviso
// genérico del estado destino) y en éste habla LA DUEÑA (su texto, palabra por
// palabra). Fundirlos obligaría a todo implementador del aviso automático a saber
// componer una cotización, y borraría en el tipo la distinción de D-044.49.
//
// POR QUÉ COMPONER Y ENTREGAR SON DOS MÉTODOS. Porque el texto se GUARDA antes de
// MANDARSE: la revisión `approved` tiene que llevar exactamente lo que sale por el
// cable (RenderedText). Un único SendQuote(ownerText) compondría por dentro y el
// llamante nunca sabría qué se envió de verdad.
type QuoteSender interface {
	// QuoteText compone el mensaje ENTERO: el texto del dueño con la plantilla de
	// seña del tenant adjunta. Sin plantilla configurada devuelve el texto del dueño
	// tal cual: un «te pedimos una seña» que no dice dónde pagarla es peor que el
	// silencio.
	//
	// No devuelve error: un fallo leyendo la config del tenant acaba en la cotización
	// sola, que es una respuesta completa, y queda en el log de quien lo intentó.
	QuoteText(ctx context.Context, tenantID string, in Intake, ownerText string) string
	// SendQuote entrega ese texto por la sesión de la solicitud. NO devuelve error,
	// por lo mismo que StatusNotifier: un mensaje que no sale no puede tumbar una
	// aprobación que ya está escrita en la base.
	SendQuote(ctx context.Context, tenantID string, in Intake, text string)
	// SendQuestion entrega la PREGUNTA que escribió el dueño al pedir más
	// información. Tampoco devuelve error, y por lo mismo.
	//
	// POR QUÉ AQUÍ Y NO EN UN PUERTO NUEVO: es la misma salida, el mismo dueño del
	// texto y las mismas reglas. Un puerto aparte sería un segundo cableado que
	// alguien puede olvidar en el arranque.
	//
	// POR QUÉ NO REUSA SendQuote: aquél entrega lo que QuoteText compuso CON la
	// plantilla de seña. Adjuntarle instrucciones de pago a una pregunta sería
	// pedirle la seña a quien todavía no sabe qué va a costar. La pregunta sale tal
	// como la escribió el dueño, sin pasar por QuoteText.
	SendQuestion(ctx context.Context, tenantID string, in Intake, question string)
}

// Option configura el Service al construirlo. Las opciones se aplican en el orden
// en que se pasan a NewService: si dos tocan lo mismo, gana la última.
type Option func(*Service)

// WithNotifier cablea el aviso al cliente de cada transición aplicada por
// SetStatus. Sin esta opción el servicio funciona igual y NO manda nada: es lo que
// hace que un test de dominio no le haga sonar el teléfono a nadie por accidente.
func WithNotifier(n StatusNotifier) Option {
	return func(s *Service) { s.notifier = n }
}

// WithDepositReminder cablea el recordatorio PEREZOSO de la seña a las LECTURAS del
// dueño (List y Get). Sin esta opción, esas lecturas son puras: ni una sentencia de
// más, ni un mensaje.
//
// POR QUÉ AQUÍ Y NO EN QUIEN LLAMA. «Tocar la solicitud dispara el recordatorio» es
// una regla del DOMINIO, no del transporte: colgada del handler HTTP quedaría fuera
// de cualquier otro lector y dependería de que cada uno se acuerde de copiar la
// llamada. Colgada de la lectura, viaja con ella.
//
// El precio —una lectura deja de ser pura— se paga con la misma moneda que
// SetStatus con su notificador: colaborador opcional, no puede devolver error, no
// puede tumbar al llamante, y sin cablear no existe.
func WithDepositReminder(d DepositTouch) Option {
	return func(s *Service) { s.deposits = d }
}

// WithExpiryReminder cablea el recordatorio PEREZOSO del plazo del presupuesto a las
// LECTURAS del dueño (D-044.50 §2). Sin esta opción, List y Get no evalúan ningún
// plazo y no avisan a nadie.
//
// 🔴 R-05 — NO SUSTITUYE NI DEPENDE DE WithDepositReminder. Son dos colaboradores
// con dos marcas, dos destinatarios y dos motivos, y cablear UNO SOLO tiene que
// funcionar: mientras el emisor real del aviso al dueño no exista es perfectamente
// posible que un despliegue lleve uno y no el otro. La guarda de la lectura
// pregunta por cada uno POR SEPARADO; una guarda que saliera al faltar el de la
// seña dejaría éste MUDO Y EN VERDE.
//
// ⚠️ LO QUE ESTA OPCIÓN NO HACE, aunque su nombre lo sugiera: no marca nada como
// vencido y no cambia el estado de ninguna solicitud. La marca «vencido» es
// derivada, se calcula al leer (Overdue) y no necesita cableado; esto solo enciende
// el RECORDATORIO.
func WithExpiryReminder(e ExpiryTouch) Option {
	return func(s *Service) { s.expiry = e }
}

// WithCRMPusher cablea el empuje al puente CRM de las revisiones que se escriben
// por este Service. Sin esta opción el servicio funciona igual y no encola nada.
//
// R-03: cablearlo NO hace que el Service empuje por su cuenta. Solo empujan las
// escrituras que paren una revisión y llaman a PushRevisionToCRM —Approve y la
// corrección de líneas— y el gemelo por id que usa el pipeline (PushRevisionByID).
//
// POR QUÉ AQUÍ Y NO EN QUIEN LLAMA. Colgado del Service, el empuje viaja con la
// escritura y una puerta nueva lo hereda sin copiar una línea; «acordarse en cada
// sitio» es exactamente cómo nacen los empujes olvidados.
func WithCRMPusher(p CRMPusher) Option {
	return func(s *Service) { s.crm = p }
}

// WithQuoteSender cablea la salida por la que el DUEÑO le responde al cliente. Se le
// pasa el MISMO *Notifier que a WithNotifier: dos objetos serían dos criterios sobre
// la misma plantilla de seña y dos caminos hacia el mismo teléfono.
//
// R-02: a diferencia de sus hermanas, su ausencia NO es un silencio. Approve y
// RequestInfo devuelven ErrNoQuoteSender antes de tocar nada: aprobar es «aprobar y
// responder» y pedir información es «preguntar»; un servicio que no puede hablar no
// puede hacer ninguna de las dos.
func WithQuoteSender(q QuoteSender) Option {
	return func(s *Service) { s.quotes = q }
}

// WithClock sustituye el reloj con el que Summary fecha su resultado
// (Summary.GeneratedAt). El defecto es time.Now. Pasar nil no hace nada: el
// servicio no se queda sin reloj.
//
// Es NUEVA respecto del paquete viejo (D-F6-5): allí Summary llamaba a time.Now()
// directo y su test solo podía afirmar un rango. Con el reloj inyectado se afirma
// el instante exacto.
//
// NO es el reloj de la telemetría: `elapsed_from_draft_ms` se mide con el de
// WithMetricsClock, que es otro y no se mueve con esta opción (ni al revés).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService construye el servicio sobre el store dado y aplica las opciones en
// orden. Sin opciones es un servicio completo para leer y transicionar: no avisa,
// no recuerda, no empuja y no publica.
//
// El puerto de ESCRITURA de revisiones sale del propio store por aserción de tipo
// (RevisionWriter), y conviene saber por qué no es ninguna de las otras dos
// opciones que había:
//
//   - No es un Option: habría que pasarle en el arranque el MISMO objeto que ya se
//     pasa como store, y un cableado que se puede olvidar es un cableado que se
//     olvida — aprobar dejaría de escribir su rastro sin que nada lo dijera.
//   - No se mete InsertRevision en el puerto Store: ese puerto es la BANDEJA del
//     dueño, y RevisionWriter está separado a propósito para que los PRODUCTORES de
//     revisiones no lo reciban entero.
//
// Con un store que no sepa escribir revisiones, Approve corta con
// ErrNoRevisionWriter en vez de aprobar sin rastro; todo lo demás funciona. Los dos
// stores reales —*Postgres y *MemoryStore— lo satisfacen.
func NewService(store Store, opts ...Option) *Service {
	// El log y los dos relojes nacen con un default utilizable y NO se exigen por
	// parámetro: un servicio sin ellos tendría que ramificar por nil en cada aviso y
	// en cada fecha. El mismo criterio que ya usan *Postgres y *MemoryStore con su
	// logger.Default().
	s := &Service{store: store, log: logger.Default(), metricsNow: time.Now, now: time.Now}
	if w, ok := store.(RevisionWriter); ok {
		s.revisions = w
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// List devuelve la página de solicitudes del tenant que casan con el filtro, con el
// total de coincidencias sin paginar. Sanea la paginación (Filter.Normalized) antes
// de consultar —el llamante no puede pedir 100k filas de un golpe— y la Page que
// devuelve lleva los valores YA saneados. Sin coincidencias, Page.Intakes es una
// lista VACÍA y no nil: la UI itera sin ramificar por el nulo. Un error del store
// se devuelve tal cual, con la Page en cero.
//
// Es además un TOQUE de los recordatorios perezosos (seña y plazo): el dueño
// abriendo su bandeja es lo que hace de reloj, porque en esta plataforma no hay
// ninguno (ADR-0003). Las reglas del toque, que valen igual para Get:
//
//   - va DESPUÉS de tener la página —solo se evalúa lo que de verdad se leyó: cada
//     colaborador cableado recibe exactamente las solicitudes de la página— y no
//     decide lo que se devuelve;
//   - una lectura que falla, o que no trae ninguna fila, no toca a nadie;
//   - R-05: se pregunta por CADA colaborador por separado; el orden entre los dos no
//     significa nada (hablan con personas distintas y escriben marcas distintas).
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

// ListDetails devuelve TODAS las solicitudes del filtro con sus líneas, sin paginar:
// es lo que consumen el export y el summary. La cota de paginación no aplica aquí
// (una hoja de cálculo partida en páginas no sirve), así que la pone
// MaxExportIntakes: con exactamente esa cantidad devuelve todo; con una más,
// ErrTooLarge y ninguna fila.
//
// Al store se le pide UNA solicitud MÁS que la cota (MaxExportIntakes+1) justamente
// para poder distinguir «cabe justo» de «se pasa»: pidiendo la cota exacta no habría
// forma de saber si sobraban filas, y el export saldría recortado sin avisar.
//
// NO es un toque de los recordatorios, a propósito: es un camino de datos masivos
// que el dueño dispara para llevarse una hoja de cálculo. Que descargar un CSV le
// mande WhatsApps a sus clientes sería una sorpresa desagradable, y sin cota útil.
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
// cota incluida: ErrTooLarge si se pasa— y delega la aritmética en BuildSummary,
// con el filtro normalizado.
//
// Summary.GeneratedAt es el instante del RELOJ INYECTADO (WithClock; time.Now por
// defecto), nunca una llamada directa al reloj del sistema (D-F6-5).
//
// Como ListDetails, NO es un toque de los recordatorios.
func (s *Service) Summary(ctx context.Context, tenantID string, f Filter) (Summary, error) {
	details, err := s.ListDetails(ctx, tenantID, f)
	if err != nil {
		return Summary{}, err
	}
	return BuildSummary(details, f.Normalized(), s.now()), nil
}

// Get devuelve la solicitud con sus líneas. ErrNotFound si no es del tenant (404
// opaco, INV-8): «no existe» y «es de otro» son la misma respuesta.
//
// Es el otro TOQUE de los recordatorios perezosos, con las reglas que describe
// List: abrir la solicitud concreta es la lectura más específica que existe y, si
// esa es justo la que tiene la seña o el plazo vencidos, es donde antes se nota. El
// colaborador recibe esa única solicitud.
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
// no hace nada: la lectura sigue siendo pura.
//
// NO se llama desde ListDetails ni Summary a propósito, aunque también leen
// solicitudes: son el EXPORT y el resumen, caminos de datos masivos que un dueño
// dispara para llevarse una hoja de cálculo. Que descargar un CSV le mande WhatsApps
// a sus clientes sería una sorpresa desagradable, y encima sin cota útil (ahí no hay
// página: son hasta MaxExportIntakes solicitudes).
//
// 🔴 SON DOS COLABORADORES INDEPENDIENTES (R-05), y la guarda tiene que preguntarlo
// dos veces. Hasta que nació el recordatorio del plazo esto era
// `if s.deposits == nil || len(touched) == 0 { return }`, y ese `return` habría
// dejado el recordatorio del plazo MUDO Y EN VERDE en cualquier despliegue sin el
// recordatorio de la seña: el colaborador nuevo ni siquiera llegaba a mirar. Lo
// único COMPARTIDO es el corte por lista vacía, que no es de nadie: sin filas leídas
// no hay nada que evaluar.
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
// transicionada. `to` se normaliza antes de nada (el alias `closed` entra como
// `confirmed`). El orden importa:
//
//  1. lee el estado actual (ErrNotFound si la solicitud no es del tenant): el
//     recurso se resuelve ANTES que el cuerpo, para no revelar por el código de
//     error si una solicitud ajena existe;
//  2. valida la transición contra la máquina de estados (*TransitionError con el
//     estado actual NORMALIZADO, el destino y los destinos permitidos). Un destino
//     DESCONOCIDO cae por aquí sin caso aparte, y pedir el estado en el que ya está
//     también: una transición a sí mismo no es una transición;
//  3. escribe con compare-and-swap sobre el estado leído, pasando al store TODAS
//     las variantes guardadas de ese estado (StoredVariants: una fila legada
//     `closed` transiciona como `confirmed`). ErrConflict si otro operador se
//     adelantó entre 1 y 3.
//
// La LÍNEA DE ENVÍO de `pending_approval` (D-041.11) no se ve en este método a
// propósito: es parte de la escritura del estado y vive en la misma transacción del
// store (Store.UpdateStatus). La Intake devuelta ya trae el total con ella.
//
// El AVISO AL CLIENTE (D-041.14) es el paso 4 y va DESPUÉS de la escritura: solo se
// le cuenta a alguien lo que ya es verdad en la base, y no puede fallar hacia
// arriba. Sostiene «como mucho un mensaje por transición» colgando de la ESCRITURA
// GANADORA y no de la petición. Se calla en cuatro casos:
//
//   - `notice` es NoticeByCaller: quien pidió la transición ya le escribe al cliente
//     con su propio texto (D-044.49). La transición se aplica IGUAL: callarse no es
//     no registrar. Con NoticeToClient (el selector de estado de la consola) sale;
//   - no hay notificador cableado (WithNotifier);
//   - la transición no se escribió (pasos 1–3 fallidos): quien pierde el
//     compare-and-swap no notifica;
//   - el store devolvió una solicitud que sigue en el estado de origen. Es defensa
//     barata contra un store que «arregle» una transición imposible devolviendo el
//     estado actual: al cliente le llegaría un WhatsApp que no corresponde a ningún
//     cambio.
//
// Al notificador se le pasa la solicitud transicionada y el origen normalizado.
// R-03: SetStatus NO empuja al puente CRM.
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
// (D-043.21). Es la puerta que el motor consume cuando cancela un evento: pide
// abandonar SU contenido sin conocer ningún id de hijo. Devuelve lo que devuelva el
// store.
//
// La idempotencia vive en el store: 0 filas —ya abandonada, ya resuelta, o un
// evento sin contenido (menú, encuesta)— es ÉXITO, y su guarda `open` garantiza que
// una `confirmed` jamás se abandona por aquí.
//
// NO notifica al cliente, a propósito: el aviso cuelga de las transiciones del
// OPERADOR (SetStatus), y la muerte del evento ya se la contó al cliente el propio
// flujo. Tampoco empuja al CRM.
//
// ⚠️ LEGADO REGISTRADO: una solicitud sin evento declarado (anterior a la inversión
// de la FK) es INALCANZABLE por esta puerta: ningún eventID la encuentra.
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
//     ErrConflict y sale por el `return` de SetStatus sin avisar a nadie;
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
	if notice.silences() {
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
// de la solicitud `d`, con su ciclo de vida REAL. Le pasa al CRMPusher exactamente
// lo que recibe, sin tocarlo. Sin CRMPusher cableado no hace nada.
//
// La llama la escritura que acaba de parir esa revisión —dentro de este Service—:
// es el mismo criterio que el aviso al cliente, que cuelga del compare-and-swap
// ganador y no de la petición (R-03: nadie más empuja).
//
// 🔴 EL `revisionNo` ES OBLIGATORIO Y EXPLÍCITO (R-12). El puente hace UPSERT por
// (intake_id, revision_no) y trata como DUPLICADO todo par repetido, así que el
// número es lo que distingue «esto es un estado nuevo» de «esto ya lo sabía». Se
// pide por parámetro en vez de deducirlo de d.Revisions porque solo el llamante
// sabe cuál de ellas acaba de escribir.
//
// ⚠️ CONSECUENCIA QUE NO SE TAPA: aquí no hay deduplicación. Llamar dos veces con el
// MISMO número empuja dos veces (webhook_outbox no tiene unicidad por ese par: la
// idempotencia es del receptor). Lo que lo impide es colgar esta llamada de la
// escritura que numeró la revisión, una vez por revisión.
func (s *Service) PushRevisionToCRM(ctx context.Context, tenantID string, d Detail, revisionNo int) {
	if s.crm == nil {
		return
	}
	s.crm.PushRevision(ctx, tenantID, d, revisionNo)
}

// EnsureShippingLine garantiza la línea estándar de envío de una solicitud
// (D-041.11) y deja su total cuadrado con las líneas. Es idempotente; delega en el
// store con la política recibida y devuelve su error tal cual.
//
// Existe suelta —y no solo dentro de SetStatus— porque hay un segundo momento en que
// la línea tiene que aparecer y no hay transición de por medio: el cierre del
// carrito, que va directo a `confirmed`. Ese llamante usa ShippingOnlyIfZones; el
// del presupuesto, ShippingAlways.
func (s *Service) EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	return s.store.EnsureShippingLine(ctx, tenantID, intakeID, policy)
}
