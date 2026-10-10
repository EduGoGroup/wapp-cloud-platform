// Porta internal/flujos/runtime/incoming.go @ e0159171

package runtime

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// Trozo de incoming.go (E-13): las guardas de BORDE, lo que HandleIncoming hace ANTES de tocar
// la conversación (incoming.go §1): el dedupe persistente de ingesta (RT-5), el perfil pasivo
// (RT-6) y el anti-self-loop (RT-7). Solo se movieron declaraciones.

// duplicateIngest es la guarda de dedupe PERSISTENTE de entrantes (Plan 028 · T6,
// ADR-0003): el outbox durable del Edge (Plan 027 Ola 3) reenvía frames tras
// reconexión ⇒ semántica at-least-once. La idempotencia consecutiva por
// last_wa_message_id (dentro de HandleIncoming) solo corta la RE-ENTREGA INMEDIATA;
// un duplicado INTERCALADO (A, B, A) o el reenvío de un entrante que dispara/escapa
// un flujo (caminos que NO tocan last_wa_message_id) se colaría. Aquí, ANTES de
// tocar el motor (resolver tenant/contacto, tomar el keyedMutex, cargar estado o
// correr efectos), se registra la clave (session_id, wa_message_id) en una tabla
// idempotente: si ya se vio ⇒ true (el llamante descarta el frame sin re-procesar
// efectos ni auto-responder). La clave única de la tabla resuelve además dos
// duplicados CONCURRENTES (cada entrante corre en su goroutine): exactamente uno
// inserta y procesa. Un wa_message_id vacío (evento sintético, no esperable en
// entrantes reales) NO se deduplica: cae al camino de siempre. Sin deduper cableado
// (nil) tampoco deduplica (no-regresión). Un fallo del deduper es best-effort
// (fail-open): se LOGUEA y devuelve false (se prefiere reprocesar a perder el
// entrante), coherente con las guardas best-effort del motor (p.ej. IsEscape).
func (rt *Runtime) duplicateIngest(ctx context.Context, sessionID string, m *cloudlinkv1.IncomingMessage) bool {
	if rt.deduper == nil || m.GetWaMessageId() == "" {
		return false
	}
	seen, err := rt.deduper.Seen(ctx, sessionID, m.GetWaMessageId())
	if err != nil {
		rt.log.Warn("runtime: dedupe de ingesta falló; se continúa (fail-open)",
			"error", err, "session_id", sessionID, "wa_message_id", m.GetWaMessageId())
		return false
	}
	if seen {
		rt.log.Debug("runtime: entrante duplicado ignorado (dedupe de ingesta)",
			"session_id", sessionID, "wa_message_id", m.GetWaMessageId())
	}
	return seen
}

// reactiveBlocked agrupa las guardas de BORDE que impiden entrar al motor reactivo
// (Plan 020). Devuelve true (y NO se procesa el entrante) si:
//   - la sesión es PASIVA (T1): escucha/transporta pero no dispara triggers, no
//     avanza con auto-envío ni escapa. Una conversación EN CURSO deja de avanzar
//     mientras siga pasiva (no se borra su estado; vuelve si se re-activa). Valor
//     vacío/desconocido ⇒ activa (no-regresión de esta guarda).
//   - el remitente es un número PROPIO del tenant (T2, anti-self-loop): una sesión
//     propia hablando; no se auto-responde (defensa semántica contra el bucle
//     sesión↔sesión del Plan 019). Consciente del perfil: solo cuentan como
//     "propios" los números de sesiones NO pasivas — una pasiva nunca auto-responde,
//     así que una sesión activa SÍ puede responder a mensajes que llegan desde el
//     número personal (pasivo) del mismo tenant sin riesgo de loop.
//
// El `profile` que entra por parámetro sale de fleet_sessions.profile (Plan 046 ·
// T1.1), agregado por el resolver. 🔴 Y su DEFAULT es PASIVO (0063, D-07): una sesión
// NUEVA sin configurar NO entra al motor reactivo hasta que su dueño la active.
//
// Sin perfil pasivo y sin números propios poblados, devuelve false ⇒ no-regresión total.
func (rt *Runtime) reactiveBlocked(ctx context.Context, tenantID, sessionID, profile, fromPn string) bool {
	if profile == profilePassive {
		rt.countReactiveBlocked(reasonPassive)
		rt.logPassiveSkip(sessionID)
		return true
	}
	return rt.isSelfLoop(ctx, tenantID, sessionID, fromPn)
}

