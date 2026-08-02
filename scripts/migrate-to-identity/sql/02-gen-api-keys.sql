-- 02-gen-api-keys.sql — se ejecuta contra el ORIGEN (BD de wApp).
--
-- Igual que 01: emite el texto de los INSERT, no inserta.
--
-- Mapeo de columnas (Plan 003 · T2.3):
--   id, client_id, key_hash, scopes, expires_at → 1:1, INTACTOS.
--     key_hash es SHA-256 hex y el destino lo valida con
--     `ck_api_keys_hash_sha256`: la credencial sigue siendo la misma.
--   ecosystem_key = 'wapp' SIEMPRE (identity ADR-0022: toda credencial M2M
--     pertenece a un ecosistema). NO se traduce a `system_id`: esa columna
--     admite UNA aplicación y wApp son dos (wapp.bff y wapp.edge), así que la
--     única ejecución posible habría sido NULL — o sea, permiso también sobre
--     las aplicaciones de EduGo. Ese era el agujero que cerró el ADR-0022.
--   revoked_at: 1:1, SALVO el caso `is_active = false` con `revoked_at IS NULL`.
--     El destino no tiene columna `is_active`: modela la baja como fecha de
--     revocación. Una llave desactivada sin fecha entraría como VIVA, que es el
--     fallo peligroso; se le pone `now()` para que entre revocada.
--   token_ttl = NULL (el origen no tiene el dato; el destino aplica su default).
--   tenant_id NO viaja: es contexto de negocio y se queda en wApp
--     (frontera del ADR-0001 de identity, INV-1).
--   last_used_at NO viaja: el destino no lo modela.

SELECT format(
    'INSERT INTO iam.api_keys (id, client_id, key_hash, scopes, ecosystem_key, token_ttl, expires_at, revoked_at, created_at) '
    || 'VALUES (%L::uuid, %L, %L, %L::text[], ''wapp'', NULL, %L::timestamptz, %L::timestamptz, %L::timestamptz) ON CONFLICT DO NOTHING;',
    k.id,
    k.client_id,
    k.key_hash,
    k.scopes,
    k.expires_at,
    CASE WHEN NOT k.is_active AND k.revoked_at IS NULL
         THEN now()            -- desactivada sin fecha → entra revocada, no viva
         ELSE k.revoked_at
    END,
    k.created_at
)
FROM public.iam_api_keys k
ORDER BY k.created_at, k.id;
