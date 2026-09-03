# Operación de `wapp-cloud-platform`

Cómo se arranca, cómo se prueba, cómo se publica y cómo se depura.

---

## 1 · 🔴 El aviso que hay que leer antes de nada

### 1.1 · Un PR aquí NO valida nada

`.github/workflows/ci.yml` es **`on: workflow_dispatch`** — decisión del dueño del 2026-08-01,
escrita en la cabecera del propio workflow. No se dispara con push ni con pull request. El
único workflow automático es `sync-main-to-dev.yml`, y **no valida**: solo alinea ramas.

⇒ **El gate real es local: `make ci-local` en tu máquina, antes de mergear y pushear.**

### 1.2 · Un `rc=0` no significa que todo pasó: hay que CONTAR LOS SKIP

`go test` devuelve `0` con un `--- SKIP` igual que con un `--- PASS`. En este repo eso importa
mucho: hay **97 ficheros `*_integration_test.go`**, **91 de los cuales leen `WAPP_TEST_DB_DSN`**,
y esos ficheros contienen **352 funciones `Test*`** — el **11,3 % de la suite** (3.116 `Test*`
en total) **se salta solo** si no hay Postgres. La regla del recuento: funciones, no ficheros —
`grep -rh '^func Test' --include='*_integration_test.go' . | wc -l` sobre
`grep -rh '^func Test' --include='*_test.go' . | wc -l`.

```bash
# la forma correcta de mirar el resultado
GOWORK=off go test ./... 2>&1 | tee /tmp/out.txt
grep -c -- '--- SKIP' /tmp/out.txt     # ← este número tiene que ser el que esperabas
grep -c -- '--- FAIL' /tmp/out.txt
```

⚠️ **Y lee el `rc` sin tubería.** Un `go test ./... | tee` te devuelve el `rc` de `tee`, no el
de `go test`. Usa `PIPESTATUS` o mira el fichero.

La red contra el falso verde ya está puesta y conviene usarla: con **`WAPP_TEST_REQUIRE_DB=1`**
los helpers `openTestDB` hacen `t.Fatal` en vez de saltar
(`internal/receipts/integration_test.go:24`). Un servicio caído deja el job **rojo**, no verde
con todo saltado.

---

## 2 · Arranque local

### 2.1 · Requisitos

