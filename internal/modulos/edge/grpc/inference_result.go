// Porta internal/gateway/grpc/inference.go @ ec236b3 (la espera y la lectura del
// resultado: awaitInference, readInference, openInference, el motivo del frame, y la
// correlación de inferencias en vuelo —deliverInference, clearInfer,
// cancelSessionInfers—). Trozo de inference.go, partido por E-13.

package grpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"
)

// awaitInference espera el InferenceResult con RELOJ PROPIO, separado del ctx del
// llamante (ver el ⚠️ de Infer). Tres salidas:
//
//   - el resultado llega ⇒ se abre y se devuelve (o su error nombrado);
//   - el canal se CIERRA ⇒ el stream murió con la inferencia en vuelo, y no hay
//     nadie que pueda contestar: edge_offline en el acto, sin consumir el plazo
//     entero (misma lección que ErrStreamClosed en awaitAck);
//   - vence NUESTRO presupuesto ⇒ timeout. Que el Edge no haya mandado siquiera su
//     propio INFERENCE_ERROR_TIMEOUT (que llegaría antes, por el margen) no cambia
//     el motivo: la inferencia no respondió dentro del plazo, que es lo que
//     `timeout` significa para el dueño.
//   - el LLAMANTE se rinde ⇒ ErrInferenceAbandoned, SIN motivo: su reloj no dice
//     nada sobre la salud de la vía.
func (s *Server) awaitInference(ctx context.Context, ch <-chan *cloudlinkv1.InferenceResult,
	cmdID, sessionID string, timeout time.Duration,
) (string, error) {
	// Una sola cuenta para el temporizador y para el rastro: así no pueden divergir.
	budget := inferTimeout(timeout) + s.inferGrace
	timer := time.NewTimer(budget)
	defer timer.Stop()

	select {
	case res, ok := <-ch:
		if !ok {
			s.log.Warn("inferencia: cancelada porque el stream de la sesión cayó",
				"command_id", cmdID, "session_id", sessionID)
			return "", inferErr(cmdID, sessionID, ReasonEdgeOffline, ErrStreamClosed)
		}
		return s.readInference(res, cmdID, sessionID)
	case <-ctx.Done():
		return "", fmt.Errorf("%w: inferencia %s: %w", ErrInferenceAbandoned, cmdID, ctx.Err())
	case <-timer.C:
		s.log.Warn("inferencia: se agotó el presupuesto del Cloud sin respuesta del Edge",
			"command_id", cmdID, "session_id", sessionID,
			"budget", budget.String())
		return "", inferErr(cmdID, sessionID, ReasonTimeout, context.DeadlineExceeded)
	}
}

// readInference desdobla las dos ramas del oneof del resultado: el error nombrado
// (en claro, fuera del sobre) o la salida sellada.
//
// El error va en claro Y ESO ES DELIBERADO en el contrato: no lleva PII —es un
// vocabulario cerrado— y el Cloud necesita poder decidir su degradación aunque el
// sellado sea justamente lo que falló.
func (s *Server) readInference(res *cloudlinkv1.InferenceResult, cmdID, sessionID string) (string, error) {
	if res == nil {
		return "", ErrInferenceNoOutput
	}
	if e := res.GetError(); e != cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED {
		return "", inferErr(cmdID, sessionID, reasonOfFrame(e), errors.New(e.String()))
	}
	enc := res.GetEncOutput()
	if len(enc) == 0 {
		return "", ErrInferenceNoOutput
	}
	return s.openInference(enc, cmdID, sessionID)
}

// openInference abre el sobre X25519 de la salida y saca el raw_json.
//
// El molde es el de decodeIncoming (connect_route.go) y la diferencia es el desenlace: allí
// un sellado corrupto DESCARTA el mensaje sin tumbar el stream, porque el mensaje era
// de un cliente y el stream sirve a muchos más; aquí se devuelve error al llamante,
// que está esperándolo, y el stream ni se entera.
func (s *Server) openInference(enc []byte, cmdID, sessionID string) (string, error) {
	if len(s.cloudEncPriv) == 0 {
		s.log.Error("inferencia: salida sellada pero la nube no tiene clave de cifrado",
			"command_id", cmdID, "session_id", sessionID)
		return "", ErrInferenceNoEncryptionKey
	}
	raw, err := envelope.OpenWith(s.cloudEncPriv, enc)
	if err != nil {
		s.log.Error("inferencia: no se pudo abrir la salida sellada",
			"command_id", cmdID, "session_id", sessionID, "error", err)
		return "", fmt.Errorf("%w: %w", ErrInferenceSealedUnreadable, err)
	}
	var out cloudlinkv1.InferenceOutput
	if err := proto.Unmarshal(raw, &out); err != nil {
		s.log.Error("inferencia: la salida sellada abrió pero no deserializa",
			"command_id", cmdID, "session_id", sessionID, "error", err)
		return "", fmt.Errorf("%w: %w", ErrInferenceSealedUnreadable, err)
	}
	// Observabilidad del sellado en tránsito, con el mismo criterio que el ingreso:
	// se registra el TAMAÑO, nunca el contenido. La salida del modelo puede llevar
	// texto literal del cliente reflejado.
	s.log.Info("inferencia: salida sellada abierta",
		"command_id", cmdID, "session_id", sessionID,
		"enc_output_bytes", len(enc), "raw_json_len", len(out.GetRawJson()))
	return out.GetRawJson(), nil
}

