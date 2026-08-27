package intakes

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
)

// ============================================================================
// Notificador de cambio de estado (D-041.14, Plan 041 · T4.2)
//
// Es el ÚNICO sitio de este paquete desde el que sale un mensaje a una persona
// real. Todo lo demás mueve filas; esto le hace sonar el teléfono a alguien que
// compró algo, y esa asimetría gobierna las tres reglas de abajo.
//
// 1. NOTIFICAR NO PUEDE TUMBAR LA TRANSICIÓN. Por eso NotifyStatus no devuelve
//    error: no es que se ignore el fallo —se loguea con su command_id—, es que la
//    firma le quita a cualquier llamante futuro la posibilidad de propagarlo y
//    dejar al dueño con un 500 y un pedido que SÍ cambió de estado. El estado ya
//    está escrito cuando esto corre; lo que queda es contarlo.
//
// 2. COMO MUCHO UN MENSAJE POR TRANSICIÓN. La notificación cuelga de la ESCRITURA
//    ganadora, no de la petición: Service.SetStatus solo llama aquí después de que
//    el compare-and-swap del store haya devuelto la solicitud transicionada. Pedir
//    dos veces lo mismo no manda dos mensajes porque la segunda vez ni siquiera hay
//    transición que notificar (ver Service.notify y los tests de service_test.go).
//
// 3. CERO PII EN LOS LOGS (ADR-0007/INV-04). El destino se resuelve por la vía
//    custodiada, se usa para enviar y NO se loguea, ni se persiste, ni vuelve al
//    llamante. Lo que sale en el log es intake_id, session_id, estados y
//    command_id: todo opaco o de negocio.
//
// ⚠️ CERO RELOJ (ADR-0003, D-041.16). Esto es SÍNCRONO a la transición: no hay
// cron, ni ticker, ni barrido. El recordatorio PEREZOSO de la seña —que se evalúa
// cuando alguien TOCA la solicitud— vive en deposit.go y hereda estas tres reglas
// reusando este mismo Notifier: el texto de la seña, la vía custodiada y la entrega
// son las de aquí, no una segunda salida hacia WhatsApp.
// ============================================================================

// StatusNotice dice QUIÉN le cuenta al cliente la transición que se está
// aplicando. Son dos momentos distintos con dos reglas distintas, y por eso lo
// elige el LLAMANTE: es el mismo patrón con el que esta capa ya resuelve
// «una regla, dos puertas» en EnsureShippingLine(…, ShippingPolicy)
// (shipping.go:71 — «dos momentos distintos con dos reglas distintas, y por eso
// el llamante la elige»).
//
// POR QUÉ UN PARÁMETRO TIPADO Y NO UN FUNCTIONAL OPTION POR LLAMADA. En este repo
// los functional options son SIEMPRE de construcción —Option de NewService,
// ReminderOption de NewDepositReminder, y una quincena más en internal/—: no hay
// un solo `opts ...` en un método. Uno por llamada sería un patrón nuevo aquí y,
// sobre todo, dejaría OPCIONAL la única decisión que no puede quedarse implícita
// —quién le habla al cliente—: el que lo omite manda un mensaje sin haberlo
// decidido, y el cliente recibe dos. Con un parámetro, cada puerta lo declara y el
// compilador no deja pasar a nadie sin decirlo.
//
// Y no es un método aparte (`SetStatusSinAviso`) porque serían dos contratos
// públicos que documentar y mantener en sincronía —con sus dos entradas en el
// puerto de publicapi— para una diferencia de un bit; el día que se añada una
// tercera regla, un método más multiplica la superficie y una constante más no.
//
// 🔴 LO QUE NO ES: no borra ni toca statusTemplates. Quitar una entrada de ese
// mapa apagaría el aviso para TODO el mundo, el <select> de estado de la consola
// incluido (Plan 041 · T4.2); esto lo apaga para UNA transición y solo si su
// llamante lo pide.
type StatusNotice int

