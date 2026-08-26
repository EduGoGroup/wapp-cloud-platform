-- ============================================================
-- 0077: intake_type — EL DISCRIMINADOR QUE LE FALTA AL OBJETO
-- (Plan 044 · Ola 2 · T2.8; la gemela en `intake_jobs` la decidió D5 de Jhoan,
--  2026-08-24, D-044.39).
--
-- QUÉ ARREGLA, Y POR QUÉ NO ESTABA
-- ------------------------------------------------------------
-- `intakes` NO nació: la renombraron. La 0041:39 hace `ALTER TABLE public.orders
-- RENAME TO intakes` y ni una columna más — cambió el NOMBRE y no la FORMA. El
-- resultado es una tabla que se llama «solicitud» y solo sabe representar un
-- PEDIDO: no hay una sola columna donde escribir que esta fila es una reserva,
-- una cita o una incidencia, que son justo los otros usos que la doctrina de
-- niveles del ADR-0044 promete vender. Hoy el objeto es MONOMÓRFICO, y lo es en
-- silencio: nada falla, simplemente no se puede preguntar.
--
-- POR QUÉ AHORA Y NO CUANDO HAGA FALTA EL SEGUNDO TIPO
-- ------------------------------------------------------------
-- 🟢 Porque el backfill de hoy es de juguete y el de mañana no. Contadas en UAT el
-- 2026-08-24: 1 fila en `intakes`, 1 en `intake_items`, 2 en `intake_jobs`. Cada
-- semana de tráfico real encarece esta migración y estrecha la ventana en que
-- puede correr sin plan de contingencia. La deuda no es la columna que falta: es
-- el momento en que añadirla deja de ser gratis.
--
-- LAS DOS COLUMNAS VAN JUNTAS, Y ESO SE DECIDIÓ AQUÍ
-- ------------------------------------------------------------
-- `intake_jobs.intake_type` NO es adorno simétrico. El job sabe qué está
-- masticando ANTES de que exista el intake —el borrador lo escribe la Ola 3, y
-- para entonces P2/P3 ya han elegido prompt—, así que sin la columna en el job no
-- hay dónde leer el tipo en el único momento en que sirve para algo. Derivarlo por
-- join desde `intakes` es imposible por construcción: durante `aggregating`,
-- `pending` y `processing` el `intake_id` del job es NULL.
--
-- 🔴 EL ENUM SE ESCRIBE DOS VECES Y ESO ES UN RIESGO CONOCIDO. El plan avisa:
-- «dos definiciones que divergen en un valor son la forma clásica de que una se
-- quede atrás». Se elige la variante de DOS CHECK IDÉNTICOS con un test que los
-- COMPARA (TestIntegration_IntakeType_LosDosCheckSonIdenticos, que lee
-- pg_get_constraintdef de los dos y exige la misma cadena) en vez de un `DO $$ …
-- EXECUTE format(…)` que lo escribiría una sola vez: en 75 ficheros de
-- `structure/` NO hay una sola línea de SQL dinámico, y meter la primera aquí
-- cambiaría el estilo de todo el directorio por un enum de cuatro valores. La
-- divergencia queda cubierta por el test, no por la esperanza.
--
-- 🔴 EL CHECK NACE CON LOS CUATRO VALORES, NO CON UNO
-- ------------------------------------------------------------
-- Un CHECK cerrado en este repo SOLO se amplía EDITANDO SU PROPIA MIGRACIÓN,
-- jamás con una posterior: el runner es full-replay por nombre, así que una 0079
-- que ampliara el juego correría DESPUÉS de que esta misma 0077 hubiera vuelto a
-- imponer el juego viejo... sobre filas que el arranque anterior ya escribió con el
-- valor nuevo — y el ADD CONSTRAINT aborta el arranque del servidor. El precedente
-- de la casa es la 0075: sus OCHO motivos (D-044.36) se consiguieron EDITÁNDOLA a
-- ella misma, no con una 0076.
--
-- ⚠️ Y NO VALE COMO CONTRAEJEMPLO LA 0029, que sí amplió `fleet_sessions.state` con
-- una migración POSTERIOR y funciona. Se comprobó leyendo las dos, no de memoria:
-- allí el CHECK original vive INLINE dentro del `CREATE TABLE` de la 0003 (0003:54),
-- así que en el replay ese CREATE entero es no-op y el CHECK estrecho NO se vuelve a
-- imponer jamás — la 0029 se sale con la suya POR ESO, no porque la regla no exista.
-- Los dos CHECK de este fichero están FUERA del CREATE y se recrean en CADA replay
-- (regla 4, y es lo que se quiere): aquí la definición vieja SÍ vuelve a correr. La
-- misma decisión que hace robustos a estos CHECK es la que los hace inampliables
-- desde fuera.
--
-- Por eso los cuatro nombres del ADR-0044 entran hoy, aunque tres no tengan todavía
-- una línea de código que los escriba:
--
--     order        — pedido      (el único que existe hoy; TODO lo de UAT es esto)
--     booking      — reserva     (mesa, sala, plaza: hay hueco y se aparta)
--     appointment  — cita        (un hueco de agenda con persona y hora)
--     incident     — incidencia  (una avería, un reclamo: no se cobra, se atiende)
--
-- Nombres en INGLÉS porque lo son todos los valores cerrados de esta base
-- ('cart', 'paid', 'pending', 'active'/'passive'); mezclar castellano aquí
-- obligaría a recordar cuál tabla habla en qué idioma.
--
-- ------------------------------------------------------------
-- 🔴 LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- (1) LAS COLUMNAS NACEN SIN DEFAULT, y el DEFAULT se pone DESPUÉS del backfill.
--     No es cosmético aunque aquí el valor coincida: separa en dos sentencias dos
--     preguntas que NO son la misma —«qué reciben las filas que YA existían»
--     (backfill) y «qué reciben las que vengan» (DEFAULT)—. Escribirlo como un
--     `ADD COLUMN … NOT NULL DEFAULT 'order'` las funde en una y deja el fichero
--     sin un solo sitio donde leer la primera; el día que el segundo tipo exista y
--     el backfill tenga que MIRAR algo para decidir (como el `role` de la 0063),
--     quien edite esto encontrará el hueco ya abierto en vez de tener que partir
--     la sentencia en caliente. La casa ya pagó la confusión al revés: un DEFAULT
--     no gobierna a las filas viejas EN GENERAL, y predecir el resultado por él es
--     predecir mal.
--
-- (2) BACKFILL CON GUARD `WHERE intake_type IS NULL` — SÍ APLICA, y es la línea de
--     esta migración que más importa. Sin el guard, CADA REPLAY —o sea, cada vez
--     que cualquiera toque cualquier `structure/*.sql`— volcaría a 'order' todas
--     las filas, incluidas las reservas y las citas que para entonces existan. Un
--     `UPDATE … SET intake_type='order'` a secas es exactamente el accidente de la
--     0063 con otro traje. Lo vigila
--     TestIntegration_IntakeType_ElReplayNoPisaUnTipoYaEscrito.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ … IF is_nullable = 'YES'` — SÍ APLICA, en
--     las dos tablas. La columna no puede nacer NOT NULL (nace sin default sobre
--     tablas CON filas), así que se promueve al final, cuando el backfill ya no
--     dejó ningún NULL. El guard evita el escaneo completo de tabla en cada uno de
--     los replays siguientes.
--     🔴 Y el NOT NULL es lo que hace CIERTO el barrido del criterio (a): sin él,
--     `SELECT count(*) … WHERE intake_type IS NULL` podría dar > 0 mañana aunque
--     hoy diera 0.
--
-- (4) CHECK CON NOMBRE EXPLÍCITO, RECREADO EN CADA REPLAY — SÍ APLICA, dos veces.
--     `DROP CONSTRAINT IF EXISTS` + `ADD`, FUERA de cualquier `CREATE TABLE`: un
--     CHECK inline no se recrea nunca más porque del segundo arranque en adelante
--     el CREATE entero es no-op (0071:106-109). Los nombres siguen la convención
--     LOCAL de cada tabla, que divergen y no se unifican aquí: `intakes_*_chk`
--     (0048, 0054, 0055) y `intake_jobs_*_check` (0072).
--
-- ORDEN DE LAS SENTENCIAS, Y POR QUÉ ESTE
-- ------------------------------------------------------------
--   A) ADD COLUMN  → B) CHECK  → C) backfill  → D) DEFAULT  → E) SET NOT NULL
--   → F) COMMENT
-- El CHECK va ANTES del backfill y no pasa nada: con las filas todavía a NULL,
-- `NULL IN (…)` evalúa a NULL y un CHECK solo rechaza el FALSE explícito, así que
-- la validación del ADD CONSTRAINT pasa (mismo razonamiento que 0072:481-491).
-- Y los `COMMENT ON COLUMN` van AL FINAL, después del `ADD COLUMN`: un COMMENT
-- sobre una columna que no existe ABORTA EL ARRANQUE, y esta casa ya lo pagó.
--
-- LO QUE ESTA MIGRACIÓN NO TRAE, A PROPÓSITO
-- ------------------------------------------------------------
-- NO trae índice sobre `intake_type`: hoy no hay una sola consulta que filtre por
-- él (los tres tipos nuevos no tienen escritor), y un índice sin lector es peso
-- muerto en cada INSERT. NO trae campo en el struct `Intake` de Go ni en el job:
-- un símbolo nuevo sin consumidor es la otra forma de deuda que este repo ya tiene
-- catalogada — la columna la leerán P2/P3 cuando elijan prompt por tipo, y ese día
-- el campo nace con su llamante. Y NO trae `intake_items.intake_type`: la línea
-- hereda el tipo de su cabecera y duplicarlo abriría la puerta a que discrepen.
--
-- SIN BUMP DE SchemaVersion: el Plan 044 lo lleva al CIERRE, en T6.2 (INV-8). El
-- runner reejecuta por HASH, así que esta migración se aplica igual sin tocar la
-- constante.
-- ============================================================

-- ------------------------------------------------------------
-- A) LAS DOS COLUMNAS, SIN DEFAULT Y NULLables (regla 1)
-- ------------------------------------------------------------
ALTER TABLE public.intakes
    ADD COLUMN IF NOT EXISTS intake_type TEXT;
ALTER TABLE public.intake_jobs
    ADD COLUMN IF NOT EXISTS intake_type TEXT;

-- ------------------------------------------------------------
-- B) LOS DOS CHECK, IDÉNTICOS Y CON NOMBRE (regla 4)
--    Si tocas UNO, toca el OTRO: los compara un test.
-- ------------------------------------------------------------
ALTER TABLE public.intakes
    DROP CONSTRAINT IF EXISTS intakes_intake_type_chk;
ALTER TABLE public.intakes
    ADD CONSTRAINT intakes_intake_type_chk
    CHECK (intake_type IN ('order', 'booking', 'appointment', 'incident'));

ALTER TABLE public.intake_jobs
    DROP CONSTRAINT IF EXISTS intake_jobs_intake_type_check;
ALTER TABLE public.intake_jobs
    ADD CONSTRAINT intake_jobs_intake_type_check
    CHECK (intake_type IN ('order', 'booking', 'appointment', 'incident'));

-- ------------------------------------------------------------
-- C) EL BACKFILL — LAS FILAS QUE YA EXISTÍAN (regla 2)
--    Todo lo que hay hoy es un pedido: la tabla no sabía decir otra cosa.
--    🔴 El `WHERE intake_type IS NULL` NO ES OPCIONAL: sin él, cada replay
--    volcaría a 'order' las reservas y las citas que existan para entonces.
-- ------------------------------------------------------------
UPDATE public.intakes     SET intake_type = 'order' WHERE intake_type IS NULL;
UPDATE public.intake_jobs SET intake_type = 'order' WHERE intake_type IS NULL;

-- ------------------------------------------------------------
-- D) EL DEFAULT — LAS FILAS QUE VENGAN (regla 1)
--    Va DESPUÉS del backfill, y alcanza SOLO a los INSERT futuros que omitan la
--    columna. Que el valor coincida con el del backfill es una casualidad de hoy,
--    no la misma decisión.
-- ------------------------------------------------------------
ALTER TABLE public.intakes     ALTER COLUMN intake_type SET DEFAULT 'order';
ALTER TABLE public.intake_jobs ALTER COLUMN intake_type SET DEFAULT 'order';

-- ------------------------------------------------------------
-- E) LA PROMOCIÓN A NOT NULL, GUARDADA (regla 3)
--    Es lo que hace verdadero para siempre el barrido del criterio (a).
-- ------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'intakes'
                 AND column_name = 'intake_type' AND is_nullable = 'YES') THEN
        ALTER TABLE public.intakes ALTER COLUMN intake_type SET NOT NULL;
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'public' AND table_name = 'intake_jobs'
                 AND column_name = 'intake_type' AND is_nullable = 'YES') THEN
        ALTER TABLE public.intake_jobs ALTER COLUMN intake_type SET NOT NULL;
    END IF;
END $$;

-- ------------------------------------------------------------
-- F) LOS COMENTARIOS — AL FINAL, con las columnas ya creadas
-- ------------------------------------------------------------
COMMENT ON COLUMN public.intakes.intake_type IS
    'Discriminador de la solicitud: order|booking|appointment|incident (pedido|reserva|cita|incidencia, ADR-0044). Existe porque esta tabla nacio de un RENAME PURO de `orders` (0041:39) y hasta hoy solo sabia representar un pedido. CERRADO por intakes_intake_type_chk, gemelo EXACTO de intake_jobs_intake_type_check: si se amplia uno hay que ampliar el otro, y se amplia EDITANDO LA 0077 -- nunca con una migracion posterior, porque el runner es full-replay y la vieja correria despues abortando el arranque. DEFAULT ''order'' alcanza SOLO a las filas nuevas; las que ya existian las poblo el BACKFILL de esta misma migracion, y todas eran pedidos porque no habia forma de que fueran otra cosa. Plan 044 · T2.8.';

COMMENT ON COLUMN public.intake_jobs.intake_type IS
    'El tipo que el job esta masticando, con el MISMO juego de valores que intakes.intake_type y el mismo CHECK. NO es adorno simetrico y NO se deriva por join: durante aggregating/pending/processing el `intake_id` es NULL -- el intake todavia no existe--, y para entonces P2/P3 ya tienen que haber elegido prompt por tipo. Por eso el tipo se decide y se escribe AQUI, y el borrador de la Ola 3 lo hereda, no al reves. Decidido en la propia T2.8 (D5 de Jhoan, 2026-08-24, D-044.39). Plan 044 · T2.8.';
