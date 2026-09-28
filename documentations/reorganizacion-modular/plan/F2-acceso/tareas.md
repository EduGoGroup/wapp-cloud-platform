# F2 · Tareas

> Formato de [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `reconstruir-modulo` (la fase), `contrato-tdd` (cada fichero), `validar-antes-de-cerrar` (cada
> gate), `traspaso-web-local` (bloque G→H), `procesos-testcontainers` (T2.33). `A` =
> `internal/modulos/acceso`, `V` = paquete viejo. Todo gate se lee **sin pipe** (el `rc` a un log y se
> lee del log). La web trabaja en su rama y abre PR hacia `dev` (`--base dev`), integrado **sin
> squash**: rojo y verde siguen siendo commits distintos. Gate estándar de rojo (**G-rojo**):
> `GOWORK=off go vet -tags pendiente ./internal/modulos/acceso/...; echo rc=$?` → `0`. Gate estándar
> de verde (**G-verde**): `GOWORK=off go test -race ./<paquete>/ > "$TMPDIR/t.log" 2>&1; echo rc=$? >> "$TMPDIR/t.log"; tail -1 "$TMPDIR/t.log"` → `rc=0` · `make cobertura-ficheros` ≥ 80 %.

## Bloque A · inventario verificado · 🌐 · T2.1
Para cuando: números de [`arquitectura.md`](arquitectura.md) §1 re-medidos, D-F2-1…7 contestadas en el README.

- [ ] **T2.1 · Verdad de campo, entradas y re-medición** · 🌐 · dep. F1 cerrado y parada resuelta · cumple R2.1.d
  - **Ficheros**: `plan/F2-acceso/README.md` (estado «en curso», SHA de arranque, respuestas D-F2-*)
  - **Hecho cuando**: las 5 entradas del README comprobadas con su comando; tabla §1 de arquitectura re-medida (si difiere, se corrige **aquí** y se dice en el commit); `git log 1b18932..origin/dev -- internal/iam internal/entitlements internal/platformadmin` revisado; D-F2-1…D-F2-7 con respuesta de Jhoan (o la recomendación marcada «asumida» si Jhoan lo delegó).
  - **Gate**: `GOWORK=off make ci-local > "$TMPDIR/g.log" 2>&1; echo GATE_RC=$? >> "$TMPDIR/g.log"; tail -1 "$TMPDIR/g.log"` → `GATE_RC=0`
  - **Commit**: `docs(reorganizacion-modular): F2 arranca — entradas verificadas`

## Bloque B · rojo de las hojas · 🌐 · T2.2–T2.8
Para cuando: `entitlements`, `iam/domain`, `ports/{in,out}` y sus suites en rojo · `ci-local` rc=0 · PR abierto.

- [ ] **T2.2 · rojo(acceso): `entitlements/entitlements.go` + `entitlementstest`** · 🌐 · dep. T2.1 · cumple R2.1.a–b, R2.3.d
  - **Ficheros**: `A/entitlements/entitlements.go`, `…/entitlements_test.go`, `A/entitlements/entitlementstest/{suite.go,fake.go,fake_test.go}`
  - **Hecho cuando**: las 11 constantes con su valor **literal** y su comentario-ADR (R-E5), `Resolver` con su promesa; `ContratoResolver` con los casos de diseño §2; `Fake` (D-F2-4) nace **completo** y pasa la suite en verde (es un doble, D-F1-3).
  - **Gate**: G-rojo · `go test -race ./internal/modulos/acceso/entitlements/entitlementstest/; echo rc=$?` → 0
  - **Commit**: `rojo(acceso): contrato de entitlements y su suite`
- [ ] **T2.3 · rojo(acceso): `entitlements/middleware.go`** · 🌐 · dep. T2.2 · cumple R2.3.d
  - **Ficheros**: `A/entitlements/middleware.go`, `…/middleware_test.go`
  - **Hecho cuando**: `RequireFeature`/`RequireAnyFeature` con R-E1…R-E4 en el comentario; el test cubre los 3 modos fail-closed, el corte en el primer error, la lista vacía y los dos cuerpos **byte a byte**.
  - **Gate**: G-rojo · `go test -tags pendiente -run '^TestRequireFeature_SinIdentidad' ./internal/modulos/acceso/entitlements/; echo rc=$?` → ≠0
  - **Commit**: `rojo(acceso): contrato de entitlements/middleware`
- [ ] **T2.4 · rojo(acceso): `entitlements/postgres.go`** · 🌐 · dep. T2.2 · cumple R2.4.c
  - **Ficheros**: `A/entitlements/postgres.go`, `…/postgres_test.go` (unitario: caché con `lookupFn`/`listFn` sustituidos y `WithReloj`, D-F2-6), `…/postgres_integracion_test.go` (`//go:build integracion`, corre `ContratoResolver`)
  - **Hecho cuando**: promesas de diseño §3 (cachés separadas, `false` cacheado, copia, TTL 60 s por defecto, mutex fuera de la consulta); el test unitario no usa reloj real.
  - **Gate**: G-rojo · `go vet -tags integracion ./internal/modulos/acceso/...; echo rc=$?` → 0
  - **Commit**: `rojo(acceso): contrato de entitlements/postgres`
- [ ] **T2.5 · rojo(acceso): `iam/domain` (4 ficheros)** · 🌐 · dep. T2.1 · cumple R2.1.a–b
  - **Ficheros**: `A/iam/domain/{canje,entities,errors,invitation}.go` y sus 4 `_test.go`
  - **Hecho cuando**: 50 exportados; los 20 centinelas con texto literal (diseño §5); R-D1…R-D5 en comentario y test (largo **exacto** del token, 32 bytes del digest, simetría de normalización, precedencia de estados, 4 veredictos).
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contratos de iam/domain` (un commit por fichero si el bloque se parte)
- [ ] **T2.6 · rojo(acceso): `iam/ports/out` + `outtest` (las 7 suites)** · 🌐 · dep. T2.5 · cumple R2.2.a
  - **Ficheros**: `A/iam/ports/out/{active_tenant,canje,repos}.go` (sin `_test`, E-3), `A/iam/ports/out/outtest/{montaje,membresias,roles,grants,auditoria,invitaciones,empresa_activa,canje}.go`
  - **Hecho cuando**: las 10 interfaces con su comentario-contrato; las 7 suites (diseño §2) con la firma de D-F1-1, casos en español, sin `t.Skip`; `go doc` las muestra.
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): puertos de salida del IAM y sus suites de contrato`
- [ ] **T2.7 · rojo(acceso): `iam/ports/in`** · 🌐 · dep. T2.5 · cumple R2.1.a–b
  - **Ficheros**: `A/iam/ports/in/{active_tenant,canje,usecases}.go`, `…/usecases_test.go`
  - **Hecho cuando**: `AuditInput` es **alias** del DTO de `platform/httpapi` (F0); `CallerResolverFunc.Caller` con `panic`; el test menciona los 25 exportados de `usecases.go`; excepción de suite para `ports/in` escrita en el comentario de paquete (D-F2-5).
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): puertos de entrada del IAM`
- [ ] **T2.8 · Cierre del bloque B** · 🌐 · dep. T2.2–T2.7
  - **Hecho cuando**: `grep -rn 'pendiente.Implementar' --include='*.go' internal/modulos/acceso | wc -l` anotado y = `make test-pendiente`; `ci-local` rc=0; `fronteras_test` sin puentes de `acceso`; PR abierto; minutos del bloque anotados (dato del piloto).
  - **Gate**: skill `validar-antes-de-cerrar` completa
  - **Commit**: — (PR)

## Bloque C · rojo del resto · 🌐 · T2.9–T2.16
Entrada: PR de B integrado. Para cuando: todo `acceso` en rojo · `vet -tags pendiente` rc=0 · PR.

- [ ] **T2.9 · rojo(acceso): `iam/infra/memory` (7 + `redeem_store.go` ✚)** · 🌐 · dep. T2.6 · cumple R2.2.b
  - **Ficheros**: `A/iam/infra/memory/*.go` y 8 `_test.go` (cada uno ejecuta **su** suite de `outtest`)
  - **Hecho cuando**: structs **sin campos** en rojo (T-14), constructores y métodos con `panic`; `var _ out.X = (*Y)(nil)` para los 7 puertos; `RedeemStore` documenta cómo reproduce los 4 pasos del canje en memoria.
  - **Gate**: G-rojo · `go test -tags pendiente -run '^TestMembershipStore_Contrato$' ./internal/modulos/acceso/iam/infra/memory/; echo rc=$?` → ≠0
  - **Commit**: `rojo(acceso): dobles en memoria del IAM` (uno por fichero si se parte)
- [ ] **T2.10 · rojo(acceso): `iam/infra/identity`** · 🌐 · dep. T2.6 · cumple R2.2.d
  - **Ficheros**: `A/iam/infra/identity/{client,m2m}.go`, `…/{client,m2m}_test.go` (con `httptest.Server` que imita identity)
  - **Hecho cuando**: R-I1…R-I9 en comentario y test; `WithReloj` en el M2M (D-F2-6); textos `"iam: …"` literales.
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contratos de iam/infra/identity`
- [ ] **T2.11 · rojo(acceso): `iam/usecase` (11 ficheros)** · 🌐 · dep. T2.6, T2.7 · cumple R2.3.a
  - **Ficheros**: `A/iam/usecase/*.go` y 11 `_test.go` (usan `infra/memory` y dobles propios de identity)
  - **Hecho cuando**: R-U1…R-U33 en los comentarios y una aserción por promesa; los tres invariantes cruzados (R-U18 selector ↔ canje, R-U6 corte, R-U12 mitigación) como tests con nombre propio.
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contrato de iam/usecase/<fichero>` — uno por fichero
- [ ] **T2.12 · rojo(acceso): `iam/infra/postgres` (8) + candados AST** · 🌐 · dep. T2.6 · cumple R2.2.c, R2.3.b–c
  - **Ficheros**: `A/iam/infra/postgres/*.go`, 8 `_test.go` unitarios (constructores, `filaAInvitacion` pura, mapeo de `pgconn` 23505 → `ErrConflict`), `…/suites_integracion_test.go` (`//go:build integracion`, las 7 suites), y los 3 candados AST portados (D-F2-1) con la guarda anti-hueco
  - **Hecho cuando**: los candados, **en rojo con su guarda** (el contrato no tiene aún el SQL: su «no encontré X» es el rojo esperado, bajo `//go:build pendiente`); el de membresía barre `internal/` y espera **dos** escritores.
  - **Gate**: G-rojo · `go vet -tags integracion ./internal/modulos/acceso/...; echo rc=$?` → 0
  - **Commit**: `rojo(acceso): contratos de iam/infra/postgres y sus candados`
- [ ] **T2.13 · rojo(acceso): `iam/transport/http` (6)** · 🌐 · dep. T2.7 · cumple R2.3.b, R2.5.e
  - **Ficheros**: `A/iam/transport/http/*.go` y 6 `_test.go`
  - **Hecho cuando**: R-H1…R-H9; los textos de diseño §5 como constantes o literales afirmados byte a byte (anti-oráculo del canje con `bytes.Equal`).
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contratos de iam/transport/http`
- [ ] **T2.14 · rojo(acceso): `platformadmin/puertos.go` ✚ + `platformadmintest`** · 🌐 · dep. T2.5 · cumple R2.2.e
  - **Ficheros**: `A/platformadmin/puertos.go`, `A/platformadmin/platformadmintest/{suite,doble,doble_test}.go`
  - **Hecho cuando**: `TenantStore` y `AccessRequestStore` cubren los métodos de `V/postgres.go:99-277` y `V/access_requests.go:136-485`; doble completo y verde contra la suite.
  - **Gate**: G-rojo · `go test -race ./internal/modulos/acceso/platformadmin/platformadmintest/; echo rc=$?` → 0
  - **Commit**: `rojo(acceso): puertos de platformadmin y su doble`
- [ ] **T2.15 · rojo(acceso): `platformadmin` (5 ficheros)** · 🌐 · dep. T2.14, T2.12 · cumple R2.2.e
  - **Ficheros**: `A/platformadmin/{access_requests,access_requests_postgres,handlers,postgres,signup}.go` y 4 `_test.go` + el de integración
  - **Hecho cuando**: handlers reciben **puertos**; R-A1…R-A10; los 30 `http.Error` y los 9 centinelas literales; `generateEnrollmentCode` `"WAPP-"` + 20 hex.
  - **Gate**: G-rojo
  - **Commit**: `rojo(acceso): contrato de platformadmin/<fichero>` — uno por fichero
- [ ] **T2.16 · Cierre del bloque C** · 🌐 · dep. T2.9–T2.15
  - **Hecho cuando**: pendientes de `acceso` anotados; `ci-local` rc=0; `exportados_cubiertos` y `un_fichero_un_test` verdes; 0 SKIP; PR.
  - **Gate**: `validar-antes-de-cerrar` · **Commit**: — (PR)

## Bloque D · verde de hojas y dobles · 🌐 · T2.17–T2.21
Entrada: PR de C integrado. Para cuando: 0 pendientes en `entitlements`, `iam/domain`, `iam/ports/in`, `iam/infra/memory` · PR.

- [ ] **T2.17 · verde(acceso): `entitlements/entitlements.go`, `middleware.go`, `postgres.go`** · 🌐 · dep. T2.16 · cumple R2.3.d, R2.4.c · un commit por fichero, `verde(acceso): entitlements/<f>` · **Gate**: G-verde (base vieja: `go test -cover ./internal/entitlements/`, anotarla)
- [ ] **T2.18 · verde(acceso): `iam/domain/*`** · 🌐 · dep. T2.16 · 4 commits · **Gate**: G-verde
- [ ] **T2.19 · verde(acceso): `iam/ports/in/usecases.go`** · 🌐 · dep. T2.18 · **Gate**: G-verde
- [ ] **T2.20 · verde(acceso): `iam/infra/memory/*`** · 🌐 · dep. T2.18 · cumple R2.2.b · 8 commits · **Hecho cuando**: las 7 suites verdes con `-race -v` y 0 SKIP · **Gate**: G-verde
- [ ] **T2.21 · Cierre del bloque D** · 🌐 · **Gate**: `validar-antes-de-cerrar` · **Commit**: — (PR)

## Bloque E · verde de `usecase` e `identity` · 🌐 · T2.22–T2.23
- [ ] **T2.22 · verde(acceso): `iam/infra/identity/{client,m2m}.go`** · 🌐 · dep. T2.21 · cumple R2.2.d · 2 commits · **Hecho cuando**: ≥ 80 % sin red · **Gate**: G-verde
- [ ] **T2.23 · verde(acceso): `iam/usecase/*` (11)** · 🌐 · dep. T2.21 · cumple R2.3.a · 11 commits, en el orden `config`, `grants`, `context_token`, `audit`, `exchange`, `delegated_auth`, `active_tenant`, `roles`, `memberships`, `invitations`, `canje` · **Gate**: G-verde · cierre con PR

## Bloque F · verde de `infra/postgres`, `transport/http`, `platformadmin` · 🌐 · T2.24–T2.27
Para cuando: 0 pendientes en `acceso` · candados AST verdes · PR.

- [ ] **T2.24 · verde(acceso): `iam/infra/postgres/*` (8)** · 🌐 · dep. T2.23 · cumple R2.3.b–c
  - **Hecho cuando**: SQL **literal** del viejo (T-2); los 3 candados AST en verde sin etiqueta; `memberships.go` en el **mismo** commit que la línea del candado viejo (D-F2-2, T-1) y el mensaje lo explica; unitarios verdes (el SQL lo cubre F9).
  - **Gate**: G-verde (sin umbral: adaptador Postgres) · `make ci-local` rc=0 (el candado viejo sigue verde)
  - **Commit**: `verde(acceso): iam/infra/postgres/<f>` — uno por fichero
- [ ] **T2.25 · verde(acceso): `iam/transport/http/*` (6)** · 🌐 · dep. T2.23 · 6 commits · **Gate**: G-verde
- [ ] **T2.26 · verde(acceso): `platformadmin/*` (5)** · 🌐 · dep. T2.24 · 5 commits; `access_requests_postgres.go` y `postgres.go` fuera del umbral · **Gate**: G-verde
- [ ] **T2.27 · Cierre del bloque F** · 🌐 · **Hecho cuando**: `grep -rn 'pendiente.Implementar' internal/modulos/acceso | wc -l` → 0; minutos anotados · **Gate**: `validar-antes-de-cerrar` · **Commit**: — (PR)

## Bloque G · puente, conmutación y rutas · 🌐 · T2.28–T2.31
Para cuando: huella igual · 23 + 8 rutas nuevas · `go list -deps` · PR · traspaso escrito.

- [ ] **T2.28 · rojo(arranque): `puente_iam.go`** · 🌐 · dep. T2.27 · cumple R2.5.a
  - **Ficheros**: `internal/arranque/puente_iam.go`, `…/puente_iam_test.go`
  - **Hecho cuando**: tipos no exportados con `var _ viejoin.Authenticator = (*puenteAutenticador)(nil)` y el del auditor; test de equivalencia de los 4 centinelas (`errors.Is` contra el **viejo**) y de los DTOs.
  - **Gate**: G-rojo sobre `./internal/arranque/...` · **Commit**: `rojo(arranque): contrato de puente_iam`
- [ ] **T2.29 · verde(arranque): `puente_iam.go`** · 🌐 · dep. T2.28 · **Gate**: G-verde · **Commit**: `verde(arranque): puente_iam`
- [ ] **T2.30 · FX TX.5–TX.6 (cara nueva: comunes + acceso)** · 🌐 · dep. T2.27 · cumple R2.5.b
  - **Ficheros**: los de [`FX-cara-http/tareas.md`](../FX-cara-http/tareas.md) TX.5–TX.6 (`apipublica/{cadena,respuesta,autenticacion,roleplane,audit,entitlements}.go`, arnés)
  - **Hecho cuando**: lo que dice TX.6 (0 pendientes en `apipublica`, ≥ 80 %). Esta tarea **es** TX.5+TX.6: se marcan las dos.
- [ ] **T2.31 · conmutar(acceso): el arranque nuevo cablea `acceso` (= FX TX.7)** · 🌐 · dep. T2.29, T2.30 · cumple R2.4.a–b, R2.5.b–e, R2.3.e
  - **Ficheros**: en `internal/arranque`: las copias de `auth.go`, `fase3_almacenes.go`, `fase4_gateway.go`, `fase8_transporte.go`, `transporte_http.go`, `transporte_rutas_admin.go`, `contenedor.go`; `huella_test.go` si hace falta un caso; `traspasos/TRASPASO-F2-acceso.md`
  - **Hecho cuando**: **un** `entitlements.NewPostgres` (nuevo) inyectado en todos los consumidores —incluida la cara vieja: `Deps.Entitlements` = el nuevo (FX TX.7)—; `Deps.{Roles,Members,Invitations,Audit}` = `nil`; A1–A7 fuera del mux viejo; J4–J11 inline con `platformadmin` nuevo sin alias; gateway viejo con `puente_iam`; aserción de cableado sobre el tipo de `c.entResolver`; `go list -deps ./cmd/server-modular` cumple R2.5.d; `git diff --stat -- cmd/server internal/bootstrap` vacío salvo D-F2-2.
  - **Gate**: `GOWORK=off go test -count=1 -v -run 'Mudanzas|Huella|PlatformPermissions|Cableado' ./internal/arranque > "$TMPDIR/m.log" 2>&1; echo rc=$? >> "$TMPDIR/m.log"; tail -1 "$TMPDIR/m.log"` → `rc=0`, `grep -c -- '--- SKIP' "$TMPDIR/m.log"` → 0 · `make ci-local` rc=0
  - **Commit**: `conmutar(acceso): el arranque nuevo cablea acceso y muda 23 rutas`

## Bloque H · cierre local · 💻 (🌐→💻) · T2.32–T2.33
- [ ] **T2.32 · Cierre de F2** · 🌐→💻 · dep. T2.31
  - **Hecho cuando**: la local repite `validar-antes-de-cerrar` con su toolchain; e2e de `cmd/server-modular` (`integration_test.go` de F0) verde; **una** petición real por familia contra el binario nuevo en local (exchange 503 en modo dual apagado, `GET /api/v1/entitlements` con `cache_ttl_seconds` 60, `POST /admin/tenants` con token `tenant_admin` → 403); `ESTADO.md` y README de F2 → «cerrada» con SHA; traspaso con su sección `CERRADO`.
  - **Gate**: `make ci-local` rc=0 en local · **Commit**: `docs(reorganizacion-modular): F2 cerrada`
- [ ] **T2.33 · Procesos de acceso contra el binario nuevo** · 🌐→💻 · **condicionada: solo si F9 adelantado está aceptado** (`plan/F9-procesos/`) · cumple R2.6.a–b
  - **Hecho cuando**: el proceso «Canje de identidad y permisos» (`05` §7.4) incluye las reglas de diseño §6 (suites Postgres, R-P1…R-P8, R-A5…R-A7, I-CP-5, migración 0038) y pasa contra `cmd/server` **y** `cmd/server-modular`; si F9 no está adelantado, esta lista se entrega a `plan/F9-procesos/` y la tarea se tacha con ese motivo.
  - **Gate**: `make test-procesos > "$TMPDIR/p.log" 2>&1; echo rc=$? >> "$TMPDIR/p.log"` → `rc=0`, 0 SKIP · **Commit**: `procesos(canje-permisos): …`

## Plantilla del informe de fase (va al README al cerrar)

Minutos por bloque · ficheros nuevos (51 + 6 ✚ + suites) · pendientes contados en B/C · cobertura
por fichero (mín., media) frente a la base vieja · candados que estorbaron · reglas E-8 que se
decidió no mantener (con motivo) · lo que la web no pudo correr.
