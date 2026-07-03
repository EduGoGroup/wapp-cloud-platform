-- ============================================================
-- 0015: Roles y grants de rol del IAM (Plan 018 · T1, design.md §4/§5).
-- Tablas NUEVAS iam_roles (+ herencia parent_role_id) e iam_role_grants (patrón
-- glob recurso.accion con effect allow/deny). Copia-adaptación del RBAC de
-- edugo-api-identity (permission_matcher/role_chain) al modelo plano de wApp.
--
-- DECISIÓN (roles = PLANTILLAS GLOBALES, R2 · task.md): tenant_id es NULLABLE.
--   * tenant_id IS NULL  -> ROL PLANTILLA GLOBAL, referenciable por cualquier
--     tenant (los 3 canónicos sembrados aquí). Es lo más simple e idempotente:
--     un seed estático no puede materializar filas por-tenant para tenants que
--     aún no existen (se crean dinámicamente). T2 puede clonar la plantilla a
--     un rol por-tenant (tenant_id set) cuando el tenant quiera personalizar.
--   * tenant_id NOT NULL -> rol propio del tenant (custom), FK a tenants.
-- Evaluación de grants (design.md §5): default DENY; un 'deny' que matchea niega;
-- un 'allow' que matchea permite (deny-precede-allow). Los grants EFECTIVOS del
-- token = role_grants(rol + cadena parent) ⊕ user_grants(overrides), resueltos
-- AL EMITIR (wapp-shared/auth), no en cada request.
--
-- CERO PII / CERO llaves: aquí solo viven roles y patrones de permiso.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- + INSERT ... ON CONFLICT DO NOTHING garantizan re-aplicación N veces sin daño.
-- NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.iam_roles (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        REFERENCES public.tenants(id) ON DELETE CASCADE,  -- NULL = plantilla global
    name           TEXT        NOT NULL,
    parent_role_id UUID        REFERENCES public.iam_roles(id) ON DELETE SET NULL,  -- herencia (cadena)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Unicidad de nombre entre PLANTILLAS globales (tenant_id NULL: los NULL son
-- distintos en un UNIQUE normal, por eso índice parcial).
CREATE UNIQUE INDEX IF NOT EXISTS iam_roles_global_name_uidx
    ON public.iam_roles (name) WHERE tenant_id IS NULL;
-- Unicidad de nombre POR tenant (roles custom).
CREATE UNIQUE INDEX IF NOT EXISTS iam_roles_tenant_name_uidx
    ON public.iam_roles (tenant_id, name) WHERE tenant_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS public.iam_role_grants (
    id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id UUID NOT NULL REFERENCES public.iam_roles(id) ON DELETE CASCADE,
    pattern TEXT NOT NULL,                          -- glob recurso.accion: '*', 'flows.*', '*.read'
    effect  TEXT NOT NULL DEFAULT 'allow' CHECK (effect IN ('allow','deny'))
);

-- Un grant por (rol, patrón, efecto): dedup e idempotencia del seed.
CREATE UNIQUE INDEX IF NOT EXISTS iam_role_grants_uidx
    ON public.iam_role_grants (role_id, pattern, effect);

-- ------------------------------------------------------------
-- SEED de roles canónicos (PLANTILLAS globales, tenant_id NULL; design.md §5).
-- IDs fijos deterministas -> ON CONFLICT (id) DO NOTHING re-aplicable sin duplicar.
-- ------------------------------------------------------------
INSERT INTO public.iam_roles (id, tenant_id, name) VALUES
    ('10000000-0000-0000-0000-000000000001', NULL, 'tenant_admin'),
    ('10000000-0000-0000-0000-000000000002', NULL, 'operator'),
    ('10000000-0000-0000-0000-000000000003', NULL, 'viewer')
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- tenant_admin: '*'
    ('20000000-0000-0000-0000-000000000001', '10000000-0000-0000-0000-000000000001', '*',                 'allow'),
    -- operator: flows.*, messages.send, media.*, contacts.read, integrations.read
    ('20000000-0000-0000-0000-000000000002', '10000000-0000-0000-0000-000000000002', 'flows.*',           'allow'),
    ('20000000-0000-0000-0000-000000000003', '10000000-0000-0000-0000-000000000002', 'messages.send',     'allow'),
    ('20000000-0000-0000-0000-000000000004', '10000000-0000-0000-0000-000000000002', 'media.*',           'allow'),
    ('20000000-0000-0000-0000-000000000005', '10000000-0000-0000-0000-000000000002', 'contacts.read',     'allow'),
    ('20000000-0000-0000-0000-000000000006', '10000000-0000-0000-0000-000000000002', 'integrations.read', 'allow'),
    -- viewer: '*.read'
    ('20000000-0000-0000-0000-000000000007', '10000000-0000-0000-0000-000000000003', '*.read',            'allow')
ON CONFLICT (id) DO NOTHING;

COMMENT ON TABLE  public.iam_roles IS 'Roles RBAC (Plan 018 §5). tenant_id NULL = PLANTILLA global canónica (tenant_admin/operator/viewer, sembrados); tenant_id set = rol custom del tenant. parent_role_id modela herencia (cadena). CERO PII ni llaves.';
COMMENT ON COLUMN public.iam_roles.id             IS 'Identidad del rol (UUID; los canónicos usan IDs fijos deterministas).';
COMMENT ON COLUMN public.iam_roles.tenant_id      IS 'Tenant dueño; NULL = plantilla GLOBAL referenciable por cualquier tenant (Decisión R2).';
COMMENT ON COLUMN public.iam_roles.name           IS 'Nombre del rol (único entre globales y único por tenant vía índices parciales).';
COMMENT ON COLUMN public.iam_roles.parent_role_id IS 'Rol padre para herencia de grants (ResolveRoleChain, wapp-shared/auth); NULL si es raíz.';
COMMENT ON TABLE  public.iam_role_grants IS 'Grants (patrón glob recurso.accion + effect allow/deny) de un rol. Evaluación deny-precede-allow (design.md §5). CERO PII ni llaves.';
COMMENT ON COLUMN public.iam_role_grants.role_id  IS 'Rol al que pertenece el grant (FK, ON DELETE CASCADE).';
COMMENT ON COLUMN public.iam_role_grants.pattern  IS 'Patrón glob recurso.accion: ''*'', ''flows.*'', ''*.read'', ''messages.send''.';
COMMENT ON COLUMN public.iam_role_grants.effect   IS 'Efecto del patrón: ''allow'' | ''deny''. deny precede a allow al evaluar.';
