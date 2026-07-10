// Command server es el binario único de la Plataforma Cloud (monolito modular).
//
// Orquesta el arranque del Gateway CloudLink con DOS listeners gRPC y un HTTP:
//   - Enrollment (TLS de servidor SOLAMENTE): el Edge enrola aquí sin cert de
//     cliente (CSR -> código -> cert firmado por la CA).
//   - CloudLink (mTLS estricto): el Edge conecta aquí con el cert emitido; el
//     servidor exige y verifica el cert de cliente contra la MISMA CA.
//   - HTTP: health (/healthz, incluye el check de BD) y admin interno de
//     revocación de leases (/admin/leases/revoke, kill-switch).
//
// Carga la configuración, construye el logger, abre PostgreSQL, corre las
// migraciones al arrancar, loguea la clave pública del lease (para configurar el
// Edge) y hace graceful shutdown de los tres servidores.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"github.com/EduGoGroup/wapp-shared/auth"
	"github.com/EduGoGroup/wapp-shared/envelope"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/objectstore"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/receipts"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		stdlog.Fatalf("fallo fatal del arranque: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg)

	// Observabilidad Prometheus (Plan 018 · T10, R11): registry propio compartido
	// por los dos listeners HTTP (métricas de request/latencia/login/rate-limit) y
	// el sink de acuses. /metrics se sirve en el listener admin (:8100), más abajo.
	mtx := metrics.New()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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
	)

	// --- Motor de Flujos (Pieza 05): registro de módulos + engine + store +
	// runtime, sobre el *sql.DB ya abierto. Se enchufa a gw.OnIncoming (cada
	// entrante avanza la conversación viva; sin estado se ignora, decisión C) y
	// expone los endpoints admin /admin/flows y /admin/flows/start (más abajo). ---
	flowReg := modules.NewRegistry()
	flowReg.Register(menu.New())
	flowReg.Register(survey.New())
	flowReg.Register(cart.New())
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
	// Fan-out de efectos EN PROCESO (ADR-0003, sin broker): el PersistSink
	// materializa cada Effect en flow_events y proyecta survey_answer →
	// survey_results (Plan 015 · T3, releva al flush viejo del Plan 014) y el
	// carrito → orders/order_items (Plan 016 · T2).
	//
	// PUNTO DE INYECCIÓN del CRM/POS (Plan 016 · T4, design.md §9.I): el runtime
	// admite MÚLTIPLES sinks (WithEventSink se acumula; el dispatch hace fan-out a
	// todos). Un CRM real se enchufa AQUÍ añadiendo otro EventSink, SIN tocar el
	// módulo ni el flujo. Hoy queda DOCUMENTADO pero NO activo: en 016 todo va a la
	// BD (PersistSink). Para activarlo se añadiría —una vez que el WebhookSink haga
	// POST real + outbox durable/reintentos + tenant_integrations con credenciales
	// cifradas (todo DIFERIDO)— la opción:
	//
	//   flowruntime.WithEventSink(flowruntime.NewWebhookSink(log)),
	//
	// (registrar hoy el stub no-op no alteraría el comportamiento observable, pero
	// se deja fuera para no introducir ruido de logs en el camino feliz).
	// Reglas de disparo (Plan 019): el ConfigResolver lee 100% de BD (flow_triggers)
	// las palabras clave, el fallback y los escapes por tenant; sin filas se comporta
	// como el Noop (no arranca nada ⇒ no-regresión, INV-6).
	// triggerStore: reusado por los handlers /admin/triggers y /api/v1/triggers (Plan 019 T5)
	triggerStore := trigger.NewPostgresStore(db)
	// Red anti-loop (Plan 020 · T0): token-bucket EN MEMORIA por conversación que
	// acota las auto-respuestas del runtime. Defaults holgados (WAPP_FLOW_REPLY_RATE /
	// WAPP_FLOW_REPLY_BURST) matan un bucle sin frenar un flujo legítimo.
	replyLimiter := ratelimit.NewLimiter(rate.Limit(cfg.Flow.ReplyRate), cfg.Flow.ReplyBurst)
	flowRuntime := flowruntime.New(flowStore, flowEngine, gw, flowResolver, flowDeps.contacts, log,
		// Proyección POR-MÓDULO (Plan 027 · Ola 3 · T8, cierra H10): el PersistSink ya
		// no conoce cart/survey; cada módulo aporta su Projector (wired con el store) y
		// el sink los ejecuta genéricamente tras flow_events. Mismas filas orders/
		// order_items/survey_results.
		flowruntime.WithEventSink(flowruntime.NewPersistSink(flowStore,
			cart.NewProjector(flowStore),
			survey.NewProjector(flowStore))),
		// Reanudación POR-MÓDULO (Plan 027 · Ola 3 · T8, cierra H9): el carrito aporta su
		// ResumePolicy (TTL perezoso + auto-reinicio + siembra de page_size); el runtime
		// la consulta por tipo de nodo, sin literales cart en el engine.
		flowruntime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(flowStore)),
		flowruntime.WithPresignClient(flowDeps.presign),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(triggerStore)),
		flowruntime.WithReplyLimiter(replyLimiter),
		// Deadline del entrante reactivo (Plan 027 · Ola 0 · T1, cierra H1): acota cada
		// goroutine de OnIncoming para que un Edge mudo no fugue la goroutine ni retenga
		// el keyedMutex de la conversación (env WAPP_FLOW_INCOMING_TIMEOUT, default 30s).
		flowruntime.WithIncomingTimeout(cfg.Flow.IncomingTimeout),
		// Semáforo de concurrencia de entrantes (Plan 027 · Ola 1 · T5, cierra H5): un
		// techo evita que una inundación de historial arranque cientos de HandleIncoming
		// en paralelo (env WAPP_FLOW_MAX_CONCURRENT_INCOMING, default 64).
		flowruntime.WithMaxConcurrentIncoming(cfg.Flow.MaxConcurrentIncoming),
		// Guarda anti-self-loop (Plan 020 · T2): el conjunto de self_pn del tenant sale
		// de fleet_sessions (lo persiste el Gateway en cada Heartbeat). Un entrante de un
		// número propio de otra sesión del mismo tenant NO auto-responde.
		flowruntime.WithSelfNumbers(flowruntime.NewPostgresSelfNumbers(db)),
		// Dedupe PERSISTENTE de entrantes (Plan 028 · T6, ADR-0003): el outbox durable
		// del Edge da semántica at-least-once (reenvía tras reconexión); la tabla
		// ingest_dedupe (migración 0031) corta el MISMO mensaje llegado dos veces por la
		// clave (session_id, wa_message_id), incluso intercalado, ANTES de tocar el motor.
		flowruntime.WithIngestDeduper(ingest.NewPostgresDeduper(db)))

	// Observabilidad de la recepción 24/7 (T6 e2e con el Edge real). Los hooks se
	// fijan antes de servir: cada IncomingMessage lo procesa el Motor de Flujos y
	// cada Heartbeat se loguea a Debug (la renovación del lease la hace el Server).
	gw.OnIncoming = flowRuntime.OnIncoming
	gw.OnHeartbeat = func(sessionID string, m *cloudlinkv1.Heartbeat) {
		log.Debug("heartbeat",
			"session_id", sessionID,
			"lease_counter", m.GetLeaseCounter(),
		)
	}

	// --- Servidor Enrollment: TLS de servidor SOLAMENTE (sin cert de cliente). ---
	enrollGS := grpc.NewServer(grpc.Creds(enrollServerCreds(serverCert)))
	enrollSrv.Register(enrollGS)
	enrollLis, err := net.Listen("tcp", cfg.GRPCEnrollAddr)
	if err != nil {
		return fmt.Errorf("escuchando enrollment en %s: %w", cfg.GRPCEnrollAddr, err)
	}

	// --- Servidor CloudLink: mTLS estricto contra la MISMA CA. ---
	// Keepalive de transporte (Plan 026 · T3, design.md §4.a): el stream Connect es
	// bidi long-lived 24/7 detrás de NAT/firewalls domésticos. Sin keepalive, un
	// corte silencioso de NAT deja el stream medio-abierto hasta que un envío falla.
	// El servidor hace PING cada Time=30s (Timeout=10s para declarar muerto el
	// transporte) y la EnforcementPolicy admite los PING del cliente con
	// MinTime=15s + PermitWithoutStream (el Edge puede pinguear entre streams sin ser
	// expulsado con GOAWAY too_many_pings; el cliente edge usa Time=30s > 15s). Esto
	// NO sustituye el Ping app-level (server.go) ni el backoff del Edge: solo detecta
	// el corte de transporte antes; la reconexión la sigue gobernando el backoff.
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

	// --- IAM (Plan 018 · T3): API pública en el SEGUNDO listener HTTP :8103
	// (Decisión D, INV-7): MISMO binario, un solo proceso. Todo el cableado
	// (repos Postgres + usecases + middleware + rutas /api/v1/auth) vive en
	// buildPublicAPIServer para no engordar run(). Devuelve además el middleware
	// de auth y el auditor, que T4 REUSA para blindar /admin/* (mismo secreto JWT
	// ⇒ los tokens valen en ambos listeners). ---
	// fleetRepo: reusado por el aislamiento del envío (SessionLister) y por el
	// admin de rol de sesión (SessionRoleStore, Plan 020 · T1) en ambos listeners.
	fleetRepo := fleet.NewPostgresRepository(db)
	publicSrv, authMW, auditor, err := buildPublicAPIServer(cfg, db, log, mtx, publicapi.Deps{
		Sender:        gw,
		Sessions:      fleetRepo,
		Flows:         flowStore,
		Modules:       flowReg,
		Starter:       flowRuntime,
		Media:         flowDeps.presign, // presign R2 (upload-url, Plan 018 · T6)
		Content:       flowStore,        // CRUD tenant_content (Plan 018 · T6)
		Triggers:      triggerStore,     // CRUD reglas de disparo (Plan 019 · T5)
		SessionRoles:  fleetRepo,        // rol bot|passive de la sesión (Plan 020 · T1)
		SessionStatus: fleetRepo,        // estatus offline|loggedout (retiro de zombie, Plan 020 · T3)
		// Audit se cablea DENTRO de buildPublicAPIServer (el AuditService concreto
		// se construye allí; expone GET /api/v1/audit, Plan 018 · T10).
	})
	if err != nil {
		return err
	}

	// --- HTTP: health + admin interno. Plan 018 · T4: TODO /admin/* se monta
	// DETRÁS de Authenticate → RequirePermission(perm) → AuditMiddleware; el
	// tenant sale del token (INV-8), NUNCA del cuerpo. /healthz queda ABIERTO
	// (sonda de vida sin tenant). El kill-switch (INV-2) conserva su semántica:
	// solo gana autenticación. ---
	checker := httpapi.NewHealthChecker()
	checker.Register(postgres.NewHealthCheck(db))
	mux := http.NewServeMux()
	mux.Handle("/healthz", httpapi.HealthHandler(checker))
	// Métricas Prometheus (Plan 018 · T10, R11): banda de observabilidad del
	// ecosistema. Se sirve en el listener admin (interno, :8100), NO en el público
	// (:8103): las métricas no se exponen a terceros. Sin auth ni rate-limit (sonda
	// interna, como /healthz). Sus labels son CERO PII (patrón de ruta, no valores).
	mux.Handle("/metrics", mtx.PromHandler())
	mux.Handle("/admin/leases/revoke", adminHandler(authMW, auditor, log,
		"leases.revoke", "lease", httpapi.RevokeLeaseHandler(gw)))
	mux.Handle("/admin/messages/send", adminHandler(authMW, auditor, log,
		"messages.send", "message", httpapi.SendMessageHandler(gw)))
	// Rotación de KEK (Plan 012 §7): re-wrap incremental por batch, reanudable e
	// idempotente. El cierre cablea db+cipher+kp del scope a crypto.Rekey.
	mux.Handle("/admin/crypto/rekey", adminHandler(authMW, auditor, log,
		"crypto.rekey", "kek", httpapi.CryptoRekeyHandler(
			func(ctx context.Context, batch int) (crypto.Report, error) {
				return crypto.Rekey(ctx, db, flowDeps.cipher, flowDeps.kp, batch)
			},
		)))
	// flowReg aporta a la validación del alta los tipos de nodo de los módulos
	// enchufables (p. ej. "cart"), para que un flujo que los usa pase POST /admin/flows.
	mux.Handle("/admin/flows", adminHandler(authMW, auditor, log,
		"flows.create", "flow", flowadmin.DefinitionHandler(flowStore, flowReg)))
	mux.Handle("/admin/flows/start", adminHandler(authMW, auditor, log,
		"flows.start", "flow", flowadmin.StartHandler(flowRuntime)))
	// Reglas de disparo (Plan 019 · T5): CRUD keyword/fallback/escape. Mismos
	// handlers que /api/v1/triggers; el tenant sale del token (INV-8). Patrones
	// método+ruta (Go 1.22+) para separar POST/GET en /admin/triggers y extraer
	// {id} en el DELETE.
	mux.Handle("POST /admin/triggers", adminHandler(authMW, auditor, log,
		"triggers.create", "trigger", flowadmin.CreateTriggerHandler(triggerStore)))
	mux.Handle("GET /admin/triggers", adminHandler(authMW, auditor, log,
		"triggers.read", "trigger", flowadmin.ListTriggersHandler(triggerStore)))
	mux.Handle("DELETE /admin/triggers/{id}", adminHandler(authMW, auditor, log,
		"triggers.delete", "trigger", flowadmin.DeleteTriggerHandler(triggerStore)))
	// Rol de sesión bot|passive (Plan 020 · T1): una sesión passive escucha/transporta
	// pero NO dispara triggers ni auto-responde. Mismo handler que /api/v1/sessions;
	// el tenant sale del token (INV-8) y la mutación se acota a él.
	mux.Handle("POST /admin/sessions/{id}/role", adminHandler(authMW, auditor, log,
		"sessions.write", "session", flowadmin.SetSessionRoleHandler(fleetRepo)))
	// Estatus de sesión (Plan 020 · T3): retirar/limpiar un zombie (loggedout) o
	// dejar offline. Mismo handler que /api/v1/sessions/{id}/status; el tenant sale
	// del token (INV-8) y la mutación se acota a él. Reusa el scope sessions.write.
	mux.Handle("POST /admin/sessions/{id}/status", adminHandler(authMW, auditor, log,
		"sessions.write", "session", flowadmin.SetSessionStatusHandler(fleetRepo)))

	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		// InstrumentHTTP envuelve el mux ENTERO: cuenta request/latencia por patrón
		// de ruta (r.Pattern, CERO PII) del listener admin. No añade rate-limit
		// (red interna). /metrics y /healthz quedan cubiertos por la métrica también.
		Handler:           mtx.InstrumentHTTP("admin", mux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// --- Arranque de los cuatro servidores (2 gRPC + 2 HTTP) y espera de parada
	// o del primer fallo; shutdown ordenado en cualquiera de los dos caminos. ---
	return serveAndWait(ctx.Done(), log,
		httpServer{srv: httpSrv, name: "admin/health"},
		httpServer{srv: publicSrv, name: "API pública"},
		grpcServer{gs: enrollGS, lis: enrollLis, addr: cfg.GRPCEnrollAddr, name: "Enrollment (TLS de servidor)"},
		grpcServer{gs: connectGS, lis: connectLis, addr: cfg.GRPCConnectAddr, name: "CloudLink (mTLS)"},
	)
}

