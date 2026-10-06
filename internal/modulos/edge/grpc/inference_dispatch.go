// Porta internal/gateway/grpc/inference.go @ ec236b3 (el despacho: Infer, la elección
// del stream por el que sale —candidato vivo, o el mejor del tenant que no haya dicho
// que no puede— y el frame). Trozo de inference.go, partido por E-13.

package grpc

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Infer pide una inferencia al Edge del tenant y devuelve el JSON CRUDO tal cual lo
// produjo el modelo (Plan 044 · Ola 1.6 · T1.6-3, REQ-34).
//
// Devuelve el texto SIN interpretar: no lo parsea, no lo valida y no comprueba que
// sea JSON. El contrato del proto es explícito en que si el modelo devolvió algo que
// no es JSON, eso exactamente es lo que debe llegar arriba; quien extrae y valida es
// el caller (llm.ExtractJSON y los Parse... del módulo compartido).
//
// # Por qué stream sale (R3.4.d, ADR-0048 regla 3)
//
//  1. Por el candidato, si tiene stream vivo en esta réplica: TargetSessionID si lo
//     hay, y si no OriginSessionID. Este paso NO mira la readiness del Edge.
//  2. Si no, por una sesión REAL del tenant (nunca el canal de control, T-5; nunca la
//     de otro tenant): se descartan los Edge que dijeron DOWN, se prefieren los que
//     dijeron READY sobre los que no dicen nada —que siguen siendo elegibles, T-6— y
//     dentro del grupo gana la primera en orden alfabético, siempre la misma.
//  3. Si nadie puede, NO se envía nada: *InferError con motivo edge_offline,
//     envolviendo session.ErrSessionOffline, sin command_id ni sesión.
//
// # El frame
//
// Sale por session.Registry.Push —o sea, por session.BoundedSend, con sus dos relojes
// (T-7)—. El envelope lleva un command_id nuevo (UUID v4, distinto en cada llamada) y
// el session_id del stream elegido; el payload repite el command_id y lleva como
// session_id el OriginSessionID tal cual (vacío si no lo hay): TargetSessionID no
// viaja. El prompt, el formato, la clase y la marca de calentamiento van verbatim; la
// temperatura, SIEMPRE con presencia explícita (también 0.0); timeout_ms es el Timeout
// en milisegundos, o 30 000 si no es positivo; max_output_tokens solo se pone si es
// positivo.
//
// # El error
//
//   - *InferError, con Motivo() del vocabulario cerrado ⇒ la vía se degradó y el
//     dueño debe enterarse (REQ-38): edge_offline (nadie elegible; la sesión elegida
//     ya no tiene stream; el stream cae con la inferencia en vuelo, con causa
//     ErrStreamClosed), timeout (el Edge no lee su stream, session.ErrPushTimeout; o
//     vence el presupuesto del Cloud, Timeout + DefaultInferGrace, con causa
//     context.DeadlineExceeded), o el motivo que nombre el Edge en su resultado.
//   - ErrInferenceAbandoned (junto al error del ctx) ⇒ el LLAMANTE se rindió, durante
//     el empuje o durante la espera. SIN motivo: no se avisa a nadie.
//   - ErrInferenceNoEncryptionKey / ErrInferenceSealedUnreadable /
//     ErrInferenceNoOutput ⇒ fallo de la nube o del protocolo. SIN motivo (ver el
//     bloque de errores de inference.go).
//
// Salga por donde salga, Infer no deja su entrada en la correlación de inferencias en
// vuelo: la entrada vive desde ANTES del empuje —para que un resultado inmediato la
// encuentre— hasta que Infer vuelve, y lleva la sesión del stream elegido, que es por
// la que la caída de ese stream la encuentra.
//
// ⚠️ DOS RELOJES, DISTINGUIBLES A PROPÓSITO, igual que en Registry.Push: el del
// llamante y el presupuesto propio (Timeout + inferGrace). Un select que los mezclara
// en un solo ctx no podría decir cuál venció, y de esa distinción depende si se avisa
// al dueño o no.
func (s *Server) Infer(_ context.Context, _ string, _ InferRequest) (string, error) {
	panic(pendiente.Implementar("grpc.Server.Infer"))
}
