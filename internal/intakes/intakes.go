// Package intakes es el dominio de las SOLICITUDES del tenant (ADR-0031, Plan 041
// D-12): la cabecera que el módulo conversacional `cart` proyecta al cerrar, las
// líneas que la componen y la máquina de estados que gobierna su ciclo de vida
// (status.go, D-041.10).
//
// El objeto se llama SOLICITUD porque pedido y presupuesto son EL MISMO OBJETO; el
// mecanismo que la arma se sigue llamando `cart`. Este paquete NO toca el módulo
// cart: lo LEE. En particular, el cart sigue cerrando en `closed` (Plan 016) y aquí
// esa clave se lee NORMALIZADA a `confirmed` en un único punto (NormalizeStatus) —
// cero migración de datos, cero regresión en el motor.
//
// CERO PII (INV-04 / ADR-0010): ContactID es la identidad OPACA del contacto tal
// como está en BD. Este paquete NUNCA la descifra ni la enriquece: la mueve tal
// cual del store al wire.
//
// Con UNA excepción, y conviene que esté escrita aquí y no escondida: el
// notificador de cambio de estado (notifier.go, D-041.14) tiene que mandarle un
// WhatsApp a una persona, y para eso hace falta su número. No lo descifra él —se lo
// pide a la vía custodiada de PII (contact.Resolver, ADR-0017), que es la misma que
// usa el motor de flujos—, lo pasa al Sender y lo suelta: no lo loguea, no lo
// persiste y no lo devuelve a ningún llamante. Lo que sigue siendo cierto sin
// matices es que ninguna LECTURA de este paquete (lista, detalle, export, summary)
// enriquece jamás el contacto: ahí el opaco viaja opaco.
//
// Aislamiento por tenant (INV-8): el tenant llega SIEMPRE como argumento —lo saca
// del token quien llama, jamás de un parámetro de la petición— y toda consulta lo
// lleva en el WHERE. Una solicitud de otro tenant es indistinguible de una
// inexistente: ErrNotFound (⇒ 404), nunca un 403 que confirmaría que existe.
package intakes

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound lo devuelven Get y SetStatus cuando no hay ninguna solicitud del
// tenant con ese id: no existe, o existe pero es de otro tenant (404 opaco, INV-8).
var ErrNotFound = errors.New("solicitud no encontrada")

// ErrConflict lo devuelve SetStatus cuando la solicitud cambió de estado entre la
// lectura que validó la transición y la escritura que iba a aplicarla (dos
// operadores a la vez). La transición NO se aplica: el llamante relee y decide.
var ErrConflict = errors.New("la solicitud cambió de estado durante la transición")

// Paginación del listado (design §4): tamaño por defecto y cota superior. La cota
// no es cosmética — sin ella una página de 100k filas es un DoS de un solo GET.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// MaxExportIntakes acota cuántas solicitudes puede abarcar UN export o UN summary.
// El export no pagina —una hoja de cálculo partida en páginas no sirve para nada— y
// esa es exactamente la razón por la que necesita su propia cota: sin ella, un GET
// sin filtros materializa la bandeja entera con sus líneas en memoria.
//
// Al superarla NO se recorta en silencio (ErrTooLarge): un export truncado es peor
// que uno rechazado, porque quien lo abre cree que tiene todos sus pedidos.
const MaxExportIntakes = 5000

// ErrTooLarge lo devuelven ListDetails y Summary cuando el filtro abarca más de
// MaxExportIntakes solicitudes. Se resuelve acotando el rango (from/to), no
// reintentando.
var ErrTooLarge = errors.New("el filtro abarca demasiadas solicitudes para un solo export")

