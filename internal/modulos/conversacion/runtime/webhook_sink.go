// Porta internal/flujos/runtime/webhook_sink.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/crmpush"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// WebhookSink es la PUERTA CONVERSACIONAL del puente CRM (Plan 042 · Ola 3 ·
// T3.2/T2.4): traduce el cierre de carrito a la forma del contrato y se lo pasa a
// crmpush, que es quien evalúa el gate y hace el INSERT en webhook_outbox.
//
// 🔄 LA REGLA NO VIVE AQUÍ (Plan 044 · Ola 4 · Tanda 2): el gate, la plantilla del
// contrato y el encolado son de crmpush (una regla, dos puertas: esta y la de las
// revisiones por HTTP). Lo que queda en este fichero es lo único que es de verdad del
// motor: sacar del EffectContext y del efecto los datos que el contrato pide.
//
// 🔴 SOLO ENCOLA (INV-02). NUNCA hace POST — ese es el worker de integraciones, que
// corre en otra goroutine y completa buyer_data, variables{} y customer_note justo
// antes de entregar (D-042.9/D-042.11). Ni este fichero ni crmpush importan net/http
// a propósito: es la garantía estructural de que el sink jamás bloquea el mensaje
// entrante con una llamada de red. Tampoco consulta la base: lo que necesita lo lee
// del efecto.
//
// Hereda el contrato de EventSink: no bloquea indefinidamente (una consulta al gate y
// un solo INSERT), NUNCA filtra PII ni credenciales, y NUNCA aborta el avance del
// flujo (Handle devuelve nil siempre).
//
// En el rojo no lleva campos. El verde le pone cuatro: el logger, el encolador de
// crmpush, el nombre del efecto que entrega y si se construyó con sus dos dependencias.
type WebhookSink struct{}

// WebhookQueuer es lo mínimo que el sink necesita del almacén de integraciones
// (interfaz local, ISP): un solo INSERT en webhook_outbox. Lo satisface el almacén
// Postgres de integraciones.
//
// Se conserva aquí —en vez de exigirle al arranque el tipo de crmpush— porque es la
// firma que este paquete publica en su cableado desde el Plan 042; es un ALIAS de
// crmpush.Queuer, así que quien satisface una satisface la otra sin adaptador.
type WebhookQueuer = crmpush.Queuer

// WebhookGate decide si un tenant tiene el puente CRM activo AHORA MISMO (D-042.8:
// entitlement `crm_bridge` + tenant_integrations con events_adapter='webhook' y
// enabled=true — mecánica ADR-0022). La implementación real se cablea en el arranque.
// Es un ALIAS de crmpush.Gate.
type WebhookGate = crmpush.Gate

// NewWebhookSink construye el sink. Nunca devuelve nil.
//
//   - log: el logger del sink y de la regla de crmpush que lleva dentro. Con nil, el
//     sink queda mudo e inerte (ver Handle).
//   - deliverEffect: el NOMBRE del efecto que este sink entrega al puente. Se
//     INYECTA —no se escribe aquí el literal de un módulo (Plan 027 · Ola 3 · T8)—:
//     el arranque lo cablea con cart.EffectCartClosed ("cart_closed").
//   - queuer y gate: pueden ser nil en los tests que solo ejercitan el camino «no
//     entrega». Si falta CUALQUIERA de los dos, el sink es un no-op seguro (ver
//     Handle).
//
// No consulta el gate ni encola al construir. El reloj del `timestamp` del contrato
// es el de crmpush por defecto (time.Now().UTC()): este constructor no lo inyecta,
// igual que el viejo.
func NewWebhookSink(log logger.Logger, deliverEffect string, queuer WebhookQueuer, gate WebhookGate) *WebhookSink {
	panic(pendiente.Implementar("runtime.NewWebhookSink"))
}

// Phase implementa PhasedSink: este sink corre en PhaseNotify, DESPUÉS de toda la
// proyección (Plan 042 · Ola 3.1, RT-18). No es una preferencia, es un requisito de
// corrección: `intake_id` no existe hasta que el proyector del carrito lo genera y lo
// anota en eff.Payload, y este sink lo LEE. Declararlo aquí hace que el orden lo
// garantice el runtime y no el orden de las líneas del arranque.
//
// Devuelve PhaseNotify siempre, también sobre un receptor nil.
func (s *WebhookSink) Phase() SinkPhase {
	panic(pendiente.Implementar("runtime.WebhookSink.Phase"))
}

