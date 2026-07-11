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
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// offlinePersistTimeout acota la persistencia de fleet-offline tras la caída del
// stream (el contexto del stream ya está cancelado; se usa uno desacoplado).
const offlinePersistTimeout = 5 * time.Second

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

	// OnIncoming, si no es nil, se invoca por cada IncomingMessage recibido del
	// Edge. Lo consume la app/los tests para observar la recepción.
	OnIncoming func(sessionID string, m *cloudlinkv1.IncomingMessage)
	// OnHeartbeat, si no es nil, se invoca por cada Heartbeat recibido. La
	// renovación del lease a partir del lease_counter la hace el propio servidor.
	OnHeartbeat func(sessionID string, m *cloudlinkv1.Heartbeat)

	acksMu sync.Mutex
	acks   map[string]chan *cloudlinkv1.Ack

	// edgeSessions mapea cada Edge (tenant+edge) al conjunto de sus sesiones
	// vivas, para que RevokeLease pueda empujar el kill-switch a todas ellas.
	trackMu      sync.Mutex
	edgeSessions map[edgeKey]map[string]struct{}
}

// edgeKey identifica un Edge dentro de un tenant.
type edgeKey struct {
	tenantID string
	edgeID   string
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
	return s
}

// Register registra este servidor en el ServiceRegistrar gRPC dado.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	cloudlinkv1.RegisterCloudLinkServer(reg, s)
}

// connCtx agrupa la identidad de un stream Connect, derivada del cert mTLS.
type connCtx struct {
	sessionID string
	tenantID  string
	edgeID    string
	// hasIdentity es true solo si se extrajo (tenantID, edgeID) del cert mTLS.
	// false en streams sin TLS (tests T2): se degrada sin lease ni fleet.
	hasIdentity bool
}

// cloudToEdgeSender es la cara de escritura de un stream Connect: el único
// método que streamSender necesita del stream gRPC (facilita el test con un fake).
type cloudToEdgeSender interface {
	Send(*cloudlinkv1.CloudToEdge) error
}

// streamSender serializa las escrituras al stream Connect de UN Edge. grpc-go
// prohíbe SendMsg concurrente sobre el mismo stream, y un Edge multiplexa N
// sesiones sobre UN solo stream (ADR-0008): por eso el candado es POR-STREAM
// (por-Edge), no por-session_id. Connect crea UNA instancia por stream y la
// registra para TODAS sus sesiones, de modo que los Push de dos sesiones del
// mismo Edge se serializan sobre el único mutex del stream (Plan 027 · Ola 0 ·
// T3, cierra H2). Satisface session.Sender.
type streamSender struct {
	mu     sync.Mutex
	stream cloudToEdgeSender
}

func newStreamSender(stream cloudToEdgeSender) *streamSender {
	return &streamSender{stream: stream}
}

func (s *streamSender) Send(msg *cloudlinkv1.CloudToEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(msg)
}

// Connect atiende el stream bidireccional CloudLink. Extrae la identidad mTLS
// del peer, registra la sesión en el primer mensaje con session_id no vacío
// (emitiendo lease inicial y marcando fleet online) y la marca offline al
// cerrarse el stream. Rutea cada EdgeToCloud por el tipo de su payload.
func (s *Server) Connect(stream grpc.BidiStreamingServer[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]) error {
	streamCtx := stream.Context()
	tenantID, edgeID, hasIdentity := peerIdentity(streamCtx)

	// Envoltorio serializado POR-STREAM: todas las sesiones de este Edge registran
	// ESTA misma instancia, así ningún par de sesiones hace SendMsg concurrente
	// sobre el stream (Plan 027 · Ola 0 · T3, cierra H2).
	sender := newStreamSender(stream)

	cc := connCtx{tenantID: tenantID, edgeID: edgeID, hasIdentity: hasIdentity}
	// releases mapea cada session_id registrado en ESTE stream a su release. Es
	// local al stream y lo muta un ÚNICO goroutine (el bucle Recv de abajo), por
	// lo que no necesita lock (ADR-0008: N sesiones multiplexadas por session_id
	// sobre un solo stream CloudLink por Edge).
	releases := make(map[string]func())
	defer func() {
		// Cierre multi-sesión: libera y marca offline CADA sesión del stream
		// (mismo patrón que RevokeLease, que itera las sesiones del Edge). El
		// map local se recorre en el goroutine de Recv, sin lock (D1/D4).
		for sid, release := range releases {
			release()
			cc2 := cc
			cc2.sessionID = sid
			s.onStreamClosed(streamCtx, cc2)
		}
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		sessionID := msg.GetSessionId()
		// connCtx por-frame: identidad de stream (tenant/edge/hasIdentity) + el
		// session_id de ESTE frame. route/renewLease/onSessionRegistered operan
		// sobre él, no sobre una 1ª sesión clavada (D3).
		frameCC := cc
		frameCC.sessionID = sessionID
		if sessionID != "" {
			// Registro perezoso por-frame (register-on-first-frame): la primera
			// vez que aparece un session_id se registra; idempotente después.
			if _, ok := releases[sessionID]; !ok {
				releases[sessionID] = s.registry.Register(sessionID, sender)
				s.log.Info("sesión CloudLink registrada",
					"session_id", sessionID, "edge_id", edgeID, "tenant_id", tenantID)
				s.onSessionRegistered(streamCtx, frameCC)
			}
		}

		s.route(streamCtx, frameCC, msg)
	}
}

