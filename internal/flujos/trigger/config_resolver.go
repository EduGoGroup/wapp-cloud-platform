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

// Resolve decide qué hacer con un entrante sin conversación viva:
//   - Si alguna regla keyword habilitada casa → {Start, FlowID} (colisión
//     determinista: priority desc, exact antes que contains, keyword asc).
//   - Si ninguna casa pero hay fallback habilitado → {Fallback, FlowID} (mayor priority).
//   - Si tampoco → {Ignore}.
func (c *ConfigResolver) Resolve(ctx context.Context, tenantID, text string) (Decision, error) {
	keywords, err := c.store.ListByKind(ctx, tenantID, KindKeyword)
	if err != nil {
		return Decision{}, err
	}
	matches := make([]Rule, 0, len(keywords))
	for _, r := range keywords {
		if r.Enabled && match(r, text) {
			matches = append(matches, r)
		}
	}
	if len(matches) > 0 {
		sortByPriority(matches)
		return Decision{Action: Start, FlowID: matches[0].FlowID}, nil
	}

	fallbacks, err := c.store.ListByKind(ctx, tenantID, KindFallback)
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
// y, de casar, devuelve el aviso configurado en esa regla (r.Message; vacío si la
// regla no define uno ⇒ el runtime cae a su aviso por defecto).
func (c *ConfigResolver) IsEscape(ctx context.Context, tenantID, text string) (bool, string, error) {
	escapes, err := c.store.ListByKind(ctx, tenantID, KindEscape)
	if err != nil {
		return false, "", err
	}
	for _, r := range escapes {
		if r.Enabled && match(r, text) {
			return true, r.Message, nil
		}
	}
	return false, "", nil
}

// sortByPriority ordena las reglas que casan de forma determinista:
// priority desc → exact antes que contains → keyword asc (orden estable final).
func sortByPriority(rules []Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.MatchType != b.MatchType {
			return a.MatchType == MatchExact // exact gana el empate
		}
		return a.Keyword < b.Keyword
	})
}

// bestEnabled devuelve la regla habilitada de mayor priority (empate → keyword asc,
// luego trigger_id asc para total determinismo), o nil si no hay ninguna.
func bestEnabled(rules []Rule) *Rule {
	var best *Rule
	for i := range rules {
		r := rules[i]
		if !r.Enabled {
			continue
		}
		if best == nil ||
			r.Priority > best.Priority ||
			(r.Priority == best.Priority && r.Keyword < best.Keyword) ||
			(r.Priority == best.Priority && r.Keyword == best.Keyword && r.TriggerID < best.TriggerID) {
			rc := r
			best = &rc
		}
	}
	return best
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
