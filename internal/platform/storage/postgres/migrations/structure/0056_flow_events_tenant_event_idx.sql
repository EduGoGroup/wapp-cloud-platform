-- ============================================================
-- 0056: índice PARCIAL para GET /api/v1/events/telemetry (Plan 043 · Ola 6 ·
-- T6.5, MD-043.17). El diseño original de T6.5 fue REFUTADO con medición
-- contra PostgreSQL 16 real (200.500 filas, barrido del 2026-08-10/11): esta
-- migración es la enmienda #2 de las cuatro que la refutación exigió.
--
-- POR QUÉ flow_events_scan_idx (0009: tenant_id, flow_id, name, created_at) NO
-- SIRVE a esta ruta: la consulta no fija flow_id (no tiene sentido para una
-- ruta que lee TODOS los flujos del tenant, no uno), así que el hueco corta el
-- prefijo del índice justo en su primera columna útil. Medido: 105 buffers
-- CONSTANTES tanto para una ventana de 1 día como de 90 (el índice entra por
-- casualidad, filtrando solo por tenant_id, no por lo que de verdad acota la
-- consulta) y, para el tenant más ruidoso, el planificador lo DESCARTA del
-- todo y hace Parallel Seq Scan: 4248 buffers, 34 MB, para devolver 385 filas.
--
-- POR QUÉ ESTE ÍNDICE Y NO OTRO: (tenant_id, created_at, id) PARCIAL —WHERE
-- name LIKE 'event\_%'— mide 16,9× menos buffers y 41,6× menos tiempo que
-- flow_events_scan_idx, y ocupa 480 kB frente a 12 MB. Se descarta la
-- alternativa con `text_pattern_ops` (que aceleraría el LIKE por sí solo)
-- porque el parcial es el ÚNICO que ADEMÁS entrega el ORDEN que la paginación
-- por keyset necesita: si `name` entrara en la CLAVE del índice, `name LIKE
-- 'event\_%'` sería una condición de RANGO sobre esa columna y el índice ya no
-- podría entregar `created_at` ordenado sin un sort aparte — exactamente lo
-- que la enmienda #3 (ORDER BY created_at, id + keyset) necesita gratis.
--
-- ⚠️ FRAGILIDAD DEL ÍNDICE PARCIAL — léelo ANTES de tocar la consulta que lo
-- usa (internal/publicapi/eventstelemetry_store.go): la aplicabilidad de un
-- índice parcial exige que el WHERE de la consulta implique LITERALMENTE el
-- predicado de este índice, carácter a carácter, incluido el escape del guion
-- bajo. Si alguien "simplifica" la consulta a `name LIKE 'event_%'` (sin
-- escapar) o a `starts_with(name, 'event_')`, el índice deja de aplicar EN
-- SILENCIO: Postgres no avisa, simplemente vuelve al plan de antes de esta
-- migración (Seq Scan o flow_events_scan_idx mal aprovechado). La única
-- defensa real contra esa degradación silenciosa es el test de integración que
-- corre EXPLAIN y afirma que el plan usa flow_events_tenant_event_idx (ver
-- internal/publicapi/eventstelemetry_integration_test.go,
-- TestListEventTelemetry_UsaElIndiceParcial).
--
-- Esta migración BUMPEA SchemaVersion (migrations/version.go): 0.31.0 → 0.32.0.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS
-- los structure/*.sql al cambiar el hash de cualquiera); CREATE INDEX IF NOT
-- EXISTS garantiza re-aplicación N veces sin daño. SIN CONCURRENTLY, igual que
-- el resto de índices de este árbol (0009, 0051): el runner ejecuta el
-- contenido de cada archivo como un único mensaje al driver, y CONCURRENTLY no
-- admite la transacción implícita que eso arma alrededor de un statement.
--
-- CERO PII: mismo criterio que flow_events (0009) — el índice solo ordena
-- columnas ya existentes (tenant_id opaco, created_at, id técnico); no añade
-- ninguna columna nueva ni toca contact_id.
-- ============================================================

CREATE INDEX IF NOT EXISTS flow_events_tenant_event_idx
    ON public.flow_events (tenant_id, created_at, id)
    WHERE name LIKE 'event\_%';

COMMENT ON INDEX public.flow_events_tenant_event_idx IS
  'Índice PARCIAL para GET /api/v1/events/telemetry (Plan 043 · T6.5): 16,9x menos buffers y 41,6x menos tiempo que flow_events_scan_idx (0009), 480 kB frente a 12 MB, medido con 200.500 filas (refutación 2026-08-10/11). Su predicado (name LIKE ''event\_%'' con el guion bajo ESCAPADO) tiene que coincidir LITERALMENTE con el WHERE de toda consulta que quiera usarlo: cambiarlo (sin escapar, o a starts_with(...)) lo deja fuera del plan SIN NINGÚN aviso de Postgres. Ver internal/publicapi/eventstelemetry_store.go y el test EXPLAIN que lo defiende.';