const (
	// NoticeToClient — la plataforma manda el aviso genérico del estado destino
	// (statusTemplates, o la plantilla de seña del tenant). Es la conducta del
	// Plan 041 y el CERO del tipo a propósito: el valor por descuido es el que
	// habla, nunca el que calla. Un silencio nace de una decisión escrita.
	NoticeToClient StatusNotice = iota
	// NoticeByCaller — el llamante YA le escribe al cliente por su cuenta y la
	// plataforma se calla en ESTA transición (D-044.49, decisión de producto del
	// 2026-08-27: al aprobar y al pedir información el cliente recibe UN SOLO
	// mensaje, el del dueño, porque su texto ya dice lo mismo y mejor).
	//
	// Se calla ENTERO, no solo el texto genérico: si el llamante anuncia la
	// transición, la plataforma no tiene nada que añadir por detrás.
	NoticeByCaller
)

// silencia responde si esta política deja el aviso en manos del llamante. Está
// escrito como método —y no como `notice == NoticeByCaller` suelto en Service—
// para que la regla viva junto al tipo, igual que ShippingPolicy.applies.
func (n StatusNotice) silencia() bool { return n == NoticeByCaller }

// MessageSender empuja un texto por una sesión viva del Edge y espera su Ack. La
// firma es la de (*gatewaygrpc.Server).SendText —la misma que ya declaran
// runtime.Sender y publicapi.MessageSender—, así que el Gateway la satisface sin
// adaptador.
//
// El command_id NO es un parámetro: lo genera el Gateway al despachar y vuelve
// dentro del Ack (o dentro del error, ver commandIDCarrier).
type MessageSender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// Destinations traduce el contact_id OPACO de la solicitud a una referencia
// direccionable. Lo satisface *contact.PostgresResolver, que es la VÍA CUSTODIADA
// de PII de la plataforma: descifra el valor con la KEK que envolvió esa fila y lo
// devuelve solo en memoria (ADR-0017).
//
// Es la única grieta —consciente y acotada— en el «cero PII» que declara el doc de
// este paquete: `intakes` sigue sin descifrar nada por su cuenta y sin guardar el
// resultado. Se lo pide a quien tiene la custodia, lo pasa al Sender y lo suelta.
type Destinations interface {
	Destino(ctx context.Context, tenantID, contactID string) (contact.Ref, error)
}

// NotifySettings es la config comercial del tenant que el mensaje necesita
// (tenant_settings, migración 0045).
type NotifySettings struct {
	// DepositTemplate es la plantilla de la SEÑA: datos de la cuenta e
	// instrucciones de pago que solo el tenant conoce. VACÍA es el estado de
	// arranque de cualquier tenant y significa «este tenant no puede pedir seña»
	// (COMMENT de la columna): sin ella no se manda nada.
	DepositTemplate string
	// DepositDueDays es el plazo de la seña en días, para el marcador {plazo}.
	DepositDueDays int
}

// SettingsReader lee del tenant lo que el texto necesita. Lo satisface *Postgres
// (y *MemoryStore en tests): es la MISMA fila de tenant_settings de la que sale
// shipping_zones, así que no hay un segundo origen de config que mantener.
type SettingsReader interface {
	NotifySettings(ctx context.Context, tenantID string) (NotifySettings, error)
}

// commandIDCarrier lo satisface el error de un envío que ya tenía command_id
// asignado (*gatewaygrpc.SendError). Se pide por duck-typing y no por el tipo
// concreto para no acoplar el dominio al Gateway: cualquier transporte que sepa
// decir «este comando se llamaba así» encaja.
type commandIDCarrier interface{ CommandID() string }

// DefaultDepositDueDays espeja el DEFAULT de tenant_settings.deposit_due_days
// (migración 0045). Vale cuando el tenant no tiene fila de config: un tenant sin
// configurar no es un error, es un tenant recién nacido.
const DefaultDepositDueDays = 3

// Marcadores que la plataforma rellena en cualquier plantilla, sea la del código o
// la del tenant. Son deliberadamente pocos y con nombre en español: quien escribe
// deposit_template es la dueña del negocio desde la consola, no un programador.
//
// {fecha_limite} lo estrena T4.4 y solo tiene valor porque la fecha ya se escribe:
// la entrada en `deposit_requested` fija intakes.deposit_due_at dentro de la MISMA
// transacción que cambia el estado (Store.UpdateStatus), así que la solicitud que
// llega aquí ya la trae. Con la fecha sin fijar el marcador se deja SIN sustituir
// —tal cual, visible— en vez de rellenarse con una fecha inventada: un mensaje que
// enseña «{fecha_limite}» delata el fallo, y uno que dice «01/01/0001» le miente al
// cliente.
const (
	placeholderTotal       = "{total}"
	placeholderPlazo       = "{plazo}"
	placeholderFechaLímite = "{fecha_limite}"
)

