# F0 · Andamiaje — tareas

> Formato de [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. IDs
> `T0.n`, nunca se renumeran. Entorno: 🌐 web entera · 💻 solo local · 🌐→💻 la web escribe y la
> local cierra. Cada gate se lee **sin pipe** (`…; echo rc=$?`) y con la skill
> `validar-antes-de-cerrar`. Toda tarea que toque código deja `make ci-local` en `rc=0`.
>
> **Orden no negociable**: la huella (bloque D) nace **antes** que los ✎ de `platform` (bloque E),
> para que sea ella la que demuestre que los ✎ no cambian nada hacia fuera.

Variables de los gates: `L=/tmp/gate-$(date +%s).log` y
`GOWORK=off make ci-local > "$L" 2>&1; echo "GATE_RC=$?" >> "$L"; tail -1 "$L"` (en adelante,
**«gate ci-local»**: aprobado solo con `GATE_RC=0` leído del log).

---

## Bloque A · el entorno web · 🌐 · T0.0–T0.1

Para cuando: `06-entorno-web.md` §5 tiene el resultado de la primera sesión web, y el hook de
`SessionStart` está commiteado y probado en sus dos ramas.

- [x] **T0.0 · verificación del entorno de la primera sesión web** · 🌐 · dep. — · cumple R0.1.a–R0.1.e — cerrada en `98e806d` (sesión F0-01, 2026-09-30: `GATE_RC=0` en 217 s, `TC_RC=0`; detalle en `06` §5)
  - **Ficheros**: `documentations/reorganizacion-modular/06-entorno-web.md` (rellenar la §5 que ya
    existe como hueco, «5 · Resultados de la primera sesión web ✎», con la fecha). **Nada más**:
    ni código ni `go.mod`.
  - **Qué se corre** (cada uno con su `rc`, sin pipe):
    ```bash
    go version                                   # ¿1.26.5 o toolchain bajada por GOTOOLCHAIN=auto?
    golangci-lint version                        # exige v2.12.2 (Makefile:12)
    GOWORK=off go build ./... ; echo rc=$?
    time (GOWORK=off make ci-local > /tmp/ci.log 2>&1; echo "GATE_RC=$?" >> /tmp/ci.log); tail -1 /tmp/ci.log
    docker info > /tmp/docker.log 2>&1; echo rc=$?
    ```
    y la **prueba de testcontainers**, en un directorio **fuera del árbol del repo**: la receta
    **autoritativa** es la de [`../00-marco/flujo-web-local.md`](../00-marco/flujo-web-local.md) §5
    (`testcontainers-go/modules/postgres@v0.44.0` —la versión que fija
    [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §6— más `pgx/v5/stdlib`;
    `postgres.Run(ctx, "postgres:17-alpine", …, postgres.BasicWaitStrategies())`, `SELECT 1`,
    `CREATE DATABASE … TEMPLATE`, `Terminate`; `TC_RC` al log). **No se commitea** y el `go.mod`
    del repo **no cambia** (`git status --short` limpio salvo `06-entorno-web.md`).
  - **Hecho cuando**: §5 de `06` lista las seis líneas con valor y `rc`, la duración de `ci-local`
    en segundos, y el veredicto de testcontainers («funciona» → la web puede correr procesos
    como **pre-chequeo**; «no funciona» → con el error literal). Si `golangci-lint` no es
    `v2.12.2`, §5 lo dice y **ningún gate de esta sesión se da por pasado**.
  - **Gate**: `git diff --stat` → solo `06-entorno-web.md`.
  - **Commit**: `docs(reorganizacion-modular): F0 · T0.0, el entorno web verificado`

- [x] **T0.1 · hook `SessionStart` que verifica la toolchain** · 🌐 · dep. T0.0 · cumple R0.1.f — cerrada en `de04088` (sesión F0-01, 2026-09-30). Dos añadidos al texto del diseño, sin cambiar su comportamiento y ya llevados a `flujo-web-local.md` §4: la línea `Go: … · golangci-lint: …` siempre (R0.1.f la exige; el diseño solo la daba en el aviso) y el comando para arrancar `dockerd` en el aviso de Docker (contradicciones 10 y 11 del `README.md`)
  - **Diseño**: lo fija [`../00-marco/flujo-web-local.md`](../00-marco/flujo-web-local.md)
    (qué imprime, qué comprueba, qué hace en local). Esta tarea **solo lo implementa**; si el
    diseño y esta ficha chocan, manda el diseño.
  - **Ficheros**: `.claude/settings.json` (nuevo: hoy el repo **no tiene** `.claude/settings.json`,
    medido con `ls .claude/` → solo `skills/`) y el script que el diseño nombra:
    `.claude/hooks/verificar-entorno.sh` (texto íntegro en `flujo-web-local.md` §4).
  - **Hecho cuando**: con `CLAUDE_CODE_REMOTE=true` el script imprime la versión de Go,
    la de `golangci-lint`, un aviso literal si no es `v2.12.2` y si Docker responde; **sin** la
    variable (local) imprime lo mismo salvo Docker —el diseño lo quiere así para delatar el lint
    v2.14.0 de la máquina local— y sale con `rc=0`. Nunca bloquea la sesión (`rc=0` siempre:
    avisa, no impide).
  - **Gate**: `bash .claude/hooks/verificar-entorno.sh; echo rc=$?` y
    `CLAUDE_CODE_REMOTE=true bash .claude/hooks/verificar-entorno.sh; echo rc=$?` → `rc=0` los dos;
    `python3 -m json.tool .claude/settings.json >/dev/null; echo rc=$?` → `rc=0`.
  - **Commit**: `andamiaje(f0): hook SessionStart que verifica la toolchain en la web`

## Bloque B · `pendiente` y los `make` · 🌐 · T0.2–T0.4

Para cuando: `make test-pendiente` imprime `PENDIENTES=0` con `rc=0` y el gate ci-local, que ya
incluye `vet-pendiente`, da `GATE_RC=0`.

- [x] **T0.2 · rojo(f0): contrato de `internal/pendiente`** · 🌐 · dep. — · cumple R0.2.a, R0.2.b — cerrada en `d7600d3` (sesión F0-02, 2026-09-30: `-tags pendiente` rc=1 por el `panic`; sin etiqueta `no test files`; `vet -tags pendiente` rc=0)
  - **Ficheros**: `internal/pendiente/pendiente.go`, `internal/pendiente/pendiente_test.go`
  - **Contrato**: el de [`diseno.md`](diseno.md) §2. Cuerpo: `panic("pendiente: sin implementar")`
    (literal: el paquete no puede usarse a sí mismo). Test con `//go:build pendiente`.
  - **Hecho cuando**: `go test -tags pendiente -run '^TestImplementar' ./internal/pendiente/`
    da `rc≠0` por el `panic`; sin la etiqueta, `no test files`.
  - **Gate**: `GOWORK=off go vet -tags pendiente ./internal/pendiente/; echo rc=$?` → `rc=0`
  - **Commit**: `andamiaje(f0): rojo — contrato de pendiente`

- [x] **T0.3 · verde(f0): `internal/pendiente`** · 🌐 · dep. T0.2 · cumple R0.2.a, R0.2.b — cerrada en `f3b322c` (sesión F0-02: `-race -cover` 100,0 %, 14 `--- PASS`, 0 SKIP; gate ci-local `GATE_RC=0`)
  - **Ficheros**: los dos de T0.2; se quita la etiqueta.
  - **Hecho cuando**: `go test -race -cover ./internal/pendiente/` `rc=0` y cobertura 100 %.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): verde — pendiente`

- [x] **T0.4 · `make vet-pendiente`, `make test-pendiente` y `ci-local`** · 🌐 · dep. T0.3 · cumple R0.2.c–R0.2.f — cerrada en `d74dd7f` (sesión F0-02: `PENDIENTES=0`, `ROJOS=0`, `rc=0`; muerden: `vet-pendiente` rc=2 con el error de tipos, `PENDIENTES=1` con `rc=0`; gate `GATE_RC=0`). ⚠️ La receta de deshacer `git stash -u && git stash drop` se lleva también el `Makefile` **sin commitear** de esta misma tarea (contradicción 12 del `README.md`)
  - **Ficheros**: `Makefile` (targets nuevos y la línea `ci-local:`; ver [`diseno.md`](diseno.md) §3).
  - **Hecho cuando**: `make test-pendiente` imprime `PENDIENTES=0` y `ROJOS=0` con `rc=0`;
    `ci-local` es `fmt-check vet vet-pendiente lint test build`. **Demostración de que muerden**
    (en el árbol, sin commitear, y se deshace con `git stash -u && git stash drop`): un fichero
    `internal/pendiente/zz_test.go` con `//go:build pendiente` y un error de tipos → `make
    vet-pendiente` da `rc≠0`; un `x.go` con una llamada `pendiente.Implementar("x")` →
    `PENDIENTES=1`. Las dos salidas se pegan en el mensaje del commit.
  - **Gate**: gate ci-local; `make test-pendiente; echo rc=$?` → `PENDIENTES=0`, `rc=0`.
  - **Commit**: `andamiaje(f0): etiqueta pendiente — vet-pendiente en ci-local y test-pendiente`

## Bloque C · los candados de fichero · 🌐 · T0.5–T0.9

Para cuando: los cinco candados de fichero corren en `ci-local` sobre el árbol real (vacío de
módulos: pasan), cada uno tiene un caso en `testdata/` que **lo pone rojo**, y el gate ci-local da
`GATE_RC=0`.

- [x] **T0.5 · rojo(f0): contratos de `internal/candados`** · 🌐 · dep. T0.4 · cumple R0.3.a–R0.3.g — cerrada en `2c2bbd6` (sesión F0-03, 2026-09-30: 10 cuerpos con `panic`, `PENDIENTES=10`, `ROJOS=6`; `vet -tags pendiente` rc=0; `go test -tags pendiente` rc=1 por el `panic`; gate `GATE_RC=0`). Preparada por `3040e82` (los contadores de `test-pendiente` ignoran `testdata/`, contradicción 14). Los dos `perfil.out` de `testdata/cobertura/` van con `internal/candados/testdata/.gitignore` (`!*.out`): la raíz ignora `*.out` (contradicción 15). Contrato ampliado con `Reglas.FasesCerradas` (regla 6), `Fuente.Paquete`/`Fset`, `MarcaPostgres`, `Exentos` y `Evaluables` (las cifras de T0.9)
  - **Ficheros**: `internal/candados/{candados,fronteras,unfichero,exportados,sinbdviva,cobertura}.go`,
    sus seis `_test.go` (etiqueta `pendiente`) y los árboles de prueba en
    `internal/candados/testdata/<candado>/{muerde,pasa}/…` (ver [`diseno.md`](diseno.md) §4).
  - **Hecho cuando**: cada exportado de los seis ficheros aparece en su test (lo exigirá el propio
    candado de T0.7); cada test tiene **al menos un caso `muerde`** que espera ≥ 1 violación con
    el fichero y el motivo; `make test-pendiente` → `PENDIENTES` = nº de cuerpos con `panic`.
  - **Gate**: `GOWORK=off go vet -tags pendiente ./internal/candados/; echo rc=$?` → `rc=0`
  - **Commit**: `andamiaje(f0): rojo — contratos de los candados de la reconstrucción`

- [x] **T0.6 · verde(f0): `internal/candados`, un fichero por commit** · 🌐 · dep. T0.5 · cumple R0.3.a–R0.3.g — cerrada en `b2ecfce` (candados.go 91,7 %), `65d4bc0` (fronteras.go 100 %), `e61567e` (unfichero.go 96,8 %), `681d84e` (exportados.go 100 %), `d48e319` (sinbdviva.go 100 %), `42884fb` (cobertura.go 99,3 %) (sesión F0-03: `go test -race -v` 104 `--- PASS`, 0 SKIP; `PENDIENTES=0`, `ROJOS=0`; gate `GATE_RC=0` en cada commit, `go test ./internal/candados/` rc=0 en cada commit sobre un clon limpio)
  - **Ficheros**: los seis de T0.5, en seis commits (`candados.go` primero: los otros lo usan).
  - **Hecho cuando**: `go test -race ./internal/candados/ -v` → `rc=0`, `--- SKIP` = 0, y cada
    caso `muerde` **pasa porque detecta** la violación (no porque no haya ficheros: cada caso
    afirma también el número de ficheros recorridos > 0). `make test-pendiente` → `PENDIENTES=0`.
  - **Gate**: gate ci-local tras cada commit.
  - **Commit**: `andamiaje(f0): verde — candados/<fichero>` (×6)

- [x] **T0.7 · los tres candados del árbol en `internal/modulos/`** · 🌐 · dep. T0.6 · cumple R0.3.a–R0.3.d — cerrada en `ca462a6` (sesión F0-03: lista blanca **medida**, 13 aristas sobre `dev` @ `c55e9e3`; 3 `--- PASS`, 0 SKIP; recorridos: fronteras 901, un fichero 18, exportados 18; muerden con un fichero sin test, un exportado sin mencionar y un import a `internal/flujos/store` —regla 2—; gate `GATE_RC=0`)
  - **Ficheros**: `internal/modulos/doc.go` (solo comentario de paquete: qué es el árbol y la
    tabla de módulos de D-5), `internal/modulos/fronteras_test.go` (con la **tabla** de
    [`diseno.md`](diseno.md) §4.1: capas, puentes —vacía—, conmutados —vacía— y el mapeo viejo→
    módulo de `04` §4), `internal/modulos/un_fichero_un_test_test.go`,
    `internal/modulos/exportados_cubiertos_test.go`.
  - **Medir antes de escribir la tabla**: correr el script de `02` §5 con el `MAP` de D-5
    (`acceso` fusionado con `operador`) sobre `dev` y copiar a la tabla las aristas
    módulo→módulo que hoy existen, **con la fecha y el SHA** en el comentario. Sin medir hoy.
  - **Hecho cuando**: los tres tests pasan sobre el árbol real con el **alcance** de
    [`diseno.md`](diseno.md) §4 —`internal/modulos`, `internal/nucleo` (vacío hasta F1: el piloto
    lo necesita cubierto, F1 entrada E2), `internal/apipublica`, `internal/pendiente`,
    `internal/candados`, `internal/arranque/huellatest`— y afirman `recorridos > 0` (sin módulos:
    recorren `pendiente`, `candados`, `huellatest` y `apipublica` cuando exista). Los paquetes
    `…test` siguen la regla de D-F1-3 (`diseno.md` §4.2).
  - **Gate**: gate ci-local; `GOWORK=off go test -v ./internal/modulos/ 2>&1 | grep -c -- '--- SKIP'` → `0`.
  - **Commit**: `andamiaje(f0): candados de fronteras, un fichero un test y exportados cubiertos`

- [x] **T0.8 · `test/procesos/sin_bd_viva_test.go`** · 🌐 · dep. T0.6 · cumple R0.3.e — cerrada en `b522b0f` (sesión F0-03: `--- PASS: TestSinBDViva`, recorridos = 2; muerde con un `_test.go` `integracion` con `postgres://…:5432`; skill corregida; gate `GATE_RC=0`)
  - **Ficheros**: `test/procesos/doc.go` (paquete `procesos`, **sin** etiqueta), 
    `test/procesos/sin_bd_viva_test.go` (**sin** etiqueta `integracion`: tiene que correr en
    `ci-local`; ver [`reglas.md`](reglas.md) §3).
    Y `.claude/skills/procesos-testcontainers/SKILL.md`: la línea «Todo con `//go:build
    integracion`» pasa a exceptuar el candado.
  - **Hecho cuando**: pasa sobre el directorio (hoy solo él y `doc.go`); su caso `muerde` vive en
    `internal/candados/testdata/sinbdviva/muerde/` (T0.5).
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): candado sin_bd_viva para los procesos de F9`

- [x] **T0.9 · `make cobertura-ficheros`** · 🌐 · dep. T0.6 · cumple R0.3.f, R0.3.g — cerrada en `3e85144` (sesión F0-03: `FICHEROS_EVALUADOS=7`, `POR_DEBAJO=0`, `EXENTOS_POSTGRES=0`, rc=0; muerde con un fichero al 0,0 % y con `testdata/cobertura/muerde` —`POR_DEBAJO=3`—; filtra con `[ -d ]` antes de `go list`; gate `GATE_RC=0` con `cobertura-ficheros` dentro de `ci-local`)
  - **Ficheros**: `cmd/cobertura-ficheros/main.go` (lee el perfil, llama a
    `candados.Cobertura`, imprime la tabla y sale con `rc=1` si hay violaciones), `Makefile`
    (target nuevo y `ci-local` lo incluye; ver [`diseno.md`](diseno.md) §3).
  - **Hecho cuando**: `make cobertura-ficheros` imprime `FICHEROS_EVALUADOS=N`,
    `POR_DEBAJO=0`, `EXENTOS_POSTGRES=0` y `rc=0`. Muerde: el caso de `testdata/cobertura/muerde/`
    (un perfil con un fichero al 50 %) da una violación en `candados` (T0.6).
  - **Gate**: gate ci-local; `make cobertura-ficheros; echo rc=$?` → `rc=0`.
  - **Commit**: `andamiaje(f0): cobertura por fichero (≥ 80 %, D-12) en ci-local`

## Bloque D · el arranque nuevo y la huella · 🌐 · T0.10–T0.15

Para cuando: `cmd/server-modular` compila, `internal/arranque` es copia del viejo (diff solo en
cabeceras y en las dos rutas relativas de T0.10), y **las dos huellas son idénticas a la dorada**.
Necesita D-F0-1 y D-F0-2 (ver [`README.md`](README.md)); sin ellas, **parar** en T0.11.

- [x] **T0.10 · `internal/arranque` como copia del arranque viejo** · 🌐 · dep. T0.7 · cumple R0.4.a–R0.4.d — cerrada en `d64dbbf` (sesión F0-04, 2026-09-30: 21 + 19 ficheros con `cp` y cabecera en la línea 1; el bucle `diff` da las dos rutas relativas **y** la línea `var _ func(context.Context) error = Ejecutar` de `orquestador_test.go`, que exige `exportados_cubiertos` —contradicción 17—; `git diff --stat -- internal/bootstrap` vacío; `go test -race -v` 53 `Test*`, 53 PASS, 0 SKIP; gate `GATE_RC=0` en *worktree* limpio)
  - **Ficheros**: los **21** `.go` de producción de `internal/bootstrap/arranque/` copiados a
    `internal/arranque/` (lista en [`arquitectura.md`](arquitectura.md) §2) con una cabecera
    `// Copia de internal/bootstrap/arranque/<f> @ <sha> (F0 · 05 §6): cablea paquetes VIEJOS.`;
    y **19 de sus 20** tests (todos menos `pool_metrics_integration_test.go`, que usa
    `WAPP_TEST_DB_DSN` y `t.Skipf`: prohibido en código nuevo, E-5). Dos ajustes obligados:
    `invitaciones_cableado_test.go:98` y `roleplane_cableado_test.go:69` leen
    `../../publicapi/roleplane.go`, que desde `internal/arranque` es `../publicapi/roleplane.go`.
  - **Cómo se copia** (E-1: se crea, no se mueve): `cp` fichero a fichero; **prohibido** `git mv`
    y prohibido tocar un solo byte de `internal/bootstrap/`.
  - **Hecho cuando**: `for f in internal/bootstrap/arranque/*.go; do diff <(grep -v '^// Copia de' internal/arranque/$(basename $f)) $f; done`
    muestra solo las dos rutas relativas; `git diff --stat -- internal/bootstrap` vacío;
    `go test -race -v ./internal/arranque/` → `rc=0`, 53 `Test*` (54 − 1), `--- SKIP` = 0.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): internal/arranque, copia exacta del arranque viejo (cablea paquetes viejos)`

- [x] **T0.11 · `cmd/server-modular`** · 🌐 · dep. T0.10 · cumple R0.4.a, R0.4.e — cerrada en `a953834` (sesión F0-04: `diff` con `cmd/server/main.go` = import + llamada; `go build` rc=0; `go list -deps … | grep -c internal/bootstrap` → 0; gate `GATE_RC=0`)
  - **Ficheros**: `cmd/server-modular/main.go` — copia de `cmd/server/main.go` (36 l) que llama a
    `arranque.Ejecutar(ctx)` de `internal/arranque` en vez de `bootstrap.Run`. **No** se copian
    `cmd/server/integration_test.go` ni `flows_integration_test.go` (ver
    [`diseno.md`](diseno.md) §5.3).
  - **Hecho cuando**: `GOWORK=off go build -o /tmp/server-modular ./cmd/server-modular; echo rc=$?`
    → `rc=0`; `go list -deps ./cmd/server-modular | grep -c internal/bootstrap` → `0`.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): cmd/server-modular, el segundo arranque`

