# F2 · Requisitos — historias y criterios EARS

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2. `A` =
> `internal/modulos/acceso`. Cada criterio nombra el comando o el test que lo verifica.

## H2.1 · Cada fichero nace con su contrato y su test

> Como **la sesión web**, quiero que cada fichero de `acceso` nazca con su contrato y el test que lo
> valida, con la ceremonia de su nivel (`05` E-12): en una pasada si es simple, rojo antes que verde si
> es medio o complejo.

- **R2.1.a** · **EL** contrato de cada fichero de `A/**` **DEBERÁ** contener solo comentario de
  paquete, cabecera `// Porta <ruta vieja> @ <sha>`, tipos, interfaces, constantes, centinelas y
  firmas **exportados**, con cuerpos `panic(pendiente.Implementar("<paq>.<Func>"))`. — Verifica:
  `golangci-lint run ./internal/modulos/acceso/...` sin `unused` (T-1 de F1) y
  `grep -rn 'return nil, nil\|return "", nil\|return false, nil' internal/modulos/acceso` vacío en rojo.
  En el nivel **simple** no hay commit de rojo: contrato, test y lógica llegan juntos.
- **R2.1.b** · **EL** test `x_test.go` de cada fichero **DEBERÁ** llevar `//go:build pendiente` en
  rojo, mencionar todo exportado de `x.go` y traer **un test por promesa del contrato**. **EL** test de un
  auxiliar no exportado **DEBERÁ** nacer en el verde, y solo si lleva regla de negocio o ramas no triviales
  (`05` E-4). — Verifica: `exportados_cubiertos_test.go` en `ci-local`.
- **R2.1.c** · **SI** un test de `A/**` llama a `t.Skip`, **ENTONCES EL** gate **DEBERÁ** fallar. —
  Verifica: `grep -rn 't.Skip' internal/modulos/acceso` vacío; `go test -v … | grep -c -- '--- SKIP'` → 0.
- **R2.1.d** · **CUANDO** se cierre una sesión con ficheros en rojo, **EL** recuento **DEBERÁ** coincidir:
  `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/acceso | wc -l` = lo que diga
  `make test-pendiente` para `acceso`, y `go vet -tags pendiente ./...` rc=0. — Verifica: esos dos
  comandos, en T2.8, T2.16, T2.21 y T2.27.
- **R2.1.e** · **ANTES** de escribir código, **EL** inventario E-12 (tabla de niveles por archivo y lista de
  adaptadores `bridge_<x>.go`) **DEBERÁ** estar aprobado por Jhoan. — Verifica: T2.1 `[x]` y [`diseno.md`](diseno.md) §1.1.

## H2.2 · Los puertos con BD nacen cubiertos (suite en memoria y en Postgres)

> Como **la sesión web**, quiero una suite de contrato por puerto con BD, corrida por su doble en memoria
> y por el adaptador Postgres con el arnés, para que memoria y Postgres se comporten igual (P4).

- **R2.2.a** · **EL** paquete `A/iam/ports/out/outhelpertest` **DEBERÁ** exportar una suite por puerto
  (`ContratoMembershipRepo`, `ContratoRoleRepo`, `ContratoGrantRepo`, `ContratoAuditRepo`,
  `ContratoInvitationRepo`, `ContratoActiveTenantRepo`, `ContratoInvitationRedeemRepo`) con la firma
  `func(t *testing.T, nuevo func(t *testing.T) Montaje…)` que fije D-F1-1. — Verifica: `go doc ./internal/modulos/acceso/iam/ports/out/outhelpertest`.
- **R2.2.b** · **EL** doble `A/iam/infra/memory` **DEBERÁ** pasar las 7 suites con `-race` y 0 SKIP.
  — Verifica: `go test -race -v ./internal/modulos/acceso/iam/infra/memory/ | grep -c -- '--- FAIL\|--- SKIP'` → 0.
- **R2.2.c** · **TODO** puerto con BD de `acceso` (los 7 de `outhelpertest`, `entitlements.Resolver` y los dos
  de `platformadmin`) **DEBERÁ** correr su suite `Contrato(t, func(t) Montaje)` también contra Postgres, desde un
  test con `//go:build integracion` y el arnés de F9-A (testcontainers); la marca de estado del `Montaje`
  **DEBERÁ** vigilar todas las columnas que la operación puede tocar. — Verifica:
  `go vet -tags integracion ./internal/modulos/acceso/...` rc=0 (la web) · la corrida, sin divergencias (T2.33).
