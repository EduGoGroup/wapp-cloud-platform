// Porta internal/flujos/events/store.go @ 9d5a4b6 (trozo: rescatables y listado del dueño; ver el
// reparto en store.go).
//
// Lo que este trozo lleva en el verde, además de sus cuatro exportados: LA consulta de rescatables
// del sistema (D-043.15), en piezas con nombre —el origen con sus dos LEFT JOIN (la vista
// public.event_content y public.tenant_settings), las dos mitades del contenido (`c.event_id IS
// NULL` y `c.state = 'alive'`), la marca derivada «vencido» como columna CALCULADA y el orden—, y
// el listado del dueño, que reusa esas mismas piezas. Reglas de esa consulta que los contratos de
// abajo prometen:
//
//   - INV-17: un evento cuyo contenido murió (`discarded`) o cuajó (`settled`) no se lista, no se
//     rescata y no se menciona;
//   - INV-19: ningún WHERE compara fechas. «Vencido» es una MARCA que informa, calculada en el
//     SELECT con el instante del reloj inyectado; el filtro `Stale` del listado se aplica sobre esa
//     columna ya calculada;
//   - el TTL de inactividad es el del tenant (tenant_settings.event_inactivity_ttl_seconds) y, sin
//     fila, 7200 s (2 h), el mismo default que la columna; 0 es «sin vencimiento», no «cero
//     segundos».

package events

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
)

// eventColumnsE es eventColumns cualificada con el alias `e`: la consulta de
// rescuables hace JOIN con la vista event_content y con tenant_settings, y entre
// las tres fuentes se repiten columnas (`kind` está en el evento y en la vista;
// `tenant_id` también en settings) — sin cualificar, Postgres la rechaza por
// ambigua.
//
// Va escrita y no derivada en tiempo de ejecución para que la consulta siga siendo
// una CONSTANTE: una consulta que se compone al arrancar es una que ya no se puede
// leer entera en el fuente (ni descartar de un vistazo como libre de concatenación).
// Lo que impide que diverja de eventColumns —y por tanto de scanEvent, donde
// desalinearse no da error de compilación sino datos cambiados de sitio— es
// TestEventColumns_QualifiedListIsTheSameList, que las compara columna a columna.
const eventColumnsE = `e.id, e.tenant_id, e.session_id, e.contact_id, e.kind, e.history_id, e.status,
	       e.flow_id, e.flow_version, e.created_at, e.last_activity_at, e.closed_at`

// scanRescuable lee una fila de la consulta de rescatables: las mismas columnas
// del evento MÁS la marca derivada «vencido» MÁS el contenido DERIVADO de la vista
// (contentDerived), en ese orden.
func scanRescuable(sc scanner) (Rescuable, error) {
	var (
		r        Rescuable
		closedAt sql.NullTime
	)
	dest := append(eventDest(&r.Event, &closedAt), &r.Stale, &r.ContentState, &r.ContentRef)
	if err := sc.Scan(dest...); err != nil {
		return Rescuable{}, err
	}
	r.ClosedAt = closedAt.Time
	return r, nil
}

// nullableID convierte un id opcional vacío en NULL.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// ── La consulta de RESCATABLES: una sola, en piezas nombradas ────────────────
//
// Es LA consulta de rescatables del sistema (D-043.15): el automensaje de rescate
// (T3.6), la entrada que ofrece cuando no hay evento (T3.8) y el listado por el que
// el dueño limpia (T3.9b) leen todos de aquí. Está partida en piezas con nombre
// para que reusarla no obligue a copiarla: quien necesite otro filtro compone sobre
// rescuableFrom y rescuableWhere en vez de escribir un segundo FROM que mañana
// diga otra cosa.

// rescuableFrom es el origen: el evento MÁS su contenido según la vista-registro
// public.event_content (D-043.22) MÁS la config del tenant (para saber cuánto
// silencio tolera).
//
// El padre pregunta por su contenido SIN conocer a nadie: la vista la definen y
// migran los dominios de contenido, y aquí solo se lee su vocabulario genérico
// (`alive`/`settled`/`discarded`). La vista tiene A LO SUMO una fila por evento
// (índice único parcial del lado del contenido), así que el LEFT JOIN no
// multiplica filas. Los dos JOIN son LEFT y no INNER a propósito: un evento sin
// contenido (un menú, una encuesta) no tiene fila en la vista y NO puede
// desaparecer por eso, y un tenant que nunca configuró nada no tiene fila en
// tenant_settings y tampoco. Sin CAST en la ligadura evento↔contenido (UUID en
// las dos puntas); el del tenant es real: conversation_events.tenant_id es UUID y
// tenant_settings.tenant_id es TEXT (migración 0013).
const rescuableFrom = `
  FROM public.conversation_events e
  LEFT JOIN public.event_content c   ON c.event_id = e.id
  LEFT JOIN public.tenant_settings s ON s.tenant_id = e.tenant_id::text`

