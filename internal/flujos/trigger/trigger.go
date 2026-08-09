// Package trigger define el puerto Resolver del Motor de Flujos: la pieza que
// decide, ANTE UN ENTRANTE SIN CONVERSACIÓN VIVA, si se arranca un flujo
// (por palabra clave), si se arranca el flujo de fallback, o si se ignora
// (decisión C histórica). También decide si un texto es una "señal de escape"
// que corta una conversación viva.
//
// Contrato PURO (sin infra): los tipos de este archivo NO dependen de BD ni de
// red; el adapter por defecto (NoopResolver) es PURO y garantiza no-regresión
// total (INV-6): si nadie cablea un resolver real, el comportamiento es idéntico
// al actual "sin estado vivo → se ignora".
//
// Zero-knowledge (ADR-0007): la resolución opera solo sobre texto de negocio;
// NUNCA toca credenciales ni llaves.
package trigger

import "context"

// MatchType es la estrategia de coincidencia de una regla de palabra clave.
type MatchType string

const (
	// MatchExact exige igualdad tras normalizar (normalize(text) == normalize(keyword)).
	MatchExact MatchType = "exact"
	// MatchContains exige que el texto normalizado CONTENGA la keyword normalizada.
	MatchContains MatchType = "contains"
)

// Kind clasifica una regla dentro de la única tabla flow_triggers. Unificar los
// tres conceptos permite "crecer por casos" (mañana regex/schedule son filas,
// no código nuevo en el engine).
type Kind string

const (
	// KindKeyword arranca un flujo cuando el entrante casa la keyword.
	KindKeyword Kind = "keyword"
	// KindFallback arranca un flujo cuando NADA casa (uno por tenant; gana priority).
	KindFallback Kind = "fallback"
	// KindEscape corta una conversación viva cuando el entrante casa la keyword.
	KindEscape Kind = "escape"
	// KindLLM arranca un flujo cuando la INTENCIÓN resuelta por el clasificador
	// (ADR-0020, Plan 029 · T7) casa el nombre de intent guardado en Keyword. Es la
	// primera señal que NO es texto crudo: la resolución vive SOLO en el resolver
	// (INV-5), no en el engine ni el runtime. Comparte tabla flow_triggers con los
	// demás kinds (crecer por casos, no por código nuevo).
	KindLLM Kind = "llm"
	// KindEventStart arranca —o CONMUTA, que para el cliente es lo mismo— el evento
	// conversacional del tipo que lleva en EventKind (Plan 043 · D-043.2). Es PAR de
	// KindKeyword en la escalera de arranque reactivo y, a diferencia de él, se evalúa
	// TAMBIÉN con conversación viva (ResolveLive): eso es justo lo que hace posible el
	// salto por tipo. NO hay un kind aparte para «retomar» (derogado en D-043.2): con
	// uno-vivo-por-tipo, «carrito» ya significa «ve al carrito», lo cree o lo conmute,
	// y un kind gemelo solo obligaría al dueño a mantener sus palabras por duplicado.
	KindEventStart Kind = "event_start"
	// KindEventStop DESACTIVA el evento activo sin matarlo (Plan 043 · D-043.2): apaga
	// el puntero flow_state.event_id y la fila sigue en status='open', rescatable. NO
	// lleva EventKind: lo que corta es el activo, sea del tipo que sea.
	KindEventStop Kind = "event_stop"
)

// Tipos de evento DE FÁBRICA (D-043.3, INV-07): el vocabulario cerrado que puede
// llevar Rule.EventKind, el mismo de conversation_events.kind.
const (
	// EventKindMenu es el despachador de nivel superior: el menú es un evento más,
	// sujeto a la misma regla de uno-vivo-por-tipo (D-043.3).
	EventKindMenu = "menu"
	// EventKindCart es el carrito sobre catálogo.
	EventKindCart = "cart"
	// EventKindSurvey es la encuesta.
	EventKindSurvey = "survey"
	// EventKindMedia es la entrega de media/PDF.
	EventKindMedia = "media"
)

// FactoryEventKinds devuelve los tipos de evento de fábrica en orden estable. Es la
// lista contra la que valida el CRUD y la que se le enseña al dueño en el error.
//
// Por qué el vocabulario se cierra AQUÍ y no en la base: flow_triggers.event_kind no
// lleva CHECK a propósito (migración 0052) para que enchufar un módulo no cueste una
// migración, y eso sigue intacto —ampliar esta lista tampoco cuesta una migración—.
// Lo que la laxitud no compraba era nada: sin cierre, un `carrrito` mal escrito
// entraba con un 200 y llegaba hasta una fila de conversation_events para parir un
// evento que NINGÚN módulo atiende, sin error en ningún punto del camino.
func FactoryEventKinds() []string {
	return []string{EventKindMenu, EventKindCart, EventKindSurvey, EventKindMedia}
}

