# F2 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo` (la fase), `contrato-tdd` (cada fichero), `validar-antes-de-cerrar` (cada
> gate), `traspaso-web-local` (solo mientras haya dos entornos), `procesos-testcontainers` (T2.33). `A` =
> `internal/modulos/acceso`, `V` = paquete viejo. Todo gate se lee **sin pipe** (el `rc` a un log y se
> lee del log). La web trabaja en su rama y abre PR hacia `dev` (`--base dev`), integrado **sin
> squash**: rojo y verde, donde los hay, siguen siendo commits distintos. Gate estándar de rojo (**G-rojo**):
> `GOWORK=off go vet -tags pendiente ./internal/modulos/acceso/...; echo rc=$?` → `0`. Gate estándar
> de verde (**G-verde**): `GOWORK=off go test -race ./<paquete>/ > "$TMPDIR/t.log" 2>&1; echo rc=$? >> "$TMPDIR/t.log"; tail -1 "$TMPDIR/t.log"` → `rc=0`,
> 0 SKIP · un test por promesa del contrato · `make cobertura-ficheros` como **informe** (la tabla va al PR; no bloquea).
>
> **Niveles (`05` E-12)**: los fija el inventario de T2.1, **aprobado por Jhoan el 2026-10-04** ([`diseno.md`](diseno.md) §1.1,
> definitivo). Cambian respecto a lo provisional: `iam/infra/memory` (T2.9) **simple**, nace completo (P1; T2.20 solo pasa el
> gate) · `iam/usecase/{config,audit,context_token}.go` (T2.11) **simple** · `iam/infra/postgres/postgres.go` (T2.12) **simple**
> y sus 5 adaptadores sin Tx más `platformadmin/postgres.go` (T2.15) **medio** (P2) · `iam/domain` los 4 **simples** (P4).
> Las tareas conservan su número. En un paquete **simple**, la tarea «contrato» trae contrato, test y lógica en una
> pasada (commit `verde(acceso): …`) y su tarea «verde» solo pasa el gate. En **medio**, rojo y verde por fichero,
> agrupados por paquete. En **complejo**, el esquema completo E-2…E-9 con mutantes donde haga falta. Si un fichero sale
> peor de lo previsto, sube de nivel y se anota aquí.
>
> **Auxiliares no exportados (`05` E-4, P6)**: nacen en el verde. Su test nace también en el verde y **solo si llevan
> regla de negocio o ramas no triviales**; no se testea fontanería ni `if err != nil`; el resto lo cubre F9.
>
> **Nombres (E-11, `05` §4.2)**: el adaptador de arranque es `bridge_iam.go` + `bridge_iam_test.go` (esta spec lo nombraba
> con el prefijo `puente`) · tipos: `puenteAutenticador` → `authenticatorBridge` · `puenteAuditor` → `auditorBridge`.
>
> **Cierre de toda sesión**: tareas `[x]` con SHA · un bloque en `ESTADO.md` · hallazgos nuevos en el [README](README.md).

## Sesión F2-01 · inventario E-12 y hojas simples · 🌐 · T2.1, T2.34, T2.2–T2.3, T2.5–T2.9, T2.17–T2.21
Para cuando: inventario aprobado por Jhoan · `auth.go` partido con la huella intacta · `entitlements` (sin `postgres.go`), `iam/domain`, `iam/ports/{in,out}`, las 7 suites e `iam/infra/memory` en verde · `ci-local` rc=0 con 0 SKIP · PR.

> **Correspondencias de nombres (E-11) de F2-01**: `TestRequireFeature_SinIdentidad` → `TestRequireFeature_NoIdentity` ·
> `FeatureMultiEmpresa` → `FeatureMultiCompany` (valor `"multi_empresa"`) · `entitlementshelpertest/suite.go` → `contrato.go`
> (`_contrato` es vocabulario del método, D-F1-12) · `outhelpertest/{montaje,membresias,roles,grants,auditoria,invitaciones,empresa_activa,canje}`
> → `contrato.go`, `memberships_contrato.go`, `roles_contrato.go`, `grants_contrato.go`, `audit_contrato.go`,
> `invitations_contrato.go`, `active_tenant_contrato.go`, `redeem_contrato.go` (suites `ContratoMembershipRepo`… y
> `Contrato(t, Montajes)`) · `iam/domain`: `RolTransversalID` → `TransversalRoleID`, `EvaluarCanje` → `EvaluateRedemption`,
> `ResultadoCanje` → `RedemptionVerdict`, `CanjeProcede/Ausente/Caducado/Consumido` → `RedemptionProceeds/Missing/Expired/Consumed`
> · `iam/infra/memory`: `ConFeatures` → `WithFeatures`, `SeedAsignacion` → `SeedAssignment` · candado nuevo
> `internal/candados/inbound_ports.go` (`InboundPortDirsWithoutSuite`). Los ficheros portados conservan el nombre del viejo
> (`canje.go`).

- [x] **T2.1 · Inventario E-12, verdad de campo y re-medición** · 🌐 · dep. F1 cerrado, parada resuelta, F1-06 y F9-04 · cumple R2.1.d, R2.1.e — cerrada en `1d73874` (inventario aprobado por Jhoan el 2026-10-04 con P1–P4; `GATE_RC=0`)
  - **Ficheros**: `plan/F2-acceso/README.md` (estado «en curso», SHA de arranque, respuestas D-F2-*), [`diseno.md`](diseno.md) §1.1 (la tabla definitiva)
  - **Qué produce**: la tabla `archivo · estado en memoria · concurrencia · BD/transacciones · nº de consumidores · nivel (simple/medio/complejo)` de los 51 + 6 ✚ ficheros, medida sobre el código viejo, y la **lista de adaptadores**: nace `bridge_iam.go` (muere en F3); F2 no retira ninguno.
  - **Hecho cuando**: **Jhoan aprueba la tabla. Antes de eso no se escribe código.** Además: las entradas del README comprobadas con su comando; tabla §1 de arquitectura re-medida (si difiere, se corrige **aquí** y se dice en el commit); `git log 1b18932..origin/dev -- internal/iam internal/entitlements internal/platformadmin` revisado; D-F2-1…D-F2-7 con respuesta de Jhoan (o la recomendación marcada «asumida» si Jhoan lo delegó). **Verificado D-F4-1** (lo hizo F0, T0.27): `grep -c 'SkipDir' internal/iam/infra/postgres/membresia_unica_ast_test.go` → ≥ 1 y el commit `andamiaje(f0): los barridos AST viejos…` en `git log origin/dev`; si falta, **parar** (sin él el verde de `memberships.go` pone rojo `ci-local`).
  - **Gate**: `GOWORK=off make ci-local > "$TMPDIR/g.log" 2>&1; echo GATE_RC=$? >> "$TMPDIR/g.log"; tail -1 "$TMPDIR/g.log"` → `GATE_RC=0`
  - **Commit**: `docs(reorganizacion-modular): F2 arranca — inventario E-12 y entradas verificadas`

