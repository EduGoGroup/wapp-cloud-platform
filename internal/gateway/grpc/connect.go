package gatewaygrpc

import (
	"context"
	"errors"
	"io"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/inferstats"
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

	// El sender de arriba viaja TAMBIÉN en el connCtx, y no solo al Registry: es lo
	// que permite contestar in-band a las peticiones que llegan por ESTE stream
	// (Plan 057 · T1.2). frameCC lo hereda por copia en cada frame.
	cc := connCtx{tenantID: tenantID, edgeID: edgeID, hasIdentity: hasIdentity, sender: sender}
	// releases mapea cada session_id registrado en ESTE stream a su release. Es
	// local al stream y lo muta un ÚNICO goroutine (el bucle Recv de abajo), por
	// lo que no necesita lock (ADR-0008: N sesiones multiplexadas por session_id
	// sobre un solo stream CloudLink por Edge).
	releases := make(map[string]func())
	// controlVisto recuerda si este stream ya usó el canal de control, para empujarle
	// su config UNA sola vez (ver el case del control, más abajo). Mismo régimen que
	// `releases`: local al stream y mutada por un ÚNICO goroutine, el bucle Recv.
	controlVisto := false
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
		sesionNueva := false
		switch {
		// ════════════════════════════════════════════════════════════════════
		// 🔴 EL CANAL DE CONTROL NO ES UNA SESIÓN (Plan 057 · Ola 2 · T2.1)
		// ════════════════════════════════════════════════════════════════════
		//
		// `__wapp_control__` es el session_id que el Edge estampa en los frames de
		// AUTH —y solo en ellos— porque el gateway exige un session_id no vacío y el
		// operador puede loguearse ANTES de emparejar ningún teléfono. Hasta el
		// 2026-09-03 se registraba como una sesión más. No lo es, y registrarlo tenía
		// TRES consecuencias, ninguna visible en un log de error:
		//
		//  1. Es una CONSTANTE IDÉNTICA en todos los Edge del planeta, y
		//     session.Registry indexa por session_id SIN TENANT con política
		//     última-gana: el segundo Edge que conectaba pisaba la entrada del primero
		//     y la respuesta del login del primero —con sus tokens— salía por el cable
		//     del segundo, aunque fuera de OTRA EMPRESA. (La Ola 1 ya lo desactivó por
		//     el otro extremo: la respuesta de auth vuelve in-band. Esto lo remata.)
		//  2. trackSession lo metía en `edgeSessions`, así que sessionsForTenant lo
		//     devolvía y el FAN-OUT DE CONFIG (PushConfig) le empujaba el catálogo de
		//     intenciones del tenant. En el Edge, handleConfigUpdate se atiende ANTES
		//     de resolver la sesión, se aplica GLOBAL y se ack-ea SIEMPRE: la config de
		//     una empresa podía APLICARSE DE VERDAD en el Edge de otra. Esto no se
		//     descartaba en el receptor, a diferencia del token.
		//  3. Por el mismo índice, inferenceSession podía elegirlo como destino: `_`
		//     (0x5F) ordena antes que cualquier UUID que empiece por `a`-`f`.
		//
		// No se registra, no se indexa y no se le emite lease (no hay teléfono ni DEK
		// detrás: el Edge lo descarta con un Warn). Lo único que necesita —que el
		// operador pueda entrar— lo da la Ola 1 sin registro alguno.
		case sessionID == cltransport.ControlSessionID:
			if !controlVisto {
				controlVisto = true
				s.onControlChannel(streamCtx, frameCC)
			}
		case sessionID != "":
			// Registro perezoso por-frame (register-on-first-frame): la primera
			// vez que aparece un session_id se registra; idempotente después.
			if _, ok := releases[sessionID]; !ok {
				releases[sessionID] = s.registry.Register(sessionID, sender)
				s.log.Info("sesión CloudLink registrada",
					"session_id", sessionID, "edge_id", edgeID, "tenant_id", tenantID)
				s.onSessionRegistered(streamCtx, frameCC)
				sesionNueva = true
			}
		}

		s.route(lane, frameCC, msg)

		// 🔴 EL CALENTAMIENTO DE COMPATIBILIDAD VA AQUÍ, DESPUÉS DE ENCAMINAR EL FRAME,
		// Y NO DENTRO DE onSessionRegistered — de donde se movió en T1.8-6.
		//
		// El problema que resuelve este orden, que no se ve leyendo onSessionRegistered:
		// EL FRAME QUE PROVOCA EL REGISTRO ES, EN EL CASO NORMAL, EL PRIMER HEARTBEAT DE
		// LA SESIÓN. Con el disparador dentro del registro, el gateway calentaba ANTES de
		// haber leído ese latido, o sea antes de saber nada de lo que el Edge dice sobre
		// su capacidad de inferencia. Un Edge que arranca sin cajero anunciaba DOWN en su
		// primerísimo latido y aun así se le mandaba un calentamiento que solo podía
		// contestar OLLAMA_DOWN. Encaminar primero (route lee el campo INLINE, ver el
		// case del Heartbeat) y preguntar después es lo que hace que ese caso sea CERO
		// calentamientos en vez de uno.
		//
		// ⚠️ SE MUEVE EL AVISO, NO EL REGISTRO. onSessionRegistered sigue corriendo ANTES
		// de route, y tiene que seguir: dentro está el MarkOnline —que crea la fila que
		// el job del propio latido va a actualizar con SaveHealth— y el lease inicial,
		// que el Edge no puede recibir después de una renovación. Mover la función
		// entera detrás de route invertiría los dos órdenes y la flota mostraría salud
		// de una sesión que aún no existe. Lo único que se retrasa es el aviso de
		// calentamiento, que no ordena nada con nadie: dispara y vuelve.
		if sesionNueva {
			s.calientaPorRegistro(frameCC)
		}
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
			// Y las inferencias en vuelo por el mismo motivo y bajo la misma condición
			// (Plan 044 · T1.6-3): su presupuesto es de decenas de segundos, así que
			// dejarlas esperando a un Edge que ya no está cuesta MÁS que en el caso del
			// ack. Ver cancelSessionInfers.
			s.cancelSessionInfers(sid)
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
		// de compatibilidad del registro (calientaPorRegistro, en el bucle Connect)
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
		s.observaReadiness(cc, p.Heartbeat)
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

// onSessionRegistered marca la sesión online en fleet, la rastrea para el
// kill-switch y empuja el lease inicial al Edge. No hace nada sin identidad mTLS.
//
// 🔴 SIGUE INLINE en la goroutine del bucle Recv, y eso es decisión explícita
// (ADR-0040 §Decisión.3): el handshake ORDENA —el Edge no puede recibir un
// LeaseUpdate de renovación antes que su lease inicial— y el carril, que es
// asíncrono, no puede garantizar ese orden frente al resto del bucle. Lo que T3.4
// le añade NO es carril: es RELOJ. Antes de esta tarea el registro corría sobre el
// ctx crudo del stream gRPC, que NO trae deadline (el Edge no lo pone), así que las
// seis o más idas y vueltas a Postgres que cuelgan de aquí podían colgar el bucle
// Recv de ese Edge indefinidamente contra una base atascada.
//
// UN reloj para todo el registro, no cuatro sueltos, porque lo que se protege es la
// unidad entera: el evento de auditoría, el MarkOnline, los TRES viajes de
// IssueInitial (TenantRevoked + Get + Upsert), el push del lease y el
// ConfigsForConnect de pushConfigsOnConnect. El presupuesto es s.workBudget
// (WAPP_GATEWAY_WORK_TIMEOUT, 5 s por defecto): el mismo que una unidad de trabajo
// del carril, y a propósito el mismo — este trabajo es del mismo tamaño y la misma
// naturaleza, solo que servido en otro sitio. NO tiene variable propia.
//
// ⚠️ El reparto es el que ya documenta jobHeartbeat: los seis viajes COMPARTEN un
// plazo, no tienen uno cada uno. El primero que tarde se come el reloj del último,
// y entonces lo que se pierde es lo de más abajo en esta función (típicamente el
// push del lease inicial o el de configs). Es una propiedad CONOCIDA y aceptada, no
// un descuido: el Edge reintenta el lease en su siguiente latido, y subir el
// presupuesto «para que quepa» sería decisión de ADR, no un ajuste.
//
// ⚠️ El padre es el ctx del STREAM y no context.WithoutCancel(streamCtx), que es lo
// contrario de lo que hace el carril (worklane.go) — y la diferencia es deliberada.
// El carril desacopla porque su trabajo importa PRECISAMENTE cuando el stream ya
// murió (persistir que un Edge se fue). Aquí es al revés: si el stream muere a mitad
// del handshake, terminar de marcar la sesión `online` y empujarle un lease a nadie
// es escribir una mentira — la misma que DEUDA-050.1 viene a evitar por el otro
// lado. Al morir el stream, el registro se rinde, y eso es correcto.
//
// Cancelar este ctx derivado NO toca al streamCtx: en Go un cancel solo se propaga
// hacia los hijos. El defer cancel() de abajo libera el timer y nada más.
//
// Cuando el plazo vence, Connect NO se cuelga: la llamada vuelve, el bucle Recv
// sigue atendiendo frames y queda el Warn de abajo diciendo qué sesión se quedó a
// medio registrar. Lo que NO hay es reintento: el Edge lo provoca reconectando.
func (s *Server) onSessionRegistered(ctx context.Context, cc connCtx) {
	if !cc.hasIdentity {
		return
	}

	regCtx, cancel := context.WithTimeout(ctx, s.workBudget)
	defer cancel()

	s.registerSession(regCtx, cc)

	// 📌 EL AVISO DE CALENTAMIENTO YA NO ESTÁ AQUÍ (T1.8-6). Estuvo en este punto
	// exacto desde T1.7-4 y se mudó al bucle Connect, DETRÁS de route, porque disparaba
	// antes de leer el primer latido de la sesión —que es el frame que suele provocar
	// este mismo registro— y por tanto antes de saber si el Edge dice que puede servir
	// inferencia. El porqué completo y qué NO se movió están en Connect y en
	// calientaPorRegistro. Se anota aquí porque quien busque el disparador leerá esta
	// función primero.

	// El molde es el de runJob (worklane.go): un trabajo que se pasa del presupuesto
	// DEJA RASTRO. Sin este aviso, un handshake que se rindió a medias —sesión sin
	// lease inicial, o sin su config— sería indistinguible de uno completo.
	//
	// ⚠️ Y se distinguen las DOS causas, porque el ctx derivado muere por dos motivos
	// muy distintos y confundirlos manda a investigar al sitio equivocado: si venció
	// el plazo, el sospechoso es Postgres y el presupuesto; si lo que se canceló fue
	// el stream —el Edge se fue a media conexión, que es corriente y no es una
	// avería—, culpar al presupuesto sería una pista falsa.
	switch err := regCtx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("handshake: el registro de la sesión no terminó dentro de su presupuesto",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"budget", s.workBudget, "error", err)
	case err != nil:
		s.log.Info("handshake: el stream se fue antes de terminar el registro de la sesión",
			"session_id", cc.sessionID, "edge_id", cc.edgeID, "error", err)
	}
}

