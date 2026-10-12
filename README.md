# wapp-cloud-platform

La **plataforma cloud de wApp** (piezas 03 y 05 del ecosistema): un **monolito modular en Go**
donde la nube *piensa*. Reúne el IAM/RBAC multi-empresa, la API pública `/api/v1`, el Motor de
Flujos conversacionales, el gateway gRPC que termina el túnel de cada Edge y el pipeline LLM que
convierte una conversación de WhatsApp en un presupuesto.

El **Edge** —el equipo del cliente— despacha WhatsApp y custodia sus llaves; **esta pieza arma el
payload, emite y revoca los leases y guarda el dato de negocio**. La nube nunca tiene la DEK ni
el almacén cifrado del Edge (zero-knowledge).

- Módulo Go: `github.com/EduGoGroup/wapp-cloud-platform`
- Go **1.26.5** (lo fijan `go.mod` y el `Makefile`)
- PostgreSQL con SQL crudo, sin ORM. Sin Redis ni broker. Sin frontend.
- Un único binario de servicio: `cmd/server`

> Este fichero es deliberadamente corto. **La documentación vive en
> [`documentations/`](documentations/README.md)**; si algo de aquí la contradice, manda ella.

## Un proceso, cuatro listeners

| Listener | Variable | Default | Qué expone |
|---|---|---|---|
| HTTP admin/health | `WAPP_HTTP_ADDR` | `:8100` | `/healthz`, `/metrics`, `/admin/*` |
| gRPC CloudLink | `WAPP_GRPC_CONNECT_ADDR` | `:8101` | `Connect` bidi, **mTLS estricto** |
| gRPC Enrollment | `WAPP_GRPC_ENROLL_ADDR` | `:8102` | `EnrollEdge`, TLS solo de servidor |
| HTTP API pública | `WAPP_PUBLIC_HTTP_ADDR` | `:8103` | `/api/v1/*` (Context Token ES256 + RBAC) |

Los defaults están en `internal/platform/config/config.go`. `/healthz` solo existe en `:8100`.
La lista de rutas, rpc y variables de entorno: [`documentations/contratos.md`](documentations/contratos.md).

## Cómo está organizado

```
cmd/                 server (el servicio) · migrate · prompts · casebank · cobertura-ficheros · debug_inferencia
internal/modulos/    los siete módulos: acceso · edge · conversacion · catalogo · captacion · inferencia · solicitudes
internal/nucleo/     lo compartido por los módulos (la identidad del contacto)
internal/platform/   soporte transversal: config, cifrado, HTTP base, métricas, Postgres, almacén de objetos
internal/apipublica/ la cara HTTP pública (el :8103)
internal/arranque/   el cableado del proceso, en nueve fases
internal/candados/   la lógica de los candados que vigilan las fronteras
test/procesos/       los procesos de negocio, caja negra contra cmd/server y testcontainers
```

Para saber **qué existe de verdad** se lee `internal/arranque/orquestador.go` (la lista `fases`).
El detalle por módulo y dónde se rompen las fronteras:
[`documentations/arquitectura.md`](documentations/arquitectura.md).

## Arranque local

```bash
make tools && make toolchain     # una vez por checkout: golangci-lint fijado y toolchain comprobada
./scripts/gen-dev-certs.sh       # PKI de desarrollo en certs/ (fuera de git)
cp .env.example .env             # rellénalo; el proceso NO lee .env: se exporta
set -a; . ./.env; set +a
make migrate                     # aplica el esquema y sale (lee WAPP_DB_*)
GOTOOLCHAIN=go1.26.5 go run ./cmd/server
curl -s localhost:8100/healthz
```

Hace falta un PostgreSQL accesible y un almacén S3-compatible vivo. Los requisitos, las llaves
de desarrollo y qué pasa si se deja vacía cada una:
[`documentations/operacion.md`](documentations/operacion.md) §2. El Postgres de desarrollo
compartido: [`deploy/README.md`](deploy/README.md).

## El gate

**Un PR aquí no valida nada**: `.github/workflows/ci.yml` es `workflow_dispatch`. El gate es
local.

| Target | Qué corre |
|---|---|
| `make ci-local` | `fmt-check` + `vet` + `vet-integracion` + `lint` + `test` + `cobertura-ficheros` + `build`. Sin Postgres ni Docker |
| `make test-procesos` | Los procesos de negocio contra `cmd/server` y un Postgres de testcontainers. **Necesita Docker.** Rojo si rc≠0 o si hay algún `--- SKIP` |
| `make test-procesos-edge-real` | Los procesos con el Edge real y WhatsApp falso. Necesita Docker y el repo del Edge |
| `make ci-docker` | `ci-local` dentro de la imagen `golang` fijada. No corre `test-procesos` |
| `make cobertura-combinada` | Informe de cobertura sumando unitarios y procesos. Necesita Docker |
| `make migrate` · `make migrate-status` | Aplica el esquema y sale · consulta la versión sin escribir |

`make ci-local` **no toca Postgres**: lo que necesita base de datos se prueba con
`make test-procesos`, aparte. Lee el código de salida **sin tubería** y cuenta los `--- SKIP`.
Qué valida cada target y cómo leerlo: [`documentations/operacion.md`](documentations/operacion.md) §3.

## Documentación

| | |
|---|---|
| [`documentations/README.md`](documentations/README.md) | El portal de la pieza y su índice |
| [`documentations/constitucion.md`](documentations/constitucion.md) | **Empieza aquí**: invariantes, homónimos, tecnología y trampas |
| [`documentations/arquitectura.md`](documentations/arquitectura.md) | Módulos, arranque, binarios, fronteras y candados |
| [`documentations/contratos.md`](documentations/contratos.md) | Rutas, rpc, CLI y variables de entorno |
| [`documentations/esquema-postgres.md`](documentations/esquema-postgres.md) | Tablas, versión del esquema y el runner de migraciones |
| [`documentations/operacion.md`](documentations/operacion.md) | Arranque local, pruebas, release y depuración |
| [`documentations/deuda.md`](documentations/deuda.md) | La deuda viva, con `fichero:línea` |
| [`CLAUDE.md`](CLAUDE.md) | Las reglas innegociables para quien toque código |

La documentación del ecosistema (ADR, constitución, planes) vive en la raíz de wApp, fuera de
este repo.
