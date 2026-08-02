-- 01-gen-users.sql — se ejecuta contra el ORIGEN (BD de wApp).
--
-- NO inserta nada: EMITE el texto de los INSERT que después se aplican al
-- destino (identity). Separar generación de aplicación es lo que permite
-- revisar el SQL antes de tocar la base y re-aplicarlo tal cual.
--
-- Mapeo de columnas (Plan 003 · T2.1):
--   id, email, password_hash, is_active, created_at, updated_at → 1:1, INTACTOS.
--     El UUID se conserva (T0.3 verificó 0 colisiones con EduGo) y el hash
--     bcrypt viaja sin tocar: la contraseña del usuario sigue funcionando.
--   first_name  = parte local del email  ─┐ el destino los exige NOT NULL y el
--   last_name   = slug del tenant        ─┘ origen no tiene ningún nombre real.
--   phone, locale, email_verified_at = NULL   (el origen no tiene el dato)
--   token_version = 0 (default del destino: nace sin revocaciones)
--   tenant_id NO viaja: es negocio y se queda en wApp (tenant_members).
--
-- Solo usuarios vivos: `deleted_at IS NOT NULL` no migra.

SELECT format(
    'INSERT INTO iam.users (id, email, password_hash, first_name, last_name, phone, locale, is_active, email_verified_at, token_version, created_at, updated_at) '
    || 'VALUES (%L::uuid, %L, %L, %L, %L, NULL, NULL, %L::boolean, NULL, 0, %L::timestamptz, %L::timestamptz) ON CONFLICT DO NOTHING;',
    u.id,
    u.email,
    u.password_hash,
    split_part(u.email, '@', 1),   -- first_name  ← parte local del email
    t.slug,                        -- last_name   ← slug del tenant de origen
    u.is_active,
    u.created_at,
    u.updated_at
)
FROM public.iam_users u
JOIN public.tenants t ON t.id = u.tenant_id
WHERE u.deleted_at IS NULL
ORDER BY u.created_at, u.id;
