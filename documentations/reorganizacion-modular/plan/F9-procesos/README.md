# F9 · Procesos — la integración de cero, por proceso, con testcontainers

> **Estado**: ⏳ sin empezar (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`).
> **Norma**: [`05`](../../05-metodo-contratos-y-tdd.md) §7 (y E-5, E-6, §3.2). **Cómo**: skill
> [`procesos-testcontainers`](../../../../.claude/skills/procesos-testcontainers/SKILL.md). Forma:
> [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md). Si esta fase choca con `05`, manda `05`.

## Objetivo en tres líneas

Un paquete `test/procesos/` que levanta **un** Postgres 17 con testcontainers por corrida, clona una
base plantilla por proceso y arranca **el binario de verdad** (viejo o nuevo) contra ella, con un
**Edge de prueba** por gRPC mTLS y dobles de S3, identity, LLM y CRM. Cada proceso de negocio entra por
la puerta real y mira Postgres. Es el oráculo de comportamiento entre `cmd/server` y
`cmd/server-modular`, y la **condición del relevo** (F10).

## 🔍 La propuesta de orden: adelantar F9 (decisión D-F9-1, abajo)

`05` §6 pone F9 **después** de F8. Esta spec recomienda **partirla en cuatro olas** y adelantar las dos
primeras, porque los procesos se escriben **contra el binario viejo**, que no depende de nada
reconstruido:

| Ola | Qué | Cuándo | Bloques |
|---|---|---|---|
| **9A · Arnés** | `TestMain`, dobles, Edge de prueba, candado, `make test-procesos`, proceso P0 (humo) | En cuanto cierre **F0** (en paralelo con F1: no comparten ficheros) | A |
| **9B · Procesos contra el viejo** | P1–P9 escritos y en verde contra `cmd/server`; corridos también contra el nuevo | Tras 9A, **antes de conmutar F2** | B1, B2 |
| **9C · Pasada por conmutación** | Al conmutar cada módulo: suites de contrato de sus adaptadores Postgres contra Postgres + toda la suite contra el nuevo | Dentro de cada ciclo `conmutar(<m>)` de F1–F8 | C (una por módulo) |
| **9D · Cierre** | Corrida final contra los dos binarios, sin intermitencias, 0 SKIP, D-13 cerrada | Tras F8, **antes de F10** | D |

Si Jhoan **no** acepta adelantar, las olas 9A, 9B y 9D se ejecutan en bloque tras F8 (el orden de
`05` §6) y las tareas de 9C se **anulan** y se sustituyen por T9.34. Las tareas que cambian llevan la
marca **🕐** en [`tareas.md`](tareas.md).

## Entradas (tiene que ser cierto para empezar)

| Para | Condición | Cómo se comprueba |
|---|---|---|
| 9A | F0 cerrada: existen `cmd/server-modular` y `internal/arranque`, y los dos binarios compilan | `GOWORK=off go build ./cmd/server ./cmd/server-modular; echo rc=$?` → `rc=0` |
| 9A | D-13 (lista de procesos) y D-F9-1..D-F9-4 decididas por Jhoan | Sección «Decisiones» de este README con fecha |
| 9A | El candado `test/procesos/sin_bd_viva_test.go` y `test/procesos/doc.go` existen (**F0**, T0.8) y, si Jhoan aceptó **D-F1-2**, el mínimo de `test/procesos/main_test.go` y `contact_contrato_test.go` (**F1**, T1.13) | `ls test/procesos/` |
| 9A | La sesión que cierra tiene Docker (la local siempre; la web solo si la prueba de T9.12 salió bien) | `docker info >/dev/null; echo rc=$?` → `rc=0` |
| 9B | 9A cerrada por la sesión local (P0 verde contra el viejo **y** el nuevo) | Traspaso `TRASPASO-F9-arnes.md` con sección `CERRADO` |
| 9C(m) | El módulo `m` está en verde y su commit `conmutar(<m>)` existe | `git log --oneline --grep='conmutar(<m>)'` |
| 9D | F8 conmutada; `fronteras_test` sin puentes declarados | La lista de puentes de `internal/modulos/fronteras_test.go` vacía |

## Salidas (es cierto al cerrar)

- `test/procesos/` con el arnés, el candado `sin_bd_viva_test.go` y **un fichero por proceso** de la
  lista cerrada D-13 (propuesta: P0–P9, [`diseno.md`](diseno.md) §4).
- `make test-procesos` corre **contra los dos binarios** y deja dos logs con su `RC`; `ci-local`
  incluye `go vet -tags integracion ./test/procesos/...`.
- Cada proceso **pasa contra el viejo y contra el nuevo**, 3 veces seguidas (`-count=3`), con
  **0 `--- SKIP`** y **0 `--- FAIL`**.
- Las **suites de contrato** (E-6) de los **22** paquetes con SQL (medidos; `05` dice 20) corren contra
  Postgres desde `test/procesos/<paquete>_contrato_test.go` (la convención que estrena F1, T1.13).
- Los candados de invariante que necesitan BD (`05` §3.2) son aserciones de su proceso.
- Ni un `WAPP_TEST_DB_DSN`, ni un puerto fijo, ni `WithReuseByName` en `test/procesos/` (el candado lo
  prueba en `ci-local`).
- **Ni una línea de código de producción cambiada** por F9 (salvo `go.mod`, `go.sum`, `Makefile`,
  `.golangci.yml`).

## Orden de lectura

1. [`requisitos.md`](requisitos.md) — historias y criterios EARS (H9.1–H9.8).
2. [`arquitectura.md`](arquitectura.md) — la topología del arnés, las olas frente a F1–F8, lo que no cambia.
3. [`diseno.md`](diseno.md) — fichero a fichero de `test/procesos/`, el entorno del servidor, los
   dobles, y **los diez procesos** con su recorrido, tablas, candados, suites y tests viejos.
4. [`reglas.md`](reglas.md) — las trampas medidas y la definición de hecho.
5. [`tareas.md`](tareas.md) — T9.1–T9.34 en bloques de sesión.

## Bloques de sesión

| Bloque | Entorno | Tareas | Para cuando |
|---|---|---|---|
| **A · arnés** | 🌐→💻 | T9.1–T9.12 | P0 verde contra los dos binarios en local; `ci-local` rc=0 con el candado |
| **B1 · procesos de plataforma y acceso** | 🌐→💻 | T9.13–T9.16 (P1, P2, P3, P9) | Los cuatro verdes contra el viejo y el nuevo, en local |
| **B2 · procesos de negocio** | 🌐→💻 | T9.17–T9.21 (P4–P8, doble CRM) y T9.35 (P10, solo si D-F9-4) | Los cinco (o seis) verdes contra el viejo y el nuevo, en local |
| **C · pasada por conmutación** 🕐 | 🌐→💻 | T9.22–T9.29, **una por módulo**, dentro de la sesión que conmuta | Suites del módulo contra Postgres + suite entera contra el nuevo, en local |
| **D · cierre** | 💻 | T9.30–T9.33 | `-count=3` limpio contra los dos; traspaso `CERRADO`; docs al día |

La numeración global de sesiones (`S0n`) vive en [`../sesiones/`](../sesiones/README.md).

## Decisiones que necesita (Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-F9-1** | ¿Se adelanta F9 (olas 9A/9B tras F0, 9C en cada conmutación) o se hace entera tras F8 (`05` §6)? | **Adelantar.** Motivo principal: E-6 deja el **SQL de cada adaptador Postgres nuevo sin probar contra Postgres** hasta F9; con el orden de `05`, F2–F8 conmutarían siete módulos con SQL no ejecutado nunca, y el primer aviso llegaría meses después. Con 9C, cada `conmutar(<m>)` tiene detrás una prueba de comportamiento, no solo la huella (que compara **nombres**, no conducta). Coste: ~8 pasadas cortas 💻 más. Análisis completo en [`arquitectura.md`](arquitectura.md) §5 |
| **D-13** | Lista cerrada de procesos | **P0–P9** de [`diseno.md`](diseno.md) §4: los 8 de `05` §7.4 + **P0 humo del arranque** (valida el arnés) + **P9 diagnóstico remoto y config empujada** (el único tramo del gRPC que ningún otro proceso toca). Opcional **P10 plataforma** si se acepta D-F9-4 |
| **D-F9-2** | 🔴 R2: ¿hace falta una opción nueva en el arranque para el doble de S3? (la pendiente de `ESTADO.md`) | **No.** El SDK usa *path-style* cuando el endpoint es una **IP**, aunque `UsePathStyle=false` (leído en `aws-sdk-go-v2/service/s3@v1.98.0/endpoints.go:5573` rama `ForcePathStyle == false`, y `:6185`, `:6298`, `:6496`: `if _url.IsIp == true` → `scheme://authority/<bucket>`). Con `WAPP_STORAGE_S3_ENDPOINT=http://127.0.0.1:<puerto>`, el `HeadBucket` de `r2_factory.go:63` llega como `HEAD /<bucket>` a un doble en proceso. **Leído en el código, no ejecutado**: lo confirma T9.11. Si T9.11 lo refuta, vuelve a Jhoan con la salida (la alternativa sería una variable nueva, que cambia la huella de las variables: `03` §1) |
| **D-F9-3** | Doble de S3: ¿servidor falso en el proceso de test o MinIO por testcontainers? | **En el proceso** (`s3falso_test.go`, `net/http` puro): el servidor solo hace `HeadBucket` al arrancar y **firma** URLs sin red (`presign.go`). MinIO (lo que corre UAT, `documentations/operacion.md` §6) sería un segundo contenedor por corrida sin ganar cobertura. Sin dependencia nueva (`gofakes3` no hace falta) |
| **D-F9-4** | 🔴 Los **9 ficheros / 25 `Test*`** de integración de `internal/platform/` **sobreviven** al relevo (platform no se borra) y seguirían leyendo `WAPP_TEST_DB_DSN` con `t.Skip` (DT-52) | **Re-expresarlos como P10 · plataforma** (réplica de migraciones sobre un clon, grants, rekey por `/admin/crypto/rekey`, el colector de `/metrics`) y borrarlos en F10 junto con `make test-integration`. Sin esto, F10 no puede dejar el repo sin `WAPP_TEST_DB_DSN`. Toca tests viejos de `platform`: por eso es decisión |
| **D-F9-5** (opcional) | ¿Medir la cobertura que los procesos dan al código nuevo (`go build -cover` + `GOCOVERDIR`)? | **Sí, informativa, sin umbral**: diría cuánto SQL de los adaptadores excluidos de E-9 ejecutan los procesos. No bloquea nada |

### Decisiones tomadas (T9.1)

> **2026-09-30**: Jhoan acepta **en bloque** la recomendación de todas las filas de
> [`../DECISIONES.md`](../DECISIONES.md) §4 «Antes de F9» (la fuente; aquí se copian con su fecha para
> que quien abra esta fase no tenga que salir de ella). Ninguna se tomó distinta de la recomendación,
> así que **no se reordena nada**.

| # | Decisión | Fecha | Efecto en las tareas |
|---|---|---|---|
| **D-F9-1** | **Sí**: se adelanta F9 (9A tras F0, 9B tras la parada de F1, 9C en cada `conmutar`, 9D antes de F10) | 2026-09-30 | Las tareas 🕐 se quedan como están; **T9.34 no se ejecuta** (queda solo como alternativa histórica); T9.22–T9.29 son las pasadas 9C |
| **D-13** | **Sí**: lista cerrada **P0–P9** de [`diseno.md`](diseno.md) §4 | 2026-09-30 | Un fichero por proceso, T9.11 y T9.13–T9.21 |
| **D-F9-2** | **Sí**: sin opción R2 en el arranque; con endpoint IP el SDK de S3 hace *path-style* solo | 2026-09-30 | Ninguna variable nueva. 🔴 La confirma **T9.11** ejecutándola; si el SDK pide *virtual-hosted*, se para y vuelve a Jhoan |
| **D-F9-3** | **Sí**: S3 falso dentro del proceso de test (`s3falso_test.go`, `net/http` puro) | 2026-09-30 | Sin MinIO ni `gofakes3` |
| **D-F9-4** | **Sí**: P10 · plataforma para los 9 ficheros / 25 `Test*` de `internal/platform/` con BD | 2026-09-30 | T9.35 **activa** (bloque B2) |
| **D-F9-5** | **Sí** (opcional, informativa, sin umbral): medir la cobertura que dan los procesos al código nuevo | 2026-09-30 | No bloquea nada; se decide cuándo en el bloque D |
| **T-2** | **Sí**: se aceptan las subidas de `httpsnoop` 1.0.4→1.1.0, `otelhttp` 0.67→0.69 y `klauspost/compress` que trae testcontainers v0.44.0 | 2026-09-30 | T9.2 va en un commit `chore(deps)` **aislado** (ver contradicción 10) |

## Encaje con F0 y F1 (escritas antes que esta spec)

- **F0** crea `test/procesos/doc.go` y el candado `sin_bd_viva_test.go` (T0.8; patrones en su
  `diseno.md` §4.4) y hace la **sonda de testcontainers en la web** fuera del árbol (T0.0, veredicto
  en `06-entorno-web.md` §5). F9 **amplía** el candado (T9.3) y **usa** ese veredicto (T9.12).
- **F1**, si Jhoan acepta **D-F1-2**, adelanta el mínimo del arnés (`main_test.go`: contenedor +
  plantilla + base clonada) para correr `contacttest.Contrato` contra `PostgresResolver`
  (`contact_contrato_test.go`, T1.13/T1.18). Entonces T9.5 **amplía** ese `main_test.go` (binarios,
  servidor por proceso) en vez de crearlo, y la pasada 9C de `nucleo` (T9.22) solo añade la corrida de
  la suite entera contra el nuevo. D-F1-2 y D-F9-1 empujan en la misma dirección: si se acepta una,
  conviene aceptar la otra.

## Contradicciones encontradas (con `04`/`05`/`ESTADO`/docs, medidas el 2026-09-28)

1. **R2 (ESTADO, skill `procesos-testcontainers`)**: «puede exigir una opción solo en el arranque
   nuevo». El código del SDK dice que no (D-F9-2). Pendiente de confirmar en T9.11.
2. **`documentations/operacion.md` §2.4 y §5.3** sitúan el `HeadBucket` en
   `internal/publicapi/flows.go:75`. Está en `internal/bootstrap/arranque/flows.go:75`
   (`objectstore.NewR2PresignClient`) → `internal/platform/storage/objectstore/r2_factory.go:54`.
3. **`05` E-6: «20 paquetes con adaptador Postgres, 8 con gemelo en memoria»**. Medidos **22** con SQL
   (`grep -rlE '"database/sql"|pgx' --include='*.go' internal | grep -v _test`, más `platformadmin`,
   que usa `*sql.DB` por `access_requests.go` y `postgres.go`): además de los 20, `flujos/events` y
   `flujos/runtime` (`tenant_resolver.go`). Y de los 12 «sin gemelo», **6 sí tienen** implementación en
   memoria con otro nombre de fichero: `gateway/enroll` (`store.go:55`, `edgecert.go:44`),
   `gateway/fleet` (`fleet.go:407`), `gateway/lease` (`repository.go:70`), `ingest` (`dedupe.go:31`),
   `intentcfg` (`store.go:53`), `diagnostics` (`diagnostics.go:115`)
   (`grep -rn '^func NewMemory' --include='*.go' internal | grep -v _test`). Recuento único (22 con
   SQL, 15 con gemelo, 7 sin él —`entitlements` sí lo tiene: `Fake`—) en
   [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) §9.
4. **`05` §1 y §7: «107 ficheros de integración»**. Medido: **97** `*_integration_test.go`
   (`find internal cmd -name '*_integration_test.go' | wc -l`) y **132** ficheros que dependen de BD
   (unión con `grep -rlE 'WAPP_TEST_DB_DSN|openTestDB|testDB\(' --include='*_test.go' internal cmd`).
   Los **135** usos de `WAPP_TEST_DB_DSN` sí cuadran (`grep -rn 'WAPP_TEST_DB_DSN' --include='*.go' . | wc -l`).
5. **`05` §7: «siguen protegiendo el código viejo hasta el relevo, y se borran con él»**. No todos:
   9 viven en `internal/platform/`, que no se borra (D-F9-4).
6. **Skill: «el proveedor LLM falso, como en el e2e del Plan 044»**. La vía `api` **no se puede
   apuntar a un doble**: `internal/llmvia/llmvia.go:275` construye `api.Config` sin `BaseURL` (el
   campo existe en `wapp-shared/llm@v0.4.5/api/api.go:84`). El único LLM falso posible sin tocar
   código es **el Edge de prueba respondiendo `InferenceRequest`** (vía `local`, la de todo tenant
   sin fila en `tenant_llm`: `llmvia.go:201-208`). Un proceso con vía `api` llamaría al proveedor
   real: prohibido (cero gasto).
7. **Skill: «KEK del cifrado de PII, con el proveedor local»**. El proveedor se llama **`env`**
   (`WAPP_KEK_PROVIDER`, default en `internal/platform/config/config.go:654`).
8. **Variables de entorno: `03`/`contratos.md` dicen 70.** Medido **71**: 69 por `loader.Get*`
   (`awk '/^func Load\(\)/,/^}/' internal/platform/config/config.go | grep -oE 'loader\.Get[A-Za-z]+\("[A-Z_0-9]+"' | sort -u | wc -l`)
   + `FLOW_REPLY_RATE` por `getFloat` (`config.go`, fuera del patrón) + `WAPP_CONFIG_FILE` por
   `os.Getenv` (`config.go:735`). Afecta a la huella de variables de F0, no a F9.
9. **`05` §7.2**: «`make test-integration` … puerto 5432». Es el **default**, sobrescribible
   (`INTEGRATION_PG_PORT ?= 5432`, `Makefile:21`). El vicio de fondo (contenedor vivo, nombre fijo
   `wapp-cloud-platform-pg-test`) sí es cierto.
