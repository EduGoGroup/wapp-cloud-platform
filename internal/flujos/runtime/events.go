package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// EventStore es el puerto ESTRECHO del runtime hacia el almacén del evento
// conversacional (Plan 043 · Ola 2). Se declara aquí —y no se importa
// *events.Store como tipo concreto— por la misma razón que SelfNumberLister o
// IngestDeduper: el motor depende de interfaces estrechas y así los tests usan un
// doble sin BD. Lo satisface *events.Store tal cual.
//
// nil (sin WithEventStore) desactiva TODO el plano de eventos: un event_start
// arranca su flujo como lo haría una keyword y un event_stop no desactiva nada.
// No-regresión total (INV-6): el motor previo al Plan 043 sigue funcionando igual.
type EventStore interface {
	// CreateEvent inserta el evento VIVO. Devuelve events.ErrAliveExists si el tipo
	// ya está ocupado en esa conversación — lo impone un índice único parcial en BD,
	// no una comprobación previa, así que es la ÚNICA forma fiable de saberlo.
	CreateEvent(ctx context.Context, in events.NewEvent) (events.Event, error)
	// GetAliveByKind devuelve el evento vivo de ese tipo. No haberlo es normal (E-6:
	// el saludo no pare nada), no un error.
	GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (events.Event, bool, error)
	// ListAlive devuelve todos los eventos vivos de la conversación. El runtime lo usa
	// para NOMBRAR el evento activo por su tipo (E-3) sin necesitar una lectura por id.
	ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]events.Event, error)
	// TransitionEvent mueve el evento a un estado terminal con el guard de la BD.
	TransitionEvent(ctx context.Context, eventID string, to events.Status) error
	// Touch refresca last_activity_at: EL reloj de conversación (E-6).
	Touch(ctx context.Context, eventID string) error
	// IsSuspended evalúa la condición DERIVADA «vencido» con el reloj del store.
	// ttl <= 0 ⇒ siempre false (override «sin vencimiento» del tenant).
	IsSuspended(e events.Event, ttl time.Duration) bool
}

// IntakeAbandoner deja en `abandoned` la solicitud que colgaba de un evento que el
// CLIENTE acaba de cerrar eligiendo empezar otro del mismo tipo (ADR-0029 · E-11).
//
// La firma es a propósito distinta de intakes.Service.SetStatus y se adapta en el
// bootstrap: el paquete runtime NO importa internal/intakes (ver la nota de
// webhook_sink.go:158, que evita el mismo acoplamiento). Lo que el adaptador NO
// puede hacer es llamar a Store.UpdateStatus: la puerta es Service.SetStatus, el
// único sitio que consulta CanTransition antes de escribir, y la tabla intakes no
// tiene CHECK que ataje una barbaridad (ADR-0029 · E-11.5).
//
// nil ⇒ el evento se cierra igual pero su solicitud NO se abandona; se LOGUEA. Es
// la mitad del hecho, así que el bootstrap lo cablea siempre.
type IntakeAbandoner interface {
	AbandonIntake(ctx context.Context, tenantID, intakeID string) error
}

// Dispatcher es el puerto hacia el despachador de nivel superior (Plan 043 · T2.3):
// el componente que decide QUÉ ofrecerle a este contacto —tipos que el tenant ofrece
// y eventos suyos que puede retomar— y que NO ejecuta nada. Lo satisface
// *events.Dispatcher.
//
// La división es la del plan y conviene no borrarla: el despachador SOLO LEE; quien
// crea filas, mueve el puntero y habla es el runtime. nil (sin WithDispatcher) ⇒ la
// palabra del menú no presenta nada.
type Dispatcher interface {
	Build(ctx context.Context, ref events.ConversationRef) (events.Menu, error)
}

// FlowForKind resuelve QUÉ FLUJO arranca un tipo de evento para este tenant.
//
// Existe porque el despachador dice «empezar un pedido» sin decir con qué flujo, y
// la respuesta está en la configuración del tenant: la regla kind='event_start' que
// declara ese event_kind. Es una lectura, no una decisión, y por eso es un puerto
// estrecho y no una dependencia del CRUD entero. Vacío ("") ⇒ ese tipo no tiene
// flujo configurado; no es un error.
type FlowForKind interface {
	FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error)
}

