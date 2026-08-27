-- ============================================================
-- 0082: intake_case_bank — EL BANCO DE CASOS CON CONSENTIMIENTO
-- (Plan 044 · Ola 5 · T5.3; design §6.4. ADR-0034 minimización de datos,
--  ADR-0030 el texto del cliente y los terceros.)
--
-- QUÉ GUARDA, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- Un DATASET DE EVALUACIÓN: solicitudes reales de clientes, ya ANONIMIZADAS, con
-- la interpretación que el pipeline DEBERÍA haber producido. Sirve para medir si
-- P2/P3/P4 mejoran o empeoran cuando se cambia un prompt o un modelo — hoy esa
-- pregunta solo se responde mandando mensajes de verdad por WhatsApp.
--
--   * tenant_id   — de quién es el caso. El dataset NO es global: el texto es del
--                   cliente de UN negocio y el consentimiento lo dio ESE negocio.
--   * consented   — el consentimiento explícito. Ver el bloque del CHECK.
--   * source_text — el literal del cliente YA ANONIMIZADO (sin nombres, sin
--                   teléfonos, sin JID). NUNCA el texto crudo.
--   * expected    — la interpretación correcta, CURADA A MANO. NULLable: un caso
--                   puede entrar al banco antes de que nadie lo haya curado, y
--                   esa es una fila útil (el material está, falta la etiqueta).
--
-- 🔴 ESTA TABLA NO LLEVA SOBRE DE CIFRADO, Y ESO ES UNA AFIRMACIÓN, NO UN OLVIDO
-- ------------------------------------------------------------
-- Todas sus hermanas que guardan texto del cliente lo llevan: `intake_jobs`
-- (0072, trío `source_text_enc/_dek/_kek_id`) e `intake_revisions` (0079). Aquí
-- NO, y la diferencia es exactamente la que justifica que esta tabla exista: lo
-- que entra aquí YA PASÓ POR EL ANONIMIZADOR (internal/casebank/anonimizar.go) y
-- por el consentimiento del tenant. Un sobre cifrado protegería un texto del que
-- ya se retiró lo que había que proteger, y a cambio haría el dataset ilegible
-- para lo único que existe: que una persona lo lea y lo cure.
--
-- ⚠️ EL COROLARIO, DICHO AQUÍ PARA QUE NADIE LO DESCUBRA TARDE: si alguien
-- escribe en `source_text` un literal SIN anonimizar, esta tabla es PII EN CLARO
-- en la nube y viola el ADR-0034 de frente. La única puerta legítima de escritura
-- es `casebank.Servicio.Insertar`, que anonimiza antes de llegar aquí. Por eso el
-- CHECK de abajo defiende el consentimiento y NO puede defender la anonimización:
-- Postgres no sabe qué es un nombre propio. Esa mitad la sostiene el código y sus
-- tests, y no hay red debajo.
--
-- 🔴 EL CHECK DE `consented` — LO QUE SÍ PUEDE VIGILAR LA BASE
-- ------------------------------------------------------------
-- El design §6.4 dejó `consented BOOLEAN NOT NULL DEFAULT false` con la nota
-- «sin true la fila no se crea (guard en código)». Se construyen LAS DOS COSAS,
-- y son distintas a propósito:
--
--   * el GUARD EN GO (casebank.ErrSinConsentimiento) rechaza ANTES de tocar la
--     base, con un error tipado que el llamador puede distinguir. Es el que da el
--     mensaje bueno;
--   * este CHECK es la RED DEBAJO DE LA RED: cubre al INSERT a mano, al script de
--     migración de datos y al store que alguien escriba mañana sin pasar por el
--     servicio.
--
-- 🔴 Y NO SE TAPAN EL UNO AL OTRO, que es el defecto clásico de la defensa
-- duplicada: el test del guard es de UNIDAD y no abre conexión (si se borra el
-- guard, el doble del store recibe la llamada y el test se pone rojo SIN que el
-- CHECK pueda salvarlo), y el test del CHECK es de INTEGRACIÓN y ejecuta un
-- INSERT crudo saltándose el servicio (si se borra el `ADD CONSTRAINT`, la base
-- acepta la fila y el test se pone rojo SIN que el guard pueda salvarlo). Cada
-- uno tiene una mutación que solo lo mata a él.
--
-- El DEFAULT es `false` y NO `true`, y esa es la mitad que importa: una fila que
-- se olvide de la columna NACE SIN CONSENTIMIENTO y el CHECK la rechaza. Un
-- `DEFAULT true` haría que todo INSERT descuidado afirmara un consentimiento que
-- nadie dio — el mismo argumento con el que la 0071 se negó a poner `now()` por
-- defecto en `consented_at`.
--
-- ⚠️ CONSECUENCIA DELIBERADA: con este CHECK, la columna solo puede valer `true`,
-- así que HOY no distingue nada. Se conserva la columna igualmente porque el
-- contrato del design la nombra y porque el día que el consentimiento se pueda
-- RETIRAR, el desenlace será retirar el CHECK (y borrar o marcar las filas), no
-- inventar una columna nueva. Una columna de un solo valor con un CHECK explícito
-- dice «esto está prohibido»; sin el CHECK diría «esto no se ha decidido».
--
-- ------------------------------------------------------------
-- 🔴 LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrations/migrate.go): re-aplica TODO el
-- directorio en cuanto cambia el hash del conjunto. Se dice qué se hizo con cada
-- regla en vez de dejar el silencio:
--
-- (1) SIN DEFAULT — NO SE APLICA a `consented`, y es la excepción razonada de
--     arriba: su `DEFAULT false` es justamente lo que hace que el descuido falle.
--     Los otros dos defaults son el `BIGSERIAL` de la PK y el `now()` de
--     `created_at`, que son el molde de toda tabla de la casa.
--
-- (2) BACKFILL CON GUARD `WHERE ... IS NULL` — NO SE APLICA: tabla nueva, cero
--     filas preexistentes, cero columnas en claro que vaciar. No hay backfill.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ ... IF is_nullable = 'YES'` — NO SE
--     APLICA: las columnas NACEN NOT NULL en el CREATE TABLE porque la tabla no
--     tiene filas. Ese `SET NOT NULL` guardado existe para columnas añadidas a
--     una tabla con datos.
--
-- (4) CHECK CON NOMBRE EXPLÍCITO, FUERA DEL `CREATE TABLE` Y RECREADO EN CADA
--     REPLAY — SÍ SE APLICA, y aplica ENTERA. Es la regla que la 0071 tuvo que
--     corregir a posteriori: un CHECK inline dentro de un `CREATE TABLE IF NOT
--     EXISTS` NO se recrea NUNCA MÁS, porque del segundo arranque en adelante el
--     CREATE entero es un NO-OP exacto. Aquí va desde el primer día con su
--     `DROP CONSTRAINT IF EXISTS` delante, de modo que cada replay lo RESTAURA
--     —y el día que el vocabulario del consentimiento cambie, editar la línea de
--     abajo cambiará de verdad la constraint de una base viva—.
--
-- EL REPLAY, AQUÍ BENIGNO
-- ------------------------------------------------------------
-- `CREATE TABLE IF NOT EXISTS` es, del segundo arranque en adelante, un NO-OP
-- EXACTO: no toca valores ni columnas. Los casos ya sembrados SOBREVIVEN a cada
-- reinicio, y su `id` (BIGSERIAL) no se recicla. Los `ADD COLUMN IF NOT EXISTS`
-- de abajo existen por si una base se quedó con una versión anterior de este
-- mismo fichero, igual que la 0047:75-77 y la 0071.
--
-- ⚠️ NO SIEMBRA NADA. A diferencia de la 0074 (que sí trae `INSERT … ON CONFLICT
-- DO NOTHING` de un catálogo de PLATAFORMA), aquí la semilla NO puede vivir en el
-- SQL: toda fila lleva `tenant_id`, y un INSERT en la migración metería en TODAS
-- las bases una fila de un tenant inventado y afirmaría un consentimiento que ese
-- tenant no dio. La siembra del caso Ambar es un acto de operador
-- (`go run ./cmd/casebank -tenant … -consentido`), y ese acto ES el registro del
-- consentimiento.
--
-- ⚠️ ORDEN: no toca ninguna tabla que exista ya, así que no depende de ninguna
-- migración anterior. Va al final, en secuencia, porque es donde va todo lo nuevo.
--
-- ADITIVA e IDEMPOTENTE. ⚠️ **NO sube `SchemaVersion`**: el Plan 044 ya gastó su
-- único bump —`0.45.0`, puesto por la Ola 4 (0080/0081)— y INV-8 da UNO por plan.
-- Esta migración entra bajo ese número, que es exactamente el caso que la primera
-- mitad de la regla de version.go contempla: una ola más de un plan que aún no
-- cerró.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE. Las ejecuta el barrido del CLI.
-- ------------------------------------------------------------
--
-- (V1) La tabla existe con sus SEIS columnas, y `expected` es la ÚNICA NULLable:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'intake_case_bank'
--    ORDER BY ordinal_position;
--
--   Salida esperada — SEIS filas:
--
--    column_name | data_type                | is_nullable | column_default
--   -------------+--------------------------+-------------+---------------------------------------------------
--    id          | bigint                   | NO          | nextval('intake_case_bank_id_seq'::regclass)
--    tenant_id   | text                     | NO          |
--    consented   | boolean                  | NO          | false
--    source_text | text                     | NO          |
--    expected    | jsonb                    | YES         |
--    created_at  | timestamp with time zone | NO          | now()
--
--   🔴 El `column_default` de `consented` tiene que decir `false`. Si dice `true`,
--   alguien invirtió el default y toda fila descuidada afirma un consentimiento
--   que nadie dio. ⚠️ Y ojo: `information_schema` enseña el estado FINAL, no con
--   qué nació la columna — que salga `false` prueba que HOY es `false`, no que
--   ninguna migración posterior lo tocó.
--
-- (V2) El CHECK del consentimiento existe, con su nombre:
--
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint
--    WHERE conrelid = 'public.intake_case_bank'::regclass AND contype = 'c';
--
--   Una fila, `conname` = intake_case_bank_consented_check, y la definición tiene
--   que ser CHECK (consented).
--
-- (V3) La base RECHAZA de verdad una fila sin consentimiento (las dos mitades: el
--      rechazo Y la aceptación, porque un rechazo sobre una tabla que no acepta
--      NADA no probaría nada):
--
--   INSERT INTO public.intake_case_bank (tenant_id, consented, source_text)
--   VALUES ('t-verificacion', false, 'texto de prueba');
--   -- esperado: ERROR ... "intake_case_bank_consented_check"
--
--   INSERT INTO public.intake_case_bank (tenant_id, consented, source_text)
--   VALUES ('t-verificacion', true, 'texto de prueba');    -- esperado: INSERT 0 1
--
--   DELETE FROM public.intake_case_bank WHERE tenant_id = 't-verificacion';
--
-- (V4) El barrido de PII sobre lo sembrado — la mitad que el CHECK NO puede
--      vigilar. Ninguna fila puede llevar un JID ni un teléfono en `source_text`:
--
--   SELECT count(*) AS con_jid FROM public.intake_case_bank
--    WHERE source_text ~ '@(s\.whatsapp\.net|g\.us|c\.us|lid)';   -- esperado: 0
--   SELECT count(*) AS con_telefono FROM public.intake_case_bank
--    WHERE source_text ~ '[0-9][0-9 ()+.-]{6,}[0-9]';             -- esperado: 0
--   SELECT count(*) AS sembradas FROM public.intake_case_bank;    -- esperado: > 0
--
--   🔴 LAS TRES, y la tercera no es decorativa: sobre una tabla vacía las dos
--   primeras dan cero y no prueban nada. Es el mismo par que la V3 de la 0071.
--   ⚠️ Esta consulta es un ECO del anonimizador de Go, no su juez: si el
--   anonimizador tiene un agujero, esta SQL lo tiene igual. Lo que caza de verdad
--   es la fila que entró SIN pasar por él.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.intake_case_bank (
    id          BIGSERIAL   PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,                  -- de quien es el caso; TEXT sin FK (convencion de la casa)
    consented   BOOLEAN     NOT NULL DEFAULT false,    -- su CHECK va APARTE, mas abajo
    source_text TEXT        NOT NULL,                  -- 🔴 ANONIMIZADO. Nunca el literal crudo del cliente
    expected    JSONB,                                 -- interpretacion correcta, curada a mano. NULL = aun sin curar
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Por si una base ya tiene la tabla de una versión anterior de este fichero: el
-- replay le añade lo que falte sin tocar las filas existentes (patrón 0047:73-77
-- y 0071). `expected` es la única que puede llegar así, porque es la única
-- NULLable: un `ADD COLUMN NOT NULL` sin default revienta sobre una tabla con
-- filas, así que las otras cinco no se pueden reponer por esta vía y no se finge
-- que sí.
ALTER TABLE public.intake_case_bank ADD COLUMN IF NOT EXISTS expected JSONB;

-- ------------------------------------------------------------
-- EL CHECK DEL CONSENTIMIENTO, FUERA DEL CREATE TABLE Y RECREADO EN CADA REPLAY
-- ------------------------------------------------------------
-- Va aquí y no inline por lo que la 0071 aprendió a posteriori: dentro de un
-- `CREATE TABLE IF NOT EXISTS`, un CHECK se aplica UNA vez y del segundo arranque
-- en adelante queda congelado — si alguien lo dropea a mano, o lo pierde un
-- restore parcial, el replay ya no lo repondría. Con nombre explícito y
-- `DROP … IF EXISTS` delante, cada replay lo RESTAURA.
ALTER TABLE public.intake_case_bank
    DROP CONSTRAINT IF EXISTS intake_case_bank_consented_check;
ALTER TABLE public.intake_case_bank
    ADD CONSTRAINT intake_case_bank_consented_check CHECK (consented);

-- ------------------------------------------------------------
-- El índice del ÚNICO acceso que hoy existe.
-- ------------------------------------------------------------
-- No es decorativo y se dice por qué: `casebank.Postgres.Existe` —el guard de
-- idempotencia de la siembra— pregunta `WHERE tenant_id = $1 AND source_text = $2`,
-- y ese es el único SELECT que esta tabla recibe hoy. Sin `created_at` detrás: la
-- lectura ordenada del dataset (montar un lote de eval) TODAVÍA NO EXISTE, y un
-- índice compuesto para una consulta que nadie escribe se pagaría en cada
-- escritura sin responder a nadie. Cuando esa lectura se construya, se amplía
-- aquí — el replay la aplicará de verdad.
--
-- 🔴 NO ES ÚNICO, a propósito. Un `UNIQUE (tenant_id, source_text)` sobre una
-- columna TEXT sin cota choca contra el límite de tamaño de entrada del btree
-- (~2704 bytes) y haría FALLAR el INSERT de un caso largo — que es justo la clase
-- de caso que más falta hace en un banco de evaluación. La idempotencia de la
-- siembra la resuelve el `WHERE NOT EXISTS` de una sola sentencia, y un duplicado
-- en un dataset es un incordio, no una corrupción.
CREATE INDEX IF NOT EXISTS idx_intake_case_bank_tenant
    ON public.intake_case_bank (tenant_id);

COMMENT ON TABLE public.intake_case_bank IS
    'Banco de casos con consentimiento (Plan 044 · T5.3, design 6.4): dataset de EVALUACION del pipeline de captacion. Solicitudes reales ya ANONIMIZADAS mas la interpretacion correcta curada a mano. 🔴 NO lleva sobre de cifrado, al reves que intake_jobs (0072) e intake_revisions (0079), y es deliberado: lo que entra aqui ya paso por el anonimizador y por el consentimiento del tenant, y cifrarlo lo volveria ilegible para lo unico que existe -- que una persona lo lea y lo cure. El corolario: una fila con texto SIN anonimizar es PII en claro en la nube (ADR-0034). La unica puerta legitima de escritura es casebank.Servicio.Insertar. Ningun camino de produccion LEE esta tabla: no alimenta al pipeline, alimenta a quien lo evalua.';
COMMENT ON COLUMN public.intake_case_bank.id          IS 'Identificador de la fila. BIGSERIAL y no UUID porque este es un dataset interno que se lee por lotes y se cita por numero en un informe, no un objeto de negocio que viaje por una API publica.';
COMMENT ON COLUMN public.intake_case_bank.tenant_id   IS 'Tenant dueno del caso. TEXT sin FK (convencion de la ficha 3). El dataset NO es global y no puede serlo: el texto es de un cliente de ESE negocio y el consentimiento lo dio ESE negocio, asi que un caso no se puede reutilizar para evaluar a otro tenant sin volver a pedirlo.';
COMMENT ON COLUMN public.intake_case_bank.consented   IS 'Consentimiento explicito del tenant a que este texto se conserve como material de evaluacion. DEFAULT false y CHECK (consented): una fila que se olvide de la columna NACE SIN CONSENTIMIENTO y la base la rechaza. Un DEFAULT true haria que todo INSERT descuidado afirmara un consentimiento que nadie dio. Hoy la columna solo puede valer true y por tanto no distingue nada: se conserva porque el dia que el consentimiento se pueda RETIRAR, el desenlace sera retirar el CHECK, no inventar otra columna. El error bueno lo da Go (casebank.ErrSinConsentimiento) ANTES de llegar aqui; esto es la red debajo de la red.';
COMMENT ON COLUMN public.intake_case_bank.source_text IS 'Literal del cliente YA ANONIMIZADO: sin nombres propios, sin telefonos y sin JID (internal/casebank/anonimizar.go). 🔴 Postgres NO puede vigilar esto -- no sabe que es un nombre propio -- asi que esta mitad la sostienen el codigo y sus tests, y no hay red debajo. El anonimizador es una bateria de expresiones regulares, NO un NER: no reconoce nombres que no se le pasen en la lista, ni direcciones, ni correos, ni documentos de identidad. Ver su docstring, que lista lo que NO cubre.';
COMMENT ON COLUMN public.intake_case_bank.expected    IS 'Interpretacion correcta del caso, curada A MANO: contra esto se compara lo que produce el pipeline. NULLable a proposito -- un caso puede entrar al banco antes de que nadie lo haya etiquetado, y esa fila ya es util (el material esta, falta la etiqueta). Puede llevar ademas la clave _procedencia, que es donde un fixture RECONSTRUIDO declara que no es el texto real del cliente: sin ella, un lector de dentro de seis meses no tiene forma de distinguir el material real del redactado.';
COMMENT ON COLUMN public.intake_case_bank.created_at  IS 'Momento del alta del caso. Usa el DEFAULT now(). No hay updated_at: un caso curado se corrige reemplazando expected, y si hiciera falta saber cuando, esa columna se anade el dia que alguien la necesite -- hoy nadie la consulta.';
