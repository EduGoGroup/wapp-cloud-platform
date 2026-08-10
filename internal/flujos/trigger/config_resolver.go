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
//  2. Si no hay intención o ninguna regla llm casa: event_start Y keyword por
//     sig.Text, compitiendo EN EL MISMO PELDAÑO (Plan 043 · D-043.2: event_start es
//     PAR de keyword, no un peldaño aparte) → {Start,FlowID} o
//     {StartEvent,EventKind,FlowID} según el kind de la regla ganadora. La colisión
//     entre ambos kinds la resuelve el desempate de siempre (específica-de-sesión
//     antes que global, priority desc, exact antes que contains, keyword asc,
//     trigger_id asc): el kind NO desempata, para que el dueño mande con priority.
//  3. Si tampoco: fallback habilitado → {Fallback, FlowID} (mejor por el mismo orden).
//  4. Si nada → {Ignore}.
//
// event_stop NO se evalúa aquí: sin conversación viva no hay evento activo que
// desactivar. Su sitio es ResolveLive.
func (c *ConfigResolver) Resolve(ctx context.Context, tenantID, sessionID string, sig Signal) (Decision, error) {
	if sig.Intent != nil {
		llm, err := c.store.ListByKind(ctx, tenantID, sessionID, KindLLM)
		if err != nil {
			return Decision{}, err
		}
		matches := make([]Rule, 0, len(llm))
		for _, r := range llm {
			if !r.Enabled || !matchIntent(r, sig.Intent.Name) {
				continue
			}
			// SCOPING POR EVENTO ACTIVO (Plan 043 · T5.3, D-043.9). Una regla llm
			// ANOTADA con un event_kind distinto del activo NO casa: el intent cae a
			// `desconocido` de facto —la señal sigue al peldaño de texto— igual que
			// cuando el clasificador no supera el umbral.
			//
			// Las dos guardas de retrocompatibilidad son la mitad del contrato:
			// sin evento activo (ActiveEventKind == "") no se acota NADA, y una regla
			// SIN anotar (EventKind == "") sigue casando siempre. Un tenant del Plan
			// 029 se comporta byte a byte como antes.
			if sig.ActiveEventKind != "" && r.EventKind != "" && r.EventKind != sig.ActiveEventKind {
				continue
			}
			matches = append(matches, r)
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

	matches, err := c.matching(ctx, tenantID, sessionID, sig.Text, KindEventStart, KindKeyword)
	if err != nil {
		return Decision{}, err
	}
	if len(matches) > 0 {
		return decisionFor(matches[0]), nil
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

// ResolveLive decide qué hacer con un entrante que llega SOBRE UNA CONVERSACIÓN VIVA
// (Plan 043 · D-043.2). Es la EXCEPCIÓN ACOTADA de INV-02: solo se consultan los dos
// kinds que son texto EXACTO configurado por el tenant —event_start (salto por tipo) y
// event_stop (desactivar sin matar)—; keyword, fallback y llm NO se evalúan, de modo
// que para todo lo demás el texto sigue siendo del módulo y INV-02 queda intacto.
//
// El desempate es el MISMO que en Resolve (matching → sortByPriority), así que un
// event_start y un event_stop que casaran a la vez se resuelven por priority, no por
// el orden en que se consultaron. Sin coincidencia → {Ignore}: el turno sigue siendo
// del módulo, exactamente como antes de este plan (INV-6).
func (c *ConfigResolver) ResolveLive(ctx context.Context, tenantID, sessionID, text string) (Decision, error) {
	// El interruptor de APAGADO se pregunta PRIMERO, y esto no es una preferencia de
	// orden: es un arreglo. Con la configuración natural del dueño —event_start
	// «carrito» por contains para entrar y event_stop «salir del carrito» por contains
	// para salir— el texto «salir del carrito» casa las DOS reglas, y con la priority
	// por defecto el desempate por keyword las ordena justo al revés de lo pedido
	// («carrito» < «salir del carrito»): ganaba el arranque y el cliente acababa
	// DENTRO del carrito del que quería salir.
	//
	// La alternativa —documentarlo y pedirle al dueño que suba la priority del stop—
	// se descartó: la configuración que cualquiera escribiría produce el
	// comportamiento contrario al pedido y el dueño no tiene forma de sospecharlo. La
	// precedencia vive en el código, no en la disciplina del usuario.
	//
	// Dentro de CADA kind sigue mandando el desempate de siempre (sortByPriority).
	stops, err := c.matching(ctx, tenantID, sessionID, text, KindEventStop)
	if err != nil {
		return Decision{}, err
	}
	if len(stops) > 0 {
		return decisionFor(stops[0]), nil
	}
	starts, err := c.matching(ctx, tenantID, sessionID, text, KindEventStart)
	if err != nil {
		return Decision{}, err
	}
	if len(starts) == 0 {
		return Decision{Action: Ignore}, nil
	}
	return decisionFor(starts[0]), nil
}

// matching devuelve las reglas HABILITADAS de los kinds dados que casan el texto, YA
// ordenadas por el desempate maestro (sortByPriority). Reunir varios kinds en una sola
// lista es lo que permite que event_start y keyword compitan en el mismo peldaño sin
// que el orden de consulta decida nada.
func (c *ConfigResolver) matching(ctx context.Context, tenantID, sessionID, text string, kinds ...Kind) ([]Rule, error) {
	out := make([]Rule, 0, len(kinds))
	for _, k := range kinds {
		rules, err := c.store.ListByKind(ctx, tenantID, sessionID, k)
		if err != nil {
			return nil, err
		}
		for _, r := range rules {
			if r.Enabled && match(r, text) {
				out = append(out, r)
			}
		}
	}
	sortByPriority(out)
	return out, nil
}

// decisionFor proyecta la regla GANADORA a su Decision según el kind (INV-5: el switch
// de interpretación vive aquí, no en el runtime). event_start lleva el EventKind —el
// tipo que el cliente pidió— y arrastra el FlowID si la regla lo trae: para menu no
// hay flujo que arrancar (el despachador es un componente del runtime, D-043.3), y
// para cart/survey el dueño puede apuntar al suyo. Cualquier otro kind conserva la
// semántica histórica {Start, FlowID}.
func decisionFor(r Rule) Decision {
	switch r.Kind {
	case KindEventStart:
		return Decision{Action: StartEvent, FlowID: r.FlowID, EventKind: r.EventKind}
	case KindEventStop:
		return Decision{Action: StopEvent}
	default:
		return Decision{Action: Start, FlowID: r.FlowID}
	}
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
// → keyword asc → trigger_id asc. La especificidad de sesión es el criterio MAESTRO:
// una regla de sesión gana a una global aunque tenga menor priority.
//
// El trigger_id final cierra el orden TOTAL (Plan 043 · T2.1). Antes bastaba el orden
// estable porque cada llamada ordenaba reglas de UN kind y el store ya las entregaba
// por trigger_id; desde que event_start y keyword compiten en el mismo peldaño, la
// lista viene de dos consultas y el empate exacto quedaría a merced del orden de
// concatenación. El kind NO desempata a propósito: quien manda es la priority del dueño.
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
		if a.Keyword != b.Keyword {
			return a.Keyword < b.Keyword
		}
		return a.TriggerID < b.TriggerID
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
