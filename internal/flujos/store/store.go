// Package store define el contrato de persistencia del motor de flujos.
//
// En T0 solo está la interfaz Repository y la clave conversacional Key; las
// implementaciones PostgresRepository(*sql.DB) y MemoryRepository llegan en T2
// (siguiendo el patrón de internal/gateway/lease).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// Key es la clave lógica de una conversación (Pieza 05 §3, design.md §5).
// ContactID es la identidad OPACA del contacto (contacts.contact_id, UUID como
// texto), NO el JID crudo: el motor se clava por contact_id (Plan 010, design.md
// §1, §3). La resolución JID→contact_id la hace el runtime (T4); esta capa opera
// sobre el contact_id ya resuelto.
type Key struct {
	TenantID  string
	SessionID string
	ContactID string
}

// String devuelve una representación estable y OPACA de la clave, apta como índice
// de mapa fuera de este paquete (p. ej. el token-bucket de auto-respuestas del
// runtime, Plan 020 · T0). Son IDs opacos (tenant/session/contact): no expone PII.
func (k Key) String() string {
	return k.TenantID + "|" + k.SessionID + "|" + k.ContactID
}

// Interfaces SEGREGADAS por subdominio (ISP, Plan 027 · Ola 2 · T9, cierra H12).
// Antes Repository era una única interfaz "gorda" (7 tablas, ~5 subdominios) que
// obligaba a cada consumidor a depender de métodos que no usa. Ahora cada
// subdominio tiene su interfaz pequeña; los consumidores declaran SOLO lo que
// necesitan (el runtime compone su FlowStore; el PersistSink su almacén de
// proyección) y Repository queda como la COMPOSICIÓN de todas (retrocompat: las
// implementaciones Postgres/Memory la satisfacen entera sin cambios).

// ConversationStore persiste el estado conversacional vivo por Key (design.md §5/§6).
type ConversationStore interface {
	// Exists indica si ya hay una conversación viva para la clave.
	Exists(ctx context.Context, key Key) (bool, error)
	// Load carga el estado de la conversación; found=false sin error si no hay.
	Load(ctx context.Context, key Key) (state model.Conversation, found bool, err error)
	// Save inserta o actualiza (upsert) el estado de la conversación.
	Save(ctx context.Context, state model.Conversation) error
	// Delete elimina la conversación viva de la clave (libera la clave para que
	// un entrante posterior pueda volver a disparar un flujo). Idempotente: si no
	// había fila, NO es error. Lo usa el escape global (Plan 019 · T4) para cortar
	// una conversación viva, misma liberación de clave que se hacía por SQL manual
	// en e2e previos (design.md §6).
	Delete(ctx context.Context, key Key) error
}

// DefinitionReader lee definiciones de flujo versionadas (lo que necesita el runtime).
type DefinitionReader interface {
	// LatestDefinition devuelve la versión vigente de la definición del flujo.
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	// GetDefinition devuelve la definición de una versión EXACTA. El runtime lo
	// usa para avanzar una conversación con la versión con la que arrancó
	// (Conversation.FlowVersion), de modo que publicar una versión nueva no
	// "salte" una conversación en curso (versionado, design.md §4). Devuelve
	// ErrDefinitionNotFound si no existe esa (tenant_id, flow_id, version).
	GetDefinition(ctx context.Context, tenantID, flowID string, version int) (model.Flow, error)
}

// DefinitionStore es DefinitionReader + el alta de versiones (lo usa el admin).
type DefinitionStore interface {
	DefinitionReader
	// InsertDefinition persiste una definición como versión nueva (no muta la
	// vigente; versionado design.md §4). La versión la asigna el repositorio
	// (version = COALESCE(max(version),0)+1 por (tenant_id, flow_id)); el campo
	// f.Version del argumento se ignora. Devuelve la versión asignada.
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
}

