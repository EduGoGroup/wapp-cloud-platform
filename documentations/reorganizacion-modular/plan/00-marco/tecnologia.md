# Tecnología — toolchain, `make`, etiquetas, gates y dependencias

> *Steering* técnico del plan. Medido el **2026-09-28** sobre `dev` @ `1b18932`. Lo que no se pudo
> medir desde la máquina local se dice **«sin medir»**.

## 1 · Toolchain fijada

| Pieza | Valor | Dónde | ⚠️ Lo que no dice el número |
|---|---|---|---|
| Go | **1.26.5** | `go.mod:3` (`go 1.26.5`) · `Makefile:11` (`GO_VERSION`) | La línea `go` de `go.mod` es un **mínimo**, no una fijación, y no hay directiva `toolchain`. Con `GOTOOLCHAIN=auto` un Go **más nuevo** corre tal cual: la máquina local corre `go1.27.1` (`go version`, 2026-09-28). Para correr **exactamente** 1.26.5: `GOTOOLCHAIN=go1.26.5` (se descarga de `proxy.golang.org`) |
| golangci-lint | **v2.12.2** | `Makefile:12` (`LINT_VERSION`) | 🔴 El target `lint` (`Makefile:42`) ejecuta **el `golangci-lint` que haya en el `PATH`**: `LINT_VERSION` solo lo usa `ci-docker` (`:90`). En la máquina local el del `PATH` es **v2.14.0** (`/opt/homebrew/bin`, compilado con go1.27.1): el `ci-local` local **tampoco es autoritativo** hoy. Ver §6, decisión T-1 |
| Módulo | `github.com/EduGoGroup/wapp-cloud-platform`, público, sin `replace` | `go.mod:1` | Se construye aislado: todo `go` se corre con `GOWORK=off` (`Makefile:13`) |
| Postgres de UAT | `postgres:17-alpine` (dato del ecosistema: UAT es un Postgres 17 en Docker en el VPS) | `05` §7.2 | `make test-integration` (viejo) usa **`postgres:16`** (`Makefile:57`) |
| Lint | `.golangci.yml` v2: 16 linters + 2 formateadores, `run.tests: true`, **sin `build-tags`** | `.golangci.yml:4-6` | El lint **no ve** los ficheros tras `//go:build pendiente` ni `integracion`: los rojos se comprueban con `go vet -tags pendiente` y se lintan al pasar a verde (se quita la etiqueta) |

🚫 No se baja `go 1.26.5` para acomodar un entorno, ni se cambia `LINT_VERSION` por comodidad.

✎ **2026-10-02** (`0b78cd1`, `26cbfbf`, `3ed9bd0`): la tabla es la medida del 2026-09-28 y sus dos
⚠️ **ya no son el estado**. Hoy el `Makefile` exporta `GOTOOLCHAIN=go$(GO_VERSION)` (`Makefile:36`),
`lint` elige el `golangci-lint` de `.bin/` (lo deja `make tools`) o el del `PATH` y falla si ninguno
es `LINT_VERSION` (T-1, desde `d05ac3a`), y `make toolchain` dice qué corre de verdad. Las líneas
citadas se movieron: `GO_VERSION` `:15`, `LINT_VERSION` `:16`, `GO` `:17`. Lo vigente, web y local:
[`../../06-entorno-web.md`](../../06-entorno-web.md) §6.

## 2 · Los `make` targets

**Existen hoy** (`Makefile:30`, `.PHONY`):

| Target | Hace | Nota |
|---|---|---|
| `fmt-check` | `gofmt -l .` vacío | |
| `vet` | `go vet ./...` | sin etiquetas |
| `lint` | `golangci-lint run --timeout=5m` | el del `PATH` (§1) |
| `test` | `go test -race ./...` | los `*_integration_test.go` viejos **se saltan solos** sin `WAPP_TEST_DB_DSN` |
| `build` | `go build ./...` | |
| **`ci-local`** | `fmt-check vet lint test build` | **El gate.** Medido §4 |
| `test-integration` | `docker run postgres:16` con **nombre fijo** y puerto `5432` (sobrescribible con `INTEGRATION_PG_PORT`) + `WAPP_TEST_REQUIRE_DB=1 go test -p 1 ./...` | La batería **vieja**: solo importa en F0 (✎ de `platform`) y en F10. 💻 |
| `ci-docker` | `ci-local` dentro de `golang:1.26.5-bookworm` con el lint v2.12.2 instalado | Toolchain exacta; **no** corre integración. 💻 |
| `migrate` · `migrate-status` | `go run ./cmd/migrate [-status]` | leen `WAPP_DB_*`. 🚫 Nunca contra UAT desde el plan |

