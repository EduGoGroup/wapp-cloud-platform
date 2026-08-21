-- ============================================================
-- 0063: fleet_sessions.profile — el PERFIL DE NEGOCIO de la sesión
-- (Plan 046 · Ola 1 · T1.1 — D-046.1 y D-046.6; cierra REQ-01 y REQ-02).
--
-- La 0025 (Plan 020 · T1) trajo `fleet_sessions.role` con el par bot|passive. Este
-- plan renombra el EJE, no el valor: lo que el dueño de una instalación configura
-- no es un «rol» sino el PERFIL de la sesión —activa | pasiva—, y la palabra «rol»
-- ya está tomada por otra cosa en el ecosistema.
--
-- ------------------------------------------------------------
-- DESLINDE OBLIGATORIO: ESTO NO ES devices.role DEL EDGE
-- ------------------------------------------------------------
-- `devices.role` del Edge (primary|standby, ADR-0018) es OTRO dominio, otro repo y
-- otra pregunta: cuál de los dispositivos vinculados manda. `fleet_sessions.profile`
-- responde a «esta sesión contesta sola o solo emite». No se mezclan, no se derivan
-- una de otra y ningún código del Edge renombra su `role` por esta migración
-- (D-046.6).
--
-- ------------------------------------------------------------
-- POR QUÉ COLUMNA NUEVA Y NO UN RENAME (D-046.1)
-- ------------------------------------------------------------
-- Un `ALTER ... RENAME COLUMN` en caliente rompe a cualquier lector todavía no
-- actualizado dentro del MISMO despliegue. Por eso nace `profile` al lado y `role`
-- SE CONSERVA un ciclo como alias deprecado: la ESCRITURA mantiene las dos
-- sincronizadas (fleet.SetRole escribe ambas) y la LECTURA de negocio pasa a
-- `profile` y solo a `profile`. El `DROP` de `role` es de un plan futuro, no de
-- aquí.
--
-- Mapeo, fijado y sin excepciones:  role='bot' -> profile='active'
--                                   role='passive' -> profile='passive'
--
-- ------------------------------------------------------------
-- 🔴 LAS TRES REGLAS DEL PATRÓN FULL-REPLAY, QUE AQUÍ NO SE RELAJAN
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY (migrate.go): re-aplica TODO el directorio en
-- cuanto cambia el hash del conjunto. Las tres reglas siguientes NO son estilo:
--
-- (1) LA COLUMNA NACE SIN DEFAULT. Si el `ADD COLUMN` la creara ya con
--     `DEFAULT 'passive'`, Postgres poblaría de 'passive' TODAS las filas
--     existentes ANTES de que el backfill pudiera mirar el `role` — y eso volcaría
--     a pasiva todas las sesiones vivas del cliente, que es exactamente lo que
--     REQ-15 (no-regresión de lo que ya funciona) prohíbe. El default se pone
--     DESPUÉS del backfill, y por eso solo alcanza a las filas FUTURAS.
--
-- (2) EL BACKFILL LLEVA `WHERE profile IS NULL`. Sin ese guard, el replay del
--     arranque siguiente re-ejecutaría el UPDATE y VOLTEARÍA el perfil de toda
--     sesión creada o reconfigurada después del primer apply: un dueño que puso
--     una sesión en activa la vería volver a pasiva sola en el próximo reinicio.
--     Con el guard, el replay es un no-op exacto.
--
-- (3) EL `SET NOT NULL` VA DENTRO DEL `DO $$ ... IF is_nullable = 'YES'`, porque
--     `ALTER ... SET NOT NULL` no es idempotente por sí solo.
--
-- (4) 🔧 EL CHECK LLEVA NOMBRE EXPLÍCITO Y SE RECREA (DROP IF EXISTS + ADD), como
--     el de la 0025. Es la única desviación del SQL literal de design.md, y el
--     porqué está detallado justo encima del bloque, más abajo.
--
-- El test que ejerce (1)+(2)+(4) contra Postgres real:
-- TestIntegration_Migracion0063Profile_ReplayNoPisaElPerfil
-- (internal/platform/storage/postgres/profile_replay_integration_test.go).
--
-- ------------------------------------------------------------
-- EL DEFAULT 'passive' Y SU ALCANCE REAL
-- ------------------------------------------------------------
-- `DEFAULT 'passive'` (Decidido, doc 14 · D-07) gobierna SOLO las filas nuevas: una
-- sesión que se empareja a partir de aquí nace PASIVA y no auto-responde hasta que
-- su dueño la active. Es privacidad por defecto, y es un cambio de comportamiento
-- DELIBERADO respecto de la 0025 (que traía DEFAULT 'bot'). Las filas que ya
-- existían conservan su semántica exacta vía el backfill de arriba.
--
-- CERO PII: un enum de dos valores sobre una tabla de negocio ya existente
-- (ADR-0009). ADITIVA e IDEMPOTENTE. SchemaVersion sube a 0.37.0.
-- ============================================================

