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
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	googlegrpc "google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Server implementa cloudlinkv1.CloudLinkServer. Es seguro para uso concurrente.
// El valor cero NO es utilizable: se construye con New.
//
// Los hooks observables (OnIncoming, OnHeartbeat, OnWarmup, OnEdgeReady) nacen nil
// y deben asignarse antes de poner el servidor a servir (no se mutan mientras se
// atienden streams).
type Server struct {
	cloudlinkv1.UnimplementedCloudLinkServer

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
}

// Option configura el Server al construirlo. New aplica las opciones en el orden
// en que se le pasan, cada una una sola vez y sobre el Server que devuelve.
type Option func(*Server)

// WithLease inyecta el gestor de leases. Sin él (o con nil), Connect no emite ni
// renueva leases y RevokeLease, RevokeTenant y RestoreTenant devuelven
// "gatewaygrpc: lease no configurado".
func WithLease(_ *lease.Manager) Option { panic(pendiente.Implementar("grpc.WithLease")) }

// WithFleet inyecta el repositorio de fleet. Sin él (o con nil), Connect no persiste
// el estado online/offline y RevokeTenant no tiene instalaciones que notificar.
func WithFleet(_ fleet.Repository) Option { panic(pendiente.Implementar("grpc.WithFleet")) }

// WithCloudEncPrivKey inyecta la privada X25519 de cifrado de tránsito de la nube
// (Plan 011 §10.F). Con ella el servidor abre el enc_payload sellado por el Edge
// al ingreso; sin ella los mensajes se procesan tal como llegan (compat §10.H).
//
// 🔴 No es la DEK del ADR-0007 (la del almacén de whatsmeow, que la nube nunca ve):
// es la mitad privada del par de TRÁNSITO de la nube.
func WithCloudEncPrivKey(_ []byte) Option {
	panic(pendiente.Implementar("grpc.WithCloudEncPrivKey"))
}

// WithReceiptSink inyecta el sink de acuses (MessageReceipt) del Plan 013 §10.F.
// Sin él (o con nil), New usa el LogReceiptSink log-only: el sink nunca es nil.
func WithReceiptSink(_ ReceiptSink) Option { panic(pendiente.Implementar("grpc.WithReceiptSink")) }

// WithInferenceStats inyecta el almacén en memoria del parte de inferencia del Edge
// (Plan 044 · Ola 1.7 · T1.7-9). Sin él, los números del latido siguen durabilizándose
// y sirviéndose por REST como hasta hoy, pero no salen por /metrics.
//
// Es un almacén y no un callback —a diferencia de OnWarmup— porque lo que se publica
// NO es un delta que empujar, sino un acumulado que se lee EN EL SCRAPE.
func WithInferenceStats(_ *inferstats.Store) Option {
	panic(pendiente.Implementar("grpc.WithInferenceStats"))
}

// WithDiagnosticsSink inyecta el receptor de DiagnosticsBundle (Plan 031 · T5,
// ADR-0023). Sin él, un bundle recibido del Edge se ignora (no hay dónde almacenarlo).
func WithDiagnosticsSink(_ diagnostics.BundleReceiver) Option {
	panic(pendiente.Implementar("grpc.WithDiagnosticsSink"))
}

// WithAckTimeout fija cuánto espera SendText/SendMedia el Ack del Edge antes de
// rendirse con context.DeadlineExceeded (env WAPP_GRPC_ACK_TIMEOUT). Un valor <=0
// se ignora y New cae al plazo por defecto, 8 s: el camino caliente NUNCA queda sin
// deadline. Mismo criterio que session.WithSendTimeout para el empuje.
func WithAckTimeout(_ time.Duration) Option { panic(pendiente.Implementar("grpc.WithAckTimeout")) }

// WithWorkQueue fija cuántos trabajos puede encolar el carril POR SESIÓN antes de
// aplicar contrapresión al bucle Recv del stream (env WAPP_GATEWAY_WORK_QUEUE). Un
// valor <=0 se ignora y New cae al tope por defecto, 64: la cola NUNCA queda sin
// tope, que es lo que reintroduciría el crecimiento sin límite.
func WithWorkQueue(_ int) Option { panic(pendiente.Implementar("grpc.WithWorkQueue")) }

// WithWorkTimeout fija el presupuesto de tiempo de pared de cada trabajo del carril
// (env WAPP_GATEWAY_WORK_TIMEOUT), que es también el plazo de RevokeLease,
// RevokeTenant y RestoreTenant. Un valor <=0 se ignora y New cae al presupuesto por
// defecto, 5 s: ningún trabajo queda sin reloj. Mismo criterio que WithAckTimeout.
func WithWorkTimeout(_ time.Duration) Option { panic(pendiente.Implementar("grpc.WithWorkTimeout")) }

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
func New(_ *session.Registry, _ logger.Logger, _ ...Option) *Server {
	panic(pendiente.Implementar("grpc.New"))
}

// Register registra este servidor en el ServiceRegistrar gRPC dado, como
// implementación del servicio wapp.cloudlink.v1.CloudLink.
func (s *Server) Register(_ googlegrpc.ServiceRegistrar) {
	panic(pendiente.Implementar("grpc.Server.Register"))
}
