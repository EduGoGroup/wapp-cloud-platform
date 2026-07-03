-- ============================================================
-- 0019: Bitácora de auditoría del IAM/admin (Plan 018 · T1, design.md §4, §7).
-- Tabla NUEVA audit_events: append-only de acciones sensibles (login, gestión de
-- usuarios/roles/api-keys, /admin/*), escrita por AuditMiddleware (T3/T4).
--
-- CERO PII (regla dura, INV-5): actor y resource son IDENTIDADES OPACAS (UUID de
-- user/client/recurso), NUNCA email, número/JID de contacto ni contenido de
-- mensajes. meta (JSONB) transporta contexto NO sensible (p.ej. endpoint,
-- método, código de resultado) — NUNCA payloads de negocio ni datos de contacto.
--
-- AISLAMIENTO: tenant_id es FK a tenants. Es NULLABLE a propósito: los eventos
-- PRE-AUTH (p.ej. login fallido cuando aún no se resolvió el tenant) se auditan
-- con tenant_id NULL. Los repos filtran por tenant_id cuando aplica (T2/T3).
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.audit_events (
    id        BIGSERIAL   PRIMARY KEY,
    tenant_id UUID        REFERENCES public.tenants(id) ON DELETE CASCADE,  -- NULL en eventos pre-auth
    actor     TEXT        NOT NULL,                -- id OPACO del user/client; NUNCA email/número
    action    TEXT        NOT NULL,                -- verbo/permiso: "auth.login", "iam.users.create", ...
    resource  TEXT        NOT NULL,                -- id OPACO del recurso afectado; NUNCA PII
    result    TEXT        NOT NULL,                -- "ok" | "error" | "allow" | "deny"
    meta      JSONB       NOT NULL DEFAULT '{}'::jsonb,  -- contexto NO sensible; NUNCA payload de negocio
    at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Índice de ESCANEO: consulta por tenant y orden temporal (GET /api/v1/audit).
CREATE INDEX IF NOT EXISTS audit_events_scan_idx
    ON public.audit_events (tenant_id, at);

COMMENT ON TABLE  public.audit_events IS 'Bitácora append-only de auditoría IAM/admin (Plan 018 §7). CERO PII (INV-5): actor/resource son ids OPACOS, meta contexto NO sensible; NUNCA email, número/JID ni contenido de mensajes. tenant_id NULLABLE para eventos pre-auth. NUNCA DEK/lease del Edge.';
COMMENT ON COLUMN public.audit_events.id        IS 'Identidad técnica de la fila (append-only, sin significado de negocio).';
COMMENT ON COLUMN public.audit_events.tenant_id IS 'Tenant del evento (FK a tenants); NULL en eventos pre-auth (p.ej. login fallido).';
COMMENT ON COLUMN public.audit_events.actor     IS 'Identidad OPACA de quien actúa (UUID de user/client). NUNCA email ni número.';
COMMENT ON COLUMN public.audit_events.action    IS 'Acción/permiso ejercido (p.ej. "auth.login", "iam.users.create").';
COMMENT ON COLUMN public.audit_events.resource  IS 'Identidad OPACA del recurso afectado. NUNCA PII ni contenido.';
COMMENT ON COLUMN public.audit_events.result    IS 'Desenlace: "ok" | "error" | "allow" | "deny".';
COMMENT ON COLUMN public.audit_events.meta      IS 'Contexto NO sensible del evento (JSONB): endpoint, método, código. NUNCA payload de negocio ni datos de contacto.';
COMMENT ON COLUMN public.audit_events.at        IS 'Momento del evento. Usa el DEFAULT now().';
