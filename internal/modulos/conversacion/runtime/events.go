// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// events.go es el PLANO DE EVENTOS del runtime (Plan 043): los puertos por los que el motor
// habla con el evento conversacional y la lógica que hace nacer un evento, saltar entre
// eventos, desactivarlo, resumir lo que se abandona y presentar el menú del despachador.
//
// Va PARTIDO POR TEMA para caber en E-13 (≤ 500 líneas por fichero, con tolerancia hasta 600),
// solo moviendo declaraciones y con el sufijo del origen:
//
//   - events.go           — los puertos, el gesto y beginEvent (el salto por tipo, EV-3), y la
//     relectura del evento activo;
//   - events_start_new.go — StartNewOfKind, E-11 y el nacimiento (EV-2);
//   - events_switch.go    — la navegación sobre conversación viva, la conmuta, la entrada al
//     evento con sus dos punteros (EV-4) y event_stop (EV-5);
//   - events_clock.go     — el reloj del evento (EV-6);
//   - events_menu.go      — el menú del despachador, la oferta y el rescate (EV-7);
//   - events_summary.go   — el resumen del abandono y la coletilla (EV-8).
//
// # Reglas del plano (las prueban los tests de events*_test.go, por HandleIncoming)
//
// EV-1 · Sin WithEventStore no hay plano: un event_start arranca su flujo como una keyword,
// ningún camino crea, toca ni transiciona un evento, y no se escribe hilo (INV-6).
//
// EV-2 · Nacimiento TARDÍO, por TRES puertas y ninguna más (E-6): la regla event_start, la
// intención mapeada a un event_kind y la elección en el despachador (StartNewOfKind). El
// `fallback` queda fuera aunque su regla traiga event_kind: un saludo no pare fila ni arranca
// reloj. La fila nace ANTES de hablarle al cliente y con el flujo Y SU VERSIÓN vigentes; un
// tipo sin flujo (el `menu`) nace con versión 0. Si la versión no se puede leer, el evento no
// nace y el error sube.
//
// EV-3 · Los cuatro caminos de un salto por tipo K, con la conversación ya bajo su candado:
//
//  1. el evento ACTIVO ya es de tipo K y el gesto es «ve»: no-op. Ni fila, ni estado, ni
//     token; el turno NO se consume y el texto sigue hacia el módulo. Debug «runtime:
//     event_start sobre el evento que ya estaba activo; no-op».
//  2. hay otro vivo de tipo K y el gesto es «ve», o es «nuevo» pero está DENTRO de su
//     ventana: CONMUTA. Touch del evento, efecto event_switched, resumen de lo que llevaba
//     (best-effort), re-entrada a SU flujo congelado. El evento que deja de ser activo sigue
//     `open`.
//  3. hay otro vivo de tipo K, el gesto es «nuevo» y está VENCIDO: E-11. El viejo pasa a
//     `cancelled` (event_cancelled), su solicitud a `abandoned`, y nace uno nuevo.
//  4. no hay ninguno vivo de tipo K: NACE (event_started).
//
// Los caminos 2, 3 y 4 cobran UN token del limitador antes de escribir nada (RT-8); agotado,
// el turno se consume sin tocar la base. Perder la carrera del INSERT contra otro entrante
// (events.ErrAliveExists) y la del UPDATE (events.ErrNotOpen) son benignas: Info y nil.
//
// EV-4 · RT-16 (O5, Plan 053) · La fila de estado lleva DOS punteros y son dos preguntas: el
// ACTIVO (Conversation.EventID: a quién le habla el contacto) y el DUEÑO
// (Conversation.OwnerEventID: de quién es el flujo que la fila carga). El activo se estampa
// siempre que se entra a un evento. El dueño SOLO cuando ese camino acaba de borrar el
// estado y arrancar el flujo PARA ese evento; nunca al montar el menú sobre un estado
// heredado, ni al entrar a un evento sin flujo. El cierre natural cierra AL DUEÑO: un `cart`
// heredado bajo un `menu` activo cierra SU evento y deja el `menu` open y activo (INV-053.2).
//
// EV-5 · event_stop DESACTIVA sin matar: apaga el activo, conserva el dueño, el flujo y las
// Vars, no transiciona nada, emite event_deactivated y confirma por NOMBRE DE TIPO, nunca
// por history_id. El resumen, el Save y el efecto van ANTES del token: sin cupo no se avisa,
// pero la desactivación ya ocurrió. Textos, byte a byte:
// "Listo, dejamos el %s por ahora. Sigue abierto: puedes retomarlo cuando quieras." (con
// events.KindName del tipo) y, sin tipo conocido,
// "Listo, lo dejamos aquí. Sigue abierto por si quieres retomarlo.".
//
// EV-6 · RT-15 (T61) · El reloj del evento. Con evento activo y vivo: dentro de la ventana
// (event_inactivity_ttl_seconds) el entrante hace Touch; vencida, se borra el estado, se emite
// event_inactivity_expired y el entrante se trata como uno NUEVO. El evento NO se toca: sigue
// `open` y rescatable. Quién decide «vencido», y con qué instante se sella last_activity_at,
// es el reloj del almacén de eventos (events.WithClock), no el del runtime. Un evento activo
// que ya no está vivo ni vence ni refresca. Un fallo al leer el TTL NO se traga: sube.
//
// EV-7 · El menú del despachador. Un menú vacío no se envía. El menú enviado queda PENDIENTE
// en Vars, entero y SELLADO con el evento sobre el que se armó ("" incluido): el número del
// mensaje siguiente solo se interpreta si el sello coincide con el activo de ese turno; si no,
// el menú caduca en silencio. Un estado TERMINAL cuenta como «ninguna conversación» al
// montarlo (se conserva solo last_wa_message_id). Un menú ilegible se descarta con WARN.
// Retomar relee los RESCATABLES (no los vivos): un evento cuyo pedido se descartó no se
// resucita (INV-17). El rescate cobra UN token para sus dos salientes.
//
// EV-8 · El resumen del abandono se escribe ANTES de destruir o apagar el estado, en los tres
// abandonos (salto por tipo, event_stop, escape), y es best-effort. La coletilla de «tienes
// algo a medias» se resuelve ANTES de crear el evento nuevo —si no, se anunciaría a sí mismo—,
// se pega al último texto (no es un saliente aparte) y se marca en el mismo Save.
//
// EV-9 · RT-17 (W45) · La cadena entera —regla de disparo → evento → engine → sink →
// proyector— funciona con lo que el propio runtime escribe: nadie siembra a mano el puntero
// del estado ni la ligadura del contenido con su evento.
//
// EV-10 · RT-11 · 🔴 El candado de rachas. Todo borrado del estado (rt.store.Delete) va
// seguido, en el mismo bloque y a no más de 3 sentencias, del cierre de su racha
// (rt.autoreplyStreaks.Close). Hoy son 6 caminos en el paquete, dos de ellos de este fichero
// (viejo: events.go:716, la entrada a un evento con flujo, y events.go:1221, la suelta por
// inactividad del evento) y cuatro del entrante (incoming.go:239, 450, 489 y 1077). Lo vigila
// el candado AST streak_invariante_test.go, cuya constante se re-mide sobre el código nuevo.

