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
)

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
}

// Decision es el resultado de Resolve.
type Decision struct {
	Action Action
	FlowID string // poblado para Start / Fallback
}

// Resolver es el puerto que consulta el runtime cuando llega un entrante SIN
// conversación viva (Resolve) y para detectar el escape sobre una conversación
// viva (IsEscape).
//
// El parámetro text es la SEÑAL DE ENTRADA REUBICABLE: hoy es el texto crudo del
// entrante; en Fase 2 será la intención resuelta por el Edge (LLM). La firma se
// mantiene estable para no comprometer ese salto futuro: el adapter decide cómo
// interpretar la señal, el runtime solo la pasa.
type Resolver interface {
	// Resolve decide qué hacer con un entrante sin conversación viva.
	Resolve(ctx context.Context, tenantID, text string) (Decision, error)
	// IsEscape indica si el texto es una señal de escape para el tenant y, si lo es,
	// devuelve el aviso configurado en la regla que casó (message; vacío si la regla
	// no define uno ⇒ el runtime cae a su aviso por defecto).
	IsEscape(ctx context.Context, tenantID, text string) (matched bool, message string, err error)
}

// NoopResolver es el adapter por DEFAULT: nunca arranca nada y nunca es escape.
// Garantiza no-regresión total (INV-6): con él cableado, un entrante sin
// conversación viva se ignora, idéntico al comportamiento previo al Plan 019.
type NoopResolver struct{}

// NewNoopResolver construye el resolver inerte por defecto.
func NewNoopResolver() NoopResolver { return NoopResolver{} }

// Resolve siempre ignora.
func (NoopResolver) Resolve(context.Context, string, string) (Decision, error) {
	return Decision{Action: Ignore}, nil
}

// IsEscape siempre es false (sin mensaje).
func (NoopResolver) IsEscape(context.Context, string, string) (bool, string, error) {
	return false, "", nil
}
