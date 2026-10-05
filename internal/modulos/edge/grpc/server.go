// Package grpc implementa el servidor del servicio CloudLink sobre los tipos
// generados públicos de wapp-cloudlink (gen/wapp/cloudlink/v1).
//
// El paquete se llama grpc porque conserva el nombre de su carpeta (D-3); en el
// árbol viejo se llamaba gatewaygrpc, y ese prefijo sigue vivo —literal— en sus
// textos de error. El paquete google.golang.org/grpc se importa aquí dentro con el
// alias googlegrpc; quien importe este desde fuera le pone el alias que le convenga.
//
// Sobre el núcleo en memoria del Connect (Plan 005 · T2: registro de sesiones,
// ruteo de EdgeToCloud, correlación de Acks, empuje de SendText/Ping) T4 cablea
// además la identidad mTLS, el lease (kill-switch, ADR-0007) y el fleet:
//   - La identidad (tenantID, edgeID) se extrae del cert de cliente mTLS del
//     peer. Si NO hay TLS, Connect degrada: no emite lease ni toca fleet.
//   - Al registrar una sesión: fleet online + lease inicial empujado al Edge.
//   - En cada Heartbeat: renovación del lease (counter = heartbeatCounter+1).
//   - Al caer el stream: fleet offline.
//   - RevokeLease dispara el kill-switch: persiste revocado y empuja el
//     LeaseUpdate(Revoked) a TODAS las sesiones vivas del Edge.
//
// Las dependencias de lease y fleet son OPCIONALES (WithLease/WithFleet): nil =
// sin lease ni fleet, lo que mantiene los tests sin TLS verdes.
//
// Porta internal/gateway/grpc/server.go @ c851591.
package grpc

