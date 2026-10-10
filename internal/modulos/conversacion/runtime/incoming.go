// Porta internal/flujos/runtime/incoming.go @ e0159171

package runtime

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// incoming.go es el camino del ENTRANTE: lo que el motor hace con cada mensaje que el Gateway
// le entrega. En rojo declara sus dos puertas exportadas; el resto —1.300 líneas en el
// viejo— nace en el verde (F8-04b), que además lo PARTE POR TEMA para caber en E-13 (≤ 500
// líneas por fichero, con tolerancia hasta 600), solo moviendo declaraciones y con el sufijo
// del origen: guardas de borde, avance de la conversación viva, disparo y oferta, escape.
//
// # Los dos textos fijos de este fichero (byte a byte; las constantes nacen en el verde)
//
//   - defaultEscapeMessage, el aviso de escape cuando la regla no trae el suyo:
//     "Listo, cerramos esto. Escribe una palabra clave cuando quieras empezar de nuevo."
//   - defaultDurableSinkFailureNotice, el aviso de avería cuando un turno se corta (RT-10):
//     "No pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos."
//     Es un default de plataforma, sin configuración por tenant. Habla del pedido del
//     cliente, nunca de un error técnico: jamás un SQLSTATE ni un detalle de la base.
//
// # RT-11 · 🔴 El candado de rachas
//
// Todo borrado del estado (rt.store.Delete) va seguido, en el mismo bloque y a no más de 3
// sentencias, del cierre de su racha (rt.autoreplyStreaks.Close): el episodio muere con el
// estado. Hoy son 6 caminos en el paquete, cuatro de ellos de este fichero (viejo:
// incoming.go:239, el TTL del limbo; :450, la suelta del estado terminal; :489, la suelta del
// menú huérfano; :1077, el escape) y dos del plano de eventos (events.go:716 y :1221). Lo
// vigila el candado AST streak_invariante_test.go, cuya constante se re-mide sobre el código
// nuevo. Un entrante NO reinicia la racha; lo que la suma es cada emisión (send.go).

// OnIncoming es el gancho que el arranque asigna al Gateway para cada entrante de un Edge. Su
// firma, sin error, es la del gancho: sessionID es la sesión receptora y m el mensaje.
//
// RT-1 · NO BLOQUEA al llamante. El Gateway lo invoca de forma síncrona dentro del bucle
// Recv del stream del Edge, y procesar el entrante exige enviar y esperar un Ack que ese
// MISMO bucle tiene que entregar: hacerlo en línea sería un deadlock por sesión. OnIncoming
// lanza UNA goroutine por entrante y vuelve de inmediato, también cuando el procesamiento
// está parado esperando al Edge.
//
// Esa goroutine llama a HandleIncoming con un contexto NUEVO —desacoplado del stream, que ya
// volvió— y ACOTADO por el plazo del entrante (RT-3: 30 s por defecto, WithIncomingTimeout):
// un Edge mudo no puede retener para siempre el candado de la conversación.
//
// RT-2 · Semáforo de concurrencia (RT-3: 64 cupos por defecto, WithMaxConcurrentIncoming).
// El cupo se pide DENTRO de la goroutine, contra ese mismo plazo, y se devuelve al terminar.
// Si no hay cupo a tiempo, el entrante se DESCARTA sin llegar a HandleIncoming:
//
//   - se cuenta el motivo "saturation" en el hook de WithReactiveBlockedHook (es el único
//     motivo que no es una decisión sino una pérdida);
//   - se loguea a WARN, byte a byte, "runtime: entrante descartado por saturación (sin cupo
//     en el pool a tiempo)", con session_id y wa_message_id y SIN PII: ni el texto ni el
//     número.
//
// Sin semáforo (cupo negativo) no se acota ni se descarta nada.
//
// Un error de HandleIncoming NO se propaga ni provoca pánico: se loguea a ERROR «runtime:
// procesar entrante» con error, session_id y wa_message_id.
//
// La serialización por conversación la sigue dando HandleIncoming: dos entrantes de la misma
// conversación se procesan de uno en uno, en el orden en que tomen el candado.
func (rt *Runtime) OnIncoming(sessionID string, m *cloudlinkv1.IncomingMessage) {
	panic(pendiente.Implementar("runtime.Runtime.OnIncoming"))
}