- [x] **T0.12 · rojo(f0): contrato de `internal/arranque/huellatest`** · 🌐 · dep. T0.6 · cumple R0.5.a–R0.5.f — cerrada en `c7ae487` (sesión F0-04: 10 cuerpos con `panic`, `PENDIENTES=10`, `ROJOS=1`; `vet -tags pendiente` rc=0; `go test -tags pendiente` rc=1 por el `panic`; los cinco `muerde` en `TestDiferenciaMuerde`; gate `GATE_RC=0`)
  - **Ficheros**: `internal/arranque/huellatest/huellatest.go` + `huellatest_test.go` (etiqueta
    `pendiente`), contrato de [`diseno.md`](diseno.md) §6.2.
  - **Hecho cuando**: el test tiene casos `muerde` por componente: un mux con una ruta de más, uno
    con una de menos, un `grpc.Server` con un servicio de más, un `/metrics` con una familia de
    menos, un paquete de prueba con una `go` de más, y cada uno exige una línea en `Diferencia`.
  - **Gate**: `GOWORK=off go vet -tags pendiente ./internal/arranque/...; echo rc=$?` → `rc=0`
  - **Commit**: `andamiaje(f0): rojo — contrato de huellatest`

- [x] **T0.13 · verde(f0): `huellatest`** · 🌐 · dep. T0.12 · cumple R0.5.a–R0.5.f — cerrada en `141d960` (sesión F0-04: 91,8 %, 34 PASS, 0 SKIP; `PENDIENTES=0`, `ROJOS=0`; `Goroutines` con importador `gc` + `go list -export` en vez de `source` —105 s → 0,55 s por paquete, contradicción 20—; gate `GATE_RC=0`)
  - **Hecho cuando**: `go test -race -cover ./internal/arranque/huellatest/` → `rc=0`, ≥ 80 %.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): verde — huellatest`

- [x] **T0.14 · la huella del arranque VIEJO y la dorada** · 🌐 · dep. T0.13, **D-F0-2** · cumple R0.5.a–R0.5.c, R0.5.g — cerrada en `fde5849` (sesión F0-04: 95 rutas = 22 + 73 en los dos perfiles, 2 rpc, 11 familias en frío —las cifras de la spec—; 0,25 s; la fase 3 corre **entera** con un S3 falso en proceso, contradicción 19; tres `-actualizar` seguidos dan la misma dorada; `git diff --stat origin/dev -- internal/bootstrap` = solo el fichero nuevo; gate `GATE_RC=0`)
  - **Ficheros**: `internal/bootstrap/arranque/huella_vieja_test.go` (**el único fichero que F0
    añade al paquete viejo**, y es de test) y `internal/arranque/testdata/huella.json`.
  - **Qué hace**: arma el «contenedor de huella» ([`diseno.md`](diseno.md) §6.3: fase 1 y el
    grupo `flowDeps` de la fase 3 simulados sin red; fases 2–8 **reales**; la 9 no se ejecuta),
    calcula la parte de ejecución de la huella con `huellatest` y la compara con la dorada. Con
    `-args -actualizar` **reescribe** la dorada: es el **único** que puede escribirla.
  - **Hecho cuando**: la dorada tiene **95** rutas (**22** en `:8100` + **73** en `:8103`), **2**
    rpc, las familias `wapp_*` en frío y los dos perfiles de configuración; `go test -run
    '^TestHuellaVieja' -v ./internal/bootstrap/arranque/` → `rc=0`. Si salen otras cifras, se
    anotan en el commit y en `README.md` §Contradicciones con el comando (no se «ajustan»).
  - **Gate**: gate ci-local; `git diff --stat -- internal/bootstrap` → solo el fichero nuevo.
  - **Commit**: `andamiaje(f0): la huella del arranque viejo, en una dorada`

- [x] **T0.15 · `internal/arranque/huella_test.go` — el candado** · 🌐 · dep. T0.14 · cumple R0.5.a–R0.5.h — cerrada en `61ce04b` (sesión F0-04: `TestHuella` y `TestHuellaEstatica` rc=0 —10 goroutines, **13** *hooks* de 12 métodos (contradicción 18), entorno ∅—; muerde: sin `/admin/crypto/rekey` → `:8100 falta /admin/crypto/rekey`, con una `go c.intakePipeline.Run` de más → `goroutines sobra pipeline.Worker.Run`; salidas en el commit; gate `GATE_RC=0`)
  - **Qué hace**: el mismo contenedor de huella sobre el arranque NUEVO, comparado con la misma
    dorada; y la parte **estática** (goroutines, *hooks* de métricas, lectura de entorno) calculada
    sobre **los dos** directorios de fuente y comparada entre sí.
  - **Hecho cuando**: `go test -run '^TestHuella' -v ./internal/arranque/` → `rc=0`. **Muerde**
    (sin commitear, se deshace): comentar el `mux.Handle("/admin/crypto/rekey", …)` de
    `internal/arranque/rutas_admin.go` → el test falla nombrando `:8100 /admin/crypto/rekey`;
    duplicar `go c.intakePipeline.Run(ctx)` en `internal/arranque/fase9_fondo.go` → falla en
    `goroutines`. Salidas pegadas en el commit.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): candado de huella entre los dos arranques`

