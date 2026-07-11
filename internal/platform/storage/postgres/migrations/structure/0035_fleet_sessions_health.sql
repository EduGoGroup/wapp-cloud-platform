-- ============================================================
-- 0035: Salud real de la sesión en fleet_sessions (Plan 031 · T3, ADR-0023).
-- El incidente del 2026-07-11 vivió en el HUECO entre el estado del STREAM
-- CloudLink (columna `state`: online/offline/loggedout, semántica de registro del
-- stream) y la salud REAL del socket whatsmeow. El Cloud reportaba online mientras
-- el socket llevaba 31 min muerto. Esta migración añade la salud del socket como
-- columnas SEPARADAS: `state` (el link CloudLink) NO se toca ni se renombra;
-- `whatsapp_state` es la verdad del socket que el Edge reporta en el SessionHealth
-- adjunto a su Heartbeat (cloudlink, Plan 031 · T1).
--
-- Lista CERRADA de campos (ADR-0023 §Decisión): solo metadatos de salud. CERO PII /
-- CERO llaves / CERO credenciales (frontera zero-knowledge, ADR-0007). Todas las
-- columnas son NULLABLE y SIN default: un Edge viejo (sin SessionHealth) las deja en
-- NULL y nada se pisa (INV no-regresión). `degraded_since` marca CUÁNDO entró en
-- degradado (se setea al entrar, se limpia al salir; lo gobierna el ingestor, no un
-- default); `last_health_at` es la marca del último snapshot recibido (alimenta la
-- derivación de `stale` en GET /api/v1/sessions, Plan 031 · T4).
--
-- ADITIVA e IDEMPOTENTE (runner hash-based FULL-REPLAY): ADD COLUMN IF NOT EXISTS ⇒
-- re-aplicable N veces sin daño. SchemaVersion sube a 0.21.0.
-- ============================================================

ALTER TABLE public.fleet_sessions
    ADD COLUMN IF NOT EXISTS whatsapp_state       TEXT,
    ADD COLUMN IF NOT EXISTS degraded_reason      TEXT,
    ADD COLUMN IF NOT EXISTS degraded_since       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_health_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_event_age_s     BIGINT,
    ADD COLUMN IF NOT EXISTS outbox_depth         BIGINT,
    ADD COLUMN IF NOT EXISTS binary_version       TEXT,
    ADD COLUMN IF NOT EXISTS uptime_s             BIGINT,
    ADD COLUMN IF NOT EXISTS dek_load_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS intent_circuit       TEXT;

COMMENT ON COLUMN public.fleet_sessions.whatsapp_state IS 'Verdad del SOCKET whatsmeow reportado por el Edge (SessionHealth): connected|connecting|degraded|dead. SEPARADO de state (registro del stream CloudLink). NULL = el Edge no reporta salud (viejo). Plan 031 · T3, ADR-0023.';
COMMENT ON COLUMN public.fleet_sessions.degraded_reason IS 'Motivo del degradado cuando whatsapp_state no es sano (p. ej. dek_load_timeout, ws_dial_timeout, db_timeout). Vacío/NULL si sano. Plan 031 · T3.';
COMMENT ON COLUMN public.fleet_sessions.degraded_since IS 'Instante en que la sesión ENTRÓ en degradado (se setea al entrar, se limpia al salir). Alimenta la derivación degraded>N min. Plan 031 · T3/T4.';
COMMENT ON COLUMN public.fleet_sessions.last_health_at IS 'Marca del último snapshot de salud recibido. Alimenta la derivación stale (sin salud > M min ⇒ dato no confiable). Plan 031 · T3/T4.';
COMMENT ON COLUMN public.fleet_sessions.last_event_age_s IS 'Prueba de vida: segundos desde el último evento entrante (SessionHealth.last_inbound_event_age_s). Plan 031 · T3.';
COMMENT ON COLUMN public.fleet_sessions.outbox_depth IS 'Profundidad del outbox del Edge (ADR-0003). Plan 031 · T3.';
COMMENT ON COLUMN public.fleet_sessions.binary_version IS 'Versión del binario del Edge (la consumirá el auto-update, ADR-0011/Plan 032). Plan 031 · T3.';
COMMENT ON COLUMN public.fleet_sessions.uptime_s IS 'Uptime del daemon del Edge en segundos (SessionHealth.daemon_uptime_s). Plan 031 · T3.';
COMMENT ON COLUMN public.fleet_sessions.dek_load_duration_ms IS 'Duración de la última carga de la DEK en ms (SessionHealth.dek_load_duration_ms). Plan 031 · T3.';
COMMENT ON COLUMN public.fleet_sessions.intent_circuit IS 'Estado del circuito del clasificador de intenciones (ADR-0020): closed|open|half_open; vacío si el 029 no aplica. Plan 031 · T3.';