ALTER TABLE public.fleet_sessions
  ADD COLUMN IF NOT EXISTS profile TEXT;

-- 🔧 CORRECCIÓN (code review 2026-08-20) — ÚNICA desviación del SQL literal de
-- design.md:171-193, y solo en la FORMA del CHECK; el backfill, el default y el
-- NOT NULL se copian verbatim.
--
-- El literal creaba el CHECK INLINE dentro del ADD COLUMN, sin nombre. Bajo este
-- runner FULL-REPLAY eso deja un agujero: en el segundo apply el
-- `ADD COLUMN IF NOT EXISTS` no hace nada, y con él se salta también el CHECK — que
-- por tanto NO se recrea nunca más. Si alguien lo dropea a mano en producción (o lo
-- pierde un restore parcial), el replay ya no lo repone y la columna se queda sin
-- guarda de dominio para siempre.
-- La convención de la casa para esto es la de su vecina 0025:22-25 —el `role` de
-- esta MISMA tabla— : constraint con NOMBRE EXPLÍCITO + `DROP ... IF EXISTS` antes
-- del `ADD`, de modo que cada replay lo RESTAURA. Se aplica aquí igual, con el
-- nombre `fleet_sessions_profile_chk` (mismo sufijo _chk que la 0025).
-- El DROP del nombre autogenerado (`fleet_sessions_profile_check`) está por si
-- alguna BD alcanzó a aplicar la versión inline de esta migración antes de esta
-- corrección: sin él quedarían DOS CHECK equivalentes sobre la columna.
--
-- Va ANTES del backfill sin riesgo: un CHECK no se viola con NULL (evalúa a
-- UNKNOWN), así que las filas recién ampliadas lo satisfacen mientras esperan al
-- UPDATE de abajo.
ALTER TABLE public.fleet_sessions
    DROP CONSTRAINT IF EXISTS fleet_sessions_profile_check;
ALTER TABLE public.fleet_sessions
    DROP CONSTRAINT IF EXISTS fleet_sessions_profile_chk;
ALTER TABLE public.fleet_sessions
    ADD CONSTRAINT fleet_sessions_profile_chk CHECK (profile IN ('active', 'passive'));
-- v1; crecer el dominio = migración aditiva que re-crea este mismo CHECK.

-- backfill (guard idempotente): solo filas aún no pobladas conservan su semántica actual
UPDATE public.fleet_sessions SET profile = CASE role WHEN 'bot' THEN 'active' ELSE 'passive' END
  WHERE profile IS NULL;

-- default 'passive' (Decidido, doc 14 D-07) + NOT NULL, con guard idempotente
ALTER TABLE public.fleet_sessions ALTER COLUMN profile SET DEFAULT 'passive';
DO $$ BEGIN
  -- 🔧 Corrección al literal de design.md:184-186: el original consulta
  -- information_schema.columns SIN filtrar por table_schema. Es inocuo hoy, pero si
  -- algún día existiera otra fleet_sessions.profile nullable en otro esquema, el IF
  -- daría un falso positivo. Se filtra por 'public', coherente con el resto del fichero.
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = 'public' AND table_name = 'fleet_sessions'
               AND column_name = 'profile' AND is_nullable = 'YES') THEN
    ALTER TABLE public.fleet_sessions ALTER COLUMN profile SET NOT NULL;
  END IF;
END $$;

COMMENT ON COLUMN public.fleet_sessions.profile IS
  'Perfil de negocio: active|passive. passive = solo emision; sus entrantes se filtran en el Edge (ADR-0027). SIN relacion con devices.role del Edge (primary/standby, ADR-0018). Sustituye a role (bot|passive), que se conserva un ciclo como alias deprecado y sincronizado en escritura: role=bot equivale a profile=active. DEFAULT passive alcanza SOLO a las filas nuevas -- las que ya existian conservan su semantica por el backfill de esta misma migracion (REQ-15). Plan 046 · T1.1.';
-- role se conserva un ciclo como alias deprecado (DROP en plan futuro)
