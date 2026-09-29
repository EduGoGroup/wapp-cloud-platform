# F2 · Arquitectura — la vista macro de `acceso`

> Medido el 2026-09-28 sobre `dev` @ `1b18932` (el código de referencia no cambió en `bad573a`).
> `A` = `internal/modulos/acceso`. Comandos al pie de cada tabla.

## 1 · Paquetes viejos → nuevos, y su tamaño

| Paquete viejo (referencia, E-8) | Paquete nuevo | Prod. | Líneas | Export. | Tests viejos · `Test*` (BD · AST) | Puertos (solo interfaces) | Adaptador Postgres · gemelo |
|---|---|---:|---:|---:|---|---|---|
| `internal/iam/domain` | `A/iam/domain` | 4 | 662 | 50 | 2 · 6 (0 · 0) | — | — |
| `internal/iam/ports/in` | `A/iam/ports/in` | 3 | 453 | 28 | 0 | `active_tenant.go`, `canje.go` (`usecases.go` es mixto) | — |
| `internal/iam/ports/out` | `A/iam/ports/out` | 3 | 413 | 10 | 0 | los 3 | — |
| `internal/iam/usecase` | `A/iam/usecase` | 11 | 1.888 | 50 | 11 · 93 (2 · 0) | — | — |
| `internal/iam/infra/memory` | `A/iam/infra/memory` | 7 (+1 ✚) | 849 | 48 | 1 · 7 | — | **es** el gemelo de 6 puertos; falta `InvitationRedeemRepo` |
| `internal/iam/infra/postgres` | `A/iam/infra/postgres` | 8 | 1.529 | 43 | 9 · 41 (37 · 4) | — | los 8 son adaptador; gemelo en `infra/memory` salvo el canje |
| `internal/iam/infra/identity` | `A/iam/infra/identity` | 2 | 1.008 | 14 | 2 · 25 (0 · 0) | — | cliente HTTP de identity (sin BD) |
| `internal/iam/transport/http` | `A/iam/transport/http` | 6 | 1.407 | 32 | 6 · 27 (0 · 0) | — | — |
| `internal/platformadmin` | `A/platformadmin` | 4 (+2 ✚) | 1.558 | 47 | 5 · 40 (33 · 0) | **ninguno** (D-F2-3) | `postgres.go` + SQL dentro de `access_requests.go` · **sin gemelo** |
| `internal/entitlements` | `A/entitlements` | 3 | 683 | 29 | 6 · 24 (3 · 0) | `Resolver` dentro de `entitlements.go` (mixto) | `postgres.go` · gemelo = `Fake` (`entitlements.go:211`) |
| **Total** | | **51** (+6 ✚) | **10.450** | **351** | **42 · 263 (75 · 4)** | | |

Comandos: `ls <dir>/*.go | grep -v _test.go | wc -l` y `cat … | wc -l` por paquete (total de líneas:
`cat $(for d in …; do ls $d/*.go | grep -v _test.go; done) | wc -l` → 10.450); exportados: recorrido
`go/ast` de las declaraciones de nivel superior (tipos, funcs, métodos exportados de tipos
exportados, const, var); `Test*`: `grep -c '^func Test'`; BD = el `Test*` alcanza `WAPP_TEST_DB_DSN`
por algún helper del paquete; AST = usa `go/parser`. Detalle por fichero en [`diseno.md`](diseno.md).

**Ficheros más grandes** (los que se parten en más de una tarea de verde si hace falta):
`platformadmin/access_requests.go` 736 · `iam/infra/identity/m2m.go` 735 ·
`iam/transport/http/roles.go` 480 · `iam/infra/postgres/memberships.go` 426 ·
`iam/usecase/exchange.go` 378 · `platformadmin/postgres.go` 322.

**Nuevos ✚** (no están en `04` §3): `A/iam/infra/memory/redeem_store.go` (gemelo del canje),
`A/platformadmin/puertos.go` y `A/platformadmin/access_requests_postgres.go` (D-F2-3),
`A/iam/ports/out/outtest/` (suites), `A/entitlements/entitlementstest/` (suite + `Fake`, D-F2-4),
`A/platformadmin/platformadmintest/` (suite + doble), `internal/arranque/puente_iam.go`.

