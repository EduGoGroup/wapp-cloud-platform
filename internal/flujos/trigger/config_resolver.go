package trigger

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ConfigResolver es el adapter que resuelve disparos leyendo las reglas del
// TriggerStore (100% de BD, cero hardcode). Implementa Resolver.
//
// El switch por match_type/kind vive SOLO aquí (INV-5): el engine y el runtime
// no conocen la estrategia de coincidencia.
type ConfigResolver struct {
	store Store
}

// NewConfigResolver construye el resolver sobre el store dado.
func NewConfigResolver(store Store) *ConfigResolver {
	return &ConfigResolver{store: store}
}

// Resolve decide qué hacer con un entrante sin conversación viva (sessionID = la
// sesión del entrante; el store ya filtró a reglas específicas de esa sesión O
// globales, Plan 020 · T4). ORDEN de resolución (design.md §4.c, INV-5: todo el
// switch de interpretación de la señal vive AQUÍ):
//  1. Si la señal trae intención (sig.Intent != nil): reglas kind='llm' cuyo
//     keyword (nombre de intent) casa EXACTO-normalizado el nombre de la intención
//     → {Start, FlowID, Params, IntentName} (mismas reglas de session_id/priority/
//     enabled que keyword). Los Params viajan a la decisión para que el runtime
//     pre-cargue el flujo (T8).
//  2. Si no hay intención o ninguna regla llm casa: keyword por sig.Text →
//     {Start, FlowID} (colisión determinista: específica-de-sesión antes que
//     global, priority desc, exact antes que contains, keyword asc).
//  3. Si tampoco: fallback habilitado → {Fallback, FlowID} (mejor por el mismo orden).
//  4. Si nada → {Ignore}.
func (c *ConfigResolver) Resolve(ctx context.Context, tenantID, sessionID string, sig Signal) (Decision, error) {
	if sig.Intent != nil {
		llm, err := c.store.ListByKind(ctx, tenantID, sessionID, KindLLM)
		if err != nil {
			return Decision{}, err
		}
		matches := make([]Rule, 0, len(llm))
		for _, r := range llm {
			if r.Enabled && matchIntent(r, sig.Intent.Name) {
				matches = append(matches, r)
			}
		}
		if len(matches) > 0 {
			sortByPriority(matches)
			return Decision{
				Action:     Start,
				FlowID:     matches[0].FlowID,
				Params:     sig.Intent.Params,
				IntentName: sig.Intent.Name,
			}, nil
		}
	}

	keywords, err := c.store.ListByKind(ctx, tenantID, sessionID, KindKeyword)
	if err != nil {
		return Decision{}, err
	}
	matches := make([]Rule, 0, len(keywords))
	for _, r := range keywords {
		if r.Enabled && match(r, sig.Text) {
			matches = append(matches, r)
		}
	}
	if len(matches) > 0 {
		sortByPriority(matches)
		return Decision{Action: Start, FlowID: matches[0].FlowID}, nil
	}

	fallbacks, err := c.store.ListByKind(ctx, tenantID, sessionID, KindFallback)
	if err != nil {
		return Decision{}, err
	}
	best := bestEnabled(fallbacks)
	if best != nil {
		return Decision{Action: Fallback, FlowID: best.FlowID}, nil
	}
	return Decision{Action: Ignore}, nil
}

// IsEscape indica si el texto casa alguna regla kind=escape habilitada del tenant
// aplicable a la sesión y, de casar, devuelve el aviso configurado en esa regla
// (r.Message; vacío si la regla no define uno ⇒ el runtime cae a su aviso por
// defecto). Si casan una regla específica de sesión y una global, gana la
// ESPECÍFICA (su message).
func (c *ConfigResolver) IsEscape(ctx context.Context, tenantID, sessionID, text string) (bool, string, error) {
	escapes, err := c.store.ListByKind(ctx, tenantID, sessionID, KindEscape)
	if err != nil {
		return false, "", err
	}
	var best *Rule
	for i := range escapes {
		r := escapes[i]
		if !r.Enabled || !match(r, text) {
			continue
		}
		if best == nil || moreSpecific(r, *best) {
			rc := r
			best = &rc
		}
	}
	if best != nil {
		return true, best.Message, nil
	}
	return false, "", nil
}

