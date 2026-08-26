package gatewaygrpc

import (
	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
)

// ════════════════════════════════════════════════════════════════════════════
// EL CLOUD CALIENTA CUANDO EL EDGE DICE QUE PUEDE (Plan 044 · Ola 1.8 · T1.8-6)
// ════════════════════════════════════════════════════════════════════════════
//
// Cierra DEUDA-044.7.
//
// # Qué había antes, y por qué era una deuda
//
// El calentamiento de la caché de prefijo (T1.7-4) se disparaba AL REGISTRAR una
// sesión, a ciegas. Si el cajero del Edge no estaba levantado, el Edge contestaba
// OLLAMA_DOWN y NADIE REINTENTABA: ese Edge se quedaba frío hasta la siguiente
// reconexión o el siguiente ConfigUpdate, y el primer cliente que escribiera pagaba
// el prefill entero (~50 s medidos en UAT) con el teléfono en la mano.
//
// La pieza que faltaba no era un reintento por reloj —Jhoan lo rechaza y con razón:
// un temporizador que pregunta «¿ya?» cada N segundos es sincronizar por reloj lo que
// tiene un evento perfectamente bueno— sino un EVENTO que dijera «ya puedo». Ese
// evento existe desde el contrato v0.17.0: `Heartbeat.inference_readiness` (campo 6).
//
// # Qué hace este fichero
//
// Convierte al gateway en CONSUMIDOR del latido:
//
//  1. `observaReadiness` — inline en la goroutine del Recv (route) — aprende lo que
//     cada Edge DICE y dispara UN calentamiento en la TRANSICIÓN A READY.
//  2. `calientaPorRegistro` — el disparador viejo, degradado a COMPATIBILIDAD y
//     movido detrás de `route` (ver Connect) — solo actúa mientras el Edge no diga
//     nada.
//
// # Sin cooldown y sin registro durable, a propósito
//
// Repetir un calentamiento sobre una caché YA caliente cuesta el prefill caliente
// (0,07–0,55 s medidos), no los ~50 s: solo el primero de cada prefijo es caro. Un
// cooldown compraría ese medio segundo a cambio de estado que caduca solo, y el
// primero que se equivocara al calibrarlo dejaría de calentar sin dar error.
//
// ⚠️ Y NO HAY REGISTRO DURABLE NI BARRENDERO NUEVO, que es una decisión y no un
// olvido (D-044.43): del calentamiento no cuelga nadie esperando —no hay respuesta
// que devolver ni cliente bloqueado—, la reconexión repone el estado sola porque el
// latido lo trae en TODOS sus frames, y el caso «vivo pero atascado» ya lo cortan
// `DefaultWarmTimeout` (110 s) y el cerrojo `calEnVuelo` del pool. Persistirlo
// añadiría una tabla, su poda y su desincronización a cambio de nada.

