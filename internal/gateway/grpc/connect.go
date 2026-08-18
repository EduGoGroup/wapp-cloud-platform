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

	// Carril de trabajo POR-STREAM (Plan 050 · Ola 1 · T1.6, ADR-0040 §Decisión.1):
	// el bucle Recv de abajo deja de hacer el trabajo pesado inline y lo SUELTA
	// aquí, en una cola por sesión servida por su propia goroutine, para que un
	// receipt lento de la sesión A no bloquee el heartbeat de la sesión B.
	//
	// Su base es context.WithoutCancel(streamCtx) —el mismo molde que onStreamClosed
	// montaba a mano— porque el trabajo en vuelo NO debe morir con el stream:
	// persistir que un Edge se fue importa PRECISAMENTE cuando su stream ya murió
	// (D-050.5). El reloj se lo pone cada job (workBudget), no el stream.
	lane := newWorkLane(context.WithoutCancel(streamCtx), s.workQueue, s.workBudget, s.log)

	cc := connCtx{tenantID: tenantID, edgeID: edgeID, hasIdentity: hasIdentity}
	// releases mapea cada session_id registrado en ESTE stream a su release. Es
	// local al stream y lo muta un ÚNICO goroutine (el bucle Recv de abajo), por
	// lo que no necesita lock (ADR-0008: N sesiones multiplexadas por session_id
	// sobre un solo stream CloudLink por Edge).
	releases := make(map[string]func())
	// Cierre en dos tiempos del carril (T1.6) con el MarkOffline ordenado dentro
	// (T1.11). Este defer corre en la goroutine del bucle Recv y SOLO DESPUÉS de que
	// Recv haya retornado: es eso lo que garantiza que ningún submit ocurra en
	// paralelo al wg.Wait() del drenaje. Ver closeStream para el orden exacto.
	defer s.closeStream(lane, cc, releases)

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

		s.route(lane, frameCC, msg)
	}
}

