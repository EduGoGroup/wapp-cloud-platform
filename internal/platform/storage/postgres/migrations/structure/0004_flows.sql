-- ============================================================
-- 0004: Motor de Flujos (Plan 006 · T2).
-- Dos tablas que cuelgan de public.tenants:
--   * flow_definitions — definiciones de flujo declarativas y VERSIONADAS (JSONB).
--   * flow_state       — estado conversacional por (tenant, sesión, contacto) (JSONB).
-- ADR-0009 (zero-knowledge / datos de negocio en la nube): aquí SOLO vive
-- contenido de NEGOCIO (definición del flujo, estado de la conversación, número
-- del contacto). NUNCA la DEK, el store cifrado de whatsmeow ni llaves privadas:
-- esos permanecen exclusivamente en el Edge (ADR-0007).
-- DDL idempotente (IF NOT EXISTS); el runner lo (re)aplica en orden alfabético.
-- ============================================================

-- ------------------------------------------------------------
-- flow_definitions: una definición de flujo (datos, no código; Pieza 05 §3) es
-- inmutable y versionada. Publicar por el endpoint admin inserta una VERSIÓN
-- NUEVA (no muta la vigente, design.md §4); la unidad persistida es
-- (tenant_id, flow_id, version).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.flow_definitions (
    tenant_id  UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    flow_id    TEXT        NOT NULL,
    version    INTEGER     NOT NULL,
    definition JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, flow_id, version)
);

COMMENT ON TABLE  public.flow_definitions IS 'Definiciones de flujo versionadas (JSONB). Solo contenido de negocio (ADR-0009); nunca DEK ni store.';
COMMENT ON COLUMN public.flow_definitions.tenant_id  IS 'Tenant dueño del flujo (FK a tenants).';
COMMENT ON COLUMN public.flow_definitions.flow_id    IS 'Identificador lógico del flujo (estable entre versiones).';
COMMENT ON COLUMN public.flow_definitions.version    IS 'Versión incremental; publicar inserta una nueva, no muta la vigente (design.md §4).';
COMMENT ON COLUMN public.flow_definitions.definition IS 'Grafo declarativo del flujo (nodes/options) en JSON (Pieza 05 §3).';
COMMENT ON COLUMN public.flow_definitions.created_at IS 'Alta de la versión.';

-- ------------------------------------------------------------
-- flow_state: estado vivo de UNA conversación, ligado a la clave lógica
-- (tenant_id, session_id, contact) (Pieza 05 §3). Guarda la versión con la que
-- arrancó (flow_version) para que una conversación en curso no salte de versión
-- (versionado, design.md §4), y la marca de idempotencia del último entrante
-- procesado (last_wa_message_id; decisión §10.G: una marca por conversación).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.flow_state (
    tenant_id          UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    session_id         TEXT        NOT NULL,
    contact            TEXT        NOT NULL,
    flow_id            TEXT        NOT NULL,
    flow_version       INTEGER     NOT NULL,
    current_node       TEXT        NOT NULL,
    vars               JSONB       NOT NULL DEFAULT '{}',
    last_wa_message_id TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, session_id, contact)
);

CREATE INDEX IF NOT EXISTS idx_flow_state_tenant
    ON public.flow_state (tenant_id);

COMMENT ON TABLE  public.flow_state IS 'Estado conversacional por (tenant, sesión, contacto). Solo contenido de negocio (ADR-0009); nunca DEK ni store.';
COMMENT ON COLUMN public.flow_state.tenant_id          IS 'Tenant dueño de la conversación (FK a tenants).';
COMMENT ON COLUMN public.flow_state.session_id         IS 'Sesión CloudLink en la que vive la conversación (la provee el Edge).';
COMMENT ON COLUMN public.flow_state.contact            IS 'Número del contacto (dato de NEGOCIO, no credencial; Pieza 05 §7).';
COMMENT ON COLUMN public.flow_state.flow_id            IS 'Flujo que ejecuta la conversación.';
COMMENT ON COLUMN public.flow_state.flow_version       IS 'Versión con la que arrancó la conversación (no salta de versión; design.md §4).';
COMMENT ON COLUMN public.flow_state.current_node       IS 'Nodo actual de la máquina de estados (centinela de fin = conversación terminada).';
COMMENT ON COLUMN public.flow_state.vars               IS 'Variables recolectadas + contador de reprompt (design.md §10.E), en JSON.';
COMMENT ON COLUMN public.flow_state.last_wa_message_id IS 'wa_message_id del último entrante procesado (idempotencia, decisión §10.G).';
COMMENT ON COLUMN public.flow_state.updated_at         IS 'Última actualización del estado (sin reaper en este corte; TTL diferido, §10.F).';
