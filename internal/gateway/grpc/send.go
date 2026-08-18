package gatewaygrpc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// ErrStreamClosed indica que el stream CloudLink de la sesión cayó mientras un envío
// esperaba su Ack, y que la sesión NO quedó con otro stream vivo detrás. Tiene
// centinela propio porque es un fallo DISTINTO de context.DeadlineExceeded, aunque
// hasta el Plan 050 · Ola 2 el llamante viera los dos como lo mismo: el timeout dice
// «esperamos el plazo entero y el Edge no contestó»; este dice «dejamos de esperar en
// el acto porque ya no hay nadie que pueda contestar». Confundirlos le costaba al
// llamante HTTP los 8 s completos de una espera que se sabía perdida desde el primer
// instante — que es, literalmente, el defecto que esta ola viene a quitar.
//
// Está EXPORTADO para que el propio paquete y sus tests lo distingan con errors.Is,
// pero NO es el camino por el que lo consumen los handlers HTTP: esos usan el
// duck-typing de SendError.StreamCaido(), porque el Gateway no debe aparecer en sus
// imports (ver allí, y el mismo argumento escrito sobre commandIDFrom en
// httpapi/admin.go). Viaja SIEMPRE envuelto en *SendError, igual que los otros dos
// caminos de salida, así que el command_id —el único hilo que correlaciona lo que la
// nube intentó con el outbox del Edge y con los acuses del Plan 013— no se pierde.
//
// 🔴 Lo que este error NO dice: que el mensaje no saliera. El Push YA tuvo éxito —el
// comando viajó al Edge y pudo haber llegado a WhatsApp antes de que el stream
// muriera—; lo único seguro es que el acuse no va a llegar por aquí. De ahí que la
// respuesta HTTP sea **504 y no 502** (decisión de Jhoan, Ola 2): el 502 de este repo
// significa lo contrario, «no salió», y es el que le corresponde a ErrSessionOffline,
// donde el fallo ocurre AL EMPUJAR. Por eso el texto de abajo habla de dejar de
// esperar y nunca de no poder enviar: redactarlo como un fallo de envío convertiría
// una respuesta honesta («no sabemos si le llegó») en una afirmación falsa.
var ErrStreamClosed = errors.New("gatewaygrpc: el stream de la sesión se cerró antes de que llegara el ack")

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
			if pushErr := s.registry.Push(ctx, sid, leaseToCloud(sid, lu)); pushErr != nil {
				s.log.Debug("revoke: push a sesión", "session_id", sid, "error", pushErr)
			}
		}(sid)
	}
	wg.Wait()
	return nil
}

// RevokeTenant dispara el kill-switch COMERCIAL de un tenant completo
// (D-055.2, Plan 055 · T3.3): persiste el corte (tenants.revoked_at, vía
// lease.Manager.RevokeTenant) y empuja el LeaseUpdate(Revoked) a las sesiones
// VIVAS de TODAS las instalaciones YA CONOCIDAS de ese tenant. "Conocidas" se
// resuelve con fleet.Repository.List (persistente, sobrevive reinicios y
// cubre instalaciones offline en este momento pero registradas alguna vez);
// las instalaciones NUEVAS que ese tenant abra después no necesitan push --
// nacen revocadas solas porque Manager.wasRevoked consulta tenants.revoked_at
// en cada IssueInitial (T3.2).
//
// NO marca leases.revoked de cada instalación (a diferencia de RevokeLease):
// eso dejaría sin retorno a un RestoreTenant posterior (D-055.2, ver el
// comentario de lease.Manager.RevokeTenant). El blob firmado que sí viaja por
// sesión lo produce SignTenantRevocation, que firma sin persistir por-edge.
func (s *Server) RevokeTenant(ctx context.Context, tenantID string) error {
	if s.leaseMgr == nil {
		return errors.New("gatewaygrpc: lease no configurado")
	}
	if err := s.leaseMgr.RevokeTenant(ctx, tenantID); err != nil {
		return err
	}

	edgeIDs := map[string]struct{}{}
	if s.fleet != nil {
		sessions, err := s.fleet.List(ctx, tenantID)
		if err != nil {
			return fmt.Errorf("gatewaygrpc: listar instalaciones del tenant: %w", err)
		}
		for _, sess := range sessions {
			edgeIDs[sess.EdgeID] = struct{}{}
		}
	}

	// Push CONCURRENTE por instalación e independiente entre instalaciones: el
	// mismo argumento que RevokeLease (arriba) -- ninguna sesión bloqueada debe
	// retrasar la notificación al resto, y aquí hay potencialmente muchas más
	// sesiones en juego (todas las instalaciones del tenant, no solo una).
	var wg sync.WaitGroup
	for edgeID := range edgeIDs {
		lu, signErr := s.leaseMgr.SignTenantRevocation(edgeID, tenantID)
		if signErr != nil {
			s.log.Error("revoke tenant: firmar revocación de instalación",
				"edge_id", edgeID, "error", signErr)
			continue
		}
		for _, sid := range s.sessionsForEdge(tenantID, edgeID) {
			wg.Add(1)
			go func(sid string, lu *cloudlinkv1.LeaseUpdate) {
				defer wg.Done()
				if pushErr := s.registry.Push(ctx, sid, leaseToCloud(sid, lu)); pushErr != nil {
					s.log.Debug("revoke tenant: push a sesión", "session_id", sid, "error", pushErr)
				}
			}(sid, lu)
		}
	}
	wg.Wait()
	return nil
}