// route despacha un EdgeToCloud según el tipo de su payload.
func (s *Server) route(ctx context.Context, cc connCtx, msg *cloudlinkv1.EdgeToCloud) {
	switch p := msg.GetPayload().(type) {
	case *cloudlinkv1.EdgeToCloud_Incoming:
		if s.OnIncoming != nil {
			// Abre el enc_payload sellado (si viene) y repuebla los campos
			// sensibles en memoria ANTES del motor. Un sellado corrupto se
			// descarta sin tumbar el stream (§10.I).
			if s.decodeIncoming(p.Incoming) {
				s.OnIncoming(cc.sessionID, p.Incoming)
			}
		}
	case *cloudlinkv1.EdgeToCloud_Ack:
		s.deliverAck(p.Ack)
	case *cloudlinkv1.EdgeToCloud_Heartbeat:
		if s.OnHeartbeat != nil {
			s.OnHeartbeat(cc.sessionID, p.Heartbeat)
		}
		// Plan 020 · T3: un Heartbeat con State=LOGGED_OUT anuncia que WhatsApp
		// cerró el device ⇒ sesión ZOMBIE. Se marca loggedout y NO se renueva el
		// lease (sesión muerta) ni se toca self_pn. Un State=UNSPECIFIED (default de
		// proto, 0) sigue EXACTAMENTE el camino de siempre (online normal): sin
		// regresión para toda sesión que nunca reporte LOGGED_OUT.
		if p.Heartbeat.GetState() == cloudlinkv1.SessionState_SESSION_STATE_LOGGED_OUT {
			s.markLoggedOut(ctx, cc)
			return
		}
		s.persistSelfPn(ctx, cc, p.Heartbeat)
		s.persistHealth(ctx, cc, p.Heartbeat)
		s.renewLease(ctx, cc, p.Heartbeat.GetLeaseCounter())
	case *cloudlinkv1.EdgeToCloud_Pong:
		s.log.Debug("pong recibido", "session_id", cc.sessionID, "nonce", p.Pong.GetNonce())
	case *cloudlinkv1.EdgeToCloud_Delivery:
		s.log.Debug("delivery status recibido", "session_id", cc.sessionID)
	case *cloudlinkv1.EdgeToCloud_Receipt:
		s.handleReceipt(ctx, cc, p.Receipt)
	default:
		s.log.Debug("payload EdgeToCloud desconocido", "session_id", cc.sessionID)
	}
}