// Handle traduce el efecto que este sink entrega (hoy cart_closed) y se lo pasa a
// crmpush para que lo ENCOLE. Devuelve nil SIEMPRE: notificar al puente jamás cuelga
// ni corta la respuesta que el cliente está esperando por WhatsApp. (La otra puerta
// del mismo empuje, la HTTP, decide distinto, y por eso esa decisión no está dentro
// de crmpush.)
//
// # Cuándo no hace nada
//
// En estos casos devuelve nil sin consultar el gate, sin encolar y sin escribir una
// sola línea de log:
//
//   - receptor nil, valor cero o sink construido con logger nil;
//   - el nombre del efecto (eff.Name) no es EXACTAMENTE el deliverEffect: la
//     navegación y la telemetría (category_selected, item_added…) no se entregan — un
//     CRM solo quiere el cierre;
//   - sink construido sin queuer o sin gate (cualquiera de los dos nil).
//
// # Cuándo encola
//
// Consulta el gate UNA vez, con ec.TenantID y el mismo ctx que recibió:
//
//   - gate cerrado (el tenant no tiene el puente activo): no encola. No es una
//     avería y el sink no loguea nada en Error ni en Warn (crmpush lo deja en Debug
//     con su motivo).
//   - gate en error: fail-closed, no encola; escribe en Error la línea de fallo.
//   - gate abierto: encola EXACTAMENTE una fila, con el tenant ec.TenantID, el kind
//     "intake.push" y el documento del contrato en JSON; y escribe en Debug el
//     mensaje literal "webhook: intake.push encolado" con las claves "tenant",
//     "outbox_id" (el id que devolvió el almacén) e "intake_id".
//   - el almacén falla al encolar: escribe en Error la línea de fallo.
//
// La línea de fallo es la MISMA para el gate y para el almacén (el viejo no las
// distingue): mensaje literal "webhook: no se pudo encolar intake.push" con las
// claves "error" (el error de crmpush, que envuelve el de origen), "tenant" y "name"
// (el nombre del efecto).
//
// # Qué lleva el documento encolado
//
// Las diez claves del contrato wapp-crm-v1 · intake.push y ninguna más:
// contract_version, verb, tenant, contact, intake_id, lifecycle_status, revision_no,
// items, total y timestamp. De dónde sale cada dato:
//
//   - tenant = ec.TenantID; contact = ec.ContactID (opaco). Ni la sesión, ni el
//     flujo, ni el evento del EffectContext viajan.
//   - intake_id, revision_no y lifecycle_status se LEEN de eff.Payload: los anota el
//     proyector del carrito DESPUÉS de proyectar, en el MISMO mapa que ve este sink.
//     Sin esas anotaciones no habría forma de correlacionar sin una consulta extra,
//     que el sink NO hace. 🔴 Los tres son DATOS, no constantes:
//   - intake_id: la cadena del payload; si falta o no es una cadena, "" — y se
//     encola igual: el sink no lo inventa ni lo busca (por eso la fase importa).
//   - revision_no: el número del payload, llegue como int, int64 o float64 (la forma
//     en proceso y la del round-trip JSON dan el mismo número). El sink no lo
//     corrige, ni lo acota, ni lo reordena. AUSENTE ⇒ 0, y se encola igual: el cero
//     es el único valor que el schema rechaza (`minimum: 1`), así que no puede
//     confundirse con una revisión legítima. NUNCA un 1 de respaldo: un número FALSO
//     es peor que uno ausente (el puente hace UPSERT por (intake_id, revision_no)).
//   - lifecycle_status: llega CRUDO —la clave legada `closed` con la que el carrito
//     escribe la fila— y sale normalizado por crmpush: `closed` ⇒ `confirmed`. El
//     contrato JAMÁS emite `closed`.
//   - items: una entrada por línea de eff.Payload["items"], en su orden, con sku,
//     label, customization, qty y unit_price. Se toleran las DOS formas del payload:
//     la de en proceso ([]map[string]any) y la del round-trip JSON ([]any de mapas,
//     números float64); en la segunda, lo que no sea un mapa se salta. Sin la clave,
//     o con otra forma, `items` sale como lista vacía (`[]`), no null. La
//     personalización de LÍNEA sí viaja (D-041.17); una línea sin ella sale con
//     customization "". Personalizar no mueve el dinero (INV-13).
//   - total = eff.Payload["total"], numérico.
//   - timestamp: el instante del encolado, RFC3339 en UTC, del reloj de crmpush.
//   - event_history_id NO aparece: este sink no lo rellena y el contrato lo omite
//     mientras esté vacío.
//
// # PII: lo que NO sale por aquí
//
//   - 🔴 customer_note NO se lee aunque esté en el efecto (el de cierre la trae: el
//     proyector la necesita para escribir intakes.customer_note). Ni el texto ni la
//     clave aparecen en el documento encolado: congelarla lo dejaría en claro en
//     webhook_outbox, una tabla que sobrevive a la entrega y que nadie poda. La
//     completa el worker justo antes del POST.
//   - buyer_data y variables tampoco: ni la clave ni sus valores (los completa el
//     worker; aquí serían cripto y consulta en línea con el mensaje, INV-02).
//   - Ninguna línea de log, en ningún nivel, lleva la nota, los datos del comprador
//     ni el payload: solo tenant, nombre del efecto, outbox_id e intake_id.
//   - El sink NO MUTA eff.Payload: sacar la nota de la plantilla no se hace
//     borrándola del efecto, que comparten todos los sinks del fan-out.
func (s *WebhookSink) Handle(ctx context.Context, ec EffectContext, eff modules.Effect) error {
	panic(pendiente.Implementar("runtime.WebhookSink.Handle"))
}
