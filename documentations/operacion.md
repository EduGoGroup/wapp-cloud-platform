# Operación de `wapp-cloud-platform`

Cómo se arranca, cómo se prueba, cómo se publica y cómo se depura.

---

## 1 · 🔴 El aviso que hay que leer antes de nada

### 1.1 · Un PR aquí NO valida nada

`.github/workflows/ci.yml` es **`on: workflow_dispatch`** — decisión del dueño del 2026-08-01,
escrita en la cabecera del propio workflow. No se dispara con push ni con pull request. El
único workflow automático es `sync-main-to-dev.yml`, y **no valida**: solo alinea ramas.

⇒ **El gate real es local: `make ci-local` y `make test-procesos` en tu máquina, antes de
integrar.**

Lanzado a mano, el workflow tiene tres jobs:

| Job | Qué hace |
|---|---|
| `test` | `gofmt -l .` vacío, `go mod download`, `go mod tidy` sin diff en `go.mod`/`go.sum`, `go vet ./...`, `go test -race ./...` y `go build ./...` |
| `integration` | con `GOWORK=off` en el entorno, `go mod download` y **`make test-procesos`**: los procesos de negocio contra un Postgres que levanta el propio `go test` con testcontainers (el runner trae Docker; no hay *service container*). Un último paso, que corre siempre, imprime la cola de `/tmp/procesos.log` para que un rojo diga qué proceso falló |
| `lint` | `golangci-lint` v2.12.2 con `--timeout=5m`, por la acción oficial |

### 1.2 · Un `rc=0` no significa que todo pasó: hay que CONTAR LOS SKIP

`go test` devuelve `0` con un `--- SKIP` igual que con un `--- PASS`. Por eso los dos targets
que prueban contra Postgres (`make test-procesos` y `make test-procesos-edge-real`) **no se
fían del `rc`**: escriben la salida entera en un log, cuentan `--- PASS`, `--- FAIL` y
`--- SKIP` con `grep -c` y **salen en rojo si hay un solo SKIP**. Un proceso que no puede
correr falla, no se salta: sin Docker, el arnés termina con error.

Para una corrida suelta de `go test`, la misma disciplina a mano:

```bash
GOTOOLCHAIN=go1.26.5 GOWORK=off go test ./... > /tmp/out.txt 2>&1; echo "rc=$?"
grep -c -- '--- SKIP' /tmp/out.txt     # ← este número tiene que ser el que esperabas
grep -c -- '--- FAIL' /tmp/out.txt
```

⚠️ **Y lee el `rc` sin tubería.** Un `go test ./... | tee` te devuelve el `rc` de `tee`, no el
de `go test`. Redirige a un fichero, como arriba, o usa `PIPESTATUS`.

🔴 **Ningún test apunta a un Postgres vivo.** La única base válida es la del contenedor que
levanta la corrida. Lo vigila `TestSinBDViva` (`test/procesos/sin_bd_viva_test.go`), que va sin
etiqueta de build y por tanto corre dentro de `make test` y de `make ci-local`: recorre todos
los `.go` de `test/procesos/` y falla si alguno abre una conexión fuera de
`test/procesos/base_test.go` o nombra una base ajena a la corrida.

---

## 2 · Arranque local

### 2.1 · Requisitos

Go **1.26.5** (el que fija `go.mod:3` y la variable `GO_VERSION` del `Makefile`), Docker (para
`make test-procesos`, `make ci-docker` y para levantar un Postgres local), `openssl` y un
almacén S3-compatible vivo.

El `go` del `PATH` **no** tiene que ser el 1.26.5. El `Makefile` exporta
`GOTOOLCHAIN=go$(GO_VERSION)` (`Makefile:38`) y cualquier `go` ≥ 1.21 ejecuta esa toolchain, que
Go descarga una vez a su caché de módulos. **El primer paso en una máquina nueva (o en un clon o
`git worktree` nuevo) es:**

