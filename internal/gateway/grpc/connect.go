package gatewaygrpc

import (
	"context"
	"errors"
	"io"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

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
	case *cloudlinkv1.EdgeToCloud_DiagnosticsBundle:
		// Diagnóstico remoto (Plan 031 · T5, ADR-0023): el Edge responde a un
		// DiagnosticsRequest con su bundle; se correlaciona por command_id y se almacena.
		s.storeDiagnosticsBundle(ctx, cc, p.DiagnosticsBundle)
	case *cloudlinkv1.EdgeToCloud_UserLogin:
		// Auth de usuario del plano de control del Edge (Plan 033 · T2.2, ADR-0025):
		// el Edge relaya credenciales/tokens; se delega en el IAM y se responde con un
		// UserAuthResponse correlacionado por command_id/session_id.
		s.handleUserLogin(ctx, cc, p.UserLogin)
	case *cloudlinkv1.EdgeToCloud_UserRefresh:
		s.handleUserRefresh(ctx, cc, p.UserRefresh)
	case *cloudlinkv1.EdgeToCloud_UserLogout:
		s.handleUserLogout(ctx, cc, p.UserLogout)
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
