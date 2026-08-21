-- ============================================================
-- BACKFILL de tenant_settings.conversation_ttl_seconds — Plan 046 · Ola 4 · T4.4
-- (D-046.12, MD-046.4 salida 1, REQ-19)
--
-- NO ES UNA MIGRACIÓN. Es una consulta OPERATIVA que se corre A MANO contra la base
-- (Neon de UAT) con `psql`, UNA sola vez por ambiente. Vive aquí y NO en
-- `internal/platform/storage/postgres/migrations/structure/` por la misma razón dura
-- que el `backfill-053-owner-event-id.sql` de al lado: el runner de migraciones es
-- hash-based FULL-REPLAY y re-ejecuta TODOS los `.sql` de `structure/` cuando cambia
-- el hash del conjunto.
--
-- 🔴 Y AQUÍ ESO NO ES UNA MOLESTIA, ES DESTRUCTIVO PARA SIEMPRE. `0` es un valor
-- LEGÍTIMO que un tenant puede elegir: significa «sin vencimiento». Si este `UPDATE`
-- viajara dentro de la 0067, el tenant que mañana ponga `0` a propósito volvería a
-- `7200` en el siguiente arranque que recalcule el hash, y otra vez, y otra. La
-- columna es `NOT NULL`, así que no hay valor centinela con el que guardar el
-- `UPDATE`: el truco de la 0063 (`profile IS NULL`) aquí NO existe.
--
-- ------------------------------------------------------------
-- EL RESIDUO DE PRODUCTO QUE ESTO NO RESUELVE (MD-046.4)
-- ------------------------------------------------------------
-- Hoy es IMPOSIBLE distinguir un `0` HEREDADO (el DEFAULT viejo de la 0034, que nadie
-- eligió) de un `0` ELEGIDO (un tenant que quiere conversaciones sin vencimiento).
-- Los dos son el mismo entero en la misma columna.
--
-- Jhoan lo resolvió el 2026-08-21 por la **salida 1: backfill por runbook**, ejecutado
-- una vez, y NO por la salida 3 (hacer la columna `NULL`-able para poder distinguir).
-- La consecuencia operativa hay que decirla en voz alta: **este script se corre ANTES
-- de que ningún tenant haya podido elegir `0` a sabiendas**. Corrido más tarde,
-- pisaría esa elección sin manera de saberlo. Si en el futuro hace falta correrlo otra
-- vez, hay que acotarlo por `tenant_id` a mano.
--
-- ------------------------------------------------------------
-- CUÁNDO SE CORRE
-- ------------------------------------------------------------
--   UNA vez por ambiente, DESPUÉS de aplicar la migración 0067 (esquema 0.43.0) y de
--   desplegar el binario que trae el espejo en Go (`store.DefaultConversationTTL`).
--
--   Correrlo N veces seguidas es INOCUO mientras nadie haya elegido `0`: el `UPDATE`
--   lleva la guarda `WHERE conversation_ttl_seconds = 0` y a la segunda pasada afecta
--   a 0 filas. Lo que NO es inocuo es correrlo tarde (ver el bloque de arriba).
--
-- 📌 Lo que este script NO cubre, y no es un bug: los tenants **sin fila** en
-- `public.tenant_settings` (hoy 2 de 3 en UAT). Esos no leen ni el DEFAULT de la
-- columna ni este `UPDATE`: los gobierna `store.DefaultTenantSettings`, que es la
-- parte (2) de T4.4 y viaja en el binario.
-- ============================================================


-- ------------------------------------------------------------
-- PASO 1 · DIAGNÓSTICO (antes de tocar nada)
-- ------------------------------------------------------------
-- Deja la foto de partida. Si `en_cero` es 0, no hay trabajo que hacer y el paso 2
-- no cambiará ninguna fila.

SELECT count(*)                                            AS filas_total,
       count(*) FILTER (WHERE conversation_ttl_seconds = 0) AS en_cero,
       count(*) FILTER (WHERE conversation_ttl_seconds = 7200) AS ya_en_7200,
       count(*) FILTER (WHERE conversation_ttl_seconds NOT IN (0, 7200)) AS con_valor_propio
  FROM public.tenant_settings;

-- El detalle, para poder pegarlo en el journal y para ver a quién se va a tocar:

SELECT tenant_id, conversation_ttl_seconds
  FROM public.tenant_settings
 ORDER BY conversation_ttl_seconds, tenant_id;


-- ------------------------------------------------------------
-- PASO 2 · EL BACKFILL
-- ------------------------------------------------------------
-- 🔴 La guarda `= 0` no es cosmética: es lo que impide tocar al tenant que ya tiene
-- un valor propio (900, 3600, lo que sea). Ese tenant DEBE conservarlo — es el
-- criterio (d) de T4.4 y la aserción que distingue este backfill de un `UPDATE`
-- incondicional.

UPDATE public.tenant_settings
   SET conversation_ttl_seconds = 7200
 WHERE conversation_ttl_seconds = 0;


-- ------------------------------------------------------------
-- PASO 3 · VERIFICACIÓN DE CIERRE
-- ------------------------------------------------------------
-- (V1) La aserción que cierra el criterio (c) de T4.4. Tiene que devolver 0.

SELECT count(*) AS siguen_en_cero
  FROM public.tenant_settings
 WHERE conversation_ttl_seconds = 0;

-- (V2) Y que los valores propios NO se movieron: compara esta salida con la del
--      paso 1. `con_valor_propio` tiene que ser EL MISMO número, con los mismos
--      tenants y los mismos valores.

SELECT tenant_id, conversation_ttl_seconds
  FROM public.tenant_settings
 WHERE conversation_ttl_seconds NOT IN (0, 7200)
 ORDER BY tenant_id;

-- (V3) El DEFAULT de la columna quedó en 7200 (lo pone la 0067, no este script; se
--      comprueba aquí porque sin él las filas NUEVAS volverían a nacer en 0):

SELECT column_default
  FROM information_schema.columns
 WHERE table_schema = 'public'
   AND table_name   = 'tenant_settings'
   AND column_name  = 'conversation_ttl_seconds';   -- esperado: 7200
