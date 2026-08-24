-- ============================================================
-- 0073: tenant_llm.via — LA VÍA ES CONFIGURACIÓN, NO CONSECUENCIA
-- (Plan 044 · Ola 1.5 · T1.5-2; ADR-0044, D-044.28, D-044.22 RATIFICADA,
--  REQ-33. Amplía la 0071).
--
-- QUÉ AÑADE, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- La 0071 dejó la tabla con UN eje: el PROVEEDOR (`anthropic`|`gemini`), que
-- dice A QUÉ TERCERO se llama. Faltaba el otro, y su ausencia era el error:
-- la VÍA (`local`|`api`), que dice QUIÉN EJECUTA — el fierro del cliente o un
-- proveedor externo. Los dos ejes son ORTOGONALES (D-044.22, design §8.1-bis) y
-- el proveedor solo tiene sentido DENTRO de la vía `api`.
--
--   * via — 'local' | 'api'. Default 'local'. Vocabulario CERRADO (ver el CHECK).
--
-- REQ-33 en una frase: mientras un tenant tenga una vía configurada, el sistema
-- JAMÁS usa la otra —ni como rescate, ni como fallback, ni «por esta vez»—, y
-- cambiar de vía es un ACTO DE CONFIGURACIÓN del tenant. Esta columna es dónde
-- vive ese acto.
--
-- 🔴 LO QUE ESTA MIGRACIÓN AFLOJA, Y POR QUÉ NO ES UNA REGRESIÓN
-- ------------------------------------------------------------
-- La 0071 puso `NOT NULL` en SEIS columnas y lo argumentó bien PARA LA PREMISA
-- QUE TENÍA: allí la fila SIGNIFICABA «este tenant tiene vía API», así que una
-- fila sin credencial no significaba nada y el estado «sin vía API» era LA
-- AUSENCIA DE FILA. D-044.28 cambia la premisa: la fila ya no significa «vía
-- API», significa «LA CONFIGURACIÓN LLM DE ESTE TENANT», y una configuración
-- `via='local'` es una configuración COMPLETA y legítima que no tiene —ni puede
-- tener— proveedor, modelo, sobre ni consentimiento: la vía local no llama a
-- ningún tercero, así que no hay a qué consentir ni con qué credencial.
--
-- Consecuencia: `provider`, `model`, el trío del sobre y `consented_at` pasan a
-- ser NULLables. 🔴 **PERO LA INVARIANTE NO SE PIERDE, CAMBIA DE FORMA**: lo que
-- antes vigilaban seis `NOT NULL` incondicionales lo vigilan ahora TRES CHECK
-- CONDICIONALES (abajo), y para la vía `api` la garantía es LA MISMA:
--
--   `via='api'` ⇒ provider, model, api_key_enc, api_key_dek, api_key_kek_id y
--   consented_at TIENEN QUE ESTAR. No existe fila `api` a medias.
--
-- Y se gana una que la 0071 no podía escribir: `via='local'` ⇒ la fila no
-- arrastra credencial. «Una sola vía activa» (REQ-33) deja de ser una promesa
-- del código y pasa a ser una forma que la tabla admite.
-- 🔧 ESA FRASE ERA UNA PROMESA HASTA EL 2026-08-23 y ahora es cierta: la escribía
-- este encabezado, pero NINGÚN CHECK la sostenía —f.2 solo acota `via='api'` y
-- f.3 solo el sobre—, así que una fila `local` con credencial y consentimiento
-- era perfectamente legal. La sostiene ahora `tenant_llm_local_sin_credencial_check`
-- (f.4), añadido con la guarda por vía de `tenantllm.APIKey`: el esquema lo
-- prohíbe y el código no lo asume.
--
-- 🔴 `consented_at` DEJA DE SER `NOT NULL` Y ESO NO ABRE NINGUNA PUERTA. El
-- párrafo de la 0071 —«un DEFAULT now() haría que toda fila naciera consentida»—
-- sigue vigente palabra por palabra: NO se le pone default, y el CHECK
-- `tenant_llm_via_api_completa_check` impide que exista una fila `api` sin él.
-- La red debajo de la red sigue puesta; lo que cambia es que ya no cuelga de
-- filas que no van a llamar a nadie.
--
-- ============================================================
-- 🔴 LA TRAMPA QUE ESTA MIGRACIÓN NO PISA: `ADD COLUMN ... DEFAULT 'local'`
-- ============================================================
-- Desde PostgreSQL 11, `ADD COLUMN … DEFAULT x` **RELLENA LAS FILAS QUE YA
-- EXISTEN** con x (no reescribe la tabla, pero las filas leen x). Si esta
-- migración añadiera la columna con su default puesto:
--
--   1. TODA fila existente nacería con `via='local'` — incluidas las que tienen
--      credencial y consentimiento, o sea TODAS las de hoy;
--   2. el backfill de abajo, guardado por `WHERE via IS NULL`, no encontraría
--      NI UNA fila que tocar;
--   3. y el criterio de T1.5-2 pasaría **verde por vacío**: cero filas
--      reparadas, cero filas rotas visibles, y cada tenant que HOY paga su
--      cuenta de Anthropic despertaría en la vía local — que es exactamente el
--      corte de servicio silencioso que el backfill existe para evitar.
--
-- Por eso el orden es y tiene que ser: **(a) columna SIN default → (b) backfill
-- explícito → (c) `SET DEFAULT` → (d) `SET NOT NULL`**. El default gobierna a
-- las filas FUTURAS; a las viejas las gobierna el UPDATE, y solo el UPDATE.
--
-- ------------------------------------------------------------
-- LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- (1) SIN DEFAULT — **NO se aplica a `via`, y es la única excepción razonada**.
--     Las columnas de la 0071 no tienen default porque no hay valor «normal»
--     que inventar por el tenant y porque un sobre inventado no lo abre ninguna
--     KEK. `via` es el caso contrario: SÍ hay valor normal, lo fija REQ-33
--     («default `local`»), y es además el valor SEGURO —`local` no manda texto
--     a ningún tercero—. Un default aquí no inventa nada: declara el estado en
--     el que un tenant está mientras no configure otra cosa. Se pone DESPUÉS
--     del backfill, por lo dicho en el bloque de arriba.
-- (2) BACKFILL CON GUARDA `WHERE … IS NULL` — **SE APLICA ENTERA**, y es el
--     corazón de esta migración. Ver su bloque.
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ … IF is_nullable = 'YES'` — **SE
--     APLICA**: aquí sí se añade una columna a una tabla CON FILAS, que es
--     justo el caso para el que esa guarda existe (0068). Va después del
--     backfill: promover antes reventaría con las filas a medio rellenar.
-- (4) CHECK CON NOMBRE EXPLÍCITO, FUERA DEL CREATE Y RECREADO EN CADA REPLAY —
--     **SE APLICA a los CUATRO CHECK nuevos**, con la corrección que la 0071 pagó
--     con tiempo real el 2026-08-22: `DROP CONSTRAINT IF EXISTS` + `ADD`, nunca
--     inline. Un CHECK inline en un `CREATE TABLE IF NOT EXISTS` es NO-OP del
--     segundo arranque en adelante.
--
-- ⚠️ ORDEN: va DESPUÉS de la 0071 (necesita la tabla) y no toca ninguna otra.
-- ADITIVA e IDEMPOTENTE. ⚠️ **NO sube `SchemaVersion`**: el Plan 044 hace UN
-- SOLO bump al cierre, en T6.2 (INV-8).
--
-- ⚠️ LO QUE ESTA MIGRACIÓN **NO** HACE: no toca el eje PROVEEDOR. El CHECK
-- `tenant_llm_provider_check` sigue siendo `IN ('anthropic','gemini')` y `local`
-- SIGUE SIN ESTAR AHÍ, porque `local` es una VÍA y no un proveedor. Fusionar los
-- dos vocabularios en una columna es el error que D-044.22 nombró; esta
-- migración lo hace imposible dándole a cada eje su columna.
--
-- ⚠️ Y NO ABRE LA VÍA LOCAL. El pipeline local no existe hasta la Ola 1.6
-- (T1.6-3): `PUT /api/v1/tenant-llm` con `via:"local"` sigue respondiendo **422
-- `llm_provider_unavailable`** en esta ola. La columna admite el valor; la
-- puerta todavía no lo deja pasar. Se dice aquí para que nadie lea el CHECK
-- como «ya se puede».
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE. Las ejecuta el barrido del CLI.
-- ------------------------------------------------------------
--
-- (V1) La columna existe, es NOT NULL y su default es 'local':
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='tenant_llm' AND column_name='via';
--
--   Esperado: UNA fila — text | NO | 'local'::text.
--   🔴 Si `is_nullable` sale YES, el `DO $$` de abajo no promovió: hay filas con
--   `via` NULL y el backfill no las cubrió. Mirar (V4).
--
-- (V2) Las seis columnas de la 0071 quedaron NULLables (y NINGUNA otra se movió):
--
--   SELECT column_name, is_nullable FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='tenant_llm'
--    ORDER BY ordinal_position;
--
--   Esperado: tenant_id NO · provider YES · model YES · api_key_enc YES ·
--   api_key_dek YES · api_key_kek_id YES · consented_at YES · created_at NO ·
--   updated_at NO · via NO.
--
-- (V3) Los CINCO CHECK existen con su nombre (el de la 0071 + los cuatro nuevos):
--
--   SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
--    WHERE conrelid='public.tenant_llm'::regclass AND contype='c' ORDER BY conname;
--
--   Esperado: tenant_llm_local_sin_credencial_check · tenant_llm_provider_check ·
--   tenant_llm_sobre_completo_check · tenant_llm_via_api_completa_check ·
--   tenant_llm_via_check.
--
-- (V4) EL REPARTO DEL BACKFILL — la verificación que de verdad importa:
--
--   SELECT via, count(*) FROM public.tenant_llm GROUP BY via ORDER BY via;
--
--   Esperado: CERO filas con via NULL, y el reparto que dice el bloque del
--   backfill. ⚠️ Sobre una base de HOY el resultado esperado es **todas 'api'**,
--   porque la 0071 no admitía filas sin credencial: ver la nota «EL REPARTO
--   REAL» abajo. Un 'local' aquí, hoy, significa que alguien escribió una fila a
--   mano.
--
-- (V5) NINGUNA fila `api` a medias (lo fuerza el CHECK; se comprueba igual,
--      porque un CHECK añadido con NOT VALID no revisaría las filas viejas y
--      este se añade VALIDADO):
--
--   SELECT count(*) AS api_a_medias FROM public.tenant_llm
--    WHERE via='api' AND (provider IS NULL OR model IS NULL
--          OR api_key_enc IS NULL OR api_key_dek IS NULL
--          OR api_key_kek_id IS NULL OR consented_at IS NULL);   -- esperado: 0
--
-- (V6) NINGÚN sobre a medias (una fila indescifrable o una DEK huérfana):
--
--   SELECT count(*) AS sobre_a_medias FROM public.tenant_llm
--    WHERE num_nulls(api_key_enc, api_key_dek, api_key_kek_id) NOT IN (0, 3);
--                                                              -- esperado: 0
--
-- (V7) NINGUNA fila `local` con credencial viva — la que de verdad mira la
--      seguridad y no la forma (f.4). Una fila aquí es una clave de un tercero
--      servible bajo la vía que declara no llamar a nadie:
--
--   SELECT count(*) AS local_con_credencial FROM public.tenant_llm
--    WHERE via = 'local' AND api_key_enc IS NOT NULL;          -- esperado: 0
-- ============================================================

-- ------------------------------------------------------------
-- (a) LA COLUMNA, SIN DEFAULT. Ver «LA TRAMPA» arriba: el default se pone en
--     (c), después del backfill, y ese orden es la migración entera.
-- ------------------------------------------------------------
ALTER TABLE public.tenant_llm ADD COLUMN IF NOT EXISTS via TEXT;

-- ------------------------------------------------------------
-- (b) LO QUE SE AFLOJA. Seis `DROP NOT NULL`, uno por columna que la vía local
--     no puede rellenar. `DROP NOT NULL` sobre una columna que YA es NULLable no
--     falla ni avisa: es NO-OP exacto, así que estas seis líneas son inertes del
--     segundo arranque en adelante (no hace falta guardarlas con un DO $$).
--
--     🔴 NO se afloja `tenant_id` (es la PK) ni `created_at`/`updated_at` (los
--     pone el molde de la casa y no dependen de la vía).
-- ------------------------------------------------------------
ALTER TABLE public.tenant_llm ALTER COLUMN provider       DROP NOT NULL;
ALTER TABLE public.tenant_llm ALTER COLUMN model          DROP NOT NULL;
ALTER TABLE public.tenant_llm ALTER COLUMN api_key_enc    DROP NOT NULL;
ALTER TABLE public.tenant_llm ALTER COLUMN api_key_dek    DROP NOT NULL;
ALTER TABLE public.tenant_llm ALTER COLUMN api_key_kek_id DROP NOT NULL;
ALTER TABLE public.tenant_llm ALTER COLUMN consented_at   DROP NOT NULL;

-- ------------------------------------------------------------
-- (c) EL BACKFILL. Un DEFAULT no gobierna las filas que ya existen: esto es un
--     UPDATE explícito, y es la única sentencia de esta migración que cambia
--     DATOS y no forma.
--
-- LA REGLA, en dos líneas:
--   * fila COMPLETA para la vía api —proveedor, modelo, sobre entero y
--     `consented_at`, LAS SEIS—                     ⇒ 'api'
--   * todas las demás                               ⇒ 'local'
--
-- POR QUÉ ESA EXCEPCIÓN Y NO «todas a local». REQ-33 fija `local` como default
-- del PRODUCTO, y sería lo cómodo de escribir. Pero cada fila de esta tabla, hoy,
-- es un tenant que TECLEÓ SU CREDENCIAL DE ANTHROPIC Y FIRMÓ EL CONSENTIMIENTO:
-- mandarlo a la vía local sería apagarle un servicio que paga, en una migración,
-- sin avisarle y sin que nadie lo pidiera. El default es para quien no ha
-- elegido; estos ya eligieron, y lo que se hace es LEER su elección de los datos
-- que ya tienen, no reasignarla.
--
-- POR QUÉ EL CONSENTIMIENTO ENTRA EN LA CONDICIÓN y no basta la credencial:
-- porque la vía `api` es la que MANDA TEXTO DEL CLIENTE A UN TERCERO, y el
-- permiso para eso es `consented_at` (ADR-0030). Una fila con clave y sin
-- consentimiento —imposible hoy por la 0071, posible desde esta migración— es un
-- tenant que dejó la credencial preparada y NO autorizó la salida: activarle la
-- vía API por tener la clave sería consentir en su nombre.
--
-- POR QUÉ ES IDEMPOTENTE, que es lo que exige el full-replay: la guarda
-- `WHERE via IS NULL` hace que el segundo arranque no encuentre fila que tocar.
-- 🔴 Y ESO NO ES UNA OPTIMIZACIÓN, ES LA PROTECCIÓN: sin la guarda, un tenant
-- que hubiera cambiado su vía a mano —de 'api' a 'local', que es literalmente el
-- acto de configuración que REQ-33 le concede— vería su elección REESCRITA a
-- 'api' en el siguiente reinicio del servicio, porque su fila sigue teniendo
-- credencial y consentimiento. La guarda es lo que distingue «rellenar lo que
-- nunca se decidió» de «pisar lo que alguien decidió».
--
-- ⚠️ EL REPARTO REAL, dicho antes de que nadie se sorprenda leyendo (V4): sobre
-- una base migrada por la 0071 **TODA fila cae en la rama 'api'**, porque allí
-- las seis columnas eran NOT NULL y no existía forma de tener fila sin sobre ni
-- sin consentimiento. La rama 'local' NO es código muerto: es la que cubre las
-- filas que ESTA MIGRACIÓN empieza a permitir (y las que un restore parcial o
-- una edición manual pudieran dejar a medias). Por eso el test del backfill
-- FABRICA el estado previo en vez de correr contra una base recién migrada: una
-- base recién migrada da el criterio por verde con cero filas.
-- ------------------------------------------------------------
-- 🔧 SON LAS SEIS COLUMNAS DEL CHECK, Y NO LAS CUATRO QUE PARECEN BASTAR
-- (corrección del code review, 2026-08-23). La condición decidía mirando el trío
-- del sobre y `consented_at`; `tenant_llm_via_api_completa_check` (f.2, abajo)
-- exige ADEMÁS `provider` y `model`. Con cuatro, una fila con credencial y
-- consentimiento pero SIN proveedor —o sin modelo— se marcaría 'api' y acto
-- seguido REVENTARÍA el `ADD CONSTRAINT` de f.2, que se añade VALIDADO: el
-- arranque muere y nadie sabe por qué, porque el fichero que la marcó y el que
-- la rechaza son el mismo.
--
-- Hoy no pasa POR ACCIDENTE HISTÓRICO, no por diseño: la 0071 creaba esas dos
-- columnas NOT NULL, así que no hay forma de tener una fila así en una base sana.
-- Justo por eso hay que alinearlo ahora — el accidente lo sostiene una premisa
-- que ESTA migración acaba de retirar, y el día que llegue la fila (un restore
-- parcial, un INSERT a mano, un `UPDATE ... SET provider = NULL` de alguien
-- limpiando) el fallo no se parecería en nada a su causa.
--
-- LA REGLA, EN UNA LÍNEA: la condición del backfill es EXACTAMENTE la del CHECK
-- que gobierna la vía a la que manda. Cualquier fila que no la cumpla entera cae
-- a 'local', que es el valor que no llama a nadie.
--
-- ⚠️ LO QUE ESTA CORRECCIÓN **NO** SALVA, dicho para que nadie se confíe: con
-- `tenant_llm_local_sin_credencial_check` (f.4) puesto, una fila con SOBRE ENTERO
-- y consentimiento pero sin proveedor es INEXPRESABLE en las dos direcciones —
-- como 'api' la rechaza f.2, como 'local' la rechaza f.4—, así que el arranque
-- muere igual; lo que cambia es QUÉ constraint la nombra. Se corrige de todas
-- formas, y por dos razones que no dependen de eso: (1) el fichero dejaba de
-- decir lo mismo en dos sitios, que es como nace el siguiente defecto; y (2) sin
-- f.4 —una base a la que le falte, un futuro en que se retire— la condición de
-- cuatro columnas SÍ marcaba 'api' una fila que f.2 rechaza, y eso era un
-- arranque muerto que esta línea evita.
UPDATE public.tenant_llm
   SET via = CASE
                WHEN provider       IS NOT NULL
                 AND model          IS NOT NULL
                 AND api_key_enc    IS NOT NULL
                 AND api_key_dek    IS NOT NULL
                 AND api_key_kek_id IS NOT NULL
                 AND consented_at   IS NOT NULL THEN 'api'
                ELSE 'local'
             END
 WHERE via IS NULL;

-- ------------------------------------------------------------
-- (d) EL DEFAULT, YA SIN PELIGRO: el backfill terminó, así que a partir de aquí
--     solo gobierna filas FUTURAS. `local` es el valor seguro (no sale texto
--     hacia ningún tercero) y es el que REQ-33 nombra.
--
--     ⚠️ No sustituye a la escritura explícita del store: `tenantllm.Postgres`
--     escribe `via` en cada upsert. Este default cubre al INSERT que alguien
--     teclee a mano, y hace que «me olvidé de la vía» sea la vía que no gasta el
--     dinero del cliente.
-- ------------------------------------------------------------
ALTER TABLE public.tenant_llm ALTER COLUMN via SET DEFAULT 'local';

-- ------------------------------------------------------------
-- (e) LA PROMOCIÓN A NOT NULL, guardada (patrón 0068/0063 regla 3). El `IF` no
--     es cosmético: `SET NOT NULL` revisa la tabla ENTERA cada vez que se
--     ejecuta, y este fichero se re-ejecuta en cada arranque que cambie el hash
--     del directorio. Con la guarda, del segundo arranque en adelante ni se
--     mira.
--
--     Si alguna fila quedara con `via` NULL, esto FALLA y el arranque muere —y
--     eso es lo correcto: significaría que el backfill no cubrió un caso, y
--     arrancar con filas sin vía sería arrancar sin saber a quién se llama.
-- ------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_schema = 'public'
           AND table_name   = 'tenant_llm'
           AND column_name  = 'via'
           AND is_nullable  = 'YES'
    ) THEN
        ALTER TABLE public.tenant_llm ALTER COLUMN via SET NOT NULL;
    END IF;
END $$;

-- ------------------------------------------------------------
-- (f) LOS CUATRO CHECK, con nombre y FUERA de cualquier CREATE, recreados en cada
--     replay (regla 4, con la corrección que la 0071 pagó cara).
-- ------------------------------------------------------------

-- (f.1) El vocabulario de la VÍA. Dos valores y no más. Crecer el dominio =
--       editar ESTA línea, y el replay la aplica de verdad.
--       🔴 `anthropic`/`gemini` NO van aquí: son PROVEEDORES, otro eje, otra
--       columna, otro CHECK (D-044.22).
ALTER TABLE public.tenant_llm
    DROP CONSTRAINT IF EXISTS tenant_llm_via_check;
ALTER TABLE public.tenant_llm
    ADD CONSTRAINT tenant_llm_via_check CHECK (via IN ('local','api'));

-- (f.2) LA INVARIANTE QUE SUSTITUYE A LOS SEIS `NOT NULL` DE LA 0071, y el
--       motivo por el que aflojarlos no es una regresión: una fila `api` sin
--       proveedor, sin modelo, sin sobre o sin consentimiento NO PUEDE EXISTIR.
--
--       Se escribe como implicación (`via <> 'api' OR (…)`) y no con un CASE
--       porque así dice exactamente lo que significa: «si la vía es api,
--       entonces todo esto». Con `via` NULL —estado transitorio que sólo puede
--       darse dentro de esta misma migración, entre (a) y (e)— la expresión da
--       NULL y el CHECK NO se considera violado, que es la semántica de SQL y
--       aquí es la que conviene: la columna acaba NOT NULL dos bloques más
--       arriba.
--
--       ⚠️ Este CHECK se añade VALIDADO (sin NOT VALID) a propósito: si alguna
--       fila existente lo violara, el arranque muere en vez de dejar una
--       constraint que no vigila el pasado. Sobre una base sana no puede
--       ocurrir: las filas de la 0071 tienen las seis columnas llenas.
ALTER TABLE public.tenant_llm
    DROP CONSTRAINT IF EXISTS tenant_llm_via_api_completa_check;
ALTER TABLE public.tenant_llm
    ADD CONSTRAINT tenant_llm_via_api_completa_check CHECK (
        via <> 'api' OR (
            provider       IS NOT NULL AND
            model          IS NOT NULL AND
            api_key_enc    IS NOT NULL AND
            api_key_dek    IS NOT NULL AND
            api_key_kek_id IS NOT NULL AND
            consented_at   IS NOT NULL
        )
    );

-- (f.3) «LAS TRES O NINGUNA» — la invariante del sobre que la 0071 le confiaba a
--       tres NOT NULL y que ahora hay que escribir, porque «ninguna» pasó a ser
--       un estado legítimo (la fila `local`). Lo que sigue prohibido es UNA o
--       DOS: un `api_key_enc` sin su DEK es un blob que no abre nadie, y una DEK
--       sin `kek_id` es una fila que la rotación no sabe re-envolver.
--
--       El argumento de la 0071 sigue siendo el que lo hace seguro: `rekeyBatch`
--       actualiza (dekCol, kekCol) EN LA MISMA sentencia y jamás toca `_enc`, así
--       que ningún paso legítimo pasa por un estado a medias.
ALTER TABLE public.tenant_llm
    DROP CONSTRAINT IF EXISTS tenant_llm_sobre_completo_check;
ALTER TABLE public.tenant_llm
    ADD CONSTRAINT tenant_llm_sobre_completo_check CHECK (
        (api_key_enc IS     NULL AND api_key_dek IS     NULL AND api_key_kek_id IS     NULL)
     OR (api_key_enc IS NOT NULL AND api_key_dek IS NOT NULL AND api_key_kek_id IS NOT NULL)
    );

-- (f.4) 🔴 LA VÍA LOCAL NO ARRASTRA CREDENCIAL — el CHECK que faltaba, y que es
--       el ÚNICO de los tres que mira la seguridad y no la forma.
--
--       Los dos de arriba acotan la vía `api` (f.2) y el sobre (f.3), pero
--       NINGUNO decía nada de una fila `via='local'` CON credencial y
--       consentimiento vivos: era LEGAL en el esquema, y `tenantllm.APIKey`
--       —que hasta este commit no miraba la vía— la habría descifrado y servido.
--       Eso es una credencial de un tercero VIVA bajo la vía que declara no
--       llamar a nadie: REQ-33 dice que mientras la vía sea una, la otra JAMÁS se
--       usa, y una fila así hace de esa frase una promesa del código en vez de
--       una forma de la tabla. La escritura por el store nunca la crea (el Upsert
--       retira el sobre al cambiar de vía); esto cubre a todo lo demás.
--
--       Se comprueba por `api_key_enc` y no por las tres porque f.3 ya garantiza
--       que van juntas: prohibir la primera prohíbe el sobre entero, y un CHECK
--       que repite lo que otro ya afirma se contradice solo el día que uno cambie.
--       `consented_at` NO entra: un consentimiento firmado y luego revocado por
--       cambio de vía es un hecho histórico sin poder —sin credencial no se llama
--       a nadie por muy consentido que esté—, y borrarlo perdería la fecha.
--
--       ⚠️ CONSECUENCIA QUE HAY QUE SABER ANTES DE LEER EL BACKFILL: con este
--       CHECK puesto, «fila local con credencial» deja de ser un estado
--       representable, y con él desaparece el único dato con el que se podía ver
--       fallar la guarda `WHERE via IS NULL` del bloque (c). La guarda SE QUEDA
--       —sigue siendo lo que hace idempotente el UPDATE y lo que documenta la
--       intención—, pero su protección pasó a ser redundante con esta constraint:
--       ninguna fila `local` puede ya cumplir la condición del CASE. Se dice aquí
--       para que nadie lea la guarda como si aún fuera la última línea, ni la
--       retire creyendo que por eso sobra.
--
--       Como los otros dos, VALIDADO (sin NOT VALID): si una base viva tuviera ya
--       esa fila, el arranque muere en vez de dejar la credencial servible. Sobre
--       una base sana no puede ocurrir — las filas de la 0071 tienen las seis
--       columnas llenas y el backfill las manda a 'api'.
ALTER TABLE public.tenant_llm
    DROP CONSTRAINT IF EXISTS tenant_llm_local_sin_credencial_check;
ALTER TABLE public.tenant_llm
    ADD CONSTRAINT tenant_llm_local_sin_credencial_check CHECK (
        via <> 'local' OR api_key_enc IS NULL
    );

-- ------------------------------------------------------------
-- (g) LOS COMENTARIOS. Un `COMMENT ON COLUMN` sobre una columna inexistente mata
--     el arranque, así que van AL FINAL: aquí `via` ya existe con seguridad.
--     Los de la 0071 se REESCRIBEN donde su texto quedó desmentido por esta
--     migración (un comentario que miente es peor que no tenerlo), y los que
--     siguen siendo ciertos no se tocan — el replay de la 0071 los repone tal
--     cual y esta migración corre DESPUÉS, así que el último que habla es éste.
-- ------------------------------------------------------------
COMMENT ON COLUMN public.tenant_llm.via IS
    'VIA de ejecucion del LLM para este tenant (REQ-33, D-044.28, ADR-0044): local = el fierro del propio cliente (su Ollama, por el frame de inferencia del ADR-0045) | api = un proveedor externo, con la credencial de la fila. DEFAULT local, que es el valor SEGURO: no sale texto hacia ningun tercero. UNA SOLA VIA ACTIVA: mientras esta columna diga una, el sistema JAMAS usa la otra -- ni rescate, ni fallback, ni "por esta vez"; cambiar de via es un ACTO DE CONFIGURACION del tenant, no una decision del runtime. 🔴 EJE DISTINTO DE provider: la via dice QUIEN EJECUTA, el proveedor dice A QUE TERCERO se llama, y el proveedor solo tiene sentido dentro de la via api (D-044.22). local NO es un proveedor y por eso no esta -- ni podria estar -- en tenant_llm_provider_check. ⚠️ En la Ola 1.5 la API todavia RECHAZA elegir local con 422 llm_provider_unavailable: la columna admite el valor, la puerta aun no lo deja pasar (lo abre T1.6-3).';
COMMENT ON COLUMN public.tenant_llm.provider IS
    'Proveedor de la via API. Vocabulario CERRADO y de wApp, no del cliente, acotado por tenant_llm_provider_check: anthropic (unica implementacion cableada del Plan 044, T0.2) | gemini (stub que compila y falla nombrado). 🔧 NULLable desde la 0073 (T1.5-2): una fila via=local NO tiene proveedor porque no llama a ningun tercero. Para via=api sigue siendo obligatorio, y lo vigila tenant_llm_via_api_completa_check. 🔴 local NO ESTA AQUI y no es un olvido: local es una VIA (columna via), no un proveedor.';
COMMENT ON COLUMN public.tenant_llm.model IS
    'Identificador de modelo del proveedor (p.ej. claude-sonnet-4-5). Texto libre SUYO: wApp no mantiene un catalogo de modelos ajenos ni lo valida contra una lista, porque esa lista caduca cada pocas semanas y una lista caducada rechaza modelos validos. Solo se acota su longitud en la API. 🔧 NULLable desde la 0073: sin proveedor no hay modelo del proveedor. Obligatorio para via=api (tenant_llm_via_api_completa_check).';
COMMENT ON COLUMN public.tenant_llm.api_key_enc IS
    'Envelope AES-256-GCM (nonce fresco por escritura) de la API key del proveedor. NUNCA se devuelve por la API ni se loguea; se descifra en el borde del pipeline, para llamar al proveedor. 🔧 NULLable desde la 0073 (T1.5-2): la fila de un tenant en via=local existe y NO tiene credencial -- ya no es cierto que "una fila sin clave no significa nada", porque la fila pasó a significar LA CONFIGURACION LLM del tenant y no "su via API". Lo que sigue siendo imposible: una fila via=api sin clave (tenant_llm_via_api_completa_check) y un sobre a medias (tenant_llm_sobre_completo_check).';
COMMENT ON COLUMN public.tenant_llm.api_key_dek IS
    'DEK por-fila (32B) que cifra api_key_enc, envuelta por la KEK maestra (design.md seccion 10.B). La KEK NO vive en esta BD. NO tiene NADA que ver con la DEK del Edge (el store de whatsmeow, ADR-0007), que la nube jamas ve. 🔧 NULLable desde la 0073: va con su sobre -- las tres columnas o ninguna (tenant_llm_sobre_completo_check).';
COMMENT ON COLUMN public.tenant_llm.api_key_kek_id IS
    'key_id de la KEK que envolvio api_key_dek. Discriminador de la rotacion: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey, censo rekeyTargets). 🔧 NULLable desde la 0073: las filas via=local no tienen sobre y se caen SOLAS del barrido, porque NULL <> current no es TRUE en SQL -- el mismo comportamiento que tenant_integrations, fleet_sessions e intake_jobs, y por eso el censo NO necesita cambiar. ⚠️ Lo que si dejo de ser cierto es la nota de rekey.go que decia "su trio es NOT NULL": corregida en el mismo commit.';
COMMENT ON COLUMN public.tenant_llm.consented_at IS
    'Momento del consentimiento explicito del tenant a que el texto de sus conversaciones salga hacia un proveedor externo (ADR-0030). SIN DEFAULT: un DEFAULT now() haria que toda fila naciera "consentida" sin que nadie consintiera, que es exactamente lo que esta columna existe para impedir. El 400 de "PUT sin consentimiento" lo da la API antes de llegar aqui. Se REFRESCA en cada PUT de la via api: el cuerpo re-afirma el consentimiento cada vez. 🔧 NULLable desde la 0073 (T1.5-2) y NO es un agujero: la via local no manda nada a ningun tercero, asi que no hay a que consentir; para via=api el consentimiento sigue siendo OBLIGATORIO y lo fuerza tenant_llm_via_api_completa_check. La red debajo de la red sigue puesta, solo que ya no cuelga de filas que no van a llamar a nadie.';
COMMENT ON TABLE public.tenant_llm IS
    'Configuracion LLM del tenant: la VIA (local|api, columna via -- REQ-33/D-044.28) y, cuando la via es api, el proveedor y su credencial cifrada en tres columnas por envelope encryption (patron de tenant_integrations.secret_*, 0047). 🔧 La 0073 cambio lo que significa la fila: la 0071 decia "SIN FILA = el tenant no tiene via API", y desde la 0073 la fila describe la configuracion ENTERA -- sin fila = el tenant esta en la via local por defecto, que es un estado legitimo y el mas comun. Aqui NO hay dato de negocio: hay una CREDENCIAL DE UN TERCERO (la cuenta del proveedor la paga el tenant). CERO PII. La clave NUNCA sale por la API: el GET solo dice si hay (key_set).';