// decodeIncoming abre el enc_payload sellado del IncomingMessage (Plan 011 §6.5)
// y repuebla los campos sensibles (text/push_name/from_pn/from_lid) EN MEMORIA
// antes de pasarlo al motor. Devuelve false si el mensaje debe descartarse.
//
// Compat (§10.H): si no hay enc_payload, los campos planos se usan tal cual.
// Descifrado defensivo (§10.I): si el sellado no puede abrirse o deserializarse,
// se descarta el mensaje con log del wa_message_id (NUNCA del contenido) y SIN
// tumbar el stream. Sin clave privada configurada pero con enc_payload presente,
// el mensaje también se descarta (no se puede recuperar el contenido).
func (s *Server) decodeIncoming(msg *cloudlinkv1.IncomingMessage) bool {
	enc := msg.GetEncPayload()
	if len(enc) == 0 {
		return true // compat: campos planos tal cual
	}
	if len(s.cloudEncPriv) == 0 {
		s.log.Error("ingreso: enc_payload presente pero la nube no tiene clave de cifrado; mensaje descartado",
			"wa_message_id", msg.GetWaMessageId())
		return false
	}
	raw, err := envelope.OpenWith(s.cloudEncPriv, enc)
	if err != nil {
		s.log.Error("ingreso: no se pudo abrir enc_payload; mensaje descartado",
			"wa_message_id", msg.GetWaMessageId(), "error", err)
		return false
	}
	var sp cloudlinkv1.SensitivePayload
	if err := proto.Unmarshal(raw, &sp); err != nil {
		s.log.Error("ingreso: enc_payload abierto pero no deserializa; mensaje descartado",
			"wa_message_id", msg.GetWaMessageId(), "error", err)
		return false
	}
	// Observabilidad del sellado en tránsito (Plan 011 §6.5): registra que el
	// entrante llegó sellado y que los campos planos viajaron VACÍOS por el cable
	// (text_plano_en_cable_len == 0). NUNCA loguea el contenido, solo su tamaño y
	// ausencia — evidencia del criterio 4 sin filtrar PII.
	s.log.Info("ingreso: enc_payload sellado abierto",
		"wa_message_id", msg.GetWaMessageId(),
		"enc_payload_bytes", len(enc),
		"text_plano_en_cable_len", len(msg.GetText()))
	msg.Text = sp.GetText()
	msg.PushName = sp.GetPushName()
	msg.FromPn = sp.GetFromPn()
	msg.FromLid = sp.GetFromLid()
	// Intención LLM sellada (Plan 029 · T7): el clasificador del Edge la manda dentro
	// del SensitivePayload (sus params pueden llevar texto literal del cliente). Sin
	// esta copia el intent sellado jamás llegaría al runtime (que la lee de
	// IncomingMessage.Intent). El gate de VERDAD sigue en el runtime (entitlements):
	// aquí solo se transporta. nil si el Edge no clasificó ⇒ campo vacío, sin cambio.
	msg.Intent = sp.GetIntent()
	return true
}

// onSessionRegistered marca la sesión online en fleet, la rastrea para el
// kill-switch y empuja el lease inicial al Edge. No hace nada sin identidad mTLS.
func (s *Server) onSessionRegistered(ctx context.Context, cc connCtx) {
	if !cc.hasIdentity {
		return
	}
	s.trackSession(cc)

	if s.fleet != nil {
		if err := s.fleet.MarkOnline(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
			s.log.Error("fleet: marcar online", "error", err,
				"edge_id", cc.edgeID, "session_id", cc.sessionID)
		}
	}

	if s.leaseMgr == nil {
		// Sin lease no hay identidad de kill-switch, pero el push de config al
		// conectar (ADR-0021) es independiente: se intenta igual.
		s.pushConfigsOnConnect(ctx, cc)
		return
	}
	lu, err := s.leaseMgr.IssueInitial(ctx, cc.tenantID, cc.edgeID)
	if err != nil {
		s.log.Error("lease: emitir inicial", "error", err, "edge_id", cc.edgeID)
		return
	}
	if err := s.registry.Push(cc.sessionID, leaseToCloud(cc.sessionID, lu)); err != nil {
		s.log.Error("lease: push inicial", "error", err, "session_id", cc.sessionID)
	}

	// Push de la config vigente del tenant (ADR-0021) tras el lease inicial, en el
	// MISMO punto donde ya se reconcilia estado del servidor al conectar.
	s.pushConfigsOnConnect(ctx, cc)
}

