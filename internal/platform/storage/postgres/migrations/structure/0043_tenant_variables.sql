-- ============================================================
-- 0043: Variables de empresa por-tenant (Plan 041 · T2.1, design.md §D-041.1).
-- Tabla NUEVA tenant_variables: pares clave→valor que define el TENANT y que
-- wApp **NO INTERPRETA**. No hay lista blanca de claves, no hay tipo de valor, no
-- hay semántica: si el tenant quiere enseñar un símbolo de moneda en sus menús,
-- eso es presentación desde SU variable (p. ej. `moneda` = `Bs`). Por eso NO
-- existe —ni aquí ni en catálogo, API o contratos— ningún campo `currency`: un
-- monto es un monto.
--
-- Ambas columnas son TEXT a propósito: tipar el valor sería empezar a
-- interpretarlo. La clave tampoco lleva CHECK de formato — la única regla es la
-- de la PK (una clave, un valor, por tenant).
--
-- Es CAPA TÉCNICA, no una capacidad comercial: las rutas
-- GET/PUT /api/v1/tenant-variables llevan scope (content.read/content.write) pero
-- NO gate de feature. El Plan 042 congela estas variables en el snapshot de cada
-- `intake.push` (D-18); el Motor de Flujos y el módulo cart NO las leen.
--
-- tenant_id es TEXT SIN FK, igual que en su tabla hermana public.tenant_content
-- (0010): el tenant llega del token (INV-8), no de un JOIN.
--
-- CERO PII / CERO llaves: aquí vive configuración de presentación del tenant,
-- nunca número/JID en claro ni material criptográfico. Quien escriba una
-- credencial en una variable la está guardando EN CLARO: no es el sitio.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY (re-aplica TODOS los
-- structure/*.sql al cambiar el hash de cualquiera); CREATE TABLE IF NOT EXISTS
-- garantiza re-aplicación N veces sin daño ni pérdida de filas. NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_variables (
    tenant_id  TEXT        NOT NULL,
    key        TEXT        NOT NULL,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key)
);

COMMENT ON TABLE  public.tenant_variables IS 'Variables de empresa por-tenant (clave→valor de TEXTO) que wApp NO interpreta (Plan 041, D-041.1): sin lista blanca de claves, sin tipos, sin moneda tipada. Capa técnica: CRUD + pantalla; el Motor NO las lee; el Plan 042 las snapshotea en intake.push. NUNCA PII ni llaves.';
COMMENT ON COLUMN public.tenant_variables.tenant_id  IS 'Tenant dueño de la variable (TEXT sin FK, como en tenant_content). Sale del token (INV-8), nunca del cuerpo.';
COMMENT ON COLUMN public.tenant_variables.key        IS 'Clave OPACA elegida por el tenant (p. ej. ''moneda''). wApp no la interpreta ni la valida contra catálogo alguno.';
COMMENT ON COLUMN public.tenant_variables.value      IS 'Valor OPACO en TEXTO (p. ej. ''Bs''). Se guarda y se devuelve VERBATIM: acentos, espacios o un JSON dentro de la cadena viajan tal cual.';
COMMENT ON COLUMN public.tenant_variables.updated_at IS 'Momento del último CAMBIO de valor (un PUT que reescribe el mismo valor NO lo mueve). Usa el DEFAULT now() en el alta.';
