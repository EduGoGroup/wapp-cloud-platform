// Package gatewaygrpc implementa el servidor del servicio CloudLink sobre los
// tipos generados públicos de wapp-cloudlink (gen/wapp/cloudlink/v1).
//
// El nombre del paquete es gatewaygrpc (no "grpc") deliberadamente: evita la
// colisión con el paquete google.golang.org/grpc, que se importa con su nombre
// natural.
//
// Sobre el núcleo en memoria del Connect (Plan 005 · T2: registro de sesiones,
// ruteo de EdgeToCloud, correlación de Acks, empuje de SendText/Ping) T4 cablea
// además la identidad mTLS, el lease (kill-switch, ADR-0007) y el fleet:
//   - La identidad (tenantID, edgeID) se extrae del cert de cliente mTLS del
//     peer. Si NO hay TLS (tests bufconn de T2), Connect degrada: no emite lease
//     ni toca fleet, conservando el comportamiento de T2 intacto.
//   - Al registrar una sesión: fleet online + lease inicial empujado al Edge.
//   - En cada Heartbeat: renovación del lease (counter = heartbeatCounter+1).
//   - Al caer el stream: fleet offline.
//   - RevokeLease dispara el kill-switch: persiste revocado y empuja el
//     LeaseUpdate(Revoked) a TODAS las sesiones vivas del Edge.
//
// Las dependencias de lease y fleet son OPCIONALES (WithLease/WithFleet): nil =
// comportamiento T2 (sin lease/fleet), lo que mantiene los tests sin TLS verdes.
package gatewaygrpc