- [x] **T2.34 · refactor(arranque): `auth.go` se parte en `auth_*.go` y `edge_config.go`** · 🌐 · dep. F0 cerrada (T0.25), T2.1 aprobado · decisión **D-F2-8** *(añadida el 2026-09-30, revisión del PR de F0-04; el ID no se renumera)* — cerrada en `f46a107` (multiconjunto de las 31 declaraciones igual; huella idéntica; 359 PASS, 0 SKIP en `internal/arranque`; `GATE_RC=0`; ficheros 268/233/178/106/57/57 l)
  - **Por qué**: la copia `internal/arranque/auth.go` (835 l) mezcla seis cosas y **crece**: la editan T2.31 (`conmutar(acceso)`) y F3 (los config providers con el `ConfigPayload` nuevo), y es el código que sobrevive a F10. En F0 no se pudo partir: R0.4.b exige copia exacta del viejo hasta cerrar F0.
  - **Ficheros** (solo en `internal/arranque`; **el viejo no se toca**), por símbolos:
    - `auth_stack.go`: `identityTokenIssuer`, `authStack`, `buildAuthStack`, `wireIdentityM2M`, `wireDelegatedAuth`, `edgeAuthenticator`, `exchanger`, `buildIdentityVerifier`;
    - `auth_jwt.go`: `defaultES256Kid`, `userJWTBundle`, `buildJWTManagers`, `buildES256Key`, `parseECP256PrivateKeyPEM`, `validateP256`, `buildJWKSConfig`;
    - `edge_config.go` (**sin** prefijo `auth_`: es la cadena de configuración que se empuja al Edge, no autenticación, y la reescribe F3): `jwksConfigProvider`, `intentConfigStore`, `intentsConfigProvider`, `filtersConfigProvider`, `chainLink`, `chainConfigProvider`, `logConfigLinkError`, `buildConfigProvider` (los cuatro métodos `ConfigsForConnect` van con su tipo);
    - `auth_roleplane.go`: `rolePlane`, `buildRolePlane`;
    - `auth_invitaciones.go`: `buildInvitationRedeem`;
    - `auth_empresa_activa.go`: `buildActiveTenantPlane`;
    - se borra `internal/arranque/auth.go`; `invitaciones_cableado_test.go:33` pasa a parsear ~~`auth_invitaciones.go`~~ **`auth_roleplane.go`** (✎ T2.1, P3 de Jhoan, 2026-10-04: la llamada que busca, `iamusecase.NewInvitationService`, vive en `buildRolePlane`, no en `buildInvitationRedeem`) (el candado lee el fichero **por nombre**: sin este cambio falla ruidoso); el comentario de `es256_key_test.go:229` nombra `auth_jwt.go`. Si algún símbolo no está en esta lista, va con el que lo usa y se dice en el commit.
  - **Cómo**: solo **mover** declaraciones, sin cambiar un byte de ninguna (comentarios incluidos); imports por fichero con `goimports`; cabecera de cada fichero `// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: <tema>.`
  - **Hecho cuando**: el multiconjunto de declaraciones de primer nivel (texto de `go/printer`, con su comentario) de los seis ficheros es **igual** al de `auth.go` antes del corte; ningún fichero nuevo pasa de 300 líneas; `go test -run '^TestHuella' ./internal/arranque/ ./internal/bootstrap/arranque/` → rc=0 (huella idéntica a la dorada); `go test -race -v ./internal/arranque/...` → rc=0, 0 SKIP; `git diff --stat -- internal/bootstrap cmd/server` vacío.
  - **Gate**: gate ci-local en un *worktree* limpio de **ruta fija** (contradicción 21 de F0) → `GATE_RC=0`
  - **Commit**: `refactor(arranque): auth.go se parte en auth_*.go y edge_config.go`

- [x] **T2.2 · `entitlements/entitlements.go` + `entitlementshelpertest`** · 🌐 · simple (prov.) · dep. T2.1 · cumple R2.1.a–b, R2.3.d — cerrada en `01950de` (simple, una pasada; suite `ContratoResolver` 13 casos contra el `Fake`)
  - **Ficheros**: `A/entitlements/entitlements.go`, `…/entitlements_test.go`, `A/entitlements/entitlementshelpertest/{suite.go,fake.go,fake_test.go}`
  - **Hecho cuando**: las 11 constantes con su valor **literal** y su comentario-ADR (R-E5), `Resolver` con su promesa; `ContratoResolver` con los casos de diseño §2 y la firma `Contrato(t, func(t) Montaje)`; `Fake` (D-F2-4) nace **completo** y pasa la suite en verde (es un doble, D-F1-3).
  - **Gate**: G-verde · `go test -race ./internal/modulos/acceso/entitlements/entitlementshelpertest/; echo rc=$?` → 0
  - **Commit**: `verde(acceso): entitlements y su suite`
