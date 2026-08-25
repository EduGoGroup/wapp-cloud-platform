package runtime

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ---------------------------------------------------------------------------
// LA BIENVENIDA ÚNICA (Plan 044 · Ola 1.8 · T1.8-2, D6)
// ---------------------------------------------------------------------------
//
// QUÉ ES, EN UNA FRASE: un saliente FIJO DEL SISTEMA —«estamos procesando»— que el
// Cloud le manda AL CLIENTE al primer mensaje de una conversación, y otra vez si el
// contacto vuelve tras un silencio largo. Jamás en cada turno.
//
// POR QUÉ EXISTE. Entre que el cliente escribe y que le llega el borrador pasan, en el
// mejor caso, los plazos de la ventana de captación (45 s de silencio / 120 s de techo,
// T1.8-1) más el pipeline entero; el presupuesto del plan es «primer borrador en < 5
// min» (T6.1). Durante todo ese rato la conversación está MUDA y el cliente no sabe si
// su mensaje llegó. Esta frase es lo único que se le dice, y dice lo único que el
// sistema sabe con certeza en ese instante: que llegó y que se está trabajando.
//
// # LAS TRES COSAS QUE ESTE FICHERO EXISTE PARA GARANTIZAR
//
//  1. 🔴 NO ENTRA EN EL ANÁLISIS, Y ESTO ES MÁS FUERTE QUE D-044.24. Ni a
//     `intake_jobs.source_refs`, ni a `source_text` (**ni rotulada**, al revés que el
//     resumen del rescate o el recordatorio de la seña), ni cuenta como actividad de la
//     ventana. El motivo no es de presupuesto: una `evidence` del borrador que apuntara
//     a este texto sería una `evidence` fabricada por nosotros —el sistema citándose a
//     sí mismo como si fuera el cliente—. Cómo se consigue, dicho por sitios:
//     • `source_refs` se construye SOLO en IntakeAggregator.Observe (aggregator.go), y
//     `Observe` recibe un ENTRANTE. Esta bienvenida no pasa por ahí: no se la ofrece.
//     • `source_text` lo compone ComposeAtFlush leyendo el HILO del evento
//     (conversation_event_messages). Esta bienvenida NO se persiste en el hilo, ni
//     con `entry_kind='message_out_of_turn'` (que sería el único sitio admisible si
//     quisiéramos trazabilidad, thread.go): el enunciado dice «ni rotulada».
//     • `intake_jobs.updated_at` no se mueve porque quien lo mueve es esa misma
//     `Observe`. 🔴 Y ESO NO ES UNA CASUALIDAD: es el criterio (d) de T1.8-1, ya
//     cerrado, y esta bienvenida es el ÚNICO caso vivo que ese criterio protege.
//     Enrutarla por `Observe` rompería una casilla cerrada de otra tarea.
//
//  2. NO PISA EL MENÚ (Nivel A). Si el contacto está DENTRO de un flujo estático —un
//     menú numérico, un carrito a medias—, este turno no lleva bienvenida. La guarda no
//     es un `if` sobre el tipo de nodo: es el SITIO desde donde se llama (incoming.go).
//     Ver «DÓNDE SE DECIDE» abajo.
//
//  3. NO LA ESCRIBE EL LLM (INV-1/INV-2). Es texto fijo: o el que el dueño configuró en
//     `tenant_settings.welcome_text`, o la constante `store.DefaultWelcomeText`.
//
// ⚠️ NO CONFUNDIR CON LOS OTROS DOS AUTOMENSAJES DE LA PLATAFORMA: la notificación de
// degradación (T1.5-4 / REQ-38) va AL DUEÑO, y el aviso de sesión pasiva
// (gateway/grpc/greeting.go) va al número de la PROPIA SESIÓN. Esta va al CLIENTE, y es
// el único texto que el Plan 044 le manda antes del borrador.
//
// # DÓNDE SE DECIDE, Y POR QUÉ AHÍ (la guarda del punto 2)
//
// El trabajo se parte en DOS por una razón de presupuesto y una de corrección:
//
//   - `observeWelcome` corre en CADA entrante, justo tras tomar el candado de la clave
//     y ANTES de cargar el estado. Es una sentencia y ninguna lectura de config: solo
//     registra que el contacto habló (el ancla del silencio) y se trae la marca previa.
//     Tiene que correr SIEMPRE porque el silencio se mide contra el ÚLTIMO mensaje del
//     contacto, no contra el último que abrió conversación: si solo tocara en los
//     turnos que saludan, una conversación que lleva horas hablando parecería llevar
//     horas callada y recibiría la bienvenida en mitad de un pedido.
//   - `welcomeIfDue` corre SOLO en los DOS caminos de HandleIncoming en que este turno
//     NO avanza una conversación viva: el LIMBO (`!ok`: no hay flow_state) y el
//     REINICIO (`restart`: el reloj del evento venció y se soltó la conversación). Los
//     dos desembocan en `handleTrigger`, y ahí es donde se lee la config del tenant.
//
// ⇒ De ese reparto sale, gratis, la garantía del punto 2: mientras el contacto navega
// un menú numérico el turno es un AVANCE (`advanceLive`), que no llama a esta función.
// Y también quedan fuera, a propósito, los caminos que SUELTAN una fila en mitad de una
// conversación (`releaseOrphanMenu`, `releaseFinishedState`): llaman a `handleTrigger`,
// sí, pero son la continuación de algo que el cliente estaba haciendo, no el primer
// mensaje de nada. Por eso la llamada NO vive dentro de `handleTrigger` —donde habría
// cubierto los cuatro de una vez— sino en los dos sitios donde es correcta.
//
// # POR QUÉ VA ANTES DE `handleTrigger` Y NO DESPUÉS
//
// Porque es un ACUSE DE RECIBO, y un acuse llega antes que la respuesta. Puesto
// después, el cliente vería primero el menú (o la oferta, o el arranque del flujo) y
// luego un «estamos procesando» que llega a destiempo y parece contradecir lo que
// acaba de leer. Puesto antes, la secuencia es la natural: «lo recibimos» → lo que sea
// que el motor tenga que decir.
//
// # EL RUNBOOK DE LA IDEMPOTENCIA ES EL DE `fleet_sessions.greeted_at` (0066)
//
// Se copia entero porque resuelve ya este mismo problema («ya avisé a ESTE»), solo que
// allí por sesión y aquí por conversación: centinela en la escritura, marcar SOLO si el
// `Ack` del Edge vuelve `ok=true`, y NO marcar si el envío falla —el siguiente mensaje
// del contacto reintenta solo—. Un aviso que no llegó no se da por dado.
//
// La única diferencia es el centinela: allí es `WHERE greeted_at IS NULL` porque la
// marca se pone una vez y para siempre; aquí vuelve a ponerse tras cada silencio largo,
// así que es un compare-and-set contra el valor leído (store.MarkWelcomed).