// EventStore es el puerto ESTRECHO del runtime hacia el almacén del evento conversacional
// (Plan 043 · Ola 2). Lo satisface *events.Store tal cual, y eventshelpertest.Store en tests.
//
// nil (sin WithEventStore) desactiva TODO el plano de eventos (EV-1).
type EventStore interface {
	// CreateEvent inserta el evento VIVO. Devuelve events.ErrAliveExists si el tipo ya está
	// ocupado en esa conversación: lo impone un índice único parcial, no una comprobación
	// previa, así que es la única forma fiable de saberlo.
	CreateEvent(ctx context.Context, in events.NewEvent) (events.Event, error)
	// GetAliveByKind devuelve el evento vivo de ese tipo. No haberlo es normal, no un error.
	GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (events.Event, bool, error)
	// ListAlive devuelve todos los eventos vivos de la conversación. El runtime lo usa para
	// releer el evento activo por su id.
	ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]events.Event, error)
	// ListRescuable devuelve los vivos que además SIGUEN siendo rescatables: los que no
	// cuelgan de una solicitud descartada, cancelada o ya cuajada (INV-17). limit <= 0 es
	// «sin tope».
	ListRescuable(ctx context.Context, tenantID, sessionID, contactID string, limit int) ([]events.Rescuable, error)
	// GetEventForTenant lee UN evento por id ACOTADO al tenant: el aislamiento va en el SQL.
	// events.ErrEventNotFound cubre toda ausencia, sea un id inexistente o de otro tenant.
	GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
	// TransitionEvent mueve el evento a un estado terminal con el guard de la base
	// (events.ErrNotOpen si ya no estaba open).
	TransitionEvent(ctx context.Context, eventID string, to events.Status) error
	// Touch refresca last_activity_at: EL reloj de conversación (E-6).
	Touch(ctx context.Context, eventID string) error
	// AppendSummary escribe el resumen del evento que se abandona (T3.4).
	AppendSummary(ctx context.Context, eventID string, body json.RawMessage) (int, error)
	// AppendDecision escribe una DECISIÓN estructurada del cliente en el hilo, en claro. La
	// produce el PersistSink, no el runtime; está en este puerto porque la satisface el mismo
	// almacén.
	AppendDecision(ctx context.Context, eventID string, payload []byte) error
	// AppendMessage escribe el TEXTO LITERAL de un turno en el hilo, siempre cifrado (sin
	// cifrador devuelve events.ErrNoCipher; jamás degrada a claro). El runtime la llama solo
	// detrás de `llm_intake` (thread.go).
	AppendMessage(ctx context.Context, eventID string, role events.Role, body string) (int, error)
	// AppendOutOfTurnMessage escribe un SALIENTE FUERA DE TURNO, con el mismo cifrado y la
	// MARCA que permite tratarlo como contexto y no como pedido. Sin rol en la firma: es la
	// voz del negocio y la clava el almacén.
	AppendOutOfTurnMessage(ctx context.Context, eventID string, body string) (int, error)
	// IsSuspended evalúa la condición DERIVADA «vencido» con el reloj del almacén. ttl <= 0
	// es siempre false.
	IsSuspended(e events.Event, ttl time.Duration) bool
}