// SurveyResultStore proyecta las respuestas de encuesta EN CLARO (Plan 014/015).
type SurveyResultStore interface {
	// InsertResults persiste (en lote) las respuestas de una encuesta como datos
	// de negocio EN CLARO en survey_results (Plan 014 §10.D, ADR-0009). El
	// runtime (T3) lo llama al terminar la conversación (flush). len(rows)==0 es
	// un no-op. answer_code NO se cifra: es un código de opción agregable, no PII
	// (la identidad la protege el contact_id opaco, ADR-0010).
	InsertResults(ctx context.Context, rows []SurveyResult) error
	// ListResults devuelve las respuestas que este contacto ya dio en este flujo,
	// EN ORDEN CRONOLÓGICO (created_at, id), acotadas al tenant (INV-8). Sin
	// respuestas devuelve la lista vacía SIN error.
	//
	// Existe para el RESUMEN del rescate (Plan 043 · Ola 3): el estado vivo de la
	// encuesta —vars["answers"]— se borra al conmutar de evento, así que sin esta
	// lectura un rescate enseñaría siempre CERO respuestas. La fuente durable ya
	// estaba escrita respuesta a respuesta por el efecto survey_answer; lo único
	// que faltaba era leerla.
	//
	// DOS TRAMPAS DE ESTA TABLA, y ninguna la puede arreglar esta función:
	//
	//   - NO HAY SESIÓN. survey_results no tiene session_id (migración 0008), así
	//     que dos sesiones del mismo tenant y contacto comparten resultados. Es la
	//     misma asimetría que ya tiene GetOpenIntake, resuelto por (tenant,
	//     contacto) sin sesión, mientras el evento es por (tenant, sesión,
	//     contacto).
	//   - NO HAY IDENTIDAD DE PASADA. Tampoco hay columna que diga QUÉ recorrido
	//     produjo la fila: quien respondió la misma encuesta el mes pasado y vuelve
	//     hoy trae las dos tandas mezcladas. Por eso el orden es cronológico y
	//     FlowVersion viaja en cada fila: quien resuma decide la política (lo
	//     normal es quedarse con la ÚLTIMA respuesta de cada question_id). Elegirla
	//     aquí sería imponérsela a todos los lectores futuros con una columna que
	//     no existe.
	ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]SurveyResult, error)
}

// FlowEventStore materializa el outbox append-only de efectos (Plan 015 · T2).
type FlowEventStore interface {
	// InsertFlowEvent persiste UN efecto del motor de flujos en el outbox
	// append-only flow_events (Plan 015 · T2, ADR-0009). CERO PII: ContactID es la
	// identidad OPACA (ADR-0010). El Payload (map) se serializa a JSONB; nil → {}.
	InsertFlowEvent(ctx context.Context, ev FlowEvent) error
}

