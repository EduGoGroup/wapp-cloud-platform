// Porta internal/flujos/runtime/incoming.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Trozo de incoming.go (E-13): lo que cruza todos los caminos del entrante (incoming.go §5): el
// token del limitador antes de cada auto-envío (RT-8) y el recordatorio perezoso de la seña
// (RT-19). Solo se movieron declaraciones.

// replyAllowed comprueba el token-bucket de auto-respuestas para la clave (Plan
// 020 · T0). Devuelve true si se puede auto-responder; false (y loguea el hecho SIN
// PII: solo IDs opacos tenant/session/contact, nunca el texto ni el número) si la
// conversación excedió su tope. Con replyLimiter nil siempre permite (no-regresión).
func (rt *Runtime) replyAllowed(key store.Key) bool {
	if rt.replyLimiter == nil || rt.replyLimiter.Allow(key.String()) {
		return true
	}
	rt.countReactiveBlocked(reasonRateLimit)
	rt.log.Warn("runtime: auto-respuesta limitada por rate-limit de conversación",
		"tenant_id", key.TenantID,
		"session_id", key.SessionID,
		"contact_id", key.ContactID,
	)
	return false
}

// touchDeposit evalúa el recordatorio perezoso de la seña para el contacto que
// acaba de escribir (Plan 041 · T4.4, D-041.12). Tres cosas que no son de estilo:
//
//   - Es el ÚNICO reloj admisible aquí: no barre nada ni corre de fondo (ADR-0003).
//     El disparador es este entrante, que ya venía; si nadie escribe y nadie mira,
//     no se recuerda nada, y eso es exactamente lo perezoso.
//   - Pregunta por el CONTACTO, no por la conversación: el cliente puede escribir
//     "hola" sin carrito abierto y seguir debiendo la seña de un pedido de la semana
//     pasada. Por eso la consulta va por (tenant, contacto) y no por la clave del
//     flujo.
//   - Va DESPUÉS de las guardas de borde (passive, anti-self-loop) porque las hereda:
//     una sesión pasiva no auto-responde nada, tampoco esto, y un entrante que es un
//     número propio del tenant no es un cliente al que recordarle nada.
//
// No consume token del rate-limit de auto-respuestas (replyAllowed): ese tope existe
// para cortar BUCLES, y aquí no puede haberlos — cada solicitud se recuerda como
// mucho una vez en su vida, y quien lo garantiza es el compare-and-swap de la marca
// en la BD, no un contador en memoria.
// ⚠️ EL HILO SE ESCRIBE AQUÍ, Y HAY QUE MIRAR EL RELOJ AL HACERLO (Plan 044 · T1.6,
// D-044.24). El recordatorio de la seña es la coletilla ARQUETÍPICA fuera de turno:
// el cliente escribió «hola» y le contestamos además que debe una seña de la semana
// pasada. Entra al hilo MARCADA, para que quien lea el hilo no la confunda con algo
// que el cliente pidió.
//
// Tres cosas que este punto tiene de particular y que no se pueden perder de vista:
//
//   - CORRE CON EL CANDADO YA LIBERADO. Este defer se registra ANTES del lock, así
//     que se ejecuta el ÚLTIMO, después de que unlock() haya soltado la clave. Otro
//     turno de la misma conversación puede estar escribiendo en el hilo mientras
//     esto escribe. No hace falta serializarlo: el seq se numera con MAX+1 dentro de
//     la propia sentencia y quien pierda contra el UNIQUE (event_id, seq) reintenta
//     (events/store.go, maxAppendAttempts = 5). Lo que NO se debe hacer es tomar el
//     candado aquí para «arreglarlo»: eso reintroduciría en el camino caliente la
//     serialización que el defer existe para evitar.
//   - VA DESPUÉS DE LA FILA DEL TURNO, y ese es el orden correcto: el recordatorio
//     sale después de haberle contestado a la persona, así que en el hilo va después.
//   - eventID VACÍO ⇒ NO SE ESCRIBE, y es fail-open hacia el lado seguro: sin evento
//     no hay hilo. Pasa cuando el entrante no tenía conversación viva o cuando el
//     reloj la soltó y el turno arrancó una nueva — casos en los que el evento al que
//     pertenecería el recordatorio o no existe o todavía no se conoce aquí.
func (rt *Runtime) touchDeposit(ctx context.Context, tenantID, sessionID, contactID, eventID string) {
	if rt.deposits == nil {
		return
	}
	sent := rt.deposits.RemindContact(ctx, tenantID, contactID)
	if len(sent) == 0 {
		return
	}
	rt.persistOutOfTurnMessage(ctx, tenantID, sessionID, eventID, sent...)
}
