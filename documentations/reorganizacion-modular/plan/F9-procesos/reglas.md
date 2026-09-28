# F9 · Reglas — lo que no se toca, las trampas y la definición de hecho

## 1 · Lo que no se toca

| Qué | Por qué |
|---|---|
| El código de producción, viejo **y** nuevo (`internal/**`, `cmd/**`) | F9 es observación. Si un proceso necesita un cambio de producción para correr, **se para** y va a Jhoan (`05` §3.1; `plantilla-de-fase.md` §4.4) |
| El arranque viejo `internal/bootstrap/**` y el nuevo `internal/arranque/**` | D-F9-2: no hace falta ninguna opción nueva para S3. Una variable nueva cambiaría la huella de variables (`03` §1) |
| `internal/platform/storage/postgres/migrations/structure/*.sql` (84) | Full-replay por hash (`documentations/esquema-postgres.md`). La plantilla se migra con el binario `cmd/migrate`, no copiando SQL |
| `documentations/literal-aviso-sesion-pasiva.md` | 🔒 Contrato congelado: P3 lo **lee**, nunca lo copia ni lo edita |
| `docs/contracts/wapp-crm-v1/` | Contrato CRM congelado: P6 lo **lee** para validar |
| Los tests viejos de integración (132 ficheros con BD) y `make test-integration` | Protegen el código viejo, que es lo que corre en UAT, hasta F10 (`05` §7) |

## 2 · Prohibiciones (cada una con su candado o su comprobación)

| Prohibido | Lo caza |
|---|---|
| Un Postgres que no sea el contenedor de la corrida (`WAPP_TEST_DB_DSN`, `:5432`, `postgres://…` literal, UAT, un contenedor con nombre fijo) | `sin_bd_viva_test.go` (F0 + T9.3) |
| `testcontainers.WithReuseByName` | Ídem. Es experimental y es el contenedor vivo con otro nombre (`05` §7.2) |
| `os.Environ()` en el entorno del servidor | Ídem (T9.3). Ver trampa T-1 |
| `t.Skip`, `t.SkipNow`, `testing.Short()` | Ídem (T9.3) y `grep -c -- '--- SKIP'` = 0 (E-5, DT-52) |
| Importar un paquete de dominio (viejo o nuevo) fuera de las suites de contrato | `go list -tags integracion -deps ./test/procesos` (R9.4.d) |
| Ramas por binario dentro de un proceso (`if binario == "nuevo"`) | `grep -n WAPP_PROCESOS_BINARIO test/procesos/*_test.go` solo en `main_test.go` (R9.8.b). Un proceso que necesita distinguir es un **hallazgo**, no un `if` |
| Vía LLM `api` en cualquier proceso | El cliente del arnés rechaza el `PUT` (R9.3.e). Cero gasto: la vía `api` llamaría al proveedor real (`llmvia.go:275` sin `BaseURL`) |
| `time.Sleep` fijo como espera | Revisión: se espera **sondeando** Postgres o un canal con tope (trampa T-6) |
| Dos servidores contra la misma base, o viejo y nuevo a la vez sobre los mismos puertos | Bases `proc_<p>_<binario>` y puertos libres por servidor (`04` §2.2) |
| Un contenedor por proceso o por fichero | Un solo `TestMain`, un solo paquete (`05` §7.2) |
| Dar por cerrado un proceso que la web solo compiló o corrió como pre-chequeo | Traspaso con `CERRADO` de la sesión local (R9.7.c) |

## 3 · Trampas conocidas (medidas en el código, con `fichero:línea`)

