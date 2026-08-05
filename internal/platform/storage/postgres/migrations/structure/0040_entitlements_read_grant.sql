-- ============================================================
-- 0040: Grant de `entitlements.read` para el rol canónico `operator`
-- (Plan 040 · T1, design.md §D-040.4).
-- El endpoint de derechos del tenant (GET /api/v1/entitlements) exige el scope
-- entitlements.read (design.md §3):
--   * tenant_admin ('*')      ya lo cubre por glob        -> NO se siembra nada aquí.
--   * viewer       ('*.read') ya cubre entitlements.read por glob -> idem.
--   * operator NO tiene glob amplio (flows.*, messages.send, media.*, sessions.*,
--     triggers.*, intents.*, diagnostics.request, …) -> se le añade el grant
--     EXPLÍCITO para que pueda consultar los derechos de SU tenant (hermana de
--     0030_iam_sessions_read_grant.sql).
--
-- Es lectura del PROPIO tenant (INV-8: el tenant sale del token, no hay consulta
-- cross-tenant). Extiende el seed de 0015_iam_roles.sql (roles = PLANTILLAS
-- globales, tenant_id NULL). CERO PII / CERO llaves: solo un patrón de permiso.
--
-- ADITIVA e IDEMPOTENTE: ID fijo determinista + ON CONFLICT (id) DO NOTHING =>
-- re-aplicable N veces (runner hash-based FULL-REPLAY) sin duplicar. NO clean-slate.
-- ============================================================

INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- operator: entitlements.read (consultar plan y features efectivas de su tenant)
    ('20000000-0000-0000-0000-000000000010', '10000000-0000-0000-0000-000000000002', 'entitlements.read', 'allow')
ON CONFLICT (id) DO NOTHING;
