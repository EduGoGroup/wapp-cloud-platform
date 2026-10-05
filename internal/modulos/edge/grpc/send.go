// Porta internal/gateway/grpc/send.go @ c851591 (los envíos y su error). El resto
// del fichero viejo nace partido por E-13: send_ack.go (la correlación de acuses) y
// send_revoke.go (la familia de la revocación).

package grpc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

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
// imports (ver allí). Viaja SIEMPRE envuelto en *SendError, igual que los otros dos
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
// —el mismo argumento que el handler admin escribe sobre su commandIDFrom—. Un handler
// que quisiera `errors.Is(err, ErrStreamClosed)` tendría que importar este paquete y
// romperlo.
//
// 🔴 El nombre NO se traduce (E-11 no alcanza a los textos observables, y este lo
// es): lo consumen por esa interfaz anónima platform/httpapi, publicapi y
// flujos/admin, y un renombrado los apagaría sin ningún rojo.
//
// El bool que devuelve es el que separa el 504 «se cayó» del 504 «no contestó a
// tiempo»: falso NO significa que el envío fuera bien, significa que falló por otra
// cosa (plazo vencido, sesión offline al empujar). No lo uses como «hubo error».
func (e *SendError) StreamCaido() bool { return errors.Is(e.err, ErrStreamClosed) }

// Error implementa error con el formato literal
// "gatewaygrpc: comando <command_id> a la sesión <session_id>: <causa>". NO incluye
// el destino ni el texto: un log de error no es sitio para PII ni para el contenido
// del mensaje.
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
// correlacionado por command_id. El command_id se genera AQUÍ DENTRO (UUIDv4, uno
// nuevo por llamada) y viaja en el frame junto al session_id.
//
// Devuelve el Ack recibido, o un *SendError —que lleva ese command_id y envuelve la
// causa— en cada uno de estos casos (R-G11):
//   - la sesión está offline al empujar: envuelve session.ErrSessionOffline; el
//     comando NO salió;
//   - el contexto del llamante se cancela o vence: envuelve ctx.Err();
//   - se agota el plazo PROPIO del Ack (WithAckTimeout, 8 s por defecto): envuelve
//     context.DeadlineExceeded. La espera tiene reloj propio, no depende de que el
//     llamante traiga deadline — un handler HTTP no lo trae;
//   - el stream de la sesión cae con el envío en vuelo y la sesión no queda con otro
//     detrás: envuelve ErrStreamClosed, EN EL ACTO, sin agotar el plazo.
//
// Salga por donde salga, no deja ninguna entrada pendiente en la correlación de acuses.
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
// Hay un segundo motivo, independiente del llamante: sin este reloj el select
// esperaría al Ack de un Edge que ya no existe. ⚠️ El eje de ese defecto es LATENCIA,
// no memoria (Plan 050 · T1.1, ADR-0040 §Contexto): SendText y SendMedia dejan un
// defer s.clearAck(cmdID) en todos sus caminos de salida, así que la entrada de la
// correlación se borra siempre —haya llegado el Ack, haya vencido el reloj o haya
// caído el stream— y vive como mucho lo que dura el ackTimeout. No hay entradas
// huérfanas que limpiar; lo que había era un llamante HTTP esperando el plazo entero
// por un acuse que el gateway ya sabía perdido.
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
// SendText (mismos cuatro caminos de error, mismo reloj propio), así el acuse
// delivered/read del Plan 013 funciona sin cambios. El binario NO viaja por gRPC:
// va la presignedURL que el Edge descarga (GET sin credenciales) y sube a WhatsApp.
//
// El frame lleva to, caption, mime, filename y la URL tal cual. kind elige el
// MediaKind: "document" → DOCUMENT, "image" → IMAGE y cualquier otro valor (también
// el vacío) → UNSPECIFIED, y el Edge decide el fallback.
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

// Ping empuja un comando Ping con el nonce dado hacia la sesión, con su command_id
// propio, y vuelve SIN esperar el Pong. El ctx acota el empuje (Plan 050 · T1.5-bis).
// Devuelve el error del empuje tal cual —sin *SendError—: session.ErrSessionOffline
// envuelto si la sesión no está online.
//
// ⚠️ SIN LLAMANTE DE PRODUCCIÓN (verificado 2026-08-12). El keepalive real lo hace el
// transporte HTTP/2 y la vivacidad la reporta el Heartbeat; nadie de la plataforma
// llama a este método. Se CONSERVA a propósito, y no por inercia: es la costura más
// barata para empujar un frame sin esperar Ack.
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
