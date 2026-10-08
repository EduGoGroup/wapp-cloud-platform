// Porta internal/integrations/postgres.go @ 36d5a04, y recoge los dos métodos de
// *Postgres que el viejo tenía fuera: SecretFingerprint (crud.go:42) y CountOutbox
// (outbox_stats.go:69) — D-F6-6.

package integrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que intakes.Postgres: SQL raw con placeholders $1..$n, sin ORM), sobre
// public.webhook_outbox (0046, 0049, 0050) y public.tenant_integrations (0047).
//
// Las reglas del puerto las fija la suite integrationshelpertest.Contrato, que
// corre contra él en los procesos de F9. Su test de fichero afirma, con un driver
// de mentira, lo que se ve sin base: el SQL que emite y sus argumentos, el mapeo
// de filas y el de errores.
//
// Cada método es UNA sentencia sobre el pool, sin transacción explícita. Todos
// los errores salen envueltos con %w.
type Postgres struct{}

// NewPostgres construye el store con la conexión y el cifrador de campo que
// custodia el secreto HMAC (mismo KeyProvider de los planes 011/012 que ya usa
// intakes para buyer_data — patrón replicado por la migración 0047). No consulta
// la base ni cifra nada al construirse.
//
// 🔴 Homónimo: la «DEK» de las columnas secret_dek es la del envelope de dato de
// negocio (crypto.FieldCipher), NO la DEK del ADR-0007 que custodia el cliente.
func NewPostgres(db *sql.DB, cipher *crypto.FieldCipher) *Postgres {
	panic(pendiente.Implementar("integrations.NewPostgres"))
}

var _ Store = (*Postgres)(nil)

// EnqueueWebhook implementa Store.EnqueueWebhook: INSERT puro, nunca hace red. El
// payload viaja como []byte; el resto de columnas (status, attempts,
// next_attempt_at, created_at) las pone el DEFAULT de la tabla.
//
// Error: «integrations: encolar entrega de <kind>: » (y el id devuelto es 0).
func (p *Postgres) EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	panic(pendiente.Implementar("integrations.Postgres.EnqueueWebhook"))
}

// ClaimWebhookBatch reclama hasta `limit` filas listas para entregar
// (status='pending', next_attempt_at vencido) en UNA SOLA sentencia atómica:
// el SELECT interno toma FOR UPDATE SKIP LOCKED —varias réplicas del worker
// pueden reclamar a la vez sin pisarse ni bloquearse— y el UPDATE que lo envuelve
// marca 'delivering' y SELLA el claim con claimed_at = now(). El RETURNING
// devuelve las filas ya con ese claimed_at: es el testigo que el worker tiene que
// presentar para cerrarlas.
//
// Antes esto eran dos sentencias dentro de una transacción explícita con su
// rollback a mano. Una sola sentencia es equivalente en garantías (Postgres la
// ejecuta atómicamente), no necesita gestionar la transacción, y sobre todo puede
// devolver el claimed_at RECIÉN escrito — que con el SELECT-luego-UPDATE habría
// que leer en una tercera consulta.
//
// Mapeo de filas: last_error NULL llega como "" y claimed_at NULL como el instante
// cero. Sin filas devuelve un slice vacío (nil) y ningún error. Las filas salen en
// el orden en que las da la base: el adaptador no reordena.
//
// Errores (con cualquiera de ellos el slice devuelto es nil, sin filas a medias):
//
//   - «integrations: reclamar lote: » — la sentencia falla;
//   - «integrations: escanear fila del lote: » — una fila no se puede escanear;
//   - «integrations: iterar lote: » — falla el recorrido de las filas o, si lo
//     demás fue bien, su cierre (un error del recorrido no se pisa con el del cierre).
func (p *Postgres) ClaimWebhookBatch(ctx context.Context, limit int) ([]WebhookOutbox, error) {
	panic(pendiente.Implementar("integrations.Postgres.ClaimWebhookBatch"))
}

