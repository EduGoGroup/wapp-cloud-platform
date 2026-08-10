// Package events es el modelo y el almacén del EVENTO conversacional (Plan 043 ·
// Ola 1 · T1.4; ADR-0029 y su «Enmienda 2 (2026-08-05, noche)» E-6; ADR-0034).
//
// Un evento es la instancia VIVA de una capacidad (carrito, encuesta, menú, media)
// para UNA conversación (tenant, sesión, contacto). Nace TARDE: el saludo no crea
// evento. Tres palabras que aquí NO son sinónimas y solo una es columna:
//
//	vivo       → Status == StatusOpen. Lo dice conversation_events.
//	activo     → flow_state.event_id apunta a él (D-043.4). No es un estado.
//	suspendido → DERIVADO (E-6): IsSuspended lo calcula, nadie lo persiste.
//
// De ahí las dos ausencias deliberadas del paquete: no hay contador de posición
// del evento en la conversación (la identidad legible es HistoryID, que se
// persiste) y no hay vencimiento absoluto de la espera. El único reloj es
// LastActivityAt, que Touch refresca en cada interacción — por eso una
// conversación activa nunca vence.
//
// # Por qué AppendSummary toma json.RawMessage y no string
//
// No es un detalle de serialización: es la puerta que se está cerrando. El
// historial está GRADUADO (ADR-0034 §Decisión 1) y el grado decide dónde acaba
// cada cosa:
//
//	decision, summary → nivel 1: ESTRUCTURA en claro, en payload JSONB. En claro
//	                    a propósito, porque es negocio cuantificable («cuántos
//	                    piden sin sal») y cifrarlo destruiría su valor sin
//	                    proteger a nadie.
//	message           → nivel 2: TEXTO LITERAL, siempre cifrado, payload NULL.
//	(la charla que no decide nada) → nivel 3: no se guarda.
//
// El riesgo real no es que alguien cifre de menos: es que alguien «resuma»
// pegando la frase del cliente en la columna EN CLARO. Con la firma en string
// eso compila, pasa el CHECK de la BD y solo se descubre auditando datos. Con
// json.RawMessage, la prosa no tiene por dónde entrar: el nivel 1 exige
// estructura y el tipo lo exige antes que nadie. Si alguien «simplifica» esto a
// string en una limpieza, reabre exactamente ese agujero — el texto libre tiene
// su puerta, AppendMessage, y allí se cifra siempre.
//
// El CHECK conversation_event_messages_grade_chk sostiene la otra mitad desde la
// BD (un summary no puede traer cuerpo cifrado; un message no puede traer
// payload), y está fijado por test contra Postgres real.
package events

import (
	"errors"
	"time"
)

// Status es el ciclo de vida del evento. Son EXACTAMENTE tres (D-043.5): ni
// «paused» ni «expired», porque «suspendido» es derivado (IsSuspended) y «activo»
// lo dice flow_state.event_id. El CHECK de conversation_events.status es el que
// impone estos tres en la BD; estas constantes son su espejo en Go.
type Status string

const (
	// StatusOpen es el evento VIVO: el único que ocupa el tipo en el índice único
	// parcial (E-2) y el único que una transición puede mover.
	StatusOpen Status = "open"
	// StatusClosed es el evento terminado BIEN. Terminal: libera el tipo.
	StatusClosed Status = "closed"
	// StatusCancelled es el evento abandonado o cancelado. Terminal: libera el tipo.
	StatusCancelled Status = "cancelled"
)

// Role es de QUIÉN es la voz de una entrada del historial. Es un ROL, no una
// persona: aquí nunca va un identificador de usuario, un número ni un nombre. Por
// dónde ENTRÓ la entrada es otra pregunta y la responde la columna origin.
type Role string

const (
	// RoleClient es la voz del cliente.
	RoleClient Role = "client"
	// RoleBusiness es la voz de la empresa.
	RoleBusiness Role = "business"
	// RoleSystem es lo que emite la plataforma; es el rol FIJO de los resúmenes
	// que escribe AppendSummary (ADR-0029 E-4).
	RoleSystem Role = "system"
)

