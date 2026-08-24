// thread.go — los productores de filas de TEXTO LITERAL del hilo del evento
// (Plan 043 · Ola 4.5 · T4.5.7b, D-043.23; Plan 044 · T1.6, D-044.24). El literal
// —lo que el cliente escribió, lo que el negocio contestó y lo que le dijimos sin
// que preguntara— persiste vía AppendMessage / AppendOutOfTurnMessage (cifrado,
// nivel 2 del ADR-0034) si y solo si el tenant tiene la feature `llm_intake`.
//
// # UN SOLO GATE, Y ES POR TENANT
//
// Del 2026-08-10 al 2026-08-22 hubo DOS condiciones: la feature Y un INTERRUPTOR DE
// DESPLIEGUE, un booleano de config con su variable de entorno. Era andamiaje
// declarado: el productor escribía sin que existiera nadie que lo leyera, así que se
// apagó el parque entero desde una variable «hasta que el Plan 044 (su LECTOR)
// exista; se quita entonces». El Plan 044 · T1.6 es ese entonces, y lo retiró
// ENTERO — el campo, la Option, la lectura de config y la variable—, de modo que sus
// nombres solo existen ya en la historia de git (así lo exige su criterio (a): cero
// ocurrencias en el árbol).
//
// 🔴 LO QUE SE FUE ES EL INTERRUPTOR, NO EL GATE. Sigue habiendo fail-closed y sigue
// siendo por tenant: sin `llm_intake`, CERO filas — sin resolver cableado, con fallo
// del resolver o sin la feature, no se persiste nada. Un fallo transitorio no abre
// una capacidad de pago.
//
// # DOS PRODUCTORES AQUÍ, Y UN TERCERO QUE NO PASA POR ESTA PUERTA
//
//   - persistTurnMessages — el TURNO: lo que el cliente escribió y lo que el flujo
//     contestó, en el orden de la conversación. entry_kind='message'. Tiene DOS
//     llamantes desde el 2026-08-22 y no uno: advanceLiveStep (el turno sobre una
//     conversación viva) y persistOpeningTurn (el turno que ABRE el evento, vía
//     startLocked). El segundo NO es un productor nuevo —es este mismo, con la guarda
//     de camino que impide que las otras puertas de arranque escriban—; el defecto
//     que cierra y la razón de la guarda están en su docstring.
//   - persistOutOfTurnMessage — el SALIENTE FUERA DE TURNO (D-044.24): lo que
//     mandamos sin que naciera de un entrante. entry_kind='message_out_of_turn'.
//   - `decision` es OTRA puerta del mismo EventStore (persist_sink.go,
//     PersistSink.WithDecisionThread) y NUNCA pasó por aquí: no lo gobernaba el
//     interruptor que se acaba de retirar y no lo gobierna nada de este fichero.
//     Sigue escribiendo igual (INV-11: cada voz por su puerta).
package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// featureThreadMessages es la ÚNICA condición que enciende los productores de texto
// literal del hilo (D-043.23, tabla de productores; Plan 044 · T1.6). Es la clave
// del pipeline LLM del Plan 044 — `llm_intake`, que YA existe en el registro de
// entitlements: la constante Go (entitlements.FeatureLLMIntake) y su siembra en
// plan_features (0039_seed_plan_taxonomy.sql: planes `advisor_ai_pro` y `pro`). No
// se siembra nada nuevo aquí: un tenant sin esos planes tiene el gate APAGADO por
// defecto, y lo enciende contratar el plan — que desde T1.6 vuelve a bastar por sí
// solo, como fue hasta el 2026-08-10.
//
// 🔴 «ÚNICA» INCLUYE A `api_llm` (ADR-0044, D-044.28). Un tenant con `llm_intake` y
// sin vía API contratada archiva su hilo EXACTAMENTE igual: el texto se guarda
// cifrado por AppendMessage y quien lo analice después —por API o en local— es otra
// decisión, tomada en otro sitio y más tarde. Añadir aquí la vía como segunda
// condición perdería el literal del cliente para siempre por una configuración que
// aún puede cambiar. Lo vigila via_local_sin_api_llm_test.go.
const featureThreadMessages = entitlements.FeatureLLMIntake

