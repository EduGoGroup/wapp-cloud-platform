-- ============================================================
-- 0044: Versiones del contenido por-tenant (Plan 041 · T3.3, design.md §D-041.8).
-- Tabla NUEVA tenant_content_versions: el ARCHIVO de lo que había en
-- public.tenant_content ANTES de cada import de catálogo aplicado. El motor sigue
-- leyendo SOLO de tenant_content (el puerto ContentSource no se toca): esta tabla
-- no participa en ninguna lectura de runtime, existe para poder mirar atrás.
--
-- QUÉ GUARDA CADA FILA: el blob VIEJO, el que el import desplazó. Por eso la
-- versión N no es "lo que hay ahora" sino "lo que había". Un tenant con dos
-- imports aplicados tiene dos filas y un tenant_content: tres estados, y el
-- vigente NO está duplicado aquí.
--
-- LA VERSIÓN 1 NACE DEL SEGUNDO ACTO, NO DEL PRIMERO. Sin blob vigente no hay
-- nada que archivar, así que el PRIMER import sobre una ref vacía escribe
-- tenant_content y NO crea fila. Numerar «1» un catálogo inexistente haría que
-- `version = 1` significara dos cosas distintas —un catálogo real anterior o la
-- nada— y quien quisiera restaurar no podría saber cuál le toca. Esto resuelve
-- el hueco #9 del mapeo de la ola: «no hay versiones» y «no hay blob vigente»
-- son casos distintos y aquí solo el segundo suprime la fila.
--
-- source ES LA PROCEDENCIA DEL ACTO QUE ARCHIVÓ, NO LA DEL BLOB ARCHIVADO. La del
-- blob viejo es indeterminable (nadie la registró cuando se escribió), así que la
-- columna responde a «qué operación desplazó esto»: import_json (T3.3),
-- import_tabular (T3.4) y manual, reservado para cuando el PUT genérico de
-- /api/v1/tenant-content también versione (follow-up declarado en D-041.8: hoy NO
-- versiona). El CHECK mantiene el conjunto cerrado: añadir un origen exige una
-- migración, que es exactamente la fricción que se quiere.
--
-- content es JSONB por coherencia con tenant_content (misma clase de dato, mismo
-- almacenamiento). tenant_id es TEXT SIN FK, igual que en tenant_content (0010) y
-- tenant_variables (0043): el tenant llega del token (INV-8), no de un JOIN.
--
-- CERO PII: aquí vive el catálogo del negocio (skus, nombres de producto,
-- precios), jamás un número de teléfono, un JID ni material criptográfico.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera); CREATE TABLE IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño ni pérdida de filas. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_content_versions (
    tenant_id  TEXT        NOT NULL,
    ref        TEXT        NOT NULL,
    version    INTEGER     NOT NULL,
    content    JSONB       NOT NULL,
    source     TEXT        NOT NULL CHECK (source IN ('import_json', 'import_tabular', 'manual')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, ref, version)
);

COMMENT ON TABLE  public.tenant_content_versions IS 'Archivo de blobs de public.tenant_content DESPLAZADOS por un import (Plan 041, D-041.8). Cada fila es el contenido VIEJO, no el vigente. El Motor NUNCA lee de aquí. La versión 1 nace del SEGUNDO acto sobre una ref: sin blob vigente no hay nada que archivar. CERO PII.';
COMMENT ON COLUMN public.tenant_content_versions.tenant_id  IS 'Tenant dueño del contenido (TEXT sin FK, como en tenant_content). Sale del token (INV-8), nunca del cuerpo.';
COMMENT ON COLUMN public.tenant_content_versions.ref        IS 'Ref lógica del contenido archivado, la misma de public.tenant_content (p. ej. ''catalogo'').';
COMMENT ON COLUMN public.tenant_content_versions.version    IS 'Correlativo por (tenant_id, ref), 1..N, en orden de archivado. Se calcula MAX+1 DENTRO de la transacción del apply, con la fila de tenant_content bloqueada FOR UPDATE.';
COMMENT ON COLUMN public.tenant_content_versions.content    IS 'El blob VIEJO tal cual estaba en tenant_content antes del import que creó esta fila. No es el blob nuevo.';
COMMENT ON COLUMN public.tenant_content_versions.source     IS 'Procedencia del ACTO que archivó esta versión (import_json | import_tabular | manual), NO del blob archivado: la de este último es indeterminable.';
COMMENT ON COLUMN public.tenant_content_versions.created_at IS 'Momento del archivado (= momento del import aplicado). Usa el DEFAULT now().';
