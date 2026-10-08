// Porta internal/intakes/notifier.go @ 64c181a

package intakes

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type Notifier struct{}

// NewNotifier construye el notificador sobre sus cuatro dependencias. Las cuatro
// son obligatorias para avisar: sin cualquiera de ellas no hay mensaje que mandar
// ni forma de contar que no se mandó. NO las valida ni las usa al construir: un
// Notifier a medias se construye igual y simplemente calla (el servicio acepta
// además un notificador nil y en ese caso no notifica).
func NewNotifier(sender MessageSender, contacts Destinations, settings SettingsReader, log logger.Logger) *Notifier {
	panic(pendiente.Implementar("intakes.NewNotifier"))
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
	panic(pendiente.Implementar("intakes.Notifier.NotifyStatus"))
}

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
	panic(pendiente.Implementar("intakes.Notifier.QuoteText"))
}

// SendQuote entrega el texto YA compuesto (el de QuoteText) por la sesión de la
// solicitud, tal cual: no compone ni renderiza nada. No devuelve error y contiene
// el pánico (regla 1): cuando esto corre, la aprobación YA está escrita y
// numerada. La entrega es la de NotifyStatus.
//
// Necesita log, MessageSender y Destinations; NO necesita el lector de config. En
// el log el motivo va como accion=approve.
func (n *Notifier) SendQuote(ctx context.Context, tenantID string, in Intake, text string) {
	panic(pendiente.Implementar("intakes.Notifier.SendQuote"))
}

// SendQuestion entrega la PREGUNTA del dueño (T4.4). Es la misma entrega que
// SendQuote con otro motivo (accion=request_info), y no compone NADA: a una
// pregunta no se le adjunta la plantilla de seña.
func (n *Notifier) SendQuestion(ctx context.Context, tenantID string, in Intake, question string) {
	panic(pendiente.Implementar("intakes.Notifier.SendQuestion"))
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
	panic(pendiente.Implementar("intakes.Notifier.NotifyCRMStatus"))
}