## 2 · Grafo interno y orden de las pasadas

Aristas de producción medidas con
`GOWORK=off go list -f '{{.ImportPath}} {{.Imports}}' ./internal/iam/... ./internal/platformadmin ./internal/entitlements`:

```
entitlements            → platform/httpapi
iam/domain              → (nada interno)                        ← hoja
iam/ports/out           → iam/domain
iam/ports/in            → iam/domain   (+ platform/httpapi tras F0: AuditInput alias)
iam/infra/memory        → entitlements, iam/domain, iam/ports/out
iam/infra/postgres      → entitlements, iam/domain, iam/ports/out
iam/infra/identity      → iam/domain, iam/ports/out
iam/usecase             → iam/domain, iam/ports/in, iam/ports/out
iam/transport/http      → iam/domain, iam/ports/in
platformadmin           → iam/domain, iam/infra/postgres, iam/ports/out, platform/httpapi, platform/ratelimit
```

**Orden de la pasada de contratos (hojas primero)** y el mismo para la de verde:
`entitlements` · `iam/domain` → `iam/ports/out` (+ `outtest`) · `iam/ports/in` → `iam/infra/memory` ·
`iam/infra/identity` → `iam/usecase` → `iam/infra/postgres` → `iam/transport/http` → `platformadmin`.

## 3 · Imports hacia fuera del módulo

| Destino | Quién | Clase |
|---|---|---|
| `internal/platform/httpapi` (`IdentityFromContext`, `EnforcePlatformCaller`…, y tras F0 el DTO de auditoría) | `entitlements/middleware.go`, `platformadmin`, `iam/ports/in` | `platform` — permitido |
| `internal/platform/ratelimit` (`*Limiter` del signup) | `platformadmin/signup.go:122` | `platform` — permitido |
| `github.com/EduGoGroup/identity-shared/auth{,/jwt,/rbac}` | `iam/usecase` (`exchange.go`, `grants.go`) | 🔒 **la única dependencia `edugo`-del-grupo permitida** (SDK del SSO; constitución I-ECO-4). No añadir otra |
| `wapp-shared/{auth/jwt,logger}`, `google/uuid`, `jackc/pgx/v5/pgconn` | varios | externas ya en `go.mod` |

**Puentes al código viejo: cero.** Ningún paquete viejo de `acceso` importa otro módulo que no sea
`platform` (medido: la lista de arriba). Por eso **el ciclo 1 de `02` §4 desaparece**: las aristas
`acceso→operador`/`operador→acceso` quedan dentro del módulo fusionado, `plataforma→acceso` y
`plataforma→edge` las quita F0, y `edge→acceso` es la dirección permitida. Comprobación al cerrar:
`go list -deps ./internal/modulos/acceso/... | grep -E 'internal/(gateway|flujos|intake|modulos/edge)'` → vacío.

Lista blanca en `internal/modulos/fronteras_test.go`: `acceso → platform` y nada más.

## 4 · Quién consume `acceso` desde el código viejo — y el puente de tipos

Consumidores de producción de los paquetes viejos (medido: `go list` inverso):

