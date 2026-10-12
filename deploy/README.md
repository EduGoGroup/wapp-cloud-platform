# deploy/ — Postgres de desarrollo de wApp

## Decisión: contenedor Postgres COMPARTIDO con EduGo (2026-07-03)

wApp **no** levanta su propio contenedor Postgres. Reutiliza la instancia de
desarrollo de EduGo (contenedor `edugo-postgres`, `localhost:5432`) con su **propia
base y rol** (`wapp_cloud` / `wapp`). Un solo contenedor para ambos ecosistemas:
menos RAM y ningún puerto de base de datos duplicado.

> Los **puertos de la aplicación** (HTTP/gRPC) sí van en una banda propia (81xx),
> aparte de EduGo (80xx). Ver `../../../docs/CONVENCIONES.md`.

## Puesta en marcha (una sola vez)

1. Levanta la infraestructura de EduGo (incluye Postgres):

   ```bash
   cd <EduGo>/EduBack/edugo-infrastructure/docker
   docker compose up -d postgres
   ```

2. Provisiona la base y el rol de wApp dentro de esa instancia:

   ```bash
   cd <wApp>/cloud/wapp-cloud-platform
   docker exec -i edugo-postgres psql -U edugo -d postgres < deploy/bootstrap-shared-db.sql
   ```

   (Ajusta `-U edugo` al `POSTGRES_USER` real del contenedor de EduGo.)

3. Arranca la plataforma con la config de puertos y BD desde `.env`:

   ```bash
   cp .env.example .env          # ajusta si hace falta
   set -a; . ./.env; set +a
   go run ./cmd/server
   ```

## Tests contra Postgres

**No usan esta instancia**, ni ninguna otra que esté corriendo: los procesos de `test/procesos/`
levantan su propio Postgres con testcontainers (`make test-procesos`, necesita Docker). Apuntar un
test a una base viva está prohibido y lo hace fallar el gate. Ver
[`documentations/operacion.md`](../documentations/operacion.md).

## Producción / cloud

En la nube no aplica nada de esto: se usa **Neon** (`.env.neon`, base `wapp` del
proyecto de EduGo en Neon). Ver `../../../docs/runbooks/postgres-neon.md`.
