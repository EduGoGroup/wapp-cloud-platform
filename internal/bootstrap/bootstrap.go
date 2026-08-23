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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/filtercfg"
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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/receipts"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
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

	// El pool de conexiones, en /metrics (Plan 050 · T4.3): las seis series
	// wapp_db_* — entre ellas WaitCount y WaitDuration, la prueba DIRECTA de que
	// alguien esperó por una conexión, que este proyecto no había medido nunca.
	// Las lee T5.5 para levantar la curva con la que T4.6 decide DEUDA-050.2.
	//
	// Va AQUÍ y no dentro de metrics.New() porque cuando se construyen las
	// métricas el *sql.DB todavía no existe: la base se abre veinte líneas más
	// abajo. Registrar sobre el registry ya creado es legal (prometheus.Registry
	// se protege por dentro), y es la primera vez que este repo lo hace.
	//
	// Se aborta el arranque si falla: el único error posible es un choque de
	// nombres en el registry, o sea un bug de programación, y esos se ven al
	// arrancar o no se ven nunca. Un fallo de la base no llega hasta aquí.
	if err := mtx.RegisterDBStats(db); err != nil {
		return err
	}

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

	// --- Dependencias del Motor que se construyen con fail-fast: el resolver de
	// contactos (cifrado de PII, Plan 011) y el almacén de objetos R2 (Plan 017).
	// Se agrupan para no cargar el arranque con dos ramas de error separadas.
	//
	// 🔴 SE ADELANTÓ AQUÍ EN EL PLAN 046 · T4.1. Antes se construía ~65 líneas más
	// abajo, junto al resto del Motor de Flujos, porque solo el Motor lo usaba. Ya
	// no: desde que `fleet_sessions.self_pn` va cifrado, el repositorio de FLOTA
	// —que se construye en el bloque de abajo— necesita el MISMO cipher y el MISMO
	// KeyProvider. Adelantarlo es seguro: buildFlowRuntimeDeps solo depende de
	// ctx/cfg/db, todos vivos desde setupDatabase, y no toca nada de lo que se
	// construye entre medias.
	//
	// ⚠️ LO QUE SÍ CAMBIA EL ADELANTO ES QUÉ ERROR DE ARRANQUE SALE PRIMERO (anotado
	// el 2026-08-21: el comentario original solo hablaba del cipher y daba a entender
	// que este bloque no hacía nada más). buildFlowRuntimeDeps construye DOS cosas,
	// no una (flows.go:36-77): el KeyProvider + FieldCipher, y ADEMÁS el cliente de
	// presign de R2 (Plan 017). Y con `WAPP_KEK_PROVIDER=kms` el primero hace una
	// LLAMADA DE RED A GCP KMS al arrancar (ADR-0036 §3: el KMS interviene una vez,
	// en el arranque). Consecuencia: en un despliegue con, a la vez, credenciales de
	// R2 mal puestas y —pongamos— el emisor JWT mal configurado, ahora se ve el error
	// de R2/KMS y antes se veía el otro. NO rompe nada —los dos son fail-fast y los
	// dos abortan igual— pero quien depure un arranque caído leyendo el primer error
	// del log tiene que saber que el orden se movió a propósito y por esta línea.
	//
	// 📌 «EL MISMO» no es una comodidad: es la condición de que haya UNA rotación de
	// KEK. Un segundo crypto.NewKeyProvider aquí crearía un segundo keyring que
	// crypto.Rekey (línea ~548) no conoce, y el día de la rotación las filas de
	// flota se quedarían atrás sin que nadie lo notara hasta no poder abrirlas.
	//
	// ⚠️ AVISO AL SIGUIENTE: el backfill de arranque de T4.1 (que sanea las filas
	// que ya tenían número en claro) necesita este mismo `flowDeps.cipher` y
	// `flowDeps.kp`. Están disponibles desde esta línea hasta el final de Run.
	flowDeps, err := buildFlowRuntimeDeps(ctx, cfg, db)
	if err != nil {
		return err
	}

	// --- Fleet + Gateway CloudLink. ---
	//
	// 📌 fleetRepo se construye AQUÍ, una sola vez, y lo comparten los cuatro
	// consumidores: el Gateway (WithFleet), el provider de filters (T2.1), las rutas
	// de sesión de la API pública y las de admin. Hasta el Plan 046 · T2.1 había DOS
	// instancias —esta, inline dentro de WithFleet, y otra más abajo— sobre el MISMO
	// *sql.DB; no era un bug (el repo no tiene estado) pero sí una invitación a que
	// mañana lo tuviera y las dos mitades vieran cosas distintas.
	//
	// 🔒 Desde T4.1 SÍ tiene estado que importa: el cipher y el KeyProvider con los
	// que abre y cierra el sobre del self_pn. El logger va enchufado a propósito —es
	// la ÚNICA vía por la que se entera nadie de que un sobre no descifró al servir
	// el listado; sin él, ese fallo sería un campo vacío y ni una línea de log.
	fleetRepo := fleet.NewPostgresRepository(db, flowDeps.cipher, flowDeps.kp,
		fleet.WithLogger(log))

	// 📌 AQUÍ VIVÍAN LOS DOS BACKFILLS CIFRADOS DEL PLAN 046 (T4.1 y T4.2), y se dice
	// en vez de dejar el hueco: cifraban las filas que todavía tenían el número propio
	// y el nombre del contacto EN CLARO —las anteriores a las migraciones 0068 y 0069—
	// y vaciaban esas dos columnas. Corrían síncronos y bloqueando el arranque, después
	// de las migraciones y antes de que se abriera un solo listener.
	//
	// Se retiraron con la 0070 (T5.4), que BORRA las dos columnas en claro: ya no hay
	// nada que migrar, y su primer SELECT —`WHERE self_pn IS NOT NULL`— habría abortado
	// el arranque con «column does not exist». Van en el MISMO commit por eso: no es
	// limpieza posterior, es la otra mitad de esa migración.
	//
	// 🔴 Lo que se fue con ellos, dicho para que nadie lo busque: la consulta
	// `count(*) WHERE self_pn IS NOT NULL = 0` que acreditaba el saneo. Ya no se puede
	// hacer —la columna que cuenta no existe— y su última ejecución contra UAT quedó
	// anotada en el journal (criterio (c) de T5.4). La garantía pasó de ser un conteo
	// a ser el esquema: no hay dónde escribir un teléfono en claro.

	gw := gatewaygrpc.New(
		// Deadline por Send hacia el Edge (Plan 027 · Ola 1 · T5, cierra H6): un Edge
		// lento no retiene al llamante ni atasca el kill-switch (env WAPP_GRPC_PUSH_TIMEOUT).
		session.NewRegistry(session.WithSendTimeout(cfg.GRPCPushTimeout)),
		log,
		// Deadline por ESPERA DEL ACK (env WAPP_GRPC_ACK_TIMEOUT): el hermano del
		// anterior. Aquel acota el empuje; este, la respuesta. Sin él, un Edge
		// saturado cuelga al llamante HTTP indefinidamente (2026-08-06).
		gatewaygrpc.WithAckTimeout(cfg.GRPCAckTimeout),
		// Carril de trabajo del stream (Plan 050 · Ola 1, ADR-0040): tope de cola POR
		// SESIÓN (env WAPP_GATEWAY_WORK_QUEUE, default 64 = el techo de entrantes
		// concurrentes) y presupuesto de pared por trabajo (env WAPP_GATEWAY_WORK_TIMEOUT,
		// default 5s). Aquí solo se materializan; quien los consume es el carril.
		gatewaygrpc.WithWorkQueue(cfg.GatewayWorkQueue),
		gatewaygrpc.WithWorkTimeout(cfg.GatewayWorkTimeout),
		gatewaygrpc.WithLease(leaseMgr),
		gatewaygrpc.WithFleet(fleetRepo),
		gatewaygrpc.WithCloudEncPrivKey(cloudEncPriv),
		gatewaygrpc.WithReceiptSink(receiptSink),
		// Push de config al conectar (ADR-0021). TRES eslabones —jwks, intents y
		// filters— armados por buildConfigProvider, que es donde vive el porqué de
		// cada regla (y, sobre todo, el único sitio desde el que un test puede
		// ejercer la cadena REAL: es el criterio (d) de T2.1).
		gatewaygrpc.WithConfigProvider(
			buildConfigProvider(jwksCfg, intentStore, entResolver, fleetRepo, log),
		),
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
	// La credencial de la vía LLM API (Plan 044 · T0.3) reusa EL MISMO cipher, y
	// por tanto el mismo keyring versionado del Plan 012, que buyerDataStore y el
	// puente CRM: tres sobres distintos, una sola rotación que gestionar. Es lo
	// que hace que meter tenant_llm en el censo de rekeyTargets (rekey.go) baste
	// para que su clave rote con todo lo demás.
	tenantLLMStore := tenantllm.NewPostgres(db, flowDeps.cipher)
	// El almacén del EVENTO conversacional (Plan 043 · Ola 1) reusa el MISMO cipher
	// que los contactos y los datos del comprador: el historial del evento guarda
	// texto literal del cliente y va cifrado con el keyring versionado del Plan 012.
	// Una tercera instancia de cipher sería una tercera rotación que gestionar.
	//
	// 🔧 SUBIÓ AQUÍ CON EL PLAN 044 · T1.4, y no por gusto: desde esa tarea el
	// eventStore es también EL LECTOR del hilo (ListThread), así que el compositor
	// del literal —que se construye unas líneas más abajo, antes del agregador— lo
	// necesita ya definido. Su consumidor de siempre (el despachador) sigue donde
	// estaba.
	eventStore := events.NewStore(db, flowDeps.cipher)
	// LA COLA DEL PIPELINE DE CAPTACIÓN (Plan 044 · Ola 1, migración 0072). Sin
	// cipher a propósito, y no es un olvido NI CADUCÓ CON T1.4: lo que llega a
	// PutSourceText son bytes YA cifrados por el compositor. Un store sin cipher no
	// puede escribir literal aunque alguien se lo pida, y eso es lo que sostiene
	// D-044.26 por construcción.
	intakeJobStore := intake.NewPostgres(db)
	// EL COMPOSITOR DEL LITERAL (T1.4). Corre AL FLUSH y nunca en línea con el
	// entrante: lee el hilo del evento por eventStore —descifrado en el borde,
	// REQ-10c—, separa el CONTEXTO (los `summary` y los salientes fuera de turno,
	// D-044.3b + D-044.24) del hilo literal, y guarda el sobre de tres piezas.
	// Reusa el MISMO cipher que el hilo: el texto sale de un sobre del keyring del
	// Plan 012 y entra en otro del mismo keyring, sin una tercera rotación.
	intakeComposer := flowruntime.NewSourceTextComposer(log, eventStore, intakeJobStore, flowDeps.cipher)
	// El AGREGADOR DE VENTANAS (T1.1/T1.2). Tres dependencias y ninguna más:
	//   - intakeJobStore, para escribir la ventana (UNA sentencia por entrante);
	//   - flowStore, para leer `aggregation_window_seconds` EN EL BARRIDO (nunca en
	//     línea con el mensaje: eso sería el SELECT que D-044.26 prohíbe);
	//   - entResolver, el MISMO resolver CACHEADO CON TTL que ya usan el hilo y el
	//     gate del puente CRM. 🔴 No se construye un segundo: dos resolvers serían
	//     dos cachés y dos verdades sobre qué tiene contratado un tenant.
	//
	// ⚠️ Va cableado con WithAggregator MÁS ABAJO y ADEMÁS arrancado con Run()
	// después de serveAndWait: sin el Run, las ventanas se abrirían y jamás se
	// cerrarían, y el fallo sería MUDO.
	//
	// 🔧 Y una CUARTA desde T1.4, que es una opción y no una dependencia del
	// constructor: WithSourceComposer. Sin ella el agregador corre con el noop
	// documentado —las ventanas se cerrarían con el sobre a NULL y el pipeline de la
	// Ola 2 recibiría jobs sin una línea de texto—, así que el cable importa tanto
	// como el código que enchufa.
	intakeAggregator := flowruntime.NewIntakeAggregator(log, intakeJobStore, flowStore, entResolver,
		flowruntime.WithSourceComposer(intakeComposer))
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
		// Ventana de captación (Plan 044 · Ola 1). NO es un EventSink y por eso no
		// entra por WithEventSink: se alimenta del ENTRANTE y no de los efectos de
		// módulo, porque EffectContext no lleva `wa_message_id` (y `source_refs` es
		// justo una lista de ellos) y porque un turno de texto libre —el caso que
		// este plan resuelve— no declara ningún efecto. El porqué entero está en la
		// cabecera de internal/flujos/runtime/aggregator.go.
		flowruntime.WithAggregator(intakeAggregator),
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
		// ⚰️ Aquí se cableaba el INTERRUPTOR DE DESPLIEGUE del productor `message` del
		// hilo del evento. Lo retiró el Plan 044 · T1.6 el 2026-08-22: era andamiaje
		// con fecha de caducidad —«hasta que el Plan 044 (su LECTOR) exista»— y el 044
		// es esa fecha. El gate NO se fue con él: sigue entero y sigue siendo por
		// tenant, y es la línea de arriba (WithEntitlements) la que lo sostiene — sin
		// `llm_intake`, cero filas. Ver internal/flujos/runtime/thread.go.
		// (Esta lápida vive DENTRO de una lista de argumentos, no sobre una declaración:
		// `revive`/`exported` no la mira. La línea en blanco es solo para que no se lea
		// como si documentara el WithReplyLimiter de debajo.)

		flowruntime.WithReplyLimiter(replyLimiter),
		flowruntime.WithIncomingTimeout(cfg.Flow.IncomingTimeout),
		flowruntime.WithMaxConcurrentIncoming(cfg.Flow.MaxConcurrentIncoming),
		// Guarda anti-self-loop (Plan 020 · T2), que desde el Plan 046 · T4.1 pregunta
		// por ÍNDICE CIEGO y no por el número en claro: por eso el checker necesita
		// ahora el KeyProvider.
		//
		// 🔴 TIENE QUE SER EL MISMO `flowDeps.kp` QUE USA LA PERSISTENCIA (línea ~203,
		// fleet.NewPostgresRepository), Y ESO NO ES UNA PREFERENCIA DE ESTILO. El bidx
		// es hex(HMAC(indexKey, tenant||0x00||número)): si el escritor y el lector
		// tuvieran DOS KeyProviders con `indexKey` distinta, NINGÚN bidx casaría jamás
		// —IsSelfNumber devolvería false para todos los números propios— y el
		// anti-self-loop DEJARÍA DE BLOQUEAR SIN DAR UN SOLO ERROR: no hay excepción,
		// no hay log, no hay métrica; solo dos sesiones del mismo tenant hablándose
		// para siempre. Un segundo crypto.NewKeyProvider aquí, aunque lea la misma
		// config, sería el mismo desastre si alguien cambia una de las dos fuentes.
		// Por eso se pasa el campo del struct y no se construye nada nuevo.
		flowruntime.WithSelfNumbers(flowruntime.NewPostgresSelfNumbers(db, flowDeps.kp)),
		flowruntime.WithIngestDeduper(ingest.NewPostgresDeduper(db)),
		// Contador de los entrantes que NO llegan al motor reactivo (passive /
		// self-loop / rate-limit): los tres cortes son silenciosos por diseño, así que
		// sin esto la única respuesta a «¿por qué no contesta?» era subir el log a
		// debug e inundarlo. Va por callback para que el motor no importe prometheus.
		flowruntime.WithReactiveBlockedHook(mtx.FlowReactiveBlocked),
		// Histograma de las rachas de auto-respuestas consecutivas por conversación
		// (Plan 049 · Opción A: OBSERVAR). Mismo desacoplo que la línea de arriba —
		// va por callback para que el motor no importe prometheus— y misma regla: el
		// hook OBSERVA, no decide. No hay umbral ni corte; cortar es la Opción B,
		// aplazada hasta tener 2-4 semanas de esta distribución con la que calibrarlo.
		flowruntime.WithAutoreplyStreakHook(mtx.FlowAutoreplyStreak),
		// Tercer toque del recordatorio perezoso de la seña (T4.4): el cliente vuelve
		// a escribir. No añade reloj ninguno — el disparador es el entrante.
		flowruntime.WithDepositReminder(depositReminder))
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
	mtx.SetFlowAutoreplyStreakMaxSource(flowRuntime.MaxAutoreplyStreak)

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

	platformRepo := platformadmin.NewRepository(db)
	// Hook del push de filtros EN CALIENTE (Plan 046 · T2.1): traduce «cambió el
	// perfil de esta sesión» a un ConfigUpdate kind:"filters" con la foto COMPLETA del
	// tenant, hacia sus sesiones vivas.
	//
	// 🔴 Se cablea en LOS DOS SITIOS que montan la ruta /sessions/{id}/profile —el
	// SessionDeps de la API pública, justo debajo, y el adminRouteDeps de más abajo—.
	// Encender solo uno deja la otra vía MUDA y no da ningún rojo: los tests de cada
	// vía pasan por separado. Si algún día se apaga, se apaga en los dos.
	filtersPusher := filtercfg.NewPusher(fleetRepo, gw)
	publicSrv, authMW, auditor, err := buildPublicAPIServer(cfg, log, mtx, authStk, publicapi.Deps{
		Sender: gw,
		FlowDeps: publicapi.FlowDeps{
			Flows:   flowStore,
			Modules: flowReg,
			Starter: flowRuntime,
		},
		SessionDeps: publicapi.SessionDeps{
			Sessions:      fleetRepo,
			SessionStatus: fleetRepo,
			// Plan 046 · T1.2: el mismo repo por el eje NUEVO (SetProfile).
			SessionProfiles: fleetRepo,
			// 🔴 SITIO 1 DE 2 del hook de filtros (Plan 046 · T2.1). El otro es el
			// `sessionProfile` de adminRouteDeps, más abajo: son DOS vías distintas
			// hacia el MISMO handler y encender solo una deja la otra muda sin dar
			// ningún rojo. Best-effort: un fallo del push NO cambia el 200 del POST.
			ProfilePush: filtersPusher,
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
		// Y la CONFIGURACIÓN de la vía LLM API (Plan 044 · T0.3): entra por el
		// puerto RECORTADO publicapi.TenantLLMStore, que NO tiene el método APIKey
		// — la capa HTTP no puede pedir la credencial ni por error.
		Integrations: integrationsStore,
		TenantLLM:    tenantLLMStore,
		CRMSecrets:   integrationsStore,
		CRMGate:      webhookGate,
		CRMReflect:   intakeStore,
		CRMNotify:    intakeNotifier,
		ConfigPush:   gw,
		Health:       publicapi.HealthRules{DegradedAfter: cfg.Health.DegradedAfter, StaleAfter: cfg.Health.StaleAfter},
		// El plazo de las consultas a BD de estos handlers (Plan 050 · Ola 3): un
		// solo valor de config para todos, porque lo que hay que respetar es la SUMA
		// con el reloj del Ack, no cada consulta por separado.
		DBTimeout: cfg.PublicAPIDBTimeout,
		// Telemetría de ciclo de vida del evento conversacional (Plan 043 ·
		// T6.5, cierra MD-043.17): SQL directo sobre el MISMO *sql.DB que ya
		// comparte toda la plataforma — no una segunda conexión ni un segundo
		// pool. Ver el comentario de propiedad en
		// internal/publicapi/eventstelemetry_store.go.
		EventTelemetry: publicapi.NewPostgresEventTelemetryStore(db),
	}, platformRepo)
	if err != nil {
		return err
	}

	// --- HTTP: health + admin interno. ---
	checker := httpapi.NewHealthChecker()
	checker.Register(postgres.NewHealthCheck(db))
	codeStore := enroll.NewPostgresCodeStore(db)
	mux := http.NewServeMux()
	// registerAdminRoutes es la ÚNICA función que cablea patrón→permiso→handler
	// contra el mux admin (Plan 056 · A-02): TestMuxRegistration_NoPanic llama a
	// esta MISMA función con deps de prueba, así que un conflicto de patrones que
	// hoy provoca panic en producción también lo provoca en el test — no una
	// copia a mano que solo se prueba a sí misma. Ver el docstring del tipo.
	registerAdminRoutes(mux, adminRouteDeps{
		authMW:  authMW,
		auditor: auditor,
		log:     log,

		health:  httpapi.HealthHandler(checker),
		metrics: mtx.PromHandler(),
		// Kill-switch COMERCIAL por tenant (Plan 055 · T3.3, D-055.2): mismo
		// patrón adminHandler (auth + permiso + auditoría). Aquí el objetivo es
		// un tenant AJENO (ADR-0039), así que el permiso lleva el sufijo '.any'.
		revokeLease: httpapi.RevokeLeaseHandler(gw),
		sendMessage: httpapi.SendMessageHandler(gw, log),
		cryptoRekey: httpapi.CryptoRekeyHandler(
			func(ctx context.Context, batch int) (crypto.Report, error) {
				return crypto.Rekey(ctx, db, flowDeps.cipher, flowDeps.kp, batch)
			},
		),
		flowsCreate:    flowadmin.DefinitionHandler(flowStore, flowReg),
		flowsStart:     flowadmin.StartHandler(flowRuntime),
		triggersCreate: flowadmin.CreateTriggerHandler(triggerStore, durableFlowChecker),
		triggersList:   flowadmin.ListTriggersHandler(triggerStore, durableFlowChecker),
		triggersDelete: flowadmin.DeleteTriggerHandler(triggerStore, durableFlowChecker),
		// 🔴 SITIO 2 DE 2 del hook de filtros (Plan 046 · T2.1). El tercer argumento
		// era nil desde T1.2 y aquí se enciende, EN PAREJA con el ProfilePush del
		// SessionDeps de buildPublicAPIServer: las dos vías llevan al MISMO handler y
		// encender solo una deja la otra muda sin dar ningún rojo (los tests de cada
		// vía pasan por separado). Si un día se apaga, se apagan las dos.
		sessionProfile: flowadmin.SetSessionProfileHandler(fleetRepo, filtersPusher, log),
		sessionStatus:  flowadmin.SetSessionStatusHandler(fleetRepo),
		revokeTenant:   httpapi.RevokeTenantHandler(gw, cfg.PlatformTenantID),
		restoreTenant:  httpapi.RestoreTenantHandler(gw, cfg.PlatformTenantID),

		// Rutas de plataforma (Plan 056 · T1.3, T3.4): platformRepo/codeStore/
		// m2mClient/platformTenantID viajan CRUDOS (no como http.Handler ya
		// armado) porque registerAdminRoutes construye los platformadmin.*Handler
		// INLINE — es justo el texto que INV-056.1 (platform_permissions_test.go)
		// busca para reconocer una ruta de plataforma. Ver el docstring del tipo.
		platformRepo:     platformRepo,
		platformTenantID: cfg.PlatformTenantID,
		codeStore:        codeStore,
		m2mClient:        authStk.m2mClient,
	})

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

	// BARRIDO DE VENTANAS DE CAPTACIÓN (Plan 044 · Ola 1 · T1.2/T1.7). Sin broker
	// (ADR-0003): un ticker de Go, sobre el MISMO ctx de signal.NotifyContext que
	// cierra todo lo demás — mismo trato que webhookWorker y flowLifecycleCollector.
	//
	// 🔴 ESTA LÍNEA ES LA MITAD QUE NO SE VE, Y SIN ELLA EL 044 NO FUNCIONA: el
	// IntakeAggregator cableado arriba solo ABRE ventanas; quien las CIERRA —y quien
	// recupera al arrancar las que vencieron mientras el proceso no estaba— es este
	// Run. Retirarla dejaría jobs en `aggregating` para siempre sin un solo error en
	// el log.
	//
	// Y por eso mismo la recuperación NO es un paso aparte: Run empieza barriendo
	// (RecoverAtBoot), porque el estado de una ventana vive en `intake_jobs` y no en
	// este proceso. Un despliegue en medio de una ráfaga no pierde el job.
	go intakeAggregator.Run(ctx)

	//nolint:contextcheck // shutdownAll parte de context.Background() a propósito: corre
	// cuando ctx ya está cancelado, y derivar de él abortaría el cierre gracioso al instante.
	return serveAndWait(ctx.Done(), log,
		httpServer{srv: httpSrv, name: "admin/health"},
		httpServer{srv: publicSrv, name: "API pública"},
		grpcServer{gs: enrollGS, lis: enrollLis, addr: cfg.GRPCEnrollAddr, name: "Enrollment (TLS de servidor)"},
		grpcServer{gs: connectGS, lis: connectLis, addr: cfg.GRPCConnectAddr, name: "CloudLink (mTLS)"},
	)
}