// persistSelfPn durabiliza el número propio (self_pn) que el Edge reporta en el
// Heartbeat (Plan 020 · T2). Lo NORMALIZA a E.164 (mismo normalizador que el
// motor de flujos usa al comparar el remitente) para que el conjunto persistido
// sea canónico, y lo escribe acotado por la identidad mTLS de la sesión. Es
// best-effort: sin fleet, sin identidad, sin self_pn o si no normaliza, es un
// no-op silencioso (NUNCA loguea el número: PII); un fallo de BD se LOGUEA con
// IDs opacos y no tumba el stream. Un self_pn vacío NO sobrescribe el previo
// (la impl de fleet lo trata como no-op).
func (s *Server) persistSelfPn(ctx context.Context, cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	raw := hb.GetSelfPn()
	if raw == "" {
		return // sesión sin emparejar aún: no se toca el valor previo.
	}
	norm, err := contact.Normalize(contact.KindPhoneE164, raw)
	if err != nil {
		// Un self_pn no normalizable (formato inesperado) se descarta: no se
		// persiste basura. Sin el número crudo en el log (PII), solo el hecho.
		s.log.Debug("heartbeat: self_pn no normalizable; se descarta",
			"session_id", cc.sessionID, "edge_id", cc.edgeID)
		return
	}
	if err := s.fleet.SetSelfPn(ctx, cc.tenantID, cc.edgeID, cc.sessionID, norm); err != nil {
		s.log.Error("fleet: persistir self_pn", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
		return
	}
	s.warnDeviceLimit(ctx, cc, norm)
}

// warnDeviceLimit avisa (Warn, sin PII) cuando el número self_pn recién persistido
// tiene más sesiones VIVAS que el tope de dispositivos de WhatsApp (REQ-D4). Es
// solo DETECCIÓN: no bloquea (WhatsApp ya rechaza la 5.ª vinculación en origen; un
// bloqueo duro aquí sería frágil y podría cortar sesiones legítimas por un conteo
// desincronizado). NUNCA loguea el número (PII): solo el conteo, el tope y los IDs
// opacos. Best-effort: un fallo del conteo se traga en Debug (no tumba el stream).
func (s *Server) warnDeviceLimit(ctx context.Context, cc connCtx, selfPn string) {
	n, err := s.fleet.CountLiveBySelfPn(ctx, cc.tenantID, selfPn)
	if err != nil {
		s.log.Debug("fleet: contar sesiones por self_pn para aviso de tope", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
		return
	}
	if n > fleet.DeviceLimit {
		s.log.Warn("un número supera el tope de dispositivos de WhatsApp",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"sesiones_vivas", n, "tope", fleet.DeviceLimit)
	}
}

// markLoggedOut marca la sesión como ZOMBIE (StateLoggedOut) en fleet: WhatsApp
// cerró el device (Plan 020 · T3). NO renueva el lease (sesión muerta) y se
// distingue del offline-por-red (que produce onStreamClosed→MarkOffline al caer el
// stream). No hace nada sin fleet, sin identidad mTLS o sin session_id. Usa el
// contexto del stream (aún vivo: el Edge sigue conectado, solo anuncia el logout).
func (s *Server) markLoggedOut(ctx context.Context, cc connCtx) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	s.log.Info("heartbeat: la sesión reportó logout de WhatsApp; marcada zombie",
		"session_id", cc.sessionID, "edge_id", cc.edgeID)
	if err := s.fleet.MarkLoggedOut(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
		s.log.Error("fleet: marcar loggedout", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
	}
}

// persistHealth durabiliza el snapshot de salud (SessionHealth) que el Edge adjunta
// al Heartbeat (Plan 031 · T3, ADR-0023). Es la ingesta que cierra el HUECO del
// incidente del 2026-07-11: el Cloud gana la verdad del socket (whatsapp_state),
// SEPARADA del estado del stream CloudLink (fleet.State). Best-effort: sin fleet, sin
// identidad, sin session_id o sin SessionHealth (Edge viejo) es un no-op silencioso
// que NO pisa los campos de salud previos; un fallo de BD se LOGUEA con IDs opacos y
// no tumba el stream. Solo metadatos de salud: CERO PII/llaves/credenciales.
func (s *Server) persistHealth(ctx context.Context, cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	sh := hb.GetSessionHealth()
	if sh == nil {
		return // Edge viejo (sin salud): no se tocan los campos de salud.
	}
	snap := fleet.HealthSnapshot{
		WhatsappState:     whatsappStateString(sh.GetWhatsappSocketState()),
		DegradedReason:    sh.GetDegradedReason(),
		LastEventAgeS:     sh.GetLastInboundEventAgeS(),
		DekLoadDurationMs: sh.GetDekLoadDurationMs(),
		IntentCircuit:     sh.GetIntentCircuit(),
		OutboxDepth:       sh.GetOutboxDepth(),
		BinaryVersion:     sh.GetBinaryVersion(),
		UptimeS:           sh.GetDaemonUptimeS(),
	}
	if err := s.fleet.SaveHealth(ctx, cc.tenantID, cc.edgeID, cc.sessionID, snap); err != nil {
		s.log.Error("fleet: persistir salud", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
	}
}

// whatsappStateString mapea el enum WhatsappSocketState del contrato CloudLink al
// texto canónico que persiste fleet (el dominio no importa el proto). UNSPECIFIED
// (Edge que aún no mide) cae a "" para que la API lo omita.
func whatsappStateString(st cloudlinkv1.WhatsappSocketState) string {
	switch st {
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTED:
		return "connected"
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTING:
		return "connecting"
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEGRADED:
		return "degraded"
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEAD:
		return "dead"
	default:
		return ""
	}
}

// renewLease renueva el lease del Edge a partir del counter del Heartbeat y
// empuja el LeaseUpdate. No hace nada sin lease o sin identidad.
func (s *Server) renewLease(ctx context.Context, cc connCtx, heartbeatCounter int64) {
	if s.leaseMgr == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	lu, err := s.leaseMgr.Renew(ctx, cc.tenantID, cc.edgeID, heartbeatCounter)
	if err != nil {
		s.log.Error("lease: renovar", "error", err, "edge_id", cc.edgeID)
		return
	}
	if err := s.registry.Push(cc.sessionID, leaseToCloud(cc.sessionID, lu)); err != nil {
		s.log.Debug("lease: push renovación", "error", err, "session_id", cc.sessionID)
	}
}

// onStreamClosed marca la sesión offline en fleet y deja de rastrearla. Usa un
// contexto desacoplado del stream (ya cancelado) para que la persistencia no
// falle por cancelación.
func (s *Server) onStreamClosed(streamCtx context.Context, cc connCtx) {
	if !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	s.untrackSession(cc)

	if s.fleet == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(streamCtx), offlinePersistTimeout)
	defer cancel()
	if err := s.fleet.MarkOffline(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
		s.log.Error("fleet: marcar offline", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
	}
}

// RevokeLease dispara el kill-switch del Edge: persiste la revocación y empuja
// el LeaseUpdate(Revoked) a TODAS sus sesiones vivas. Devuelve error si el lease
// no está configurado. El endpoint admin HTTP que lo invoca es T5.
func (s *Server) RevokeLease(ctx context.Context, tenantID, edgeID string) error {
	if s.leaseMgr == nil {
		return errors.New("gatewaygrpc: lease no configurado")
	}
	lu, err := s.leaseMgr.Revoke(ctx, tenantID, edgeID)
	if err != nil {
		return err
	}
	// Push CONCURRENTE del LeaseUpdate(Revoked) a todas las sesiones del Edge (Plan
	// 027 · Ola 1 · T5, cierra H6): cada Push ya está acotado por sendTimeout, y
	// paralelizarlos evita que una sesión bloqueada retrase la revocación en el resto
	// (el kill-switch debe llegar a TODAS cuanto antes). La revocación en el lease.
	// Manager ya está persistida; estos push son la notificación best-effort.
	var wg sync.WaitGroup
	for _, sid := range s.sessionsForEdge(tenantID, edgeID) {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if pushErr := s.registry.Push(sid, leaseToCloud(sid, lu)); pushErr != nil {
				s.log.Debug("revoke: push a sesión", "session_id", sid, "error", pushErr)
			}
		}(sid)
	}
	wg.Wait()
	return nil
}

// trackSession añade la sesión al conjunto vivo de su Edge.
func (s *Server) trackSession(cc connCtx) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	k := edgeKey{tenantID: cc.tenantID, edgeID: cc.edgeID}
	set := s.edgeSessions[k]
	if set == nil {
		set = make(map[string]struct{})
		s.edgeSessions[k] = set
	}
	set[cc.sessionID] = struct{}{}
}

// untrackSession quita la sesión del conjunto vivo de su Edge.
func (s *Server) untrackSession(cc connCtx) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	k := edgeKey{tenantID: cc.tenantID, edgeID: cc.edgeID}
	set := s.edgeSessions[k]
	if set == nil {
		return
	}
	delete(set, cc.sessionID)
	if len(set) == 0 {
		delete(s.edgeSessions, k)
	}
}