// varPendingMenu es la clave de Vars donde vive el menú YA RENDERIZADO mientras se
// espera la elección del cliente. Se persiste el menú entero —no solo un «hay menú
// pendiente»— porque los números que el cliente ve tienen que seguir significando lo
// mismo en el mensaje siguiente: reconstruirlo al vuelo lo recalcularía sobre un
// estado que pudo cambiar, y el «2» del cliente elegiría otra cosa.
const varPendingMenu = "dispatcher_menu"

// eventGesture distingue las DOS cosas que un cliente puede querer decir cuando
// nombra un tipo de evento. No son la misma y confundirlas le cuesta un pedido:
//
//   - gestureGoTo — «carrito». Significa VE al carrito. Si hay uno vivo se conmuta
//     hacia él SIEMPRE, esté o no vencido: el cliente no pidió empezar de cero.
//     Es lo que produce un kind='event_start' y una intención LLM mapeada a un tipo.
//   - gestureNew — «1) Hacer un pedido» en el despachador. Significa uno NUEVO, y
//     solo por eso puede cerrar el viejo (E-11), y solo si el viejo está VENCIDO.
//
// El límite de E-11 es la mitad de la enmienda (ADR-0029 · E-11.3): un evento vivo
// DENTRO de su ventana se conmuta, nunca se cierra — nadie pierde un pedido en curso
// por elegir mal una opción del menú.
type eventGesture int

const (
	gestureGoTo eventGesture = iota
	gestureNew
)

// beginEvent es el nacimiento y el SALTO POR TIPO del evento conversacional
// (Plan 043 · T2.2, D-043.2/D-043.4), idempotente por tipo. Corre bajo el
// single-flight de la clave, que HandleIncoming ya tomó.
//
// Cuatro caminos, y solo uno crea fila:
//
//  1. el evento ACTIVO ya es de tipo K ⇒ NO-OP: ni fila nueva, ni status tocado, ni
//     puntero movido. El turno NO se consume: el texto sigue su curso hacia el
//     módulo, que es lo correcto —quien dice «carrito» estando en el carrito está
//     hablando con el carrito—.
//  2. hay otro vivo de tipo K y el gesto es «ve» (o es «nuevo» pero NO está vencido)
//     ⇒ CONMUTA: flow_state.event_id pasa a apuntarlo y se re-entra a SU flujo. El
//     evento que deja de ser activo sigue `open`: no se pausa, no se cierra, su
//     status ni se toca (D-043.4).
//  3. hay otro vivo de tipo K, el gesto es «nuevo» y está VENCIDO ⇒ E-11: se cancela
//     el viejo, su solicitud queda `abandoned` y nace uno nuevo. Lo escribe una
//     elección de la persona, no un reloj.
//  4. no hay ninguno vivo de tipo K ⇒ NACE (E-6: tarde, y solo por estas puertas).
//
// El bool devuelto dice si el turno se consumió. false ⇒ el llamante sigue con su
// camino normal (solo ocurre en el caso 1 y cuando no hay plano de eventos).
func (rt *Runtime) beginEvent(ctx context.Context, key store.Key, sessionID string, dec trigger.Decision, g eventGesture, active string) (bool, error) {
	if rt.events == nil || dec.EventKind == "" {
		// Sin plano de eventos cableado, un event_start se comporta como la keyword
		// que siempre fue: arranca su flujo y no pare nada (INV-6).
		return false, nil
	}
	ev, alive, err := rt.events.GetAliveByKind(ctx, key.TenantID, sessionID, key.ContactID, dec.EventKind)
	if err != nil {
		return false, fmt.Errorf("runtime: buscar evento vivo de tipo %q: %w", dec.EventKind, err)
	}
	if alive && ev.ID == active && g == gestureGoTo {
		// Caso 1: ya está donde pidió estar. Nada que escribir y turno NO consumido.
		rt.log.Debug("runtime: event_start sobre el evento que ya estaba activo; no-op",
			"session_id", sessionID, "event_kind", dec.EventKind)
		return false, nil
	}
	// A partir de aquí SÍ se escribe y SÍ se habla, así que la red anti-loop (Plan
	// 020 · T0) se cobra su token: después de descartar el no-op —que no responde y
	// no debe gastar cuota— y ANTES de tocar la base, porque parir un evento al que
	// no se va a poder contestar es peor que no parirlo.
	if !rt.replyAllowed(key) {
		return true, nil
	}
	if alive {
		reuse, cerr := rt.reuseOrRetire(ctx, key.TenantID, ev, g)
		if cerr != nil {
			return false, cerr
		}
		if reuse {
			return true, rt.switchToEvent(ctx, key, sessionID, ev)
		}
	}
	return true, rt.birthEvent(ctx, key, sessionID, dec)
}