| Paquete viejo | Lo importan (producción) | ¿Qué tipo cruza? | En el binario nuevo tras F2 |
|---|---|---|---|
| `entitlements` | `bootstrap/arranque` (4 ficheros), `flujos/events`, `flujos/runtime` (4), `reanalisis`, `publicapi` (6), `iam/infra/{memory,postgres}` | la interfaz `Resolver` (`Has`, `ListEffective`, `CacheTTL`: solo stdlib) y las constantes `Feature*` | **estructural**: el resolver nuevo se inyecta tal cual en los consumidores viejos. Las constantes son `string` sin tipo: el viejo sigue usando las suyas, mismos valores |
| `iam/ports/in` | `bootstrap/arranque`, `gateway/grpc`, `publicapi`, (`platform/httpapi` hasta F0) | `in.Authenticator` (`LoginInput`, `RefreshInput`, `LogoutInput`, `domain.AuthResult`), `in.Auditor` (`AuditInput`, `[]domain.AuditEvent`) | **nominal**: el gateway viejo (hasta F3) necesita `puente_iam.go` |
| `iam/domain` | `gateway/grpc` (`auth.go:200-206` compara 4 centinelas con `errors.Is`), `publicapi` | centinelas y `AuthResult` | ídem: el puente **traduce los centinelas** nuevos a los viejos |
| `iam/transport/http` · `platformadmin` · `iam/usecase` · `iam/infra/*` | solo `bootstrap/arranque` y `publicapi` | — | el arranque nuevo cablea los nuevos; `publicapi` viejo recibe `nil` en `Roles`, `Members`, `Invitations`, `Audit` (FX §4) |

`internal/arranque/puente_iam.go` (nace en F2, **muere en F3** cuando el gw nuevo recibe el
`in.Authenticator` nuevo), sin exportados, patrón de F1:

- `puenteAutenticador` implementa el `in.Authenticator` **viejo** sobre el `usecase.DelegatedAuthService`
  **nuevo**: copia `LoginInput/RefreshInput/LogoutInput` campo a campo, convierte `domain.AuthResult`
  nuevo → viejo, y **traduce errores**: `errors.Is(err, nuevo.ErrX)` → devuelve `fmt.Errorf("%w", viejo.ErrX)`
  envolviendo el original, para los centinelas que el gateway viejo compara (`ErrInvalidCredentials`,
  `ErrUserInactive`, `ErrRefreshInvalid`, `ErrInvalidInput`); el resto pasa tal cual.
- `puenteAuditor` implementa el `in.Auditor` **viejo** (`Record` con el `AuditInput` alias de
  `platform` —mismo tipo en los dos lados tras F0— y `ListAudit` convirtiendo `[]domain.AuditEvent`).
- `var _ viejoin.Authenticator = (*puenteAutenticador)(nil)` y el hermano, para que `unused` no los
  marque (T-1 de F1).

## 5 · Estado en memoria, goroutines, métricas y relojes

| Qué | Dónde (viejo) | Consecuencia de dos instancias | Regla en el binario nuevo |
|---|---|---|---|
| 🔴 Caché de `Has` por (tenant, feature) y de `ListEffective` por tenant, TTL 60 s, **sin desalojo** (deuda D-18) | `entitlements/postgres.go:15,32-39,98-139` | «dos cachés con TTL y dos verdades sobre qué tiene contratado un tenant» (`bootstrap/arranque/fase3_almacenes.go:74-77`) | **Una** instancia nueva para todo el proceso (R2.4.a), también para la cara vieja (FX TX.7) |
| Service token M2M cacheado + caché negativa, con candado que serializa el canje | `iam/infra/identity/m2m.go:78-97` | dos canjes con identity en frío; ninguno incorrecto | una instancia (la construye `wireIdentityM2M`, `bootstrap/arranque/auth.go:204`) |
| Limitador del signup por IP (1/60 s, ráfaga 5) | construido en `bootstrap/arranque/http.go:164` | cubos partidos | lo construye el arranque nuevo, una vez |
| Verificador JWKS de identity y emisor ES256 del Context Token | `bootstrap/arranque/auth.go:290,306,341` | dos claves → tokens que el otro no valida | arranque (no es de `acceso`); una instancia |

- **Goroutines**: **ninguna** en producción de `acceso` (medido:
  `grep -rn 'go func\|go [a-zA-Z.]*(' internal/iam internal/platformadmin internal/entitlements | grep -v _test` → vacío).
