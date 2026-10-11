// Porta internal/flujos/runtime/welcome.go @ e0159171

package runtime

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// welcome.go es LA BIENVENIDA ÚNICA (Plan 044 · Ola 1.8 · T1.8-2, D6): un saliente FIJO DEL
// SISTEMA —«estamos procesando»— que el Cloud le manda AL CLIENTE al primer mensaje de una
// conversación, y otra vez si el contacto vuelve tras un silencio largo. Jamás en cada turno.
//
// Declara el puerto y su opción; la mecánica no exportada la prueban los tests
// (welcome*_test.go) por HandleIncoming.
//
// # Promesas
//
// Gate (RT-20), fail-closed:
//
//   - WL-1 · La mecánica está activa solo con las TRES: WithWelcomeStore, WithEntitlements y
//     la feature `llm_intake` del tenant. Es la MISMA feature que abre el hilo y la ventana de
//     captación, y es su único interruptor: no hay columna de apagado.
//   - WL-2 · Si falta cualquiera, o el resolver falla (WARN «runtime: no se pudo resolver la
//     feature llm_intake; no se manda bienvenida»), no se manda nada y NO SE ESCRIBE NADA: el
//     gate va antes de TouchContact.
//
// Registro del contacto:
//
//   - WL-3 · En CADA entrante que pasa las guardas de borde, con el candado de la conversación
//     tomado y ANTES de cargar el estado, se llama a TouchContact con el instante del reloj
//     del runtime. También en los turnos que avanzan una conversación viva: el silencio se
//     mide contra el ÚLTIMO mensaje del contacto.
//   - WL-4 · Ese instante se toma UNA vez por turno: es el mismo que se escribe en
//     TouchContact, con el que se mide el silencio y con el que se sella MarkWelcomed.
//   - WL-5 · Si TouchContact falla: WARN «runtime: no se pudo registrar la actividad del
//     contacto; no se manda bienvenida» y el turno sigue sin bienvenida.
//
// Cuándo se manda:
//
//   - WL-6 · Solo en los DOS caminos en que el turno NO avanza una conversación viva: el
//     LIMBO (no hay estado) y el REINICIO (el reloj del evento venció y soltó la
//     conversación). Va ANTES de resolver el disparo: es un acuse de recibo.
//   - WL-7 · Nunca en un avance (no pisa un menú ni un carrito a medias), ni en las sueltas
//     de mitad de conversación (el menú huérfano, el estado terminal), ni en un Start por API.
//   - WL-8 · Toca si NUNCA se saludó (WelcomedAt cero), o si el mensaje ANTERIOR del contacto
//     (LastIncomingAt de la marca previa) queda a WelcomeSilence o más de este. El ancla es
//     el último mensaje del contacto, no la última bienvenida. El borde es inclusivo: a
//     exactamente N, sale. Con WelcomeSilence 0 sale siempre.
//   - WL-9 · El texto es fijo, no lo escribe ningún LLM: TenantSettings.WelcomeText o, si
//     está vacío, store.DefaultWelcomeText. La cadena vacía significa «el texto de
//     plataforma», no «sin bienvenida».
//   - WL-10 · La configuración del tenant se lee solo en esos dos caminos. Si no se puede
//     leer: no se saluda (WARN «runtime: no se pudo leer la config del tenant; no se manda
//     bienvenida»).
//
// Entrega y sello (el runbook de fleet_sessions.greeted_at):
//
//   - WL-11 · Se marca (MarkWelcomed, con la marca previa como testigo) SOLO si el Ack del
//     Edge vuelve con ok = true. Si el envío falla, o el Edge lo rechaza, NO se marca y el
//     siguiente mensaje del contacto reintenta. WARN «runtime: el envío de la bienvenida
//     falló; NO se marca y el próximo mensaje reintenta» / «runtime: el Edge rechazó la
//     bienvenida; NO se marca y el próximo mensaje reintenta».
//   - WL-12 · Entregada y marcada: Info «runtime: bienvenida entregada al contacto».
//     Entregada y la marca falla: ERROR «runtime: la bienvenida se entregó pero no se pudo
//     marcar; el cliente recibirá un duplicado». Entregada y otro turno marcó primero
//     (MarkWelcomed false sin error): WARN «runtime: otro turno marcó la bienvenida primero;
//     este envío fue un duplicado».
//   - WL-13 · Best-effort integral: ningún fallo de la bienvenida devuelve error ni impide
//     que el turno siga hacia el disparo.
//
// Lo que NO hace:
//
//   - WL-14 · 🔴 No entra en el análisis: no se escribe en el hilo del evento (ni marcada
//     como fuera de turno), no se ofrece al agregador y no mueve su ventana.
//   - WL-15 · No suma a la racha de auto-respuestas y no cobra token del limitador (send.go).
//   - WL-16 · Los logs llevan solo ids opacos (tenant, sesión, contacto): ni el número ni el
//     texto.

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
//     • `source_text` lo compone SourceTextComposer.Compose leyendo el HILO del evento
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

// WelcomeStore es el puerto del estado de la bienvenida (Plan 044 · T1.8-2): «a esta
// conversación ya la saludé» y «cuándo habló el contacto por última vez». Lo satisfacen
// *store.PostgresRepository y *store.MemoryRepository.
//
// Se declara aquí, y no se importa el tipo concreto, por lo de siempre en este paquete: el
// motor declara lo que necesita, no de quién lo obtiene.
type WelcomeStore interface {
	// TouchContact registra que el contacto acaba de escribir en el instante now y devuelve
	// la marca ANTERIOR a este turno (las dos fechas en cero = nunca habló, nunca se le
	// saludó).
	TouchContact(ctx context.Context, key store.Key, now time.Time) (store.WelcomeMark, error)
	// MarkWelcomed sella la bienvenida como entregada en now, con centinela sobre la marca
	// leída (witness): solo marca si nadie la movió desde entonces. false SIN error = otro
	// turno ganó la carrera.
	MarkWelcomed(ctx context.Context, key store.Key, witness store.WelcomeMark, now time.Time) (bool, error)
}