```bash
make tools                      # golangci-lint v2.12.2 en .bin/ (ignorado por git); idempotente
make toolchain; echo "rc=$?"    # TOOLCHAIN=OK y rc=0: lo que corre bajo make es lo fijado
```

🔴 **Un `go` suelto, fuera de `make`, sigue siendo el del sistema.** Para el Go fijado, antepón
`GOTOOLCHAIN=go1.26.5 GOWORK=off` o usa el target de `make`. Un gate corrido con otro Go no es
autoritativo.

### 2.2 · Los cuatro pasos

```bash
# 1 · PKI de desarrollo (CA + cert de servidor, SAN localhost/127.0.0.1)
./scripts/gen-dev-certs.sh          # escribe en certs/, que está fuera de git

# 2 · Un Postgres (cualquiera; aquí uno efímero)
docker run -d --name wapp-pg -e POSTGRES_USER=wapp -e POSTGRES_PASSWORD=wapp \
  -e POSTGRES_DB=wapp_cloud -p 5432:5432 postgres:17-alpine

# 3 · Variables. NO hay carga de .env en el proceso: se exportan.
cp .env.example .env      # y lo rellenas; luego:  set -a; . ./.env; set +a

# 4 · Migrar y arrancar
make migrate              # aplica el DDL y SALE
GOTOOLCHAIN=go1.26.5 GOWORK=off go run ./cmd/server
```

Hay **un único binario de servicio**, `cmd/server`. `cmd/migrate` aplica el esquema y sale, sin
abrir un solo listener; su conexión sale de las mismas variables `WAPP_DB_*` que el servidor,
nunca de un argumento.

⚠️ **El proceso no lee ningún `.env` por sí mismo** (no hay cargador de `.env` en `go.mod`). En
un despliegue lo inyecta systemd con `EnvironmentFile=`; en local lo exportas tú. Confundir esto
es la causa número uno de «pero si la variable está puesta».

⚠️ El `.env.example` **no es el inventario** de variables: declara menos de las que lee
`config.Load()`. La lista buena está en [`contratos.md`](contratos.md) §5.

### 2.3 · Las llaves de dev, y cuál te muerde si la dejas vacía

| Llave | Cómo se genera en dev | Si falta |
|---|---|---|
| PKI (`WAPP_PKI_*`) | `./scripts/gen-dev-certs.sh` | sin los ficheros, la primera fase del arranque falla al cargarlos (`internal/arranque/pki.go:33`) y el proceso no levanta |
| JWT ES256 (`WAPP_JWT_EC_PRIVATE_KEY_FILE`) | `openssl ecparam -name prime256v1 -genkey -noout \| openssl pkcs8 -topk8 -nocrypt -out jwt-es256.pem && chmod 600 jwt-es256.pem` | en dev se genera una efímera con aviso (`internal/arranque/auth_jwt.go:92`): los tokens no sobreviven al reinicio. En prod, el arranque falla |
| KEK maestra (`WAPP_KEK_MASTER_B64`) e índice (`WAPP_KEK_INDEX_B64`) | `openssl rand -base64 32` cada una | no se construye el proveedor de llaves de la PII de negocio y el arranque falla (`internal/arranque/flows.go:52`) |
| **Lease Ed25519** (`WAPP_LEASE_PRIVATE_KEY_FILE` o `WAPP_LEASE_PRIVATE_KEY_B64`) | PEM PKCS8 Ed25519, o semilla de 32 B en base64 | 🔴 en dev la plataforma genera una clave **EFÍMERA en cada arranque** (`internal/arranque/lease.go:50`) **e invalida a TODOS los Edges al reiniciar**. En prod es obligatoria: sin ella el arranque falla (`internal/arranque/lease.go:27`) |
| `WAPP_CLOUD_ENC_PRIVKEY_B64` | X25519, la privada con la que se abren los sobres del Edge | se genera un par **efímero** con aviso (`internal/arranque/pki.go:121`): un Edge enrolado antes del reinicio sella con una pública que ya no abre nadie |

