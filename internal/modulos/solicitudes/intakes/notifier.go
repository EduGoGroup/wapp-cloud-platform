// Porta internal/intakes/notifier.go @ 64c181a

package intakes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// ============================================================================
// Notificador de cambio de estado (D-041.14, Plan 041 · T4.2)
//
// Es el ÚNICO sitio de este paquete desde el que sale un mensaje a una persona
// real. Todo lo demás mueve filas; esto le hace sonar el teléfono a alguien que
// compró algo, y esa asimetría gobierna las tres reglas de abajo.
//
// 1. NOTIFICAR NO PUEDE TUMBAR LA TRANSICIÓN. Por eso ninguna salida de Notifier
//    devuelve error: no es que se ignore el fallo —se registra con su
//    command_id—, es que la firma le quita a cualquier llamante futuro la
//    posibilidad de propagarlo y dejar al dueño con un 500 y un pedido que SÍ
//    cambió de estado. Y es ESTRUCTURAL, no una convención de firmas: un pánico
//    de cualquiera de las piezas que consume —el resolver de PII, el Gateway, un
//    Ack inesperado— se CONTIENE aquí y se registra en Error con el mensaje
//    «notificación: pánico avisando del cambio de estado; la transición YA está
//    aplicada» y el pánico entero. Sin eso el dueño vería un 500, reintentaría y
//    se toparía con un 422 por estar la solicitud ya en el destino.
//
// 2. COMO MUCHO UN MENSAJE POR TRANSICIÓN. La notificación cuelga de la ESCRITURA
//    ganadora, no de la petición: el servicio solo llama aquí después de que el
//    compare-and-swap del store haya devuelto la solicitud transicionada. Cada
//    llamada a una salida de Notifier manda COMO MUCHO un SendText.
//
// 3. CERO PII EN LOS LOGS (ADR-0007/INV-04). El destino se resuelve por la vía
//    custodiada, se usa para enviar y NO se registra, ni se persiste, ni vuelve al
//    llamante. Lo que sale en el log es intake_id, tenant_id, session_id, estados,
//    la acción y command_id: todo opaco o de negocio.
//
// ⚠️ CERO RELOJ (ADR-0003, D-041.16). Esto es SÍNCRONO a la transición: no hay
// cron, ni ticker, ni barrido. El recordatorio PEREZOSO de la seña vive en
// deposit.go y hereda estas tres reglas reusando este mismo Notifier: el texto de
// la seña, la vía custodiada y la entrega son las de aquí, no una segunda salida
// hacia WhatsApp.
//
// 🔶 INVENTARIO DE LO QUE VE EL CLIENTE (texto observable, BYTE A BYTE; el test
// lleva una aserción literal de cada mensaje ya renderizado).
//
// Por estado DESTINO del ciclo de vida (eran `statusTemplates`), 7 claves:
//
//	pending_approval  «Recibimos tu pedido y lo estamos revisando. Te avisamos apenas te lo confirmemos.»
//	confirmed         «✅ Tu pedido quedó confirmado. Total {total}. ¡Gracias!»   (1 marcador)
//	deposit_paid      «Recibimos tu seña. Tu pedido queda reservado; te avisamos cuando esté listo.»
//	settled           «Tu pedido está pagado por completo. ¡Gracias por tu compra!»
//	cancelled         «Tu pedido fue cancelado. Si fue un error, respóndenos por aquí y lo retomamos.»
//	rejected          «No podemos tomar tu pedido en este momento. Si quieres, respóndenos y lo vemos.»
//	needs_info        «Nos falta un dato para avanzar con tu pedido. Te escribimos enseguida por aquí.»
//
// Un estado AUSENTE de esa lista no notifica, y esa ausencia es la decisión:
// `deposit_requested` no está porque su texto no puede vivir en el código (son los
// datos bancarios del tenant: sale de NotifySettings.DepositTemplate);
// `abandoned` no está porque el descarte es HIGIENE INTERNA del dueño y no se le
// cuenta al cliente (D-041.18: «no borra y no notifica»); `open` y el legado de
// vencimiento no están porque nadie transiciona hacia ellos. El texto NO repite lo
// que el carrito ya dijo al cerrar: es la voz del DUEÑO moviendo el pedido desde
// la consola, un momento distinto y posterior.
//
// Por estado del CRM (eran `crmStatusTemplates`), 4 claves, ninguna con marcador:
//
//	paid       «Recibimos tu pago. ¡Gracias! Ya estamos con tu pedido.»
//	preparing  «Tu pedido ya se está preparando. Te avisamos apenas salga.»
//	delivered  «Tu pedido fue entregado. ¡Que lo disfrutes! Cualquier cosa, respóndenos por aquí.»
//	rejected   el MISMO texto que `rejected` del ciclo de vida, no una copia
//
// Los dos vocabularios son DISJUNTOS (D-042.6) y por eso son dos tablas: fundirlas
// obligaría a decidir qué significa `preparing` en el ciclo de vida de wApp, la
// semántica de CRM que INV-08 prohíbe inventar. `rejected` es el único literal
// compartido y el hecho que le llega al cliente es el mismo, así que dos
// redacciones solo se separarían con el tiempo. Hay un texto por cada estado
// canónico del CRM y ninguno de más, y ninguno usa {plazo} ni {fecha_limite}: son
// del cobro que gestiona el dueño, no del CRM.
//
// Los TRES MARCADORES que la plataforma rellena en cualquier plantilla, sea la
// del código o la del tenant. Son deliberadamente pocos y con nombre en español:
// quien escribe deposit_template es la dueña del negocio desde la consola.
//
//   - {total}: "$" y dos decimales (`$%.2f`) de Intake.Total, sin separador de
//     miles. Es el MISMO formato que el carrito le enseña al cliente al cerrar; se
//     replica en vez de importarse para no acoplar el dominio al módulo
//     conversacional por un formateador de una línea.
//   - {plazo}: los días de la seña en decimal. Un valor no positivo —columna sin
//     configurar, tenant sin fila— es DefaultDepositDueDays. Es la MISMA regla con
//     la que se fija deposit_due_at: sería absurdo prometer «3 días» y fijar el
//     vencimiento a otra cosa.
//   - {fecha_limite}: Intake.DepositDueAt en UTC, formato día/mes/año
//     ("02/01/2006"). Con la fecha SIN fijar (cero) el marcador se deja SIN
//     sustituir —tal cual, visible—: un mensaje que enseña «{fecha_limite}» delata
//     el fallo y uno que dice «01/01/0001» le miente al cliente. Va en UTC porque
//     no existe zona horaria del tenant; con un plazo en DÍAS, un tenant lejos de
//     UTC puede ver la fecha corrida un día, y se acepta.
//
// Los marcadores distinguen mayúsculas y no toleran espacios dentro de las
// llaves; cada aparición se sustituye (también las repetidas) en UNA pasada, sin
// recursión; el resto de la plantilla —espacios de los extremos, saltos, emojis—
// sale intacto. Una plantilla sin marcadores sale intacta.
//
// Además salen por WhatsApp, sin plantilla del código: la plantilla de seña del
// tenant renderizada (NotifyStatus hacia deposit_requested), la cotización
// compuesta por QuoteText (SendQuote) y la pregunta del dueño (SendQuestion).
// ============================================================================

