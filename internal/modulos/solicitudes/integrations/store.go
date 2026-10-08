// Porta internal/integrations/store.go @ 36d5a04

// Package integrations implementa el puente CRM del Plan 042: la cola durable
// webhook_outbox, la configuración por-tenant tenant_integrations, y el worker en
// proceso que entrega firmado (D-042.4/D-042.5). NUNCA importa net/http en este
// archivo ni en postgres.go — el POST vive exclusivamente en worker.go (INV-02:
// el WebhookSink solo encola, nunca entrega en línea con el mensaje).
//
// Las reglas del puerto Store las fija la suite integrationshelpertest.Contrato,
// que corren el doble en memoria (integrationshelpertest.Memoria) en unitario y el
// adaptador Postgres en los procesos de F9.
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
//
// Llega siempre ENVUELTO (%w): se reconoce con errors.Is, nunca comparando el
// texto. Su texto es «integrations: el claim de la entrega ya no es vigente».
var ErrClaimLost = errors.New("integrations: el claim de la entrega ya no es vigente")

// Estados del ciclo de vida INTERNO de una entrega (webhook_outbox.status, CHECK
// en la 0046 — vocabulario cerrado que fija este plan, al contrario que
// intakes.status). Ver structure/0046_webhook_outbox.sql.
//
// StatusDelivered y StatusDead son además dos de los cuatro valores de la métrica
// wapp_webhook_deliveries_total (los otros dos, `failed` y `claim_lost`, no son
// estados de la tabla y viven en worker.go).
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
//
// HasSecret, CreatedAt y UpdatedAt son de LECTURA: los pone el almacén. Lo que
// traigan en un UpsertTenantIntegration se ignora.
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
// *Postgres; el doble en memoria integrationshelpertest.Memoria basta para los
// tests de worker.go y del sink que no requieren Postgres real. Las dos cumplen la
// misma suite (integrationshelpertest.Contrato).
//
// La cola es UNA para todos los tenants: el reclamo y el rescate no filtran por
// tenant (el worker sirve a todos). La configuración sí es por tenant.
type Store interface {
	// EnqueueWebhook encola una entrega (INSERT puro, T3.2 — INV-02: el llamante
	// NUNCA hace POST). Devuelve el id de la fila para correlación en logs.
	//
	// La fila nace `pending`, con cero intentos, sin error, sin claim y reclamable
	// YA (next_attempt_at = created_at = el reloj del almacén). Los id son
	// estrictamente crecientes en el orden de encolado, sea cual sea el tenant (no
	// necesariamente consecutivos). Un payload que no es JSON válido se rechaza con
	// un error de prefijo «integrations: encolar entrega de <kind>: » y no deja fila.
	EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error)

	// ClaimWebhookBatch reclama hasta `limit` filas pending/vencidas con
	// FOR UPDATE SKIP LOCKED (seguro con más de una réplica del proceso), las
	// marca delivering y les SELLA el claim con claimed_at = now(), todo en UNA
	// sentencia atómica. Las filas devueltas traen ya ese claimed_at: es el
	// testigo que hay que devolver en las tres transiciones de cierre.
	//
	// Solo son reclamables las filas `pending` cuyo next_attempt_at ya pasó (o es
	// ahora mismo). Si hay más que `limit`, se eligen las de next_attempt_at más
	// antiguo —no las de id menor—. Una fila ya reclamada no vuelve a salir
	// mientras siga en `delivering`, aunque su lease haya vencido: para eso está
	// RecoverOrphanDeliveries. Dos reclamos simultáneos nunca se llevan la misma
	// fila. El reclamo no toca intentos, next_attempt_at, payload ni last_error.
	// Sin nada reclamable (o con limit 0) devuelve cero filas y ningún error.
	ClaimWebhookBatch(ctx context.Context, limit int) ([]WebhookOutbox, error)

	// MarkWebhookDelivered cierra una entrega en 2xx (terminal) Y VACÍA su payload
	// en la misma sentencia: la fila queda como RECIBO (id, tenant, kind,
	// timestamps, attempts, status) sin conservar una copia en claro de lo que ya
	// tiene el puente. `claim` es la fila TAL COMO la devolvió ClaimWebhookBatch:
	// su claimed_at es la valla. Devuelve ErrClaimLost si el claim ya no es
	// vigente (lease vencido y re-reclamada por otro worker).
	//
	// Vaciar NO es borrar: la fila sobrevive. 🔴 Y no la borra nadie después: la
	// retención por antigüedad se DESCARTÓ (D-046.16, ADR-0043), así que la fila se
	// queda indefinidamente — con su payload ya vacío desde la 0050.
	//
	// La fila queda `delivered`, sin claim y con el payload `{}`; NO cuenta un
	// intento y conserva next_attempt_at y last_error. La valla (común a las tres
	// transiciones): la fila tiene que seguir en `delivering` con un claimed_at
	// IGUAL al de `claim`. Si no —id desconocido, fila no reclamada, fila ya
	// cerrada, sello de otro claim, o fila en `delivering` sin sello— devuelve
	// «integrations: entrega <id> hacia delivered: » + ErrClaimLost (envuelto) y la
	// fila queda IDÉNTICA.
	MarkWebhookDelivered(ctx context.Context, claim WebhookOutbox) error

	// MarkWebhookFailed registra un intento fallido: attempts++, vuelve a pending
	// con next_attempt_at = nextAttemptAt (el backoff lo calcula el worker) y deja
	// el motivo en last_error (T3.4, visibilidad de dead). Mismas reglas de valla
	// que MarkWebhookDelivered.
	//
	// El payload se conserva (hay que volver a entregarlo) y el claim se cierra. Con
	// la valla rota: «integrations: entrega <id> hacia reintento: » + ErrClaimLost.
	MarkWebhookFailed(ctx context.Context, claim WebhookOutbox, nextAttemptAt time.Time, lastErr string) error

	// MarkWebhookDead cierra una entrega que agotó sus reintentos (terminal,
	// visible — T3.4). Mismas reglas de valla que MarkWebhookDelivered.
	//
	// La fila queda `dead` con attempts++, el motivo en last_error, sin claim y
	// CON su payload: es lo único que dice qué no se entregó (definitivo desde
	// D-046.16). No vuelve a ser reclamable ni rescatable. Con la valla rota:
	// «integrations: entrega <id> hacia dead: » + ErrClaimLost.
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
	//
	// Un claim vence cuando claimed_at es ESTRICTAMENTE anterior a (ahora − lease).
	// La fila rescatada queda `pending`, con attempts++, sin claim y con el
	// last_error literal «claim vencido: el worker que reclamó la entrega no la
	// resolvió dentro del lease»; conserva su payload y su next_attempt_at, así que
	// vuelve a ser reclamable enseguida. El sello del worker que la tenía deja de
	// valer (ErrClaimLost). No toca filas pending, delivered ni dead, ni las
	// `delivering` de claim vigente.
	RecoverOrphanDeliveries(ctx context.Context, lease time.Duration) (int, error)

	// GetTenantIntegration lee la configuración de adaptadores del tenant. found
	// es false si la fila no existe (equivale a local/local — sin CRM).
	//
	// NUNCA devuelve el secreto, ni cifrado: solo HasSecret.
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
	//
	// Las cuatro columnas de configuración (los dos adaptadores, el endpoint y el
	// interruptor) se REEMPLAZAN siempre con lo que trae ti: un EndpointURL vacío
	// borra el endpoint. created_at es el del alta y no se pisa; updated_at se
	// refresca en CADA llamada, cambie algo o no. HasSecret, CreatedAt y UpdatedAt
	// de ti se ignoran. Solo toca la fila de ti.TenantID.
	UpsertTenantIntegration(ctx context.Context, ti TenantIntegration, secret string) error

	// DeleteTenantIntegration borra la fila del tenant (vuelve a local/local).
	//
	// Con la fila se va su secreto: es la única forma de retirarlo. Borrar lo que
	// no existe no es un error. No toca la cola: las entregas ya encoladas siguen
	// ahí (el worker las fallará por falta de destino).
	DeleteTenantIntegration(ctx context.Context, tenantID string) error
}
