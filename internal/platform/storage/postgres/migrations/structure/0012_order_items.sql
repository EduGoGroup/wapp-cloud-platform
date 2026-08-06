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
--
-- ⚠️ GUARD `public.intake_items` (Plan 041 · T1.0): esta tabla YA NO SE LLAMA
-- `order_items`, y su FK ya no se llama `order_id`. La 0041 renombra ambas cosas
-- (D-12). El guard es obligatorio por la misma razón que en la 0011: el runner es
-- FULL-REPLAY y sin él este archivo resucitaría una `order_items` vacía en cada
-- corrida, dejando a la 0041 sin poder renombrar y abortando el arranque. Ver la
-- nota larga en 0011_orders.sql.
-- ============================================================

DO $$
BEGIN
    -- Ya renombrada por la 0041: nada que crear (y nada que resucitar).
    IF to_regclass('public.intake_items') IS NOT NULL THEN
        RETURN;
    END IF;

    CREATE TABLE IF NOT EXISTS public.order_items (
        id          BIGSERIAL   PRIMARY KEY,
        order_id    UUID        NOT NULL REFERENCES public.orders(id),
        sku         TEXT        NOT NULL,      -- código de negocio; cero PII
        label       TEXT        NOT NULL,
        qty         INTEGER     NOT NULL,
        unit_price  NUMERIC     NOT NULL,
        added_at    TIMESTAMPTZ NOT NULL DEFAULT now()
    );

    -- Índice de JOIN / AGREGACIÓN: sirve la lectura de las líneas por orden y el
    -- GROUP BY del total — el valor consultable de la tabla (design.md §9.F).
    CREATE INDEX IF NOT EXISTS order_items_order_idx
        ON public.order_items (order_id);
END $$;

-- Los COMMENT definitivos viven en la 0041, sobre los nombres finales
-- (public.intake_items): esta tabla nace aquí y se renombra allí en la misma corrida.
