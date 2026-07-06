-- ============================================================
-- 0023: Reglas de disparo del Motor de Flujos (Plan 019 · T1, ADR-0009).
-- Tabla NUEVA flow_triggers: una sola tabla unifica los tres conceptos vía la
-- columna kind ('keyword' | 'fallback' | 'escape'), de modo que crecer por
-- casos (mañana 'regex', 'schedule', ...) es AÑADIR FILAS, no código nuevo en
-- el engine.
--
-- CERO PII / CERO llaves: aquí solo viven reglas de configuración del tenant
-- (palabra clave + flujo objetivo). NO guarda contenido de mensajes ni identidad
-- del contacto. Es DATO DE NEGOCIO EN CLARO (ADR-0009: la nube aloja contenido
-- de negocio; la DEK y el store cifrado NUNCA salen del Edge, ADR-0007).
--
-- Semántica por kind:
--   keyword  -> requiere keyword + flow_id; arranca flow_id si el entrante casa.
--   fallback -> requiere flow_id (keyword NULL); uno por tenant (gana priority).
--   escape   -> requiere keyword (flow_id NULL); corta la conversación viva y envía
--               'message' como aviso (NULL ⇒ el runtime usa su aviso por defecto).
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE/INDEX IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.flow_triggers (
    tenant_id   UUID        NOT NULL,
    trigger_id  UUID        NOT NULL DEFAULT gen_random_uuid(),
    kind        TEXT        NOT NULL,                    -- 'keyword' | 'fallback' | 'escape'
    keyword     TEXT        NULL,                        -- requerido kind IN ('keyword','escape')
    match_type  TEXT        NOT NULL DEFAULT 'exact',    -- 'exact' | 'contains'
    flow_id     TEXT        NULL,                        -- requerido kind IN ('keyword','fallback')
    priority    INTEGER     NOT NULL DEFAULT 0,
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    message     TEXT        NULL,                        -- aviso de escape (kind='escape'); NULL ⇒ default del runtime
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, trigger_id)
);

CREATE INDEX IF NOT EXISTS idx_flow_triggers_lookup
    ON public.flow_triggers (tenant_id, kind, enabled);

COMMENT ON TABLE  public.flow_triggers IS 'Reglas de disparo del Motor de Flujos (Plan 019): keyword/fallback/escape como filas (kind), extensible sin tocar el engine. DATO DE NEGOCIO EN CLARO (ADR-0009); NUNCA PII ni llaves.';
COMMENT ON COLUMN public.flow_triggers.tenant_id  IS 'Tenant dueño de la regla (aislamiento INV-8; toda query filtra por aquí).';
COMMENT ON COLUMN public.flow_triggers.trigger_id IS 'Id opaco de la regla (PK junto a tenant_id).';
COMMENT ON COLUMN public.flow_triggers.kind       IS 'keyword | fallback | escape: unifica los tres conceptos en una tabla.';
COMMENT ON COLUMN public.flow_triggers.keyword    IS 'Palabra clave a casar (kind keyword/escape); NULL para fallback.';
COMMENT ON COLUMN public.flow_triggers.match_type IS 'exact | contains: estrategia de coincidencia tras normalizar (default exact).';
COMMENT ON COLUMN public.flow_triggers.flow_id    IS 'Flujo objetivo a arrancar (kind keyword/fallback); NULL para escape.';
COMMENT ON COLUMN public.flow_triggers.priority   IS 'Desempate determinista: mayor priority gana entre reglas que casan.';
COMMENT ON COLUMN public.flow_triggers.enabled    IS 'La regla solo aplica si enabled=true (borrado lógico / apagado).';
COMMENT ON COLUMN public.flow_triggers.message    IS 'Aviso a enviar al cortar la conversación (kind escape); NULL ⇒ el runtime usa su aviso por defecto.';