✎ **2026-10-02**: dos targets más y dos cambios sobre la tabla de arriba (`Makefile:103`, `.PHONY`).
`tools` deja el `golangci-lint` fijado en `.bin/` (binario oficial, sha256 verificado; en local, una
vez por *checkout*); `toolchain` imprime la toolchain efectiva y sale ≠ 0 si no es la fijada.
`fmt-check` usa el `gofmt` del `GOROOT` fijado, no el del `PATH`; `ci-docker` monta
`$(go env GOMODCACHE)` e instala el lint con `make tools TOOLS_DIR=/usr/local/bin`. Detalle en
[`../../06-entorno-web.md`](../../06-entorno-web.md) §6.

**Nacen en F0** (los escribe la fase `F0`; aquí solo su contrato):

| Target / cambio | Contrato |
|---|---|
| `ci-local` += `vet-pendiente` | `GOWORK=off go vet -tags pendiente ./...`: un rojo que no compila rompe el gate (`05` §5) |
| `test-pendiente` | Imprime el **número de llamadas a `pendiente.Implementar(`** en ficheros de producción **fuera de `internal/pendiente/`** (el paquete que la define no cuenta) y corre `go test -tags pendiente` sobre `internal/modulos/... internal/nucleo/... internal/arranque/...` **sin** que su fallo rompa el target: el rojo es esperado. La cifra que importa es la cuenta, no los FAIL (un `panic` aborta el binario del paquete entero, `05` E-5) |
| `cobertura-ficheros` | §5: cobertura de sentencias **por fichero** ≥ 80 % (D-12) sobre los ficheros **ya en verde**; sale con rc≠0 y lista los que no llegan |
| `test-procesos` | `GOWORK=off go test -tags integracion -v -count=1 ./test/procesos/...` contra `WAPP_PROCESOS_BINARIO=viejo` y luego `=nuevo` (✎ D-F9-7, 2026-10-02: `TestMain` sale con código 2 si `GOWORK` no vale `off`; el `Makefile` lo pone, una invocación directa lo lleva delante). En F0 puede nacer solo con el candado `sin_bd_viva_test.go`; el arnés y los procesos llegan en F9. ⚠️ La skill `procesos-testcontainers` dice que lo crea F9: **cualquiera de las dos vale** si F0 lo deja escrito |

## 3 · Las dos etiquetas de build (D-11)

| Etiqueta | Primera línea | Dónde | Quién la quita |
|---|---|---|---|
| `pendiente` | `//go:build pendiente` | Todo `x_test.go` **en rojo** de `internal/{modulos,nucleo,arranque}/**` | El commit `verde(<m>): <fichero>` |
| `integracion` | `//go:build integracion` | Todo `test/procesos/*_test.go` **salvo** `sin_bd_viva_test.go`, que va **sin etiqueta** para que `ci-local` lo corra siempre | Nadie: es permanente |

🔴 **Cero `t.Skip`** en código nuevo (E-5, DT-52). Un test que no puede correr aquí **falla** o vive
tras una etiqueta; nunca se salta con `rc=0`.

### 3.1 · La regla del rojo: el contrato lleva **solo exportados**

Hallazgo del piloto (F1 [`reglas.md`](../F1-nucleo-contact/reglas.md) T-1, sonda del 2026-09-28 con
golangci-lint 2.14.0; **se reconfirma con v2.12.2** en T1.1). El fichero de producción de un rojo
**no** lleva etiqueta —solo su `x_test.go` la lleva—, así que `make lint` lo ve; y el linter
`unused` falla ante cualquier `const`, `var`, `func`, tipo o campo **no exportado** sin uso. Un
contrato con ayudantes privados, o con un struct que ya declara sus campos, **rompe `ci-local`**.

