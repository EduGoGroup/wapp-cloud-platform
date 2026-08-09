package integrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que internal/intakes/postgres.go: SQL raw con placeholders $1..$n, sin ORM).
type Postgres struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
}

// NewPostgres construye el store con la conexión y el cifrador de campo que
// custodia el secreto HMAC (mismo KeyProvider de los planes 011/012 que ya usa
// internal/intakes para buyer_data — patrón replicado por la migración 0047).
func NewPostgres(db *sql.DB, cipher *crypto.FieldCipher) *Postgres {
	return &Postgres{db: db, cipher: cipher}
}

// EnqueueWebhook implementa Store.EnqueueWebhook: INSERT puro, nunca hace red.
func (p *Postgres) EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	var id int64
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO public.webhook_outbox (tenant_id, kind, payload)
		VALUES ($1, $2, $3)
		RETURNING id
	`, tenantID, kind, []byte(payload)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("integrations: encolar entrega de %s: %w", kind, err)
	}
	return id, nil
}

// orphanReason es el last_error que deja la recuperación por lease vencido. Es
// diagnóstico, no un error del puente: distingue "el CRM respondió 500" de "nadie
// resolvió esta entrega y hubo que rescatarla".
const orphanReason = "claim vencido: el worker que reclamó la entrega no la resolvió dentro del lease"

// ClaimWebhookBatch reclama hasta `limit` filas listas para entregar
// (status='pending', next_attempt_at vencido) en UNA SOLA sentencia atómica:
// el SELECT interno toma FOR UPDATE SKIP LOCKED —varias réplicas del worker
// pueden reclamar a la vez sin pisarse ni bloquearse— y el UPDATE que lo envuelve
// marca 'delivering' y SELLA el claim con claimed_at = now(). El RETURNING
// devuelve las filas ya con ese claimed_at: es el testigo que el worker tiene que
// presentar para cerrarlas (ver closeClaim).
//
// Antes esto eran dos sentencias dentro de una transacción explícita con su
// rollback a mano. Una sola sentencia es equivalente en garantías (Postgres la
// ejecuta atómicamente), no necesita gestionar la transacción, y sobre todo puede
// devolver el claimed_at RECIÉN escrito — que con el SELECT-luego-UPDATE habría
// que leer en una tercera consulta.
func (p *Postgres) ClaimWebhookBatch(ctx context.Context, limit int) ([]WebhookOutbox, error) {
	rows, err := p.db.QueryContext(ctx, `
		UPDATE public.webhook_outbox AS o
		SET status = $1, claimed_at = now()
		FROM (
			SELECT id
			FROM public.webhook_outbox
			WHERE status = $2 AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		) AS c
		WHERE o.id = c.id
		RETURNING o.id, o.tenant_id, o.kind, o.payload, o.status, o.attempts,
		          o.next_attempt_at, o.created_at, COALESCE(o.last_error, ''), o.claimed_at
	`, StatusDelivering, StatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("integrations: reclamar lote: %w", err)
	}
	return scanWebhookRows(rows)
}

// scanWebhookRows agota y cierra rows, devolviendo las filas escaneadas. Extraído
// de ClaimWebhookBatch para mantener su complejidad ciclomática razonable.
func scanWebhookRows(rows *sql.Rows) ([]WebhookOutbox, error) {
	var claimed []WebhookOutbox
	for rows.Next() {
		var (
			w         WebhookOutbox
			claimedAt sql.NullTime
		)
		if serr := rows.Scan(&w.ID, &w.TenantID, &w.Kind, &w.Payload, &w.Status, &w.Attempts,
			&w.NextAttemptAt, &w.CreatedAt, &w.LastError, &claimedAt); serr != nil {
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[wapp][integrations][WARN] claim: cerrar filas tras error de escaneo: %v", cerr)
			}
			return nil, fmt.Errorf("integrations: escanear fila del lote: %w", serr)
		}
		w.ClaimedAt = claimedAt.Time // NULL ⇒ cero (no hay claim vigente)
		claimed = append(claimed, w)
	}
	rerr := rows.Err()
	if cerr := rows.Close(); cerr != nil && rerr == nil {
		rerr = cerr
	}
	if rerr != nil {
		return nil, fmt.Errorf("integrations: iterar lote: %w", rerr)
	}
	return claimed, nil
}

// closeClaim ejecuta una transición que CIERRA el claim vigente de una entrega.
// Centraliza la valla optimista de las tres: la sentencia siempre lleva
// `WHERE id = $1 AND claimed_at = $2` con el testigo que devolvió el claim, así
// que si el lease venció y otro worker reclamó la fila mientras tanto, el UPDATE
// afecta 0 filas y esto devuelve ErrClaimLost en vez de pisar el resultado ajeno.
//
// Los argumentos propios de cada transición van a partir de $3.
func (p *Postgres) closeClaim(ctx context.Context, claim WebhookOutbox, what, query string, args ...any) error {
	full := append([]any{claim.ID, claim.ClaimedAt}, args...)
	res, err := p.db.ExecContext(ctx, query, full...)
	if err != nil {
		return fmt.Errorf("integrations: marcar entrega %d %s: %w", claim.ID, what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("integrations: filas afectadas al marcar la entrega %d %s: %w", claim.ID, what, err)
	}
	if n == 0 {
		return fmt.Errorf("integrations: entrega %d hacia %s: %w", claim.ID, what, ErrClaimLost)
	}
	return nil
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
// Esto NO prejuzga la retención por antigüedad (borrar filas viejas), que queda
// reservada al Plan 046: aquí no se borra ninguna fila ni se les pone TTL.
func (p *Postgres) MarkWebhookDelivered(ctx context.Context, claim WebhookOutbox) error {
	return p.closeClaim(ctx, claim, "delivered", `
		UPDATE public.webhook_outbox
		SET status = $3, claimed_at = NULL, payload = '{}'::jsonb
		WHERE id = $1 AND claimed_at = $2 AND status = $4
	`, StatusDelivered, StatusDelivering)
}

// MarkWebhookFailed implementa Store.MarkWebhookFailed.
func (p *Postgres) MarkWebhookFailed(ctx context.Context, claim WebhookOutbox, nextAttemptAt time.Time, lastErr string) error {
	return p.closeClaim(ctx, claim, "reintento", `
		UPDATE public.webhook_outbox
		SET status = $3, attempts = attempts + 1, next_attempt_at = $4, last_error = $5, claimed_at = NULL
		WHERE id = $1 AND claimed_at = $2 AND status = $6
	`, StatusPending, nextAttemptAt, lastErr, StatusDelivering)
}

// MarkWebhookDead implementa Store.MarkWebhookDead.
func (p *Postgres) MarkWebhookDead(ctx context.Context, claim WebhookOutbox, lastErr string) error {
	return p.closeClaim(ctx, claim, "dead", `
		UPDATE public.webhook_outbox
		SET status = $3, attempts = attempts + 1, last_error = $4, claimed_at = NULL
		WHERE id = $1 AND claimed_at = $2 AND status = $5
	`, StatusDead, lastErr, StatusDelivering)
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
func (p *Postgres) RecoverOrphanDeliveries(ctx context.Context, lease time.Duration) (int, error) {
	res, err := p.db.ExecContext(ctx, `
		UPDATE public.webhook_outbox
		SET status = $1, attempts = attempts + 1, last_error = $2, claimed_at = NULL
		WHERE status = $3
		  AND (claimed_at IS NULL OR claimed_at < now() - make_interval(secs => $4::double precision))
	`, StatusPending, orphanReason, StatusDelivering, lease.Seconds())
	if err != nil {
		return 0, fmt.Errorf("integrations: recuperar entregas huérfanas: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("integrations: contar entregas recuperadas: %w", err)
	}
	return int(n), nil
}

// GetTenantIntegration implementa Store.GetTenantIntegration.
func (p *Postgres) GetTenantIntegration(ctx context.Context, tenantID string) (TenantIntegration, bool, error) {
	var (
		ti          TenantIntegration
		endpointURL sql.NullString
		secretEnc   []byte
	)
	err := p.db.QueryRowContext(ctx, `
		SELECT tenant_id, catalog_adapter, events_adapter, endpoint_url, secret_enc, enabled, created_at, updated_at
		FROM public.tenant_integrations
		WHERE tenant_id = $1
	`, tenantID).Scan(&ti.TenantID, &ti.CatalogAdapter, &ti.EventsAdapter, &endpointURL, &secretEnc, &ti.Enabled, &ti.CreatedAt, &ti.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantIntegration{}, false, nil
	}
	if err != nil {
		return TenantIntegration{}, false, fmt.Errorf("integrations: leer integración de %s: %w", tenantID, err)
	}
	ti.EndpointURL = endpointURL.String
	ti.HasSecret = secretEnc != nil
	return ti, true, nil
}

// GetTenantSecret descifra con la KEK QUE ENVOLVIÓ ESTA FILA (secret_kek_id), no
// la current: tras una rotación parcial del Plan 012 coexisten filas envueltas
// por distintas KEK, igual que intake_buyer_data (buyerdata.go).
func (p *Postgres) GetTenantSecret(ctx context.Context, tenantID string) (string, bool, error) {
	var enc, dek []byte
	var kekID sql.NullString
	err := p.db.QueryRowContext(ctx, `
		SELECT secret_enc, secret_dek, secret_kek_id
		FROM public.tenant_integrations
		WHERE tenant_id = $1
	`, tenantID).Scan(&enc, &dek, &kekID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("integrations: leer secreto de %s: %w", tenantID, err)
	}
	if enc == nil || dek == nil || !kekID.Valid {
		return "", false, nil
	}
	plain, err := p.cipher.Decrypt(enc, dek, kekID.String)
	if err != nil {
		return "", false, fmt.Errorf("integrations: descifrar secreto de %s: %w", tenantID, err)
	}
	return plain, true, nil
}

// UpsertTenantIntegration crea o actualiza la fila. secret == "" preserva el
// secreto existente (permite reconfigurar endpoint/adapters sin reenviarlo); un
// secret no vacío lo cifra y reemplaza las tres columnas del envelope (patrón de
// PostgresBuyerData.Put, buyerdata.go — la migración 0047 documenta por qué son
// tres columnas y no la secret_ciphertext BYTEA única que dibujaba el design).
func (p *Postgres) UpsertTenantIntegration(ctx context.Context, ti TenantIntegration, secret string) error {
	var endpointURL sql.NullString
	if ti.EndpointURL != "" {
		endpointURL = sql.NullString{String: ti.EndpointURL, Valid: true}
	}

	if secret == "" {
		_, err := p.db.ExecContext(ctx, `
			INSERT INTO public.tenant_integrations (tenant_id, catalog_adapter, events_adapter, endpoint_url, enabled, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (tenant_id) DO UPDATE SET
				catalog_adapter = EXCLUDED.catalog_adapter,
				events_adapter  = EXCLUDED.events_adapter,
				endpoint_url    = EXCLUDED.endpoint_url,
				enabled         = EXCLUDED.enabled,
				updated_at      = now()
		`, ti.TenantID, ti.CatalogAdapter, ti.EventsAdapter, endpointURL, ti.Enabled)
		if err != nil {
			return fmt.Errorf("integrations: upsert de %s (sin tocar el secreto): %w", ti.TenantID, err)
		}
		return nil
	}

	enc, dek, kekID, err := p.cipher.Encrypt(secret)
	if err != nil {
		return fmt.Errorf("integrations: cifrar el secreto de %s: %w", ti.TenantID, err)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO public.tenant_integrations (tenant_id, catalog_adapter, events_adapter, endpoint_url, secret_enc, secret_dek, secret_kek_id, enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			catalog_adapter = EXCLUDED.catalog_adapter,
			events_adapter  = EXCLUDED.events_adapter,
			endpoint_url    = EXCLUDED.endpoint_url,
			secret_enc      = EXCLUDED.secret_enc,
			secret_dek      = EXCLUDED.secret_dek,
			secret_kek_id   = EXCLUDED.secret_kek_id,
			enabled         = EXCLUDED.enabled,
			updated_at      = now()
	`, ti.TenantID, ti.CatalogAdapter, ti.EventsAdapter, endpointURL, enc, dek, kekID, ti.Enabled)
	if err != nil {
		return fmt.Errorf("integrations: upsert de %s: %w", ti.TenantID, err)
	}
	return nil
}

// DeleteTenantIntegration implementa Store.DeleteTenantIntegration.
func (p *Postgres) DeleteTenantIntegration(ctx context.Context, tenantID string) error {
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM public.tenant_integrations WHERE tenant_id = $1
	`, tenantID); err != nil {
		return fmt.Errorf("integrations: borrar integración de %s: %w", tenantID, err)
	}
	return nil
}
