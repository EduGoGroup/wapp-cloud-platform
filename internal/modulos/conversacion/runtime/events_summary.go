// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Trozo de events.go (E-13): el RESUMEN del evento que se abandona, la COLETILLA de «tienes
// algo a medias» y el recordatorio de lo decidido al volver a un evento (EV-8). Solo se
// movieron declaraciones.

// varTaglineOffered marca que a ESTA conversación ya se le pegó la coletilla de
// «tienes algo a medias» (T3.8 punto 2: una sola vez por conversación).
//
// Vive en flow_state.vars —y no en una tabla ni en conversation_events— porque ese es
// exactamente el alcance que el plan pide: la marca tiene que MORIR con la
// conversación. Guardarla en algo más duradero haría que alguien que vuelve semanas
// después no la viera nunca, que es lo contrario de lo que se busca.
//
// Consecuencia asumida, y escrita para que nadie la trate como fallo: si la
// conversación se descarta y renace —TTL del limbo, escape global—, la coletilla puede
// volver a salir UNA vez. No es un bug; es lo que significa «por conversación».
//
// Hoy, además, la garantía la da el SITIO: la coletilla solo se pega al ARRANCAR, y
// arrancar crea la conversación. Esta marca es la red para el día que alguien la emita
// desde otro punto del camino.
const varTaglineOffered = "tagline_offered"

// summarizeAbandoned escribe el resumen del evento que la conversación ACABA DE
// ABANDONAR (T3.4, E-4). Los tres abandonos reales son el salto por tipo, el
// `event_stop` y el escape global; los tres pasan por aquí.
//
// Dos condiciones de uso que no son negociables:
//
//  1. SE LLAMA ANTES DE BORRAR EL flow_state. El nivel de la sub-máquina («te
//     quedaste eligiendo la cantidad») sale de conv.Vars, y las Vars mueren con el
//     estado. Llamarla después no falla ni avisa: escribe un resumen mudo, y nadie se
//     entera hasta que un humano lee el historial y no encuentra dónde se quedó.
//  2. NO se llama al vencer la inactividad. Al vencer, el evento no muere: sigue
//     `open` y rescatable, y lo único que pasa es que la conversación suelta el
//     puntero. CALLARSE NO ES ABANDONAR. Escribir ahí una fila marcaría como
//     terminado lo que sigue abierto, y la reescribiría en cada vencimiento sucesivo
//     del mismo evento.
//
// Es BEST-EFFORT a propósito: un fallo al resumir se LOGUEA y no aborta el abandono.
// El cliente ya pidió cambiar de conversación, y negárselo porque no se pudo escribir
// una fila de historial sería castigarle por un problema nuestro. `written == false`
// sin error es lo NORMAL (no había nada que resumir) y no se registra como fallo.
func (rt *Runtime) summarizeAbandoned(ctx context.Context, key store.Key, sessionID string, st model.Conversation, target string) {
	if rt.events == nil || st.EventID == "" || st.EventID == target {
		return
	}
	if rt.sources.Lines == nil && rt.sources.Answers == nil {
		return // sin fuentes cableadas no hay resumen que armar (no-regresión).
	}
	ev, alive, err := rt.aliveByID(ctx, key.TenantID, sessionID, key.ContactID, st.EventID)
	if err != nil || !alive {
		rt.log.Warn("runtime: no se pudo releer el evento abandonado; se sigue sin resumen",
			"error", err, "session_id", sessionID)
		return
	}
	seq, written, err := events.PersistSummary(ctx, rt.events, rt.sources, ev, st.Vars)
	if err != nil {
		rt.log.Warn("runtime: no se pudo escribir el resumen del evento abandonado",
			"error", err, "session_id", sessionID, "event_kind", ev.Kind)
		return
	}
	if written {
		rt.log.Debug("runtime: resumen del evento abandonado escrito",
			"session_id", sessionID, "event_kind", ev.Kind, "seq", seq)
	}
}