// WithWelcomeStore cablea la bienvenida única. Sin ella (nil) el motor NO manda ninguna
// bienvenida y NO escribe una sola fila de su estado: idéntico a antes de que existiera
// (RT-12).
//
// Va SIEMPRE con WithEntitlements: el gate por tenant es fail-closed, así que con el resolver
// a nil queda inerte aunque se cablee (WL-1).
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
	// active dice si la bienvenida está cableada Y el tenant tiene la feature. Con
	// false, welcomeIfDue es un no-op y no se ha tocado ninguna fila.
	active bool
	// previous es la marca ANTES de este mensaje: cuándo habló el contacto por última
	// vez y cuándo se le saludó por última vez. Los dos ceros significan «nunca».
	previous store.WelcomeMark
	// now es EL instante de este turno, tomado una sola vez del reloj inyectable.
	now time.Time
}

// due responde la pregunta del turno: ¿a este mensaje le toca bienvenida?
//
// DOS causas, y solo dos:
//
//  1. NUNCA SE LE SALUDÓ (`WelcomedAt` cero). Es el primer mensaje de la primera
//     conversación de este contacto por esta sesión.
//  2. VOLVIÓ TRAS EL SILENCIO. El último mensaje del contacto —`LastIncomingAt`, que
//     es el ANTERIOR a este, no este— queda a `silence` o más de distancia.
//
// 🔴 EL ANCLA ES EL ÚLTIMO MENSAJE DEL CONTACTO, NO LA ÚLTIMA BIENVENIDA, y confundirlos
// es el defecto que este método existe para no tener. Anclado en la bienvenida, la
// regla dejaría de ser «vuelve tras N h de silencio» y pasaría a ser «repite cada N h»:
// una conversación larga y viva recibiría la frase a mitad de un pedido, que es
// exactamente lo que el enunciado prohíbe («nunca por interacción»).
//
// ⚠️ `silence == 0` ⇒ SIEMPRE true (la resta es siempre >= 0). Es la lectura
// documentada del 0 en la migración 0076 —«vencido siempre», igual que el 0 de
// aggregation_window_seconds— y es una configuración legítima, aunque desaconsejada.
// El `>=` (y no `>`) es lo que la hace cierta, y de paso fija el borde: a EXACTAMENTE N
// de silencio, la bienvenida sale.
func (t welcomeTurn) due(silence time.Duration) bool {
	if !t.active {
		return false
	}
	if t.previous.WelcomedAt.IsZero() {
		return true
	}
	return t.now.Sub(t.previous.LastIncomingAt) >= silence
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
// resolver de entitlements, o con el resolver caído ⇒ `active=false`, y entonces no se
// escribe NADA. Un gate que en caso de duda saluda mandaría mensajes automáticos a
// clientes de tenants que no compraron esto.
//
// 🔴 EL GATE VA ANTES DE LA ESCRITURA, y no es solo higiene: si tocara primero y
// preguntara después, `conversation_welcomes` acumularía una fila por contacto de TODO
// el parque —incluidos los tenants que jamás recibirán una bienvenida—, y la tabla
// dejaría de significar lo que su COMMENT dice que significa.
//
// Un fallo del store se LOGUEA y devuelve `active=false`: la bienvenida jamás tumba el
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
	now := rt.now()
	previous, err := rt.welcomes.TouchContact(ctx, key, now)
	if err != nil {
		rt.log.Warn("runtime: no se pudo registrar la actividad del contacto; no se manda bienvenida",
			"error", err, "tenant_id", tenantID, "session_id", key.SessionID, "contact_id", key.ContactID)
		return welcomeTurn{}
	}
	return welcomeTurn{active: true, previous: previous, now: now}
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
	if !t.active {
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
	if !t.due(cfg.WelcomeSilence) {
		return
	}
	to, err := rt.destination(ctx, tenantID, contactID)
	if err != nil {
		rt.log.Warn("runtime: destino no resoluble; no se manda bienvenida",
			"error", err, "tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID)
		return
	}
	ack, err := rt.sendSystemText(ctx, sessionID, to, welcomeText(cfg))
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
	marked, err := rt.welcomes.MarkWelcomed(ctx, key, t.previous, t.now)
	switch {
	case err != nil:
		// El mensaje YA SALIÓ y la marca no se puso: el próximo mensaje del contacto lo
		// mandará otra vez. Error y no Warn porque el precio lo paga el cliente en su
		// teléfono, con una frase repetida, y porque —a diferencia de un rechazo del
		// Edge— esto no se arregla solo.
		rt.log.Error("runtime: la bienvenida se entregó pero no se pudo marcar; el cliente recibirá un duplicado",
			"error", err, "tenant_id", tenantID, "session_id", sessionID, "contact_id", contactID)
	case !marked:
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

// welcomeText resuelve QUÉ frase se manda. Es el ÚNICO sitio que traduce la CADENA
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
func welcomeText(cfg store.TenantSettings) string {
	if cfg.WelcomeText == "" {
		return store.DefaultWelcomeText
	}
	return cfg.WelcomeText
}