Go **1.26.5** (el toolchain que fija `go.mod:3` y el `Makefile`), Docker (solo para los tests
de integración y para levantar un Postgres), `openssl` y un almacén S3-compatible vivo.

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
go run ./cmd/server
```

⚠️ **El proceso no lee ningún `.env` por sí mismo** (cero `godotenv` en el árbol). En UAT lo
inyecta systemd con `EnvironmentFile=`; en local lo exportas tú. Confundir esto es la causa
número uno de «pero si la variable está puesta».

⚠️ El `.env.example` declara **47** claves y `config.Load()` lee **70**. No lo tomes como
inventario: la lista buena está en [`contratos.md`](contratos.md).

### 2.3 · Las llaves de dev, y cuál te muerde si la dejas vacía

| Llave | Cómo se genera en dev | Si la dejas vacía |
|---|---|---|
| PKI (`WAPP_PKI_*`) | `./scripts/gen-dev-certs.sh` | el listener mTLS `:8101` no levanta |
| JWT ES256 (`WAPP_JWT_EC_PRIVATE_KEY_FILE`) | `openssl ecparam -name prime256v1 -genkey -noout \| openssl pkcs8 -topk8 -nocrypt -out jwt-es256.pem && chmod 600 jwt-es256.pem` | en dev se genera una efímera; los tokens no sobreviven al reinicio |
| KEK maestra (`WAPP_KEK_MASTER_B64`) e índice (`WAPP_KEK_INDEX_B64`) | `openssl rand -base64 32` cada una | no se puede cifrar/descifrar PII de negocio |
| **Lease Ed25519** (`WAPP_LEASE_PRIVATE_KEY_FILE` o `…_B64`) | PEM PKCS8 Ed25519, o semilla de 32 B en base64 | 🔴 **la plataforma genera una clave EFÍMERA en cada arranque e invalida a TODOS los Edges al reiniciar** |
| `WAPP_CLOUD_ENC_PRIVKEY_B64` | X25519, la privada con la que se abren los sobres del Edge | no se pueden abrir los `enc_payload` sellados |

⚠️ **`certs/`, `.env` y los binarios están en `.gitignore`.** Verificado: `git ls-files` no
tiene un solo secreto; los valores de `.env.example` son placeholders.

### 2.4 · Comprobar que arrancó

```bash
curl -s localhost:8100/healthz     # incluye el check de Postgres
curl -s localhost:8100/metrics | grep '^wapp_'
```

⚠️ El listener público `:8103` **no tiene sonda de salud**: `curl localhost:8103/healthz` da
404 y eso es lo esperado hoy, no un fallo.

⚠️ **El arranque depende de S3/R2 vivo**: `NewR2PresignClient` valida el bucket con
`HeadBucket` y si falla **el proceso no levanta** (`internal/publicapi/flows.go:75`). En local
sirve un MinIO (`docker run … minio/minio`) con `WAPP_STORAGE_S3_ENDPOINT` apuntándole.

---

## 3 · Cómo se prueba: los `make` reales y qué valida cada uno

| Target | Qué corre | Qué demuestra |
|---|---|---|
| `make fmt-check` | `gofmt -l .` tiene que salir vacío | formato |
| `make vet` | `GOWORK=off go vet ./...` | errores estáticos del compilador extendido |
| `make lint` | `golangci-lint` **v2.12.2 fijado** (no el de `~/go/bin`) | los **16** linters de `.golangci.yml` (`linters.enable`) más sus 2 formateadores (`formatters.enable`) |
| `make test` | `GOWORK=off go test -race ./...` | unitarios con detector de carreras. **Los `*_integration_test.go` se saltan solos sin `WAPP_TEST_DB_DSN`** |
| `make build` | `GOWORK=off go build ./...` | compila |
| **`make ci-local`** | los cinco de arriba, en ese orden, **sin integración** | el gate de pre-push |
| `make test-integration` | levanta un `postgres:16` efímero, corre `go test -p 1 ./...` con `WAPP_TEST_DB_DSN` y `WAPP_TEST_REQUIRE_DB=1`, y lo destruye | el 14 % de la suite que solo corre con base |
| `make ci-docker` | repite `ci-local` dentro de `golang:1.26.5-bookworm` | que no dependes de tu toolchain local |
| `make migrate` / `make migrate-status` | `go run ./cmd/migrate` [`-status`] | aplica / consulta el esquema |

**Por qué `GOWORK=off`**: el repo vive dentro de un `go.work` del ecosistema. Con el workspace
activo compilas contra el árbol de al lado, no contra la versión **publicada** de
`wapp-shared`. El gate tiene que ver lo mismo que verá el consumidor.

**Por qué `-p 1` en integración**: los tests de BD se serializan con un advisory lock. Sin
`-p 1` se pisan.

⚠️ **`make test-integration` usa un nombre de contenedor FIJO** (`wapp-cloud-platform-pg-test`).
Dos corridas a la vez se matan la base la una a la otra y verás cientos de
`connection refused` que no son de nadie. Si el 5432 del host está ocupado:

```bash
INTEGRATION_PG_PORT=55441 make test-integration
```

### Estado medido el 2026-08-30 en esta máquina

```
GOWORK=off go build ./...   → rc=0
GOWORK=off go vet ./...     → rc=0
gofmt -l .                  → 0 ficheros
GOWORK=off go test ./...    → rc=0 · 71 paquetes ok · 5 sin tests
```
**NO VERIFICADO**: `make lint` (no comprobé que exista `golangci-lint v2.12.2` en la máquina) y
`make test-integration` (no levanté Docker).

La suite entera son **3.116** funciones `Test*`/`Benchmark*`/`Fuzz*` en **528** ficheros.

---

## 4 · Cómo se publica una versión

**Este repo NO tiene `release.yml`**: los tags se cortan a mano. Hoy hay **dos**: `v0.1.0`
(2026-08-13) y `v0.2.0` (2026-08-28).

La cadencia del ecosistema, que aplica aquí:

1. El trabajo aterriza en **`dev`**.
2. **A `main` se pasa al FINAL del plan**, no ola a ola.
3. El tag se corta sobre `main`, a mano, después de un `make ci-local` verde **y** un
   `make test-integration` verde.

⚠️ **Dependencia de `wapp-shared`**: si tu cambio necesita un símbolo nuevo de un módulo de
`wapp-shared`, **primero** se corta el release de ese módulo (tag `<modulo>/vX.Y.Z`, y su
`CHANGELOG` va con `## [0.1.0]` **sin la «v»**) y **luego** se sube aquí en `go.mod`. Verifica
tu puerto contra el **módulo publicado**, no contra el árbol de al lado: por eso el gate corre
con `GOWORK=off`.

🔴 **El `CHANGELOG.md` de este repo va por detrás.** Su `[Unreleased]` estaba **vacío** con 76
ficheros y +9.575/−271 líneas de cambios sobre el último tag. Si publicas, rellénalo tú.

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

En UAT no pasa por `journald`: la unidad usa `StandardOutput=append:…/cloud.log`. Con
`WAPP_LOG_LEVEL=debug` y `WAPP_LOG_JSON=false` ese fichero crece rápido (14 MB observados).
**Antes de pedir una prueba de campo nueva, mira si el log ya la tiene**: guarda semanas.

