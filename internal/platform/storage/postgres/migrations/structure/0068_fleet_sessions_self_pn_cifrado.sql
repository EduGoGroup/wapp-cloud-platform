-- ============================================================
-- 0068: fleet_sessions.self_pn — EL SOBRE DE CUATRO PIEZAS
-- (Plan 046 · Ola 4 · T4.1, D-046.9; cierra la mitad DDL de REQ-16).
--
-- QUÉ GUARDA, Y POR QUÉ EXISTE
-- ------------------------------------------------------------
-- La 0028 trajo `self_pn TEXT NULL`: el número propio (E.164 sin '+', normalizado)
-- que cada sesión reporta en su Heartbeat, EN CLARO. El censo del MP-06 lo señaló
-- como el ÚNICO teléfono en claro de toda la base. Esta migración le monta el mismo
-- sobre de cuatro piezas que `public.contacts` lleva desde la 0006 (ADR-0017):
--
--   * self_pn_bidx   — hex(HMAC-SHA256(indexKey, tenant_id||0x00||self_pn_norm)):
--                      índice CIEGO, es lo único por lo que se puede buscar.
--   * self_pn_enc    — envelope AES-256-GCM del número NORMALIZADO (nonce fresco).
--   * self_pn_dek    — DEK por-fila (32B), envuelta por la KEK maestra.
--   * self_pn_kek_id — key_id de la KEK que envolvió la DEK (discriminador del Rekey).
--
-- ADR-0009: aquí solo vive dato de NEGOCIO cifrado. La KEK NO vive en esta BD
-- (env/secret store, design.md §10.A) y la DEK del Edge —la del store de whatsmeow,
-- ADR-0007— no se toca ni se menciona: son cosas distintas que comparten nombre.
--
-- 🔴 ESTA MIGRACIÓN NO CIFRA NADA, Y ESO NO ES UN OLVIDO
-- ------------------------------------------------------------
-- Se ve incompleta a propósito, y dentro de seis meses alguien va a querer
-- "arreglarla" metiéndole el backfill dentro. NO SE PUEDE: cifrar exige la KEK y la
-- indexKey, y POSTGRES NO LAS TIENE — precisamente porque no las tiene es por lo que
-- este sobre protege algo (`0006:20-21`). El relleno lo hace un paso de Go (arranque
-- o comando), que lee el claro, calcula el bidx sobre el NORMALIZADO, cifra, escribe
-- las cuatro columnas y VACÍA la columna en claro. No hay runbook SQL equivalente,
-- al revés que el backfill del Plan 053 (`docs/runbooks/backfill-053-owner-event-id.sql`),
-- que es SQL porque no cifra.
--
-- 🔴 `self_pn` SE CONSERVA Y QUEDA VACÍA. Su DROP es OTRA migración, de otra tarea:
-- aquí sigue existiendo para que el paso de backfill tenga de dónde leer, y para que
-- un rollback del binario durante el despliegue no se encuentre la columna borrada.
-- ⚠️ Cuando llegue ese DROP, tendrá que convivir con el replay de la 0028, que
-- RECREA la columna vacía en cada arranque — el mismo baile 0025↔0064 que el retiro
-- de `role` documentó: converge, cuesta catálogo y no datos.
--
-- ------------------------------------------------------------
-- 🔴 LAS TRES REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrations/migrate.go): re-aplica TODO el
-- directorio en cuanto cambia el hash del conjunto. Las tres reglas de la 0063 no
-- son estilo, así que se dice una por una qué se hizo con cada cuál:
--
-- (1) SIN DEFAULT — SE APLICA, y aquí ni siquiera hay un default que tenga sentido.
--     Un valor por defecto en `self_pn_bidx` sería un HMAC inventado que haría casar
--     el anti-self-loop de todas las filas contra el mismo número fantasma; uno en
--     `self_pn_enc`/`self_pn_dek` sería un texto cifrado que ninguna KEK abre. Las
--     cuatro nacen NULL y NULL significa exactamente «esta sesión no tiene número
--     conocido, o su backfill todavía no ha corrido».
--
-- (2) BACKFILL CON GUARD `WHERE ... IS NULL` — NO SE APLICA AQUÍ, porque no hay
--     backfill en este fichero (ver arriba: Postgres no puede cifrar). Pero la regla
--     NO desaparece, se MUDA: el paso en Go que rellene esto tiene que llevar su
--     propio centinela —«solo filas con `self_pn` no vacío y `self_pn_bidx IS NULL`»—
--     o el segundo arranque re-cifrará filas ya cifradas, y como el nonce es fresco
--     por escritura, cada reinicio dejaría un `self_pn_enc` distinto para el mismo
--     número. El bidx no cambiaría (es determinista), así que el anti-loop seguiría
--     casando y NADIE SE ENTERARÍA: es escritura muda, la peor clase.
--
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ ... IF is_nullable = 'YES'` — NO SE APLICA,
--     y esto se escribe explícitamente en vez de dejar el silencio: aquí NO HAY
--     `SET NOT NULL` NI LO HABRÁ. Las cuatro columnas se quedan NULLables PARA
--     SIEMPRE, porque una sesión sin emparejar legítimamente NO TIENE número
--     (`0028:12-13`: un self_pn vacío no sobrescribe un valor previo bueno), y el
--     aviso de la 0066 depende justo de esa distinción. Es el mismo caso NULLable
--     que el trío de `tenant_integrations` (0047:39-42): las cuatro van juntas o no
--     van —o las cuatro NULL, o las cuatro pobladas—, invariante que vive en el
--     CÓDIGO y no en un CHECK, para no bloquear la escritura parcial de una rotación.
--
-- (4) La cuarta regla que la 0063 añadió en su corrección (CHECK con nombre explícito
--     y recreado en cada replay) tampoco aplica: aquí no hay ningún CHECK. No lo
--     hay porque no existe dominio cerrado que acotar —un HMAC hex y tres blobs— y
--     porque la invariante «las cuatro o ninguna» es del código, por lo de (3).
--
-- EL REPLAY, AQUÍ BENIGNO (mismo argumento que 0066:54-61)
-- ------------------------------------------------------------
-- `ADD COLUMN IF NOT EXISTS` es, del segundo arranque en adelante, un NO-OP EXACTO:
-- no toca valores. Los sobres ya escritos SOBREVIVEN a cada reinicio, y —lo que más
-- importa— el replay de la 0028 NO resucita los números que el backfill vació, por
-- lo mismo: su `ADD COLUMN IF NOT EXISTS self_pn` no se ejecuta cuando la columna ya
-- existe, y un ADD COLUMN que no corre no escribe nada.
--
-- ⚠️ ORDEN: toca la MISMA tabla que la 0003/0028/0063/0064/0065/0066 y va POR ENCIMA
-- de todas. No lee ninguna de sus columnas en este DDL, pero renumerarla por debajo
-- de la 0003 (que CREA la tabla) o de la 0028 la rompería igual que a cualquier otra.
--
-- ADITIVA e IDEMPOTENTE. SchemaVersion sube a 0.41.0.
--
-- ------------------------------------------------------------
-- VERIFICACIÓN (no hay PostgreSQL en el entorno donde se escribió esto;
-- estas consultas son para el sweep del CLI / el operador en UAT)
-- ------------------------------------------------------------
--
-- (V1) Las cuatro columnas existen, NULLABLES y SIN default:
--
--   SELECT column_name, data_type, is_nullable, column_default
--     FROM information_schema.columns
--    WHERE table_schema = 'public' AND table_name = 'fleet_sessions'
--      AND column_name LIKE 'self\_pn\_%'
--    ORDER BY column_name;
--
--   Salida esperada — EXACTAMENTE cuatro filas, `is_nullable` = YES en todas y
--   `column_default` vacío en todas:
--
--    column_name    | data_type | is_nullable | column_default
--   ----------------+-----------+-------------+----------------
--    self_pn_bidx   | text      | YES         |
--    self_pn_dek    | bytea     | YES         |
--    self_pn_enc    | bytea     | YES         |
--    self_pn_kek_id | text      | YES         |
--   (4 rows)
--
-- (V2) El índice del lookup existe, es PARCIAL y NO es único:
--
--   SELECT indexdef FROM pg_indexes
--    WHERE schemaname = 'public' AND tablename = 'fleet_sessions'
--      AND indexname = 'idx_fleet_sessions_self_pn_bidx';
--
--   El `indexdef` tiene que contener `(tenant_id, self_pn_bidx)`, el
--   `WHERE (self_pn_bidx IS NOT NULL)` y NO contener `UNIQUE` (ver el porqué abajo).
--
-- (V3) Criterio (a) de T4.1, DESPUÉS de correr el backfill de Go — las dos mitades,
--      porque vaciar sin cifrar también daría cero en la primera:
--
--   SELECT count(*) AS en_claro FROM public.fleet_sessions
--    WHERE self_pn IS NOT NULL AND self_pn <> '';           -- esperado: 0
--   SELECT count(*) AS cifradas FROM public.fleet_sessions
--    WHERE self_pn_bidx IS NOT NULL;                        -- esperado: nº de filas que tenían número
--
-- (V4) La invariante «las cuatro o ninguna», que no tiene CHECK que la vigile:
--
--   SELECT count(*) AS sobres_rotos FROM public.fleet_sessions
--    WHERE num_nonnulls(self_pn_bidx, self_pn_enc, self_pn_dek, self_pn_kek_id)
--          NOT IN (0, 4);                                   -- esperado: 0
-- ============================================================

-- Las cuatro piezas del sobre. Nombres y tipos calcados de public.contacts (0006:48-50)
-- y del trío NULLable de public.tenant_integrations (0047:65-67, 75-77), con el
-- prefijo del campo que protegen: <campo>_bidx / _enc / _dek / _kek_id.
ALTER TABLE public.fleet_sessions ADD COLUMN IF NOT EXISTS self_pn_enc    BYTEA;
ALTER TABLE public.fleet_sessions ADD COLUMN IF NOT EXISTS self_pn_dek    BYTEA;
ALTER TABLE public.fleet_sessions ADD COLUMN IF NOT EXISTS self_pn_kek_id TEXT;
ALTER TABLE public.fleet_sessions ADD COLUMN IF NOT EXISTS self_pn_bidx   TEXT;

-- ------------------------------------------------------------
-- El índice del lookup ciego.
-- ------------------------------------------------------------
-- Hoy la tabla solo tiene idx_fleet_sessions_tenant (tenant_id, 0003:57-58), que
-- para estas consultas obliga a leer TODAS las sesiones del tenant y filtrar.
-- Tras T4.1 hay DOS lecturas que preguntan exactamente por (tenant_id, self_pn_bidx):
-- el anti-self-loop (`IsSelfNumber`, self_numbers.go — el que decide si un entrante
-- viene de otra sesión del mismo tenant) y el aviso de tope de dispositivos
-- (`CountLiveBySelfPn`, gateway/fleet/repository_postgres.go). Las dos pasan de
-- igualdad sobre el número en claro a igualdad sobre el HMAC, y el orden de las
-- columnas es el de la consulta: tenant primero (INV-7: NINGUNA consulta cruza
-- tenants), bidx después.
--
-- PARCIAL `WHERE self_pn_bidx IS NOT NULL`: las dos consultas buscan siempre un bidx
-- CONCRETO, así que las filas sin número —una sesión aún sin emparejar, y toda fila
-- preexistente mientras el backfill no corra— no se pueden encontrar por aquí nunca.
-- Fuera del índice, entonces: menos páginas y menos escrituras en el camino caliente
-- del Heartbeat, que reescribe estas filas constantemente.
--
-- 🔴 NO ES UNIQUE, Y NO ES UN DESCUIDO. El bidx es determinista por (tenant, valor),
-- así que dos sesiones del MISMO tenant con el MISMO número tienen el MISMO bidx —
-- que es justo el caso que el `GROUP BY` + `HAVING` del anti-loop existe para
-- resolver (decide POR NÚMERO, no por fila; la semántica que arregló MP-03). Un
-- índice único aquí haría fallar el Heartbeat de la segunda sesión con violación de
-- unicidad, y por debajo se llevaría por delante esa semántica.
CREATE INDEX IF NOT EXISTS idx_fleet_sessions_self_pn_bidx
    ON public.fleet_sessions (tenant_id, self_pn_bidx)
    WHERE self_pn_bidx IS NOT NULL;

-- ------------------------------------------------------------
-- El índice de la rotación de KEK.
-- ------------------------------------------------------------
-- ⚠️ Va MÁS ALLÁ del enunciado literal de T4.1 (que solo nombra el índice de arriba),
-- y se añade porque las TRES tablas que hoy están en el censo de rotación lo tienen:
-- idx_contacts_kek (0007:34), idx_intake_buyer_data_kek (0045:193) y
-- tenant_integrations_kek_idx (0047:82). T4.1 mete fleet_sessions en ese censo
-- (rekey.go, dekCol self_pn_dek / kekCol self_pn_kek_id), así que sin esta línea
-- sería la única de las cuatro sin él. Parcial por lo mismo que el de arriba: la fila
-- sin sobre queda fuera del barrido sola —`NULL <> 'x'` no es TRUE en SQL (0047:66-69)—
-- y no tiene por qué ocupar índice.
CREATE INDEX IF NOT EXISTS idx_fleet_sessions_self_pn_kek
    ON public.fleet_sessions (self_pn_kek_id)
    WHERE self_pn_kek_id IS NOT NULL;

COMMENT ON COLUMN public.fleet_sessions.self_pn_bidx IS
  'Indice ciego del numero propio: hex(HMAC-SHA256(indexKey, tenant_id||0x00||self_pn_norm)), 64 chars. Determinista por (tenant, valor) -- por eso agrupa igual que el self_pn en claro y el GROUP BY del anti-self-loop conserva su semantica (MP-03) -- y no invertible sin la indexKey, que vive fuera de esta BD (design.md seccion 10.C). 🔴 Se calcula SIEMPRE sobre el numero NORMALIZADO (contact.Normalize, E.164 sin +): calcularlo sobre el crudo deja el anti-loop mudo, casando solo cuando el remitente venga escrito identico. Es lo unico por lo que se puede buscar esta columna. Estable ante rotacion de KEK. NULL = sesion sin numero conocido. Plan 046 · T4.1, D-046.9.';
COMMENT ON COLUMN public.fleet_sessions.self_pn_enc IS
  'Numero propio NORMALIZADO cifrado con envelope AES-256-GCM (nonce fresco por escritura), patron de contacts.value_enc (0006). Sustituye a self_pn, que queda VACIA tras el backfill y se borra en una migracion futura. Se descifra en el borde de la app (GET /api/v1/sessions sigue sirviendo el numero en claro). Dato de NEGOCIO (ADR-0009), nunca credencial ni llave; NUNCA se loguea (PII). Plan 046 · T4.1.';
COMMENT ON COLUMN public.fleet_sessions.self_pn_dek IS
  'DEK por-fila (32B) que cifra self_pn_enc, envuelta por la KEK maestra (design.md seccion 10.B). La KEK NO vive en esta BD. NO tiene NADA que ver con la DEK del Edge (el store de whatsmeow, ADR-0007), que la nube jamas ve. Plan 046 · T4.1.';
COMMENT ON COLUMN public.fleet_sessions.self_pn_kek_id IS
  'key_id de la KEK que envolvio self_pn_dek. Discriminador de la rotacion: distinto del current => fila pendiente de re-envolver (crypto.PendingByKeyID / Rekey, censo rekeyTargets). Sin esta tabla en el censo, la rotacion diria completa con estas filas aun bajo la KEK vieja -- el fallo exacto que el censo existe para impedir (rekey.go:41-45). Plan 046 · T4.1.';

-- Las cuatro son NULLables PARA SIEMPRE y van juntas o no van (o las cuatro NULL, o
-- las cuatro pobladas): invariante del CODIGO, sin CHECK, para no bloquear la
-- escritura parcial de una rotacion -- igual que el trio de tenant_integrations
-- (0047:39-42). Comprobable con la consulta (V4) de la cabecera.
