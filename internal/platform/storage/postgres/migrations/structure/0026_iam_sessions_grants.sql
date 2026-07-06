-- ============================================================
-- 0026: Grant de `sessions.write` para el rol canónico `operator` (Plan 020 · T1).
-- La administración del rol de sesión (POST /admin/sessions/{id}/role y su gemelo
-- /api/v1/sessions/{id}/role) exige el scope sessions.write:
--   * tenant_admin ('*')  ya lo cubre por glob    -> NO se siembra nada aquí.
--   * viewer       ('*.read') NO da escritura       -> correcto (solo lee).
--   * operator NO tiene glob amplio (flows.*, messages.send, media.*, …) -> se le
--     añade el grant EXPLÍCITO para que pueda administrar el rol de sus sesiones.
--
-- Extiende el seed de 0015_iam_roles.sql (roles = PLANTILLAS globales, tenant_id
-- NULL). CERO PII / CERO llaves: solo un patrón de permiso.
--
-- ADITIVA e IDEMPOTENTE: ID fijo determinista + ON CONFLICT (id) DO NOTHING =>
-- re-aplicable N veces (runner hash-based FULL-REPLAY) sin duplicar. NO clean-slate.
-- ============================================================

INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- operator: sessions.write (administrar el rol bot|passive de sus sesiones)
    ('20000000-0000-0000-0000-00000000000b', '10000000-0000-0000-0000-000000000002', 'sessions.write', 'allow')
ON CONFLICT (id) DO NOTHING;
