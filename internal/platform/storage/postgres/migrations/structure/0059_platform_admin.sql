-- ============================================================
-- 0059: PLANO DE PLATAFORMA — el tenant operador de wApp y el rol que sí
-- puede cortar a un tercero (Plan 055 · REQ-055.7, ADR-0039).
--
-- POR QUÉ EXISTE ESTA MIGRACIÓN. La 0058 dio a public.tenants su revoked_at
-- (kill-switch COMERCIAL: "esta empresa dejó de pagar"), pero el endpoint que
-- lo dispara tomaba el tenant objetivo de la Identity del PROPIO llamante
-- (INV-8, Plan 018 · T4). Consecuencia medida contra el código, no supuesta:
-- /admin/tenants/revoke solo sabía revocar a QUIEN LLAMA. wApp no podía cortar
-- a un cliente moroso -- únicamente el cliente podía cortarse a sí mismo -- y,
-- peor, ese mismo cliente se desrevocaba solo llamando a
-- /admin/tenants/restore. El kill-switch comercial existía en la tabla y no
-- existía en la práctica.
--
-- El arreglo (ADR-0039) separa dos cosas que INV-8 tenía fundidas: el tenant
-- que EJECUTA (sigue saliendo del token, jamás del cuerpo) y el tenant OBJETIVO
-- (ahora viaja en el cuerpo). Para que eso no abra un agujero peor -- cualquier
-- tenant revocando a cualquier otro -- hacen falta las tres piezas de aquí.
--
-- 1. EL TENANT DE PLATAFORMA. wApp, como operador del servicio, necesita ser
--    un tenant más para poder tener usuarios y emitir tokens: el mismo IAM, sin
--    un segundo plano de autenticación. Su id es FIJO y determinista porque el
--    binario lo compara contra la Identity del llamante (config
--    PlatformTenantID, default idéntico a este); un id generado al azar exigiría
--    reconfigurar la plataforma tras cada bootstrap.
--
-- 2. EL ROL platform_admin. Plantilla GLOBAL (tenant_id NULL, Decisión R2 de
--    0015_iam_roles.sql) con los dos permisos nuevos, y SOLO esos dos:
--    'tenants.revoke.any' y 'tenants.restore.any'. El sufijo '.any' no es
--    decorativo -- nombra exactamente lo que se concede: actuar sobre un tenant
--    que no es el tuyo.
--
-- 3. POR QUÉ HACE FALTA EL DENY. Sin él las otras dos piezas no sirven de nada:
--    tenant_admin tiene el grant '*' (0015), y '*' cubre CUALQUIER request en el
--    matcher (identity-shared/auth/rbac: `if pattern == "*" { return true }`).
--    Es decir, todo administrador de todo cliente ya tendría 'tenants.revoke.any'
--    el día que la ruta lo exija: el agujero cambiaría de sitio, no se cerraría.
--    El deny '*.any' sobre tenant_admin lo tapa aprovechando la precedencia
--    deny-sobre-allow de EvaluateGrants, y lo hace de forma DURADERA: cubre por
--    forma cualquier permiso '.any' futuro, sin depender de que nadie se acuerde
--    de añadirlo a una lista.
--    Verificado contra el matcher real (rama '*.suffix'): '*.any' cubre
--    'tenants.revoke.any' y NO cubre 'leases.revoke' -- el kill-switch
--    anti-clon por instalación (ADR-0007), que es del tenant y debe seguir
--    siéndolo, queda intacto.
--
-- CERO PII / CERO llaves: un tenant administrativo, un rol y tres patrones de
-- permiso. Zero-knowledge (ADR-0007/0009) intacto.
--
-- ADITIVA e IDEMPOTENTE: IDs fijos deterministas + ON CONFLICT DO NOTHING =>
-- re-aplicable N veces (el runner es hash-based FULL-REPLAY) sin duplicar ni
-- pisar nada. NO clean-slate. El ON CONFLICT va SIN target a propósito: cada
-- una de estas tablas tiene, además de su PK, un índice único de negocio
-- (tenants.slug; iam_roles_global_name_uidx sobre name WHERE tenant_id IS NULL;
-- iam_role_grants_uidx sobre (role_id, pattern, effect)), y la forma sin target
-- los cubre todos -- si una fila equivalente ya existe con otro id, la
-- migración no revienta.
--
-- NO bumpea SchemaVersion: 0.33.0 ya es el bump de este Plan 055 (la 0058 es su
-- hermana) y aún no se ha publicado (regla de version.go: un bump por plan).
-- ============================================================

-- 1. El tenant operador de wApp. plan_id se deja NULL (nullable desde
-- 0032_entitlements.sql): se resuelve como 'basic' y da igual -- este tenant no
-- consume features de producto, solo emite tokens administrativos.
INSERT INTO public.tenants (id, slug, display_name) VALUES
    ('55550000-0000-0000-0000-000000000055', 'wapp-platform', 'wApp (plataforma)')
ON CONFLICT DO NOTHING;

-- 2. El rol de plataforma: plantilla GLOBAL, como los tres canónicos.
INSERT INTO public.iam_roles (id, tenant_id, name) VALUES
    ('10000000-0000-0000-0000-000000000004', NULL, 'platform_admin')
ON CONFLICT DO NOTHING;

-- 3. Los grants. Los tres primeros IDs libres de la serie 2000...0001-0013.
INSERT INTO public.iam_role_grants (id, role_id, pattern, effect) VALUES
    -- platform_admin: cortar y reactivar a un tenant AJENO, y nada más.
    ('20000000-0000-0000-0000-000000000014', '10000000-0000-0000-0000-000000000004', 'tenants.revoke.any',  'allow'),
    ('20000000-0000-0000-0000-000000000015', '10000000-0000-0000-0000-000000000004', 'tenants.restore.any', 'allow'),
    -- tenant_admin: su '*' NO alcanza al plano de plataforma (deny precede a allow).
    ('20000000-0000-0000-0000-000000000016', '10000000-0000-0000-0000-000000000001', '*.any',               'deny')
ON CONFLICT DO NOTHING;

COMMENT ON TABLE public.iam_roles IS 'Roles RBAC (Plan 018 §5). tenant_id NULL = PLANTILLA global canónica (tenant_admin/operator/viewer, sembrados en 0015; platform_admin en 0059); tenant_id set = rol custom del tenant. parent_role_id modela herencia (cadena). CERO PII ni llaves.';