## Bloque E · la cara nueva vacía, los ✎ de `platform` y la deriva · 🌐→💻 · T0.16–T0.21, T0.27

Para cuando: `apipublica` montada y vacía con la huella **igual**, los tres ✎ hechos con la
huella **igual** y `go list` sin aristas `platform → dominio`, la deriva documental cerrada, y el
traspaso escrito para el bloque F.

- [x] **T0.16 · `internal/apipublica` vacía, montada delante del `publicapi` viejo** · 🌐 · dep. T0.15 · cumple R0.6.a–R0.6.c — cerrada en `5a11f5b` (rojo, TX.1), `de0c29b` (verde, TX.2: 42 PASS, 100 % los dos ficheros) y `9dcf7e8` (montaje, TX.3) (sesión F0-05, 2026-09-30: `TestCaraVacia_TodoALaVieja` —código, cuerpo y cabeceras del viejo, su 404 y su 405—; huella igual; `fronteras_test` verde; gate `GATE_RC=0`). Desviación: la copia cambia además en `fase8_transporte.go` y `contenedor.go` (campo `publicCompuesto`, contradicción 24). Con TX.4 (`7b7e01f`): `mapa.tsv`, `mudanzas.go` (`FaseActual = 0`) y el candado de mudanzas
  - **Mecanismo**: el de [`../FX-cara-http/diseno.md`](../FX-cara-http/diseno.md) (cómo se
    compone el `http.Handler` del `:8103`, cómo gana la cara nueva, cómo se decide el
    *fallback*). Esta tarea **no lo rediseña**.
  - **Ficheros**: `internal/apipublica/apipublica.go` (comentario de paquete + el símbolo de
    montaje que fije FX, que en F0 **no registra ninguna ruta**) + `apipublica_test.go`;
    `internal/arranque/http.go` (primera y única desviación de la copia en F0: el montaje).
  - **Hecho cuando**: la huella del arranque nuevo **no cambia** (`TestHuella` verde con la misma
    dorada); `apipublica_test.go` afirma que el montaje vacío deja pasar **toda** petición al
    viejo; `fronteras_test` verde (`apipublica` no importa nada viejo).
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): apipublica vacía, montada delante del publicapi viejo`

- [x] **T0.17 · ✎ `platform/httpapi/admin.go` deja de importar `gateway/session`** · 🌐→💻 · dep. T0.15, **D-F0-3** · cumple R0.7.a, R0.7.d, R0.7.e — cerrada en `6d83620` (sesión F0-05: el centinela vive en `platform/httpapi/admin.go`; `session` hace `var ErrSessionOffline = httpapi.ErrSessionOffline`; `grep -c internal/gateway` → 0; tests viejos de `httpapi`, `gateway/...`, `publicapi`, `flujos/admin` verdes sin editar; huella igual; gate `GATE_RC=0`). Integración vieja: T0.22 (💻)
  - **Ficheros**: `internal/platform/httpapi/admin.go` (`:13` el import, `:306` el único uso:
    `errors.Is(err, session.ErrSessionOffline)`), el centinela nuevo en `platform` y
    `internal/gateway/session/registry.go:22` (una línea: el centinela viejo pasa a **ser** el de
    `platform`). Detalle en [`diseno.md`](diseno.md) §7.
  - **Hecho cuando**: `GOWORK=off go list -f '{{join .Imports "\n"}}' ./internal/platform/httpapi | grep -c internal/gateway` → `0`;
    el texto `"sesión offline"` no cambia; los tests viejos de `httpapi` y de `session` verdes;
    `TestHuella` verde con la dorada intacta.
  - **Gate**: gate ci-local. **La integración vieja la cierra T0.22 (💻).**
  - **Commit**: `andamiaje(f0): platform/httpapi deja de depender de gateway/session`

- [x] **T0.18 · ✎ `platform/httpapi/audit_mw.go` deja de importar `iam/ports/in`** · 🌐→💻 · dep. T0.17 · cumple R0.7.b, R0.7.d, R0.7.e — cerrada en `b65b788` (sesión F0-05: `httpapi.AuditInput`; `in.AuditInput = httpapi.AuditInput`; `grep -c internal/iam` → 0; el arranque viejo compila sin tocarlo; huella igual; gate `GATE_RC=0`). Queda caducado el comentario de `usecases.go:~155-158` (contradicción 28)
  - **Ficheros**: `internal/platform/httpapi/audit_mw.go` (`:8` import, `:34` y `:85`:
    `in.AuditInput`), el DTO nuevo en `platform`, `internal/iam/ports/in/usecases.go:129` (una
    línea: `AuditInput` pasa a ser alias del DTO de `platform`).
  - **Hecho cuando**: `go list … ./internal/platform/httpapi | grep -c internal/iam` → `0`;
    `*iamusecase.AuditService` sigue satisfaciendo `httpapi.AuditRecorder` sin adaptador (compila
    el arranque viejo **sin tocarlo**); huella intacta.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): platform/httpapi deja de depender de iam/ports/in`

