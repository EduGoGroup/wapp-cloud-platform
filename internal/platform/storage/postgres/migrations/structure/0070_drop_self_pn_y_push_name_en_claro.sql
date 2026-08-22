-- ============================================================
-- 0070: RETIRO de las DOS columnas de PII en claro que quedaron vivas y vacías
-- tras los backfills cifrados — public.fleet_sessions.self_pn (T4.1, 0068) y
-- public.contacts.push_name (T4.2, 0069).
-- (Plan 046 · Ola 5 · T5.4, D-046.17.)
--
-- POR QUÉ VAN LAS DOS EN UNA SOLA MIGRACIÓN
-- ------------------------------------------------------------
-- Son el mismo acto: cerrar el saneo de PII del plan. Separarlas daría dos
-- despliegues, dos bumps y dos ventanas en las que el esquema afirma media verdad
-- («ya no hay teléfonos en claro, pero sí nombres»). D-046.17 las juntó a propósito.
--
-- 🔴 ESTO NO ES SOLO UNA MIGRACIÓN, Y ESO ES LO QUE NO SE VE LEYÉNDOLA
-- ------------------------------------------------------------
-- Hay código Go VIVO que nombra estas dos columnas en su SQL, y corre EN CADA
-- ARRANQUE, ANTES de que se abra un solo listener:
--
--   · fleet.BackfillSelfPn        — `WHERE self_pn IS NOT NULL AND self_pn <> ''`
--   · contact.BackfillPushName    — `WHERE push_name IS NOT NULL`
--   · fleet.SetSelfPn             — `SET ... self_pn = NULL` + `OR self_pn IS NOT NULL`
--   · contact.resolveExisting     — `SET ... push_name = NULL`
--
-- Los dos backfills BLOQUEAN el arranque y abortan el proceso si su consulta falla
-- (bootstrap.go). Aplicar este DROP sin retirarlos deja la plataforma SIN ARRANCAR,
-- con `column "self_pn" does not exist`. Por eso T5.4 despliega la migración Y la
-- retirada de ese código EN EL MISMO COMMIT: no son dos pasos, es uno.
--
-- 🔴 EL REPLAY, Y LA ASIMETRÍA ENTRE LAS DOS COLUMNAS (MEDIDA, NO DEDUCIDA)
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY: re-aplica TODO el directorio, en orden, en
-- cuanto cambia el hash del conjunto. Las dos columnas NO se comportan igual, y eso
-- no se ve leyendo — salió al correr `make test-integration`:
--
--   · self_pn  — la 0028 hace `ADD COLUMN IF NOT EXISTS`, así que RESUCITA en cada
--     replay y este DROP la vuelve a borrar. Ciclo crear/borrar por arranque, igual
--     que el que la 0025/0064 hacen con `role` desde la Ola 1. Converge.
--   · push_name — la declaran la 0005 y la 0006 DENTRO de un `CREATE TABLE IF NOT
--     EXISTS`. Sobre una tabla que ya existe, ese CREATE no hace NADA: la columna NO
--     vuelve. Este DROP es no-op a partir del segundo arranque.
--
-- 💥 Y ahí estaba la trampa: los `COMMENT ON COLUMN public.contacts.push_name` de la
-- 0005, la 0006 y la 0069 quedaban apuntando a una columna inexistente y el replay
-- MORÍA con «column "push_name" of relation "public.contacts" does not exist» — o
-- sea, la plataforma dejaba de arrancar al segundo boot. Los tres van ahora envueltos
-- en un `DO $$ … IF EXISTS … END $$`, que es el patrón que la propia 0005 ya usaba
-- para el COMMENT de `value` cuando la 0006 lo superseded. No se cambió ni una
-- semántica: solo se saltan cuando la columna ya no está.
--
-- El coste del ciclo de self_pn es de CATÁLOGO, no de datos: desde PostgreSQL 11 un
-- `ADD COLUMN` sin default no reescribe la tabla y un `DROP COLUMN` solo marca el
-- atributo como borrado.
--
-- ⚠️ EL ORDEN ES UNA INVARIANTE, NO UNA PREFERENCIA. Esta migración tiene que ir
-- SIEMPRE por debajo de la 0005, la 0006, la 0028, la 0068 y la 0069 — las cinco
-- nombran alguna de las dos columnas. Renumerarla por encima de cualquiera de ellas
-- rompe el replay con «column does not exist» y **el arranque de la plataforma
-- entera se cae**. Es el mismo aviso que lleva la 0064 respecto de la 0063, y lo
-- vigila un test: TestIntegration_Migracion0070_LasDosColumnasEnClaroNoVuelven.
--
-- 🔴 EL ROLLBACK REPUEBLA LA COLUMNA, Y NO ES UN FALLO
-- ------------------------------------------------------------
-- Volver a un binario anterior a este despliegue NO deja la base sin la columna: ese
-- binario aplica SU PROPIO directorio embebido —que no contiene esta migración— y la
-- 0028 (o la 0005/0006) la recrea VACÍA en su primer arranque. Comprobado el
-- 2026-08-21 contra PostgreSQL 16 con el `cmd/migrate` de 5207a78 (D-046.17).
-- Consecuencia práctica: este DROP cierra la HIGIENE DEL ESQUEMA, no la puerta del
-- rollback. Quien lo lea esperando lo segundo se equivocará de garantía.
--
-- ⚠️ QUÉ PASA SI ESTA MIGRACIÓN LLEGA A UNA BASE QUE AÚN TENÍA DATOS EN CLARO.
-- Se borran SIN cifrarlos: el DROP corre en el runner, que va ANTES que los
-- backfills del bootstrap, y además esos backfills ya no existen. No es pérdida de
-- privacidad —es lo contrario— y el dato de negocio se auto-repuebla solo: el
-- `self_pn` en el siguiente Heartbeat de la sesión (SetSelfPn lo cifra), y el
-- `push_name` en el siguiente entrante de ese contacto (resolveExisting lo cifra).
-- En UAT no aplica: T4.1 y T4.2 se desplegaron y verificaron ANTES que esto, con
-- los dos conteos en claro a 0 (criterio (c) de T5.4).
--
-- 🔴 Y ES EL PUNTO DE NO RETORNO DE LA PRUEBA. Mientras estas columnas existían,
-- `count(*) WHERE self_pn IS NOT NULL` y `count(*) WHERE push_name IS NOT NULL`
-- eran LA evidencia de que los backfills funcionaron. Después de esta migración esa
-- consulta ya no se puede hacer: la columna que cuenta deja de existir. Por eso el
-- criterio (c) de T5.4 exige correrla y ANOTARLA en el journal justo antes.
--
-- CERO PII. Idempotente por IF EXISTS. SchemaVersion sube a 0.44.0.
-- ============================================================