// entryKind marca el GRADO de una entrada del historial (ADR-0034 §Decisión 1).
// No se exporta porque no es una elección del llamador: cada método de escritura
// del store fija el suyo, y esa es justo la garantía de que un resumen no pueda
// entrar disfrazado de mensaje del cliente (la doble contabilidad de INV-11).
type entryKind string

const (
	entryKindSummary entryKind = "summary"
	entryKindMessage entryKind = "message"
)

// Errores del store. Son sentinelas para que el llamador distinga «la BD falló» de
// «la regla dijo que no», que son decisiones distintas río arriba.
var (
	// ErrNotOpen lo devuelve TransitionEvent cuando el evento ya no estaba vivo:
	// el UPDATE lleva `AND status='open'` y no tocó ninguna fila. Es el guard que
	// hace imposible resucitar un evento terminal.
	ErrNotOpen = errors.New("events: el evento no está open (transición rechazada por el guard)")
	// ErrEventMissing lo devuelve Touch cuando el id no existe.
	ErrEventMissing = errors.New("events: el evento no existe")
	// ErrEventNotFound lo devuelve GetEventForTenant cuando no hay fila para
	// (id, tenant): también cuando el id EXISTE pero es de otro tenant, y esa
	// indistinción es deliberada (T4.2) — para el llamante «no es tuyo» y «no
	// existe» tienen que ser la MISMA respuesta (el 404 que no filtra existencia
	// cruzada). Es un sentinela aparte de ErrEventMissing porque Touch responde a
	// una pregunta sin tenant y este a una acotada; fusionarlos invitaría a usar
	// Touch como comprobación de pertenencia, que es justo lo que no comprueba.
	ErrEventNotFound = errors.New("events: el evento no existe para ese tenant")
	// ErrSummaryNotJSON lo devuelve AppendSummary si el cuerpo no es JSON válido.
	// El nivel 1 del ADR-0034 es ESTRUCTURA en claro, no prosa: si el resumen no
	// es JSON, lo que se está intentando meter es texto libre por la puerta que no
	// cifra.
	ErrSummaryNotJSON = errors.New("events: el resumen debe ser JSON válido (el nivel 1 es estructura, no prosa)")
	// ErrNoCipher lo devuelve AppendMessage si el store se construyó sin
	// FieldCipher. Falla en vez de degradar a texto en claro: la ausencia de
	// cifrado cierra la puerta, no la abre.
	ErrNoCipher = errors.New("events: sin FieldCipher no se persiste texto literal (no hay camino en claro)")
	// ErrAliveExists lo devuelve CreateEvent cuando ya hay un evento vivo de ese
	// tipo en la conversación: lo impone el índice único parcial (E-2), no el
	// código, y por eso llega como violación de unicidad desde Postgres.
	ErrAliveExists = errors.New("events: ya existe un evento vivo de ese tipo en la conversación")
	// ErrNotTerminal lo devuelve TransitionEvent si el destino no es terminal.
	// StatusOpen es el estado de NACIMIENTO, no un destino al que se transite.
	ErrNotTerminal = errors.New("events: el destino de una transición debe ser closed o cancelled")
	// ErrInvalidRole lo devuelve AppendMessage si el rol no es uno de los tres.
	ErrInvalidRole = errors.New("events: rol desconocido")
)

// Event es una fila de public.conversation_events.
type Event struct {
	// ID es la identidad TÉCNICA (UUID): lo que referencian flow_state.event_id y
	// el historial. No es lo que se le enseña a un humano — eso es HistoryID.
	ID string
	// TenantID aísla (INV-8): sale del token, jamás del cuerpo.
	TenantID string
	// SessionID es la sesión CloudLink. Forma parte de la identidad de la
	// conversación: el MISMO contacto en otra sesión es otra conversación.
	SessionID string
	// ContactID es la identidad OPACA del contacto (contacts.contact_id).
	ContactID string
	// Kind es la capacidad instanciada: menu | cart | survey | media. Es la
	// dimensión del único parcial: uno vivo POR TIPO y conversación (E-2).
	Kind string
	// HistoryID es el id legible «<tipo>-YYYY-MM-DD-HHMM» en UTC (E-3). Se
	// PERSISTE, no se recalcula: un identificador que se recalcula deja de ser el
	// mismo cuando cambia la fórmula.
	HistoryID string
	// Status es el ciclo de vida. Ver Status.
	Status Status
	// FlowID y FlowVersion congelan el flujo con el que nació el evento, para que
	// el historial siga siendo legible aunque el flujo se edite después.
	FlowID      string
	FlowVersion int
	// IntakeID liga la solicitud que este evento parió (ADR-0031), si la parió.
	IntakeID string
	// CreatedAt es el nacimiento, que es TARDÍO (E-6): el saludo no crea evento.
	CreatedAt time.Time
	// LastActivityAt es EL reloj (E-6): la última interacción. Touch lo refresca
	// en cada una, y de él —y solo de él— deriva IsSuspended.
	LastActivityAt time.Time
	// ClosedAt sella la muerte EXPLÍCITA (closed|cancelled). Cero mientras siga
	// open. No es un vencimiento: nada mata un evento por tiempo.
	ClosedAt time.Time
}