// Intake es la CABECERA de una solicitud tal como se lee. Status viene siempre
// NORMALIZADO (el `closed` que escribe el cart se lee `confirmed`); TenantID no
// viaja porque quien lee ya es el dueño del tenant (INV-8).
type Intake struct {
	ID        string
	ContactID string // OPACO (ADR-0010); NUNCA número/JID en claro
	SessionID string
	Status    string // clave canónica de D-041.10 (ya normalizada)
	Total     float64
	CreatedAt time.Time
	UpdatedAt time.Time
	// CustomerNote es la indicación del cliente para TODO el pedido (D-041.19):
	// «dejarlo en portería». Es la HERMANA de Item.Customization y son dos campos
	// distintos a propósito: la de la línea es RECETA y solo significa algo pegada a
	// su línea; esta es de ENTREGA y no es de ningún artículo en particular.
	//
	// Como aquélla, jamás entra en el cálculo del Total (INV-13) y viaja EN CLARO —
	// pero por COSTE, no por doctrina (D-041.23): es donde de verdad se cuela la
	// PII, y lo que la contiene hoy es el saneo de la puerta (cart.SanitizeNote), el
	// límite de 280 runas y la poda por retención del Plan 046. Quien añada un
	// consumidor nuevo de este campo hereda ese contexto.
	//
	// Cadena vacía = el cliente no indicó nada (es el DEFAULT de la columna).
	CustomerNote string
	// DepositDueAt es la fecha límite de la SEÑA (D-041.12). La fija la entrada en
	// `deposit_requested` —dentro de la MISMA escritura del estado— como
	// `now + tenant_settings.deposit_due_days`.
	//
	// NO es un TTL y esa distinción es la tarea entera: pasarse de esta fecha NO
	// mata la solicitud (D-041.16, nada vence por tiempo); lo único que habilita es
	// el RECORDATORIO. Zero = no se ha pedido seña (NULL en la columna).
	DepositDueAt time.Time
	// DepositRemindedAt marca que al cliente YA se le recordó la seña. Es lo que
	// hace que el recordatorio sea uno y no un goteo: se escribe con un
	// compare-and-swap contra NULL, así que de N toques simultáneos solo uno manda
	// (D-041.12: «un solo recordatorio en v1»). Zero = nunca se le recordó.
	DepositRemindedAt time.Time
}

// Item es una LÍNEA de la solicitud. SKU/Label son códigos del catálogo del
// tenant (dato de negocio), NO PII.
//
// Customization es la personalización NO FACTURABLE de esa línea —«sin sal», «sin
// cebolla»— (D-041.17). Dos cosas que no son de estilo:
//
//   - NUNCA entra en ningún cálculo de dinero (INV-13): ni en UnitPrice, ni en el
//     line_total que derivan el export y el ranking, ni en el Total de la
//     cabecera. El perro caliente no es más barato por quitarle la cebolla.
//   - Un añadido FACTURABLE no se guarda aquí: si el catálogo tiene un ítem para
//     eso, es su propia línea con su SKU y su precio. La regla es una — ¿hay ítem
//     para eso? sí ⇒ línea; no ⇒ Customization.
//
// Cadena vacía = sin personalización (es el DEFAULT de la columna, no un "no sé").
type Item struct {
	SKU           string
	Label         string
	Customization string
	Qty           int
	UnitPrice     float64
	AddedAt       time.Time
}

// Detail es la solicitud completa: cabecera + líneas + revisiones.
//
// Items es la verdad de lo VENDIDO; Revisions es el rastro de cómo se llegó a ello
// (ADR-0031 §3). Son cosas distintas y por eso viajan por separado: corregir el
// borrador no reescribe lo vendido, y ver lo vendido no obliga a leer la
// negociación entera.
//
// Revisions solo lo puebla Get. El export (ListDetails) NO las trae: una hoja de
// cálculo de la bandeja del día no es el sitio donde se audita una negociación, y
// arrastrarlas multiplicaría por N el volumen de un camino que ya tiene su cota en
// MaxExportIntakes.
type Detail struct {
	Intake
	Items     []Item
	Revisions []Revision
	// BuyerDataPresent dice si la solicitud tiene datos del comprador guardados
	// (D-041.13, T4.5). Un BOOLEANO y nada más: es TODO lo que este dominio publica
	// del checklist del comprador.
	//
	// No es una simplificación provisional. Los valores están cifrados en
	// public.intake_buyer_data y su descifrado está CUSTODIADO: llega con su
	// consumidor real (el puente del Plan 042 o una pantalla), no por si acaso. Este
	// booleano es lo que permite a una consola decir «este pedido trae los datos que
	// pediste» sin que el dato salga de la fila.
	//
	// Lo puebla Get. ListDetails (export y summary) lo deja en false a propósito y
	// eso NO es un descuido: ni el CSV ni el summary.json publican nada del
	// comprador —ni siquiera su existencia— y calcularlo ahí solo serviría para que
	// alguien lo colara en una columna.
	BuyerDataPresent bool
}

// Filter acota el listado de solicitudes. Todos los campos son opcionales: el
// cero-valor lista todo el tenant.
//
// Extensible sin rediseño: el filtro `orphan=true` del design §4 es una condición
// DERIVADA (se recalcula al leer, D-041.16) y la trae T4.8 (Ola 4). Cuando llegue,
// entra como un campo más de este struct y una cláusula más en el store — ni la
// firma de Service.List ni la del puerto Store cambian.
type Filter struct {
	// From/To acotan por created_at. From es INCLUSIVO y To EXCLUSIVO: quien
	// traduce una fecha del usuario a este rango decide qué significa "hasta"
	// (el handler suma un día a una fecha suelta para que el día pedido entre
	// entero). Zero-valor ⇒ sin cota por ese lado.
	From time.Time
	To   time.Time
	// Status es la clave CANÓNICA por la que se filtra (ya normalizada). Vacío ⇒
	// todos. Filtrar por `confirmed` alcanza también las filas históricas escritas
	// como `closed`: de eso se encarga StoredVariants en el store.
	Status string
	// SessionID acota a la sesión (Edge/WhatsApp) que originó la solicitud.
	SessionID string
	// Page es 1-based; PageSize se acota a [1, MaxPageSize].
	Page     int
	PageSize int
}

