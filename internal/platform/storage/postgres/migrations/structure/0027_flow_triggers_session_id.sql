-- ============================================================
-- 0027: session_id opcional en flow_triggers (Plan 020 · T4).
-- Una regla de disparo puede ACOTARSE a una sesión concreta (session_id) o ser
-- GLOBAL del tenant (session_id NULL). Semántica de lookup (config_resolver):
--   * NULL           -> aplica a TODAS las sesiones del tenant (retrocompatible).
--   * '<session_id>'  -> aplica SOLO a esa sesión.
-- Ante un entrante de la sesión S, el resolver carga las reglas con
-- (session_id = S OR session_id IS NULL) y, en el desempate, la ESPECÍFICA de
-- sesión gana a la GLOBAL cuando ambas casan.
--
-- Extiende 0023_flow_triggers.sql. CERO PII / CERO llaves: session_id es un id
-- opaco de sesión de fleet (dato de negocio/config, ADR-0009), NUNCA contenido ni
-- identidad de contacto.
--
-- ADITIVA e IDEMPOTENTE (runner hash-based FULL-REPLAY): ADD COLUMN IF NOT EXISTS +
-- CREATE INDEX IF NOT EXISTS => re-aplicable N veces sin daño. session_id NULL por
-- defecto garantiza que las reglas existentes siguen siendo GLOBALES (INV-6
-- no-regresión: sin tocar nada, el keyword-trigger del 019 se comporta idéntico).
-- ============================================================

ALTER TABLE public.flow_triggers
    ADD COLUMN IF NOT EXISTS session_id TEXT NULL;

-- Índice de lookup ampliado con session_id: sostiene el filtro
-- (tenant_id, kind, session_id OR NULL) del config_resolver. El índice previo
-- idx_flow_triggers_lookup (tenant_id, kind, enabled) se conserva.
CREATE INDEX IF NOT EXISTS idx_flow_triggers_lookup_session
    ON public.flow_triggers (tenant_id, kind, session_id, enabled);

COMMENT ON COLUMN public.flow_triggers.session_id IS 'Sesión a la que se acota la regla (id opaco de fleet); NULL ⇒ regla GLOBAL del tenant (todas las sesiones). En el desempate, la regla específica de sesión gana a la global. DATO DE NEGOCIO/CONFIG (ADR-0009); NUNCA PII ni llaves.';
