// Copia de internal/bootstrap/arranque/fase7_flujos.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo edge, que desde F3 (T3.28, conmutar(edge)) es internal/modulos/edge.
package arranque

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/media"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
)

// faseFlujos construye el Motor de Flujos (Pieza 05) y le engancha al gateway los
// cuatro hooks por los que un mensaje de WhatsApp entra en el sistema.
//
// Es la ÚLTIMA fase de dominio y la que más cables tiene: por debajo de ella ya está
// todo construido —almacenes, gateway, stack LLM, bandeja— y lo que hace es unirlo.
//
// 🔴 SIN LOS HOOKS DEL FINAL, ESTA FASE ENTERA NO HACE NADA. El motor quedaría armado
// y ningún mensaje llegaría a él. Es el modo de fallo que este repo ya sufrió dos
// veces: una ola cerrada no es una ola encendida.
type faseFlujos struct{}

func (faseFlujos) nombre() string { return "flujos" }

func (faseFlujos) requiere() []string {
	return []string{"gateway", "selector", "almacenes", "solicitudes"}
}

func (faseFlujos) ejecutar(_ context.Context, c *contenedor) error {
	// --- Registro de módulos del Motor. ---
	c.flowReg = modules.NewRegistry()
	c.flowReg.Register(menu.New())
	c.flowReg.Register(survey.New())
	// El carrito recibe el logger para AVISAR de los campos del catálogo v2 que
	// su parseo tolerante descarta (Plan 041 · T2.2): en runtime un catálogo a
	// medias sigue vendiendo, pero el dueño tiene que poder enterarse de qué
	// parte suya quedó fuera. Sin logger el módulo funciona igual, en silencio.
	// Y el hook de la cascada determinista (Plan 044 · Ola 3.5 · T3.5-1), que
	// publica en /metrics QUÉ escalón resolvió cada entrada de texto del cliente
	// (exact|fuzzy|ninguno) por nivel del carrito. Es el dato con el que se decide
	// cuánto trabajo le queda de verdad al turno LLM; sin él la cascada funciona
	// igual, en silencio.
	c.flowReg.Register(cart.New(cart.WithLogger(c.log), cart.WithMatchHook(c.mtx.CartMatch)))
	c.flowReg.Register(media.New()) // Plan 017: nodo "media" (envía archivos por WhatsApp)

	// --- EL MOTOR, con las dos mitades del re-entry de consultas (Plan 044 · Ola
	// 3.5 · T3.5-2) ---
	//
	// Fuente de contenido enrutada POR-NODO (Plan 015 T4a): el Router compone el
	// adapter Static (PURO, default de menú/encuesta) con el adapter JSON
	// (tenant_content). El engine ve UN puerto content.Source; el switch por
	// fuente vive SOLO en el Router (el dominio no conoce orígenes). Menú/encuesta
	// sin `content` siguen resolviéndose byte-a-byte por la rama static.
	//
	// EL RESOLUTOR (turnoacotado) es el TERCER escalón del carrito: código exacto →
	// cascada determinista (T3.5-1) → preguntarle al modelo del tenant. Sin esta
	// línea el mecanismo entero está construido y NO LO EJECUTA NADIE —el engine
	// devuelve «sin_resolutor» y el carrito repromptea como siempre—, que es
	// literalmente lo que ya pasó dos veces en este plan. Lo custodia
	// TestTurnoAcotadoCableado, contra el AST de este paquete.
	//
	// Y EL OBSERVADOR de desenlaces, que se cablea igual: el engine no tiene logger
	// —es el núcleo puro de la máquina de estados— así que publica por callback, como
	// el carrito publica los escalones de su cascada. Sin él una degradación sería
	// INDISTINGUIBLE de un turno normal, que es el modo de fallo del best-effort mudo
	// del content. Los tres argumentos son de cardinalidad acotada; el texto del
	// cliente no sale por aquí (engine/consulta.go).
	c.flowEngine = engine.New(c.flowReg,
		engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(c.flowStore))),
		engine.WithConsultaResolver(c.consultaResolver),
		engine.WithConsultaObserver(observaConsultas(c.log)))

	// Puerto ESTRECHO de T2.6/T2.7 (Plan 054 · F3, D-054.6/D-054.8): junta el MISMO
	// flowStore/flowEngine que ya alimentan DefinitionHandler/StartHandler/flowRuntime
	// —cero dependencias nuevas, solo una lectura nueva sobre objetos que YA existen—
	// para responder «¿el flujo de esta regla tiene contenido durable?» en tiempo de
	// CONFIGURACIÓN. Parámetro POSICIONAL de los tres constructores CRUD de la fase de
	// transporte (y de publicapi.Deps.DurableFlowChecker): omitirlo no compila.
	c.durableFlowChecker = flowadmin.NewEngineDurableFlowChecker(c.flowStore, c.flowEngine)

	c.replyLimiter = ratelimit.NewLimiter(rate.Limit(c.cfg.Flow.ReplyRate), c.cfg.Flow.ReplyBurst)

	cablearVentanaDeCaptacion(c)

	// El despachador de nivel superior (Plan 043 · T2.3) SOLO LEE: los eventos vivos
	// del contacto, los tipos que el tenant ofrece (sus reglas event_start) y las
	// features de su plan. Quien crea filas, mueve el puntero y habla es el motor.
	c.dispatcher = events.NewDispatcher(c.eventStore, events.NewTriggerKindOffer(c.triggerStore), c.entResolver)

	c.flowRuntime = construirRuntimeDeFlujos(c)

	// Fuente del gauge wapp_flow_autoreply_streak_max (Plan 049 · Opción A). Va AQUÍ,
	// después de construir el runtime, y NO como una Option, porque la dependencia va
	// AL REVÉS que la del hook de arriba: el histograma lo EMPUJA el motor cuando una
	// racha se cierra (push), pero un gauge se TIRA en el scrape (pull), así que quien
	// tiene que poder preguntar es métricas, y lo que se le inyecta es la función a la
	// que preguntar. El gauge ya está registrado desde metrics.New(); hasta esta línea
	// su fuente es nil y el scrape devuelve 0 — ventana esperada, no un fallo.
	//
	// 🔴 MaxAutoreplyStreak es O(conversaciones vivas) bajo el candado del contador:
	// esta línea es el ÚNICO sitio del que debe colgar. Ver su docstring.
	//
	// 🔴 Y NO ES UNA LECTURA INOCUA: de paso BARRE las rachas vencidas por inactividad
	// y las manda al histograma. Es a propósito —el scrape es el único latido regular
	// que este servicio tiene sin montar una goroutine de fondo (ADR-0003)— y tiene una
	// consecuencia operativa que conviene saber: si nadie raspa /metrics, los episodios
	// abandonados no se cierran nunca. Explicado en streakCounter.Max (streak.go).
	c.mtx.SetFlowAutoreplyStreakMaxSource(c.flowRuntime.MaxAutoreplyStreak)

	// 🔴 LOS DOS CABLES POR LOS QUE ENTRA UN MENSAJE. Sin el primero, el proceso
	// arranca entero, los cuatro listeners levantan y NINGUNA conversación avanza.
	c.gw.OnIncoming = c.flowRuntime.OnIncoming
	c.gw.OnHeartbeat = func(sessionID string, m *cloudlinkv1.Heartbeat) {
		c.log.Debug("heartbeat",
			"session_id", sessionID,
			"lease_counter", m.GetLeaseCounter(),
		)
	}

	c.marca("flujos")
	return nil
}