// adminRouteDeps agrupa lo que registerAdminRoutes necesita para cablear el mux
// admin/health (:8100): las tres piezas transversales de adminHandler
// (autenticación, auditoría, log) y un handler por ruta.
//
// Las rutas de plataforma (/admin/tenants*, /admin/access-requests*) son la
// excepción: en vez de un http.Handler ya armado, viajan las piezas CRUDAS
// (platformRepo, platformTenantID, codeStore, m2mClient) y registerAdminRoutes
// construye el platformadmin.*Handler(...) INLINE, dentro de la propia llamada a
// mux.Handle. Eso no es un capricho de estilo: TestINV056_1_PlatformPermissionsMustEndInDotAny
// (platform_permissions_test.go, A-01) detecta una ruta de plataforma leyendo el
// TEXTO fuente del argumento handler de cada adminHandler(...) en busca de
// "platformadmin." — si esas llamadas se pre-construyeran en un campo http.Handler
// como las demás, el detector se quedaría ciego ante una ruta de plataforma nueva
// registrada bajo un patrón que no empiece por /admin/tenants o /admin/access-requests
// (justo el escenario que A-01 documenta). Cualquier handler platformadmin.* nuevo
// debe seguir este mismo patrón: pieza cruda en el struct, constructor inline abajo.
type adminRouteDeps struct {
	authMW  *httpapi.Middleware
	auditor httpapi.AuditRecorder
	log     sharedlogger.Logger

	health         http.Handler
	metrics        http.Handler
	revokeLease    http.Handler
	sendMessage    http.Handler
	cryptoRekey    http.Handler
	flowsCreate    http.Handler
	flowsStart     http.Handler
	triggersCreate http.Handler
	triggersList   http.Handler
	triggersDelete http.Handler
	sessionProfile http.Handler
	sessionStatus  http.Handler
	revokeTenant   http.Handler
	restoreTenant  http.Handler

	platformRepo     *platformadmin.Repository
	platformTenantID string
	codeStore        platformadmin.CodeIssuer
	m2mClient        out.IdentityM2MClient
}

