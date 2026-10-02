# F2 · Reglas — lo que no se toca, las trampas y la definición de hecho

> Las reglas comunes (toolchain, gates, ramas, commits) están en [`00-marco/`](../00-marco/README.md);
> aquí solo lo de `acceso`. Toda trampa lleva su `fichero:línea` sobre `dev` @ `1b18932`.

## 1 · Lo que no se toca

- **El código viejo** (`internal/iam/**`, `internal/entitlements`, `internal/platformadmin`,
  `internal/bootstrap/**`) no se edita (E-1). La línea que hace que
  `internal/iam/infra/postgres/membresia_unica_ast_test.go` no barra el árbol nuevo la pone **F0**
  (T0.27, D-F4-1, que subsume D-F2-2): F2 no toca código viejo.
- 🔒 **Zero-knowledge**: `acceso` no autentica personas ni guarda contraseñas (constitución
  I-CP-9; la migración `0038_retiro_iam_propio.sql:59` borró `iam_users`, `iam_refresh_tokens`,
  `iam_api_keys`). Ningún contrato nuevo valida una contraseña, resuelve un usuario o emite un refresh
  propio.
- 🔒 **`identity-shared/auth v0.3.1`** es la única dependencia del grupo permitida y **no se añade
  otra** (I-ECO-4); ni se retira creyendo que es una violación.
- Las 31 rutas, sus permisos, cadenas, textos y condiciones de montaje (mapa FX §2.1–2.3 y J4–J11).
- `WAPP_PLATFORM_TENANT_ID` (default `55550000-0000-0000-0000-000000000055`) y el plano de plataforma
  (ADR-0039, fuera de este repo: *«wApp se da de alta como tenant de sí misma y abre una excepción
  acotada a INV-8, sostenida por tres cercas: un rol `platform_admin` con grants `.any`, un deny
  `*.any` sobre `tenant_admin` y una comprobación de pertenencia en cada handler»*).

## 2 · Trampas conocidas

| # | Trampa | Dónde | Qué hacer |
|---|---|---|---|
| T-1 | **El candado viejo de «una sola empresa» barre todo `internal/`** y se pone rojo en cuanto el verde de `A/iam/infra/postgres/memberships.go` escribe `INSERT INTO public.tenant_members` | `internal/iam/infra/postgres/membresia_unica_ast_test.go:74,97-130` | **Resuelto en F0** (T0.27, D-F4-1): el barrido viejo salta `internal/{modulos,nucleo,arranque,apipublica,pendiente,candados}`. F2 lo **verifica** antes del verde (T2.1). El candado **nuevo** barre todo `internal/` y espera los dos escritores hasta F10. *Si D-F4-1 = no*: D-F2-2, añadir el escritor nuevo a `escritoresEsperados` (`:88-90`) en el mismo commit |
| T-2 | **El SQL no se puede componer por trozos**: los candados buscan el literal `"INSERT INTO public.tenant_members"`, `"SET redeemed_at"` y `"pg_advisory_xact_lock"` en literales de cadena | `membresia_unica_ast_test.go:77-84`, `canje_orden_ast_test.go:40-47` | copiar el SQL **literal**; si cambia, el candado se reescribe, no se borra |
| T-3 | **I-CP-5 queda ciego sin ponerse rojo** si el handler de plataforma se pre-arma en un campo o se importa el paquete con alias: detecta por el texto `"platformadmin."` del argumento | `internal/bootstrap/arranque/platform_permissions_test.go:48-50`; deuda D-13 | en `internal/arranque/rutas_admin.go`, handlers **inline** e import **sin alias** |
| T-4 | **Centinelas por identidad**: el gateway viejo compara `domain.ErrInvalidCredentials`, `ErrUserInactive`, `ErrRefreshInvalid`, `ErrInvalidInput` **viejos** | `internal/gateway/grpc/auth.go:200-206` | `puente_iam.go` los traduce (arquitectura §4); test de equivalencia con `errors.Is` |
| T-5 | **Dos resolvers = dos verdades**: la caché de derechos no desaloja ni se invalida | `internal/entitlements/postgres.go:32-39`; comentario 🔴 en `bootstrap/arranque/fase3_almacenes.go:74-77` | una sola instancia nueva, también inyectada en la cara vieja (FX TX.7) |
| T-6 | **El mutex de la caché no se sostiene durante la consulta**: está bien hecho, no «arreglarlo» | `entitlements/postgres.go:98-113` (arquitectura.md §2.6 lo dice) | portarlo igual |
| T-7 | **`ListEffective` ordena en Go, no con `ORDER BY`**: el collation del servidor ordena el `_` distinto | `entitlements/postgres.go:204-207` | igual; el test afirma el orden |
| T-8 | **Un token sin empresa atraviesa 3 rutas** (`/auth/whoami`, `/invitations/accept`, `/auth/active-tenant` + `/auth/tenants`) con `Authenticate` **a secas**; `protect`/`protectRead` le darían 403 a todas las personas para las que existen, y **no se auditan** (la bitácora es por tenant) | `bootstrap/arranque/http.go:86-153` | la cara nueva conserva la cadena A del mapa (FX A3–A6) |
| T-9 | **`POST /api/v1/members` se monta siempre y degrada a 503**, nunca 404; el signup sin M2M es un **503 fijo** registrado en otra rama | `http.go:59-66,163-171`, `constitucion.md` trampa 3 | tests de montaje en `apipublica` |
| T-10 | **Rol con `tenant_id` NULL vale en todas las empresas**; el transversal se distingue por **ID**, no por nombre (cualquiera puede crear un rol llamado `platform_admin`) | constitución I-CP-9; `rol_transversal_integration_test.go:37-87` | R-U20, suite `ContratoRoleRepo` (nil **y** `""`) |
| T-11 | **Fail-closed invertido** en `multi_empresa`: «si no se puede resolver, no se concede» = **mantener el rechazo** del alta | `memberships_test.go:166`, `multi_empresa_integration_test.go:124` | R-U24 |
| T-12 | **`platformadmin` re-declara `systemWappPlatform`** para no importar `usecase` (arrastraría el canje) | `platformadmin/access_requests.go:21-26` | mantener la re-declaración; no importar `A/iam/usecase` desde `A/platformadmin` |
| T-13 | **`iam/infra/memory` no es solo de tests en el árbol**: es un paquete de producción que nadie importa en producción (fan-in vacío) | `go list` inverso | se reconstruye igual (tiene `x_test.go`), y su cobertura cuenta |
| T-14 | **`unused` rompe el rojo** con no exportados sin uso | T-1 de [F1](../F1-nucleo-contact/reglas.md) | rojo solo con exportados; el puente con `var _ viejo.X = (*y)(nil)` |
| T-15 | **Candados AST que miran un fichero por nombre** vigilan una pared si el fichero se renombra | `canje_una_consulta_ast_test.go:37` (`ficheroDelCanje = "canje.go"`) | mantener los nombres `canje.go`, `memberships.go`; el candado exige encontrar lo que busca (guarda anti-hueco) |

