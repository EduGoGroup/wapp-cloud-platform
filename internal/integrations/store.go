// Package integrations implementa el puente CRM del Plan 042: la cola durable
// webhook_outbox, la configuración por-tenant tenant_integrations, y el worker en
// proceso que entrega firmado (D-042.4/D-042.5). NUNCA importa net/http en este
// archivo ni en postgres.go — el POST vive exclusivamente en worker.go (INV-02:
// el WebhookSink solo encola, nunca entrega en línea con el mensaje).
package integrations

import (
	"context"
	"encoding/json"
	"time"
)

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
	// FOR UPDATE SKIP LOCKED (seguro con más de una réplica del proceso) y las
	// marca delivering en la MISMA transacción, para que un crash entre el claim
	// y el POST dependa solo de RecoverOrphanDeliveries, nunca de una carrera.
	ClaimWebhookBatch(ctx context.Context, limit int) ([]WebhookOutbox, error)

	// MarkWebhookDelivered cierra una entrega en 2xx (terminal).
	MarkWebhookDelivered(ctx context.Context, id int64) error

	// MarkWebhookFailed registra un intento fallido: attempts++, vuelve a pending
	// con next_attempt_at = nextAttemptAt (el backoff lo calcula el worker), y dl
	// deja el motivo en last_error (T3.4, visibilidad de dead).
	MarkWebhookFailed(ctx context.Context, id int64, nextAttemptAt time.Time, lastErr string) error

	// MarkWebhookDead cierra una entrega que agotó sus reintentos (terminal,
	// visible — T3.4).
	MarkWebhookDead(ctx context.Context, id int64, lastErr string) error

	// RecoverOrphanDeliveries vuelve a pending toda fila delivering cuyo
	// next_attempt_at ya venció (D-042.4): un crash del proceso a mitad de
	// entrega no pierde el registro. Se llama UNA vez al arrancar el worker.
	// Devuelve cuántas filas recuperó (para el log de arranque).
	RecoverOrphanDeliveries(ctx context.Context) (int, error)

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
