// Porta internal/gateway/grpc/connect.go @ cbc5736 (el reparto de cada frame: qué se resuelve
// en la goroutine del bucle Recv y qué se suelta al carril). Trozo de connect.go, partido
// por E-13.
//
// Aquí viven route y lo que cuelga solo de él: el job del latido (submitHeartbeat), la
// puerta única al carril (submitJob) y la apertura del entrante sellado (decodeIncoming).
// Lo que hace cada parte del job del latido está en connect_heartbeat.go.
//
// Nombres (E-11), viejo → nuevo: observaReadiness → observeReadiness (readiness.go);
// calientaPorRegistro → warmOnRegister (readiness.go). El resto ya estaba en inglés. Los
// textos y las claves de log son los del viejo, literales.

package grpc

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"
)

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
	case *cloudlinkv1.EdgeToCloud_InferenceResult:
		// 🔴 SE QUEDA INLINE, Y ES EL MISMO ARGUMENTO QUE EL ACK (ADR-0040
		// §Decisión.3). deliverInference es O(1) en memoria: un lookup en un map y un
		// envío no bloqueante a un canal con buffer. Lo caro de este frame —abrir el
		// sobre X25519 y deserializar— NO se hace aquí: lo paga el llamante en su
		// propia goroutine, que es quien está bloqueado esperando (ver el ⚠️ de
		// deliverInference). Mandarlo al carril le pondría delante la cola de la
		// sesión justo al camino que este plan viene a acortar.
		s.deliverInference(p.InferenceResult)
	case *cloudlinkv1.EdgeToCloud_Heartbeat:
		// El hook es de test/observación, no I/O: se queda inline (design.md §3).
		if s.OnHeartbeat != nil {
			s.OnHeartbeat(cc.sessionID, p.Heartbeat)
		}
		// 🔴 SE QUEDA INLINE, Y ES LA MISMA REGLA QUE ESCRIBE LA LÍNEA DE ARRIBA
		// (ADR-0040 §Decisión.3, design.md §3): leer un enum del frame y tocar un mapa
		// en memoria es O(1) y no roza ni la red ni la base, que es exactamente la
		// clase que el ADR manda resolver en la goroutine del Recv.
		//
		// Y aquí, además, el inline no es solo legítimo: es NECESARIO. El disparador
		// de compatibilidad del registro (warmOnRegister, en el bucle Connect)
		// corre justo DESPUÉS de que route retorne y pregunta por lo que este latido
		// acaba de enseñar. Mandarlo al carril lo volvería asíncrono y esa pregunta
		// pasaría a ser una carrera: el registro leería «no lo dice» de un Edge que
		// acababa de decir DOWN y lo calentaría igual — el defecto entero que T1.8-6
		// viene a quitar, reintroducido por el sitio.
		//
		// VA ANTES del submit a propósito: submitHeartbeat puede BLOQUEAR al bucle Recv
		// si la cola de la sesión llegó a su tope (contrapresión, REQ-050.4), y lo que
		// el Edge DICE sobre su capacidad de inferencia se aprende igual esté la cola
		// llena o vacía.
		s.observeReadiness(cc, p.Heartbeat)
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
// 📌 Desde el Plan 046 · T3.2 (b) son CUATRO: el saludo de la sesión recién
// emparejada (greetIfNeeded) va enganchado al final del mismo job, y por los mismos
// dos motivos —lee lo que persistSelfPn acaba de escribir, y envía por el lease que
// renewLease acaba de renovar—. El párrafo de abajo sobre el presupuesto vale igual,
// solo que repartido entre cuatro; el reparto exacto está en greeting.go.
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
		s.observeInference(cc, hb)
		s.persistHealth(ctx, cc, hb)
		s.renewLease(ctx, cc, hb.GetLeaseCounter())
		// 🔴 EL CUARTO VA AL FINAL A PROPÓSITO (Plan 046 · T3.2 (b)). El saludo de la
		// sesión recién emparejada necesita ir DESPUÉS de persistSelfPn —que es quien
		// deja el número en la fila que su consulta lee— y se pone DESPUÉS DE TODO
		// porque es el único de los cuatro que espera un Ack del Edge. Los cuatro
		// comparten UN presupuesto, no uno cada uno (ver el ⚠️ de arriba): al final
		// solo puede gastar el reloj que los otros tres no gastaron. Antes de
		// renewLease le robaría el presupuesto justo al lease del que depende para
		// poder enviar. Su docstring tiene el reparto entero.
		s.greetIfNeeded(ctx, cc)
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
//   - Receipt: handleReceipt (send_ack.go) solo comprueba `receipt == nil`. Con
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
// habla del campo 2 del ENVELOPE (`EdgeToCloud.session_id`, el que lee el bucle Recv). Los
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
	// 🔧 AQUÍ VIVÍA `msg.Intent = sp.GetIntent()` (Plan 029 · T7): el transporte del
	// intent que el clasificador del Edge sellaba dentro del SensitivePayload. Se fue
	// con el campo: T1.6-1 retiró ClassifiedIntent del contrato y esta línea dejó de
	// COMPILAR, así que su borrado no es una decisión que se tome aquí — es el
	// realineo que el proto obliga (D-044.31, la ventana de 4 s se disuelve y P1 pasa
	// a pull).
	//
	// ⚠️ EL RETIRO NO ESTÁ COMPLETO Y ESO NO ES DE ESTA TAREA. `internal/flujos/
	// runtime` (buildSignal, observeForAggregation) sigue leyendo el campo muerto y
	// hoy no compila; quien lo cierra —y quien construye el PULL que lo sustituye— es
	// T1.6-4. Se retira aquí y solo aquí porque es la línea que impedía compilar ESTE
	// paquete, no por ampliar el alcance.
	return true
}