import (
	"sync"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// offlinePersistTimeout acota la persistencia de fleet-offline tras la caída del
// stream (el contexto del stream ya está cancelado; se usa uno desacoplado).
const offlinePersistTimeout = 5 * time.Second

// defaultAckTimeout acota la espera del Ack del Edge cuando no se configura otro
// con WithAckTimeout. Ver el porqué del valor —y la invariante contra el
// WriteTimeout del servidor HTTP— en config.AppConfig.GRPCAckTimeout.
const defaultAckTimeout = 8 * time.Second

// Server implementa cloudlinkv1.CloudLinkServer. Es seguro para uso concurrente.
//
// Los hooks observables (OnIncoming, OnHeartbeat) deben asignarse antes de poner
// el servidor a servir (no se mutan mientras se atienden streams).
type Server struct {
	cloudlinkv1.UnimplementedCloudLinkServer

	registry *session.Registry
	log      logger.Logger

	// leaseMgr y fleet son OPCIONALES. nil => degradación a comportamiento T2
	// (sin lease ni fleet). Se inyectan con WithLease/WithFleet.
	leaseMgr *lease.Manager
	fleet    fleet.Repository

	// cloudEncPriv es la privada X25519 (32B) del par de cifrado de tránsito de la
	// nube (Plan 011 §10.F). Con ella se abre (OpenWith) el enc_payload sellado por
	// el Edge al ingreso y se repueblan los campos sensibles en memoria. Vacía =
	// no se intenta abrir (los IncomingMessage llegan siempre en claro, compat).
	cloudEncPriv []byte

	// configProvider entrega las configs vigentes a empujar al Edge al conectar
	// (ADR-0021), ya gateadas por entitlements. nil = sin push de config al conectar
	// (comportamiento previo). Se inyecta con WithConfigProvider.
	configProvider ConfigProvider

	// receiptSink es el enganche por el que se entrega cada MessageReceipt
	// (acuse de entrega/lectura) recibido del Edge (Plan 013 §10.F). Nunca es nil:
	// New() lo inicializa a un LogReceiptSink (log-only) si no se inyecta otro con
	// WithReceiptSink.
	receiptSink ReceiptSink

	// diag recibe los DiagnosticsBundle que sube el Edge (Plan 031 · T5, ADR-0023):
	// los correlaciona con su solicitud pendiente por command_id. nil = no se procesa
	// el diagnóstico remoto (un bundle recibido se ignora). Se inyecta con
	// WithDiagnosticsSink.
	diag diagnostics.BundleReceiver

	// authn delega las RPCs de auth de usuario del Edge (UserLogin/UserRefresh/
	// UserLogout, Plan 033 · T2.2, ADR-0025) en el AuthService del IAM. nil = auth
	// no disponible (esas RPCs responden UserAuthError{internal}). Se inyecta con
	// WithAuthenticator.
	authn in.Authenticator

	// authAuditor registra los eventos edge.auth.* en audit_events (CERO PII). nil =
	// la auth funciona pero no se audita. Se inyecta con WithAuthAuditor.
	authAuditor in.Auditor

	// OnIncoming, si no es nil, se invoca por cada IncomingMessage recibido del
	// Edge. Lo consume la app/los tests para observar la recepción.
	OnIncoming func(sessionID string, m *cloudlinkv1.IncomingMessage)
	// OnHeartbeat, si no es nil, se invoca por cada Heartbeat recibido. La
	// renovación del lease a partir del lease_counter la hace el propio servidor.
	OnHeartbeat func(sessionID string, m *cloudlinkv1.Heartbeat)

	acksMu sync.Mutex
	acks   map[string]chan *cloudlinkv1.Ack

	// ackTimeout acota cuánto espera SendText/SendMedia el Ack del Edge. Nunca es
	// cero: New() lo materializa a defaultAckTimeout. Es lo que impide que un Edge
	// saturado —o un stream que muere sin acusar, caso en el que NADA limpia la
	// entrada de s.acks— retenga para siempre al llamante.
	ackTimeout time.Duration

	// edgeSessions mapea cada Edge (tenant+edge) al conjunto de sus sesiones
	// vivas, para que RevokeLease pueda empujar el kill-switch a todas ellas.
	trackMu      sync.Mutex
	edgeSessions map[edgeKey]map[string]struct{}
}

// Option configura el Server al construirlo.
type Option func(*Server)

// WithLease inyecta el gestor de leases. Sin él, Connect no emite ni renueva
// leases (comportamiento T2).
func WithLease(m *lease.Manager) Option { return func(s *Server) { s.leaseMgr = m } }

// WithFleet inyecta el repositorio de fleet. Sin él, Connect no persiste el
// estado online/offline (comportamiento T2).
func WithFleet(r fleet.Repository) Option { return func(s *Server) { s.fleet = r } }

// WithCloudEncPrivKey inyecta la privada X25519 de cifrado de tránsito de la nube
// (Plan 011 §10.F). Con ella el servidor abre el enc_payload sellado por el Edge
// al ingreso; sin ella los mensajes se procesan tal como llegan (compat §10.H).
func WithCloudEncPrivKey(priv []byte) Option { return func(s *Server) { s.cloudEncPriv = priv } }

// WithReceiptSink inyecta el sink de acuses (MessageReceipt) del Plan 013 §10.F.
// Sin él, New() usa el LogReceiptSink log-only por defecto (v1: sin persistencia).
func WithReceiptSink(sink ReceiptSink) Option { return func(s *Server) { s.receiptSink = sink } }

// WithDiagnosticsSink inyecta el receptor de DiagnosticsBundle (Plan 031 · T5,
// ADR-0023). Sin él, un bundle recibido del Edge se ignora (no hay dónde almacenarlo).
func WithDiagnosticsSink(r diagnostics.BundleReceiver) Option { return func(s *Server) { s.diag = r } }

// WithAckTimeout fija cuánto espera SendText/SendMedia el Ack del Edge antes de
// rendirse con context.DeadlineExceeded (env WAPP_GRPC_ACK_TIMEOUT). Un valor <=0
// se ignora y New cae a defaultAckTimeout: el camino caliente NUNCA queda sin
// deadline. Mismo criterio que session.WithSendTimeout para el empuje.
func WithAckTimeout(d time.Duration) Option { return func(s *Server) { s.ackTimeout = d } }

// New construye un Server con el registro de sesiones y el logger dados. Las
// dependencias opcionales (lease, fleet) se pasan como Option.
func New(registry *session.Registry, log logger.Logger, opts ...Option) *Server {
	s := &Server{
		registry:     registry,
		log:          log,
		acks:         make(map[string]chan *cloudlinkv1.Ack),
		edgeSessions: make(map[edgeKey]map[string]struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	// El sink de acuses (Plan 013 §10.F) nunca es nil: log-only por defecto.
	if s.receiptSink == nil {
		s.receiptSink = NewLogReceiptSink(log)
	}
	// La espera del Ack nunca queda sin reloj (ver ackTimeout).
	if s.ackTimeout <= 0 {
		s.ackTimeout = defaultAckTimeout
	}
	return s
}

// Register registra este servidor en el ServiceRegistrar gRPC dado.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	cloudlinkv1.RegisterCloudLinkServer(reg, s)
}
