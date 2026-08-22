-- ============================================================
-- 0069: contacts.push_name — EL SOBRE DE TRES PIEZAS
-- (Plan 046 · Ola 4 · T4.2, MD-046.5 / D-046.17 / D-046.18 / D-046.19;
--  cierra REQ-17 y la asignación literal del ADR-0034 §Decisión 3).
--
-- QUÉ GUARDA, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- La 0005 trajo `push_name TEXT NULL` y la 0006 lo dejó EXPRESAMENTE EN CLARO
-- («R-d del ADR-0017»): cuando se cifró el identificador del contacto, el nombre
-- que WhatsApp reporta en cada entrante se consideró dato de negocio de segunda.
-- El ADR-0034 revisó esa clasificación y asignó literalmente «Cifrar
-- contacts.push_name → Plan 046, ola de saneo» (§Decisión 3), rechazando el statu
-- quo en su tabla de Alternativas. Por su propia regla de admisión —«¿esto es
-- cuantificable para el negocio, o identifica a una persona?»— un nombre propio es
-- nivel 2 y va cifrado. Esta migración le monta el sobre:
--
--   * push_name_enc    — envelope AES-256-GCM del nombre TAL CUAL (nonce fresco).
--   * push_name_dek    — DEK por-fila (32B), envuelta por la KEK maestra.
--   * push_name_kek_id — key_id de la KEK que envolvió la DEK (discriminador del Rekey).
--
-- ADR-0009: aquí solo vive dato de NEGOCIO cifrado. La KEK NO vive en esta BD
-- (env/secret store, design.md §10.A) y la DEK del Edge —la del store de whatsmeow,
-- ADR-0007— no se toca ni se menciona: son cosas distintas que comparten nombre.
--
-- 🔴 SON TRES PIEZAS Y NO CUATRO: AQUÍ NO HAY ÍNDICE CIEGO, Y NO ES UN OLVIDO
-- ------------------------------------------------------------
-- El sobre de `value` (0006) y el de `self_pn` (0068) llevan un `_bidx` porque hay
-- consultas que buscan POR ESE VALOR: la deduplicación de refs por (tenant, kind,
-- value_bidx) en un caso, el anti-self-loop y el aviso de tope de dispositivos en el
-- otro. Del push_name NO BUSCA NADIE: no aparece en un solo `WHERE`, ni en una PK,
-- ni en un índice. Un bidx aquí sería un HMAC que se calcula en cada escritura, se
-- almacena en cada fila y no responde a ninguna pregunta — coste permanente y cero
-- lecturas. Si algún día alguien quiere buscar contactos por nombre, ESE día se
-- añade la columna y se rellena; hoy no existe la consulta que lo justifique.
--
-- 🔴 ESTA MIGRACIÓN NO CIFRA NADA, Y ESO NO ES UN OLVIDO (mismo argumento que 0068)
-- ------------------------------------------------------------
-- Cifrar exige la KEK, y POSTGRES NO LA TIENE — precisamente por no tenerla es por
-- lo que este sobre protege algo (0006:20-21). El relleno lo hace un paso de Go:
-- `contact.PostgresResolver.BackfillPushName`, colgado del arranque del servidor
-- (internal/flujos/contact/backfill_push_name.go). No hay runbook SQL equivalente,
-- al revés que el backfill del Plan 053 (docs/runbooks/backfill-053-owner-event-id.sql),
-- que es SQL porque no cifra.
--
-- 🔴 `push_name` SE CONSERVA Y QUEDA VACÍA — SU DROP ES DE T5.4 (D-046.17)
-- ------------------------------------------------------------
-- El enunciado viejo de T4.2 hablaba de «borrado de la columna en claro». Ese borrado
-- YA NO ES DE ESTA TAREA: la Ola 5 retira `contacts.push_name` y `fleet_sessions.self_pn`
-- EN UNA SOLA migración (T5.4), cuando las dos estén verificadas EN CAMPO. Aquí la
-- columna sigue existiendo por dos razones, y la segunda es la que importa:
--   (1) el backfill necesita de dónde leer, y un rollback del binario durante el
--       despliegue no puede encontrarse la columna borrada;
--   (2) MIENTRAS EXISTA, EL CONTEO EN CLARO ES LA PRUEBA de que el backfill funcionó.
--       Borrarla el mismo día que se cifra sería quedarse sin el único testigo — y el
--       DROP no protege contra el rollback, que es la comprobación que ya se hizo.
--
-- ------------------------------------------------------------
-- 🔴 LAS TRES REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrations/migrate.go): re-aplica TODO el
-- directorio en cuanto cambia el hash del conjunto. Se dice qué se hizo con cada regla:
--
-- (1) SIN DEFAULT — SE APLICA. Un default en `push_name_enc`/`push_name_dek` sería un
--     texto cifrado que ninguna KEK abre, y uno en `push_name_kek_id` etiquetaría con
--     una KEK a filas SIN sobre, metiéndolas en el barrido del Rekey para re-envolver
--     una DEK que no existe. Las tres nacen NULL, y NULL significa exactamente «este
--     contacto no tiene nombre conocido, o su backfill todavía no ha corrido».
--     ⚠️ OJO AL CONTRASTE CON LA 0007, que hizo lo contrario en esta MISMA tabla:
--     `value_kek_id TEXT NOT NULL DEFAULT '1'`. Allí el default era correcto porque
--     TODA fila tenía ya un `value_dek` que envolver, del Plan 011. Aquí no: un
--     contacto sin nombre no tiene nada dentro del sobre. Copiar aquel molde sería el
--     error.
--
-- (2) BACKFILL CON GUARD `WHERE ... IS NULL` — NO SE APLICA EN ESTE FICHERO, porque
--     no hay backfill aquí (ver arriba). La regla NO desaparece, se MUDA: el paso de
--     Go lleva su propio centinela —«filas con push_name no nulo y push_name_enc
--     IS NULL»— o el segundo arranque re-cifraría filas ya cifradas. Y como el nonce
--     es fresco por escritura, cada reinicio dejaría un `push_name_enc` distinto para
--     el mismo nombre sin que NADIE se enterase: escritura muda, la peor clase.
--     Aquí el testigo del centinela es el propio `_enc` y no un bidx (que no existe),
--     y funciona porque el backfill VACÍA la columna en claro en el mismo UPDATE: la
--     fila deja de casar por los dos lados a la vez.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ ... IF is_nullable = 'YES'` — NO SE APLICA,
--     y se escribe en vez de dejar el silencio: aquí NO HAY `SET NOT NULL` NI LO
--     HABRÁ. Las tres columnas se quedan NULLables PARA SIEMPRE, porque un contacto
--     legítimamente puede no tener nombre —WhatsApp no siempre lo reporta, y la
--     verdad de campo de Jhoan es que «a veces llega posterior»—. Mismo caso NULLable
--     que el trío de tenant_integrations (0047:39-42) y que el sobre de la 0068: las
--     tres van juntas o no van —o las tres NULL, o las tres pobladas—, invariante que
--     vive en el CÓDIGO y no en un CHECK, para no bloquear la escritura parcial de una
--     rotación.
--
-- (4) La cuarta regla que la 0063 añadió (CHECK con nombre explícito, recreado en cada
--     replay) tampoco aplica: no hay dominio cerrado que acotar —dos blobs y un id de
--     clave— y la invariante «las tres o ninguna» es del código, por lo de (3).
--
-- EL REPLAY, AQUÍ BENIGNO — Y LA 0006 NO RESUCITA NADA
-- ------------------------------------------------------------
-- `ADD COLUMN IF NOT EXISTS` es, del segundo arranque en adelante, un NO-OP EXACTO.
-- Y el replay de la 0006 —que es la que declara `push_name TEXT` en su CREATE TABLE—
-- NO devuelve los nombres que el backfill vació: su guarda de clean-slate solo
-- descarta la tabla si todavía existe la columna `value` plana del esquema de la 0005
-- (0006:14-18), y desde el Plan 011 existe `value_bidx`, así que no se descarta; lo
-- que queda es un `CREATE TABLE IF NOT EXISTS` sobre una tabla que ya existe, que no
-- escribe ni un valor. El mismo argumento que la 0068 hizo con la 0028.
--
-- ⚠️ ORDEN: toca la MISMA tabla que la 0005/0006/0007 y va POR ENCIMA de las tres. No
-- lee ninguna de sus columnas en este DDL, pero renumerarla por debajo de la 0006
-- (que CREA la tabla en su forma cifrada) la rompería.
--
-- ⚠️ EL HUECO DE LA 0067 SIGUE VACÍO A PROPÓSITO (reservado a T4.4) y NO se rellena:
-- el runner lista el directorio embebido, lo ordena lexicográficamente y ejecuta lo
-- que hay, sin tabla de aplicadas ni exigencia de continuidad — hoy ya falta la 0021
-- y el sistema arranca. Comprobado en campo con la 0068.
--
-- ADITIVA e IDEMPOTENTE. SchemaVersion sube a 0.42.0.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE. Las ejecuta el barrido del CLI.
-- ------------------------------------------------------------
--
-- (V1) Las tres columnas existen, NULLABLES y SIN default:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'contacts'
--      AND column_name LIKE 'push\_name\_%'
--    ORDER BY column_name;
--
--   Salida esperada — EXACTAMENTE tres filas, `is_nullable` = YES en todas y
--   `column_default` vacío en todas:
--
--    column_name      | data_type | is_nullable | column_default
--   ------------------+-----------+-------------+----------------
--    push_name_dek    | bytea     | YES         |
--    push_name_enc    | bytea     | YES         |
--    push_name_kek_id | text      | YES         |
--   (3 rows)
--
--   ⚠️ Y NO debe aparecer ninguna cuarta fila `push_name_bidx`: si aparece, alguien
--   copió el molde de la 0068 sin leer por qué aquí son tres.
--
-- (V2) El índice del CONTEO de la rotación existe y es PARCIAL:
--
--   SELECT indexdef FROM pg_indexes
--    WHERE schemaname = 'public' AND tablename = 'contacts'
--      AND indexname = 'idx_contacts_push_name_kek';
--
--   El `indexdef` tiene que contener `(push_name_kek_id)` y el
--   `WHERE (push_name_kek_id IS NOT NULL)`.
--
-- (V3) Criterio (a) de T4.2, DESPUÉS de correr el backfill de Go — las dos mitades,
--      porque vaciar sin cifrar también daría cero en la primera:
--
--   SELECT count(*) AS en_claro FROM public.contacts
--    WHERE push_name IS NOT NULL;                           -- esperado: 0
--   SELECT count(*) AS cifrados FROM public.contacts
--    WHERE push_name_enc IS NOT NULL;                       -- esperado: nº de filas que tenían nombre
--
--   🔴 LA PRIMERA MITAD VA SIN `AND push_name <> ''`, Y ESA DIFERENCIA CON LA V3 DE LA
--   0068 ES DELIBERADA. Allí el backfill EXCLUÍA la cadena vacía —no normalizaba, así
--   que habría engordado un contador de omitidas en cada arranque— y su criterio tuvo
--   que llevar el `<> ''` para poder dar cero. Aquí no hay normalización y por tanto
--   no hay nada que falle: el backfill VACÍA también la cadena vacía, en su propia
--   sentencia y sin cifrarla. Se decidió así, y no copiando el molde, por dos razones:
--     (i) cifrar la cadena vacía dejaría `push_name_enc` NO NULO con el sobre de un
--         no-valor, y el centinela `IS NULL` de MD-046.5 dejaría de casar ⇒ el nombre
--         real que llegase después SE PERDERÍA PARA SIEMPRE, que es justo lo que
--         MD-046.5 existe para impedir;
--     (ii) a diferencia del self_pn —cuyo residuo se auto-sanea en el siguiente
--         Heartbeat, porque la guarda de SetSelfPn casa con el vacío— aquí NO HAY
--         latido equivalente: ni el INSERT ni el UPDATE de resolveExisting tocarían
--         nunca esa fila. Lo que el backfill no vacíe, no lo vacía nadie jamás.
--   Desde Go la cadena vacía es además INALCANZABLE (nullStr la convierte en NULL en
--   los dos INSERT, y el UPDATE está guardado por «nombre no vacío»): el manejo es
--   defensivo, contra SQL escrito a mano o un binario antiguo. En UAT hoy hay CERO.
--
--   🔴 ESTA CONSULTA ES LA SEÑAL, Y NO UNA ALERTA (D-046.18, decisión de Jhoan). El
--   residuo PII se acepta y se vigila con una consulta DOCUMENTADA, no con una métrica:
--   en UAT nadie consume las series —el cron raspa /metrics con `-o /dev/null`— así que
--   una métrica más sería otro sitio donde mirar, no un aviso. La consulta dice la
--   verdad del presente siempre que se corra, sin depender de que un cron siga
--   instalado ni de que alguien abra el log en el minuto del arranque.
--   Tamaño real medido contra la Neon de UAT el 2026-08-21, ANTES de desplegar:
--   `public.contacts` tiene 15 filas, 13 con nombre y 0 con la cadena vacía, un solo
--   tenant. O sea que `en_claro` debe pasar de 13 a 0 y `cifrados` de 0 a 13.
--
-- (V4) La invariante «las tres o ninguna», que no tiene CHECK que la vigile:
--
--   SELECT count(*) AS sobres_rotos FROM public.contacts
--    WHERE num_nonnulls(push_name_enc, push_name_dek, push_name_kek_id)
--          NOT IN (0, 3);                                   -- esperado: 0
-- ============================================================

-- Las tres piezas del sobre. Nombres y tipos calcados del sobre de `value` en esta
-- misma tabla (0006:48-50 + 0007:27) y del de `self_pn` (0068), con el prefijo del
-- campo que protegen: <campo>_enc / _dek / _kek_id. Sin _bidx: nadie busca por nombre.
ALTER TABLE public.contacts ADD COLUMN IF NOT EXISTS push_name_enc    BYTEA;
ALTER TABLE public.contacts ADD COLUMN IF NOT EXISTS push_name_dek    BYTEA;
ALTER TABLE public.contacts ADD COLUMN IF NOT EXISTS push_name_kek_id TEXT;

-- ------------------------------------------------------------
-- El índice del CONTEO de la rotación de KEK.
-- ------------------------------------------------------------
-- 🔴 SE LLAMA «DE LA ROTACIÓN» EN LAS OTRAS CUATRO TABLAS Y ESO ES MEDIO MENTIRA, así
-- que aquí se dice bien: este índice NO lo usa el barrido. El SELECT y el UPDATE del
-- barrido filtran por `push_name_kek_id <> $1`, y la desigualdad no es un predicado
-- indexable en un btree: durante una rotación casan casi todas las filas y el planner
-- se va a seq scan igualmente, que además es lo barato ahí. A quien sirve es a
-- `pendingSQL` —el `GROUP BY push_name_kek_id` de PendingByKeyID, que puede resolverse
-- con un index-only scan—, o sea a la consulta que se ejecuta CUANDO YA NO QUEDA NADA
-- y que es justo la que AUTORIZA retirar una KEK del keyring (§10.F). Paga en el
-- momento que importa, no durante el trabajo. Se conserva el sufijo `_kek` del nombre
-- por consistencia con las otras cuatro; lo que se corrige es la descripción.
--
-- T4.2 mete una SEGUNDA entrada de public.contacts en el censo de rekeyTargets (la
-- del sobre de push_name, junto a la que ya existía para el de value), y el barrido
-- de esa entrada filtra por `push_name_kek_id <> current`. Las cuatro tablas del
-- censo tienen su índice de rotación —idx_contacts_kek (0007:34),
-- idx_intake_buyer_data_kek (0045:193), tenant_integrations_kek_idx (0047:82) e
-- idx_fleet_sessions_self_pn_kek (0068)—, así que este sobre no va a ser el único sin él.
--
-- 🔴 PARCIAL, Y CON UNA FORMA DISTINTA A LA DE SU HERMANO idx_contacts_kek, QUE VA
-- SOBRE LA MISMA TABLA. Aquel es (tenant_id, value_kek_id) y NO parcial, y podía
-- serlo porque value_kek_id es NOT NULL DEFAULT '1': toda fila tiene sobre de value.
-- Aquí el trío es NULLable —un contacto sin nombre no tiene sobre— y esas filas
-- quedan fuera del barrido SOLAS, porque `NULL <> 'x'` no es TRUE en SQL
-- (0047:66-69). Fuera del índice, entonces: en una tabla donde solo una parte de las
-- filas tiene nombre, el índice parcial es más pequeño y no se reescribe cuando se
-- toca una fila sin sobre. Sin tenant_id delante porque la consulta del Rekey NO
-- filtra por tenant: barre global por key_id (rekey.go, pendingSQL/selectSQL), igual
-- que el de la 0068.
CREATE INDEX IF NOT EXISTS idx_contacts_push_name_kek
    ON public.contacts (push_name_kek_id)
    WHERE push_name_kek_id IS NOT NULL;

COMMENT ON COLUMN public.contacts.push_name_enc IS
  'Ultimo push_name visto, cifrado con envelope AES-256-GCM (nonce fresco por escritura), patron de contacts.value_enc (0006). Sustituye a push_name, que queda VACIA tras el backfill y se borra en T5.4 junto con fleet_sessions.self_pn (D-046.17). SIN indice ciego: nadie busca por nombre. NO se normaliza: es texto libre, no un identificador. GANA EL PRIMER NOMBRE NO VACIO -- el UPDATE lleva centinela push_name_enc IS NULL, no comparacion por contenido, porque dos cifrados del mismo texto nunca son iguales y una guarda por valor tomaria row-lock en cada entrante (MD-046.5). Dato de NEGOCIO (ADR-0009), nivel 2 del ADR-0034; NUNCA se loguea (PII). Hoy NO LO LEE NADIE: no aparece en ningun SELECT, asi que tampoco hay codigo de descifrado -- se escribira el dia que aparezca un lector. Plan 046 · T4.2.';
COMMENT ON COLUMN public.contacts.push_name_dek IS
  'DEK por-fila (32B) que cifra push_name_enc, envuelta por la KEK maestra (design.md seccion 10.B). La KEK NO vive en esta BD. NO tiene NADA que ver con la DEK del Edge (el store de whatsmeow, ADR-0007), que la nube jamas ve. Es una DEK DISTINTA de value_dek, en la misma fila: dos sobres independientes que rotan por separado. Plan 046 · T4.2.';
COMMENT ON COLUMN public.contacts.push_name_kek_id IS
  'key_id de la KEK que envolvio push_name_dek. Discriminador de la rotacion: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey). 🔴 Necesita su PROPIA entrada en el censo rekeyTargets, separada de la de value_kek_id: son dos sobres de la misma fila y el barrido de uno no ve al otro -- sin esa entrada esta DEK no rotaria jamas y la rotacion diria completa con estos nombres aun bajo la KEK vieja. NULL = contacto sin nombre conocido, y esas filas quedan fuera del barrido solas porque NULL <> valor no es TRUE en SQL. Plan 046 · T4.2.';

-- Se CORRIGE el comentario de la columna en claro, que desde la 0006 dice «EN CLARO»
-- y a partir de aqui miente. No se reescribe la 0006 (es historia); se enmienda desde
-- la migracion nueva, que es la convencion de la casa.
-- 🔧 GUARD AÑADIDO EN T5.4, y no es cosmético: la 0070 RETIRA esta columna, y en el
-- FULL-REPLAY siguiente el CREATE TABLE IF NOT EXISTS de la 0006 no la repone (la
-- tabla ya existe), así que un COMMENT pelado aquí revienta el arranque con «column
-- does not exist». Mismo patrón que el de `value` en la 0005. El texto se conserva
-- tal cual para la base virgen, donde esta columna sí llega a existir un instante.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'contacts'
          AND column_name = 'push_name'
    ) THEN
        COMMENT ON COLUMN public.contacts.push_name IS
          'OBSOLETA desde el Plan 046 · T4.2: el nombre vive cifrado en push_name_enc/_dek/_kek_id. Esta columna queda VACIA (el backfill de arranque la nulifica) y la retira la 0070 (T5.4), junto con fleet_sessions.self_pn en una sola migracion. Mientras existio, count(*) WHERE push_name IS NOT NULL fue la PRUEBA de que el backfill funciono, y debia dar 0 (D-046.17, D-046.18). NO la vuelvas a escribir: el codigo de T4.2 ya no lo hace.';
    END IF;
END $$;

-- Las tres son NULLables PARA SIEMPRE y van juntas o no van (o las tres NULL, o las
-- tres pobladas): invariante del CODIGO, sin CHECK, para no bloquear la escritura
-- parcial de una rotacion -- igual que el trio de tenant_integrations (0047:39-42) y
-- que el sobre de la 0068. Comprobable con la consulta (V4) de la cabecera.
