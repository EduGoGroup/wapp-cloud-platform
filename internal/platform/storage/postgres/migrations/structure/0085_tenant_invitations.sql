-- ============================================================
-- 0085: tenant_invitations — LA INVITACIÓN DE UN SOLO USO PARA INCORPORAR A
-- ALGUIEN A UNA EMPRESA (Plan 047 · Ola A · T-A1; D-047.11, aprobada el
-- 2026-08-28. ADR-0034 minimización de datos, ADR-0009 zero-knowledge.)
--
-- POR QUÉ EXISTE
-- ------------------------------------------------------------
-- La Ola 1 dejó una pregunta abierta que su T1.6 solo apaña: ¿cómo llega a la
-- empresa alguien a quien la dueña NO PUEDE BUSCAR? Hoy la única vía es que la
-- persona le DICTE su `user_id`. La dueña no tiene bandeja y no ve a nadie: para
-- ella, alguien que se registra por su cuenta es invisible.
--
-- El mecanismo que resuelve eso, pieza a pieza:
--   * la dueña (scope `members.write`) EMITE una invitación para SU empresa —el
--     tenant sale de su token, INV-04, nunca del cuerpo—;
--   * se la reparte a la persona POR WHATSAPP, que es el producto, no un canal
--     prestado. No hay envío automático, ni SMTP, ni mailer, ni Plan 007 de
--     identity: lo que viaja es un código OPACO;
--   * la persona se registra ella misma por el signup público de identity
--     (elige su propia clave) y CANJEA con su Context Token SIN TENANT
--     (`resolveTenant` con cero membresías devuelve token sin tenant, no un 401
--     — exchange.go:235-236, D-056.12).
--
-- 🔴 CERO PII, Y AQUÍ ES UNA AFIRMACIÓN FUERTE: esta tabla NO tiene columna de
-- correo, ni de nombre, ni de teléfono, y NO la va a tener. Esa es exactamente la
-- diferencia con `access_requests` (0060), que sí guarda el `email` porque su
-- destinatario es el OPERADOR DE PLATAFORMA y su flujo es otro. Aquí la dueña NO
-- TECLEA NINGÚN CORREO EN NINGÚN MOMENTO (D-047.11): el reparto lo hace ella por
-- su canal, así que la nube nunca necesita saber a quién se la mandó. Todo lo que
-- vive aquí son identificadores opacos (UUID), marcas de tiempo y un digest.
--
-- 🔴 EL TOKEN EN CLARO NO SE GUARDA JAMÁS: solo su SHA-256 en `token_hash`, y el
-- canje busca POR EL HASH (de ahí su índice ÚNICO). Quien emite lo ve UNA sola
-- vez, en la respuesta de `POST /api/v1/invitations` (T-A2); a partir de ahí ni
-- la base ni el listado pueden reconstruirlo. Es la diferencia deliberada con su
-- precedente canónico `enrollment_codes` (0002), del que esta tabla copia todo lo
-- demás —código de un solo uso, `expires_at NOT NULL`, marca de consumo que pasa
-- de NULL a now() de forma atómica—: aquél guarda el código EN CLARO porque el
-- Edge lo presenta junto a su CSR en una red que ya es mTLS; éste viaja por
-- WhatsApp y por el portapapeles de dos personas.
--
-- EL TTL: DECIDIDO, Y PARAMETRIZABLE POR LLAMADA
-- ------------------------------------------------------------
-- `expires_at` es NOT NULL porque una invitación SIN caducidad no es una
-- invitación, es una puerta abierta. Y CUÁNTO dura está DECIDIDO —lo cerró Jhoan
-- el 2026-08-28, antes de escribir esta migración—: lo elige QUIEN EMITE, en el
-- cuerpo de `POST /api/v1/invitations` (`{"ttl": <segundos>}`), con DEFAULT de
-- 24 h (86400) y CLAMP a [60 s, 30 días].
--
-- El número no se inventó aquí: es el precedente LITERAL de los códigos de
-- enrolamiento, que resuelven exactamente el mismo problema —un código de un solo
-- uso que alguien reparte a mano y que tiene que caducar—. Ver
-- `internal/platformadmin/handlers.go:249-260`: `ttl := 86400 // 24 horas por
-- defecto`, sobre `IssueEnrollmentCodeRequest{ TTLSeconds int json:"ttl" }`, y
-- después `if ttl < 60 { ttl = 60 } else if ttl > 30*86400 { ttl = 30*86400 }`.
-- Copiar esa forma —incluido el nombre `ttl` del campo— es lo que hace que las
-- dos emisiones de código de la casa se expliquen con la misma frase.
--
-- 🔴 AUN ASÍ, ESTA MIGRACIÓN NO PONE DEFAULT SOBRE `expires_at`, Y AHORA POR UNA
-- RAZÓN MÁS FUERTE QUE ANTES. No es que falte el número: es que el número es
-- PARAMETRIZABLE POR LLAMADA, así que el esquema no es su sitio. Un
-- `DEFAULT now() + interval '24 hours'` aquí (a) congelaría en el DDL un valor
-- que el emisor puede sobreescribir en cada petición, (b) haría creer al que lea
-- la tabla que el CLAMP también vive aquí —y no vive: [60 s, 30 días] es una
-- guarda de Go, y Postgres no la aplicaría a un INSERT a mano—, y (c) dejaría dos
-- fuentes para el mismo número, que es como se desincronizan. El default y el
-- clamp viven en UN solo sitio, el emisor (T-A2); la base solo exige que haya
-- caducidad.
--
-- ------------------------------------------------------------
-- LA MÁQUINA DE ESTADOS, QUE ES DE TRES SALIDAS Y NO DE DOS
-- ------------------------------------------------------------
--   pendiente  -> redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > now()
--   canjeada   -> redeemed_at IS NOT NULL   (terminal; T-A3)
--   revocada   -> revoked_at  IS NOT NULL   (terminal; T-A8, la casilla nueva)
--   caducada   -> expires_at <= now()       (terminal por el paso del tiempo, sin
--                                            escritura: nadie tiene que barrerla)
--
-- ⚠️ `revoked_at` NO estaba en el enunciado original de T-A1: la añade T-A8
-- (aprobada el 2026-08-28) y entra aquí, en la misma migración, en vez de en una
-- 0086 propia. Por qué: la tabla NO SE HA PUBLICADO TODAVÍA —esta migración nace
-- con este fichero—, así que no hay ninguna base en el mundo con la versión sin
-- la columna, y partirla en dos ficheros dejaría el rastro de una evolución que
-- no ocurrió. El `ADD COLUMN IF NOT EXISTS` de abajo la repone igualmente si
-- alguien se cruza con una base intermedia durante el desarrollo de la ola.
--
-- 🔴 LA EXCLUSIVIDAD DE LOS DOS TERMINALES NO LA VIGILA LA BASE, Y ES DELIBERADO.
-- Que una fila no pueda estar canjeada Y revocada a la vez lo sostiene el UPDATE
-- ATÓMICO de T-A3/T-A8 (`... WHERE redeemed_at IS NULL AND revoked_at IS NULL`),
-- que es además lo que da el «un solo uso» frente a dos canjes concurrentes. Un
-- CHECK aquí no añadiría atomicidad —dos transacciones simultáneas pasarían las
-- dos el CHECK y una perdería igual— y sí ataría a un futuro que puede querer
-- registrar la revocación de algo ya canjeado. Lo que la base SÍ vigila está
-- abajo: la COHERENCIA del par (`redeemed_by`, `redeemed_at`), que no depende de
-- ninguna carrera.
--
-- ------------------------------------------------------------
-- LO QUE ESTA MIGRACIÓN NO HACE
-- ------------------------------------------------------------
-- * NO siembra grants. El scope que gobierna la emisión es `members.write` y YA
--   EXISTE: lo estrenó la 0084 (Plan 047 · Ola 1.0), tenant_admin lo alcanza por
--   su '*' (0015) y platform_admin lo tiene DENEGADO por la cerca INV-10. Una
--   invitación es una membresía en diferido, así que no estrena vocabulario.
-- * NO escribe en `tenant_members`. El canje DEBE pasar por `GrantTenantAccess`
--   (restricción transversal de la Ola A): esa tabla tiene UN SOLO escritor en
--   todo el código, vigilado por un candado sobre el AST
--   (internal/iam/infra/postgres/membresia_unica_ast_test.go).
-- * NO toca `access_requests`. La solicitud huérfana que el invitado deja al
--   registrarse la cierra T-A4, en Go y dentro del canje.
--
-- ------------------------------------------------------------
-- 🔴 LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrations/migrate.go): re-aplica TODO el
-- directorio en cuanto cambia el hash del conjunto, y lo hace AL ARRANCAR
-- `bin/server`. Se dice qué se hizo con cada regla en vez de dejar el silencio:
--
-- (1) SIN DEFAULT — SÍ SE APLICA, y con doble motivo. `expires_at` no lo lleva
--     por lo dicho arriba (el TTL está decidido, pero es parametrizable por
--     llamada: su default y su clamp viven en el emisor, no en el DDL), y
--     tampoco lo lleva ninguna de las cuatro
--     columnas de estado (`role_id`, `redeemed_by`, `redeemed_at`, `revoked_at`)
--     lo lleva tampoco: un default sobre cualquiera de ellas poblaría de golpe
--     TODAS las filas existentes en el replay. Los dos únicos defaults son el
--     `gen_random_uuid()` de la PK y el `now()` de `created_at`, que son el molde
--     de toda tabla de la casa (0002, 0015).
--
-- (2) BACKFILL CON GUARD `WHERE ... IS NULL` — NO SE APLICA: tabla nueva, cero
--     filas preexistentes, ninguna columna que rellenar desde otra. No hay
--     backfill, y por tanto no hay UPDATE que el replay pueda repetir.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ ... IF is_nullable = 'YES'` — NO SE
--     APLICA: las cinco columnas NOT NULL NACEN así en el CREATE TABLE, porque la
--     tabla no tiene filas. Ese `SET NOT NULL` guardado existe para columnas
--     añadidas a una tabla CON datos.
--
-- (4) CHECK CON NOMBRE EXPLÍCITO, FUERA DEL `CREATE TABLE` Y RECREADO EN CADA
--     REPLAY — SÍ SE APLICA, a los dos CHECK. Es la regla que la 0071 tuvo que
--     corregir a posteriori y que la 0082 ya estrenó desde el primer día: un
--     CHECK inline dentro de un `CREATE TABLE IF NOT EXISTS` NO se recrea NUNCA
--     MÁS, porque del segundo arranque en adelante el CREATE entero es un NO-OP
--     exacto — si alguien lo dropea a mano o lo pierde un restore parcial, el
--     replay ya no lo repondría.
--
-- ⚠️ LAS DOS FK NO RECIBEN EL MISMO TRATO, Y LA ASIMETRÍA ES EL PUNTO.
--
--   * `tenant_id` lleva su FK INLINE en el `CREATE TABLE`, como en 0002, 0015,
--     0016 y 0037, y por tanto NO se recrea en el replay. Se dice en vez de
--     callarlo, y aquí el silencio no cuesta nada: `tenant_id` es NOT NULL, así
--     que NINGÚN `ADD COLUMN IF NOT EXISTS` puede reponerla (un `ADD COLUMN NOT
--     NULL` sin default revienta sobre una tabla con filas). Si esa columna
--     faltara, faltaría la tabla entera, que es un escenario que este fichero no
--     pretende arreglar.
--
--   * `role_id` lleva su FK FUERA, con `DROP CONSTRAINT IF EXISTS` + `ADD`, igual
--     que los CHECK. 🔴 Y esto NO es simetría por gusto: es que `role_id` es la
--     ÚNICA columna con FK que el bloque de `ADD COLUMN IF NOT EXISTS` de abajo
--     PUEDE reponer —por ser NULLable—, y un `ADD COLUMN IF NOT EXISTS role_id
--     UUID` la devuelve DESNUDA: sin la referencia a `iam_roles` y sin el
--     `ON DELETE SET NULL`. Es decir, el mismo mecanismo que existe para reparar
--     una base a medias la dejaría, EN SILENCIO, con un `role_id` que apunta a
--     roles que pueden no existir y que un `DELETE` de rol ya no limpiaría. Con
--     la FK fuera, el replay la repone junto a la columna.
--
-- El precio, medido y dicho: cada replay revalida esa FK contra `iam_roles`
-- (scan de ESTA tabla + lock sobre la referenciada). Sobre una tabla de
-- invitaciones —decenas de filas por empresa, no millones— es ruido; el día que
-- deje de serlo, la conversación es cambiar el guard, no descubrir el coste.
-- 🔧 El nombre `tenant_invitations_role_id_fkey` es EXACTAMENTE el que Postgres
-- genera para una FK inline (`<tabla>_<columna>_fkey`), a propósito: una base
-- creada por un borrador anterior de este fichero —que sí la tenía inline—
-- converge, porque el `DROP … IF EXISTS` acierta con su nombre en vez de dejar
-- dos constraints gemelas.
--
-- EL REPLAY, AQUÍ BENIGNO
-- ------------------------------------------------------------
-- `CREATE TABLE IF NOT EXISTS` es, del segundo arranque en adelante, un NO-OP
-- EXACTO: no toca valores ni columnas, y las invitaciones vivas —incluidas las ya
-- canjeadas— SOBREVIVEN a cada reinicio. Los `ADD COLUMN IF NOT EXISTS` de abajo
-- existen por si una base se quedó con una versión anterior de este mismo fichero
-- (patrón 0046:69-72, 0047:73-77 y 0082).
--
-- ⚠️ ORDEN: depende de `public.tenants` (0001) y de `public.iam_roles` (0015),
-- las dos muy anteriores y ninguna retirada, así que no necesita el guard
-- `IF EXISTS` que la 0037 tuvo que ponerle a `iam_users`. Va al final, en
-- secuencia, porque es donde va todo lo nuevo.
--
-- ADITIVA e IDEMPOTENTE. SchemaVersion sube a 0.47.0 (T-A6): la 0.46.0 ya está
-- escrita en la fila de `public.schema_version` de UAT (journal 2026-08-28,
-- `version=0.46.0 content_hash=195f1659f1371310 skipped=false`), así que ésta es
-- la primera migración que cambia el esquema POR ENCIMA de una versión ya
-- publicada — segunda mitad de la regla de version.go. NO clean-slate.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN — ⏳ ESCRITAS SIN BASE DELANTE en el primer borrador; (V1) y (V2)
-- ejecutadas después contra un Postgres 17 efímero (ver el informe de T-A1).
-- ------------------------------------------------------------
--
-- (V1) La tabla existe con sus DIEZ columnas, y CUATRO son NULLable:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'tenant_invitations'
--    ORDER BY ordinal_position;
--
--   Salida esperada — DIEZ filas; `role_id`, `redeemed_by`, `redeemed_at` y
--   `revoked_at` con is_nullable = YES y SIN column_default; las otras seis con
--   is_nullable = NO; column_default solo en `id` (gen_random_uuid()) y
--   `created_at` (now()).
--
--   🔴 `expires_at` tiene que salir con column_default VACÍO. Si algún día sale
--   con un `now() + interval`, alguien fijó el TTL en el esquema en vez de en el
--   emisor —y con él habrá enterrado el clamp [60 s, 30 días], que Postgres no
--   aplica—. ⚠️ Y ojo: `information_schema` enseña el estado FINAL, no con qué
--   nació la columna.
--
-- (V2) Los dos CHECK y el índice único existen, con sus nombres:
--
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint
--    WHERE conrelid = 'public.tenant_invitations'::regclass AND contype = 'c';
--   -- esperado: tenant_invitations_token_hash_len_check
--   --           tenant_invitations_redeemed_pair_check
--
--   Y la FK de `role_id` sigue viva y con su regla de borrado — es la que el
--   replay recrea, así que es la que hay que mirar:
--
--   SELECT conname, pg_get_constraintdef(oid)
--     FROM pg_constraint
--    WHERE conrelid = 'public.tenant_invitations'::regclass AND contype = 'f';
--   -- esperado: DOS filas, y la de role_id tiene que decir ON DELETE SET NULL.
--   -- 🔴 UNA SOLA fila (o una role_id sin ON DELETE) significa que el ADD COLUMN
--   -- repuso la columna desnuda y el bloque de la FK no corrió detrás.
--
--   SELECT indexname, indexdef FROM pg_indexes
--    WHERE schemaname = 'public' AND tablename = 'tenant_invitations';
--   -- esperado: la PK, tenant_invitations_token_hash_uidx (UNIQUE) y
--   --           idx_tenant_invitations_tenant, este último COMPUESTO:
--   --           (tenant_id, created_at DESC). Si sale con una sola columna, la
--   --           base se quedó con la versión previa a la ampliación de T-A2 y su
--   --           `DROP INDEX IF EXISTS` no corrió: el listado ordena sin índice.
--
-- (V3) La base RECHAZA de verdad las dos incoherencias, y ACEPTA la fila buena
--      (las dos mitades: un rechazo sobre una tabla que no acepta NADA no
--      probaría nada). Sustituye <T> por un tenant real de la base:
--
--   INSERT INTO public.tenant_invitations (tenant_id, token_hash, expires_at, created_by)
--   VALUES ('<T>', sha256('prueba'::bytea), now() + interval '1 day', gen_random_uuid());
--   -- esperado: INSERT 0 1
--
--   INSERT INTO public.tenant_invitations (tenant_id, token_hash, expires_at, created_by)
--   VALUES ('<T>', 'token-en-claro'::bytea, now() + interval '1 day', gen_random_uuid());
--   -- esperado: ERROR ... "tenant_invitations_token_hash_len_check"
--
--   UPDATE public.tenant_invitations SET redeemed_at = now() WHERE tenant_id = '<T>';
--   -- esperado: ERROR ... "tenant_invitations_redeemed_pair_check"
--
--   DELETE FROM public.tenant_invitations WHERE tenant_id = '<T>';
--
-- (V4) El barrido de PII: la tabla no tiene NI UNA columna de texto donde quepa
--      un correo o un nombre. No es una consulta sobre las filas, es una sobre el
--      esquema, y por eso NO caduca con los datos:
--
--   SELECT count(*) AS columnas_de_texto FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'tenant_invitations'
--      AND data_type IN ('text','character varying','character');   -- esperado: 0
--
--   🔴 Si algún día devuelve > 0, alguien le añadió una columna de texto a una
--   tabla que se diseñó para no tener ninguna, y el ADR-0034 pide justificarla
--   antes que aceptarla.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_invitations (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    token_hash  BYTEA       NOT NULL,   -- 🔴 SHA-256 del token. El token en claro NO se guarda jamás
    role_id     UUID,                   -- NULL = alta sin rol. Su FK va APARTE, mas abajo (ver cabecera)
    expires_at  TIMESTAMPTZ NOT NULL,   -- SIN default: el TTL lo elige el emisor (24 h por defecto, clamp 60 s - 30 dias)
    created_by  UUID        NOT NULL,   -- quien emite; identidad en identity (otra BD), SIN FK
    redeemed_by UUID,                   -- quien canjeo; NULL mientras no se canjea. SIN FK, misma razon
    redeemed_at TIMESTAMPTZ,            -- NULL -> now() de forma atomica, un solo uso (molde 0002)
    revoked_at  TIMESTAMPTZ,            -- anulada por la duena (T-A8); terminal, como redeemed_at
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Por si una base ya tiene la tabla de una versión anterior de este fichero: el
-- replay le añade lo que falte sin tocar las filas existentes (patrón 0046:69-72,
-- 0047:73-77 y 0082). Solo las CUATRO NULLables pueden llegar por esta vía, y se
-- dice por qué las otras seis no: un `ADD COLUMN NOT NULL` sin default revienta
-- sobre una tabla con filas, así que no se puede fingir que se reponen.
--
-- 🔴 Y ESTO NO ES ADORNO, es la mitad que evita el fallo ya sufrido en este repo:
-- un `COMMENT ON COLUMN` sobre una columna que no existe MATA EL SEGUNDO ARRANQUE
-- del servidor, no una consulta lejana. Los COMMENT del final dependen de que
-- estas cuatro existan.
ALTER TABLE public.tenant_invitations ADD COLUMN IF NOT EXISTS role_id     UUID;
ALTER TABLE public.tenant_invitations ADD COLUMN IF NOT EXISTS redeemed_by UUID;
ALTER TABLE public.tenant_invitations ADD COLUMN IF NOT EXISTS redeemed_at TIMESTAMPTZ;
ALTER TABLE public.tenant_invitations ADD COLUMN IF NOT EXISTS revoked_at  TIMESTAMPTZ;

-- ------------------------------------------------------------
-- LA FK DE `role_id`, FUERA DEL CREATE TABLE Y RECREADA EN CADA REPLAY
-- ------------------------------------------------------------
-- Va aquí, y no inline como la de `tenant_id`, por lo dicho en la cabecera: el
-- `ADD COLUMN IF NOT EXISTS role_id UUID` de tres líneas más arriba repone la
-- columna DESNUDA —sin referencia y sin `ON DELETE SET NULL`—, así que la única
-- vía que este fichero tiene para reparar una base a medias dejaría, en silencio,
-- un `role_id` sin integridad referencial. Con la FK fuera, la columna y su
-- referencia se reponen JUNTAS. Tiene que ir DESPUÉS del `ADD COLUMN`: sobre una
-- base a medias, la columna aún no existe cuando este bloque se ejecuta.
ALTER TABLE public.tenant_invitations
    DROP CONSTRAINT IF EXISTS tenant_invitations_role_id_fkey;
ALTER TABLE public.tenant_invitations
    ADD CONSTRAINT tenant_invitations_role_id_fkey
    FOREIGN KEY (role_id) REFERENCES public.iam_roles(id) ON DELETE SET NULL;

-- ------------------------------------------------------------
-- LOS DOS CHECK, FUERA DEL CREATE TABLE Y RECREADOS EN CADA REPLAY (regla 4)
-- ------------------------------------------------------------
-- (a) EL TAMAÑO DEL DIGEST. Un SHA-256 son 32 bytes EXACTOS, así que esta
--     constraint convierte el enunciado «aquí va el hash, no el token» en algo
--     que la base puede rechazar: un `[]byte(token)` descuidado, o un hash
--     guardado en hex (64 bytes), fallan en el INSERT en vez de colarse.
--     🔴 LO QUE NO PUEDE HACER, dicho aquí para que nadie lo lea de más: NO
--     distingue un digest de un token en claro que midiera justo 32 bytes.
--     Postgres no sabe qué es un hash. Esa mitad la sostienen el emisor (T-A2) y
--     sus tests, y no hay red debajo.
ALTER TABLE public.tenant_invitations
    DROP CONSTRAINT IF EXISTS tenant_invitations_token_hash_len_check;
ALTER TABLE public.tenant_invitations
    ADD CONSTRAINT tenant_invitations_token_hash_len_check
    CHECK (octet_length(token_hash) = 32);

-- (b) LA COHERENCIA DEL PAR DEL CANJE. `redeemed_by` y `redeemed_at` son dos
--     mitades de UN hecho —quién canjeó y cuándo—, y una fila con una sola de
--     las dos es un dato roto: con `redeemed_at` a solas la invitación queda
--     consumida sin que nadie sepa por quién (y el alta de T-A3 no se puede
--     auditar); con `redeemed_by` a solas, la invitación sigue pareciendo
--     PENDIENTE aunque alguien ya la usó, que es el fallo peor de los dos.
--     Se escribe como igualdad de nulidad —no como implicación— para que cubra
--     las DOS direcciones a la vez.
ALTER TABLE public.tenant_invitations
    DROP CONSTRAINT IF EXISTS tenant_invitations_redeemed_pair_check;
ALTER TABLE public.tenant_invitations
    ADD CONSTRAINT tenant_invitations_redeemed_pair_check
    CHECK ((redeemed_by IS NULL) = (redeemed_at IS NULL));

-- ------------------------------------------------------------
-- Los dos índices, y el porqué de cada uno.
-- ------------------------------------------------------------
-- ÚNICO sobre el hash: es la clave de acceso del canje. La persona llega con un
-- token y SIN tenant, así que la única pregunta que la base recibe en ese camino
-- es `WHERE token_hash = $1` — no hay tenant por el que filtrar, y por eso el
-- índice es GLOBAL y no por empresa. Único además de índice: dos invitaciones
-- distintas con el mismo digest harían ambiguo el canje, y un choque aquí es la
-- señal de que el generador de tokens dejó de ser aleatorio.
CREATE UNIQUE INDEX IF NOT EXISTS tenant_invitations_token_hash_uidx
    ON public.tenant_invitations (token_hash);

-- Por empresa, Y POR FECHA: el listado de T-A2 («qué invitaciones tengo vivas»)
-- filtra por tenant y ordena por `created_at DESC, id DESC`.
--
-- ⚠️ ESTE ÍNDICE NACIÓ SIENDO SOLO `(tenant_id)` y se amplió el mismo día. El
-- criterio de la 0082 —no pagar un índice compuesto por una consulta que nadie ha
-- escrito— era correcto **mientras T-A2 no existía**; en cuanto fijó su ORDER BY,
-- la condición que lo justificaba dejó de cumplirse. Se deja dicho para que nadie
-- lea la ampliación como una contradicción del criterio: es el criterio aplicado.
--
-- 🔴 VA CON `DROP INDEX IF EXISTS` DELANTE, y no es adorno: `CREATE INDEX IF NOT
-- EXISTS` con el MISMO nombre sobre una base que ya tiene la versión de una sola
-- columna es un NO-OP EXACTO — no la amplía, no avisa, y el listado seguiría
-- ordenando sin índice. Es el mismo gotcha que los CHECK y la FK de arriba: lo que
-- el replay no recrea, no se corrige nunca. El nombre se conserva a propósito para
-- que una base de desarrollo con el índice viejo CONVERJA en vez de quedarse con
-- dos índices solapados.
--
-- `id DESC` NO entra en el índice: desempata dentro de un mismo `created_at`
-- (colisión posible solo entre dos emisiones en el mismo microsegundo) y añadirlo
-- se pagaría en cada escritura para ordenar un caso que casi nunca ocurre.
DROP INDEX IF EXISTS idx_tenant_invitations_tenant;
CREATE INDEX IF NOT EXISTS idx_tenant_invitations_tenant
    ON public.tenant_invitations (tenant_id, created_at DESC);

COMMENT ON TABLE public.tenant_invitations IS
    'Invitaciones de UN SOLO USO para incorporar a alguien a una empresa (Plan 047 · Ola A · T-A1, D-047.11). La duena emite, reparte el codigo por WhatsApp, y la persona lo canjea con un Context Token SIN TENANT despues de registrarse ella misma en el signup publico de identity. 🔴 CERO PII: no hay correo, ni nombre, ni telefono, y no los va a haber -- la duena no teclea ningun correo en ningun momento, asi que la nube nunca necesita saber a quien se mando la invitacion. Esa es la diferencia con access_requests (0060), cuyo destinatario es el operador de plataforma. 🔴 El token en claro NO se guarda: solo su SHA-256 (token_hash), y quien emite lo ve UNA sola vez. El canje NO escribe aqui la membresia: pasa por GrantTenantAccess, unico escritor de tenant_members (vigilado por membresia_unica_ast_test.go).';
COMMENT ON COLUMN public.tenant_invitations.id          IS 'Identidad de la invitacion (UUID). NO es el token: el token no se deriva de aqui ni se puede reconstruir desde aqui. Este id es lo que cita el listado y lo que revoca T-A8.';
COMMENT ON COLUMN public.tenant_invitations.tenant_id   IS 'Empresa a la que invita (FK a tenants, ON DELETE CASCADE: si la empresa desaparece, sus invitaciones no sobreviven para dar acceso a nada). Sale del token de quien emite (INV-04), NUNCA del cuerpo de la peticion -- leerlo del cuerpo es la mutacion que T-A3 declara roja.';
COMMENT ON COLUMN public.tenant_invitations.token_hash  IS 'SHA-256 del token opaco, 32 bytes exactos (CHECK tenant_invitations_token_hash_len_check). 🔴 El token EN CLARO no se guarda jamas ni se puede recuperar: se devuelve una sola vez al emitir y nunca aparece en el listado. Clave de acceso del canje, con indice UNICO y GLOBAL porque quien canjea llega sin tenant y no hay nada mas por lo que filtrar. El CHECK no puede distinguir un digest de un texto de 32 bytes: esa mitad la sostiene el emisor.';
COMMENT ON COLUMN public.tenant_invitations.role_id     IS 'Rol que se concedera AL CANJEAR (FK a iam_roles). NULLable a proposito: GrantTenantAccess acepta roleID nil porque dar de alta a alguien y darle un rol son dos decisiones distintas del administrador, y la segunda tiene su propia puerta (in.RoleAdmin.AssignRole) -- ver memberships.go:196-198. ON DELETE SET NULL y no CASCADE: si el rol se borra, la invitacion sigue VIVA y da de alta sin rol; borrar un rol no puede anular por sorpresa las incorporaciones en vuelo. Apunta tanto a una plantilla global (tenant_id NULL) como a un rol propio del tenant (0015).';
COMMENT ON COLUMN public.tenant_invitations.expires_at  IS 'Vencimiento de la invitacion; el canje la rechaza si ya paso (410 en T-A3). NOT NULL porque una invitacion sin caducidad no es una invitacion, es una puerta abierta. El TTL esta DECIDIDO (Jhoan, 2026-08-28): lo elige quien emite en el cuerpo de POST /api/v1/invitations ({"ttl": segundos}), con default 24 h (86400) y clamp [60 s, 30 dias] -- precedente literal de los codigos de enrolamiento, platformadmin/handlers.go:249-260. ⚠️ SIN DEFAULT en el esquema y a proposito: el numero es PARAMETRIZABLE POR LLAMADA, asi que el DDL no es su sitio. Un DEFAULT aqui congelaria un valor que el emisor sobreescribe, haria creer que el clamp vive en la base (no vive: es una guarda de Go y no alcanza a un INSERT a mano) y dejaria dos fuentes para el mismo numero. El default y el clamp viven en UN sitio, el emisor (T-A2).';
COMMENT ON COLUMN public.tenant_invitations.created_by  IS 'Usuario que emitio la invitacion. UUID de identity (iam.users, OTRA base de datos) y SIN FK, mismo criterio que tenant_members.user_id (0037) e INV-02 de identity: la frontera no lleva integridad referencial fisica. Es un identificador opaco, no una persona: aqui no hay nombre ni correo.';
COMMENT ON COLUMN public.tenant_invitations.redeemed_by IS 'Usuario que canjeo la invitacion; NULL mientras no se canjea. UUID de identity SIN FK, igual que created_by. Va SIEMPRE en pareja con redeemed_at (CHECK tenant_invitations_redeemed_pair_check): una fila con uno solo de los dos es un dato roto.';
COMMENT ON COLUMN public.tenant_invitations.redeemed_at IS 'Momento del canje; NULL = sin canjear. Pasa de NULL a now() de forma ATOMICA en el mismo UPDATE que comprueba que seguia pendiente (... WHERE redeemed_at IS NULL AND revoked_at IS NULL): ahi vive el UN SOLO USO frente a dos canjes concurrentes, no en una constraint. Molde exacto de enrollment_codes.used_at (0002).';
COMMENT ON COLUMN public.tenant_invitations.revoked_at  IS 'Momento en que la duena ANULO la invitacion (T-A8); NULL = no revocada. Terminal, como redeemed_at, y con el mismo tratamiento atomico. La exclusividad entre los dos terminales NO la vigila un CHECK: la da el UPDATE condicional, que ademas es lo unico que resuelve la carrera -- un CHECK dejaria pasar a las dos transacciones igual.';
COMMENT ON COLUMN public.tenant_invitations.created_at  IS 'Momento de la emision. Usa el DEFAULT now(). No hay updated_at: una invitacion no se edita, se revoca y se emite otra.';
