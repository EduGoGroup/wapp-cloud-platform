-- ============================================================
-- 0012: Líneas de orden del módulo Carrito (Plan 016 · T0, ADR-0009 / ADR-0010).
-- Tabla NUEVA order_items: las líneas del pedido (una por artículo) que el módulo
-- cart proyecta desde cart_closed junto con orders (0011). Es DATO DE NEGOCIO EN
-- CLARO en Postgres cloud (ADR-0009: la nube aloja contenido de negocio; la DEK y
-- el store cifrado NUNCA salen del Edge, ADR-0007).
--
-- CERO PII: sku/label son CÓDIGOS de negocio (catálogo del tenant), NO PII. La
-- identidad ya la protege el contact_id opaco de orders (ADR-0010). Aquí NUNCA se
-- guarda número/JID en claro ni ninguna llave.
--
-- FK a orders(id): cada línea pertenece a una orden; el índice order_items_order_idx
-- sirve el join / la agregación (GROUP BY) que es el valor consultable de la tabla
-- (design.md §9.F).
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica todos
-- los structure/*.sql al cambiar el hash); CREATE TABLE/INDEX IF NOT EXISTS
-- garantizan re-aplicación N veces sin daño. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.order_items (
    id          BIGSERIAL   PRIMARY KEY,
    order_id    UUID        NOT NULL REFERENCES public.orders(id),
    sku         TEXT        NOT NULL,          -- código de negocio; cero PII
    label       TEXT        NOT NULL,
    qty         INTEGER     NOT NULL,
    unit_price  NUMERIC     NOT NULL,
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Índice de JOIN / AGREGACIÓN: sirve la lectura de las líneas por orden y el
-- GROUP BY del total — el valor consultable de la tabla (design.md §9.F).
CREATE INDEX IF NOT EXISTS order_items_order_idx
    ON public.order_items (order_id);

COMMENT ON TABLE  public.order_items IS 'Líneas de orden del módulo Carrito (proyección de cart_closed) como datos de NEGOCIO EN CLARO (ADR-0009). sku/label son códigos de negocio, NO PII; la identidad la protege el contact_id opaco de orders (ADR-0010). NUNCA DEK, store cifrado ni número/JID en claro.';
COMMENT ON COLUMN public.order_items.id         IS 'Identidad técnica de la fila (append-only; sin significado de negocio).';
COMMENT ON COLUMN public.order_items.order_id   IS 'Orden (orders.id) a la que pertenece la línea.';
COMMENT ON COLUMN public.order_items.sku        IS 'Código de artículo (catálogo del tenant), dato de negocio. NUNCA PII.';
COMMENT ON COLUMN public.order_items.label      IS 'Etiqueta legible del artículo (catálogo del tenant). NUNCA PII.';
COMMENT ON COLUMN public.order_items.qty        IS 'Cantidad pedida del artículo (qty>=1; sin validación de stock, design.md §9.D).';
COMMENT ON COLUMN public.order_items.unit_price IS 'Precio unitario del artículo al momento del pedido. Dato de negocio.';
COMMENT ON COLUMN public.order_items.added_at   IS 'Momento en que se añadió la línea. Usa el DEFAULT now().';