// observaReadiness aprende del latido lo que ESTE EDGE DICE sobre su capacidad de
// servir inferencia y, si eso es una TRANSICIÓN A READY, dispara el calentamiento.
//
// Es memoria pura: un enum del frame y un mapa. No toca la base, no acota nada y por
// eso NO recibe ctx — mismo molde que observeInference, su vecino en el mismo case.
//
// 🔴 LA TRANSICIÓN, NO EL ESTADO. El campo viaja en TODOS los latidos (es estado, no
// evento), así que calentar «cuando llega READY» sería calentar en cada cadencia del
// Edge: decenas de veces por hora contra la plaza única de su Ollama. Lo que se
// dispara es el FLANCO —de UNSPECIFIED o DOWN a READY—, que es lo que de verdad
// significa «este Edge acaba de poder».
//
// ⚠️ NO SE EXCLUYE AQUÍ EL CANAL DE CONTROL, y no es un descuido. `__wapp_control__`
// es el session_id que el Edge estampa en los frames de AUTH y SOLO en ellos
// (wapp-edge-agent, internal/adapters/cloudlink/auth.go:50 y ss.); los latidos salen
// del adaptador de sesiones con el session_id real del teléfono. Una guarda aquí
// sería una guarda sobre un camino que nadie recorre, o sea deuda con buena letra: el
// sitio donde ese id SÍ llega y SÍ hay que excluirlo es calientaPorRegistro, porque
// el registro perezoso sí se dispara con un frame de auth.
//
// La guarda que sí hace falta es la de identidad y session_id: sin mTLS no hay
// (tenant, edge) que indexar —todos los streams anónimos colisionarían en la misma
// clave vacía— y sin session_id no hay por dónde emitir el calentamiento ni quién
// limpie la entrada después (la limpieza cuelga de untrackSession, que solo existe
// para sesiones registradas). Es el mismo motivo por el que submitJob rechaza los
// frames sin session_id, y aquí además evita una entrada que nadie borraría.
func (s *Server) observaReadiness(cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	// GetInferenceReadiness es nil-safe y devuelve el cero (UNSPECIFIED) tanto si el
	// Edge no manda el campo como si el propio Heartbeat viniera nil.
	if !s.anotaReadiness(cc, hb.GetInferenceReadiness()) {
		return
	}
	s.log.Info("calentamiento: el Edge acaba de decir que puede servir inferencia",
		"tenant_id", cc.tenantID, "edge_id", cc.edgeID, "session_id", cc.sessionID)
	if s.OnWarmup != nil {
		// El `kind` va VACÍO por lo mismo que en el handshake: no se acaba de publicar
		// ninguna config concreta, es que este Edge acaba de poder servir. Un kind no vacío
		// que no fuera `intents` haría que el consumidor lo filtrara (intakeahead.Warm) y
		// el calentamiento no saldría.
		s.OnWarmup(cc.tenantID, cc.edgeID, cc.sessionID, "")
	}
	// 🔴 EL SEGUNDO CONSUMIDOR DEL MISMO FLANCO (T2.7, D-044.43): la cadena de lote se
	// REANUDA POR EVENTO. Va DESPUÉS del calentamiento a propósito —no es orden
	// cosmético—: las dos cosas acaban pidiéndole trabajo al MISMO Ollama, y el
	// calentamiento es el barato (0,07–0,55 s con la caché ya puesta) frente a los
	// 22–32 s de una llamada de lote. Disparar primero el lote dejaría el prefill
	// caliente detrás de media hora de presupuestos, que es justo lo que la Ola 1.7
	// existió para evitar.
	//
	// El log de arriba se emite AUNQUE los dos hooks sean nil: es la señal de que el
	// flanco ocurrió, y colgarlo del `!= nil` lo dejaría mudo justo en el despliegue
	// donde todavía no hay consumidor cableado (§5.2·bis del plan: no cuelgues la
	// señal del desenlace feliz).
	if s.OnEdgeReady != nil {
		s.OnEdgeReady(cc.tenantID, cc.edgeID)
	}
}

// anotaReadiness guarda la disponibilidad que el Edge acaba de declarar y devuelve
// true SOLO si eso es una transición a READY desde otra cosa.
//
// 🔴 EL CERO NO SE ANOTA, Y ESE ES EL CORAZÓN DE LA TAREA.
// INFERENCE_READINESS_UNSPECIFIED significa «este Edge no lo dice» —un Edge viejo que
// no envía el campo, o uno que todavía no lo sabe—, NUNCA «no puede». De ahí las dos
// mitades de esta guarda:
//
//   - No se lee como DOWN. Leerlo así dejaría de calentar a TODA la flota que aún no
//     publica el campo, sin producir un solo error: la forma más cara de fallar.
//   - No se ESCRIBE. Un silencio no borra lo aprendido. Si se anotara, un Edge que
//     dijo READY y luego callara pasaría a «no lo dice», y el siguiente READY se leería
//     como flanco: un calentamiento inventado por un latido que no cambió nada. Peor:
//     el disparador de compatibilidad volvería a creer que este Edge no dice nada y
//     calentaría también, que es justo lo que ya no debe pasar.
//
// Corre bajo `trackMu` —el candado de edgeSessions, ver el porqué en el campo
// edgeReadiness— y NO llama a nadie con el candado tomado: devuelve el veredicto y es
// el llamante quien invoca OnWarmup ya fuera. Es la misma disciplina de
// unaSesionPorEdge (config_push.go).
func (s *Server) anotaReadiness(cc connCtx, r cloudlinkv1.InferenceReadiness) bool {
	if r == cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED {
		return false
	}
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	k := edgeKey{tenantID: cc.tenantID, edgeID: cc.edgeID}
	anterior := s.edgeReadiness[k]
	s.edgeReadiness[k] = r
	return r == cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY &&
		anterior != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY
}

