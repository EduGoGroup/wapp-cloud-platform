// Porta internal/gateway/grpc/send.go @ c851591 (la correlación de acuses:
// deliverAck, clearAck, cancelSessionAcks, y la recepción de MessageReceipt). Trozo
// de send.go, partido por E-13.

package grpc

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// deliverAck entrega un Ack al chan pendiente correlacionado por
// acked_command_id, de forma no bloqueante, y limpia la entrada.
//
// 🔴 Es el ÚNICO escritor de los canales de s.acks (verificado en la Ola 2 del Plan 050), y el
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
// índice por sesión: en pendingAck está por qué se prefiere eso a un mapa
// paralelo. n son los envíos en vuelo del gateway entero —vida máxima ackTimeout— y
// esto corre una vez por caída de stream, en un camino frío.
func (s *Server) cancelSessionAcks(sessionID string) int {
	var cancelled []chan *cloudlinkv1.Ack // en el paquete viejo, cancelados

	s.acksMu.Lock()
	for cmdID, p := range s.acks {
		if p.sessionID != sessionID {
			continue
		}
		delete(s.acks, cmdID)
		cancelled = append(cancelled, p.ch)
	}
	s.acksMu.Unlock()

	for _, ch := range cancelled {
		close(ch)
	}

	if len(cancelled) > 0 {
		// Warn, no Info: cada canal cerrado aquí es un envío que su llamante HTTP va a
		// ver fallar, y esta es la ÚNICA línea que dice CUÁNTOS cayeron a la vez —el
		// Warn de awaitAck dice cuáles, uno por command_id—. Esa cifra es justo lo que
		// se busca cuando alguien pregunta por qué fallaron varios envíos de golpe.
		// Cuando no hay nada en vuelo NO se loguea nada: el cierre limpio de un stream
		// es el caso normal y no es noticia.
		s.log.Warn("gateway: el stream cayó con envíos esperando ack",
			"session_id", sessionID,
			"cancelados", len(cancelled),
		)
	}
	return len(cancelled)
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