// closeStream cierra el stream y su carril, en el ORDEN que el carril exige
// (T1.6 · T1.11 · T2.2):
//
//  1. por cada sesión del stream: release() en el registry, cancelación de sus acuses
//     en vuelo SI la sesión quedó sin stream (ver la condición en el cuerpo) y
//     MarkOffline ENCOLADO como último job de esa sesión (onStreamClosed), no
//     ejecutado por fuera.
//  2. seal(): el carril deja de aceptar trabajo nuevo —devolviendo error, no
//     encolando— y los workers ociosos despiertan para morir.
//  3. drain(): se espera a los workers con el presupuesto de una unidad de trabajo;
//     lo que no quepa se abandona con un Warn que dice cuántos jobs y de qué tipo.
//
// 🔴 Los tres pasos ocurren en la MISMA goroutine —la del bucle Recv, y solo después
// de que Recv haya retornado—. Esa es la razón de que el drenaje sea seguro: un
// submit que cree una cola nueva (y con ella un wg.Add) en paralelo al wg.Wait() de
// drain sería un «WaitGroup misuse: Add called concurrently with Wait», que es
// pánico. Nada más puede encolar: route se llama ÚNICAMENTE desde ese bucle.
//
// 🔴 Por qué el MarkOffline se encola ANTES del seal y no después, aunque el carril
// exente al jobOffline del sellado. La exención cubre sessQueue.sealing, pero NO
// cubre sessQueue.done, que el worker se pone a sí mismo —con el mutex tomado— justo
// antes de morir, y que enqueue comprueba PRIMERO, también para el jobOffline. En el
// caso normal de cierre la cola está vacía y su worker ocioso: seal() lo despierta,
// el worker marca done y muere, y un submit posterior rebota con errLaneSealed. Es
// decir: sellar primero perdería el MarkOffline casi siempre, y la flota mostraría
// «online» un Edge que ya se fue — exactamente lo que T1.11 viene a evitar.
//
// Encolar primero es, además, DETERMINISTA: antes del seal ninguna cola puede estar
// done (un worker solo sale de su espera con trabajo o con sealing=true), así que
// este submit no puede fallar por carrera. Y no altera la garantía de orden que pide
// D-050.2: como Recv ya retornó, nadie más encolará después, de modo que el
// MarkOffline sigue siendo el ÚLTIMO job de su sesión.
func (s *Server) closeStream(lane *workLane, cc connCtx, releases map[string]func()) {
	// Cierre multi-sesión: libera y marca offline CADA sesión del stream (mismo
	// patrón que RevokeLease, que itera las sesiones del Edge). El map local se
	// recorre en el goroutine de Recv, sin lock (D1/D4).
	for sid, release := range releases {
		release()
		// Los envíos en vuelo de esta sesión dejan de esperar YA (Plan 050 · Ola 2 ·
		// T2.2): sin esto, el llamante HTTP se come el ackTimeout entero —8 s— por un
		// acuse que el gateway ya sabe que no va a llegar.
		//
		// 🔴 La condición NO es defensiva, es obligatoria, y es el hallazgo de esta ola.
		// session.Registry.Register es ÚLTIMA-GANA y el release() que devuelve compara
		// identidad: el release de un stream ya reemplazado es un no-op deliberado. Si
		// el Edge RECONECTÓ antes de que este stream terminara de morir, quien está
		// registrado bajo este session_id es el stream NUEVO, y cancelar «los acuses de
		// la sesión» a secas mataría los envíos en vuelo de un Edge que está
		// perfectamente vivo — reportándole al operador «la sesión se cayó» sobre una
		// conexión sana. Preguntar por Online() es lo que distingue «este stream murió»
		// de «esta sesión se quedó sin nadie», que es la condición real.
		//
		// Que eso pueda pasar cuelga de que s.acks se correlaciona por command_id, y un
		// command_id no sabe de qué stream salió: el Ack de un comando empujado por el
		// stream viejo puede llegar por el nuevo, y DEBE poder llegar. Es la misma
		// familia que DEUDA-050.1 (la carrera de la reconexión rápida), declarada más
		// abajo en onStreamClosed sobre este mismo cierre.
		if !s.registry.Online(sid) {
			s.cancelSessionAcks(sid)
		}
		cc2 := cc
		cc2.sessionID = sid
		s.onStreamClosed(lane, cc2)
	}
	lane.seal()
	lane.drain(s.workBudget)
}