- **Métricas**: ninguna propia (`grep -rln prometheus\|internal/platform/metrics` → vacío en `acceso`).
- **Relojes reales** (`time.Now()` en producción): `entitlements` 2, `iam/infra/identity` 5,
  `iam/infra/memory` 7, `iam/transport/http` 2, `iam/usecase` 1, `platformadmin` 1. Los tests nuevos
  no usan reloj real: opciones `WithReloj` exportadas donde hace falta (D-F2-6); `canje` evita el
  reloj del proceso porque trae `now()` de la base en la misma consulta (`canje_una_consulta_ast_test.go:73-76`).

## 6 · Cableado en `internal/arranque` y conmutación

Qué fases del arranque (copia de F0) tocan `acceso` hoy (`internal/bootstrap/arranque/orquestador.go:51-61`):

| Fase vieja | Qué construye de `acceso` | Fichero de referencia |
|---|---|---|
| 2 `autenticacion` | `buildAuthStack`: emisor/validador ES256, `ExchangeService`, `DelegatedAuthService`, clientes de identity, auditor | `auth.go:108-277` |
| 3 `almacenes` | **el** `entResolver` | `fase3_almacenes.go:78` |
| 4 `gateway` | `WithAuthenticator(edgeAuthenticator())`, `WithAuthAuditor(auditor)` | `fase4_gateway.go:82-83` |
| 8 `transporte` | `platformadmin.NewRepository(db, entResolver)`, plano de roles, canje, empresa activa, signup, rutas `:8103` y `:8100` | `fase8_transporte.go:76` · `http.go:28-171` · `rutas_admin.go:76-91` |

`conmutar(acceso)` cambia, **en la copia de `internal/arranque`** (el viejo no se toca): los imports a
`internal/modulos/acceso/...`; `c.entResolver` pasa a `*acceso/entitlements.Postgres`; el gateway
(aún viejo) recibe `puente_iam`; `http.go` monta A1–A7 en `apipublica`
(FX TX.7); `rutas_admin.go` construye J4–J11 **inline** con `platformadmin.*` nuevo
(I-CP-5). La huella (`huella_test.go`) tiene que dar lo mismo: rutas, permisos, rpc, métricas,
goroutines. **Cómo se prueba que usa lo nuevo**: `go list -deps ./cmd/server-modular` (R2.5.d) y una
aserción de cableado en `internal/arranque` sobre el tipo concreto de `c.entResolver`.

## 7 · Rutas (autoridad: [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md))

| Listener | Filas del mapa | Nº | Qué cambia en F2 |
|---|---|---:|---|
| `:8103` | A1–A7 (auth, canje, empresa activa, signup) · B1–B14 (roles, miembros, invitaciones) · C1 `GET /api/v1/audit` · C2 `GET /api/v1/entitlements` | **23** | se sirven desde `internal/apipublica` (FX TX.5–TX.7) sobre los puertos **nuevos** de `acceso` |
| `:8100` | J4–J11 (`/admin/tenants*`, `/admin/access-requests*`) | **8** | handlers de `A/platformadmin`, **inline**, permiso `*.any` |

Regla de conteo (la del ecosistema, FX §0): una ruta = un patrón registrado en ejecución, una vez
aunque aparezca en dos ramas de un `if` (`POST /api/v1/signup`, `http.go:165/168`).

## 8 · Lo que no cambia hacia fuera

Las 31 rutas y sus textos; los permisos y el sufijo `.any` (I-CP-5, migración `0060`); el cuerpo
`{"error":"feature_not_enabled","feature":…}` y el plural `features`; los cuerpos anti-oráculo del
canje y de la empresa activa; los 503 del alta sin M2M y del signup sin M2M; el `COALESCE(plan_id,'basic')`;
las tablas (`iam_*`, `tenant_members`, `tenant_invitations`, `user_active_tenant`, `audit_events`,
`access_requests`, `plans`, `plan_features`, `tenant_features`) y su SQL; las variables
`WAPP_IDENTITY_{URL,JWKS_URL,TIMEOUT,API_KEY}`, `WAPP_JWT_*`, `WAPP_PLATFORM_TENANT_ID`; la
dependencia `identity-shared/auth v0.3.1` en `go.mod`. `cmd/server` no cambia un byte.