// countReactiveBlocked registra el corte en el contador inyectado (nil-safe: sin
// WithReactiveBlockedHook no cuenta nada). Observa; NUNCA decide.
func (rt *Runtime) countReactiveBlocked(reason string) {
	if rt.onReactiveBlocked != nil {
		rt.onReactiveBlocked(reason)
	}
}

// logPassiveSkip anuncia el corte por rol passive UNA vez por sesión a INFO y las
// siguientes a Debug. El corte es un ESTADO configurado, no un evento: repetirlo a
// INFO en cada entrante inunda el log, pero dejarlo solo en Debug lo hace invisible
// con el nivel por defecto y el operador acaba diagnosticando otra cosa. La primera
// línea lleva el session_id —que el contador no puede etiquetar sin disparar la
// cardinalidad— y es justo lo que hace falta para saber QUÉ sesión marcar como bot.
func (rt *Runtime) logPassiveSkip(sessionID string) {
	if _, announced := rt.passiveAnnounced.LoadOrStore(sessionID, struct{}{}); announced {
		rt.log.Debug("runtime: sesión passive; motor reactivo omitido", "session_id", sessionID)
		return
	}
	rt.log.Info("runtime: sesión passive; motor reactivo omitido — no auto-responderá mientras siga passive (se anuncia una vez por sesión; el resto en debug)",
		"session_id", sessionID)
}

// isSelfLoop decide si un entrante proviene de un número PROPIO del tenant (una
// sesión propia hablando), en cuyo caso NO se debe auto-responder (Plan 020 · T2,
// defensa semántica contra el bucle sesión↔sesión del Plan 019).
//
// 🔑 LA NORMALIZACIÓN OCURRE AQUÍ Y SOLO AQUÍ (Plan 046 · T4.1). El remitente
// (from_pn) llega como lo mande WhatsApp —con '+', espacios, guiones o paréntesis—
// y se pasa por contact.Normalize(KindPhoneE164, …), que lo deja en dígitos puros.
// El checker calcula el ÍNDICE CIEGO sobre exactamente esos bytes, y el escritor del
// índice normalizó con la MISMA función: por eso "+57 300-111-0000" y "573001110000"
// dan el mismo veredicto. Si esta línea desapareciera, el HMAC saldría distinto y el
// número dejaría de casar consigo mismo — el fallo sería MUDO (la guarda no
// bloquearía nada y nadie vería un error), que es lo peor que puede pasarle a una
// defensa anti-bucle. crypto.KeyProvider.BlindIndex NO normaliza a propósito: es un
// HMAC sobre bytes, no conoce el dominio de los teléfonos.
//
// El veredicto es CONSCIENTE DEL PERFIL: el checker excluye los números de sesiones
// pasivas — una pasiva nunca auto-responde (reactiveBlocked lo corta), así que un
// mensaje desde ese número no puede cerrar un bucle; bloquear ahí solo impediría
// atender al número personal del tenant. El rate-limit por conversación (T0) sigue
// como red.
//
// Es CONSERVADORA hacia PROCESAR: sin checker (nil), sin from_pn, si el número no
// normaliza o si la consulta falla ⇒ devuelve false (no bloquea: la ausencia de dato
// no debe silenciar tráfico legítimo). NUNCA loguea el número ni su índice ciego
// (PII): solo el hecho y IDs opacos.
func (rt *Runtime) isSelfLoop(ctx context.Context, tenantID, sessionID, fromPn string) bool {
	if rt.selfNumbers == nil || fromPn == "" {
		return false
	}
	norm, err := contact.Normalize(contact.KindPhoneE164, fromPn)
	if err != nil {
		return false // sin número normalizable no se puede afirmar self-loop.
	}
	own, err := rt.selfNumbers.IsSelfNumber(ctx, tenantID, norm)
	if err != nil {
		// El error se ANUNCIA (no se traga) pero no bloquea: una BD lenta o un
		// KeyProvider ausente no pueden convertirse en un tenant que deja de atender.
		rt.log.Warn("runtime: no se pudo comprobar si el remitente es un número propio del tenant; guarda anti-self-loop omitida",
			"error", err, "session_id", sessionID)
		return false
	}
	if !own {
		return false
	}
	rt.countReactiveBlocked(reasonSelfLoop)
	rt.log.Warn("runtime: entrante de un número propio del tenant; auto-respuesta evitada (anti-self-loop)",
		"tenant_id", tenantID, "session_id", sessionID)
	return true
}