// RestoreTenant reactiva un tenant previamente revocado (Plan 055 · T3.3,
// reverso de RevokeTenant): delega en lease.Manager.RestoreTenant
// (tenants.revoked_at = NULL). NO re-emite leases vigentes ni empuja ningún
// push por sí mismo -- las instalaciones ya conectadas seguirán viendo su
// LeaseUpdate(Revoked) previo hasta su siguiente Heartbeat/Renew, momento en
// el que Manager.wasRevoked ya verá el tenant activo (mismo comportamiento
// que la reconexión tras un RevokeLease individual: la revocación previa no
// se retracta con un push, se deja de reafirmar en la siguiente emisión).
func (s *Server) RestoreTenant(ctx context.Context, tenantID string) error {
	if s.leaseMgr == nil {
		return errors.New("gatewaygrpc: lease no configurado")
	}
	return s.leaseMgr.RestoreTenant(ctx, tenantID)
}

// deliverAck entrega un Ack al chan pendiente correlacionado por
// acked_command_id, de forma no bloqueante, y limpia la entrada.
//
// 🔴 Es el ÚNICO escritor de los canales de s.acks (verificado en la Ola 2), y el
// orden de sus dos mitades es lo que sostiene la invariante de cierre: RETIRA la
// entrada del mapa bajo acksMu y solo DESPUÉS escribe en el canal. Gracias a eso, un
// cierre de stream concurrente que logre retirar la misma entrada sabe con certeza
// que aquí no va a escribir nadie, y puede cerrar el canal sin riesgo. Invertir el
// orden —escribir y luego borrar— rompería esa certeza en silencio. Ver el enunciado
// completo en cancelSessionAcks.
func (s *Server) deliverAck(ack *cloudlinkv1.Ack) {
	id := ack.GetAckedCommandId()

	s.acksMu.Lock()
	p, ok := s.acks[id]
	if ok {
		delete(s.acks, id)
	}
	s.acksMu.Unlock()

	if !ok {
		s.log.Debug("ack sin comando pendiente", "acked_command_id", id)
		return
	}

	select {
	case p.ch <- ack:
	default:
	}
}

// handleReceipt procesa un MessageReceipt (acuse de entrega/lectura) recibido del
// Edge (Plan 013 §10.F/§10.G). Correlaciona por command_id con el SendText
// original y lo entrega al receiptSink.
//
// Desde el Plan 050 · T1.8 NO corre en el bucle Recv sino en el carril de su sesión
// (jobReceipt): el sink de producción escribe una fila por message_id, y eso es
// exactamente el trabajo que no debe frenar al resto del stream. El ctx es el del
// job, con presupuesto propio. Un receipt encolado NUNCA se coalesce ni se descarta:
// llega tarde, pero llega (ADR-0037 §Decisión.7 — un acuse es estado idempotente
// sobre un mensaje nuestro, así que diferirlo es legítimo y perderlo no).
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

// SendError es el fallo de un comando que YA tenía command_id asignado, con ese
// identificador a mano del llamante. Envuelve la causa (session.ErrSessionOffline,
// context.DeadlineExceeded…), así que `errors.Is` sigue diciendo lo mismo que antes
// de que este tipo existiera: los `writeSendError` de los handlers no cambian.
//
// Existe porque el command_id se genera DENTRO del envío y hasta ahora se perdía
// justo cuando más falta hace. Con el Ack no hay problema —lo trae él—, pero un
// envío que falla no devuelve Ack, y entonces el operador se queda sin el único
// hilo que correlaciona lo que la nube intentó con el outbox del Edge y con los
// acuses del Plan 013. La distinción que eso permite no es cosmética: si el fallo
// fue empujar (sesión offline) el mensaje NO salió, pero si fue esperar el ack, el
// comando ya viajó y el cliente pudo haberlo recibido — y saber cuál de las dos
// cosas pasó es exactamente lo que se busca cuando alguien pregunta «¿le llegó?».
type SendError struct {
	commandID string
	sessionID string
	err       error
}