// contentNone es la mitad «este evento no tiene contenido»: los tipos que no lo
// producen y los que aún no llegaron a producirlo. Es el conjunto que solo
// `…/cancel` puede cerrar, y por eso vale de filtro por sí sola en el listado del
// dueño (REQ-28, T3.9b): sin fila en la vista no hay contenido que declarar.
const contentNone = `c.event_id IS NULL`

// contentAlive es la otra mitad: su contenido sigue VIVO. Con el LEFT JOIN un
// evento sin contenido da `c.state` NULL, y `NULL = 'alive'` es NULL —no true—,
// así que por aquí no se cuela ninguno de los de contentNone. Las dos mitades son
// disjuntas, y eso es lo que permite que el listado ofrezca `none` y `alive` como
// filtros distintos sin escribir una condición nueva.
const contentAlive = `c.state = 'alive'`

// rescuableContent es la condición de CONTENIDO de T3.6/REQ-26c: un evento cuyo
// contenido ya MURIÓ (`discarded`) o ya CUAJÓ (`settled`) no se lista, no se
// rescata y no se menciona (INV-17) — la escena de Marta. Qué transiciones del
// hijo producen cada estado vive del lado del contenido, en la migración de la
// vista: aquí no hay ni un literal de ese dominio.
//
// Va partida en sus dos mitades con nombre, y no escrita de corrido, porque el
// listado del dueño (T3.9b) filtra por UNA de ellas: sin el corte, ese filtro
// tendría que escribir su propio `c.event_id IS NULL`, y entonces habría dos
// sitios diciendo qué es «sin contenido» — que es justo lo que REQ-28 prohíbe.
const rescuableContent = `(` + contentNone + ` OR ` + contentAlive + `)`

// contentDerived expone el contenido DERIVADO del join como columnas del SELECT:
// el estado y el ref de la vista, con ” cuando no hay fila (el evento sin
// contenido). DERIVADO quiere decir exactamente eso (D-043.22): no hay columna en
// conversation_events que lo almacene ni INSERT que lo escriba — si el hijo
// cambia, la siguiente lectura ya lo dice, sin sincronizar nada.
const contentDerived = `COALESCE(c.state, '') AS content_state,
       COALESCE(c.ref::text, '') AS content_ref`

// rescuableWhere es el filtro.
//
// INV-19: aquí NO hay ni una comparación de fechas. El vencimiento es una MARCA que
// informa (ver rescuableStale), no un filtro: un evento vencido sigue siendo
// rescatable, que es justo lo que hace que nadie pierda un pedido por callarse.
const rescuableWhere = `
 WHERE e.tenant_id = $1 AND e.session_id = $2 AND e.contact_id = $3
   AND e.status = 'open'
   AND ` + rescuableContent

// rescuableOrder: lo primero que se ofrece retomar es lo último que se tocó. El
// desempate por id hace el orden total y por tanto el test determinista.
const rescuableOrder = `
 ORDER BY e.last_activity_at DESC, e.id`

// rescuableLimit acota el lote. NULLIF(...,0) traduce «sin tope» a LIMIT NULL, que
// para Postgres es no tener límite: así el tope es un PARÁMETRO y no obliga a
// concatenar SQL ni a tener dos consultas, una con LIMIT y otra sin él.
const rescuableLimit = `
 LIMIT NULLIF($5::bigint, 0)`

// rescuableTTL es el reloj de conversación del tenant, con el default de PLATAFORMA
// puesto cuando no tiene fila de config. El 7200 (2 h) espeja el DEFAULT de
// event_inactivity_ttl_seconds de la migración 0052 y el store.DefaultEventInactivityTTL
// de Go; se repite aquí —y no se importa— porque importarlo obligaría a este
// paquete a depender del store del motor entero por una constante. Lo que ata esta
// punta es la suite contra Postgres (eventshelpertest, ListRescuable_StaleIsAMarkNotAFilter):
// un tenant SIN fila vence pasadas 2 h justas, ni antes ni en el segundo exacto.
const rescuableTTL = `COALESCE(s.event_inactivity_ttl_seconds, 7200)`

