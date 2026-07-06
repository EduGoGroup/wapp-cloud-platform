-- ============================================================
-- 0024: Grants de `triggers.*` para el rol canónico `operator` (Plan 019 · T5).
-- El CRUD de reglas de disparo (/admin/triggers y /api/v1/triggers) exige los
-- scopes triggers.create / triggers.read / triggers.delete (design.md §7):
--   * tenant_admin ('*')  ya los cubre por glob    -> NO se siembra nada aquí.
--   * viewer       ('*.read') ya cubre triggers.read por glob -> idem.
--   * operator NO tiene glob amplio (flows.*, messages.send, media.*, …) -> se
--     le añaden los tres grants EXPLÍCITOS para que pueda administrar triggers.
--
-- Extiende el seed de 0015_iam_roles.sql (roles = PLANTILLAS globales, tenant_id
-- NULL). CERO PII / CERO llaves: solo patrones de permiso.
--
-- ADITIVA e IDEMPOTENTE: IDs fijos deterministas + ON CONFLICT (id) DO NOTHING
-- => re-aplicable N veces (runner hash-based FULL-REPLAY) sin duplicar. NO
-- clean-slate.
-- ============================================================

INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- operator: triggers.create / triggers.read / triggers.delete (CRUD de disparos)
    ('20000000-0000-0000-0000-000000000008', '10000000-0000-0000-0000-000000000002', 'triggers.create', 'allow'),
    ('20000000-0000-0000-0000-000000000009', '10000000-0000-0000-0000-000000000002', 'triggers.read',   'allow'),
    ('20000000-0000-0000-0000-00000000000a', '10000000-0000-0000-0000-000000000002', 'triggers.delete', 'allow')
ON CONFLICT (id) DO NOTHING;