// route despacha un EdgeToCloud según el tipo de su payload. Lo que es O(1) en
// memoria se resuelve AQUÍ, en la goroutine del bucle Recv; lo que toca red o base
// de datos se SUELTA al carril (ADR-0040 §Decisión.3, design.md §3). La enumeración
// de qué se queda dentro es la del ADR y no se amplía sin tocarlo.
//
// Ya no recibe el ctx del stream: todo lo que lo necesitaba vive ahora en el carril,
// y cada job trae el SUYO (presupuesto propio, desacoplado de la muerte del stream).
// Lo que queda inline no necesita contexto.
//
// Los punteros del payload se pueden retener en la cola sin copiarlos: grpc-go
// asigna un EdgeToCloud NUEVO en cada Recv, así que el job encolado no compite con
// el siguiente frame por la misma memoria.
//
// 🔴 INVARIANTE — route tiene UN ÚNICO LLAMANTE en producción: el bucle Recv de
// Connect. No se le añade un segundo sin leer esto antes. De ese invariante cuelga
// la garantía de D-050.2 (que el MarkOffline sea el ÚLTIMO job de su sesión): el
// closeStream encola el jobOffline con el carril TODAVÍA ABIERTO —tiene que
// hacerlo, ver allí—, así que nada impide MECÁNICAMENTE que llegue otro submit
// después; lo que lo impide es que Recv ya retornó y no queda nadie que encole.
// Es una garantía POR CONSTRUCCIÓN, no por mecanismo. Un segundo llamante de route
// (una goroutine de push, un reintento, un test que lo cablee en producción) la
// rompe en silencio: el MarkOffline dejaría de ser el último y la flota podría
// mostrar «online» un Edge que ya se fue.
func (s *Server) route(lane *workLane, cc connCtx, msg *cloudlinkv1.EdgeToCloud) {
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
		// 🔴 SE QUEDA INLINE A PROPÓSITO (ADR-0040 §Decisión.3). deliverAck es O(1) en
		// memoria: un lookup en un map y un envío no bloqueante a un canal con buffer.
		// El Ack es la VÍCTIMA del head-of-line, no su causa — mandarlo al carril le
		// añadiría la latencia del trabajo pesado justo en el camino que este plan
		// viene a proteger.
		s.deliverAck(p.Ack)
	case *cloudlinkv1.EdgeToCloud_Heartbeat:
		// El hook es de test/observación, no I/O: se queda inline (design.md §3).
		if s.OnHeartbeat != nil {
			s.OnHeartbeat(cc.sessionID, p.Heartbeat)
		}
		s.submitHeartbeat(lane, cc, p.Heartbeat)
	case *cloudlinkv1.EdgeToCloud_Pong:
		s.log.Debug("pong recibido", "session_id", cc.sessionID, "nonce", p.Pong.GetNonce())
	// El case de EdgeToCloud_Delivery se retiró el 2026-08-12 junto con el campo 11 del
	// contrato: era un frame con consumidor (este log.Debug y nada más) y sin productor
	// —ningún punto del Edge lo emitió nunca—. Los acuses reales llegan como Receipt.
	case *cloudlinkv1.EdgeToCloud_Receipt:
		// T1.8: el sink escribe UNA FILA POR message_id contra la base. Al carril, y
		// sin coalescer ni descartar jamás: un acuse es estado idempotente sobre un
		// mensaje NUESTRO (ADR-0037 §Decisión.7), así que diferirlo es legítimo y
		// perderlo no lo es.
		receipt := p.Receipt
		s.submitJob(lane, cc, jobReceipt, func(ctx context.Context) {
			s.handleReceipt(ctx, cc, receipt)
		})
	case *cloudlinkv1.EdgeToCloud_DiagnosticsBundle:
		// Diagnóstico remoto (Plan 031 · T5, ADR-0023): el Edge responde a un
		// DiagnosticsRequest con su bundle; se correlaciona por command_id y se almacena.
		// T1.10: escritura GRANDE y sin urgencia ⇒ al carril, la que menos discusión
		// tiene de las cinco.
		bundle := p.DiagnosticsBundle
		s.submitJob(lane, cc, jobDiagnostics, func(ctx context.Context) {
			s.storeDiagnosticsBundle(ctx, cc, bundle)
		})
	// Las tres ramas de auth (T1.9) hacen una LLAMADA HTTP SALIENTE a identity-core
	// más el INSERT de auditoría. La cola de latencia de una salida de red la fija un
	// TERCERO, así que son las que peor pintan dentro del bucle Recv.
	//
	// 🔴 Único cambio de semántica visible desde fuera de toda la ola (design.md §3):
	// la respuesta sale ahora desde el carril, no desde la goroutine del bucle. El
	// Edge no nota diferencia —viaja por el mismo streamSender, que serializa las
	// escrituras—, pero el orden relativo entre una respuesta de auth y un
	// ConfigUpdate empujado por OTRA vía deja de estar garantizado por accidente.
	// Hoy nadie depende de ese orden; queda escrito para que nadie empiece.
	case *cloudlinkv1.EdgeToCloud_UserLogin:
		// Auth de usuario del plano de control del Edge (Plan 033 · T2.2, ADR-0025):
		// el Edge relaya credenciales/tokens; se delega en el IAM y se responde con un
		// UserAuthResponse correlacionado por command_id/session_id.
		login := p.UserLogin
		s.submitJob(lane, cc, jobAuth, func(ctx context.Context) {
			s.handleUserLogin(ctx, cc, login)
		})
	case *cloudlinkv1.EdgeToCloud_UserRefresh:
		refresh := p.UserRefresh
		s.submitJob(lane, cc, jobAuth, func(ctx context.Context) {
			s.handleUserRefresh(ctx, cc, refresh)
		})
	case *cloudlinkv1.EdgeToCloud_UserLogout:
		logout := p.UserLogout
		s.submitJob(lane, cc, jobAuth, func(ctx context.Context) {
			s.handleUserLogout(ctx, cc, logout)
		})
	default:
		s.log.Debug("payload EdgeToCloud desconocido", "session_id", cc.sessionID)
	}
}

