-- ============================================================
-- 0014: Usuarios del IAM (Plan 018 · T1, design.md §4, ADR-0004 / ADR-0009).
-- Tabla NUEVA iam_users: operadores del tenant que se autentican contra la
-- Plataforma Cloud (login JWT, §8). Copia-adaptación del schema de
-- edugo-api-identity al modelo MULTI-TENANT PLANO de wApp (Decisión C): solo
-- {tenant_id, user_id, roles, grants}, sin jerarquía escuela/unidad/ward.
--
-- PII (design.md §4): email es del OPERADOR del tenant, NO un contacto de
-- WhatsApp -> se guarda EN CLARO (permite el login por email). Los contactos
-- siguen cifrados por Plan 011 (INV-5). password_hash es bcrypt (cost 12,
-- wapp-shared/auth) — NUNCA la contraseña en claro. CERO llaves criptográficas.
--
-- AISLAMIENTO: tenant_id es FK a tenants (0001_tenants.sql); los repos filtran
-- por él (aplicación-level, T2). UNIQUE (tenant_id, email): un email por tenant.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE/INDEX IF NOT EXISTS
-- garantizan re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.iam_users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    email         TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,          -- bcrypt (cost 12); NUNCA contraseña en claro
    is_active     BOOLEAN     NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ                    -- soft-delete; NULL = activo
);

-- Un email por tenant (identidad de login). El login resuelve (tenant, email).
CREATE UNIQUE INDEX IF NOT EXISTS iam_users_tenant_email_uidx
    ON public.iam_users (tenant_id, email);

COMMENT ON TABLE  public.iam_users IS 'Operadores del tenant que se autentican contra la Plataforma Cloud (IAM, Plan 018). email/ password_hash son datos OPERATIVOS del tenant (ADR-0009): email en claro (no es contacto WhatsApp), password_hash bcrypt. NUNCA DEK, store cifrado ni llaves.';
COMMENT ON COLUMN public.iam_users.id            IS 'Identidad del usuario (UUID).';
COMMENT ON COLUMN public.iam_users.tenant_id     IS 'Tenant dueño del usuario (FK a tenants; aislamiento multi-tenant plano, Decisión C).';
COMMENT ON COLUMN public.iam_users.email         IS 'Email del OPERADOR del tenant, EN CLARO (login por email). NO es contacto WhatsApp (ese va cifrado, Plan 011).';
COMMENT ON COLUMN public.iam_users.password_hash IS 'Hash bcrypt (cost 12, wapp-shared/auth). NUNCA la contraseña en claro.';
COMMENT ON COLUMN public.iam_users.is_active     IS 'Usuario habilitado para operar; false lo bloquea sin borrarlo.';
COMMENT ON COLUMN public.iam_users.created_at    IS 'Momento del alta. Usa el DEFAULT now().';
COMMENT ON COLUMN public.iam_users.updated_at    IS 'Momento de la última modificación. Usa el DEFAULT now() en el alta.';
COMMENT ON COLUMN public.iam_users.deleted_at    IS 'Soft-delete: instante de baja lógica; NULL = activo.';