- En el commit `rojo`: solo símbolos **exportados** (con su comentario-promesa y cuerpo
  `panic(pendiente.Implementar(…))`) y structs **sin campos**.
- Los no exportados (ayudantes, campos, constantes internas) **nacen con la lógica**, en el commit
  `verde`, junto con sus casos de test. Un exportado cuyo único propósito es un ayudante tampoco se
  inventa para esquivarlo.
- Un tipo **no exportado** que implementa un puerto (los adaptadores `internal/arranque/puente_<x>.go`)
  se mantiene «usado» con una aserción de compilación: `var _ viejo.Puerto = (*puenteX)(nil)`.
- Lo mismo vale para `internal/apipublica` y `internal/nucleo`: la regla es del linter, no del
  módulo. Toda fase la cita como «T-1 de F1»; si una fase dice otra cosa, manda esta.

## 4 · Cuánto tarda el gate (medido en local el 2026-09-28)

| Corrida | `make ci-local` | rc | Máquina |
|---|---:|---:|---|
| Con cachés calientes | **42 s** | 0 · lint `0 issues` | 8 núcleos arm64, Go 1.27.1, lint v2.14.0 |
| En frío (`GOCACHE` y caché del lint vacíos, `GOFLAGS=-count=1`) | **148 s** | 0 · `0 issues` | la misma |

Los paquetes más lentos en frío: `internal/intake/stages` 42,5 s · `internal/intake/catalogo`
31,6 s · `internal/publicapi` 15,3 s. **En la web (4 vCPU x86_64) sin medir**: con 148 s en 8
núcleos, una primera corrida pasa con holgura de los **2 min** que la herramienta de Bash espera por
defecto. Por eso el entorno web lleva `BASH_DEFAULT_TIMEOUT_MS` y `BASH_MAX_TIMEOUT_MS`
([`flujo-web-local.md`](flujo-web-local.md) §3), y el gate se lanza **escribiendo el `rc` en el
log** (§5), no leyendo la notificación.

## 5 · Cómo se lee un gate sin engañarse

```bash
L=/tmp/gate-$(date +%s).log
GOWORK=off make ci-local > "$L" 2>&1; echo "GATE_RC=$?" >> "$L"
tail -1 "$L"                                          # GATE_RC=0 o nada que celebrar
```

1. **`rc` sin pipe.** `make ci-local | tail` da el `rc` de `tail`: siempre 0. Y si se lanza en
   segundo plano con `…; echo rc=$?`, la notificación dice el `rc` del `echo`. **Pasó el
   2026-09-27** (notificación 0, gate rc=2 por el lint). El `rc` se escribe en el log y se lee de ahí.
2. **SKIP con `-v`.** Sin `-v`, `grep -c -- '--- SKIP'` da 0 siempre. En código nuevo:
   `GOWORK=off go test -v ./internal/modulos/... ./internal/nucleo/... ./internal/arranque/... 2>&1 | grep -c -- '--- SKIP'` → **0**, siempre.
3. **Pendientes, por cuenta estática.** Hasta que exista `make test-pendiente`:
   `grep -rn --include='*.go' --exclude-dir=pendiente 'pendiente\.Implementar(' internal | grep -vc '_test\.go:'`.
   Se informa **antes → después**. En F10 debe ser 0 (`no_pending_test.go`; D-F1-12, 2026-10-02: antes `sin_pendientes_test.go`).
4. **Versión de herramienta.** Un gate corrido con un lint distinto de v2.12.2 o un Go distinto de
   1.26.5 se informa **con la versión usada** y como **no autoritativo**. ✎ 2026-10-02: se comprueba
   con `make toolchain` (`TOOLCHAIN=OK`, `rc=0`). Bajo `make` la versión es la fijada; un `go`
   **suelto** en local (los de los puntos 2 y de «Cobertura por fichero») es `go1.27.1` salvo que
   lleve `GOTOOLCHAIN=go1.26.5` delante.
5. **Lo no corrido se dice «no corrido»**, con el motivo. Nunca «debería pasar».

Formato del informe: el de la skill `validar-antes-de-cerrar` («Cómo se informa»).

### Cobertura por fichero

`go test -coverprofile` escribe una línea por **bloque**: `fichero:l0.c0,l1.c1 numSentencias cuenta`.
`go tool cover -func` agrega **por función**, no por fichero; la agregación por fichero es un awk:

