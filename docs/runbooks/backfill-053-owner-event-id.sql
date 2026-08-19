-- ============================================================
-- BACKFILL de flow_state.owner_event_id — Plan 053 · Ola 1 · T1.3 (D-053.7, hueco E)
--
-- NO ES UNA MIGRACIÓN. Es una consulta OPERATIVA, repetible e idempotente, que se
-- corre A MANO contra la base (Neon de UAT) con `psql`. Vive aquí y NO en
-- `internal/platform/storage/postgres/migrations/structure/` por una razón dura: el
-- runner de migraciones es hash-based FULL-REPLAY (migrate.go:83,87,98,130-153) y
-- re-ejecuta TODOS los `.sql` de `structure/` cuando cambia el hash del conjunto. Un
-- `UPDATE` de datos ahí dentro volvería a correr en CADA replay futuro y PISARÍA las
-- resoluciones manuales del paso 3 — que son justo el dato que ningún automatismo
-- puede reconstruir.
--
-- ------------------------------------------------------------
-- CUÁNDO SE CORRE (y por qué DOS veces, no una)
-- ------------------------------------------------------------
-- El `tasks.md` de T1.3 lo exige como CONSULTA REPETIBLE, no como gesto único, por
-- el hueco E del paso 0: toda fila de `flow_state` escrita ENTRE la migración 0062 y
-- el despliegue del código de T1.5 nace con `owner_event_id NULL` y `flow_id <> ''`.
-- Cuando T1.6 entre en vigor, la tercera cláusula de REQ-053.1 («dueño vacío ⇒ no
-- transiciona nada») dejaría esos eventos `open` PARA SIEMPRE — una regresión frente
-- al comportamiento de hoy.
--
--   1ª pasada — justo DESPUÉS de aplicar la 0062 (deja la foto y resuelve el grueso).
--   2ª pasada — INMEDIATAMENTE ANTES de arrancar el binario con T1.5 + T1.6 (que se
--               despliegan JUNTOS, no por separado). Barre la ventana.
--
-- Correrlo N veces es seguro: todos los `UPDATE` llevan una guarda `IS DISTINCT
-- FROM` / `IS NOT NULL` que los deja en 0 filas cuando ya no hay nada que cambiar.
--
-- ------------------------------------------------------------
-- LAS TRES CATEGORÍAS DE D-053.7 (y las dos que el diseño no nombró)
-- ------------------------------------------------------------
--   1 · sin_flujo            -> `flow_id = ''`            ⇒ owner_event_id = NULL.
--   2 · dueno_evidente       -> `flow_id <> ''` y el evento de `event_id` ES el que
--                               abrió ese flujo            ⇒ owner_event_id = event_id.
--   3 · menu_sobre_heredado  -> `flow_id <> ''` y el `kind` de `event_id` es `menu`
--                               (LA DIVERGENCIA que este plan existe para nombrar)
--                                                          ⇒ A MANO, una por una.
--   4 · sin_evento_activo    -> `flow_id <> ''` y `event_id IS NULL`. 📌 NO está en
--       D-053.7. Es un flujo arrancado por una keyword/fallback de siempre (sin
--       evento) o legado anterior al 043. NO hay candidato que asignar: se queda
--       NULL, y es correcto — el invariante del plan es de UNA dirección
--       (`owner NOT NULL ⟹ flow_id <> ''`), no de dos.
--   5 · divergente_no_menu   -> `flow_id <> ''`, el activo NO es `menu` y su
--       `flow_id` de nacimiento NO coincide con el de la fila. 📌 Tampoco está en
--       D-053.7. Si aparece alguna, PARAR: significa que la divergencia ocurre por
--       una vía que el diseño no modeló y hay que volver al `design.md` antes de
--       tocar nada.
--
-- ------------------------------------------------------------
-- CÓMO SE DECIDE «EL EVENTO ES EL DUEÑO DE ESE FLUJO» (verificado contra el código)
-- ------------------------------------------------------------
-- D-053.7 lo enuncia como «el `kind` del evento coincide con el MÓDULO de ese
-- `flow_id`». No existe en el esquema ninguna tabla que mapee `flow_id -> módulo`:
-- `flow_definitions` (0004) solo guarda el grafo en JSONB y el vocabulario de tipos
-- lo fija el Registry de módulos EN CÓDIGO. Lo que SÍ hay es el dato equivalente y
-- persistido: `conversation_events.flow_id`, que se estampa al NACER el evento con
-- el `dec.FlowID` del disparo (runtime/events.go:443-450, `birthEvent`) y cuyo
-- COMMENT (0051) lo declara «el flujo que ejecutaba la conversación cuando nació el
-- evento». Por eso el criterio operativo es `ev.flow_id = fs.flow_id` — que es,
-- literalmente, la misma comparación que hace hoy la guarda de posesión de H2
-- (`ev.FlowID != st.FlowID`, event_lifecycle.go:186) y que este plan retira porque
-- `owner_event_id` la convierte en un hecho escrito.
-- El `menu` es reconocible por `conversation_events.kind = 'menu'`
-- (trigger.EventKindMenu, trigger/trigger.go:65; vocabulario de fábrica completo:
-- menu | cart | survey | media, `FactoryEventKinds()` en :83-85). `kind` NO tiene
-- CHECK en BD a propósito (0051, COMMENT de la columna): el vocabulario lo fija el
-- Registry, no el esquema.
--
-- ⚠️ DISCREPANCIA DEL `tasks.md`, RESUELTA AQUÍ. El criterio de T1.3 escribe
-- `SELECT id, flow_id, event_id FROM flow_state ...`. **`flow_state` NO TIENE
-- columna `id`**: su PK es `(tenant_id, session_id, contact_id)` (0005_contacts.sql:
-- 87-97; la tabla se re-claveó en el Plan 010). Esa consulta, literal, falla con
-- «column "id" does not exist». Aquí va escrita con la PK REAL — es la misma
-- consulta, con la identidad que la tabla de verdad tiene.
--
-- CERO PII: solo identificadores técnicos. `contact_id` es OPACO (ADR-0010/ADR-0017),
-- nunca un número ni un JID.
-- ============================================================