// dueDateLayout es el formato de {fecha_limite}: día/mes/año, que es como lee una
// fecha quien recibe el WhatsApp.
//
// Se formatea en UTC porque el sistema NO tiene zona horaria del tenant (no hay
// columna, ni en tenant_settings ni en tenants) y el resto del repo ya publica sus
// fechas en UTC (publicapi/export.go). La consecuencia hay que saberla: con un
// plazo en DÍAS, un tenant lejos de UTC puede ver la fecha corrida un día. Se
// acepta porque el plazo es grueso —tres días por defecto— y porque inventar una
// zona sería peor que no tenerla; el día que exista la del tenant, se cambia aquí.
const dueDateLayout = "02/01/2006"

// statusTemplates es el texto por estado DESTINO, en español (D-041.14). Un estado
// AUSENTE de este mapa no notifica, y esa ausencia es la decisión:
//
//   - `deposit_requested` no está porque su texto no puede vivir en el código: son
//     los datos bancarios del tenant. Sale de NotifySettings.DepositTemplate.
//   - `abandoned` no está porque el descarte es HIGIENE INTERNA del dueño y no se
//     le cuenta al cliente (design.md §D-041.18: «no borra y no notifica»).
//     Avisar de que su pedido «se abandonó» sería, además de inútil, una forma
//     rara de despedirse.
//   - `open` y `expired` no están porque nadie transiciona hacia ellos.
//
// El texto NO repite lo que el carrito ya dijo al cerrar (cart/screens.go pinta su
// propio «¡Pedido confirmado!» al proyectar): lo de aquí es la voz del DUEÑO
// moviendo el pedido desde la consola, que es un momento distinto y posterior.
var statusTemplates = map[string]string{
	StatusPendingApproval: "Recibimos tu pedido y lo estamos revisando. Te avisamos apenas te lo confirmemos.",
	StatusConfirmed:       "✅ Tu pedido quedó confirmado. Total " + placeholderTotal + ". ¡Gracias!",
	StatusDepositPaid:     "Recibimos tu seña. Tu pedido queda reservado; te avisamos cuando esté listo.",
	StatusSettled:         "Tu pedido está pagado por completo. ¡Gracias por tu compra!",
	StatusCancelled:       "Tu pedido fue cancelado. Si fue un error, respóndenos por aquí y lo retomamos.",
	StatusRejected:        "No podemos tomar tu pedido en este momento. Si quieres, respóndenos y lo vemos.",
	StatusNeedsInfo:       "Nos falta un dato para avanzar con tu pedido. Te escribimos enseguida por aquí.",
}

// Notifier le cuenta al CLIENTE que su solicitud cambió de estado, por la MISMA
// sesión de WhatsApp con la que la armó (Intake.SessionID). Satisface
// StatusNotifier.
type Notifier struct {
	sender   MessageSender
	contacts Destinations
	settings SettingsReader
	log      logger.Logger
}

// NewNotifier construye el notificador. Las cuatro dependencias son obligatorias:
// sin cualquiera de ellas no hay mensaje que mandar ni forma de contar que no se
// mandó, así que un Notifier a medias sería peor que ninguno (Service acepta un
// notificador nil y en ese caso simplemente no notifica).
func NewNotifier(sender MessageSender, contacts Destinations, settings SettingsReader, log logger.Logger) *Notifier {
	return &Notifier{sender: sender, contacts: contacts, settings: settings, log: log}
}

// NotifyStatus despacha el aviso de la transición `from` → `in.Status`. No
// devuelve error a propósito (ver el bloque de cabecera, regla 1): todo fallo se
// loguea aquí y muere aquí.
//
// El orden es el que hace que no se mande un mensaje a medias: primero se decide
// SI hay algo que decir y se arma el texto entero, y solo entonces se toca la vía
// custodiada de PII. Al revés, un tenant sin plantilla de seña habría hecho
// descifrar un contacto para nada.
func (n *Notifier) NotifyStatus(ctx context.Context, tenantID string, in Intake, from string) {
	if n == nil || n.log == nil || n.sender == nil || n.contacts == nil || n.settings == nil {
		return // un notificador a medias no avisa, pero tampoco rompe nada
	}
	defer n.contenerPánico(in)

	to := NormalizeStatus(in.Status)
	log := n.log.With(
		"intake_id", in.ID,
		"tenant_id", tenantID,
		"session_id", in.SessionID,
		"status_from", NormalizeStatus(from),
		"status_to", to,
	)

	text, ok := n.text(ctx, tenantID, in, to, log)
	if !ok {
		return
	}
	n.deliver(ctx, tenantID, in, text, log)
}

