package gatewaygrpc

// plaza.go — LA DIRECCIÓN DE LA PLAZA (Plan 044 · Ola 2 · T2.7, ADR-0046 Mecanismo 1).
//
// El recurso escaso de la vía local es UN OLLAMA POR MÁQUINA, y la máquina es el
// Edge. El aforo que lo reparte vive en el Cloud (ADR-0038 Enmienda 2) y necesita
// saber una sola cosa que solo este paquete puede responder: **qué Edge atendería
// esta inferencia**. Eso es exactamente lo que ya decide `inferenceSession` para
// elegir el stream; aquí no se decide nada nuevo, se PUBLICA lo decidido.
//
// 🔴 POR QUÉ (tenant, Edge) Y NO (tenant, sesión). Un Edge multiplexa TODAS sus
// sesiones sobre un solo stream (ADR-0008): dos sesiones del mismo Edge son el mismo
// proceso y el mismo Ollama. Un aforo indexado por sesión le daría DOS plazas a una
// sola máquina en cuanto el cliente emparejara un segundo teléfono, y el entero
// dejaría de proteger lo que dice proteger — sin un solo error.
//
// 🔴 Y POR QUÉ NO UN ENTERO GLOBAL DEL PROCESO. Porque serializaría los presupuestos
// de TODOS los clientes detrás del más lento: el tenant A con un pedido de 10 ítems
// (5–6 min) dejaría al tenant B, con su propio Edge ocioso, esperando sin motivo
// (D7-b, D-044.42).

// PlazaDe dice QUÉ EDGE del tenant atendería una inferencia originada en esa sesión,
// o `false` si el tenant no tiene ninguna sesión viva en esta réplica.
//
// Es el MISMO recorrido que hace `Infer`, y lo es a propósito: se resuelve el stream
// con `inferenceSession` —candidato vivo, o la primera alfabética de las vivas del
// tenant— y luego se traduce ese stream a su Edge. Reimplementar aquí el criterio
// sería fabricar la avería clásica de los caminos gemelos: el aforo protegería un
// Edge y la petición saldría por otro.
//
// ⚠️ ES UNA FOTO, NO UNA RESERVA. Entre esta llamada y la inferencia real el Edge
// puede irse y el enrutado caer a otro. Consecuencia acotada y conocida: el aforo
// guardaría la plaza de un Edge que ya no sirve, y el que sí sirve quedaría un rato
// sin proteger. No se arregla con un candado más largo —habría que sostenerlo
// durante los minutos que dura una cadena de lote, con el registro entero detrás—
// sino con el hecho de que el siguiente job vuelve a preguntar.
func (s *Server) PlazaDe(tenantID, originSessionID string) (string, bool) {
	sid, ok := s.inferenceSession(tenantID, originSessionID)
	if !ok {
		return "", false
	}
	return s.edgeDeSesion(tenantID, sid)
}

// edgeDeSesion traduce una sesión viva al Edge que la sostiene. Recorre el mismo
// índice que `sessionsForTenant` y bajo el mismo candado.
//
// El recorrido es lineal sobre los Edges del proceso y no sobre un índice inverso
// `sesión -> Edge` porque ese índice sería una TERCERA estructura que mantener en
// sincronía con `edgeSessions` y `edgeReadiness` en cada track/untrack, a cambio de
// ahorrar un bucle sobre unas decenas de entradas que se recorre una vez por JOB DE
// LOTE — o sea, una vez cada varios minutos. La estructura de más se pagaría en
// desincronizaciones, que es lo caro.
func (s *Server) edgeDeSesion(tenantID, sessionID string) (string, bool) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	for k, set := range s.edgeSessions {
		if k.tenantID != tenantID {
			continue
		}
		if _, vive := set[sessionID]; vive {
			return k.edgeID, true
		}
	}
	return "", false
}
