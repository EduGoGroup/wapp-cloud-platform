// Porta internal/gateway/grpc/connect.go @ cbc5736 (el bucle Recv del stream CloudLink, su
// cierre y la identidad mTLS del peer). Trozo de connect.go, partido por E-13.
//
// Aquí vive Connect y lo que es solo suyo: closeStream y peerIdentity. Lo que el bucle
// LLAMA está en los otros trozos: el reparto de cada frame en connect_route.go, el registro
// y el cierre de una sesión en connect_session.go, y las partes del job del latido en
// connect_heartbeat.go.
//
// Nombres (E-11), viejo → nuevo: controlVisto → controlSeen; sesionNueva → newSession;
// calientaPorRegistro → warmOnRegister (readiness.go). El paquete google.golang.org/grpc se
// importa como googlegrpc. Los textos y las claves de log son los del viejo, literales.

package grpc

import (
	"context"
	"errors"
	"io"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Connect atiende el stream bidireccional CloudLink de UN Edge: lee sus frames hasta que
// el stream termina y devuelve nil si el Edge cerró con orden (io.EOF) o, si no, el error
// de Recv tal cual. Es el método del servicio gRPC; lo registra Register.
//
// Lo que promete, y prueban connect_test.go y sus hermanos:
//
//   - IDENTIDAD. El (tenant, Edge) del stream sale SOLO del certificado mTLS del peer
//     (CN = edge_id, Organization[0] = tenant_id). Sin TLS, o con un certificado al que
//     le falte cualquiera de los dos, el stream es anónimo y degrada: sus sesiones entran
//     en el Registry y sus frames se encaminan, pero no hay flota, lease, config,
//     seguimiento ni calentamiento a nombre de nadie.
//   - REGISTRO PEREZOSO (ADR-0008). Cada session_id no vacío se registra con su PRIMER
//     frame, una sola vez por stream: entra en el Registry con el cable de este stream
//     —el MISMO candado de escritura para todas sus sesiones (R-G12)— y, con identidad,
//     se rastrea, se audita, se marca online y recibe su lease inicial y después su
//     config, antes de encaminar ese frame. Un frame sin session_id no registra nada.
//   - EL CANAL DE CONTROL NO ES UNA SESIÓN (ADR-0048). `__wapp_control__` no entra en el
//     Registry ni en el seguimiento, ni produce flota, lease o calentamiento. La primera
//     vez que un stream lo usa recibe la config de su tenant por su propio cable; las
//     respuestas de auth vuelven por ese mismo cable.
//   - DESPACHO. Cada frame se encamina bajo SU session_id (route): lo que es memoria se
//     resuelve aquí, en la goroutine del bucle Recv, y lo que toca red o base va al
//     carril de su sesión, que no muere con el stream.
//   - CALENTAMIENTO. Tras encaminar el frame que registró una sesión NUEVA, y solo ese,
//     se avisa del calentamiento de compatibilidad (warmOnRegister): después, para que lo
//     que ese frame diga sobre la capacidad de inferencia del Edge ya esté aprendido.
//   - HOOKS. OnIncoming, OnHeartbeat, OnWarmup y OnEdgeReady corren INLINE en el bucle
//     Recv: mientras uno no vuelve, no se lee el siguiente frame.
//   - CIERRE. Al terminar el stream, cada una de sus sesiones sale del Registry; si quedó
//     sin stream, sus envíos en vuelo dejan de esperar; y su MarkOffline se encola como
//     ÚLTIMO trabajo de la sesión. El carril se sella y se drena antes de volver. Una
//     sesión que ya reconectó por otro stream no se toca (R-G4).
func (s *Server) Connect(stream googlegrpc.BidiStreamingServer[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]) error {
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
	// controlSeen recuerda si este stream ya usó el canal de control, para empujarle
	// su config UNA sola vez (ver el case del control, más abajo). Mismo régimen que
	// `releases`: local al stream y mutada por un ÚNICO goroutine, el bucle Recv.
	controlSeen := false
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
		newSession := false
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
			if !controlSeen {
				controlSeen = true
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
				newSession = true
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
		if newSession {
			s.warmOnRegister(frameCC)
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
		// familia que DEUDA-050.1 (la carrera de la reconexión rápida), declarada en
		// onStreamClosed (connect_session.go) sobre este mismo cierre.
		if !s.registry.Online(sid) {
			s.cancelSessionAcks(sid)
			// TODO(F3-03 tanda 3): aquí va `s.cancelSessionInfers(sid)`, justo después de cancelSessionAcks y bajo la MISMA condición (`!s.registry.Online(sid)`): las inferencias en vuelo de una sesión que se quedó sin stream dejan de esperar ya.
		}
		cc2 := cc
		cc2.sessionID = sid
		s.onStreamClosed(lane, cc2)
	}
	lane.seal()
	lane.drain(s.workBudget)
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