// IntakeAbandoner deja en `abandoned` la solicitud que colgaba de un evento que se acaba de
// cancelar: por E-11 (EV-3, camino 3) o por el cancel por id de la app (CancelEventForTenant).
//
// La firma es POR EVENTO (D-043.21): se pide «abandona el intake `open` que DECLARE este
// event_id». Cero filas tocadas es ÉXITO IDEMPOTENTE: cubre el evento sin solicitud, la
// solicitud ya abandonada y el reintento de una cancelación a medias. Una transición
// imposible de verdad —abandonar una `confirmed`— sí sale como error.
//
// La implementación vive en el dominio de solicitudes y la adapta el arranque: este paquete
// no la importa. nil ⇒ el evento se cancela igual, su solicitud NO se abandona y se avisa a
// WARN «runtime: evento cancelado sin IntakeAbandoner cableado; si tenía solicitud, quedó sin
// abandonar».
type IntakeAbandoner interface {
	AbandonByEvent(ctx context.Context, tenantID, eventID string) error
}

// Dispatcher es el puerto hacia el despachador de nivel superior (Plan 043 · T2.3): decide QUÉ
// ofrecerle a este contacto —los tipos que el tenant ofrece y los eventos suyos que puede
// retomar— y NO ejecuta nada. Lo satisface *events.Dispatcher.
//
// El despachador solo LEE; quien crea filas, mueve el puntero y habla es el runtime. nil ⇒ el
// evento `menu` nace pero no presenta nada (Debug «runtime: sin despachador cableado; el menú
// no presenta nada»). Un error de Build sube y corta el turno.
type Dispatcher interface {
	Build(ctx context.Context, ref events.ConversationRef) (events.Menu, error)
}