```bash
GOWORK=off go test -race -covermode=atomic -coverprofile=/tmp/c.out \
  ./internal/modulos/... ./internal/nucleo/... ./internal/arranque/... ; echo "rc=$?"
awk 'NR>1 { split($1,a,":"); k=$1; n[k]=$2; if ($3>0) hit[k]=1; f[k]=a[1] }
     END  { for (k in n) { t[f[k]]+=n[k]; if (hit[k]) c[f[k]]+=n[k] }
            for (x in t) printf "%6.1f%%  %s\n", 100*c[x]/t[x], x }' /tmp/c.out | sort -n
```

- El bloque se deduplica por su clave (`fichero:rango`), por si un bloque aparece dos veces.
- **Se excluyen** del umbral: los adaptadores Postgres (`postgres.go`, `*_postgres.go`,
  `repository_postgres.go`: E-6) y todo `x.go` cuyo `x_test.go` aún lleva `//go:build pendiente`
  (está en rojo: sin etiqueta no hay test que lo cubra). `make cobertura-ficheros` codifica esta
  lista; una exclusión nueva es decisión escrita.
- Sin `-coverpkg`, cada fichero cuenta **solo con los tests de su paquete**: justo lo que pide
  «un fichero, un test».

## 6 · testcontainers-go (F9)

| | Valor | Cómo se supo |
|---|---|---|
| Versión a fijar | **`github.com/testcontainers/testcontainers-go v0.44.0`** y **`…/modules/postgres v0.44.0`** (última, publicada el 2026-08-07) | `curl -s https://proxy.golang.org/github.com/testcontainers/testcontainers-go/@latest` |
| Línea `go` que exige | `go 1.25.0` (`toolchain go1.25.9`): **no sube** nuestro `go 1.26.5` | `…/@v/v0.44.0.mod` |
| Imagen | **`postgres:17-alpine`** | `05` §7.2 |
| API | `postgres.Run(ctx, img, postgres.WithDatabase(…), postgres.BasicWaitStrategies())` · `ctr.ConnectionString(ctx, "sslmode=disable")` · `ctr.Terminate(ctx)` | skill `procesos-testcontainers` |
| Quién lo importa | **Solo** `test/procesos/` | `05` §7.3 |

🔴 **Efecto medido en `go.mod`** (simulación sobre una copia de `go.mod`/`go.sum` fuera del repo,
`go get …@v0.44.0`, rc=0): añade **35** módulos y **sube tres que ya estaban**:
`felixge/httpsnoop v1.0.4 → v1.1.0`, `klauspost/compress v1.18.0 → v1.18.6` y
`otelhttp v0.67.0 → v0.69.0`. **`httpsnoop` y `otelhttp` están en el binario de producción**
(`GOWORK=off go list -deps ./cmd/server`). Es decir: aunque solo `test/procesos/` importe
testcontainers, **la selección de versiones de Go cambia dependencias de lo que se despliega**.
Decisión **T-2** (abajo).

Prohibido: `testcontainers.WithReuseByName`, `WAPP_TEST_DB_DSN` en `test/procesos/`, un puerto fijo
de Postgres, y el **PostgreSQL 16 que viene preinstalado en la VM web** (doc oficial, «Installed
tools»): es exactamente un «Postgres vivo». El candado `sin_bd_viva_test.go` lo caza.

## 7 · Lo que NO se añade

| No | Por qué |
|---|---|
| ORM, *query builder* | SQL crudo con `pgx/v5` stdlib (constitución §4) |
| Redis, broker, cola externa | ADR-0003 y constitución I-ECO-3: la durabilidad va en tablas (`webhook_outbox`, `intake_jobs`) |
| Frameworks de *mocks* (gomock, mockery…) | Los dobles se escriben a mano en `<paquete>helpertest` (E-6, D-F1-10) |
| Otra librería de aserciones | `testify v1.11.1` ya está en `go.mod`; no se suma otra |
| `godotenv` o lectura de `.env` | El proceso no lee `.env` (contratos §5) |
| Un repo `edugo-*` | ADR-0004: copia-adaptación. Única excepción ya presente: `identity-shared/auth` |
| Cualquier dependencia de producción nueva | La reconstrucción **porta**; no cambia el grafo de lo que se despliega. Si hace falta, decisión de Jhoan |
| Subir `wapp-cloudlink`, `wapp-shared/*` | Cambiaría contratos de fuera (`03` §1) |

