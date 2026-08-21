-- ============================================================
-- 0064: RETIRO de fleet_sessions.role — muere el eje legado bot|passive
-- (Plan 046 · Ola 1, D-046.1 REVISADA el 2026-08-21).
--
-- POR QUÉ AHORA, y no «en un plan futuro» como decía la 0063
-- ------------------------------------------------------------
-- La 0063 creó `profile` al lado de `role` y prometió conservar `role` «un ciclo
-- como alias deprecado», con el razonamiento estándar: no romper a los clientes
-- que no se despliegan a la vez que la plataforma. El razonamiento era correcto;
-- la premisa, no. Al comprobarla contra el código —los seis repos del ecosistema—
-- resultó que ESE CLIENTE NO EXISTE:
--
--   · el BFF llama a /profile y NO conserva la ruta vieja ni en su propia consola;
--   · platform-console, cloudlink, edge-agent y edge-intent: cero referencias;
--   · el proto de CloudLink no transporta `role` en absoluto — el Edge nunca lo vio;
--   · el único sitio que mencionaba /role era la plataforma, publicándola para que
--     alguien la deprecara.
--
-- Un ciclo de deprecación que no protege a nadie no es prudencia: es código que hay
-- que mantener, tests que hay que correr y decisiones que hay que tomar (la
-- micro-duda MD-046.2 existía SOLO por esta columna). Se retira. Estamos en alpha y
-- no hay ningún consumidor externo de la API pública.
--
-- 🔴 LO QUE ESTO IMPLICA Y HAY QUE SABER ANTES DE DESPLEGAR
-- ------------------------------------------------------------
-- Rollback: el binario ANTERIOR a este despliegue lee `COALESCE(role,'bot')` en
-- selectSessionCols. Con esta migración aplicada, volver a ese binario ROMPE la
-- lectura de sesiones. El código y esta migración se despliegan JUNTOS y el rollback
-- deja de ser una opción a partir de aquí: si hay que volver atrás, se vuelve al
-- binario Y se restaura la columna (la 0025 la recrea sola en el replay).
--
-- 🔴 EL REPLAY, QUE ES LO NO OBVIO
-- ------------------------------------------------------------
-- El runner es hash-based FULL-REPLAY: re-aplica TODO el directorio, en orden, en
-- cuanto cambia el hash del conjunto. Eso significa que en cada arranque ocurre:
--
--   0025 → `ADD COLUMN IF NOT EXISTS role ... NOT NULL DEFAULT 'bot'`  (la RECREA)
--   0063 → backfill de profile `WHERE profile IS NULL`                 (no-op: NOT NULL)
--   0064 → este DROP                                                   (la vuelve a borrar)
--
-- Es correcto y converge, y NO se toca ni la 0025 ni la 0063: las migraciones
-- aplicadas no se reescriben (mismo patrón que la 0038 con el IAM propio y la 0054).
-- El coste es de CATÁLOGO, no de datos: desde PostgreSQL 11 un `ADD COLUMN` con
-- DEFAULT no reescribe la tabla, y `DROP COLUMN` solo marca el atributo. Un ciclo
-- crear/borrar por arranque sobre una tabla de flota (decenas de filas) es ruido.
--
-- ⚠️ Y por eso el orden importa: la 0063 LEE `role` en su CASE. Si algún día se
-- renumerara esta migración por debajo de la 0063, el replay fallaría con
-- «column role does not exist» y **el arranque de la plataforma entera se cae**.
-- Esta migración tiene que ir SIEMPRE después de la 0063.
--
-- CERO PII. ADITIVA en el sentido del runner (idempotente por IF EXISTS).
-- SchemaVersion sube a 0.38.0.
-- ============================================================

-- El CHECK cuelga de la columna y cae con ella; el DROP explícito es por si alguna
-- BD quedó con la constraint y sin la columna (estado imposible hoy, barato de cubrir).
ALTER TABLE public.fleet_sessions
    DROP CONSTRAINT IF EXISTS fleet_sessions_role_chk;

ALTER TABLE public.fleet_sessions
    DROP COLUMN IF EXISTS role;

-- El COMMENT de `profile` (0063) ya dice que sustituye a role. Se reescribe aquí
-- para que deje de hablar de una columna que no existe: un comentario que nombra
-- algo retirado es una pista falsa para quien inspeccione el esquema.
COMMENT ON COLUMN public.fleet_sessions.profile IS
  'Perfil de negocio de la sesion: active|passive. active = ejecuta el motor de flujos (dispara triggers y auto-responde); passive = solo emision, y sus entrantes se filtran en el Edge (ADR-0027). DEFAULT passive: una sesion recien emparejada NO auto-responde hasta que su dueno la active (Plan 046 D-07). SIN relacion con devices.role del Edge (primary/standby, ADR-0018), que es otro dominio y otro repo. Sustituyo a la columna role (bot|passive), retirada en la 0064.';