// cablearVentanaDeCaptacion arma el pool que pide clasificaciones y el agregador de
// ventanas, y los enchufa entre sí y al gateway.
//
// 🔴 EL NUDO DE CONSTRUCCIÓN, dicho porque el orden de estas líneas parece arbitrario
// y no lo es: el agregador PIDE por el pool y el pool RESPONDE al agregador, así que
// se necesitan mutuamente. Se corta con una clausura (SinkFunc) que se resuelve al
// llamar en vez de al construir. La alternativa era un setter público sobre el
// agregador, es decir, dejar el cable mutable en caliente para arreglar un problema
// que solo existe durante el arranque.
func cablearVentanaDeCaptacion(c *contenedor) {
	c.intakeAhead = intakeahead.New(c.log, c.intentStore, c.llmSelector,
		intakeahead.SinkFunc(func(key intake.WindowKey, intent string, confidence float64) {
			c.intakeAggregator.OnClassified(key, intent, confidence)
		}),
		// EL CALENTAMIENTO DE LA CACHÉ DE PREFIJO (T1.7-4). El emisor es el MISMO
		// selector de vía, porque decidir si un tenant tiene caché que calentar es
		// preguntar por la vía y eso se hace en un solo sitio (C2). Sin este cable el
		// pipeline funciona igual: solo vuelve a pagar el prefill frío (~50 s) en la
		// primera inferencia de cada prefijo nuevo.
		intakeahead.WithCalentador(c.llmSelector),
		intakeahead.WithCalentamiento(c.cfg.LLM.WarmupEnabled))

	// El OTRO extremo del mismo cable, y va aquí por el nudo de construcción: el
	// gateway se arma ANTES que el pool (el selector necesita el gateway y el pool
	// necesita el selector), así que el gateway recibe el hook DESPUÉS, con el mismo
	// molde que OnIncoming/OnHeartbeat. El gateway solo dice CUÁNDO se enfrió la caché
	// —sesión registrada, ConfigUpdate empujado—; el qué y el si son del pool.
	c.gw.OnWarmup = c.intakeAhead.Warm

	// 🔴 EL VALOR QUE GOBIERNA ES EL EFECTIVO, Y SE LEE AQUÍ O NO SE LEE.
	//
	// Los dos interruptores existen para el control A/B de campo, y un A/B sobre un
	// valor que no se puede confirmar no prueba nada: hay que poder mirar el arranque y
	// saber en qué lado está ESTA instancia. La lección es cara y de esta casa —un
	// default recalibrado que vivía en el binario y que el `.env` del VPS pisaba, sin
	// que ningún test pudiera verlo—, así que se imprimen los dos JUNTOS y con el
	// nombre de su variable, para que quien lo lea sepa qué tocar.
	//
	// ⚠️ En el VPS esta línea NO va a journald: la unidad escribe a `cloud.log`
	// (StandardOutput=append:), así que se busca ahí y no con `journalctl`.
	c.log.Info("orquestación LLM (Plan 044 · Ola 1.7): interruptores efectivos",
		"warmup_enabled", c.cfg.LLM.WarmupEnabled,
		"warmup_env", "WAPP_LLM_WARMUP_ENABLED",
		"max_output_tokens_enabled", c.cfg.LLM.MaxOutputTokensEnabled,
		"max_output_tokens_env", "WAPP_LLM_MAX_OUTPUT_TOKENS_ENABLED")

	// El AGREGADOR DE VENTANAS (T1.1/T1.2). Tres dependencias y ninguna más:
	//   - intakeJobStore, para escribir la ventana (UNA sentencia por entrante);
	//   - flowStore, para leer `aggregation_window_seconds` EN EL BARRIDO (nunca en
	//     línea con el mensaje: eso sería el SELECT que D-044.26 prohíbe);
	//   - entResolver, el MISMO resolver CACHEADO CON TTL que ya usan el hilo y el
	//     gate del puente CRM. 🔴 No se construye un segundo: dos resolvers serían
	//     dos cachés y dos verdades sobre qué tiene contratado un tenant.
	//
	// ⚠️ Va cableado con WithAggregator en el runtime de abajo y ADEMÁS arrancado con
	// Run() en la fase de fondo: sin el Run, las ventanas se abrirían y jamás se
	// cerrarían, y el fallo sería MUDO.
	//
	// 🔧 Y una CUARTA desde T1.4, que es una opción y no una dependencia del
	// constructor: WithSourceComposer. Sin ella el agregador corre con el noop
	// documentado —las ventanas se cerrarían con el sobre a NULL y el pipeline de la
	// Ola 2 recibiría jobs sin una línea de texto—, así que el cable importa tanto
	// como el código que enchufa.
	//
	// 🔧 Y una QUINTA desde T1.6-4: WithAheadRequester, quien PIDE la clasificación
	// que hasta la Ola 1.6 llegaba adjunta al mensaje (D-044.31 mató el push). Sin
	// ella el agregador no adelanta nunca y toda ventana cierra por su reloj — que es
	// una forma legítima (T1.7), no una avería, pero deja sobre la mesa el minuto de
	// latencia que esta ola existe para recortar.
	c.intakeAggregator = flowruntime.NewIntakeAggregator(c.log, c.intakeJobStore, c.flowStore, c.entResolver,
		flowruntime.WithSourceComposer(c.intakeComposer),
		flowruntime.WithAheadRequester(c.intakeAhead))

	// EL DISPARADOR POR EVENTO (D-044.43). Mismo molde que `gw.OnWarmup` de arriba y
	// sobre el MISMO objeto que se registra en el servidor gRPC de la fase de
	// transporte: el gateway detecta el flanco a READY una sola vez y lo reparte a sus
	// dos consumidores. Sin esta línea el hook queda nil —el estado en que nació— y los
	// jobs de un Edge que acaba de recuperar su Ollama esperarían a que venciera su
	// backoff, hasta 5 minutos, sin que nada lo dijera.
	//
	// `Despertar` cumple la única exigencia del hook —volver en el acto—: es un envío
	// no bloqueante a un canal con buffer, y el hook corre INLINE en la goroutine del
	// Recv del stream.
	c.gw.OnEdgeReady = c.intakePipeline.Despertar
}

