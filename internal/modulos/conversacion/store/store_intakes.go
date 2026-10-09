// Porta internal/flujos/store/store.go @ c0c0c03
//
// Trozo de store.go (05 E-13): las solicitudes del carrito (public.intakes y
// public.intake_items) vistas desde el motor de flujos. Solo declaraciones movidas.

package store

import (
	"context"
	"time"
)

// IntakeReader lee la solicitud abierta del carrito (reanudación/TTL en el runtime).
type IntakeReader interface {
	// GetOpenIntake devuelve la solicitud "open" del contacto para (tenantID, contactID),
	// si existe (found=false sin error si no hay). Identidad de negocio: UNA solicitud
	// "open" por (tenant_id, contact_id) (design.md §3.4). La usa el runtime al
	// reanudar y para evaluar el TTL (design.md §4.3).
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (intake Intake, found bool, err error)
	// GetIntakeByEvent devuelve la solicitud que declara `eventID` como padre
	// (D-043.21), si existe (found=false sin error si no hay). Es la lectura que le
	// faltaba al par de E-8: `AbandonByEvent` ya ESCRIBÍA por evento y nadie sabía
	// PREGUNTAR por él.
	//
	// 🔴 NO FILTRA POR ESTADO, Y ESO ES EL CONTRATO ENTERO (D-044.46, hallazgo #24).
	// GetOpenIntake resuelve por identidad de NEGOCIO (tenant, contacto) y solo ve
	// las `open`; desde el Plan 044 hay DOS productores de contenido durable sobre el
	// mismo evento —el proyector del carrito y la etapa `draft` del pipeline— y el
	// intake que uno dejó en `pending_approval` le resultaba INVISIBLE al otro, que
	// creaba el suyo y chocaba contra `intakes_event_id_uidx` (medido en UAT las dos
	// direcciones). Filtrar aquí por `open` reproduciría exactamente esa ceguera.
	//
	// Por E-8 (único parcial de la 0054) hay A LO SUMO una fila por evento; el
	// desempate por `created_at, id` existe para que el doble en memoria —que NO
	// impone el índice— conteste algo determinista y sea SIEMPRE la que ya estaba.
	GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (intake Intake, found bool, err error)
	// ListIntakeItems devuelve las líneas de una solicitud EN EL ORDEN EN QUE LAS VE
	// EL CLIENTE (added_at, id: el orden en que las armó el carrito). Sin líneas
	// devuelve la lista vacía SIN error; un intakeID que no sea un UUID es un ERROR
	// —no una solicitud sin líneas—, para que el hueco típico («no comprobé el found
	// de GetOpenIntake y pasé la cadena vacía») se vea en vez de disfrazarse de
	// pedido vacío.
	//
	// Es la lectura que faltaba para el RESUMEN del rescate (Plan 043 · Ola 3): desde
	// esta ola las líneas de una solicitud "open" están al día en intake_items, pero
	// ningún puerto sabía leerlas.
	//
	// NO ACOTA POR TENANT, y por eso el intakeID tiene que venir de una lectura que
	// SÍ lo haga (GetOpenIntake). Misma frontera que intakes.itemsOf, que lee las
	// líneas después de que su llamante haya resuelto y bloqueado la cabecera.
	//
	// ⚠️ Hereda la asimetría de quien resuelve ese id: GetOpenIntake busca por
	// (tenant, contacto) SIN sesión y se queda con la más reciente
	// (repository_postgres.go, ORDER BY created_at DESC LIMIT 1), mientras que un
	// evento conversacional es por (tenant, SESIÓN, contacto). Dos sesiones del mismo
	// tenant hablando con el mismo contacto pueden acabar leyendo las líneas del
	// pedido de la otra. Se documenta y NO se corrige aquí: el filtro vive en la
	// costura del evento, no en esta lectura.
	ListIntakeItems(ctx context.Context, intakeID string) ([]IntakeItem, error)
}