// CommandID devuelve el command_id del comando que falló. Se consume por
// duck-typing (`interface{ CommandID() string }`) para que un llamante pueda
// loguearlo sin importar este paquete.
func (e *SendError) CommandID() string { return e.commandID }

// SessionID devuelve la sesión a la que iba dirigido el comando.
func (e *SendError) SessionID() string { return e.sessionID }

// StreamCaido indica que el stream CloudLink de la sesión se cerró mientras se
// esperaba el ack, y que la sesión no quedó con otro stream detrás (ErrStreamClosed).
// Se consume por duck-typing (`interface{ StreamCaido() bool }`), exactamente igual
// que CommandID(): el contrato es la interfaz anónima, no un tipo compartido, y ese es
// el desacople que permite al Gateway no aparecer en los imports de los handlers HTTP
// —el mismo argumento que httpapi/admin.go escribe sobre commandIDFrom—. Un handler
// que quisiera `errors.Is(err, gatewaygrpc.ErrStreamClosed)` tendría que importar este
// paquete y romperlo.
//
// El bool que devuelve es el que separa el 504 «se cayó» del 504 «no contestó a
// tiempo»: falso NO significa que el envío fuera bien, significa que falló por otra
// cosa (plazo vencido, sesión offline al empujar). No lo uses como «hubo error».
func (e *SendError) StreamCaido() bool { return errors.Is(e.err, ErrStreamClosed) }

// Error implementa error. NO incluye el destino ni el texto: un log de error no es
// sitio para PII ni para el contenido del mensaje.
func (e *SendError) Error() string {
	return fmt.Sprintf("gatewaygrpc: comando %s a la sesión %s: %v", e.commandID, e.sessionID, e.err)
}

// Unwrap expone la causa para errors.Is/As.
func (e *SendError) Unwrap() error { return e.err }

// sendErr envuelve la causa de un envío fallido con su command_id. Un err nil
// devuelve nil: así el llamante puede envolver sin ramificar.
func sendErr(cmdID, sessionID string, err error) error {
	if err == nil {
		return nil
	}
	return &SendError{commandID: cmdID, sessionID: sessionID, err: err}
}

// SendText empuja un comando SendText hacia la sesión dada y espera su Ack,
// correlacionado por command_id. Devuelve el Ack recibido o un *SendError si la
// sesión está offline, si el contexto del llamante se cancela, o si se agota
// s.ackTimeout esperando el acuse (ver awaitAck: la espera tiene reloj PROPIO, no
// depende de que el llamante traiga deadline — un handler HTTP no lo trae).
//
// ⚠️ Un Ack devuelto NO significa "entregado": el Edge puede acusar con Ok=false y
// su motivo en Error. Quien necesite saber si el mensaje salió de verdad tiene que
// mirar ack.GetOk(), no solo el error.
func (s *Server) SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	cmdID, err := newCommandID()
	if err != nil {
		return nil, err
	}

	ch := make(chan *cloudlinkv1.Ack, 1)
	s.acksMu.Lock()
	s.acks[cmdID] = pendingAck{ch: ch, sessionID: sessionID}
	s.acksMu.Unlock()
	defer s.clearAck(cmdID)

	msg := &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_SendText{
			SendText: &cloudlinkv1.SendText{To: to, Text: text},
		},
	}
	if pushErr := s.registry.Push(ctx, sessionID, msg); pushErr != nil {
		return nil, sendErr(cmdID, sessionID, pushErr)
	}

	return s.awaitAck(ctx, ch, cmdID, sessionID)
}