// threadAllowed resuelve el gate UNA vez y responde a la única pregunta que los dos
// productores comparten: ¿puede este tenant dejar texto literal en el hilo de este
// evento?
//
// Se extrae para que la respuesta sea LA MISMA para el turno y para el saliente
// fuera de turno. Que las dos clases de fila compartan gate no es una comodidad de
// implementación: es INV-11 / ADR-0034 aplicado — el texto conversacional se
// persiste, o no se persiste, por tenant y en bloque. Un saliente fuera de turno de
// un tenant sin la feature produce CERO filas, igual que su turno.
//
// Fail-closed en los cuatro caminos: sin plano de eventos, sin evento, sin resolver
// o con el resolver caído ⇒ false.
func (rt *Runtime) threadAllowed(ctx context.Context, tenantID, sessionID, eventID string) bool {
	if rt.events == nil || eventID == "" || rt.entitlements == nil {
		return false
	}
	has, err := rt.entitlements.Has(ctx, tenantID, featureThreadMessages)
	if err != nil {
		rt.log.Warn("runtime: no se pudo resolver la feature llm_intake; no se escribe en el hilo",
			"error", err, "tenant_id", tenantID, "session_id", sessionID)
		return false
	}
	return has
}

// persistTurnMessages deja en el hilo del evento el literal del turno que el motor
// acaba de procesar: la entrada del cliente (RoleClient) y las respuestas que el
// flujo produjo (RoleBusiness), en ese orden — el orden de la conversación.
//
// Tres decisiones que no son de estilo:
//
//   - SOLO dentro de evento vivo: eventID es el turnEventID capturado ANTES del
//     cierre natural (advanceLive) — los textos del turno que TERMINA el flujo
//     pertenecen al evento que estaba vivo mientras se produjeron, igual que sus
//     efectos (T4.5.1). Sin evento no hay hilo, y no se inventa uno.
//   - GATE de verdad de la feature (misma mecánica que buildSignal, ADR-0022):
//     rt.entitlements decide por tenant. Ver threadAllowed.
//   - Se persiste lo que el motor PRODUJO, antes del envío, aunque sendReply
//     luego falle o el rate-limit lo calle: es el MISMO tradeoff aceptado del
//     orden Save-antes-de-Send (el estado ya avanzó con esa respuesta; el hilo
//     cuenta el turno del motor, no la entrega del transporte).
//
// BEST-EFFORT integral (patrón PersistSummary): cualquier fallo se LOGUEA y el
// turno sigue — el hilo jamás tumba la conversación.
func (rt *Runtime) persistTurnMessages(ctx context.Context, tenantID, sessionID, eventID, clientText string, outs []engine.Output) {
	if !rt.threadAllowed(ctx, tenantID, sessionID, eventID) {
		return
	}
	if clientText != "" {
		if _, err := rt.events.AppendMessage(ctx, eventID, events.RoleClient, clientText); err != nil {
			rt.log.Warn("runtime: no se pudo escribir el mensaje del cliente en el hilo; el turno sigue",
				"error", err, "session_id", sessionID)
		}
	}
	for _, out := range outs {
		if out.Text == "" {
			continue // un adjunto sin texto no tiene literal que guardar.
		}
		if _, err := rt.events.AppendMessage(ctx, eventID, events.RoleBusiness, out.Text); err != nil {
			rt.log.Warn("runtime: no se pudo escribir la respuesta del negocio en el hilo; el turno sigue",
				"error", err, "session_id", sessionID)
		}
	}
}

