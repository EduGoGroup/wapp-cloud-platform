package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// Store es el almacén Postgres del evento conversacional y de su historial.
//
// Dos garantías que NO son del llamador y por eso viven aquí:
//
//   - EL RELOJ ES UNO Y ES INYECTABLE. HistoryID, el nacimiento y cada Touch salen
//     de now, no de now() de la BD. Un reloj que no se puede fijar no se puede
//     testear, y un id legible derivado de un reloj que nadie controla no se puede
//     afirmar.
//   - NO HAY PUERTA PARA PERSISTIR TEXTO LIBRE EN CLARO. AppendMessage es la única
//     entrada de texto literal y cifra SIEMPRE (no admite variante sin cifrar);
//     AppendSummary solo acepta estructura ya serializada (json.RawMessage), que es
//     el nivel 1 en claro del ADR-0034 y no un cajón de prosa.
type Store struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
	now    func() time.Time
}

// Option configura el Store en la construcción.
type Option func(*Store)

// WithClock inyecta el reloj del store: el que compone HistoryID, el que estampa
// el nacimiento y el que refresca last_activity_at en Touch. Sin él, NewStore usa
// time.Now. Existe para tests deterministas (mismo patrón que el runtime).
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// NewStore construye el store sobre el pool dado. cipher es obligatorio para
// AppendMessage: sin él, la única puerta de texto literal queda cerrada (devuelve
// error) en vez de abrir una que escriba en claro.
func NewStore(db *sql.DB, cipher *crypto.FieldCipher, opts ...Option) *Store {
	s := &Store{db: db, cipher: cipher, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// eventColumns es la lista de columnas que scanEvent espera, en orden.
const eventColumns = `id, tenant_id, session_id, contact_id, kind, history_id, status,
	       flow_id, flow_version, created_at, last_activity_at, closed_at`

// eventColumnsE es eventColumns cualificada con el alias `e`: la consulta de
// rescatables hace JOIN con la vista event_content y con tenant_settings, y entre
// las tres fuentes se repiten columnas (`kind` está en el evento y en la vista;
// `tenant_id` también en settings) — sin cualificar, Postgres la rechaza por
// ambigua.
//
// Va escrita y no derivada en tiempo de ejecución para que la consulta siga siendo
// una CONSTANTE: una consulta que se compone al arrancar es una que ya no se puede
// leer entera en el fuente (ni descartar de un vistazo como libre de concatenación).
// Lo que impide que diverja de eventColumns —y por tanto de scanEvent, donde
// desalinearse no da error de compilación sino datos cambiados de sitio— es
// TestQualify_LaListaCualificadaEsLaMISMA, que las compara columna a columna.
const eventColumnsE = `e.id, e.tenant_id, e.session_id, e.contact_id, e.kind, e.history_id, e.status,
	       e.flow_id, e.flow_version, e.created_at, e.last_activity_at, e.closed_at`

// scanner abstrae *sql.Row y *sql.Rows para compartir scanEvent.
type scanner interface {
	Scan(dest ...any) error
}

// eventDest son los destinos de scan de un Event EN EL ORDEN de eventColumns. Es
// la única lista de campos del paquete: scanEvent y scanRescuable la comparten, y
// así una columna nueva no puede quedar leída en un sitio y olvidada en el otro.
func eventDest(ev *Event, closedAt *sql.NullTime) []any {
	return []any{&ev.ID, &ev.TenantID, &ev.SessionID, &ev.ContactID, &ev.Kind,
		&ev.HistoryID, &ev.Status, &ev.FlowID, &ev.FlowVersion,
		&ev.CreatedAt, &ev.LastActivityAt, closedAt}
}

// scanEvent lee una fila de conversation_events en un Event, traduciendo el
// nullable (closed_at) a su cero de Go.
func scanEvent(sc scanner) (Event, error) {
	var (
		ev       Event
		closedAt sql.NullTime
	)
	if err := sc.Scan(eventDest(&ev, &closedAt)...); err != nil {
		return Event{}, err
	}
	ev.ClosedAt = closedAt.Time
	return ev, nil
}

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

const insertEventSQL = `
INSERT INTO public.conversation_events
       (tenant_id, session_id, contact_id, kind, history_id, status,
        flow_id, flow_version, created_at, last_activity_at)
VALUES ($1, $2, $3, $4, $5, 'open', $6, $7, $8, $8)
RETURNING ` + eventColumns

// CreateEvent inserta un evento VIVO y devuelve la fila tal como quedó.
//
// El padre nace SIN columnas de ningún hijo (D-043.21): si el evento produce
// contenido durable, es el contenido quien declara a su padre con su event_id —
// nunca al revés.
//
// El nacimiento y el reloj arrancan en el MISMO instante del reloj inyectado (por
// eso $8 va dos veces), y de ese instante sale también HistoryID en UTC: la fila
// no puede quedar diciendo que nació a una hora y que su id legible es de otra.
//
// Si ya hay un evento vivo de ese tipo en la conversación devuelve ErrAliveExists.
// Esa regla la impone el índice único parcial de la BD (E-2), no una comprobación
// previa del código: entre un SELECT y un INSERT cabe otro escritor, y en el
// índice no cabe nadie.
func (s *Store) CreateEvent(ctx context.Context, in NewEvent) (Event, error) {
	born := s.now().UTC()
	row := s.db.QueryRowContext(ctx, insertEventSQL,
		in.TenantID, in.SessionID, in.ContactID, in.Kind, HistoryID(in.Kind, born),
		in.FlowID, in.FlowVersion, born)

	ev, err := scanEvent(row)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return Event{}, fmt.Errorf("%w (tenant=%s sesión=%s tipo=%s): %w",
				ErrAliveExists, in.TenantID, in.SessionID, in.Kind, err)
		}
		return Event{}, fmt.Errorf("events: insertar evento: %w", err)
	}
	return ev, nil
}

