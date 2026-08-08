-- ============================================================
-- 0048: Reflejo del estado del CRM sobre public.intakes (Plan 042, design.md §D-042.6).
--
-- POR QUÉ EXISTE ESTA MIGRACIÓN (no estaba en tasks.md): el design §D-042.6 dice
-- que «el reflejo (lifecycle_status + timestamp) vive sobre intakes ... ambas las
-- crea el Plan 041», y la T4.1 manda VERIFICAR esa existencia antes de implementar.
-- Verificado el 2026-08-07 contra el esquema real: el Plan 041 NO las creó. En
-- public.intakes no existe ninguna columna lifecycle_status ni ningún timestamp de
-- sync con un CRM — la 0041 renombró orders→intakes y la 0045 añadió el ciclo de
-- vida de NEGOCIO (deposit_due_at, deposit_reminded_at, customer_note) sobre la
-- columna `status` que ya venía del Plan 016. El supuesto del design era falso, así
-- que las columnas del reflejo se crean AQUÍ. Decisión tomada en EJECUCIÓN de la
-- Ola 1; se anota para que el design §D-042.6 se corrija al cerrar el plan.
--
-- ------------------------------------------------------------
-- POR QUÉ EN COLUMNAS PROPIAS Y NO PISANDO intakes.status
-- ------------------------------------------------------------
-- Porque los dos vocabularios son DISJUNTOS. No se solapan en una sola clave:
--
--   * intakes.status  (ciclo de vida de wApp, D-041.10): open | pending_approval |
--     needs_info | confirmed | deposit_requested | deposit_paid | settled |
--     cancelled | rejected | abandoned (+ las legadas closed/expired).
--   * crm_status      (contrato wapp-crm-v1, §D-042.6): paid | preparing |
--     delivered | rejected. Cuatro estados canónicos, y punto.
--
-- Meterlos en la misma columna obligaría a wApp a DECIDIR qué significa 'preparing'
-- en su propia máquina de estados, o qué estado canónico le corresponde a
-- 'deposit_requested'. Eso es inventar semántica de CRM, que es exactamente lo que
-- prohíben INV-08 y el ADR-0032: «la adaptación vive en el puente». Cada puente
-- mapea los nombres de SU CRM a los cuatro canónicos; wApp no conoce el vocabulario
-- de ningún CRM y tampoco debe traducir en sentido contrario.
--
-- Separadas, la regla «cuando HAY CRM, el CRM manda» (ADR-0031, §D-042.6) se lee
-- sin ambigüedad: crm_status es el espejo de lo que dijo el CRM y wApp JAMÁS lo
-- pisa con lógica local; intakes.status sigue siendo la verdad de wApp para los
-- tenants SIN integración. La pantalla del 041 enseña el estado como solo-lectura
-- con origen «CRM» cuando crm_status no es NULL — y esa condición sólo se puede
-- expresar si son dos columnas.
--
-- LAS TRES SON NULLables: la inmensa mayoría de solicitudes no tiene CRM detrás.
-- NULL = «esta solicitud nunca se reflejó desde un CRM», y es el estado normal, no
-- un dato faltante.
--
-- El CHECK de crm_status admite NULL explícitamente (crm_status IS NULL OR ...) y
-- acota los CUATRO canónicos: aquí sí hay CHECK, al revés que en intakes.status,
-- porque este vocabulario es CERRADO por contrato congelado — si algún día crece,
-- crece con una v2 del contrato, y esa v2 trae su migración.
--
-- CERO PII: crm_external_ref es una referencia OPACA del sistema del cliente (un id
-- de pedido en su CRM), no un dato de persona. Si un puente mete ahí un teléfono o
-- un nombre, está incumpliendo el contrato — wApp no lo interpreta ni lo valida.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY; ADD COLUMN IF NOT
-- EXISTS + DROP/ADD CONSTRAINT IF EXISTS (patrón de 0025/0029) + CREATE INDEX IF
-- NOT EXISTS => re-aplicable N veces sin daño ni pérdida de filas. NO clean-slate.
-- ============================================================

ALTER TABLE public.intakes ADD COLUMN IF NOT EXISTS crm_status       TEXT;
ALTER TABLE public.intakes ADD COLUMN IF NOT EXISTS crm_synced_at    TIMESTAMPTZ;
ALTER TABLE public.intakes ADD COLUMN IF NOT EXISTS crm_external_ref TEXT;

-- CHECK idempotente: se recrea (DROP IF EXISTS + ADD) en cada replay del runner,
-- igual que fleet_sessions_state_chk en la 0029.
ALTER TABLE public.intakes
    DROP CONSTRAINT IF EXISTS intakes_crm_status_chk;
ALTER TABLE public.intakes
    ADD CONSTRAINT intakes_crm_status_chk
    CHECK (crm_status IS NULL OR crm_status IN ('paid','preparing','delivered','rejected'));

-- Localiza las solicitudes por antigüedad de sync dentro de un tenant: es la
-- consulta de la bandeja con CRM («qué se reflejó y cuándo») y la que descubre
-- integraciones mudas (nada sincronizado desde hace horas).
CREATE INDEX IF NOT EXISTS intakes_crm_sync_idx
    ON public.intakes (tenant_id, crm_synced_at);

COMMENT ON COLUMN public.intakes.crm_status IS
    'Estado CANÓNICO reflejado desde el CRM del cliente (contrato wapp-crm-v1, verbo intake.status): paid | preparing | delivered | rejected. NULL = esta solicitud nunca se reflejó desde un CRM (el caso normal). Columna PROPIA y NO intakes.status a propósito: los dos vocabularios son DISJUNTOS y mapear uno sobre otro obligaría a wApp a inventar semántica de CRM — justo lo que prohíben INV-08 y el ADR-0032 («la adaptación vive en el puente»). Regla ADR-0031: cuando HAY CRM, el CRM manda — wApp JAMÁS pisa este valor con lógica local.';
COMMENT ON COLUMN public.intakes.crm_synced_at IS
    'Momento del último reflejo recibido del CRM (callback autenticado por HMAC, D-042.5). NULL mientras no haya habido ninguno. Es lo que permite enseñar «actualizado hace X» y detectar integraciones mudas.';
COMMENT ON COLUMN public.intakes.crm_external_ref IS
    'Referencia OPACA del pedido en el sistema del cliente (campo external_ref del verbo intake.status). wApp la guarda VERBATIM y no la interpreta: es para que una persona pueda cruzar la solicitud con su CRM. NO es una clave: no se valida, no es única y no se usa para correlacionar (eso lo hace intake_id). CERO PII por contrato.';
