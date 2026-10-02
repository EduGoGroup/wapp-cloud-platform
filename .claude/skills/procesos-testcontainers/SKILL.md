---
name: procesos-testcontainers
description: Use when writing, running or fixing the process-level integration tests of wapp-cloud-platform (phase F9 of the modular reconstruction, package test/procesos/) — black-box tests per business process against a real Postgres started with testcontainers-go, one shared container per run and one cloned database per process, run against both the old and the new server binary. Also use whenever someone is about to point a test at a live Postgres. Triggers — "test de proceso", "F9", "test/procesos", "testcontainers", "integración por proceso", "make test-procesos", "WAPP_TEST_DB_DSN".
---

# Tests de proceso con testcontainers

> **La norma vive en [`documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md`](../../../documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md) §7.**
> Esta skill es el cómo. Si choca con `05`, manda `05`.

Los 107 ficheros de integración viejos **no se portan**. La suite nueva se escribe **de cero,
por proceso de negocio**, y es **condición del relevo** (F10): sin ella, el código nuevo no tendría
ni una prueba contra Postgres.

## 🔒 La regla que motiva esta skill

**Nunca un Postgres vivo.** Ni `WAPP_TEST_DB_DSN`, ni `localhost:5432`, ni un contenedor con nombre
fijo levantado a mano, ni UAT, ni `testcontainers.WithReuseByName` (experimental, y es el mismo
vicio con otro nombre). La única cadena de conexión válida es la que devuelve el contenedor de la
corrida. El candado `test/procesos/sin_bd_viva_test.go` hace fallar el gate si aparece alguna, y
además si **cualquier fichero que no sea `test/procesos/base_test.go` abre una conexión**
(`sql.Open`, `pgx.Connect`, `pgxpool.New`…; lista blanca por ruta exacta, D-F9-6, 2026-10-02): un
proceso no abre la suya, usa la que le da el arnés (`nuevaBase(t, …)` y `base.Abrir(t)`).

Los tests **viejos** sí usan `WAPP_TEST_DB_DSN` (135 usos): siguen así hasta el relevo, porque
protegen el código viejo. No se copian.

## Quién lo corre

- **Sesión web**: escribe el proceso y comprueba que compila,
  `GOWORK=off go vet -tags integracion ./test/procesos/...`. **No lo da por bueno**: no lo corrió.
- **Sesión local** (tiene Docker): `make test-procesos`, contra los dos binarios. **Es quien cierra
  F9.** Traspaso con la skill `traspaso-web-local`.
- Sin Docker, el `TestMain` **falla**. No se salta.
- 🔴 **`TestMain` exige `GOWORK=off`** (D-F9-7): sin la variable en el entorno sale con código 2 antes de
  levantar nada (`go test` lo devuelve como rc=1). `make test-procesos` ya la pone; un
  `go test -tags integracion ./test/procesos/` directo la lleva delante, también donde no hay
  `go.work`, junto a `WAPP_PROCESOS_BINARIO=viejo|nuevo`. `go env -w` no vale.
- 🔴 **En local, un `go` suelto es el del sistema** (`go1.27.1`), no el fijado: usa
  `make vet-integracion` y `make test-procesos`, o antepón `GOTOOLCHAIN=go1.26.5`. Compruébalo
  con `make toolchain` (`TOOLCHAIN=OK`). En la web da igual.

## La forma

```
test/procesos/
├── main_test.go             TestMain: un contenedor, la plantilla migrada, los binarios compilados
├── sweep_test.go            el directorio de la corrida y el barrido de los huérfanos (D-F9-8)
├── arnes_test.go            base clonada por proceso + servidor por proceso + clientes HTTP/gRPC
├── doc.go                   el comentario del paquete (sin etiqueta)
├── sin_bd_viva_test.go      el candado (sin etiqueta: corre en ci-local)
├── enrolamiento_lease_test.go
├── canje_permisos_test.go
├── mensaje_a_borrador_test.go
└── …                        un fichero por proceso (05 §7.4)
```

Todo con `//go:build integracion` en la primera línea, **salvo el candado `sin_bd_viva_test.go`**,
que va sin etiqueta para morder en `ci-local` (F0 T0.8, `reglas.md` §3), y `doc.go`. **Un solo
paquete**: Go compila un binario de test por paquete, y dos paquetes serían dos contenedores.

⚠️ `TestMain` barre al entrar los directorios `procesos-<cifras>` huérfanos del `TMPDIR` **real** (los de
más de una hora y con su marcador): quien toque las condiciones de `sweep_test.go` corre el paquete con
`TMPDIR` en un directorio de usar y tirar.

### `TestMain`: una instancia por corrida

API comprobada contra la documentación de testcontainers-go (módulo `postgres`):

```go
//go:build integracion

package procesos

import (
	"context"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // driver nativo: sin él, snapshot/restore cae a docker exec
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var instancia *postgres.PostgresContainer

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:17-alpine", // la versión mayor de UAT
		postgres.WithDatabase("plantilla"),
		postgres.WithUsername("wapp"),
		postgres.WithPassword("wapp"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "procesos: no se pudo levantar Postgres (¿hay Docker?): %v\n", err)
		os.Exit(1) // FALLA; no se salta
	}
	instancia = ctr
	// 1 · migrar "plantilla" UNA vez (las 84 migraciones) y cerrar esa conexión
	// 2 · compilar cmd/server y cmd/server-modular una vez, a un directorio temporal
	code := m.Run()
	_ = ctr.Terminate(ctx) // y el reaper de testcontainers, si el proceso muere antes
	os.Exit(code)
}
```