// contenerPánico es lo que hace ESTRUCTURAL la regla 1, y no solo una convención
// de firmas: NotifyStatus no devuelve error, pero sin esto un pánico en cualquiera
// de las piezas que consume —el resolver de PII, el Gateway, un Ack inesperado— se
// llevaría por delante la respuesta de una transición que YA está escrita en la
// base. El dueño vería un 500, reintentaría, y se toparía con un 422 por estar la
// solicitud ya en el destino: acabaría creyendo que su cambio no se aplicó cuando
// sí se aplicó.
//
// NO es tragarse el defecto: se registra en Error con el pánico entero, que es
// donde hay que ir a buscarlo. Lo que se contiene es el ALCANCE del daño, no la
// noticia.
func (n *Notifier) contenerPánico(in Intake) {
	r := recover()
	if r == nil {
		return
	}
	n.log.Error("notificación: pánico avisando del cambio de estado; la transición YA está aplicada",
		"intake_id", in.ID, "panic", fmt.Sprint(r))
}

// text arma el mensaje del estado destino, o dice que no hay ninguno que mandar.
// El booleano NO es «hubo error»: es «hay algo que decirle al cliente», y los dos
// casos en que vale false —estado sin plantilla, tenant sin plantilla de seña— son
// silencios NORMALES, no averías.
func (n *Notifier) text(ctx context.Context, tenantID string, in Intake, to string, log logger.Logger) (string, bool) {
	if to == StatusDepositRequested {
		return n.depositText(ctx, tenantID, in, log)
	}
	tpl, ok := statusTemplates[to]
	if !ok {
		// Silencio deliberado: ver el comentario de statusTemplates. Queda en debug
		// porque es el camino normal de `abandoned`, no una anomalía.
		log.Debug("notificación: el estado no le dice nada al cliente, no se envía")
		return "", false
	}
	return render(tpl, in, DefaultDepositDueDays), true
}

// depositText resuelve el texto de la SEÑA, que es el único que NO puede vivir en
// el código: lleva los datos de la cuenta del tenant, que solo el tenant conoce.
//
// DECISIÓN DE PRODUCTO (T4.2): sin plantilla configurada NO se manda nada. Un
// «te pedimos una seña» genérico, sin decir dónde ni cómo pagarla, deja al cliente
// preguntando y a la dueña en evidencia — es peor que el silencio. Y es lo que ya
// dice la columna: «Vacía ⇒ el tenant no puede pedir seña» (COMMENT de la 0045).
// La TRANSICIÓN a deposit_requested se aplica igual: no notificar no es no
// registrar, y el dueño ve en su bandeja que el pedido está esperando seña.
//
// Un fallo LEYENDO la config sí es una avería (nivel error) y también acaba en
// silencio: preferimos no mandar a mandar un texto con marcadores sin rellenar.
func (n *Notifier) depositText(ctx context.Context, tenantID string, in Intake, log logger.Logger) (string, bool) {
	cfg, ok := n.depositSettings(ctx, tenantID, log, sinPlantillaAlPedirSeña)
	if !ok {
		return "", false
	}
	return render(cfg.DepositTemplate, in, cfg.DepositDueDays), true
}

