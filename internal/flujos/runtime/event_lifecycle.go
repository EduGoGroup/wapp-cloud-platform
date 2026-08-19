package runtime

// event_lifecycle.go es la MUERTE EXPLÍCITA del evento conversacional (Plan 043 ·
// Ola 4, E-5): el cierre NATURAL cuando su flujo termina (T4.1) y la cancelación
// por id desde la app del dueño (T4.2-core + T4.3). Son los ÚNICOS dos caminos que
// mueven `status` (D-043.5): ni el vencimiento, ni el escape, ni el event_stop
// tocan la fila — esos solo sueltan el puntero, y quien lo diga distinto está
// reintroduciendo el `expired` que E-6 derogó.

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// ErrNoEventPlane lo devuelven GetEventForTenant/CancelEventForTenant cuando el
// runtime se construyó sin WithEventStore. NO se disfraza de «no encontrado»: un
// despliegue sin plano de eventos que expone el endpoint de cancelación es un
// error de cableado, y contestarle 404 al dueño escondería exactamente eso.
var ErrNoEventPlane = errors.New("runtime: sin plano de eventos cableado (WithEventStore)")

// closeIfFinished aplica la MUERTE POR FIN DE FLUJO (T4.1): si el estado quedó en el
// centinela y la conversación tenía evento activo, transiciona el evento a su estado
// terminal (el guard y el sellado de closed_at los pone el store) y apaga el puntero
// EN EL STRUCT — persistirlo es del llamante, que ya tiene un Save por delante; así el
// cierre no añade una segunda escritura de flow_state.
//
// A QUÉ estado terminal lo lleva NO es una constante desde el hallazgo #29 (Plan 043 ·
// Ola 6, decisión de Jhoan 2026-08-11): lo dice el DESENLACE que el módulo declaró y
// que el engine selló en el estado (model.Conversation.Outcome). El módulo declara
// CÓMO terminó; esta función TRADUCE:
//
//	OutcomeCancelled            → events.StatusCancelled  (+ event_cancelled)
//	completado / sin declarar   → events.StatusClosed     (+ event_closed, lo de siempre)
//
// El porqué de la traducción, y no de dejarlo todo en `closed`: D-043.5 describe
// `closed` como «fin NATURAL del flujo», y un pedido cancelado no lo es; y la enmienda
// E-11 del ADR-0029 ya produce `cancelled` cuando el cliente cierra su evento con un
// gesto MENOS explícito que teclear «cancelar pedido». Dos gestos del mismo cliente,
// el más explícito de los dos, no pueden acabar en estados contrarios.
//
// EL EFECTO SIGUE AL ESTADO, NO AL CAMINO (decisión, no obviedad): con desenlace
// cancelado se emite `event_cancelled`, el MISMO nombre que emite cancelAndAbandon.
// La alternativa —conservar `event_closed` porque «este camino es el cierre natural»—
// se descarta por dos razones medibles. (1) flow_events es una bitácora append-only y
// conversation_events es la tabla: un `event_closed` junto a una fila que dice
// `cancelled` es una contradicción escrita, y quien las cruce no puede saber cuál
// miente. (2) El embudo dejaría de contar: «cuántos eventos se cancelaron» es hoy el
// recuento de `event_cancelled`, y la cancelación MÁS FRECUENTE —la clienta que
// cancela dentro de su pedido— quedaría fuera de ese recuento sin que el nombre lo
// delatara. Es el mismo eje que la Ola 6 fijó para EffectEventEscaped: el nombre
// describe el EFECTO, no la intención ni la puerta por la que se entró.
//
// Tres reglas que no son de estilo:
//
//   - ErrNotOpen es carrera benigna (mismo trato que retireForNew): otro escritor
//     selló la muerte primero —una cancelación desde la app entre el Step y este
//     UPDATE— y el puntero se apaga igual, porque apunta a un muerto.
//   - Un fallo REAL de la transición NO limpia el puntero (un solo hecho, E-8 §4)
//     y NO aborta el turno: se LOGUEA y el estado se persiste con el puntero
//     intacto. El reintento es natural: el estado queda terminal con evento
//     activo, y el siguiente entrante vuelve a pasar por aquí. Abortar perdería
//     el Save del avance —la respuesta al cliente y los efectos del módulo— por
//     no poder sellar una fila que se puede sellar después.
//   - El intake NO se toca, TAMPOCO con desenlace cancelado (verificado, no heredado):
//     la proyección del carrito ya deja su solicitud en el estado que le toca —
//     cart_closed la cierra, cart_cancelled la lleva a `cancelled`
//     (transitionOpenIntake, modules/cart/projection.go)—. Abandonarla aquí pisaría
//     ese hecho y, con el #29, además lo DEGRADARÍA: un pedido que la clienta canceló
//     figuraría como `abandoned`, que es otra cosa. Abandonar sigue siendo SOLO de la
//     cancelación desde la app (T4.3).
//
// # QUÉ EVENTO SE CIERRA (Plan 053 · T1.6, D-053.2 / REQ-053.1)
//
// El DUEÑO (st.OwnerEventID), NUNCA el activo (st.EventID). Son dos preguntas
// distintas sobre la misma fila —«¿a quién le habla el contacto ahora?» frente a «¿de
// quién es el FlowID/CurrentNode/Vars que esta fila carga?»— y hasta el Plan 053 las
// contestaba una sola columna. Cerrar por el activo es cerrar al que estaba delante,
// no al que acaba de terminar: con un `menu` montado sobre un `cart` a medias,
// terminar el carrito mataba el menú (el falso `event_closed` de #22 / H2). Cuando los
// dos punteros coinciden —el caso común, sin despachador de por medio— esto es byte a
// byte el gesto de siempre.
//
// # LA LIMPIEZA DE LOS DOS PUNTEROS (huecos C y D del paso 0)
//
// No es cosmética; sin ella el plan se rompe a sí mismo.
//
//   - C · el DUEÑO se apaga SIEMPRE que el cierre se consuma. La condición de entrada
//     de pendingClosure es ahora st.OwnerEventID, así que es este apagado —y no el del
//     activo— lo que dice «ya no queda cierre pendiente». Sin él, pendingClosure
//     contestaría pendiente=true en TODOS los turnos siguientes (D-053.3), advanceLive
//     no alcanzaría nunca releaseFinishedState y el contacto se quedaría MUDO sobre una
//     fila terminal que nadie suelta. Hoy no pasa porque quien apaga la condición es
//     st.EventID = ""; al mover la condición al dueño, la limpieza se mueve con ella.
//   - D · el ACTIVO se apaga SOLO si era el mismo evento. En el caso divergente
//     st.EventID apunta al `menu`, que sigue siendo el activo y NO ha terminado nada:
//     apagarlo sería desactivar de tapadillo un evento vivo por el fin de un flujo
//     ajeno —justo el efecto colateral que H2 existía para evitar— y haría inalcanzable
//     el criterio de T5.1 («el menu sigue open Y ACTIVO») con el propio código del plan.
//
// eraElMismo se captura ANTES de tocar nada, y ese orden es la parte frágil: leerlo
// después del `st.OwnerEventID = ""` compararía "" contra el activo y contestaría «no
// eran el mismo» exactamente en el caso común, dejando el puntero activo colgando de un
// evento ya cerrado en TODAS las conversaciones. Se calcula arriba del todo, antes
// incluso de saber si hay algo que cerrar, para que no haya ninguna rama futura entre
// la captura y su uso.
func (rt *Runtime) closeIfFinished(ctx context.Context, st *model.Conversation) {
	// Hueco D: ANTES de cualquier limpieza. Ver el docstring.
	//
	// ⚠️ ACOPLAMIENTO ANOTADO: el cuadrante `owner == "" && active == ""` también da
	// eraElMismo = true, y solo es inofensivo PORQUE la guarda de entrada de
	// pendingClosure (`st.OwnerEventID == ""`, más abajo) corta antes y nunca se llega
	// a la limpieza. Si alguien relaja esa condición, este cuadrante apagaría
	// st.EventID gratis — desactivando un evento activo por un cierre que no ocurrió.
	eraElMismo := st.OwnerEventID == st.EventID
	ev, conocido, pendiente := rt.pendingClosure(ctx, *st)
	if !pendiente {
		return
	}
	destino, efecto := terminalFor(st.Outcome())
	nuestra := true
	if err := rt.events.TransitionEvent(ctx, st.OwnerEventID, destino); err != nil {
		if !errors.Is(err, events.ErrNotOpen) {
			// El puntero que se conserva para reintentar es EL DEL DUEÑO (los dos, si
			// coincidían): se nombra en el log porque desde T1.6 hay dos punteros en la
			// fila y «el puntero» a secas ya no identifica ninguno.
			rt.log.Warn("runtime: no se pudo cerrar el evento al terminar su flujo; el puntero se conserva para reintentar",
				"error", err, "session_id", st.SessionID, "owner_event_id", st.OwnerEventID, "destino", destino)
			return
		}
		rt.log.Info("runtime: el evento ya no estaba open al terminar su flujo (carrera benigna)",
			"session_id", st.SessionID)
		nuestra = false
	}
	if nuestra && !conocido {
		// #15 (E8 punto 2): la transición la ganamos NOSOTROS pero la relectura
		// previa de `ev` falló, así que emitEventEffect (ev.ID == "") no puede
		// emitir el efecto: el evento murió en BD sin dejar su fila de telemetría.
		//
		// El id que se loguea es el del DUEÑO y la clave se renombra a owner_event_id
		// (T1.6): este WARN es la única pista de una fila de telemetría que no se
		// escribió, y quien la persiga tiene que ir al evento que MURIÓ. Dejar aquí
		// st.EventID mandaría a leer el `menu` —vivo, intacto y ajeno a lo ocurrido—
		// en el caso divergente, que es el mismo error de destinatario que esta tarea
		// corrige tres líneas más arriba.
		rt.log.Warn("runtime: el evento murió al terminar su flujo pero no se pudo releer antes para emitir su efecto de ciclo de vida",
			"session_id", st.SessionID, "owner_event_id", st.OwnerEventID, "efecto", efecto)
	}
	if nuestra && conocido {
		rt.emitEventEffect(ctx, ev, efecto)
	}
	// Hueco C: obligatorio, y llega hasta aquí también en la carrera benigna
	// (nuestra=false) — el dueño está muerto igual, lo matara quien lo matara, y
	// conservar el puntero solo dejaría un reintento que no tiene nada que reintentar.
	st.OwnerEventID = ""
	if eraElMismo {
		st.EventID = ""
	}
}