// httpServer y grpcServer agrupan cada listener con su nombre para el arranque
// concurrente y el shutdown ordenado (evitan que run() acumule las ramas de las
// cuatro goroutines).
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

// serveAndWait arranca los cuatro servidores en goroutines, espera a la señal de
// parada (done, típicamente ctx.Done()) o al primer error de arranque, y hace
// shutdown ordenado en ambos casos. Recibe done (no el context) a propósito: el
// shutdown deriva su PROPIO timeout de background porque el context de arranque
// ya está cancelado cuando toca cerrar. Devuelve el error del servidor que falló,
// o nil si fue parada limpia.
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

// serveGRPC sirve un servidor gRPC y reporta el error al canal (salvo cierre).
func serveGRPC(errCh chan<- error, log sharedlogger.Logger, s grpcServer) {
	log.Info("servidor gRPC iniciado", "name", s.name, "addr", s.addr)
	if err := s.gs.Serve(s.lis); err != nil {
		errCh <- fmt.Errorf("%s gRPC: %w", s.name, err)
	}
}

// serveHTTP sirve un servidor HTTP y reporta el error al canal (ErrServerClosed
// es cierre ordenado, no error).
func serveHTTP(errCh chan<- error, log sharedlogger.Logger, s httpServer) {
	log.Info("servidor HTTP iniciado", "name", s.name, "addr", s.srv.Addr)
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("http %s: %w", s.name, err)
	}
}

