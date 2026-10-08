//go:build pendiente

package integrations_test

// Las doce sentencias del adaptador, byte a byte (con su sangría): son las de
// internal/integrations/postgres.go y outbox_stats.go @ 36d5a04, extraídas de esos ficheros por
// un script y no a mano. El unitario del adaptador afirma que cada método emite la suya; lo que
// HACEN contra una base lo prueba la suite de contrato en test/procesos.

const (
	// enqueueSQL es la de EnqueueWebhook.
	enqueueSQL = `
		INSERT INTO public.webhook_outbox (tenant_id, kind, payload)
		VALUES ($1, $2, $3)
		RETURNING id
	`
	// claimSQL es la de ClaimWebhookBatch.
	claimSQL = `
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
	`
	// deliveredSQL es la de MarkWebhookDelivered.
	deliveredSQL = `
		UPDATE public.webhook_outbox
		SET status = $3, claimed_at = NULL, payload = '{}'::jsonb
		WHERE id = $1 AND claimed_at = $2 AND status = $4
	`
	// failedSQL es la de MarkWebhookFailed.
	failedSQL = `
		UPDATE public.webhook_outbox
		SET status = $3, attempts = attempts + 1, next_attempt_at = $4, last_error = $5, claimed_at = NULL
		WHERE id = $1 AND claimed_at = $2 AND status = $6
	`
	// deadSQL es la de MarkWebhookDead.
	deadSQL = `
		UPDATE public.webhook_outbox
		SET status = $3, attempts = attempts + 1, last_error = $4, claimed_at = NULL
		WHERE id = $1 AND claimed_at = $2 AND status = $5
	`
	// recoverSQL es la de RecoverOrphanDeliveries.
	recoverSQL = `
		UPDATE public.webhook_outbox
		SET status = $1, attempts = attempts + 1, last_error = $2, claimed_at = NULL
		WHERE status = $3
		  AND (claimed_at IS NULL OR claimed_at < now() - make_interval(secs => $4::double precision))
	`
	// getTenantSQL es la de GetTenantIntegration.
	getTenantSQL = `
		SELECT tenant_id, catalog_adapter, events_adapter, endpoint_url, secret_enc, enabled, created_at, updated_at
		FROM public.tenant_integrations
		WHERE tenant_id = $1
	`
	// getSecretSQL es la de GetTenantSecret (y SecretFingerprint, que lee por él).
	getSecretSQL = `
		SELECT secret_enc, secret_dek, secret_kek_id
		FROM public.tenant_integrations
		WHERE tenant_id = $1
	`
	// upsertKeepSecretSQL es la de UpsertTenantIntegration sin secreto: no nombra las columnas del sobre.
	upsertKeepSecretSQL = `
			INSERT INTO public.tenant_integrations (tenant_id, catalog_adapter, events_adapter, endpoint_url, enabled, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (tenant_id) DO UPDATE SET
				catalog_adapter = EXCLUDED.catalog_adapter,
				events_adapter  = EXCLUDED.events_adapter,
				endpoint_url    = EXCLUDED.endpoint_url,
				enabled         = EXCLUDED.enabled,
				updated_at      = now()
		`
	// upsertWithSecretSQL es la de UpsertTenantIntegration con secreto.
	upsertWithSecretSQL = `
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
	`
	// deleteTenantSQL es la de DeleteTenantIntegration.
	deleteTenantSQL = `
		DELETE FROM public.tenant_integrations WHERE tenant_id = $1
	`
	// countSQL es la de CountOutbox.
	countSQL = `
		SELECT
		    COUNT(*) FILTER (WHERE status = $2) AS pending,
		    COUNT(*) FILTER (WHERE status = $3) AS delivering,
		    COUNT(*) FILTER (WHERE status = $4) AS delivered,
		    COUNT(*) FILTER (WHERE status = $5) AS dead,
		    MIN(created_at) FILTER (WHERE status = $2) AS oldest_pending_at
		FROM public.webhook_outbox
		WHERE tenant_id = $1
	`
)