// terminalFor traduce el desenlace declarado por el módulo al par (estado terminal
// del evento, efecto de ciclo de vida) que le corresponde. Los dos salen del MISMO
// sitio a propósito: si el nombre del efecto se eligiera aparte, nada impediría que
// un cambio futuro moviera el estado y dejara el nombre viejo, que es exactamente la
// contradicción que el docstring de closeIfFinished prohíbe.
//
// Cualquier desenlace que no sea `cancelled` —incluido el CERO, que es lo que
// declaran menu, survey y media— cae en `closed`: el default es la conducta de
// siempre, no una rama nueva.
func terminalFor(o model.Outcome) (events.Status, string) {
	if o == model.OutcomeCancelled {
		return events.StatusCancelled, EffectEventCancelled
	}
	return events.StatusClosed, EffectEventClosed
}

// pendingClosure contesta, con UNA SOLA relectura del evento, las dos preguntas que
// el cierre natural plantea: cuál es la fila del evento que va a morir —que
// closeIfFinished necesita para su telemetría— y si a este flow_state le queda un
// cierre PENDIENTE, es decir, si alguien va a cerrar ese evento por esta vía alguna
// vez.
//
// Vive aparte porque la segunda pregunta la necesita un SEGUNDO sitio y en OTRO
// TURNO: la rama de suelta del flow_state terminal (#28 / H2, Ola 6 · incoming.go),
// que hasta la Ola 6 preguntaba `st.EventID == ""` y por eso se quedaba esperando un
// apagado que la guarda de posesión de entonces no iba a producir NUNCA. Devolver el
// motivo en vez de recalcularlo allí evita que dos sitios decidan lo mismo por
// separado.
//
// # LA CONDICIÓN DE ENTRADA MIRA AL DUEÑO (Plan 053 · T1.6, D-053.3)
//
// `st.OwnerEventID == ""` y no `st.EventID == ""`, y es LA BISAGRA de toda la tarea,
// no un detalle de simetría. Tres razones, cada una suficiente:
//
//  1. Se pregunta por el campo que se USA. Quien contesta pendiente=true autoriza a
//     closeIfFinished a llamar a TransitionEvent(st.OwnerEventID, …): preguntar por
//     otro campo dejaría pasar una fila con dueño VACÍO —una fila legada anterior al
//     backfill de T1.3, con flow_id poblado y owner_event_id todavía NULL— y el
//     cierre iría contra el id "" contra la base. REQ-053.1 lo dice como regla: con
//     el dueño vacío NO se transiciona ningún evento.
//  2. Es la condición que la limpieza APAGA. closeIfFinished limpia el dueño al
//     terminar (hueco C); si la condición mirase al activo, en el caso divergente
//     —donde el activo NO se apaga a propósito (hueco D)— esto contestaría
//     pendiente=true en todos los turnos siguientes, advanceLive no alcanzaría jamás
//     releaseFinishedState y el contacto se quedaría mudo. Condición y limpieza
//     tienen que ser el mismo campo o el ciclo no cierra.
//  3. `Finished()` habla del FLUJO, y el flujo es del dueño. Cruzar «terminó el
//     flujo» con «hay alguien activo» es exactamente el cruce de dos preguntas
//     distintas que este plan viene a deshacer.
//
// Y por eso mismo la relectura es del DUEÑO: el `ev` que se devuelve alimenta la
// telemetría del evento que MUERE (event_closed/event_cancelled con su kind y su
// history_id). Releer el activo daría la fila del `menu` —vivo, intacto y ajeno a lo
// que acaba de terminar— y firmaría un event_closed con sus datos: el falso positivo
// de #22 / H2 otra vez, ahora solo en la bitácora. El activo NO necesita releerse
// aquí: nadie va a tocarlo.
//
// pendiente=true incluye el evento que NO se pudo releer (conocido=false con el dueño
// puesto): esa ausencia no autoriza a dar el cierre por perdido, y closeIfFinished
// sigue intentando la transición a ciegas como siempre.
//
// # LA GUARDA DE POSESIÓN DE H2, RETIRADA (D-053.2) — qué había aquí y por qué se va
//
// Aquí vivía, y ya no vive:
//
//	if conocido && ev.FlowID != st.FlowID {
//	    return ev, true, false
//	}
//
// Existía porque hasta el Plan 053 solo había UN puntero y había que ADIVINAR la
// posesión comparando el FlowID congelado en la fila del evento con el FlowID del
// flow_state. Lo pedía este escenario: saveMenuState (events.go) hereda el flow_state
// de un flujo AJENO todavía en curso al armar el evento `menu` sobre él —el puntero
// pasa al `menu`, pero FlowID/CurrentNode siguen siendo los del `cart`—, y si ese
// flujo ajeno alcanzaba su nodo terminal más tarde, st.Finished() era cierto y el
// cierre natural mataba el `menu` con un event_closed FALSO (flow_id="" en la fila,
// porque flow_id sale del EVENTO y no del turno).
//
// Se retira, y se retira ENTERA en vez de repararse, porque ya no le queda trabajo:
// en ese mismo escenario st.OwnerEventID ES el `cart`, nunca el `menu`. No hay nada
// que comparar —owner_event_id es el HECHO, escrito por quien arrancó el flujo
// (pointStateAtEvent, T1.5), no una inferencia sobre otro campo—, y una guarda que
// solo puede confirmar lo que el campo ya dice es una guarda que miente el día que
// los dos discrepen. Que la propiedad que protegía sigue en pie no es un acto de fe:
// lo fija TestCierreNatural_NoMataElEventoActivoConFlujoAjenoAunEnCurso —el `menu`
// sigue open— y lo verifica el test de mutación de event_lifecycle_owner_test.go, que
// reintroduce esta guarda a mano y comprueba que no cambia ni un resultado.
//
// 🎁 Y de propina se cierra un gap que la Ola 6 dejó CONSCIENTE: mientras la guarda
// disparaba, closeIfFinished no cerraba nada —ni el `menu` (correcto) ni el `cart`
// (incorrecto: se quedaba `open` para siempre, con su intake colgando de un evento que
// ya nadie iba a matar por esta vía)—. Con el dueño explícito el `cart` SÍ se cierra,
// y el `menu` sigue vivo por construcción en vez de por una comparación.
//
// ⚠️ La única forma de resucitar el defecto es que alguien estampe como dueño un
// evento que no lo es. El sitio es pointStateAtEvent (T1.5) y su contrato está escrito
// allí: se estampa donde el flujo se acaba de arrancar PARA ese evento, y NO en
// saveMenuState. Ver la nota de MD sobre el `event_start` sin flow_id en el test de
// mutación.
//
// ⚠️ `conocido=true` con `pendiente=false` YA NO ES ALCANZABLE: era la firma exacta de
// la guarda retirada. Los dos consumidores lo saben —closeIfFinished ignora `ev`
// cuando no hay cierre pendiente, y releaseFinishedState (incoming.go) recibe siempre
// conocido=false por esta vía, así que su rama del evento AJENO vivo queda muerta—.
// Retirar ese parámetro es de T2.3 (D-053.3), no de aquí: esta tarea no toca
// incoming.go.
func (rt *Runtime) pendingClosure(ctx context.Context, st model.Conversation) (ev events.Event, conocido, pendiente bool) {
	if rt.events == nil || st.OwnerEventID == "" || !st.Finished() {
		return events.Event{}, false, false
	}
	// Lectura PREVIA (Plan 043 · T5.4, D2 · sitio 5): tras la transición el evento
	// sale de los vivos y ya no se puede releer por aliveByID. Solo ocurre sobre un
	// flow_state TERMINAL con dueño puesto — el turno que termina el flujo y, si ese
	// cierre no llegó a producirse, el siguiente entrante que vuelva a preguntar.
	ev, conocido = rt.activeEvent(ctx, store.Key{
		TenantID: st.TenantID, SessionID: st.SessionID, ContactID: st.ContactID,
	}, st.SessionID, st.OwnerEventID)
	return ev, conocido, true
}