// sessionsForEdge devuelve una copia de las sesiones vivas del Edge dado.
func (s *Server) sessionsForEdge(tenantID, edgeID string) []string {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	set := s.edgeSessions[edgeKey{tenantID: tenantID, edgeID: edgeID}]
	out := make([]string, 0, len(set))
	for sid := range set {
		out = append(out, sid)
	}
	return out
}

// deliverAck entrega un Ack al chan pendiente correlacionado por
// acked_command_id, de forma no bloqueante, y limpia la entrada.
func (s *Server) deliverAck(ack *cloudlinkv1.Ack) {
	id := ack.GetAckedCommandId()

	s.acksMu.Lock()
	ch, ok := s.acks[id]
	if ok {
		delete(s.acks, id)
	}
	s.acksMu.Unlock()

	if !ok {
		s.log.Debug("ack sin comando pendiente", "acked_command_id", id)
		return
	}

	select {
	case ch <- ack:
	default:
	}
}

// handleReceipt procesa un MessageReceipt (acuse de entrega/lectura) recibido del
// Edge (Plan 013 §10.F/§10.G). Correlaciona por command_id con el SendText
// original y lo entrega al receiptSink.
//
// Sobre la correlación: la nube NO mantiene un registro persistente
// command_id -> destino. El único registro (s.acks) es efímero: SendText lo crea
// y deliverAck lo BORRA al llegar el Ack, mucho antes de que WhatsApp emita el
// delivered/read. Por tanto la correlación aquí es por metadato: el command_id
// (opaco) que el Edge propaga desde el SendText original, más los message_ids.
// Se loguea correlacionado y se pasa al sink; una fase futura con tabla podrá
// unir command_id con el mensaje enviado. Higiene §10.G: SOLO metadatos
// (session_id, command_id, status, message_ids, timestamp); NUNCA texto/JID.
func (s *Server) handleReceipt(ctx context.Context, cc connCtx, receipt *cloudlinkv1.MessageReceipt) {
	if receipt == nil {
		return
	}
	s.log.Info("acuse recibido del Edge",
		"session_id", cc.sessionID,
		"command_id", receipt.GetCommandId(),
		"status", receipt.GetStatus().String(),
		"message_ids", receipt.GetMessageIds(),
		"timestamp", receipt.GetTimestamp(),
	)
	if err := s.receiptSink.Record(ctx, receipt); err != nil {
		s.log.Error("acuse: el sink no pudo registrar el receipt",
			"session_id", cc.sessionID,
			"command_id", receipt.GetCommandId(),
			"error", err,
		)
	}
}

