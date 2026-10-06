// Porta internal/gateway/grpc/inference.go @ ec236b3 (el despacho: Infer, la elección
// del stream por el que sale —candidato vivo, o el mejor del tenant que no haya dicho
// que no puede— y el frame). Trozo de inference.go, partido por E-13.

package grpc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// preferredRoute (en el paquete viejo, preferredRoute) resuelve la PRECEDENCIA de enrutado —cable primero, conversación
// después— sin tocar inferenceSession, que sigue respondiendo a la pregunta de
// siempre («¿por qué stream sale esto?») con un único candidato preferido.
func (r InferRequest) preferredRoute() string {
	if r.TargetSessionID != "" {
		return r.TargetSessionID
	}
	return r.OriginSessionID
}

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
func (s *Server) Infer(ctx context.Context, tenantID string, req InferRequest) (string, error) {
	sessionID, ok := s.inferenceSession(tenantID, req.preferredRoute())
	if !ok {
		return "", inferErr("", "", ReasonEdgeOffline,
			fmt.Errorf("%w: el tenant no tiene ninguna sesión viva en esta réplica", session.ErrSessionOffline))
	}

	cmdID, err := newCommandID()
	if err != nil {
		return "", err
	}

	ch := make(chan *cloudlinkv1.InferenceResult, 1)
	s.infersMu.Lock()
	s.infers[cmdID] = pendingInfer{ch: ch, sessionID: sessionID}
	s.infersMu.Unlock()
	defer s.clearInfer(cmdID)

	if pushErr := s.registry.Push(ctx, sessionID, inferToCloud(cmdID, sessionID, req)); pushErr != nil {
		if errors.Is(pushErr, session.ErrPushAbandoned) {
			// El llamante se rindió mientras se empujaba: no es una degradación de la
			// vía, es su propio reloj. Sin motivo ⇒ sin aviso.
			return "", fmt.Errorf("%w: %w", ErrInferenceAbandoned, pushErr)
		}
		return "", inferErr(cmdID, sessionID, reasonOfPush(pushErr), pushErr)
	}

	return s.awaitInference(ctx, ch, cmdID, sessionID, req.Timeout)
}

// reasonOfPush (en el paquete viejo, reasonOfPush) traduce el fallo del empuje. Solo dos desenlaces llegan aquí, porque
// ErrPushAbandoned lo filtra Infer antes:
//
//   - ErrSessionOffline ⇒ edge_offline. La sesión se fue entre la selección y el
//     empuje (carrera legítima: el Edge se desconectó en ese hueco).
//   - ErrPushTimeout ⇒ timeout. El Edge NO LEE su stream: está vivo para gRPC y
//     atascado de verdad, que es un fallo de la vía y no del llamante.
func reasonOfPush(err error) string {
	if errors.Is(err, session.ErrSessionOffline) {
		return ReasonEdgeOffline
	}
	return ReasonTimeout
}

// inferenceSession elige POR QUÉ STREAM sale el frame.
//
// 🔴 EL PROBLEMA, PORQUE NO ES OBVIO: Registry.Push exige un session_id, pero
// InferenceRequest.session_id va normalmente VACÍO — la inferencia es de alcance
// EDGE (un proceso, un Ollama), no de una sesión de WhatsApp. Es decir: hay que
// elegir una sesión de la que solo se usa el CABLE.
//
// Que elegir cualquiera sea correcto lo garantiza el ADR-0008: un Edge multiplexa
// TODAS sus sesiones sobre UN SOLO stream, así que dos sesiones del mismo Edge son
// literalmente el mismo cable y el mismo proceso. Lo único que la elección decide de
// verdad es QUÉ EDGE atiende cuando un tenant tiene varias instalaciones — y para
// eso vale cualquiera: cada una tiene su propio Ollama.
//
// El criterio, en dos pasos, sobre el candidato que le pase el llamante (que
// InferRequest.preferredRoute resuelve: TargetSessionID si lo hay —el cable que se
// exige, sin conversación detrás—, y si no OriginSessionID):
//
//  1. **El candidato, si está vivo.** En el caso normal es la conversación que
//     generó la pregunta, así que el frame sale por el mismo Edge que recibió el
//     mensaje del cliente y el session_id del frame dice algo cierto (trazabilidad,
//     que es justo para lo que el proto declara ese campo).
//
//  2. **Si no, el mejor candidato del tenant que NO haya dicho que no puede**
//     (Plan 057 · Ola 3, REQ-057.10). Se descartan los Edge que declararon
//     `INFERENCE_READINESS_DOWN` —mandarles el prompt solo podía volver como
//     `ollama_down` tras esperar el presupuesto entero— y se prefiere a los que
//     dijeron READY sobre los que no dicen nada.
//
//     El orden alfabético se conserva DENTRO de cada grupo, y por el motivo
//     original: el recorrido de un map de Go está ALEATORIZADO, así que sin ordenar,
//     dos peticiones seguidas del mismo tenant podrían irse a Edges distintos.
//     Ordenar las manda al mismo, que es lo que hace que el breaker del Edge
//     (ADR-0042) y el modelo ya cargado en memoria signifiquen algo. No se persigue
//     balancear: no hay medida que diga que haga falta, y un reparto aleatorio es
//     peor que uno estable.
//
// 🔴 EL PASO 1 NO MIRA LA READINESS, Y ESO ES DOCTRINA, NO UN OLVIDO. El Edge que
// sostiene la conversación es el único que la atiende: la inferencia jamás cruza
// entre nodos Edge. Si su Ollama está caído, lo correcto es un `ollama_down` honesto
// del Edge que de verdad tiene el hilo, no una respuesta fabricada por la máquina de
// otra instalación del mismo cliente, que no ha visto ni un mensaje de esa charla.
// El desempate del paso 2 solo entra cuando NO hay conversación detrás.
//
// El Registry NO sabe de tenants (es map[session_id]) y no hizo falta ampliarlo: el
// índice por tenant ya existía en el Server (edgeSessions → sessionsForTenant), que
// es el mismo que usa el push de config del ADR-0021.
func (s *Server) inferenceSession(tenantID, origin string) (string, bool) {
	if origin != "" && s.registry.Online(origin) {
		return origin, true
	}
	ready, silent := s.candidatesByReadiness(tenantID)
	for _, group := range [][]string{ready, silent} {
		if len(group) > 0 {
			slices.Sort(group)
			return group[0], true
		}
	}
	return "", false
}