// MarkWebhookDelivered implementa Store.MarkWebhookDelivered y, en la MISMA
// sentencia, VACÍA el payload (política elegida el 2026-08-08).
//
// El motivo es que una fila entregada conserva para siempre una copia en claro de
// lo que se entregó, y nadie la vuelve a leer: el puente ya la tiene. La fila
// SOBREVIVE como recibo —id, tenant, kind, created_at, attempts, status— así que
// la trazabilidad no se toca; lo que desaparece es el duplicado del contenido.
// Cero residuo desde el minuto uno, sin esperar a una política de retención.
//
// Se vacía AQUÍ y no en un barrido posterior porque es el único instante en que la
// copia deja de tener uso, y hacerlo en la misma sentencia que cierra el claim
// significa que no existe ventana en la que la fila esté `delivered` con payload:
// un barrido por antigüedad, en cambio, siempre deja una.
//
// `'{}'::jsonb` y no NULL: la columna es NOT NULL (0046) y relajarla obligaría a
// que todo lector distinguiera tres casos (contenido / vacío / nulo) donde solo hay
// dos. Un objeto JSON vacío se lee y se decodifica igual que cualquier payload.
//
// Esto NO prejuzga la retención por antigüedad (borrar filas viejas). 🔴 Y esa
// retención NO va a existir: el Plan 046 la descartó el 2026-08-20 (D-046.16,
// ADR-0043). Aquí no se borra ninguna fila ni se les pone TTL, y en ningún otro
// sitio tampoco.
//
// La valla optimista, común a las tres transiciones que cierran un claim: la
// sentencia lleva siempre `WHERE id = $1 AND claimed_at = $2 AND status =
// 'delivering'`, con el id y el ClaimedAt de `claim` como $1 y $2 y los
// argumentos propios de la transición a partir de $3. Si el lease venció y otro
// worker reclamó la fila mientras tanto, el UPDATE afecta 0 filas y se devuelve
// ErrClaimLost en vez de pisar el resultado ajeno.
//
// Errores de las tres, con <qué> = «delivered» aquí, «reintento» en
// MarkWebhookFailed y «dead» en MarkWebhookDead:
//
//   - «integrations: marcar entrega <id> <qué>: » — la sentencia falla;
//   - «integrations: filas afectadas al marcar la entrega <id> <qué>: » — el
//     driver no sabe decir cuántas filas tocó;
//   - «integrations: entrega <id> hacia <qué>: » + ErrClaimLost — 0 filas afectadas.
func (p *Postgres) MarkWebhookDelivered(ctx context.Context, claim WebhookOutbox) error {
	panic(pendiente.Implementar("integrations.Postgres.MarkWebhookDelivered"))
}

// MarkWebhookFailed implementa Store.MarkWebhookFailed: una sentencia con la
// valla de MarkWebhookDelivered que deja la fila `pending` con attempts + 1,
// next_attempt_at = nextAttemptAt, last_error = lastErr y sin claim. Sus errores
// son los de MarkWebhookDelivered con <qué> = «reintento».
func (p *Postgres) MarkWebhookFailed(ctx context.Context, claim WebhookOutbox, nextAttemptAt time.Time, lastErr string) error {
	panic(pendiente.Implementar("integrations.Postgres.MarkWebhookFailed"))
}

// MarkWebhookDead implementa Store.MarkWebhookDead: una sentencia con la valla de
// MarkWebhookDelivered que deja la fila `dead` con attempts + 1, last_error =
// lastErr y sin claim; no toca el payload. Sus errores son los de
// MarkWebhookDelivered con <qué> = «dead».
func (p *Postgres) MarkWebhookDead(ctx context.Context, claim WebhookOutbox, lastErr string) error {
	panic(pendiente.Implementar("integrations.Postgres.MarkWebhookDead"))
}

