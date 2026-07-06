-- ============================================================
-- 0029: estado 'loggedout' (zombie) en fleet_sessions (Plan 020 · T3).
-- Amplía el CHECK de state para admitir un TERCER valor:
--   * online     -> stream vivo.
--   * offline    -> stream caído (offline por red); recuperable al reconectar.
--   * loggedout  -> WhatsApp cerró el device (events.LoggedOut reportado por el
--                   Edge en su Heartbeat State=LOGGED_OUT). Sesión ZOMBIE: no
--                   vuelve sola (hay que reemparejar) y NO renueva su lease.
-- El Cloud distingue el zombie del offline-por-red: el offline lo produce el
-- cierre del stream (onStreamClosed→MarkOffline); el loggedout llega EXPLÍCITO
-- por la señal del Edge (MarkLoggedOut). Un admin puede fijar offline|loggedout
-- para retirar/limpiar una sesión (API acotada al tenant del token, INV-8).
--
-- Extiende 0003_lease_fleet.sql. CERO PII / CERO llaves: solo un flag de estado.
--
-- IDEMPOTENTE (runner hash-based FULL-REPLAY): DROP CONSTRAINT IF EXISTS + ADD
-- => re-aplicable N veces sin daño. Ampliar el conjunto NO invalida filas
-- existentes (online/offline siguen admitidos): INV no-regresión.
-- ============================================================

ALTER TABLE public.fleet_sessions
    DROP CONSTRAINT IF EXISTS fleet_sessions_state_chk;
ALTER TABLE public.fleet_sessions
    ADD CONSTRAINT fleet_sessions_state_chk CHECK (state IN ('online', 'offline', 'loggedout'));

COMMENT ON COLUMN public.fleet_sessions.state IS 'online = stream vivo; offline = stream caído (offline por red, recuperable); loggedout = WhatsApp cerró el device (zombie, Plan 020 T3: no vuelve solo, no renueva lease). CHECK acota los valores.';
