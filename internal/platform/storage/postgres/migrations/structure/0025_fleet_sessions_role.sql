-- ============================================================
-- 0025: Rol de sesión bot|passive en fleet_sessions (Plan 020 · T1).
-- Cada sesión de fleet tiene un ROL:
--   * bot     -> ejecuta el motor de flujos (dispara triggers / auto-responde).
--   * passive -> escucha/transporta; NO dispara triggers ni auto-responde.
-- El runtime lee este rol al resolver el tenant del entrante (tenant_resolver) y,
-- si es passive, NO entra al motor reactivo (ni trigger ni auto-envío). La escucha
-- y los acuses siguen normales. Se administra por API acotada al tenant del token.
--
-- Extiende 0003_lease_fleet.sql. CERO PII / CERO llaves: solo un flag de rol.
--
-- ADITIVA e IDEMPOTENTE (runner hash-based FULL-REPLAY): ADD COLUMN IF NOT EXISTS +
-- DROP/ADD CONSTRAINT IF EXISTS => re-aplicable N veces sin daño. DEFAULT 'bot'
-- garantiza que las filas existentes conservan el comportamiento previo (INV-6
-- no-regresión: sin configurar nada, el 019 se comporta idéntico).
-- ============================================================

ALTER TABLE public.fleet_sessions
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'bot';

-- CHECK idempotente: se recrea (DROP IF EXISTS + ADD) en cada replay del runner.
ALTER TABLE public.fleet_sessions
    DROP CONSTRAINT IF EXISTS fleet_sessions_role_chk;
ALTER TABLE public.fleet_sessions
    ADD CONSTRAINT fleet_sessions_role_chk CHECK (role IN ('bot', 'passive'));

COMMENT ON COLUMN public.fleet_sessions.role IS 'Rol de la sesión: bot = ejecuta el motor de flujos (dispara triggers / auto-responde); passive = solo escucha/transporta (no dispara triggers ni auto-responde). DEFAULT bot => no-regresión.';