// rescuableStale es la marca DERIVADA «vencido» (T3.9a) como columna CALCULADA del
// SELECT.
//
// Dos decisiones que no son de estilo:
//
//   - El instante viene por PARÁMETRO ($4), no de now() de la BD. El reloj de este
//     store es uno y es inyectable (ver el comentario de Store): un now() de
//     servidor no se puede fijar, y entonces «el 15 de enero sigue en la lista y
//     llega marcada vencida» no se puede afirmar en un test.
//   - El `> 0` NO sobra: 0 es el override «sin vencimiento» del tenant (E-6), no un
//     plazo de cero segundos. Sin esa mitad, un tenant que apagó el reloj vería TODO
//     marcado como vencido. Es la misma regla que la función pura IsSuspended,
//     escrita en el otro idioma.
const rescuableStale = `(` + rescuableTTL + ` > 0
        AND $4::timestamptz - e.last_activity_at > make_interval(secs => ` + rescuableTTL + `)) AS stale`

const selectRescuableSQL = `
SELECT ` + eventColumnsE + `,
       ` + rescuableStale + `,
       ` + contentDerived + rescuableFrom + rescuableWhere + rescuableOrder + rescuableLimit

// ListRescuable devuelve los eventos que se le pueden ofrecer al contacto para
// retomarlos, ordenados por ÚLTIMA ACTIVIDAD descendente y marcados con «vencido».
//
// No es ListAlive con otro ORDER BY, aunque lo pareciera antes de T3.6: filtra
// además por el contenido (INV-17). Son dos preguntas distintas —«¿qué
// tiene abierto?» y «¿qué le ofrezco retomar?»— y desde INV-17 tienen respuestas
// distintas: un carrito cuyo pedido descartó el dueño sigue vivo pero ya no se
// ofrece.
//
// limit <= 0 ⇒ sin tope. Un evento suspendido sigue aquí, marcado: suspendido no es
// muerto (E-6), y por eso la marca va en el SELECT y no en el WHERE (INV-19).
//
// Cada Rescuable trae, además del evento, Stale (la marca) y el contenido DERIVADO de la vista:
// ContentState ("" sin contenido, o "alive") y ContentRef ("" o el id del contenido). Desempate
// del orden: id ascendente. Acota por (tenant, sesión, contacto) y status = 'open'.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: listar eventos rescatables: %w" si la consulta falla;
//   - "events: leer fila de evento: %w", "events: recorrer eventos vivos: %w" y
//     "events: cerrar filas de eventos: %w", como ListAlive (el segundo texto dice «vivos» también
//     aquí: se porta tal cual).
func (s *Store) ListRescuable(ctx context.Context, tenantID, sessionID, contactID string, limit int) ([]Rescuable, error) {
	if limit < 0 {
		limit = 0 // negativo no es «al revés»: es «sin tope», y Postgres rechazaría el LIMIT
	}
	rows, err := s.db.QueryContext(ctx, selectRescuableSQL,
		tenantID, sessionID, contactID, s.now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("events: listar eventos rescatables: %w", err)
	}
	return collect(rows, scanRescuable)
}

// ── Los tipos que el tenant PUEDE ver (decisión de Jhoan, 2026-08-09) ────────
//
// Las dos funciones de aquí no tienen tabla propia ni criterio propio: leen el
// MISMO mapa tipo → feature con el que el despachador arma el menú del cliente (T2.3).
// Que las dos superficies discrepen —que el menú ofrezca «encuesta» y la bandeja
// del dueño no enseñe sus encuestas, o al revés— sería peor que cualquiera de las
// dos políticas por separado, y con dos mapas acabaría pasando.

