// Nace con D-F3-13 (hallazgo 81 de F3): no porta nada del gateway viejo, que no lo tiene.

package grpc

// in_flight.go — CUÁNTO ESPERA RESPUESTA DEL EDGE AHORA MISMO (D-F3-13).
//
// El stream bidi Connect de un Edge no termina nunca por sí solo, así que un
// GracefulStop del servidor CloudLink con un Edge conectado agota SIEMPRE su plazo
// (10 s medidos en los dos binarios) y acaba en el Stop() forzado. Lo único útil de
// esa espera es dejar terminar lo que está EN VUELO; el resto es parada en vacío. Para
// poder cortarla, el arranque necesita una sola cifra que solo este paquete conoce, y
// aquí no se decide nada: se PUBLICA lo que las dos correlaciones ya saben.

// InFlight dice cuántas peticiones de la nube esperan AHORA MISMO la respuesta de un
// Edge: los envíos (SendText, SendMedia) que esperan su Ack más las inferencias (Infer)
// que esperan su InferenceResult. Es la suma de las entradas de s.acks y de s.infers.
//
// Una petición cuenta desde que se registra en su correlación —ANTES de empujar el
// frame al stream— hasta que sale por cualquiera de sus caminos: llegó la respuesta,
// venció su reloj propio, el llamante se rindió o cayó el stream de su sesión. En
// reposo es 0, y vuelve a 0 por todos esos caminos: las entradas no se fugan (ver
// awaitAck).
//
// 🔴 QUÉ NO CUENTA, y quien lo use para decidir una parada tiene que saberlo:
//
//   - el trabajo del CARRIL (worklane.go): un Incoming, un login en banda o un receipt
//     que se están procesando NO son peticiones esperando al Edge. Su cierre ordenado
//     es de closeStream (seal + drain), no de esta cifra;
//   - los frames ENTRANTES en tránsito o sin leer, y los empujes SIN respuesta
//     correlacionada (Ping, PushConfig, RequestDiagnostics, el kill-switch): salen por
//     el stream y nadie espera nada de vuelta aquí;
//   - lo que un llamante vaya a pedir DESPUÉS: una cadena «inferencia → envío» es
//     invisible en el hueco entre sus dos pasos.
//
// ⚠️ ES UNA FOTO, NO UNA RESERVA (mismo aviso que PlazaDe). Las dos correlaciones se
// leen una detrás de otra, cada una bajo su mutex y nunca las dos a la vez: no hay un
// instante único al que corresponda la suma, y un envío puede nacer justo después de
// leer 0. Un 0 significa «no vi nada», no «no habrá nada».
//
// Solo lee: no retira entradas, no cierra canales y no loguea. Es segura en
// concurrencia con todo lo demás del Server y devuelve en el acto (dos len bajo dos
// mutex que nunca envuelven una llamada).
func (s *Server) InFlight() int {
	s.acksMu.Lock()
	sends := len(s.acks)
	s.acksMu.Unlock()

	s.infersMu.Lock()
	inferences := len(s.infers)
	s.infersMu.Unlock()

	return sends + inferences
}