-- ------------------------------------------------------------
-- (a) DIAGNÓSTICO — la foto ANTES de tocar nada, con la clasificación al lado.
--     Se corre siempre primero y su salida se pega en docs/journal/<fecha>.md.
-- ------------------------------------------------------------
SELECT fs.tenant_id,
       fs.session_id,
       fs.contact_id,
       fs.flow_id,
       fs.event_id,
       fs.owner_event_id,
       ev.kind        AS kind_activo,
       ev.status      AS status_activo,
       ev.flow_id     AS flow_id_del_activo,
       ev.history_id  AS history_id_activo,
       CASE
           WHEN fs.flow_id = ''                       THEN '1 · sin_flujo -> NULL'
           WHEN fs.event_id IS NULL                   THEN '4 · sin_evento_activo -> NULL (no en D-053.7)'
           WHEN ev.id IS NULL                         THEN '4b · event_id HUERFANO -> revisar (no hay FK en event_id)'
           WHEN ev.kind = 'menu'                      THEN '3 · menu_sobre_heredado -> A MANO'
           WHEN ev.flow_id = fs.flow_id               THEN '2 · dueno_evidente -> owner = event_id'
           ELSE                                            '5 · divergente_no_menu -> PARAR y revisar (no en D-053.7)'
       END AS categoria
  FROM public.flow_state fs
  LEFT JOIN public.conversation_events ev ON ev.id = fs.event_id
 ORDER BY categoria, fs.tenant_id, fs.session_id, fs.contact_id;


-- ------------------------------------------------------------
-- (b) LOS UPDATE AUTOMÁTICOS — categorías 1 y 2, y SOLO esas.
--     Los dos son idempotentes: la guarda del WHERE los deja en 0 filas si el valor
--     ya es el correcto, así que correrlos dos veces no cambia nada.
--     La categoría 2 es además NO DESTRUCTIVA: solo RELLENA HUECOS
--     (`owner_event_id IS NULL`) y NUNCA reescribe un dueño ya existente. Importa
--     porque este fichero se anuncia como repetible PARA SIEMPRE: en cuanto T1.5 está
--     desplegado, el dueño lo escribe PRODUCCIÓN, y una re-ejecución tardía sobre una
--     fila cuyo dueño legítimo discrepa del activo (el `menu` sobre heredado, o
--     cualquier divergencia futura) lo pisaría. Con la guarda de NULL, correrlo mil
--     veces sobre una base ya migrada es exactamente 0 filas.
--     Ninguno toca las filas de la categoría 3 (el predicado las excluye por
--     `ev.kind = 'menu'`), así que una resolución MANUAL previa NUNCA se pisa.
--     🔴 NINGUNO ESCRIBE EN `event_id`: cero migración de datos sobre esa columna
--     (INV-053.3).
-- ------------------------------------------------------------