// depositSettings resuelve la config de la seña y responde a UNA pregunta: ¿puede
// este tenant decirle algo al cliente sobre la seña? Está separada del render porque
// el recordatorio (deposit.go) necesita preguntarlo ANTES de gastar la marca de «ya
// recordado»: si se marcara primero, un tenant que todavía no configuró su plantilla
// dejaría a ese cliente sin recordatorio para siempre, incluso después de
// configurarla.
//
// Los dos `false` son los de siempre: sin plantilla es un silencio NORMAL (Warn con
// la causa) y un fallo de lectura es una avería (Error) que también acaba en
// silencio, porque mandar un texto con marcadores sin rellenar es peor que no mandar.
//
// `sinPlantilla` es la CONSECUENCIA, y la trae el llamante porque no es la misma en
// los dos caminos: al pedir la seña, sin plantilla no sale NADA; al aprobar (T4.3),
// sale la cotización del dueño sola. Un texto fijo aquí le contaría al log de uno la
// consecuencia del otro — y un log que afirma lo que no pasó es la misma clase de
// defecto que un mensaje que afirma un estado que no es.
func (n *Notifier) depositSettings(ctx context.Context, tenantID string, log logger.Logger, sinPlantilla string) (NotifySettings, bool) {
	cfg, err := n.settings.NotifySettings(ctx, tenantID)
	if err != nil {
		log.Error("notificación: no se pudo leer la config del tenant", "error", err, "consecuencia", sinPlantilla)
		return NotifySettings{}, false
	}
	if strings.TrimSpace(cfg.DepositTemplate) == "" {
		log.Warn("notificación: el tenant no tiene plantilla de seña (tenant_settings.deposit_template); " +
			sinPlantilla)
		return NotifySettings{}, false
	}
	return cfg, true
}

// --- la cotización del DUEÑO (Plan 044 · T4.3, D-044.49 §1) ------------------
//
// Las dos funciones de abajo satisfacen QuoteSender y son la MISMA salida hacia
// WhatsApp que el aviso automático, con otro dueño del texto: aquí las palabras las
// pone la dueña del negocio y la plataforma solo adjunta lo que solo ella sabe (sus
// datos de pago) y lo entrega. Por eso reusan `deliver` entero —vía custodiada de
// PII, Ack y cero PII en los logs— en vez de abrir una segunda puerta hacia el
// Gateway.

// sinPlantillaAlPedirSeña y sinPlantillaAlAprobar son las dos consecuencias de que un
// tenant no tenga configurada su plantilla de seña. Ver depositSettings.
const (
	sinPlantillaAlPedirSeña = "la transición se aplicó pero al cliente no se le manda nada"
	sinPlantillaAlRecordar  = "no se manda el recordatorio y la marca de «ya recordado» sigue libre"
	sinPlantillaAlAprobar   = "se le manda la cotización del dueño sola, sin instrucciones de pago"
)

// QuoteText implementa QuoteSender: compone la cotización ENTERA que va a salir —el
// texto del dueño más la plantilla de seña del tenant— y la devuelve para que el
// llamante la GUARDE antes de mandarla.
//
// El texto del dueño se devuelve TAL CUAL, byte a byte, y la plantilla se pega
// detrás separada por un renglón en blanco. No se recorta, no se normaliza y no se
// le añade nada más: lo que se guarda en la revisión `approved` es exactamente esto,
// y cualquier retoque posterior convertiría ese registro en una aproximación.
//
// Un notificador a medias —o un tenant sin plantilla, o un fallo leyendo su config—
// devuelve la cotización sola. Es una respuesta COMPLETA: el cliente recibe su
// presupuesto; lo que falta es el «cómo pagar la seña», que este tenant no ha escrito.
func (n *Notifier) QuoteText(ctx context.Context, tenantID string, in Intake, ownerText string) string {
	if n == nil || n.log == nil || n.settings == nil {
		return ownerText
	}
	log := n.log.With("intake_id", in.ID, "tenant_id", tenantID, "accion", "approve")
	cfg, ok := n.depositSettings(ctx, tenantID, log, sinPlantillaAlAprobar)
	if !ok {
		return ownerText
	}
	// render rellena {total} y {plazo}; {fecha_limite} se queda SIN sustituir a
	// propósito, porque al aprobar todavía no hay seña pedida y por tanto no hay
	// deposit_due_at (ver la constante placeholderFechaLímite): un marcador visible
	// delata que la plantilla promete una fecha que este momento no tiene, y eso es
	// estrictamente mejor que estamparle al cliente una fecha inventada.
	return ownerText + separadorDeSeña + render(cfg.DepositTemplate, in, cfg.DepositDueDays)
}