// enrollServerCreds construye credentials de TLS de servidor SOLAMENTE (sin
// exigir cert de cliente): el Edge enrola aquí antes de tener cert. NO se puede
// usar mtls.ServerCreds porque exige RequireAndVerifyClientCert.
func enrollServerCreds(serverCert tls.Certificate) credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
	})
}

// loadPKI carga la CA firmante y el cert de servidor desde las rutas de config,
// las dos piezas PKI que comparten los listeners de enroll y CloudLink.
func loadPKI(cfg config.AppConfig) (*enroll.CA, tls.Certificate, error) {
	ca, err := loadCA(cfg)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	serverCert, err := tls.LoadX509KeyPair(cfg.PKI.ServerCertFile, cfg.PKI.ServerKeyFile)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("cargando cert de servidor (%s / %s): %w",
			cfg.PKI.ServerCertFile, cfg.PKI.ServerKeyFile, err)
	}
	return ca, serverCert, nil
}

// loadCA carga la CA (cert + clave PEM) desde las rutas de config. La clave es
// necesaria para firmar CSRs en el enrolamiento; el cert alimenta el Pool del
// mTLS de CloudLink.
func loadCA(cfg config.AppConfig) (*enroll.CA, error) {
	certPEM, err := os.ReadFile(cfg.PKI.CACertFile)
	if err != nil {
		return nil, fmt.Errorf("leyendo cert de CA %q: %w", cfg.PKI.CACertFile, err)
	}
	keyPEM, err := os.ReadFile(cfg.PKI.CAKeyFile)
	if err != nil {
		return nil, fmt.Errorf("leyendo clave de CA %q: %w", cfg.PKI.CAKeyFile, err)
	}
	ca, err := enroll.LoadCAFromPEM(certPEM, keyPEM, 0)
	if err != nil {
		return nil, fmt.Errorf("cargando CA: %w", err)
	}
	return ca, nil
}

