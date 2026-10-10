// Porta internal/flujos/events/store.go @ 9d5a4b6 (trozo: el filtro y la página del listado del
// dueño; ver el reparto en store.go).
//
// Es puro —ni base, ni reloj— y nace VERDE con su test: lo usan el adaptador Postgres
// (store_list.go) y el doble en memoria (eventshelpertest), que tienen que normalizar igual.

package events

// Paginación del listado (REQ-28): tamaño por defecto y cota superior. La cota no
// es cosmética — sin ella un GET sin filtros materializa todos los eventos vivos
// del tenant.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ContentFilter es la pregunta por el CONTENIDO del evento, y son exactamente
// tres respuestas porque la mitad `none` es la que hace falta que exista: el
// conjunto de eventos que no produjeron contenido (REQ-28), el que dejaba a
// Herminia sin dónde limpiar. El vocabulario público es `any|none|alive` y no
// cambia con la vista: es el contrato del transporte.
type ContentFilter string

const (
	// ContentAny no distingue entre las dos mitades: enseña las dos, y es el default.
	//
	// «Any» es cualquiera de los dos CONTENIDOS del predicado de rescatables, NO
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
	// tenant sin ninguna de las cuatro features (ver ListEvents).
	Kinds []string
	// Content es la pregunta por la solicitud ligada. Vacío ⇒ ContentAny.
	Content ContentFilter
	// Stale filtra por la marca DERIVADA «vencido»: nil ⇒ no filtra, true ⇒ solo
	// los vencidos, false ⇒ solo los que no lo están.
	//
	// Es un puntero y no un bool porque «no me importa» y «los que no están
	// vencidos» son preguntas distintas, y con un bool a false serían la misma.
	// El filtro se aplica SOBRE LA COLUMNA CALCULADA, nunca comparando fechas en
	// el predicado (INV-19): ver ListEvents.
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
