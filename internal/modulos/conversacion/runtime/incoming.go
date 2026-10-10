// Porta internal/flujos/runtime/incoming.go @ e0159171

package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// incoming.go es el camino del ENTRANTE: lo que el motor hace con cada mensaje que el Gateway
// le entrega. Va PARTIDO POR TEMA para caber en E-13 (≤ 500 líneas por fichero, con tolerancia
// hasta 600), solo moviendo declaraciones y con el sufijo del origen:
//
//   - incoming.go         — las dos puertas exportadas, los dos textos fijos y los dos relojes;
//   - incoming_guards.go  — lo que pasa ANTES de tocar la conversación (§1: RT-5, RT-6, RT-7);
//   - incoming_trigger.go — el disparo y la oferta (§3);
//   - incoming_advance.go — el avance de la conversación viva, sus sueltas y el escape (§4);
//   - incoming_limiter.go — lo que cruza todos los caminos (§5: RT-8, RT-19).
//
// # Los dos textos fijos de este fichero (byte a byte)
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

// defaultEscapeMessage es el aviso corto que se envía al cortar una conversación
// viva por escape global (Plan 019 · T4) cuando la regla de escape que casó NO
// define un aviso propio. Si la regla trae message (columna flow_triggers.message,
// Plan 019 · T4b), handleEscape lo usa en su lugar.
const defaultEscapeMessage = "Listo, cerramos esto. Escribe una palabra clave cuando quieras empezar de nuevo."