ALTER TABLE public.fleet_sessions
    DROP COLUMN IF EXISTS self_pn;

ALTER TABLE public.contacts
    DROP COLUMN IF EXISTS push_name;

-- Los COMMENT de los dos sobres cifrados prometían este DROP en futuro («queda VACIA
-- tras el backfill y se borra en T5.4»). Ya ocurrió: se reescriben para que dejen de
-- hablar de una columna que no existe. Un comentario que nombra algo retirado es una
-- pista falsa para quien inspeccione el esquema — misma razón por la que la 0064
-- reescribió el de `profile`.
COMMENT ON COLUMN public.fleet_sessions.self_pn_enc IS
  'Numero propio NORMALIZADO cifrado con envelope AES-256-GCM (nonce fresco por escritura), patron de contacts.value_enc (0006). Es la UNICA representacion del numero propio que existe en la base: la columna en claro self_pn se retiro en la 0070 (Plan 046 · T5.4). Se descifra en el borde de la app (GET /api/v1/sessions sigue sirviendo el numero en claro). Dato de NEGOCIO (ADR-0009), nunca credencial ni llave; NUNCA se loguea (PII). Plan 046 · T4.1.';

COMMENT ON COLUMN public.contacts.push_name_enc IS
  'Ultimo push_name visto, cifrado con envelope AES-256-GCM (nonce fresco por escritura), patron de contacts.value_enc (0006). Es la UNICA representacion del nombre que existe en la base: la columna en claro push_name se retiro en la 0070 (Plan 046 · T5.4). SIN indice ciego: nadie busca por nombre. NO se normaliza: es texto libre, no un identificador. GANA EL PRIMER NOMBRE NO VACIO -- el UPDATE lleva centinela push_name_enc IS NULL, no comparacion por contenido, porque dos cifrados del mismo texto nunca son iguales y una guarda por valor tomaria row-lock en cada entrante (MD-046.5). Dato de NEGOCIO (ADR-0009), nivel 2 del ADR-0034; NUNCA se loguea (PII). Hoy NO LO LEE NADIE: no aparece en ningun SELECT, asi que tampoco hay codigo de descifrado -- se escribira el dia que aparezca un lector. Plan 046 · T4.2.';