// moreSpecific reporta si a debe preferirse sobre b únicamente por ESPECIFICIDAD de
// sesión: una regla acotada a una sesión (SessionID != "") gana a una global
// (SessionID == ""). Es el criterio maestro del desempate (Plan 020 · T4): la
// regla específica de sesión gana a la global cuando ambas casan.
func moreSpecific(a, b Rule) bool {
	return a.SessionID != "" && b.SessionID == ""
}

// sortByPriority ordena las reglas que casan de forma determinista:
// específica-de-sesión antes que global → priority desc → exact antes que contains
// → keyword asc (orden estable final). La especificidad de sesión es el criterio
// MAESTRO: una regla de sesión gana a una global aunque tenga menor priority.
func sortByPriority(rules []Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if sa, sb := a.SessionID != "", b.SessionID != ""; sa != sb {
			return sa // específica de sesión gana a global
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.MatchType != b.MatchType {
			return a.MatchType == MatchExact // exact gana el empate
		}
		return a.Keyword < b.Keyword
	})
}

// bestEnabled devuelve la mejor regla habilitada por el mismo orden que
// sortByPriority (específica-de-sesión → priority desc → keyword asc → trigger_id
// asc para total determinismo), o nil si no hay ninguna.
func bestEnabled(rules []Rule) *Rule {
	var best *Rule
	for i := range rules {
		r := rules[i]
		if !r.Enabled {
			continue
		}
		if best == nil || fallbackBetter(r, *best) {
			rc := r
			best = &rc
		}
	}
	return best
}

// fallbackBetter reporta si a debe ganar a b como fallback elegido:
// específica-de-sesión antes que global → mayor priority → keyword asc →
// trigger_id asc. Cuando la especificidad es igual, conserva el desempate del 019.
func fallbackBetter(a, b Rule) bool {
	if sa, sb := a.SessionID != "", b.SessionID != ""; sa != sb {
		return sa
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if a.Keyword != b.Keyword {
		return a.Keyword < b.Keyword
	}
	return a.TriggerID < b.TriggerID
}

// matchIntent evalúa una regla kind='llm' contra el NOMBRE de la intención
// resuelta: match EXACTO tras normalizar ambos lados (design.md §4.c). No usa
// MatchType (el clasificador ya resolvió la intención; el nombre es un
// identificador, no texto libre a contener). Una keyword vacía nunca casa.
func matchIntent(r Rule, intentName string) bool {
	nk := normalize(r.Keyword)
	if nk == "" {
		return false
	}
	return nk == normalize(intentName)
}

// match evalúa una regla contra el texto entrante según su MatchType. Ambos
// lados se normalizan con la MISMA función (normalize).
func match(r Rule, text string) bool {
	nt := normalize(text)
	nk := normalize(r.Keyword)
	if nk == "" {
		return false
	}
	switch r.MatchType {
	case MatchContains:
		return strings.Contains(nt, nk)
	case MatchExact:
		return nt == nk
	default:
		// match_type desconocido: exact por defecto (conservador).
		return nt == nk
	}
}

// normalize canoniza una señal de texto para comparar de forma robusta:
// minúsculas + strip de diacríticos (NFD + quita marcas Mn) + trim + colapso de
// espacios internos. UNA sola función, aplicada por igual a texto y keyword.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = stripDiacritics(s)
	// Colapsar cualquier run de espacios (incluye \t, \n) a un único espacio y
	// recortar los extremos.
	return strings.Join(strings.Fields(s), " ")
}

// stripDiacritics descompone (NFD) y elimina las marcas combinantes (unicode.Mn),
// de modo que "pedído"/"PEDIDO" normalizan igual que "pedido".
func stripDiacritics(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