// construirRuntimeDeFlujos arma el runtime del Motor con sus opciones. Es la lista de
// cables más larga del arranque y cada uno lleva escrito qué se rompe sin él.
func construirRuntimeDeFlujos(c *contenedor) *flowruntime.Runtime {
	return flowruntime.New(c.flowStore, c.flowEngine, c.gw, c.flowResolver, c.flowDeps.contacts, c.log,
		// WithDecisionThread (T4.5.7a): el MISMO eventStore que gobierna el ciclo de
		// vida escribe las filas `decision` del hilo — el sink solo ve el puerto
		// estrecho DecisionAppender. Una segunda instancia sería un segundo cipher
		// y un segundo reloj sobre conversation_events.
		flowruntime.WithEventSink(flowruntime.NewPersistSink(c.flowStore,
			cart.NewProjector(c.flowStore, c.intakeStore, c.intakeStore, c.buyerDataStore),
			survey.NewProjector(c.flowStore)).WithDecisionThread(c.eventStore)),
		// Puente CRM (Plan 042 · Ola 3): SOLO encola (INV-02); el worker que
		// entrega de verdad se arranca en la fase de fondo.
		//
		// Este sink LEE el `intake_id` que el proyector del carrito anota en
		// eff.Payload al cerrar, así que tiene que correr DESPUÉS del PersistSink.
		// Eso ya NO depende de que esta línea vaya debajo de la anterior: el sink
		// declara PhaseNotify y Runtime.New ordena el fan-out por fase (Plan 042 ·
		// Ola 3.1, ver SinkPhase en flujos/runtime/event_sink.go). El orden de
		// estas dos líneas es legible, no load-bearing.
		flowruntime.WithEventSink(flowruntime.NewWebhookSink(c.log, cart.EffectCartClosed, c.integrationsStore, c.webhookGate)),
		// Ventana de captación (Plan 044 · Ola 1). NO es un EventSink y por eso no
		// entra por WithEventSink: se alimenta del ENTRANTE y no de los efectos de
		// módulo, porque EffectContext no lleva `wa_message_id` (y `source_refs` es
		// justo una lista de ellos) y porque un turno de texto libre —el caso que
		// este plan resuelve— no declara ningún efecto. El porqué entero está en la
		// cabecera de internal/flujos/runtime/aggregator.go.
		flowruntime.WithAggregator(c.intakeAggregator),
		// La BIENVENIDA ÚNICA (Plan 044 · Ola 1.8 · T1.8-2, D6): el «estamos
		// procesando» que el cliente recibe al primer mensaje de una conversación y
		// otra vez tras un silencio largo. Va cableada con el MISMO flowStore que todo
		// lo demás —`conversation_welcomes` es estado conversacional y vive en el
		// repositorio de flujos— y depende del gate por tenant que pone
		// WithEntitlements, más abajo: sin `llm_intake` no manda nada y no escribe una
		// sola fila. Va pegada al agregador porque son la misma promesa vista por las
		// dos caras: aquella acumula lo que el cliente pide, y esta le dice que lo
		// estamos procesando mientras tanto.
		flowruntime.WithWelcomeStore(c.flowStore),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(c.flowStore)),
		flowruntime.WithPresignClient(c.flowDeps.presign),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(c.triggerStore)),
		// El plano de EVENTOS conversacionales (Plan 043 · Ola 2): sin estas dos
		// opciones el motor se comporta exactamente como antes del plan —un
		// event_start arranca su flujo sin parir evento y un event_stop no desactiva
		// nada—, así que van juntas o no van.
		flowruntime.WithEventStore(c.eventStore),
		// El Service satisface el puerto IntakeAbandoner DIRECTO desde que la FK
		// se invirtió (T4.5.5a): AbandonByEvent habla de EVENTOS —vocabulario que
		// el runtime sí conoce—, así que el adapter que traducía ids de intakes
		// murió con la columna conversation_events.intake_id (0054). La puerta
		// sigue siendo el Service y no el Store: ahí vive la frontera del dominio,
		// y el CAS `status='open'` del SQL conserva la garantía de que una
		// `confirmed` jamás se abandona por aquí (ADR-0029 · E-11.5).
		flowruntime.WithIntakeAbandoner(c.intakeService),
		// La fuente DURABLE del resumen del evento abandonado (Plan 043 · T3.4): las
		// líneas del pedido abierto. Sin ella los tres abandonos —salto por tipo,
		// event_stop y escape— ocurren igual pero no dejan rastro en el historial, que
		// es media verdad y no la que queremos en producción.
		flowruntime.WithSummarySources(flowruntime.NewSummarySources(c.flowStore)),
		flowruntime.WithDispatcher(c.dispatcher),
		// La ENTRADA QUE OFRECE (Plan 043 · T3.8, REQ-27/REQ-27b, ADR-0029 · E-9), cableada
		// el 2026-08-12 sobre el MISMO *events.Dispatcher que la línea de arriba — que es
		// exactamente lo que su docstring pedía («en producción lo satisface el MISMO
		// *events.Dispatcher»). Estuvo construida y probada desde el 043 y SIN ENCHUFAR, y
		// eso no fue gratis: con `opening` a nil la rama Fallback de handleTrigger caía
		// SIEMPRE a startPlainFlow —el camino que E-9 vino a reemplazar—, que arranca el
		// flujo del tenant SIN evento padre. Con un tenant cuyo flujo lleva un nodo `cart`,
		// eso es una comanda perdida en silencio contra el NOT NULL de intakes.event_id
		// (migración 0054): medido dos veces en UAT el 2026-08-12, hallazgos #001 y #003 de
		// docs/runbooks/bitacora-errores-uat.md. Con el cable puesto, el entrante que no casa
		// nada recibe los tipos que el tenant habilita y el evento nace por la TERCERA puerta
		// de T2.5 (elección en el despachador) — sin tocar el tiempo muerto: el caso vacío
		// (Offering.Empty) sigue cayendo al fallback de siempre (REQ-27b, INV-20).
		flowruntime.WithOpeningBuilder(c.dispatcher),
		flowruntime.WithFlowForKind(flowForKind{rules: c.triggerStore}),
		flowruntime.WithEntitlements(c.entResolver),
		// ⚰️ Aquí se cableaba el INTERRUPTOR DE DESPLIEGUE del productor `message` del
		// hilo del evento. Lo retiró el Plan 044 · T1.6 el 2026-08-22: era andamiaje
		// con fecha de caducidad —«hasta que el Plan 044 (su LECTOR) exista»— y el 044
		// es esa fecha. El gate NO se fue con él: sigue entero y sigue siendo por
		// tenant, y es la línea de arriba (WithEntitlements) la que lo sostiene — sin
		// `llm_intake`, cero filas. Ver internal/flujos/runtime/thread.go.
		// (Esta lápida vive DENTRO de una lista de argumentos, no sobre una declaración:
		// `revive`/`exported` no la mira. La línea en blanco es solo para que no se lea
		// como si documentara el WithReplyLimiter de debajo.)

		flowruntime.WithReplyLimiter(c.replyLimiter),
		flowruntime.WithIncomingTimeout(c.cfg.Flow.IncomingTimeout),
		flowruntime.WithMaxConcurrentIncoming(c.cfg.Flow.MaxConcurrentIncoming),
		// Guarda anti-self-loop (Plan 020 · T2), que desde el Plan 046 · T4.1 pregunta
		// por ÍNDICE CIEGO y no por el número en claro: por eso el checker necesita
		// ahora el KeyProvider.
		//
		// 🔴 TIENE QUE SER EL MISMO `flowDeps.kp` QUE USA LA PERSISTENCIA (el
		// fleet.NewPostgresRepository de la fase de almacenes), Y ESO NO ES UNA
		// PREFERENCIA DE ESTILO. El bidx es hex(HMAC(indexKey, tenant||0x00||número)):
		// si el escritor y el lector tuvieran DOS KeyProviders con `indexKey` distinta,
		// NINGÚN bidx casaría jamás —IsSelfNumber devolvería false para todos los
		// números propios— y el anti-self-loop DEJARÍA DE BLOQUEAR SIN DAR UN SOLO
		// ERROR: no hay excepción, no hay log, no hay métrica; solo dos sesiones del
		// mismo tenant hablándose para siempre. Un segundo crypto.NewKeyProvider aquí,
		// aunque lea la misma config, sería el mismo desastre si alguien cambia una de
		// las dos fuentes. Por eso se pasa el campo del contenedor y no se construye
		// nada nuevo.
		flowruntime.WithSelfNumbers(flowruntime.NewPostgresSelfNumbers(c.db, c.flowDeps.kp)),
		flowruntime.WithIngestDeduper(ingest.NewPostgresDeduper(c.db)),
		// Contador de los entrantes que NO llegan al motor reactivo (passive /
		// self-loop / rate-limit): los tres cortes son silenciosos por diseño, así que
		// sin esto la única respuesta a «¿por qué no contesta?» era subir el log a
		// debug e inundarlo. Va por callback para que el motor no importe prometheus.
		flowruntime.WithReactiveBlockedHook(c.mtx.FlowReactiveBlocked),
		// Histograma de las rachas de auto-respuestas consecutivas por conversación
		// (Plan 049 · Opción A: OBSERVAR). Mismo desacoplo que la línea de arriba —
		// va por callback para que el motor no importe prometheus— y misma regla: el
		// hook OBSERVA, no decide. No hay umbral ni corte; cortar es la Opción B,
		// aplazada hasta tener 2-4 semanas de esta distribución con la que calibrarlo.
		flowruntime.WithAutoreplyStreakHook(c.mtx.FlowAutoreplyStreak),
		// Tercer toque del recordatorio perezoso de la seña (T4.4): el cliente vuelve
		// a escribir. No añade reloj ninguno — el disparador es el entrante.
		flowruntime.WithDepositReminder(c.depositReminder))
}

