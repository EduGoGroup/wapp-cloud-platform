// Porta internal/intakes/notifier.go @ 64c181a

package intakes

// Las plantillas que ve el cliente y su render. Se partió de notifier.go por
// tamaño (E-13) solo moviendo declaraciones: el inventario normativo —qué texto
// lleva cada estado y qué hace cada marcador— está en la cabecera de notifier.go.
// placeholderDueDate era placeholderFechaLímite en el viejo.

import (
	"fmt"
	"strconv"
	"strings"
)

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
	placeholderTotal   = "{total}"
	placeholderPlazo   = "{plazo}"
	placeholderDueDate = "{fecha_limite}"
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
			placeholderDueDate, in.DepositDueAt.UTC().Format(dueDateLayout))
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
