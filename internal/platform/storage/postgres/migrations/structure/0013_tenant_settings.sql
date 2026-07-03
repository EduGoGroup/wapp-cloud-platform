-- ============================================================
-- 0013: Config por-tenant del módulo Carrito (Plan 016 · T0, ADR-0009).
-- Tabla NUEVA tenant_settings: config ligera por-tenant (tamaño de página de la
-- paginación del carrito y TTL de la orden). Es DATO DE NEGOCIO EN CLARO en
-- Postgres cloud (ADR-0009: la nube aloja contenido de negocio; la DEK y el store
-- cifrado NUNCA salen del Edge, ADR-0007).
--
-- CERO PII / CERO llaves: aquí solo vive configuración operativa del tenant.
--
-- DEFAULTS embebidos (design.md §9.E/§9.G): page_size=5, order_ttl_seconds=3600.
-- Si el tenant NO tiene fila, el store devuelve estos mismos defaults (el carrito
-- funciona sin configurar nada). Es el GERMEN del futuro tenant_integrations
-- (endpoint CRM + credenciales cifradas), diferido (design.md §10).
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE IF NOT EXISTS garantiza
-- re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_settings (
    tenant_id          TEXT    PRIMARY KEY,
    page_size          INTEGER NOT NULL DEFAULT 5,
    order_ttl_seconds  INTEGER NOT NULL DEFAULT 3600   -- el store lo mapea a time.Duration
);

COMMENT ON TABLE  public.tenant_settings IS 'Config ligera por-tenant del módulo Carrito (paginación + TTL de orden) como dato de NEGOCIO EN CLARO (ADR-0009). Germen del futuro tenant_integrations (diferido). NUNCA PII ni llaves.';
COMMENT ON COLUMN public.tenant_settings.tenant_id         IS 'Tenant dueño de la config (PK).';
COMMENT ON COLUMN public.tenant_settings.page_size         IS 'Ítems por página de la paginación del carrito (default 5, design.md §9.E).';
COMMENT ON COLUMN public.tenant_settings.order_ttl_seconds IS 'TTL de la orden en segundos (default 3600); el store lo mapea a time.Duration (design.md §9.G).';