// defaultDurableSinkFailureNotice es el aviso EXPLÍCITO que recibe el cliente
// cuando el turno se corta por D-054.4 (Plan 054 · T3): el sink que MATERIALIZA
// contenido durable (la proyección a intakes/survey_results) agotó su reintento
// acotado. Es un DEFAULT DE PLATAFORMA, deliberadamente sin superficie de
// configuración por tenant (no la pidió nadie; ampliarla no es parte de este
// plan) — mismo estatus que defaultEscapeMessage, arriba. Dice qué pasó en
// términos del cliente (su pedido, no un error técnico) y qué puede hacer
// (reintentar), nunca un SQLSTATE ni un detalle de Postgres.
const defaultDurableSinkFailureNotice = "No pudimos registrar tu pedido. Por favor, intenta de nuevo en unos minutos."

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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), rt.incomingTimeout)
		defer cancel()
		// Semáforo de concurrencia (Plan 027 · Ola 1 · T5, cierra H5): se adquiere el
		// cupo DENTRO de la goroutine para no bloquear el loop Recv del stream. Si no
		// hay cupo dentro del incomingTimeout, se descarta el entrante con log (sin
		// PII): bajo saturación sostenida es preferible soltar uno a acumular
		// goroutines colgadas sin techo. Sin semáforo (incomingSem nil) no acota.
		//
		// El descarte se CUENTA además de loguearse: es el único camino del contador
		// que no responde a una política sino a una pérdida —el mensaje debía entrar al
		// motor y se tiró—, y sin contador solo se sabía leyendo logs a mano.
		if rt.incomingSem != nil {
			select {
			case rt.incomingSem <- struct{}{}:
				defer func() { <-rt.incomingSem }()
			case <-ctx.Done():
				rt.countReactiveBlocked(reasonSaturation)
				rt.log.Warn("runtime: entrante descartado por saturación (sin cupo en el pool a tiempo)",
					"session_id", sessionID, "wa_message_id", m.GetWaMessageId())
				return
			}
		}
		if err := rt.HandleIncoming(ctx, sessionID, m); err != nil {
			rt.log.Error("runtime: procesar entrante",
				"error", err,
				"session_id", sessionID,
				"wa_message_id", m.GetWaMessageId(),
			)
		}
	}()
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
	// Dedupe PERSISTENTE de ingesta (Plan 028 · T6, ADR-0003): un reenvío del outbox
	// del Edge se corta ANTES de tocar el motor. Ver duplicateIngest.
	if rt.duplicateIngest(ctx, sessionID, m) {
		return nil
	}
	tenantID, profile, err := rt.resolver.ResolveTenant(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("runtime: resolver tenant: %w", err)
	}
	// Guardas de BORDE del motor reactivo (Plan 020 · T1 passive + T2 anti-self-loop):
	// se cortan ANTES de resolver contacto, tomar el keyedMutex o cargar estado; la
	// escucha y los acuses (vía separada del Gateway) no se ven afectados.
	if rt.reactiveBlocked(ctx, tenantID, sessionID, profile, m.GetFromPn()) {
		return nil
	}
	// Resuelve la identidad enriquecida del entrante (from_pn/from_lid, con
	// fallback al JID crudo) a un contact_id OPACO antes de clavar la key: así el
	// mismo contacto casa el MISMO estado aunque el JID llegue como número o LID
	// (Plan 010, design.md §5, §6).
	refs := contact.RefsFrom(m.GetFromPn(), m.GetFromLid(), m.GetFrom())
	contactID, err := rt.contacts.Resolve(ctx, tenantID, refs, m.GetPushName())
	if err != nil {
		return fmt.Errorf("runtime: resolver contacto: %w", err)
	}
	// TOQUE del recordatorio de la seña (Plan 041 · T4.4): el cliente acaba de
	// hablar. Va en DEFER —y registrado ANTES del candado, así que corre el último—
	// para que ocurra después de haberle contestado y con la clave ya libre: primero
	// se atiende lo que la persona vino a hacer, y solo entonces se le recuerda lo que
	// debe. Sin cablear (deposits nil) esto no existe.
	//
	// `depositEventID` es una VARIABLE CAPTURADA, y esa indirección es el precio de
	// que el defer se registre antes de saber a qué evento pertenece el turno (Plan
	// 044 · T1.6): el estado —y con él el puntero de evento— no se ha cargado
	// todavía. Se rellena más abajo, cuando se sabe, y solo en el camino en que el
	// evento SIGUE siendo el activo. Queda en "" en los dos caminos en que no lo es
	// (sin conversación viva, o reloj vencido que soltó la conversación), y entonces
	// el recordatorio no deja fila — ver touchDeposit.
	var depositEventID string
	defer func() { rt.touchDeposit(ctx, tenantID, sessionID, contactID, depositEventID) }()
	key := store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	unlock := rt.locks.lock(key)
	defer unlock()

	// LA BIENVENIDA ÚNICA, primera mitad (Plan 044 · T1.8-2, D6): registrar que el
	// contacto acaba de hablar y traerse la marca previa. Va AQUÍ —en CADA entrante,
	// dentro del candado y antes de cargar el estado— porque el umbral de silencio se
	// mide contra el ÚLTIMO mensaje del contacto, no contra el último que abrió
	// conversación: tocando solo en los turnos que saludan, una conversación que lleva
	// horas hablando parecería llevar horas callada.
	//
	// Es una sentencia y ninguna lectura de config; sin la feature `llm_intake` del
	// tenant (o sin WithWelcomeStore) no escribe nada. La SEGUNDA mitad —decidir y
	// mandar— vive en los dos caminos de abajo, y su docstring explica por qué en esos
	// dos y no aquí. Ver welcome.go.
	welcome := rt.observeWelcome(ctx, tenantID, key)

	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: cargar estado: %w", err)
	}
	if !ok {
		// Sin conversación viva: consulta el resolver de disparos (Plan 019 · T3).
		// Con NoopResolver (default) devuelve Ignore ⇒ return nil idéntico a la
		// decisión C histórica (INV-6). El contexto (tenantID, contactID, key,
		// sessionID) ya está resuelto ⇒ se arranca sin re-resolver el contacto. La
		// Signal lleva el texto y, si el tenant tiene la feature, la intención LLM. Sin
		// flow_state no hay puntero de evento ⇒ activeEventKind="" (Plan 043 · T5.3).
		//
		// LA BIENVENIDA, segunda mitad — CAMINO 1 DE 2: EL LIMBO. No hay conversación
		// viva, así que este mensaje no avanza nada y no hay ningún menú que pisar. Va
		// ANTES del handleTrigger porque es un ACUSE DE RECIBO: primero «lo recibimos»,
		// después lo que el motor tenga que contestar. Es best-effort y no devuelve
		// error: nada de aquí puede tumbar el turno del cliente (welcome.go).
		rt.welcomeIfDue(ctx, tenantID, sessionID, contactID, key, welcome)
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, ""), m)
	}
	// EL reloj de esta conversación (Plan 043 · T3.1/T3.2/T3.7). Va ANTES de IsEscape /
	// consecutiveReplay / prepareResume: un estado que ya no vale no debe escapar ni
	// avanzar. Devuelve si el entrante se trata como ARRANQUE NUEVO en vez de avance.
	restart, err := rt.conversationClock(ctx, tenantID, key, st)
	if err != nil {
		return err
	}
	if restart {
		// El reloj del evento venció y releaseForNewConversation ya borró el estado
		// (T3.2/E-6): el evento sigue `open` pero YA NO es el activo ⇒
		// activeEventKind="" (Plan 043 · T5.3). Atarlo aquí reintroduciría la atadura
		// que T3.2 quita.
		//
		// LA BIENVENIDA, segunda mitad — CAMINO 2 DE 2: EL REINICIO. El reloj venció y
		// la conversación acaba de soltarse: para el cliente esto es el primer mensaje
		// de una conversación nueva, exactamente igual que el LIMBO de arriba, y el
		// menú que hubiera ya no existe. Omitirlo aquí dejaría sin bienvenida justo el
		// caso de «volvió tras el silencio», que es la mitad del enunciado.
		rt.welcomeIfDue(ctx, tenantID, sessionID, contactID, key, welcome)
		return rt.handleTrigger(ctx, tenantID, sessionID, key, contactID, rt.buildSignal(ctx, tenantID, m, ""), m)
	}
	// El turno avanza sobre el evento que el estado apuntaba: ES el activo, y es el
	// hilo al que pertenece un recordatorio de seña disparado por este entrante
	// (Plan 044 · T1.6). Se fija AQUÍ y no antes porque hasta el reloj no se sabía si
	// el evento seguía gobernando la conversación.
	depositEventID = st.EventID
	return rt.advanceLive(ctx, tenantID, sessionID, key, contactID, st, m)
}

