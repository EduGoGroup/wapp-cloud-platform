-- ============================================================
-- 0011: Órdenes del módulo Carrito (Plan 016 · T0, ADR-0009 / ADR-0010).
-- Tabla NUEVA orders: proyección tipada de la orden que el módulo cart cierra
-- (efecto cart_closed) sobre el outbox flow_events. Es DATO DE NEGOCIO EN CLARO
-- en Postgres cloud (ADR-0009: la nube aloja contenido de negocio; la DEK y el
-- store cifrado NUNCA salen del Edge, ADR-0007).
--
-- CERO PII: contact_id es la identidad OPACA (contacts.contact_id, Plan 010 /
-- ADR-0010: UUID sin número). Aquí NUNCA se guarda número/JID en claro ni ninguna
-- llave. Los ítems del pedido van en order_items (0012); el total es agregado.
--
-- Identidad de negocio (design.md §3.4): UNA orden "open" por (tenant_id,
-- contact_id); el índice orders_open_idx la recupera al reanudar y sirve la
-- evaluación de TTL. Los estados: "open" → "closed" | "cancelled" | "expired".
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE/INDEX IF NOT EXISTS
-- garantizan re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.orders (
    id          UUID        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    contact_id  TEXT        NOT NULL,          -- OPACO (Plan 010); NUNCA número/JID en claro
    session_id  TEXT        NOT NULL,
    status      TEXT        NOT NULL,          -- "open" | "closed" | "cancelled" | "expired"
    total       NUMERIC     NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ
);

-- Índice de REANUDACIÓN / TTL: recupera la orden "open" del contacto al reanudar
-- la conversación y sirve la evaluación perezosa de expiración (design.md §4.3).
CREATE INDEX IF NOT EXISTS orders_open_idx
    ON public.orders (tenant_id, contact_id, status);

COMMENT ON TABLE  public.orders IS 'Órdenes del módulo Carrito (proyección tipada de cart_closed) como datos de NEGOCIO EN CLARO (ADR-0009). CERO PII: la identidad la protege el contact_id opaco (ADR-0010). NUNCA DEK, store cifrado ni número/JID en claro.';
COMMENT ON COLUMN public.orders.id         IS 'Identidad de la orden (UUID; asignada al abrir la orden "open").';
COMMENT ON COLUMN public.orders.tenant_id  IS 'Tenant dueño de la orden.';
COMMENT ON COLUMN public.orders.contact_id IS 'Identidad OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010). NUNCA el número/JID en claro.';
COMMENT ON COLUMN public.orders.session_id IS 'Sesión (Edge/WhatsApp) que originó la orden; metadato de trazabilidad.';
COMMENT ON COLUMN public.orders.status     IS 'Estado del ciclo de vida: "open" | "closed" | "cancelled" | "expired".';
COMMENT ON COLUMN public.orders.total      IS 'Total agregado del pedido (suma de qty*unit_price de order_items). Dato de negocio.';
COMMENT ON COLUMN public.orders.created_at IS 'Momento del alta (apertura de la orden). Usa el DEFAULT now().';
COMMENT ON COLUMN public.orders.updated_at IS 'Momento de la última transición de estado. Usa el DEFAULT now() en el alta.';
COMMENT ON COLUMN public.orders.expires_at IS 'Instante de expiración por TTL (now + tenant_settings.order_ttl_seconds); NULL si no aplica. Evaluado perezosamente al reanudar (design.md §4.3).';