// awaitAck espera el Ack correlacionado CON RELOJ PROPIO (s.ackTimeout), no solo
// contra el contexto del llamante. La distinción es la que costó el incidente del
// 2026-08-06: el ctx de un handler HTTP no trae deadline —el WriteTimeout del
// http.Server no interrumpe al handler ni cancela su contexto, solo hace fallar el
// Write posterior—, así que esperar únicamente por ctx.Done() significaba esperar
// indefinidamente. Un POST /api/v1/messages colgó 88s contra un Edge saturado y el
// servidor cerró la conexión sin responder ni loguear nada.
//
// Hay un segundo motivo, independiente del llamante: si el stream del Edge muere
// sin acusar, NADA limpia la entrada de s.acks (deliverAck solo corre al recibir el
// Ack), de modo que sin este reloj el select se quedaría colgado para siempre
// aunque el Edge ya no exista.
//
// ⚠️ CORREGIDO el 2026-08-18 (Plan 050 · T1.1, ADR-0040 §Contexto). El párrafo de
// arriba se conserva literal —es el que dimensionó el follow-up F2 de la pieza 03—
// pero su "NADA limpia la entrada de s.acks" es FALSO, y lo era ya cuando se
// escribió, a pocas líneas de la prueba en contra: SendText y SendMedia dejan un
// defer s.clearAck(cmdID) en todos sus caminos de salida, así que la entrada se borra
// siempre, haya llegado el Ack o haya vencido el reloj. No hay entradas huérfanas y
// no hay fuga de memoria; la entrada vive como mucho lo que dura el ackTimeout.
// Lo que sí es cierto del párrafo: sin este reloj el select esperaría al Ack de un
// Edge que ya no existe. El defecto real es de LATENCIA —el gateway sabe que el
// stream cayó y aun así hace esperar el plazo entero al llamante HTTP—, no de
// memoria. Confundir los dos ejes es lo que le dio a la Ola 2 un alcance de
// "limpieza/TTL de huérfanas" que no tiene a quién limpiar.
//
// El error viaja envuelto en *SendError, así que el llamante conserva el command_id
// —el único hilo que correlaciona lo que la nube intentó con el outbox del Edge y
// con los acuses del Plan 013— y errors.Is(err, context.DeadlineExceeded) sigue
// diciendo la verdad.
//
// Desde el Plan 050 · Ola 2 · T2.3 hay TRES salidas, no dos, y la nueva es la que da
// sentido a la ola: el canal CERRADO. Cuando el stream de la sesión cae y nadie lo
// reemplaza, closeStream cancela sus acuses en vuelo (cancelSessionAcks) cerrando
// estos canales, y esta espera termina en el acto con ErrStreamClosed en vez de
// consumir el ackTimeout entero contra un Edge que ya no está. El llamante distingue
// las dos cosas con errors.Is, que es justo lo que el mapeo HTTP necesita: «no
// contestó a tiempo» y «se cayó» merecen respuestas distintas.
func (s *Server) awaitAck(ctx context.Context, ch <-chan *cloudlinkv1.Ack, cmdID, sessionID string) (*cloudlinkv1.Ack, error) {
	ctx, cancel := context.WithTimeout(ctx, s.ackTimeout)
	defer cancel()

	select {
	case ack, ok := <-ch:
		if ok {
			return ack, nil
		}
		// Canal cerrado = el stream murió con este envío en vuelo. Se loguea a nivel de
		// COMANDO —igual que el timeout, y por el mismo motivo— porque el command_id es
		// lo único que permite después averiguar si el mensaje llegó a salir: el Warn
		// agregado de cancelSessionAcks dice CUÁNTOS cayeron de golpe, este dice CUÁLES.
		s.log.Warn("gateway: el ack se canceló porque el stream de la sesión cayó",
			"command_id", cmdID,
			"session_id", sessionID,
		)
		return nil, sendErr(cmdID, sessionID, ErrStreamClosed)
	case <-ctx.Done():
		// El comando YA viajó al Edge: esto NO dice que el mensaje no saliera, dice
		// que no sabemos si salió. De ahí que el command_id sea obligatorio aquí.
		s.log.Warn("gateway: se agotó la espera del ack del Edge",
			"command_id", cmdID,
			"session_id", sessionID,
			"ack_timeout", s.ackTimeout.String(),
			"error", ctx.Err(),
		)
		return nil, sendErr(cmdID, sessionID, ctx.Err())
	}
}

// SendMedia empuja un comando SendMedia (adjunto por URL prefirmada) hacia la
// sesión y espera su Ack, correlacionado por command_id — idéntico patrón a
// SendText (mismo awaitAck, mismo reloj propio), así el acuse delivered/read del
// Plan 013 funciona sin cambios. El
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
	s.acks[cmdID] = pendingAck{ch: ch, sessionID: sessionID}
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
	if pushErr := s.registry.Push(ctx, sessionID, msg); pushErr != nil {
		return nil, sendErr(cmdID, sessionID, pushErr)
	}

	return s.awaitAck(ctx, ch, cmdID, sessionID)
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
//
// ⚠️ SIN LLAMANTE DE PRODUCCIÓN (verificado 2026-08-12). El keepalive real lo hace el
// transporte HTTP/2 y la vivacidad la reporta el Heartbeat; nadie de la plataforma
// llama a este método. Se CONSERVA a propósito, y no por inercia: es la costura más
// barata para empujar un frame sin esperar Ack, y sobre ella se apoya el test de
// fan-out concurrente del registry (server_test.go, 20 goroutines contra dos sesiones
// vivas mientras el Edge manda heartbeats). Borrarlo costaría esa cobertura y
// obligaría a reescribir el test sobre SendText, que sí espera Ack y traería
// flakiness. Si un día se retira, hay que llevarse el test o portarlo antes.
//
// El ctx SÍ se usa desde el Plan 050 · T1.5-bis: acota el Push (antes se descartaba
// con `_` porque no había a quién dárselo).
func (s *Server) Ping(ctx context.Context, sessionID string, nonce int64) error {
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
	return s.registry.Push(ctx, sessionID, msg)
}