## 3 · Prohibiciones

- 🚫 `t.Skip` (cualquier motivo); 🚫 tocar Postgres desde un test de fichero (E-6).
- 🚫 Portar tests viejos tal cual; se leen (E-8, [`diseno.md`](diseno.md) §4).
- 🚫 Importar desde `acceso` cualquier paquete de otro módulo o del código viejo: no hace falta ni un
  puente (arquitectura §3). Uno que aparezca es un defecto, no un puente a declarar.
- 🚫 Un segundo `entitlements.NewPostgres` en `internal/arranque`.
- 🚫 Un `RequireFeature(…, FeatureMultiEmpresa)` o `FeatureAPILLM` en una ruta de captación o de
  miembros (`entitlements.go:146-172`: sería un defecto).
- 🚫 Cambiar un texto de §5 de [`diseno.md`](diseno.md), aunque «suene mejor».

## 4 · Definición de hecho de F2

1. `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/acceso | wc -l` → **0**.
2. `GOWORK=off make ci-local` → `GATE_RC=0` leído del log (skill `validar-antes-de-cerrar`), con
   golangci-lint `v2.12.2` (o la decisión T-1 del marco).
3. `go test -v ./internal/modulos/acceso/... 2>&1 | grep -c -- '--- SKIP'` → **0**.
4. `make cobertura-ficheros` → ≥ 80 % en cada fichero de `acceso` salvo los Postgres
   (`postgres.go`, `*_postgres.go` y los de `iam/infra/postgres/`).
5. Las 7 suites de `outhelpertest` + `ContratoResolver` + la de `platformadminhelpertest` verdes contra sus dobles.
6. Los 3 candados AST verdes en `A/iam/infra/postgres/` y el I-CP-5 verde en `internal/arranque`.
7. `huella_test.go` igual; `go list -deps ./cmd/server-modular` con `internal/modulos/acceso` y sin
   `internal/entitlements`/`internal/platformadmin`; `cmd/server` intacto.
8. `puente_iam_test.go` verde; `internal/modulos/fronteras_test.go` sin puentes de `acceso`.
9. `go vet -tags integracion ./...` rc=0 (las suites Postgres compilan); su corrida anotada «no
   corrida» o hecha por la local (T2.33 = T9.23, D-F9-1).
10. `ESTADO.md` y este `README.md` actualizados; traspaso escrito si queda algo 💻.
