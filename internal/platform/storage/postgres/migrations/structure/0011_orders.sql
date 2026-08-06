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
--
-- ⚠️ GUARD `public.intakes` (Plan 041 · T1.0): esta tabla YA NO SE LLAMA `orders`.
-- La 0041 la renombra a `intakes` (D-12: el objeto de dominio es la SOLICITUD; el
-- módulo conversacional sigue siendo `cart`). Como el runner es FULL-REPLAY, sin
-- este guard el `CREATE TABLE IF NOT EXISTS public.orders` de abajo RESUCITARÍA
-- una `orders` VACÍA en cada corrida posterior al renombre, y acto seguido la 0041
-- reventaría al intentar renombrarla sobre una `intakes` que ya existe —
-- ABORTANDO EL ARRANQUE del servidor (fail-fast de bootstrap/database.go). Con el
-- guard, este archivo sigue siendo la definición reproducible de la tabla sobre
-- una base que aún no la tiene, y es un no-op limpio sobre una ya renombrada.
-- Mismo patrón que 0037_tenant_members.sql frente a la 0038.
-- ============================================================

DO $$
BEGIN
    -- Ya renombrada por la 0041: nada que crear (y nada que resucitar).
    IF to_regclass('public.intakes') IS NOT NULL THEN
        RETURN;
    END IF;

    CREATE TABLE IF NOT EXISTS public.orders (
        id          UUID        PRIMARY KEY,
        tenant_id   TEXT        NOT NULL,
        contact_id  TEXT        NOT NULL,      -- OPACO (Plan 010); NUNCA número/JID en claro
        session_id  TEXT        NOT NULL,
        status      TEXT        NOT NULL,      -- "open" | "closed" | "cancelled" | "expired"
        total       NUMERIC     NOT NULL DEFAULT 0,
        created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
        expires_at  TIMESTAMPTZ
    );

    -- Índice de REANUDACIÓN: recupera la orden "open" del contacto al reanudar la
    -- conversación. (Servía además la evaluación perezosa de expiración, derogada
    -- por el Plan 041 · T4.7 / D-041.16: nada vence por tiempo.)
    CREATE INDEX IF NOT EXISTS orders_open_idx
        ON public.orders (tenant_id, contact_id, status);
END $$;

-- Los COMMENT definitivos viven en la 0041, sobre los nombres finales
-- (public.intakes): esta tabla nace aquí y se renombra allí en la misma corrida.
