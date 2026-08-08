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

// ClaimWebhookBatch reclama hasta `limit` filas listas para reintentar
// (status='pending', next_attempt_at vencido) con FOR UPDATE SKIP LOCKED —
// múltiples réplicas del worker pueden llamarse a la vez sin pisarse ni
// bloquearse entre sí — y las marca 'delivering' en la MISMA transacción antes
// de devolverlas: si el proceso muere entre el claim y el POST,
// RecoverOrphanDeliveries las recupera al rearrancar (D-042.4).
func (p *Postgres) ClaimWebhookBatch(ctx context.Context, limit int) (out []WebhookOutbox, err error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("integrations: iniciar transacción del claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rerr := tx.Rollback(); rerr != nil {
				log.Printf("[wapp][integrations][WARN] claim: rollback: %v", rerr)
			}
		}
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, kind, payload, status, attempts, next_attempt_at, created_at, COALESCE(last_error, '')
		FROM public.webhook_outbox
		WHERE status = $1 AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, StatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("integrations: reclamar lote: %w", err)
	}
	claimed, err := scanWebhookRows(rows)
	if err != nil {
		return nil, err
	}
	if len(claimed) == 0 {
		if cerr := tx.Commit(); cerr != nil {
			return nil, fmt.Errorf("integrations: confirmar claim vacío: %w", cerr)
		}
		committed = true
		return nil, nil
	}

	ids := make([]int64, len(claimed))
	for i, w := range claimed {
		ids[i] = w.ID
	}
	// pgx (driver real bajo database/sql en este repo) codifica un []int64 Go
	// como un array de Postgres de forma nativa: no hace falta un wrapper tipo
	// pq.Array (eso es lib/pq, no el driver de aquí).
	if _, err := tx.ExecContext(ctx, `
		UPDATE public.webhook_outbox SET status = $1 WHERE id = ANY($2)
	`, StatusDelivering, ids); err != nil {
		return nil, fmt.Errorf("integrations: marcar lote delivering: %w", err)
	}
	for i := range claimed {
		claimed[i].Status = StatusDelivering
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("integrations: confirmar claim: %w", err)
	}
	committed = true
	return claimed, nil
}

// scanWebhookRows agota y cierra rows, devolviendo las filas escaneadas. Extraído
// de ClaimWebhookBatch para mantener su complejidad ciclomática razonable.
func scanWebhookRows(rows *sql.Rows) ([]WebhookOutbox, error) {
	var claimed []WebhookOutbox
	for rows.Next() {
		var w WebhookOutbox
		if serr := rows.Scan(&w.ID, &w.TenantID, &w.Kind, &w.Payload, &w.Status, &w.Attempts, &w.NextAttemptAt, &w.CreatedAt, &w.LastError); serr != nil {
			if cerr := rows.Close(); cerr != nil {
				log.Printf("[wapp][integrations][WARN] claim: cerrar filas tras error de escaneo: %v", cerr)
			}
			return nil, fmt.Errorf("integrations: escanear fila del lote: %w", serr)
		}
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

// MarkWebhookDelivered implementa Store.MarkWebhookDelivered.
func (p *Postgres) MarkWebhookDelivered(ctx context.Context, id int64) error {
	if _, err := p.db.ExecContext(ctx, `
		UPDATE public.webhook_outbox SET status = $1 WHERE id = $2
	`, StatusDelivered, id); err != nil {
		return fmt.Errorf("integrations: marcar entrega %d delivered: %w", id, err)
	}
	return nil
}

// MarkWebhookFailed implementa Store.MarkWebhookFailed.
func (p *Postgres) MarkWebhookFailed(ctx context.Context, id int64, nextAttemptAt time.Time, lastErr string) error {
	if _, err := p.db.ExecContext(ctx, `
		UPDATE public.webhook_outbox
		SET status = $1, attempts = attempts + 1, next_attempt_at = $2, last_error = $3
		WHERE id = $4
	`, StatusPending, nextAttemptAt, lastErr, id); err != nil {
		return fmt.Errorf("integrations: marcar entrega %d failed: %w", id, err)
	}
	return nil
}

// MarkWebhookDead implementa Store.MarkWebhookDead.
func (p *Postgres) MarkWebhookDead(ctx context.Context, id int64, lastErr string) error {
	if _, err := p.db.ExecContext(ctx, `
		UPDATE public.webhook_outbox
		SET status = $1, attempts = attempts + 1, last_error = $2
		WHERE id = $3
	`, StatusDead, lastErr, id); err != nil {
		return fmt.Errorf("integrations: marcar entrega %d dead: %w", id, err)
	}
	return nil
}

// RecoverOrphanDeliveries vuelve a pending toda fila 'delivering' cuyo
// next_attempt_at ya venció: al ENCOLAR una entrega next_attempt_at queda en
// "ya" (DEFAULT now()), y ClaimWebhookBatch no lo toca al marcar delivering — es
// la misma marca de tiempo la que delata a un huérfano si nadie la resolvió a
// tiempo. Se llama una sola vez, al arrancar el worker (D-042.4).
func (p *Postgres) RecoverOrphanDeliveries(ctx context.Context) (int, error) {
	res, err := p.db.ExecContext(ctx, `
		UPDATE public.webhook_outbox
		SET status = $1
		WHERE status = $2 AND next_attempt_at <= now()
	`, StatusPending, StatusDelivering)
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
