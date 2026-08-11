-- ============================================================
-- 0055: intakes.event_id se vuelve NOT NULL de verdad — el legado sin padre se borra
-- (Plan 043 · decisión del dueño, Jhoan, 2026-08-10).
--
-- Esta migración ejecuta la salida "backfill + NOT NULL" que la cabecera de la 0054
-- ya dejó prevista para el legado — pero SIN backfill (no hay dato del que rellenar)
-- y CON BORRADO en su lugar.
--
-- EL PORQUÉ, medido en Neon (no supuesto, 2026-08-10): de las intakes con
-- event_id IS NULL, CERO son reparables por backfill porque
-- conversation_events.intake_id —la columna que hubiera dado el dato— NUNCA se
-- escribió en producción (0 filas con valor en toda la base; ver la cabecera de la
-- 0054). Son datos de DESARROLLO sin valor de negocio (no estamos en producción).
-- Además el CHECK ... NOT VALID de la 0054 tiene un efecto medido no deseado: valida
-- también los UPDATE de las filas legadas, así que ninguna puede cambiar de status
-- sin declarar event_id en el MISMO statement
-- (ERROR: ... violates check constraint "intakes_event_id_required_chk").
--
-- QUÉ HACE, en orden:
--   1. Borra las líneas (intake_items) de las intakes legadas. Es el ÚNICO de los
--      tres hijos de intakes que NO lleva ON DELETE CASCADE (0012/0041 — a
--      diferencia de intake_revisions e intake_buyer_data, 0045): sin este paso el
--      DELETE del paso 2 revienta con una violación de FK.
--   2. Borra las intakes legadas (event_id IS NULL). intake_revisions e
--      intake_buyer_data se van SOLAS por CASCADE.
--   3. Retira el CHECK intakes_event_id_required_chk (sustituido por NOT NULL real:
--      deja de hacer falta tolerar legado porque ya no queda ninguno).
--   4. ALTER COLUMN event_id SET NOT NULL.
--
-- IDEMPOTENCIA / REPLAY: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera). Sobre una base YA migrada por
-- esta versión: los DELETE no encuentran filas (WHERE event_id IS NULL ya está
-- vacío, y con la columna NOT NULL la condición nunca es cierta) y son no-op; DROP
-- CONSTRAINT usa IF EXISTS; SET NOT NULL sobre una columna que ya es NOT NULL no es
-- un error en Postgres. Sobre BASE LIMPIA (structure completo desde cero, sin
-- clean-slate): la 0054 crea la columna nullable con el CHECK NOT VALID en la MISMA
-- corrida en la que esta migración se ejecuta a continuación; la tabla intakes nace
-- vacía (nadie siembra filas de negocio), así que no hay nada que borrar y el SET
-- NOT NULL no tiene fila alguna que escanear.
--
-- CERO PII: solo identificadores técnicos (UUID) sobre tablas de negocio ya
-- existentes.
-- ============================================================

-- 1. Líneas de las intakes legadas (sin CASCADE, 0012/0041): hay que adelantarse
--    o el DELETE del paso 2 revienta con "violates foreign key constraint".
DELETE FROM public.intake_items
 WHERE intake_id IN (SELECT id FROM public.intakes WHERE event_id IS NULL);

-- 2. Las intakes legadas. intake_revisions e intake_buyer_data (0045) se van
--    solas: sus FK sí llevan ON DELETE CASCADE.
DELETE FROM public.intakes WHERE event_id IS NULL;

-- 3. El CHECK NOT VALID (0054) deja de hacer falta: ya no queda legado que tolerar.
ALTER TABLE public.intakes
    DROP CONSTRAINT IF EXISTS intakes_event_id_required_chk;

-- 4. NOT NULL de verdad.
ALTER TABLE public.intakes
    ALTER COLUMN event_id SET NOT NULL;

COMMENT ON COLUMN public.intakes.event_id IS
  'Evento conversacional (conversation_events.id) del que nació esta solicitud (D-043.21: el hijo declara a su padre; el padre NO tiene columnas de ningún hijo). La escribe el PROYECTOR del módulo cart al parir la fila (ensureOpenIntake, con el EventID que viaja en modules.EffectMeta). NOT NULL real desde la 0055: el legado sin ligadura (event_id NULL, filas anteriores a la 0054 sin conversation_events.intake_id que lo respaldara) se BORRÓ —decisión del dueño, 2026-08-10, medida contra Neon: 0 filas reparables por backfill, datos de desarrollo sin valor de negocio—, así que ya no hace falta tolerar NULL. ÚNICO parcial (intakes_event_id_uidx, 0054): un evento tiene a lo sumo un contenido durable (E-8); el rescate REUSA el mismo intake (REQ-26b). Sin ON DELETE a propósito: aquí nada se borra por reloj (INV-09); este borrado fue una decisión explícita de una sola vez sobre legado sin valor.';