### 5.3 · El árbol de decisión de los fallos frecuentes

| Síntoma | Mira esto primero |
|---|---|
| **El proceso no arranca y el error habla de prompts** | I-CP-1: un directorio de `WAPP_LLM_PROMPTS_DIR` inválido **aborta a propósito**. El error dice el fichero y la etapa. Valida con `go run ./cmd/prompts -comprobar <dir>` |
| **El proceso no arranca y el error habla del bucket** | `HeadBucket` falló: R2/MinIO caído o credenciales malas (`internal/publicapi/flows.go:75`) |
| **Un ajuste de prompt no tiene efecto** | ¿Reiniciaste? No hay recarga en caliente. Y mira la línea de log del arranque: dice `p4=/ruta/…` o `compilada`. En UAT `WAPP_LLM_PROMPTS_DIR` **está vacía**, así que corren los **compilados** |
| **Una ruta responde 404 y jurarías que existe** | Es **condicional**: su dependencia en `publicapi.Deps` es `nil` y no se montó (`roleplane.go:75`). Mira el cableado en `bootstrap.go` |
| **`POST /api/v1/members` responde 503** | Es lo diseñado: se monta siempre y degrada a 503 sin plano M2M. Nunca da 404 |
| **`/api/v1/signup` responde 503** | Falta `WAPP_IDENTITY_API_KEY`: se cablea un 503 fijo (`bootstrap/http.go:168`) |
| **`quote-suggestion` corta a los 10 s** | Alguien tocó el `WriteTimeout` global sin mirar el plazo propio de esa ruta (`publicapi.go:770`) |
| **La migración se queda colgada** | El advisory lock, repartido por un pooler en modo transacción. Apunta al **host directo** |
| **El segundo arranque muere en una migración** | `CREATE TABLE IF NOT EXISTS` no repuso una columna borrada y el `COMMENT ON COLUMN` de debajo falla |
| **Una métrica nueva no aparece en `/metrics`** | Un `CounterVec` no emite hasta su primer incremento, y el reinicio lo borra. No es un fallo |
| **Los jobs se quedan en `pending` para siempre, sin un error** | Alguna de las cinco goroutines de fondo murió. **El fallo es MUDO por diseño**: no hay supervisor. Ver `arquitectura.md` §5 |
| **Un `401` que tarda 5 s** | La latencia distingue «el usuario no existe» de «existe y la credencial no cuela»: el hash solo se paga si el usuario existe |

### 5.4 · Diagnóstico de un Edge concreto

`POST /api/v1/sessions/{id}/diagnostics` (permiso `diagnostics.request`) pide un *bundle* al
Edge, y `GET /api/v1/diagnostics/{command_id}` lo recoge. Tiene **TTL** (`WAPP_DIAGNOSTICS_BUNDLE_TTL`,
30 min) y exige consentimiento del tenant (`tenant_diagnostics_consent`).

### 5.5 · El estado del esquema, sin escribir nada

```bash
make migrate-status
psql "$DSN" -c 'select * from public.schema_version order by 1 desc limit 1'
```
Compara con `internal/platform/storage/postgres/migrations/version.go:435`. Hoy: **0.48.0** en
las dos partes.

---

## 6 · Estado del ambiente UAT (verificado el 2026-08-29)

Se anota porque contradice documentación vieja que sigue circulando:

- 🔴 **UAT ya NO usa Neon.** La base es un **PostgreSQL 17 en Docker** en el propio VPS
  (contenedor `wapp-postgres`, `postgres:17-alpine`), y el almacén S3 es un **MinIO** local
  (`minio-dev`), no R2. El bucket se llama `edugo-materials`.
- La unidad systemd es `wapp-cloud.service`; el binario, `bin/server` del checkout.
- Esquema en base **`0.48.0`**, sin deriva con el código.
- `WAPP_APP_ENV=dev` **en la máquina de UAT**, y `WAPP_LOG_LEVEL=debug`.
- **37 de las 70 variables no están puestas** y corren con su default. Las que más cambian una
  decisión: el bloque `WAPP_KEK_*` de KMS (⇒ **el KMS no está activado**) y
  **`WAPP_LLM_PROMPTS_DIR` vacía** (⇒ corren los prompts **compilados**; cualquier ajuste «por
  fichero» **no está activo en UAT**).
- 🔴 **Hallazgo de seguridad de campo, no de este código**: en esa máquina el cortafuegos está
  apagado y siete puertos responden desde Internet, entre ellos el `5432` de Postgres y el
  listener **admin `:8100`**. Los cuatro listeners bindean a `*` porque sus variables van sin
  host (`WAPP_HTTP_ADDR=:8100`). Está en [`deuda.md`](deuda.md).