// conversationClock aplica el reloj que gobierna esta conversación y dice si el
// entrante debe tratarse como un arranque nuevo (true) o como un avance (false).
//
// Hay DOS relojes y son EXCLUYENTES (REQ-01c, INV-18, D-043.16). Cuál manda lo
// decide una sola cosa: si la conversación tiene evento activo.
//
//   - CON evento activo manda event_inactivity_ttl_seconds y NADIE más (T3.1/T3.2):
//     dentro de la ventana el entrante REFRESCA el reloj; vencida, se suelta la
//     conversación —el evento sigue `open`— y el texto entra como uno nuevo.
//   - SIN evento activo —el LIMBO: un saludo, la cháchara, un menú a medias— manda
//     conversation_ttl_seconds, que es recolección de basura del flow_state y nada
//     más (Plan 029 · T9).
//
// La guarda es la tarea (T3.7): hasta la Ola 3, el TTL conversacional se evaluaba
// SIEMPRE, también con un pedido en curso, y descartaba su estado por un reloj
// pensado para otra cosa. Lo que aquella tarea cambió es CUÁNDO se pregunta, y esa
// guarda sigue intacta.
//
// 🔧 CORREGIDO POR T4.4 (Plan 046, D-046.12): esta cabecera decía además «no se toca
// la migración 0034 ni su default 0», y ya NO es cierto. El default de
// conversation_ttl_seconds pasó de 0 a 7200 (migración 0067) y el espejo en Go lo
// acompaña (store.DefaultConversationTTL), porque el 0 significaba «sin vencimiento»
// y dejaba el flow_state —con el texto literal del cliente— sin caducar nunca.
// 🔴 Lo que NO cambió es la SUBORDINACIÓN, que es lo que esta función implementa: con
// evento activo manda event_inactivity_ttl_seconds y este reloj no se evalúa. Que los
// dos valgan ahora 7200 NO los colapsa en una sola clave (ADR-0029 §E-9.2).
func (rt *Runtime) conversationClock(ctx context.Context, tenantID string, key store.Key, st model.Conversation) (bool, error) {
	if st.EventID != "" {
		return rt.eventClock(ctx, tenantID, key, st.EventID)
	}
	// El limbo no toca conversation_events: aquí no se lee ni se escribe ningún
	// last_activity_at (T3.7.2). El reloj del evento empieza a contar cuando el evento
	// NACE, no cuando el contacto saludó.
	if !rt.conversationExpired(ctx, tenantID, st) {
		return false, nil
	}
	if err := rt.store.Delete(ctx, key); err != nil {
		return false, fmt.Errorf("runtime: cerrar conversación vencida (TTL): %w", err)
	}
	// FIN DE EPISODIO de la racha de auto-respuestas (Plan 049 · Opción A). El estado
	// conversacional acaba de destruirse, así que la racha que venía contándose para
	// esta clave TERMINA aquí: se reporta su longitud al histograma y se olvida. Va
	// DESPUÉS del Delete —solo se cierra lo que de verdad murió— y es idempotente y
	// nil-safe, así que puede colgar de los seis caminos de destrucción sin llevar la
	// cuenta de cuál llegó primero. Qué es una racha, por qué se mide el EPISODIO y
	// por qué un entrante NO lo cierra: ver la cabecera de streak.go.
	rt.autoreplyStreaks.Close(key, rt.now())
	return true, nil
}

// conversationExpired decide si un estado vivo venció por el TTL conversacional del
// tenant (Plan 029 · T9). Lee conversation_ttl_seconds de tenant_settings (mismo
// store/camino que page_size/order_ttl); ttl<=0 ⇒ nunca vence (tenants sin configurar
// intactos). Un fallo de settings es best-effort: se loguea y devuelve false (no
// vence — se prefiere no descartar una conversación por un fallo transitorio). La
// comparación usa rt.now() (inyectable en tests) contra st.UpdatedAt (lo estampa el
// store en cada Save). Con UpdatedAt cero (estado sin marca) no vence.
//
// Desde el Plan 043 · T3.7 esto SOLO se pregunta cuando la conversación no tiene
// evento activo: lo garantiza conversationClock, su único llamante. No es un detalle
// de orden — este TTL es recolección de basura del limbo, y evaluarlo sobre un pedido
// en curso descartaba el estado de un evento vivo por un reloj que no es el suyo
// (REQ-01c, INV-18). Si vuelves a llamarlo sin mirar st.EventID, reabres eso.
func (rt *Runtime) conversationExpired(ctx context.Context, tenantID string, st model.Conversation) bool {
	settings, err := rt.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		rt.log.Warn("runtime: no se pudo leer el TTL conversacional; no se vence el estado",
			"error", err, "tenant_id", tenantID)
		return false
	}
	if settings.ConversationTTL <= 0 || st.UpdatedAt.IsZero() {
		return false
	}
	return rt.now().Sub(st.UpdatedAt) > settings.ConversationTTL
}
