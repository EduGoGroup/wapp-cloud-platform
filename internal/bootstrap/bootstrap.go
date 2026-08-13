package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
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
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics/flowlifecycle"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/receipts"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// Run ejecuta el ciclo de vida completo del servidor: carga de config,
// construcción de dependencias, arranque de listeners y espera de parada.
// Devuelve nil en shutdown limpio o el error del primer fallo fatal.
func Run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg)

	// Observabilidad Prometheus (Plan 018 · T10, R11): registry propio compartido
	// por los dos listeners HTTP (métricas de request/latencia/login/rate-limit) y
	// el sink de acuses. /metrics se sirve en el listener admin (:8100), más abajo.
	mtx := metrics.New()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := setupDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeDB(db, log)

	// --- PKI: CA firmante (enroll) + Pool (mTLS) + cert de servidor (ambos). ---
	ca, serverCert, err := loadPKI(cfg)
	if err != nil {
		return err
	}

	// --- Lease (kill-switch): clave de firma + persistencia en PostgreSQL.
	// Construido ANTES que el servidor de enrolamiento (Plan 055 · T4.2,
	// D-055.5): el enrolamiento necesita leaseMgr.PublicKey() para publicarla
	// al Edge en EnrollEdgeResponse.lease_pubkey (H-5). Sin este orden, la
	// pública del lease no existiría todavía cuando buildEnrollServer la
	// necesita. buildLeaseManager solo depende de cfg/db/log —construidos
	// arriba (setupDatabase, línea ~73)— así que adelantarla es seguro: no usa
	// nada de lo que antes se construía entre medias (enrollSrv, cloudEncPriv).
	leaseMgr, err := buildLeaseManager(cfg, db, log)
	if err != nil {
		return err
	}

	// --- Enrolamiento + par X25519 de cifrado de tránsito de la nube (Plan 011
	// §10.F): el enrolamiento publica la pública al Edge; la privada la usa el
	// gateway para abrir el enc_payload sellado al ingreso. También publica la
	// pública del lease (Plan 055 · T4.2), ya disponible por el reordenamiento
	// de arriba. ---
	enrollSrv, cloudEncPriv, err := buildEnrollServer(cfg, db, ca, leaseMgr.PublicKey(), log)
	if err != nil {
		return err
	}

	// --- Acuses persistidos (Plan 018 · T10, R11): los MessageReceipt del Edge
	// (Plan 013) se materializan en message_receipts (migración 0022) de forma
	// idempotente, reemplazando el LogReceiptSink log-only. onRecord alimenta la
	// métrica wapp_receipts_total (delivered|read). CERO PII: solo metadatos. ---
	receiptSink := receipts.NewSink(receipts.NewPostgresStore(db), mtx.Receipt)

	// --- Entitlements (ADR-0022) + config de intenciones (Plan 029): el resolver de
	// features (con caché) es el gate de VERDAD del servidor; el store del blob de
	// intents alimenta el push de config y la API /api/v1/intents. El provider ata el
	// kind "intents" al push al conectar (el Gateway queda genérico). ---
	entResolver := entitlements.NewPostgres(db)
	intentStore := intentcfg.NewPostgresStore(db)

	// --- Diagnóstico remoto (Plan 031 · T5, ADR-0023 capa 3): el store persiste las
	// solicitudes/bundles y el consentimiento por tenant. Se comparte entre el Gateway
	// (recibe el DiagnosticsBundle por el demux) y la API pública (emite el request y
	// sirve la descarga). ---
	diagStore := diagnostics.NewPostgres(db)

	// --- Plano de auth de usuario del IAM (Plan 018 · T3, ADR-0019). Se construye
	// AQUÍ (antes que el gateway y que el servidor público) porque el gateway
	// CloudLink lo consume para las RPCs UserLogin/Refresh/Logout del Edge (Plan
	// 033 · T2.2, ADR-0025) y el servidor :8103 lo reusa tal cual. ---
	authStk, err := buildAuthStack(cfg, db, log)
	if err != nil {
		return err
	}
	// Config kind:"jwks" (ADR-0025 dec.2): la pública ES256 del emisor de usuario,
	// que el Edge verifica offline. Es GLOBAL del emisor ⇒ se entrega a todo Edge
	// que conecta (jwksConfigProvider), delegando el kind "intents" al provider
	// existente. La rotación reusa gw.PushConfig(ctx, tenant, "jwks", version, payload).
	jwksCfg, err := buildJWKSConfig(authStk.jwtBundle.esPub, authStk.jwtBundle.kid)
	if err != nil {
		return err
	}

	// --- Fleet + Gateway CloudLink. ---
	gw := gatewaygrpc.New(
		// Deadline por Send hacia el Edge (Plan 027 · Ola 1 · T5, cierra H6): un Edge
		// lento no retiene al llamante ni atasca el kill-switch (env WAPP_GRPC_PUSH_TIMEOUT).
		session.NewRegistry(session.WithSendTimeout(cfg.GRPCPushTimeout)),
		log,
		// Deadline por ESPERA DEL ACK (env WAPP_GRPC_ACK_TIMEOUT): el hermano del
		// anterior. Aquel acota el empuje; este, la respuesta. Sin él, un Edge
		// saturado cuelga al llamante HTTP indefinidamente (2026-08-06).
		gatewaygrpc.WithAckTimeout(cfg.GRPCAckTimeout),
		gatewaygrpc.WithLease(leaseMgr),
		gatewaygrpc.WithFleet(fleet.NewPostgresRepository(db)),
		gatewaygrpc.WithCloudEncPrivKey(cloudEncPriv),
		gatewaygrpc.WithReceiptSink(receiptSink),
		// Push de config al conectar (ADR-0021): entrega SIEMPRE la pública ES256
		// (kind:"jwks", ADR-0025) y, encadenado, la config de intents vigente del tenant
		// SOLO si tiene la feature llm_intent y hay config persistida.
		gatewaygrpc.WithConfigProvider(jwksConfigProvider{
			jwks: jwksCfg,
			next: intentsConfigProvider{store: intentStore, ents: entResolver},
		}),
		// Recepción del DiagnosticsBundle (Plan 031 · T5, ADR-0023): el demux correlaciona
		// el bundle con su solicitud pendiente por command_id y lo almacena.
		gatewaygrpc.WithDiagnosticsSink(diagStore),
		// Auth de usuario del plano de control del Edge (Plan 033 · T2.2, ADR-0025): el
		// gateway delega UserLogin/Refresh/Logout en un puerto de autenticación y audita
		// edge.auth.* (CERO PII) con el mismo auditor del :8103. Detrás del puerto está
		// identity-core si la delegación de la Ola 3 está encendida (WAPP_IDENTITY_URL),
		// o el IAM local si no; el gateway no distingue los dos casos.
		gatewaygrpc.WithAuthenticator(authStk.edgeAuthenticator()),
		gatewaygrpc.WithAuthAuditor(authStk.auditor),
	)

	// --- Motor de Flujos (Pieza 05): registro de módulos + engine + store +
	// runtime, sobre el *sql.DB ya abierto. Se enchufa a gw.OnIncoming (cada
	// entrante avanza la conversación viva; sin estado se ignora, decisión C) y
	// expone los endpoints admin /admin/flows y /admin/flows/start (más abajo). ---
	flowReg := modules.NewRegistry()
	flowReg.Register(menu.New())
	flowReg.Register(survey.New())
	// El carrito recibe el logger para AVISAR de los campos del catálogo v2 que
	// su parseo tolerante descarta (Plan 041 · T2.2): en runtime un catálogo a
	// medias sigue vendiendo, pero el dueño tiene que poder enterarse de qué
	// parte suya quedó fuera. Sin logger el módulo funciona igual, en silencio.
	flowReg.Register(cart.New(cart.WithLogger(log)))
	flowReg.Register(media.New()) // Plan 017: nodo "media" (envía archivos por WhatsApp)
	flowStore := flowstore.NewPostgresRepository(db)
	// Fuente de contenido enrutada POR-NODO (Plan 015 T4a): el Router compone el
	// adapter Static (PURO, default de menú/encuesta) con el adapter JSON
	// (tenant_content). El engine ve UN puerto content.Source; el switch por
	// fuente vive SOLO en el Router (el dominio no conoce orígenes). Menú/encuesta
	// sin `content` siguen resolviéndose byte-a-byte por la rama static.
	flowEngine := engine.New(flowReg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(flowStore))))
	flowResolver := flowruntime.NewPostgresTenantResolver(db)
	// Dependencias del Motor que se construyen con fail-fast: el resolver de
	// contactos (cifrado de PII, Plan 011) y el almacén de objetos R2 (Plan 017).
	// Se agrupan para no cargar el arranque con dos ramas de error separadas.
	flowDeps, err := buildFlowRuntimeDeps(ctx, cfg, db)
	if err != nil {
		return err
	}

	triggerStore := trigger.NewPostgresStore(db)
	// Puerto ESTRECHO de T2.6/T2.7 (Plan 054 · F3, D-054.6/D-054.8): junta el MISMO
	// flowStore/flowEngine que ya alimentan DefinitionHandler/StartHandler/flowRuntime
	// —cero dependencias nuevas, solo una lectura nueva sobre objetos que YA existen—
	// para responder «¿el flujo de esta regla tiene contenido durable?» en tiempo de
	// CONFIGURACIÓN. Parámetro POSICIONAL de los tres constructores CRUD de abajo (y
	// de publicapi.Deps.DurableFlowChecker): omitirlo no compila.
	durableFlowChecker := flowadmin.NewEngineDurableFlowChecker(flowStore, flowEngine)
	replyLimiter := ratelimit.NewLimiter(rate.Limit(cfg.Flow.ReplyRate), cfg.Flow.ReplyBurst)
	// El store de SOLICITUDES lo comparten dos consumidores: el proyector del
	// carrito, que le cuelga la revisión 1 al cerrar (ADR-0031 §3) y le pone la
	// línea de envío (D-041.11), y la API pública, que lee la bandeja. Es el mismo
	// pool y el mismo dominio: dos instancias solo serían dos nombres para lo mismo.
	// Por eso el proyector lo recibe DOS VECES: satisface sus dos puertos —escritor
	// de revisiones y garante del envío— sin que el carrito conozca el store entero.
	intakeStore := intakes.NewPostgres(db)
	// Los DATOS DEL COMPRADOR van por su propio escritor y no por intakeStore (T4.5,
	// D-041.13): es el único componente del dominio de solicitudes que necesita el
	// cipher de PII, y tenerlo aparte hace que el store normal —que lo consumen la
	// API pública, el notificador y el proyector— no pueda cifrar ni descifrar nada.
	// Reusa el MISMO stack de claves que los contactos (flowDeps.cipher, KEK del
	// keyring versionado del Plan 012): dos ciphers serían dos rotaciones.
	buyerDataStore := intakes.NewPostgresBuyerData(db, flowDeps.cipher)
	// El puente CRM (Plan 042 · Ola 3) reusa el MISMO cipher que buyerDataStore
	// (mismo keyring versionado del Plan 012): el secreto HMAC de
	// tenant_integrations y los datos del comprador comparten el stack de
	// claves, no hay una tercera rotación que gestionar.
	integrationsStore := integrations.NewPostgres(db, flowDeps.cipher)
	webhookGate := integrations.NewEntitlementsGate(entResolver, integrationsStore, entitlements.FeatureCRMBridge)
	// El aviso al cliente y el recordatorio de la seña son la MISMA salida hacia
	// WhatsApp con dos motivos (D-041.14 y D-041.12): el notificador se construye una
	// vez y el recordatorio lo reusa entero —texto de la seña, vía custodiada de PII y
	// SendText del Gateway—. Se arma AQUÍ, antes del runtime, porque el recordatorio
	// tiene tres disparadores y dos de ellos están en sitios distintos: el motor
	// (cuando el cliente escribe) y las lecturas del dueño (más abajo, en el Service).
	// Es un solo objeto: dos serían dos criterios de "ya recordé".
	intakeNotifier := intakes.NewNotifier(gw, flowDeps.contacts, intakeStore, log)
	depositReminder := intakes.NewDepositReminder(intakeNotifier, intakeStore)
	// El Service de solicitudes se arma AQUÍ, antes del runtime, porque desde el Plan
	// 043 tiene DOS consumidores: la API del dueño (Deps.Intakes, más abajo) y el
	// motor, que abandona la solicitud del evento que el cliente cierra al empezar
	// otro del mismo tipo (E-11). Es un solo objeto a propósito: dos serían dos
	// máquinas de estados opinando sobre las mismas filas.
	intakeService := intakes.NewService(intakeStore,
		intakes.WithNotifier(intakeNotifier),
		intakes.WithDepositReminder(depositReminder))
	// El almacén del EVENTO conversacional (Plan 043 · Ola 1) reusa el MISMO cipher
	// que los contactos y los datos del comprador: el historial del evento guarda
	// texto literal del cliente y va cifrado con el keyring versionado del Plan 012.
	// Una tercera instancia de cipher sería una tercera rotación que gestionar.
	eventStore := events.NewStore(db, flowDeps.cipher)
	// El despachador de nivel superior (Plan 043 · T2.3) SOLO LEE: los eventos vivos
	// del contacto, los tipos que el tenant ofrece (sus reglas event_start) y las
	// features de su plan. Quien crea filas, mueve el puntero y habla es el motor.
	dispatcher := events.NewDispatcher(eventStore, events.NewTriggerKindOffer(triggerStore), entResolver)
	flowRuntime := flowruntime.New(flowStore, flowEngine, gw, flowResolver, flowDeps.contacts, log,
		// WithDecisionThread (T4.5.7a): el MISMO eventStore que gobierna el ciclo de
		// vida escribe las filas `decision` del hilo — el sink solo ve el puerto
		// estrecho DecisionAppender. Una segunda instancia sería un segundo cipher
		// y un segundo reloj sobre conversation_events.
		flowruntime.WithEventSink(flowruntime.NewPersistSink(flowStore,
			cart.NewProjector(flowStore, intakeStore, intakeStore, buyerDataStore),
			survey.NewProjector(flowStore)).WithDecisionThread(eventStore)),
		// Puente CRM (Plan 042 · Ola 3): SOLO encola (INV-02); el worker que
		// entrega de verdad se arranca más abajo, después de serveAndWait.
		//
		// Este sink LEE el `intake_id` que el proyector del carrito anota en
		// eff.Payload al cerrar, así que tiene que correr DESPUÉS del PersistSink.
		// Eso ya NO depende de que esta línea vaya debajo de la anterior: el sink
		// declara PhaseNotify y Runtime.New ordena el fan-out por fase (Plan 042 ·
		// Ola 3.1, ver SinkPhase en flujos/runtime/event_sink.go). El orden de
		// estas dos líneas es legible, no load-bearing.
		flowruntime.WithEventSink(flowruntime.NewWebhookSink(log, cart.EffectCartClosed, integrationsStore, webhookGate)),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(flowStore)),
		flowruntime.WithPresignClient(flowDeps.presign),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(triggerStore)),
		// El plano de EVENTOS conversacionales (Plan 043 · Ola 2): sin estas dos
		// opciones el motor se comporta exactamente como antes del plan —un
		// event_start arranca su flujo sin parir evento y un event_stop no desactiva
		// nada—, así que van juntas o no van.
		flowruntime.WithEventStore(eventStore),
		// El Service satisface el puerto IntakeAbandoner DIRECTO desde que la FK
		// se invirtió (T4.5.5a): AbandonByEvent habla de EVENTOS —vocabulario que
		// el runtime sí conoce—, así que el adapter que traducía ids de intakes
		// murió con la columna conversation_events.intake_id (0054). La puerta
		// sigue siendo el Service y no el Store: ahí vive la frontera del dominio,
		// y el CAS `status='open'` del SQL conserva la garantía de que una
		// `confirmed` jamás se abandona por aquí (ADR-0029 · E-11.5).
		flowruntime.WithIntakeAbandoner(intakeService),
		// La fuente DURABLE del resumen del evento abandonado (Plan 043 · T3.4): las
		// líneas del pedido abierto. Sin ella los tres abandonos —salto por tipo,
		// event_stop y escape— ocurren igual pero no dejan rastro en el historial, que
		// es media verdad y no la que queremos en producción.
		flowruntime.WithSummarySources(flowruntime.NewSummarySources(flowStore)),
		flowruntime.WithDispatcher(dispatcher),
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
		flowruntime.WithOpeningBuilder(dispatcher),
		flowruntime.WithFlowForKind(flowForKind{rules: triggerStore}),
		flowruntime.WithEntitlements(entResolver),
		// Segunda condición del productor `message` del hilo (D-043.23, decisión de
		// Jhoan del 2026-08-10): la feature llm_intake YA NO basta por sí sola —
		// hace falta ADEMÁS este interruptor de despliegue, apagado por defecto
		// (WAPP_CONVERSATION_THREAD_MESSAGES=false), hasta que el Plan 044 (su
		// lector) exista. Ver internal/flujos/runtime/thread.go.
		flowruntime.WithMessageThreadEnabled(cfg.ConversationThread.Messages),
		flowruntime.WithReplyLimiter(replyLimiter),
		flowruntime.WithIncomingTimeout(cfg.Flow.IncomingTimeout),
		flowruntime.WithMaxConcurrentIncoming(cfg.Flow.MaxConcurrentIncoming),
		flowruntime.WithSelfNumbers(flowruntime.NewPostgresSelfNumbers(db)),
		flowruntime.WithIngestDeduper(ingest.NewPostgresDeduper(db)),
		// Contador de los entrantes que NO llegan al motor reactivo (passive /
		// self-loop / rate-limit): los tres cortes son silenciosos por diseño, así que
		// sin esto la única respuesta a «¿por qué no contesta?» era subir el log a
		// debug e inundarlo. Va por callback para que el motor no importe prometheus.
		flowruntime.WithReactiveBlockedHook(mtx.FlowReactiveBlocked),
		// Tercer toque del recordatorio perezoso de la seña (T4.4): el cliente vuelve
		// a escribir. No añade reloj ninguno — el disparador es el entrante.
		flowruntime.WithDepositReminder(depositReminder))

	gw.OnIncoming = flowRuntime.OnIncoming
	gw.OnHeartbeat = func(sessionID string, m *cloudlinkv1.Heartbeat) {
		log.Debug("heartbeat",
			"session_id", sessionID,
			"lease_counter", m.GetLeaseCounter(),
		)
	}

	// --- Servidor Enrollment: TLS de servidor SOLAMENTE (sin cert de cliente). ---
	enrollGS := grpc.NewServer(grpc.Creds(EnrollServerCreds(serverCert)))
	enrollSrv.Register(enrollGS)
	enrollLis, err := net.Listen("tcp", cfg.GRPCEnrollAddr)
	if err != nil {
		return fmt.Errorf("escuchando enrollment en %s: %w", cfg.GRPCEnrollAddr, err)
	}

	// --- Servidor CloudLink: mTLS estricto contra la MISMA CA. ---
	connectGS := grpc.NewServer(
		grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             15 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	gw.Register(connectGS)
	connectLis, err := net.Listen("tcp", cfg.GRPCConnectAddr)
	if err != nil {
		return fmt.Errorf("escuchando cloudlink en %s: %w", cfg.GRPCConnectAddr, err)
	}

	fleetRepo := fleet.NewPostgresRepository(db)
	publicSrv, authMW, auditor, err := buildPublicAPIServer(cfg, log, mtx, authStk, publicapi.Deps{
		Sender: gw,
		FlowDeps: publicapi.FlowDeps{
			Flows:   flowStore,
			Modules: flowReg,
			Starter: flowRuntime,
		},
		SessionDeps: publicapi.SessionDeps{
			Sessions:      fleetRepo,
			SessionRoles:  fleetRepo,
			SessionStatus: fleetRepo,
		},
		MediaDeps: publicapi.MediaDeps{
			Media:           flowDeps.presign,
			Content:         flowStore,
			ContentMaxBytes: cfg.TenantContent.MaxBytes,
			ContentVersions: flowStore,
			ImportMaxItems:  cfg.Import.MaxItems,
		},
		DiagDeps: publicapi.DiagDeps{
			Diagnostics:          diagStore,
			DiagnosticsRequester: gw,
			DiagnosticsBundleTTL: cfg.Diagnostics.BundleTTL,
		},
		Triggers:            triggerStore,
		TriggersDurableFlow: durableFlowChecker,
		Intents:             intentStore,
		Entitlements:        entResolver,
		// El notificador (D-041.14 · T4.2) usa las MISMAS tres piezas que ya usa el
		// motor para hablarle a un contacto: el Gateway como sender, el resolver
		// custodiado de PII para el destino y el store de solicitudes para la config
		// del tenant. No hay un segundo camino de salida hacia WhatsApp.
		//
		// El recordatorio de la seña (D-041.12 · T4.4) entra por su propia opción
		// porque cuelga de otro sitio: no de la transición, sino de las LECTURAS del
		// dueño (listado y detalle), que es lo que en esta plataforma hace de reloj.
		Intakes: intakeService,
		// La bandeja de EVENTOS conversacionales (Plan 043 · T3.9b) lee del MISMO
		// store que el motor y el despachador: es la misma consulta de rescatables
		// leída desde el lado del dueño, y una segunda instancia sería un segundo
		// reloj opinando sobre qué está vencido.
		ConversationEvents: eventStore,
		// La cancelación de esa bandeja (Plan 043 · T4.2/T4.3) la sirve el MISMO
		// runtime del motor —no el store— porque cancelar orquesta tres efectos:
		// guard del evento, puntero del flow_state y solicitud colgante. El runtime
		// ya viene armado arriba con WithEventStore y WithIntakeAbandoner; sin este
		// cable la ruta POST …/{id}/cancel no se monta.
		EventCanceller:  flowRuntime,
		TenantVariables: tenantvars.NewPostgres(db),
		// La VUELTA del puente CRM (Plan 042 · T4.2/T4.3/T4.4). Las cuatro piezas ya
		// existen arriba y se reutilizan tal cual: el MISMO store que guarda el secreto
		// de la ida, el MISMO gate que decide si se encola, el store de solicitudes y el
		// notificador del Plan 041. Nada de esto es nuevo salvo el cable.
		// La CONFIGURACIÓN del puente (Plan 042 · T5.1): el MISMO store, otra
		// pregunta. El CRUD lee y escribe tenant_integrations; el secreto solo
		// entra (write-only) y sale como huella.
		Integrations: integrationsStore,
		CRMSecrets:   integrationsStore,
		CRMGate:      webhookGate,
		CRMReflect:   intakeStore,
		CRMNotify:    intakeNotifier,
		ConfigPush:   gw,
		Health:       publicapi.HealthRules{DegradedAfter: cfg.Health.DegradedAfter, StaleAfter: cfg.Health.StaleAfter},
		// Telemetría de ciclo de vida del evento conversacional (Plan 043 ·
		// T6.5, cierra MD-043.17): SQL directo sobre el MISMO *sql.DB que ya
		// comparte toda la plataforma — no una segunda conexión ni un segundo
		// pool. Ver el comentario de propiedad en
		// internal/publicapi/eventstelemetry_store.go.
		EventTelemetry: publicapi.NewPostgresEventTelemetryStore(db),
	})
	if err != nil {
		return err
	}

	// --- HTTP: health + admin interno. ---
	checker := httpapi.NewHealthChecker()
	checker.Register(postgres.NewHealthCheck(db))
	mux := http.NewServeMux()
	mux.Handle("/healthz", httpapi.HealthHandler(checker))
	mux.Handle("/metrics", mtx.PromHandler())
	mux.Handle("/admin/leases/revoke", adminHandler(authMW, auditor, log,
		"leases.revoke", "lease", httpapi.RevokeLeaseHandler(gw)))
	// Kill-switch COMERCIAL por tenant (Plan 055 · T3.3, D-055.2): hermano del
	// de leases, mismo patrón adminHandler (auth + permiso + auditoría). El rol
	// tenant_admin ya cubre "tenants.revoke"/"tenants.restore" con su grant '*'
	// (0015_iam_roles.sql): no hace falta migración de IAM nueva (design.md §1.3).
	mux.Handle("/admin/tenants/revoke", adminHandler(authMW, auditor, log,
		"tenants.revoke", "tenant", httpapi.RevokeTenantHandler(gw)))
	mux.Handle("/admin/tenants/restore", adminHandler(authMW, auditor, log,
		"tenants.restore", "tenant", httpapi.RestoreTenantHandler(gw)))
	mux.Handle("/admin/messages/send", adminHandler(authMW, auditor, log,
		"messages.send", "message", httpapi.SendMessageHandler(gw, log)))
	mux.Handle("/admin/crypto/rekey", adminHandler(authMW, auditor, log,
		"crypto.rekey", "kek", httpapi.CryptoRekeyHandler(
			func(ctx context.Context, batch int) (crypto.Report, error) {
				return crypto.Rekey(ctx, db, flowDeps.cipher, flowDeps.kp, batch)
			},
		)))
	mux.Handle("/admin/flows", adminHandler(authMW, auditor, log,
		"flows.create", "flow", flowadmin.DefinitionHandler(flowStore, flowReg)))
	mux.Handle("/admin/flows/start", adminHandler(authMW, auditor, log,
		"flows.start", "flow", flowadmin.StartHandler(flowRuntime)))
	mux.Handle("POST /admin/triggers", adminHandler(authMW, auditor, log,
		"triggers.create", "trigger", flowadmin.CreateTriggerHandler(triggerStore, durableFlowChecker)))
	mux.Handle("GET /admin/triggers", adminHandler(authMW, auditor, log,
		"triggers.read", "trigger", flowadmin.ListTriggersHandler(triggerStore, durableFlowChecker)))
	mux.Handle("DELETE /admin/triggers/{id}", adminHandler(authMW, auditor, log,
		"triggers.delete", "trigger", flowadmin.DeleteTriggerHandler(triggerStore, durableFlowChecker)))
	mux.Handle("POST /admin/sessions/{id}/role", adminHandler(authMW, auditor, log,
		"sessions.write", "session", flowadmin.SetSessionRoleHandler(fleetRepo)))
	mux.Handle("POST /admin/sessions/{id}/status", adminHandler(authMW, auditor, log,
		"sessions.write", "session", flowadmin.SetSessionStatusHandler(fleetRepo)))

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mtx.InstrumentHTTP("admin", mux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Worker del puente CRM (Plan 042 · Ola 3, D-042.4): primera goroutine de
	// polling de larga vida de este repo. Arranca sobre el MISMO ctx derivado de
	// signal.NotifyContext (línea ~68) que cierra todo lo demás — un solo
	// Ctrl+C también para el worker, sin un segundo mecanismo de shutdown.
	// intakeStore entra aquí como TERCER completador del payload (Ola 5): la
	// indicación del cliente ya no viaja congelada en webhook_outbox.payload —era
	// PII en claro sobreviviendo a la entrega— y se lee de public.intakes justo
	// antes del POST, igual que buyerDataStore descifra buyer_data. Es el MISMO
	// store que ya usan el proyector y la API pública: uno solo, no dos nombres
	// para lo mismo.
	webhookWorker := integrations.NewWorker(
		integrationsStore, buyerDataStore, intakeStore, tenantvars.NewPostgres(db), log,
		integrations.WorkerConfig{
			PollInterval: cfg.Webhook.PollInterval,
			MaxAttempts:  cfg.Webhook.MaxAttempts,
			Timeout:      cfg.Webhook.Timeout,
		},
		mtx.WebhookDelivery,
	)
	go webhookWorker.Run(ctx)

	// Colector incremental de telemetría de eventos (Plan 043 · T6.5, cierra
	// MD-043.17): PRIMER consumidor de PRODUCCIÓN del outbox append-only
	// flow_events, no un assert de test (T6.2 ya lo lee desde un test y eso NO
	// cierra el hallazgo — ver tasks.md §T6.5). Arranca sobre el MISMO ctx
	// derivado de signal.NotifyContext que cierra todo lo demás, mismo trato
	// que webhookWorker. onCount es mtx.FlowEventLifecycle: el colector
	// (internal/platform/metrics/flowlifecycle) NUNCA importa prometheus ni
	// internal/flujos, mismo desacoplo que receiptSink/webhookWorker de
	// arriba.
	flowLifecycleCollector := flowlifecycle.NewCollector(db, mtx.FlowEventLifecycle, log)
	go flowLifecycleCollector.Run(ctx)

	//nolint:contextcheck // shutdownAll parte de context.Background() a propósito: corre
	// cuando ctx ya está cancelado, y derivar de él abortaría el cierre gracioso al instante.
	return serveAndWait(ctx.Done(), log,
		httpServer{srv: httpSrv, name: "admin/health"},
		httpServer{srv: publicSrv, name: "API pública"},
		grpcServer{gs: enrollGS, lis: enrollLis, addr: cfg.GRPCEnrollAddr, name: "Enrollment (TLS de servidor)"},
		grpcServer{gs: connectGS, lis: connectLis, addr: cfg.GRPCConnectAddr, name: "CloudLink (mTLS)"},
	)
}

type httpServer struct {
	srv  *http.Server
	name string
}

type grpcServer struct {
	gs   *grpc.Server
	lis  net.Listener
	addr string
	name string
}

func serveAndWait(done <-chan struct{}, log sharedlogger.Logger, admin, public httpServer, enroll, connect grpcServer) error {
	errCh := make(chan error, 4)
	go serveGRPC(errCh, log, enroll)
	go serveGRPC(errCh, log, connect)
	go serveHTTP(errCh, log, admin)
	go serveHTTP(errCh, log, public)

	select {
	case <-done:
		log.Info("señal de parada recibida, cerrando")
	case serveErr := <-errCh:
		log.Error("fallo de un servidor", "error", serveErr)
		shutdownAll(admin.srv, public.srv, enroll.gs, connect.gs, log)
		return serveErr
	}
	shutdownAll(admin.srv, public.srv, enroll.gs, connect.gs, log)
	log.Info("servidor detenido limpiamente")
	return nil
}

func serveGRPC(errCh chan<- error, log sharedlogger.Logger, s grpcServer) {
	log.Info("servidor gRPC iniciado", "name", s.name, "addr", s.addr)
	if err := s.gs.Serve(s.lis); err != nil {
		errCh <- fmt.Errorf("%s gRPC: %w", s.name, err)
	}
}

func serveHTTP(errCh chan<- error, log sharedlogger.Logger, s httpServer) {
	log.Info("servidor HTTP iniciado", "name", s.name, "addr", s.srv.Addr)
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("http %s: %w", s.name, err)
	}
}

