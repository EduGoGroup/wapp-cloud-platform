---
name: procesos-testcontainers
description: Use when writing, running or fixing the process-level integration tests of wapp-cloud-platform (package test/procesos/) — black-box tests per business process against a real Postgres started with testcontainers-go, one shared container per run and one cloned database per process, run against the server binary (cmd/server). Also use whenever someone is about to point a test at a live Postgres. Triggers — "test de proceso", "test/procesos", "testcontainers", "integración por proceso", "make test-procesos", "suite de contrato contra Postgres".
---

# Tests de proceso con testcontainers

> **La norma vive en [`documentations/constitucion.md`](../../../documentations/constitucion.md) §5** (E-5: ni un
> `t.Skip`; E-6: la suite de contrato corre en memoria y contra Postgres) y en
> [`operacion.md`](../../../documentations/operacion.md). Esta skill es el cómo. Si chocan, manda la constitución.

`test/procesos/` es **la única prueba contra Postgres** que tiene el repo: los unitarios
(`make test`, dentro de `make ci-local`) corren sin base de datos. La integración va **por proceso
de negocio**, no por fichero de código.

## 🔒 La regla que motiva esta skill

**Nunca un Postgres vivo.** Ni una DSN en una variable de entorno, ni `localhost:5432`, ni un
contenedor con nombre fijo levantado a mano, ni UAT, ni `testcontainers.WithReuseByName`. La única
cadena de conexión válida es la que devuelve el contenedor de la corrida. El candado
`test/procesos/sin_bd_viva_test.go` (sin etiqueta: corre en `ci-local`) hace fallar el gate si
aparece alguna, y además si **cualquier fichero que no sea `test/procesos/base_test.go` abre una
conexión** (`sql.Open`, `pgx.Connect`, `pgxpool.New`…; lista blanca por ruta exacta): un proceso no
abre la suya, usa la que le da el arnés (`nuevaBase(t, …)` y `base.Abrir(t)`).

## Cómo se corre

```bash
make vet-integracion                 # compilan (sin Docker); va dentro de ci-local
CUENTA=3 PROCESOS_LOG_DIR=/tmp make test-procesos; echo "rc=$?"
#   → «procesos: RC=0 · PASS=<n> FAIL=0 SKIP=0 · /tmp/procesos.log»
tail -1 /tmp/procesos.log            # RC=<n>: el rc de `go test`, escrito en el log
```

- **Necesita Docker.** Sin él, `TestMain` **falla**; no se salta.
- **Un SKIP es rojo**: el target sale ≠ 0 si `go test` salió ≠ 0 **o** hubo algún `--- SKIP`.
- `CUENTA` es el `-count` (por defecto 1; para cerrar algo, 3). `PROCESOS_LOG_DIR` es dónde deja
  `procesos.log` (por defecto `/tmp`).
- 🔴 **`TestMain` exige `GOWORK=off` en el entorno**: sin la variable sale con código 2 antes de
  levantar nada. `make test-procesos` ya la pone; un `go test -tags integracion ./test/procesos/`
  directo la lleva delante, junto a `GOTOOLCHAIN=go1.26.5` (un `go` suelto es el del sistema). `go env -w` no vale.
- **Un proceso suelto**, para iterar:
  `GOTOOLCHAIN=go1.26.5 GOWORK=off go test -tags integracion -count=1 -v -run '^TestP3_' ./test/procesos/`.
- **Con Edge real** (el binario `wapp-edge-agent` de verdad, con el transporte de WhatsApp falso, en
  un contenedor): `make test-procesos-edge-real`. Necesita Docker y el repo del Edge
  (`WAPP_PROCESOS_EDGE_DIR`); corre solo los `TestEdgeReal_…`, que viven tras una segunda etiqueta,
  `edgereal`, y es rojo también con `PASS=0`.
- `make cobertura-combinada` corre los procesos con el binario instrumentado y suma su cobertura a la
  de los unitarios: es un informe, y también necesita Docker.

## La forma

Un solo paquete, `test/procesos/` (Go compila un binario de test por paquete, y dos paquetes serían
dos contenedores). Todo con `//go:build integracion` en la primera línea, **salvo** los candados
`sin_bd_viva_test.go` y `domain_imports_test.go` y `doc.go`, que van sin etiqueta para morder en
`ci-local`.