// StatusNotice dice QUIÉN le cuenta al cliente la transición que se está
// aplicando. Son dos momentos distintos con dos reglas distintas, y por eso lo
// elige el LLAMANTE: es el mismo patrón con el que esta capa ya resuelve «una
// regla, dos puertas» en EnsureShippingLine(…, ShippingPolicy).
//
// POR QUÉ UN PARÁMETRO TIPADO Y NO UN FUNCTIONAL OPTION POR LLAMADA. En este repo
// los functional options son SIEMPRE de construcción: no hay un solo `opts ...` en
// un método. Uno por llamada dejaría OPCIONAL la única decisión que no puede
// quedarse implícita —quién le habla al cliente—: el que lo omite manda un mensaje
// sin haberlo decidido, y el cliente recibe dos. Con un parámetro, cada puerta lo
// declara y el compilador no deja pasar a nadie sin decirlo. Y no es un método
// aparte porque serían dos contratos públicos que mantener en sincronía para una
// diferencia de un bit.
//
// 🔴 LO QUE NO ES: no borra ni toca las plantillas por estado. Quitar una entrada
// apagaría el aviso para TODO el mundo, el <select> de estado de la consola
// incluido (Plan 041 · T4.2); esto lo apaga para UNA transición y solo si su
// llamante lo pide. Por eso `confirmed` y `needs_info` —las dos transiciones que
// el dueño anuncia por su cuenta— SIGUEN teniendo texto en NotifyStatus.
type StatusNotice int