func shutdownAll(httpSrv, publicSrv *http.Server, enrollGS, connectGS *grpc.Server, log sharedlogger.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP admin", "error", err)
	}
	if err := publicSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP público", "error", err)
	}
	gracefulStopGRPC(enrollGS, "enroll", log)
	gracefulStopGRPC(connectGS, "cloudlink", log)
}

func gracefulStopGRPC(gs *grpc.Server, name string, log sharedlogger.Logger) {
	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		log.Warn("shutdown gRPC: GracefulStop excedió el timeout; forzando Stop()",
			"servidor", name, "timeout", shutdownTimeout)
		gs.Stop()
		<-done
	}
}

// flowForKind resuelve «qué flujo arranca este tipo de evento» leyendo las reglas
// kind='event_start' del tenant. Lo necesita la opción «empezar uno nuevo» del menú
// del despachador, que dice el TIPO pero no el flujo.
//
// La fuente es la misma que la de los tipos ofrecibles (events.TriggerKindOffer), y
// eso no es casualidad: un tipo está ofrecido porque el tenant le puso una palabra, y
// esa misma regla es la que dice a qué flujo lleva. Dos fuentes distintas podrían
// ofrecer un tipo que luego no arrancara nada.
type flowForKind struct{ rules *trigger.PostgresStore }

// FlowForKind devuelve el flow_id de la primera regla event_start HABILITADA de ese
// tipo, en el orden determinista del store. "" si no hay ninguna: no es un error —el
// tipo `menu` no tiene flujo por diseño (D-043.3).
func (f flowForKind) FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	rules, err := f.rules.ListByKind(ctx, tenantID, sessionID, trigger.KindEventStart)
	if err != nil {
		return "", fmt.Errorf("bootstrap: leer las reglas event_start: %w", err)
	}
	for _, r := range rules {
		if r.Enabled && r.EventKind == kind && r.FlowID != "" {
			return r.FlowID, nil
		}
	}
	return "", nil
}