// IntakeWriter escribe/transiciona solicitudes del carrito (proyección del PersistSink).
type IntakeWriter interface {
	// UpsertIntake inserta o actualiza (upsert por ID) una solicitud del carrito en
	// public.intakes (Plan 016 · T0/T2, ADR-0009). Idempotente por o.ID: crea el
	// "open" una vez (primer item_added) y no lo duplica si se reprocesa el mismo
	// entrante. Las transiciones de estado posteriores van por MarkIntakeStatus.
	// CERO PII: o.ContactID es la identidad OPACA (ADR-0010).
	UpsertIntake(ctx context.Context, o Intake) error
	// ReplaceIntakeItems deja las líneas de CLIENTE de la solicitud EXACTAMENTE en
	// `items`: retira las que hubiera y escribe éstas, en un solo acto. added_at usa
	// el DEFAULT now() de la tabla. sku/label son códigos de negocio, NO PII.
	//
	// Es un REEMPLAZO y no un INSERT, y ahí está la tarea entera (Plan 043 · Ola 3):
	// desde que el carrito proyecta sus líneas en cada item_added —para que una
	// solicitud "open" tenga las suyas al día y no solo al cerrar—, la MISMA solicitud
	// recibe varias escrituras del mismo conjunto. Con un INSERT, cada una las
	// duplicaría; con un reemplazo, escribir dos veces lo mismo deja lo mismo. Por eso
	// aquí ya no hay ningún escritor que añada líneas sin quitar las anteriores: la
	// puerta que duplicaba no existe.
	//
	// len(items)==0 BORRA las líneas de cliente: es la foto de un carrito vacío, no un
	// no-op. Quien no tenga foto que escribir no debe llamar (ver cart.Projector).
	//
	// Las líneas de LA PLATAFORMA (prefijo reservado: hoy la de envío, D-041.11)
	// sobreviven intactas, igual que en intakes.replaceClientItemsTx: el carrito
	// escribe lo que armó el cliente y no puede tirar lo que puso wApp encima.
	ReplaceIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error
	// MarkIntakeStatus transiciona el estado de una solicitud (por ID) y fija su total,
	// actualizando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled"
	// | "expired". Reprocesar el mismo entrante no cambia la semántica (idempotente
	// por el last_wa_message_id del runtime).
	MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error
	// CloseIntake cierra ATÓMICAMENTE (una sola transacción) la solicitud "open" del
	// contacto —o crea una "closed" coherente si no la hubiera— fijando su total y
	// dejando sus líneas EXACTAMENTE en in.Items (Plan 027 · Ola 1 · T4, cierra H4).
	// Garantiza la invariante "una solicitud closed SIEMPRE tiene sus líneas": nunca
	// deja una solicitud cerrada sin líneas por un fallo entre dos escrituras (antes
	// eran MarkIntakeStatus + un INSERT suelto, sin transacción). El PostgresRepository
	// bloquea la solicitud abierta con FOR UPDATE para serializar cierres concurrentes
	// del mismo contacto y reintenta ante deadlock/serialización (postgres.WithTx).
	//
	// Las líneas se REEMPLAZAN, no se insertan (misma semántica que
	// ReplaceIntakeItems), y es lo que impide que el cierre DUPLIQUE lo que la
	// proyección de item_added ya materializó mientras la solicitud estaba abierta. El
	// conjunto del cierre es la verdad final —trae las personalizaciones y los splits
	// que el carrito hizo después del último item_added— y sustituye lo que hubiera.
	//
	// Devuelve el ID de la solicitud cerrada: quien cierra necesita saber sobre
	// qué cerró para poder colgarle la revisión 1 del ciclo extendido (ADR-0031
	// §3). Releerlo por "la última cerrada de este contacto" sería una carrera con
	// el carrito siguiente.
	CloseIntake(ctx context.Context, in IntakeClose) (intakeID string, err error)
}

// IntakeStore es la lectura + escritura de solicitudes.
type IntakeStore interface {
	IntakeReader
	IntakeWriter
}