// taglineFor resuelve la COLETILLA del camino con llm_intent (T3.8 punto 2, REQ-27):
// cuando la intención inferida se atiende, la respuesta termina avisando de que hay
// algo a medias. Devuelve "" cuando no toca decir nada, y ese es el caso normal.
//
// Solo se emite en el camino de la INTENCIÓN —intentName vacío ⇒ nada—, y esa es la
// mitad que protege a los demás: un arranque por keyword, por fallback o por la API no
// lleva coletilla, porque ahí lo que va es la lista numerada (o el flujo del tenant) y
// no un aviso pegado.
//
// ⚠️ El otro llamante es birthEvent (events.go), que ya NO es un caso mudo (Plan 054 ·
// F2b, D-A — decisión de Jhoan 2026-08-12, sustituye a CONTRATO-OLA5 D1): el
// ConfigResolver SÍ combina las dos cosas cuando una regla kind='llm' gana con
// event_kind poblado —{StartEvent, FlowID, EventKind, Params, IntentName}, las cinco
// a la vez—, así que dec.IntentName puede llegar no-vacío a un arranque que TAMBIÉN
// pare un evento. event_start (sin intención propia) sigue llegando con intentName ""
// y esa llamada se sigue callando sola —lo fija
// TestTagline_ArranquePorEventStartNoLlevaColetilla, que no se movió—.
//
// La combinación NUEVA (intención + nacimiento) es segura PORQUE birthEvent resuelve
// la coletilla ANTES de CreateEvent (ver su docstring, unas líneas más abajo): el
// evento que se está a punto de parir no existe todavía cuando BuildTagline consulta
// los rescatables, así que nunca puede anunciarse «a medias» sobre sí mismo, sea cual
// sea la puerta (event_start o esta). Lo fija, de forma genérica,
// TestTagline_ElEventoQueAcabaDeNacerNoSeAnunciaASiMismo (con un resolver de prueba
// que fabrica la combinación a mano) y, de extremo a extremo con el ConfigResolver de
// producción, TestTagline_LLMRuleConEventKind_NoSeAnunciaASiMismo.
//
// 🔴 SEGUNDO DESTINO PENDIENTE: el criterio (c) de T3.8 exige que esta MISMA coletilla
// aparezca también en el borrador del pipeline del Plan 044 que la operadora ve en KMP.
// **El Plan 044 no existe todavía**, así que ese destino no se puede cablear ni probar
// hoy: lo que hay aquí cubre el destino de WhatsApp y nada más. No se escribió ningún
// test que finja lo contrario, y por eso T3.8 NO está entera.
func (rt *Runtime) taglineFor(ctx context.Context, tenantID, sessionID, contactID, intentName string) string {
	if rt.opening == nil || intentName == "" {
		return ""
	}
	tag, err := rt.opening.BuildTagline(ctx, events.ConversationRef{
		TenantID: tenantID, SessionID: sessionID, ContactID: contactID,
	})
	if err != nil {
		// Best-effort: sin coletilla se atiende igual la intención, que es lo que el
		// cliente pidió. Negarle la respuesta por no poder añadirle un aviso sería
		// castigarle por un problema nuestro.
		rt.log.Warn("runtime: no se pudo armar la coletilla; se responde sin ella",
			"error", err, "session_id", sessionID)
		return ""
	}
	return tag
}

// sendResumeSummary le recuerda al cliente que vuelve QUÉ LLEVABA DECIDIDO (T3.4,
// E-4: «además se envía al cliente al reanudar»). Va ANTES de la pantalla del flujo,
// porque el orden es el de la conversación: primero «esto es lo que tenías», luego
// «sigue por aquí».
//
// El resumen se arma AL VUELO y con vars nil, y las dos cosas son deliberadas:
//
//   - Al vuelo, y no leyendo la fila `summary` persistida, porque esa fila puede no
//     existir. El rescate llega por dos caminos y solo uno la escribe: tras un abandono
//     real (salto por tipo, event_stop, escape) sí; tras VENCER LA INACTIVIDAD no, por
//     la decisión de esta ola —callarse no es abandonar—. Y ese segundo camino es el
//     más común: el cliente que vuelve tras el silencio es exactamente a quien hay que
//     recordarle lo que llevaba. Leer lo persistido le dejaría mudo.
//   - vars nil porque el flow_state YA NO EXISTE cuando el cliente vuelve. Lo que se
//     enseña sale de las fuentes DURABLES —las líneas del pedido, las respuestas
//     dadas—, no del puntero de nodo que se soltó al vencer.
//
// No devuelve error a propósito: es BEST-EFFORT. Si el resumen no se puede armar, el
// rescate sigue y el cliente vuelve a su pedido igual; quedarse sin recordatorio es
// peor que quedarse sin rescate, pero mucho menos malo que perder las dos cosas.
func (rt *Runtime) sendResumeSummary(ctx context.Context, key store.Key, sessionID string, ev events.Event) {
	if rt.sources.Lines == nil && rt.sources.Answers == nil {
		return
	}
	sum, err := events.LoadSummary(ctx, rt.sources, ev, nil)
	if err != nil {
		rt.log.Warn("runtime: no se pudo armar el resumen del evento rescatado; se retoma sin él",
			"error", err, "session_id", sessionID, "event_kind", ev.Kind)
		return
	}
	if sum.Empty() {
		// Un evento sin nada decidido no tiene qué recordar: se retoma en silencio en
		// vez de mandar un resumen vacío que solo ocuparía pantalla.
		return
	}
	to, err := rt.destination(ctx, key.TenantID, key.ContactID)
	if err != nil {
		rt.log.Warn("runtime: sin destino para el resumen del rescate", "error", err, "session_id", sessionID)
		return
	}
	text := sum.Render()
	if _, err := rt.send(ctx, sessionID, to, key, []engine.Output{{Text: text}}); err != nil {
		rt.log.Warn("runtime: no se pudo enviar el resumen del rescate; se retoma igual",
			"error", err, "session_id", sessionID)
		return
	}
	// EL SALIENTE FUERA DE TURNO ARQUETÍPICO (Plan 044 · T1.6, D-044.24). Este
	// automensaje no nace de nada que el cliente pidiera y ADEMÁS LISTA PRODUCTOS,
	// así que es el caso exacto que la marca existe para desactivar: sin ella, un
	// «sí, esas dos» posterior no tendría antecedente; con ella pero sin rótulo, el
	// LLM extraería como pedido la lista que imprimió el propio rescate.
	//
	// Va DESPUÉS del envío y solo si salió, al revés que persistTurnMessages: allí
	// se persiste lo PRODUCIDO porque el estado ya avanzó con ello; aquí no hay
	// estado que avanzar —el rescate ya ocurrió— y el hilo debe contar lo que el
	// cliente de verdad tiene delante. Un resumen que no llegó no es antecedente de
	// nada.
	rt.persistOutOfTurnMessage(ctx, key.TenantID, sessionID, ev.ID, text)
}
