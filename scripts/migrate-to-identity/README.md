# migrate-to-identity

Migra **usuarios** y **credenciales M2M** de la BD de wApp a la de **identity**, el SSO del grupo.
Es la ejecución de las casillas **T2.1** y **T2.3** de la Ola 2 del *Plan 003* de identity.

Existe como script versionado, y no como una tanda de SQL suelto, porque la migración tiene que
poder **repetirse**: recrear identity desde cero con sus seeds y volver a lanzar esto debe dejar la
misma foto, sin fricción y sin borrar nada.

## Qué migra, y qué no

| Origen (wApp) | Destino (identity) | |
| :--- | :--- | :--- |
| `public.iam_users` | `iam.users` | UUID, email y hash **bcrypt intactos** |
| `public.iam_api_keys` | `iam.api_keys` | `key_hash` intacto, `ecosystem_key = 'wapp'` |
| `iam_users.tenant_id` | — | **se queda en wApp**, en `tenant_members` |
| `public.iam_refresh_tokens` | — | **no migran**: re-login limpio |

El `tenant_id` no cruza porque identity no conoce empresas: es la frontera del **ADR-0001** de
identity (INV-1). Su destino es `public.tenant_members`, que crea la migración
`0037_tenant_members.sql` de este mismo repo (casilla T2.2) — no este script.

Los refresh tokens tampoco viajan (DUD-16): identity usa otro formato de token opaco, así que los
hashes guardados no casarían. Las sesiones abiertas piden re-login, y es la decisión tomada.

## Cómo se ejecuta

```bash
export WAPP_SOURCE_DB_URL='postgres://…'   # ORIGEN: BD de wApp. Solo lectura.
export IDENTITY_DB_URL='postgres://…'      # DESTINO: BD de identity, schema iam.

./scripts/migrate-to-identity/migrate-to-identity.sh
```

Variables opcionales: `OUT_DIR` (dónde deja el SQL generado; por defecto un temporal) y `DRY_RUN=1`
(genera y comprueba, no escribe). **Lanza siempre el `DRY_RUN` primero** y mira el SQL: son unos
pocos `INSERT` y se leen en un minuto.

No hay ninguna credencial dentro del script ni de los `.sql`. Contra Neon, usa el host **directo**
(sin `-pooler`) si vas a escribir.

## Idempotencia

Todos los `INSERT` llevan `ON CONFLICT DO NOTHING`, sin nombrar un índice concreto: así una segunda
pasada no escribe nada **sea cual sea** el índice único que choque (`id` o el `email` de los vivos).
El script no borra ni actualiza nunca — si una fila ya está, se respeta la que está.

Consecuencia que conviene tener presente: si cambias un usuario en wApp **después** de migrarlo,
re-ejecutar esto **no** propaga el cambio. No es un sincronizador; es una migración que se puede
repetir.

## Las tres comprobaciones que lo paran

1. **Emails duplicados en el origen** (`00-preflight-source.sql`). Dos usuarios vivos con el mismo
   email es un error de datos, y la migración aborta — el `design.md` del Plan 003 lo pide explícito.
2. **Colisión con el destino**: un email que ya está en identity **con otro UUID**. Es el caso del
   **ADR-0011 §3** (el UUID canónico es el de EduGo): hay que poblar `iam.legacy_user_map` y remapear
   las filas de wApp que apuntaban al viejo. Es una decisión humana y el script no la toma solo.
   > Al 2026-08-02 no hay ninguna: el inventario de T0.3 dio **0 colisiones**, y se re-ejecutó al
   > empezar la Ola 2 con el mismo resultado.
3. **Conteos que no cuadran** entre lo generado y lo que hay en el origen, o entre origen y destino.

Además, la escritura va en **una transacción por archivo**: si un `CHECK` del destino rechaza una
fila —email en minúsculas, forma del `client_id`, hash de 64 hex— no entra ninguna. Media migración
es peor que ninguna.

Al final compara **fila a fila** el UUID, el email y una huella del hash bcrypt entre las dos bases.
Es la comprobación que de verdad importa: los conteos no ven un hash corrupto, y un hash corrupto
significa que la contraseña del usuario dejó de funcionar.

## Dos decisiones de mapeo que hay que conocer

**`first_name` y `last_name`.** El destino los exige `NOT NULL` y el origen **no tiene ningún
nombre**: `iam_users` solo guarda email y hash. Se rellenan con `first_name` = parte local del email
y `last_name` = **slug del tenant** (vía `public.tenants`). Se eligió por legibilidad: en una lista
de usuarios de identity, `ana / acme` se lee y se ubica, mientras que un placeholder tipo `-` o el
email repetido no dicen nada. **No son nombres reales y no deben tratarse como tales**: en cuanto
identity tenga edición de perfil, la persona los corrige.

**Una API key desactivada entra revocada.** El origen modela la baja con `is_active = false`; el
destino no tiene esa columna, la modela con `revoked_at`. Una llave con `is_active = false` y
`revoked_at IS NULL` entraría al destino como **viva**, que es el fallo peligroso — se le pone
`now()` para que entre revocada. Al revés no pasa nada: `revoked_at` con fecha viaja tal cual.

## Archivos

| Archivo | Dónde corre | Qué hace |
| :--- | :--- | :--- |
| `migrate-to-identity.sh` | — | Orquesta los seis pasos. |
| `sql/00-preflight-source.sql` | origen | Conteos y duplicados de email. |
| `sql/01-gen-users.sql` | origen | **Emite** los `INSERT` de usuarios (no inserta). |
| `sql/02-gen-api-keys.sql` | origen | **Emite** los `INSERT` de API keys. |
| `sql/03-verify-target.sql` | destino | Conteos finales. |

Los `sql/01` y `sql/02` **generan texto**; separar la generación de la aplicación es lo que permite
revisar el SQL antes de tocar la base y volver a aplicarlo tal cual.

## Higiene de secretos

`psql` vuelca la **URI entera, con la contraseña**, cuando la conexión falla. Todo el `stderr` del
script pasa por un filtro (`redact`) que tapa las passwords de Neon y cualquier `user:password@`. No
lo quites: es lo que separa un error de un incidente de seguridad — el **bug 0009 de identity**
(`Identity-core/docs/bugs/0009-password-de-neon-en-claro-en-un-transcript.md`) nació exactamente de
ese volcado.
