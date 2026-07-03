-- ============================================================
-- 0010: Contenido de negocio por-tenant (Plan 015 · T2, ADR-0009).
-- Tabla NUEVA tenant_content: blobs JSONB de contenido dinámico por tenant y
-- referencia (prompt/options/items de nodos interactivos, catálogos de pedido),
-- que el adapter content.JSON (T1) lee por (tenant_id, ref). Es DATO DE NEGOCIO
-- EN CLARO en Postgres cloud (ADR-0009: la nube aloja contenido de negocio; la
-- DEK y el store cifrado NUNCA salen del Edge, ADR-0007).
--
-- CERO PII / CERO llaves: aquí vive contenido de plantilla del tenant, nunca
-- número/JID en claro ni material criptográfico.
--
-- PK COMPUESTA (tenant_id, ref): un blob por referencia y tenant; el upsert lo
-- resuelve el repositorio.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE IF NOT EXISTS garantiza
-- re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_content (
    tenant_id   TEXT        NOT NULL,
    ref         TEXT        NOT NULL,
    content     JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, ref)
);

COMMENT ON TABLE  public.tenant_content IS 'Blobs de contenido de negocio por-tenant (JSONB) leídos por el adapter content.JSON vía (tenant_id, ref). Dato de NEGOCIO EN CLARO (ADR-0009). NUNCA PII, número/JID en claro ni llaves.';
COMMENT ON COLUMN public.tenant_content.tenant_id  IS 'Tenant dueño del contenido.';
COMMENT ON COLUMN public.tenant_content.ref        IS 'Referencia lógica del blob (Node.Content.Ref).';
COMMENT ON COLUMN public.tenant_content.content    IS 'Blob de contenido (JSONB): prompt/options/items del nodo. Dato de negocio, NUNCA PII ni llaves.';
COMMENT ON COLUMN public.tenant_content.created_at IS 'Momento del alta. Usa el DEFAULT now().';
COMMENT ON COLUMN public.tenant_content.updated_at IS 'Momento de la última escritura (upsert). Usa el DEFAULT now() en el alta.';