// SendText empuja un comando SendText hacia la sesión dada y espera su Ack,
// correlacionado por command_id. Devuelve el Ack recibido o un error si la
// sesión está offline o si el contexto se cancela/expira antes del Ack.
func (s *Server) SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	cmdID, err := newCommandID()
	if err != nil {
		return nil, err
	}

	ch := make(chan *cloudlinkv1.Ack, 1)
	s.acksMu.Lock()
	s.acks[cmdID] = ch
	s.acksMu.Unlock()
	defer s.clearAck(cmdID)

	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_SendText{
			SendText: &cloudlinkv1.SendText{To: to, Text: text},
		},
	}
	if pushErr := s.registry.Push(sessionID, msg); pushErr != nil {
		return nil, pushErr
	}

	select {
	case ack := <-ch:
		return ack, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("esperando ack de %q: %w", cmdID, ctx.Err())
	}
}

// SendMedia empuja un comando SendMedia (adjunto por URL prefirmada) hacia la
// sesión y espera su Ack, correlacionado por command_id — idéntico patrón a
// SendText, así el acuse delivered/read del Plan 013 funciona sin cambios. El
// binario NO viaja por gRPC: va la presignedURL (design.md §6.1) que el Edge
// descarga (GET sin credenciales) y sube a WhatsApp. kind ("document"|"image")
// elige la rama DocumentMessage/ImageMessage vía mapKind.
func (s *Server) SendMedia(ctx context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error) {
	cmdID, err := newCommandID()
	if err != nil {
		return nil, err
	}

	ch := make(chan *cloudlinkv1.Ack, 1)
	s.acksMu.Lock()
	s.acks[cmdID] = ch
	s.acksMu.Unlock()
	defer s.clearAck(cmdID)

	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_SendMedia{
			SendMedia: &cloudlinkv1.SendMedia{
				To:       to,
				Caption:  caption,
				Mime:     mime,
				Filename: filename,
				Kind:     mapKind(kind),
				Src:      &cloudlinkv1.SendMedia_PresignedUrl{PresignedUrl: presignedURL},
			},
		},
	}
	if pushErr := s.registry.Push(sessionID, msg); pushErr != nil {
		return nil, pushErr
	}

	select {
	case ack := <-ch:
		return ack, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("esperando ack de %q: %w", cmdID, ctx.Err())
	}
}