// KindFeatures devuelve, en orden alfabético y sin repetir, las features que
// habilitan ALGÚN tipo de evento. Es la lista con la que se gatea el listado del
// dueño: basta UNA para entrar (entitlements.RequireAnyFeature).
//
// El orden es alfabético por ser determinista y no significar nada más: el gate
// pasa con cualquiera, así que un orden con intención sería inventarse una
// prioridad que no existe.
//
// Hoy son cuatro tipos y cuatro features: menu → entitlements.FeatureMenu, cart →
// entitlements.FeatureCartBasic, survey → entitlements.FeatureSurvey, media →
// entitlements.FeatureMedia. Cada llamada devuelve un slice NUEVO.
func KindFeatures() []string {
	out := make([]string, 0, len(featureByKind))
	for _, f := range featureByKind {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// AllowedKinds devuelve los tipos de evento que ESTE tenant tiene habilitados, en
// orden alfabético. Es lo que acota el CONTENIDO del listado: pasar el gate por
// tener una feature no da derecho a ver los tipos de las otras.
//
// Consecuencia viva y DESEADA: si un tenant pierde una feature, sus eventos de ese
// tipo dejan de aparecer en la bandeja. No se borra nada —las filas siguen ahí,
// intactas— y vuelven a listarse en cuanto la recupere. Es la misma regla que ya
// gobierna el menú que ve el cliente, y tenerla en un solo sitio es justo lo que
// impide que las dos pantallas cuenten historias distintas.
//
// Un error del resolver se PROPAGA, y aquí sí se separa del gate: aquel decide el
// ACCESO y falla cerrado con un 403 (mejor negar que abrir por un fallo
// transitorio); este decide el CONTENIDO, y devolver silenciosamente una lista
// recortada le diría a quien limpia «ya no queda nada» cuando la verdad es «no
// pude mirar». Eso se contesta con un 5xx, no con una bandeja vacía.
//
// Hace un Has por tipo. Sin ninguna feature devuelve la lista vacía (no nil) y sin error.
//
// Textos de error (literales):
//   - "events: sin resolver de features no se puede saber qué tipos ve el tenant" si feats es nil;
//   - "events: resolver la feature %q del tenant: %w" si el resolver falla.
func AllowedKinds(ctx context.Context, feats entitlements.Resolver, tenantID string) ([]string, error) {
	if feats == nil {
		return nil, fmt.Errorf("events: sin resolver de features no se puede saber qué tipos ve el tenant")
	}
	out := make([]string, 0, len(featureByKind))
	for kind, feature := range featureByKind {
		has, err := feats.Has(ctx, tenantID, feature)
		if err != nil {
			return nil, fmt.Errorf("events: resolver la feature %q del tenant: %w", feature, err)
		}
		if has {
			out = append(out, kind)
		}
	}
	slices.Sort(out)
	return out, nil
}

// listEventsWhere es el filtro del listado. Los tres filtros opcionales van con la
// forma `$n IS NULL OR col = $n` y no concatenando trozos de SQL: así la consulta
// sigue siendo UNA constante —legible entera en el fuente, imposible de inyectar—
// en vez de un string que se arma en tiempo de ejecución.
//
// El cast de cada parámetro es el de SU columna, y no uno genérico a text: la de
// contacto es UUID (ADR-0017) y `contact_id = $5::text` no compara —Postgres corta
// con «operator does not exist: uuid = text» ANTES de mirar ninguna fila, también
// cuando el filtro va vacío—. Se descubrió contra Postgres real; con un doble en
// memoria habría llegado hasta producción. Casteando la COLUMNA a text en vez del
// parámetro también compilaría, pero dejaría fuera al índice
// conversation_events_alive_idx, que empieza justo por ahí.
//
// $4 NO aparece aquí y su hueco está reservado a propósito: es el instante del
// reloj inyectado que consume rescuableStale, y respetarle la posición es lo que
// permite reusar esa expresión TAL CUAL en vez de escribir una segunda.
//
// INV-19: ni una comparación de fechas. El vencido se filtra después, y sobre la
// columna calculada (listEventsStale).
const listEventsWhere = `
 WHERE e.tenant_id = $1
   AND e.status = $2
   AND ($3::text IS NULL OR e.kind = $3)
   AND ($5::uuid IS NULL OR e.contact_id = $5)
   AND ($8::text[] IS NULL OR e.kind = ANY($8))
   AND (($6::text = '` + string(ContentAny) + `' AND ` + rescuableContent + `)
        OR ($6::text = '` + string(ContentNone) + `' AND ` + contentNone + `)
        OR ($6::text = '` + string(ContentAlive) + `' AND ` + contentAlive + `))`

// listEventsInner es el evento con su marca de vencido y su contenido derivado ya
// resueltos. Las piezas que lo componen son las MISMAS que sirven al rescate.
const listEventsInner = `
SELECT ` + eventColumnsE + `,
       ` + rescuableStale + `,
       ` + contentDerived + rescuableFrom + listEventsWhere

// listEventsStale filtra por «vencido» SOBRE LA COLUMNA CALCULADA de la subconsulta
// (que se llama `e` para que el ORDER BY del rescate valga tal cual aquí fuera).
//
// Esto es lo que INV-19 pide y no una forma retorcida de saltárselo: la comparación
// de fechas sigue viviendo en el SELECT, se evalúa para TODAS las filas y la marca
// se sigue devolviendo en las que no se filtran. Meter `now() - last_activity_at >
// ttl` en el WHERE de la lista sería lo prohibido, y además otra cosa: haría que
// «vencido» dejara de informar para empezar a decidir.
const listEventsStale = `
 WHERE ($7::boolean IS NULL OR e.stale = $7)`

const listEventsSQL = `
SELECT * FROM (` + listEventsInner + `
) e` + listEventsStale + rescuableOrder + `
 LIMIT $9 OFFSET $10`

// countEventsSQL cuenta las coincidencias del MISMO filtro, y por eso reusa la
// misma subconsulta: dos WHERE que tuvieran que coincidir a mano acabarían no
// coincidiendo, y un paginador que miente sobre el total manda a Herminia a una
// página vacía. Referencia hasta $8, así que se invoca con OCHO argumentos —los
// de paginación no entran.
const countEventsSQL = `
SELECT count(*) FROM (` + listEventsInner + `
) e` + listEventsStale

// listArgs son los OCHO argumentos del filtro, en el orden de los $n. El instante
// va en $4 aunque el WHERE no lo use: ese hueco es de rescuableStale.
func (s *Store) listArgs(tenantID string, f ListFilter) []any {
	// Los dos nil van como `any` y no como *bool / []string: así el driver recibe un
	// NULL a secas, que es lo que `$7::boolean IS NULL` y `$8::text[] IS NULL`
	// esperan, sin depender de que la capa de conversión desreferencie punteros ni
	// distinga un slice nil de uno vacío por su cuenta.
	var stale any
	if f.Stale != nil {
		stale = *f.Stale
	}
	// nil ⇒ sin filtro; una lista VACÍA ⇒ ningún tipo pasa (`kind = ANY('{}')` es
	// falso para todas las filas). La distinción no es un tecnicismo de Go: es la
	// diferencia entre «no me filtres por tipo» y «este tenant no tiene ninguno
	// habilitado», y confundirlas enseñaría la bandeja ENTERA justo en el caso en
	// que no debe verse nada. Falla cerrado, como el gate.
	var kinds any
	if f.Kinds != nil {
		kinds = f.Kinds
	}
	return []any{tenantID, string(f.Status), nullableID(f.Kind), s.now().UTC(),
		nullableID(f.ContactID), string(f.Content), stale, kinds}
}

// ListEvents devuelve la página de eventos del TENANT que casan con el filtro,
// ordenados por última actividad descendente y con la marca «vencido» resuelta.
//
// Es la lectura de REQ-28: la que hace ejecutable «ve limpiando». Nunca acota a una
// sesión ni a un contacto salvo que el filtro lo pida, y el tenant lo pone el
// llamante desde el token (INV-8) — este método no tiene forma de sacarlo de otro
// sitio, que es justo la garantía.
//
// El filtro se normaliza (ListFilter.Normalized) antes de consultar, y la página devuelta trae el
// Page y el PageSize YA normalizados. Total cuenta las coincidencias del filtro entero, no las de
// la página. Son DOS sentencias —la cuenta y la página— sobre la misma subconsulta: la cuenta
// lleva los ocho argumentos del filtro y la página esos ocho más el tamaño y el desplazamiento.
// El instante del reloj inyectado viaja como cuarto argumento de las dos.
//
// Filtros: Status exacto; Kind exacto si no va vacío; Kinds como conjunto cerrado (nil = sin
// filtro, lista vacía = ninguno pasa); ContactID exacto si no va vacío; Content any|none|alive
// (un evento con contenido `settled` o `discarded` no sale con NINGUNO de los tres, INV-17);
// Stale sobre la marca calculada. Orden: last_activity_at descendente y, a igual instante, id.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: contar los eventos del tenant: %w";
//   - "events: listar los eventos del tenant: %w";
//   - los tres de recorrido de ListAlive, tal cual.
func (s *Store) ListEvents(ctx context.Context, tenantID string, f ListFilter) (EventPage, error) {
	f = f.Normalized()
	args := s.listArgs(tenantID, f)

	var total int
	if err := s.db.QueryRowContext(ctx, countEventsSQL, args...).Scan(&total); err != nil {
		return EventPage{}, fmt.Errorf("events: contar los eventos del tenant: %w", err)
	}

	paged := make([]any, 0, len(args)+2)
	paged = append(paged, args...)
	paged = append(paged, f.PageSize, f.Offset())
	rows, err := s.db.QueryContext(ctx, listEventsSQL, paged...)
	if err != nil {
		return EventPage{}, fmt.Errorf("events: listar los eventos del tenant: %w", err)
	}
	evs, err := collect(rows, scanRescuable)
	if err != nil {
		return EventPage{}, err
	}
	return EventPage{Events: evs, Page: f.Page, PageSize: f.PageSize, Total: total}, nil
}
