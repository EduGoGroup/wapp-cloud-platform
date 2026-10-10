// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// events.go es el PLANO DE EVENTOS del runtime (Plan 043): los puertos por los que el motor
// habla con el evento conversacional y la lógica que hace nacer un evento, saltar entre
// eventos, desactivarlo, resumir lo que se abandona y presentar el menú del despachador.
//
// En rojo solo declara lo exportado; el resto —1.500 líneas en el viejo— nace en el verde
// (F8-04b), que además lo PARTE POR TEMA para caber en E-13 (≤ 500 líneas por fichero, con
// tolerancia hasta 600), solo moviendo declaraciones y con el sufijo del origen: nacimiento y
// salto, reloj del evento, menú y oferta, resumen y coletilla.
//
// # Reglas del plano que el verde tiene que cumplir (y los tests de la ola siguiente, probar)
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

// StartNewOfKind es la TERCERA puerta del nacimiento tardío (EV-2): la elección EXPLÍCITA de
// empezar uno nuevo del tipo kind. Es el único camino con el gesto «nuevo» y, por tanto, el
// único que puede aplicar E-11 (EV-3). flowID es el flujo que arranca ("" = sin flujo);
// activeEventID, el evento activo de la conversación ("" = ninguno).
//
// Devuelve si el turno se CONSUMIÓ:
//
//   - sin plano de eventos, o con kind vacío: (false, nil), sin tocar nada;
//   - hay un vivo de ese tipo DENTRO de su ventana: se conmuta hacia él y no se cierra nada
//     (E-11.3: nadie pierde un pedido en curso por tocar una opción) → (true, nil);
//   - hay un vivo de ese tipo y está VENCIDO: se cancela (event_cancelled), se abandona su
//     solicitud y nace uno nuevo (event_started) → (true, nil). Si la cancelación pierde la
//     carrera (events.ErrNotOpen) se sigue igual; si el abandono falla, el error sube con el
//     evento ya cancelado;
//   - no hay ninguno: nace (event_started) y arranca flowID → (true, nil);
//   - cupo del limitador agotado: (true, nil) sin crear ni enviar nada, y se cuenta
//     `rate_limit`.
//
// 🔴 Se llama con el candado de la conversación YA TOMADO: tomarlo aquí sería auto-deadlock.
//
// El mensaje que la dispara NO entra ni en el hilo del evento ni en la ventana de captación:
// es el número de una lista que pintó la plataforma, no lo que el cliente quiere pedir.
func (rt *Runtime) StartNewOfKind(ctx context.Context, key store.Key, sessionID, kind, flowID, activeEventID string) (bool, error) {
	panic(pendiente.Implementar("runtime.Runtime.StartNewOfKind"))
}