// StartNewOfKind es la TERCERA puerta del nacimiento tardío (T2.5): la elección
// EXPLÍCITA en el despachador de empezar uno nuevo del tipo K. Es el único camino
// que lleva el gesto «nuevo», y por tanto el único por el que puede aplicarse E-11.
//
// Se llama con el keyedMutex de la clave YA TOMADO (el despachador corre dentro de
// HandleIncoming): re-tomarlo aquí sería auto-deadlock, igual que llamar a Start
// desde handleTrigger. Devuelve si el turno se consumió.
//
// «Nuevo» no significa «cierra lo que haya»: si el evento vivo de ese tipo está
// DENTRO de su ventana se conmuta hacia él y no se cierra nada (E-11.3). Solo un
// evento vivo y VENCIDO cede su sitio, y lo cede ante una persona que pulsó una
// opción, nunca ante un reloj.
func (rt *Runtime) StartNewOfKind(ctx context.Context, key store.Key, sessionID, kind, flowID, activeEventID string) (bool, error) {
	dec := trigger.Decision{Action: trigger.StartEvent, EventKind: kind, FlowID: flowID}
	return rt.beginEvent(ctx, key, sessionID, dec, gestureNew, activeEventID)
}

// liveEventSwitch atiende un entrante que llega SOBRE una conversación viva y
// pregunta al resolver si ese texto es una orden de navegación entre eventos
// (Plan 043 · T2.2/T2.4). Devuelve true si el turno se consumió.
//
// Solo se consultan event_start y event_stop —eso lo garantiza ResolveLive, no
// esto—: keyword, fallback y llm NO se evalúan con conversación viva, de modo que
// INV-02 sigue intacto para todo lo demás y el texto sigue siendo del módulo.
//
// Un fallo del resolver es BEST-EFFORT, igual que IsEscape: se LOGUEA y la
// conversación avanza con normalidad. Preferimos un salto perdido a una
// conversación bloqueada por un error transitorio.
func (rt *Runtime) liveEventSwitch(ctx context.Context, tenantID, sessionID string, key store.Key, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	if rt.events == nil {
		return false, nil
	}
	// La elección del menú se mira ANTES que los disparos: es la respuesta directa a
	// la pregunta que acabamos de hacerle al cliente, y solo se consulta mientras el
	// evento ACTIVO es el propio menú — así el «2» de un carrito nunca se confunde con
	// el «2» de una lista que ya nadie tiene delante.
	if done, cerr := rt.menuChoice(ctx, key, sessionID, st, m); cerr != nil || done {
		return done, cerr
	}
	dec, err := rt.triggers.ResolveLive(ctx, tenantID, sessionID, m.GetText())
	if err != nil {
		rt.log.Warn("runtime: ResolveLive falló; se ignora el salto de evento",
			"error", err, "session_id", sessionID)
		return false, nil
	}
	switch dec.Action {
	case trigger.StartEvent:
		return rt.beginEvent(ctx, key, sessionID, dec, gestureGoTo, st.EventID)
	case trigger.StopEvent:
		return true, rt.stopEvent(ctx, key, sessionID, st)
	default:
		return false, nil
	}
}

