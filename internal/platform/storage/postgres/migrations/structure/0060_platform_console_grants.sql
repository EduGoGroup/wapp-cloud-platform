-- ============================================================
-- 0060: Consola de plataforma -- el scope multi-empresa de los roles, la
-- bandeja de solicitudes de acceso y los cinco permisos de lectura/alta que
-- la consola necesita (Plan 056 · T1.1/T3.1, INV-056.6: tres cosas, un solo
-- fichero).
--
-- (a) iam_user_roles GANA tenant_id (D-056.11 / REQ-056.17). Hoy los roles de
-- wApp son GLOBALES por usuario (0016_iam_user_roles.sql:17-21); le falta el
-- scope que EduGo ya tiene (iam.user_roles.school_id nullable). Aditiva y NO
-- migra un dato: toda fila existente queda tenant_id NULL = global, que es
-- literalmente lo que es hoy y lo que platform_admin necesita seguir siendo.
--
-- La PK compuesta (user_id, role_id) deja de alcanzar (dos filas del mismo
-- rol para el mismo usuario, una global y una acotada a un tenant, dejarían
-- de ser la misma fila) y pasa a UNIQUE (user_id, role_id, tenant_id).
-- Verificado contra el DDL real (0016_iam_user_roles.sql:17-21): la PK se
-- declaró INLINE en el CREATE TABLE, sin nombre explícito, así que Postgres
-- le puso el suyo por defecto -- "iam_user_roles_pkey" -- y ese es el nombre
-- que se suelta aquí, no uno adivinado. (0038_retiro_iam_propio.sql:46-47 ya
-- soltó la FK iam_user_roles_user_id_fkey de esta misma tabla -- el usuario
-- vive en identity-core, otra base -- pero la PK, que no cruza esa frontera,
-- seguía intacta hasta hoy.)
--
-- La UNIQUE se implementa como CREATE UNIQUE INDEX ... IF NOT EXISTS (no
-- ADD CONSTRAINT, que no admite IF NOT EXISTS) para que el DROP+CREATE sea
-- idempotente bajo el runner FULL-REPLAY, mismo patrón que
-- iam_user_grants_uidx (0016) e iam_role_grants_uidx (0059). Sigue
-- encabezada por user_id, así que cualquier consulta que hoy se apoye en la
-- PK para buscar "los roles de este usuario" (roles.RolesOfUser) sigue
-- servida igual de bien por el índice nuevo.
--
-- 🔴 RIESGO CONOCIDO, RESUELTO por el índice único PARCIAL al final de este
-- bloque (a): un UNIQUE con columna NULLable NO deduplica los NULL en
-- Postgres -- dos filas (user_id=U, role_id=R, tenant_id=NULL) pasarían la
-- UNIQUE de 3 columnas sin chocar, rompiendo en silencio la garantía "un rol
-- global una sola vez por usuario" que la PK vieja sí daba gratis. El daño
-- medido no es duplicar grants (internal/iam/usecase/grants.go:58 dedupea por
-- rol vía seenRole) sino que roleNames se llena ANTES del dedupe
-- (grants.go:49): el token emitiría el mismo rol N veces, rompiendo el
-- criterio "byte a byte" de T1.1b. Por eso el índice parcial UNIQUE
-- (user_id, role_id) WHERE tenant_id IS NULL sí se añade en esta migración,
-- pese a que el plan (D-056.11, tasks.md T1.1) lo dejaba condicionado a "si
-- eso importa": el AssignToUser real de roles.go depende de un target de
-- ON CONFLICT que sin este índice no existe (ver el diff de roles.go).
--
-- (b) public.access_requests (T3.1) -- la bandeja de solicitudes de acceso a
-- la consola. Bloque literal de design.md §1.0b. user_id SIN FK a propósito:
-- el usuario vive en identity-core (otra base), mismo patrón que
-- tenant_members (0037_tenant_members.sql:39). email se duplica para poder
-- listar la bandeja sin llamar a identity fila por fila. Los dos índices son
-- PARCIALES porque status='pending' es el único estado que se consulta con
-- frecuencia (la bandeja) y porque el UNIQUE de una-solicitud-viva-por-
-- persona depende de status: los NULL no aplican aquí (no hay columna
-- nullable en el índice), pero si se hiciera total sobre user_id a secas
-- impediría volver a pedir acceso tras un rechazo -- por eso el WHERE.
--
-- (c) Los grants de la consola: cinco filas nuevas en iam_role_grants, todas
-- 'allow', todas al rol platform_admin ('10000000-0000-0000-0000-000000000004',
-- sembrado en 0059), con ids continuando la serie HEXADECIMAL desde
-- '20000000-0000-0000-0000-000000000017' hasta …001b (el último usado es
-- …0016, 0059_platform_admin.sql:76-81): tenants.read.any, tenants.create.any,
-- fleet.read.any, users.provision.any, enrollment.issue.any. Los ids son
-- …0017, …0018, …0019, …001a, …001b -- CONTIGUOS en hex, sin el salto a
-- …0020/…0021 (decimal) de una versión anterior de esta migración, que habría
-- dejado …001a-…001f libres y en riesgo de choque silencioso (ON CONFLICT DO
-- NOTHING se traga un futuro grant que reusara …0020) con una migración futura
-- que sí siguiera la serie hex.
--
-- POR QUÉ ESTA MIGRACIÓN NO LLEVA NINGÚN deny (a diferencia de la 0059, que sí
-- necesitó uno -- ver su cabecera, 0059:33-45): las cinco patrones nuevas
-- TERMINAN en '.any', y la 0059 ya sembró un deny DURADERO y genérico sobre
-- tenant_admin para exactamente esa forma -- '20000000-…-0016':
-- ('10000000-…-0001', '*.any', 'deny') -- que la propia cabecera de la 0059
-- documenta como cobertura "por forma [de] cualquier permiso '.any' futuro,
-- sin depender de que nadie se acuerde de añadirlo a una lista". Estos cinco
-- permisos son justo ese futuro: ya llegan bloqueados para tenant_admin (cuyo
-- grant '*' los alcanzaría de otro modo) sin que esta migración tenga que
-- repetir el deny. No es un descuido: es el deny de la 0059 haciendo el
-- trabajo para el que se escribió.
--
-- CERO PII / CERO llaves en las tres piezas: una columna de scope, una
-- bandeja de solicitudes (sin credenciales ni material criptográfico) y
-- patrones de permiso. Zero-knowledge (ADR-0007/0009) intacto.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; IF NOT EXISTS +
-- ON CONFLICT DO NOTHING => re-aplicable N veces sin duplicar ni pisar nada.
-- NO clean-slate.
-- ============================================================