// WelcomeStore es el puerto del estado de la bienvenida (Plan 044 · T1.8-2). Lo
// satisface *store.PostgresRepository y, en tests, *store.MemoryRepository.
//
// Se declara AQUÍ y no se importa el tipo concreto por lo de siempre en este paquete
// (mismo criterio que DepositReminder o EventStore): el motor de flujos declara lo que
// necesita, no de quién lo obtiene.
type WelcomeStore interface {
	// TouchContact registra que el contacto acaba de escribir y devuelve el estado
	// ANTERIOR a este turno (ver store.WelcomeStore para el porqué del «anterior»).
	TouchContact(ctx context.Context, key store.Key, now time.Time) (store.WelcomeMark, error)
	// MarkWelcomed sella la bienvenida como entregada, con centinela sobre el testigo
	// leído. false SIN error = otro turno ganó la carrera.
	MarkWelcomed(ctx context.Context, key store.Key, testigo store.WelcomeMark, now time.Time) (bool, error)
}

// WithWelcomeStore cablea la bienvenida única (Plan 044 · T1.8-2). Sin ella (nil), el
// motor NO manda ninguna bienvenida y NO escribe una sola fila en
// `conversation_welcomes`: comportamiento idéntico al previo a esta tarea. Es la misma
// no-regresión por defecto que el resto de piezas opcionales del Runtime.
func WithWelcomeStore(w WelcomeStore) Option {
	return func(rt *Runtime) { rt.welcomes = w }
}