// reuseOrRetire decide, para un evento YA VIVO del tipo pedido, si se conmuta hacia
// él (true) o si el gesto del cliente lo retira para dejar sitio a uno nuevo (false).
// Es donde vive el límite de E-11 y por eso está aislada: la condición «vencido» es
// DERIVADA (E-6) y se LEE, nunca se persiste.
func (rt *Runtime) reuseOrRetire(ctx context.Context, tenantID string, ev events.Event, g eventGesture) (bool, error) {
	if g == gestureGoTo {
		return true, nil
	}
	ttl, err := rt.eventInactivityTTL(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if !rt.events.IsSuspended(ev, ttl) {
		// E-11.3: vivo y DENTRO de su ventana ⇒ se conmuta, no se cierra. Nadie pierde
		// un pedido en curso por tocar una opción del menú.
		return true, nil
	}
	return false, rt.retireForNew(ctx, tenantID, ev)
}

// retireForNew aplica E-11: el cliente eligió empezar de nuevo sobre un evento
// VENCIDO suyo, así que ese acto deliberado lo cierra. El evento pasa a `cancelled`
// y su solicitud a `abandoned` — un SOLO hecho (E-8 §4): si el evento no se pudo
// cerrar, la solicitud no se toca.
//
// El pedido NO se borra (INV-09): sigue en la bandeja y sigue exportable.
func (rt *Runtime) retireForNew(ctx context.Context, tenantID string, ev events.Event) error {
	if err := rt.events.TransitionEvent(ctx, ev.ID, events.StatusCancelled); err != nil {
		if errors.Is(err, events.ErrNotOpen) {
			// Otro escritor lo cerró entre el SELECT y el UPDATE: el tipo quedó libre,
			// que es justo lo que queríamos. Carrera benigna.
			rt.log.Info("runtime: el evento vencido ya no estaba open al retirarlo (carrera benigna)",
				"event_kind", ev.Kind)
			return nil
		}
		return fmt.Errorf("runtime: cerrar el evento vencido para empezar otro (E-11): %w", err)
	}
	if ev.IntakeID == "" {
		return nil
	}
	if rt.intakes == nil {
		rt.log.Warn("runtime: evento cerrado por E-11 pero su solicitud NO se abandonó (sin IntakeAbandoner cableado)",
			"tenant_id", tenantID, "event_kind", ev.Kind)
		return nil
	}
	if err := rt.intakes.AbandonIntake(ctx, tenantID, ev.IntakeID); err != nil {
		return fmt.Errorf("runtime: abandonar la solicitud del evento cerrado (E-11): %w", err)
	}
	return nil
}

// birthEvent crea el evento y arranca su flujo. El orden es deliberado: la fila
// nace ANTES de hablarle al cliente, de modo que no exista una respuesta que
// mencione un evento que no está en la base.
//
// El texto de esa primera respuesta lo produce el flujo del tenant y NO lleva el
// history_id (E-3): el identificador legible existe para que un humano hable de
// «ese pedido» en la bandeja, no para que el cliente lo memorice.
func (rt *Runtime) birthEvent(ctx context.Context, key store.Key, sessionID string, dec trigger.Decision) error {
	ev, err := rt.events.CreateEvent(ctx, events.NewEvent{
		TenantID:  key.TenantID,
		SessionID: sessionID,
		ContactID: key.ContactID,
		Kind:      dec.EventKind,
		FlowID:    dec.FlowID,
	})
	if err != nil {
		if errors.Is(err, events.ErrAliveExists) {
			// Carrera con otro entrante de la misma conversación: alguien lo parió entre
			// nuestro SELECT y nuestro INSERT. No es un fallo — es exactamente la regla
			// que el índice único parcial existe para imponer.
			rt.log.Info("runtime: el evento ya existía al crearlo (carrera benigna)",
				"session_id", sessionID, "event_kind", dec.EventKind)
			return nil
		}
		return fmt.Errorf("runtime: crear evento de tipo %q: %w", dec.EventKind, err)
	}
	return rt.enterEventFlow(ctx, key, sessionID, ev.ID, dec.EventKind, dec.FlowID, dec.Params, dec.IntentName)
}

// switchToEvent conmuta la conversación hacia un evento que YA estaba vivo: apunta
// el puntero, refresca su reloj y re-entra al flujo con el que ese evento nació
// (ev.FlowID, congelado en la fila para que el salto no dependa de qué regla lo
// trajo). El status del evento que se abandona NO se toca (D-043.4).
//
// El progreso de nodo del evento que se deja atrás se pierde: flow_state tiene UNA
// fila por conversación. Es el precio aceptado por el diseño, que restaura desde la
// VERDAD DURABLE del módulo (las líneas del intake, las respuestas de la encuesta),
// no desde el puntero de nodo.
func (rt *Runtime) switchToEvent(ctx context.Context, key store.Key, sessionID string, ev events.Event) error {
	if err := rt.events.Touch(ctx, ev.ID); err != nil {
		return fmt.Errorf("runtime: refrescar el reloj del evento al conmutar: %w", err)
	}
	return rt.enterEventFlow(ctx, key, sessionID, ev.ID, ev.Kind, ev.FlowID, nil, "")
}

// enterEventFlow deja la conversación dentro del evento eventID: arranca (o
// re-arranca) flowID y estampa el puntero flow_state.event_id.
//
// Se borra el flow_state previo a propósito: startLocked rechaza con
// ErrConversationExists si ya hay uno, y un salto por tipo es precisamente cambiar
// de conversación. Lo que se borra es el PUNTERO DE NODO del motor, nunca datos de
// negocio: el evento que se abandona sigue `open` con su intake intacto.
//
// flowID vacío ⇒ no hay flujo que arrancar y solo se estampa el puntero. Es el caso
// del tipo `menu`, cuyo contenido lo renderiza el despachador y no una fila de
// flow_definitions (D-043.3).
func (rt *Runtime) enterEventFlow(ctx context.Context, key store.Key, sessionID, eventID, kind, flowID string, params map[string]string, intentName string) error {
	if kind == trigger.EventKindMenu {
		// El menú NO es una fila de flow_definitions (D-043.3): su contenido es dinámico
		// por contacto, así que lo renderiza el despachador y no el motor.
		return rt.presentMenu(ctx, key, sessionID, eventID)
	}
	if flowID != "" {
		if err := rt.store.Delete(ctx, key); err != nil {
			return fmt.Errorf("runtime: liberar el estado previo al entrar al evento: %w", err)
		}
		if _, err := rt.startLocked(ctx, key.TenantID, flowID, sessionID, key, key.ContactID, params, intentName); err != nil {
			return fmt.Errorf("runtime: arrancar el flujo del evento: %w", err)
		}
	}
	return rt.pointStateAtEvent(ctx, key, eventID)
}

// pointStateAtEvent estampa flow_state.event_id (D-043.4). Va DESPUÉS de arrancar el
// flujo porque startLocked escribe el estado desde cero y borraría el puntero si se
// estampara antes; el single-flight de la clave garantiza que nadie observa el hueco
// dentro del proceso.
//
// Sin flow_state (flujo vacío, o un arranque que no persistió) no hay dónde estampar
// y se LOGUEA: el evento existe y es rescatable por su tipo, que es lo que importa.
func (rt *Runtime) pointStateAtEvent(ctx context.Context, key store.Key, eventID string) error {
	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: releer el estado para apuntar al evento: %w", err)
	}
	if !ok {
		rt.log.Debug("runtime: sin flow_state donde apuntar el evento activo",
			"session_id", key.SessionID)
		return nil
	}
	st.EventID = eventID
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: apuntar el evento activo: %w", err)
	}
	return nil
}

