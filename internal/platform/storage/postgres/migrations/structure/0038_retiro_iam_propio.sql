-- ============================================================
-- 0038: Retiro del IAM propio de wApp — mueren iam_users,
-- iam_refresh_tokens e iam_api_keys
-- (identity Plan 003 · T5.1, design.md «Ola 5 — diseño de la limpieza»).
--
-- POR QUÉ: desde la Ola 3, quien acredita a las personas del grupo es
-- identity-core (identity ADR-0001). wApp dejó de validar contraseñas, de
-- custodiar sesiones y de emitir credenciales M2M; lo que sobrevivía aquí era el
-- almacenamiento de todo eso, sin nadie que lo escribiera. Es el INV-2 en su
-- forma literal: se elimina, no se comenta ni se deja tras un flag.
--
-- 🔴 LO QUE **NO** MUERE, y es la mitad del punto de esta migración:
--   iam_roles, iam_role_grants, iam_user_roles, iam_user_grants **SE QUEDAN**.
-- Su prefijo `iam_` engaña: NO son identidad, son el RBAC de NEGOCIO de wApp
-- (permisos sobre sesiones de WhatsApp, disparadores de flujo, diagnósticos), y
-- por la frontera del ADR-0001 su sitio es este. La prueba está en los datos: la
-- Ola 2 no las migró y los usuarios de wApp no tienen ningún rol en identity.
-- Renombrarlas para que dejen de mentir es deuda registrada, no de esta ola.
--
-- ORDEN INTERNO — no es estético, es lo único que evita una pérdida silenciosa:
--   1. SOLTAR las FKs de iam_user_roles/iam_user_grants hacia iam_users.
--      Ambas cuelgan con ON DELETE CASCADE de una tabla que muere: un
--      `DROP TABLE iam_users CASCADE` **funcionaría** y se llevaría por delante
--      las asignaciones de rol de los usuarios vivos SIN un solo error. Se
--      descubriría semanas después, cuando alguien no pudiera hacer algo que sí
--      podía. Soltarlas antes deja user_id como UUID suelto, que es exactamente
--      el criterio que ya aplicó tenant_members (0037): el usuario vive en OTRA
--      base y la integridad referencial física no cruza esa frontera.
--   2. DROP de las tres tablas, y en ESTE orden (dependientes primero) para no
--      necesitar CASCADE: si quedara una dependencia que nadie previó, esto
--      falla ruidosamente en vez de borrarla en silencio.
--
-- ADITIVA e IDEMPOTENTE: el runner es hash-based FULL-REPLAY, así que reejecuta
-- TODOS los structure/*.sql en cada corrida con cambios. Ojo al efecto: 0014,
-- 0017 y 0018 vuelven a CREATE ... IF NOT EXISTS estas tres tablas VACÍAS en
-- cada replay, y esta migración las vuelve a borrar al final de la MISMA
-- corrida. El estado final es siempre el mismo (no existen); los IF EXISTS son
-- los que hacen ese ciclo inocuo. NO se borran los archivos 0014–0018: borrar
-- una migración no borra nada en la BD —hace falta este DROP explícito— y sí
-- destruiría la historia de cómo llegó el esquema hasta aquí.
-- ============================================================

-- ------------------------------------------------------------
-- 1) Soltar las FKs de las tablas que SOBREVIVEN.
-- ------------------------------------------------------------
ALTER TABLE public.iam_user_roles
    DROP CONSTRAINT IF EXISTS iam_user_roles_user_id_fkey;

ALTER TABLE public.iam_user_grants
    DROP CONSTRAINT IF EXISTS iam_user_grants_user_id_fkey;

COMMENT ON COLUMN public.iam_user_roles.user_id IS 'Usuario al que se asigna el rol. UUID SIN FK: la persona vive en identity-core (iam.users, OTRA base de datos) desde la Ola 5 del Plan 003; la integridad referencial física no cruza la frontera (mismo criterio que tenant_members).';
COMMENT ON COLUMN public.iam_user_grants.user_id IS 'Usuario dueño del override. UUID SIN FK: la persona vive en identity-core (iam.users, OTRA base de datos) desde la Ola 5 del Plan 003; la integridad referencial física no cruza la frontera (mismo criterio que tenant_members).';

-- ------------------------------------------------------------
-- 2) DROP del padrón, las sesiones y las credenciales M2M propias.
--    Dependientes primero: iam_refresh_tokens tiene FK a iam_users.
-- ------------------------------------------------------------
DROP TABLE IF EXISTS public.iam_refresh_tokens;
DROP TABLE IF EXISTS public.iam_api_keys;
DROP TABLE IF EXISTS public.iam_users;