-- Categoría 1 — sin flujo en curso ⇒ no hay nada que poseer.
-- Normalmente 0 filas (la columna nace NULL); existe para que el fichero sea capaz
-- de REPARAR el invariante `owner NOT NULL ⟹ flow_id <> ''` si algo lo violó.
UPDATE public.flow_state fs
   SET owner_event_id = NULL
 WHERE fs.flow_id = ''
   AND fs.owner_event_id IS NOT NULL;   -- guarda de idempotencia

-- Categoría 2 — el evento activo ES el que abrió el flujo que corre en la fila.
-- El caso común, sin `menu` de por medio.
UPDATE public.flow_state fs
   SET owner_event_id = fs.event_id
  FROM public.conversation_events ev
 WHERE ev.id       = fs.event_id
   AND fs.flow_id <> ''
   AND ev.kind    <> 'menu'             -- la categoría 3 se resuelve a mano, no aquí
   AND ev.flow_id  = fs.flow_id         -- «el evento es el dueño de ESE flujo»
   AND fs.owner_event_id IS NULL;       -- SOLO rellena huecos: nunca reescribe un dueño ya puesto


-- ------------------------------------------------------------
-- (c) VERIFICACIÓN DEL CRITERIO de T1.3, con la PK REAL de flow_state.
--     Debe devolver ÚNICAMENTE filas `menu`-sobre-heredado (kind_activo = 'menu'),
--     que son las que quedan pendientes de resolver a mano en el paso (d).
--     Si aparece cualquier otra cosa:
--       · kind_activo IS NULL con event_id IS NULL -> categoría 4: legítimo, se
--         queda NULL. Anotarlo en el journal y seguir.
--       · kind_activo IS NULL con event_id NOT NULL -> puntero HUÉRFANO: `event_id`
--         no tiene FK (0052:78-84), así que esto es posible. Investigar antes de
--         desplegar T1.6.
--       · cualquier otro kind -> categoría 5: PARAR. La divergencia ocurre por una
--         vía que el diseño no modeló.
--     Tras la resolución manual del paso (d), esta consulta debe quedar en 0 filas
--     salvo las categorías 4 documentadas.
-- ------------------------------------------------------------
SELECT fs.tenant_id,
       fs.session_id,
       fs.contact_id,          -- ⬅ la PK real; el `tasks.md` decía `id`, que NO existe
       fs.flow_id,
       fs.event_id,
       ev.kind   AS kind_activo,
       ev.status AS status_activo
  FROM public.flow_state fs
  LEFT JOIN public.conversation_events ev ON ev.id = fs.event_id
 WHERE fs.flow_id <> ''
   AND fs.owner_event_id IS NULL
 ORDER BY fs.tenant_id, fs.session_id, fs.contact_id;


-- ------------------------------------------------------------
-- (d) RESOLUCIÓN A MANO de la categoría 3 (`menu` sobre flujo heredado).
--     Regla de D-053.7: buscar en conversation_events qué evento del MISMO
--     tenant/sesión/contacto tiene ese `flow_id` como su flow_id de NACIMIENTO y
--     sigue `open`. Con el volumen actual (orden de magnitud del 043: 4 eventos,
--     2 solicitudes) es una consulta y una revisión, no un script de riesgo.
-- ------------------------------------------------------------