// IsFactoryEventKind reporta si k es uno de los tipos de evento de fábrica.
func IsFactoryEventKind(k string) bool {
	for _, f := range FactoryEventKinds() {
		if k == f {
			return true
		}
	}
	return false
}

// Action es la decisión que toma el Resolver para un entrante sin conversación
// viva. El valor cero (Ignore) es el default seguro (no-regresión, INV-6).
type Action int

const (
	// Ignore no hace nada (decisión C histórica). Es el valor cero.
	Ignore Action = iota
	// Start arranca el flujo Decision.FlowID (palabra clave que casó).
	Start
	// Fallback arranca el flujo de fallback del tenant (Decision.FlowID).
	Fallback
	// Escape indica que el texto es una señal de escape (lo usa IsEscape, no Resolve).
	Escape
	// StartEvent arranca o CONMUTA el evento conversacional de tipo Decision.EventKind
	// (Plan 043 · D-043.2/D-043.4). El salto es idempotente POR TIPO y quien lo ejecuta
	// es el runtime: el resolver solo dice qué tipo pidió el cliente.
	StartEvent
	// StopEvent desactiva el evento activo sin matarlo (Plan 043 · D-043.2): el puntero
	// se apaga, el status NO se toca.
	StopEvent
)

// Rule es una regla de disparo PURA (fila de flow_triggers proyectada al dominio).
type Rule struct {
	TenantID  string
	TriggerID string
	Kind      Kind
	Keyword   string    // vacío para fallback
	MatchType MatchType // relevante para keyword/escape
	FlowID    string    // vacío para escape
	Priority  int
	Enabled   bool
	// Message es el aviso que el runtime envía cuando esta regla kind=escape casa
	// y corta la conversación viva (Plan 019 · T4b). Solo tiene sentido para escape;
	// vacío ⇒ el runtime usa su aviso por defecto. NULL en flow_triggers ⇔ "".
	Message string
	// SessionID acota la regla a una sesión concreta (Plan 020 · T4). Vacío ("") ⇒
	// regla GLOBAL del tenant (aplica a todas las sesiones; NULL en flow_triggers).
	// En el desempate, una regla específica de sesión gana a la global cuando ambas
	// casan. Retrocompatible: las reglas del 019 no traían SessionID ⇒ globales.
	SessionID string
	// EventKind es el TIPO de evento conversacional que pare este disparo (Plan 043 ·
	// D-043.2): menu | cart | survey | media, el mismo vocabulario que
	// conversation_events.kind. Obligatorio para kind='event_start' y vacío para
	// TODOS los demás kinds (incluido event_stop, que corta el activo sea cual sea).
	// Vacío ⇔ NULL en flow_triggers ⇒ las reglas de siempre siguen sin parir nada.
	EventKind string
}

// Decision es el resultado de Resolve.
type Decision struct {
	Action Action
	FlowID string // poblado para Start / Fallback
	// Params son los parámetros extraídos por el clasificador que ORIGINÓ el
	// arranque (Plan 029 · T8). Se poblan SOLO cuando la decisión provino de una
	// regla kind='llm' (una regla keyword/fallback los deja nil): el runtime los
	// siembra en Conversation.Vars["intent_params"] antes del primer paso, de modo
	// que un módulo (p. ej. cart) pueda pre-cargarse. "el LLM extrae, el código
	// resuelve".
	Params map[string]string
	// IntentName es el nombre de la intención que casó (kind='llm'); vacío para
	// keyword/fallback. El runtime lo siembra en Vars["intent_name"] junto a Params.
	IntentName string
	// EventKind es el TIPO de evento que el cliente pidió (Plan 043 · D-043.2); solo
	// se puebla con Action=StartEvent. El runtime lo usa para arrancar o conmutar el
	// evento; el resolver NO sabe si ese evento ya existe (eso es estado, no regla).
	EventKind string
}