const (
	// NoticeToClient — la plataforma manda el aviso genérico del estado destino
	// (la plantilla del estado, o la de seña del tenant). Es la conducta del Plan
	// 041 y el CERO del tipo a propósito: el valor por descuido —un campo de
	// struct, un mapa, un decode sin inicializar— es el que habla, nunca el que
	// calla. Un silencio nace de una decisión escrita.
	NoticeToClient StatusNotice = iota
	// NoticeByCaller — el llamante YA le escribe al cliente por su cuenta y la
	// plataforma se calla en ESTA transición (D-044.49, decisión de producto del
	// 2026-08-27: al aprobar y al pedir información el cliente recibe UN SOLO
	// mensaje, el del dueño, porque su texto ya dice lo mismo y mejor). Vale 1.
	//
	// Se calla ENTERO, no solo el texto genérico: si el llamante anuncia la
	// transición, la plataforma no tiene nada que añadir por detrás.
	NoticeByCaller
)

// silences responde si esta política deja el aviso en manos del llamante. Está
// escrito como método —y no como `notice == NoticeByCaller` suelto en Service—
// para que la regla viva junto al tipo, igual que ShippingPolicy.applies.
// Era silencia en el viejo.
func (n StatusNotice) silences() bool { return n == NoticeByCaller }

// MessageSender empuja un texto por una sesión viva del Edge y espera su Ack. La
// firma es la del SendText del Gateway —la misma que ya declaran el runtime y la
// API pública—, así que el Gateway la satisface sin adaptador.
//
// El command_id NO es un parámetro: lo genera el Gateway al despachar y vuelve
// dentro del Ack (AckedCommandId) o dentro del error, cuando el error —o
// cualquiera de los que envuelve— tiene un método `CommandID() string`. Se pide
// por duck-typing y no por el tipo concreto para no acoplar el dominio al
// Gateway.
type MessageSender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// Destinations traduce el contact_id OPACO de la solicitud a una referencia
// direccionable. Lo satisface el resolver de contactos de la plataforma
// (contact.Resolver), que es la VÍA CUSTODIADA de PII: descifra el valor con la
// KEK que envolvió esa fila y lo devuelve solo en memoria (ADR-0017).
//
// Es la única grieta —consciente y acotada— en el «cero PII» de este paquete:
// `intakes` sigue sin descifrar nada por su cuenta y sin guardar el resultado. Se
// lo pide a quien tiene la custodia, lo pasa al MessageSender y lo suelta.
//
// El método se llama Destino porque así se llama en el puerto ya reconstruido de
// internal/nucleo/contact (E-11: lo ya escrito no se renombra).
type Destinations interface {
	Destino(ctx context.Context, tenantID, contactID string) (contact.Ref, error)
}

// NotifySettings es la config comercial del tenant que el mensaje necesita
// (tenant_settings, migración 0045).
type NotifySettings struct {
	// DepositTemplate es la plantilla de la SEÑA: datos de la cuenta e
	// instrucciones de pago que solo el tenant conoce. VACÍA —o solo espacios en
	// blanco, Unicode incluido— es el estado de arranque de cualquier tenant y
	// significa «este tenant no puede pedir seña» (COMMENT de la columna): sin
	// ella no se manda nada.
	DepositTemplate string
	// DepositDueDays es el plazo de la seña en días, para el marcador {plazo}.
	DepositDueDays int
}

// SettingsReader lee del tenant lo que el texto necesita. Lo satisfacen los dos
// almacenes del dominio: es la MISMA fila de tenant_settings de la que sale
// shipping_zones, así que no hay un segundo origen de config que mantener.
type SettingsReader interface {
	NotifySettings(ctx context.Context, tenantID string) (NotifySettings, error)
}

// commandIDCarrier lo satisface el error de un envío que ya tenía command_id
// asignado (el SendError del Gateway). Se pide por duck-typing y no por el tipo
// concreto para no acoplar el dominio al Gateway: cualquier transporte que sepa
// decir «este comando se llamaba así» encaja.
type commandIDCarrier interface{ CommandID() string }

// DefaultDepositDueDays espeja el DEFAULT de tenant_settings.deposit_due_days
// (migración 0045): 3. Vale cuando el tenant no tiene fila de config —un tenant
// sin configurar no es un error, es un tenant recién nacido— y cuando el plazo
// configurado no es positivo.
const DefaultDepositDueDays = 3