// RecoverOrphanDeliveries devuelve a pending las entregas cuyo CLAIM VENCIÓ, y
// SOLO esas (Plan 042 · Ola 3.1).
//
// La versión de la Ola 3 preguntaba por `next_attempt_at <= now()`, que para una
// fila en vuelo es SIEMPRE cierto —el claim no lo tocaba, y ser reclamable exigía
// que ya estuviera vencido—, así que revertía también las entregas vivas. Con una
// sola instancia no se veía; con dos (rolling deploy, réplica nueva) el arranque
// de una devolvía a pending lo que la otra estaba entregando, y la misma solicitud
// salía dos veces hacia el CRM.
//
// Ahora el discriminante es `claimed_at`: solo se recupera lo que lleva más de un
// `lease` reclamado. `claimed_at IS NULL` con status 'delivering' se trata como
// huérfana inmediata porque esa combinación solo la deja el código anterior a la
// migración 0049 — un worker de esta versión siempre sella el claim.
//
// attempts++ porque un claim vencido FUE un intento: sin contarlo, una entrega que
// tumba a su worker de forma reproducible giraría para siempre sin llegar nunca a
// `dead`, que es justo lo que MaxAttempts existe para impedir.
//
// El lease viaja en SEGUNDOS (lease.Seconds(), un float64) y el last_error que
// deja es el literal «claim vencido: el worker que reclamó la entrega no la
// resolvió dentro del lease»: es diagnóstico, no un error del puente — distingue
// "el CRM respondió 500" de "nadie resolvió esta entrega y hubo que rescatarla".
// Devuelve las filas afectadas.
//
// Errores (con 0 como cuenta):
//
//   - «integrations: recuperar entregas huérfanas: » — la sentencia falla;
//   - «integrations: contar entregas recuperadas: » — el driver no sabe decir
//     cuántas filas tocó.
func (p *Postgres) RecoverOrphanDeliveries(ctx context.Context, lease time.Duration) (int, error) {
	panic(pendiente.Implementar("integrations.Postgres.RecoverOrphanDeliveries"))
}

// GetTenantIntegration implementa Store.GetTenantIntegration. Sin fila devuelve
// (TenantIntegration{}, false, nil). endpoint_url NULL llega como "" y HasSecret
// dice si secret_enc NO es NULL; el blob cifrado se lee para saberlo, pero no
// sale del método.
//
// Error: «integrations: leer integración de <tenant>: » (con el valor cero y false).
func (p *Postgres) GetTenantIntegration(ctx context.Context, tenantID string) (TenantIntegration, bool, error) {
	panic(pendiente.Implementar("integrations.Postgres.GetTenantIntegration"))
}

// GetTenantSecret descifra con la KEK QUE ENVOLVIÓ ESTA FILA (secret_kek_id), no
// la current: tras una rotación parcial del Plan 012 coexisten filas envueltas
// por distintas KEK, igual que intake_buyer_data.
//
// Devuelve ("", false, nil) si el tenant no tiene fila o si CUALQUIERA de las tres
// columnas del envelope (secret_enc, secret_dek, secret_kek_id) es NULL: un sobre
// a medias no es un secreto y no se intenta descifrar.
//
// Errores (con "" y false):
//
//   - «integrations: leer secreto de <tenant>: » — la consulta falla;
//   - «integrations: descifrar secreto de <tenant>: » — el sobre no abre.
//
// Ningún error cita el secreto.
func (p *Postgres) GetTenantSecret(ctx context.Context, tenantID string) (string, bool, error) {
	panic(pendiente.Implementar("integrations.Postgres.GetTenantSecret"))
}

// UpsertTenantIntegration crea o actualiza la fila. secret == "" preserva el
// secreto existente (permite reconfigurar endpoint/adapters sin reenviarlo); un
// secret no vacío lo cifra y reemplaza las tres columnas del envelope (patrón de
// PostgresBuyerData.Put — la migración 0047 documenta por qué son tres columnas y
// no la secret_ciphertext BYTEA única que dibujaba el design).
//
// Son DOS sentencias distintas, y se emite una u otra: sin secreto, un upsert que
// NO nombra las columnas del envelope (por eso las conserva) y no toca el
// cifrador; con secreto, otro que además las escribe con lo que devuelve
// cipher.Encrypt. En las dos, un EndpointURL vacío viaja como NULL, y updated_at
// se pone a now() tanto en el alta como en la actualización; created_at no se
// nombra (DEFAULT en el alta, intacto después). El secreto en claro NUNCA viaja a
// la base.
//
// Errores:
//
//   - «integrations: upsert de <tenant> (sin tocar el secreto): » — falla la
//     sentencia sin secreto;
//   - «integrations: cifrar el secreto de <tenant>: » — falla el cifrador (y no
//     se emite ninguna sentencia);
//   - «integrations: upsert de <tenant>: » — falla la sentencia con secreto.
func (p *Postgres) UpsertTenantIntegration(ctx context.Context, ti TenantIntegration, secret string) error {
	panic(pendiente.Implementar("integrations.Postgres.UpsertTenantIntegration"))
}

