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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
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

	// --- Enrolamiento + par X25519 de cifrado de tránsito de la nube (Plan 011
	// §10.F): el enrolamiento publica la pública al Edge; la privada la usa el
	// gateway para abrir el enc_payload sellado al ingreso. ---
	enrollSrv, cloudEncPriv, err := buildEnrollServer(cfg, db, ca, log)
	if err != nil {
		return err
	}

	// --- Lease (kill-switch): clave de firma + persistencia en PostgreSQL. ---
	leaseMgr, err := buildLeaseManager(cfg, db, log)
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
	replyLimiter := ratelimit.NewLimiter(rate.Limit(cfg.Flow.ReplyRate), cfg.Flow.ReplyBurst)
	// El store de SOLICITUDES lo comparten dos consumidores: el proyector del
	// carrito, que le cuelga la revisión 1 al cerrar (ADR-0031 §3) y le pone la
	// línea de envío (D-041.11), y la API pública, que lee la bandeja. Es el mismo
	// pool y el mismo dominio: dos instancias solo serían dos nombres para lo mismo.
	// Por eso el proyector lo recibe DOS VECES: satisface sus dos puertos —escritor
	// de revisiones y garante del envío— sin que el carrito conozca el store entero.
	intakeStore := intakes.NewPostgres(db)
	flowRuntime := flowruntime.New(flowStore, flowEngine, gw, flowResolver, flowDeps.contacts, log,
		flowruntime.WithEventSink(flowruntime.NewPersistSink(flowStore,
			cart.NewProjector(flowStore, intakeStore, intakeStore),
			survey.NewProjector(flowStore))),
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(flowStore)),
		flowruntime.WithPresignClient(flowDeps.presign),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(triggerStore)),
		flowruntime.WithEntitlements(entResolver),
		flowruntime.WithReplyLimiter(replyLimiter),
		flowruntime.WithIncomingTimeout(cfg.Flow.IncomingTimeout),
		flowruntime.WithMaxConcurrentIncoming(cfg.Flow.MaxConcurrentIncoming),
		flowruntime.WithSelfNumbers(flowruntime.NewPostgresSelfNumbers(db)),
		flowruntime.WithIngestDeduper(ingest.NewPostgresDeduper(db)))

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
		Triggers:     triggerStore,
		Intents:      intentStore,
		Entitlements: entResolver,
		// El notificador (D-041.14 · T4.2) usa las MISMAS tres piezas que ya usa el
		// motor para hablarle a un contacto: el Gateway como sender, el resolver
		// custodiado de PII para el destino y el store de solicitudes para la config
		// del tenant. No hay un segundo camino de salida hacia WhatsApp.
		Intakes: intakes.NewService(intakeStore, intakes.WithNotifier(
			intakes.NewNotifier(gw, flowDeps.contacts, intakeStore, log),
		)),
		TenantVariables: tenantvars.NewPostgres(db),
		ConfigPush:      gw,
		Health:          publicapi.HealthRules{DegradedAfter: cfg.Health.DegradedAfter, StaleAfter: cfg.Health.StaleAfter},
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
	mux.Handle("/admin/messages/send", adminHandler(authMW, auditor, log,
		"messages.send", "message", httpapi.SendMessageHandler(gw)))
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
		"triggers.create", "trigger", flowadmin.CreateTriggerHandler(triggerStore)))
	mux.Handle("GET /admin/triggers", adminHandler(authMW, auditor, log,
		"triggers.read", "trigger", flowadmin.ListTriggersHandler(triggerStore)))
	mux.Handle("DELETE /admin/triggers/{id}", adminHandler(authMW, auditor, log,
		"triggers.delete", "trigger", flowadmin.DeleteTriggerHandler(triggerStore)))
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