- [x] **T0.19 · ✎ `platform/metrics/inferstats.go` deja de importar `internal/inferstats`** · 🌐→💻 · dep. T0.18 · cumple R0.7.c–R0.7.e — cerrada en `5305134` (sesión F0-05: `Agregado` en el paquete hoja **`internal/platform/metrics/inferencia`**, no en `platform/metrics` —ciclo en test, contradicción 23, decidido por el usuario—; `inferstats.Agregado = inferencia.Agregado`; el criterio de los tres ✎ → 0; `inferstats_test.go` verde sin editar; huella igual; gate `GATE_RC=0`)
  - **Ficheros**: `internal/platform/metrics/inferstats.go` (`:9` import, `:16` `type
    FuenteInferencia func() inferstats.Agregado`), `internal/inferstats/inferstats.go:144` (una
    línea: `Agregado` pasa a ser alias del tipo que ahora declara `platform/metrics`, con **su
    comentario entero**).
  - **Hecho cuando**: `go list … ./internal/platform/... | grep -cE 'internal/(gateway|iam|inferstats)'` → `0`
    (el criterio de las tres); los cinco nombres `wapp_edge_*` intactos
    (`internal/platform/metrics/inferstats_test.go` verde); huella intacta.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): platform/metrics deja de depender de inferstats`

- [x] **T0.20 · la deriva documental de `03` §2.2** · 🌐 · dep. T0.10 · cumple R0.8.a — cerrada en `8096232` (sesión F0-05: 17 de las 18 líneas corregidas + 5 rutas caducadas que el `grep` no ve; el `grep` → 1, `deuda.md:139`, historia. `README.md:53` **no** era historia, contradicción 26)
  - **Ficheros**: `documentations/{README,constitucion,contratos,operacion,deuda}.md` — las **18**
    líneas medidas con
    `grep -n 'internal/bootstrap/[a-z_]*\.go\|internal/publicapi/flows.go' documentations/*.md | grep -v bootstrap/arranque | wc -l`
    (→ 18 el 2026-09-29 sobre `dev` @ `7021144`; el plan decía 17), más «8 `*_cableado_test.go`» (`constitucion.md` §5, `deuda.md` D-9) → son **9**
    y viven en `internal/bootstrap/arranque/`. Cada ruta se corrige a la de hoy **y** se añade
    «(copia en `internal/arranque/` desde F0)» donde el lector deba saberlo. `deuda.md:137` y
    `README.md:53` narran historia: se corrige solo lo que afirma el presente.
  - **Hecho cuando**: el mismo `grep` devuelve solo las líneas de historia, y `03` §2.2 y `ESTADO`
    marcan la deriva como cerrada.
  - **Gate**: `grep … | wc -l` → nº de líneas de historia, dicho en el commit.
  - **Commit**: `docs(reorganizacion-modular): F0 cierra la deriva de rutas del arranque`

- [x] **T0.21 · el traspaso a la sesión local** · 🌐 · dep. T0.16–T0.20 · cumple R0.9.a — cerrada en `15223ff` (sesión F0-05: `traspasos/TRASPASO-F0-andamiaje.md`, rama `reorg/f0-e-cara-platform` empujada, `push rc=0`)
  - **Ficheros**: `documentations/reorganizacion-modular/traspasos/TRASPASO-F0-andamiaje.md`, con
    la skill `traspaso-web-local` (ocho secciones). La §4 lleva T0.22–T0.24 literales; la §7,
    como mínimo: que la simulación de fases 1 y 3 es la misma en los dos lados, que los alias de
    D-F0-3 no cambian ningún texto, y que el `go.sum` no cambió.
  - **Hecho cuando**: el fichero existe y la rama de la web está empujada (su `rc`).
  - **Commit**: `docs(reorganizacion-modular): traspaso de F0 a la sesión local`

## Bloque F · el cierre local · 💻 · T0.22–T0.25

Para cuando: los cinco criterios de salida de [`README.md`](README.md) se cumplen y `dev`
contiene F0 entera.

- [ ] **T0.22 · integración vieja con Postgres real tras los ✎** · 💻 · dep. T0.19, T0.21 · cumple R0.7.f
  - **Por qué**: los ✎ tocan código viejo compartido por los dos arranques (DT-52: 438 tests
    saltados con la pantalla en verde, `05` E-5).
  - **Qué se corre**: `INTEGRATION_PG_PORT=<libre> make test-integration` **y**, para contar,
    la misma batería con `-v` a un log: el `Makefile` ya exporta `WAPP_TEST_REQUIRE_DB=1`, pero
    solo **50** de los **91** ficheros de test que leen `WAPP_TEST_DB_DSN` lo honran
    (`grep -rln WAPP_TEST_REQUIRE_DB --include='*.go' . | wc -l` → 50;
    `grep -rln WAPP_TEST_DB_DSN --include='*_test.go' . | wc -l` → 91, 2026-09-28): los otros
    41 se saltarían en silencio si la BD fallara. Por eso se cuentan los SKIP.
  - **Hecho cuando**: `rc=0`; `grep -c -- '--- SKIP'` → `0`; `--- FAIL` → `0`; y el nº de
    `--- PASS` ≥ el de `ESTADO.md` (4.318, 2026-09-27) o la diferencia explicada.
  - **Commit**: ninguno (se anota en el `CERRADO` del traspaso).

- [ ] **T0.23 · arranque real de `cmd/server-modular` en local** · 💻 · dep. T0.22 · cumple R0.4.f
  - **Qué se corre**: el binario nuevo, **nunca a la vez** que `cmd/server`, contra una base
    **desechable** (contenedor efímero en puerto libre, jamás UAT ni el Postgres compartido) y
    con el R2/MinIO de desarrollo que ya usa Jhoan (`flows.go:75` hace `HeadBucket` y sin él no
    arranca). Es la única prueba de F0 de las fases 1 y 3 **reales** del arranque nuevo.
  - **Hecho cuando**: el log trae las nueve líneas `arranque: fase completada` (`9/9`), `curl
    -s :8100/healthz` 200, y se apaga limpio con SIGINT (`servidor detenido limpiamente`).
  - **Commit**: ninguno.

- [ ] **T0.24 · integrar F0 en `dev`** · 💻 · dep. T0.22, T0.23 · cumple R0.9.b
  - **Qué se hace**: gate ci-local en local con la toolchain fijada; merge de la rama de la web
    **sin squash** (`rojo`/`verde` distintos, E-4); `git push origin dev` leyendo su `rc`.
  - **Hecho cuando**: `git log origin/dev` contiene los commits de T0.1–T0.21 en orden.

- [ ] **T0.25 · cerrar F0 en la documentación** · 💻 · dep. T0.24 · cumple R0.9.c
  - **Ficheros**: `CERRADO <fecha>` en el traspaso; `ESTADO.md` (fase actual: F0 cerrada, F1
    siguiente); `README.md` de esta carpeta (estado y SHA de cada tarea).
  - **Commit**: `docs(reorganizacion-modular): F0 cerrada`

## Añadidas después (van en el bloque que se indica; los IDs no se renumeran)

- [x] **T0.26 · `make lint` falla si `golangci-lint` no es `v2.12.2`** · 🌐 · bloque B, dep. T0.4, **decisión T-1 del marco** · cumple R0.2.g — cerrada en `d05ac3a` (sesión F0-02: con v2.12.2 `rc=0`; binario falso 2.14.0 y sin binario → `rc=2` y el mensaje; gate `GATE_RC=0`)
  - **Por qué**: `Makefile:12` declara `LINT_VERSION := v2.12.2` pero `lint` corre el binario que
    haya en el `PATH`; otra versión da otro resultado y el gate deja de ser autoritativo
    ([`../00-marco/tecnologia.md`](../00-marco/tecnologia.md), T-1). Si Jhoan dice «no», se tacha.
  - **Ficheros**: `Makefile` (el target `lint` compara `golangci-lint version` con
    `$(LINT_VERSION)` antes de correr y sale con un mensaje literal si no casan).
  - **Hecho cuando**: con la versión fijada, `make lint; echo rc=$?` → `rc=0`; con un binario
    falso en el `PATH` que imprime otra versión (sin commitear) → `rc≠0` y el mensaje.
  - **Gate**: gate ci-local.
  - **Commit**: `andamiaje(f0): lint exige la versión fijada del Makefile`

- [x] **T0.27 · los barridos AST viejos dejan de ver el árbol nuevo** · 🌐 · bloque E, dep. T0.16, **decisión D-F4-1** · cumple R0.3.h — cerrada en `dd1e2bd` (sesión F0-05: muerde al revés RC=1 → RC=0, 0 SKIP; en `c2_via_test.go` la condición va en un ayudante `esDelArbolNuevo` porque en línea `gocyclo` subía a 18 —+14 líneas, no una—; gate `GATE_RC=0`)
  - **Por qué**: `internal/llmvia/c2_via_test.go:117` (`filepath.WalkDir("..")`) e
    `internal/iam/infra/postgres/membresia_unica_ast_test.go:97` (`WalkDir(raizDelBarrido)`, con
    `raizDelBarrido = "../../.."` en `:74`) recorren **todo** `internal/` y exigen una lista
    **exacta** (ficheros que comparan por vía; escritores de `tenant_members`). El primer `verde`
    de `modulos/acceso/iam/infra/postgres/memberships.go` (F2) o de
    `modulos/inferencia/tenantllm/tenantllm.go` (F4) los pondría rojos, y con ellos `ci-local`.
    Es la segunda excepción a E-1 (la primera, D-F0-3) y **subsume D-F2-2**. El árbol nuevo trae
    sus propios candados (F2: el de membresía en `acceso`; F4: el C2 en `inferencia/llmvia`, que
    barren el árbol nuevo). F2 y F4 **verifican** esta tarea; no la repiten. Si Jhoan dice «no», se
    tacha y valen las alternativas: D-F2-2 en F2 y ampliar la lista del C2 viejo en F4, TX.13 y F7.
  - **Ficheros**: esos dos tests viejos, **una línea** en cada callback de `WalkDir`, antes de
    cualquier otro `return`: si la entrada es un directorio de **primer nivel** de `internal/` en
    {`modulos`, `nucleo`, `arranque`, `apipublica`, `pendiente`, `candados`}, `return
    filepath.SkipDir`. 🔴 Se compara la **ruta relativa a `internal/`**, no `d.Name()`:
    `internal/bootstrap/arranque` también se llama `arranque` y tiene que seguir barriéndose.
    (`candados` es nuevo desde T0.5; sus `testdata/` no deben poder poner rojo un candado viejo.)
  - **Hecho cuando**: `git diff --stat` → solo esos dos ficheros, +1 línea cada uno (más un
    import si hace falta); sus listas (`escritoresEsperados`, los permitidos del C2) **sin tocar**.
    **Muerde al revés** (en el árbol, sin commitear, se deshace con `git stash -u && git stash
    drop`): un `internal/modulos/zz/zz.go` con `if via == "local"` y el literal
    `"INSERT INTO public.tenant_members"` → antes de la línea, los dos candados viejos en rojo;
    después, verdes. Las dos salidas se pegan en el mensaje del commit.
  - **Gate**: gate ci-local; `GOWORK=off go test -count=1 -v -run 'TestC2_|TestMembresiaUnica_' ./internal/llmvia/ ./internal/iam/infra/postgres/ > "$L" 2>&1; echo "RC=$?" >> "$L"; tail -1 "$L"` → `RC=0` y `grep -c -- '--- SKIP' "$L"` → `0`.
  - **Commit**: `andamiaje(f0): los barridos AST viejos no ven el árbol nuevo (D-F4-1)`