| # | Trampa | Dónde | Qué hacer |
|---|---|---|---|
| T-1 | El proceso **no lee `.env`** pero hereda el entorno: quien trabaja con `set -a; . ./.env` tiene `WAPP_DB_*` de su base en el shell | `documentations/operacion.md` §2.2; `config.go:730-760` (overlay de entorno) | Entorno del subproceso construido desde cero (`diseno.md` §2) |
| T-2 | `WAPP_CONFIG_FILE` apunta a un YAML que **pisa** defaults | `config.go:733-739` | No se pone nunca |
| T-3 | `HeadBucket` incondicional: sin S3 el arranque muere en la fase `almacenes` | `internal/bootstrap/arranque/flows.go:75` → `internal/platform/storage/objectstore/r2_factory.go:54,63` | Doble en `127.0.0.1` (D-F9-2). ⚠️ `operacion.md` cita `internal/publicapi/flows.go:75`: está mal |
| T-4 | `UsePathStyle=false` fijo: con endpoint **nombre** (`localhost`) el SDK pediría `procesos.localhost` | `r2_factory.go:51`; `aws-sdk-go-v2/service/s3@v1.98.0/endpoints.go:5573,6185` | Endpoint siempre **IP** `127.0.0.1` |
| T-5 | El JWKS se pide **al arrancar** y es fail-closed: si el doble no está arriba, el servidor no arranca | `internal/bootstrap/arranque/auth.go:148-151`, `:277-294` | El doble de identidad se levanta **antes** que el servidor |
| T-6 | Barrido de ventanas cada **5 s**, worker del pipeline cada **5 s**, colector cada **15 s** (constantes, no variables) | `internal/flujos/runtime/aggregator.go:186`, `internal/intake/pipeline/backoff.go:105`, `internal/platform/metrics/flowlifecycle/collector.go:80` | Ventana a 0 por fixture; esperar sondeando con tope 60 s |
| T-7 | Sin `INFERENCE_READINESS_READY` en el latido, el servidor **no manda** inferencia al Edge | `internal/gateway/grpc/readiness.go:132-142`, `inference.go:439-450` | El Edge de prueba late READY |
| T-8 | `InferenceRequest.class` es telemetría (`interactivo`/`lote`), **no** la etapa | `internal/gateway/grpc/inference.go:281-283`; `internal/llmvia/local/local.go:100` | El guion reconoce la etapa por el prompt o por `max_output_tokens` (`diseno.md` §3.4) |
| T-9 | La salida de inferencia **va sellada**; sin sello → `ErrInferenceSinSalida` | `inference.go:553-590` | `SealFor(cloud_enc_pubkey)` sobre `InferenceOutput` |
| T-10 | Un `CounterVec` no aparece en `/metrics` hasta su primer incremento | `contratos.md` §8 | P0 no exige esos nombres; P3/P4/P6 los exigen **después** de provocar el evento |
| T-11 | Rutas **condicionales**: si su dependencia es `nil`, 404 | `internal/publicapi/roleplane.go:75` | Un 404 inesperado en un proceso se contrasta primero con el entorno (§2 de `diseno.md`), después se declara hallazgo |
| T-12 | `quote-suggestion` tiene plazo propio mayor que el `WriteTimeout` global de 10 s | `internal/bootstrap/arranque/http.go:23`; `contratos.md` §2.5 | P5 lo prueba con un retardo de 12 s del guion |
| T-13 | `CREATE DATABASE … TEMPLATE` falla si la plantilla tiene una conexión abierta | Postgres | La plantilla la migra un **subproceso** (`cmd/migrate`), que sale y cierra; el arnés no conecta a `plantilla` jamás |
| T-14 | Un `rc` leído detrás de un pipe es el del pipe | Regla 3 del ecosistema | `make test-procesos` escribe `RC=` en el log antes de nada (`diseno.md` §7) |
| T-15 | `go test` compila sin la etiqueta: sin `-tags integracion` el paquete solo tiene el candado y da `ok` | — | `make test-procesos` siempre con `-tags integracion`; contar `--- PASS` > 1 |
| T-16 | Los nombres de variable en el arnés van **completos** (`WAPP_DB_HOST`): el servidor compone `WAPP_` + clave en el loader | `config.go:24` | Nunca `DB_HOST` a secas |

## 4 · Definición de hecho

**De un proceso** (commit `procesos(<proceso>): …`):

1. Compila: `GOWORK=off go vet -tags integracion ./test/procesos/...; echo rc=$?` → 0 (web).
2. **Contra el viejo**, en local: `BINARIO=viejo make test-procesos` → `RC=0`, sus `--- PASS`
   contados, 0 SKIP, 0 FAIL.
3. **Contra el nuevo**, en local: `BINARIO=nuevo make test-procesos` → `RC=0`; si falla solo contra el
   nuevo, hallazgo en el traspaso (§7), **no** se ajusta el test.
4. Los candados de invariante que le tocan (`diseno.md` §4) son aserciones con nombre.
5. Sus tests viejos (E-8) están leídos y el commit dice qué regla **no** se llevó, con motivo.

**De una pasada 9C** (dentro del ciclo `conmutar(<m>)`): las suites de contrato del módulo verdes
contra Postgres **y** la suite entera verde contra el nuevo; si falla, el `conmutar(<m>)` no cierra.

**De la fase** (9D): `CUENTA=3 make test-procesos` → los dos binarios `RC=0`, 0 SKIP, 0 FAIL; las 22
filas de `diseno.md` §5 con su `--- PASS`; `make ci-local` `GATE_RC=0`; traspaso `CERRADO`;
`ESTADO.md` y este `README.md` al día.
