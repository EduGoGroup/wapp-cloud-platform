// Package integrations implementa el puente CRM del Plan 042: la cola durable
// webhook_outbox, la configuración por-tenant tenant_integrations, y el worker en
// proceso que entrega firmado (D-042.4/D-042.5). NUNCA importa net/http en este
// archivo ni en postgres.go — el POST vive exclusivamente en worker.go (INV-02:
// el WebhookSink solo encola, nunca entrega en línea con el mensaje).
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrClaimLost lo devuelven las tres transiciones terminales/de reintento cuando
// la valla optimista NO casa: la fila ya no está en `delivering` con el
// `claimed_at` que este worker reclamó, porque su lease venció y OTRO worker la
// reclamó mientras tanto (Plan 042 · Ola 3.1).
//
// No es un fallo de entrega: es este worker llegando tarde. El llamante NO debe
// reintentar ni contar el intento — la fila ya tiene otro dueño que la resolverá.
var ErrClaimLost = errors.New("integrations: el claim de la entrega ya no es vigente")

// Estados del ciclo de vida INTERNO de una entrega (webhook_outbox.status, CHECK
// en la 0046 — vocabulario cerrado que fija este plan, al contrario que
// intakes.status). Ver structure/0046_webhook_outbox.sql.
const (
	StatusPending    = "pending"
	StatusDelivering = "delivering"
	StatusDelivered  = "delivered"
	StatusDead       = "dead"
)

// WebhookOutbox es una fila de public.webhook_outbox (0046). Payload es JSON crudo:
// ni el store ni el worker necesitan tipar su forma (la fija el contrato
// wapp-crm-v1, no esta capa).
type WebhookOutbox struct {
	ID            int64
	TenantID      string
	Kind          string
	Payload       json.RawMessage
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	LastError     string // "" == NULL (nunca falló)
	// ClaimedAt es el instante del claim VIGENTE (migración 0049). Cero == NULL
	// (no hay claim vigente). Es lo que hace de lease —distingue "la están
	// entregando ahora" de "su worker murió"— y de valla optimista al cerrar la
	// fila: las tres transiciones lo exigen para no pisar el claim de otro.
	ClaimedAt time.Time
}

// TenantIntegration es una fila de public.tenant_integrations (0047), SIN el
// secreto: el secreto cifrado se lee aparte con GetTenantSecret, para que un
// caller que solo necesita saber "hay integración habilitada" (el gate del
// WebhookSink) no tenga que tocar criptografía.
type TenantIntegration struct {
	TenantID       string
	CatalogAdapter string
	EventsAdapter  string
	EndpointURL    string // "" == NULL
	HasSecret      bool
	Enabled        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store es el puerto que el WebhookSink (encolador) y el Worker (entregador)
// necesitan del almacén (T3.1, design.md §4). La implementación real es
// *Postgres; un fake en memoria basta para los tests de worker.go y del sink que
// no requieren Postgres real.
type Store interface {
	// EnqueueWebhook encola una entrega (INSERT puro, T3.2 — INV-02: el llamante
	// NUNCA hace POST). Devuelve el id de la fila para correlación en logs.
	EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error)

	// ClaimWebhookBatch reclama hasta `limit` filas pending/vencidas con
	// FOR UPDATE SKIP LOCKED (seguro con más de una réplica del proceso), las
	// marca delivering y les SELLA el claim con claimed_at = now(), todo en UNA
	// sentencia atómica. Las filas devueltas traen ya ese claimed_at: es el
	// testigo que hay que devolver en las tres transiciones de cierre.
	ClaimWebhookBatch(ctx context.Context, limit int) ([]WebhookOutbox, error)

	// MarkWebhookDelivered cierra una entrega en 2xx (terminal). `claim` es la
	// fila TAL COMO la devolvió ClaimWebhookBatch: su claimed_at es la valla.
	// Devuelve ErrClaimLost si el claim ya no es vigente (lease vencido y
	// re-reclamada por otro worker).
	MarkWebhookDelivered(ctx context.Context, claim WebhookOutbox) error

	// MarkWebhookFailed registra un intento fallido: attempts++, vuelve a pending
	// con next_attempt_at = nextAttemptAt (el backoff lo calcula el worker) y deja
	// el motivo en last_error (T3.4, visibilidad de dead). Mismas reglas de valla
	// que MarkWebhookDelivered.
	MarkWebhookFailed(ctx context.Context, claim WebhookOutbox, nextAttemptAt time.Time, lastErr string) error

	// MarkWebhookDead cierra una entrega que agotó sus reintentos (terminal,
	// visible — T3.4). Mismas reglas de valla que MarkWebhookDelivered.
	MarkWebhookDead(ctx context.Context, claim WebhookOutbox, lastErr string) error

	// RecoverOrphanDeliveries devuelve a pending las filas `delivering` cuyo CLAIM
	// VENCIÓ — las que llevan más de `lease` sin resolverse, más las que no tienen
	// claimed_at (reclamadas por el código anterior a la migración 0049).
	//
	// El lease es lo que distingue "su worker murió" de "la están entregando
	// ahora mismo": sin él (Ola 3) esta consulta revertía TODA fila en vuelo, y
	// con dos réplicas eso producía entregas duplicadas — el hallazgo #2 del
	// review de las Olas 1-3. Cuenta el intento (attempts++) para que una fila que
	// tumba a su worker una y otra vez acabe en dead en vez de girar para siempre.
	//
	// Se llama al arrancar Y periódicamente (Plan 042 · Ola 3.1): con más de una
	// réplica, el trabajo de la que muere lo recogen las vivas sin esperar a que
	// alguien reinicie un proceso. Devuelve cuántas filas recuperó.
	RecoverOrphanDeliveries(ctx context.Context, lease time.Duration) (int, error)

	// GetTenantIntegration lee la configuración de adaptadores del tenant. found
	// es false si la fila no existe (equivale a local/local — sin CRM).
	GetTenantIntegration(ctx context.Context, tenantID string) (ti TenantIntegration, found bool, err error)

	// GetTenantSecret descifra el secreto de firma HMAC del tenant (con la KEK
	// que envolvió ESA fila, no la current — Plan 012 §10.D, coexisten filas de
	// varias KEK tras una rotación parcial). found es false si el tenant no
	// tiene fila o no tiene secreto configurado (las tres columnas NULL juntas).
	GetTenantSecret(ctx context.Context, tenantID string) (secret string, found bool, err error)

	// UpsertTenantIntegration crea o actualiza la configuración del tenant. Si
	// secret es "" el secreto EXISTENTE no se toca (permite reconfigurar
	// endpoint/adapters sin re-enviar el secreto); si no es "", se cifra y
	// reemplaza las tres columnas del envelope.
	UpsertTenantIntegration(ctx context.Context, ti TenantIntegration, secret string) error

	// DeleteTenantIntegration borra la fila del tenant (vuelve a local/local).
	DeleteTenantIntegration(ctx context.Context, tenantID string) error
}
