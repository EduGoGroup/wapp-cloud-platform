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

// Connect atiende el stream bidireccional CloudLink. Extrae la identidad mTLS
// del peer, registra la sesión en el primer mensaje con session_id no vacío
// (emitiendo lease inicial y marcando fleet online) y la marca offline al
// cerrarse el stream. Rutea cada EdgeToCloud por el tipo de su payload.
func (s *Server) Connect(stream grpc.BidiStreamingServer[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]) error {
	streamCtx := stream.Context()
	tenantID, edgeID, hasIdentity := peerIdentity(streamCtx)

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
				releases[sessionID] = s.registry.Register(sessionID, stream)
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
		s.renewLease(ctx, cc, p.Heartbeat.GetLeaseCounter())
	case *cloudlinkv1.EdgeToCloud_Pong:
		s.log.Debug("pong recibido", "session_id", cc.sessionID, "nonce", p.Pong.GetNonce())
	case *cloudlinkv1.EdgeToCloud_Delivery:
		s.log.Debug("delivery status recibido", "session_id", cc.sessionID)
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
	for _, sid := range s.sessionsForEdge(tenantID, edgeID) {
		if pushErr := s.registry.Push(sid, leaseToCloud(sid, lu)); pushErr != nil {
			s.log.Debug("revoke: push a sesión", "session_id", sid, "error", pushErr)
		}
	}
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