// GetEventForTenant lee un evento por id acotado al tenant del token (T4.2).
// Delega TODO en el store: el aislamiento vive en el SQL y aquí no se re-decide.
func (rt *Runtime) GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	if rt.events == nil {
		return events.Event{}, ErrNoEventPlane
	}
	return rt.events.GetEventForTenant(ctx, tenantID, eventID)
}

// CancelEventForTenant es la cancelación EXPLÍCITA desde la app del dueño
// (T4.2-core + T4.3): guard open→cancelled, solicitud a `abandoned`, puntero de la
// conversación apagado. Devuelve la fila ya terminal — la que el endpoint enseña.
//
// Idempotente sobre terminales por DOS capas que se refuerzan: el fetch temprano
// devuelve la fila TAL CUAL sin tocar nada (la segunda llamada no cambia ni
// closed_at ni vuelve a abandonar), y si aun así dos cancelaciones corren a la
// vez, el compare-and-swap del store deja que una gane y la otra re-lea (el mismo
// patrón de carrera benigna de retireForNew).
//
// El keyedMutex de la conversación se toma ANTES de escribir: la limpieza del
// flow_state comparte fila con HandleIncoming (Load→Save), y sin el single-flight
// un avance concurrente podría re-escribir el puntero recién apagado. Se toma
// DESPUÉS del fetch —la clave sale de la propia fila— y el hueco que eso deja lo
// cubre el guard: si el estado del evento cambió entre medias, la transición
// pierde limpia con ErrNotOpen.
//
// Orden del hecho (E-8 §4, precedente retireForNew): transición → abandono →
// puntero. Si el abandono falla, el error SE PROPAGA con el evento ya cancelado
// (costura conocida: reintentar la cancelación NO reintenta el abandono, porque
// la segunda llamada entra por la rama idempotente). Si falla la limpieza del
// puntero, también se propaga: el puntero colgando es autocorregible —eventClock
// trata un evento no-vivo como benigno— pero el llamante debe saber que el
// criterio «flow_state.event_id queda NULL» no se cumplió en ESTA llamada.
func (rt *Runtime) CancelEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error) {
	if rt.events == nil {
		return events.Event{}, ErrNoEventPlane
	}
	ev, err := rt.events.GetEventForTenant(ctx, tenantID, eventID)
	if err != nil {
		return events.Event{}, err
	}
	if !ev.Alive() {
		// Ya terminal: la FILA DEL EVENTO no se toca —closed_at es el de la primera
		// muerte, no el de este reintento—, pero sus dos efectos colaterales sí se
		// COMPLETAN si quedaron a medias. Ver repairCancelled.
		return rt.repairCancelled(ctx, tenantID, ev)
	}
	key := store.Key{TenantID: ev.TenantID, SessionID: ev.SessionID, ContactID: ev.ContactID}
	unlock := rt.locks.lock(key)
	defer unlock()
	if err := rt.cancelAndAbandon(ctx, tenantID, ev); err != nil {
		if errors.Is(err, events.ErrNotOpen) {
			// Carrera benigna: otro escritor selló la muerte entre el fetch y el
			// UPDATE. Re-leer y devolver lo que quedó es exactamente la rama
			// idempotente, solo que ganada por otro.
			return rt.events.GetEventForTenant(ctx, tenantID, eventID)
		}
		return events.Event{}, fmt.Errorf("runtime: cancelar el evento: %w", err)
	}
	if err := rt.releaseStateFrom(ctx, key, eventID); err != nil {
		return events.Event{}, err
	}
	// Re-fetch y no un mutate local: la fila que se devuelve lleva el closed_at
	// REAL que selló el store, no uno reconstruido aquí que podría discrepar.
	return rt.events.GetEventForTenant(ctx, tenantID, eventID)
}