const selectAliveByKindSQL = `
SELECT ` + eventColumns + `
  FROM public.conversation_events
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND kind = $4
   AND status = 'open'`

// GetAliveByKind devuelve el evento VIVO de ese tipo en la conversación. El
// segundo retorno dice si lo había: no haberlo es normal (E-6, el saludo no crea
// evento), no un error.
//
// No hace falta LIMIT 1: el índice único parcial garantiza que hay como mucho uno.
func (s *Store) GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (Event, bool, error) {
	row := s.db.QueryRowContext(ctx, selectAliveByKindSQL, tenantID, sessionID, contactID, kind)
	ev, err := scanEvent(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Event{}, false, nil
	case err != nil:
		return Event{}, false, fmt.Errorf("events: leer evento vivo de tipo %q: %w", kind, err)
	}
	return ev, true, nil
}

const selectAliveSQL = `
SELECT ` + eventColumns + `
  FROM public.conversation_events
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
   AND status = 'open'
 ORDER BY created_at, id`

// selectEventForTenantSQL lee UN evento por id ACOTADO AL TENANT. El `AND
// tenant_id` no es una optimización: es el aislamiento (INV-8) escrito en el SQL,
// donde no se puede olvidar. Leer por id y comparar el tenant en Go dejaría una
// ventana en la que la fila ajena YA está en memoria y un refactor descuidado la
// devuelve.
const selectEventForTenantSQL = `
SELECT ` + eventColumns + `
  FROM public.conversation_events
 WHERE id = $1 AND tenant_id = $2`

// GetEventForTenant devuelve el evento id SI Y SOLO SI pertenece al tenant
// (T4.2). Cualquier ausencia —id inexistente, id de otro tenant, id que ni
// siquiera es un UUID— es el MISMO ErrEventNotFound: el llamante lo traduce a un
// 404 que no revela si el evento existe para otro (criterio literal de T4.2).
//
// El UUID se valida ANTES de consultar, por la misma razón que
// store.ListIntakeItems: el id llega de un segmento de URL que puede traer
// cualquier cosa, y sin la guarda Postgres contestaría 22P02 —un error real que
// acabaría en 500— a una pregunta cuya respuesta honesta es «eso no existe».
func (s *Store) GetEventForTenant(ctx context.Context, tenantID, eventID string) (Event, error) {
	if _, perr := uuid.Parse(eventID); perr != nil {
		return Event{}, fmt.Errorf("%w (id=%q no es un UUID)", ErrEventNotFound, eventID)
	}
	row := s.db.QueryRowContext(ctx, selectEventForTenantSQL, eventID, tenantID)
	ev, err := scanEvent(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Event{}, fmt.Errorf("%w (id=%s)", ErrEventNotFound, eventID)
	case err != nil:
		return Event{}, fmt.Errorf("events: leer el evento del tenant: %w", err)
	}
	return ev, nil
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
// paquete a depender del store del motor entero por una constante. Lo que ata las
// dos puntas es TestIntegration_MarcaVencidoSinFilaUsaElMismoDefaultQueLaBD, que
// compara un tenant sin fila con uno cuya fila trae el DEFAULT de la columna.
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

// ListAlive devuelve los eventos VIVOS de la conversación en orden de NACIMIENTO:
// status='open' y nada más, sin mirar la solicitud.
//
// ⚠️ NO es la lista de lo que se le puede ofrecer a un cliente. Un carrito cuyo
// pedido descartó el dueño sigue VIVO un rato —el descarte cancela el evento en
// otra sentencia— y aquí sale. Quien vaya a ENSEÑAR o RETOMAR algo usa
// ListRescuable (INV-17: no se lista, no se rescata y no se menciona); esta
// responde la otra pregunta, «¿qué tiene abierto?», que es de quien audita o
// ejecuta.
func (s *Store) ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, selectAliveSQL, tenantID, sessionID, contactID)
	if err != nil {
		return nil, fmt.Errorf("events: listar eventos vivos: %w", err)
	}
	return collect(rows, scanEvent)
}