// SendQuote implementa QuoteSender: entrega el texto ya compuesto por la sesión de la
// solicitud. No devuelve error (regla 1 de la cabecera) y contiene el pánico por lo
// mismo: cuando esto corre, la aprobación YA está escrita y numerada.
func (n *Notifier) SendQuote(ctx context.Context, tenantID string, in Intake, text string) {
	if n == nil || n.log == nil || n.sender == nil || n.contacts == nil {
		return // un notificador a medias no avisa, pero tampoco rompe nada
	}
	defer n.contenerPánico(in)

	log := n.log.With(
		"intake_id", in.ID,
		"tenant_id", tenantID,
		"session_id", in.SessionID,
		"status_to", NormalizeStatus(in.Status),
		"accion", "approve",
	)
	n.deliver(ctx, tenantID, in, text, log)
}

// deliver resuelve el destino por la vía custodiada y despacha. Es la parte que
// toca PII y la que no puede fallar hacia arriba.
//
// Un Ack con Ok=false NO es un éxito: el Edge acusó recibo del comando y avisó de
// que el envío falló (por ejemplo, un destino que WhatsApp rechaza). Se loguea como
// error con su command_id, igual que un fallo de transporte, porque para el cliente
// la consecuencia es la misma: no le llegó nada.
func (n *Notifier) deliver(ctx context.Context, tenantID string, in Intake, text string, log logger.Logger) {
	dst, err := n.contacts.Destino(ctx, tenantID, in.ContactID)
	if err != nil {
		// El error del resolver nombra el contact_id OPACO y el kind, nunca el valor.
		log.Error("notificación: no se pudo resolver el destino del contacto", "error", err)
		return
	}
	to, err := dst.Sendable()
	if err != nil {
		log.Error("notificación: el contacto no tiene destino direccionable", "error", err)
		return
	}

	// A partir de aquí `to` es PII en memoria: se pasa al Sender y NO se loguea.
	ack, err := n.sender.SendText(ctx, in.SessionID, to, text)
	if err != nil {
		log.Error("notificación: el envío falló; la transición ya está aplicada",
			"command_id", commandIDOf(err), "error", err)
		return
	}
	if !ack.GetOk() {
		log.Error("notificación: el Edge rechazó el envío; la transición ya está aplicada",
			"command_id", ack.GetAckedCommandId(), "edge_error", ack.GetError())
		return
	}
	log.Info("notificación de cambio de estado enviada al cliente",
		"command_id", ack.GetAckedCommandId())
}

// commandIDOf extrae el command_id de un error de envío, si lo lleva. Devuelve
// cadena vacía cuando el fallo ocurrió ANTES de que hubiera comando (o cuando el
// transporte no sabe decirlo): un command_id inventado sería peor que ninguno,
// porque quien lo busque en los acuses del Edge no encontrará nada y creerá que el
// mensaje se perdió en el camino.
func commandIDOf(err error) string {
	var carrier commandIDCarrier
	if !errors.As(err, &carrier) {
		return ""
	}
	return carrier.CommandID()
}

// render sustituye los marcadores de una plantilla con los datos de la solicitud.
// Una plantilla sin marcadores sale intacta, que es el caso de casi todas.
//
// El formato del dinero es el MISMO que el carrito ya le enseña al cliente por
// WhatsApp al cerrar (cart/screens.go, `money`): "$" y dos decimales. Se replica en
// vez de importarse para no acoplar el dominio de solicitudes al módulo
// conversacional por un formateador de una línea — a cambio, quien cambie el del
// carrito tiene que acordarse de este (lo fija un test).
func render(tpl string, in Intake, dueDays int) string {
	replacements := []string{
		placeholderTotal, fmt.Sprintf("$%.2f", in.Total),
		placeholderPlazo, strconv.Itoa(depositDueDays(dueDays)),
	}
	// Sin fecha fijada el marcador NO se toca (ver el comentario de la constante):
	// se prefiere un texto que delata el fallo a uno que le da al cliente una fecha
	// que nadie escribió.
	if !in.DepositDueAt.IsZero() {
		replacements = append(replacements,
			placeholderFechaLímite, in.DepositDueAt.UTC().Format(dueDateLayout))
	}
	return strings.NewReplacer(replacements...).Replace(tpl)
}