// stopEvent DESACTIVA el evento activo sin matarlo (Plan 043 · T2.4, D-043.2): apaga
// flow_state.event_id y deja la fila en `open`, rescatable diciendo su tipo.
//
// Lo que NO hace, y es la tarea entera: no llama a TransitionEvent, no toca `status`
// y no borra el flow_state. `event_stop` no es el escape global —ese sigue con su
// semántica EXACTA en handleEscape, borrando el estado— sino dejar de atender un
// evento. El resumen que acompaña a la confirmación lo conecta la Ola 3.
//
// La confirmación nombra el TIPO, nunca el history_id (E-3).
func (rt *Runtime) stopEvent(ctx context.Context, key store.Key, sessionID string, st model.Conversation) error {
	if st.EventID == "" {
		// Nada que desactivar: el turno se consumió igual (el cliente dijo la palabra),
		// pero no hay estado que escribir ni nada que confirmar.
		return nil
	}
	kind := rt.activeEventKind(ctx, key, sessionID, st.EventID)
	st.EventID = ""
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: desactivar el evento activo: %w", err)
	}
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destino(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, []engine.Output{{Text: stopNotice(kind)}})
	return err
}

// stopNotice arma la confirmación de event_stop. Nombra el TIPO del evento que se
// deja de atender y JAMÁS su history_id (E-3): el identificador legible es para la
// bandeja del negocio, no para la conversación.
//
// El nombre sale de events.KindName —«carrito», «encuesta», «documentos»—, el MISMO
// que usa el despachador para rotular sus opciones: si el menú ofrece «Retomar el
// pedido» y la confirmación hablara de un «cart», serían dos vocabularios para el
// mismo objeto en la misma conversación. Sin tipo conocido cae a una frase genérica
// antes que mentir con un nombre.
func stopNotice(kind string) string {
	if kind == "" {
		return "Listo, lo dejamos aquí. Sigue abierto por si quieres retomarlo."
	}
	// No se le promete NINGUNA palabra concreta para volver: la que sirve es la que el
	// tenant configuró en su regla event_start, que puede no coincidir con el nombre
	// de cara al cliente (hoy el tipo `cart` se llama «pedido» y la palabra puede ser
	// «carrito»). Prometer una palabra que quizá no dispare nada sería peor que no
	// prometer ninguna.
	return fmt.Sprintf("Listo, dejamos el %s por ahora. Sigue abierto: puedes retomarlo cuando quieras.", events.KindName(kind))
}