- [x] **T2.3 · `entitlements/middleware.go`** · 🌐 · medio (prov.) · dep. T2.2 · cumple R2.3.d — cerrada en `43704f1` (rojo) → `3742090` (verde); 5 mutantes muertos
  - **Ficheros**: `A/entitlements/middleware.go`, `…/middleware_test.go`
  - **Hecho cuando**: `RequireFeature`/`RequireAnyFeature` con R-E1…R-E4 en el comentario; el test cubre los 3 modos fail-closed, el corte en el primer error, la lista vacía y los dos cuerpos **byte a byte**. Rojo: `go test -tags pendiente -run '^TestRequireFeature_SinIdentidad' ./internal/modulos/acceso/entitlements/; echo rc=$?` → ≠0.
  - **Gate**: G-rojo y, tras la lógica, G-verde
  - **Commit**: `rojo(acceso): contrato de entitlements/middleware` · `verde(acceso): entitlements/middleware`
- [x] **T2.5 · `iam/domain` (4 ficheros)** · 🌐 · simple (prov.) · dep. T2.1 · cumple R2.1.a–b — cerrada en `30345e7`, `e64cc3a`, `9b4e407`, `2868e61` + `2776d82` (refactor E-11); 50 exportados, 20 centinelas literales, 12/12 mutantes
  - **Ficheros**: `A/iam/domain/{canje,entities,errors,invitation}.go` y sus 4 `_test.go`
  - **Hecho cuando**: 50 exportados; los 20 centinelas con texto literal (diseño §5); R-D1…R-D5 en comentario y test (largo **exacto** del token, 32 bytes del digest, simetría de normalización, precedencia de estados, 4 veredictos). Los casos de equivalencia de `HashInvitationToken` (R-D3) llevan entradas adversarias ([`reglas.md`](reglas.md) §5).
  - **Gate**: G-verde
  - **Commit**: `verde(acceso): iam/domain/<fichero>` — uno por fichero
- [x] **T2.6 · `iam/ports/out` + `outhelpertest` (las 7 suites)** · 🌐 · simple (prov.) · dep. T2.5 · cumple R2.2.a — cerrada en `8290814` (7 suites, 63 casos, y `Contrato(t, Montajes)` que las corre todas)
  - **Ficheros**: `A/iam/ports/out/{active_tenant,canje,repos}.go` (sin `_test`, E-3), `A/iam/ports/out/outhelpertest/{montaje,membresias,roles,grants,auditoria,invitaciones,empresa_activa,canje}.go`
  - **Hecho cuando**: las 10 interfaces con su comentario-contrato; las 7 suites (diseño §2) con la firma de D-F1-1, casos con nombre **en inglés** (`05` E-11; decía «en español» hasta la revisión del 2026-10-01), sin `t.Skip`; `go doc` las muestra. La marca de estado de cada `Montaje` vigila **todas** las columnas que la operación puede tocar (hallazgo 35 de F1).
  - **Nombres (E-11)**: `outhelpertest/` aún no existe y esta tarea nombra en español seis de sus ficheros (`montaje`, `membresias`, `auditoria`, `invitaciones`, `empresa_activa`, `canje`): la sesión que los cree los escribe en inglés y anota aquí la correspondencia (`05` E-11; si `montaje` cuenta como vocabulario del método, depende de D-F1-12 del README de F1). `ports/out/canje.go` conserva el nombre del fichero viejo.
  - **Gate**: `go vet ./internal/modulos/acceso/...; echo rc=$?` → 0
  - **Commit**: `verde(acceso): puertos de salida del IAM y sus suites de contrato`
- [x] **T2.7 · `iam/ports/in`** · 🌐 · simple (prov.) · dep. T2.5 · cumple R2.1.a–b — cerrada en `354f060`, con la excepción del candado `4e97ee3` (rojo) → `1d3b10b` (verde) por D-F2-5 (ver README, hallazgo 10)
  - **Ficheros**: `A/iam/ports/in/{active_tenant,canje,usecases}.go`, `…/usecases_test.go`
  - **Hecho cuando**: `AuditInput` es **alias** del DTO de `platform/httpapi` (F0); `CallerResolverFunc.Caller` delega en la función; el test menciona los 25 exportados de `usecases.go`; excepción de suite para `ports/in` escrita en el comentario de paquete (D-F2-5).
  - **Gate**: G-verde
  - **Commit**: `verde(acceso): puertos de entrada del IAM`