// OpeningBuilder es el puerto hacia el constructor de la ENTRADA de una conversación SIN
// evento (Plan 043 · T3.8, REQ-27): lo que se le ofrece a quien escribió algo que no casó
// nada. Lo satisface el mismo *events.Dispatcher.
//
// Un events.Offering trae las DOS mitades que el runtime necesita: Text, que se ENVÍA, y Menu,
// que se PERSISTE pendiente para interpretar el número del mensaje siguiente. El caso vacío
// (Offering.Empty) no es una lista vacía que enseñar: es la señal de caer al `fallback` de
// siempre (INV-20), lo que impide que un tenant recién creado se quede mudo.
//
// nil ⇒ el fallback se comporta como antes del Plan 043 (RT-12).
type OpeningBuilder interface {
	// BuildOpening arma la entrada de una conversación sin evento. Un error se loguea a WARN
	// («runtime: no se pudo construir la entrada que ofrece») y se cae al fallback.
	BuildOpening(ctx context.Context, ref events.ConversationRef) (events.Offering, error)
	// BuildRescue arma la lista de lo que este contacto puede RETOMAR. Elegir «retomar» en la
	// entrada abre esta lista: son dos pasos. Vacía ⇒ el turno NO se consume y el texto sigue
	// su camino. Un error sube.
	BuildRescue(ctx context.Context, ref events.ConversationRef) (events.Offering, error)
	// BuildTagline arma la coletilla que avisa de que hay algo a medias. Cadena vacía ⇒ no
	// se dice nada. Un error se loguea a WARN y se responde sin ella.
	BuildTagline(ctx context.Context, ref events.ConversationRef) (string, error)
}