⚠️ **`certs/`, `.env` y los binarios están en `.gitignore`.** Los valores de `.env.example` son
marcadores de posición, no credenciales.

### 2.4 · Comprobar que arrancó

```bash
curl -s localhost:8100/healthz     # incluye el check de Postgres
curl -s localhost:8100/metrics | grep '^wapp_'
```

Las dos rutas se montan en el listener admin (`internal/arranque/rutas_admin.go:69`), y el check
de Postgres se registra en `internal/arranque/fase8_transporte.go:132`.

⚠️ El listener público `:8103` **no tiene sonda de salud**: `curl localhost:8103/healthz` da
404 y eso es lo esperado, no un fallo.

⚠️ **El arranque depende de S3/R2 vivo**: `objectstore.NewR2PresignClient` se construye en el
arranque (`internal/arranque/flows.go:75`) y valida el bucket con `HeadBucket`
(`internal/platform/storage/objectstore/r2_factory.go:64`); si falla, **el proceso no levanta**.
En local sirve cualquier almacén S3-compatible con `WAPP_STORAGE_S3_ENDPOINT` apuntándole.

---

## 3 · Cómo se prueba: los `make` reales y qué valida cada uno

Son **16 targets** (los de la línea `.PHONY` del `Makefile`), y no hay ninguno más.

| Target | Qué corre | Qué demuestra |
|---|---|---|
| `make tools` | descarga el binario **oficial** de `golangci-lint` v2.12.2 a `.bin/` (ignorado por git), comparando su sha256 con el fichero de checksums de la release **antes** de extraer; si `.bin/` ya tiene esa versión, no descarga nada. No ejecuta ningún script remoto | nada: prepara. Una vez por *checkout* |
| `make toolchain` | imprime `GO_PINNED`, `GO_SYSTEM`, `GO_EFFECTIVE`, `GOFMT_EFFECTIVE`, `LINT_PINNED`, `LINT_EFFECTIVE` y `TOOLCHAIN=OK` o `TOOLCHAIN=NOT_READY`; `rc≠0` si lo efectivo no es lo fijado | que lo que va a correr `make` es Go 1.26.5 y `golangci-lint` v2.12.2. **Paso 0 de cualquier gate** |
| `make fmt-check` | `gofmt -l .` tiene que salir vacío, con el `gofmt` del `GOROOT` de Go 1.26.5 (el del `PATH` no obedece a `GOTOOLCHAIN`) | formato |
| `make vet` | `GOWORK=off go vet ./...` | errores estáticos |
| `make vet-integracion` | `GOWORK=off go vet ./test/procesos/...` dos veces: con `-tags integracion` y con `-tags integracion,edgereal` | que los procesos, y los casos con Edge real, compilan. Sin Docker |
| `make lint` | `golangci-lint` **v2.12.2 fijado**: el de `.bin/` si existe, ejecuta y es esa versión; si no, el del `PATH`. Falla si ninguno lo es y manda a `make tools`. Su caché es **por *checkout*** (`.bin/lint-cache`, variable `LINT_CACHE_DIR`), no la del usuario: compartida entre *worktrees* devuelve resultados con rutas de otro *checkout*. La primera pasada de cada *checkout* va en frío | los **16** linters de `.golangci.yml` (`linters.enable`) más sus formateadores; con las etiquetas `integracion` y `edgereal`, así que también ve `test/procesos/` |
| `make test` | `GOWORK=off go test -race ./...` | los unitarios, con detector de carreras y **sin base de datos**. Los procesos quedan fuera: llevan etiqueta de build |
| `make cobertura-ficheros` | corre los tests de `internal/modulos`, `internal/nucleo`, `internal/apipublica`, `internal/candados` e `internal/arranque/huellatest` con perfil de cobertura y se lo pasa a `cmd/cobertura-ficheros`: imprime la tabla por fichero, `FICHEROS_EVALUADOS` y `POR_DEBAJO` (referencia 80 %) | un **informe**: **no bloquea** por un fichero bajo; sí por un test que falla o no compila |
| `make cobertura-combinada` | el mismo **informe**, sumando lo que ejecutan los procesos contra el servidor compilado con `go build -cover` y las suites que corren en el propio proceso de test; deja los perfiles en `COBERTURA_COMBINADA_DIR` (por defecto, `cobertura-combinada` bajo `PROCESOS_LOG_DIR`). **Necesita Docker.** No está en `ci-local` | qué huecos de los unitarios **no** cubre ningún flujo; `rc=0` aunque haya ficheros por debajo |
| `make build` | `GOWORK=off go build ./...` | compila |
| **`make ci-local`** | `fmt-check vet vet-integracion lint test cobertura-ficheros build`, en ese orden, **sin Postgres** | el gate de pre-push. Le falta `test-procesos`, que se corre aparte |
| **`make test-procesos`** | `go test -tags integracion -v -timeout 30m -parallel 4 ./test/procesos/...` contra `cmd/server`. Rojo si `rc≠0` **o** hay algún `--- SKIP` | el comportamiento de caja negra, por proceso de negocio. **La única prueba del repo contra Postgres** |
| `make test-procesos-edge-real` | los casos `TestEdgeReal_…` (`-tags integracion,edgereal -run '^TestEdgeReal_'`): el servidor contra el binario **real** `wapp-edge-agent` con WhatsApp falso, en un contenedor Linux. Rojo si `rc≠0`, hay algún `--- SKIP` **o no corrió ningún caso** (`PASS=0`) | la conversación extremo a extremo con el Edge de verdad. No forma parte de `make test-procesos` |
| `make ci-docker` | `make tools TOOLS_DIR=/usr/local/bin` y luego `make ci-local LINT_CACHE_DIR=/tmp/lint-cache`, dentro de `golang:1.26.5-bookworm` | que el gate no depende de tu máquina (ver abajo) |
| `make migrate` / `make migrate-status` | `GOWORK=off go run ./cmd/migrate` [`-status`] | aplica / consulta el esquema. `-status` no escribe nada |