- **R2.2.d** · **DONDE** el puerto sea un cliente de identity (`IdentityClient`, `IdentityM2MClient`,
  `UserSystemsClient`), **EL** cliente real **DEBERÁ** probarse contra un `httptest.Server` que imita
  identity, sin red. — Verifica: `go test ./internal/modulos/acceso/iam/infra/identity/` rc=0.
- **R2.2.e** · **EL** paquete `A/platformadmin` **DEBERÁ** declarar sus puertos (D-F2-3) y
  `A/platformadmin/platformadminhelpertest` **DEBERÁ** traer suite y doble en memoria. — Verifica: los tests
  de `handlers.go`, `access_requests.go` y `signup.go` corren sin BD.

## H2.3 · El canje y los permisos no cambian de conducta

> Como **la dueña del negocio**, quiero que entrar, elegir empresa, invitar y canjear se comporten
> igual, para no notar la reconstrucción.

- **R2.3.a** · **EL** canje Identity Token → Context Token **DEBERÁ** conservar las reglas E-8 de
  [`diseno.md`](diseno.md) §4 (usecase/exchange): el Context Token nunca sobrevive al Identity Token;
  sin membresía → token **sin** empresa y **sin** grants; varias empresas sin elegir → sin empresa; la
  empresa activa guardada que ya no es suya no da empresa; un fallo leyendo la empresa activa
  **corta** (no degrada). — Verifica: `go test -run 'Exchange' ./internal/modulos/acceso/iam/usecase/`.
- **R2.3.b** · **SI** una invitación no existe o está caducada, **ENTONCES EL** transporte **DEBERÁ**
  responder cuerpos **byte a byte idénticos** (anti-oráculo) y el adaptador **DEBERÁ** hacer una sola
  consulta en `leerInvitacion`. — Verifica: `TestCanje_NoExisteYCaducadaSonIndistinguibles` nuevo y el
  candado AST portado (D-F2-1).
- **R2.3.c** · **EL** único escritor de `public.tenant_members` del código nuevo **DEBERÁ** ser
  `A/iam/infra/postgres/memberships.go`, con cerrojo `pg_advisory_xact_lock` antes de la guarda
  `countOtherMemberships`, y la guarda antes del `INSERT`, en el cuerpo de la función. — Verifica:
  candado `membresia_unica_ast_test.go` portado (y el viejo, ciego al árbol nuevo desde F0 T0.27, D-F4-1).
- **R2.3.d** · **EL** gate de features **DEBERÁ** ser fail-closed: sin identidad, con resolver caído o
  con resolver nil responde **403** `{"error":"feature_not_enabled","feature":"<clave>"}`; el plural
  corta en el primer error y una lista vacía **no abre**. — Verifica: `middleware_test.go` nuevo.
- **R2.3.e** · **EL** candado I-CP-5 **DEBERÁ** seguir detectando las 8 rutas de plataforma tras la
  conmutación: el arranque nuevo importa el paquete nuevo **con el nombre `platformadmin`** (sin alias).
  — Verifica: `TestINV056_1_PlatformPermissionsMustEndInDotAny` y su caso negativo en `internal/arranque`.

## H2.4 · Una sola verdad sobre los derechos

> Como **Jhoan**, quiero una sola instancia del resolver de derechos en el binario nuevo, para que no
> haya dos cachés con TTL y dos respuestas a «¿qué tiene contratado este tenant?».

- **R2.4.a** · **EL** arranque nuevo **DEBERÁ** construir **una** `acceso/entitlements.Postgres` y
  pasarla a todos sus consumidores, viejos y nuevos (el puerto viejo es estructural). — Verifica:
  `grep -rn 'entitlements.NewPostgres' internal/arranque | wc -l` → 1 y el test de cableado de T2.29.
- **R2.4.b** · **MIENTRAS** el arranque nuevo corra, **NINGÚN** paquete viejo `internal/entitlements`
  **DEBERÁ** instanciarse en él. — Verifica: `grep -rn '"…/internal/entitlements"' internal/arranque` vacío.
- **R2.4.c** · **EL** `CacheTTL()` del resolver nuevo **DEBERÁ** devolver 60 s por defecto
  (`defaultCacheTTL`) y `GET /api/v1/entitlements` **DEBERÁ** publicarlo como hoy. — Verifica: test de
  `postgres.go` y la huella.

