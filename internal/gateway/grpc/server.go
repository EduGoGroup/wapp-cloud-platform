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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/inferstats"
)

// offlinePersistTimeout acota la persistencia de fleet-offline tras la caída del
// stream (el contexto del stream ya está cancelado; se usa uno desacoplado).
const offlinePersistTimeout = 5 * time.Second

// defaultAckTimeout acota la espera del Ack del Edge cuando no se configura otro
// con WithAckTimeout. Ver el porqué del valor —y la invariante contra el
// WriteTimeout del servidor HTTP— en config.AppConfig.GRPCAckTimeout.
const defaultAckTimeout = 8 * time.Second

// defaultWorkQueue es el tope de trabajos encolados POR SESIÓN en el carril de
// trabajo cuando no se configura otro con WithWorkQueue. Vale 64 igualado al techo
// de entrantes concurrentes del runtime de flujos, para que ninguna de las dos
// colas sea el cuello por accidente. Ver config.AppConfig.GatewayWorkQueue.
const defaultWorkQueue = 64

// defaultWorkBudget es el presupuesto de tiempo de pared de cada trabajo del
// carril cuando no se configura otro con WithWorkTimeout. Vale lo mismo que
// offlinePersistTimeout (5s) porque es el mismo orden de trabajo —una escritura
// contra la base— ya calibrado. Ver config.AppConfig.GatewayWorkTimeout.
const defaultWorkBudget = 5 * time.Second

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
	// inferStats guarda el último parte de inferencia de cada Edge para que /metrics
	// lo publique (T1.7-9). nil = no se recoge; el resto sigue igual. Se inyecta con
	// WithInferenceStats.
	inferStats *inferstats.Store

	// OnWarmup, si no es nil, se invoca cuando la caché de prefijo del Ollama de un
	// Edge puede haberse quedado fría: al registrar una sesión suya y tras empujarle
	// un ConfigUpdate (Plan 044 · Ola 1.7 · T1.7-4).
	//
	// `kind` es el del ConfigUpdate que se acaba de empujar, o VACÍO cuando el aviso
	// viene del handshake («este Edge acaba de conectar; no hay nada cacheado, sea cual
	// sea el kind»). Viaja porque el gateway NO SABE —ni debe— qué kinds cambian el
	// prompt: hoy empuja tres (jwks, intents, filters) y solo uno de ellos forma el
	// prefijo. Decidir aquí cuál sería meterle al gateway un conocimiento que su propio
	// ConfigProvider evita a propósito («el Gateway permanece genérico, no conoce
	// kinds»), y el precio de equivocarse es un prefill frío de ~50 s en la máquina del
	// cliente cada vez que rota una JWKS.
	//
	// 🔴 TIENE QUE VOLVER EN EL ACTO. Se invoca desde el bucle Recv del stream y desde
	// el fan-out del PUT de intents; un calentamiento dura ~50 s, así que el que lo
	// atiende ha de disparar y volver. Lo cumple *intakeahead.Pool.Warm, que encola en
	// su propia goroutine — y es un hook y no una interfaz por la misma razón que
	// OnIncoming: el nudo de construcción (el gateway se arma ANTES que el pool, que a
	// su vez necesita el selector, que necesita el gateway) se corta con una clausura
	// que se resuelve al llamar y no al construir.
	//
	// ⚠️ Este paquete NO sabe qué es un prompt ni si el tenant tiene caché que
	// calentar: solo dice CUÁNDO ha pasado algo que la enfría. El qué y el si son de
	// más arriba.
	OnWarmup func(tenantID, edgeID, sessionID, kind string)

	// acks correlaciona command_id -> envío en vuelo que espera su Ack. Desde el
	// Plan 050 · Ola 2 · T2.1 la entrada NO es el canal pelado sino un pendingAck
	// (types.go) que lleva su session_id dentro, para que la caída de un stream pueda
	// cancelar de golpe los envíos de ESA sesión sin un índice paralelo que mantener.
	//
	// 🔴 INVARIANTE — solo cierra el canal quien logra RETIRAR su entrada de este mapa
	// bajo acksMu. El enunciado completo, y por qué no basta con decir «delete y close
	// bajo el mismo mutex», está en cancelSessionAcks (send.go).
	acksMu sync.Mutex
	acks   map[string]pendingAck

	// ackTimeout acota cuánto espera SendText/SendMedia el Ack del Edge. Nunca es
	// cero: New() lo materializa a defaultAckTimeout. Es lo que impide que un Edge
	// saturado —o un stream que muere sin acusar, caso en el que NADA limpia la
	// entrada de s.acks— retenga para siempre al llamante.
	//
	// ⚠️ CORREGIDO el 2026-08-18 (Plan 050 · T1.1, ADR-0040 §Contexto). El enunciado
	// de arriba se conserva literal porque es el que dimensionó el follow-up F2 de la
	// pieza 03, pero EXAGERA: el "NADA limpia" es falso. El defer s.clearAck(cmdID)
	// de SendText y SendMedia —no de un "sendCommand", que no existe ni existió en
	// este repo— limpia la entrada SIEMPRE, por todos sus caminos de salida: haya
	// llegado el Ack, haya vencido el reloj o (desde T2.3) haya caído el stream. La
	// entrada no se fuga; vive como mucho lo que dure el ackTimeout. Lo que este reloj
	// evita de verdad NO es una fuga de memoria sino una espera: sin él, el llamante
	// HTTP se quedaría colgado. Ese matiz importa porque el eje del defecto es
	// LATENCIA, no memoria.
	ackTimeout time.Duration

	// workQueue es el tope de trabajos encolados POR SESIÓN en el carril de trabajo
	// del stream (Plan 050 · Ola 1, REQ-050.4). Nunca es cero: New() lo materializa
	// a defaultWorkQueue. Subirlo cuesta memoria por stream.
	workQueue int

	// workBudget es el presupuesto de tiempo de pared de cada trabajo del carril
	// (Plan 050 · Ola 1). Nunca es cero: New() lo materializa a defaultWorkBudget.
	// Subirlo cuesta más tiempo colgado por trabajo, y el carril es serie por sesión.
	workBudget time.Duration

	// edgeSessions mapea cada Edge (tenant+edge) al conjunto de sus sesiones
	// vivas, para que RevokeLease pueda empujar el kill-switch a todas ellas.
	trackMu      sync.Mutex
	edgeSessions map[edgeKey]map[string]struct{}

	// infers correlaciona command_id -> inferencia en vuelo que espera su
	// InferenceResult (Plan 044 · Ola 1.6 · T1.6-3, REQ-34). Es el GEMELO de acks:
	// misma invariante de cierre, mismo reloj propio, misma cancelación al caer el
	// stream. El porqué de que sean dos mapas y no uno genérico está en
	// pendingInfer (types.go).
	infersMu sync.Mutex
	infers   map[string]pendingInfer

	// inferGrace es el margen que el Cloud espera POR ENCIMA del timeout_ms que le
	// dio al Edge. Nunca es cero: New() lo materializa a DefaultInferGrace. Ver el
	// porqué del margen en Infer.
	inferGrace time.Duration
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

