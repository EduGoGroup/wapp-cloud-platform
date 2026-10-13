//go:build integracion

package procesos

import (
	"fmt"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"
)

// La inferencia que sirve el Edge de prueba: registrar la petición, el gate de lease con su gracia,
// consultar el guion y contestar con la salida sellada o con el error nombrado.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// alRecibirInferencia registra la petición (con una copia), le aplica el gate de lease y la
// contesta con un InferenceResult: si el Edge puede operar, la salida del guion sellada con la
// pública de la nube o el error nombrado; si no, INFERENCE_ERROR_LEASE_INVALID sin consultar el
// guion (también a un calentamiento), que es lo que contesta el Edge real
// (internal/adapters/cloudlink/inferencia.go, atender: `c.responderError(c2e,
// app.ErrInferenciaLeaseInvalido)`). La respuesta bloqueada tiene la misma forma que cualquier
// otra del doble: un InferenceResult correlacionado por command_id y NINGÚN Ack. La petición
// bloqueada queda registrada igual (la nube la pidió). El gate va ANTES de la plaza única: una
// petición que espera su gracia no retiene el guion. Las inferencias se atienden de una en una.
func (e *edge) alRecibirInferencia(cmd *cloudlinkv1.CloudToEdge, req *cloudlinkv1.InferenceRequest) {
	if copia, ok := proto.Clone(req).(*cloudlinkv1.InferenceRequest); ok {
		e.mu.Lock()
		e.inferencias = append(e.inferencias, copia)
		e.mu.Unlock()
	}
	id := req.GetCommandId()
	if id == "" {
		id = cmd.GetCommandId()
	}
	var res *cloudlinkv1.InferenceResult
	if e.inferenceBlockedByLease() {
		res = edgeErrorInferencia(id, cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID)
	} else {
		e.inferMu.Lock()
		res = e.resultadoDe(id, req)
		e.inferMu.Unlock()
	}
	e.emitirOAnotar(&cloudlinkv1.EdgeToCloud{
		SessionId: e.sesionDe(cmd),
		Payload:   &cloudlinkv1.EdgeToCloud_InferenceResult{InferenceResult: res},
	})
}

// inferenceBlockedByLease aplica a una inferencia el gate de lease del Edge real (ADR-0007: servir
// inferencia es operar). Devuelve true si NO se puede servir —y quien llama contesta
// INFERENCE_ERROR_LEASE_INVALID— y false si el Edge puede operar. Es la regla de
// carrilInferencia.leaseVigente de wapp-edge-agent (internal/adapters/cloudlink/inferencia.go), que
// NO es la de los envíos (blockedByLease):
//
//   - Alcance DAEMON. Allí basta con que UNA sesión cualquiera tenga su lease vigente
//     (algunaSesionOperable), porque el session_id de un inference_request viene normalmente vacío.
//     El doble tiene una sola sesión con su Validator, así que la pregunta acaba siendo la misma que
//     la de los envíos, puedeOperar(); y como esa sesión existe siempre, la rama «sin ninguna sesión
//     registrada se sirve» del Edge real aquí no ocurre.
//   - Con GRACIA. Si el Edge no puede operar no rechaza en el acto: vuelve a mirar cada
//     edgeInferenceLeasePoll hasta agotar la gracia (inferenceLeaseGrace; por defecto
//     edgeInferenceLeaseGrace) y solo entonces bloquea. Si el lease se vuelve vigente antes, sirve.
//     Con el lease vigente desde el principio no espera nada.
//   - Si el enlace se cierra durante la espera deja de esperar y bloquea (en el Edge real, el
//     contexto del carril cancelado), para que cerrar no tarde lo que dure la gracia.
//
// Sin su modo sombra, igual que blockedByLease. Un bloqueo no es un error del núcleo: no se anota en
// Errores, y el doble no lleva cuenta de los bloqueos.
func (e *edge) inferenceBlockedByLease() bool {
	grace := e.inferenceLeaseGrace
	if grace <= 0 {
		grace = edgeInferenceLeaseGrace
	}
	start := time.Now()
	for !e.puedeOperar() {
		if time.Since(start) >= grace || e.estaCerrando() {
			return true
		}
		time.Sleep(edgeInferenceLeasePoll)
	}
	return false
}

// resultadoDe decide qué contesta el Edge a una inferencia: un calentamiento, una salida vacía
// (sin consultar el guion); sin guion, OLLAMA_DOWN; con guion, lo que devuelva.
func (e *edge) resultadoDe(id string, req *cloudlinkv1.InferenceRequest) *cloudlinkv1.InferenceResult {
	switch {
	case req.GetWarmup():
		return e.salidaSellada(id, edgeSalidaCalentamiento)
	case e.Inferir == nil:
		return edgeErrorInferencia(id, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
	}
	crudo, fallo := e.Inferir(req)
	if fallo != nil {
		return edgeErrorInferencia(id, *fallo)
	}
	return e.salidaSellada(id, crudo)
}

// salidaSellada arma el InferenceResult con la salida: un InferenceOutput{raw_json} marshalado y
// sellado con la pública de la nube. Si no se puede sellar anota el error y contesta OLLAMA_DOWN,
// para no dejar a la nube esperando.
func (e *edge) salidaSellada(id, crudo string) *cloudlinkv1.InferenceResult {
	plano, err := proto.Marshal(&cloudlinkv1.InferenceOutput{RawJson: crudo})
	if err == nil {
		var sellado []byte
		if sellado, err = envelope.SealFor(e.CloudEncPub, plano); err == nil {
			return &cloudlinkv1.InferenceResult{
				CommandId: id,
				Result:    &cloudlinkv1.InferenceResult_EncOutput{EncOutput: sellado},
			}
		}
	}
	e.anotarError(fmt.Errorf("sellar la salida de la inferencia %s: %w", id, err))
	return edgeErrorInferencia(id, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
}

// edgeErrorInferencia arma un InferenceResult con error nombrado. UNSPECIFIED no viaja nunca por
// el cable (en el contrato significa «no hay error»), así que se manda como OLLAMA_DOWN.
func edgeErrorInferencia(id string, codigo cloudlinkv1.InferenceError) *cloudlinkv1.InferenceResult {
	if codigo == cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED {
		codigo = cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN
	}
	return &cloudlinkv1.InferenceResult{
		CommandId: id,
		Result:    &cloudlinkv1.InferenceResult_Error{Error: codigo},
	}
}