// submitHeartbeat suelta al carril el trabajo del Heartbeat (T1.7).
//
// Los TRES —self_pn, salud y renovación de lease— viajan en UN SOLO job porque son
// un solo hecho y su orden importa: lo que el Edge cuenta en un latido se persiste
// junto, y el lease se renueva DESPUÉS de haberlo guardado. Repartirlos en tres jobs
// los expondría a intercalarse con la coalescencia (D-050.4), que sustituye el job
// pendiente entero: dos latidos podrían quedar mezclados a medias.
//
// ⚠️ Los tres comparten UN presupuesto (workBudget, 5 s por defecto), no uno cada
// uno. Si persistSelfPn se come el reloj, renewLease recibe un ctx casi vencido y su
// Push falla: el lease NO queda renovado ante el Edge (lo grita el Warn de runJob,
// más el log del propio renewLease) y el Edge lo reintentará en el siguiente latido.
// Subir el presupuesto «para que quepa» sería una decisión de ADR, no un ajuste.
func (s *Server) submitHeartbeat(lane *workLane, cc connCtx, hb *cloudlinkv1.Heartbeat) {
	// Plan 020 · T3: un Heartbeat con State=LOGGED_OUT anuncia que WhatsApp cerró el
	// device ⇒ sesión ZOMBIE. Se marca loggedout y NO se renueva el lease (sesión
	// muerta) ni se toca self_pn. Un State=UNSPECIFIED (default de proto, 0) sigue
	// EXACTAMENTE el camino de siempre (online normal): sin regresión para toda
	// sesión que nunca reporte LOGGED_OUT.
	//
	// 🔴 CORREGIDO EL 2026-08-18 (Plan 050 · Ola 1, decisión de Jhoan). Se encola
	// como jobLogout: un tipo PROPIO, exento de coalescencia. Sigue yendo por la
	// MISMA cola de la sesión, así que conserva el orden FIFO frente al trabajo en
	// vuelo — que es lo único que la versión anterior quería y lo único que hacía
	// falta.
	//
	// El enunciado que sustituye, literal (nada se borra):
	//
	//	«Se encola como jobHeartbeat, no como un tipo propio, para COMPARTIR la
	//	serialización de su rama hermana: son dos versiones del mismo hecho y
	//	ninguna puede adelantar a la otra. El precio, explícito: como todo
	//	jobHeartbeat, un latido posterior lo coalesce (D-050.4) — y eso es lo
	//	correcto, porque la regla del tipo es que gane el más reciente.»
	//
	// Por qué era falso: «gana el más reciente» vale entre dos latidos NORMALES,
	// que son el mismo hecho contado dos veces. Un logout no es eso: es un hecho
	// TERMINAL. Coalescido, un latido posterior lo SUSTITUÍA en sitio
	// (worklane.go, enqueue) y la sesión zombi (a) no se marcaba `loggedout` y
	// (b) SÍ renovaba su lease — las dos cosas exactas que el Plan 020 · T3
	// prohíbe. Y no es un caso raro: el Edge sigue latiendo después de anunciar el
	// logout, así que el latido que lo borra es el caso NORMAL, no el excepcional.
	//
	// jobLogout no se coalesce ni se descarta jamás. La regla «solo los heartbeats
	// se coalescen» (D-050.4) no se toca: es la que hace que esta corrección
	// consista en un tipo nuevo y nada más.
	if hb.GetState() == cloudlinkv1.SessionState_SESSION_STATE_LOGGED_OUT {
		s.submitJob(lane, cc, jobLogout, func(ctx context.Context) {
			s.markLoggedOut(ctx, cc)
		})
		return
	}
	s.submitJob(lane, cc, jobHeartbeat, func(ctx context.Context) {
		s.persistSelfPn(ctx, cc, hb)
		s.persistHealth(ctx, cc, hb)
		s.renewLease(ctx, cc, hb.GetLeaseCounter())
	})
}