// IntentSignal es la intención resuelta por el clasificador local (ADR-0020) que
// viaja en la Signal de entrada (Plan 029 · T7). Name es la etiqueta de intención
// (casa flow_triggers.keyword en las reglas kind='llm'); Params son los datos
// extraídos (pueden llevar texto literal del cliente); Confidence es la confianza
// reportada; ConfigVersion es la versión del catálogo con la que se clasificó. El
// runtime SOLO la construye si el mensaje trae intent Y el tenant tiene la feature
// (gate de verdad, entitlements): así una regla llm nunca dispara sin derecho.
type IntentSignal struct {
	Name          string
	Params        map[string]string
	Confidence    float64
	ConfigVersion string
}

// Signal es la SEÑAL DE ENTRADA del Resolver (Plan 029 · T7): el salto que el
// docstring del Resolver reservó. Text es el texto crudo del entrante (la señal
// histórica); Intent, si no es nil, es la intención resuelta por el Edge. El
// resolver decide cómo interpretarla (INV-5); el runtime solo la transporta.
type Signal struct {
	Text   string
	Intent *IntentSignal
}

// Resolver es el puerto que consulta el runtime cuando llega un entrante SIN
// conversación viva (Resolve) y para detectar el escape sobre una conversación
// viva (IsEscape).
//
// El parámetro sig es la SEÑAL DE ENTRADA (Plan 029 · T7): el salto que la firma
// reservó. sig.Text es el texto crudo del entrante (la señal histórica); sig.Intent,
// si no es nil, es la intención resuelta por el Edge (LLM). El adapter decide cómo
// interpretar la señal (INV-5: todo el switch de interpretación vive en el resolver),
// el runtime solo la pasa.
//
// sessionID es la sesión del entrante (Plan 020 · T4): permite acotar una regla a
// una sesión concreta. sessionID vacío ("") ⇒ solo casan las reglas GLOBALES del
// tenant (session_id NULL), comportamiento idéntico al 019 (INV-6).
type Resolver interface {
	// Resolve decide qué hacer con un entrante sin conversación viva a partir de la
	// señal (texto y, opcionalmente, intención resuelta).
	Resolve(ctx context.Context, tenantID, sessionID string, sig Signal) (Decision, error)
	// IsEscape indica si el texto es una señal de escape para el tenant/sesión y, si
	// lo es, devuelve el aviso configurado en la regla que casó (message; vacío si la
	// regla no define uno ⇒ el runtime cae a su aviso por defecto). Opera SIEMPRE
	// sobre texto: una conversación viva la corta el usuario escribiendo, no el
	// clasificador (design.md §4.c: con conversación viva el texto manda).
	IsEscape(ctx context.Context, tenantID, sessionID, text string) (matched bool, message string, err error)
	// ResolveLive decide qué hacer con un entrante que llega SOBRE UNA CONVERSACIÓN
	// VIVA (Plan 043 · D-043.2). Es la EXCEPCIÓN ACOTADA de INV-02: con conversación
	// viva el texto sigue mandando, y por eso solo se consultan los dos kinds que son
	// texto EXACTO del tenant —event_start (salto por tipo) y event_stop (desactivar
	// sin matar)—; keyword, fallback y llm NO se evalúan aquí, así que para todo lo
	// demás INV-02 queda intacto y el turno es del módulo.
	//
	// Recibe texto crudo, no Signal: con conversación viva la intención del
	// clasificador NO abre ni conmuta nada (design.md §4.c). Devuelve StartEvent (con
	// EventKind poblado), StopEvent o Ignore; el orden de desempate es el MISMO que en
	// Resolve (específica-de-sesión → priority desc → exact antes que contains).
	ResolveLive(ctx context.Context, tenantID, sessionID, text string) (Decision, error)
}

// NoopResolver es el adapter por DEFAULT: nunca arranca nada y nunca es escape.
// Garantiza no-regresión total (INV-6): con él cableado, un entrante sin
// conversación viva se ignora, idéntico al comportamiento previo al Plan 019.
type NoopResolver struct{}

// NewNoopResolver construye el resolver inerte por defecto.
func NewNoopResolver() NoopResolver { return NoopResolver{} }

// Resolve siempre ignora.
func (NoopResolver) Resolve(context.Context, string, string, Signal) (Decision, error) {
	return Decision{Action: Ignore}, nil
}

// IsEscape siempre es false (sin mensaje).
func (NoopResolver) IsEscape(context.Context, string, string, string) (bool, string, error) {
	return false, "", nil
}

// ResolveLive siempre ignora: sin resolver real cableado, una conversación viva
// nunca salta de tipo ni se desactiva por texto (no-regresión total, INV-6).
func (NoopResolver) ResolveLive(context.Context, string, string, string) (Decision, error) {
	return Decision{Action: Ignore}, nil
}