// ListRescuable devuelve los eventos que se le pueden ofrecer al contacto para
// retomarlos, ordenados por ÚLTIMA ACTIVIDAD descendente y marcados con «vencido».
//
// No es ListAlive con otro ORDER BY, aunque lo pareciera antes de T3.6: filtra
// además por el contenido (rescuableWhere). Son dos preguntas distintas —«¿qué
// tiene abierto?» y «¿qué le ofrezco retomar?»— y desde INV-17 tienen respuestas
// distintas: un carrito cuyo pedido descartó el dueño sigue vivo pero ya no se
// ofrece.
//
// limit <= 0 ⇒ sin tope. Un evento suspendido sigue aquí, marcado: suspendido no es
// muerto (E-6), y por eso la marca va en el SELECT y no en el WHERE (INV-19).
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

// ── El listado por el que el DUEÑO limpia (T3.9b, REQ-28) ───────────────────
//
// Es la MISMA consulta de rescatables leída desde el otro lado del mostrador: allí
// la pregunta es «¿qué le ofrezco retomar a ESTE contacto?» y aquí «¿qué tengo
// abierto en todo el tenant?». Comparte FROM, condición de contenido, marca de
// vencido y orden — todo lo que se reusa está arriba, con su nombre; lo único que
// esta mitad añade son los filtros que la otra no necesitaba (tipo, estado,
// contacto opcional) y la paginación.
//
// Lo que NO comparte es el WHERE entero, y no puede: el rescate acota SIEMPRE a una
// conversación (sesión + contacto) y el dueño no acota a ninguna. Escribir un solo
// WHERE para las dos preguntas obligaría a que el rescate pasara sesión y contacto
// como «opcionales», y un filtro opcional que en realidad es obligatorio es la clase
// de cosa que se rompe en silencio el día que alguien lo llama con la cadena vacía.

// Paginación del listado (REQ-28): tamaño por defecto y cota superior. La cota no
// es cosmética — sin ella un GET sin filtros materializa todos los eventos vivos
// del tenant.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ── Los tipos que el tenant PUEDE ver (decisión de Jhoan, 2026-08-09) ────────
//
// Las dos funciones de aquí no tienen tabla propia ni criterio propio: leen el
// MISMO featurePorTipo con el que el despachador arma el menú del cliente (T2.3).
// Que las dos superficies discrepen —que el menú ofrezca «encuesta» y la bandeja
// del dueño no enseñe sus encuestas, o al revés— sería peor que cualquiera de las
// dos políticas por separado, y con dos mapas acabaría pasando.