// Normalized devuelve el filtro con la paginación saneada (página ≥ 1, tamaño en
// [1, MaxPageSize] con DefaultPageSize si no se pidió) y el estado normalizado.
// Es idempotente.
func (f Filter) Normalized() Filter {
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
	out.Status = NormalizeStatus(out.Status)
	return out
}

// Offset es el desplazamiento SQL que corresponde a la página del filtro.
func (f Filter) Offset() int {
	return (f.Page - 1) * f.PageSize
}

// Page es una página del listado con el total de coincidencias del filtro (no de
// la página): sin él la UI no puede pintar el paginador.
type Page struct {
	Intakes  []Intake
	Page     int
	PageSize int
	Total    int
}

// Store es el puerto de persistencia de solicitudes. Lo satisface *Postgres
// (producción) y *MemoryStore (tests). Toda operación va acotada al tenant (INV-8).
type Store interface {
	// List devuelve la página de solicitudes que casan con el filtro (más
	// recientes primero) y el TOTAL de coincidencias del filtro sin paginar.
	List(ctx context.Context, tenantID string, f Filter) (items []Intake, total int, err error)

	// Get devuelve la solicitud con sus líneas y sus revisiones, o ErrNotFound si
	// no existe en ESE tenant (404 opaco: no se distingue de "es de otro tenant").
	Get(ctx context.Context, tenantID, intakeID string) (Detail, error)

	// ListDetails devuelve las solicitudes que casan con el filtro CON sus líneas,
	// en el mismo orden que List (más recientes primero) y hasta `limit`
	// cabeceras. Es lo que consumen el export y el summary: el MISMO predicado que
	// la lista —si divergiera, el CSV no contendría lo que la bandeja muestra— pero
	// sin paginar y sin una consulta por solicitud.
	//
	// Page/PageSize del filtro se IGNORAN aquí: la cota es `limit`.
	ListDetails(ctx context.Context, tenantID string, f Filter, limit int) ([]Detail, error)

	// UpdateStatus aplica la transición de forma ATÓMICA (compare-and-swap): solo
	// escribe `to` si el estado almacenado sigue siendo uno de `expected` (las
	// variantes en BD del estado que se validó, StoredVariants). Devuelve
	// ErrNotFound si la solicitud no es del tenant y ErrConflict si existe pero su
	// estado ya no es el esperado.
	//
	// Cuando el destino es `pending_approval` garantiza ADEMÁS la línea de envío
	// (D-041.11) en la MISMA unidad de trabajo, y la cabecera que devuelve ya trae
	// el total con ella. No es un efecto colateral escondido: «toda solicitud en
	// pending_approval lleva su línea de envío» es un INVARIANTE del estado, y el
	// único sitio donde se puede sostener sin ventana es donde se escribe el
	// estado. Hacerlo en dos pasos dejaría presupuestos sin envío cada vez que el
	// segundo fallara, y el reintento del operador chocaría con un 422 por estar ya
	// en pending_approval.
	UpdateStatus(ctx context.Context, tenantID, intakeID, to string, expected []string) (Intake, error)

	// EnsureShippingLine garantiza que la solicitud tenga EXACTAMENTE UNA línea de
	// envío (sku ShippingSKU, D-041.11) y deja el total de la cabecera coherente
	// con sus líneas. Es IDEMPOTENTE: aplicarla N veces deja una sola línea.
	//
	// `policy` decide si la línea se materializa siempre o solo cuando el tenant
	// tiene zonas configuradas (ver ShippingPolicy). Devuelve ErrNotFound si la
	// solicitud no es del tenant.
	EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy ShippingPolicy) error

	// ReplaceItems sustituye las líneas de CLIENTE de la solicitud por `items`,
	// PRESERVA las del sistema (la línea de envío, ReservedSKUPrefix), deja el
	// total cuadrado con lo que quedó y escribe la revisión `corrected` del dueño
	// — todo en la MISMA unidad de trabajo (T4.10, REQ-36).
	//
	// La revisión NO es un efecto colateral que se dispare después, y por eso
	// entra en este puerto en vez de dejarse al llamante: una corrección aplicada
	// sin su rastro es exactamente el agujero de auditoría que D-041.26 pide
	// cerrar, y en dos pasos ese agujero se abre cada vez que falle el segundo.
	// El llamante no elige el `kind` ni el autor: esta operación es LA corrección
	// manual del dueño, no una escritura genérica de revisiones (para eso está
	// RevisionWriter).
	//
	// `expected` son las variantes en BD del estado que el dominio validó
	// (StoredVariants): la escritura solo ocurre si la solicitud sigue ahí
	// (ErrConflict si no, ErrNotFound si no es del tenant). Devuelve el detalle ya
	// coherente, revisión nueva incluida.
	ReplaceItems(ctx context.Context, tenantID, intakeID string, items []Item, expected []string) (Detail, error)

	// ApplyRevalidation escribe el resultado de una revalidación contra el catálogo
	// vigente (D-041.25, T4.9) en UNA sola unidad de trabajo: re-precia las líneas
	// que cambiaron, borra las que se retiraron, cuadra el total de la cabecera y
	// escribe la revisión `revalidated` con el texto exacto que se le mandó al
	// cliente.
	//
	// El diff llega YA CALCULADO (Revalidate, que es pura): este puerto no consulta
	// el catálogo ni decide qué se retira. Lo que sí es contrato suyo es que la
	// escritura sea SURGICAL —UPDATE de las líneas repreciadas y DELETE de las
	// retiradas, jamás un borrado y alta del conjunto—, porque el orden de las
	// líneas del pedido es su `added_at` y reescribirlas todas le cambiaría al
	// cliente el orden de su propio pedido sin que nadie lo tocara.
	//
	// Las líneas de LA PLATAFORMA (ReservedSKUPrefix: hoy el envío) no se tocan ni
	// para bien: ni se re-precian, ni se borran, ni se reescriben con su mismo
	// valor. El diff ya las excluye; esto es la segunda cerradura.
	//
	// El `status` NO se toca: aunque se retiren todas las líneas, la solicitud se
	// queda donde estaba (E-7). `expected` son las variantes en BD del estado que el
	// dominio validó: ErrConflict si ya no está ahí, ErrNotFound si no es del tenant.
	ApplyRevalidation(ctx context.Context, tenantID, intakeID string, rv Revalidation, renderedText string, expected []string) (Detail, error)

	// Discard aplica el DESCARTE MANUAL del dueño sobre UNA solicitud (D-041.18,
	// T4.8) en una sola unidad de trabajo: la deja en `abandoned`, escribe su
	// revisión `discarded` y CIERRA SU CONTENEDOR — el evento conversacional que la
	// propia solicitud declara pasa de `open` a `cancelled` con `closed_at`
	// (REQ-32e; D-043.15(1) re-expresada por D-043.21). ErrNotFound si no es del
	// tenant (404 opaco).
	//
	// La CONDICIÓN de escritura es toda del llamante y va en `discardable` (las
	// claves almacenadas desde las que se puede descartar, DiscardableStatuses):
	// esta operación no consulta la máquina de estados ni decide qué es descartable.
	// Lo que sí es contrato de este puerto es el ORDEN en que se rechaza —primero el
	// estado, después el evento vivo— y que un rechazo NO escribe NADA.
	//
	// Las comprobaciones y las escrituras ocurren bajo el MISMO candado de la
	// cabecera: sin eso, entre "está descartable y sin evento vivo" y el UPDATE
	// cabría un rescate, y el descarte mataría un pedido que ya había vuelto a la
	// vida.
	//
	// "Evento vivo" (DiscardOutcome.LiveEvent) es desde la Ola 4.5 el criterio
	// REAL (DT-043.2 SALDADA): ¿está `open` el evento que ESTA solicitud declara en
	// su `intakes.event_id` (D-043.21)? La aproximación por el `cart` del
	// flow_state que vivió aquí desde el Plan 041 murió con la columna
	// conversation_events.intake_id (0054). Una solicitud LEGADA sin event_id no
	// tiene evento vivo que mirar ⇒ descartable.
	Discard(ctx context.Context, tenantID, intakeID string, discardable []string) (DiscardOutcome, error)

	// AbandonByEvent deja en `abandoned` la solicitud `open` que declara `eventID`
	// como padre (D-043.21, T4.5.5(a)): la dirección contenedor→contenido de E-8,
	// consumida por el runtime cuando un evento muere cancelado (puerto
	// IntakeAbandoner). Es un CAS de una sentencia (`WHERE event_id=… AND
	// tenant_id=… AND status='open'`) y 0 filas es ÉXITO IDEMPOTENTE: el reintento
	// de una cancelación a medias tiene que poder terminar, y un evento sin
	// contenido (menu, survey) no tiene nada que abandonar. A diferencia de la
	// vieja puerta por intakeID (Service.Abandon → SetStatus), aquí no hay
	// TransitionError posible: el guard del estado va en el propio SQL.
	AbandonByEvent(ctx context.Context, tenantID, eventID string) error
}