// observaConsultas cablea el observador del re-entry de consultas del Motor de
// Flujos (Plan 044 · Ola 3.5 · T3.5-2) al log del proceso.
//
// El engine NO tiene logger —es el núcleo puro de la máquina de estados— y por eso
// publica sus desenlaces por un callback, igual que el carrito publica los
// escalones de su cascada. Se cablea AQUÍ, que es donde hay logger, y se cablea YA
// aunque el resolutor todavía no exista: sin esto, un módulo que pida ayuda que
// nadie le da degradaría en SILENCIO ABSOLUTO —sin log, sin métrica y sin
// distinguirse de un turno normal—, que es el modo de fallo del best-effort mudo
// del content (engine.go). Los tres argumentos son de cardinalidad ACOTADA; el
// texto del cliente no sale por aquí (engine/consulta.go).
//
// Vive como función con nombre y no como closure inline porque gocyclo imputa los
// FuncLit anidados a la función madre.
func observaConsultas(log sharedlogger.Logger) engine.ObservadorConsulta {
	return func(clase, nivel, desenlace string) {
		if desenlace == engine.DesenlaceResuelto {
			log.Info("flujos: consulta resuelta", "clase", clase, "nivel", nivel)
			return
		}
		// Lo demás es degradación (sin resolutor, fallo, no concluyente) o un bug de
		// módulo (bucle): eso sí tiene que verse en el log de campo, que es lo que se
		// raspa cuando algo «no entiende» al cliente.
		log.Warn("flujos: consulta NO resuelta", "clase", clase, "nivel", nivel, "desenlace", desenlace)
	}
}