## H2.5 · Conmutar sin que la dueña ni el Edge lo noten

> Como **el Edge**, quiero que el login del operador por el canal de control siga respondiendo igual
> mientras el gateway es aún el viejo, para no perder la consola local.

- **R2.5.a** · **CUANDO** el arranque nuevo conmute `acceso`, **EL** gateway viejo **DEBERÁ** recibir
  un `in.Authenticator` e `in.Auditor` **viejos** implementados por el adaptador `internal/arranque/bridge_iam.go`
  sobre los usecases nuevos, traduciendo DTOs y **centinelas** (`ErrInvalidCredentials`,
  `ErrUserInactive`, `ErrRefreshInvalid`, `ErrInvalidInput`). — Verifica: `bridge_iam_test.go`
  (equivalencia de los cuatro centinelas con `errors.Is` contra el viejo).
- **R2.5.b** · **EL** binario nuevo **DEBERÁ** servir por `internal/apipublica` las 23 rutas F2 del
  mapa FX (A1–A7, B1–B14, C1–C2) y por los handlers nuevos las 8 de `:8100` (J4–J11). — Verifica:
  `internal/arranque/huella_test.go` y el listado de rutas de `apipublica`.
- **R2.5.c** · **EL** binario viejo (`cmd/server`) **NO DEBERÁ** cambiar. — Verifica:
  `git diff --stat <sha-inicio-F2>..HEAD -- cmd/server internal/bootstrap` vacío.
- **R2.5.d** · **EL** binario nuevo **DEBERÁ** enlazar `internal/modulos/acceso/...` y **no** enlazar
  `internal/iam/...`, `internal/entitlements` ni `internal/platformadmin`… salvo a través de los
  paquetes viejos aún no reconstruidos que los importan (gateway viejo hasta F3). — Verifica:
  `go list -deps ./cmd/server-modular | grep -E 'internal/(iam|entitlements|platformadmin)'` → solo
  `internal/iam/{domain,ports/in}` (tipos del adaptador, arquitectura §4).
  ✎ **Corregido en F2-04 (hallazgo 37)**: esa salida es imposible mientras `publicapi`, `flujos/{events,runtime}` y
  `reanalisis` importen el `internal/entitlements` viejo y `publicapi` el `internal/iam/transport/http` viejo. Se verifica
  por la cláusula: `go list -deps -f '{{.ImportPath}}: {{join .Imports " "}}' ./cmd/server-modular` → todo importador de
  un paquete viejo de `acceso` es un paquete viejo sin reconstruir o `internal/arranque`, y en éste solo `bridge_iam.go`
  (`access_wiring_test.go`).
- **R2.5.e** · **SI** falta `WAPP_IDENTITY_API_KEY`, **ENTONCES** `POST /api/v1/members` **DEBERÁ**
  responder 503 (nunca 404) y `POST /api/v1/signup` el 503 fijo `registro no disponible`. — Verifica:
  tests de `apipublica` y de `A/iam/transport/http/roles.go`.
- **R2.5.f** · **EL** test de cableado **DEBERÁ** afirmar que el arranque construye el resolver, el autenticador y
  el auditor **nuevos**, y que ninguna fase de `internal/arranque` importa `internal/iam/...`,
  `internal/entitlements` ni `internal/platformadmin` fuera de `bridge_iam.go`. **EL** módulo `acceso` **NO DEBERÁ**
  entrar en `Conmutados` hasta que muera ese adaptador (F3). — Verifica: T2.29 y `internal/modulos/fronteras_test.go`.

## H2.6 · Los procesos de acceso (F9) tienen su guion

> Como **la sesión local**, quiero saber qué proceso de F9 hereda cada regla que en F2 no se puede
> probar sin BD, para cerrarla contra los dos binarios.

- **R2.6.a** · **EL** `diseno.md` §6 **DEBERÁ** listar las reglas que pasan al proceso «Canje de
  identidad y permisos» (`05` §7.4), incluido I-CP-5 con un admin de cliente denegado en una ruta `.any`.
  — Verifica: la lista existe y `plan/F9-procesos/` la referencia (T2.33).
- **R2.6.b** · **DONDE** F9 esté adelantado, **EL** cierre de F2 **DEBERÁ** correr esos procesos contra
  `cmd/server-modular`. — Verifica: `make test-procesos` en local (T2.33 = T9.23).