// repairCancelled es la rama IDEMPOTENTE del cancel sobre un evento ya terminal, y
// además la REPARACIÓN de su costura de fallo parcial (Plan 043 · Ola 4).
//
// El orden del hecho es transición → abandono → puntero, así que un fallo a mitad
// deja el evento `cancelled` con la solicitud todavía `open` y/o el puntero puesto.
// Antes esta rama devolvía la fila tal cual y ahí se acababa: el reintento del dueño
// recorría el camino idempotente sin reparar nada, y la solicitud huérfana TAMPOCO
// era descartable a mano —`POST /api/v1/intakes/discard` la salta con `live_event`
// porque su guarda mira el `cart` del flow_state y no el estado del evento
// (`intakes/postgres.go:hasLiveCartTx`, aproximación cuya sustitución el Plan 041
// dejó anotada a nombre de T4.3)—. Con las dos puertas cerradas, la única salida era
// un reconciliador. Reintentar el cancel es esa salida, y no hace falta nada más.
//
// SOLO para `cancelled`. Un evento `closed` (fin natural) JAMÁS abandona su
// solicitud: la cerró la proyección de cart_closed y abandonarla aquí pisaría ese
// hecho —es la misma regla que closeIfFinished respeta arriba—.
//
// 🔴 EL #29 LE AÑADE POBLACIÓN, Y SE ACEPTA A PROPÓSITO (mirado con lupa, no
// heredado): desde el hallazgo #29 hay una SEGUNDA forma de llegar a `cancelled` —la
// clienta cancelando dentro de su pedido, vía closeIfFinished—, así que un cancel por
// id sobre uno de esos eventos ya NO cae en el return temprano de arriba y entra aquí.
// Se deja entrar, por tres hechos comprobados:
//
//   - En el estado asentado no escribe NADA. AbandonByEvent lleva `AND status = 'open'`
//     en su WHERE (intakes/postgres.go) y la solicitud de ese pedido ya está
//     `cancelled` por la proyección: cero filas, que por contrato es éxito. Y
//     releaseStateFrom no toca un puntero que ya apagó closeIfFinished en el mismo Save.
//   - Cuando SÍ escribe, escribe lo que hay que escribir. El fan-out que proyecta
//     cart_cancelled es BEST-EFFORT (dispatch loguea y sigue): si su sink falla, la
//     solicitud se queda `open` colgando de un evento ya `cancelled` — el huérfano
//     exacto para el que se construyó esta reparación, solo que llegando por otra
//     puerta. Antes del #29 ese huérfano no tenía NINGUNA salida (el evento quedaba
//     `closed` y el return temprano lo dejaba pasar de largo); ahora reintentar el
//     cancel lo repara, igual que repara el de E-8 §4.
//   - La contrapartida es una ventana estrecha y se nombra en vez de esconderse: el
//     dispatch corre DESPUÉS del Save, así que durante esos milisegundos la solicitud
//     sigue `open`. Un cancel desde la app que caiga justo ahí la dejaría en
//     `abandoned` en vez de `cancelled`. Las dos son terminales, ninguna borra nada
//     (INV-09) y el pedido sigue en la bandeja; distinguirlas exigiría que el evento
//     recordara POR QUÉ murió, que es dato nuevo en la fila y materia del Plan 053.
//
// Las dos reparaciones son seguras de repetir: AbandonByEvent es idempotente por
// contrato (T4.5.5a, D-043.21 — cero filas tocadas es éxito, así que «¿hay algo que
// reparar?» se DELEGA en la propia llamada en vez de leerse de una columna del
// evento, que ya no existe) y releaseStateFrom no toca un puntero que ya mira a
// otro sitio. Sobre una cancelación que salió bien, este camino no escribe nada.
func (rt *Runtime) repairCancelled(ctx context.Context, tenantID string, ev events.Event) (events.Event, error) {
	if ev.Status != events.StatusCancelled {
		return ev, nil
	}
	if rt.intakes != nil {
		if err := rt.intakes.AbandonByEvent(ctx, tenantID, ev.ID); err != nil {
			return events.Event{}, fmt.Errorf("runtime: completar el abandono de la solicitud del evento cancelado: %w", err)
		}
	}
	key := store.Key{TenantID: ev.TenantID, SessionID: ev.SessionID, ContactID: ev.ContactID}
	unlock := rt.locks.lock(key)
	defer unlock()
	if err := rt.releaseStateFrom(ctx, key, ev.ID); err != nil {
		return events.Event{}, err
	}
	return ev, nil
}

// releaseStateFrom apaga flow_state.event_id SI la conversación del evento seguía
// apuntándolo (mismo gesto que stopEvent: st.EventID="" + Save). La clave sale de
// la propia fila del evento (tenant, sesión, contacto) — el evento SABE de qué
// conversación es, y por eso no hace falta SQL nuevo para encontrarla.
//
// Que apunte a OTRO evento (o a ninguno) es normal y no toca nada: la conversación
// siguió su vida —saltó de evento, venció, se escapó— y su puntero ya no es asunto
// de esta cancelación.
func (rt *Runtime) releaseStateFrom(ctx context.Context, key store.Key, eventID string) error {
	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: leer el estado al cancelar su evento: %w", err)
	}
	if !ok || st.EventID != eventID {
		return nil
	}
	st.EventID = ""
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: apagar el puntero del evento cancelado: %w", err)
	}
	return nil
}