// buildLeaseManager resuelve la clave de firma del lease (archivo > base64 >
// generación de dev), construye el Manager con persistencia en PostgreSQL y
// loguea la clave pública en base64 para configurar el Validator del Edge (T6).
func buildLeaseManager(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger) (*lease.Manager, error) {
	priv, source, err := lease.ResolveSigningKey(cfg.Lease.PrivateKeyFile, cfg.Lease.PrivateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("resolviendo clave de lease: %w", err)
	}

	opts := []lease.Option{}
	if cfg.Lease.TTLMinutes > 0 {
		opts = append(opts, lease.WithTTL(time.Duration(cfg.Lease.TTLMinutes)*time.Minute))
	}

	mgr, err := lease.NewManager(priv, lease.NewPostgresRepository(db), opts...)
	if err != nil {
		return nil, fmt.Errorf("construyendo lease manager: %w", err)
	}

	log.Info("clave pública del lease (configurar en el Edge)",
		"key_source", string(source),
		"public_key_base64", mgr.PublicKeyBase64(),
	)
	if source == lease.KeySourceGenerated {
		log.Warn("clave de lease EFÍMERA de dev: cambia en cada arranque (no apta para producción)")
	}
	return mgr, nil
}

// buildPublicAPIServer cablea el módulo IAM (Plan 018 · T3) y devuelve el
// segundo servidor HTTP (API pública, :8103). Construye los repos Postgres sobre
// el *sql.DB ya abierto, los usecases (que consumen wapp-shared/auth:
// JWT/bcrypt/glob-RBAC), el middleware reutilizable (Authenticate/
// RequirePermission, listo para que T4 envuelva /admin/* y T5 monte negocio) y
// monta /api/v1/auth/* (T3) y las rutas de operación pública /api/v1 (T5:
// mensajes + flujos CRUD/arranque) que reciben en `pub` las dependencias de
// negocio (gateway, store, motor). gRPC (:8101/:8102) y el admin (:8100) quedan
// intactos: este servidor es aparte.
func buildPublicAPIServer(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger, mtx *metrics.Metrics, pub publicapi.Deps) (*http.Server, *httpapi.Middleware, httpapi.AuditRecorder, error) {
	jwtBundle, svcJWTMgr, err := buildJWTManagers(cfg, log)
	if err != nil {
		return nil, nil, nil, err
	}
	// EMISOR DEL PLANO DE USUARIO (Plan 028 · T3/T4, ADR-0019): ES256 con `kid`
	// (jwtBundle.es256). El emisor HS256 legacy quedó RETIRADO del plano de usuario
	// (T4): WAPP_JWT_SECRET solo sobrevive para el ServiceJWTManager M2M (más abajo).
	userTokenIssuer := jwtBundle.es256
	// Validación del :8103 (Plan 028 · T4, ADR-0019): un MultiVerifier con la ÚNICA
	// entrada ES256 por su `kid` (pública derivada) y SIN default, de modo que un
	// token HS256 de usuario (con o sin `kid`) se RECHAZA. *auth.MultiVerifier
	// satisface la interface UserTokenValidator del middleware (authmw.go no cambia)
	// y también el TokenValidator del AuthService: el mismo objeto valida el :8103 y
	// el path Verify del IAM (una sola política de aceptación). El guard anti
	// alg-confusion es transitivo (un HS256 con el kid de ES256 se rechaza).
	userValidator, err := auth.NewMultiVerifier(
		cfg.JWT.Issuer,
		map[string]auth.VerifierKey{jwtBundle.kid: auth.ES256VerifierKey(jwtBundle.esPub)},
		auth.VerifierKey{},
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("construyendo MultiVerifier de usuario (ES256): %w", err)
	}
	auditor, err := iamusecase.NewAuditService(iampostgres.NewAuditRepo(db))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("construyendo AuditService (IAM): %w", err)
	}
	// El mismo AuditService sirve la consulta GET /api/v1/audit (Plan 018 · T10):
	// lee la bitácora del tenant del token (audit.read). CERO PII (eventos opacos).
	pub.Audit = auditor
	authSvc, err := iamusecase.NewAuthService(
		iampostgres.NewUserRepo(db),
		iampostgres.NewRoleRepo(db),
		iampostgres.NewGrantRepo(db),
		iampostgres.NewRefreshRepo(db),
		iampostgres.NewAuditRepo(db),
		userTokenIssuer,
		userValidator,
		iamusecase.Config{},
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("construyendo AuthService (IAM): %w", err)
	}
	m2mSvc, err := iamusecase.NewM2MService(iampostgres.NewAPIKeyRepo(db), svcJWTMgr, iamusecase.Config{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("construyendo M2MService (IAM): %w", err)
	}
	authMW := httpapi.NewMiddleware(userValidator, m2mSvc, log)

	publicMux := http.NewServeMux()
	iamhttp.Register(publicMux, authSvc, m2mSvc, log)
	// Ruta protegida de referencia: ejercita el middleware de extremo a extremo y
	// documenta el contrato de identidad para T4/T5 (tenant/subject del token).
	publicMux.Handle("/api/v1/auth/whoami", authMW.Authenticate(httpapi.WhoAmIHandler()))

	// Operación pública (Plan 018 · T5): mensajes + flujos CRUD/arranque, cada ruta
	// autenticada por api-key/scope (mismo authMW) y las escrituras auditadas (mismo
	// auditor). El tenant SIEMPRE sale del token (INV-8). T10 añade GET /api/v1/audit.
	publicapi.Register(publicMux, pub, authMW, auditor, log)

	// Blindaje transversal de la API pública (Plan 018 · T10, R11): rate-limit por
	// credencial (api-key/tenant) y por IP en el login (anti fuerza bruta) +
	// métricas de request/latencia. Envuelven el mux ENTERO. Orden de ejecución:
	// métricas (siempre cuenta, incluso un 429) → rate-limit → mux. NO tocan
	// /healthz/metrics (viven en el listener admin).
	publicLim := httpapi.NewLimiter(rate.Limit(cfg.RateLimit.PublicRPS), cfg.RateLimit.PublicBurst)
	loginLim := httpapi.NewLimiter(rate.Limit(float64(cfg.RateLimit.LoginPerMin)/60.0), cfg.RateLimit.LoginBurst)
	var handler http.Handler = publicMux
	handler = httpapi.PublicRateLimit(handler, publicLim, loginLim, mtx, log)
	handler = mtx.InstrumentHTTP("public", handler)

	srv := &http.Server{
		Addr:              cfg.PublicHTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	return srv, authMW, auditor, nil
}

// adminHandler blinda un endpoint /admin/* con la cadena de la fase IAM (Plan
// 018 · T4): Authenticate (identidad del token) → RequirePermission(perm) →
// AuditMiddleware(action=perm, resource) → handler. El tenant SIEMPRE sale del
// token (INV-8, lo lee el handler con IdentityFromContext) y la operación queda
// auditada sin PII (actor/resource opacos). El nombre del permiso se reutiliza
// como `action` de la bitácora (p. ej. "flows.create").
func adminHandler(mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger, perm, resource string, h http.Handler) http.Handler {
	h = httpapi.AuditMiddleware(auditor, perm, resource, log)(h)
	h = mw.RequirePermission(perm)(h)
	return mw.Authenticate(h)
}

// defaultES256Kid es el `kid` por defecto cuando WAPP_JWT_KID está vacío (solo
// dev; en producción se define un kid con la convención es256-YYYYMMDD).
const defaultES256Kid = "es256-dev"

// userJWTBundle agrupa el material de tokens de USUARIO del IAM (ADR-0019, Plan
// 028). Tras el retiro de HS256 del plano de usuario (T4), ES256 es el único
// emisor: reúne el emisor ES256 (con `kid`) y el material derivado que necesita
// el MultiVerifier del middleware (la pública ES256 y el `kid` para su entrada).
// El secreto HS256 (WAPP_JWT_SECRET) ya NO forma parte del plano de usuario;
// sobrevive solo para el ServiceJWTManager M2M (ver buildJWTManagers).
type userJWTBundle struct {
	es256 *auth.JWTManager // emisor ES256 con `kid` estampado (único emisor de usuario).
	esPub *ecdsa.PublicKey // pública ES256 derivada (entrada `kid` del MultiVerifier).
	kid   string           // key id activo ES256.
}

// buildJWTManagers construye el material de tokens de usuario (emisor ES256) y el
// ServiceJWTManager M2M del IAM (Plan 018 §6, ADR-0019) a partir de la config.
// Zero-knowledge: los secretos/claves salen de env, NUNCA se hardcodean ni se
// loguean. La clave EC (WAPP_JWT_EC_PRIVATE_KEY_FILE) firma los tokens de usuario
// y el secreto HS256 (WAPP_JWT_SECRET) firma el service token M2M; ambos son
// obligatorios en prod (fail-fast) y efímeros con warning en dev. El service
// token exige `aud` propia (aísla los planos usuario/M2M). Tras T4 el secreto
// HS256 NO firma ni valida tokens de usuario: es exclusivo del plano M2M.
func buildJWTManagers(cfg config.AppConfig, log sharedlogger.Logger) (*userJWTBundle, *auth.ServiceJWTManager, error) {
	secret := cfg.JWT.Secret
	if secret == "" {
		if cfg.Env == "prod" {
			return nil, nil, errors.New("WAPP_JWT_SECRET es obligatorio en prod (zero-knowledge: sin default)")
		}
		gen, err := randomSecret()
		if err != nil {
			return nil, nil, fmt.Errorf("generando secreto JWT de dev: %w", err)
		}
		secret = gen
		log.Warn("secreto JWT EFÍMERO de dev: cambia en cada arranque; los tokens no sobreviven a un reinicio (no apto para producción)")
	}

	// Par ES256 (F1, ADR-0019): emisor asimétrico que convive con HS256. En T1 se
	// construye pero NO corta la emisión todavía (ver punto de conmutación).
	priv, err := buildES256Key(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	kid := cfg.JWT.Kid
	if kid == "" {
		// Con ES256 como único emisor de usuario (T4), el `kid` es obligatorio en
		// prod: es lo que ata el token a su entrada de verificación en el rotado.
		if cfg.Env == "prod" {
			return nil, nil, errors.New("WAPP_JWT_KID es obligatorio en prod (ADR-0019: ES256 es el único emisor de usuario)")
		}
		kid = defaultES256Kid
		log.Warn("WAPP_JWT_KID vacío: usando kid por defecto \"" + defaultES256Kid + "\" (define uno con convención es256-YYYYMMDD)")
	}
	es256Mgr, err := auth.NewJWTManagerES256(priv, cfg.JWT.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("construyendo emisor ES256: %w", err)
	}
	es256Mgr = es256Mgr.WithKid(kid)

	bundle := &userJWTBundle{
		es256: es256Mgr,
		esPub: &priv.PublicKey,
		kid:   kid,
	}
	// El secreto HS256 ya no firma tokens de usuario (T4): solo el service token M2M.
	svcMgr := auth.NewServiceJWTManager(secret, cfg.JWT.Issuer, cfg.JWT.ServiceAudience)
	return bundle, svcMgr, nil
}

// randomSecret genera 32 bytes aleatorios en base64 (secreto HS256 efímero de
// dev). No apto para producción: no persiste entre arranques.
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// buildES256Key resuelve la clave privada EC P-256 que firma los tokens de
// usuario en ES256 (ADR-0019, Plan 028). Reglas por entorno (espejo del secreto
// HS256): con WAPP_JWT_EC_PRIVATE_KEY_FILE lee el PEM, en prod exige permisos
// <=0600, parsea PKCS#8 o SEC1 y valida curva P-256; en prod sin archivo (o
// inválido/permisos laxos) hace fail-fast; en dev sin archivo genera un par
// EFÍMERO en memoria con warning (permite `go run` sin fricción).
func buildES256Key(cfg config.AppConfig, log sharedlogger.Logger) (*ecdsa.PrivateKey, error) {
	path := cfg.JWT.ECPrivateKeyFile
	if path == "" {
		if cfg.Env == "prod" {
			return nil, errors.New("WAPP_JWT_EC_PRIVATE_KEY_FILE es obligatorio en prod (ADR-0019: emisión ES256 sin default)")
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generando par ES256 efímero de dev: %w", err)
		}
		log.Warn("clave ES256 EFÍMERA de dev: cambia en cada arranque; los tokens no sobreviven a un reinicio (no apto para producción)")
		return key, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("leyendo la clave ES256 %q: %w", path, err)
	}
	// En prod exige permisos estrictos (<=0600): cualquier bit de grupo/otros
	// delata una clave privada expuesta.
	if cfg.Env == "prod" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("permisos laxos en la clave ES256 %q: %#o (exige <=0600 en prod)", path, info.Mode().Perm())
	}
	pemBytes, err := os.ReadFile(path) // #nosec G304 -- ruta provista por la config de confianza del operador
	if err != nil {
		return nil, fmt.Errorf("leyendo la clave ES256 %q: %w", path, err)
	}
	key, err := parseECP256PrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("clave ES256 %q: %w", path, err)
	}
	return key, nil
}