// persistOpeningTurn deja en el hilo EL TURNO QUE ABRE EL EVENTO: el literal con el
// que el cliente lo disparó (RoleClient) y lo que el flujo contestó al arrancar
// (RoleBusiness), en ese orden (Plan 044 · T1.4, corrección del 2026-08-22).
//
// # QUÉ ESTABA ROTO, Y POR QUÉ NO LO CUBRÍA persistTurnMessages
//
// persistTurnMessages tenía UN solo llamante —advanceLiveStep—, que es el turno de
// una conversación YA VIVA. El camino del disparador (handleTrigger →
// startFromDecision → beginEvent → birthEvent → enterEventFlow → startLocked) no
// escribía NI UNA fila `message`, así que el «quiero presupuesto de 20 hamburguesas»
// que abre el pedido no llegaba nunca al hilo. Desde que startFromDecision observa
// para la ventana, ese mismo mensaje SÍ está en `intake_jobs.source_refs` y ancla el
// `message_ts` (D-044.9): la referencia existía y el literal no, y el `source_text`
// que componía T1.4 empezaba por el SEGUNDO mensaje de la ráfaga. Sin error.
//
// # NO ES UNA SEGUNDA PUERTA: ES persistTurnMessages CON SU GATE
//
// No duplica nada. Reutiliza el MISMO productor —y con él el mismo gate
// (threadAllowed), el mismo sobre cifrado (AppendMessage, nivel 2 del ADR-0034), los
// mismos roles y el mismo best-effort—. Lo único que añade es la GUARDA DE CAMINO:
// solo escribe si este arranque nació de un entrante del cliente.
//
// 🔴 LA GUARDA `opening.FromClient` ES LO QUE IMPIDE LA DOBLE ESCRITURA, y hay que
// decir contra qué protege. `seq` se calcula con `MAX+1` contra un
// `UNIQUE(event_id, seq)` y 5 reintentos (events/store.go): una fila repetida NO
// revienta, SE CUELA EN SILENCIO —y un literal duplicado en el hilo es una cantidad
// duplicada en el presupuesto—. Las dos formas en que podría colarse quedan cerradas:
//
//   - POR EL TURNO NORMAL: no puede. Por cada entrante corre handleTrigger O
//     advanceLive, jamás los dos (HandleIncoming enruta a uno u otro, y las dos
//     sueltas que reentran —releaseFinishedState y releaseOrphanMenu— hacen `return`
//     sin llegar al Step). Es el mismo argumento que ya sostiene a
//     observeForAggregation, y por eso las dos escrituras viven en el mismo par de
//     caminos excluyentes.
//   - POR LAS OTRAS PUERTAS DE startLocked: tampoco, y por esta guarda. `Start`
//     (API/admin) y `startPlainFlow` (keyword/fallback) pasan el valor cero; la
//     CONMUTA (switchToEvent) también, y ahí la guarda hace trabajo REAL — ese camino
//     re-entra al flujo del evento vivo y volvería a escribir su nodo inicial en un
//     hilo que ya lo tiene. Lo que la conmuta sí deja en el hilo es su resumen de
//     rescate, por la puerta de los MARCADOS (sendResumeSummary →
//     persistOutOfTurnMessage), que es otra clase de fila y no puede confundirse.
//
// BEST-EFFORT y fail-closed, heredados enteros: sin `llm_intake`, CERO filas; un
// fallo se LOGUEA y el arranque sigue. El hilo jamás tumba la conversación.
func (rt *Runtime) persistOpeningTurn(ctx context.Context, tenantID, sessionID, eventID string, opening openingTurn, outs []engine.Output) {
	if !opening.FromClient {
		return
	}
	rt.persistTurnMessages(ctx, tenantID, sessionID, eventID, opening.Text, outs)
}

// persistOutOfTurnMessage deja en el hilo un saliente que NO nace de un turno
// entrante, MARCADO como tal (Plan 044 · T1.6, D-044.24).
//
// # QUÉ COMPRA LA MARCA, Y QUÉ COMPRARÍA NO PONERLA
//
// Las tres salidas se sopesaron y las dos descartadas fallan por lados opuestos:
//
//   - DEJARLOS FUERA pierde el antecedente. El cliente que contesta «sí, esas dos»
//     a un resumen de rescate deja un «esas dos» que no apunta a nada, y el
//     pipeline no puede resolverlo con lo que hay en el hilo.
//   - METERLOS SIN DISTINGUIR es peor, y esto es lo concreto: EL RESCATE LISTA
//     PRODUCTOS. Un LLM que lea esa lista sin saber quién la escribió extrae como
//     pedido del cliente lo que imprimió nuestro propio automensaje. Es la misma
//     familia de fallo que ya midió el README del plan.
//
// ⇒ Entran, rotulados. Es el MISMO trato que `entry_kind='summary'` (REQ-10b,
// D-043.3b): bloque rotulado en el prompt, NO cuentan volumen, NO disparan la
// ventana de silencio y NINGUNA `evidence` del borrador puede apuntarles.
//
// ⚠️ AQUÍ SOLO SE CONSTRUYE EL PRODUCTOR Y LA MARCA. Quien las consume rotulándolas
// en el prompt es T1.4, que va después. Este fichero no sabe nada de prompts.
//
// BEST-EFFORT, igual que su hermana: un fallo se LOGUEA y el envío —que ya
// ocurrió— sigue su curso. El hilo jamás tumba nada.
func (rt *Runtime) persistOutOfTurnMessage(ctx context.Context, tenantID, sessionID, eventID string, texts ...string) {
	// El gate se resuelve UNA vez para todas las cadenas: los emisores de aquí
	// mandan una o dos, y preguntarle al resolver por cada una sería una consulta
	// por línea de un mismo saliente.
	if !rt.threadAllowed(ctx, tenantID, sessionID, eventID) {
		return
	}
	for _, text := range texts {
		if text == "" {
			continue
		}
		if _, err := rt.events.AppendOutOfTurnMessage(ctx, eventID, text); err != nil {
			rt.log.Warn("runtime: no se pudo escribir el saliente fuera de turno en el hilo; el envío ya salió",
				"error", err, "session_id", sessionID)
		}
	}
}

