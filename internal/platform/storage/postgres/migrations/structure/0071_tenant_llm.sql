-- ============================================================
-- 0071: tenant_llm — LA CREDENCIAL LLM DEL TENANT, EN SOBRE DE TRES PIEZAS
-- (Plan 044 · Ola 0 · T0.3; design §6.1 y §8. ADR-0030 vía «api», ADR-0011/0012
--  KeyProvider, ADR-0034 «nada sensible en claro»).
--
-- QUÉ GUARDA, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- La vía API del pipeline de captación (ADR-0030, D-044.1) la consume el CLOUD
-- con la credencial DEL TENANT: es el tenant quien paga su cuenta de Anthropic,
-- así que es el tenant quien la aporta y quien la revoca. Esta tabla es donde
-- vive esa configuración: qué proveedor, qué modelo, con qué clave y desde
-- cuándo consintió que su texto salga hacia un tercero.
--
--   * provider       — proveedor elegido, vocabulario CERRADO (ver el CHECK).
--   * model          — identificador de modelo del proveedor, texto libre suyo.
--   * api_key_enc    — envelope AES-256-GCM de la API key (nonce fresco).
--   * api_key_dek    — DEK por-fila (32B), envuelta por la KEK maestra.
--   * api_key_kek_id — key_id de la KEK que envolvió la DEK (discriminador del Rekey).
--   * consented_at   — momento del consentimiento explícito. Sin él NO HAY FILA.
--
-- ADR-0009: aquí NO vive dato de negocio, vive una CREDENCIAL DE UN TERCERO.
-- Es el mismo caso que el secreto HMAC del puente CRM (0047) y se trata igual.
-- La KEK NO vive en esta BD (env/secret store, design.md §10.A) y la DEK del Edge
-- —la del store de whatsmeow, ADR-0007— no se toca ni se menciona: son cosas
-- distintas que comparten nombre.
--
-- 🔴 EL SOBRE ES DE TRES PIEZAS Y ES `NOT NULL`, AL REVÉS QUE EL DE LA 0047
-- ------------------------------------------------------------
-- El trío de `tenant_integrations` (0047:65-67) es NULLable porque allí la fila
-- puede existir SIN secreto: un tenant en local/local tiene configuración de
-- puente y ninguna credencial. Aquí eso no puede pasar, y copiar aquel molde
-- sería el error:
--
--   una fila de tenant_llm SIN clave no significa nada. No hay «configuración
--   parcial» de la vía API: sin clave no se puede llamar al proveedor, y una
--   fila así solo serviría para que el pipeline la leyera, la encontrara vacía y
--   fallara aguas abajo. El estado «este tenant no tiene vía API» ya tiene
--   representante, y es LA AUSENCIA DE FILA.
--
-- Consecuencia deliberada: la invariante «las tres o ninguna» que en
-- contacts / fleet_sessions / tenant_integrations vive en el CÓDIGO (sin CHECK,
-- para no bloquear la escritura parcial de una rotación) aquí la vigila POSTGRES
-- con tres NOT NULL. Se puede porque la rotación NUNCA escribe una sola de las
-- tres: `rekeyBatch` actualiza (dekCol, kekCol) EN LA MISMA sentencia y jamás
-- toca `_enc`, así que ningún paso legítimo pasa por un estado a medias.
--
-- 🔴 `consented_at` ES `NOT NULL` Y NO TIENE DEFAULT, Y ESO ES EL CRITERIO
-- ------------------------------------------------------------
-- El criterio de T0.3 dice «PUT sin consentimiento ⇒ 400». Ese 400 lo da la API
-- (internal/publicapi/tenantllm.go), no esta tabla: la validación llega ANTES,
-- porque dejar que el NOT NULL sea quien rechace convertiría un error del
-- cliente en un 500 —el mismo argumento que la 0047 escribió para su CHECK de
-- adaptadores (integrations.go:26-27)—. Lo que hace la columna es impedir que
-- exista NUNCA una fila sin consentimiento, venga de donde venga la escritura:
-- es la red debajo de la red. Un DEFAULT now() aquí la anularía —toda fila
-- nacería «consentida» sin que nadie consintiera—, así que no lo hay.
--
-- 🔴 ESTA MIGRACIÓN NO CIFRA NADA, Y AQUÍ NO HAY NADA QUE CIFRAR
-- ------------------------------------------------------------
-- A diferencia de la 0068 y la 0069, esta tabla NACE VACÍA: no hay columna en
-- claro de la que partir, no hay backfill que escribir y no hay runbook que
-- acompañe. Toda fila nace ya cifrada, por el único camino que la escribe
-- (tenantllm.Postgres.Upsert, que llama a crypto.FieldCipher.Encrypt).
-- Postgres sigue sin poder cifrar —no tiene la KEK, y por no tenerla es por lo
-- que este sobre protege algo (0006:20-21)—, solo que aquí eso no obliga a
-- ningún paso extra.
--
-- ⚠️ EL DESIGN §6.1 DIBUJABA UNA SOLA COLUMNA `api_key_enc BYTEA`. Se divide en
-- tres por lo mismo que la 0047 dividió `secret_ciphertext`:
-- crypto.FieldCipher.Encrypt devuelve (valueEnc, valueDEK, keyID) y las tres
-- piezas hay que persistirlas o la fila es indescifrable. No es una desviación
-- del diseño: es el diseño escrito contra la firma real de la casa.
--
-- ------------------------------------------------------------
-- 🔴 LAS TRES REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrations/migrate.go): re-aplica TODO el
-- directorio en cuanto cambia el hash del conjunto. Se dice qué se hizo con cada
-- regla en vez de dejar el silencio:
--
-- (1) SIN DEFAULT — SE APLICA a las seis columnas que importan. `provider` y
--     `model` no tienen default porque no hay un proveedor «normal» que elegir
--     por el tenant; el sobre no lo tiene porque un texto cifrado inventado no
--     lo abre ninguna KEK y un `api_key_kek_id` por defecto metería en el
--     barrido del Rekey a filas cuya DEK no existe (el error que la 0069 evitó
--     no copiando el `DEFAULT '1'` de la 0007); y `consented_at` no lo tiene por
--     lo dicho arriba. Los ÚNICOS defaults son los dos `now()` de created_at /
--     updated_at, que es el molde de toda tabla de la casa.
--
-- (2) BACKFILL CON GUARD `WHERE ... IS NULL` — NO SE APLICA, y no porque se haya
--     mudado a Go como en la 0068/0069, sino porque NO HAY BACKFILL EN ABSOLUTO:
--     tabla nueva, cero filas preexistentes, cero columnas en claro que vaciar.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ ... IF is_nullable = 'YES'` — NO SE
--     APLICA, y aquí la razón es la contraria a la de la 0068: no hace falta
--     promover nada a NOT NULL después, porque las columnas NACEN NOT NULL en el
--     CREATE TABLE. Ese `SET NOT NULL` guardado existe para columnas que se
--     añaden a una tabla con filas; esta tabla no tiene ninguna.
--
-- (4) La cuarta regla de la 0063 —CHECK con nombre explícito, RECREADO en cada
--     replay— SÍ APLICA, y aplica ENTERA: hay un dominio cerrado (`provider`),
--     su CHECK lleva nombre propio (`tenant_llm_provider_check`) para poder
--     nombrarlo en un error y retirarlo sin adivinar, Y VIVE FUERA DEL
--     `CREATE TABLE`, con `DROP CONSTRAINT IF EXISTS` + `ADD` (bloque de abajo).
--     🔧 Hasta la revisión del 2026-08-22 iba inline dentro del CREATE y esta
--     nota decía que la regla se cumplía: no se cumplía. Un CHECK inline en un
--     `CREATE TABLE IF NOT EXISTS` NO se recrea nunca más, porque del segundo
--     arranque en adelante el CREATE entero es NO-OP. El porqué completo, con
--     el fallo concreto que evita, está sobre el bloque.
--
-- EL REPLAY, AQUÍ BENIGNO
-- ------------------------------------------------------------
-- `CREATE TABLE IF NOT EXISTS` es, del segundo arranque en adelante, un NO-OP
-- EXACTO: no toca valores ni columnas. Las credenciales ya escritas SOBREVIVEN a
-- cada reinicio. Los `ADD COLUMN IF NOT EXISTS` de abajo existen por si una base
-- se quedó con una versión anterior de este mismo fichero, igual que la 0047:75-77.
-- Y por ESO MISMO el CHECK no puede ir dentro del CREATE: lo que hace benigno al
-- replay de la tabla es justo lo que dejaría al CHECK congelado para siempre.
--
-- ⚠️ ORDEN: no toca ninguna tabla que exista ya, así que no depende de ninguna
-- migración anterior. Va al final, en secuencia, porque es donde va todo lo
-- nuevo — el hueco 0020/0021 NO se rellena.
--
-- ADITIVA e IDEMPOTENTE. ⚠️ **NO sube `SchemaVersion`**: el Plan 044 hace UN SOLO
-- bump al cierre, en su T6.2 (tasks.md, cabecera). Si esta migración se despliega
-- suelta antes de ese bump, ver la nota abierta del handoff de T0.3.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE. Las ejecuta el barrido del CLI.
-- ------------------------------------------------------------
--
-- (V1) La tabla existe con sus NUEVE columnas —SIETE de negocio más los dos
--      timestamps del molde de la casa— y las nueve son NOT NULL:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'tenant_llm'
--    ORDER BY ordinal_position;
--
--   Salida esperada — NUEVE filas, `is_nullable` = NO en LAS NUEVE (ninguna
--   excepción: la tabla no admite fila a medias), y `column_default` vacío en
--   TODAS salvo los dos now():
--
--    column_name    | data_type                   | is_nullable | column_default
--   ----------------+-----------------------------+-------------+----------------
--    tenant_id      | text                        | NO          |
--    provider       | text                        | NO          |
--    model          | text                        | NO          |
--    api_key_enc    | bytea                       | NO          |
--    api_key_dek    | bytea                       | NO          |
--    api_key_kek_id | text                        | NO          |
--    consented_at   | timestamp with time zone    | NO          |
--    created_at     | timestamp with time zone    | NO          | now()
--    updated_at     | timestamp with time zone    | NO          | now()
--
--   🔧 CORREGIDO el 2026-08-22: este bloque decía «ocho columnas» y pedía el
--   NOT NULL «en las seis primeras y en created_at/updated_at» — mal contado, y
--   con el mal conteo se caía del cheque justo `consented_at`, LA COLUMNA POR LA
--   QUE EXISTE MEDIA MIGRACIÓN. Los DOS que más importan, por si alguien recorta
--   esta verificación:
--
--     * `consented_at` NOT NULL → si sale YES, puede existir fila sin
--       consentimiento y la red debajo de la red no está puesta (ver arriba).
--     * el trío `api_key_enc` / `api_key_dek` / `api_key_kek_id` NOT NULL → si
--       sale YES en cualquiera de las tres, alguien copió el molde NULLable de
--       la 0047 sin leer por qué aquí NO aplica, y la tabla admite ya una fila
--       indescifrable.
--
-- (V2) El CHECK del vocabulario existe, con su nombre:
--
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint
--    WHERE conrelid = 'public.tenant_llm'::regclass AND contype = 'c';
--
--   Una fila, `conname` = tenant_llm_provider_check, y la definición tiene que
--   nombrar 'anthropic' y 'gemini' y NINGÚN otro valor.
--
-- (V3) Criterio de T0.3 «PUT válido ⇒ el BYTEA está cifrado, verificable por SQL».
--      Tras un PUT con la clave de prueba, la clave NO aparece en claro:
--
--   SELECT count(*) AS en_claro FROM public.tenant_llm
--    WHERE encode(api_key_enc, 'escape') LIKE '%sk-ant-%';   -- esperado: 0
--   SELECT count(*) AS cifradas FROM public.tenant_llm
--    WHERE api_key_enc IS NOT NULL;                          -- esperado: nº de tenants configurados
--
--   🔴 LAS DOS MITADES, y no solo la primera: una tabla vacía daría cero en la
--   primera y no probaría nada. Es el mismo par que la V3 de la 0068/0069.
--   El LIKE va contra el prefijo público del formato de clave de Anthropic
--   (`sk-ant-`), que es lo que un barrido de PII busca de verdad; el envelope
--   antepone un nonce aleatorio, así que un `encode(...,'escape')` del blob no
--   puede contenerlo salvo que alguien haya guardado el texto plano.
--
-- (V4) El barrido general: la clave no está en claro en NINGUNA columna de texto
--      de la fila (por si alguien la coló en `model`, que es texto libre):
--
--   SELECT count(*) AS filtrada FROM public.tenant_llm
--    WHERE model LIKE '%sk-ant-%' OR provider LIKE '%sk-ant-%';  -- esperado: 0
--
-- (V5) Sin fila sin consentimiento, que es lo que la columna existe para impedir:
--
--   SELECT count(*) AS sin_consentir FROM public.tenant_llm
--    WHERE consented_at IS NULL;                             -- esperado: 0 (lo fuerza el NOT NULL)
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_llm (
    tenant_id      TEXT        PRIMARY KEY,   -- una configuración LLM por tenant en v1
    provider       TEXT        NOT NULL,      -- vocabulario CERRADO: su CHECK va APARTE, más abajo
    model          TEXT        NOT NULL,      -- identificador del proveedor, texto libre SUYO
    api_key_enc    BYTEA       NOT NULL,      -- envelope AES-256-GCM de la API key
    api_key_dek    BYTEA       NOT NULL,      -- DEK por-fila, envuelta por la KEK
    api_key_kek_id TEXT        NOT NULL,      -- key_id de la KEK que envolvió api_key_dek
    consented_at   TIMESTAMPTZ NOT NULL,      -- consentimiento explícito: sin esto no hay fila
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Por si una base ya tiene la tabla de una versión anterior de este fichero: el
-- replay le añade lo que falte sin tocar las filas existentes (patrón 0047:73-77).
-- 🔴 SE AÑADEN NULLables A PROPÓSITO Y NO SE PROMUEVEN. Un ADD COLUMN NOT NULL
-- sin default revienta sobre una tabla con filas, y un SET NOT NULL guardado
-- reventaría igual si alguna fila vieja las tuviera vacías. En la base virgen
-- —el caso real— estas líneas son NO-OP exacto y las columnas nacen NOT NULL del
-- CREATE TABLE de arriba; sobre una base a medias, el arranque no muere y el
-- estado a medias se ve con la (V1).
ALTER TABLE public.tenant_llm ADD COLUMN IF NOT EXISTS api_key_enc    BYTEA;
ALTER TABLE public.tenant_llm ADD COLUMN IF NOT EXISTS api_key_dek    BYTEA;
ALTER TABLE public.tenant_llm ADD COLUMN IF NOT EXISTS api_key_kek_id TEXT;

-- ------------------------------------------------------------
-- EL CHECK DEL VOCABULARIO, FUERA DEL CREATE TABLE Y RECREADO EN CADA REPLAY
-- ------------------------------------------------------------
-- 🔧 CORRECCIÓN (revisión adversarial, 2026-08-22). Este CHECK estaba INLINE en el
-- `CREATE TABLE IF NOT EXISTS` de arriba, y ahí NO es replay-safe: del segundo
-- arranque en adelante el CREATE es un NO-OP exacto y se salta el CHECK con él.
-- Consecuencia concreta, no teórica: el día que el vocabulario crezca —y este
-- vocabulario es de PROVEEDORES, que se añaden solos con el tiempo; `local` no
-- cuenta, que es una VÍA y nunca entrará aquí—, editar la lista de valores de
-- aquí NO cambiaría la constraint de una base viva; el PUT del proveedor nuevo
-- daría 500 por violación de CHECK y nadie sabría por qué. Igual si alguien lo
-- dropea a mano o lo pierde un restore parcial: el replay ya no lo repondría.
--
-- La forma correcta es la de la 0063:99-104 (que a su vez copia la 0025): nombre
-- explícito + `DROP CONSTRAINT IF EXISTS` antes del `ADD`, de modo que cada
-- replay lo RESTAURA. Se conserva el nombre `tenant_llm_provider_check` —el
-- mismo de la versión inline— justamente porque es el que ya tendrían las bases
-- que alcanzaron a aplicarla: el DROP las cubre sin necesitar el segundo DROP que
-- la 0063 sí tuvo que escribir (allí el inline era ANÓNIMO y Postgres le puso
-- otro nombre). Un solo DROP basta aquí, y no es un descuido.
--
-- ⚠️ NO se toca el orden: va DESPUÉS de los ADD COLUMN, porque un CHECK sobre una
-- columna que aún no existe no se puede añadir. Sobre `provider` eso no aplica
-- (nace en el CREATE TABLE), pero mantener el bloque abajo lo deja donde crecerá
-- si algún día hay otro dominio cerrado.
ALTER TABLE public.tenant_llm
    DROP CONSTRAINT IF EXISTS tenant_llm_provider_check;
ALTER TABLE public.tenant_llm
    ADD CONSTRAINT tenant_llm_provider_check CHECK (provider IN ('anthropic','gemini'));
-- v1; crecer el dominio = editar ESTA línea, y el replay la aplica de verdad.

-- ------------------------------------------------------------
-- El índice del CONTEO de la rotación de KEK.
-- ------------------------------------------------------------
-- Mismo papel real que el de la 0069, dicho igual de bien: NO lo usa el barrido
-- —el SELECT y el UPDATE de rekeyBatch filtran por `api_key_kek_id <> $1`, y la
-- desigualdad no es predicado indexable en un btree—, lo usa `pendingSQL`, el
-- `GROUP BY api_key_kek_id` de PendingByKeyID: la consulta que se ejecuta CUANDO
-- YA NO QUEDA NADA y que es justo la que AUTORIZA retirar una KEK del keyring
-- (§10.F). Paga en el momento que importa. Se conserva el sufijo `_kek` por
-- consistencia con las otras cinco entradas del censo.
--
-- 🔴 NO ES PARCIAL, y ahí se separa de la 0068/0069: `api_key_kek_id` es NOT
-- NULL, así que un `WHERE api_key_kek_id IS NOT NULL` no excluiría ni una fila y
-- sería un predicado decorativo. La forma que le toca es la de idx_contacts_kek
-- (0007:34), que tampoco es parcial y por la misma razón. Sin `tenant_id`
-- delante porque la consulta del Rekey NO filtra por tenant: barre global por
-- key_id (rekey.go, pendingSQL/selectSQL).
CREATE INDEX IF NOT EXISTS idx_tenant_llm_kek
    ON public.tenant_llm (api_key_kek_id);

COMMENT ON TABLE public.tenant_llm IS
    'Credencial y configuracion de la via LLM API por tenant (Plan 044 · T0.3, ADR-0030). SIN FILA = el tenant no tiene via API configurada, que es un estado legitimo y el default. Aqui NO hay dato de negocio: hay una CREDENCIAL DE UN TERCERO (la cuenta del proveedor la paga el tenant), guardada CIFRADA en tres columnas por envelope encryption, patron de tenant_integrations.secret_* (0047). CERO PII. La clave NUNCA sale por la API: el GET solo dice si hay (key_set).';
COMMENT ON COLUMN public.tenant_llm.tenant_id      IS 'Tenant dueno de la configuracion (TEXT sin FK, convencion de la ficha 3; PK porque en v1 hay una configuracion LLM por tenant). Sale del TOKEN, jamas del cuerpo (INV-7 / INV-8).';
COMMENT ON COLUMN public.tenant_llm.provider       IS 'Proveedor de la via API. Vocabulario CERRADO y de wApp, no del cliente, por eso lo acota un CHECK con nombre: anthropic (unica implementacion cableada del Plan 044, T0.2) | gemini (stub que compila y falla nombrado). 🔴 local NO ESTA AQUI y no es un olvido: local es una VIA (ADR-0030), no un proveedor, y su implementacion es futura (D-044.4) -- la API lo rechaza antes de llegar a este CHECK con 422 llm_provider_unavailable, que es "te entiendo y no puedo", distinto del 400 de un valor que no existe.';
COMMENT ON COLUMN public.tenant_llm.model          IS 'Identificador de modelo del proveedor (p.ej. claude-sonnet-4-5). Texto libre SUYO: wApp no mantiene un catalogo de modelos ajenos ni lo valida contra una lista, porque esa lista caduca cada pocas semanas y una lista caducada rechaza modelos validos. Solo se acota su longitud en la API.';
COMMENT ON COLUMN public.tenant_llm.api_key_enc    IS 'Envelope AES-256-GCM (nonce fresco por escritura) de la API key del proveedor. NOT NULL: una fila sin clave no significa nada -- el estado "sin via API" es la AUSENCIA DE FILA, no una fila vacia (contraste deliberado con tenant_integrations, cuyo trio es NULLable porque alli la fila si existe sin secreto). NUNCA se devuelve por la API ni se loguea. Se descifra en el borde del pipeline, para llamar al proveedor.';
COMMENT ON COLUMN public.tenant_llm.api_key_dek    IS 'DEK por-fila (32B) que cifra api_key_enc, envuelta por la KEK maestra (design.md seccion 10.B). La KEK NO vive en esta BD. NO tiene NADA que ver con la DEK del Edge (el store de whatsmeow, ADR-0007), que la nube jamas ve.';
COMMENT ON COLUMN public.tenant_llm.api_key_kek_id IS 'key_id de la KEK que envolvio api_key_dek. Discriminador de la rotacion: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey, censo rekeyTargets). Sin esta tabla en el censo la rotacion diria completa con estas credenciales aun bajo la KEK vieja, y retirar esa KEK dejaria a los tenants sin via API y sin forma de recuperarla salvo re-tecleando la clave. NOT NULL: toda fila tiene sobre.';
COMMENT ON COLUMN public.tenant_llm.consented_at   IS 'Momento del consentimiento explicito del tenant a que el texto de sus conversaciones salga hacia un proveedor externo (ADR-0030). NOT NULL y SIN DEFAULT: un DEFAULT now() haria que toda fila naciera "consentida" sin que nadie consintiera, que es exactamente lo que esta columna existe para impedir. El 400 de "PUT sin consentimiento" lo da la API antes de llegar aqui (dejar que el NOT NULL rechace convertiria un error del cliente en un 500); esta columna es la red debajo de la red. Se REFRESCA en cada PUT: el cuerpo re-afirma el consentimiento cada vez, asi que la columna dice cuando se afirmo por ultima vez, no cuando se afirmo por primera.';
COMMENT ON COLUMN public.tenant_llm.created_at     IS 'Momento del alta de la configuracion. Usa el DEFAULT now(). No se pisa en el upsert.';
COMMENT ON COLUMN public.tenant_llm.updated_at     IS 'Momento del ultimo cambio. Usa el DEFAULT now() en el alta y se pone a now() en cada upsert.';