// featureWelcome es el gate por tenant de la bienvenida. Es DELIBERADAMENTE la MISMA
// feature que abre el hilo literal y la ventana de captación (`llm_intake`,
// entitlements.FeatureLLMIntake), y no una propia:
//
// La bienvenida promete «estamos procesando». Eso es cierto exactamente cuando hay un
// pipeline de captación detrás, y el interruptor de que lo haya es esta feature. Un
// tenant sin `llm_intake` no tiene ventana, ni hilo, ni borrador: mandarle a su cliente
// un «estamos procesando» sería un mensaje automático que afirma un estado que el
// sistema NO está en — el mismo modo de fallo que ya mordió con el aviso de sesión
// pasiva, que se mandaba a sesiones activas y les mentía en sus tres frases.
//
// ⇒ Y por eso mismo esta feature ES el interruptor de apagado de la bienvenida, y no
// hay ninguna columna `welcome_enabled` en `tenant_settings`. Un segundo interruptor
// para lo mismo es justo lo que T1.6 tuvo que RETIRAR de thread.go.
const featureWelcome = entitlements.FeatureLLMIntake

// welcomeTurn es lo que un turno sabe de su propia bienvenida: si la mecánica está
// activa para este tenant, cuál era la marca ANTES de este mensaje, y con qué instante
// se decide.
//
// 🔴 EL INSTANTE VIAJA EN EL STRUCT Y NO SE VUELVE A PEDIR. `observeWelcome` escribe
// `last_incoming_at` con él y `welcomeIfDue` mide el silencio con él: si cada uno
// llamara a rt.now() por su cuenta, la resta compararía dos instantes distintos del
// mismo turno. Es pequeño aquí y es el mismo vicio que en esta casa ya salió caro
// comparando el reloj de Postgres con el de Go — que es también por lo que
// `last_incoming_at` NO tiene `DEFAULT now()` en el esquema.
type welcomeTurn struct {
	// activa dice si la bienvenida está cableada Y el tenant tiene la feature. Con
	// false, welcomeIfDue es un no-op y no se ha tocado ninguna fila.
	activa bool
	// previo es la marca ANTES de este mensaje: cuándo habló el contacto por última
	// vez y cuándo se le saludó por última vez. Los dos ceros significan «nunca».
	previo store.WelcomeMark
	// ahora es EL instante de este turno, tomado una sola vez del reloj inyectable.
	ahora time.Time
}

// debe responde la pregunta del turno: ¿a este mensaje le toca bienvenida?
//
// DOS causas, y solo dos:
//
//  1. NUNCA SE LE SALUDÓ (`WelcomedAt` cero). Es el primer mensaje de la primera
//     conversación de este contacto por esta sesión.
//  2. VOLVIÓ TRAS EL SILENCIO. El último mensaje del contacto —`LastIncomingAt`, que
//     es el ANTERIOR a este, no este— queda a `silencio` o más de distancia.
//
// 🔴 EL ANCLA ES EL ÚLTIMO MENSAJE DEL CONTACTO, NO LA ÚLTIMA BIENVENIDA, y confundirlos
// es el defecto que este método existe para no tener. Anclado en la bienvenida, la
// regla dejaría de ser «vuelve tras N h de silencio» y pasaría a ser «repite cada N h»:
// una conversación larga y viva recibiría la frase a mitad de un pedido, que es
// exactamente lo que el enunciado prohíbe («nunca por interacción»).
//
// ⚠️ `silencio == 0` ⇒ SIEMPRE true (la resta es siempre >= 0). Es la lectura
// documentada del 0 en la migración 0076 —«vencido siempre», igual que el 0 de
// aggregation_window_seconds— y es una configuración legítima, aunque desaconsejada.
// El `>=` (y no `>`) es lo que la hace cierta, y de paso fija el borde: a EXACTAMENTE N
// de silencio, la bienvenida sale.
func (t welcomeTurn) debe(silencio time.Duration) bool {
	if !t.activa {
		return false
	}
	if t.previo.WelcomedAt.IsZero() {
		return true
	}
	return t.ahora.Sub(t.previo.LastIncomingAt) >= silencio
}

