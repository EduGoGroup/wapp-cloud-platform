-- ============================================================
-- 0009: Outbox de efectos del motor de flujos (Plan 015 · T2, ADR-0009 / ADR-0010).
-- Tabla NUEVA flow_events: bitácora append-only de los efectos que el runtime
-- emite (persist/event) al avanzar una conversación. Es DATO DE NEGOCIO EN CLARO
-- en Postgres cloud (ADR-0009: la nube aloja contenido de negocio; la DEK y el
-- store cifrado NUNCA salen del Edge, ADR-0007).
--
-- CERO PII: contact_id es la identidad OPACA (contacts.contact_id, Plan 010 /
-- ADR-0010: UUID sin número). Aquí NUNCA se guarda número/JID en claro ni ninguna
-- llave. El payload (JSONB) transporta datos de negocio del efecto, no PII.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE/INDEX IF NOT EXISTS
-- garantizan re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.flow_events (
    id            BIGSERIAL   PRIMARY KEY,
    tenant_id     TEXT        NOT NULL,
    contact_id    TEXT        NOT NULL,   -- OPACO (Plan 010); NUNCA número/JID en claro
    flow_id       TEXT        NOT NULL,
    flow_version  INTEGER     NOT NULL,
    kind          TEXT        NOT NULL,   -- "persist" | "event"
    name          TEXT        NOT NULL,   -- "survey_answer" | ...
    payload       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Índice de ESCANEO: sirve las lecturas por (tenant, flujo, tipo de efecto, orden
-- temporal) — el consumo del outbox por los sinks/consumidores (T2b).
CREATE INDEX IF NOT EXISTS flow_events_scan_idx
    ON public.flow_events (tenant_id, flow_id, name, created_at);

COMMENT ON TABLE  public.flow_events IS 'Outbox append-only de efectos del motor de flujos (persist/event) como datos de NEGOCIO EN CLARO (ADR-0009). CERO PII: la identidad la protege el contact_id opaco (ADR-0010). NUNCA DEK, store cifrado ni número/JID en claro.';
COMMENT ON COLUMN public.flow_events.id           IS 'Identidad técnica de la fila (append-only; no tiene significado de negocio).';
COMMENT ON COLUMN public.flow_events.tenant_id    IS 'Tenant dueño del flujo.';
COMMENT ON COLUMN public.flow_events.contact_id   IS 'Identidad OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010). NUNCA el número/JID en claro.';
COMMENT ON COLUMN public.flow_events.flow_id      IS 'Flujo que produjo el efecto.';
COMMENT ON COLUMN public.flow_events.flow_version IS 'Versión del flujo con la que se emitió el efecto.';
COMMENT ON COLUMN public.flow_events.kind         IS 'Clase del efecto: "persist" (persistir dato de negocio) o "event" (evento a despachar).';
COMMENT ON COLUMN public.flow_events.name         IS 'Nombre lógico del efecto (p.ej. "survey_answer").';
COMMENT ON COLUMN public.flow_events.payload      IS 'Cuerpo del efecto (JSONB), dato de negocio; DEFAULT ''{}''. NUNCA PII ni llaves.';
COMMENT ON COLUMN public.flow_events.created_at   IS 'Momento de la escritura (emisión del efecto). Usa el DEFAULT now().';