// WithInferenceStats inyecta el almacén en memoria del parte de inferencia del Edge
// (Plan 044 · Ola 1.7 · T1.7-9). Sin él, los números del latido siguen durabilizándose
// y sirviéndose por REST como hasta hoy, pero no salen por /metrics.
//
// Es un almacén y no un callback —a diferencia de OnWarmup o de FlowEventLifecycle—
// porque lo que se publica NO es un delta que empujar, sino un acumulado que se lee EN
// EL SCRAPE. Ver el porqué completo en metrics.RegisterInferenceStats.
func WithInferenceStats(st *inferstats.Store) Option { return func(s *Server) { s.inferStats = st } }

// WithDiagnosticsSink inyecta el receptor de DiagnosticsBundle (Plan 031 · T5,
// ADR-0023). Sin él, un bundle recibido del Edge se ignora (no hay dónde almacenarlo).
func WithDiagnosticsSink(r diagnostics.BundleReceiver) Option { return func(s *Server) { s.diag = r } }

// WithAckTimeout fija cuánto espera SendText/SendMedia el Ack del Edge antes de
// rendirse con context.DeadlineExceeded (env WAPP_GRPC_ACK_TIMEOUT). Un valor <=0
// se ignora y New cae a defaultAckTimeout: el camino caliente NUNCA queda sin
// deadline. Mismo criterio que session.WithSendTimeout para el empuje.
func WithAckTimeout(d time.Duration) Option { return func(s *Server) { s.ackTimeout = d } }

// WithWorkQueue fija cuántos trabajos puede encolar el carril POR SESIÓN antes de
// aplicar contrapresión al bucle Recv del stream (env WAPP_GATEWAY_WORK_QUEUE). Un
// valor <=0 se ignora y New cae a defaultWorkQueue: la cola NUNCA queda sin tope,
// que es lo que reintroduciría el crecimiento sin límite.
func WithWorkQueue(n int) Option { return func(s *Server) { s.workQueue = n } }

// WithWorkTimeout fija el presupuesto de tiempo de pared de cada trabajo del carril
// (env WAPP_GATEWAY_WORK_TIMEOUT). Un valor <=0 se ignora y New cae a
// defaultWorkBudget: ningún trabajo del carril queda sin reloj. Mismo criterio que
// WithAckTimeout.
func WithWorkTimeout(d time.Duration) Option { return func(s *Server) { s.workBudget = d } }

// New construye un Server con el registro de sesiones y el logger dados. Las
// dependencias opcionales (lease, fleet) se pasan como Option.
func New(registry *session.Registry, log logger.Logger, opts ...Option) *Server {
	s := &Server{
		registry:     registry,
		log:          log,
		acks:         make(map[string]pendingAck),
		infers:       make(map[string]pendingInfer),
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
	// El carril de trabajo nunca arranca sin tope ni sin reloj (ver workQueue y
	// workBudget): un cero aquí sería cola infinita o trabajo sin deadline.
	if s.workQueue <= 0 {
		s.workQueue = defaultWorkQueue
	}
	if s.workBudget <= 0 {
		s.workBudget = defaultWorkBudget
	}
	// La espera de la inferencia nunca queda sin margen (ver inferGrace e Infer).
	if s.inferGrace <= 0 {
		s.inferGrace = DefaultInferGrace
	}
	return s
}

// Register registra este servidor en el ServiceRegistrar gRPC dado.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	cloudlinkv1.RegisterCloudLinkServer(reg, s)
}