// parseECP256PrivateKeyPEM decodifica un PEM con una clave privada EC en formato
// PKCS#8 o SEC1 y exige la curva P-256 (la de ES256). Función pura (sin E/S) para
// poder testear el parseo y la validación de curva de forma aislada.
func parseECP256PrivateKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no contiene un bloque PEM válido")
	}
	// PKCS#8 primero (formato del openssl pkcs8 -topk8 documentado); si no, SEC1.
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("la clave PKCS#8 no es ECDSA (es %T)", parsed)
		}
		return validateP256(ec)
	}
	ec, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("no es una clave EC PKCS#8 ni SEC1: %w", err)
	}
	return validateP256(ec)
}

// validateP256 comprueba que la clave EC use la curva P-256 (obligatoria para
// ES256, ADR-0019); cualquier otra curva se rechaza.
func validateP256(ec *ecdsa.PrivateKey) (*ecdsa.PrivateKey, error) {
	if ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("curva %q no soportada: ES256 exige P-256", ec.Curve.Params().Name)
	}
	return ec, nil
}

// buildEnrollServer construye el servidor de enrolamiento y resuelve el par
// X25519 de cifrado de tránsito de la nube (Plan 011 §10.F): publica la pública
// al Edge en el enrolamiento y devuelve la privada para que el gateway abra el
// enc_payload al ingreso.
func buildEnrollServer(cfg config.AppConfig, db *sql.DB, ca *enroll.CA, log sharedlogger.Logger) (*enroll.Server, []byte, error) {
	cloudEncPub, cloudEncPriv, err := buildCloudEncKeypair(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	enrollSvc := enroll.NewService(
		enroll.NewPostgresCodeStore(db),
		ca,
		enroll.NewPostgresEdgeCertRepository(db),
	)
	return enroll.NewServer(enrollSvc, log, enroll.WithCloudEncPubkey(cloudEncPub)), cloudEncPriv, nil
}

// buildCloudEncKeypair resuelve el par X25519 de cifrado de tránsito de la nube
// (Plan 011 §10.F). Si WAPP_CLOUD_ENC_PRIVKEY_B64 está, decodifica la privada
// (32B) y deriva la pública multiplicando por el punto base de la curva; si falta,
// genera un par efímero de dev (con warning, como la clave del lease). Loguea la
// pública en base64 para diagnóstico y para configurar el Edge fuera de banda.
func buildCloudEncKeypair(cfg config.AppConfig, log sharedlogger.Logger) (pub, priv []byte, err error) {
	if b64 := cfg.Crypto.CloudEncPrivKeyB64; b64 != "" {
		priv, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, nil, fmt.Errorf("clave de cifrado de la nube: base64 inválido: %w", err)
		}
		if len(priv) != envelope.PrivateKeySize {
			return nil, nil, fmt.Errorf("clave de cifrado de la nube: debe medir %d bytes (X25519), mide %d",
				envelope.PrivateKeySize, len(priv))
		}
		pub, err = curve25519.X25519(priv, curve25519.Basepoint)
		if err != nil {
			return nil, nil, fmt.Errorf("derivando pública de cifrado de la nube: %w", err)
		}
		log.Info("clave pública de cifrado de la nube (publicada al Edge en el enrolamiento)",
			"key_source", "config",
			"public_key_base64", base64.StdEncoding.EncodeToString(pub),
		)
		return pub, priv, nil
	}

	pub, priv, err = envelope.GenerateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generando par de cifrado de la nube: %w", err)
	}
	log.Info("clave pública de cifrado de la nube (publicada al Edge en el enrolamiento)",
		"key_source", "generated",
		"public_key_base64", base64.StdEncoding.EncodeToString(pub),
	)
	log.Warn("clave de cifrado de la nube EFÍMERA de dev: cambia en cada arranque (no apta para producción)")
	return pub, priv, nil
}