// Alive reporta si el evento está VIVO (status='open'). Vivo no es «activo» (eso
// lo dice flow_state.event_id) ni lo contrario de «suspendido» (un evento
// suspendido sigue vivo y sigue siendo rescatable).
func (e Event) Alive() bool { return e.Status == StatusOpen }

// Rescuable es un evento que se le puede OFRECER al contacto para retomarlo, con
// la marca derivada «vencido» ya resuelta por la consulta (T3.9a).
//
// Es un tipo aparte de Event y no un campo suyo porque Stale NO es una propiedad
// del evento: es una propiedad de la PREGUNTA «¿estaba vencido cuando miré?», y
// depende del reloj y de la config del tenant. Un Event leído por cualquier otra
// vía no la tiene ni podría tenerla, y meterla en Event dejaría un bool a false
// que parecería decir «no está vencido» cuando en realidad nadie lo miró.
type Rescuable struct {
	Event
	// Stale es la marca que INFORMA (D-043.18/E-10): el evento lleva sin tocarse
	// más de lo que el tenant tolera. No filtra nada (INV-19) — un evento vencido
	// sigue siendo rescatable, y esa es justo la razón de que el rescate exista.
	Stale bool
}

// NewEvent son los datos con que nace un evento. Lo que NO está aquí es
// deliberado: el status nace siempre en open, y HistoryID, CreatedAt y
// LastActivityAt los deriva el store del reloj inyectado — no se dictan desde
// fuera, porque entonces dos eventos podrían discrepar sobre qué hora es.
type NewEvent struct {
	TenantID    string
	SessionID   string
	ContactID   string
	Kind        string
	FlowID      string
	FlowVersion int
	// IntakeID es opcional: vacío = el evento aún no parió solicitud o es de un
	// tipo que no la produce.
	IntakeID string
}

// historyIDLayout es el formato de HistoryID tras el tipo: YYYY-MM-DD-HHMM (E-3).
// Sin segundos a propósito: es un id para que un humano hable de «ese pedido», no
// una marca de tiempo.
const historyIDLayout = "2006-01-02-1504"

// HistoryID compone el id legible «<tipo>-YYYY-MM-DD-HHMM» a partir del instante
// dado, SIEMPRE en UTC (E-3). Se expone para que quien lo lea pueda comprobar el
// formato sin duplicar la fórmula; quien lo escribe es CreateEvent, y lo hace con
// el reloj inyectado del store — nunca con now() de la BD, que no es inyectable.
func HistoryID(kind string, at time.Time) string {
	return kind + "-" + at.UTC().Format(historyIDLayout)
}

// IsSuspended reporta si el evento está SUSPENDIDO en el instante now: vivo pero
// sin interacción durante más de ttl (E-6, D-043.7).
//
// Es DERIVADA y PURA: no toca la BD —no recibe context ni conexión, así que no
// puede— y no persiste nada. No hay cron ni columna que la respalde; se evalúa
// perezosamente al llegar el entrante.
//
// ttl <= 0 devuelve SIEMPRE false: es el override «sin vencimiento» del tenant
// (event_inactivity_ttl_seconds = 0), no un TTL de cero segundos que caducaría
// todo al instante. Un evento terminal tampoco está suspendido: ya murió.
func IsSuspended(e Event, ttl time.Duration, now time.Time) bool {
	if ttl <= 0 || !e.Alive() {
		return false
	}
	return now.Sub(e.LastActivityAt) > ttl
}