// ---------------------------------------------------------------------------
// EL CENSO DE EMISORES FUERA DE TURNO, Y QUIÉN QUEDA FUERA
// ---------------------------------------------------------------------------
//
// Se levantó entero el 2026-08-22 contra el código, no contra el plan. Se dice
// también lo que NO se cableó, porque un censo que solo enumera lo hecho hace creer
// que la cobertura es total — el mismo agujero que send.go documenta para la racha
// de auto-respuestas (Plan 049).
//
// ✅ CABLEADOS (todos tienen el event_id en la mano y viven en este paquete):
//
//   - events.go · sendResumeSummary   — el resumen del RESCATE. El arquetipo que
//     D-044.24 nombra: es el que lista productos.
//   - events.go · stopEvent           — la confirmación de un `event_stop`.
//   - incoming.go · handleEscape      — el aviso de escape (solo con evento conocido).
//   - resume.go · prepareResume       — el aviso de reinicio + el nodo inicial, y el
//     aviso de fallo del sink durable de ese mismo camino.
//   - incoming.go · touchDeposit      — el RECORDATORIO DE LA SEÑA, la coletilla
//     arquetípica. Sale por el Notifier de `intakes`, que no conoce el evento; el
//     reparto está explicado en el puerto DepositReminder (runtime_engine.go).
//
// ❌ FUERA, Y POR QUÉ. Ninguno es un olvido:
//
//   - gateway/grpc/greeting.go — el SALUDO de sesión. No hay evento que tocar: el
//     saludo NO crea evento (ADR-0029 E-6), así que no hay hilo donde escribir. Lo
//     excluye el modelo de datos, no una política.
//   - events.go · sendOfferNow — la oferta de tipos. Su propio código dice
//     «eventID vacío A PROPÓSITO: esta conversación no tiene evento». Mismo caso.
//   - events.go · presentMenu — el despachador. No pertenece a UN evento: LISTA
//     varios. Atarlo a uno cualquiera sería inventar una pertenencia.
//   - intakes/notifier.go · deliver y NotifyCRMStatus — las notificaciones de
//     cambio de estado. 🔴 ESTAS SÍ DEBERÍAN ENTRAR y no entran por una razón
//     material: `intakes.Intake` NO LLEVA EventID (intakes.go:70-114). La columna
//     `intakes.event_id` existe en la base desde la 0054, pero ni el struct la
//     tiene ni los SELECT la leen, así que hoy no hay forma de saber a qué hilo
//     pertenece el aviso. Ampliar el struct y sus dos stores es trabajo REAL y de
//     otro paquete: queda como seguimiento nombrado de T1.6, no como cobertura.
//   - publicapi/messages.go y platform/httpapi/admin.go — los envíos HUMANOS.
//     Quedan fuera POR DECISIÓN, y hay dos razones. (1) MATERIAL: esos endpoints
//     reciben sesión y destino, no evento; derivar uno exigiría una lectura nueva
//     (contacto → evento activo) en el camino del envío, y no hay respuesta obvia
//     cuando el contacto tiene dos eventos abiertos. (2) DE MODELO, y es la que
//     manda: un humano tecleando no es un saliente fuera de turno, es una TERCERA
//     VOZ, y la columna que ya modela «esto lo escribió el dueño» es `origin`
//     ('owner_pasted', D-043.19, Plan 045 · D-045.5). Meterlos aquí con
//     origin='whatsapp' corrompería el rastro que esa columna existe para guardar.
//     Cuando el 045 construya su camino, el texto del dueño entra por ahí — con su
//     `origin` correcto— y no por éste.
