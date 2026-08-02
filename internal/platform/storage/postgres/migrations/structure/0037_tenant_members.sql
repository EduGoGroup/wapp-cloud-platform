-- ============================================================
-- 0037: Membresía usuario↔tenant — tenant_members
-- (identity Plan 003 · T2.2, design.md §Migración de datos punto 4).
--
-- POR QUÉ EXISTE: los usuarios de wApp se van a identity (`iam.users` del SSO del
-- grupo), y allí NO cabe el tenant — es contexto de NEGOCIO y lo prohíbe la
-- frontera del ADR-0001 de identity (INV-1: identity no conoce empresas). El
-- vínculo usuario→tenant, que hoy vive como `iam_users.tenant_id`, se queda en
-- wApp y pasa a esta tabla, que es su hogar definitivo.
--
-- QUIÉN LA LEE: el `/auth/exchange` de cloud-platform (Ola 3 del mismo plan).
-- Recibe un Identity Token de identity —que solo dice QUIÉN es la persona— y
-- resuelve aquí a QUÉ tenant pertenece para emitir el Context Token. Sin esta
-- tabla el exchange no tiene de dónde sacar el tenant.
--
-- FORMA: M2M (user_id, tenant_id) con PK compuesta, no 1:1. Hoy la realidad es
-- 1:1 —cada usuario en un tenant, `iam_users.tenant_id` es NOT NULL—, pero el
-- modelo de destino admite que una persona opere en varios tenants sin cambiar
-- de identidad, que es justo lo que un SSO habilita. La FK a tenants replica la
-- de sus vecinas (0014_iam_users.sql): ON DELETE CASCADE.
--
-- user_id SIN FK: apunta a `iam.users` de identity, que vive en OTRA base de
-- datos (mismo criterio que el INV-2 de identity: sin FKs físicas cruzando la
-- frontera). La FK a `iam_users` sería peor que ninguna — esa tabla la borra la
-- Ola 5 de este mismo plan y se llevaría por delante las membresías.
--
-- POBLACIÓN: desde `iam_users.tenant_id` de los usuarios VIVOS (deleted_at IS
-- NULL); los borrados lógicos no viajan a identity, así que tampoco tienen
-- membresía que conservar.
--
-- CERO PII / CERO llaves: solo dos identificadores y una marca de tiempo.
--
-- ADITIVA e IDEMPOTENTE: runner hash-based FULL-REPLAY; CREATE TABLE IF NOT
-- EXISTS + INSERT ... ON CONFLICT DO NOTHING => re-aplicable N veces sin daño.
-- NO clean-slate.
-- ============================================================

CREATE TABLE IF NOT EXISTS public.tenant_members (
    user_id    UUID        NOT NULL,   -- identidad en identity (iam.users); SIN FK: otra BD
    tenant_id  UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tenant_id)
);

-- La inversa «quiénes son los miembros de este tenant»: la PK sirve al acceso
-- por usuario (el del exchange), este índice al de administración por tenant.
CREATE INDEX IF NOT EXISTS idx_tenant_members_tenant
    ON public.tenant_members (tenant_id);

-- Población desde el vínculo actual. Solo usuarios vivos: los soft-deleted no
-- migran a identity. Se conserva su created_at para no inventar una antigüedad
-- de membresía más nueva que la del propio usuario.
INSERT INTO public.tenant_members (user_id, tenant_id, created_at)
SELECT id, tenant_id, created_at
FROM public.iam_users
WHERE deleted_at IS NULL
ON CONFLICT (user_id, tenant_id) DO NOTHING;

COMMENT ON TABLE  public.tenant_members IS 'Membresía usuario↔tenant: el vínculo de NEGOCIO que se queda en wApp cuando los usuarios pasan a identity (identity ADR-0001, INV-1). La lee /auth/exchange para resolver el tenant del Context Token. CERO PII ni llaves.';
COMMENT ON COLUMN public.tenant_members.user_id    IS 'Usuario en identity (iam.users, otra BD). SIN FK a propósito: la frontera no lleva integridad referencial física.';
COMMENT ON COLUMN public.tenant_members.tenant_id  IS 'Tenant del que la persona es miembro (FK a tenants, ON DELETE CASCADE).';
COMMENT ON COLUMN public.tenant_members.created_at IS 'Alta de la membresía. En la migración inicial hereda el created_at del usuario de origen.';