import (
	"sync"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	googlegrpc "google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// defaultAckTimeout acota la espera del Ack del Edge cuando no se configura otro
// con WithAckTimeout. Va POR DEBAJO del WriteTimeout del servidor HTTP (10 s) a
// propósito: el 504 tiene que poder escribirse antes de que venza la escritura. Si
// alguien sube este valor, tiene que subir el WriteTimeout en el mismo cambio.
const defaultAckTimeout = 8 * time.Second

// defaultWorkQueue es el tope de trabajos encolados POR SESIÓN en el carril de
// trabajo cuando no se configura otro con WithWorkQueue. Vale 64 igualado al techo
// de entrantes concurrentes del runtime de flujos, para que ninguna de las dos
// colas sea el cuello por accidente.
const defaultWorkQueue = 64

// defaultWorkBudget es el presupuesto de tiempo de pared de cada trabajo del
// carril cuando no se configura otro con WithWorkTimeout. Vale 5 s, lo mismo que la
// persistencia del fleet-offline tras la caída del stream, porque es el mismo orden
// de trabajo —una escritura contra la base— ya calibrado.
const defaultWorkBudget = 5 * time.Second

// Server implementa cloudlinkv1.CloudLinkServer. Es seguro para uso concurrente.
// El valor cero NO es utilizable: se construye con New.
//
// Los hooks observables (OnIncoming, OnHeartbeat, OnWarmup, OnEdgeReady) nacen nil
// y deben asignarse antes de poner el servidor a servir (no se mutan mientras se
// atienden streams).
type Server struct {
	cloudlinkv1.UnimplementedCloudLinkServer

	registry *session.Registry
	log      logger.Logger

	// leaseMgr y fleet son OPCIONALES. nil => degradación (sin lease ni fleet). Se
	// inyectan con WithLease/WithFleet.
	leaseMgr *lease.Manager
	fleet    fleet.Repository

	// cloudEncPriv es la privada X25519 (32B) del par de cifrado de tránsito de la
	// nube (Plan 011 §10.F). Con ella se abre el enc_payload sellado por el Edge al
	// ingreso y se repueblan los campos sensibles en memoria. Vacía = no se intenta
	// abrir (los IncomingMessage llegan siempre en claro, compat).
	cloudEncPriv []byte

	// receiptSink es el enganche por el que se entrega cada MessageReceipt
	// (acuse de entrega/lectura) recibido del Edge (Plan 013 §10.F). Nunca es nil:
	// New lo inicializa a un LogReceiptSink (log-only) si no se inyecta otro con
	// WithReceiptSink.
	receiptSink ReceiptSink

	// diag recibe los DiagnosticsBundle que sube el Edge (Plan 031 · T5, ADR-0023):
	// los correlaciona con su solicitud en espera por command_id. nil = no se procesa
	// el diagnóstico remoto (un bundle recibido se ignora). Se inyecta con
	// WithDiagnosticsSink.
	diag diagnostics.BundleReceiver

	// configProvider entrega las configs vigentes del tenant para el push al conectar
	// (ADR-0021). nil = Connect no empuja config. Se inyecta con WithConfigProvider
	// (config_push.go).
	configProvider ConfigProvider

	// inferStats guarda el último parte de inferencia de cada Edge para que /metrics
	// lo publique (T1.7-9). nil = no se recoge; el resto sigue igual. Se inyecta con
	// WithInferenceStats.
	inferStats *inferstats.Store

	// OnIncoming, si no es nil, se invoca por cada IncomingMessage recibido del
	// Edge. Lo consume la app/los tests para observar la recepción.
	OnIncoming func(sessionID string, m *cloudlinkv1.IncomingMessage)
	// OnHeartbeat, si no es nil, se invoca por cada Heartbeat recibido. La
	// renovación del lease a partir del lease_counter la hace el propio servidor.
	OnHeartbeat func(sessionID string, m *cloudlinkv1.Heartbeat)

	// OnWarmup, si no es nil, se invoca cuando la caché de prefijo del Ollama de un
	// Edge puede haberse quedado fría, o cuando ese Edge acaba de poder servirla.
	//
	// Los TRES disparadores de hoy, y de dónde salen:
	//
	//  1. Tras empujarle un ConfigUpdate (T1.7-4, el fan-out de PushConfig). UNO
	//     POR EDGE, no por sesión.
	//  2. 🆕 En la TRANSICIÓN A READY de `Heartbeat.inference_readiness` (T1.8-6,
	//     observeReadiness). Es el disparador BUENO: el Cloud calienta cuando el
	//     Edge DICE que puede, no cuando se registra.
	//  3. Al registrar una sesión suya, y SOLO mientras ese Edge no diga nada
	//     (T1.7-4, degradado a compatibilidad por T1.8-6; warmOnRegister, que lleva
	//     escrita su condición de retirada).
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
	// 🔴 TIENE QUE VOLVER EN EL ACTO (T-13). Se invoca desde el bucle Recv del stream y
	// desde el fan-out del PUT de intents; un calentamiento dura ~50 s, así que el que
	// lo atiende ha de disparar y volver. Lo cumple el pool de inferencia anticipada
	// (su Warm encola en su propia goroutine) — y es un hook y no una interfaz por la
	// misma razón que OnIncoming: el nudo de construcción (el gateway se arma ANTES que
	// el pool, que a su vez necesita el selector, que necesita el gateway) se corta con
	// una clausura que se resuelve al llamar y no al construir.
	//
	// ⚠️ Este paquete NO sabe qué es un prompt ni si el tenant tiene caché que
	// calentar: solo dice CUÁNDO ha pasado algo que la enfría. El qué y el si son de
	// más arriba.
	OnWarmup func(tenantID, edgeID, sessionID, kind string)

	// OnEdgeReady, si no es nil, se invoca en la MISMA TRANSICIÓN A READY que dispara
	// el calentamiento (observeReadiness), y con la misma dirección: (tenant, Edge).
	// Es el disparador POR EVENTO de la cadena de lote —Plan 044 · Ola 2 · T2.7,
	// D-044.43—, y su consumidor natural es el despertar del worker del pipeline.
	//
	// 🔴 POR QUÉ UN SEGUNDO HOOK SOBRE EL MISMO FLANCO, Y NO UN `kind` MÁS DE OnWarmup.
	// Porque son DOS trabajos con dueños distintos y consecuencias distintas: calentar
	// es best-effort y se puede repetir gratis; reanudar la cadena de lote mueve filas
	// de `intake_jobs` a `processing`. Meterlos en la misma clausura obligaría al que
	// la atienda a distinguirlos por el `kind`, que es exactamente el `if` que el hook
	// existe para no tener. La DETECCIÓN del flanco sigue siendo una sola: lo que hay
	// son dos consumidores de un mismo hecho.
	//
	// 🔴 TIENE QUE VOLVER EN EL ACTO, igual que OnWarmup y por el mismo motivo (T-13):
	// se invoca INLINE en la goroutine del Recv del stream. Lo cumple el despertar del
	// worker, que es un envío no bloqueante a un canal con buffer.
	//
	// Mientras siga nil, el gateway se comporta EXACTAMENTE igual que sin él.
	OnEdgeReady func(tenantID, edgeID string)

	// acks correlaciona command_id -> envío en vuelo que espera su Ack. Desde el
	// Plan 050 · Ola 2 · T2.1 la entrada NO es el canal pelado sino un pendingAck
	// que lleva su session_id dentro, para que la caída de un stream pueda
	// cancelar de golpe los envíos de ESA sesión sin un índice paralelo que mantener.
	//
	// 🔴 INVARIANTE — solo cierra el canal quien logra RETIRAR su entrada de este mapa
	// bajo acksMu. El enunciado completo, y por qué no basta con decir «delete y close
	// bajo el mismo mutex», está en cancelSessionAcks.
	acksMu sync.Mutex
	acks   map[string]pendingAck

	// ackTimeout acota cuánto espera SendText/SendMedia el Ack del Edge. Nunca es
	// cero: New lo materializa a defaultAckTimeout. Es lo que impide que un Edge
	// saturado —o un stream que muere sin acusar— retenga para siempre al llamante.
	//
	// ⚠️ Lo que este reloj evita NO es una fuga de memoria sino una espera (Plan 050 ·
	// T1.1, ADR-0040 §Contexto): el defer s.clearAck(cmdID) de SendText y SendMedia
	// limpia la entrada SIEMPRE, por todos sus caminos de salida —haya llegado el Ack,
	// haya vencido el reloj o (desde T2.3) haya caído el stream—. La entrada no se
	// fuga; vive como mucho lo que dure el ackTimeout. Sin el reloj, el llamante HTTP
	// se quedaría colgado. Ese matiz importa porque el eje del defecto es LATENCIA,
	// no memoria.
	ackTimeout time.Duration

	// workQueue es el tope de trabajos encolados POR SESIÓN en el carril de trabajo
	// del stream (Plan 050 · Ola 1, REQ-050.4). Nunca es cero: New lo materializa
	// a defaultWorkQueue. Subirlo cuesta memoria por stream.
	workQueue int

	// workBudget es el presupuesto de tiempo de pared de cada trabajo del carril
	// (Plan 050 · Ola 1). Nunca es cero: New lo materializa a defaultWorkBudget.
	// Subirlo cuesta más tiempo colgado por trabajo, y el carril es serie por sesión.
	workBudget time.Duration

	// edgeSessions mapea cada Edge (tenant+edge) al conjunto de sus sesiones
	// vivas, para que RevokeLease pueda empujar el kill-switch a todas ellas.
	//
	// edgeReadiness guarda la ÚLTIMA DISPONIBILIDAD DE INFERENCIA QUE ESE EDGE HA
	// DICHO (Heartbeat.inference_readiness, campo 6 del contrato desde v0.17.0;
	// Plan 044 · Ola 1.8 · T1.8-6, D-044.43). Es lo que convierte al gateway en
	// CONSUMIDOR del latido: hasta hoy el calentamiento se disparaba a ciegas al
	// registrar la sesión, y si el cajero del Edge no estaba, el Edge contestaba
	// OLLAMA_DOWN y nadie reintentaba jamás.
	//
	// 🔴 POR QUÉ VIVE AQUÍ Y NO JUNTO AL `calEnVuelo` DEL POOL DE INFERENCIA ANTICIPADA.
	// Aquel es el cerrojo «uno en vuelo por Edge» del pool: se pone y se borra
	// con un defer alrededor de UNA goroutine de calentamiento, y su única pregunta
	// es «¿ya hay uno corriendo?». Esto es otra cosa y con otro dueño: su función es
	// decidir SI EL GATEWAY LLAMA a OnWarmup, o sea, ocurre un escalón ANTES y en
	// otro paquete. El gateway no puede alcanzar el candado del pool —ni debe: el
	// hook OnWarmup existe precisamente para que estos dos no se conozcan— así que
	// ponerlo allí obligaría a exportar un getter del pool y a que el gateway
	// dependiera de él, deshaciendo el desacople que el hook compra.
	//
	// Su hermano de verdad es `edgeSessions`, que está justo encima: mismo eje
	// (por-Edge, no por-sesión, ADR-0008), mismo ciclo de vida (nace con el primer
	// latido del Edge, muere cuando se va su última sesión) y por eso MISMO CANDADO.
	// Un mutex propio solo añadiría un orden de bloqueo que mantener a cambio de
	// nada: las dos escrituras son O(1) y nunca llaman a nadie con el candado tomado.
	//
	// ⚠️ NO ES DURABLE Y ES UNA DECISIÓN (D-044.43). Un reinicio del Cloud lo vacía y
	// no se pregunta a nadie: el estado se REAPRENDE con el primer latido de cada
	// Edge —que llegan solos y en cadencia— y ese primer latido READY se lee como
	// transición, así que el calentamiento se repite. Repetirlo es barato (prefill
	// caliente 0,07–0,55 s medidos) y nadie espera detrás. Por lo mismo no hay
	// barrendero: lo que detecta el «vivo pero atascado» es DefaultWarmTimeout
	// (110 s) más el cerrojo calEnVuelo del pool, no un registro aquí.
	//
	// 🔴 EL CERO (INFERENCE_READINESS_UNSPECIFIED) SIGNIFICA «ESTE EDGE NO LO DICE»,
	// JAMÁS «no puede». Leerlo como DOWN dejaría de calentar a toda la flota vieja
	// sin producir un solo error (T-6). Ver observeReadiness y warmOnRegister.
	trackMu       sync.Mutex
	edgeSessions  map[edgeKey]map[string]struct{}
	edgeReadiness map[edgeKey]cloudlinkv1.InferenceReadiness
}

// Option configura el Server al construirlo. New aplica las opciones en el orden
// en que se le pasan, cada una una sola vez y sobre el Server que devuelve.
type Option func(*Server)

// WithLease inyecta el gestor de leases. Sin él (o con nil), Connect no emite ni
// renueva leases y RevokeLease, RevokeTenant y RestoreTenant devuelven
// "gatewaygrpc: lease no configurado".
func WithLease(m *lease.Manager) Option { return func(s *Server) { s.leaseMgr = m } }

// WithFleet inyecta el repositorio de fleet. Sin él (o con nil), Connect no persiste
// el estado online/offline y RevokeTenant no tiene instalaciones que notificar.
func WithFleet(r fleet.Repository) Option { return func(s *Server) { s.fleet = r } }

// WithCloudEncPrivKey inyecta la privada X25519 de cifrado de tránsito de la nube
// (Plan 011 §10.F). Con ella el servidor abre el enc_payload sellado por el Edge
// al ingreso; sin ella los mensajes se procesan tal como llegan (compat §10.H).
//
// 🔴 No es la DEK del ADR-0007 (la del almacén de whatsmeow, que la nube nunca ve):
// es la mitad privada del par de TRÁNSITO de la nube.
func WithCloudEncPrivKey(priv []byte) Option { return func(s *Server) { s.cloudEncPriv = priv } }

// WithReceiptSink inyecta el sink de acuses (MessageReceipt) del Plan 013 §10.F.
// Sin él (o con nil), New usa el LogReceiptSink log-only: el sink nunca es nil.
func WithReceiptSink(sink ReceiptSink) Option { return func(s *Server) { s.receiptSink = sink } }

// WithInferenceStats inyecta el almacén en memoria del parte de inferencia del Edge
// (Plan 044 · Ola 1.7 · T1.7-9). Sin él, los números del latido siguen durabilizándose
// y sirviéndose por REST como hasta hoy, pero no salen por /metrics.
//
// Es un almacén y no un callback —a diferencia de OnWarmup— porque lo que se publica
// NO es un delta que empujar, sino un acumulado que se lee EN EL SCRAPE.
func WithInferenceStats(st *inferstats.Store) Option { return func(s *Server) { s.inferStats = st } }

// WithDiagnosticsSink inyecta el receptor de DiagnosticsBundle (Plan 031 · T5,
// ADR-0023). Sin él, un bundle recibido del Edge se ignora (no hay dónde almacenarlo).
func WithDiagnosticsSink(r diagnostics.BundleReceiver) Option { return func(s *Server) { s.diag = r } }

// WithAckTimeout fija cuánto espera SendText/SendMedia el Ack del Edge antes de
// rendirse con context.DeadlineExceeded (env WAPP_GRPC_ACK_TIMEOUT). Un valor <=0
// se ignora y New cae al plazo por defecto, 8 s: el camino caliente NUNCA queda sin
// deadline. Mismo criterio que session.WithSendTimeout para el empuje.
func WithAckTimeout(d time.Duration) Option { return func(s *Server) { s.ackTimeout = d } }

// WithWorkQueue fija cuántos trabajos puede encolar el carril POR SESIÓN antes de
// aplicar contrapresión al bucle Recv del stream (env WAPP_GATEWAY_WORK_QUEUE). Un
// valor <=0 se ignora y New cae al tope por defecto, 64: la cola NUNCA queda sin
// tope, que es lo que reintroduciría el crecimiento sin límite.
func WithWorkQueue(n int) Option { return func(s *Server) { s.workQueue = n } }

// WithWorkTimeout fija el presupuesto de tiempo de pared de cada trabajo del carril
// (env WAPP_GATEWAY_WORK_TIMEOUT), que es también el plazo de RevokeLease,
// RevokeTenant y RestoreTenant. Un valor <=0 se ignora y New cae al presupuesto por
// defecto, 5 s: ningún trabajo queda sin reloj. Mismo criterio que WithAckTimeout.
func WithWorkTimeout(d time.Duration) Option { return func(s *Server) { s.workBudget = d } }

// New construye un Server con el registro de sesiones y el logger dados, y le
// aplica las opciones en orden. Las dependencias opcionales (lease, fleet, sinks)
// se pasan como Option; sin ellas el gateway degrada a no-op en esa pieza.
//
// Lo que New materializa después de las opciones (D-F2-10: el cero NUNCA es «sin
// reloj» ni «cola infinita»):
//   - sin sink de acuses → un LogReceiptSink sobre el mismo logger;
//   - plazo del Ack <= 0 → 8 s;
//   - tope de cola del carril <= 0 → 64;
//   - presupuesto de trabajo <= 0 → 5 s.
//
// Los cuatro hooks nacen nil. El Server devuelto no tiene sesiones ni envíos en vuelo.
func New(registry *session.Registry, log logger.Logger, opts ...Option) *Server {
	s := &Server{
		registry:      registry,
		log:           log,
		acks:          make(map[string]pendingAck),
		edgeSessions:  make(map[edgeKey]map[string]struct{}),
		edgeReadiness: make(map[edgeKey]cloudlinkv1.InferenceReadiness),
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
	return s
}

// Register registra este servidor en el ServiceRegistrar gRPC dado, como
// implementación del servicio wapp.cloudlink.v1.CloudLink.
func (s *Server) Register(reg googlegrpc.ServiceRegistrar) {
	cloudlinkv1.RegisterCloudLinkServer(reg, s)
}