-- d.1 — Los candidatos, uno por fila pendiente.
--       `candidatos` dice cuántos hay: 1 = decisión mecánica; 0 = el dueño ya se
--       cerró (ver d.2); >1 = ambiguo, hay que mirar `created_at`/`last_activity_at`
--       y decidir con criterio, dejándolo escrito en el journal.
SELECT fs.tenant_id,
       fs.session_id,
       fs.contact_id,
       fs.flow_id                AS flow_id_de_la_fila,
       fs.event_id               AS activo_es_el_menu,
       cand.id                   AS candidato_dueno,
       cand.kind                 AS kind_candidato,
       cand.history_id           AS history_id_candidato,
       cand.status               AS status_candidato,
       cand.created_at           AS nacimiento_candidato,
       cand.last_activity_at     AS ultima_actividad_candidato,
       COUNT(cand.id) OVER (PARTITION BY fs.tenant_id, fs.session_id, fs.contact_id) AS candidatos
  FROM public.flow_state fs
  JOIN public.conversation_events act
    ON act.id   = fs.event_id
   AND act.kind = 'menu'
  LEFT JOIN public.conversation_events cand
    ON  cand.tenant_id  = fs.tenant_id
    AND cand.session_id = fs.session_id
    AND cand.contact_id = fs.contact_id
    AND cand.flow_id    = fs.flow_id     -- su flow_id de NACIMIENTO
    AND cand.status     = 'open'         -- «y sigue open» (D-053.7)
    AND cand.id        <> fs.event_id    -- el propio menú no es candidato a dueño
 WHERE fs.flow_id <> ''
   AND fs.owner_event_id IS NULL
 ORDER BY fs.tenant_id, fs.session_id, fs.contact_id, cand.last_activity_at DESC;

-- d.2 — Red de seguridad: los MISMOS candidatos SIN el filtro `status = 'open'`.
--       Si d.1 devuelve `candidatos = 0` para una fila, es probable que el dueño ya
--       esté `closed`/`cancelled`. Ese caso NO se backfillea a ciegas: un dueño
--       muerto con la fila viva es exactamente el gap que la Ola 6 dejó consciente
--       (el `cart` heredado que no se cerraba nunca) y merece decisión explícita.
SELECT fs.tenant_id, fs.session_id, fs.contact_id, fs.flow_id,
       cand.id, cand.kind, cand.history_id, cand.status, cand.closed_at
  FROM public.flow_state fs
  JOIN public.conversation_events act
    ON act.id = fs.event_id AND act.kind = 'menu'
  JOIN public.conversation_events cand
    ON  cand.tenant_id  = fs.tenant_id
    AND cand.session_id = fs.session_id
    AND cand.contact_id = fs.contact_id
    AND cand.flow_id    = fs.flow_id
    AND cand.id        <> fs.event_id
 WHERE fs.flow_id <> ''
   AND fs.owner_event_id IS NULL
 ORDER BY fs.tenant_id, fs.session_id, fs.contact_id, cand.created_at DESC;

-- d.3 — PLANTILLA de la escritura manual. UNA por fila, con la PK COMPLETA y el
--       UUID del dueño ESCRITO A MANO tras mirar d.1/d.2. Idempotente por el
--       `IS DISTINCT FROM`. NO se generaliza a un UPDATE masivo A PROPÓSITO: la
--       elección del dueño es un juicio, y automatizarlo es justo lo que D-053.7
--       prohíbe.
--
--       📌 Cada ejecución se documenta INDIVIDUALMENTE en docs/journal/<fecha>.md:
--          PK de la fila, su `flow_id`, el evento elegido (id + history_id) y POR QUÉ.
--
-- UPDATE public.flow_state
--    SET owner_event_id = '<UUID-DEL-EVENTO-DUENO>'::uuid
--  WHERE tenant_id  = '<UUID-TENANT>'::uuid
--    AND session_id = '<SESSION-ID>'
--    AND contact_id = '<UUID-CONTACT>'::uuid
--    AND flow_id   <> ''                                  -- nunca sobre una fila sin flujo
--    AND owner_event_id IS DISTINCT FROM '<UUID-DEL-EVENTO-DUENO>'::uuid;

-- d.4 — Cierre: volver a correr (c). Debe quedar en 0 filas salvo las de categoría 4
--       ya documentadas. Y volver a correr (b) + (c) otra vez justo antes de
--       desplegar T1.5 + T1.6 juntos.