| Ficheros | Qué son |
|---|---|
| `main_test.go` | `TestMain`: un contenedor `postgres:17-alpine` (la versión mayor de UAT), la base `plantilla` migrada **una vez** con `cmd/migrate`, y `cmd/server` compilado una vez |
| `base_test.go` | la base clonada por proceso (`CREATE DATABASE proc_<proceso> TEMPLATE plantilla`): el **único** fichero que abre conexiones |
| `servidor_test.go`, `servidor_*_test.go` | el servidor por proceso, lanzado como subproceso: sus puertos libres, su log y su parada |
| `clientes_test.go`, `identidad_test.go`, `claves_test.go`, `pki_test.go`, `fixtures_test.go` | el cliente HTTP, la identidad falsa, las claves (lease, JWT, KEK local) y la PKI del gRPC, generadas por el arnés |
| `edge_falso_test.go`, `edge_falso_*_test.go` | el Edge de prueba, que habla el gRPC real |
| `s3falso_test.go`, `crmfalso_test.go`, `guion_test.go` | los dobles de lo de fuera: el S3 que el arranque sondea, el CRM del webhook y el guion del proveedor LLM (cero gasto) |
| `sweep_test.go` | el directorio de la corrida y el barrido de los huérfanos |
| `p<n>_<proceso>_test.go`, `p<n>_<proceso>_<tema>_test.go` | **los procesos**, de `p0_arranque` a `p10_plataforma` |
| `<paquete>_contrato_test.go` | las suites de contrato de los puertos con BD, contra Postgres |
| `*_selftest_*_test.go` | los tests propios del arnés (`TestArnes_…`) |

Ningún fichero pasa de 500 líneas: se parte por tema, y toda pieza se llama
`<fichero de origen>_<tema>_test.go`.

⚠️ `TestMain` barre al entrar los directorios `procesos-<cifras>` huérfanos del `TMPDIR` **real** (los de
más de una hora y con su marcador): quien toque las condiciones de `sweep_test.go` corre el paquete con
`TMPDIR` en un directorio de usar y tirar.

### Aislamiento: una base y un servidor por proceso

- Cada proceso pide al arnés **su base** con `nuevaBase(t, "<proceso>")`: un clon de la plantilla,
  en milisegundos y sin re-migrar. ⚠️ La plantilla **no acepta conexiones** desde que se migra (un
  `CREATE DATABASE … TEMPLATE` falla si la plantilla tiene una sesión abierta): nadie se conecta a
  ella, tampoco para mirar.
- Y **su servidor** con `arrancar(t, …)`: el binario compilado, con la base del proceso en las
  `WAPP_DB_*` y los cuatro listeners (`WAPP_HTTP_ADDR`, `WAPP_PUBLIC_HTTP_ADDR`,
  `WAPP_GRPC_CONNECT_ADDR`, `WAPP_GRPC_ENROLL_ADDR`) en **puertos libres** que elige el arnés. Nunca
  los puertos por defecto.
- Con base y servidor propios, los procesos van en **paralelo** (`t.Parallel()`, `-parallel 4`).

### Caja negra

El proceso entra por la puerta real (HTTP, gRPC con el Edge de prueba) y comprueba lo que devuelve
y lo que queda en Postgres. **No importa paquetes de dominio.** La única excepción son los
`<paquete>_contrato_test.go`: importan la **suite de contrato** del puerto (`<paquete>helpertest`) y
el constructor del adaptador Postgres que prueban, para correrla contra una base clonada. Lo
comprueba, fichero a fichero, el candado `candados.ProcessImports`
(`test/procesos/domain_imports_test.go`).

Si un proceso falla, lo primero es decidir cuál de los dos está mal, el test o el servidor, **leyendo
el contrato del código** (el comentario del símbolo exportado): no se ajusta el test a lo que el
servidor hace «para que pase».

## Antes de dar un proceso por bueno

`RC=0`, `FAIL=0` y `SKIP=0` leídos de la línea de resumen y de la última línea del log, con
`CUENTA=3`. Y la skill `validar-antes-de-cerrar` para el resto de gates.

## Antipatrones

- «Solo para probar rápido», apuntar a un Postgres que ya está corriendo. Es justo lo prohibido.
- Un contenedor por fichero de test o por proceso: la instancia es **una** por corrida.
- Procesos que dependen del orden o de datos que dejó otro: cada uno con su base.
- Abrir una conexión fuera de `base_test.go`, o conectarse a la plantilla.
- Un `t.Skip` porque falta Docker o una dependencia: falla, no se salta.
- Organizar por fichero de código. La integración va **por proceso**.

## Relacionadas

`validar-antes-de-cerrar` · `describir-pr`