// DeleteTenantIntegration implementa Store.DeleteTenantIntegration: un DELETE
// por tenant_id. Que no borre ninguna fila no es un error.
//
// Error: «integrations: borrar integración de <tenant>: ».
func (p *Postgres) DeleteTenantIntegration(ctx context.Context, tenantID string) error {
	panic(pendiente.Implementar("integrations.Postgres.DeleteTenantIntegration"))
}

// SecretFingerprint descifra el secreto del tenant y devuelve SOLO su huella
// (Fingerprint). found es false si el tenant no tiene fila o no tiene secreto, y
// entonces la huella es "".
//
// Vive aquí y no en el handler A PROPÓSITO: así el secreto EN CLARO no cruza la
// frontera de este paquete. La capa HTTP —la que serializa respuestas y escribe
// logs, o sea la que puede filtrarlo— recibe ocho caracteres hex y nunca tiene el
// valor en una variable que pueda acabar en un %v.
//
// No es del puerto Store (lo usa solo la superficie HTTP del CRUD). Lee por
// GetTenantSecret: sus errores son los de ese método, tal cual, con ("", false).
func (p *Postgres) SecretFingerprint(ctx context.Context, tenantID string) (string, bool, error) {
	panic(pendiente.Implementar("integrations.Postgres.SecretFingerprint"))
}

// CountOutbox devuelve el estado agregado de la cola del tenant.
//
// UNA sola consulta con agregados condicionales, no cinco: los cuatro contadores
// y la antigüedad salen del MISMO recorrido de las filas del tenant, así que la
// foto es coherente por construcción. Con cinco consultas los números podrían
// venir de instantes distintos y sumar mal —el worker mueve filas entre estados
// mientras se pregunta— y la pantalla enseñaría una cola que nunca existió.
//
// El filtro por tenant_id lo sirve webhook_outbox_tenant_idx (0046). El escaneo
// crece con las `delivered` acumuladas, y 🔴 NO HAY RETENCIÓN POR ANTIGÜEDAD EN
// CAMINO: el Plan 046 la descartó el 2026-08-20 (D-046.16, ADR-0043). Queda como
// deuda de RENDIMIENTO, sin dueño y sin fecha — no de privacidad: las `delivered`
// ya vacían su payload desde la 0050, así que lo que se acumula son filas de
// metadatos, no contenido. A las escalas de hoy no es un problema (la tabla tiene
// UNA fila) y cuando lo sea lo arregla una purga que habrá que escribir, no un
// índice más.
//
// SIN ErrNoRows POSIBLE: un agregado sin GROUP BY devuelve SIEMPRE una fila, con
// ceros si el tenant no tiene ninguna entrega. Por eso «este tenant nunca encoló
// nada» y «este tenant tiene la cola vacía» responden lo mismo, que es lo
// correcto: las dos cosas significan que no hay nada esperando.
//
// Los argumentos son el tenant y los cuatro estados (StatusPending,
// StatusDelivering, StatusDelivered, StatusDead, en ese orden). OldestPendingAt es
// el created_at mínimo de las `pending`; si no hay ninguna (NULL) llega como el
// instante cero. No es del puerto Store.
//
// Error: «integrations: contar la cola de entregas: » (con OutboxCounts{}).
func (p *Postgres) CountOutbox(ctx context.Context, tenantID string) (OutboxCounts, error) {
	panic(pendiente.Implementar("integrations.Postgres.CountOutbox"))
}
