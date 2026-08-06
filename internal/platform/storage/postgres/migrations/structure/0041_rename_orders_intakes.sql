-- ============================================================
-- 0041: `orders` → `intakes` y `order_items` → `intake_items`
-- (Plan 041 · T1.0, D-12 / ADR-0031).
--
-- POR QUÉ: pedido y presupuesto son EL MISMO OBJETO, y ese objeto se llama
-- SOLICITUD (`intake`). El nombre `orders` mentía en las dos direcciones: nombraba
-- «orden» algo que todavía puede ser un presupuesto sin confirmar, y arrastraba a
-- toda la API pública a hablar de órdenes cuando el negocio habla de solicitudes.
-- El renombre es de OBJETO, no de mecanismo: el módulo conversacional se sigue
-- llamando `cart` (D-12) porque el carrito es CÓMO se arma la solicitud, no lo que
-- la solicitud es.
--
-- QUÉ RENOMBRA, todo en el mismo archivo porque nada de esto viaja solo:
--   1. las dos tablas;
--   2. la columna FK `order_items.order_id` → `intake_items.intake_id` — así las
--      tablas del plan que apuntan a la cabecera (`intake_revisions.intake_id`,
--      `intake_buyer_data.intake_id`, design §3) la nombran TODAS igual;
--   3. índices, constraints (PK y FK) y la secuencia del BIGSERIAL: PostgreSQL
--      NO los arrastra al renombrar la tabla, y un `orders_pkey` colgando de
--      `intakes` es exactamente la clase de resto que después nadie se atreve a
--      tocar porque no sabe si algo depende de él.
--
-- IDEMPOTENTE, y aquí no es una formalidad: el runner es hash-based FULL-REPLAY
-- (re-aplica TODOS los structure/*.sql al cambiar el hash), así que este archivo
-- se ejecuta otra vez sobre una base YA renombrada en cada corrida futura. Cada
-- paso va con su guarda de existencia y no toca nada si ya está hecho. La otra
-- mitad de esa defensa vive en la 0011/0012, que llevan el guard que impide que
-- el replay resucite los nombres viejos (ver la nota larga en 0011_orders.sql).
--
-- SIN pérdida de datos y SIN reescritura: ALTER ... RENAME es un cambio de
-- catálogo, no mueve una sola fila. Las filas históricas siguen ahí tal cual.
-- ============================================================

DO $$
BEGIN
    -- 1) La cabecera. Solo si el nombre viejo existe y el nuevo todavía no:
    --    en una base ya migrada las dos condiciones fallan y esto es un no-op.
    IF to_regclass('public.orders') IS NOT NULL AND to_regclass('public.intakes') IS NULL THEN
        ALTER TABLE public.orders RENAME TO intakes;
    END IF;

    -- 2) Las líneas.
    IF to_regclass('public.order_items') IS NOT NULL AND to_regclass('public.intake_items') IS NULL THEN
        ALTER TABLE public.order_items RENAME TO intake_items;
    END IF;

    -- 3) La columna FK: `order_id` deja de existir; la línea apunta al intake.
    IF to_regclass('public.intake_items') IS NOT NULL THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'intake_items'
              AND column_name = 'order_id'
        ) AND NOT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'intake_items'
              AND column_name = 'intake_id'
        ) THEN
            ALTER TABLE public.intake_items RENAME COLUMN order_id TO intake_id;
        END IF;
    END IF;

    -- 4) Índices. `to_regclass` también resuelve índices (viven en pg_class).
    IF to_regclass('public.orders_open_idx') IS NOT NULL AND to_regclass('public.intakes_open_idx') IS NULL THEN
        ALTER INDEX public.orders_open_idx RENAME TO intakes_open_idx;
    END IF;
    IF to_regclass('public.order_items_order_idx') IS NOT NULL AND to_regclass('public.intake_items_intake_idx') IS NULL THEN
        ALTER INDEX public.order_items_order_idx RENAME TO intake_items_intake_idx;
    END IF;

    -- 5) Constraints: PostgreSQL las bautizó con el nombre viejo de la tabla y las
    --    deja intactas al renombrarla. Se comprueban contra pg_constraint por
    --    (nombre, tabla dueña) para no chocar con una homónima de otra tabla.
    IF to_regclass('public.intakes') IS NOT NULL THEN
        IF EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conname = 'orders_pkey' AND conrelid = 'public.intakes'::regclass
        ) THEN
            ALTER TABLE public.intakes RENAME CONSTRAINT orders_pkey TO intakes_pkey;
        END IF;
    END IF;

    IF to_regclass('public.intake_items') IS NOT NULL THEN
        IF EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conname = 'order_items_pkey' AND conrelid = 'public.intake_items'::regclass
        ) THEN
            ALTER TABLE public.intake_items RENAME CONSTRAINT order_items_pkey TO intake_items_pkey;
        END IF;
        IF EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conname = 'order_items_order_id_fkey' AND conrelid = 'public.intake_items'::regclass
        ) THEN
            ALTER TABLE public.intake_items
                RENAME CONSTRAINT order_items_order_id_fkey TO intake_items_intake_id_fkey;
        END IF;
    END IF;

    -- 6) La secuencia del BIGSERIAL. El DEFAULT de la columna la referencia por
    --    OID (regclass), así que sigue funcionando sola tras el renombre; se
    --    renombra para que `\d intake_items` no muestre un nombre que ya no existe
    --    en ninguna otra parte del esquema.
    IF to_regclass('public.order_items_id_seq') IS NOT NULL AND to_regclass('public.intake_items_id_seq') IS NULL THEN
        ALTER SEQUENCE public.order_items_id_seq RENAME TO intake_items_id_seq;
    END IF;
END $$;

-- COMMENT definitivos sobre los nombres finales (se movieron aquí desde la
-- 0011/0012: allí quedarían escritos sobre una tabla que ya no se llama así).
-- Son incondicionales a propósito: cuando esta línea se ejecuta, las tablas
-- existen sí o sí — o las creó la 0011/0012 en esta misma corrida, o ya estaban.
COMMENT ON TABLE  public.intakes IS 'SOLICITUD del cliente (pedido = presupuesto, ADR-0031 / Plan 041 D-12): la cabecera que el módulo cart proyecta al cerrar (cart_closed). Nació como `orders` en el Plan 016 y se renombró en la 0041 — el mecanismo se llama cart, el objeto se llama intake. Dato de NEGOCIO EN CLARO (ADR-0009). CERO PII: la identidad la protege el contact_id opaco (ADR-0010). NUNCA DEK, store cifrado ni número/JID en claro.';
COMMENT ON COLUMN public.intakes.id         IS 'Identidad de la solicitud (UUID; asignada al abrirla en estado "open").';
COMMENT ON COLUMN public.intakes.tenant_id  IS 'Tenant dueño de la solicitud.';
COMMENT ON COLUMN public.intakes.contact_id IS 'Identidad OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010). NUNCA el número/JID en claro.';
COMMENT ON COLUMN public.intakes.session_id IS 'Sesión (Edge/WhatsApp) que originó la solicitud; metadato de trazabilidad.';
COMMENT ON COLUMN public.intakes.status     IS 'Estado del ciclo de vida: "open" | "closed" | "cancelled" | "expired". Sin CHECK a propósito: el ciclo extendido del Plan 041 (D-041.10) añade estados y la validación vive en la máquina de estados del código, no en la tabla.';
COMMENT ON COLUMN public.intakes.total      IS 'Total agregado de la solicitud (suma de qty*unit_price de intake_items). Dato de negocio.';
COMMENT ON COLUMN public.intakes.created_at IS 'Momento del alta (apertura de la solicitud). Usa el DEFAULT now().';
COMMENT ON COLUMN public.intakes.updated_at IS 'Momento de la última transición de estado. Usa el DEFAULT now() en el alta.';
COMMENT ON COLUMN public.intakes.expires_at IS
    'HISTORICA. Ya NO se escribe (2026-08-05, noche) y NADIE la obedece: ninguna solicitud vence por tiempo. Las filas viejas conservan su valor tal cual.';

COMMENT ON TABLE  public.intake_items IS 'Líneas de la solicitud (una por artículo), proyección de cart_closed. Nació como `order_items` en el Plan 016 y se renombró en la 0041. Dato de NEGOCIO EN CLARO (ADR-0009): sku/label son códigos del catálogo del tenant, NO PII; la identidad la protege el contact_id opaco de intakes (ADR-0010). NUNCA DEK, store cifrado ni número/JID en claro.';
COMMENT ON COLUMN public.intake_items.id         IS 'Identidad técnica de la fila (append-only; sin significado de negocio).';
COMMENT ON COLUMN public.intake_items.intake_id  IS 'Solicitud (intakes.id) a la que pertenece la línea. Se llamaba order_id hasta la 0041.';
COMMENT ON COLUMN public.intake_items.sku        IS 'Código de artículo (catálogo del tenant), dato de negocio. NUNCA PII.';
COMMENT ON COLUMN public.intake_items.label      IS 'Etiqueta legible del artículo (catálogo del tenant). NUNCA PII.';
COMMENT ON COLUMN public.intake_items.qty        IS 'Cantidad pedida del artículo (qty>=1; sin validación de stock, Plan 016 design.md §9.D).';
COMMENT ON COLUMN public.intake_items.unit_price IS 'Precio unitario del artículo al momento de pedirlo. Dato de negocio.';
COMMENT ON COLUMN public.intake_items.added_at   IS 'Momento en que se añadió la línea. Usa el DEFAULT now().';