// TenantContentReader lee el contenido de negocio por-tenant (Plan 015 · T2).
type TenantContentReader interface {
	// GetTenantContent devuelve el blob JSON crudo de contenido de negocio para
	// (tenantID, ref) de public.tenant_content (Plan 015 · T2). Su firma coincide
	// EXACTAMENTE con content.Store (structural typing): el PostgresRepository lo
	// satisface. Devuelve ErrTenantContentNotFound si la ref no existe.
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// IntakeReader lee la solicitud abierta del carrito (reanudación/TTL en el runtime).
type IntakeReader interface {
	// GetOpenIntake devuelve la solicitud "open" del contacto para (tenantID, contactID),
	// si existe (found=false sin error si no hay). Identidad de negocio: UNA solicitud
	// "open" por (tenant_id, contact_id) (design.md §3.4). La usa el runtime al
	// reanudar y para evaluar el TTL (design.md §4.3).
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (intake Intake, found bool, err error)
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

// TenantSettingsReader lee la config del carrito por-tenant (Plan 016 · T0).
type TenantSettingsReader interface {
	// GetTenantSettings devuelve la config del carrito para tenantID desde
	// public.tenant_settings (Plan 016 · T0). Si el tenant NO tiene fila, devuelve
	// DefaultTenantSettings(tenantID) SIN error: el carrito funciona sin configurar
	// nada (design.md §9.E/§9.G). Si SÍ tiene fila, devuelve lo que diga la fila sin
	// sustituir ceros por defaults (un 0 puede ser un override explícito, no un hueco).
	GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error)
}

// Repository es la COMPOSICIÓN de las interfaces segregadas: persiste el estado
// conversacional, las definiciones versionadas, los resultados/efectos y las
// solicitudes del carrito. Implementaciones: MemoryRepository (unit CI-safe) y
// PostgresRepository (integración, JSONB vía json.Marshal/Unmarshal). Se conserva
// para retrocompat y para quien necesite el conjunto completo; los consumidores
// concretos deben preferir la interfaz segregada que realmente usan (ISP).
type Repository interface {
	ConversationStore
	DefinitionStore
	SurveyResultStore
	FlowEventStore
	TenantContentReader
	IntakeStore
	TenantSettingsReader
}

// Ambas implementaciones satisfacen la composición completa (retrocompat tras la
// segregación por subdominio, Plan 027 · Ola 2 · T9): un método que falte en
// cualquiera rompe aquí en compilación, no en un consumidor lejano.
var (
	_ Repository = (*PostgresRepository)(nil)
	_ Repository = (*MemoryRepository)(nil)

	// El versionado NO entra en Repository: solo lo necesita el import de catálogo
	// (Plan 041 · T3.3), y meterlo en la composición obligaría a implementarlo a
	// cualquier doble futuro que solo quiera conversaciones. Se afirma aparte.
	_ TenantContentVersioner = (*PostgresRepository)(nil)
	_ TenantContentVersioner = (*MemoryRepository)(nil)
)

// FlowSummary es el resumen de UN flujo publicado por un tenant: su flow_id y la
// última versión vigente (más su fecha de alta). Lo devuelve ListDefinitions para
// alimentar el listado de la API pública (GET /api/v1/flows, Plan 018 · T5). No
// incluye la definición completa (solo la cabecera); el detalle se obtiene con
// LatestDefinition. CERO PII: flow_id es un identificador de negocio (ADR-0009).
type FlowSummary struct {
	FlowID    string
	Version   int
	CreatedAt time.Time
}

// TenantContentSummary es la cabecera de un blob de contenido por-tenant
// (public.tenant_content, Plan 018 · T6): su ref lógica y las marcas de tiempo. NO
// incluye el blob (se obtiene con GetTenantContent). La devuelve ListTenantContent
// para alimentar el listado de la API pública (GET /api/v1/tenant-content). CERO
// PII: ref es un identificador de negocio (ADR-0009).
type TenantContentSummary struct {
	Ref       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Procedencias válidas de una fila de public.tenant_content_versions (D-041.8).
// Describen el ACTO que archivó la versión, no el blob archivado —la de este
// último no la registró nadie cuando se escribió—. Espejan el CHECK de la
// migración 0044: si aquí se admitiera una más, Postgres la rechazaría y el
// repositorio en memoria no, y los tests dejarían de decir la verdad.
const (
	// VersionSourceImportJSON: la desplazó un POST /api/v1/catalog/import con
	// documento JSON (Plan 041 · T3.3).
	VersionSourceImportJSON = "import_json"
	// VersionSourceImportTabular: la desplazó el import de planilla CSV/XLSX
	// (Plan 041 · T3.4).
	VersionSourceImportTabular = "import_tabular"
	// VersionSourceManual: la desplazó una escritura a mano del contenido. HOY NO
	// SE EMITE: el PUT /api/v1/tenant-content genérico no versiona (follow-up
	// declarado en D-041.8). Existe para que ese follow-up no tenga que migrar el
	// CHECK.
	VersionSourceManual = "manual"
)

// ErrInvalidVersionSource lo devuelve ReplaceTenantContentVersioned cuando la
// procedencia no es una de las tres válidas. Es un error de PROGRAMACIÓN (la
// procedencia la elige el código, nunca el usuario), así que se rechaza antes de
// tocar la BD en vez de dejar que reviente el CHECK.
var ErrInvalidVersionSource = errors.New("procedencia de versión de contenido inválida")

// TenantContentVersion es UNA fila archivada de public.tenant_content_versions:
// el blob que estaba vigente ANTES del import que la creó. Content es el blob
// VIEJO (no el que quedó), Source la procedencia del acto que lo desplazó. La
// devuelve el repositorio en memoria para que los tests puedan afirmar QUÉ se
// archivó; el camino Postgres la escribe y hoy nadie la relee (la restauración es
// follow-up de D-041.8). CERO PII: aquí solo hay catálogo.
type TenantContentVersion struct {
	Version   int
	Content   []byte
	Source    string
	CreatedAt time.Time
}

// TenantContentVersioner archiva el contenido vigente y escribe el nuevo en UN
// SOLO ACTO (Plan 041 · T3.3, D-041.8). Lo satisfacen *PostgresRepository y
// *MemoryRepository.
//
// POR QUÉ UN MÉTODO Y NO DOS LLAMADAS DEL LLAMANTE: archivar y escribir por
// separado deja una ventana en la que el proceso se cae entre medias y queda o
// una versión de algo que sigue vigente, o un catálogo nuevo sin rastro del que
// sustituyó. Con dos operaciones no hay orden que salve las dos: el único
// arreglo es que sean una.
type TenantContentVersioner interface {
	// ReplaceTenantContentVersioned archiva el blob VIGENTE de (tenantID, ref)
	// como la siguiente versión y escribe blob en su lugar, todo en la misma
	// transacción. source debe ser una de las constantes VersionSource*.
	//
	// Devuelve el número de versión ARCHIVADA, o 0 si no había blob vigente que
	// archivar: el primer import sobre una ref vacía escribe y no versiona
	// (D-041.8; «no hay versiones» y «no hay contenido» son casos distintos).
	ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (archived int, err error)
}

// validVersionSource comprueba la procedencia contra el conjunto cerrado del
// CHECK de la 0044.
func validVersionSource(source string) bool {
	switch source {
	case VersionSourceImportJSON, VersionSourceImportTabular, VersionSourceManual:
		return true
	default:
		return false
	}
}

// SurveyResult es una respuesta de encuesta lista para persistir EN CLARO en
// survey_results (Plan 014 §10.D). ContactID es la identidad OPACA del contacto
// (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID crudo.
// AnswerCode es el código de la opción elegida (dato de negocio agregable, no
// PII). created_at lo pone el DEFAULT de la tabla.
type SurveyResult struct {
	TenantID    string
	ContactID   string
	FlowID      string
	FlowVersion int
	QuestionID  string
	AnswerCode  string
	// EventID es el evento conversacional (conversation_events.id) durante el que
	// se respondió (D-043.21: el hijo declara a su padre; migración 0054). Lo
	// escribe el proyector del módulo survey con el EventID de la EffectMeta. Es
	// TRAZABILIDAD, no «contenido que puede morir» (D-043.18): la encuesta NO entra
	// en la vista event_content. Cadena vacía ⇒ NULL en la tabla — solo legítimo en
	// filas legadas pre-0054; para una fila NUEVA el CHECK
	// survey_results_event_id_required_chk la rechaza.
	EventID string
	// CreatedAt lo pone el DEFAULT now() de la tabla y solo lo rellena la LECTURA
	// (ListResults); InsertResults ni lo mira, igual que IntakeItem.AddedAt.
	//
	// Se lee porque hasta la 0054 era lo ÚNICO que permitía acotar «las respuestas
	// de ESTA pasada»: survey_results no tenía session_id ni event_id, así que
	// quien resumía una encuesta rescatada solo podía separar la tanda de hoy de la
	// del mes pasado usando la fecha del evento como cota inferior
	// (runtime/summary_sources.go). Con EventID esa correlación por timestamp queda
	// como FALLBACK del legado (filas con event_id NULL); la vía por event_id es la
	// preferida (D-043.21).
	CreatedAt time.Time
}

// FlowEvent es un efecto del motor de flujos listo para persistir en el outbox
// append-only flow_events (Plan 015 · T2, ADR-0009). ContactID es la identidad
// OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el
// número/JID crudo. Kind es "persist" | "event"; Name es el nombre lógico del
// efecto (p.ej. "survey_answer"). Payload es el cuerpo de negocio del efecto y se
// serializa a JSONB en el repositorio Postgres (nil → {}). created_at lo pone el
// DEFAULT de la tabla.
type FlowEvent struct {
	TenantID    string
	ContactID   string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	FlowID      string
	FlowVersion int
	Kind        string         // "persist" | "event"
	Name        string         // "survey_answer" | ...
	Payload     map[string]any // se serializa a JSONB en el repo postgres
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

// TenantSettings es la config del carrito por-tenant (public.tenant_settings,
// Plan 016 · design.md §3.4/§9.G). PageSize es el tamaño de página de la
// paginación (default 5); OrderTTL se LEE y no se obedece (ver su campo).
// GetTenantSettings devuelve los defaults si el tenant no tiene fila.
type TenantSettings struct {
	TenantID string
	PageSize int // default DefaultPageSize
	// OrderTTL es el TTL de la solicitud (order_ttl_seconds INTEGER, default
	// 3600s), DEROGADO como causa de muerte por D-041.16 (T4.7): se sigue leyendo
	// por compatibilidad y NINGÚN código actúa sobre él. Si vuelves a consumirlo
	// para matar algo, estás reintroduciendo el reloj que este plan derogó.
	OrderTTL time.Duration
	// ConversationTTL es el TTL CONVERSACIONAL genérico (Plan 029 · T9, migración
	// 0034): tiempo tras el cual un estado vivo sin tocar se descarta y el entrante
	// arranca de nuevo. Persistido como conversation_ttl_seconds; 0 (DEFAULT) ⇒ sin
	// vencimiento (tenants existentes intactos). Es el ÚNICO TTL vivo de este
	// struct: OrderTTL quedó derogado (D-041.16) y este no lo sustituye — vencer
	// la conversación descarta el estado conversacional, jamás la solicitud.
	ConversationTTL time.Duration
	// BuyerFields es el CHECKLIST de datos mínimos del comprador que el dueño
	// configura (buyer_fields JSONB, migración 0045 · D-041.13). Vacío ⇒ el carrito
	// no pregunta nada y el recorrido de compra es EXACTAMENTE el de antes (INV-15).
	//
	// Es CONFIGURACIÓN, no dato: aquí viven las etiquetas de lo que se va a pedir
	// («RUT», «Dirección de entrega»), jamás lo que el cliente responde. Lo que el
	// cliente responde es PII y no pasa por este struct ni por Vars: viaja en un
	// efecto privado y acaba CIFRADO en intake_buyer_data (T4.5).
	BuyerFields []BuyerField
	// EventInactivityTTL es EL reloj de conversación, en singular (ADR-0029 E-6 /
	// D-043.7, migración 0052 · event_inactivity_ttl_seconds): el silencio tolerado
	// entre interacciones de un evento. Se mide desde conversation_events.last_activity_at
	// y se REFRESCA en cada interacción, así que una conversación activa nunca vence.
	//
	// 0 ⇒ SIN VENCIMIENTO, y es un override EXPLÍCITO de la empresa, no un "no
	// configurado": la columna es NOT NULL DEFAULT 7200, de modo que un tenant que no
	// tocó nada trae 2 h y solo llega un 0 aquí si alguien lo escribió. Confundir los
	// dos casos es el error caro de este campo (ver GetTenantSettings).
	//
	// No sustituye a ConversationTTL ni se colapsa con él (ADR-0029 E-9.2): aquel es
	// recolección de basura del flow_state y solo se evalúa cuando NO hay evento activo.
	EventInactivityTTL time.Duration
	// EventHistoryTTL es RETENCIÓN DE DATOS, no un reloj de conversación (D-043.13,
	// migración 0052 · event_history_ttl_seconds): cuánto tiempo se tiene derecho a
	// guardar el texto del hilo (conversation_event_messages) antes de que la poda
	// perezosa lo vacíe. Vencer no interrumpe ninguna conversación.
	//
	// 0 ⇒ sin poda. Es un default TÉCNICO y retrocompatible, NO una decisión de
	// retención: el valor de negocio lo fija el Plan 046 (MD-043.6).
	EventHistoryTTL time.Duration
}

// DefaultTenantSettings es la config que vale para un tenant SIN fila en
// public.tenant_settings. Espeja los DEFAULT de las columnas (migraciones 0013,
// 0034, 0045, 0052 y 0067) y es la ÚNICA definición de ese juego de valores: la usan
// tanto PostgresRepository como MemoryRepository, para que los dos no puedan
// divergir en silencio como divergirían dos literales copiados.
//
// «Sin fila» es lo único que esto responde. Un tenant CON fila devuelve lo que diga
// la fila, aunque coincida con el cero de Go: ahí no se aplica nada de esto.
func DefaultTenantSettings(tenantID string) TenantSettings {
	return TenantSettings{
		TenantID: tenantID,
		PageSize: DefaultPageSize,
		OrderTTL: DefaultOrderTTL,
		// 🔴 ConversationTTL SE NOMBRA DESDE T4.4 (Plan 046), y omitirlo era el defecto:
		// mientras no estuvo aquí valía el cero de Go, o sea «sin vencimiento», y este
		// es el camino por el que pasan los tenants SIN fila --hoy 2 de 3 en UAT--, que
		// NO leen el DEFAULT de la 0067. Sin esta línea, aquel ALTER no habría llegado a
		// la mayoría de los tenants. BuyerFields nil ⇒ el carrito no pregunta nada.
		ConversationTTL:    DefaultConversationTTL,
		EventInactivityTTL: DefaultEventInactivityTTL,
		EventHistoryTTL:    DefaultEventHistoryTTL,
	}
}

// BuyerField es UN campo del checklist del comprador (D-041.13, ADR-0031 §5).
//
// wApp NO valida la semántica (REQ-25): no sabe qué es un RUT ni si una dirección
// existe. Recolecta lo que el dueño pide y lo pasa. Por eso el campo no tiene tipo
// ni patrón — inventarlos sería prometer una validación que no se hace.
//
// Las etiquetas json son el CONTRATO con la columna JSONB y con el round-trip por
// Conversation.Vars: el runtime siembra estos campos para que el módulo puro los
// lea sin tocar la BD, y ese viaje pasa por JSONB (map[string]any) en cuanto la
// conversación se guarda.
type BuyerField struct {
	// Key es el identificador ESTABLE del dato ("rut"): es la clave con la que se
	// guarda dentro del blob cifrado. Renombrarla en la config no renombra lo ya
	// guardado.
	Key string `json:"key"`
	// Label es lo que se le enseña al cliente por WhatsApp ("RUT"). Vacío ⇒ se
	// pregunta por la Key, que siempre existe.
	Label string `json:"label"`
	// Required distingue lo que el carrito PIDE antes de cerrar de lo que solo
	// declara. Hoy el cart numérico pregunta EXCLUSIVAMENTE los required (D-041.13):
	// un campo opcional en un menú numérico obligaría a inventar una tecla de
	// "saltar" en cada paso, y quien no quiera pedir algo simplemente no lo pone.
	Required bool `json:"required"`
}

// Defaults de tenant_settings (design.md §9.E/§9.G): valen cuando el tenant no
// tiene fila en public.tenant_settings. Espejan los DEFAULT de las migraciones 0013,
// 0034 (conversation_ttl_seconds, hoy con el DEFAULT que le puso la 0067), 0045 y
// 0052.
const (
	// DefaultPageSize es el tamaño de página por defecto de la paginación del carrito.
	DefaultPageSize = 5
	// DefaultOrderTTL es el default de order_ttl_seconds (3600s = 1h) que espeja la
	// migración 0013. DEROGADO como causa de muerte (D-041.16): es el valor que se
	// devuelve cuando el tenant no tiene fila, no un plazo que alguien aplique.
	DefaultOrderTTL = time.Hour
	// DefaultConversationTTL es el default de PLATAFORMA de conversation_ttl_seconds
	// (7200s = 2h, D-046.12 / REQ-19) que espeja el DEFAULT de la migración 0067.
	//
	// 🔴 ANTES ERA 0, Y EL 0 SIGNIFICABA «SIN VENCIMIENTO». Esa era la raíz del
	// hallazgo de privacidad del Plan 046: con 0, el flow_state y sus vars --que
	// llevan el TEXTO LITERAL del cliente-- no caducaban nunca. El valor se igualó al
	// del reloj único (DefaultEventInactivityTTL) por D-046.12.
	//
	// Que compartan número NO los convierte en la misma clave: este es el reloj
	// SUBORDINADO y solo se evalúa con flow_state.event_id IS NULL; con evento
	// conversacional activo manda el otro (ADR-0029 §E-9.2). Colapsarlos está
	// prohibido.
	//
	// Vale para el tenant SIN fila; un tenant CON fila manda siempre, incluido su 0
	// explícito, que sigue siendo un override legítimo («sin vencimiento»).
	DefaultConversationTTL = 2 * time.Hour
	// DefaultEventInactivityTTL es el default de PLATAFORMA de
	// event_inactivity_ttl_seconds (7200s = 2h, D-043.7 / ADR-0029 E-6) que espeja el
	// DEFAULT de la migración 0052. Vale para el tenant SIN fila; un tenant CON fila
	// manda siempre, incluido su 0 («sin vencimiento»).
	DefaultEventInactivityTTL = 2 * time.Hour
	// DefaultEventHistoryTTL es el default TÉCNICO de event_history_ttl_seconds (0 =
	// sin poda) que espeja el DEFAULT de la migración 0052. NO es una decisión de
	// retención: esa la toma el Plan 046 (MD-043.6). Se nombra en vez de dejar el cero
	// implícito para que quien lo cambie sepa qué está cambiando.
	DefaultEventHistoryTTL = time.Duration(0)
)

// ErrDefinitionNotFound lo devuelve LatestDefinition cuando no existe ninguna
// versión de la definición para (tenant_id, flow_id). Se inspecciona con
// errors.Is.
var ErrDefinitionNotFound = errors.New("definición de flujo no encontrada")

// ErrTenantContentNotFound lo devuelve GetTenantContent cuando no existe blob de
// contenido para (tenant_id, ref) en tenant_content. Se inspecciona con errors.Is.
var ErrTenantContentNotFound = errors.New("contenido de tenant no encontrado")