// onControlChannel es lo ÚNICO que el gateway hace cuando un stream usa el canal de
// control, y corre UNA VEZ POR STREAM (Plan 057 · Ola 2 · T2.2).
//
// Es la mitad que SÍ se conserva de onSessionRegistered para este id. La otra mitad
// —registrar en el Registry, indexar en edgeSessions, marcar flota, emitir lease— se
// retiró en T2.1 y el porqué está en el `case` de Connect.
//
// 🔴 QUÉ PASARÍA SI ESTA FUNCIÓN NO EXISTIERA, que es la razón de que exista. El push
// de config al conectar (ADR-0021) vivía DENTRO de registerSession. Para un Edge
// recién arrancado SIN NINGÚN TELÉFONO EMPAREJADO, el frame de auth era el único que
// provocaba registro: ese Edge recibía su catálogo de intenciones por el canal de
// control y por ningún otro sitio. Quitar el registro sin reponer esto lo dejaría
// arrancando con el catálogo vacío, sin un solo error en ningún log. Ver
// pushConfigsInBand.
//
// El reloj es el mismo molde que onSessionRegistered —ctx del STREAM acotado por
// workBudget— y por el mismo motivo: si el stream muere a mitad, empujarle config a
// nadie no sirve de nada, así que este trabajo se rinde con él. Corre INLINE en la
// goroutine del bucle Recv, igual que el registro de una sesión normal, y por eso el
// presupuesto no es opcional: lo que hay dentro toca la base (ConfigsForConnect) y la
// red (el envío), y sin techo retendría el bucle que lee los frames del Edge.
func (s *Server) onControlChannel(ctx context.Context, cc connCtx) {
	if !cc.hasIdentity {
		return
	}
	s.log.Info("canal de control en uso (no se registra como sesión)",
		"edge_id", cc.edgeID, "tenant_id", cc.tenantID)

	ctlCtx, cancel := context.WithTimeout(ctx, s.workBudget)
	defer cancel()
	s.pushConfigsInBand(ctlCtx, cc)

	// Mismo aviso, y misma distinción de causas, que en onSessionRegistered: un plazo
	// vencido acusa a Postgres y al presupuesto; un stream que se fue es corriente y
	// culpar al presupuesto mandaría a investigar al sitio equivocado.
	switch err := ctlCtx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("canal de control: la config inicial no salió dentro de su presupuesto",
			"edge_id", cc.edgeID, "budget", s.workBudget, "error", err)
	case err != nil:
		s.log.Info("canal de control: el stream se fue antes de empujar la config inicial",
			"edge_id", cc.edgeID, "error", err)
	}
}

