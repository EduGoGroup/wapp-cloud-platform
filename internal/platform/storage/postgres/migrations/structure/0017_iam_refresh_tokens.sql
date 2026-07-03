-- ============================================================
-- 0017: Refresh tokens del IAM (Plan 018 · T1, design.md §4, §8).
-- Tabla NUEVA iam_refresh_tokens: soporta el ciclo login -> refresh -> logout.
-- SOLO se guarda el HASH (SHA256, wapp-shared/auth) del token, NUNCA el token en
-- claro (si se filtrara la BD no se pueden reusar). revoked_at implementa el
-- logout (revoca refresh); expires_at el vencimiento natural.
--
-- CERO PII / CERO llaves: token_hash es un digest opaco; NUNCA material de la
-- doble llave (DEK/lease) del Edge.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE ... IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.iam_refresh_tokens (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES public.iam_users(id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL,                -- SHA256 del refresh token; NUNCA el token en claro
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,                         -- NULL = vigente; set = revocado (logout)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup por hash en el refresh (único: un hash = un token).
CREATE UNIQUE INDEX IF NOT EXISTS iam_refresh_tokens_hash_uidx
    ON public.iam_refresh_tokens (token_hash);
-- Escaneo de los tokens de un usuario (revocación masiva / limpieza).
CREATE INDEX IF NOT EXISTS iam_refresh_tokens_user_idx
    ON public.iam_refresh_tokens (user_id);

COMMENT ON TABLE  public.iam_refresh_tokens IS 'Refresh tokens del IAM (Plan 018 §8). Solo el HASH SHA256 (wapp-shared/auth), NUNCA el token en claro. revoked_at = logout. CERO PII; NUNCA DEK/lease del Edge.';
COMMENT ON COLUMN public.iam_refresh_tokens.id         IS 'Identidad de la fila (UUID).';
COMMENT ON COLUMN public.iam_refresh_tokens.user_id    IS 'Usuario dueño del token (FK iam_users, ON DELETE CASCADE).';
COMMENT ON COLUMN public.iam_refresh_tokens.token_hash IS 'SHA256 del refresh token. NUNCA el token en claro.';
COMMENT ON COLUMN public.iam_refresh_tokens.expires_at IS 'Vencimiento natural del token.';
COMMENT ON COLUMN public.iam_refresh_tokens.revoked_at IS 'Instante de revocación (logout); NULL = vigente.';
COMMENT ON COLUMN public.iam_refresh_tokens.created_at IS 'Momento del alta. Usa el DEFAULT now().';