### 3.1 · En qué difieren `ci-local` y `ci-docker`

Corren **los mismos siete pasos**; lo que cambia es dónde:

| | `make ci-local` | `make ci-docker` |
|---|---|---|
| Sistema | tu máquina | un contenedor Linux (`golang:1.26.5-bookworm`) |
| Go | el `go` del `PATH`, redirigido por `GOTOOLCHAIN` a la 1.26.5 | el de la imagen, que ya es el 1.26.5 |
| `golangci-lint` | el de `.bin/` (o el del `PATH`) | uno recién instalado en `/usr/local/bin` por `make tools` dentro del contenedor; el `.bin/` del host viaja montado pero es de otro sistema y `lint` lo descarta solo |
| Caché del linter | `.bin/lint-cache` del *checkout* | `/tmp/lint-cache` del contenedor: siempre en frío |
| Módulos | tu caché | la misma, montada desde `go env GOMODCACHE` |
| Extra | — | `GOFLAGS=-buildvcs=false` |

Un verde de `ci-local` dice «pasa con lo que tengo instalado»; un verde de `ci-docker` dice
«pasa en un Linux limpio con la toolchain exacta». **Ninguno de los dos corre
`make test-procesos`** (dentro del contenedor haría falta Docker-in-Docker): se lanza aparte, en
el host.

### 3.2 · Los procesos: la prueba contra Postgres

Viven en `test/procesos/`, tras la etiqueta de build `integracion`. Cada corrida levanta **un**
contenedor `postgres:17-alpine` con testcontainers (`test/procesos/main_test.go:32`) y clona una
base por proceso; el arnés compila `cmd/server` y lo prueba desde fuera.

- **Exige Docker.** Sin él la corrida falla (`test/procesos/main_test.go:91`); no se salta.
- **Exige `GOWORK=off` en el entorno**: si no, el arnés compilaría el servidor contra los módulos
  vecinos del `go.work`. El `TestMain` lo mira al entrar y sale con código 2
  (`test/procesos/main_test.go:83`). El `Makefile` ya lo antepone a cada `go`.
