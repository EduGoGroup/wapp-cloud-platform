-- ============================================================
-- 0030: Grant de `sessions.read` para el rol canónico `operator` (Plan 021 · T0).
-- El listado de sesiones (GET /api/v1/sessions) exige el scope sessions.read
-- (design.md §1):
--   * tenant_admin ('*')  ya lo cubre por glob    -> NO se siembra nada aquí.
--   * viewer       ('*.read') ya cubre sessions.read por glob -> idem.
--   * operator NO tiene glob amplio (flows.*, messages.send, media.*, sessions.write,
--     triggers.*, …) -> se le añade el grant EXPLÍCITO para que pueda listar sus
--     sesiones (hermana de 0026_iam_sessions_grants.sql, que le dio sessions.write).
--
-- Extiende el seed de 0015_iam_roles.sql (roles = PLANTILLAS globales, tenant_id
-- NULL). CERO PII / CERO llaves: solo un patrón de permiso.
--
-- ADITIVA e IDEMPOTENTE: ID fijo determinista + ON CONFLICT (id) DO NOTHING =>
-- re-aplicable N veces (runner hash-based FULL-REPLAY) sin duplicar. NO clean-slate.
-- ============================================================

INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- operator: sessions.read (listar sus sesiones/teléfonos vinculados)
    ('20000000-0000-0000-0000-00000000000c', '10000000-0000-0000-0000-000000000002', 'sessions.read', 'allow')
ON CONFLICT (id) DO NOTHING;