// FlowForKind resuelve QUÉ FLUJO arranca un tipo de evento para este tenant: lo dice la regla
// kind='event_start' que declara ese event_kind. La necesita la elección «empezar uno nuevo»
// del menú, que dice el tipo pero no el flujo.
//
// Vacío ("") sin error = ese tipo no tiene flujo configurado: el evento nace sin flujo. Un
// error sube y corta el turno. nil (sin WithFlowForKind) equivale a contestar siempre "".
type FlowForKind interface {
	FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error)
}

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
//
// # EL PRIMER VALOR: EL ID DEL EVENTO EN EL QUE QUEDA LA CONVERSACIÓN (Plan 044)
//
// Esta función es EL ÚNICO SITIO del camino de disparo donde el `event_id` existe: nace
// en el `CreateEvent` de birthEvent o se recupera del vivo en switchToEvent. Se devuelve
// —en vez de que el 044 se cuele dentro de esas dos— para que la ventana de captación
// pueda anclar en él el mensaje que ARRANCA el evento (startFromDecision, incoming.go).
// Vale "" en los CUATRO casos en los que no hay un evento sobre el que sea legítimo
// anclar la ventana:
//
//   - sin plano de eventos cableado, o con `dec.EventKind` vacío (la keyword de siempre);
//   - el no-op del caso 1 (ya se estaba en ese evento) — el llamante sigue su camino;
//   - el cupo anti-loop agotado, y la carrera benigna del `ErrAliveExists` dentro de
//     birthEvent: el turno se consume, pero aquí no se sabe sobre qué fila;
//   - 🔴 el ARRANQUE CORTADO por el sink durable (D-054.4, añadido el 2026-08-22). Aquí
//     el evento SÍ existe y su id se conocía, y aun así no sube: startLocked volvió sin
//     escribir hilo, así que anclar la ventana en ese mensaje dejaría una referencia sin
//     literal — el `source_text` saldría incompleto SIN dar error. Ver el `if cutOff`
//     de birthEvent, que es donde se decide.
//
// Ese "" NO es un valor de error y nadie tiene que comprobarlo: `Observe` lo descarta
// solo, porque `intake.WindowKey.Valid()` exige `event_id`.
//
// # EL TURNO DE APERTURA, QUE SOLO BAJA (Plan 044 · T1.4, 2026-08-22)
//
// `opening` viaja hacia abajo hasta startLocked y es lo ÚNICO del 044 que lo hace: el
// `event_id` SUBE (ver arriba) porque es un `string` que nace aquí, y el LITERAL baja
// porque las salidas del arranque nacen allá. Que las dos direcciones convivan no es
// una incoherencia: cada dato viaja desde donde existe hasta donde hace falta, y en
// ningún caso baja el `*cloudlinkv1.IncomingMessage` —el plano de eventos del 043
// sigue sin conocer la forma del entrante—. Ver openingTurn (start.go).
//
// De las CINCO puertas que llaman aquí, solo startFromDecision pasa un turno de
// apertura poblado. Las otras cuatro pasan el valor cero A PROPÓSITO, y el porqué es
// el MISMO que ya las deja fuera de la ventana de captación: StartNewOfKind (el «1»
// del despachador no es el pedido), liveEventSwitch y exitMenuChoice (un salto por
// tipo sobre conversación viva es navegación, no pedido) y el salto del menú de
// salida. Un literal que no puede anclar la ventana tampoco tiene por qué abrir el
// hilo del evento al que se salta.
func (rt *Runtime) beginEvent(ctx context.Context, key store.Key, sessionID string, dec trigger.Decision, g eventGesture, active string, opening openingTurn) (string, bool, error) {
	if rt.events == nil || dec.EventKind == "" {
		// Sin plano de eventos cableado, un event_start se comporta como la keyword
		// que siempre fue: arranca su flujo y no pare nada (INV-6).
		return "", false, nil
	}
	ev, alive, err := rt.events.GetAliveByKind(ctx, key.TenantID, sessionID, key.ContactID, dec.EventKind)
	if err != nil {
		return "", false, fmt.Errorf("runtime: buscar evento vivo de tipo %q: %w", dec.EventKind, err)
	}
	if alive && ev.ID == active && g == gestureGoTo {
		// Caso 1: ya está donde pidió estar. Nada que escribir y turno NO consumido.
		rt.log.Debug("runtime: event_start sobre el evento que ya estaba activo; no-op",
			"session_id", sessionID, "event_kind", dec.EventKind)
		return "", false, nil
	}
	// A partir de aquí SÍ se escribe y SÍ se habla, así que la red anti-loop (Plan
	// 020 · T0) se cobra su token: después de descartar el no-op —que no responde y
	// no debe gastar cuota— y ANTES de tocar la base, porque parir un evento al que
	// no se va a poder contestar es peor que no parirlo.
	if !rt.replyAllowed(key) {
		return "", true, nil
	}
	if alive {
		reuse, cerr := rt.reuseOrRetire(ctx, key.TenantID, ev, g)
		if cerr != nil {
			return "", false, cerr
		}
		if reuse {
			// El id se devuelve AUNQUE switchToEvent falle: da igual, el llamante corta
			// por el error antes de mirarlo. Se escribe así —y no con un "" en el camino
			// de error— porque el evento existe de verdad; mentir sobre eso para adornar
			// la firma sería peor que la línea de más.
			//
			// 🔴 LA CONMUTA ESCRIBE EL LITERAL DEL CLIENTE Y **NO** LAS SALIDAS, y la
			// asimetría es el punto (Plan 044 · T1.4). La invariante que se sostiene es
			// «todo mensaje que entra en `source_refs` tiene su literal en el hilo»: cuando
			// se llega aquí desde startFromDecision, ese entrante SÍ entra en la ventana
			// (observeForAggregation, con este mismo ev.ID) y anclaría el `message_ts`
			// (D-044.9), así que su texto no puede faltar — el compositor de T1.4 leería el
			// hilo y no lo encontraría.
			//
			// Las SALIDAS, en cambio, quedan fuera: switchToEvent RE-ENTRA a un evento que
			// ya existía y cuyo hilo ya tiene escrito su nodo inicial. Volver a meterlo
			// duplicaría el literal EN SILENCIO —el `seq` es MAX+1 y no hay UNIQUE por
			// texto—, que es el defecto peor de los dos. Por eso se llama al productor con
			// `nil` en las salidas y no a persistOpeningTurn.
			//
			// Las otras cuatro puertas que llegan aquí pasan el valor cero de openingTurn,
			// así que para ellas esta línea es un no-op exacto (ver la cabecera).
			if opening.FromClient {
				rt.persistTurnMessages(ctx, key.TenantID, sessionID, ev.ID, opening.Text, nil)
			}
			return ev.ID, true, rt.switchToEvent(ctx, key, sessionID, ev)
		}
	}
	born, berr := rt.birthEvent(ctx, key, sessionID, dec, opening)
	return born, true, berr
}