-- ------------------------------------------------------------
-- (a) Scope multi-empresa de iam_user_roles (D-056.11).
-- ------------------------------------------------------------
ALTER TABLE public.iam_user_roles
    ADD COLUMN IF NOT EXISTS tenant_id UUID NULL REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE public.iam_user_roles
    DROP CONSTRAINT IF EXISTS iam_user_roles_pkey;

CREATE UNIQUE INDEX IF NOT EXISTS iam_user_roles_user_role_tenant_uidx
    ON public.iam_user_roles (user_id, role_id, tenant_id);

-- Índice único PARCIAL: sin él, un UNIQUE con columna NULLable no deduplica
-- los NULL (dos filas (user_id=U, role_id=R, tenant_id=NULL) pasarían la
-- UNIQUE de arriba sin chocar), perdiendo la garantía "un rol global una sola
-- vez por usuario" que la PK vieja daba gratis. Además, este es el target que
-- infra/postgres/roles.go:AssignToUser necesita para su ON CONFLICT cuando el
-- rol asignado es global (tenant_id NULL) -- un índice PARCIAL no participa en
-- la inferencia por columnas a secas, así que roles.go usa ON CONFLICT DO
-- NOTHING sin target (cubre este índice y el de 3 columnas de arriba a la
-- vez; mismo patrón que 0059_platform_admin.sql:52-57).
CREATE UNIQUE INDEX IF NOT EXISTS iam_user_roles_user_role_global_uidx
    ON public.iam_user_roles (user_id, role_id) WHERE tenant_id IS NULL;

COMMENT ON COLUMN public.iam_user_roles.tenant_id IS 'Ámbito del rol asignado (Plan 056 · D-056.11). NULL = rol GLOBAL (todas las filas previas a esta migración, incluida la de platform_admin). NOT NULL = rol acotado a ese tenant; gana sobre el global del mismo rol al resolver grants efectivos (WHERE tenant_id = $1 OR tenant_id IS NULL). Mismo patrón que iam.user_roles.school_id en EduGo.';

COMMENT ON TABLE public.iam_user_roles IS 'Asignación M2M usuario↔rol (Plan 018 §5). SIN PK desde 0060: UNIQUE (user_id, role_id, tenant_id) + UNIQUE parcial (user_id, role_id) WHERE tenant_id IS NULL. CERO PII ni llaves.';

-- ------------------------------------------------------------
-- (b) Bandeja de solicitudes de acceso a la consola (T3.1, design.md §1.0b).
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.access_requests (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL,              -- SIN FK: el usuario vive en identity (patrón 0037)
    email       TEXT NOT NULL,              -- copia para poder listar sin llamar a identity
    origin      TEXT NOT NULL CHECK (origin IN ('bff','edge')),
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','approved','rejected')),
    reason      TEXT,                       -- motivo del rechazo
    decided_by  UUID,                       -- el operador que resolvió
    decided_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- una sola solicitud viva por persona; los NULL no deduplican, por eso índice PARCIAL
CREATE UNIQUE INDEX IF NOT EXISTS access_requests_one_pending
    ON public.access_requests (user_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS access_requests_pending_at
    ON public.access_requests (created_at) WHERE status = 'pending';

COMMENT ON TABLE public.access_requests IS 'Bandeja de solicitudes de acceso a la consola de plataforma (Plan 056 · T3.1). user_id SIN FK: la persona vive en identity-core (otra base de datos), mismo criterio que tenant_members (0037). CERO PII más allá del email de contacto, CERO llaves.';

-- ------------------------------------------------------------
-- (c) Los cinco grants de lectura/alta de la consola, todos platform_admin.
-- ------------------------------------------------------------
INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    ('20000000-0000-0000-0000-000000000017', '10000000-0000-0000-0000-000000000004', 'tenants.read.any',     'allow'),
    ('20000000-0000-0000-0000-000000000018', '10000000-0000-0000-0000-000000000004', 'tenants.create.any',   'allow'),
    ('20000000-0000-0000-0000-000000000019', '10000000-0000-0000-0000-000000000004', 'fleet.read.any',       'allow'),
    ('20000000-0000-0000-0000-00000000001a', '10000000-0000-0000-0000-000000000004', 'users.provision.any',  'allow'),
    ('20000000-0000-0000-0000-00000000001b', '10000000-0000-0000-0000-000000000004', 'enrollment.issue.any', 'allow')
ON CONFLICT DO NOTHING;