// observeWelcome registra que el contacto acaba de escribir y se trae la marca previa.
// Corre en CADA entrante, dentro del candado de la clave y ANTES de cargar el estado.
//
// PRESUPUESTO, dicho aquí porque este código está en línea con el mensaje del cliente:
// una consulta al resolver de features (que cachea) y UNA sentencia. Cero criptografía,
// cero red, ninguna lectura de `tenant_settings` — la config solo se lee en el camino
// que de verdad puede saludar (welcomeIfDue), que es una minoría de los turnos.
//
// FAIL-CLOSED en los tres caminos, calcado de threadAllowed: sin store cableado, sin
// resolver de entitlements, o con el resolver caído ⇒ `activa=false`, y entonces no se
// escribe NADA. Un gate que en caso de duda saluda mandaría mensajes automáticos a
// clientes de tenants que no compraron esto.
//
// 🔴 EL GATE VA ANTES DE LA ESCRITURA, y no es solo higiene: si tocara primero y
// preguntara después, `conversation_welcomes` acumularía una fila por contacto de TODO
// el parque —incluidos los tenants que jamás recibirán una bienvenida—, y la tabla
// dejaría de significar lo que su COMMENT dice que significa.
//
// Un fallo del store se LOGUEA y devuelve `activa=false`: la bienvenida jamás tumba el
// turno del cliente (misma regla que el agregador, INV-10).
func (rt *Runtime) observeWelcome(ctx context.Context, tenantID string, key store.Key) welcomeTurn {
	if rt.welcomes == nil || rt.entitlements == nil {
		return welcomeTurn{}
	}
	has, err := rt.entitlements.Has(ctx, tenantID, featureWelcome)
	if err != nil {
		rt.log.Warn("runtime: no se pudo resolver la feature llm_intake; no se manda bienvenida",
			"error", err, "tenant_id", tenantID, "session_id", key.SessionID)
		return welcomeTurn{}
	}
	if !has {
		return welcomeTurn{}
	}
	ahora := rt.now()
	previo, err := rt.welcomes.TouchContact(ctx, key, ahora)
	if err != nil {
		rt.log.Warn("runtime: no se pudo registrar la actividad del contacto; no se manda bienvenida",
			"error", err, "tenant_id", tenantID, "session_id", key.SessionID, "contact_id", key.ContactID)
		return welcomeTurn{}
	}
	return welcomeTurn{activa: true, previo: previo, ahora: ahora}
}

