// Porta internal/flujos/trigger/config_resolver.go @ c0c0c03

package trigger

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ConfigResolver es el adapter que resuelve disparos leyendo las reglas del
// TriggerStore (100% de BD, cero hardcode). Implementa Resolver.
//
// El switch por match_type/kind vive SOLO aquí (INV-5): el engine y el runtime
// no conocen la estrategia de coincidencia.
//
// # Lo que promete, común a los tres métodos
//
//   - Solo cuentan las reglas HABILITADAS (Enabled) que el store entrega para el
//     tenant y la sesión: las acotadas a esa sesión y las globales (INV-8: una regla
//     de otro tenant nunca es visible; una de otra sesión, tampoco).
//   - Un error del store se devuelve tal cual, con la decisión cero.
//   - NORMALIZACIÓN: texto y keyword pasan por la MISMA función antes de comparar:
//     minúsculas (strings.ToLower, mapeo simple runa a runa) → descomposición NFD y
//     descarte de las marcas combinantes (unicode.Mn) → recorte y colapso de todo
//     tramo de espacios Unicode (unicode.IsSpace: incluye \t, \n, NBSP, U+2009,
//     U+3000) a un solo espacio. Consecuencias que el corpus de
//     config_resolver_normalize_test.go fija: «MENÚ», «menú» en NFC y en NFD y
//     «menu» son lo mismo; «año» y «ano» también (la virgulilla es una marca); la
//     İ turca baja a «i» y la ı sin punto NO es «i»; los dígitos no ASCII, las
//     ligaduras y los anchos completos NO se pliegan (no hay NFKD); U+200B no es
//     espacio; los signos no se tocan («a@@b» no es «a@b»).
//   - COINCIDENCIA: MatchContains ⇒ el texto normalizado contiene la keyword
//     normalizada; MatchExact —y cualquier match_type desconocido, por conservador—
//     ⇒ son iguales. Una keyword que normaliza a vacío NUNCA casa.
//   - DESEMPATE entre reglas que casan, total y determinista: específica de sesión
//     antes que global (criterio MAESTRO: gana aunque tenga menor priority) →
//     priority desc → exact antes que contains → keyword asc → trigger_id asc. El
//     kind NO desempata: manda la priority del dueño.
type ConfigResolver struct{}

// NewConfigResolver construye el resolver sobre el store dado. No lo consulta.
func NewConfigResolver(_ Store) *ConfigResolver {
	panic(pendiente.Implementar("trigger.NewConfigResolver"))
}

// Resolve decide qué hacer con un entrante sin conversación viva (sessionID = la
// sesión del entrante; el store ya filtró a reglas específicas de esa sesión O
// globales, Plan 020 · T4). ORDEN de resolución (design.md §4.c, INV-5: todo el
// switch de interpretación de la señal vive AQUÍ):
//  1. Si la señal trae intención (sig.Intent != nil): reglas kind='llm' cuyo
//     keyword (nombre de intent) casa EXACTO-normalizado el nombre de la intención
//     (sin mirar MatchType; una keyword vacía nunca casa) → {Start, FlowID, Params,
//     IntentName} (mismas reglas de session_id/priority/enabled que keyword) o, si
//     la regla GANADORA trae event_kind, → {StartEvent, FlowID, EventKind, Params,
//     IntentName}: la SEGUNDA puerta del nacimiento (Plan 043 · T2.5/REQ-01b,
//     construida por el Plan 054 · F2b). Los Params viajan a la decisión para que el
//     runtime pre-cargue el flujo (T8), con o sin evento.
//     SCOPING POR EVENTO ACTIVO (Plan 043 · T5.3, D-043.9): una regla llm ANOTADA
//     con un event_kind distinto de sig.ActiveEventKind NO casa, y la señal sigue al
//     peldaño de texto. Sin evento activo (ActiveEventKind == "") no se acota nada,
//     y una regla SIN anotar casa siempre.
//     🔴 Deuda D-5: en producción sig.Intent es siempre nil y este peldaño no lo
//     alcanza nadie. Se conserva con su test; no se arregla.
//  2. Si no hay intención o ninguna regla llm casa: event_start Y keyword por
//     sig.Text, compitiendo EN EL MISMO PELDAÑO (Plan 043 · D-043.2: event_start es
//     PAR de keyword, no un peldaño aparte) → {Start,FlowID} o
//     {StartEvent,EventKind,FlowID} según el kind de la regla ganadora, sin Params
//     ni IntentName. La colisión entre ambos kinds la resuelve el desempate de
//     siempre: el kind NO desempata, para que el dueño mande con priority.
//  3. Si tampoco: fallback habilitado → {Fallback, FlowID}, el mejor por
//     específica-de-sesión → priority desc → keyword asc → trigger_id asc.
//  4. Si nada → {Ignore}.
//
// event_stop NO se evalúa aquí: sin conversación viva no hay evento activo que
// desactivar. Su sitio es ResolveLive.
func (c *ConfigResolver) Resolve(_ context.Context, _, _ string, _ Signal) (Decision, error) {
	panic(pendiente.Implementar("trigger.ConfigResolver.Resolve"))
}

// ResolveLive decide qué hacer con un entrante que llega SOBRE UNA CONVERSACIÓN VIVA
// (Plan 043 · D-043.2). Es la EXCEPCIÓN ACOTADA de INV-02: solo se consultan los dos
// kinds que son texto EXACTO configurado por el tenant —event_start (salto por tipo) y
// event_stop (desactivar sin matar)—; keyword, fallback y llm NO se evalúan, de modo
// que para todo lo demás el texto sigue siendo del módulo y INV-02 queda intacto.
//
// Devuelve {StopEvent} (sin EventKind: corta el activo sea del tipo que sea),
// {StartEvent, EventKind, FlowID} o {Ignore}. event_stop se pregunta PRIMERO y, si
// alguna regla suya casa, gana a cualquier event_start, tenga la priority que tenga:
// con «carrito» por contains para entrar y «salir del carrito» por contains para
// salir, el texto «salir del carrito» casa las dos y el desempate por keyword las
// ordenaría al revés de lo pedido. Dentro de CADA kind manda el desempate de siempre.
// Sin coincidencia → {Ignore}: el turno sigue siendo del módulo (INV-6).
func (c *ConfigResolver) ResolveLive(_ context.Context, _, _, _ string) (Decision, error) {
	panic(pendiente.Implementar("trigger.ConfigResolver.ResolveLive"))
}

// IsEscape indica si el texto casa alguna regla kind=escape habilitada del tenant
// aplicable a la sesión y, de casar, devuelve el aviso configurado en esa regla
// (r.Message; vacío si la regla no define uno ⇒ el runtime cae a su aviso por
// defecto). Si casan una regla específica de sesión y una global, gana la
// ESPECÍFICA (su message); entre reglas de la misma especificidad gana la primera
// que entrega el store (trigger_id asc): aquí la priority NO desempata.
func (c *ConfigResolver) IsEscape(_ context.Context, _, _, _ string) (bool, string, error) {
	panic(pendiente.Implementar("trigger.ConfigResolver.IsEscape"))
}