// submitJob suelta un trabajo al carril de su sesión y NUNCA lo pierde en silencio:
// si el carril ya está sellado —el stream se está cerrando— lo dice con un Warn que
// nombra la sesión y el tipo de trabajo. Una pérdida muda aquí sería el mismo defecto
// que este plan viene a arreglar, con otra cara.
//
// El submit puede BLOQUEAR al bucle Recv si la cola de esa sesión llegó a su tope
// (REQ-050.4). Es intencionado —frenar, no descartar—: es así como el backpressure
// nativo de HTTP/2 sigue llegando hasta el Edge en vez de fabricarse uno propio.
//
// 🔴 Un frame SIN session_id no llega a encolarse. El carril indexa por session_id,
// así que un submit con la llave "" crearía la cola perSess[""] Y SU GOROUTINE: un
// carril fantasma que no corresponde a ninguna sesión, que nunca recibe su
// jobOffline —closeStream itera `releases`, donde solo hay session_id no vacíos— y
// que solo muere en el seal del cierre del stream. Antes del carril ese trabajo se
// resolvía inline; ahora se rechaza AQUÍ, en el llamante, que es donde el rechazo
// es explícito, barato y observable.
//
// submitHeartbeat no necesita su propia guarda: sus dos ramas terminan las dos en
// este submitJob, así que quedan cubiertas por esta.
//
// 🔴 ES UN CAMBIO DE COMPORTAMIENTO DELIBERADO, del 2026-08-18 (Plan 050 · Ola 1),
// y no un no-op. La premisa con la que nació la guarda —«esos jobs eran no-ops de
// todas formas»— es FALSA para dos ramas de route, y queda escrito aquí para que
// nadie lo descubra en producción:
//
//   - Receipt: handleReceipt (send.go) solo comprueba `receipt == nil`. Con
//     session_id vacío ANTES se logueaba el acuse y se llamaba a receiptSink.Record,
//     que escribe una fila por message_id. AHORA no se hace nada.
//   - Las TRES de auth (auth.go, handleUserLogin/Refresh/Logout): comprueban
//     `s.authn == nil` y `cc.hasIdentity`, NUNCA `cc.sessionID`. Con session_id
//     vacío ANTES llegaban a identity-core y respondían un UserAuthResponse (o un
//     pushAuthError). AHORA el Edge no recibe respuesta a ese frame.
//
// Se acepta el cambio porque el carril fantasma es peor —una cola y una goroutine
// bajo la llave "" que nunca recibe su jobOffline (closeStream itera `releases`,
// donde solo hay session_id no vacíos) y que solo muere en el seal del cierre—, y
// porque un frame sin session_id es una ANOMALÍA DE PROTOCOLO: el Edge lo rellena
// siempre (ADR-0008: N sesiones multiplexadas POR session_id sobre un stream). Por
// eso el aviso es Warn y no Debug: si esto aparece en un log, hay un Edge
// emitiendo frames inválidos, y eso no es ruido de diagnóstico.
//
// Auth y Receipt sin session_id NO tenían efecto útil en el otro extremo (el
// pushAuthError se correlaciona por session_id, y un acuse sin sesión no se puede
// atribuir), así que lo que se pierde es trabajo que ya no llegaba a destino. Lo
// que se gana es que ahora SE VE.
//
// Y conviene ser literal con QUÉ session_id, porque el contrato no ayuda: aquí se
// habla del campo 2 del ENVELOPE (`EdgeToCloud.session_id`, connect.go:66). Los
// tres mensajes de auth llevan ADEMÁS un `session_id` propio dentro del payload, y
// ESE sí está documentado como «puede ir vacío» (cloudlink.proto:389, :399, :410).
// El Edge de hoy no se acoge a ese permiso —estampa `__wapp_control__` en ambos
// (adapters/cloudlink/auth.go:30), justo para el operador que se loguea antes de
// emparejar ningún teléfono—, pero un Edge futuro que leyera solo el .proto podría
// creerse autorizado a vaciar el envelope y quedaría descartado aquí sin recurso.
func (s *Server) submitJob(lane *workLane, cc connCtx, kind jobKind, run func(ctx context.Context)) {
	if cc.sessionID == "" {
		s.log.Warn("carril: frame sin session_id; el trabajo no se encola (no hay sesión a la que atribuirlo)",
			"edge_id", cc.edgeID, "kind", kind.String())
		return
	}
	if err := lane.submit(cc.sessionID, kind, run); err != nil {
		s.log.Warn("carril: el trabajo no se encoló",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"kind", kind.String(), "error", err)
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
	// Evento del plano de máquina (identity Plan 003 · design.md Ola 3 §1.3): el
	// actor es el EdgeID del cert mTLS, no una persona.
	s.recordEdgeSession(ctx, cc)

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
	if err := s.registry.Push(ctx, cc.sessionID, leaseToCloud(cc.sessionID, lu)); err != nil {
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
// stream). No hace nada sin fleet, sin identidad mTLS o sin session_id.
//
// Desde T1.7 corre dentro de un job del carril, así que el ctx es el del JOB
// (base desacoplada del stream, presupuesto workBudget), no el del stream. El
// cambio va a favor: el logout se persiste aunque el Edge cuelgue justo después de
// anunciarlo.
//
// ⚠️ Corregido el 2026-08-18: ese job era un `jobHeartbeat` y desde hoy es un
// `jobLogout`, exento de coalescencia. Con el tipo anterior, un latido normal
// posterior sustituía a este trabajo en la cola y la sesión zombi ni se marcaba ni
// dejaba de renovar el lease. Ver submitHeartbeat.
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

		// Bloque del WORKER (Plan 051 · T4.3, campos 9-15). 🔴 El contrato transporta «no lo sé»
		// como el CERO del tipo, así que se traduce AQUÍ, antes de tocar el dominio:
		// worker_taskset vacío e intent_p50_ms 0 significan «este Edge no lo sabe», NUNCA
		// "disjunta" ni "0 ms" (el Edge manda los tres a su cero A PROPÓSITO cuando el parte del
		// worker lleva >90 s sin refrescarse). El mapa va TAL CUAL: sin sumar nada (INV-051.3) y
		// sin asumir las ocho claves — solo llegan las que no valen cero, y puede llegar nil.
		WorkerTaskset:         sh.GetWorkerTaskset(),
		IntentP50Ms:           nonZeroOrNil(sh.GetIntentP50Ms()),
		IntentOmittedByReason: sh.GetIntentOmittedByReason(),
		// Los cuatro contadores del despachador (T3.12) NO tienen presencia en proto3: un Edge
		// viejo y un Edge nuevo sin incidencias llegan igual (0). Se persiste ese 0 tal cual.
		// 🔴 failed_seal_dispatch y failed_seal_budget NUNCA se agregan: solo el PRIMERO implica
		// mensajes duplicados.
		StuckHeads:         ptrInt64(sh.GetStuckHeads()),
		StuckHeadPolls:     ptrInt64(sh.GetStuckHeadPolls()),
		FailedSealDispatch: ptrInt64(sh.GetFailedSealDispatch()),
		FailedSealBudget:   ptrInt64(sh.GetFailedSealBudget()),
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

// nonZeroOrNil traduce a puntero un entero del contrato cuyo CERO significa «no
// medible» y no «cero medido» (SessionHealth.intent_p50_ms, campo 10): 0 ⇒ nil,
// para que la nube no publique "0 ms" sobre un dato que el Edge no tiene.
func nonZeroOrNil(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// ptrInt64 devuelve un puntero al valor tal cual. Se usa con los contadores
// acumulados del despachador (campos 12-15), donde el contrato define 0 como «no
// ocurrió (o el Edge no lo mide)» y proto3 no ofrece presencia para separarlos.
func ptrInt64(v int64) *int64 { return &v }

// renewLease renueva el lease del Edge a partir del counter del Heartbeat y
// empuja el LeaseUpdate. No hace nada sin lease o sin identidad.
//
// Desde T1.7 es la TERCERA parte de un jobHeartbeat y hereda el ctx del job, que ya
// viene gastado por las dos escrituras anteriores. Ese ctx manda sobre el reloj
// interno del Registry (defaultSendTimeout, 10 s): si el presupuesto se agota antes,
// el Push vuelve al instante con el error de cancelación y el Edge NO recibe el
// LeaseUpdate. El fallo se loguea aquí y el carril lo grita en su Warn — un lease
// que no se pudo empujar no debe darse por renovado ante el Edge, que lo reintenta
// en el siguiente latido.
func (s *Server) renewLease(ctx context.Context, cc connCtx, heartbeatCounter int64) {
	if s.leaseMgr == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	lu, err := s.leaseMgr.Renew(ctx, cc.tenantID, cc.edgeID, heartbeatCounter)
	if err != nil {
		s.log.Error("lease: renovar", "error", err, "edge_id", cc.edgeID)
		return
	}
	if err := s.registry.Push(ctx, cc.sessionID, leaseToCloud(cc.sessionID, lu)); err != nil {
		s.log.Debug("lease: push renovación", "error", err, "session_id", cc.sessionID)
	}
}

// onStreamClosed deja de rastrear la sesión y ENCOLA su MarkOffline como ÚLTIMO job
// de esa sesión en el carril (T1.11, D-050.2).
//
// 🔴 No se ejecuta por fuera del carril, y esa es la tarea entera. Con carril, un
// SaveHealth del mismo session_id puede seguir pendiente cuando el stream cae; si el
// MarkOffline lo adelantara, el SaveHealth escribiría después y la flota mostraría
// «online» un Edge que ya se fue. Encolado, la cola FIFO de la sesión impone el orden
// —MarkOffline es lo último que se escribe— y por eso el cierre DRENA en vez de
// cancelar: un context.Cancel mataría precisamente el trabajo que hay que respetar.
//
// El contexto ya no se construye aquí: se lo da el carril, cuya base es
// context.WithoutCancel(streamCtx) acotada por workBudget. Es el mismo molde
// (desacoplado del stream ya cancelado + reloj propio) que este método montaba a
// mano con offlinePersistTimeout, y por defecto vale lo mismo: 5 s.
//
// Lo llama closeStream ANTES del seal() —ver allí por qué ese orden y no el
// inverso—, así que este submit no puede rebotar por carrera con la muerte de un
// worker. Si aun así rebotara, submitJob lo grita: no hay pérdida muda.
func (s *Server) onStreamClosed(lane *workLane, cc connCtx) {
	if !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	s.untrackSession(cc)

	if s.fleet == nil {
		return
	}
	// 🔴 DEUDA-050.1 — la carrera de la RECONEXIÓN RÁPIDA. Declarada el 2026-08-18,
	// dueño: la Ola 3 del Plan 050. NO se arregla aquí, y esto no es un olvido.
	//
	// Este MarkOffline es DIFERIDO (hasta el presupuesto de drain, 5 s por defecto, o
	// sin techo si el drenaje se abandona); el MarkOnline de onSessionRegistered
	// sigue siendo INLINE E INMEDIATO. Si el Edge reconecta rápido —cae el stream A,
	// se encola su jobOffline; el stream B registra la sesión y hace MarkOnline YA;
	// el jobOffline de A aterriza DESPUÉS— la fila queda `offline` con la sesión
	// VIVA, y nada la corrige hasta la siguiente reconexión. Antes de esta ola el
	// MarkOffline era síncrono en el defer: la ventana era de milisegundos, no de
	// segundos.
	//
	// El arreglo NO cabe en este fichero: es de fleet.Repository (un
	// `UPDATE … WHERE last_connected_at <= $epoch`, o un contador de generación por
	// sesión) para que la escritura vieja no pueda pisar a la nueva. Enunciado
	// completo, mecanismo y precio en
	// docs/plans/050-robustez-gateway-cloudlink/tasks.md (DEUDA-050.1).
	//
	// ⚠️ HOY NINGÚN TEST LO REPRODUCE: TestConnectReconexionIdempotente corre sin
	// fleet y TestMTLSFleetOnlineThenOffline nunca reconecta.
	s.submitJob(lane, cc, jobOffline, func(ctx context.Context) {
		if err := s.fleet.MarkOffline(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
			s.log.Error("fleet: marcar offline", "error", err,
				"edge_id", cc.edgeID, "session_id", cc.sessionID)
		}
	})
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
