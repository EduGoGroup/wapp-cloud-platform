// Porta internal/publicapi/messages.go @ 115a4ba (messagesHandler, sessionBelongsToTenant,
// writeSendError, streamCaidoFrom, commandIDFrom y sus textos), los puertos MessageSender y
// SessionLister (internal/publicapi/publicapi.go @ 115a4ba, líneas 39-51) y el registro de D1
// (publicapi.go @ 115a4ba, línea 443).
//
// messages.go — EL ENVÍO DE UN TEXTO POR UNA SESIÓN DEL EDGE (mapa §2.4, D1). writeError, que en
// la cara vieja vivía en este fichero, nació en response.go (T-15).

package apipublica

import (
	"context"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MessageSender empuja un SendText hacia una sesión viva del Edge y espera su Ack. Lo satisface
// *grpc.Server del módulo edge NUEVO (internal/modulos/edge/grpc).
//
// Lo que D1 espera de un error de SendText, sin importar ese paquete (duck-typing):
//
//   - errors.Is contra los centinelas de internal/modulos/edge/session (ErrSessionOffline,
//     ErrPushTimeout, ErrPushAbandoned) y contra context.DeadlineExceeded / context.Canceled;
//   - `interface{ CommandID() string }`: el command_id del comando que falló;
//   - `interface{ StreamCaido() bool }`: el stream de la sesión cayó esperando el ack. El nombre
//     del método NO se traduce: es el contrato observable que cumple grpc.SendError.
type MessageSender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// SessionLister lista las sesiones (durables) de un tenant. Lo satisface fleet.Repository del
// módulo edge NUEVO (su método List). Se usa para el aislamiento por tenant del envío por
// session_id (INV-8): una sesión que no aparece en la lista del tenant del token NO es suya.
type SessionLister interface {
	List(ctx context.Context, tenantID string) ([]fleet.Session, error)
}

// MessagesDeps es lo que D1 necesita.
type MessagesDeps struct {
	// Sender es el gateway CloudLink (SendText).
	Sender MessageSender
	// Sessions es la flota: las sesiones del tenant, para la guarda de aislamiento.
	Sessions SessionLister
	// DBTimeout es el plazo de la consulta de la guarda de tenant, cableado desde
	// config.PublicAPIDBTimeout. <= 0 ⇒ 1,5 s (el cargador de config no normaliza: el suelo lo
	// pone el consumidor).
	DBTimeout time.Duration
	// SendBudget es el PRESUPUESTO DE LA PETICIÓN: el techo por encima de los relojes
	// secuenciales del envío (guarda, empuje, espera del ack). NO se cablea a mano: se deriva
	// con SendBudgetFrom(writeTimeout). <= 0 ⇒ sin plazo.
	SendBudget time.Duration
}

// MountMessages registra en c "POST /api/v1/messages" (D1) SIEMPRE —no tiene condición de
// montaje: con d vacío la ruta existe igual, como en la cara vieja—, con cadena W, permiso
// "messages.send" y recurso de auditoría "message" (ver Common: 401 sin token, 403 sin el
// permiso o con un token sin empresa, y EXACTAMENTE un registro de auditoría por petición que
// pasa el permiso, "success" o "failure" según el código).
//
// El cuerpo es {"session_id","to","text"}. El tenant NO viaja en él (INV-8): sale del token, y
// un tenant_id en el cuerpo se ignora. En orden:
//
//   - cuerpo que no es JSON ⇒ 400 {"error":"cuerpo JSON inválido"};
//   - session_id, to o text vacíos ⇒ 400 {"error":"session_id, to y text son requeridos"}. En
//     ninguno de los dos 400 se consulta la flota ni se envía nada;
//   - GUARDA DE TENANT: d.Sessions.List(tenant del token) acotada por d.DBTimeout (<= 0 ⇒
//     1,5 s). Si la sesión no está entre las del tenant ⇒ 404 {"error":"sesión no encontrada
//     para el tenant"} (404 y no 403: no se revela si existe en OTRO tenant) y no se envía;
//     si vence el plazo ⇒ 504 {"error":"la verificación de la sesión no respondió a tiempo: el
//     mensaje NO se envió, reintenta"}, una línea Warn "lectura a BD vencida: se responde 504"
//     con "op"="messages.guarda_tenant", "tenant_id" y "session_id", y no se envía; otro fallo
//     de List ⇒ 500 {"error":"no se pudo verificar la sesión"};
//   - d.Sender.SendText(session_id, to, text). Con Ack ⇒ 200 {"acked_command_id","ok","error"}
//     ("error" se omite si está vacío) TAMBIÉN si ok=false: el Edge recibió el comando y su
//     ejecución falló. Deja una línea Info "mensaje enviado por la API pública" con
//     "command_id", "session_id" y "ok"; si la respuesta no se pudo escribir, una línea Error
//     "no se pudo escribir la respuesta del envío" con "command_id", "session_id" y "error";
//   - un error de SendText ⇒ cuerpo {"error", "command_id"} ("command_id" se omite si el error
//     no lo trae), con el PRIMER caso que case, en este orden:
//     1. session.ErrSessionOffline ⇒ 502 "sesión offline: no hay stream vivo para el Edge" (el
//     comando NO salió);
//     2. StreamCaido() verdadero ⇒ 504 «el stream del Edge se cerró antes del ack…» (el comando
//     YA viajó: no se sabe si el mensaje salió);
//     3. session.ErrPushTimeout ⇒ 504 «el Edge dejó de leer su stream…»;
//     4. session.ErrPushAbandoned ⇒ 504 «se agotó el plazo de la petición…» (aunque envuelva
//     además un error de contexto);
//     5. context.DeadlineExceeded o context.Canceled ⇒ 504 "timeout esperando el ack del Edge";
//     6. cualquier otro ⇒ 500 "no se pudo enviar el texto".
//     Deja una línea Error "envío por la API pública fallido" con "status", "command_id",
//     "session_id" y "error"; si la respuesta no se pudo escribir, otra línea Error "no se pudo
//     escribir la respuesta de error del envío" con los mismos campos.
//
// PLAZOS. d.SendBudget acota la petición ENTERA desde después de las validaciones: la guarda
// de tenant Y el envío (el contexto que recibe SendText vence con el presupuesto). <= 0 ⇒ el
// contexto de SendText no trae plazo, y la guarda conserva el suyo (d.DBTimeout). Cancelar ese
// contexto NO cancela un envío ya en vuelo: por eso ningún texto de los 504 del envío invita a
// reintentar.
//
// CERO PII en los logs: nunca el destino ni el texto; solo command_id, session_id y tenant_id,
// que son opacos. k.Log nil ⇒ mismas respuestas, sin líneas.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common). d.Sender y d.Sessions NO se
// comprueban al montar, igual que en la cara vieja: el arranque los cablea siempre.
func MountMessages(c *Cara, k Common, d MessagesDeps) {
	panic(pendiente.Implementar("apipublica.MountMessages"))
}