// candidatesByReadiness (en el paquete viejo, candidatesByReadiness; sus dos listas
// eran `listas` y `mudas`) reparte las sesiones vivas del tenant en los DOS grupos que
// pueden atender una inferencia, y deja fuera al tercero.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ DOS LISTAS Y NO UN FILTRO POR READY
// ════════════════════════════════════════════════════════════════════════════
//
// `INFERENCE_READINESS_UNSPECIFIED` significa «este Edge no lo dice» —un Edge con el
// contrato anterior a v0.17.0, o uno recién arrancado que todavía no lo sabe—, NUNCA
// «no puede»; la distinción está escrita y razonada en noteReadiness (readiness.go).
// Exigir READY para ser elegible, que es lo que pedía el análisis de origen de este
// plan, dejaría SIN INFERENCIA a toda la flota que no publica el campo, y sin un solo
// error: el gateway diría «este tenant no tiene a nadie vivo» de un cliente cuya única
// instalación está perfectamente sana. Por eso el filtro es en NEGATIVO —se descarta
// lo que dijo DOWN— y READY es una PREFERENCIA que ordena, no una puerta que cierra.
//
// Corre bajo `trackMu` —el mismo candado e índice que sessionsForTenant— y lee
// `edgeReadiness` DENTRO del mismo bloque, sin llamar a nadie con el candado tomado:
// la disciplina de noteReadiness y oneSessionPerEdge.
func (s *Server) candidatesByReadiness(tenantID string) (ready, silent []string) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	for k, set := range s.edgeSessions {
		if k.tenantID != tenantID {
			continue
		}
		dest := &silent
		switch s.edgeReadiness[k] {
		case cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN:
			continue
		case cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY:
			dest = &ready
		}
		for sid := range set {
			*dest = append(*dest, sid)
		}
	}
	return ready, silent
}

// inferToCloud envuelve la petición en un CloudToEdge dirigido a la sesión dada.
//
// ⚠️ El session_id del ENVELOPE y el del payload dicen cosas distintas y van así a
// propósito: el del envelope es POR DÓNDE sale (lo exige el multiplexado del
// ADR-0008), y el del payload es la conversación que originó la pregunta, que puede
// no existir. Rellenar el segundo con el primero convertiría un dato de trazabilidad
// en una coincidencia sin significado.
func inferToCloud(cmdID, sessionID string, req InferRequest) *cloudlinkv1.CloudToEdge {
	temp := float32(req.Temperature)
	frame := &cloudlinkv1.InferenceRequest{
		CommandId:   cmdID,
		SessionId:   req.OriginSessionID,
		Prompt:      req.Prompt,
		Format:      req.Format,
		Temperature: &temp,
		TimeoutMs:   inferTimeout(req.Timeout).Milliseconds(),
		Class:       req.Class,
		Warmup:      req.Warmup,
	}
	// PRESENCIA EXPLÍCITA, y solo cuando hay algo que decir: el campo es `optional`
	// porque «quiero 0» y «no dije nada» serían el mismo byte en el cable. Aquí el
	// Cloud NUNCA quiere 0 —una salida de cero tokens no es una respuesta—, así que
	// un valor no positivo significa «no lo fijo» y el Edge aplica su default.
	// Escribir un puntero a 0 le pediría al Edge un num_predict de cero.
	if req.MaxOutputTokens > 0 {
		limit := req.MaxOutputTokens
		frame.MaxOutputTokens = &limit
	}
	return &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_InferenceRequest{InferenceRequest: frame},
	}
}