// readinessDelEdge devuelve lo último que este Edge dijo, o el cero si nunca dijo
// nada. La ausencia de entrada y el cero son LA MISMA respuesta —«no lo dice»— y por
// eso no se distingue con el `ok` del mapa: distinguirlas invitaría a tratar una de
// las dos como un veredicto.
func (s *Server) readinessDelEdge(cc connCtx) cloudlinkv1.InferenceReadiness {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	return s.edgeReadiness[edgeKey{tenantID: cc.tenantID, edgeID: cc.edgeID}]
}

// calientaPorRegistro es EL DISPARADOR VIEJO, CONSERVADO SOLO COMO COMPATIBILIDAD.
//
// Avisa de que la caché de prefijo del Edge recién conectado está fría, igual que
// hacía onSessionRegistered desde T1.7-4, pero ahora con DOS diferencias:
//
//   - Corre DESPUÉS de encaminar el frame que provocó el registro (ver Connect), de
//     modo que si ese frame era el primer latido de la sesión, lo que dijera ya está
//     aprendido cuando se llega aquí.
//   - Solo actúa si el Edge NO HA DICHO NADA. Si dijo READY, el flanco ya disparó su
//     calentamiento y repetirlo sería un segundo aviso por el mismo hecho; si dijo
//     DOWN, calentar es mandar un trabajo que solo puede volver como OLLAMA_DOWN, que
//     es exactamente la deuda que esta tarea cierra.
//
// 🔴 CONDICIÓN DE RETIRADA — CUÁNDO SE BORRA ESTA FUNCIÓN. El día que TODA la flota
// reporte `inference_readiness` en su latido, esto deja de tener consumidor y debe
// irse entera, junto con su llamada en Connect. La señal de que ese día llegó no es
// una fecha ni una versión mínima del Edge: es que ningún Edge vivo llegue aquí con
// el cero. Se mide sin desplegar nada — el log de arriba («el Edge acaba de decir que
// puede servir inferencia») nombra a los que SÍ lo dicen, y el inventario de la flota
// dice cuántos hay. Mientras quede uno que calle, esta rama es lo único que le queda:
// borrarla lo dejaría frío para siempre y sin un solo error.
//
// ⚠️ EL CANAL DE CONTROL NO SE CALIENTA, y esta guarda sí es load-bearing (a
// diferencia de la de observaReadiness): el registro es perezoso POR FRAME y los
// frames de auth llevan `__wapp_control__`, así que este es el sitio por donde ese id
// entra de verdad. Un Edge con el operador logueado y CERO teléfonos emparejados no
// va a recibir ningún mensaje que clasificar, así que calentarlo gastaría ~50 s de la
// CPU del cliente y ~250 MB de su caché por un prefijo que nadie va a pedir. Es el
// mismo criterio con el que esa sesión tampoco es flota (MP-11), aplicado al recurso.
//
// 🔴 VA FUERA DE CUALQUIER regCtx, y tiene que ir fuera: el presupuesto del handshake
// es del orden de segundos y un calentamiento dura ~50 s. Colgarlo de ahí lo mataría
// siempre, sin un solo error que lo delatara. Quien lo atiende dispara y vuelve, con
// su propio reloj (ver el contrato de OnWarmup).
func (s *Server) calientaPorRegistro(cc connCtx) {
	if s.OnWarmup == nil || !cc.hasIdentity || cc.sessionID == cltransport.ControlSessionID {
		return
	}
	if s.readinessDelEdge(cc) != cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED {
		return
	}
	// El `kind` va VACÍO: no se acaba de publicar ninguna config concreta, es que este
	// Edge no tiene NADA cacheado todavía.
	s.OnWarmup(cc.tenantID, cc.edgeID, cc.sessionID, "")
}
