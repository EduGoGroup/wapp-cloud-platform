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
	cfg, ok := n.depositSettings(ctx, tenantID, log)
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
func (n *Notifier) depositSettings(ctx context.Context, tenantID string, log logger.Logger) (NotifySettings, bool) {
	cfg, err := n.settings.NotifySettings(ctx, tenantID)
	if err != nil {
		log.Error("notificación: no se pudo leer la config del tenant; no se envía", "error", err)
		return NotifySettings{}, false
	}
	if strings.TrimSpace(cfg.DepositTemplate) == "" {
		log.Warn("notificación: el tenant no tiene plantilla de seña (tenant_settings.deposit_template); " +
			"la transición se aplicó pero al cliente no se le manda nada")
		return NotifySettings{}, false
	}
	return cfg, true
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
