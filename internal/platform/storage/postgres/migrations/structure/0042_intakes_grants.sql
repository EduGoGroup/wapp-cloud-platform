-- ============================================================
-- 0042: Grants de `intakes.read` / `intakes.write` para el rol canónico `operator`
-- (Plan 041 · T1.1/T1.4, design.md §4).
-- La bandeja de SOLICITUDES (GET /api/v1/intakes, GET /api/v1/intakes/{id},
-- POST /api/v1/intakes/{id}/status) exige esos scopes:
--   * tenant_admin ('*')      ya los cubre por glob              -> NO se siembra nada aquí.
--   * viewer       ('*.read') ya cubre intakes.read por glob     -> idem (y NO debe
--     poder escribir: cambiar el estado de un pedido es una decisión de negocio,
--     no una lectura, así que intakes.write se le queda fuera a propósito).
--   * operator NO tiene glob amplio (flows.*, messages.send, media.*, sessions.*,
--     triggers.*, intents.*, diagnostics.request, entitlements.read, …) -> se le
--     añaden los DOS grants EXPLÍCITOS: es el rol que atiende los pedidos del día.
--     (hermana de 0030_iam_sessions_read_grant.sql y 0040_entitlements_read_grant.sql).
--
-- Es operación del PROPIO tenant (INV-8: el tenant sale del token, no hay consulta
-- cross-tenant; una solicitud ajena responde 404). Extiende el seed de
-- 0015_iam_roles.sql (roles = PLANTILLAS globales, tenant_id NULL). CERO PII /
-- CERO llaves: solo dos patrones de permiso.
--
-- El scope NO basta para entrar: estas rutas llevan ADEMÁS el gate de la feature
-- `cart_basic` (ADR-0022 / Plan 040). El grant dice "puedes operar esto"; la
-- feature dice "tu plan lo incluye" — esta migración solo siembra lo primero.
--
-- ADITIVA e IDEMPOTENTE: ID fijo determinista + ON CONFLICT (id) DO NOTHING =>
-- re-aplicable N veces (runner hash-based FULL-REPLAY) sin duplicar. NO clean-slate.
-- ============================================================

INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- operator: intakes.read (listar y abrir las solicitudes de su tenant)
    ('20000000-0000-0000-0000-000000000011', '10000000-0000-0000-0000-000000000002', 'intakes.read',  'allow'),
    -- operator: intakes.write (transicionar el estado del ciclo de vida, D-041.10)
    ('20000000-0000-0000-0000-000000000012', '10000000-0000-0000-0000-000000000002', 'intakes.write', 'allow')
ON CONFLICT (id) DO NOTHING;