// flowRuntimeDeps agrupa las dependencias del Motor de Flujos que se construyen
// con fail-fast a partir de secretos de config: el stack de cifrado de PII (Plan
// 011) y el almacén de objetos R2 (Plan 017). Se devuelven juntas para que el
// arranque tenga UNA sola rama de error (cualquier fallo aborta el proceso).
type flowRuntimeDeps struct {
	// contacts resuelve la identidad OPACA del contacto (cifra/descifra PII).
	contacts contact.Resolver
	// cipher y kp son el stack de cifrado de PII (Plan 011); el runtime los usa vía
	// el resolver, y el endpoint admin /admin/crypto/rekey los necesita en crudo
	// para la rotación de KEK (Plan 012).
	cipher *crypto.FieldCipher
	kp     crypto.KeyProvider
	// presign firma la key de un adjunto al despachar un nodo media (Plan 017).
	presign objectstore.PresignClient
}

// buildFlowRuntimeDeps construye, con fail-fast, las dependencias anteriores: el
// KeyProvider de PII (ADR-0017: la KEK maestra vive en env/secret store, separada
// del dato) y el PresignClient de Cloudflare R2 (§3/§8: valida el bucket con
// HeadBucket; sin bucket/credenciales el proceso no levanta). Mismo R2 en dev y
// prod (sin MinIO local); credenciales por WAPP_STORAGE_S3_* (.env, no versionado).
func buildFlowRuntimeDeps(ctx context.Context, cfg config.AppConfig, db *sql.DB) (flowRuntimeDeps, error) {
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: cfg.Crypto.KEKKeyring,
		CurrentID:  cfg.Crypto.KEKCurrent,
		MasterB64:  cfg.Crypto.KEKMasterB64,
		IndexB64:   cfg.Crypto.KEKIndexB64,
		Prod:       cfg.Env == "prod",
	})
	if err != nil {
		return flowRuntimeDeps{}, fmt.Errorf("construyendo KeyProvider de PII (Plan 011): %w", err)
	}
	cipher := crypto.NewFieldCipher(kp)

	presignClient, err := objectstore.NewR2PresignClient(ctx, objectstore.R2Config{
		Region:          cfg.Storage.Region,
		Bucket:          cfg.Storage.Bucket,
		AccessKeyID:     cfg.Storage.AccessKeyID,
		SecretAccessKey: cfg.Storage.SecretAccessKey,
		Endpoint:        cfg.Storage.Endpoint,
		PresignExpiry:   cfg.Storage.PresignExpiry,
	})
	if err != nil {
		return flowRuntimeDeps{}, fmt.Errorf("construyendo PresignClient R2 (Plan 017): %w", err)
	}
	return flowRuntimeDeps{
		contacts: contact.NewPostgresResolver(db, cipher, kp),
		cipher:   cipher,
		kp:       kp,
		presign:  presignClient,
	}, nil
}

