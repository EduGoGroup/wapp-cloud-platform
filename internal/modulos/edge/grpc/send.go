// Porta internal/gateway/grpc/send.go @ c851591 (los envíos y su error). El resto
// del fichero viejo nace partido por E-13: send_ack.go (la correlación de acuses) y
// send_revoke.go (la familia de la revocación).

package grpc

import (
	"context"
	"errors"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type SendError struct{}

// CommandID devuelve el command_id del comando que falló. Se consume por
// duck-typing (`interface{ CommandID() string }`) para que un llamante pueda
// loguearlo sin importar este paquete.
func (e *SendError) CommandID() string { panic(pendiente.Implementar("grpc.SendError.CommandID")) }

// SessionID devuelve la sesión a la que iba dirigido el comando.
func (e *SendError) SessionID() string { panic(pendiente.Implementar("grpc.SendError.SessionID")) }

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
func (e *SendError) StreamCaido() bool {
	panic(pendiente.Implementar("grpc.SendError.StreamCaido"))
}

// Error implementa error con el formato literal
// "gatewaygrpc: comando <command_id> a la sesión <session_id>: <causa>". NO incluye
// el destino ni el texto: un log de error no es sitio para PII ni para el contenido
// del mensaje.
func (e *SendError) Error() string { panic(pendiente.Implementar("grpc.SendError.Error")) }

// Unwrap expone la causa para errors.Is/As.
func (e *SendError) Unwrap() error { panic(pendiente.Implementar("grpc.SendError.Unwrap")) }

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
func (s *Server) SendText(_ context.Context, _, _, _ string) (*cloudlinkv1.Ack, error) {
	panic(pendiente.Implementar("grpc.Server.SendText"))
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
func (s *Server) SendMedia(_ context.Context, _, _, _, _, _, _, _ string) (*cloudlinkv1.Ack, error) {
	panic(pendiente.Implementar("grpc.Server.SendMedia"))
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
func (s *Server) Ping(_ context.Context, _ string, _ int64) error {
	panic(pendiente.Implementar("grpc.Server.Ping"))
}