// activeEventKind resuelve el TIPO del evento activo para poder nombrarlo en la
// confirmación (E-3: se nombra el tipo, jamás el history_id). Es BEST-EFFORT: si no
// se puede averiguar devuelve "" y el aviso cae al genérico — quedarse sin confirmar
// por no saber cómo llamarlo sería peor que confirmar sin nombre.
//
// Va por ListAlive —UNA consulta— y no recorriendo los tipos de fábrica preguntando
// por cada uno, que serían cuatro consultas para responder a la misma pregunta.
func (rt *Runtime) activeEventKind(ctx context.Context, key store.Key, sessionID, eventID string) string {
	alive, err := rt.events.ListAlive(ctx, key.TenantID, sessionID, key.ContactID)
	if err != nil {
		rt.log.Warn("runtime: no se pudo resolver el tipo del evento activo; se usa el aviso genérico",
			"error", err, "session_id", sessionID)
		return ""
	}
	for _, ev := range alive {
		if ev.ID == eventID {
			return ev.Kind
		}
	}
	return ""
}

// eventInactivityTTL lee EL reloj de conversación del tenant (E-6). Un fallo de
// settings NO se traga: a diferencia del TTL conversacional —donde no vencer es el
// lado seguro— aquí el valor decide si se CIERRA un evento del cliente, y cerrar por
// un fallo transitorio de lectura sería destruir un pedido por un timeout.
func (rt *Runtime) eventInactivityTTL(ctx context.Context, tenantID string) (time.Duration, error) {
	settings, err := rt.store.GetTenantSettings(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("runtime: leer el TTL de inactividad del evento: %w", err)
	}
	return settings.EventInactivityTTL, nil
}

// presentMenu construye el menú del despachador, lo envía y lo DEJA PENDIENTE en el
// estado para poder interpretar la respuesta del mensaje siguiente.
//
// Un menú vacío no se manda: si el tenant no ofrece nada y el contacto no tiene nada
// que retomar, mandar una lista sin opciones sería peor que callar.
func (rt *Runtime) presentMenu(ctx context.Context, key store.Key, sessionID, eventID string) error {
	if rt.dispatcher == nil {
		rt.log.Debug("runtime: sin despachador cableado; el menú no presenta nada", "session_id", sessionID)
		return nil
	}
	menu, err := rt.dispatcher.Build(ctx, events.ConversationRef{
		TenantID: key.TenantID, SessionID: sessionID, ContactID: key.ContactID,
	})
	if err != nil {
		return fmt.Errorf("runtime: construir el menú del despachador: %w", err)
	}
	if menu.Empty() {
		return nil
	}
	raw, err := menu.Encode()
	if err != nil {
		return fmt.Errorf("runtime: serializar el menú pendiente: %w", err)
	}
	if err := rt.saveMenuState(ctx, key, eventID, string(raw)); err != nil {
		return err
	}
	to, err := rt.destino(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, []engine.Output{{Text: menu.Render()}})
	return err
}