// Notifier le cuenta al CLIENTE que su solicitud cambió de estado, por la MISMA
// sesión de WhatsApp con la que la armó (Intake.SessionID). Satisface los dos
// puertos de salida del servicio (StatusNotifier y QuoteSender).
//
// Un *Notifier nil, o uno construido sin alguna dependencia que la salida
// necesita, NO rompe nada: no avisa, no registra y no entra en pánico. Qué
// dependencias necesita cada salida lo dice su comentario.
type Notifier struct {
	sender   MessageSender
	contacts Destinations
	settings SettingsReader
	log      logger.Logger
}

// NewNotifier construye el notificador sobre sus cuatro dependencias. Las cuatro
// son obligatorias para avisar: sin cualquiera de ellas no hay mensaje que mandar
// ni forma de contar que no se mandó. NO las valida ni las usa al construir: un
// Notifier a medias se construye igual y simplemente calla (el servicio acepta
// además un notificador nil y en ese caso no notifica).
func NewNotifier(sender MessageSender, contacts Destinations, settings SettingsReader, log logger.Logger) *Notifier {
	return &Notifier{sender: sender, contacts: contacts, settings: settings, log: log}
}

// NotifyStatus despacha el aviso de la transición `from` → `in.Status`. No
// devuelve error a propósito (regla 1 de la cabecera): todo fallo se registra
// aquí y muere aquí, pánicos incluidos.
//
// Necesita las CUATRO dependencias (y un receptor no nil); si falta alguna, no
// hace nada.
//
// El estado destino es NormalizeStatus(in.Status): el alias legado `closed`
// recibe el texto de `confirmed`. No se recorta ni se pasa a minúsculas: un
// estado con otra grafía es un estado sin plantilla.
//
//   - Destino `deposit_requested`: el texto es NotifySettings.DepositTemplate
//     renderizado con el plazo del tenant. DECISIÓN DE PRODUCTO (T4.2): sin
//     plantilla NO se manda nada —un «te pedimos una seña» sin decir dónde ni cómo
//     pagarla deja al cliente preguntando; es peor que el silencio— y queda un
//     Warn que nombra tenant_settings.deposit_template. La TRANSICIÓN se aplicó
//     igual: no notificar no es no registrar. Un fallo LEYENDO la config es una
//     avería (Error) y también acaba en silencio: se prefiere no mandar a mandar
//     un texto con marcadores sin rellenar.
//   - Cualquier otro destino: su plantilla del inventario, renderizada con
//     DefaultDepositDueDays como plazo. Sin plantilla NO se envía, y es el camino
//     normal de `abandoned` (Debug, no una anomalía). No lee la config del tenant.
//
// EL ORDEN es el que hace que no se mande un mensaje a medias: primero se decide
// SI hay algo que decir y se arma el texto entero, y solo entonces se toca la vía
// custodiada de PII. Un silencio no llama a Destinations.
//
// LA ENTREGA (la misma para todas las salidas de Notifier): Destino(tenantID,
// in.ContactID) → Ref.Sendable() → SendText(in.SessionID, destino, texto), una
// sola vez. Cada tropiezo se registra en Error y termina ahí:
//
//   - el resolver falla, o la Ref no es direccionable (p. ej. wa_username): no se
//     envía;
//   - SendText devuelve error: con el command_id que lleve el error ("" si no
//     lleva: un command_id inventado sería peor que ninguno);
//   - el Ack llega con Ok=false: NO es un éxito —para el cliente la consecuencia
//     es la misma, no le llegó nada—; Error con AckedCommandId y el error del
//     Edge, y ningún Info.
//
// El éxito se registra en Info con el command_id del Ack.
func (n *Notifier) NotifyStatus(ctx context.Context, tenantID string, in Intake, from string) {
	if n == nil || n.log == nil || n.sender == nil || n.contacts == nil || n.settings == nil {
		return // un notificador a medias no avisa, pero tampoco rompe nada
	}
	defer n.containPanic(in)

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

// containPanic es lo que hace ESTRUCTURAL la regla 1, y no solo una convención de
// firmas (ver la cabecera). NO es tragarse el defecto: se registra en Error con el
// pánico entero, que es donde hay que ir a buscarlo. Lo que se contiene es el
// ALCANCE del daño, no la noticia. Era contenerPánico en el viejo.
func (n *Notifier) containPanic(in Intake) {
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
// La decisión de producto (sin plantilla NO se manda nada) está en NotifyStatus.
func (n *Notifier) depositText(ctx context.Context, tenantID string, in Intake, log logger.Logger) (string, bool) {
	cfg, ok := n.depositSettings(ctx, tenantID, log, noTemplateOnDepositRequest)
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
// `consequence` es la CONSECUENCIA, y la trae el llamante porque no es la misma en
// los dos caminos: al pedir la seña, sin plantilla no sale NADA; al aprobar (T4.3),
// sale la cotización del dueño sola. Un texto fijo aquí le contaría al log de uno la
// consecuencia del otro — y un log que afirma lo que no pasó es la misma clase de
// defecto que un mensaje que afirma un estado que no es.
func (n *Notifier) depositSettings(ctx context.Context, tenantID string, log logger.Logger, consequence string) (NotifySettings, bool) {
	cfg, err := n.settings.NotifySettings(ctx, tenantID)
	if err != nil {
		log.Error("notificación: no se pudo leer la config del tenant", "error", err, "consecuencia", consequence)
		return NotifySettings{}, false
	}
	if strings.TrimSpace(cfg.DepositTemplate) == "" {
		log.Warn("notificación: el tenant no tiene plantilla de seña (tenant_settings.deposit_template); " +
			consequence)
		return NotifySettings{}, false
	}
	return cfg, true
}

// --- la cotización del DUEÑO (Plan 044 · T4.3, D-044.49 §1) ------------------
//
// Las salidas de abajo satisfacen QuoteSender y son la MISMA salida hacia WhatsApp
// que el aviso automático, con otro dueño del texto: aquí las palabras las pone la
// dueña del negocio y la plataforma solo adjunta lo que solo ella sabe (sus datos
// de pago) y lo entrega. Por eso reusan `deliver` entero —vía custodiada de PII,
// Ack y cero PII en los logs— en vez de abrir una segunda puerta hacia el Gateway.

// Las consecuencias de que un tenant no tenga configurada su plantilla de seña,
// una por camino. Ver depositSettings. Eran sinPlantillaAlPedirSeña,
// sinPlantillaAlRecordar y sinPlantillaAlAprobar en el viejo.
const (
	noTemplateOnDepositRequest = "la transición se aplicó pero al cliente no se le manda nada"
	noTemplateOnReminder       = "no se manda el recordatorio y la marca de «ya recordado» sigue libre"
	noTemplateOnApprove        = "se le manda la cotización del dueño sola, sin instrucciones de pago"
)

// Las dos ACCIONES del dueño que hablan por su cuenta, tal como se registran en el
// log. Son etiquetas de observabilidad y no del wire: lo que separan es «¿por qué
// salió este mensaje?» cuando alguien lea el log buscando un envío que no llegó.
// Eran accionAprobar, accionPedirInfo y claveAcciónDelLog en el viejo.
const (
	actionApprove     = "approve"
	actionRequestInfo = "request_info"
	logKeyAction      = "accion"
)

// quoteDepositSeparator es lo que va entre la cotización del dueño y la plantilla
// de seña: un renglón en blanco. Era separadorDeSeña (approve.go) en el viejo.
const quoteDepositSeparator = "\n\n"

// QuoteText compone la cotización ENTERA que va a salir —el texto del dueño más
// la plantilla de seña del tenant— y la devuelve para que el llamante la GUARDE
// antes de mandarla. No envía nada y no toca la vía custodiada de PII.
//
// El texto del dueño se devuelve TAL CUAL, byte a byte —no se recorta, no se
// normaliza y sus llaves NO se tratan como marcadores—, y la plantilla
// renderizada se pega detrás separada por un renglón en blanco ("\n\n"). Lo que
// se guarda en la revisión `approved` es exactamente esto.
//
// {total} y {plazo} de la plantilla se rellenan; {fecha_limite} se queda SIN
// sustituir cuando la solicitud no trae deposit_due_at, que es lo normal al
// aprobar: todavía no hay seña pedida.
//
// Solo necesita el log y el lector de config. Un notificador nil o sin alguno de
// los dos, un tenant sin plantilla (Warn) o un fallo leyendo su config (Error)
// devuelven ownerText SOLO. Es una respuesta COMPLETA: el cliente recibe su
// presupuesto; lo que falta es el «cómo pagar la seña», que este tenant no ha
// escrito.
func (n *Notifier) QuoteText(ctx context.Context, tenantID string, in Intake, ownerText string) string {
	if n == nil || n.log == nil || n.settings == nil {
		return ownerText
	}
	log := n.log.With("intake_id", in.ID, "tenant_id", tenantID, logKeyAction, actionApprove)
	cfg, ok := n.depositSettings(ctx, tenantID, log, noTemplateOnApprove)
	if !ok {
		return ownerText
	}
	// render rellena {total} y {plazo}; {fecha_limite} se queda SIN sustituir a
	// propósito, porque al aprobar todavía no hay seña pedida y por tanto no hay
	// deposit_due_at (ver la constante placeholderDueDate): un marcador visible
	// delata que la plantilla promete una fecha que este momento no tiene, y eso es
	// estrictamente mejor que estamparle al cliente una fecha inventada.
	return ownerText + quoteDepositSeparator + render(cfg.DepositTemplate, in, cfg.DepositDueDays)
}

// SendQuote entrega el texto YA compuesto (el de QuoteText) por la sesión de la
// solicitud, tal cual: no compone ni renderiza nada. No devuelve error y contiene
// el pánico (regla 1): cuando esto corre, la aprobación YA está escrita y
// numerada. La entrega es la de NotifyStatus.
//
// Necesita log, MessageSender y Destinations; NO necesita el lector de config. En
// el log el motivo va como accion=approve.
func (n *Notifier) SendQuote(ctx context.Context, tenantID string, in Intake, text string) {
	n.sendAsOwner(ctx, tenantID, in, text, actionApprove)
}

// SendQuestion entrega la PREGUNTA del dueño (T4.4). Es la misma entrega que
// SendQuote con otro motivo (accion=request_info), y no compone NADA: a una
// pregunta no se le adjunta la plantilla de seña.
func (n *Notifier) SendQuestion(ctx context.Context, tenantID string, in Intake, question string) {
	n.sendAsOwner(ctx, tenantID, in, question, actionRequestInfo)
}

// sendAsOwner es lo COMÚN de las dos salidas en las que habla la dueña: las
// guardas del notificador a medias, la contención del pánico, el contexto del log y la
// bajada a `deliver`. Existe como función porque lo único que distingue a las dos es
// la etiqueta del motivo, y dos copias del mismo bloque habrían divergido en el primer
// campo de log que alguien añadiera a una de ellas. Era enviarComoElDueño en el viejo.
func (n *Notifier) sendAsOwner(ctx context.Context, tenantID string, in Intake, text, action string) {
	if n == nil || n.log == nil || n.sender == nil || n.contacts == nil {
		return // un notificador a medias no avisa, pero tampoco rompe nada
	}
	defer n.containPanic(in)

	log := n.log.With(
		"intake_id", in.ID,
		"tenant_id", tenantID,
		"session_id", in.SessionID,
		"status_to", NormalizeStatus(in.Status),
		logKeyAction, action,
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

// NotifyCRMStatus avisa al cliente de que su pedido cambió de estado EN EL CRM
// del negocio (Plan 042 · T4.4). Reutiliza la entrega de NotifyStatus y cambia
// solo de dónde sale el texto: la plantilla del estado CRM del inventario.
//
// crmStatus se busca TAL CUAL, sin normalizar. Un estado sin plantilla no envía
// y, al revés que en NotifyStatus, es una ANOMALÍA (Warn), no un silencio normal:
// todo estado canónico tiene texto.
//
// NO devuelve error y contiene el pánico (regla 1): cuando esto corre, el reflejo
// YA está escrito. Un error hacia arriba haría que el puente reintentara y
// volviera a escribir lo mismo para nada.
//
// Solo se llama con un cambio REAL: un puente con reintentos manda el mismo
// estado muchas veces y el cliente no puede recibir un mensaje por reintento. Esa
// decisión vive en el llamante, que es quien sabe si la fila cambió.
//
// Necesita las CUATRO dependencias, igual que NotifyStatus, aunque NO lee la
// config del tenant (se conserva la guarda del paquete viejo).
func (n *Notifier) NotifyCRMStatus(ctx context.Context, tenantID string, in Intake, crmStatus string) {
	if n == nil || n.log == nil || n.sender == nil || n.contacts == nil || n.settings == nil {
		return // un notificador a medias no avisa, pero tampoco rompe nada
	}
	defer n.containPanic(in)

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