- **`CUENTA=n`** repite la suite (`-count`; por defecto 1): `make test-procesos CUENTA=3`.
- **`PROCESOS_LOG_DIR`** dice dónde queda el log (por defecto `/tmp`): `procesos.log`, o
  `procesos-edge-real.log`. Su última línea es `RC=<n>`, y el target imprime una línea de
  resumen con `RC`, `PASS`, `FAIL` y `SKIP`.
- **La etiqueta es obligatoria**: sin `-tags integracion` el paquete solo tiene el candado y
  `go test` da `ok` sin ejecutar un solo proceso.

`make test-procesos-edge-real` necesita además **el repo del Edge en disco**: la variable
`WAPP_PROCESOS_EDGE_DIR` (por defecto, el repo hermano `edge/wapp-edge-agent` del ecosistema). El
arnés compila el agente para Linux con su revisión de git dentro, y lo primero que afirma cada
caso es que el binario que corre dice esa revisión. Si el directorio no existe o no es el Edge,
el caso falla.

Medido el 2026-10-11 con `grep -rh '^func Test' test/procesos | wc -l`: **128** funciones `Test`
de primer nivel (regla: declaraciones, no subtests), de las que **3** son `TestEdgeReal_…`.

### 3.3 · Por qué así

**Por qué todos corren con Go 1.26.5 aunque el `go` del `PATH` sea otro**: el `Makefile` exporta
`GOTOOLCHAIN=go$(GO_VERSION)` a todo subproceso (los `$(MAKE)` anidados, el `go build` del arnés
de `test/procesos/`, el `go` que lanza `golangci-lint`). Sin `+auto`: si `go.mod` pidiera un Go
más nuevo que `GO_VERSION`, `go` falla y lo dice, en vez de subir de versión por su cuenta.
Subir de Go es cambiar `GO_VERSION` y `go.mod` a la vez; subir de linter, `LINT_VERSION`.
`TOOLS_DIR`, `LINT_BIN`, `LINT_CACHE_DIR` y `LINT_RELEASE_URL` se cambian **solo** en la línea
de comandos (`make lint LINT_BIN=<ruta>`).

**Por qué `GOWORK=off`**: el repo vive dentro de un `go.work` del ecosistema. Con el workspace
activo compilas contra el árbol de al lado, no contra la versión **publicada** de
`wapp-shared`. El gate tiene que ver lo mismo que verá el consumidor.

### Estado medido el 2026-10-11 en la máquina de desarrollo

```
make toolchain   → rc=0 · GO_SYSTEM=go1.27.1 · GO_EFFECTIVE=go1.26.5 · LINT_EFFECTIVE=v2.12.2 · TOOLCHAIN=OK
```

Es lo único que se midió al escribir este documento. El resultado de `make ci-local`,
`make ci-docker` y `make test-procesos` se mira corriéndolos, no aquí.

---

## 4 · Cómo se publica una versión

**Este repo NO tiene `release.yml`**: los tags se cortan a mano. Hoy hay **dos**: `v0.1.0`
(2026-08-13) y `v0.2.0` (2026-08-28).

La cadencia del ecosistema, que aplica aquí:

1. El trabajo nace en una rama partida de **`dev`** y llega a `dev` por PR.
2. **A `main` se pasa al FINAL del plan**, no ola a ola. Un push a `main` dispara
   `sync-main-to-dev.yml`, que alinea `dev`.
3. El tag se corta sobre `main`, a mano, después de un `make ci-local` verde **y** un
   `make test-procesos` verde.

⚠️ **Dependencia de `wapp-shared`**: si tu cambio necesita un símbolo nuevo de un módulo de
`wapp-shared`, **primero** se corta el release de ese módulo (tag `<modulo>/vX.Y.Z`, y su
`CHANGELOG` va con `## [0.1.0]` **sin la «v»**) y **luego** se sube aquí en `go.mod`. Verifica
tu cambio contra el **módulo publicado**, no contra el árbol de al lado: por eso el gate corre
con `GOWORK=off`.

