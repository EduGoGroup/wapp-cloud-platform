-- ============================================================
-- 0016: Asignación usuario↔rol (M2M) y overrides de grants por usuario
-- (Plan 018 · T1, design.md §4/§5).
-- Tablas NUEVAS iam_user_roles (qué roles tiene un usuario) e iam_user_grants
-- (grants EXTRA/override por usuario que se mergean sobre los del rol al emitir
-- el token). Copia-adaptación del RBAC de edugo-api-identity.
--
-- Los grants EFECTIVOS del token (design.md §5) = role_grants(rol + cadena
-- parent) ⊕ user_grants(overrides), resueltos AL EMITIR por wapp-shared/auth.
--
-- CERO PII / CERO llaves: solo referencias (UUID) y patrones de permiso.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.iam_user_roles (
    user_id UUID NOT NULL REFERENCES public.iam_users(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES public.iam_roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS public.iam_user_grants (
    id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.iam_users(id) ON DELETE CASCADE,
    pattern TEXT NOT NULL,                          -- glob recurso.accion (override)
    effect  TEXT NOT NULL DEFAULT 'allow' CHECK (effect IN ('allow','deny'))
);

-- Un override por (usuario, patrón, efecto): dedup e idempotencia.
CREATE UNIQUE INDEX IF NOT EXISTS iam_user_grants_uidx
    ON public.iam_user_grants (user_id, pattern, effect);

COMMENT ON TABLE  public.iam_user_roles  IS 'Asignación M2M usuario↔rol (Plan 018 §5). PK (user_id, role_id). CERO PII ni llaves.';
COMMENT ON COLUMN public.iam_user_roles.user_id IS 'Usuario asignado (FK iam_users, ON DELETE CASCADE).';
COMMENT ON COLUMN public.iam_user_roles.role_id IS 'Rol asignado (FK iam_roles, ON DELETE CASCADE).';
COMMENT ON TABLE  public.iam_user_grants IS 'Overrides de grants por usuario que se mergean sobre los del rol al emitir el token (design.md §5). CERO PII ni llaves.';
COMMENT ON COLUMN public.iam_user_grants.user_id IS 'Usuario dueño del override (FK iam_users, ON DELETE CASCADE).';
COMMENT ON COLUMN public.iam_user_grants.pattern IS 'Patrón glob recurso.accion del override.';
COMMENT ON COLUMN public.iam_user_grants.effect  IS 'Efecto del override: ''allow'' | ''deny''. deny precede a allow al evaluar.';
