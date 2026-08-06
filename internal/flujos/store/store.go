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
}

// IntakeWriter escribe/transiciona solicitudes del carrito (proyección del PersistSink).
type IntakeWriter interface {
	// UpsertIntake inserta o actualiza (upsert por ID) una solicitud del carrito en
	// public.intakes (Plan 016 · T0/T2, ADR-0009). Idempotente por o.ID: crea el
	// "open" una vez (primer item_added) y no lo duplica si se reprocesa el mismo
	// entrante. Las transiciones de estado posteriores van por MarkIntakeStatus.
	// CERO PII: o.ContactID es la identidad OPACA (ADR-0010).
	UpsertIntake(ctx context.Context, o Intake) error
	// InsertIntakeItems persiste (en lote) las líneas de una solicitud en
	// public.intake_items (Plan 016 · T0/T2). len(items)==0 es un no-op. added_at
	// usa el DEFAULT now() de la tabla. sku/label son códigos de negocio, NO PII.
	InsertIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error
	// MarkIntakeStatus transiciona el estado de una solicitud (por ID) y fija su total,
	// actualizando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled"
	// | "expired". Reprocesar el mismo entrante no cambia la semántica (idempotente
	// por el last_wa_message_id del runtime).
	MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error
	// CloseIntake cierra ATÓMICAMENTE (una sola transacción) la solicitud "open" del
	// contacto —o crea una "closed" coherente si no la hubiera— fijando su total e
	// insertando TODAS sus líneas (Plan 027 · Ola 1 · T4, cierra H4). Garantiza la
	// invariante "una solicitud closed SIEMPRE tiene sus líneas": nunca deja una solicitud
	// cerrada sin líneas por un fallo entre dos escrituras (antes eran MarkIntakeStatus
	// + InsertIntakeItems sueltos, sin transacción). El PostgresRepository bloquea la
	// solicitud abierta con FOR UPDATE para serializar cierres concurrentes del mismo
	// contacto y reintenta ante deadlock/serialización (postgres.WithTx).
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
	// los DEFAULTS (PageSize=5, OrderTTL=3600s) SIN error: el carrito funciona sin
	// configurar nada (design.md §9.E/§9.G).
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
// Status es "open" | "closed" | "cancelled" | "expired". ExpiresAt es now +
// order_ttl (nulo/zero si no aplica). CreatedAt/UpdatedAt los pone el DEFAULT de
// la tabla en el alta.
type Intake struct {
	ID        string // uuid (asignado al abrir la solicitud "open")
	TenantID  string
	ContactID string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	SessionID string
	Status    string // "open" | "closed" | "cancelled" | "expired"
	Total     float64
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time // now + tenant_settings.order_ttl; zero si no aplica
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
	Items        []IntakeItem
}

// TenantSettings es la config del carrito por-tenant (public.tenant_settings,
// Plan 016 · design.md §3.4/§9.G). PageSize es el tamaño de página de la
// paginación (default 5); OrderTTL es el TTL de la solicitud (persistido como
// order_ttl_seconds INTEGER, default 3600s). GetTenantSettings devuelve los
// defaults si el tenant no tiene fila.
type TenantSettings struct {
	TenantID string
	PageSize int           // default DefaultPageSize
	OrderTTL time.Duration // persistido como order_ttl_seconds; default DefaultOrderTTL
	// ConversationTTL es el TTL CONVERSACIONAL genérico (Plan 029 · T9, migración
	// 0034): tiempo tras el cual un estado vivo sin tocar se descarta y el entrante
	// arranca de nuevo. Persistido como conversation_ttl_seconds; 0 (DEFAULT) ⇒ sin
	// vencimiento (tenants existentes intactos). Semántica DISTINTA a OrderTTL (que
	// es de la SOLICITUD del carrito, no de la conversación).
	ConversationTTL time.Duration
}

// Defaults de tenant_settings (design.md §9.E/§9.G): valen cuando el tenant no
// tiene fila en public.tenant_settings. Espejan los DEFAULT de la migración 0013.
const (
	// DefaultPageSize es el tamaño de página por defecto de la paginación del carrito.
	DefaultPageSize = 5
	// DefaultOrderTTL es el TTL por defecto de una solicitud (3600s = 1h).
	DefaultOrderTTL = time.Hour
)

// ErrDefinitionNotFound lo devuelve LatestDefinition cuando no existe ninguna
// versión de la definición para (tenant_id, flow_id). Se inspecciona con
// errors.Is.
var ErrDefinitionNotFound = errors.New("definición de flujo no encontrada")

// ErrTenantContentNotFound lo devuelve GetTenantContent cuando no existe blob de
// contenido para (tenant_id, ref) en tenant_content. Se inspecciona con errors.Is.
var ErrTenantContentNotFound = errors.New("contenido de tenant no encontrado")
