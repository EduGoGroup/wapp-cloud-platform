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
	"encoding/base64"
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
	"github.com/EduGoGroup/wapp-shared/envelope"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
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

	// --- Fleet + Gateway CloudLink. ---
	gw := gatewaygrpc.New(
		session.NewRegistry(),
		log,
		gatewaygrpc.WithLease(leaseMgr),
		gatewaygrpc.WithFleet(fleet.NewPostgresRepository(db)),
		gatewaygrpc.WithCloudEncPrivKey(cloudEncPriv),
	)

	// --- Motor de Flujos (Pieza 05): registro de módulos + engine + store +
	// runtime, sobre el *sql.DB ya abierto. Se enchufa a gw.OnIncoming (cada
	// entrante avanza la conversación viva; sin estado se ignora, decisión C) y
	// expone los endpoints admin /admin/flows y /admin/flows/start (más abajo). ---
	flowReg := modules.NewRegistry()
	flowReg.Register(menu.New())
	flowReg.Register(survey.New())
	flowReg.Register(cart.New())
	flowStore := flowstore.NewPostgresRepository(db)
	// Fuente de contenido enrutada POR-NODO (Plan 015 T4a): el Router compone el
	// adapter Static (PURO, default de menú/encuesta) con el adapter JSON
	// (tenant_content). El engine ve UN puerto content.Source; el switch por
	// fuente vive SOLO en el Router (el dominio no conoce orígenes). Menú/encuesta
	// sin `content` siguen resolviéndose byte-a-byte por la rama static.
	flowEngine := engine.New(flowReg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(flowStore))))
	flowResolver := flowruntime.NewPostgresTenantResolver(db)
	// KeyProvider + FieldCipher del cifrado de PII en reposo (Plan 011, ADR-0017):
	// la KEK maestra vive en env/secret store (§10.A), separada del dato. Fail-fast
	// si falta, igual que la clave del lease.
	contactKP, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: cfg.Crypto.KEKKeyring,
		CurrentID:  cfg.Crypto.KEKCurrent,
		MasterB64:  cfg.Crypto.KEKMasterB64,
		IndexB64:   cfg.Crypto.KEKIndexB64,
		Prod:       cfg.Env == "prod",
	})
	if err != nil {
		return fmt.Errorf("construyendo KeyProvider de PII (Plan 011): %w", err)
	}
	contactCipher := crypto.NewFieldCipher(contactKP)
	contactResolver := contact.NewPostgresResolver(db, contactCipher, contactKP)
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
	flowRuntime := flowruntime.New(flowStore, flowEngine, gw, flowResolver, contactResolver, log,
		flowruntime.WithEventSink(flowruntime.NewPersistSink(flowStore)))

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
	// Rotación de KEK (Plan 012 §7): re-wrap incremental por batch, reanudable e
	// idempotente. El cierre cablea db+cipher+kp del scope a crypto.Rekey.
	mux.Handle("/admin/crypto/rekey", httpapi.CryptoRekeyHandler(
		func(ctx context.Context, batch int) (crypto.Report, error) {
			return crypto.Rekey(ctx, db, contactCipher, contactKP, batch)
		},
	))
	// flowReg aporta a la validación del alta los tipos de nodo de los módulos
	// enchufables (p. ej. "cart"), para que un flujo que los usa pase POST /admin/flows.
	flowadmin.Register(mux, flowStore, flowRuntime, flowReg)

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