// activeEvent relee la FILA del evento ACTIVO. Es BEST-EFFORT: un fallo de lectura o
// un puntero que ya no apunta a nada vivo devuelven ok=false y el llamante decide.
// Quedarse sin confirmar por no saber cómo llamarlo sería peor que confirmar sin
// nombre (Plan 043 · T5.4, D2 · sitio 3).
func (rt *Runtime) activeEvent(ctx context.Context, key store.Key, sessionID, eventID string) (events.Event, bool) {
	ev, ok, err := rt.aliveByID(ctx, key.TenantID, sessionID, key.ContactID, eventID)
	if err != nil {
		// #15 (E8 punto 1): esta rama la comparten TODOS los llamantes de activeEvent
		// (closeIfFinished, stopEvent, handleEscape, el quinto camino de suelta…), y
		// desde T5.4 cada uno de ellos SE COME un efecto de ciclo de vida cuando esto
		// falla —el mensaje solo mencionaba el aviso genérico al cliente, no la
		// telemetría perdida. Se amplía para que el log diga las DOS cosas que se
		// pierden, no una.
		rt.log.Warn("runtime: no se pudo releer el evento activo; se usa el aviso genérico y se pierde su efecto de ciclo de vida (sin event_closed/event_deactivated/event_escaped/etc. para este turno)",
			"error", err, "session_id", sessionID)
		return events.Event{}, false
	}
	return ev, ok
}

// activeEventKind resuelve el TIPO del evento activo para poder nombrarlo en la
// confirmación (E-3: se nombra el tipo, jamás el history_id). Es BEST-EFFORT: si no
// se puede averiguar devuelve "" y el aviso cae al genérico — quedarse sin confirmar
// por no saber cómo llamarlo sería peor que confirmar sin nombre.
//
// ⚠️ Lo usa T5.3 desde incoming.go (D1): NO se renombra ni se borra. Reimplementado
// sobre activeEvent (T5.4, D2 · sitio 3) — misma firma, mismo comportamiento.
func (rt *Runtime) activeEventKind(ctx context.Context, key store.Key, sessionID, eventID string) string {
	ev, ok := rt.activeEvent(ctx, key, sessionID, eventID)
	if !ok {
		return ""
	}
	return ev.Kind
}

// aliveByID relee el evento VIVO al que apunta flow_state.event_id. Va por ListAlive
// —UNA consulta— y no recorriendo los tipos de fábrica preguntando por cada uno, que
// serían cuatro consultas para responder a la misma pregunta.
//
// No encontrarlo NO es un error (ok=false): el puntero puede haber quedado apuntando
// a un evento que se cerró desde la app del dueño entre un entrante y el siguiente.
// Quien llama decide qué hacer con esa ausencia; aquí no se inventa nada.
func (rt *Runtime) aliveByID(ctx context.Context, tenantID, sessionID, contactID, eventID string) (events.Event, bool, error) {
	alive, err := rt.events.ListAlive(ctx, tenantID, sessionID, contactID)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("runtime: releer los eventos vivos de la conversación: %w", err)
	}
	for _, ev := range alive {
		if ev.ID == eventID {
			return ev, true, nil
		}
	}
	return events.Event{}, false, nil
}
