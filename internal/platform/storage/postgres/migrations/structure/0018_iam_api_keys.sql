-- ============================================================
-- 0018: API keys de terceros (M2M) del IAM (Plan 018 · T1, design.md §4, §8).
-- Tabla NUEVA iam_api_keys: credencial de la API pública (:8103) por
-- client_id + secreto. SOLO se guarda el HASH (SHA256) de la key, NUNCA la key en
-- claro (se devuelve UNA vez al emitirla, §8). scopes[] gobierna qué puede hacer
-- (messages.send, flows.*, media.upload, ...). El middleware Authenticate hace
-- lookup por key_hash e inyecta {tenant_id, client_id, scopes} en el contexto.
--
-- CERO PII / CERO llaves: key_hash es un digest opaco; NUNCA la doble llave del
-- Edge. client_id es un id operativo del tenant, no un contacto.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.iam_api_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    client_id    TEXT        NOT NULL,             -- identificador público de la credencial
    key_hash     TEXT        NOT NULL,             -- SHA256 del secreto; NUNCA la key en claro
    scopes       TEXT[]      NOT NULL DEFAULT '{}',-- permisos M2M (glob recurso.accion)
    is_active    BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,                      -- telemetría de uso (nullable)
    expires_at   TIMESTAMPTZ,                      -- vencimiento opcional (NULL = sin caducidad)
    revoked_at   TIMESTAMPTZ                       -- NULL = vigente; set = revocada
);

-- client_id único (identidad pública de la credencial) y key_hash único (lookup).
CREATE UNIQUE INDEX IF NOT EXISTS iam_api_keys_client_uidx
    ON public.iam_api_keys (client_id);
CREATE UNIQUE INDEX IF NOT EXISTS iam_api_keys_hash_uidx
    ON public.iam_api_keys (key_hash);
-- Escaneo por tenant (listado/gestión).
CREATE INDEX IF NOT EXISTS iam_api_keys_tenant_idx
    ON public.iam_api_keys (tenant_id);

COMMENT ON TABLE  public.iam_api_keys IS 'API keys M2M de la API pública (Plan 018 §8). Solo el HASH SHA256 de la key (NUNCA en claro); scopes[] gobierna permisos. tenant_id FK a tenants (aislamiento). CERO PII; NUNCA DEK/lease del Edge.';
COMMENT ON COLUMN public.iam_api_keys.id           IS 'Identidad de la fila (UUID).';
COMMENT ON COLUMN public.iam_api_keys.tenant_id    IS 'Tenant dueño de la credencial (FK a tenants).';
COMMENT ON COLUMN public.iam_api_keys.client_id    IS 'Identificador PÚBLICO de la credencial (único). Dato operativo, no PII.';
COMMENT ON COLUMN public.iam_api_keys.key_hash     IS 'SHA256 del secreto de la key. NUNCA la key en claro (se devuelve una vez al emitir).';
COMMENT ON COLUMN public.iam_api_keys.scopes       IS 'Permisos M2M (patrones glob recurso.accion) que la key puede ejercer.';
COMMENT ON COLUMN public.iam_api_keys.is_active    IS 'Credencial habilitada; false la bloquea sin borrarla.';
COMMENT ON COLUMN public.iam_api_keys.created_at   IS 'Momento del alta. Usa el DEFAULT now().';
COMMENT ON COLUMN public.iam_api_keys.last_used_at IS 'Último uso observado (telemetría); NULL si nunca se usó.';
COMMENT ON COLUMN public.iam_api_keys.expires_at   IS 'Vencimiento opcional; NULL = sin caducidad.';
COMMENT ON COLUMN public.iam_api_keys.revoked_at   IS 'Instante de revocación; NULL = vigente.';
