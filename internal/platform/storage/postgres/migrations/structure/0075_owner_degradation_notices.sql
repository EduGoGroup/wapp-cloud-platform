-- ============================================================
-- 0075: owner_degradation_notices — EL AVISO AL DUEÑO CUANDO EL LLM SE CAE
-- (Plan 044 · Ola 1.5 · T1.5-4; D-044.32, REQ-38, ADR-0044 §5. INV-6.)
--
-- QUÉ GUARDA, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- Cuando el consumo del adaptador de la vía configurada FALLA —el Edge no
-- responde el frame de inferencia, el breaker está abierto, el proveedor externo
-- devuelve 5xx o la credencial dejó de valer—, el sistema degrada al Nivel A
-- (esa parte ya es la conducta: REQ-06/INV-10) y el dueño tiene que ENTERARSE.
-- Esta tabla es dónde se escribe ese enterarse.
--
--   * tenant_id    — de quién es el aviso. Sale del token / de la sesión, jamás
--                    del cuerpo de una petición (INV-7 / INV-8).
--   * reason       — POR QUÉ se degradó. Vocabulario CERRADO (ver el CHECK).
--   * via          — QUÉ vía falló (`local` | `api`), el mismo eje que
--                    tenant_llm.via de la 0073.
--   * window_start — inicio de la VENTANA en la que cae el fallo. Es la pieza
--     /window_end    del dedupe: ver el bloque grande de abajo.
--   * occurrences  — CUÁNTOS fallos colapsó este aviso. Un contador, no una
--                    lista: es lo que convierte «una degradación sostenida = UN
--                    aviso» en un aviso que además dice cuánto duró.
--   * read_at      — cuándo la leyó el dueño. NULL = sin leer.
--   * created_at   — cuándo nació el aviso (el PRIMER fallo de la ventana).
--   * last_seen_at — cuándo se vio el ÚLTIMO fallo que cayó en este aviso.
--
-- 🔴 NADIE LA PUEBLA EN ESTA OLA, Y ESO ES LA TAREA. T1.5-4 construye el PUNTO
-- DE INYECCIÓN: tabla, escritor y lectura. Los productores llegan en T1.6-6 (el
-- mapeo error-del-frame → motivo) y en la O2 (el pipeline). Una tabla vacía al
-- cerrar esta ola es el resultado ESPERADO, no un cabo suelto.
--
-- ============================================================
-- 🔴 INV-6 — CERO TEXTO LIBRE DEL CLIENTE, Y ES ESTRUCTURAL
-- ============================================================
-- Esta tabla NO TIENE UNA SOLA COLUMNA DONDE QUEPA UNA FRASE. Ni `message`, ni
-- `detail`, ni `error_text`, ni el teléfono, ni el JID, ni el id de sesión. Y la
-- ausencia es el mecanismo, no una omisión al escribir: lo que no tiene columna
-- no se puede filtrar por descuido, del mismo modo que la API key no puede
-- escaparse por `tenant_llm` porque el DTO no tiene dónde ponerla
-- (publicapi/tenantllm.go:90).
--
-- Todo lo que hay es: dos identificadores internos (`tenant_id`, `id`), dos
-- vocabularios CERRADOS acotados por CHECK (`reason`, `via`), cuatro instantes y
-- un entero. Ninguno de ellos puede contener nada que haya tecleado un cliente
-- final. Si algún día hace falta CONTEXTO, tiene que entrar como una columna
-- ESTRUCTURADA con su propio vocabulario cerrado — nunca como un TEXT libre, que
-- es el único formato en el que el contenido de una conversación puede colarse.
--
-- ⚠️ `session_id` SE CONSIDERÓ Y SE DEJÓ FUERA a propósito: sería operativamente
-- cómodo («¿qué teléfono se quedó sin servicio?») pero es un identificador que
-- apunta a un número de WhatsApp, y la degradación es un hecho DEL TENANT, no de
-- una conversación. Anotarlo aquí sería meter un puntero a PII en la única tabla
-- del plan que presume de no tener ninguno.
--
-- ============================================================
-- 🔴 EL DEDUPE VIVE EN LA BASE, NO EN GO — Y POR QUÉ LA VENTANA ES FIJA
-- ============================================================
-- REQ-38 pide que «una degradación sostenida produzca UN aviso por ventana, no
-- uno por mensaje». Hay dos formas de escribir eso y solo una sobrevive a dos
-- réplicas del servidor:
--
--   (A) VENTANA DESLIZANTE — «no escribas si ya hay un aviso de los últimos N
--       minutos». Es la intuitiva y es la que NO se puede pedir a la base: no
--       hay clave que indexar, así que el dedupe se convierte en un SELECT
--       seguido de un INSERT, y dos réplicas que atiendan dos fallos a la vez
--       leen «no hay aviso» las dos y escriben dos filas. Un dedupe que solo
--       vive en el código es un dedupe que se pierde el día que hay dos procesos.
--
--   (B) VENTANA FIJA (bucket) — el instante del fallo se TRUNCA a un múltiplo de
--       la ventana, y ese truncado ES parte de la clave. Como es una FUNCIÓN PURA
--       del instante, las dos réplicas calculan el MISMO `window_start` sin
--       hablar entre ellas, y el índice único de abajo hace que la segunda
--       escritura colapse sobre la primera. La base garantiza la invariante.
--
-- Se elige (B). El precio, dicho antes de que nadie lo descubra: DOS FALLOS
-- SEPARADOS POR UN SEGUNDO PERO A CABALLO DEL BORDE DEL BUCKET PRODUCEN DOS
-- AVISOS. Es real y es aceptado, porque a cambio se obtiene un techo DURO y
-- calculable: con la ventana de 15 minutos del escritor (degradation.VentanaPorDefecto)
-- el peor caso absoluto es 4 avisos por hora y por par (motivo, vía) — 96 al día
-- si TODO falla TODO el tiempo, que es un escenario en el que el dueño tiene un
-- problema mucho mayor que su bandeja. La ventana deslizante no da ese techo, y
-- además no lo da a cambio de nada, porque tampoco puede garantizarse.
--
-- ⚠️ EL TAMAÑO DE LA VENTANA NO ESTÁ EN ESTE ESQUEMA, y es deliberado: la tabla
-- guarda el intervalo YA CALCULADO (`window_start`/`window_end`), no la política
-- que lo calculó. Cambiar la ventana en el escritor NO corrompe nada ni exige
-- migración: re-agrupa a partir de ese momento y las filas viejas siguen
-- diciendo la verdad sobre el intervalo que las produjo.
--
-- ============================================================
-- LO QUE ESTA MIGRACIÓN **NO** HACE
-- ============================================================
-- * NO sube `SchemaVersion`. El Plan 044 hace UN SOLO bump al cierre, en T6.2
--   (INV-8). Esta tabla nace y el número no se mueve.
-- * NO cablea productor ninguno (T1.6-6 / O2).
-- * NO construye poda de retención, igual que `intakes`, `flow_events` y
--   `webhook_outbox` tras el ADR-0043: la retención es INDEFINIDA y es una
--   DECISIÓN, no un olvido. Aquí además el crecimiento está acotado POR
--   CONSTRUCCIÓN (el techo de 96 filas/día/tenant del párrafo del dedupe), que es
--   lo que hace que la decisión sea barata de mantener.
-- * NO añade una acción de «marcar como leída». La columna `read_at` existe
--   —para que el día que llegue el endpoint sea un handler y no una migración—
--   y hoy NINGÚN camino de código la escribe: toda fila nace y vive con NULL.
--
-- ------------------------------------------------------------
-- LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- (1) SIN DEFAULT en lo que no tiene valor «normal» — se aplica: `read_at` no
--     tiene default (un aviso no nace leído) y `window_start`/`window_end` los
--     pone SIEMPRE el escritor (un default `now()` ahí inventaría una ventana).
--     Sí tienen default `occurrences` (1: todo aviso nace con un fallo dentro),
--     `created_at`/`last_seen_at` (`now()`, el molde de la casa) e `id`.
-- (2) BACKFILL CON GUARDA — NO SE APLICA: la tabla nace vacía en esta migración,
--     no hay estado previo que reparar y no hay una sola sentencia que toque
--     DATOS. Es una migración puramente estructural.
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$` — NO SE APLICA: no se añade ninguna
--     columna a una tabla CON FILAS. Las `NOT NULL` NACEN en el `CREATE TABLE`,
--     que es el caso limpio (precedente: `intake_jobs`, 0072:394).
-- (4) CHECK CON NOMBRE EXPLÍCITO, FUERA DEL CREATE Y RECREADO EN CADA REPLAY —
--     **SE APLICA A LOS CUATRO**, con la corrección que la 0071 pagó con tiempo
--     real el 2026-08-22: `DROP CONSTRAINT IF EXISTS` + `ADD`, nunca inline. Un
--     CHECK inline dentro de un `CREATE TABLE IF NOT EXISTS` es NO-OP del segundo
--     arranque en adelante: la tabla ya existe, el CREATE no corre, y editar el
--     vocabulario en el fichero no cambia NADA en la base.
--
-- POR QUÉ CADA SENTENCIA ES IDEMPOTENTE (el replay corre en cada arranque que
-- cambie el hash del directorio de migraciones):
--   * `CREATE TABLE IF NOT EXISTS`      — NO-OP exacto del segundo arranque.
--   * `DROP CONSTRAINT IF EXISTS` + `ADD CONSTRAINT` — el par se recrea idéntico
--     cada vez; el DROP hace que el ADD nunca choque con un nombre ya tomado, y
--     esa recreación es justamente lo que hace que EDITAR un vocabulario aquí
--     surta efecto en la base (lo contrario del CHECK inline).
--   * `CREATE [UNIQUE] INDEX IF NOT EXISTS` con NOMBRE — NO-OP del segundo
--     arranque. 🔴 Este es el motivo por el que la unicidad del dedupe se pide
--     como ÍNDICE con nombre y no como `ALTER TABLE … ADD CONSTRAINT … UNIQUE`:
--     el `ADD CONSTRAINT UNIQUE` no admite `IF NOT EXISTS` y reventaría el
--     arranque en cuanto la constraint ya existiera, o exigiría un DROP+ADD que
--     RECONSTRUYE el índice entero en cada arranque. Con `CREATE UNIQUE INDEX IF
--     NOT EXISTS` no se duplica ni se reconstruye: se mira y se sigue.
--   * `COMMENT ON …` — reasigna el mismo texto; van AL FINAL, cuando tabla y
--     columnas existen con seguridad (un COMMENT sobre columna inexistente mata
--     el arranque).
--
-- ⚠️ ORDEN: independiente de la 0073/0074. No las toca, no las necesita y no las
-- contradice; solo comparte con la 0073 el vocabulario de la VÍA. ADITIVA.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE. Las ejecuta el barrido del CLI.
-- ------------------------------------------------------------
--
-- (V1) La tabla existe con sus diez columnas y su nulabilidad:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='owner_degradation_notices'
--    ORDER BY ordinal_position;
--
--   Esperado: id uuid NO gen_random_uuid() · tenant_id text NO · reason text NO ·
--   via text NO · window_start timestamptz NO · window_end timestamptz NO ·
--   occurrences integer NO 1 · read_at timestamptz **YES** · created_at
--   timestamptz NO now() · last_seen_at timestamptz NO now().
--   🔴 `read_at` es la ÚNICA NULLable, y su NULL SIGNIFICA «sin leer».
--
-- (V2) Los CUATRO CHECK existen con su nombre y con su texto:
--
--   SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
--    WHERE conrelid='public.owner_degradation_notices'::regclass AND contype='c'
--    ORDER BY conname;
--
--   Esperado CUATRO: …_occurrences_check · …_reason_check · …_ventana_check ·
--   …_via_check. Y el de `reason` tiene que listar LOS SEIS motivos, ni uno más.
--
-- (V3) EL DEDUPE ES DE LA BASE — la verificación que de verdad importa:
--
--   SELECT indexname, indexdef FROM pg_indexes
--    WHERE schemaname='public' AND tablename='owner_degradation_notices'
--    ORDER BY indexname;
--
--   Esperado TRES índices además de la PK: ux_owner_degradation_notices_ventana
--   (UNIQUE sobre tenant_id, reason, via, window_start),
--   idx_owner_degradation_notices_reciente e
--   idx_owner_degradation_notices_sin_leer (parcial, WHERE read_at IS NULL).
--   🔴 Si `ux_…_ventana` NO sale como UNIQUE, el dedupe NO existe: lo que quede
--   en Go se pierde con dos réplicas.
--
-- (V4) El dedupe funciona de verdad (N fallos ⇒ 1 fila). Ejecutar TRES veces
--      seguidas, con la misma ventana:
--
--   INSERT INTO public.owner_degradation_notices
--          (tenant_id, reason, via, window_start, window_end, last_seen_at)
--   VALUES ('t-verif','ollama_down','local',
--           '2026-08-23T10:00:00Z','2026-08-23T10:15:00Z', now())
--   ON CONFLICT (tenant_id, reason, via, window_start) DO UPDATE
--      SET occurrences = public.owner_degradation_notices.occurrences + 1
--   RETURNING occurrences;
--
--   Esperado: devuelve 1, luego 2, luego 3 — y
--   `SELECT count(*) FROM public.owner_degradation_notices WHERE tenant_id='t-verif'`
--   da **1**. Limpiar después con DELETE … WHERE tenant_id='t-verif'.
--
-- (V5) El vocabulario de motivos está CERRADO (un motivo sano no entra ni a mano):
--
--   INSERT INTO public.owner_degradation_notices
--          (tenant_id, reason, via, window_start, window_end)
--   VALUES ('t-verif','fastlane','local', now(), now() + interval '15 min');
--
--   Esperado: **ERROR** violación de owner_degradation_notices_reason_check.
--   Si esto INSERTA, el CHECK se coló dentro del CREATE TABLE y la regla (4)
--   está rota.
--
-- (V6) `SchemaVersion` NO se movió (INV-8) — se comprueba en el código, no aquí:
--   grep de SchemaVersion en internal/platform/storage/postgres: el mismo número
--   que antes de esta rama.
-- ============================================================

-- ------------------------------------------------------------
-- (a) LA TABLA. Todas las `NOT NULL` nacen aquí (regla 3: es el caso limpio, no
--     hay filas previas que promover). NINGÚN CHECK inline — van en (b).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.owner_degradation_notices (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    TEXT        NOT NULL,   -- del token/sesión, jamás del cuerpo (INV-7)
    reason       TEXT        NOT NULL,   -- vocabulario CERRADO: su CHECK va APARTE, en (b)
    via          TEXT        NOT NULL,   -- 'local' | 'api', el eje de tenant_llm.via (0073)
    window_start TIMESTAMPTZ NOT NULL,   -- inicio del bucket; parte de la clave del dedupe
    window_end   TIMESTAMPTZ NOT NULL,   -- fin del bucket (= window_start + ventana)
    occurrences  INTEGER     NOT NULL DEFAULT 1,  -- cuántos fallos colapsó este aviso
    read_at      TIMESTAMPTZ,            -- NULL = sin leer. La ÚNICA NULLable, a propósito
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ------------------------------------------------------------
-- (b) LOS CUATRO CHECK, con nombre y FUERA del CREATE, recreados en cada replay
--     (regla 4, con la corrección que la 0071 pagó cara).
-- ------------------------------------------------------------

-- (b.1) EL VOCABULARIO DE MOTIVOS. SEIS valores y NI UNO MÁS, y esta línea es la
--       mitad de la tarea T1.5-4: el aviso se escribe SOLO desde fallo-de-
--       adaptador, así que el motivo tiene que ser un enum cerrado y no un texto.
--       Los motivos SANOS de alto volumen —atajo determinista, fastlane, «sin
--       texto», umbral no alcanzado— NO ESTÁN AQUÍ y no pueden estar: avisar el
--       funcionamiento correcto mata el canal (D-044.32, misma razón por la que
--       el ADR-0038 desglosó sus siete motivos). Que ni siquiera quepan en la
--       columna es lo que hace que ese «no» sobreviva a un llamante despistado.
--
--       Los cuatro primeros son fallos de la vía LOCAL (el Edge y su Ollama), los
--       dos últimos de la vía API (el proveedor externo). ⚠️ El reparto es
--       DESCRIPTIVO, no exclusivo, y por eso NO hay un CHECK que ate motivo↔vía:
--       `timeout` es plausible en las dos vías (una llamada HTTP a un proveedor
--       también expira), y un CHECK de pares dejaría a T1.6-6 chocando contra el
--       esquema el día que mapee ese caso. La consistencia del par la decide
--       quien escribe, con el vocabulario de cada eje cerrado por separado.
--
--       🔴 HUECO DECLARADO — `lease_invalid`. REQ-34/T1.6-1 lo nombra como ERROR
--       DEL FRAME (`inference_result` lo puede devolver), y AQUÍ NO ESTÁ: el
--       mapeo error-del-frame → motivo-de-notificación es de **T1.6-6**, no de
--       esta tarea, y añadirlo por si acaso sería decidir por ella. Cuando T1.6-6
--       decida, o le asigna uno de estos seis o edita ESTA línea — y el replay la
--       aplica de verdad, que es para lo que el CHECK vive fuera del CREATE.
ALTER TABLE public.owner_degradation_notices
    DROP CONSTRAINT IF EXISTS owner_degradation_notices_reason_check;
ALTER TABLE public.owner_degradation_notices
    ADD CONSTRAINT owner_degradation_notices_reason_check CHECK (
        reason IN ('ollama_down','breaker_open','edge_offline','timeout',
                   'api_error','credencial')
    );

-- (b.2) EL VOCABULARIO DE LA VÍA. El MISMO que `tenant_llm_via_check` (0073),
--       escrito otra vez y no referenciado, porque en SQL no hay forma de
--       compartir un dominio entre dos tablas sin crear un TYPE — y un TYPE
--       enumerado es exactamente lo que este repo no usa (los vocabularios van en
--       CHECK con nombre, precedente 0071/0072/0073). El día que la vía crezca,
--       crecen las dos líneas: están las dos en `git grep "IN ('local','api')"`.
ALTER TABLE public.owner_degradation_notices
    DROP CONSTRAINT IF EXISTS owner_degradation_notices_via_check;
ALTER TABLE public.owner_degradation_notices
    ADD CONSTRAINT owner_degradation_notices_via_check CHECK (via IN ('local','api'));

-- (b.3) LA VENTANA ES UN INTERVALO, NO DOS FECHAS SUELTAS. Sin esto, una fila
--       con `window_end` anterior a `window_start` sería un aviso que dice haber
--       cubierto un intervalo negativo, y quien lo pinte en la consola tendría
--       que decidir qué hacer con eso. Estricto (`>`, no `>=`): una ventana de
--       duración cero no agrupa nada y no puede ser el resultado de un escritor
--       sano.
ALTER TABLE public.owner_degradation_notices
    DROP CONSTRAINT IF EXISTS owner_degradation_notices_ventana_check;
ALTER TABLE public.owner_degradation_notices
    ADD CONSTRAINT owner_degradation_notices_ventana_check CHECK (window_end > window_start);

-- (b.4) UN AVISO EXISTE PORQUE HUBO AL MENOS UN FALLO. `occurrences = 0` sería
--       un aviso de nada; negativo, un contador que alguien decrementó. Ninguno
--       de los dos es un estado al que se pueda llegar por un camino legítimo, y
--       por eso se prohíben en vez de documentarse.
ALTER TABLE public.owner_degradation_notices
    DROP CONSTRAINT IF EXISTS owner_degradation_notices_occurrences_check;
ALTER TABLE public.owner_degradation_notices
    ADD CONSTRAINT owner_degradation_notices_occurrences_check CHECK (occurrences >= 1);

-- ------------------------------------------------------------
-- (c) LOS ÍNDICES. El primero NO es una optimización: ES el dedupe.
-- ------------------------------------------------------------

-- (c.1) 🔴 LA INVARIANTE DE REQ-38, ESCRITA DONDE NO SE PUEDE PERDER. La clave es
--       (tenant, motivo, vía, inicio-de-ventana): una degradación sostenida cae
--       toda en el mismo bucket, choca contra este índice y el `ON CONFLICT DO
--       UPDATE` del escritor la colapsa en la fila que ya estaba, subiendo
--       `occurrences`. UN aviso, aunque fallen mil mensajes.
--
--       Va como ÍNDICE ÚNICO con nombre y `IF NOT EXISTS`, y no como `ALTER TABLE
--       … ADD CONSTRAINT … UNIQUE`, por el replay: el ADD CONSTRAINT no admite
--       `IF NOT EXISTS`, así que el segundo arranque o revienta o exige un
--       DROP+ADD que reconstruye el índice entero cada vez. Aquí el segundo
--       arranque mira y sigue.
--
--       ⚠️ El `ON CONFLICT (tenant_id, reason, via, window_start)` del store
--       DEPENDE de que este índice exista con EXACTAMENTE estas cuatro columnas y
--       en este orden: Postgres resuelve el arbitrio por inferencia, y sin índice
--       que lo case el INSERT falla en tiempo de ejecución. Cambiar una cosa
--       obliga a cambiar la otra en el mismo commit.
CREATE UNIQUE INDEX IF NOT EXISTS ux_owner_degradation_notices_ventana
    ON public.owner_degradation_notices (tenant_id, reason, via, window_start);

-- (c.2) La lectura de la consola/app: los avisos del tenant, el más reciente
--       primero. Es el ORDER BY exacto del GET, y por eso el índice lleva el DESC
--       escrito: un índice ascendente también serviría (Postgres lo recorre al
--       revés), pero dejarlo explícito hace que el plan no dependa de esa
--       simetría el día que se añada un segundo criterio de orden.
CREATE INDEX IF NOT EXISTS idx_owner_degradation_notices_reciente
    ON public.owner_degradation_notices (tenant_id, window_start DESC);

-- (c.3) El filtro `?unread=true` — parcial, porque la pregunta del teléfono es
--       «¿tengo algo pendiente?» y la respuesta vive en un puñado de filas aunque
--       el histórico crezca. Un índice parcial sobre `read_at IS NULL` no paga
--       por las leídas, que son las que se acumulan.
--
--       ⚠️ HOY ESTE ÍNDICE CUBRE LA TABLA ENTERA, porque nada escribe `read_at`
--       todavía (no hay endpoint de marcar-como-leída: lo pedirá el Plan 045/047).
--       Se crea igual: el día que ese endpoint llegue no debe traer migración.
CREATE INDEX IF NOT EXISTS idx_owner_degradation_notices_sin_leer
    ON public.owner_degradation_notices (tenant_id, window_start DESC)
    WHERE read_at IS NULL;

-- ------------------------------------------------------------
-- (d) LOS COMENTARIOS. Al final: aquí tabla y columnas existen con seguridad (un
--     COMMENT sobre una columna inexistente mata el arranque).
-- ------------------------------------------------------------
COMMENT ON TABLE public.owner_degradation_notices IS
    'Avisos al DUENO de que el LLM se degrado al Nivel A (Plan 044 · T1.5-4, D-044.32, REQ-38). Se escribe SOLO desde fallo-de-adaptador -- frame sin respuesta, breaker abierto, proveedor 5xx, credencial rota --, NUNCA desde un motivo sano (atajo determinista, fastlane, sin texto, umbral no alcanzado): avisar el funcionamiento correcto mata el canal. UNA degradacion sostenida = UN aviso: el dedupe lo garantiza el indice UNICO ux_owner_degradation_notices_ventana sobre (tenant_id, reason, via, window_start), no el codigo -- un dedupe que solo viva en Go se pierde con dos replicas. 🔴 CERO PII y CERO TEXTO LIBRE (INV-6): no hay una sola columna donde quepa una frase, y esa ausencia es el mecanismo. En la Ola 1.5 NADIE la puebla: los productores llegan en T1.6-6 y O2; la entrega push al telefono es del Plan 045/047.';
COMMENT ON COLUMN public.owner_degradation_notices.id IS
    'Identificador del aviso. UUID como el resto de las tablas recientes del repo (intake_jobs, 0072): opaco, no enumerable y no revela volumen. Viaja al wire como string.';
COMMENT ON COLUMN public.owner_degradation_notices.tenant_id IS
    'Tenant dueno del aviso (TEXT sin FK, convencion de la ficha 3). Sale del TOKEN o de la sesion que fallo, JAMAS del cuerpo de una peticion (INV-7 / INV-8). No es PK: un tenant tiene muchos avisos.';
COMMENT ON COLUMN public.owner_degradation_notices.reason IS
    'POR QUE se degrado. Vocabulario CERRADO de SEIS, acotado por owner_degradation_notices_reason_check -- fallo de la via LOCAL: ollama_down | breaker_open | edge_offline | timeout; fallo de la via API: api_error | credencial. Los motivos SANOS no estan y no pueden estar (D-044.32). 🔴 HUECO DECLARADO: lease_invalid es un error del FRAME (REQ-34/T1.6-1) y todavia no tiene motivo de notificacion asignado -- lo decide T1.6-6, no esta migracion.';
COMMENT ON COLUMN public.owner_degradation_notices.via IS
    'QUE VIA fallo: local (el Ollama del Edge del propio tenant) | api (el proveedor externo). Mismo eje y mismo vocabulario que tenant_llm.via (0073), repetido en un CHECK propio porque SQL no comparte dominios entre tablas sin un TYPE, y este repo no usa TYPE enumerados. ⚠️ NO hay CHECK que ate motivo-a-via: el reparto de arriba es descriptivo, no exclusivo (timeout es plausible en las dos), y atarlo dejaria a T1.6-6 chocando contra el esquema.';
COMMENT ON COLUMN public.owner_degradation_notices.window_start IS
    'Inicio de la VENTANA FIJA (bucket) en la que cae el fallo: el instante del fallo TRUNCADO a un multiplo de la ventana del escritor (hoy 15 min, degradation.VentanaPorDefecto). 🔴 ES PARTE DE LA CLAVE DEL DEDUPE, y por eso la ventana es fija y no deslizante: al ser una funcion PURA del instante, dos replicas calculan el mismo valor sin hablar entre ellas y el indice unico colapsa la segunda escritura. Precio aceptado: dos fallos separados por un segundo pero a caballo del borde producen DOS avisos. A cambio, techo duro de 4 avisos/hora por par (motivo, via).';
COMMENT ON COLUMN public.owner_degradation_notices.window_end IS
    'Fin de la ventana (= window_start + la ventana vigente cuando se escribio). Se GUARDA en vez de recalcularse para que la fila siga diciendo la verdad si algun dia cambia el tamano de la ventana: cambiarlo re-agrupa a partir de entonces y NO exige migracion ni corrompe lo escrito. owner_degradation_notices_ventana_check exige window_end > window_start.';
COMMENT ON COLUMN public.owner_degradation_notices.occurrences IS
    'Cuantos fallos colapso este aviso. Un CONTADOR, no una lista: es lo que convierte "una degradacion sostenida = UN aviso" en un aviso que ademas dice cuanto duro. Nace en 1 y lo sube el ON CONFLICT DO UPDATE del store. >= 1 por CHECK: un aviso existe porque hubo al menos un fallo.';
COMMENT ON COLUMN public.owner_degradation_notices.read_at IS
    'Cuando el dueno leyo el aviso. NULL = SIN LEER, y es la unica columna NULLable de la tabla. Es un instante y no un booleano porque responde a las dos preguntas -- "?leida?" (IS NOT NULL) y "?cuando?" -- con una sola columna, y porque un booleano obligaria a una segunda columna el dia que alguien pregunte lo segundo. ⚠️ HOY NINGUN camino de codigo la escribe: el endpoint de marcar-como-leida lo pide el Plan 045/047. La columna existe para que ese dia sea un handler y no una migracion.';
COMMENT ON COLUMN public.owner_degradation_notices.created_at IS
    'Momento del PRIMER fallo de la ventana, o sea el nacimiento del aviso. Usa el DEFAULT now(). NO se pisa en el ON CONFLICT: el aviso nacio cuando nacio, aunque siga acumulando fallos.';
COMMENT ON COLUMN public.owner_degradation_notices.last_seen_at IS
    'Momento del ULTIMO fallo que cayo dentro de este aviso. Junto con created_at dice cuanto lleva durando la degradacion, que es la informacion que el dueno necesita para saber si sigue rota o fue un pico. Se pisa en cada ON CONFLICT con GREATEST, no con asignacion directa: dos replicas pueden escribir fuera de orden y un aviso no debe RETROCEDER en el tiempo.';
