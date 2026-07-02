-- ============================================================
-- 0005: Identidad de contacto flexible (Plan 010 · T0).
-- Introduce la capa de identidad del motor de flujos (ADR-0017, plan i):
--   * contacts   — resuelve (tenant_id, kind, value) -> contact_id opaco (UUID).
--   * flow_state — RE-CLAVEADO de (tenant_id, session_id, contact TEXT) a
--                  (tenant_id, session_id, contact_id UUID) (design.md §2.2, §10.C).
-- ADR-0009 (datos de negocio en la nube): aquí SOLO vive contenido de NEGOCIO
-- (identidad y valor del contacto). NUNCA la DEK, el store cifrado de whatsmeow
-- ni llaves privadas: esos permanecen exclusivamente en el Edge (ADR-0007).
-- El `value` va EN CLARO en este corte; el Plan 011 lo cifrará (wapp-shared/envelope,
-- design.md §10.G) sin empeorar la exposición actual (ya estaba en claro en 0004).
--
-- DDL idempotente (IF NOT EXISTS + guarda condicional). El runner (hash-based)
-- RE-APLICA todos los structure/*.sql en orden alfabético cuando cambia el hash;
-- por eso este script debe poder correr N veces sin daño. Corre DESPUÉS de 0004
-- y SUPERSEDE el claveado de flow_state que 0004 creó con la columna `contact`
-- (clean-slate pre-producción, sin datos reales -> sin backfill; design.md §10.C).
-- ============================================================

-- ------------------------------------------------------------
-- contacts: una fila por (tenant_id, kind, value). Un mismo contact_id puede
-- tener VARIAS filas (una por kind: número + LID + username), todas con el mismo
-- contact_id (fusión, design.md §5). La PK (tenant_id, kind, value) DEDUPLICA
-- por ref; el índice (tenant_id, contact_id) resuelve la inversa
-- contact_id -> refs, para elegir el destino enviable (design.md §2.1, §4).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.contacts (
    contact_id UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    kind       TEXT        NOT NULL,
    value      TEXT        NOT NULL,          -- en claro, a cifrar en Plan 011
    push_name  TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, kind, value)      -- dedup por ref
);

CREATE INDEX IF NOT EXISTS idx_contacts_id
    ON public.contacts (tenant_id, contact_id);

COMMENT ON TABLE  public.contacts IS 'Identidad flexible de contacto: resuelve (tenant_id, kind, value) -> contact_id opaco (Plan 010). Solo negocio (ADR-0009); nunca DEK ni store.';
COMMENT ON COLUMN public.contacts.contact_id IS 'Identidad OPACA y estable del contacto (UUID); con ella opera el motor. Varias filas (kinds) comparten contact_id (fusión, design.md §5).';
COMMENT ON COLUMN public.contacts.tenant_id  IS 'Tenant dueño del contacto (FK a tenants).';
COMMENT ON COLUMN public.contacts.kind       IS 'Tipo de referencia: phone_e164 | wa_lid | wa_username (extensible; design.md §10.B).';
COMMENT ON COLUMN public.contacts.value      IS 'Valor NORMALIZADO de la referencia, EN CLARO en este corte (a cifrar en Plan 011). Dedup por (tenant_id, kind, value).';
COMMENT ON COLUMN public.contacts.push_name  IS 'Último push_name visto (dato de negocio, opcional).';
COMMENT ON COLUMN public.contacts.created_at IS 'Alta de la referencia.';
COMMENT ON COLUMN public.contacts.updated_at IS 'Última actualización de la referencia.';

-- ------------------------------------------------------------
-- flow_state RE-CLAVEADO a contact_id (design.md §2.2, §10.C).
-- Clean-slate: al ser PRE-PRODUCCIÓN (sin datos reales de negocio) NO se hace
-- backfill del JID->uuid. Guarda idempotente:
--   * si la tabla aún tiene la columna vieja `contact` (la creó 0004) y todavía
--     NO tiene `contact_id`, se DESCARTA para recrearla re-claveada;
--   * si ya está re-claveada (existe contact_id) NO se toca -> correr N veces es
--     inocuo.
-- NO toca flow_definitions ni datos ajenos.
-- ------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'flow_state'
          AND column_name = 'contact'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'flow_state'
          AND column_name = 'contact_id'
    ) THEN
        DROP TABLE public.flow_state;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS public.flow_state (
    tenant_id          UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    session_id         TEXT        NOT NULL,
    contact_id         UUID        NOT NULL,
    flow_id            TEXT        NOT NULL,
    flow_version       INTEGER     NOT NULL,
    current_node       TEXT        NOT NULL,
    vars               JSONB       NOT NULL DEFAULT '{}',
    last_wa_message_id TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, session_id, contact_id)
);

CREATE INDEX IF NOT EXISTS idx_flow_state_tenant
    ON public.flow_state (tenant_id);

COMMENT ON TABLE  public.flow_state IS 'Estado conversacional por (tenant, sesión, contact_id) (Plan 010, re-claveado desde 0004). Solo negocio (ADR-0009); nunca DEK ni store.';
COMMENT ON COLUMN public.flow_state.tenant_id          IS 'Tenant dueño de la conversación (FK a tenants).';
COMMENT ON COLUMN public.flow_state.session_id         IS 'Sesión CloudLink en la que vive la conversación (la provee el Edge).';
COMMENT ON COLUMN public.flow_state.contact_id         IS 'Identidad OPACA del contacto (contacts.contact_id); desacopla el motor del JID crudo (design.md §1, §2.2).';
COMMENT ON COLUMN public.flow_state.flow_id            IS 'Flujo que ejecuta la conversación.';
COMMENT ON COLUMN public.flow_state.flow_version       IS 'Versión con la que arrancó la conversación (no salta de versión; design.md §4).';
COMMENT ON COLUMN public.flow_state.current_node       IS 'Nodo actual de la máquina de estados (centinela de fin = conversación terminada).';
COMMENT ON COLUMN public.flow_state.vars               IS 'Variables recolectadas + contador de reprompt (design.md §10.E), en JSON.';
COMMENT ON COLUMN public.flow_state.last_wa_message_id IS 'wa_message_id del último entrante procesado (idempotencia).';
COMMENT ON COLUMN public.flow_state.updated_at         IS 'Última actualización del estado.';