// depositDueDays aplica la regla del plazo en UN solo sitio: un valor no positivo
// —columna sin configurar, tenant sin fila— es el plazo por defecto. La usan el
// render del marcador {plazo} y el cálculo de deposit_due_at, y tienen que decir lo
// mismo: sería absurdo prometerle al cliente «3 días» y fijar el vencimiento a otra
// cosa.
func depositDueDays(days int) int {
	if days <= 0 {
		return DefaultDepositDueDays
	}
	return days
}

// crmStatusTemplates es el texto por estado del CRM (Plan 042 · T4.4). Es un mapa
// SEPARADO de statusTemplates y no una ampliación suya, por la misma razón por la
// que crm_status es una columna propia: los dos vocabularios son DISJUNTOS
// (D-042.6). Fundirlos obligaría a decidir qué significa `preparing` en el ciclo de
// vida de wApp, que es justo la semántica de CRM que INV-08 prohíbe inventar.
//
// `rejected` NO tiene texto propio: toma el del ciclo de vida a propósito. Es el
// único literal que comparten los dos vocabularios y el hecho que le llega al
// cliente es el mismo —«no podemos tomar tu pedido»—, así que dos redacciones
// distintas para lo mismo solo se separarían con el tiempo.
//
// Los otros tres se escribieron aquí porque el 041 no tenía ninguno que sirviera:
// `paid` no puede tomar prestado el de `settled` ni el de `deposit_paid`, que hablan
// del cobro que gestiona el DUEÑO en wApp, no de lo que su CRM dio por pagado.
// Siguen el criterio del 041: no repiten lo que el carrito ya dijo al cerrar, no
// prometen plazos que nadie puede cumplir y dejan la puerta abierta a responder.
var crmStatusTemplates = map[string]string{
	CRMStatusPaid:      "Recibimos tu pago. ¡Gracias! Ya estamos con tu pedido.",
	CRMStatusPreparing: "Tu pedido ya se está preparando. Te avisamos apenas salga.",
	CRMStatusDelivered: "Tu pedido fue entregado. ¡Que lo disfrutes! Cualquier cosa, respóndenos por aquí.",
	CRMStatusRejected:  statusTemplates[StatusRejected],
}

// NotifyCRMStatus avisa al cliente de que su pedido cambió de estado EN EL CRM del
// negocio (Plan 042 · T4.4).
//
// Reutiliza la mecánica entera del 041 O4 —resolución del destinatario por la vía
// custodiada, envío por la sesión del negocio, contención del pánico— y cambia solo
// de dónde sale el texto. Eso es lo que hace que el aviso salga igual venga el
// cambio del CRM o de la pantalla del dueño, que es lo que pide T4.4.
//
// NO devuelve error, igual que NotifyStatus y por lo mismo (regla 1 de la cabecera):
// cuando esto corre, el reflejo YA está escrito. Un error hacia arriba haría que el
// puente reintentara y volviera a escribir lo mismo para nada, y el aviso tampoco se
// recuperaría por ese camino.
//
// Solo se llama con un cambio REAL: un puente con reintentos manda el mismo estado
// muchas veces y el cliente no puede recibir el mismo mensaje una vez por reintento.
// Esa decisión vive en el llamante, que es quien sabe si la fila cambió.
func (n *Notifier) NotifyCRMStatus(ctx context.Context, tenantID string, in Intake, crmStatus string) {
	if n == nil || n.log == nil || n.sender == nil || n.contacts == nil || n.settings == nil {
		return // un notificador a medias no avisa, pero tampoco rompe nada
	}
	defer n.contenerPánico(in)

	log := n.log.With(
		"intake_id", in.ID,
		"tenant_id", tenantID,
		"session_id", in.SessionID,
		"crm_status", crmStatus,
	)

	tpl, ok := crmStatusTemplates[crmStatus]
	if !ok {
		// Un estado canónico SIN plantilla no debería existir —hay un test que lo
		// vigila—, así que esto no es el silencio normal de statusTemplates: es una
		// anomalía y se registra como tal.
		log.Warn("notificación CRM: estado canónico sin texto, el cliente no se entera")
		return
	}
	// render con dueDays en cero: ninguna de estas plantillas usa {plazo} ni
	// {fecha_limite} —son del cobro que gestiona el dueño, no del CRM— y el {total}
	// se resuelve igual que en el resto de los avisos.
	n.deliver(ctx, tenantID, in, render(tpl, in, 0), log)
}