// welcomeIfDue manda la bienvenida si a este turno le toca, y la sella si salió.
//
// BEST-EFFORT INTEGRAL, y por eso no devuelve error: ningún fallo de aquí puede tumbar
// el procesamiento del mensaje que el cliente acaba de mandar. Es la misma regla que
// gobierna al agregador (INV-10), al hilo literal (thread.go) y al recordatorio de la
// seña. Cada camino que se rinde lo hace SIN marcar, así que el siguiente mensaje del
// contacto vuelve a intentarlo.
//
// PII: los logs llevan solo IDs OPACOS (tenant/session/contact). Ni el número —que solo
// viaja a SendText— ni el texto. Mismo criterio que greeting.go y que intakes/notifier.go.
func (rt *Runtime) welcomeIfDue(ctx context.Context, tenantID, sessionID, contactID string, key store.Key, t welcomeTurn) {
	if !t.activa {
		return
	}
	// La config se lee AQUÍ y no en observeWelcome: este camino es una minoría de los
	// turnos (solo los que no avanzan conversación viva), así que la lectura no pesa
	// sobre cada mensaje del parque. Un fallo ⇒ no se saluda (fail-closed): con la
	// config ilegible no se sabe ni qué decir ni cada cuánto.
	cfg, err := rt.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		rt.log.Warn("runtime: no se pudo leer la config del tenant; no se manda bienvenida",
			"error", err, "tenant_id", tenantID, "session_id", sessionID)
		return
	}
	if !t.debe(cfg.WelcomeSilence) {
		return
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		rt.log.Warn("runtime: destino no resoluble; no se manda bienvenida",
			"error", err, "tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID)
		return
	}
	ack, err := rt.sendSystemText(ctx, sessionID, to, textoDeBienvenida(cfg))
	if err != nil {
		// Warn y no Error: el reintento está garantizado por el siguiente mensaje del
		// contacto y no se ha perdido nada. Mismo criterio que greeting.go, donde un
		// rechazo en la ventana del lease es lo ESPERADO en el primer intento.
		rt.log.Warn("runtime: el envío de la bienvenida falló; NO se marca y el próximo mensaje reintenta",
			"error", err, "tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID)
		return
	}
	if !ack.GetOk() {
		// El Edge acusó el comando y avisó de que NO lo envió (típicamente «lease no
		// vigente»). No se marca ⇒ el siguiente mensaje reintenta.
		rt.log.Warn("runtime: el Edge rechazó la bienvenida; NO se marca y el próximo mensaje reintenta",
			"tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID,
			"command_id", ack.GetAckedCommandId(), "edge_error", ack.GetError())
		return
	}
	marcada, err := rt.welcomes.MarkWelcomed(ctx, key, t.previo, t.ahora)
	switch {
	case err != nil:
		// El mensaje YA SALIÓ y la marca no se puso: el próximo mensaje del contacto lo
		// mandará otra vez. Error y no Warn porque el precio lo paga el cliente en su
		// teléfono, con una frase repetida, y porque —a diferencia de un rechazo del
		// Edge— esto no se arregla solo.
		rt.log.Error("runtime: la bienvenida se entregó pero no se pudo marcar; el cliente recibirá un duplicado",
			"error", err, "tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID)
	case !marcada:
		// El centinela hizo su trabajo en la BD: otro turno de esta misma conversación
		// marcó primero. El keyedMutex serializa por clave DENTRO de un proceso, así que
		// para ver esto hacen falta dos instancias del Cloud sobre la misma base. El
		// mensaje de ESTE camino ya salió: el duplicado se ve aquí y en ningún otro sitio.
		rt.log.Warn("runtime: otro turno marcó la bienvenida primero; este envío fue un duplicado",
			"tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID)
	default:
		rt.log.Info("runtime: bienvenida entregada al contacto",
			"tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID,
			"command_id", ack.GetAckedCommandId())
	}
}

// textoDeBienvenida resuelve QUÉ frase se manda. Es el ÚNICO sitio que traduce la CADENA
// VACÍA de `tenant_settings.welcome_text` al texto de plataforma, y por eso existe como
// función con nombre en vez de un `if` dentro del envío.
//
// 🔴 LA CADENA VACÍA NO ES UN OVERRIDE, al revés que los ceros de las columnas vecinas de esa
// tabla (`aggregation_window_seconds`, `event_inactivity_ttl_seconds`): es el DEFAULT
// de la columna, o sea lo que trae TODA fila preexistente, y significa «el texto de
// plataforma». Leerlo como «sin bienvenida» apagaría la funcionalidad entera para todo
// tenant que tenga fila; leerlo como texto literal mandaría un mensaje VACÍO. Por eso
// GetTenantSettings lo devuelve tal cual —su contrato es no inventar— y la traducción
// vive aquí, donde se sabe para qué es.
//
// Con esto, los DOS caminos convergen en la misma frase: el tenant SIN fila recibe
// DefaultWelcomeText por DefaultTenantSettings, y el tenant CON fila y vacía lo recibe por
// esta función. Apagar la bienvenida no se hace con el texto: se hace quitándole al
// tenant la feature `llm_intake`.
func textoDeBienvenida(cfg store.TenantSettings) string {
	if cfg.WelcomeText == "" {
		return store.DefaultWelcomeText
	}
	return cfg.WelcomeText
}
