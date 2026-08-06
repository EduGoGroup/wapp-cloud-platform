-- ============================================================
-- 0013: Config por-tenant del módulo Carrito (Plan 016 · T0, ADR-0009).
-- Tabla NUEVA tenant_settings: config ligera por-tenant (tamaño de página de la
-- paginación del carrito y —hasta el Plan 041— TTL de la orden). Es DATO DE
-- NEGOCIO EN CLARO en Postgres cloud (ADR-0009: la nube aloja contenido de
-- negocio; la DEK y el store cifrado NUNCA salen del Edge, ADR-0007).
--
-- CERO PII / CERO llaves: aquí solo vive configuración operativa del tenant.
--
-- DEFAULTS embebidos (design.md §9.E/§9.G): page_size=5, order_ttl_seconds=3600.
-- Si el tenant NO tiene fila, el store devuelve estos mismos defaults (el carrito
-- funciona sin configurar nada). Es el GERMEN del futuro tenant_integrations
-- (endpoint CRM + credenciales cifradas), diferido (design.md §10).
--
-- ⚠️ order_ttl_seconds está DEROGADA como causa de muerte desde el Plan 041 · O4 ·
-- T4.7 (D-041.16, 2026-08-05): la columna se queda y se sigue leyendo, pero ya no
-- la obedece nadie. La columna NO se tira porque el runner es FULL-REPLAY y un
-- DROP obligaría a toda base vacía a crear-y-tirar, además de desalinear
-- TenantSettings con los tenants que ya tienen valor puesto. Su COMMENT —abajo—
-- se reescribió AQUÍ, en el archivo que la creó, para que este fichero siga
-- siendo autoritativo sobre su propia columna en cada replay.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE IF NOT EXISTS garantiza
-- re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_settings (
    tenant_id          TEXT    PRIMARY KEY,
    page_size          INTEGER NOT NULL DEFAULT 5,
    order_ttl_seconds  INTEGER NOT NULL DEFAULT 3600   -- DEROGADA (T4.7): se lee, no se obedece
);

COMMENT ON TABLE  public.tenant_settings IS 'Config ligera por-tenant del módulo Carrito como dato de NEGOCIO EN CLARO (ADR-0009). Germen del futuro tenant_integrations (diferido). NUNCA PII ni llaves.';
COMMENT ON COLUMN public.tenant_settings.tenant_id         IS 'Tenant dueño de la config (PK).';
COMMENT ON COLUMN public.tenant_settings.page_size         IS 'Ítems por página de la paginación del carrito (default 5, design.md §9.E).';
COMMENT ON COLUMN public.tenant_settings.order_ttl_seconds IS
    'DEROGADO como causa de muerte (2026-08-05, noche): ya NO vence ninguna solicitud. Evento y pedido son la misma cosa y los objetos de negocio no mueren por tiempo, mueren por accion humana. Se sigue LEYENDO (TenantSettings.OrderTTL) por compatibilidad y NO se obedece: ningun codigo actua sobre este valor. El unico reloj de conversacion es event_inactivity_ttl_seconds (Plan 043); el pedido huerfano lo descarta el dueno a mano (D-041.18).';