- [x] **T2.8 · Punto de control: hojas y puertos** · 🌐 · dep. T2.2–T2.7 — cerrada sobre `354f060` (sin commit): ver T2.21
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/acceso | wc -l` anotado y = `make test-pendiente`; `ci-local` rc=0; `fronteras_test` sin puentes (import) de `acceso`; minutos anotados. **Punto limpio de corte** si la sesión no cabe en ~90 min: se cierra con las tres cosas y se relanza.
  - **Gate**: skill `validar-antes-de-cerrar` completa
  - **Commit**: —
- [x] **T2.9 · `iam/infra/memory` (7 + `redeem_store.go` ✚)** · 🌐 · simple (prov.: dobles, D-F1-3) · dep. T2.6 · cumple R2.2.b — cerrada en `13d4171`, `d551edf`, `1093835`, `8009cc6`, `930e7d5`, `1cebb56`, `feae908`, `38a8d0b` (simple, P1; 8/8 mutantes)
  - **Ficheros**: `A/iam/infra/memory/*.go` y 8 `_test.go` (cada uno ejecuta **su** suite de `outhelpertest`)
  - **Hecho cuando**: `var _ out.X = (*Y)(nil)` para los 7 puertos; `RedeemStore` documenta cómo reproduce los 4 pasos del canje en memoria; nacen completos, en una pasada. Si el inventario los sube de nivel (tienen estado en memoria), el rojo va con structs **sin campos** (T-14) y métodos con `panic`, y el verde es T2.20.
  - **Gate**: G-verde
  - **Commit**: `verde(acceso): iam/infra/memory/<fichero>` — uno por fichero
- [x] **T2.17 · verde de `entitlements/entitlements.go` y `middleware.go`** · 🌐 · dep. T2.2, T2.3 · cumple R2.3.d · si llegaron verdes en T2.2–T2.3, aquí solo se pasa el gate · **Gate**: G-verde (base vieja: `go test -cover ./internal/entitlements/`, anotarla como informe) — cerrada en `3742090` (base vieja `go test -cover ./internal/entitlements/` 73,4 %, informe; nuevo `middleware.go` 91,6 %, `fake.go` 100 %)
- [x] **T2.18 · verde de `iam/domain/*`** · 🌐 · dep. T2.5 · ídem · **Gate**: G-verde — cerrada en `2776d82` (`canje.go` 100 %, `invitation.go` 90,9 %)
- [x] **T2.19 · verde de `iam/ports/in/usecases.go`** · 🌐 · dep. T2.7 · ídem · **Gate**: G-verde — cerrada en `354f060` (100 %)
- [x] **T2.20 · verde de `iam/infra/memory/*`** · 🌐 · dep. T2.9 · cumple R2.2.b · **Hecho cuando**: las 7 suites verdes con `-race -v` y 0 SKIP · **Gate**: G-verde — cerrada en `38a8d0b` (las 7 suites verdes con `-race -v`, 0 SKIP; 97,7–100 % por fichero)
- [x] **T2.21 · Cierre de F2-01** · 🌐 · dep. T2.17–T2.20 · **Hecho cuando**: 0 pendientes en `entitlements` (salvo `postgres.go`, que aún no existe), `iam/domain`, `iam/ports/in`, `iam/infra/memory`; `exportados_cubiertos` y `un_fichero_un_test` verdes; las tres cosas del cierre · **Gate**: `validar-antes-de-cerrar` · **Commit**: — (PR) — cerrada sobre `354f060`: `make ci-local` `GATE_RC=0` (99 `ok`, lint 0 issues) · `go vet -tags pendiente ./...` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` · `go test -race -v ./internal/{modulos,arranque,candados}/...` rc=0, 1.210 PASS, **0 SKIP** · `grep pendiente.Implementar internal/modulos/acceso` → 0 · `exportados_cubiertos` (147 recorridos) y `un_fichero_un_test` (98) verdes · `fronteras_test` sin puentes de `acceso` · `make cobertura-ficheros` (informe) `FICHEROS_EVALUADOS=32`, `POR_DEBAJO=1` (`nucleo/contact/repository_postgres.go`, previo) · ~81 min activos (D-R-6, ESTADO)

## Sesión F2-02 · `usecase` e `identity` · 🌐 · T2.10–T2.11, T2.16, T2.22–T2.23
Entrada: PR de F2-01 integrado. Para cuando: 0 pendientes en `iam/usecase` e `iam/infra/identity` · `ci-local` rc=0 con 0 SKIP · PR.

> **Correspondencias de nombres (E-11) de F2-02**: `WithReloj` (D-F2-6, nombre que la spec daba a un símbolo que aún no
> existía) → **`WithClock`**, con su tipo `M2MOption` y `NewM2M(…, opts ...M2MOption)` variádico (convención de la casa:
> `memory.Store.WithClock`) · auxiliares de `iam/usecase`: `tenantEfectivo` → `effectiveTenant` y `esMiembro` → `isMember`
> (viven en `exchange.go`, su primer consumidor en verde; `active_tenant.go` los reutiliza), `vidaDe` → `lifetimeOf`,
> `rolPedido` → `requestedRole`, `exigirRolVisible` → `requireVisibleRole`, `ttlInvitacion{PorDefecto,Minimo,Maximo}` →
> `invitationTTL{Default,Min,Max}`, `acreditar` → `accredit`, `anotarFallo` → `recordFailure`, `pasoLeer/pasoDeclarar` →
> `stepRead/stepDeclare` (los valores literales `"leer_accesos"`/`"declarar_accesos"` no cambian) · tests: los invariantes
> cruzados son `TestSelectorAndExchangeNeverDisagree` (R-U18), `TestExchange_ActiveTenantReadFailureAborts` (R-U6) y
> `TestLogoutAll_PartialFailureClosesRotatedSession` (R-U12). Reparto de R-U30…R-U33: `audit` R-U30, `context_token` R-U31,
> `grants` R-U32, `config` R-U33.
>
> **Desviaciones de orden (aplicadas, ver README hallazgos 19–20)**: los tres simples (`config`, `context_token`, `audit`)
> nacieron completos **antes** del rojo de los medios, porque los contratos de `exchange` y `delegated_auth` usan `Config` y
> `TokenValidator`; `grants.go` (0 exportados) no tiene rojo: nace entero en su verde. El resto del verde sigue el orden de T2.23.

- [x] **T2.10 · rojo(acceso): `iam/infra/identity`** · 🌐 · `client.go` medio, `m2m.go` complejo (prov.) · dep. T2.6 · cumple R2.2.d — cerrada en `5a686b8` (`WithReloj` → `WithClock`, E-11; cabeceras corregidas a `@ 9a77307` en `86912f7`)
  - **Ficheros**: `A/iam/infra/identity/{client,m2m}.go`, `…/{client,m2m}_test.go` (con `httptest.Server` que imita identity)
  - **Hecho cuando**: R-I1…R-I9 en comentario y test; `WithReloj` en el M2M (D-F2-6); textos `"iam: …"` literales.
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contratos de iam/infra/identity`
- [x] **T2.22 · verde(acceso): `iam/infra/identity/{client,m2m}.go`** · 🌐 · dep. T2.10 · cumple R2.2.d · 2 commits · **Hecho cuando**: verde sin red; un test por promesa (R-I1…R-I9); mutantes sobre la caché y el candado del M2M (R-I3, R-I5) · **Gate**: G-verde — cerrada en `39688bf` (client), `744dd2a` (m2m); 129 PASS, 0 SKIP, `-race -count=30` estable; mutantes del M2M 19 sembrados · 18 muertos · 0 vivos · 1 equivalente (umbral `>`→`>=` de `usableLifetime` con 60 s); cobertura 91,6 % · 95,4 %
- [x] **T2.11 · rojo(acceso): `iam/usecase` (11 ficheros)** · 🌐 · medio (prov.) · dep. T2.6, T2.7, T2.20 · cumple R2.3.a — cerrada en `803cd77` (config), `0214e6a` (context_token), `79ded5c` (audit) —simples, una pasada (D-R-5)— y rojos `08cced4` (exchange), `4f3d718` (delegated_auth), `cbf703d` (active_tenant), `ff462e6` (roles), `5ad7390` (memberships), `563c7f9` (invitations), `482682e` (canje); 29 pendientes al cerrar el rojo; `grants.go` sin rojo (0 exportados)
  - **Ficheros**: `A/iam/usecase/*.go` y 11 `_test.go` (usan `infra/memory` y dobles propios de identity)
  - **Hecho cuando**: R-U1…R-U33 en los comentarios y una aserción por promesa; los tres invariantes cruzados (R-U18 selector ↔ canje, R-U6 corte, R-U12 mitigación) como tests con nombre propio.
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contrato de iam/usecase/<fichero>` — uno por fichero
- [x] **T2.23 · verde(acceso): `iam/usecase/*` (11)** · 🌐 · dep. T2.11 · cumple R2.3.a · 11 commits, en el orden `config`, `grants`, `context_token`, `audit`, `exchange`, `delegated_auth`, `active_tenant`, `roles`, `memberships`, `invitations`, `canje` · **Gate**: G-verde — cerrada en `d09ff88` (grants, nace entero), `e32e4b5` (exchange), `fe49dda` (delegated_auth), `505aeac` (active_tenant), `68696d9` (roles), `55a9a67` (memberships), `b743b50` (invitations), `93aeb9d` (canje) + `7295586` (test de la promesa de `Refresh` que el rojo no afirmaba) y `f0d777e` (comentario de `ports/in` con el nombre nuevo); config/context_token/audit llegaron verdes en T2.11; cobertura 87,0–100 % por fichero (informe)
- [x] **T2.16 · Cierre de F2-02** · 🌐 · dep. T2.22, T2.23
  - **Hecho cuando**: 0 pendientes en los dos paquetes; pendientes de `acceso` anotados; `ci-local` rc=0; `exportados_cubiertos` y `un_fichero_un_test` verdes; 0 SKIP; las tres cosas del cierre; PR.
  - **Gate**: `validar-antes-de-cerrar` · **Commit**: — (PR) — cerrada sobre `f0d777e`: `make toolchain` OK (go1.26.5, lint v2.12.2) · `make ci-local` `GATE_RC=0` (103 `ok`, lint 0 issues) · `make vet-pendiente` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` (pendientes de `acceso`: 0; quedan `entitlements/postgres.go`, `infra/postgres`, `transport/http`, `platformadmin`, que aún no existen) · `go test -v ./internal/{modulos,nucleo,arranque,candados}/...` rc=0, 1.852 PASS, **0 SKIP** · `cobertura-ficheros` (informe) 45 evaluados, 1 por debajo (`nucleo/contact/repository_postgres.go`, previo) · `go list -deps ./internal/modulos/acceso/...` sin `internal/{iam,entitlements,platformadmin,gateway,flujos,intake}`

## Sesión F2-03 · `infra/postgres`, `entitlements/postgres.go`, `transport/http`, `platformadmin` · 🌐 · T2.4, T2.12–T2.15, T2.24–T2.27
Entrada: PR de F2-02 integrado. Para cuando: 0 pendientes en `acceso` · candados AST verdes · suites compilan contra Postgres · PR.

> **Correspondencias de nombres (E-11) de F2-03**: `WithReloj` → `WithClock` (ya fijada en F2-02) · `puertos.go` → **`ports.go`**
> · `platformadminhelpertest/{suite,doble,doble_test}.go` → `contrato.go`, `fake.go`, `fake_test.go` (como `entitlementshelpertest`)
> · `iam/infra/postgres`: `leerInvitacion` → `readInvitation`, `cerrarSolicitudDeAcceso` → `closeAccessRequest`,
> `bloqueoAltaDeMembresia` → `membershipWriteLock` (mismo valor, 47052: viejo y nuevo se serializan entre sí),
> `multiEmpresaConcedida` → `multiCompanyGranted`, `porQueNoSeRevoco` → `whyNotRevoked`, `validarAmbitoDeAsignacion` →
> `validateAssignmentScope`, `filaAInvitacion` (nombre de la spec; el viejo tenía `scanInvitation`) → `invitationFromRow` +
> `invitationRow`; candados AST `membresia_unica_ast_test.go` → `single_membership_writer_ast_test.go`,
> `canje_orden_ast_test.go` → `redeem_order_ast_test.go`, `canje_una_consulta_ast_test.go` (test 1) →
> `redeem_single_query_ast_test.go` (el test 2 es conducta de `invitationFromRow`, `canje_test.go`), y sus constantes
> `ficheroDelCanje`/`lecturaDelCanje`/`escritoresEsperados` → `redeemFile`/`invitationReader`/`expectedWriters` ·
> `iam/transport/http`: `mensajeInvitacionInservible` → `unusableInvitationMessage`, `codigoIndistinguible` →
> `indistinguishableCode`, `instante` → `instant` · `platformadmin`: `vigentes`/`deseados` → `current`/`desired`;
> `lookupAccessRequestStatus`/`resolveRoleID`/`checkRetryApproved`/`executeApprovalTx` pasan a métodos **exportados** del
> puerto `AccessRequestStore` (mismo nombre en mayúscula) · candado nuevo `internal/candados/contract_adapters.go`
> (`ContractAdapterDirs`, D-F2-9) · `outhelpertest.Invitation`/`Membership` (alias de `domain`, decisión de Jhoan en la sesión).
> Los ficheros conservan el nombre del viejo (`canje.go`, `memberships.go`, T-15).
>
> **Desviaciones (aplicadas, ver README hallazgos 26–35)**: las pasadas contra Postgres viven en
> `test/procesos/{entitlements,iam,platformadmin}_contrato_test.go` (convención H9.5/F1, el arnés no se exporta), no en
> `postgres_integracion_test.go`/`suites_integracion_test.go` dentro del paquete · el unitario de la caché de
> `entitlements/postgres.go` sustituye un *driver* falso de `database/sql` en vez de `lookupFn`/`listFn` (T-1) · los commits
> rojos son uno por fichero (`rojo(acceso): contrato de iam/transport/http/<f>`), no uno por paquete.
>
> **Corte por tamaño (a petición de Jhoan, tras abrir el PR #31; ver README hallazgo 36)**: los 6 ficheros de F2-03 de más
> de 500 líneas se partieron por tema **solo moviendo declaraciones** (multiconjunto igual, byte a byte), con el sufijo del
> fichero de origen: `entitlements/postgres_{fakedb,has,list,concurrency}_test.go` (`3aacb27`) ·
> `platformadminhelpertest/{tenants,access_requests,approval}_contrato.go` (`3147c55`; `*_contrato.go` para seguir exentos
> de la cobertura, D-F1-13) · `platformadminhelpertest/fake_{access_requests,state}.go` (`23ab8e1`) ·
> `platformadmin/access_requests_{helpers,approve}_test.go` (`de3f593`) · `access_requests_postgres_fakesql_test.go`
> (`c1a4acc`) · `handlers_{helpers,enrollment}_test.go` (`5a91467`). El fichero con el nombre del gemelo conserva las
> menciones a los exportados (`exportados_cubiertos`).
> Después, con la regla ya escrita (D-R-7, `05` E-13, candado `file_size_test.go` `181458d` → `bec5116`), los de F2-02
> de más de 600: `identity/m2m{,_token,_wire}.go` con sus gemelos `m2m{,_token,_wire,_fake}_test.go` (`f26af82`) y
> `usecase/exchange{,_fixture,_tenant,_audit_roles}_test.go` (`4f6e178`).

- [x] **T2.4 · `entitlements/postgres.go`: rojo y verde** · 🌐 · complejo (prov.: caché, mutex, Postgres) · dep. T2.2 · cumple R2.4.c — cerrada en `90b786b` (rojo) → `f7d36eb` (verde) + `368d2fd` (suite contra Postgres); 15 mutantes sembrados · 15 muertos · 0 vivos · 0 equivalentes; 98,5 % (informe)
  - **Ficheros**: `A/entitlements/postgres.go`, `…/postgres_test.go` (unitario: caché con `lookupFn`/`listFn` sustituidos y `WithReloj`, D-F2-6), `…/postgres_integracion_test.go` (`//go:build integracion`, corre `ContratoResolver` con el arnés)
  - **Hecho cuando**: promesas de diseño §3 (cachés separadas, `false` cacheado, copia, TTL 60 s por defecto, mutex fuera de la consulta); el test unitario no usa reloj real; mutantes sobre la caché (TTL, `false` cacheado, copia).
  - **Gate**: G-rojo · `go vet -tags integracion ./internal/modulos/acceso/...; echo rc=$?` → 0 · G-verde
  - **Commit**: `rojo(acceso): contrato de entitlements/postgres` · `verde(acceso): entitlements/postgres`
- [x] **T2.12 · rojo(acceso): `iam/infra/postgres` (8) + candados AST** · 🌐 · complejo (prov.) · dep. T2.6 · cumple R2.2.c, R2.3.b–c — cerrada en `c182a0a` (`postgres.go`, simple, una pasada) y los rojos `6cd7dcc`, `1e3648e`, `cdba8b0`, `e0b7dff`, `a82e5d3`, `975d92b` (con el candado de escritor único), `e72425d` (con los dos del canje) + `e87b552`/`f812cfb` (las 7 suites contra Postgres), sobre el candado D-F2-9 `7da5367` → `a9a5fdf`
  - **Ficheros**: `A/iam/infra/postgres/*.go`, 8 `_test.go` unitarios (constructores, `filaAInvitacion` pura, mapeo de `pgconn` 23505 → `ErrConflict`), `…/suites_integracion_test.go` (`//go:build integracion`, las 7 suites con su `Montaje` de Postgres y el arnés), y los 3 candados AST portados (D-F2-1) con la guarda anti-hueco
  - **Hecho cuando**: los candados, **en rojo con su guarda** (el contrato no tiene aún el SQL: su «no encontré X» es el rojo esperado, bajo `//go:build pendiente`); el de membresía barre `internal/` y espera **dos** escritores.
  - **Gate**: G-rojo · `go vet -tags integracion ./internal/modulos/acceso/...; echo rc=$?` → 0
  - **Commit**: `rojo(acceso): contratos de iam/infra/postgres y sus candados`
- [x] **T2.24 · verde(acceso): `iam/infra/postgres/*` (8)** · 🌐 · dep. T2.12, T2.23 · cumple R2.3.b–c — cerrada en `95cb3e3`, `3183dd2`, `a8660ed`, `dc9abcc`, `2851eab`, `c482ae4` (memberships), `798bbb3` (canje); los 3 candados AST verdes; el viejo sigue verde sin tocarlo (D-F4-1 verificada: `SkipDir` = 1); mutantes sin BD muertos 8/8 en memberships y los estructurales de canje; con BD (pre-chequeo) sobreviven 4, todos de carrera (hallazgo 29)
  - **Hecho cuando**: SQL **literal** del viejo (T-2); los 3 candados AST en verde sin etiqueta; el candado viejo sigue verde **sin tocarlo** porque F0 ya lo dejó ciego al árbol nuevo (T0.27, D-F4-1; si D-F4-1 = no: `memberships.go` en el mismo commit que la línea de D-F2-2, T-1); unitarios verdes; mutantes sobre `memberships.go` y `canje.go` (los corre quien tenga Postgres: pre-chequeo en la web si hay Docker, cierre en T2.33).
  - **Gate**: G-verde (su verdad la da la suite contra Postgres, P4, y F9) · `make ci-local` rc=0 (el candado viejo sigue verde)
  - **Commit**: `verde(acceso): iam/infra/postgres/<f>` — uno por fichero
- [x] **T2.13 · rojo(acceso): `iam/transport/http` (6)** · 🌐 · medio (prov.) · dep. T2.7 · cumple R2.3.b, R2.5.e — cerrada en `9452ca0`, `1da40da`, `8792bd9`, `aa8a009`, `674e13e` (un rojo por fichero); `http.go` sin exportados nace en su verde `00ed7e4`
  - **Ficheros**: `A/iam/transport/http/*.go` y 6 `_test.go`
  - **Hecho cuando**: R-H1…R-H9; los textos de diseño §5 como constantes o literales afirmados byte a byte (anti-oráculo del canje con `bytes.Equal`).
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contratos de iam/transport/http`
- [x] **T2.25 · verde(acceso): `iam/transport/http/*` (6)** · 🌐 · dep. T2.13, T2.23 · 6 commits · **Gate**: G-verde — cerrada en `95aa600`, `dd1d345`, `65b4e52`, `9c3f5d6`, `75bd52d`; R-H1…R-H9; 157 PASS, 0 SKIP; 94,0–100 % (informe)
- [x] **T2.14 · `platformadmin/puertos.go` ✚ + `platformadminhelpertest`** · 🌐 · simple (prov.) · dep. T2.5 · cumple R2.2.e — cerrada en `3b58eba` (`ports.go` + `platformadminhelpertest`, simple, una pasada) y `7a32260` (dos casos de alcance por empresa que destaparon los mutantes de BD; 29 casos); los tipos van antes: rojos `2cb4954`, `5299fd5`
  - **Ficheros**: `A/platformadmin/puertos.go`, `A/platformadmin/platformadminhelpertest/{suite,doble,doble_test}.go`
  - **Hecho cuando**: `TenantStore` y `AccessRequestStore` cubren los métodos de `V/postgres.go:99-277` y `V/access_requests.go:136-485`; suite con la firma `Contrato(t, func(t) Montaje)`; doble completo y verde contra la suite.
  - **Gate**: `go test -race ./internal/modulos/acceso/platformadmin/platformadminhelpertest/; echo rc=$?` → 0
  - **Commit**: `verde(acceso): puertos de platformadmin y su doble`
- [x] **T2.15 · rojo(acceso): `platformadmin` (5 ficheros)** · 🌐 · medio; `postgres.go` y `access_requests_postgres.go` complejo (prov.) · dep. T2.14, T2.12 · cumple R2.2.e — cerrada en `2cb4954`, `5299fd5`, `49a2cf8`, `0600a15`, `681ece7` y `a4464e6` (`access_requests_postgres.go` ✚)
  - **Ficheros**: `A/platformadmin/{access_requests,access_requests_postgres,handlers,postgres,signup}.go` y 4 `_test.go` + el de integración (la suite de `platformadminhelpertest` con su `Montaje` de Postgres y el arnés)
  - **Hecho cuando**: handlers reciben **puertos**; R-A1…R-A10; los 30 `http.Error` y los 9 centinelas literales; `generateEnrollmentCode` `"WAPP-"` + 20 hex. El corpus de equivalencia del correo del signup (R-A8) lleva entradas adversarias ([`reglas.md`](reglas.md) §5).
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contrato de platformadmin/<fichero>` — uno por fichero
- [x] **T2.26 · verde(acceso): `platformadmin/*` (5)** · 🌐 · dep. T2.15, T2.24 · 5 commits; la verdad de `access_requests_postgres.go` y `postgres.go` la da la suite contra Postgres (P4) y F9 · **Gate**: G-verde — cerrada en `20330c6`, `e500186`, `3706323`, `8a38c67`, `a7e72b4` (`NewRepository`), `27028a8` (`access_requests_postgres.go`, complejo) + `99336f3` (suite contra Postgres); mutantes 16/16 sin BD y 13/13 contra Postgres; 95,0–100 % fuera de los adaptadores (informe)
- [x] **T2.27 · Cierre de F2-03** · 🌐 · **Hecho cuando**: `grep -rn 'pendiente.Implementar' internal/modulos/acceso | wc -l` → 0; `go vet -tags integracion ./...` rc=0; minutos anotados; las tres cosas del cierre · **Gate**: `validar-antes-de-cerrar` · **Commit**: — (PR) — cerrada sobre `99336f3`: `make toolchain` OK (go1.26.5, lint v2.12.2) · `make ci-local` `GATE_RC=0` (111 `ok`, lint 0 issues) · `make vet-pendiente` rc=0 · `go vet -tags integracion ./...` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` · `grep pendiente.Implementar internal/modulos/acceso` → **0** · `go test -v ./internal/{modulos,nucleo,arranque,candados}/...` rc=0, 2.445 PASS, **0 SKIP** · los 3 candados AST verdes · `cobertura-ficheros` (informe) 67 evaluados, 8 por debajo: los 7 adaptadores Postgres nuevos (fuera del umbral por spec) y `nucleo/contact/repository_postgres.go` (previo) · `go list -deps ./internal/modulos/acceso/...` sin `internal/{iam,entitlements,platformadmin}` · pre-chequeo (no cierra; cierra F2-05) de las 4 suites contra Postgres en la web: rc=0, 148 PASS, 0 FAIL, 0 SKIP · ≈ 90 min de pared (D-R-6, ESTADO)

## Sesión F2-04 · adaptador, conmutación y rutas · 🌐 · T2.28–T2.31
Entrada: PR de F2-03 integrado. Para cuando: huella igual · 23 + 8 rutas nuevas · `go list -deps` · test de cableado completo · PR.

- [ ] **T2.28 · `bridge_iam.go`: el adaptador de arranque** · 🌐 · simple (`05` §4.2) · dep. T2.27 · cumple R2.5.a
  - **Ficheros**: `internal/arranque/bridge_iam.go`, `…/bridge_iam_test.go`
  - **Hecho cuando**: contrato, test y lógica en una pasada; tipos no exportados, sin estado, con `var _ viejoin.Authenticator = (*authenticatorBridge)(nil)` y el de `auditorBridge`; test de equivalencia de los 4 centinelas (`errors.Is` contra el **viejo**) y de los DTOs. `un_fichero_un_test` lo incluye.
  - **Gate**: G-verde sobre `./internal/arranque/...` · **Commit**: `verde(arranque): bridge_iam`
- [ ] **T2.29 · Test de cableado de `acceso`, completo** · 🌐 · dep. T2.28, T2.31 (va en el mismo commit que la conmutación) · cumple R2.4.a, R2.5.f
  - **Ficheros**: `internal/arranque/bridge_iam_test.go` (o el `_test.go` de cableado de la fase que toque)
  - **Hecho cuando** (hallazgo 39 de F1): afirma que el arranque construye el `entitlements.Postgres`, el `DelegatedAuthService` y el auditor **nuevos**, **y** que ninguna fase de `internal/arranque` importa `internal/iam/...`, `internal/entitlements` ni `internal/platformadmin` fuera de `bridge_iam.go` (grep por ruta de import, dentro del test); no basta el campo del contenedor.
  - **Gate**: G-verde · **Commit**: parte de `conmutar(acceso)`
- [ ] **T2.30 · FX TX.5–TX.6 (cara nueva: comunes + acceso)** · 🌐 · dep. T2.27 · cumple R2.5.b
  - **Ficheros**: los de [`FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.5–TX.6 (`apipublica/{cadena,respuesta,autenticacion,roleplane,audit,entitlements}.go`, arnés)
  - **Hecho cuando**: lo que dice TX.6 (0 pendientes en `apipublica`), sin umbral de cobertura (P2): un test por promesa del contrato. Esta tarea **es** TX.5+TX.6: se marcan las dos.
- [ ] **T2.31 · conmutar(acceso): el arranque nuevo cablea `acceso` (= FX TX.7)** · 🌐 · dep. T2.28, T2.30, T2.34 · cumple R2.4.a–b, R2.5.b–e, R2.3.e
  - **Ficheros**: en `internal/arranque`: `auth_*.go` (partidos en T2.34), `fase3_almacenes.go`, `fase4_gateway.go`, `fase8_transporte.go`, `http.go`, `rutas_admin.go`, `contenedor.go`; `huella_test.go` si hace falta un caso
  - **Hecho cuando**: **un** `entitlements.NewPostgres` (nuevo) inyectado en todos los consumidores —incluida la cara vieja: `Deps.Entitlements` = el nuevo (FX TX.7)—; `Deps.{Roles,Members,Invitations,Audit}` = `nil`; A1–A7 fuera del mux viejo; J4–J11 inline con `platformadmin` nuevo sin alias; gateway viejo con `bridge_iam.go`; test de cableado de T2.29 verde; `go list -deps ./cmd/server-modular` cumple R2.5.d; `git diff --stat -- cmd/server internal/bootstrap` vacío. **`acceso` no entra en `Conmutados`**: entra cuando muera `bridge_iam.go` (F3).
  - **Gate**: `GOWORK=off go test -count=1 -v -run 'Mudanzas|Huella|PlatformPermissions|Cableado' ./internal/arranque > "$TMPDIR/m.log" 2>&1; echo rc=$? >> "$TMPDIR/m.log"; tail -1 "$TMPDIR/m.log"` → `rc=0`, `grep -c -- '--- SKIP' "$TMPDIR/m.log"` → 0 · `make ci-local` rc=0
  - **Commit**: `conmutar(acceso): el arranque nuevo cablea acceso y muda 23 rutas`

## Sesión F2-05 · cierre local · 💻 · T2.32–T2.33
- [ ] **T2.32 · Cierre de F2** · 💻 · dep. T2.31
  - **Hecho cuando**: la local repite `validar-antes-de-cerrar` con su toolchain; e2e de `cmd/server-modular` (`integration_test.go` de F0) verde; **una** petición real por familia contra el binario nuevo en local (exchange 503 en modo dual apagado, `GET /api/v1/entitlements` con `cache_ttl_seconds` 60, `POST /admin/tenants` con token `tenant_admin` → 403); `ESTADO.md` y README de F2 → «cerrada» con SHA.
  - **Gate**: `make ci-local` rc=0 en local, 0 SKIP · **Commit**: `docs(reorganizacion-modular): F2 cerrada`
- [ ] **T2.33 · Suites y procesos de acceso contra Postgres y el binario nuevo (= T9.23, 9C de `acceso`)** · 💻 · con **D-F9-1 = sí** (decidido 2026-09-30; si no, se tacha y lo cubre T9.34) · cumple R2.2.c, R2.6.a–b
  - **Hecho cuando**: las 7 suites de `outhelpertest`, `ContratoResolver` y la de `platformadminhelpertest` pasan contra Postgres con el arnés (las mismas que ya pasan en memoria: cero divergencias); los mutantes del nivel complejo, corridos; el proceso «Canje de identidad y permisos» (`05` §7.4) incluye las reglas de diseño §6 (R-P1…R-P8, R-A5…R-A7, I-CP-5, migración 0038) y pasa contra `cmd/server` **y** `cmd/server-modular`.
  - **Gate**: `make test-procesos > "$TMPDIR/p.log" 2>&1; echo rc=$? >> "$TMPDIR/p.log"` → `rc=0`, 0 SKIP · **Commit**: `procesos(canje-permisos): …`

## Plantilla del informe de fase (va al README al cerrar)

Minutos por sesión · ficheros nuevos (51 + 6 ✚ + suites) por nivel E-12, y los que subieron de nivel · pendientes
contados en cada cierre · cobertura por fichero (mín., media) frente a la base vieja, **como informe** · mutantes
(sembrados, muertos, vivos) · candados que estorbaron · reglas E-8 que se decidió no mantener (con motivo) · lo que la
web no pudo correr.