// registerAdminRoutes cablea TODAS las rutas del listener admin/health (:8100)
// contra mux: patrón HTTP → adminHandler(auth + permiso + auditoría) → handler.
// Es la ÚNICA función que hace este cableado (Plan 056 · A-02): Run() y el test
// de registro (mux_registration_test.go, TestMuxRegistration_NoPanic) llaman a
// esta MISMA función — un test que reconstruyera esta lista a mano solo probaría
// su propia copia, no lo que corre en producción (el conflicto de patrones que
// http.ServeMux resuelve con panic AL REGISTRAR, no en caliente).
func registerAdminRoutes(mux *http.ServeMux, d adminRouteDeps) {
	mux.Handle("/healthz", d.health)
	mux.Handle("/metrics", d.metrics)
	mux.Handle("/admin/leases/revoke", adminHandler(d.authMW, d.auditor, d.log,
		"leases.revoke", "lease", d.revokeLease))
	// Kill-switch COMERCIAL por tenant (Plan 055 · T3.3, D-055.2) y rutas de
	// plataforma (Plan 056 · T1.3, T3.4): mismo patrón adminHandler (auth + permiso + auditoría).
	// Aquí el objetivo es un tenant AJENO (ADR-0039), así que el permiso lleva el
	// sufijo '.any': la migración 0059/0060 se lo da SOLO a platform_admin y se lo
	// niega al '*' de tenant_admin con un deny '*.any'. Ese deny es la pieza que
	// convierte esto en un permiso de verdad — sin él, el '*' de cualquier
	// administrador de cliente ya cubriría estas rutas. El handler además exige,
	// por su cuenta, que el token sea del tenant de plataforma.
	mux.Handle("GET /admin/tenants", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.read.any", "tenant", platformadmin.ListTenantsHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/tenants", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.create.any", "tenant", platformadmin.CreateTenantHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("GET /admin/tenants/{id}", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.read.any", "tenant", platformadmin.GetTenantHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("GET /admin/tenants/{id}/installations", adminHandler(d.authMW, d.auditor, d.log,
		"fleet.read.any", "fleet", platformadmin.ListInstallationsHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/tenants/{id}/enrollment-codes", adminHandler(d.authMW, d.auditor, d.log,
		"enrollment.issue.any", "enrollment", platformadmin.IssueEnrollmentCodeHandler(d.platformRepo, d.codeStore, d.platformTenantID)))
	mux.Handle("GET /admin/access-requests", adminHandler(d.authMW, d.auditor, d.log,
		"users.provision.any", "user", platformadmin.ListAccessRequestsHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/access-requests/{id}/approve", adminHandler(d.authMW, d.auditor, d.log,
		"users.provision.any", "user", platformadmin.ApproveAccessRequestHandler(d.platformRepo, d.m2mClient, d.platformTenantID)))
	mux.Handle("POST /admin/access-requests/{id}/reject", adminHandler(d.authMW, d.auditor, d.log,
		"users.provision.any", "user", platformadmin.RejectAccessRequestHandler(d.platformRepo, d.platformTenantID)))
	mux.Handle("POST /admin/tenants/revoke", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.revoke.any", "tenant", d.revokeTenant))
	mux.Handle("POST /admin/tenants/restore", adminHandler(d.authMW, d.auditor, d.log,
		"tenants.restore.any", "tenant", d.restoreTenant))
	mux.Handle("/admin/messages/send", adminHandler(d.authMW, d.auditor, d.log,
		"messages.send", "message", d.sendMessage))
	mux.Handle("/admin/crypto/rekey", adminHandler(d.authMW, d.auditor, d.log,
		"crypto.rekey", "kek", d.cryptoRekey))
	mux.Handle("/admin/flows", adminHandler(d.authMW, d.auditor, d.log,
		"flows.create", "flow", d.flowsCreate))
	mux.Handle("/admin/flows/start", adminHandler(d.authMW, d.auditor, d.log,
		"flows.start", "flow", d.flowsStart))
	mux.Handle("POST /admin/triggers", adminHandler(d.authMW, d.auditor, d.log,
		"triggers.create", "trigger", d.triggersCreate))
	mux.Handle("GET /admin/triggers", adminHandler(d.authMW, d.auditor, d.log,
		"triggers.read", "trigger", d.triggersList))
	mux.Handle("DELETE /admin/triggers/{id}", adminHandler(d.authMW, d.auditor, d.log,
		"triggers.delete", "trigger", d.triggersDelete))
	mux.Handle("POST /admin/sessions/{id}/profile", adminHandler(d.authMW, d.auditor, d.log,
		"sessions.write", "session", d.sessionProfile))
	mux.Handle("POST /admin/sessions/{id}/status", adminHandler(d.authMW, d.auditor, d.log,
		"sessions.write", "session", d.sessionStatus))
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