// Intake es una solicitud del módulo Carrito, proyección tipada de cart_closed sobre
// public.intakes (Plan 016 · design.md §3.4). ContactID es la identidad OPACA del
// contacto (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID crudo.
// Status es "open" | "closed" | "cancelled" | "expired". ExpiresAt es HISTÓRICA:
// desde T4.7 (D-041.16) no la escribe nadie y nadie la obedece — las solicitudes
// nuevas nacen con zero y las viejas conservan su valor. CreatedAt/UpdatedAt los
// pone el DEFAULT de la tabla en el alta.
type Intake struct {
	ID        string // uuid (asignado al abrir la solicitud "open")
	TenantID  string
	ContactID string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	SessionID string
	Status    string // "open" | "closed" | "cancelled" | "expired"
	Total     float64
	// EventID es el evento conversacional (conversation_events.id) del que nació la
	// solicitud (D-043.21: el hijo declara a su padre; migración 0054). Lo escribe
	// el proyector del cart al CREAR la fila (ensureOpenIntake, con el EventID de
	// la EffectMeta) y NUNCA se pisa uno ya escrito: el upsert lo protege con
	// COALESCE (un evento tiene a lo sumo un contenido durable, índice único
	// parcial intakes_event_id_uidx — E-8). Cadena vacía ⇒ NULL — solo legítimo en
	// filas legadas pre-0054; una fila NUEVA sin él revienta contra el CHECK
	// intakes_event_id_required_chk, que es exactamente lo que D-043.21 quiere: el
	// huérfano imposible por construcción, no improbable.
	EventID   string
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time // HISTÓRICA (T4.7): ya no se escribe ni se obedece; zero en lo nuevo
	// CustomerNote la escribe SOLO el cierre (CloseIntake, D-041.19): el cliente la
	// teclea en el resumen, que es el último paso antes de confirmar, así que una
	// solicitud "open" nunca la tiene. Por eso GetOpenIntake no la lee y
	// UpsertIntake no la escribe —su UPDATE ni menciona la columna, de modo que un
	// upsert sobre una solicitud ya cerrada tampoco podría borrarla—. Para LEER la
	// nota de una solicitud, el camino es el dominio de solicitudes
	// (intakes.Intake), que sí la trae.
	CustomerNote string
}

// IntakeItem es una línea de una solicitud del carrito, lista para persistir en
// public.intake_items (Plan 016 · design.md §3.4). SKU/Label son códigos de
// negocio (catálogo del tenant), NO PII. AddedAt lo pone el DEFAULT de la tabla.
//
// Customization es la personalización NO FACTURABLE de la línea (D-041.17): el
// «sin cebolla» que quien prepara tiene que leer. JAMÁS entra en el cálculo del
// precio (INV-13) y su cero-valor —cadena vacía— es exactamente lo que escribe una
// línea sin personalizar, igual que el DEFAULT de la columna.
type IntakeItem struct {
	IntakeID      string
	SKU           string
	Label         string
	Customization string
	Qty           int
	UnitPrice     float64
	AddedAt       time.Time
}

// IntakeClose es la entrada del cierre atómico de una solicitud del carrito
// (Repository.CloseIntake, Plan 027 · Ola 1 · T4). Total es el total agregado del
// pedido; Items son TODAS sus líneas (fuente de verdad, se insertan de una vez en
// la misma transacción que la transición a "closed"). ContactID es la identidad
// OPACA (Plan 010 / ADR-0010); SessionID solo se usa si hay que crear la solicitud
// "closed" desde cero (no había abierta). El IntakeID de cada Item lo fija CloseIntake.
//
// CustomerNote es la indicación del cliente para TODO el pedido (D-041.19): el
// «dejarlo en portería». Se escribe en la MISMA transacción que la cabecera —es un
// campo suyo, no una tabla aparte— y no toca el total (INV-13). Su cero-valor es
// exactamente lo que cierra un carrito sin indicación, igual que el DEFAULT de la
// columna.
type IntakeClose struct {
	TenantID     string
	ContactID    string
	SessionID    string
	Total        float64
	CustomerNote string
	// EventID es el evento conversacional del cierre (D-043.21), el mismo que viaja
	// en la EffectMeta de cart_closed. En el camino normal la solicitud abierta YA
	// lo declara (lo estampó ensureOpenIntake) y aquí solo rellena un NULL legado
	// (COALESCE, nunca pisa); en la rama sin solicitud abierta —cierre que crea la
	// fila "closed" coherente— es el dato con el que esa fila nueva declara a su
	// padre, sin el cual el INSERT revienta contra el CHECK de la 0054.
	EventID string
	Items   []IntakeItem
}
