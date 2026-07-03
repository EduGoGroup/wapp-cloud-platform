-- bootstrap-shared-db.sql — provisiona la base de wApp DENTRO del Postgres COMPARTIDO de EduGo.
--
-- Decisión (2026-07-03): wApp NO levanta su propio contenedor Postgres. Reutiliza la
-- instancia de EduGo (contenedor `edugo-postgres`, localhost:5432) con su PROPIA base y
-- rol. Un solo contenedor para los dos ecosistemas → menos RAM, cero puertos duplicados.
--
-- Ejecutar UNA vez como superusuario de esa instancia (ajusta -U al POSTGRES_USER de EduGo):
--     docker exec -i edugo-postgres psql -U edugo -d postgres < deploy/bootstrap-shared-db.sql
--
-- Credenciales de DESARROLLO (coinciden con .env.example y los defaults de
-- internal/platform/config). NO usar en producción; en la nube se usa Neon (.env.neon).

-- 1) Rol de aplicación de wApp (idempotente).
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wapp') THEN
    CREATE ROLE wapp LOGIN PASSWORD 'wapp';
  END IF;
END
$$;

-- 2) Base de datos propia de wApp (idempotente; CREATE DATABASE no admite IF NOT EXISTS).
SELECT 'CREATE DATABASE wapp_cloud OWNER wapp'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'wapp_cloud')\gexec

GRANT ALL PRIVILEGES ON DATABASE wapp_cloud TO wapp;
