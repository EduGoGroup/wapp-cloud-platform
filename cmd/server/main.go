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
	"crypto/tls"
	"database/sql"
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
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := setupDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeDB(db, log)

	// --- PKI: CA firmante (enroll) + Pool (mTLS) + cert de servidor (ambos). ---
	ca, err := loadCA(cfg)
	if err != nil {
		return err
	}
	serverCert, err := tls.LoadX509KeyPair(cfg.PKI.ServerCertFile, cfg.PKI.ServerKeyFile)
	if err != nil {
		return fmt.Errorf("cargando cert de servidor (%s / %s): %w",
			cfg.PKI.ServerCertFile, cfg.PKI.ServerKeyFile, err)
	}

	// --- Enrolamiento: códigos + certs en PostgreSQL, firma con la CA. ---
	enrollSvc := enroll.NewService(
		enroll.NewPostgresCodeStore(db),
		ca,
		enroll.NewPostgresEdgeCertRepository(db),
	)
	enrollSrv := enroll.NewServer(enrollSvc, log)

	// --- Lease (kill-switch): clave de firma + persistencia en PostgreSQL. ---
	leaseMgr, err := buildLeaseManager(cfg, db, log)
	if err != nil {
		return err
	}

	// --- Fleet + Gateway CloudLink. ---
	gw := gatewaygrpc.New(
		session.NewRegistry(),
		log,
		gatewaygrpc.WithLease(leaseMgr),
		gatewaygrpc.WithFleet(fleet.NewPostgresRepository(db)),
	)

	// Observabilidad de la recepción 24/7 (T6 e2e con el Edge real). Los hooks se
	// fijan antes de servir: cada IncomingMessage se loguea a Info y cada
	// Heartbeat a Debug (la renovación del lease la hace el propio Server).
	gw.OnIncoming = func(sessionID string, m *cloudlinkv1.IncomingMessage) {
		log.Info("mensaje entrante",
			"session_id", sessionID,
			"from", m.GetFrom(),
			"text", m.GetText(),
			"wa_message_id", m.GetWaMessageId(),
		)
	}
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
	connectGS := grpc.NewServer(grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())))
	gw.Register(connectGS)
	connectLis, err := net.Listen("tcp", cfg.GRPCConnectAddr)
	if err != nil {
		return fmt.Errorf("escuchando cloudlink en %s: %w", cfg.GRPCConnectAddr, err)
	}

	// --- HTTP: health + admin interno de revocación. ---
	checker := httpapi.NewHealthChecker()
	checker.Register(postgres.NewHealthCheck(db))
	mux := http.NewServeMux()
	mux.Handle("/healthz", httpapi.HealthHandler(checker))
	mux.Handle("/admin/leases/revoke", httpapi.RevokeLeaseHandler(gw))
	mux.Handle("/admin/messages/send", httpapi.SendMessageHandler(gw))

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// --- Arranque de los tres servidores; el primer fallo aborta. ---
	errCh := make(chan error, 3)
	go func() {
		log.Info("servidor gRPC Enrollment iniciado (TLS de servidor)", "addr", cfg.GRPCEnrollAddr)
		if serveErr := enrollGS.Serve(enrollLis); serveErr != nil {
			errCh <- fmt.Errorf("enrollment gRPC: %w", serveErr)
		}
	}()
	go func() {
		log.Info("servidor gRPC CloudLink iniciado (mTLS)", "addr", cfg.GRPCConnectAddr)
		if serveErr := connectGS.Serve(connectLis); serveErr != nil {
			errCh <- fmt.Errorf("cloudlink gRPC: %w", serveErr)
		}
	}()
	go func() {
		log.Info("servidor HTTP iniciado", "addr", cfg.HTTPAddr)
		if serveErr := httpSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", serveErr)
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("señal de parada recibida, cerrando")
	case serveErr := <-errCh:
		log.Error("fallo de un servidor", "error", serveErr)
		shutdownAll(httpSrv, enrollGS, connectGS, log)
		return serveErr
	}

	shutdownAll(httpSrv, enrollGS, connectGS, log)
	log.Info("servidor detenido limpiamente")
	return nil
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

// shutdownAll detiene los tres servidores de forma ordenada.
func shutdownAll(httpSrv *http.Server, enrollGS, connectGS *grpc.Server, log sharedlogger.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP", "error", err)
	}
	enrollGS.GracefulStop()
	connectGS.GracefulStop()
}

// closeDB cierra el pool de conexiones registrando cualquier error.
func closeDB(db *sql.DB, log sharedlogger.Logger) {
	if err := db.Close(); err != nil {
		log.Error("error cerrando la base de datos", "error", err)
	}
}