// registerSession es el cuerpo del registro de sesión, ya acotado por el reloj que
// le pone onSessionRegistered. Está separado solo para que ese reloj y su aviso se
// lean de un vistazo; el orden de sus pasos no cambió con T3.4.
func (s *Server) registerSession(ctx context.Context, cc connCtx) {
	s.trackSession(cc)
	// Evento del plano de máquina (identity Plan 003 · design.md Ola 3 §1.3): el
	// actor es el EdgeID del cert mTLS, no una persona.
	s.recordEdgeSession(ctx, cc)

	// 🔴 EL CANAL DE CONTROL NO ES FLOTA (MP-11). Todo lo de arriba SÍ le corresponde —se
	// registra en el Registry (sin eso el login del operador no tiene por dónde volver), se
	// trackea y se audita—, pero NO se persiste como sesión: no hay ningún teléfono detrás.
	//
	// Sin esta guarda nace una fila en `fleet_sessions` que el CLIENTE ve en su dashboard como
	// si fuera un teléfono más: con `self_pn` vacío la plantilla cae al `session_id`, así que
	// lee «__wapp_control__» en la columna del número, con su selector de perfil —marcarlo
	// pasivo movería la `version` del mapa de filters del tenant ENTERO— y seleccionable como
	// destino de envío.
	//
	// El criterio no es de gusto: lo fija la razón de ser de la flota (funcionalidad 31,
	// ADR-0023), que nació de una consola diciendo `online` con el socket muerto hacía 31
	// minutos — «estar registrado no es estar sano». Esta fila NUNCA podrá reportar un
	// SessionHealth, porque no tiene socket. Es justo lo que esa funcionalidad existe para
	// desenmascarar.
	//
	// ⚠️ Consecuencia ACEPTADA (MP-11 §5): un Edge con el operador logueado y CERO teléfonos
	// emparejados deja de aparecer en `fleet.List`, así que `RevokeTenant` no cosecha su
	// edge_id y no le empuja la revocación. La revocación SIGUE SIENDO EFECTIVA —
	// MarkTenantRevoked escribe en la BD antes del fan-out— y ese Edge no envía ni recibe
	// nada; se pierde solo el aviso inmediato.
	if s.fleet != nil && cc.sessionID != cltransport.ControlSessionID {
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

// observeInference recoge el bloque de inferencia del latido para que /metrics lo
// publique (Plan 044 · Ola 1.7 · T1.7-9). Es memoria pura: no toca la base, no acota
// nada y por eso NO recibe ctx.
//
// 🔴 VA APARTE DE persistHealth Y NO DENTRO, aunque lea el mismo SessionHealth. Aquel
// se rinde sin `fleet` —no hay dónde durabilizar—, y esa guarda es correcta para él y
// equivocada para esto: un despliegue sin repositorio de flota seguiría sirviendo
// /metrics, y colgar la recogida de ahí la haría desaparecer sin un solo error. Son
// dos destinos con dos condiciones, no uno con dos pasos.
//
// ⚠️ Corre dentro de un job COALESCIBLE del carril, así que un latido intermedio puede
// descartarse (D-050.4). Da igual: lo que llega son ACUMULADOS del Edge, no deltas, y
// el siguiente latido trae el mismo total o uno mayor. Si algún día esto pasara a
// contar diferencias, la coalescencia dejaría de ser inocua — y ese es el motivo por
// el que aquí no se resta nada.
func (s *Server) observeInference(cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if s.inferStats == nil || !cc.hasIdentity {
		return
	}
	sh := hb.GetSessionHealth()
	if sh == nil {
		return // Edge viejo: no reporta salud.
	}
	// 🔴 LA CLAVE ES EL EDGE, NO LA SESIÓN. Los contadores los lleva el PROCESO del
	// Edge (un cajero, un Ollama) pero viajan en el latido de CADA sesión suya: un Edge
	// con tres teléfonos manda tres latidos con LOS MISMOS totales. Indexar por sesión
	// multiplicaría por tres las inferencias del mundo con una serie creíble y falsa.
	// Es la misma lección que el calentamiento de T1.7-4, y por el mismo motivo
	// (ADR-0008: N sesiones sobre un proceso).
	s.inferStats.Observa(
		inferstats.Clave{TenantID: cc.tenantID, EdgeID: cc.edgeID},
		inferstats.Parte{
			PorRegimen:        sh.GetInferenceByRegime(),
			PorClase:          sh.GetInferenceByClass(),
			OmitidasPorMotivo: sh.GetIntentOmittedByReason(),
			// 🔴 PRESENCIA NATIVA, NO EL CERO. Los dos son sub-mensajes: ausente
			// significa «este Edge no mide esa fase», y convertirlo en 0 lo volvería
			// «cero observaciones», que es una afirmación distinta y publicable.
			MuestrasPrefill:    muestrasDe(sh.GetInferencePrefill()),
			MuestrasGeneracion: muestrasDe(sh.GetInferenceGeneration()),
		})
}

// muestrasDe extrae el `n` de un InferenceLatency conservando su ausencia. El
// sub-mensaje lleva el cuantil y su `n` JUNTOS a propósito —aquí un cuantil sin saber
// cuántas muestras lo sostienen ya fabricó una conclusión falsa—, y lo que se publica
// es el `n`: ver descMuestrasLatencia para por qué el cuantil no sube a /metrics.
func muestrasDe(l *cloudlinkv1.InferenceLatency) *int64 {
	if l == nil {
		return nil
	}
	n := l.GetSamples()
	return &n
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
	// 🔴 DEUDA-050.1 — la carrera de la RECONEXIÓN RÁPIDA. Declarada el 2026-08-18 y
	// CERRADA el mismo día (Plan 050 · Ola 3) con la mitigación que hay debajo. Ya no
	// es una deuda abierta; es una carrera con red, y con el alcance de esa red
	// escrito.
	//
	// La carrera: este MarkOffline es DIFERIDO (hasta el presupuesto de drain, 5 s por
	// defecto, o sin techo si el drenaje se abandona), mientras que el MarkOnline de
	// onSessionRegistered es INLINE E INMEDIATO. Si el Edge reconecta rápido —cae el
	// stream A y se encola su jobOffline; el stream B registra la sesión y hace
	// MarkOnline YA; el jobOffline de A aterriza DESPUÉS— la fila quedaba `offline`
	// con la sesión VIVA, y nada la corregía hasta la siguiente reconexión. Antes de
	// esta ola el MarkOffline era síncrono en el defer: la ventana era de
	// milisegundos, no de segundos.
	//
	// La mitigación: la pregunta «¿sigue caída esta sesión?» se hace DENTRO del
	// closure, o sea AL EJECUTAR el job, no al encolarlo. Que es lo único que importa,
	// porque es entre esos dos instantes donde cabe la reconexión. session.Registry
	// es última-gana con comparación de identidad (registry.go, Register/release), de
	// modo que «hay entrada para este session_id» tras el release() del stream que
	// cae significa exactamente «alguien reconectó». Es el MISMO mecanismo con el que
	// la Ola 2 decide si cancelar los acuses en vuelo (closeStream, arriba).
	//
	// ⚠️ Lo que esta red NO cubre, y no se oculta:
	//
	//   - Registry.Online indexa por session_id GLOBAL, sin tenant. Dos tenants con el
	//     mismo session_id se confundirían aquí (hoy no ocurre: el session_id lo genera
	//     el Edge y es un UUID).
	//   - No cubre un REINICIO del proceso entre la caída y el job: el Registry vive en
	//     memoria y el job muere con él, así que no hay escritura que pisar — pero
	//     tampoco queda quien marque offline, y la fila se queda `online` hasta la
	//     siguiente reconexión. Eso es el estado previo, no una regresión de esta
	//     mitigación.
	//
	// Y por qué NO se hizo en SQL, que es lo que proponía el plan (un
	// `UPDATE … WHERE last_connected_at <= $epoch`): esa condición compara DOS RELOJES
	// DISTINTOS —MarkOnline escribe con el now() de Postgres (repository_postgres.go)
	// y el $epoch saldría del reloj de Go—, así que un desfase de reloj podía dejar una
	// sesión MUERTA marcada `online` para siempre. Peor que la deuda que arregla.
	// Descartada por Jhoan el 2026-08-18 junto con la variante de cambiar la firma de
	// fleet.Repository.
	//
	// Lo reproducen TestReconexionRapidaNoDejaLaSesionOffline y su gemelo
	// TestCaidaSinReconexionSigueMarcandoOffline (connect_reconexion_internal_test.go),
	// que hay que leer en pareja: el segundo es el que impide que la mitigación degenere
	// en «dejar de marcar offline».
	s.submitJob(lane, cc, jobOffline, func(ctx context.Context) {
		if s.registry.Online(cc.sessionID) {
			s.log.Info("fleet: no se marca offline; la sesión ya reconectó por otro stream",
				"edge_id", cc.edgeID, "session_id", cc.sessionID)
			return
		}
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

// untrackSession quita la sesión del conjunto vivo de su Edge, y con la ÚLTIMA de
// ellas olvida también lo que ese Edge decía sobre su capacidad de inferencia.
//
// 🔴 EL OLVIDO DEL READINESS VA BAJO LA MISMA CONDICIÓN QUE EL DEL EDGE, no suelto
// (Plan 044 · Ola 1.8 · T1.8-6). La trampa, que no se ve desde aquí: onStreamClosed
// —el único llamante— se invoca UNA VEZ POR SESIÓN del stream (closeStream itera
// `releases`), y `edgeReadiness` está indexado POR EDGE. Un delete incondicional
// borraría lo aprendido de un Edge que todavía tiene otras sesiones vivas en el mismo
// stream: la siguiente vez que una de ellas latiera READY se leería como flanco y
// dispararía un calentamiento inventado, y mientras tanto el disparador de
// compatibilidad volvería a creer que ese Edge no dice nada. Colgarlo del `len(set)
// == 0` que ya decide que este Edge se quedó sin sesiones es lo que hace que las dos
// estructuras nazcan y mueran juntas.
//
// El `return` de arriba (set == nil) tampoco deja nada colgando: ese caso es un
// untrack de un Edge que YA se vació, y quien lo vació borró las dos entradas.
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
		delete(s.edgeReadiness, k)
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
