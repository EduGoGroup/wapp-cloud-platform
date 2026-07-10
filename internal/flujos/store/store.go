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

// Repository persiste el estado conversacional y las definiciones de flujo
// versionadas. Implementaciones (T2): MemoryRepository (unit CI-safe) y
// PostgresRepository (integración, JSONB vía json.Marshal/Unmarshal).
type Repository interface {
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
	// LatestDefinition devuelve la versión vigente de la definición del flujo.
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	// GetDefinition devuelve la definición de una versión EXACTA. El runtime lo
	// usa para avanzar una conversación con la versión con la que arrancó
	// (Conversation.FlowVersion), de modo que publicar una versión nueva no
	// "salte" una conversación en curso (versionado, design.md §4). Devuelve
	// ErrDefinitionNotFound si no existe esa (tenant_id, flow_id, version).
	GetDefinition(ctx context.Context, tenantID, flowID string, version int) (model.Flow, error)
	// InsertDefinition persiste una definición como versión nueva (no muta la
	// vigente; versionado design.md §4). La versión la asigna el repositorio
	// (version = COALESCE(max(version),0)+1 por (tenant_id, flow_id)); el campo
	// f.Version del argumento se ignora. Devuelve la versión asignada.
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
	// InsertResults persiste (en lote) las respuestas de una encuesta como datos
	// de negocio EN CLARO en survey_results (Plan 014 §10.D, ADR-0009). El
	// runtime (T3) lo llama al terminar la conversación (flush). len(rows)==0 es
	// un no-op. answer_code NO se cifra: es un código de opción agregable, no PII
	// (la identidad la protege el contact_id opaco, ADR-0010).
	InsertResults(ctx context.Context, rows []SurveyResult) error
	// InsertFlowEvent persiste UN efecto del motor de flujos en el outbox
	// append-only flow_events (Plan 015 · T2, ADR-0009). CERO PII: ContactID es la
	// identidad OPACA (ADR-0010). El Payload (map) se serializa a JSONB; nil → {}.
	InsertFlowEvent(ctx context.Context, ev FlowEvent) error
	// GetTenantContent devuelve el blob JSON crudo de contenido de negocio para
	// (tenantID, ref) de public.tenant_content (Plan 015 · T2). Su firma coincide
	// EXACTAMENTE con content.Store (structural typing): el PostgresRepository lo
	// satisface. Devuelve ErrTenantContentNotFound si la ref no existe.
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)

	// UpsertOrder inserta o actualiza (upsert por ID) una orden del carrito en
	// public.orders (Plan 016 · T0/T2, ADR-0009). Idempotente por o.ID: crea el
	// "open" una vez (primer item_added) y no lo duplica si se reprocesa el mismo
	// entrante. Las transiciones de estado posteriores van por MarkOrderStatus.
	// CERO PII: o.ContactID es la identidad OPACA (ADR-0010).
	UpsertOrder(ctx context.Context, o Order) error
	// InsertOrderItems persiste (en lote) las líneas de una orden en
	// public.order_items (Plan 016 · T0/T2). len(items)==0 es un no-op. added_at
	// usa el DEFAULT now() de la tabla. sku/label son códigos de negocio, NO PII.
	InsertOrderItems(ctx context.Context, orderID string, items []OrderItem) error
	// GetOpenOrder devuelve la orden "open" del contacto para (tenantID, contactID),
	// si existe (found=false sin error si no hay). Identidad de negocio: UNA orden
	// "open" por (tenant_id, contact_id) (design.md §3.4). La usa el runtime al
	// reanudar y para evaluar el TTL (design.md §4.3).
	GetOpenOrder(ctx context.Context, tenantID, contactID string) (order Order, found bool, err error)
	// MarkOrderStatus transiciona el estado de una orden (por ID) y fija su total,
	// actualizando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled"
	// | "expired". Reprocesar el mismo entrante no cambia la semántica (idempotente
	// por el last_wa_message_id del runtime).
	MarkOrderStatus(ctx context.Context, orderID, status string, total float64) error
	// CloseOrder cierra ATÓMICAMENTE (una sola transacción) la orden "open" del
	// contacto —o crea una "closed" coherente si no la hubiera— fijando su total e
	// insertando TODAS sus líneas (Plan 027 · Ola 1 · T4, cierra H4). Garantiza la
	// invariante "una orden closed SIEMPRE tiene sus líneas": nunca deja una orden
	// cerrada sin líneas por un fallo entre dos escrituras (antes eran MarkOrderStatus
	// + InsertOrderItems sueltos, sin transacción). El PostgresRepository bloquea la
	// orden abierta con FOR UPDATE para serializar cierres concurrentes del mismo
	// contacto y reintenta ante deadlock/serialización (postgres.WithTx).
	CloseOrder(ctx context.Context, in OrderClose) error
	// GetTenantSettings devuelve la config del carrito para tenantID desde
	// public.tenant_settings (Plan 016 · T0). Si el tenant NO tiene fila, devuelve
	// los DEFAULTS (PageSize=5, OrderTTL=3600s) SIN error: el carrito funciona sin
	// configurar nada (design.md §9.E/§9.G).
	GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error)
}

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