// KindFeatures devuelve, en orden alfabético y sin repetir, las features que
// habilitan ALGÚN tipo de evento. Es la lista con la que se gatea el listado del
// dueño: basta UNA para entrar (RequireAnyFeature).
//
// El orden es alfabético por ser determinista y no significar nada más: el gate
// pasa con cualquiera, así que un orden con intención sería inventarse una
// prioridad que no existe.
func KindFeatures() []string {
	out := make([]string, 0, len(featurePorTipo))
	for _, f := range featurePorTipo {
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
func AllowedKinds(ctx context.Context, feats entitlements.Resolver, tenantID string) ([]string, error) {
	if feats == nil {
		return nil, fmt.Errorf("events: sin resolver de features no se puede saber qué tipos ve el tenant")
	}
	out := make([]string, 0, len(featurePorTipo))
	for kind, feature := range featurePorTipo {
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

// ContentFilter es la pregunta por el CONTENIDO del evento, y son exactamente
// tres respuestas porque la mitad `none` es la que hace falta que exista: el
// conjunto de eventos que no produjeron contenido (REQ-28), el que dejaba a
// Herminia sin dónde limpiar. El vocabulario público es `any|none|alive` y no
// cambia con la vista: es el contrato del transporte.
type ContentFilter string

const (
	// ContentAny no distingue entre las dos mitades: enseña las dos, y es el default.
	//
	// «Any» es cualquiera de los dos CONTENIDOS del predicado (rescuableContent), NO
	// «cualquier evento»: un evento cuyo contenido murió (`discarded`) o ya cuajó
	// (`settled`) NO sale por aquí, porque el listado del dueño va sobre la MISMA
	// consulta filtrada que el rescate (INV-17: no se lista, no se rescata, no se
	// menciona). Es el criterio literal de T3.9 —descartado el contenido, el evento
	// desaparece de la lista— y la razón de que esta sea una superficie más de ese
	// invariante y no una excepción.
	ContentAny ContentFilter = "any"
	// ContentNone son los eventos SIN contenido (sin fila en la vista): los que
	// solo `…/cancel` puede cerrar.
	ContentNone ContentFilter = "none"
	// ContentAlive son los eventos cuyo contenido sigue vivo (`state='alive'`).
	ContentAlive ContentFilter = "alive"
)

// IsContentFilter reporta si v es uno de los tres valores del filtro. Existe para
// que el transporte pueda rechazar un typo con un 400 en vez de tragárselo y
// devolver una lista que no es la que se pidió.
func IsContentFilter(v string) bool {
	switch ContentFilter(v) {
	case ContentAny, ContentNone, ContentAlive:
		return true
	}
	return false
}

// IsStatus reporta si v es uno de los tres estados del ciclo de vida. Misma razón
// que IsContentFilter: `status=abiertos` es un error del llamante, no «todos».
func IsStatus(v string) bool {
	switch Status(v) {
	case StatusOpen, StatusClosed, StatusCancelled:
		return true
	}
	return false
}

// ListFilter es el filtro del listado del dueño. Lo que NO está aquí es tan
// deliberado como lo que sí: no hay tenant (sale del token, INV-8) y no hay
// sesión, porque la pregunta del dueño es por su NEGOCIO y no por un teléfono.
type ListFilter struct {
	// Status es el estado exacto. Vacío ⇒ StatusOpen (REQ-28): lo que se limpia
	// es lo que sigue abierto, y una bandeja que arrancara con los cancelados
	// dentro daría trabajo terminado por trabajo pendiente.
	Status Status
	// Kind acota al tipo de evento (cart, survey, menu, media). Vacío ⇒ todos.
	Kind string
	// Kinds acota a un CONJUNTO de tipos: los que el tenant tiene habilitados por
	// su plan (decisión de Jhoan del 2026-08-09). Es distinto de Kind, que es lo
	// que el llamante pidió ver; este es lo que el llamante PUEDE ver, y los dos se
	// aplican a la vez — pedir `kind=cart` sin la feature del carrito devuelve una
	// lista vacía, no un 403: la ruta se abrió porque el tenant tiene ALGÚN tipo.
	//
	// nil ⇒ sin filtro. Lista VACÍA ⇒ ningún tipo pasa, que es lo correcto para un
	// tenant sin ninguna de las cuatro features (ver listArgs).
	Kinds []string
	// Content es la pregunta por la solicitud ligada. Vacío ⇒ ContentAny.
	Content ContentFilter
	// Stale filtra por la marca DERIVADA «vencido»: nil ⇒ no filtra, true ⇒ solo
	// los vencidos, false ⇒ solo los que no lo están.
	//
	// Es un puntero y no un bool porque «no me importa» y «los que no están
	// vencidos» son preguntas distintas, y con un bool a false serían la misma.
	// El filtro se aplica SOBRE LA COLUMNA CALCULADA, nunca comparando fechas en
	// el predicado (INV-19): ver listEventsStale.
	Stale *bool
	// ContactID acota a un contacto (identificador OPACO, ADR-0017). Vacío ⇒ todos.
	ContactID string
	// Page es 1-based; PageSize se acota a [1, MaxPageSize].
	Page     int
	PageSize int
}

// Normalized devuelve el filtro con la paginación saneada y los defaults puestos
// (estado `open`, contenido `any`). Es idempotente.
func (f ListFilter) Normalized() ListFilter {
	out := f
	if out.Page < 1 {
		out.Page = 1
	}
	switch {
	case out.PageSize <= 0:
		out.PageSize = DefaultPageSize
	case out.PageSize > MaxPageSize:
		out.PageSize = MaxPageSize
	}
	if out.Status == "" {
		out.Status = StatusOpen
	}
	if out.Content == "" {
		out.Content = ContentAny
	}
	return out
}

// Offset es el desplazamiento SQL que corresponde a la página del filtro.
func (f ListFilter) Offset() int { return (f.Page - 1) * f.PageSize }

// EventPage es una página del listado con el TOTAL de coincidencias del filtro (no
// de la página): sin él la consola no puede pintar el paginador ni decirle a
// Herminia cuánto le queda por limpiar.
type EventPage struct {
	Events   []Rescuable
	Page     int
	PageSize int
	Total    int
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

// collect recorre las filas con el escáner dado y CIERRA siempre, propagando el
// error de cierre solo si no había otro peor que contar.
func collect[T any](rows *sql.Rows, scan func(scanner) (T, error)) (out []T, err error) {
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("events: cerrar filas de eventos: %w", cerr)
		}
	}()

	for rows.Next() {
		v, sErr := scan(rows)
		if sErr != nil {
			return nil, fmt.Errorf("events: leer fila de evento: %w", sErr)
		}
		out = append(out, v)
	}
	if rErr := rows.Err(); rErr != nil {
		return nil, fmt.Errorf("events: recorrer eventos vivos: %w", rErr)
	}
	return out, nil
}

// transitionSQL es el compare-and-swap: el `AND status='open'` es el guard. Un
// evento terminal no tiene transición de vuelta, y quien pierde la carrera con
// otro escritor no pisa la muerte que el otro ya selló.
const transitionSQL = `
UPDATE public.conversation_events
   SET status = $2, closed_at = $3
 WHERE id = $1 AND status = 'open'`

// TransitionEvent mueve un evento VIVO a un estado terminal y sella closed_at con
// el reloj inyectado.
//
// Es un compare-and-swap contra la BD, no un «leer, decidir, escribir»: el UPDATE
// lleva `AND status='open'`, así que si el evento ya era closed o cancelled no
// toca nada y devuelve ErrNotOpen. De ahí salen las dos imposibilidades del
// diseño: closed→open y cancelled→closed.
//
// El destino debe ser terminal (ErrNotTerminal si no): open es el estado de
// nacimiento, y «reabrir» no es una transición sino un evento nuevo.
func (s *Store) TransitionEvent(ctx context.Context, eventID string, to Status) error {
	if to != StatusClosed && to != StatusCancelled {
		return fmt.Errorf("%w (recibido %q)", ErrNotTerminal, to)
	}

	res, err := s.db.ExecContext(ctx, transitionSQL, eventID, to, s.now().UTC())
	if err != nil {
		return fmt.Errorf("events: transitar evento a %q: %w", to, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("events: filas afectadas por la transición: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w (id=%s destino=%s)", ErrNotOpen, eventID, to)
	}
	return nil
}

// touchSQL refresca SOLO el reloj. Que status no aparezca en el SET no es un
// detalle de implementación: es la regla (E-6). Nada mata ni resucita un evento
// por actividad.
const touchSQL = `
UPDATE public.conversation_events
   SET last_activity_at = $2
 WHERE id = $1`

// Touch estampa last_activity_at con el reloj inyectado: es EL refresco del reloj
// de conversación (E-6), lo que hace que una conversación activa nunca venza.
//
// NO toca status —ni el vivo ni el terminal— y no depende de él: el llamador
// refresca al interactuar, y si el evento estaba suspendido deja de estarlo por
// haber vuelto a hablar, no por un cambio de estado que no existe.
func (s *Store) Touch(ctx context.Context, eventID string) error {
	res, err := s.db.ExecContext(ctx, touchSQL, eventID, s.now().UTC())
	if err != nil {
		return fmt.Errorf("events: refrescar el reloj del evento: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("events: filas afectadas por el refresco: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w (id=%s)", ErrEventMissing, eventID)
	}
	return nil
}

// IsSuspended reporta si el evento está suspendido AHORA, según el reloj inyectado
// del store. Es la función pura IsSuspended con el reloj ya puesto: no consulta ni
// escribe nada, y con ttl <= 0 devuelve siempre false.
func (s *Store) IsSuspended(e Event, ttl time.Duration) bool {
	return IsSuspended(e, ttl, s.now().UTC())
}

// appendEntrySQL numera la entrada leyendo el máximo actual DENTRO de la misma
// sentencia: entre el cálculo y la escritura no cabe una lectura ajena. Lo que sí cabe es otro INSERT concurrente
// que calcule el mismo número; ese pierde contra el UNIQUE (event_id, seq) y lo
// resuelve el reintento, no un candado que serializaría a todos.
//
// Numerar con MAX+1 —y no con una secuencia— es lo que da el «sin huecos» que pide
// el diseño: un INSERT que falla y revierte no consume número.
//
// 🔧 `origin` ENTRÓ EN LA SENTENCIA CON T4.6 (Plan 044 · Ola 4). Hasta entonces la
// columna existía —la creó la 0051 con su CHECK y su DEFAULT 'whatsapp'— y NINGÚN
// escritor la nombraba: todas las filas caían al default, que era correcto porque
// todas venían del canal. La segunda procedencia (`owner_pasted`, la transcripción
// que pega el dueño) obliga a decirlo, y se dice en el INSERT y no con un UPDATE
// posterior: una fila que nace `whatsapp` y se corrige después tiene un instante en
// el que miente sobre su propia procedencia.
const appendEntrySQL = `
INSERT INTO public.conversation_event_messages
       (event_id, seq, role, entry_kind, payload, body_enc, body_dek, body_kek_id, origin)
SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, $2, $3, $4::jsonb, $5, $6, $7, $8
  FROM public.conversation_event_messages WHERE event_id = $1::uuid
RETURNING seq`

// maxAppendAttempts acota los reintentos por colisión de numeración. Los
// escritores del historial de UN evento son pocos (los mensajes de una
// conversación llegan de uno en uno): 5 intentos sobran, y agotarlos devuelve el
// error en vez de girar indefinidamente.
const maxAppendAttempts = 5

// entry es una entrada del historial ya resuelta a columnas. El grado del ADR-0034
// se decide AQUÍ, en el store, y no lo elige el llamador: o trae payload en claro
// (decision/summary) o trae cuerpo cifrado (message), nunca las dos cosas — que es
// justo lo que exige conversation_event_messages_grade_chk.
type entry struct {
	role      Role
	kind      entryKind
	payload   []byte
	bodyEnc   []byte
	bodyDEK   []byte
	bodyKEKID any
	// origin es POR DÓNDE entró la fila. Cero-valor ⇒ OriginWhatsApp: lo resuelve
	// appendEntry, no cada llamante, para que añadir un método de escritura no
	// pueda dejar la columna a merced de un olvido (el mismo criterio con el que
	// `kind` y `role` los clava cada puerta y no el llamador).
	origin Origin
}

// AppendSummary añade el RESUMEN determinista que emitimos nosotros al dejar de
// ser activo un evento (ADR-0029 E-4), con role='system' fijo.
//
// El cuerpo es json.RawMessage y no string A PROPÓSITO. Un resumen es nivel 1 del
// ADR-0034: estructura EN CLARO en payload, porque es negocio cuantificable y
// cifrarlo destruiría su valor sin proteger a nadie. Pedir estructura ya
// serializada es lo que impide que por esta puerta entre prosa: el texto libre no
// tiene sitio aquí, tiene AppendMessage, y allí se cifra siempre.
//
// El rol fijo no es comodidad: toda fila emitida por nosotros va marcada como
// nuestra, para que quien analice el hilo después no cuente dos veces la decisión
// que ya está en la tabla y ADEMÁS aparece dentro del resumen (INV-11).
//
// Devuelve el seq asignado.
func (s *Store) AppendSummary(ctx context.Context, eventID string, body json.RawMessage) (int, error) {
	if !json.Valid(body) {
		return 0, ErrSummaryNotJSON
	}
	return s.appendEntry(ctx, eventID, entry{
		role:    RoleSystem,
		kind:    entryKindSummary,
		payload: body,
	})
}

// AppendDecision añade una DECISIÓN estructurada del cliente al hilo (D-043.23,
// D-043.13): la línea agregada o quitada del carrito, la respuesta de encuesta —
// el efecto ya decidido, no la charla que llevó a él.
//
// Es nivel 1 del ADR-0034: ESTRUCTURA en claro en payload, con role='client'
// FIJO, porque la decisión es del cliente — la voz de la entrada es suya aunque
// quien la serialice seamos nosotros. Igual que en AppendSummary, el payload se
// valida como JSON (ErrSummaryNotJSON si no lo es): la prosa no tiene por dónde
// entrar en el nivel que no cifra.
//
// INV-11 sigue intacto: nada NUESTRO se escribe como `decision`. Lo que emite la
// plataforma (los resúmenes de abandono) entra por AppendSummary con
// entry_kind='summary' y role='system'; esta puerta registra lo que el cliente
// decidió, y por eso no hay doble contabilidad entre el resumen y sus decisiones.
func (s *Store) AppendDecision(ctx context.Context, eventID string, payload []byte) error {
	if !json.Valid(payload) {
		return ErrSummaryNotJSON
	}
	_, err := s.appendEntry(ctx, eventID, entry{
		role:    RoleClient,
		kind:    entryKindDecision,
		payload: payload,
	})
	return err
}

// AppendMessage añade el TEXTO LITERAL de una interacción, SIEMPRE CIFRADO
// (envelope AES-256-GCM con DEK fresca por valor envuelta por la KEK; el mismo
// FieldCipher que usa contacts).
//
// Es la única puerta de texto libre del store y no tiene variante en claro: no
// existe parámetro, bandera ni camino alternativo que persista body sin cifrar. La
// fila queda con payload NULL, como exige el CHECK de grado — el literal jamás se
// cuela en el nivel 1.
//
// Devuelve el seq asignado.
func (s *Store) AppendMessage(ctx context.Context, eventID string, role Role, body string) (int, error) {
	if role != RoleClient && role != RoleBusiness && role != RoleSystem {
		return 0, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}
	if s.cipher == nil {
		return 0, ErrNoCipher
	}

	bodyEnc, bodyDEK, kekID, err := s.cipher.Encrypt(body)
	if err != nil {
		return 0, fmt.Errorf("events: cifrar el cuerpo de la entrada: %w", err)
	}

	return s.appendEntry(ctx, eventID, entry{
		role:      role,
		kind:      entryKindMessage,
		bodyEnc:   bodyEnc,
		bodyDEK:   bodyDEK,
		bodyKEKID: kekID,
	})
}

// AppendOutOfTurnMessage añade al hilo un SALIENTE FUERA DE TURNO: el texto que la
// plataforma le manda al cliente sin que nazca de un entrante suyo (Plan 044 ·
// T1.6, D-044.24) — el resumen del rescate, la confirmación de un `event_stop`, el
// recordatorio de la seña.
//
// Es AppendMessage con OTRO grado, y nada más: mismo cifrado, mismo sobre de tres
// piezas, mismo nivel 2 del ADR-0034 y misma numeración sin huecos. La única
// diferencia es `entry_kind='message_out_of_turn'`, que es lo que permite a quien
// lea el hilo (T1.4) meter el texto como CONTEXTO y no como pedido del cliente. El
// porqué de esa forma —y por qué no un `role` nuevo ni un flag en `payload`— está
// en la constante entryKindMessageOutOfTurn (events.go).
//
// 🔴 EL ROL ES FIJO Y ES `business`, y no es comodidad: un saliente es, por
// definición, la voz del negocio. Dejar que el llamante lo eligiera abriría la
// puerta a registrar texto del CLIENTE como contexto fuera de turno, que es
// exactamente la doble contabilidad que INV-11 existe para impedir — el mismo
// criterio con el que AppendSummary clava `system` y AppendDecision clava `client`.
//
// Devuelve el seq asignado.
func (s *Store) AppendOutOfTurnMessage(ctx context.Context, eventID string, body string) (int, error) {
	if s.cipher == nil {
		return 0, ErrNoCipher
	}

	bodyEnc, bodyDEK, kekID, err := s.cipher.Encrypt(body)
	if err != nil {
		return 0, fmt.Errorf("events: cifrar el cuerpo del saliente fuera de turno: %w", err)
	}

	return s.appendEntry(ctx, eventID, entry{
		role:      RoleBusiness,
		kind:      entryKindMessageOutOfTurn,
		bodyEnc:   bodyEnc,
		bodyDEK:   bodyDEK,
		bodyKEKID: kekID,
	})
}

// AppendPastedMessage añade al hilo la TRANSCRIPCIÓN EXTERNA que el dueño pegó en
// el cuerpo de `POST /api/v1/intakes/{id}/reanalyze` (Plan 044 · Ola 4 · T4.6;
// REQ-32 / D-044.17, cierre de MD-044.2).
//
// Es AppendMessage con OTRO origen y NADA más: mismo `entry_kind='message'`, mismo
// cifrado, mismo sobre de tres piezas, mismo nivel 2 del ADR-0034 y misma numeración
// sin huecos. La única diferencia viaja en `origin`, que es la columna que responde
// «por dónde entró» y que el LLM no ve.
//
// 🔴 EL ROL ES FIJO Y ES `client`, Y ESO ES LA DECISIÓN, NO UN DESCUIDO (D-044.17).
// Lo que el dueño pega es lo que DIJO EL CLIENTE por otro canal —un audio que
// transcribió, un mensaje que le llegó por Instagram—, así que su voz es la del
// cliente. Marcarlo `business` lo convertiría en CONTEXTO para el compositor
// (speakerOf) y el pipeline dejaría de extraer de ahí ni un ítem, que es
// exactamente lo contrario de para qué se pega. El rastro de que lo escribió el
// dueño no se pierde: vive en `origin`, que es la columna de esa pregunta.
//
// 🔴 EL SANEO NO ESTÁ AQUÍ, Y TAMPOCO ES UN OLVIDO. `cart.SanitizeNote` es la
// puerta de contenido del texto libre del dueño y la aplica quien recibe la
// petición (`internal/reanalisis`), antes de decidir si la fila es un duplicado —
// porque el dedupe se hace por el hash del texto YA SANEADO. Sanear otra vez aquí
// sería una segunda regla en un segundo sitio, y este paquete no importa `cart`.
//
// Devuelve el seq asignado.
func (s *Store) AppendPastedMessage(ctx context.Context, eventID string, body string) (int, error) {
	if s.cipher == nil {
		return 0, ErrNoCipher
	}

	bodyEnc, bodyDEK, kekID, err := s.cipher.Encrypt(body)
	if err != nil {
		// El error NO cita el cuerpo: es texto del cliente (ADR-0034).
		return 0, fmt.Errorf("events: cifrar la transcripción pegada por el dueño: %w", err)
	}

	return s.appendEntry(ctx, eventID, pastedEntry(bodyEnc, bodyDEK, kekID))
}

// pastedEntry arma la fila del texto pegado. Existe como función aparte —y no
// inline en AppendPastedMessage— para que las CUATRO decisiones de forma (rol
// `client`, grado `message`, origen `owner_pasted`, payload NULL) se puedan afirmar
// en un test sin Postgres. Sin ella, lo único que las cubriría serían los
// `TestPostgres_*`, que se SALTAN sin `DATABASE_URL`.
func pastedEntry(bodyEnc, bodyDEK []byte, kekID string) entry {
	return entry{
		role:      RoleClient,
		kind:      entryKindMessage,
		bodyEnc:   bodyEnc,
		bodyDEK:   bodyDEK,
		bodyKEKID: kekID,
		origin:    OriginOwnerPasted,
	}
}

// appendEntry escribe una entrada del historial numerándola sin huecos,
// reintentando si otro escritor se llevó ese seq.
func (s *Store) appendEntry(ctx context.Context, eventID string, e entry) (int, error) {
	var lastErr error
	for attempt := 0; attempt < maxAppendAttempts; attempt++ {
		var seq int
		origin := e.origin
		if origin == "" {
			origin = OriginWhatsApp
		}
		err := s.db.QueryRowContext(ctx, appendEntrySQL, eventID, e.role, e.kind,
			nullableJSON(e.payload), nullableBytes(e.bodyEnc), nullableBytes(e.bodyDEK),
			e.bodyKEKID, string(origin)).Scan(&seq)
		switch {
		case err == nil:
			return seq, nil
		case postgres.IsUniqueViolation(err):
			// Otro escritor se llevó ese seq: reintentar relee un máximo ya mayor.
			lastErr = err
		default:
			return 0, fmt.Errorf("events: insertar entrada %q del historial: %w", e.kind, err)
		}
	}
	return 0, fmt.Errorf("events: numerar la entrada del historial tras %d intentos: %w", maxAppendAttempts, lastErr)
}

// nullableJSON manda NULL en vez de una cadena vacía (que no es JSON válido para
// el cast a jsonb) y, si hay estructura, la pasa como texto para que Postgres la
// convierta con el `::jsonb` de la sentencia.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// nullableBytes manda NULL en vez de un blob vacío: el CHECK de grado razona sobre
// IS NULL, y un []byte de longitud cero no es NULL para Postgres.
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