// reasonOfFrame (en el paquete viejo, motivoDeFrame) traduce el enum del proto al
// vocabulario de motivos. Es la ÚNICA traducción de la inferencia, y es 1:1 salvo
// UNSPECIFIED —que no viaja nunca por el cable (cuando no hay error, la rama del oneof
// es enc_output) y por eso readInference lo usa como centinela de «no hay error»—.
//
// Un valor DESCONOCIDO —un Edge más nuevo que esta nube— cae a ollama_down y no a un
// motivo inventado: el dueño acaba mirando su proveedor local, que es el sitio
// correcto para todos los errores que este frame sabe nombrar.
func reasonOfFrame(e cloudlinkv1.InferenceError) string {
	switch e {
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_BREAKER_OPEN:
		return ReasonBreakerOpen
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT:
		return ReasonTimeout
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID:
		return ReasonLeaseInvalid
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_EDGE_SIN_CAPACIDAD:
		return ReasonEdgeSinCapacidad
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN,
		cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED:
		return ReasonOllamaDown
	default:
		return ReasonOllamaDown
	}
}

// deliverInference entrega un InferenceResult a la inferencia pendiente
// correlacionada por command_id, de forma no bloqueante, y limpia la entrada.
//
// 🔴 MISMA INVARIANTE DE CIERRE QUE deliverAck, y por la misma razón: RETIRA la
// entrada del mapa bajo infersMu y solo DESPUÉS escribe en el canal, de modo que un
// cierre de stream concurrente que logre retirar la misma entrada sabe con certeza
// que aquí no va a escribir nadie y puede cerrar el canal sin riesgo. El enunciado
// completo está en cancelSessionAcks (send_ack.go) y no se repite.
//
// ⚠️ NO ABRE EL SOBRE. Corre en el bucle Recv y ahí solo cabe lo que es O(1) en
// memoria (ADR-0040 §Decisión.3): un lookup y un envío no bloqueante, exactamente lo
// mismo que hace el Ack. El descifrado X25519 y el Unmarshal los paga el LLAMANTE en
// su propia goroutine (openInference), que es quien está esperando y a quien le
// corresponde el coste.
func (s *Server) deliverInference(res *cloudlinkv1.InferenceResult) {
	id := res.GetCommandId()

	s.infersMu.Lock()
	p, ok := s.infers[id]
	if ok {
		delete(s.infers, id)
	}
	s.infersMu.Unlock()

	if !ok {
		// Huérfano: llegó tarde, duplicado, o su llamante ya se rindió. Se ignora con
		// log y NO tumba el stream — mismo criterio que el bundle de diagnóstico.
		s.log.Debug("inferencia sin petición pendiente", "command_id", id)
		return
	}

	select {
	case p.ch <- res:
	default:
	}
}

// clearInfer elimina la entrada pendiente si aún existe. NO cierra el canal, por el
// mismo motivo que clearAck: quien sale por aquí es el propio llamante de Infer, que
// ya dejó de leerlo, y una segunda mano capaz de cerrar el canal es justo lo que la
// invariante evita.
func (s *Server) clearInfer(cmdID string) {
	s.infersMu.Lock()
	delete(s.infers, cmdID)
	s.infersMu.Unlock()
}

// cancelSessionInfers cancela de golpe las inferencias en vuelo de una sesión: retira
// sus entradas y cierra sus canales, con lo que cada awaitInference despierta al
// instante con edge_offline en vez de agotar su presupuesto —que aquí es de DECENAS
// DE SEGUNDOS, no de ocho— esperando a un Edge que ya no está. Devuelve cuántas
// canceló.
//
// Es el gemelo exacto de cancelSessionAcks, invariante incluida, y existe por la
// misma lección medida del Plan 050 · Ola 2: el gateway sabía que el stream había
// caído y aun así hacía esperar el plazo entero al llamante.
func (s *Server) cancelSessionInfers(sessionID string) int {
	var cancelled []chan *cloudlinkv1.InferenceResult // en el paquete viejo, cancelados

	s.infersMu.Lock()
	for cmdID, p := range s.infers {
		if p.sessionID != sessionID {
			continue
		}
		delete(s.infers, cmdID)
		cancelled = append(cancelled, p.ch)
	}
	s.infersMu.Unlock()

	for _, ch := range cancelled {
		close(ch)
	}

	if len(cancelled) > 0 {
		s.log.Warn("gateway: el stream cayó con inferencias en vuelo",
			"session_id", sessionID, "cancelados", len(cancelled))
	}
	return len(cancelled)
}