// setupDatabase abre la conexión a PostgreSQL y corre las migraciones de
// esquema al arrancar. Si la BD no está disponible, devuelve un error claro
// (fail-fast) en lugar de arrancar a medias.
func setupDatabase(ctx context.Context, cfg config.AppConfig, log sharedlogger.Logger) (*sql.DB, error) {
	db, err := postgres.Open(ctx, postgres.Config{DSN: cfg.DB.DSN()})
	if err != nil {
		return nil, fmt.Errorf("base de datos no disponible al arrancar: %w", err)
	}

	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("aplicando migraciones: %w", err), db.Close())
	}
	log.Info("migraciones aplicadas",
		"version", res.Version,
		"content_hash", res.ContentHash,
		"skipped", res.Skipped,
	)

	return db, nil
}

// shutdownAll detiene los cuatro servidores de forma ordenada (los dos HTTP con
// timeout de drenado; los dos gRPC con GracefulStop acotado por shutdownTimeout).
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

// gracefulStopGRPC drena un servidor gRPC con GracefulStop pero ACOTADO por
// shutdownTimeout (Plan 027 · Ola 0 · T2, cierra H3): CloudLink mantiene streams
// Connect 24/7 vivos, y GracefulStop bloquea hasta que TODOS terminen ⇒ cada
// SIGTERM colgaría el deploy hasta el SIGKILL del orquestador. Se lanza el drenado
// en una goroutine y, si no completa a tiempo, se fuerza con Stop() (cierra los
// streams en curso). Así SIGTERM SIEMPRE termina en < shutdownTimeout.
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
		<-done // GracefulStop retorna en cuanto Stop() cierra los streams.
	}
}

// closeDB cierra el pool de conexiones registrando cualquier error.
func closeDB(db *sql.DB, log sharedlogger.Logger) {
	if err := db.Close(); err != nil {
		log.Error("error cerrando la base de datos", "error", err)
	}
}