// HandleIncoming procesa UN entrante de principio a fin, en la goroutine de quien llama y con
// su contexto. Devuelve nil cuando el entrante se atendió, se ignoró o se cortó por una
// guarda; un error solo cuando algo que no es best-effort falló (y entonces OnIncoming lo
// loguea).
//
// # 1 · Antes de tocar la conversación (en este orden)
//
// RT-5 · Dedupe PERSISTENTE (Plan 028 · T6). Con WithIngestDeduper y un wa_message_id no
// vacío, lo primero es IngestDeduper.Seen(session_id, wa_message_id). Ya visto → nil, sin
// resolver el tenant, sin tocar estado, sin efectos y sin responder (Debug «runtime: entrante
// duplicado ignorado (dedupe de ingesta)»). Cubre lo que la consecutiva no ve: el duplicado
// intercalado (A, B, A) y el reenvío de un entrante que dispara o escapa. Fail-open: si Seen
// falla se loguea a WARN «runtime: dedupe de ingesta falló; se continúa (fail-open)» y el
// entrante se procesa. Sin deduplicador, o con wa_message_id vacío, no se consulta.
//
// Tenant y perfil: TenantResolver.ResolveTenant(session_id). Si falla → error «runtime:
// resolver tenant: …» y nada más ocurre.
//
// RT-6 · Perfil PASIVO. Solo el literal "passive" corta; vacío o desconocido es activo. Una
// sesión pasiva no dispara, no avanza, no escapa y no responde, y el estado de una
// conversación en curso NO se borra (vuelve a avanzar si la sesión se reactiva). Se cuenta
// "passive" y se anuncia UNA vez por sesión a INFO («runtime: sesión passive; motor reactivo
// omitido — no auto-responderá mientras siga passive (se anuncia una vez por sesión; el
// resto en debug)», con session_id); los siguientes entrantes de esa sesión, a DEBUG
// («runtime: sesión passive; motor reactivo omitido»). Otra sesión pasiva se anuncia a su
// vez. Resultado: nil.
//
// RT-7 · Anti-self-loop (Plan 020 · T2, Plan 046 · T4.1). Con WithSelfNumbers y from_pn no
// vacío: el remitente se normaliza con contact.Normalize(contact.KindPhoneE164, …) —dígitos
// puros— y ESE valor es el que se pregunta a SelfNumberChecker.IsSelfNumber, por tenant. Si es
// propio: se cuenta "self_loop", WARN «runtime: entrante de un número propio del tenant;
// auto-respuesta evitada (anti-self-loop)» con tenant_id y session_id, y nil. Es conservadora
// hacia PROCESAR: sin checker, sin from_pn, con un número que no normaliza o con un error del
// checker (WARN «runtime: no se pudo comprobar si el remitente es un número propio del
// tenant; guarda anti-self-loop omitida») el entrante sigue. Nunca se loguea el número ni su
// índice ciego.
//
// Contacto: las referencias del entrante (contact.RefsFrom(from_pn, from_lid, from)) y su
// push_name se resuelven a un contact_id OPACO. El mismo contacto casa el MISMO estado llegue
// como número o como LID. Si falla → «runtime: resolver contacto: …».
//
// # 2 · Bajo el candado de la conversación (tenant, sesión, contacto)
//
// Primero se registra al contacto para la bienvenida (welcome.go, WL-3). Después se carga el
// estado («runtime: cargar estado: …» si falla) y se elige UNO de tres caminos:
//
//   - SIN estado (el limbo) → bienvenida si toca, y DISPARO (§3).
//   - Con estado y el reloj VENCIDO → se suelta, bienvenida si toca, y DISPARO (§3).
//   - Con estado vigente → AVANCE (§4).
//
// Los dos relojes son EXCLUYENTES (INV-18) y lo decide una sola cosa: si el estado tiene
// evento activo.
//
//   - CON evento activo manda el reloj del EVENTO y nadie más (events.go, EV-6; RT-15).
//   - SIN evento activo manda conversation_ttl_seconds, medido con el reloj del runtime contra
//     Conversation.UpdatedAt: vencido (estrictamente más que el TTL) se BORRA el estado y se
//     cierra su racha. Un TTL <= 0 o un UpdatedAt cero no vencen nunca. Un fallo al leer los
//     ajustes NO vence (WARN «runtime: no se pudo leer el TTL conversacional; no se vence el
//     estado»). En el limbo no se toca ningún evento.
//
// # 3 · Disparo: un entrante sin conversación viva
//
// Se pregunta a trigger.Resolver.Resolve con la señal {texto, tipo del evento activo}. La
// intención LLM del entrante NO viaja en la señal (el contrato dejó de traerla): es siempre
// nil. Un error del resolver se loguea a WARN «runtime: resolver de disparos falló; se ignora
// el entrante» y el resultado es nil.
//
//   - Ignore (y cualquier acción desconocida): nil, sin escribir ni enviar nada (decisión C;
//     es lo que da el resolver noop).
//   - Start (keyword) y StartEvent: con plano de eventos y event_kind, nace o se conmuta el
//     evento (events.go, EV-2 y EV-3) y ESE mensaje entra en el hilo y en la ventana de
//     captación. Si no —sin plano, o sin event_kind— arranque PLANO: se cobra un token (RT-8)
//     y se arranca el flujo sin evento (start.go). Sin flow_id no hay nada que arrancar
//     (Debug) y nil.
//   - Fallback: jamás pare evento, aunque la regla traiga event_kind. RT-12: con
//     WithOpeningBuilder y una oferta NO vacía se cobra un token, se guarda el menú de la
//     oferta pendiente (sin evento) y se envía su texto. Sin oferta, arranque plano del flujo
//     del fallback, como siempre.
//
// RT-9b · Degradación de un flujo durable (D-054.3). Si el arranque plano rebota con
// ErrDurableFlowNeedsEvent, el cliente NO ve un error: WARN «runtime: flujo con contenido
// durable no puede arrancar sin evento; se degrada a la oferta del despachador» (flow_id,
// node_type, origin = keyword | event | fallback, session_id) y se envía la oferta SIN cobrar
// un segundo token. Sin oferta a la que degradar, el contacto se queda sin respuesta (segundo
// WARN) y el resultado es nil: nunca reentra ni entra en bucle. BuildOpening se llama una
// sola vez por entrante.
//
// Un ErrConversationExists en el arranque plano es carrera benigna: Info y nil.
//
// # 4 · Avance: un entrante sobre una conversación viva (en este orden)
//
//  1. RT-9 · ESCAPE global. Si trigger.Resolver.IsEscape casa: se escribe el resumen del
//     abandono, se BORRA el estado, se cierra la racha, se emite event_escaped (si había
//     evento conocido, con reason = EscapeReasonClientEscape) y, SOLO ENTONCES, se cobra el token
//     del aviso. Sin cupo no se avisa, pero el escape YA ocurrió. El aviso es el `message` de
//     la regla o, si viene vacío, defaultEscapeMessage; entra al hilo como fuera de turno. Un
//     error de IsEscape no escapa ni corta: WARN «runtime: IsEscape falló; se ignora el
//     escape» y el avance sigue.
//  2. Navegación entre eventos (solo con plano de eventos): la elección del menú pendiente
//     y, después, trigger.Resolver.ResolveLive (event_start / event_stop). Un error de
//     ResolveLive es best-effort (WARN «runtime: ResolveLive falló; se ignora el salto de
//     evento»). Ver events.go, EV-3, EV-5 y EV-7.
//  3. Menú de salida (exit_menu.go).
//  4. Idempotencia CONSECUTIVA: mismo wa_message_id que el último procesado (no vacío) →
//     nil, sin avanzar ni reenviar.
//  5. Estado SIN flujo (el menú pendiente cuyo texto no era una opción): se borra el estado,
//     se cierra la racha, se emite event_escaped (EscapeReasonOrphanMenu) si el evento activo
//     seguía vivo, y el entrante se trata como DISPARO (§3) con el tipo de ese evento en la
//     señal.
//  6. Estado TERMINAL sin cierre pendiente: lo mismo (EscapeReasonOwnerFlowFinished).
//     Un contacto cuyo flujo ya terminó no se queda mudo. Con un cierre PENDIENTE
//     (event_lifecycle.go, LC-6) NO se suelta: sigue al paso 8 para reintentarlo.
//  7. Definición con la VERSIÓN con la que corría («runtime: definición en curso (v%d): …»).
//  8. Reanudación por módulo (resume.go) y, si no consumió el turno, engine.Step con el
//     texto («runtime: step: …»). Se estampa last_wa_message_id y se aplica el cierre natural.
//  9. Fan-out de los efectos del paso, ANTES del Save, con el evento que estaba activo antes
//     del cierre y Durable según el tipo del nodo que los produjo.
//  10. RT-4 · Save («runtime: guardar estado: …»), hilo del turno (thread.go), ventana de
//     captación y, por último, la respuesta: un token y el envío (send.go). Sin salidas no se
//     cobra ni se envía.
//
// RT-10 · Si el fan-out del paso 9 corta el turno (resume.go, FO-6): NO se guarda el estado
// avanzado, no se escribe el turno en el hilo, no entra en la ventana de captación, y el
// cliente recibe SOLO defaultDurableSinkFailureNotice, por el camino normal (con su token).
// ERROR «runtime: turno cortado: el sink durable no pudo materializar el efecto tras el
// reintento acotado». El resultado es el del envío del aviso.
//
// # 5 · Lo que cruza todos los caminos
//
// RT-8 · Un token del limitador ANTES de cada auto-envío: el arranque plano, el nacimiento o
// la conmuta de un evento, la oferta, el avance, el aviso de escape, la confirmación de un
// event_stop, el reinicio por reanudación, el rescate y la re-emisión del menú de salida.
// Agotado: no se responde, se cuenta "rate_limit" y se loguea a WARN solo con ids opacos. Sin
// limitador no hay tope. Ignore no gasta cuota; el recordatorio de la seña y la bienvenida,
// tampoco.
//
// RT-19 · Recordatorio de la seña. Con WithDepositReminder, tras pasar las guardas de borde y
// resolver el contacto, CADA entrante termina llamando a RemindContact(tenant, contacto): lo
// último, después de contestar y con el candado ya suelto. Pregunta por el contacto, no por
// la conversación (también con un «hola» sin estado). Los textos que devuelve se escriben en
// el hilo como fuera de turno (thread.go, TH-17). Una sesión pasiva o un número propio no
// llegan a evaluarlo.
//
// RT-20 · Hilo y bienvenida solo con `llm_intake`, fail-closed (thread.go, welcome.go).
//
// Ventana de captación: con WithAggregator, el mensaje del turno se ofrece al agregador en
// tres sitios —el avance normal, el mensaje que ABRE un evento por el disparador y el
// reinicio por reanudación—, siempre con el evento del turno y nunca en un turno cortado.
// Nunca dos veces por un mismo entrante. No devuelve error ni corta el turno.
//
// PII: ningún log de este camino lleva el texto del cliente, su número ni un identificador de
// evento que llegue al cliente; solo ids opacos.
func (rt *Runtime) HandleIncoming(ctx context.Context, sessionID string, m *cloudlinkv1.IncomingMessage) error {
	panic(pendiente.Implementar("runtime.Runtime.HandleIncoming"))
}