// Order es una orden del módulo Carrito, proyección tipada de cart_closed sobre
// public.orders (Plan 016 · design.md §3.4). ContactID es la identidad OPACA del
// contacto (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID crudo.
// Status es "open" | "closed" | "cancelled" | "expired". ExpiresAt es now +
// order_ttl (nulo/zero si no aplica). CreatedAt/UpdatedAt los pone el DEFAULT de
// la tabla en el alta.
type Order struct {
	ID        string // uuid (asignado al abrir la orden "open")
	TenantID  string
	ContactID string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	SessionID string
	Status    string // "open" | "closed" | "cancelled" | "expired"
	Total     float64
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time // now + tenant_settings.order_ttl; zero si no aplica
}

// OrderItem es una línea de una orden del carrito, lista para persistir en
// public.order_items (Plan 016 · design.md §3.4). SKU/Label son códigos de
// negocio (catálogo del tenant), NO PII. AddedAt lo pone el DEFAULT de la tabla.
type OrderItem struct {
	OrderID   string
	SKU       string
	Label     string
	Qty       int
	UnitPrice float64
	AddedAt   time.Time
}

// OrderClose es la entrada del cierre atómico de una orden del carrito
// (Repository.CloseOrder, Plan 027 · Ola 1 · T4). Total es el total agregado del
// pedido; Items son TODAS sus líneas (fuente de verdad, se insertan de una vez en
// la misma transacción que la transición a "closed"). ContactID es la identidad
// OPACA (Plan 010 / ADR-0010); SessionID solo se usa si hay que crear la orden
// "closed" desde cero (no había abierta). El OrderID de cada Item lo fija CloseOrder.
type OrderClose struct {
	TenantID  string
	ContactID string
	SessionID string
	Total     float64
	Items     []OrderItem
}

// TenantSettings es la config del carrito por-tenant (public.tenant_settings,
// Plan 016 · design.md §3.4/§9.G). PageSize es el tamaño de página de la
// paginación (default 5); OrderTTL es el TTL de la orden (persistido como
// order_ttl_seconds INTEGER, default 3600s). GetTenantSettings devuelve los
// defaults si el tenant no tiene fila.
type TenantSettings struct {
	TenantID string
	PageSize int           // default DefaultPageSize
	OrderTTL time.Duration // persistido como order_ttl_seconds; default DefaultOrderTTL
}

// Defaults de tenant_settings (design.md §9.E/§9.G): valen cuando el tenant no
// tiene fila en public.tenant_settings. Espejan los DEFAULT de la migración 0013.
const (
	// DefaultPageSize es el tamaño de página por defecto de la paginación del carrito.
	DefaultPageSize = 5
	// DefaultOrderTTL es el TTL por defecto de una orden (3600s = 1h).
	DefaultOrderTTL = time.Hour
)

// ErrDefinitionNotFound lo devuelve LatestDefinition cuando no existe ninguna
// versión de la definición para (tenant_id, flow_id). Se inspecciona con
// errors.Is.
var ErrDefinitionNotFound = errors.New("definición de flujo no encontrada")

// ErrTenantContentNotFound lo devuelve GetTenantContent cuando no existe blob de
// contenido para (tenant_id, ref) en tenant_content. Se inspecciona con errors.Is.
var ErrTenantContentNotFound = errors.New("contenido de tenant no encontrado")
