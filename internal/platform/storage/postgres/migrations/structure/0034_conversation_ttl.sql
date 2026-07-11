-- ============================================================
-- 0034: TTL conversacional genérico (Plan 029 · T9, design.md §4.d).
-- El Motor de Flujos gana un vencimiento por CONVERSACIÓN: tras
-- conversation_ttl_seconds sin actividad, un estado vivo se descarta y el próximo
-- entrante arranca de nuevo (camino de disparo, donde la señal LLM aplica). Es una
-- semántica DISTINTA a order_ttl_seconds (migración 0013), que es el TTL de la ORDEN
-- del carrito: el TTL de la orden se evalúa aparte y después, como hoy.
--
-- DEFAULT 0 ⇒ SIN vencimiento: los tenants existentes quedan intactos (una
-- conversación viva nunca vence salvo que el tenant configure un TTL > 0). El store
-- mapea el entero a time.Duration (segundos); el runtime compara now - flow_state.
-- updated_at contra este valor ANTES de escape/replay/reanudación.
--
-- ADITIVA e IDEMPOTENTE: ADD COLUMN IF NOT EXISTS ⇒ re-aplicable N veces sin daño
-- (runner hash-based FULL-REPLAY). CERO PII / CERO llaves: solo config de negocio.
-- SchemaVersion se mantiene en 0.20.0 (frente A): el runner detecta el archivo nuevo
-- por el hash de contenido y re-aplica la estructura, sin necesidad de subir la versión.
-- ============================================================

ALTER TABLE public.tenant_settings
    ADD COLUMN IF NOT EXISTS conversation_ttl_seconds INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN public.tenant_settings.conversation_ttl_seconds IS 'TTL de la CONVERSACIÓN en segundos (default 0 = sin vencimiento; Plan 029 · T9). Distinto de order_ttl_seconds (TTL de la orden). El store lo mapea a time.Duration; el runtime descarta el estado vivo tras ese silencio.';