// saveMenuState deja el estado apuntando al evento del menú y con el menú pendiente
// en Vars. Crea el flow_state si no existía: un contacto puede pedir el menú sin
// tener ninguna conversación abierta, y ese estado NO tiene flujo — es legítimo, y
// por eso advanceLive comprueba que haya definición antes de ir al motor.
func (rt *Runtime) saveMenuState(ctx context.Context, key store.Key, eventID, raw string) error {
	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: cargar estado para el menú: %w", err)
	}
	if !ok {
		st = model.Conversation{TenantID: key.TenantID, SessionID: key.SessionID, ContactID: key.ContactID}
	}
	if st.Vars == nil {
		st.Vars = map[string]any{}
	}
	st.Vars[varPendingMenu] = raw
	st.EventID = eventID
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar el menú pendiente: %w", err)
	}
	return nil
}

// menuChoice interpreta la respuesta del cliente contra el menú pendiente. Devuelve
// true si el turno se consumió.
//
// Solo actúa si el evento ACTIVO es aquel para el que se pintó el menú: en cuanto el
// cliente elige, el activo pasa a ser otro y los números dejan de significar nada.
// Sin esa guarda, el «2» con el que alguien elige un artículo del carrito se leería
// como la opción 2 de un menú que ya nadie tiene delante.
func (rt *Runtime) menuChoice(ctx context.Context, key store.Key, sessionID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	raw, ok := st.Vars[varPendingMenu].(string)
	if !ok || raw == "" {
		return false, nil
	}
	menu, err := events.DecodeMenu([]byte(raw))
	if err != nil {
		// Un menú ilegible no bloquea la conversación: se descarta y el texto sigue su
		// camino normal.
		rt.log.Warn("runtime: menú pendiente ilegible; se ignora", "error", err, "session_id", sessionID)
		return false, nil
	}
	choice, ok := menu.Resolve(m.GetText())
	if !ok {
		// No era un número del menú. El menú es un atajo, no una cárcel.
		return false, nil
	}
	switch choice.Action {
	case events.ActionResume:
		return true, rt.resumeEvent(ctx, key, sessionID, choice.EventID)
	case events.ActionStart:
		flowID, ferr := rt.flowForKind(ctx, key.TenantID, sessionID, choice.Kind)
		if ferr != nil {
			return false, ferr
		}
		dec := trigger.Decision{Action: trigger.StartEvent, EventKind: choice.Kind, FlowID: flowID}
		// Gesto NUEVO: es la única puerta por la que E-11 puede cerrar un vencido.
		return rt.beginEvent(ctx, key, sessionID, dec, gestureNew, st.EventID)
	default:
		return false, nil
	}
}

// resumeEvent retoma un evento vivo elegido por su número en el menú. Se relee de la
// lista de vivos en vez de fiarse del id que venía en el menú: entre que se pintó y
// que el cliente contestó, ese evento pudo cerrarse desde la app del dueño, y
// retomar un evento terminal sería resucitarlo por la puerta de atrás.
func (rt *Runtime) resumeEvent(ctx context.Context, key store.Key, sessionID, eventID string) error {
	alive, err := rt.events.ListAlive(ctx, key.TenantID, sessionID, key.ContactID)
	if err != nil {
		return fmt.Errorf("runtime: releer los eventos vivos al retomar: %w", err)
	}
	for _, ev := range alive {
		if ev.ID == eventID {
			if !rt.replyAllowed(key) {
				return nil
			}
			return rt.switchToEvent(ctx, key, sessionID, ev)
		}
	}
	rt.log.Info("runtime: el evento elegido ya no está vivo; no se retoma",
		"session_id", sessionID)
	return nil
}

// flowForKind resuelve el flujo que arranca un tipo. Sin puerto cableado, o sin regla
// para ese tipo, devuelve "" y el evento nace sin flujo (el caso del menú).
func (rt *Runtime) flowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	if rt.flows == nil {
		return "", nil
	}
	flowID, err := rt.flows.FlowForKind(ctx, tenantID, sessionID, kind)
	if err != nil {
		return "", fmt.Errorf("runtime: resolver el flujo del tipo %q: %w", kind, err)
	}
	return flowID, nil
}