🔴 **El `CHANGELOG.md` de este repo va por detrás.** Su `[Unreleased]` está **vacío** y desde
`v0.2.0` hay 1.064 commits que tocan 2.580 ficheros (medido el 2026-10-11 con
`git log --oneline v0.2.0..HEAD | wc -l` y `git diff --shortstat v0.2.0..HEAD`). Si publicas,
rellénalo tú.

---

## 5 · Cómo se depura cuando falla

### 5.1 · Lo primero: ¿qué binario está corriendo?

**La revisión del fichero instalado NO es la del proceso vivo**: instalar y reiniciar son dos
pasos, y se olvidan por separado. Pregunta por el proceso, no por el fichero:

```bash
readlink -f /proc/$(systemctl show -p MainPID --value wapp-cloud)/exe
sha256sum <esa ruta>          # y compáralo con el binario que crees haber desplegado
```

### 5.2 · El log

En UAT no pasa por `journald`: la unidad escribe a un fichero propio, `cloud.log`, que no rota.
Con `WAPP_LOG_LEVEL=debug` y `WAPP_LOG_JSON=false` crece rápido. **Antes de pedir una prueba de
campo nueva, mira si el log ya la tiene.**

### 5.3 · El árbol de decisión de los fallos frecuentes

| Síntoma | Mira esto primero |
|---|---|
| **El proceso no arranca y el error habla de prompts** | Un directorio de `WAPP_LLM_PROMPTS_DIR` inválido **aborta a propósito**. El error dice el fichero y la etapa. Valida con `GOTOOLCHAIN=go1.26.5 GOWORK=off go run ./cmd/prompts -comprobar <dir>` |
| **El proceso no arranca y el error habla del bucket** | `HeadBucket` falló: el almacén S3/R2 está caído o las credenciales son malas (`internal/arranque/flows.go:75`) |
| **El proceso no arranca y el error habla del `KeyProvider`** | Falta el material de KEK de la PII de negocio, o `WAPP_KEK_PROVIDER` pide un KMS que no responde (`internal/arranque/flows.go:52`) |
| **Un ajuste de prompt no tiene efecto** | ¿Reiniciaste? No hay recarga en caliente. Y mira la línea de log del arranque `prompts: plantillas de las etapas ajustables` (`internal/arranque/prompts.go:38`): por cada etapa dice la ruta del fichero o `compilada`. Con `WAPP_LLM_PROMPTS_DIR` vacía corren las compiladas |
| **Una ruta responde 404 y jurarías que existe** | Es **condicional**: su dependencia llegó `nil` a la cara HTTP y no se montó (el patrón, en `internal/apipublica/roleplane.go:112`). Mira qué se le pasa a `buildPublicAPIServer` en `internal/arranque/fase8_transporte.go:98` |
| **`POST /api/v1/members` responde 503** | Es lo diseñado: se monta siempre y degrada a 503 `identity_no_configurado` sin credencial M2M. Nunca da 404 (`internal/apipublica/roleplane.go:90`) |
| **`/api/v1/signup` responde 503** | Falta `WAPP_IDENTITY_API_KEY`: se cablea un 503 fijo y el arranque lo avisa en el log (`internal/apipublica/auth.go:159`) |
| **`quote-suggestion` corta antes de responder** | El `WriteTimeout` del servidor público (`internal/arranque/fase8_transporte.go:196`) no vale para esa ruta: lleva un plazo propio, **derivado** del presupuesto de inferencia y no copiado (`internal/apipublica/writedeadline.go:100`, montado en `internal/apipublica/intakereports.go:227`). Si alguien tocó uno sin el otro, se rompe |
| **La migración se queda colgada** | El `pg_advisory_lock` con que se serializa el runner (`internal/platform/storage/postgres/migrations/migrate.go:61`), repartido por un pooler en modo transacción. Apunta al **host directo** |
| **Una métrica nueva no aparece en `/metrics`** | Un `CounterVec` no emite hasta su primer incremento, y el reinicio lo borra. No es un fallo |
| **Los jobs se quedan en `pending` para siempre, sin un error** | Alguna de las cinco goroutines de fondo murió, o nunca se lanzó. **El fallo es MUDO**: ver abajo |