### Aislamiento: una base y un servidor por proceso

- Cada proceso pide al arnés **su base**: conectado a la base de mantenimiento `postgres`,
  `CREATE DATABASE proc_<nombre> TEMPLATE plantilla`. Son milisegundos y no re-migra. ⚠️ La
  plantilla no puede tener conexiones abiertas en ese momento: por eso se cierra la de la migración.
- Y **su servidor**: el binario elegido, lanzado como subproceso con `WAPP_DB_HOST`, `WAPP_DB_PORT`,
  `WAPP_DB_USER`, `WAPP_DB_PASSWORD`, `WAPP_DB_NAME=proc_<nombre>` y `WAPP_DB_SSLMODE=disable`
  (del `ctr.Host`/`ctr.MappedPort`), y los cuatro listeners (`WAPP_HTTP_ADDR`,
  `WAPP_PUBLIC_HTTP_ADDR`, `WAPP_GRPC_CONNECT_ADDR`, `WAPP_GRPC_ENROLL_ADDR`) en **puertos libres**
  que elige el arnés. Nunca los puertos por defecto.
- Con base y servidor propios, los procesos pueden ir en **paralelo** (`t.Parallel()`).
- Alternativa solo si se corre en serie: `postgres.WithSnapshot()` + `ctr.Restore(ctx)` entre
  procesos. Exige cerrar antes el servidor, porque `Restore` borra y recrea la base.

### Caja negra, contra los dos binarios

El proceso entra por la puerta real (HTTP, gRPC con un Edge de prueba) y comprueba lo que devuelve
y lo que queda en Postgres. **No importa paquetes de dominio**; solo puede importar las **suites
de contrato** (`<paquete>helpertest`, D-F1-10) y, en su `<paquete>_contrato_test.go`, el constructor del
adaptador Postgres que prueban con sus argumentos (D-F1-8), para correrlas contra Postgres (`05` E-6).
Lo comprueba el comando de R9.4.d (`plan/F9-procesos/requisitos.md`), que mira imports directos.

Qué binario se prueba lo elige el arnés con `WAPP_PROCESOS_BINARIO=viejo|nuevo`. Orden
obligatorio para cada proceso nuevo:

1. **Contra el viejo** (`cmd/server`, lo que corre en UAT). Si falla, **el test está mal**, no el
   servidor: corrígelo hasta que pase.
2. **Contra el nuevo** (`cmd/server-modular`). Si falla, el código nuevo se aparta del viejo: eso
   es un hallazgo de la reconstrucción.

## ⚠️ El servidor necesita más que la BD: el arnés es el primer entregable

`cmd/server` completo no arranca solo con Postgres. Antes del primer proceso, el arnés tiene que
resolver, **sin tocar el código viejo**:

- **PKI** del gRPC (CA y certificados): `scripts/gen-dev-certs.sh` es el precedente.
- **Clave del lease** y **claves JWT** de prueba, generadas por el arnés.
- **KEK del cifrado de PII**, con el proveedor local.
- **R2 / S3**: `internal/bootstrap/arranque/flows.go:75` construye **siempre** el cliente de
  presign y hace `HeadBucket` con estilo *virtual-hosted* (`r2_factory.go`); si falla, el arranque
  aborta. Hace falta un doble compatible con S3 que responda a eso (otro contenedor del arnés, o un
  servidor HTTP falso). ⚠️ No está resuelto: puede exigir una opción de configuración **solo en el
  arranque nuevo**. Si es así, la decisión es de Jhoan, y el viejo no se toca.
- **El proveedor LLM**: falso, como en el e2e del Plan 044 (política de cero gasto).

Precedente parcial: `cmd/server/integration_test.go` (`TestInProcessEnrollConnectLeaseSendRecv`)
compone piezas **dentro del proceso**, no el binario entero. Sirve de referencia para la PKI y el
lease, no como arnés de caja negra.

## Antes de dar un proceso por bueno

```bash
make test-procesos > /tmp/procesos.log 2>&1; echo "RC=$?" >> /tmp/procesos.log; tail -1 /tmp/procesos.log
grep -c -- '--- PASS' /tmp/procesos.log; grep -c -- '--- FAIL' /tmp/procesos.log
```

`make test-procesos` lo crea F9: corre con `-tags integracion -v`, contra los dos binarios. Y la
skill `validar-antes-de-cerrar` para el resto de gates.

## Antipatrones

- «Solo para probar rápido», apuntar a un Postgres que ya está corriendo. Es justo lo prohibido.
- Un contenedor por fichero de test o por proceso: la instancia es **una** por corrida.
- Procesos que dependen del orden o de datos que dejó otro: cada uno con su base.
- Escribir el proceso solo contra el binario nuevo: se pierde el oráculo.
- Organizar por fichero de código. La integración va **por proceso**.

## Relacionadas

`validar-antes-de-cerrar` · `traspaso-web-local` · `contrato-tdd` (las suites de contrato que se
corren aquí contra Postgres)