## 8 · Decisiones que necesita Jhoan

- **T-1 · El lint del `Makefile` no está fijado.** Recomendación: F0 hace que `lint` compruebe
  `golangci-lint version` contra `LINT_VERSION` y **falle** si no coincide (o que use un binario
  instalado en `bin/` con esa versión). Es un cambio del `Makefile`, que comparten los dos arranques;
  no toca código viejo. Hasta entonces, todo informe de gate dice la versión usada.
- **T-2 · testcontainers sube `httpsnoop` y `otelhttp` en producción.** Recomendación: aceptarlo en
  un commit **aislado** (`chore(deps): …`) en F9, corrido por `make test-integration` y `ci-local`
  antes, y dicho en el traspaso; la alternativa (un `go.mod` aparte para `test/procesos/`) añade un
  segundo módulo y contradice «un solo paquete, un contenedor» solo en apariencia, pero complica
  `GOWORK=off`.

## 9 · Cifras de referencia que citan varias fases (medidas el 2026-09-29, `dev` @ `7021144`)

Las fases las heredaron de `03`/`05` o las midieron cada una con otra regla, y discrepaban. Estas
son **las buenas**, con su comando; si una fase dice otra cifra, manda esta (y se corrige la fase).

| Qué | Cifra | Regla de conteo y comando |
|---|---:|---|
| Variables de entorno | **71** | ver [`producto.md`](producto.md) §4: 69 `loader.Get*` + `FLOW_REPLY_RATE` (`getFloat`) + `WAPP_CONFIG_FILE` (`os.Getenv`) |
| Tests de cableado del arranque viejo | **9** | ficheros con `cablead` en el nombre: `ls internal/bootstrap/arranque/*cablead*_test.go \| wc -l` → 9 = **8** `*_cableado_test.go` + `flow_options_cableadas_test.go`. «8» (constitución, `deuda.md` D-9) cuenta solo el sufijo exacto; «11» (`05` §3.2) es otra cosa: los ficheros de test del arranque que **leen AST** (9 + `platform_permissions_test.go` + `astpaquete_test.go`) |
| Paquetes de dominio con SQL | **22** | `grep -rlE '"database/sql"\|"github.com/jackc/pgx' --include='*.go' internal \| grep -v '_test\.go' \| xargs -n1 dirname \| sort -u \| grep -vE '^internal/(platform/\|bootstrap\|publicapi)' \| wc -l` → 22. Fuera: `platform/**` (se queda), el arranque y la cara vieja (`publicapi/eventstelemetry_store.go`, que se queda en la cara por D-FX-4). `05` E-6 dice 20: le faltan `flujos/events` y `flujos/runtime` |
| … de ellos, **con** gemelo en memoria | **15** | por paquete, `grep -lE '^func (\([a-z]+ \*?[A-Za-z]+\) )?(NewMemory\|NuevaMemoria\|NewFake)\|^type (Memory\|Memoria\|Fake)[A-Za-z]* struct' <paquete>/*.go \| grep -v _test`, más `iam/infra/memory` para `iam/infra/postgres`: `diagnostics`, `entitlements` (`Fake`), `flujos/contact`, `flujos/store`, `flujos/trigger`, `gateway/enroll`, `gateway/fleet`, `gateway/lease`, `iam/infra/postgres`, `ingest`, `intake`, `intakes`, `intentcfg`, `receipts`, `tenantvars` |
| … **sin** gemelo en memoria | **7** | `casebank`, `degradation`, `flujos/events`, `flujos/runtime` (`self_numbers.go`, `tenant_resolver.go`), `integrations`, `platformadmin`, `tenantllm`. Necesitan **doble nuevo** en su `<paquete>helpertest` (E-6, D-F1-10). `05` E-6 dice «12 sin gemelo» (contaba por nombre de fichero `memory*`); las fases dijeron 11 (F7) o 6+2 (F9, que además daba `entitlements` sin gemelo) |