// mapKind traduce el kind del descriptor (MediaRef.Kind) al enum MediaKind del
// proto. Un kind desconocido cae a UNSPECIFIED (el Edge decide el fallback);
// "document" e "image" son los soportados en 017. Se usan literales (no el paquete
// media) para no acoplar el Gateway al módulo del Motor.
func mapKind(kind string) cloudlinkv1.MediaKind {
	switch kind {
	case "document":
		return cloudlinkv1.MediaKind_MEDIA_KIND_DOCUMENT
	case "image":
		return cloudlinkv1.MediaKind_MEDIA_KIND_IMAGE
	default:
		return cloudlinkv1.MediaKind_MEDIA_KIND_UNSPECIFIED
	}
}

// Ping empuja un comando Ping hacia la sesión dada. No espera el Pong (mínimo
// del corte): el Pong recibido se registra en nivel debug.
func (s *Server) Ping(_ context.Context, sessionID string, nonce int64) error {
	cmdID, err := newCommandID()
	if err != nil {
		return err
	}

	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_Ping{
			Ping: &cloudlinkv1.Ping{Nonce: nonce},
		},
	}
	return s.registry.Push(sessionID, msg)
}

// clearAck elimina la entrada de ack pendiente si aún existe (p.ej. tras un
// timeout sin respuesta del Edge).
func (s *Server) clearAck(cmdID string) {
	s.acksMu.Lock()
	delete(s.acks, cmdID)
	s.acksMu.Unlock()
}

// leaseToCloud envuelve un LeaseUpdate en un CloudToEdge dirigido a la sesión
// dada. No lleva command_id: es un push del servidor, no un comando con Ack.
func leaseToCloud(sessionID string, lu *cloudlinkv1.LeaseUpdate) *cloudlinkv1.CloudToEdge {
	return &cloudlinkv1.CloudToEdge{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu},
	}
}

// peerIdentity extrae (tenantID, edgeID) del cert de cliente mTLS del peer:
// CN = edgeID, Organization[0] = tenantID (como los firma la CA de enrolamiento,
// T3). Devuelve ok=false si no hay TLS o el cert no trae ambos campos: en ese
// caso Connect degrada sin lease ni fleet (compatibilidad con tests T2 sin TLS).
func peerIdentity(ctx context.Context) (tenantID, edgeID string, ok bool) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return "", "", false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", "", false
	}
	certs := tlsInfo.State.PeerCertificates
	if len(certs) == 0 {
		return "", "", false
	}
	leaf := certs[0]
	edgeID = leaf.Subject.CommonName
	if len(leaf.Subject.Organization) > 0 {
		tenantID = leaf.Subject.Organization[0]
	}
	if edgeID == "" || tenantID == "" {
		return "", "", false
	}
	return tenantID, edgeID, true
}

// newCommandID genera un identificador único de comando con el formato UUIDv4,
// usando crypto/rand (sin dependencias externas).
func newCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generando command_id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
