-- ============================================================
-- 0072: intake_jobs + tenant_settings.aggregation_window_seconds
-- LA COLA DEL PIPELINE DE CAPTACIÓN, Y EL RELOJ QUE LA CIERRA
-- (Plan 044 · Ola 1 · T1.0; design §6.2, D-044.25 y D-044.26. ADR-0003 «sin
--  broker», ADR-0017 contact_id opaco, ADR-0034 «nada sensible en claro»).
--
-- QUÉ GUARDA, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- Un cliente no pide un presupuesto en un mensaje: lo pide en cinco seguidos.
-- `intake_jobs` es LA FILA QUE EXISTE PARA QUE EL TRABAJO CARO LO HAGA OTRO,
-- DESPUÉS — el mismo papel que `webhook_outbox` (0046) desempeña para el Plan 042.
-- El `AggregatorSink` (T1.5), en línea con el entrante, ejecuta UNA sentencia y
-- ninguna lectura: un `INSERT … ON CONFLICT DO UPDATE` que ABRE la ventana si no
-- existía y le AÑADE la referencia del mensaje si ya existía. Todo lo demás
-- —leer el hilo, descifrarlo, componer el literal, llamar al LLM— ocurre AL
-- FLUSH, fuera del camino del mensaje del cliente (D-044.26).
--
--   * id                 — identidad propia del job. UUID, `gen_random_uuid()`.
--   * tenant_id          — dueño. TEXT, convención de la ficha 3 (ver divergencia abajo).
--   * session_id         — sesión de la flota por la que entró.
--   * contact_id         — identificador OPACO del contacto (ADR-0017). NO es un teléfono.
--   * event_id           — evento de conversación (Plan 043). FK LÓGICA, sin FK física.
--   * status             — máquina de estados. Vocabulario CERRADO (ver el CHECK).
--   * stage              — etapa dentro de `processing`. CERRADO y NULLable.
--   * message_ts         — ts del PRIMER mensaje de la ventana (base de fechas, D-044.9).
--   * source_text_enc    — envelope AES-256-GCM del literal compuesto AL FLUSH.
--   * source_text_dek    — DEK por-fila (32B), envuelta por la KEK maestra.
--   * source_text_kek_id — key_id de la KEK que envolvió la DEK (discriminador del Rekey).
--   * source_refs        — `wa_message_id`s de la ventana. Identificadores opacos.
--   * artifacts          — salidas versionadas de cada paso del pipeline.
--   * error              — última causa de fallo, para el operador.
--   * intake_id          — borrador creado al final (FK LÓGICA, la escribe la Ola 3).
--
-- Y lo que este fichero toca FUERA de `intake_jobs`, que ya NO es «una sola
-- columna» (lo fue hasta que la sección F entró el 2026-08-22):
--
--   * tenant_settings.aggregation_window_seconds — cuántos segundos espera el
--     agregador antes de cerrar la ventana y disparar el pipeline (T1.2). Es la
--     sección E, y toca la tabla de la 0013.
--   * conversation_event_messages — sus DOS CHECK se recrean con un `entry_kind`
--     nuevo, `message_out_of_turn`. Es la sección F, y toca la tabla de la 0051.
--
-- 🔴 Y UNA TERCERA COSA, AÑADIDA EL 2026-08-22 POR T1.6 (sección F). NO estaba
-- prevista en T1.0 y por eso se avisa aquí arriba: `conversation_event_messages`
-- gana un cuarto `entry_kind`, `message_out_of_turn`, que es la MARCA del saliente
-- fuera de turno (D-044.24). Cuesta DDL porque el vocabulario de esa columna es
-- CERRADO y su CHECK está congelado dentro de un `CREATE TABLE IF NOT EXISTS`. Van
-- DOS constraints, no una —el vocabulario y el CHECK de GRADO del ADR-0034—, y el
-- porqué entero está en la sección F. Se mete aquí, y no en un `0073`, porque este
-- fichero es de la MISMA ola y todavía NO SE HA DESPLEGADO: no hay ninguna base con
-- la 0072 aplicada a la que este añadido llegue tarde.
--
-- 🔴 POR QUÉ LAS DOS COSAS VAN EN UN SOLO FICHERO (D-044.25)
-- ------------------------------------------------------------
-- No es comodidad, es una corrección de secuenciación. El §6.2 encabezaba esta
-- tabla como «migración nueva — O2» mientras el `AggregatorSink` de la **O1** la
-- escribía: la Ola 1 no puede insertar en una tabla que crea la Ola 2. Y la
-- columna de `tenant_settings` NO TENÍA DUEÑO —el enunciado la dejaba «en la
-- migración de la ola siguiente o en la de T0.3», y T0.3 ya cerró con la 0071—.
-- Las dos se adelantan aquí, en un solo fichero, que además respeta el «una sola
-- migración extra máximo» que pide el propio T1.2. Son la misma unidad de
-- despliegue porque el agregador NO ARRANCA sin las dos: sin la tabla no tiene
-- dónde encolar y sin la columna no sabe cuándo cerrar.
--
-- 🔴 EL SOBRE ES DE TRES PIEZAS Y ES NULLable, AL REVÉS QUE EL DE LA 0071
-- ------------------------------------------------------------
-- El §6.2 dibujaba `source_text TEXT` con el comentario «CIFRADO (KEK)», y en
-- esta casa un campo cifrado no es una columna, SON TRES:
-- `crypto.FieldCipher.Encrypt` devuelve `(valueEnc, valueDEK, keyID)` y las tres
-- hay que persistirlas o la fila es INDESCIFRABLE. Es literalmente la nota que la
-- 0071 escribió al dividir su `api_key_enc` (bloque «EL DESIGN §6.1 DIBUJABA UNA
-- SOLA COLUMNA»), y no es una desviación del diseño: es el diseño escrito contra
-- la firma real de la casa.
--
-- Pero la 0071 promovió su trío a `NOT NULL` y AQUÍ ESO SERÍA EL ERROR, porque el
-- argumento se invierte entero:
--
--   una fila de `tenant_llm` sin clave no significa nada; una fila de
--   `intake_jobs` sin sobre significa LA VENTANA ESTÁ ABIERTA, que es el estado
--   normal y mayoritario de esta tabla. El sink NO compone el literal en línea
--   (D-044.26): si no lee, no puede concatenar, y no se puede añadir texto a un
--   blob cifrado con una sentencia SQL. El sobre NACE VACÍO y se llena al flush.
--
-- Consecuencia: las tres columnas son NULLables, no se promueven nunca, y la
-- invariante «las tres o ninguna» vive en el CÓDIGO, como en contacts /
-- fleet_sessions / tenant_integrations — no en Postgres, como en la 0071.
--
-- ⚠️ Y VUELVEN A `NULL` AL FINAL. INV-13: al entrar en `done`/`failed`, LAS TRES
-- se ponen a NULL en la MISMA transacción del guard. Es la ÚNICA excepción
-- escrita al «nada se borra» de la máquina de estados, y es deliberada: sin
-- lector post-terminal, el literal solo suma superficie de riesgo. El rastro NO
-- se pierde — queda `source_refs`, y la fuente canónica del texto nunca fue esta
-- tabla sino `conversation_event_messages` (Plan 043 D-043.13). Esta migración NO
-- implementa ese vaciado: lo hace el guard de estado, que es código Go de otra
-- tarea. Aquí solo se deja la forma que lo PERMITE (columnas NULLables).
--
-- 🔴 ESTA MIGRACIÓN NO CIFRA NADA, Y AQUÍ NO HAY NADA QUE CIFRAR
-- ------------------------------------------------------------
-- Como la 0071 y a diferencia de la 0068/0069: la tabla NACE VACÍA. No hay
-- columna en claro de la que partir, no hay backfill que escribir y no hay
-- runbook que acompañe. Postgres sigue sin poder cifrar —no tiene la KEK, y por
-- no tenerla es por lo que este sobre protege algo (0006:20-21)—, solo que aquí
-- eso no obliga a ningún paso extra.
--
-- ⚠️ QUÉ PII HAY AQUÍ, DICHO SIN ROMANTICISMO
-- ------------------------------------------------------------
-- CERO PII en las columnas de identidad: `contact_id` es OPACO (ADR-0017) y los
-- `wa_message_id` de `source_refs` son identificadores opacos del protocolo. La
-- ÚNICA columna con contenido de persona es el trío del sobre, y por eso está
-- cifrado y por eso se vacía. `error` es texto de operador y NO debe llevar
-- literal del cliente: quien lo escriba tiene esa responsabilidad, no la tiene
-- esta tabla (ver su COMMENT).
--
-- ⚠️ DIVERGENCIA DE TIPOS, PREEXISTENTE Y NO SE UNIFICA AQUÍ
-- ------------------------------------------------------------
-- `tenant_id` y `contact_id` son TEXT en esta tabla y UUID en
-- `conversation_events`. Es divergencia PREEXISTENTE del repo, anotada como
-- MD-043.3, y este plan NO la unifica: se copia la convención de la ficha 3 para
-- no inventar una tercera forma. `event_id` SÍ es UUID y ahí el tipo CASA —
-- verificado el 2026-08-05: `conversation_events.id` es UUID PK.
--
-- ⚠️ FK LÓGICAS, NO FÍSICAS. Ni `event_id` ni `intake_id` llevan `REFERENCES`. Es
-- la convención de la casa: los módulos no se acoplan por constraint de base, y
-- un job cuyo evento se cancele NO debe desaparecer por cascada — tiene que
-- terminar su camino o quedar `failed` (§6.2). El correlativo `carrito_001` quedó
-- DEROGADO (ADR-0029 E-2) y el ID de historial `<tipo>-YYYY-MM-DD-HHMM` (E-3)
-- vive en `conversation_events.history_id`: se deriva por join, NO se copia aquí,
-- y NUNCA se muestra por WhatsApp.
--
-- ------------------------------------------------------------
-- 🔴 LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrations/migrate.go:79-102): re-aplica
-- TODO el directorio en cuanto cambia el hash del conjunto. Se dice qué se hizo
-- con cada regla en vez de dejar el silencio:
--
-- (1) SIN DEFAULT — SE APLICA A `intake_jobs` Y **NO** SE APLICA A LA COLUMNA DE
--     `tenant_settings`, y la diferencia es el motivo entero de la regla. En
--     `intake_jobs` los únicos defaults son los del molde de la casa (los dos
--     `now()`, el `gen_random_uuid()` de la PK, los dos literales JSONB vacíos) más
--     `status = 'aggregating'`, que es el estado inicial de la máquina y no un
--     valor «normal» inventado. El sobre NO tiene default —un texto cifrado
--     inventado no lo abre ninguna KEK, y un `source_text_kek_id` por defecto
--     metería en el barrido del Rekey a filas cuya DEK no existe: es el error que
--     la 0069 evitó no copiando el `DEFAULT '1'` de la 0007—.
--     🔴 En `tenant_settings`, en cambio, el `DEFAULT 45` SÍ está y SÍ alcanza a
--     las filas que ya existen, Y ESO ES LO QUE SE QUIERE. Aquí no hay nada que
--     un backfill pudiera mirar para decidir mejor —la ventana de agregación no
--     se deriva de ninguna columna anterior, es un parámetro nuevo—, así que el
--     default ES el backfill. Es la situación contraria a la de la 0063, donde
--     poner el default antes del backfill habría volcado a 'passive' todas las
--     sesiones vivas del cliente. Distíngase: allí el default DESTRUÍA
--     información existente; aquí no hay información que destruir.
--
-- (2) BACKFILL CON GUARD `WHERE ... IS NULL` — NO SE APLICA, y por dos razones
--     distintas, una por mitad. En `intake_jobs`, porque NO HAY BACKFILL EN
--     ABSOLUTO: tabla nueva, cero filas preexistentes. En `tenant_settings`,
--     porque el poblado de las filas viejas lo hace el propio `ADD COLUMN … NOT
--     NULL DEFAULT 45` en un solo paso, y su guard de idempotencia es el
--     `IF NOT EXISTS`: DEL SEGUNDO ARRANQUE EN ADELANTE LA SENTENCIA ES UN NO-OP
--     EXACTO. Eso importa más de lo que parece — un tenant que baje su ventana a
--     30 s NO la ve volver a 45 en el próximo reinicio, que es exactamente el
--     accidente que la regla (2) existe para impedir.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ ... IF is_nullable = 'YES'` — NO SE
--     APLICA, y aquí por la misma razón que en la 0071: no hay nada que promover.
--     Las columnas `NOT NULL` de `intake_jobs` NACEN así en el `CREATE TABLE`
--     sobre una tabla sin filas, y la de `tenant_settings` nace `NOT NULL` porque
--     lleva default y por tanto el `ADD COLUMN` no puede fallar. El `SET NOT NULL`
--     guardado existe para columnas que se añaden SIN default a una tabla CON
--     filas; ninguno de los dos casos de este fichero lo es.
--     ⚠️ Y las columnas de la COLA de `ADD COLUMN IF NOT EXISTS` de abajo tampoco
--     se promueven, pero eso NO es esta regla: es que son NULLables de verdad.
--
-- (4) CHECK CON NOMBRE EXPLÍCITO, RECREADO EN CADA REPLAY — SÍ APLICA, y aplica
--     CINCO VECES (eran TRES hasta que la sección F añadió las dos últimas el
--     2026-08-22; este párrafo no se releyó entonces y decía TRES):
--       1. `intake_jobs_status_check`                        (sección C)
--       2. `intake_jobs_stage_check`                         (sección C)
--       3. `tenant_settings_aggregation_window_check`        (sección E)
--       4. `conversation_event_messages_entry_kind_check`    (sección F, tabla de la 0051)
--       5. `conversation_event_messages_grade_chk`           (sección F, tabla de la 0051)
--     Las cinco llevan nombre propio —para poder nombrarlas en un error y
--     retirarlas sin adivinar— y las cinco VIVEN FUERA de su `CREATE TABLE` /
--     `ADD COLUMN`, con `DROP CONSTRAINT IF EXISTS` + `ADD`. El porqué, con el
--     fallo concreto que evita, está sobre cada bloque; el resumen es el de la
--     0071:106-109 — un CHECK inline dentro de un `CREATE TABLE IF NOT EXISTS` NO
--     SE RECREA NUNCA MÁS, porque del segundo arranque en adelante el CREATE
--     entero es NO-OP. Las dos de la sección F son EXACTAMENTE ese caso ya
--     consumado: nacieron inline en la 0051, así que allí ya están congeladas y
--     por eso se recrean AQUÍ (ver el bloque «POR QUÉ VAN AQUÍ Y NO EDITANDO LA
--     0051»).
--
-- EL REPLAY, AQUÍ BENIGNO EN LAS DOS MITADES
-- ------------------------------------------------------------
-- `CREATE TABLE IF NOT EXISTS` y `ADD COLUMN IF NOT EXISTS` son, del segundo
-- arranque en adelante, NO-OP EXACTOS: no tocan valores ni columnas. Los jobs en
-- vuelo SOBREVIVEN a cada reinicio —que es justo el criterio de T1.1, «reinicio
-- simulado ⇒ el job no se pierde», y la razón de que esta tabla exista en vez de
-- un mapa en memoria—, y las ventanas que cada tenant haya configurado SOBREVIVEN
-- igual. Y por ESO MISMO los CHECK no pueden ir dentro: lo que hace benigno al
-- replay es justo lo que dejaría a la constraint congelada para siempre.
--
-- ⚠️ NO HAY `DROP COLUMN source_text`, y no es un olvido: esta migración NUNCA se
-- ha desplegado con la forma antigua de §6.2 (la columna única en TEXT), así que
-- no existe base alguna que la tenga. Si alguna llegara a existir, la columna
-- sobraría sin estorbar y su retirada sería migración aparte — el criterio de la
-- 0013 con `order_ttl_seconds`: bajo un runner FULL-REPLAY un DROP obliga a toda
-- base virgen a crear-y-tirar.
--
-- ⚠️ ORDEN: `intake_jobs` no toca ninguna tabla existente. La columna de
-- `tenant_settings` SÍ depende de la 0013, que va mucho antes en secuencia. Va al
-- final, en secuencia, porque es donde va todo lo nuevo — el hueco 0020/0021 NO
-- se rellena.
--
-- ⚠️ `gen_random_uuid()` no lleva `CREATE EXTENSION`: ya lo usan la 0001, la 0051
-- y otras seis migraciones de este mismo directorio, así que la función está
-- disponible en toda base donde este runner haya llegado hasta aquí.
--
-- ⚠️ SIN GRANTS. `intake_jobs` es una tabla INTERNA del pipeline: no la lee
-- ninguna ruta con scope IAM en esta ola, así que no hay `iam_role_grants` que
-- sembrar (contraste con la 0042, que sí los sembró para `intakes` porque esa sí
-- tiene bandeja). El día que se exponga, será migración aparte.
--
-- ADITIVA e IDEMPOTENTE. ⚠️ **NO sube `SchemaVersion`**: salió a `main` bajo la
-- PRIMERA mitad de la regla —ola intermedia, sin bump— con la constante en `"0.44.0"`.
-- 🔧 El bump del Plan 044 ya NO espera a T6.2: llegó con la publicación de la Ola 4
-- (`0.45.0`, ver `migrations/version.go`) y cubre también a ésta y al resto de las
-- `0071`–`0079`, que salieron sin él. Esta migración NO lo toca. El runner la aplica igual —el disparo es el CAMBIO DE HASH
-- del directorio, no el número de versión (migrate.go:83-96)—, así que añadir este
-- fichero basta para que se ejecute.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE. Las ejecuta el barrido del CLI.
-- ------------------------------------------------------------
-- 🔴 NINGUNA de las SIETE se ha ejecutado. Se escribieron en un entorno sin
-- Postgres y sin Docker, así que ninguna está declarada como pasada.
-- Son V1–V5 (aquí abajo, `intake_jobs` y `tenant_settings`) más V6 y V7, que viven
-- al final del fichero con la sección F que las trajo. Este párrafo decía «las
-- cinco» porque se escribió antes de que existiera la F; no había cinco, hay siete.
--
-- (V1) La tabla existe con sus DIECISIETE columnas —QUINCE de negocio más los dos
--      timestamps del molde de la casa— y los `is_nullable` son LOS QUE SE ESPERAN.
--      🔴 Aquí NO se copia el criterio de la 0071: allí eran «las nueve en NO», y
--      quien traiga esa frase a esta tabla la romperá. Aquí hay SIETE en `YES`, y
--      son siete a propósito:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'intake_jobs'
--    ORDER BY ordinal_position;
--
--   Salida esperada — DIECISIETE filas, exactamente estas:
--
--    column_name        | data_type                | is_nullable | column_default
--   --------------------+--------------------------+-------------+---------------------
--    id                 | uuid                     | NO          | gen_random_uuid()
--    tenant_id          | text                     | NO          |
--    session_id         | text                     | NO          |
--    contact_id         | text                     | NO          |
--    event_id           | uuid                     | NO          |
--    status             | text                     | NO          | 'aggregating'::text
--    stage              | text                     | YES         |
--    message_ts         | timestamp with time zone | YES         |
--    source_text_enc    | bytea                    | YES         |
--    source_text_dek    | bytea                    | YES         |
--    source_text_kek_id | text                     | YES         |
--    source_refs        | jsonb                    | NO          | '[]'::jsonb
--    artifacts          | jsonb                    | NO          | '{}'::jsonb
--    error              | text                     | YES         |
--    intake_id          | uuid                     | YES         |
--    created_at         | timestamp with time zone | NO          | now()
--    updated_at         | timestamp with time zone | NO          | now()
--
--   Cuéntense: DIECISIETE filas, y SIETE de ellas en `YES` (stage, message_ts, el
--   trío del sobre, error, intake_id). Si sale otro número, falta o sobra una
--   columna — y si la que falta es del trío, ver el punto siguiente.
--
--   🔴 LOS DOS CHEQUES QUE MÁS IMPORTAN, por si alguien recorta esta verificación:
--
--     * el trío `source_text_enc` / `source_text_dek` / `source_text_kek_id` en
--       `YES` → si sale NO en cualquiera de las tres, alguien copió el molde
--       NOT NULL de la 0071 sin leer por qué aquí NO aplica, y NINGÚN INSERT del
--       `AggregatorSink` podrá abrir una ventana: el sink no compone el literal en
--       línea, así que escribiría los tres a NULL y reventaría en cada entrante.
--     * `status` con `DEFAULT 'aggregating'` → si sale vacío, el `ON CONFLICT` del
--       sink abriría ventanas sin estado y el índice único parcial de la (V3) no
--       las vería, con lo que dejaría de morder en silencio.
--
-- (V2) LOS DOS CHECK de `intake_jobs` existen, con su nombre:
--
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint
--    WHERE conrelid = 'public.intake_jobs'::regclass AND contype = 'c'
--    ORDER BY conname;
--
--   Salida esperada — DOS filas:
--     intake_jobs_stage_check  | CHECK (stage IS NULL OR stage = ANY (ARRAY['p2'…'draft']))
--     intake_jobs_status_check | CHECK (status = ANY (ARRAY['aggregating'…'failed']))
--
--   Y que MUERDEN, que es lo que la consulta de arriba no prueba:
--     INSERT … (status) VALUES ('inventado');   -- esperado: ERROR, viola intake_jobs_status_check
--     INSERT … (stage)  VALUES ('p9');          -- esperado: ERROR, viola intake_jobs_stage_check
--     INSERT … (stage)  VALUES (NULL);          -- esperado: PASA (stage es NULLable a propósito)
--
-- (V3) EL ÍNDICE ÚNICO PARCIAL EXISTE, ES ÚNICO Y ES PARCIAL. Las tres cosas, no
--      solo la primera — un índice con el nombre correcto y sin `UNIQUE` dejaría
--      pasar dos ventanas abiertas y nadie se enteraría hasta ver presupuestos
--      duplicados:
--
--   SELECT indexname, indexdef FROM pg_indexes
--    WHERE schemaname = 'public' AND tablename = 'intake_jobs'
--    ORDER BY indexname;
--
--   Salida esperada — CUATRO filas (la PK más los tres índices de este fichero), y
--   la de `intake_jobs_ventana_viva_uidx` tiene que contener LAS DOS cadenas:
--     · `CREATE UNIQUE INDEX`                    ← si falta UNIQUE, el índice no sirve
--     · `WHERE (status = 'aggregating'::text)`   ← si falta, el índice es TOTAL y
--       bloquearía reabrir una ventana para una tupla que ya tuvo un job cerrado
--
--   🔴 Y LA PRUEBA DE QUE MUERDE, que es la verificación de verdad. Sobre una base
--   de pruebas, con la MISMA tupla de ventana en los dos INSERT:
--
--     -- (a) dos ventanas abiertas para la misma tupla => el SEGUNDO FALLA
--     INSERT INTO public.intake_jobs (tenant_id, session_id, contact_id, event_id, status)
--       VALUES ('t1','s1','c1','00000000-0000-0000-0000-0000000000aa','aggregating');
--     INSERT INTO public.intake_jobs (tenant_id, session_id, contact_id, event_id, status)
--       VALUES ('t1','s1','c1','00000000-0000-0000-0000-0000000000aa','aggregating');
--     -- esperado: ERROR duplicate key value violates unique constraint
--     --           "intake_jobs_ventana_viva_uidx"
--
--     -- (b) el MISMO par, con el primero ya CERRADO => el segundo PASA
--     UPDATE public.intake_jobs SET status = 'pending'
--      WHERE tenant_id = 't1' AND session_id = 's1' AND contact_id = 'c1';
--     INSERT INTO public.intake_jobs (tenant_id, session_id, contact_id, event_id, status)
--       VALUES ('t1','s1','c1','00000000-0000-0000-0000-0000000000aa','aggregating');
--     -- esperado: OK, 1 fila. El parcial solo vigila las VIVAS, y eso es el diseño:
--     --           un cliente puede volver a pedir sobre el mismo evento.
--
--   Las DOS mitades, y no solo la (a): la (a) sola no distingue este índice de uno
--   TOTAL, que también fallaría — y que estaría MAL.
--
-- (V4) La columna de `tenant_settings` existe, es NOT NULL, tiene default 45, y
--      —lo que de verdad hay que mirar— LAS FILAS PREEXISTENTES QUEDARON EN 45:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'tenant_settings'
--      AND column_name = 'aggregation_window_seconds';
--   -- esperado: 1 fila | integer | NO | 45
--
--   SELECT count(*) AS en_cero FROM public.tenant_settings
--    WHERE aggregation_window_seconds = 0;    -- esperado: 0
--   SELECT count(*) AS en_45,
--          (SELECT count(*) FROM public.tenant_settings) AS total
--     FROM public.tenant_settings WHERE aggregation_window_seconds = 45;
--   -- esperado: en_45 = total (todo tenant preexistente hereda la ventana por defecto)
--
--   🔴 El `en_cero` NO es decorativo: 0 es un valor LEGÍTIMO de esta columna
--   (flush inmediato, ver el CHECK), así que un tenant en 0 no es un error — pero
--   un tenant en 0 QUE NADIE PUSO ahí significaría que el `ADD COLUMN` se aplicó
--   sin default y Postgres rellenó con el cero del tipo. Eso apagaría la
--   agregación de todo el parque en silencio: cada mensaje dispararía su propio
--   pipeline. Por eso se cuenta, y por eso se cuenta ANTES de que nadie configure
--   nada. El CHECK no puede protegernos de esto — 0 lo satisface.
--
--   Y el replay no la pisa (regla 2): tras un `UPDATE … SET
--   aggregation_window_seconds = 30` sobre un tenant, forzar el replay de la (V5)
--   y volver a leer ⇒ sigue en 30, NO vuelve a 45.
--
-- (V5) EL REPLAY FORZADO, que es la única forma de probar la regla (4). 🔴 UN
--      SEGUNDO ARRANQUE NORMAL NO PRUEBA NADA: sale `skipped=true` porque el hash
--      del directorio no cambió (migrate.go:85-92) y no llega a ejecutar una sola
--      sentencia de este fichero. Hay que forzarlo a mano:
--
--     -- 1. se retira a mano una constraint, simulando el restore parcial
--     ALTER TABLE public.intake_jobs DROP CONSTRAINT intake_jobs_status_check;
--     -- 2. se invalida el hash registrado para que el runner NO salte
--     UPDATE public.schema_version SET content_hash = 'forzar-replay';
--     -- 3. se re-ejecuta el runner
--     --    $ go run ./cmd/migrate
--     -- 4. la constraint TIENE que estar de vuelta => re-ejecutar la (V2)
--
--   Esperado: la (V2) vuelve a dar sus dos filas. Si `intake_jobs_status_check` NO
--   reaparece, el CHECK se coló dentro del `CREATE TABLE` y la regla (4) está rota
--   —el mismo fallo que la revisión del 2026-08-22 encontró en la 0071—.
--
--   ⚠️ Y de paso queda probado que el replay no destruye: los jobs y los
--   `aggregation_window_seconds` configurados antes del paso 3 tienen que seguir
--   ahí después, con sus mismos valores.
-- ============================================================

-- ------------------------------------------------------------
-- A) LA TABLA
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.intake_jobs (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          TEXT        NOT NULL,   -- TEXT aquí y UUID en conversation_events: MD-043.3
    session_id         TEXT        NOT NULL,
    contact_id         TEXT        NOT NULL,   -- OPACO (ADR-0017). NO es un teléfono.
    event_id           UUID        NOT NULL,   -- FK LÓGICA a conversation_events.id (sin REFERENCES)
    status             TEXT        NOT NULL DEFAULT 'aggregating',  -- CERRADO: su CHECK va APARTE
    stage              TEXT,                   -- CERRADO y NULLable: su CHECK va APARTE
    message_ts         TIMESTAMPTZ,            -- ts del PRIMER mensaje (D-044.9); solo en el INSERT
    -- EL BÚFER DE TRABAJO NO ES UNA COLUMNA, SON TRES (D-044.26). Los tres NULLables
    -- a propósito: durante 'aggregating' están legítimamente vacíos. Ver cabecera.
    source_text_enc    BYTEA,                  -- envelope AES-256-GCM del literal, compuesto AL FLUSH
    source_text_dek    BYTEA,                  -- DEK por-fila (32B), envuelta por la KEK maestra
    source_text_kek_id TEXT,                   -- key_id de la KEK que envolvió source_text_dek
    source_refs        JSONB       NOT NULL DEFAULT '[]',  -- wa_message_ids + refs de media (Plan 017)
    artifacts          JSONB       NOT NULL DEFAULT '{}',  -- {"p2":{…},"p3":{…},"p4":{…}} versionados
    error              TEXT,                   -- causa del último fallo, para el operador
    intake_id          UUID,                   -- FK LÓGICA al borrador creado (la escribe la Ola 3)
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ------------------------------------------------------------
-- B) LA COLA DE `ADD COLUMN IF NOT EXISTS`
-- ------------------------------------------------------------
-- Por si una base ya tiene la tabla de una versión anterior de ESTE MISMO fichero:
-- el replay le añade lo que falte sin tocar las filas existentes (patrón 0071:217-224
-- y 0047:73-77). En la base virgen —el caso real hoy, porque esta migración no se ha
-- desplegado nunca— estas líneas son NO-OP EXACTO y las columnas nacen del CREATE
-- TABLE de arriba con la forma correcta.
--
-- 🔴 SE AÑADEN NULLables Y NO SE PROMUEVEN, pero ojo: aquí eso NO es la concesión
-- defensiva que hacía la 0071. Estas siete columnas SON NULLables de verdad, en el
-- CREATE TABLE también. No hay tensión entre las dos formas y no hay nada que
-- promover después.
--
-- ⚠️ EL CASO REAL QUE ESTA COLA CUBRE es el trío del sobre: hasta D-044.26
-- (2026-08-22) el §6.2 dibujaba una sola `source_text TEXT`, así que una base que
-- hubiera alcanzado a aplicar aquella forma tendría la tabla SIN las tres columnas
-- de abajo y CON una `source_text` de más. Las tres primeras líneas se la reparan.
-- (La `source_text` sobrante NO se dropea; el porqué está en la cabecera.)
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS source_text_enc    BYTEA;
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS source_text_dek    BYTEA;
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS source_text_kek_id TEXT;

-- Las otras cuatro NULLables, por completitud del patrón: son NO-OP en toda base
-- que venga del CREATE de arriba, y baratas de escribir.
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS stage      TEXT;
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS message_ts TIMESTAMPTZ;
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS error      TEXT;
ALTER TABLE public.intake_jobs ADD COLUMN IF NOT EXISTS intake_id  UUID;

-- ⚠️ LAS COLUMNAS `NOT NULL` **NO** ESTÁN EN ESTA COLA, Y ES DELIBERADO. Un
-- `ADD COLUMN … NOT NULL` sin default revienta sobre una tabla con filas, y
-- añadirlas NULLables aquí sería mentir sobre su forma —dejaría una base
-- reparada «a medias» que el CREATE nunca produce y que la (V1) marcaría en rojo—.
-- Además: una base a la que le falte `tenant_id` o `status` no es una base a
-- medias, es una base SIN LA TABLA, y de ese caso se ocupa el CREATE de arriba.

-- ------------------------------------------------------------
-- C) LOS DOS CHECK, FUERA DEL CREATE TABLE Y RECREADOS EN CADA REPLAY
-- ------------------------------------------------------------
-- Van FUERA por la regla (4) del patrón full-replay, y el fallo que evita es
-- concreto, no teórico: un CHECK inline dentro de un `CREATE TABLE IF NOT EXISTS`
-- se salta con el CREATE del segundo arranque en adelante, así que NO SE RECREA
-- NUNCA MÁS. El día que la máquina de estados gane un estado —y esta máquina va a
-- ganar estados: `status` y `stage` describen un pipeline que la Ola 3 y la Ola 5
-- todavía van a extender—, editar la lista de valores de aquí NO cambiaría la
-- constraint de una base viva: el `UPDATE` al estado nuevo daría error de CHECK y
-- nadie sabría por qué. Igual si alguien la dropea a mano o la pierde un restore
-- parcial: el replay ya no la repondría. La (V5) prueba justamente esto.
--
-- Se usa UN SOLO `DROP … IF EXISTS` por constraint —no dos como la 0063— porque
-- estos CHECK NUNCA existieron con nombre autogenerado: nacen ya nombrados en este
-- fichero, así que no hay un `intake_jobs_status_check1` anónimo que retirar.
--
-- ⚠️ Van DESPUÉS de los `ADD COLUMN`, porque un CHECK sobre una columna que aún no
-- existe no se puede añadir. Sobre `status` eso no aplica (nace en el CREATE), pero
-- sobre `stage` SÍ importa: `stage` está en la cola de arriba.
ALTER TABLE public.intake_jobs
    DROP CONSTRAINT IF EXISTS intake_jobs_status_check;
ALTER TABLE public.intake_jobs
    ADD CONSTRAINT intake_jobs_status_check
    CHECK (status IN ('aggregating','pending','processing','done','failed'));
-- v1; crecer el dominio = editar ESTA línea, y el replay la aplica de verdad.

-- 🔧 DESVIACIÓN DEL LITERAL DE §6.2:1122-1124, y solo en la FORMA. El design escribe
-- `CHECK (stage IN (…))` a secas. Es SEMÁNTICAMENTE IDÉNTICO —`NULL IN (…)` evalúa a
-- NULL, y un CHECK que da NULL SE SATISFACE, así que la forma corta ya admite el
-- NULL—, pero se escribe el `stage IS NULL OR` explícito porque esa equivalencia es
-- justo la que un lector apurado lee al revés: `stage` es NULLable a propósito —una
-- ventana en 'aggregating' todavía no tiene etapa— y la forma corta parece prohibirlo.
-- La constraint hace lo mismo; lo que cambia es que ahora se lee lo que hace.
ALTER TABLE public.intake_jobs
    DROP CONSTRAINT IF EXISTS intake_jobs_stage_check;
ALTER TABLE public.intake_jobs
    ADD CONSTRAINT intake_jobs_stage_check
    CHECK (stage IS NULL OR stage IN ('p2','p3','p4','match','draft'));
-- v1; las cinco etapas de `processing`. Crecer el pipeline = editar ESTA línea.

-- ------------------------------------------------------------
-- D) LOS TRES ÍNDICES
-- ------------------------------------------------------------

-- (D.1) EL BARRIDO DEL WORKER, POR ESTADO. NO es único y NO es parcial: barre
-- POR `status` incluyendo filas YA CERRADAS (`done`/`failed`), que es justo lo que
-- el parcial de abajo no cubre.
-- ⚠️ NO ES REDUNDANTE CON `intake_jobs_ventana_viva_uidx`, aunque sus cuatro
-- primeras columnas coincidan y aunque un linter de índices lo señale. El único
-- parcial SOLO indexa las filas 'aggregating' —el planner no puede usarlo para una
-- consulta que pregunte por 'pending' o por 'done', porque esas filas literalmente
-- no están en él—. Este lleva `status` DENTRO de la clave y cubre las cinco.
-- NO LO BORRES POR PARECER UN DUPLICADO DEL SIGUIENTE.
CREATE INDEX IF NOT EXISTS intake_jobs_window_idx
    ON public.intake_jobs (tenant_id, session_id, contact_id, event_id, status);

-- (D.2) LA VENTANA VIVA: ÚNICA Y PARCIAL. Lo EXIGE el `ON CONFLICT` del
-- `AggregatorSink` (D-044.26): sin un índice único que lo soporte, esa sentencia no
-- existe — `ON CONFLICT (a,b,c,d) WHERE …` necesita un índice único que coincida con
-- el predicado. Y hace cumplir EN POSTGRES —no en el código— la regla de que NO PUEDE
-- HABER DOS VENTANAS ABIERTAS para la misma tupla, que es lo que el agregador da por
-- cierto sin poder comprobarlo (no lee: D-044.26).
-- Es la MISMA forma con la que el Plan 043 garantiza «un evento vivo por tipo»
-- (`conversation_events_one_alive_per_kind_idx`, 0051:131-133): mismo eje
-- (tenant, session, contact, +1) y mismo `WHERE` sobre el estado vivo.
-- 🔴 EL `WHERE` NO ES OPCIONAL. Sin él el índice sería TOTAL y prohibiría abrir una
-- ventana nueva sobre un evento que ya tuvo un job cerrado — es decir, un cliente no
-- podría volver a pedir sobre la misma conversación. El parcial vigila las VIVAS y
-- deja el histórico en paz.
-- ⚠️ Precio aceptado y escrito (D-044.26): dos ventanas vivas para la misma tupla
-- pasan de ser un caso raro que duplicaba en silencio a un ERROR DE BASE DE DATOS.
-- Es lo que se quería, y obliga a escribir el camino de error del `ON CONFLICT`
-- mirándolo.
CREATE UNIQUE INDEX IF NOT EXISTS intake_jobs_ventana_viva_uidx
    ON public.intake_jobs (tenant_id, session_id, contact_id, event_id)
    WHERE status = 'aggregating';

-- (D.3) EL CENSO DEL REKEY: índice de CONTEO del sobre. Mismo papel real que el de
-- la 0071 y el de la 0069: NO lo usa el barrido —el SELECT y el UPDATE de
-- `rekeyBatch` filtran por `<> $1`, y la desigualdad no es predicado indexable en un
-- btree—, lo usa `pendingSQL`, el `GROUP BY` de `PendingByKeyID`: la consulta que se
-- ejecuta CUANDO YA NO QUEDA NADA y que es la que AUTORIZA retirar una KEK del
-- keyring (§10.F). Paga en el momento que importa. Sin `tenant_id` delante porque el
-- Rekey NO filtra por tenant: barre global por key_id.
--
-- 🔴 ES PARCIAL, AL CONTRARIO QUE EL `idx_tenant_llm_kek` DE LA 0071. Allí la columna
-- es NOT NULL y un `WHERE … IS NOT NULL` no excluiría ni una fila: sería un predicado
-- decorativo. AQUÍ `source_text_kek_id` SÍ es NULLable y la MAYORÍA de las filas vivas
-- ('aggregating') NO TIENEN SOBRE, así que el WHERE hace trabajo real y mantiene el
-- índice pequeño. La forma que le toca es la de la 0068/0069, no la de la 0071.
--
-- ⚠️⚠️ AVISO QUE NO SE PUEDE OMITIR: **CABLEAR `intake_jobs` EN EL CENSO DEL REKEY ES
-- CÓDIGO GO QUE ESTA OLA NO ESCRIBE.** El índice existe para cuando se cablee; hoy
-- `rekeyTargets` NO incluía esta tabla, así que una rotación de KEK habría dicho
-- COMPLETA con estos sobres aún bajo la KEK vieja, y retirar esa KEK dejaría
-- ilegibles los `source_text` de los jobs en vuelo. Es exactamente el fallo que el
-- COMMENT de `tenant_llm.api_key_kek_id` describe para su tabla.
-- ✅ **YA TIENE DUEÑO Y YA ESTÁ HECHO (2026-08-22, Plan 044 · T1.4)**: la entrada
-- `public.intake_jobs` (pk `id`, `source_text_dek`, `source_text_kek_id`) está en
-- `rekeyTargets` desde el MISMO commit que empieza a cifrar aquí, que es la regla
-- que ese censo se escribió a sí mismo con `fleet_sessions`. No esperó a T2.1.
-- Mitigante que ya no hace falta invocar, pero sigue siendo cierto: el sobre es
-- efímero —se llena al flush y se vacía al llegar a `done`/`failed` (INV-13)—, así
-- que la ventana de exposición es la vida de un job, no la de la conversación.
CREATE INDEX IF NOT EXISTS idx_intake_jobs_kek
    ON public.intake_jobs (source_text_kek_id)
    WHERE source_text_kek_id IS NOT NULL;

-- ------------------------------------------------------------
-- E) LA VENTANA DE AGREGACIÓN, EN `tenant_settings` (T1.2)
-- ------------------------------------------------------------
-- LA PRIMERA de las DOS secciones que tocan una tabla que ya existe: esta va contra la
-- 0013 (`tenant_settings`) y la F, contra la 0051 (`conversation_event_messages`). Este
-- párrafo decía «única línea», y lo fue hasta que la sección F entró el 2026-08-22.
-- Cuántos segundos espera el agregador desde el PRIMER mensaje de la ventana antes de
-- cerrarla y disparar el pipeline. Es dato de NEGOCIO EN CLARO (ADR-0009), como todo
-- lo demás de esta tabla: cero PII, cero llaves.
--
-- El `DEFAULT 45` alcanza a las filas que YA EXISTEN, y eso es lo que se quiere: no
-- hay columna anterior de la que derivar un valor mejor, así que el default ES el
-- backfill (regla (1), ver cabecera). Y el `IF NOT EXISTS` es su guard de
-- idempotencia: del segundo arranque en adelante la sentencia es NO-OP EXACTO, así
-- que un tenant que baje su ventana a 30 NO la ve volver a 45 en el próximo reinicio.
ALTER TABLE public.tenant_settings
    ADD COLUMN IF NOT EXISTS aggregation_window_seconds INTEGER NOT NULL DEFAULT 45;

-- Su CHECK, con nombre y FUERA del `ADD COLUMN`, por la regla (4): un CHECK inline en
-- un `ADD COLUMN IF NOT EXISTS` se salta con él del segundo arranque en adelante y
-- deja de recrearse para siempre.
--
-- 🔴 POR QUÉ `>= 0` Y NO `> 0`: **0 ES UNA CONFIGURACIÓN LEGÍTIMA**, no un valor
-- degenerado. Significa FLUSH INMEDIATO — sin agregación, cada mensaje dispara su
-- propio pipeline. Es el comportamiento que tendría el sistema sin esta ola, y es una
-- elección razonable para un tenant cuyos clientes escriben mensajes largos y sueltos.
-- Prohibirlo obligaría a inventar otro interruptor para decir lo mismo.
--
-- 🔴 Y POR QUÉ NO HAY TECHO SUPERIOR: **porque ningún requisito lo discutió.** No se
-- pone un límite inventado —«600», «3600»— que luego nadie sabría defender ni de dónde
-- salió. Lo único que el CHECK descarta es lo que NO SIGNIFICA NADA: un negativo, que
-- no es «esperar poco» sino un valor sin lectura posible. Si algún día un techo hace
-- falta, será porque un incidente lo pida, y entonces el número tendrá origen.
-- (Nota operativa, no de esquema: una ventana absurdamente larga no rompe la base,
-- retrasa el presupuesto de ese tenant y solo de ese tenant.)
ALTER TABLE public.tenant_settings
    DROP CONSTRAINT IF EXISTS tenant_settings_aggregation_window_check;
ALTER TABLE public.tenant_settings
    ADD CONSTRAINT tenant_settings_aggregation_window_check
    CHECK (aggregation_window_seconds >= 0);

-- ------------------------------------------------------------
-- F) EL SALIENTE FUERA DE TURNO: UN `entry_kind` NUEVO EN
--    `conversation_event_messages` (Plan 044 · T1.6, D-044.24)
-- ------------------------------------------------------------
-- 🔴 AMPLIACIÓN DE T1.0 QUE EL PLAN NO HABÍA PREVISTO, y por eso se escribe con su
-- razón entera. T1.6 tenía que MARCAR las filas del hilo que son salientes NUESTROS
-- sin entrante detrás (el resumen del rescate, la confirmación de un event_stop, el
-- recordatorio de la seña) para que el prompt de P2 (T1.4) los trate como CONTEXTO y
-- no como pedido del cliente. La forma de la marca era decisión abierta y salió
-- `entry_kind`, que es la columna que la casa YA usa para esto (`summary` existe por
-- el mismo motivo). Y ampliar `entry_kind` cuesta DDL: aquí está.
--
-- POR QUÉ ESTAS DOS CONSTRAINTS Y NO UNA. Hay que tocar las DOS o la fila REBOTA:
--
--   (1) `conversation_event_messages_entry_kind_check` — el vocabulario. Sin esto,
--       'message_out_of_turn' no es un valor legal y el INSERT falla.
--   (2) `conversation_event_messages_grade_chk` — el GRADO del ADR-0034. Este es el
--       que se olvida, y es el que muerde. Su forma actual es
--           (entry_kind IN ('decision','summary') AND body_enc IS NULL ...)
--        OR (entry_kind = 'message' AND payload IS NULL)
--       Con un `entry_kind` que no está en NINGUNA de las dos ramas, las dos dan
--       false, el CHECK da false y la fila se rechaza — aunque el vocabulario ya la
--       admitiera. El saliente fuera de turno es nivel 2 igual que `message`
--       (cifrado, `payload` NULL), así que se suma a la SEGUNDA rama, no a la
--       primera.
--
-- ⚠️ POR QUÉ VAN AQUÍ Y NO EDITANDO LA 0051. Porque editar la 0051 NO HARÍA NADA.
-- Sus dos CHECK son INLINE dentro de un `CREATE TABLE IF NOT EXISTS`, y sobre una
-- base donde la tabla ya existe ese CREATE es NO-OP entero: la constraint quedaría
-- congelada en la lista del día que nació. Lo dice la propia 0051 en su cabecera
-- (:83-90), y prescribe exactamente este remedio: «Quien añada un `entry_kind` o un
-- `origin` nuevo lo hace con `ALTER TABLE ... DROP CONSTRAINT IF EXISTS` + `ADD
-- CONSTRAINT`, como la 0045 hizo con intake_revisions_kind_check». Es literalmente
-- el molde de 0045:141-143, copiado.
--
-- ⚠️ EL NOMBRE DE (1) ES EL QUE POSTGRES AUTOGENERÓ, no uno inventado: un CHECK
-- inline sobre la columna `entry_kind` de la tabla `conversation_event_messages` se
-- llama `conversation_event_messages_entry_kind_check`. Si en alguna base se llamara
-- de otro modo, el `DROP ... IF EXISTS` no lo encontraría, el `ADD` chocaría con el
-- viejo y el error lo diría en voz alta — que es preferible a un DROP a ciegas.
-- 🔴 SIN VERIFICAR CONTRA UNA BASE: ver la (V6) de abajo.
--
-- ⚠️ ORDEN. Van DESPUÉS de todo lo de `intake_jobs` porque tocan OTRA tabla, la de
-- la 0051, que va mucho antes en secuencia. Mismo criterio que la columna de
-- `tenant_settings`: lo nuevo va al final, en secuencia.
--
-- ⚠️ SIN BACKFILL Y SIN MIGRAR NADA. Ninguna fila existente cambia de `entry_kind`:
-- lo que había sigue siendo `decision`/`summary`/`message` y significa lo mismo. Lo
-- único que cambia es que a partir de aquí se puede escribir un cuarto valor. Un
-- replay es NO-OP semántico: recrea las dos constraints con la misma definición.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN de esta sección — ⏳ TAMPOCO EJECUTADA (sin Postgres delante)
-- ------------------------------------------------------------
-- (V6) LAS DOS CONSTRAINTS EXISTEN CON SU NOMBRE Y CON LA LISTA NUEVA:
--
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint
--    WHERE conrelid = 'public.conversation_event_messages'::regclass AND contype = 'c'
--    ORDER BY conname;
--
--   Esperado: entre las filas, `conversation_event_messages_entry_kind_check` con
--   los CUATRO valores y `conversation_event_messages_grade_chk` nombrando
--   'message_out_of_turn' en su segunda rama.
--
--   🔴 Y ANTES DE NADA, EL NOMBRE — es el único supuesto de este bloque que no se
--   pudo comprobar. Si esta consulta devuelve un CHECK sobre `entry_kind` con OTRO
--   nombre (p. ej. `..._entry_kind_check1`), el `DROP ... IF EXISTS` de abajo no lo
--   habrá retirado y habrá DOS constraints sobre la columna, la vieja rechazando lo
--   que la nueva admite. Se corrige nombrando la vieja en un DROP explícito.
--
-- (V7) Y QUE MUERDEN, en las dos direcciones:
--
--   -- (a) el valor nuevo PASA, con sobre y sin payload (nivel 2)
--   INSERT INTO public.conversation_event_messages
--          (event_id, seq, role, entry_kind, body_enc, body_dek, body_kek_id)
--   VALUES ('<un event_id real>', 999, 'business', 'message_out_of_turn',
--           '\x00'::bytea, '\x00'::bytea, 'k1');   -- esperado: OK
--
--   -- (b) el valor nuevo CON payload REBOTA (el grado sigue siendo excluyente)
--   INSERT INTO public.conversation_event_messages
--          (event_id, seq, role, entry_kind, payload)
--   VALUES ('<un event_id real>', 998, 'business', 'message_out_of_turn', '{}'::jsonb);
--   -- esperado: ERROR, viola conversation_event_messages_grade_chk
--
--   -- (c) un entry_kind inventado sigue rebotando (el vocabulario sigue CERRADO)
--   INSERT INTO public.conversation_event_messages (event_id, seq, role, entry_kind)
--   VALUES ('<un event_id real>', 997, 'business', 'inventado');
--   -- esperado: ERROR, viola conversation_event_messages_entry_kind_check
--
--   La (b) es la que de verdad prueba el trabajo: sin tocar el CHECK de grado, la
--   (a) ya habría fallado; tocándolo mal —metiendo el valor nuevo en la PRIMERA
--   rama— la (a) fallaría igual y la (b) PASARÍA, que es texto literal colándose en
--   el nivel 1 en claro. Las dos, o ninguna.

-- (1) EL VOCABULARIO. Molde de 0045:141-143, prescrito por 0051:88-90.
ALTER TABLE public.conversation_event_messages
    DROP CONSTRAINT IF EXISTS conversation_event_messages_entry_kind_check;
ALTER TABLE public.conversation_event_messages
    ADD  CONSTRAINT conversation_event_messages_entry_kind_check
    CHECK (entry_kind IN ('decision', 'summary', 'message', 'message_out_of_turn'));
-- v2; crecer el dominio = editar ESTA linea, y el replay la aplica de verdad.

-- (2) EL GRADO (ADR-0034). `message_out_of_turn` es nivel 2 y por eso entra en la
-- SEGUNDA rama, junto a `message`: cuerpo cifrado, `payload` NULL. Meterlo en la
-- primera dejaria pasar texto literal al JSONB EN CLARO, que es lo contrario de lo
-- que esta constraint existe para impedir.
ALTER TABLE public.conversation_event_messages
    DROP CONSTRAINT IF EXISTS conversation_event_messages_grade_chk;
ALTER TABLE public.conversation_event_messages
    ADD  CONSTRAINT conversation_event_messages_grade_chk
    CHECK (
        (entry_kind IN ('decision','summary') AND body_enc IS NULL AND body_dek IS NULL)
     OR (entry_kind IN ('message','message_out_of_turn') AND payload IS NULL)
    );

COMMENT ON COLUMN public.conversation_event_messages.entry_kind IS
    'decision = decision TOMADA por el cliente, estructurada y en claro. summary = resumen determinista que emitimos NOSOTROS al cambiar de evento (ADR-0029 E-4, role=system). message = texto literal de un TURNO (lo que el cliente escribio, lo que el flujo contesto), SOLO con llm_intake y SOLO cifrado. message_out_of_turn = texto literal que la plataforma mando SIN que naciera de un entrante del cliente (Plan 044 T1.6, D-044.24): el resumen del rescate, la confirmacion de un event_stop, el recordatorio de la sena. Mismo grado y mismo cifrado que message; lo que cambia es que quien lea el hilo sabe que NO es un pedido. Quien analice el hilo (Plan 044 o un humano) DEBE tratar summary y message_out_of_turn como CONTEXTO, nunca como mensaje original: la decision ya esta en la tabla y ADEMAS aparece en el resumen, y el automensaje de rescate LISTA productos que el cliente no acaba de pedir. Contarlos dos veces es creer que el usuario pidio 2 hamburguesas cuando pidio 1.';

-- ------------------------------------------------------------
-- G) COMENTARIOS DE ESQUEMA (sin tildes ni no-ASCII, convención de la 0071)
-- ------------------------------------------------------------
COMMENT ON TABLE public.intake_jobs IS
    'Cola del pipeline de captacion por LLM (Plan 044 - Ola 1, T1.0; design 6.2). Una fila = UNA VENTANA DE AGREGACION: los mensajes seguidos de un contacto sobre un mismo evento se juntan aqui y disparan UN solo pipeline, no uno por mensaje. Desempena para el Plan 044 el mismo papel que webhook_outbox (0046) para el 042: es la fila que existe para que el trabajo caro lo haga otro, despues. El AggregatorSink escribe UNA sentencia por entrante y NINGUNA lectura (D-044.26, INV-02 del Plan 050). SIN BROKER (ADR-0003): la durabilidad es esta tabla, y por eso un reinicio no pierde el job. Maquina de estados: aggregating -> pending -> processing(p2,p3,p4,match,draft) -> done | failed; guards UPDATE ... WHERE status=...; terminales absorbentes; nada se borra CON UNA SOLA EXCEPCION ESCRITA: el trio source_text_* se pone a NULL al entrar en done/failed (INV-13, cierre de MD-044.1). PII: CERO en las columnas de identidad (contact_id es OPACO, ADR-0017; los wa_message_id son opacos); la unica con contenido de persona es el trio del sobre, y por eso va cifrado y por eso se vacia.';
COMMENT ON COLUMN public.intake_jobs.id                 IS 'Identidad propia del job (PK, UUID con gen_random_uuid()). NO se muestra por WhatsApp jamas: el ID que ve el cliente es el history_id del evento (ADR-0029 E-3), que vive en conversation_events y se deriva por join.';
COMMENT ON COLUMN public.intake_jobs.tenant_id          IS 'Tenant dueno del job. TEXT sin FK, convencion de la ficha 3. Sale del TOKEN o del contexto de la sesion, jamas del cuerpo de una peticion (INV-7 / INV-8). DIVERGENCIA DE TIPO CONOCIDA: aqui TEXT y en conversation_events UUID -- preexistente del repo, anotada como MD-043.3; el Plan 044 NO la unifica.';
COMMENT ON COLUMN public.intake_jobs.session_id         IS 'Sesion de la flota por la que entro la conversacion. Forma parte de la clave de ventana: dos sesiones distintas del mismo tenant hablando con el mismo contacto son DOS conversaciones, no una.';
COMMENT ON COLUMN public.intake_jobs.contact_id         IS 'Identificador OPACO del contacto (ADR-0017). NO ES UN TELEFONO y no se puede revertir a uno desde aqui: el telefono vive cifrado en contacts. Por eso esta columna, aun siendo parte de la clave de ventana, NO cuenta como PII en esta tabla.';
COMMENT ON COLUMN public.intake_jobs.event_id           IS 'Evento de conversacion al que pertenece la ventana (Plan 043). FK LOGICA a conversation_events.id, SIN REFERENCES a proposito: los modulos no se acoplan por constraint de base y un job cuyo evento se cancele NO debe desaparecer por cascada -- termina su camino o queda failed, y el intake ligado pasa a abandoned (D-041.10). El tipo CASA (UUID a ambos lados, verificado 2026-08-05). El correlativo carrito_001 QUEDO DEROGADO (ADR-0029 E-2).';
COMMENT ON COLUMN public.intake_jobs.status             IS 'Estado en la maquina del pipeline. Vocabulario CERRADO por intake_jobs_status_check (CHECK con nombre y FUERA del CREATE, para que el replay lo recree): aggregating (ventana abierta, acumulando referencias) | pending (ventana cerrada, esperando worker) | processing (en curso, ver stage) | done | failed. DEFAULT aggregating: toda fila nace como ventana abierta, que es lo unico que el AggregatorSink sabe crear. Los dos terminales son ABSORBENTES. Este valor gobierna ADEMAS el indice unico parcial de la ventana viva: solo las filas en aggregating estan en el, asi que cerrar una ventana es lo que LIBERA la tupla para que se pueda abrir otra.';
COMMENT ON COLUMN public.intake_jobs.stage              IS 'Etapa dentro de processing. Vocabulario CERRADO por intake_jobs_stage_check: p2 | p3 | p4 | match | draft. NULLable A PROPOSITO y ese es el estado normal en aggregating y en pending -- una ventana abierta todavia no tiene etapa. Su CHECK escribe el "stage IS NULL OR" explicito aunque un CHECK que evalua a NULL ya se satisface, porque la forma corta se lee al reves. Sirve para el REDELIVERY: un job que vuelve salta las etapas cuyo artefacto ya esta persistido en artifacts.';
COMMENT ON COLUMN public.intake_jobs.message_ts         IS 'Timestamp del PRIMER mensaje de la ventana. Es la BASE DE FECHAS del presupuesto (D-044.9): "para el jueves" se resuelve contra este instante, no contra el reloj del worker, que puede correr minutos u horas despues. Se fija SOLO en el INSERT y NUNCA en el DO UPDATE del ON CONFLICT (D-044.26), que es como el sink conserva el ts del primer mensaje sin necesitar leer la fila.';
COMMENT ON COLUMN public.intake_jobs.source_text_enc    IS 'Envelope AES-256-GCM (nonce fresco por escritura) del literal compuesto AL FLUSH a partir del hilo del evento. NULLable A PROPOSITO, al reves que el api_key_enc de la 0071: durante aggregating esta legitimamente vacio porque el sink NO compone el literal en linea -- si no lee, no puede concatenar, y no se puede anadir texto a un blob cifrado con una sentencia SQL (D-044.26). NO ES LA FUENTE CANONICA del texto: esa es conversation_event_messages (D-043.13); esto es un BUFER DE TRABAJO. Se pone a NULL al entrar en done/failed (INV-13).';
COMMENT ON COLUMN public.intake_jobs.source_text_dek    IS 'DEK por-fila (32B) que cifra source_text_enc, envuelta por la KEK maestra (design.md seccion 10.B). La KEK NO vive en esta BD. NO tiene NADA que ver con la DEK del Edge (el store de whatsmeow, ADR-0007), que la nube jamas ve. NULLable por lo mismo que las otras dos del trio, y se vacia con ellas.';
COMMENT ON COLUMN public.intake_jobs.source_text_kek_id IS 'key_id de la KEK que envolvio source_text_dek. Discriminador de la rotacion: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey). NULLable, y su indice idx_intake_jobs_kek es PARCIAL por eso -- al contrario que el de la 0071, donde la columna es NOT NULL y el predicado seria decorativo. AVISO VIVO: cablear esta tabla en el censo del Rekey (rekeyTargets) es CODIGO GO QUE LA OLA 1 NO ESCRIBE y esa tarea SIGUE SIN DUENO (propuesta T2.1); hasta entonces una rotacion diria COMPLETA con estos sobres aun bajo la KEK vieja. Mitigante que NO la cancela: el sobre es efimero (se llena al flush, se vacia en terminal).';
COMMENT ON COLUMN public.intake_jobs.source_refs        IS 'Referencias de los mensajes que componen la ventana: wa_message_ids mas refs de media/audio (Plan 017). En CLARO y sin problema: son identificadores OPACOS del protocolo, no contenido. Es lo UNICO que el AggregatorSink escribe en linea con el entrante -- crece con source_refs = source_refs || $n::jsonb en el DO UPDATE (D-044.26). SOBREVIVE al vaciado del trio en estado terminal: cuando el literal se borra por INV-13, esto es el rastro que queda de que hubo mensajes y cuales.';
COMMENT ON COLUMN public.intake_jobs.artifacts          IS 'Salidas VERSIONADAS de cada paso del pipeline: {"p2":{...},"p3":{...},"p4":{...}}. Es lo que permite que el redelivery SALTE etapas ya resueltas en vez de re-pagar el LLM. DEFAULT {} porque un job recien abierto no ha producido nada todavia. NO se vacia en estado terminal: a diferencia del trio del sobre, esto NO es literal del cliente sino la decision estructurada que tomo el pipeline, y sigue teniendo lector (auditoria, depuracion de un presupuesto raro).';
COMMENT ON COLUMN public.intake_jobs.error              IS 'Causa del ULTIMO fallo, para el operador. NULL mientras no haya fallado nada. AVISO PARA QUIEN LA ESCRIBA: aqui NO va literal del cliente ni contenido descifrado -- esta columna esta EN CLARO y no se vacia en estado terminal, asi que un mensaje de error que arrastre el texto del cliente convertiria una tabla sin PII en una tabla con PII permanente. Va el motivo tecnico y el identificador, no el contenido.';
COMMENT ON COLUMN public.intake_jobs.intake_id          IS 'Borrador de presupuesto creado al final del pipeline. FK LOGICA a intakes.id, SIN REFERENCES (misma razon que event_id). NULL hasta que el job llega a draft/done: la escribe la Ola 3, no esta ola. Un job failed puede quedarse con esto en NULL para siempre, y es correcto: no llego a producir borrador.';
COMMENT ON COLUMN public.intake_jobs.created_at         IS 'Momento de apertura de la ventana. Usa el DEFAULT now(). No se pisa nunca. OJO: NO es la base de fechas del presupuesto -- esa es message_ts, que es el ts del mensaje del cliente y no el del reloj del servidor.';
COMMENT ON COLUMN public.intake_jobs.updated_at         IS 'Momento del ultimo cambio. Usa el DEFAULT now() en el alta y lo pone a now() cada transicion de estado y cada anadido a source_refs. RETENCION: esta tabla NO tiene poda por tiempo y eso es una DECISION, no un olvido -- misma situacion que intakes, flow_events y webhook_outbox tras el ADR-0043 (Plan 046). Lo que si se recorta es el CONTENIDO sensible, por INV-13 y no por reloj.';

COMMENT ON COLUMN public.tenant_settings.aggregation_window_seconds IS
    'Segundos que el agregador espera desde el PRIMER mensaje de una ventana antes de cerrarla y disparar el pipeline de captacion (Plan 044 - T1.0/T1.2). DEFAULT 45, y ese default ALCANZA A LAS FILAS QUE YA EXISTIAN: no hay columna anterior de la que derivar un valor mejor, asi que el default ES el backfill (al reves que el profile de la 0063, donde el default habria destruido informacion). 0 ES VALOR LEGITIMO y significa FLUSH INMEDIATO -- sin agregacion, un pipeline por mensaje, que es lo que el sistema hacia antes de esta ola. SIN TECHO SUPERIOR a proposito: ningun requisito lo discutio y un limite inventado no se podria defender; el CHECK solo descarta el negativo, que no significa nada. Dato de NEGOCIO EN CLARO (ADR-0009): cero PII, cero llaves.';
