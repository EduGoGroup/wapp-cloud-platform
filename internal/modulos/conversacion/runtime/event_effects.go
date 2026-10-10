// Porta internal/flujos/runtime/event_effects.go @ e0159171

package runtime

// Nombres de los efectos de CICLO DE VIDA del evento conversacional (Plan 043 ·
// T5.4, D-043.11). Son de PLATAFORMA, no de módulo: ningún modules.Module los
// declara; los emite el runtime en los puntos en que la vida del evento cambia. Son
// SIETE y son literales observables: se escriben tal cual en la columna
// flow_events.name, que es de donde lee el colector de
// wapp_flow_event_lifecycle_total. No se renombran.
//
// ⚠️ `event_expired` NO está y NUNCA estuvo: no existe esa transición (E-6 la derogó).
//
// # Lo que el runtime promete al emitirlos (se prueba con el Runtime, ola de
// `event_lifecycle`/`incoming`; este fichero solo fija los literales)
//
// El emisor no es exportado (en el viejo, emitEventEffect, emitEventEscaped y
// emitEventEffectWithReason, event_effects.go:99-160): nace en el verde. Su contrato,
// para quien lo escriba y para quien lo pruebe:
//
//   - Despacha UN efecto por el MISMO fan-out que los efectos de módulo (los sinks del
//     runtime, PersistSink → flow_events). Es BEST-EFFORT: ningún emisor aborta su
//     operación por no haber podido escribir telemetría; el hecho (la fila del evento)
//     ya está sellado en BD antes de emitir.
//   - Un evento sin ID ("") no emite nada.
//   - El Kind del efecto es EXACTAMENTE "event": es el valor de la COLUMNA
//     flow_events.kind para estos efectos. La columna admite "persist" | "event"
//     (migración 0009, sin CHECK): "persist" es «persistir dato de negocio» y "event"
//     es «suceso a despachar». El ciclo de vida no proyecta a ninguna tabla tipada: es
//     bitácora. (En el viejo, la constante no exportada effectKindEvent,
//     event_effects.go:78.)
//   - El EffectContext sale ENTERO de la fila del evento y no del turno (por eso sirve
//     a CancelEventForTenant, que entra por HTTP y no tiene turno): TenantID,
//     ContactID, SessionID, FlowID, FlowVersion y EventID = el id del evento. Para el
//     tipo `menu`, FlowID es "" y FlowVersion 0, y esa es la VERDAD, no una omisión
//     (D-043.3: el menú no es una fila de flow_definitions). Durable queda en false:
//     el corte de turno de D-054.4 no aplica a efectos de plataforma.
//   - El payload es {"history_id": <HistoryID del evento>, "kind": <tipo del evento>} y
//     nada más, para los seis primeros. ⚠️ Colisión de nombres consciente: la clave
//     `kind` del payload es el TIPO DE EVENTO (menu|cart|survey|media) y la COLUMNA
//     `kind` de la misma fila es la clase del efecto ("event").
//   - Solo EffectEventEscaped lleva además "reason", con una de las tres causas de
//     abajo, y la lleva SIEMPRE. En los otros seis la clave NO se escribe (ni siquiera
//     vacía): un lector de una bitácora append-only no tiene que distinguir «sin
//     causa» de «causa vacía».
//   - Ningún payload lleva rastro del contacto (ni número, ni JID, ni nombre).
const (
	// EffectEventStarted: nació un evento.
	EffectEventStarted = "event_started"
	// EffectEventSwitched: la conversación conmutó a otro evento vivo.
	EffectEventSwitched = "event_switched"
	// EffectEventDeactivated: se apagó el puntero al evento CONSERVANDO su flow_state
	// (se puede retomar tal cual).
	EffectEventDeactivated = "event_deactivated"
	// EffectEventInactivityExpired: venció la ventana de inactividad del evento.
	EffectEventInactivityExpired = "event_inactivity_expired"
	// EffectEventClosed: el evento cerró de forma natural (su flujo terminó).
	EffectEventClosed = "event_closed"
	// EffectEventCancelled: el evento se canceló (desde la app o por el cliente).
	EffectEventCancelled = "event_cancelled"
	// EffectEventEscaped es el SÉPTIMO efecto de ciclo de vida (Ola 6 · E5, cierra
	// MD-043.16): un abandono que DESTRUYE el flow_state (Delete). Decisión de Jhoan
	// (2026-08-11): NO reusar event_deactivated, que apaga el puntero y CONSERVA el
	// estado. Son abandonos distintos y fundirlos en un solo nombre haría inútil el
	// embudo: quien lea flow_events no podría distinguir «se puede retomar tal cual» de
	// «el progreso de nodo se perdió».
	EffectEventEscaped = "event_escaped"
)

// Las TRES causas de EffectEventEscaped (Plan 053 · Ola 4 · T4.1, REQ-053.4).
// Viajan en `payload.reason`, NO en el nombre del efecto: multiplicar el nombre en
// tres es justo lo que D-053.6 rechaza. El eje del NOMBRE es «¿el Delete destruyó el
// flow_state?» y es el mismo para las tres; lo que el nombre no puede contestar, y
// contesta el payload, es POR QUÉ se destruyó. Un embudo que no distingue «el flujo
// terminó solo» de «el cliente dijo salir» mide tres fenómenos distintos bajo una sola
// serie.
//
// Son valores de bitácora, no de dominio: nadie ramifica sobre ellos. Se exportan
// para que el test que los distingue pueda nombrarlos sin repetir los literales.
const (
	// EscapeReasonOwnerFlowFinished: se soltó un flow_state que ya había alcanzado su
	// nodo terminal. NADIE lo pidió: el flujo se acabó y su fila se recoge. Es la causa
	// más benigna de las tres.
	EscapeReasonOwnerFlowFinished = "owner_flow_finished"
	// EscapeReasonOrphanMenu: el quinto camino de suelta (E-6). El contacto tecleó algo
	// que la lista del despachador no reconoce sobre un flow_state SIN flujo (el `menu`,
	// D-043.3). Tampoco lo pidió, pero aquí SÍ hubo una intención humana que el sistema
	// no entendió: si esta serie sube, la lista del despachador se está quedando corta.
	EscapeReasonOrphanMenu = "orphan_menu"
	// EscapeReasonClientEscape: el único de los tres que el contacto pidió con todas las
	// letras (la palabra de escape configurada por el tenant). Mide abandono DELIBERADO.
	EscapeReasonClientEscape = "client_escape"
)