**Las cinco goroutines de fondo.** Las lanza la novena y última fase del arranque,
`internal/arranque/fase9_fondo.go`, con una sentencia `go` cada una y sobre el mismo `ctx` que
cierra el proceso:

| # | Qué corre | Línea | Si falta o muere |
|---|---|---|---|
| 1 | el worker del puente CRM, que entrega `webhook_outbox` | `:61` | los webhooks no salen |
| 2 | el colector de telemetría de `flow_events` | `:70` | las métricas de ciclo de vida de los flujos se quedan quietas |
| 3 | el barrido de ventanas de captación (`c.intakeAggregator.Run`) | `:82` | las ventanas se abren y nunca cierran: jobs en `aggregating` para siempre |
| 4 | el pool que pide las clasificaciones por adelantado (`c.intakeAhead.Run`) | `:91` | no hay adelanto nunca; la cola se llena y se descarta |
| 5 | el worker del pipeline P2→P4 (`c.intakePipeline.Run`), **uno solo** | `:106` | los jobs quedan `pending` y nadie los reclama: presupuestos que no llegan |

🔴 **No hay supervisor ni reinicio**: si una muere, el proceso sigue vivo, los cuatro listeners
responden y no hay una línea de error. El síntoma es trabajo que se acumula. Está anotado como
deuda en [`deuda.md`](deuda.md), y el detalle de cada una en
[`arquitectura.md`](arquitectura.md) §5.

### 5.4 · Diagnóstico de un Edge concreto

`POST /api/v1/sessions/{id}/diagnostics` pide un *bundle* al Edge y
`GET /api/v1/diagnostics/{command_id}` lo recoge (`internal/apipublica/diagnostics.go:159`). Las
dos exigen el permiso `diagnostics.request` y quedan auditadas. El *bundle* tiene **TTL**
(`WAPP_DIAGNOSTICS_BUNDLE_TTL`, 30 min por defecto) y exige consentimiento del tenant
(`tenant_diagnostics_consent`).

### 5.5 · El estado del esquema, sin escribir nada

```bash
make migrate-status
```

Imprime dos líneas: `estado registrado` (versión, hash y si está al día, leídos de la base) y
`estado embebido` (lo que trae el binario). La versión embebida es la constante `SchemaVersion`
de `internal/platform/storage/postgres/migrations/version.go`; qué contiene ese esquema lo
cuenta [`esquema-postgres.md`](esquema-postgres.md).

⚠️ **`-status` consulta la base que digan las variables `WAPP_DB_*` del entorno donde lo
lanzas.** Sin ellas usa los valores por defecto (`localhost`, base `wapp_cloud`), y contesta
sobre esa base sin avisar de que no es la que querías.

---

## 6 · Estado del ambiente UAT

El despliegue en UAT **no se documenta aquí**: su fuente es la documentación del ecosistema, en
la raíz de wApp (fuera de este repo), eje de operación, documento «despliegue-uat». Allí están
las unidades, los ficheros de entorno, las sondas y el procedimiento.

De esta pieza, lo que conviene tener presente al entrar en el VPS:

- La unidad systemd es `wapp-cloud.service` y el binario, `bin/server` del *checkout*.
- 🔴 **`cmd/migrate -status` necesita cargado el fichero de entorno de la unidad.** Lanzado en
  una shell limpia no ve las `WAPP_DB_*` de UAT y consulta la base por defecto, que no es la de
  UAT: la respuesta parece buena y habla de otra base (ver §5.5).
- El listener público `:8103` no tiene sonda de salud; la sonda es `/healthz` en el admin
  `:8100`.