// clearAck elimina la entrada de ack pendiente si aún existe (p.ej. tras un
// timeout sin respuesta del Edge).
//
// NO cierra el canal, y no es un olvido: quien sale por aquí es el propio llamante de
// SendText/SendMedia, que ya dejó de leerlo. Cerrar aquí solo añadiría una segunda
// mano capaz de cerrar el mismo canal, que es exactamente lo que la invariante de
// cancelSessionAcks evita.
func (s *Server) clearAck(cmdID string) {
	s.acksMu.Lock()
	delete(s.acks, cmdID)
	s.acksMu.Unlock()
}

// cancelSessionAcks cancela DE GOLPE todos los envíos en vuelo de una sesión: retira
// sus entradas de s.acks y cierra sus canales, con lo que el awaitAck de cada uno
// despierta al instante con ErrStreamClosed en lugar de agotar el ackTimeout entero
// esperando a un Edge que ya no está. Devuelve cuántos canceló (Plan 050 · Ola 2 · T2.2).
//
// 🔴 LA INVARIANTE QUE HACE SEGURO CERRAR: solo cierra el canal quien logra RETIRAR su
// entrada de s.acks bajo acksMu. Ojo, porque el enunciado fácil —«delete y close bajo
// el mismo mutex»— es necesario pero NO es lo que protege: lo que protege es la
// EXCLUSIVIDAD del retiro. deliverAck es el único que escribe en estos canales, y saca
// su entrada del mapa bajo acksMu ANTES de escribir; así que si la sacó él, aquí ya no
// la encontramos, y si la sacamos nosotros, él encuentra el hueco y se limita a loguear.
// Los dos no pueden tener nunca el mismo canal. Por eso cerrar FUERA del lock —después
// de haber retirado dentro— no puede producir ni un envío sobre canal cerrado ni un
// doble close, y a cambio no se tiene el mutex tomado mientras se cierran n canales.
//
// El barrido recorre TODO el mapa porque la entrada trae su sessionID dentro y no hay
// índice por sesión: en pendingAck (types.go) está por qué se prefiere eso a un mapa
// paralelo. n son los envíos en vuelo del gateway entero —vida máxima ackTimeout— y
// esto corre una vez por caída de stream, en un camino frío.
func (s *Server) cancelSessionAcks(sessionID string) int {
	var cancelados []chan *cloudlinkv1.Ack

	s.acksMu.Lock()
	for cmdID, p := range s.acks {
		if p.sessionID != sessionID {
			continue
		}
		delete(s.acks, cmdID)
		cancelados = append(cancelados, p.ch)
	}
	s.acksMu.Unlock()

	for _, ch := range cancelados {
		close(ch)
	}

	if len(cancelados) > 0 {
		// Warn, no Info: cada canal cerrado aquí es un envío que su llamante HTTP va a
		// ver fallar, y esta es la ÚNICA línea que dice CUÁNTOS cayeron a la vez —el
		// Warn de awaitAck dice cuáles, uno por command_id—. Esa cifra es justo lo que
		// se busca cuando alguien pregunta por qué fallaron varios envíos de golpe.
		// Cuando no hay nada en vuelo NO se loguea nada: el cierre limpio de un stream
		// es el caso normal y no es noticia.
		s.log.Warn("gateway: el stream cayó con envíos esperando ack",
			"session_id", sessionID,
			"cancelados", len(cancelados),
		)
	}
	return len(cancelados)
}

// leaseToCloud envuelve un LeaseUpdate en un CloudToEdge dirigido a la sesión
// dada. No lleva command_id: es un push del servidor, no un comando con Ack.
func leaseToCloud(sessionID string, lu *cloudlinkv1.LeaseUpdate) *cloudlinkv1.CloudToEdge {
	return &cloudlinkv1.CloudToEdge{
		SessionId: sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_LeaseUpdate{LeaseUpdate: lu},
	}
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
